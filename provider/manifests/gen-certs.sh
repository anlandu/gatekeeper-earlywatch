#!/usr/bin/env bash
# Generates a self-signed CA + server cert for the approval-verifier service
# and prints the kubectl commands to create the TLS secret + the base64 CA
# bundle to drop into provider.yaml.
#
# Usage: ./gen-certs.sh [output-dir]
set -euo pipefail

OUT="${1:-./certs}"
mkdir -p "$OUT"

openssl genrsa -out "$OUT/ca.key" 4096
openssl req -x509 -new -nodes -key "$OUT/ca.key" -sha256 -days 3650 \
  -subj "/CN=earlywatch-approval-ca" -out "$OUT/ca.crt"

openssl genrsa -out "$OUT/tls.key" 4096
openssl req -new -key "$OUT/tls.key" \
  -subj "/CN=approval-verifier.gatekeeper-system.svc" \
  -out "$OUT/tls.csr"

cat > "$OUT/san.cnf" <<EOF
subjectAltName=DNS:approval-verifier.gatekeeper-system.svc,DNS:approval-verifier.gatekeeper-system.svc.cluster.local
EOF

openssl x509 -req -in "$OUT/tls.csr" \
  -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" -CAcreateserial \
  -out "$OUT/tls.crt" -days 825 -sha256 -extfile "$OUT/san.cnf"

echo
echo "# Secret:"
echo "kubectl -n gatekeeper-system create secret tls approval-verifier-tls \\"
echo "  --cert=$OUT/tls.crt --key=$OUT/tls.key"
echo
echo "# caBundle for provider.yaml:"
base64 < "$OUT/ca.crt" | tr -d '\n'; echo
