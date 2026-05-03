package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func mustKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return priv, string(pemBytes)
}

func sign(t *testing.T, priv *rsa.PrivateKey, msg string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPSS(rand.Reader, priv, crypto.SHA256, sum[:], nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func TestDeleteValid(t *testing.T) {
	priv, pub := mustKey(t)
	path := "v1/namespaces/default/services/web"
	sig := sign(t, priv, path)
	key := strings.Join([]string{"delete", pub, path, sig}, "|")
	if got := evaluate(key); got != "valid" {
		t.Fatalf("want valid, got %q", got)
	}
}

func TestDeleteTampered(t *testing.T) {
	priv, pub := mustKey(t)
	sig := sign(t, priv, "good-path")
	key := strings.Join([]string{"delete", pub, "tampered-path", sig}, "|")
	if got := evaluate(key); got == "valid" {
		t.Fatal("want failure, got valid")
	}
}

func updateKey(pub, annotationKey, oldJSON, newJSON, sig string) string {
	return strings.Join([]string{
		"update",
		pub,
		annotationKey,
		base64.StdEncoding.EncodeToString([]byte(oldJSON)),
		base64.StdEncoding.EncodeToString([]byte(newJSON)),
		sig,
	}, "|")
}

func TestUpdateValid(t *testing.T) {
	priv, pub := mustKey(t)
	oldJSON := `{"metadata":{"name":"x"},"spec":{"replicas":1}}`
	newJSON := `{"metadata":{"name":"x"},"spec":{"replicas":3}}`
	patch, err := mergePatch(oldJSON, newJSON)
	if err != nil {
		t.Fatal(err)
	}
	sig := sign(t, priv, patch)
	key := strings.Join([]string{"update", pub, oldJSON, newJSON, sig}, "|")
	if got := evaluate(key); got != "valid" {
		t.Fatalf("want valid, got %q", got)
	}
}

func TestUpdateUnsignedSpecChange(t *testing.T) {
	priv, pub := mustKey(t)
	oldJSON := `{"spec":{"replicas":1}}`
	newJSON := `{"spec":{"replicas":3}}`
	// sign a different patch
	sig := sign(t, priv, `{"spec":{"replicas":2}}`)
	key := strings.Join([]string{"update", pub, oldJSON, newJSON, sig}, "|")
	if got := evaluate(key); got == "valid" {
		t.Fatal("want failure for mismatched signature")
	}
}

func TestUpdateNormalizationIgnoresStatusAndManagedMetadata(t *testing.T) {
	priv, pub := mustKey(t)
	oldJSON := `{"metadata":{"name":"x","resourceVersion":"1","generation":1,"uid":"old","creationTimestamp":"2024-01-01T00:00:00Z","managedFields":[{"manager":"a"}],"selfLink":"/api/v1/x"},"spec":{"replicas":1},"status":{"readyReplicas":1}}`
	newJSON := `{"metadata":{"name":"x","resourceVersion":"2","generation":2,"uid":"new","creationTimestamp":"2024-01-02T00:00:00Z","managedFields":[{"manager":"b"}],"selfLink":"/api/v1/y"},"spec":{"replicas":1},"status":{"readyReplicas":0}}`
	patch, err := normalizedMergePatch(oldJSON, newJSON, []string{defaultChangeApprovalAnnotation})
	if err != nil {
		t.Fatal(err)
	}
	if patch != `{}` {
		t.Fatalf("want empty patch, got %s", patch)
	}
	sig := sign(t, priv, patch)
	if got := evaluate(updateKey(pub, defaultChangeApprovalAnnotation, oldJSON, newJSON, sig)); got != "valid" {
		t.Fatalf("want valid, got %q", got)
	}
}

func TestUpdateNormalizationStripsChangeAnnotationAndDropsEmptyAnnotations(t *testing.T) {
	priv, pub := mustKey(t)
	oldJSON := `{"metadata":{"name":"x","annotations":{"earlywatch.io/change-approved":"old"}},"spec":{"replicas":1}}`
	newJSON := `{"metadata":{"name":"x","annotations":{"earlywatch.io/change-approved":"new"}},"spec":{"replicas":3}}`
	patch, err := normalizedMergePatch(oldJSON, newJSON, []string{defaultChangeApprovalAnnotation})
	if err != nil {
		t.Fatal(err)
	}
	if patch != `{"spec":{"replicas":3}}` {
		t.Fatalf("want spec-only patch, got %s", patch)
	}
	sig := sign(t, priv, patch)
	if got := evaluate(updateKey(pub, defaultChangeApprovalAnnotation, oldJSON, newJSON, sig)); got != "valid" {
		t.Fatalf("want valid, got %q", got)
	}
}

func TestUpdateNormalizationDropsNullAnnotations(t *testing.T) {
	oldJSON := `{"metadata":{"name":"x","annotations":null},"spec":{"replicas":1}}`
	newJSON := `{"metadata":{"name":"x"},"spec":{"replicas":1}}`
	patch, err := normalizedMergePatch(oldJSON, newJSON, []string{defaultChangeApprovalAnnotation})
	if err != nil {
		t.Fatal(err)
	}
	if patch != `{}` {
		t.Fatalf("want empty patch, got %s", patch)
	}
}

func TestUpdateBase64KeyAllowsPipeInJSON(t *testing.T) {
	priv, pub := mustKey(t)
	oldJSON := `{"metadata":{"name":"x"},"data":{"k":"a|b"}}`
	newJSON := `{"metadata":{"name":"x"},"data":{"k":"c|d"}}`
	patch, err := normalizedMergePatch(oldJSON, newJSON, []string{defaultChangeApprovalAnnotation})
	if err != nil {
		t.Fatal(err)
	}
	sig := sign(t, priv, patch)
	if got := evaluate(updateKey(pub, defaultChangeApprovalAnnotation, oldJSON, newJSON, sig)); got != "valid" {
		t.Fatalf("want valid, got %q", got)
	}
}

func TestUpdateTamperedMeaningfulChangeFails(t *testing.T) {
	priv, pub := mustKey(t)
	oldJSON := `{"metadata":{"name":"x"},"data":{"k":"old"}}`
	signedNewJSON := `{"metadata":{"name":"x"},"data":{"k":"signed"}}`
	tamperedNewJSON := `{"metadata":{"name":"x"},"data":{"k":"tampered"}}`
	patch, err := normalizedMergePatch(oldJSON, signedNewJSON, []string{defaultChangeApprovalAnnotation})
	if err != nil {
		t.Fatal(err)
	}
	sig := sign(t, priv, patch)
	if got := evaluate(updateKey(pub, defaultChangeApprovalAnnotation, oldJSON, tamperedNewJSON, sig)); got == "valid" {
		t.Fatal("want failure for tampered meaningful change")
	}
}

func TestMergePatchRemoval(t *testing.T) {
	got, err := mergePatch(`{"a":1,"b":2}`, `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"b":null}` {
		t.Fatalf("want removal patch, got %s", got)
	}
}
