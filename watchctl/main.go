// watchctl — sign EarlyWatch approvals for Gatekeeper EWApprovalCheck.
//
//	watchctl approve-delete \
//	  --key=privkey.pem \
//	  --group="" --version=v1 --resource=services \
//	  --namespace=default --name=web
//
//	watchctl approve-update \
//	  --key=privkey.pem \
//	  --old=old.json --new=new.json
//
// Outputs the base64 RSA-PSS signature on stdout. Pipe into the appropriate
// annotation:
//
//	kubectl annotate svc/web "earlywatch.io/approved=$(watchctl approve-delete ...)"
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
)

const defaultChangeApprovalAnnotation = "earlywatch.io/change-approved"

func loadKey(path string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA private key")
	}
	return rsaKey, nil
}

func signMsg(k *rsa.PrivateKey, msg string) string {
	sum := sha256.Sum256([]byte(msg))
	sig, _ := rsa.SignPSS(rand.Reader, k, crypto.SHA256, sum[:], nil)
	return base64.StdEncoding.EncodeToString(sig)
}

var serverManagedMetadataFields = []string{
	"resourceVersion",
	"generation",
	"uid",
	"creationTimestamp",
	"managedFields",
	"selfLink",
}

var topLevelServerManagedFields = []string{
	"status",
}

func normalizedMergePatch(oldJSON, newJSON string, stripAnnotations []string) (string, error) {
	o, err := normalizeForPatch([]byte(oldJSON), stripAnnotations)
	if err != nil {
		return "", fmt.Errorf("normalizing old object: %w", err)
	}
	n, err := normalizeForPatch([]byte(newJSON), stripAnnotations)
	if err != nil {
		return "", fmt.Errorf("normalizing new object: %w", err)
	}
	return canonical(diff(o, n)), nil
}

func normalizeForPatch(raw []byte, stripAnnotations []string) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("unmarshalling object: %w", err)
	}

	for _, field := range topLevelServerManagedFields {
		delete(obj, field)
	}

	metadata, _ := obj["metadata"].(map[string]any)
	if metadata != nil {
		for _, field := range serverManagedMetadataFields {
			delete(metadata, field)
		}

		if annotationsRaw, ok := metadata["annotations"]; ok {
			if annotationsRaw == nil {
				delete(metadata, "annotations")
			} else if annotations, ok := annotationsRaw.(map[string]any); ok {
				for _, key := range stripAnnotations {
					delete(annotations, key)
				}
				if len(annotations) == 0 {
					delete(metadata, "annotations")
				}
			}
		}

		obj["metadata"] = metadata
	}

	return obj, nil
}

// Mirrors provider's canonicalJSON / diff.
func canonical(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kj, _ := json.Marshal(k)
			b.Write(kj)
			b.WriteByte(':')
			b.WriteString(canonical(t[k]))
		}
		b.WriteByte('}')
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, el := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(canonical(el))
		}
		b.WriteByte(']')
		return b.String()
	default:
		out, _ := json.Marshal(t)
		return string(out)
	}
}

func diff(o, n any) any {
	om, ok1 := o.(map[string]any)
	nm, ok2 := n.(map[string]any)
	if !ok1 || !ok2 {
		if reflect.DeepEqual(o, n) {
			return map[string]any{}
		}
		return n
	}
	out := map[string]any{}
	for k, nv := range nm {
		ov, ok := om[k]
		if !ok {
			out[k] = nv
			continue
		}
		d := diff(ov, nv)
		if m, isMap := d.(map[string]any); isMap && len(m) == 0 && reflect.DeepEqual(ov, nv) {
			continue
		}
		out[k] = d
	}
	for k := range om {
		if _, ok := nm[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

func resourcePath(group, version, resource, namespace, name string) string {
	prefix := version
	if group != "" {
		prefix = group + "/" + version
	}
	if namespace != "" {
		return fmt.Sprintf("%s/namespaces/%s/%s/%s", prefix, namespace, resource, name)
	}
	return fmt.Sprintf("%s/%s/%s", prefix, resource, name)
}

func approveDelete(args []string) {
	fs := flag.NewFlagSet("approve-delete", flag.ExitOnError)
	keyPath := fs.String("key", "", "PEM private key")
	group := fs.String("group", "", "API group (\"\" for core)")
	version := fs.String("version", "v1", "API version")
	resource := fs.String("resource", "", "API resource (plural)")
	namespace := fs.String("namespace", "", "Namespace (empty for cluster-scoped)")
	name := fs.String("name", "", "Object name")
	_ = fs.Parse(args)

	k, err := loadKey(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(signMsg(k, resourcePath(*group, *version, *resource, *namespace, *name)))
}

func approveUpdate(args []string) {
	fs := flag.NewFlagSet("approve-update", flag.ExitOnError)
	keyPath := fs.String("key", "", "PEM private key")
	oldPath := fs.String("old", "", "old object JSON")
	newPath := fs.String("new", "", "new object JSON")
	annotationKey := fs.String("annotation-key", defaultChangeApprovalAnnotation, "Change approval annotation key to strip before signing")
	_ = fs.Parse(args)

	k, err := loadKey(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	oldB, err := os.ReadFile(*oldPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	newB, err := os.ReadFile(*newPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	patch, err := normalizedMergePatch(string(oldB), string(newB), []string{*annotationKey})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(signMsg(k, patch))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: watchctl {approve-delete|approve-update} ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "approve-delete":
		approveDelete(os.Args[2:])
	case "approve-update":
		approveUpdate(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}
