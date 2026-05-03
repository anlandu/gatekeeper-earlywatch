// EarlyWatch ApprovalCheck external-data provider.
//
// Implements Gatekeeper's external-data ProviderRequest/ProviderResponse
// contract over mTLS and verifies RSA-PSS SHA-256 signatures against the
// canonical message formats produced by 04-approval-check-template.yaml:
//
//	delete|<pem-public-key>|<canonical-path>|<base64-signature>
//	update|<pem-public-key>|<change-annotation-key>|<base64-old-json>|<base64-new-json>|<base64-signature>
//
// For UPDATE the provider decodes the old/new JSON objects, normalizes them
// the same way upstream EarlyWatch does, computes the RFC 7396 JSON merge-patch,
// and verifies the signature over that patch.
//
// Each incoming key is answered with ["<key>", "valid"] on success or
// ["<key>", "<reason>"] on failure, matching the shape the ConstraintTemplate
// expects from response.responses[i].
package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// --- Gatekeeper external-data wire types (v1beta1) ---

type providerRequest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Request    struct {
		Keys []string `json:"keys"`
	} `json:"request"`
}

type providerItem struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	Error string `json:"error,omitempty"`
}

type providerResponse struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Response   struct {
		Idempotent  bool           `json:"idempotent"`
		Items       []providerItem `json:"items"`
		SystemError string         `json:"systemError,omitempty"`
	} `json:"response"`
}

// --- Verification ---

// Pubkey cache: PEM string -> *rsa.PublicKey. PEM parsing is ~50us/key; cache
// trims that off the admission latency budget for steady-state workloads.
var pubKeyCache sync.Map // map[string]*rsa.PublicKey

// Metrics counters (exposed at /metrics in Prometheus text format).
var (
	mValid    atomic.Uint64
	mInvalid  atomic.Uint64
	mError    atomic.Uint64
	mRequests atomic.Uint64
)

// Trusted-key directory: when --trusted-keys-dir is set, public keys passed
// inline via the constraint parameter are accepted ONLY if their PEM matches a
// key on disk. This shifts the trust root from constraint authors (who have RBAC
// write on EWApprovalCheck) to whoever populates the verifier's mounted Secret.
var trustedKeys map[string]*rsa.PublicKey

func parsePublicKey(pemStr string) (*rsa.PublicKey, error) {
	if v, ok := pubKeyCache.Load(pemStr); ok {
		return v.(*rsa.PublicKey), nil
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in public key data")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing PKIX public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not an RSA key")
	}
	pubKeyCache.Store(pemStr, rsaPub)
	return rsaPub, nil
}

func loadTrustedKeys(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	trustedKeys = map[string]*rsa.PublicKey{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(dir + "/" + e.Name())
		if err != nil {
			return err
		}
		key, err := parsePublicKey(string(b))
		if err != nil {
			slog.Warn("skip non-PEM file in trusted-keys-dir", "file", e.Name(), "err", err)
			continue
		}
		trustedKeys[strings.TrimSpace(string(b))] = key
	}
	slog.Info("loaded trusted keys", "count", len(trustedKeys))
	return nil
}

