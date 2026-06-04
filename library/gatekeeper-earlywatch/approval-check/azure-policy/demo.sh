#!/usr/bin/env bash
# EarlyWatch Azure Policy demo for rule: approval-check
# Renders an end-to-end deny-mode demo against the current az/kubectl context.
set -euo pipefail
RULE=approval-check
NS=ew-approval-check
DEFNAME=ew-approvalcheck
HERE=$(cd "$(dirname "$0")" && pwd)
SUB=${SUB:-43771321-626c-4a17-983b-f87080a5f77f}
RG=${RG:-anlantest}
SCOPE="/subscriptions/$SUB/resourceGroups/$RG"
ASSIGN_NAME="ew-$RULE"

log() { printf '\n=== [%s] %s ===\n' "$RULE" "$*"; }

log "ensuring namespace"
kubectl create ns "$NS" --dry-run=client -o yaml | kubectl apply -f -


log "ensure approval-check signing keypair (no-op if orchestrator already did it)"
KEY_DIR="$HERE/keys"
mkdir -p "$KEY_DIR"
if [ ! -s "$KEY_DIR/priv.pem" ] || [ ! -s "$KEY_DIR/pub.pem" ]; then
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$KEY_DIR/priv.pem" >/dev/null 2>&1
  openssl pkey -in "$KEY_DIR/priv.pem" -pubout -out "$KEY_DIR/pub.pem"
fi

log "patch publicKey into definition.json (no-op if unchanged)"
if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  PUB_PEM=$(cat "$KEY_DIR/pub.pem")
  TMP=$(mktemp)
  jq --arg pk "$PUB_PEM" '.properties.policyRule.then.details.values.publicKey = $pk' "$HERE/definition.json" > "$TMP"
  mv "$TMP" "$HERE/definition.json"
fi

log "create policy definition (idempotent)"
if [ -f "$HERE/definition.json" ]; then
  if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  az policy definition create \
      --name "$DEFNAME" \
      --rules @"$HERE/definition.json" \
      --mode Microsoft.Kubernetes.Data \
      --subscription "$SUB" >/dev/null || true
fi
  POLICY_ID="/subscriptions/$SUB/providers/Microsoft.Authorization/policyDefinitions/$DEFNAME"
else
  # built-in (annotation-check)
  POLICY_ID=$(jq -r .policyDefinitionId "$HERE/assignment.json")
fi

log "create assignment in deny mode"
if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  az policy assignment create \
    --name "$ASSIGN_NAME" \
    --display-name "EarlyWatch $RULE" \
    --scope "$SCOPE" \
    --policy "$POLICY_ID" \
    --params @"$HERE/assignment.json" >/dev/null
fi

log "force SyncPolicies resync"
if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  bash "/mnt/c/Users/anlandu/workspace/gatekeeper-earlywatch/scripts/force-syncpolicies.sh" || true
fi

log "wait for constraint to land on cluster (best-effort)"
CT_TOKEN="ew${DEFNAME#ew-}"
for _ in $(seq 1 30); do
  kubectl get constraints 2>/dev/null | grep -q "$CT_TOKEN" && break
  sleep 10
done

sign_delete_path() {
  # echo -n to avoid trailing newline in the signed message; must match rego canonical_path exactly.
  printf '%s' "$1" | openssl dgst -sha256 -sigopt rsa_padding_mode:pss -sign "$KEY_DIR/priv.pem" -binary | base64 -w0
}

GOOD_PATH="v1/namespaces/$NS/configmaps/protected-gf-good"
GOOD_SIG=$(sign_delete_path "$GOOD_PATH")
BAD_SIG="AAAA-bogus-signature-AAAA"

log "greenfield seed CMs with delete-approval annotations (CREATE skips rule)"
cat <<EOF | kubectl -n "$NS" apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: protected-gf-good
  annotations:
    earlywatch.io/approved: "$GOOD_SIG"
data: {k: v}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: protected-gf-bad
  annotations:
    earlywatch.io/approved: "$BAD_SIG"
data: {k: v}
EOF

log "greenfield BAD: DELETE protected-gf-bad with bogus signature (expect deny)"
if kubectl -n "$NS" delete cm protected-gf-bad --wait=false 2>&1 | tee /tmp/$RULE.bad.log; then
  echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2
  exit 1
fi
grep -qi 'denied\|signature\|rejected' /tmp/$RULE.bad.log || echo "warn: denial output did not include expected text"

log "greenfield GOOD: DELETE protected-gf-good with valid signature (expect allow)"
kubectl -n "$NS" delete cm protected-gf-good --wait=false

log "done"
