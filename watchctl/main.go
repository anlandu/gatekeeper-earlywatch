// watchctl — sign EarlyWatch approvals for Gatekeeper EWApprovalCheck.
//
//   watchctl approve-delete \
//     --key=privkey.pem \
//     --group="" --version=v1 --resource=services \
//     --namespace=default --name=web --uid=<uid>
//
//   watchctl approve-update \
//     --key=privkey.pem \
//     --old=old.json --new=new.json
//
// Outputs the base64 RSA-PSS signature on stdout. Pipe into the appropriate
// annotation:
//
//   kubectl annotate svc/web "earlywatch.io/approved=$(watchctl approve-delete ...)"
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
	return k.(*rsa.PrivateKey), nil
}

func signMsg(k *rsa.PrivateKey, msg string) string {
	sum := sha256.Sum256([]byte(msg))
	sig, _ := rsa.SignPSS(rand.Reader, k, crypto.SHA256, sum[:], nil)
	return base64.StdEncoding.EncodeToString(sig)
}

// Mirrors provider's canonicalJSON / mergePatch.
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

func approveDelete(args []string) {
	fs := flag.NewFlagSet("approve-delete", flag.ExitOnError)
	keyPath := fs.String("key", "", "PEM private key")
	group := fs.String("group", "", "API group (\"\" for core)")
	version := fs.String("version", "v1", "API version")
	resource := fs.String("resource", "", "API resource (plural)")
	namespace := fs.String("namespace", "", "Namespace (empty for cluster-scoped)")
	name := fs.String("name", "", "Object name")
	uid := fs.String("uid", "", "Object metadata.uid (binds the signature to this specific object instance)")
	_ = fs.Parse(args)

	k, err := loadKey(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var msg string
	if *namespace != "" {
		msg = fmt.Sprintf("%s/%s/namespaces/%s/%s/%s/%s",
			*group, *version, *namespace, *resource, *name, *uid)
	} else {
		msg = fmt.Sprintf("%s/%s/%s/%s/%s",
			*group, *version, *resource, *name, *uid)
	}
	fmt.Println(signMsg(k, msg))
}

func approveUpdate(args []string) {
	fs := flag.NewFlagSet("approve-update", flag.ExitOnError)
	keyPath := fs.String("key", "", "PEM private key")
	oldPath := fs.String("old", "", "old object JSON")
	newPath := fs.String("new", "", "new object JSON")
	_ = fs.Parse(args)

	k, err := loadKey(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	oldB, _ := os.ReadFile(*oldPath)
	newB, _ := os.ReadFile(*newPath)
	var o, n any
	_ = json.Unmarshal(oldB, &o)
	_ = json.Unmarshal(newB, &n)
	patch := canonical(diff(o, n))
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