func verifyPSS(pubPEM, message, sigB64 string) error {
	if trustedKeys != nil {
		if _, ok := trustedKeys[strings.TrimSpace(pubPEM)]; !ok {
			return fmt.Errorf("public key not in trusted-keys-dir")
		}
	}
	pub, err := parsePublicKey(pubPEM)
	if err != nil {
		return fmt.Errorf("parse pubkey: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	sum := sha256.Sum256([]byte(message))
	if err := rsa.VerifyPSS(pub, crypto.SHA256, sum[:], sig, nil); err != nil {
		return fmt.Errorf("signature invalid: %w", err)
	}
	return nil
}

const defaultChangeApprovalAnnotation = "earlywatch.io/change-approved"

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

// mergePatch computes the RFC 7396 JSON merge-patch from oldJSON -> newJSON.
// It is kept as the legacy/no-strip helper for tests and the pre-parity key
// format. ApprovalCheck UPDATE verification should use normalizedMergePatch.
func mergePatch(oldJSON, newJSON string) (string, error) {
	return normalizedMergePatch(oldJSON, newJSON, nil)
}

// normalizedMergePatch mirrors upstream EarlyWatch's internal patch helper:
// strip server-managed top-level and metadata fields, strip configured approval
// annotations, remove empty/null annotation maps, then compute a canonical RFC
// 7396 JSON merge patch.
func normalizedMergePatch(oldJSON, newJSON string, stripAnnotations []string) (string, error) {
	o, err := normalizeForPatch([]byte(oldJSON), stripAnnotations)
	if err != nil {
		return "", fmt.Errorf("normalizing old object: %w", err)
	}
	n, err := normalizeForPatch([]byte(newJSON), stripAnnotations)
	if err != nil {
		return "", fmt.Errorf("normalizing new object: %w", err)
	}
	patch := diff(o, n)
	return canonicalJSON(patch)
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

// canonicalJSON emits JSON with object keys sorted lexicographically at every
// level. This is a pragmatic subset of RFC 8785 (JCS) — sufficient for our
// usage because all signed payloads originate as JSON objects (no float
// re-encoding concerns for k8s metadata).
func canonicalJSON(v any) (string, error) {
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
			s, err := canonicalJSON(t[k])
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		b.WriteByte('}')
		return b.String(), nil
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, el := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			s, err := canonicalJSON(el)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		b.WriteByte(']')
		return b.String(), nil
	default:
		out, err := json.Marshal(t)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
}

// diff implements RFC 7396 merge-patch semantics: keys present in new replace
// old; keys absent in new but present in old yield an explicit null; nested
// objects recurse; non-object values compared by deep-equal.
func diff(oldV, newV any) any {
	oldObj, oldIsObj := oldV.(map[string]any)
	newObj, newIsObj := newV.(map[string]any)
	if !oldIsObj || !newIsObj {
		if reflect.DeepEqual(oldV, newV) {
			return map[string]any{}
		}
		return newV
	}
	out := map[string]any{}
	for k, nv := range newObj {
		ov, ok := oldObj[k]
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
	for k := range oldObj {
		if _, ok := newObj[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

// --- Key parsing ---

// splitN splits on '|' but keeps the JSON payloads (which may contain '|')
// intact by relying on the fixed field count per opcode.
func parseKey(key string) (op string, parts []string, err error) {
	if i := strings.IndexByte(key, '|'); i > 0 {
		op = key[:i]
		rest := key[i+1:]
		switch op {
		case "delete":
			parts = strings.SplitN(rest, "|", 3)
			if len(parts) != 3 {
				return op, nil, fmt.Errorf("delete: want 3 fields after op, got %d", len(parts))
			}
		case "update":
			parts = strings.SplitN(rest, "|", 5)
			if len(parts) == 5 {
				return op, parts, nil
			}
			// Legacy raw-JSON key format: update|pub|old-json|new-json|sig.
			parts = strings.SplitN(rest, "|", 4)
			if len(parts) != 4 {
				return op, nil, fmt.Errorf("update: want 5 fields (or 4 legacy fields) after op, got %d", len(parts))
			}
		default:
			return op, nil, fmt.Errorf("unknown op %q", op)
		}
		return op, parts, nil
	}
	return "", nil, fmt.Errorf("no opcode")
}

func evaluate(key string) string {
	op, parts, err := parseKey(key)
	if err != nil {
		return err.Error()
	}
	switch op {
	case "delete":
		pub, path, sig := parts[0], parts[1], parts[2]
		if err := verifyPSS(pub, path, sig); err != nil {
			return err.Error()
		}
		return "valid"
	case "update":
		var pub, oldJSON, newJSON, sig string
		stripAnnotations := []string{defaultChangeApprovalAnnotation}
		if len(parts) == 5 {
			pub = parts[0]
			annotationKey := parts[1]
			if annotationKey == "" {
				annotationKey = defaultChangeApprovalAnnotation
			}
			stripAnnotations = []string{annotationKey}
			oldBytes, err := base64.StdEncoding.DecodeString(parts[2])
			if err != nil {
				return fmt.Sprintf("decode old object: %v", err)
			}
			newBytes, err := base64.StdEncoding.DecodeString(parts[3])
			if err != nil {
				return fmt.Sprintf("decode new object: %v", err)
			}
			oldJSON, newJSON, sig = string(oldBytes), string(newBytes), parts[4]
		} else {
			pub, oldJSON, newJSON, sig = parts[0], parts[1], parts[2], parts[3]
		}
		patch, err := normalizedMergePatch(oldJSON, newJSON, stripAnnotations)
		if err != nil {
			return err.Error()
		}
		if err := verifyPSS(pub, patch, sig); err != nil {
			return err.Error()
		}
		return "valid"
	}
	return "unhandled op"
}

// --- HTTP ---

func handle(w http.ResponseWriter, r *http.Request) {
	mRequests.Add(1)
	var req providerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mError.Add(1)
		slog.Warn("decode request", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := providerResponse{
		APIVersion: "externaldata.gatekeeper.sh/v1beta1",
		Kind:       "ProviderResponse",
	}
	resp.Response.Idempotent = true
	for _, k := range req.Request.Keys {
		v := evaluate(k)
		switch v {
		case "valid":
			mValid.Add(1)
		default:
			mInvalid.Add(1)
		}
		resp.Response.Items = append(resp.Response.Items, providerItem{Key: k, Value: v})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func metricsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP approval_requests_total Total ProviderRequests received.\n")
	fmt.Fprintf(w, "# TYPE approval_requests_total counter\n")
	fmt.Fprintf(w, "approval_requests_total %d\n", mRequests.Load())
	fmt.Fprintf(w, "# HELP approval_verifications_total Verification outcomes by result.\n")
	fmt.Fprintf(w, "# TYPE approval_verifications_total counter\n")
	fmt.Fprintf(w, "approval_verifications_total{result=\"valid\"} %d\n", mValid.Load())
	fmt.Fprintf(w, "approval_verifications_total{result=\"invalid\"} %d\n", mInvalid.Load())
	fmt.Fprintf(w, "approval_verifications_total{result=\"error\"} %d\n", mError.Load())
}

func main() {
	addr := flag.String("addr", ":8443", "listen address")
	cert := flag.String("cert", "/certs/tls.crt", "server certificate")
	key := flag.String("key", "/certs/tls.key", "server key")
	insecure := flag.Bool("insecure", false, "serve plain HTTP (testing only)")
	trustedDir := flag.String("trusted-keys-dir", "", "if set, only PEMs in this directory are accepted")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if *trustedDir != "" {
		if err := loadTrustedKeys(*trustedDir); err != nil {
			slog.Error("loadTrustedKeys", "err", err)
			os.Exit(1)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/validate", handle)
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: *addr, Handler: mux}
	slog.Info("approval-verifier listening", "addr", *addr, "trustedKeys", *trustedDir != "")
	if *insecure {
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
		return
	}
	if _, err := os.Stat(*cert); err != nil {
		slog.Error("cert", "err", err)
		os.Exit(1)
	}
	if err := srv.ListenAndServeTLS(*cert, *key); err != nil {
		slog.Error("listen-tls", "err", err)
		os.Exit(1)
	}
}
