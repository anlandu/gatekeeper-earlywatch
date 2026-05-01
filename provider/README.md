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
| update  | `<pem-pubkey>|<old-json>|<new-json>|<base64-sig>`       | RFC 7396 merge-patch(old → new)    |

For each key the response contains `["<key>", "valid"]` on success or
`["<key>", "<reason>"]` on failure — the constraint denies on anything
other than `"valid"`.

## Deploy

```bash
# 1. Build & push the image
docker build -t ghcr.io/your-org/earlywatch-approval-verifier:latest .
docker push  ghcr.io/your-org/earlywatch-approval-verifier:latest

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

## CRD parity (optional)

The provider as shipped takes the public key inline via the constraint
parameter, so it does not require any EarlyWatch CRDs.

If you want full parity with EarlyWatch's `ChangeApproval` CR-based flow
(approvals stored as cluster resources rather than annotations), extend the
provider to:

1. Add a Kubernetes client (`client-go`).
2. On request, look up `ChangeApproval` CRs by name/owner and read the
   stored signature.
3. Optionally read the policy from a `ChangeValidator` CR (signing key,
   N-of-M approvers) instead of trusting the constraint parameters.

That widens the RBAC surface (`get`/`list` on `changeapprovals`,
`changevalidators`) but keeps the operator UX identical to upstream
EarlyWatch.
