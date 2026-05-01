# watchctl

Tiny CLI to mint EarlyWatch ApprovalCheck signatures, mirroring the upstream
`watchctl approve` UX. Produces base64 RSA-PSS SHA-256 signatures using the
**same canonical message formats** the provider expects (`provider/main.go`).

```bash
go build -o watchctl .

# DELETE approval — bind to UID
SIG=$(./watchctl approve-delete \
        --key=admin.pem \
        --group="" --version=v1 --resource=services \
        --namespace=default --name=web \
        --uid="$(kubectl get svc web -o jsonpath='{.metadata.uid}')")
kubectl annotate svc/web "earlywatch.io/approved=$SIG"

# UPDATE approval — sign the merge-patch
kubectl get deploy/api -o json > old.json
yq -i '.spec.replicas=5' old.json > new.json
SIG=$(./watchctl approve-update --key=admin.pem --old=old.json --new=new.json)
kubectl annotate deploy/api "earlywatch.io/change-approved=$SIG"
```
