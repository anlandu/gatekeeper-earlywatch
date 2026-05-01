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
	path := "/v1/namespaces/default/services/web"
	sig := sign(t, priv, path)
	pubOneLine := strings.ReplaceAll(pub, "\n", "\\n")
	_ = pubOneLine
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

func TestMergePatchRemoval(t *testing.T) {
	got, err := mergePatch(`{"a":1,"b":2}`, `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"b":null}` {
		t.Fatalf("want removal patch, got %s", got)
	}
}
