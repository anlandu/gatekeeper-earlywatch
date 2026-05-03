# watchctl

Tiny CLI to mint EarlyWatch ApprovalCheck signatures. It produces base64
RSA-PSS SHA-256 signatures using the same canonical message formats the
Gatekeeper provider verifies (`provider/main.go`).

```bash
go build -o watchctl .

# DELETE approval — sign upstream-compatible ResourcePath.
# Core resources have no leading slash and DELETE approvals do not include UID.
SIG=$(./watchctl approve-delete \
        --key=admin.pem \
        --group="" --version=v1 --resource=services \
        --namespace=default --name=web)
kubectl annotate svc/web "earlywatch.io/approved=$SIG"

# UPDATE approval — sign the upstream-normalized RFC 7396 merge patch.
# The normalizer strips status, server-managed metadata, and the configured
# change-approval annotation (default: earlywatch.io/change-approved) before
# computing the patch.
kubectl get deploy/api -o json > old.json
cp old.json new.json
yq -i '.spec.replicas=5' new.json
SIG=$(./watchctl approve-update \
        --key=admin.pem \
        --old=old.json --new=new.json \
        --annotation-key=earlywatch.io/change-approved)
kubectl annotate deploy/api "earlywatch.io/change-approved=$SIG"
```
