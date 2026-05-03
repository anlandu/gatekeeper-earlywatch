# EarlyWatch ApprovalCheck — external-data Provider

Stateless HTTPS service that fulfills the contract documented in
`../04-approval-check-template.yaml`. It answers Gatekeeper external-data
requests, verifying RSA-PSS SHA-256 signatures over the canonical
DELETE/UPDATE messages produced by the constraint template.

## Wire contract

The constraint sends one `keys[]` entry per admission review. The provider
parses the opcode prefix and dispatches:

| Opcode  | Fields (joined by `|`)                                  | Verifies signature over            |
|---------|---------------------------------------------------------|------------------------------------|
| delete  | `<pem-pubkey>|<canonical-path>|<base64-sig>`            | the canonical-path string          |
| update  | `<pem-pubkey>|<change-annotation-key>|<base64-old-json>|<base64-new-json>|<base64-sig>` | upstream-normalized RFC 7396 merge-patch(old → new) |

For each key the response contains `["<key>", "valid"]` on success or
`["<key>", "<reason>"]` on failure — the constraint denies on anything
other than `"valid"`.

## Deploy

```bash
# 1. Build and publish or load the image used by manifests/deployment.yaml.
#    For local kind testing, for example:
docker build -t docker.io/sozercan/keymaster-approval-verifier:parity .
kind load docker-image docker.io/sozercan/keymaster-approval-verifier:parity
#    Then patch manifests/deployment.yaml to use that image, or retag to the
#    image already referenced there.

# 2. Generate certs and create the TLS secret
./manifests/gen-certs.sh ./certs
kubectl -n gatekeeper-system create secret tls approval-verifier-tls \
  --cert=./certs/tls.crt --key=./certs/tls.key

# 3. Apply manifests
kubectl apply -f manifests/deployment.yaml

# 4. Patch the Provider with the base64 CA bundle, then apply
CA_B64=$(base64 < ./certs/ca.crt | tr -d '\n')
sed "s|REPLACE_WITH_BASE64_CA_BUNDLE|$CA_B64|" manifests/provider.yaml | kubectl apply -f -

# 5. Apply the constraint template & a constraint pointing provider: approval-verifier
kubectl apply -f ../04-approval-check-template.yaml
kubectl apply -f ../04-approval-check-constraint.yaml
```

## Test locally

```bash
go test ./...
go run . --insecure --addr=:8080   # plain HTTP for local curl
```

## EarlyWatch parity notes

The provider mirrors upstream EarlyWatch's annotation-based ApprovalCheck flow:

- DELETE verifies an `earlywatch.io/approved` signature over the canonical
  `ResourcePath`.
- UPDATE verifies an `earlywatch.io/change-approved` signature over the
  normalized merge patch between old and new objects.

The public key is still supplied by the Gatekeeper `EWApprovalCheck`
constraint (or constrained further with `--trusted-keys-dir`), because this
project intentionally maps EarlyWatch behavior onto Gatekeeper-native
constraints rather than requiring upstream `ChangeValidator` CRs.
