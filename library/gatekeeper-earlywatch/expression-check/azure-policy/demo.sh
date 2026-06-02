#!/usr/bin/env bash
set -euo pipefail
RULE=expression-check
DEFNAME=ew-expressioncheck
HERE=$(cd "$(dirname "$0")" && pwd)
SUB=${SUB:-43771321-626c-4a17-983b-f87080a5f77f}
RG=${RG:-anlantest}
SCOPE="/subscriptions/$SUB/resourceGroups/$RG"
ASSIGN_NAME="ew-$RULE"
log() { printf '\n=== [%s] %s ===\n' "$RULE" "$*"; }

for ns in production staging; do
  kubectl create ns "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

log "brownfield seed"
kubectl apply -f "$HERE/fixtures/brownfield/bad.yaml"
kubectl apply -f "$HERE/fixtures/brownfield/good.yaml"

if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  az policy definition create --name "$DEFNAME" --rules @"$HERE/definition.json" --mode Microsoft.Kubernetes.Data --subscription "$SUB" >/dev/null || true
fi
POLICY_ID="/subscriptions/$SUB/providers/Microsoft.Authorization/policyDefinitions/$DEFNAME"
if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  az policy assignment create --name "$ASSIGN_NAME" --display-name "EarlyWatch $RULE" --scope "$SCOPE" --policy "$POLICY_ID" --params @"$HERE/assignment.json" >/dev/null
fi

if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  bash "/mnt/c/Users/anlandu/workspace/gatekeeper-earlywatch/scripts/force-syncpolicies.sh" || true
fi
for _ in $(seq 1 30); do kubectl get constraints 2>/dev/null | grep -q "ew${DEFNAME#ew-}" && break; sleep 10; done

# Greenfield (and brownfield share fixture content): re-apply just to make sure live.
kubectl apply -f "$HERE/fixtures/greenfield/bad.yaml"
kubectl apply -f "$HERE/fixtures/greenfield/good.yaml"

log "greenfield BAD: kubectl delete cm in production (expect deny)"
if kubectl delete cm prod-cm -n production 2>&1 | tee /tmp/$RULE.bad.log; then
  echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2; exit 1
fi
log "greenfield GOOD: kubectl delete cm in staging (expect allow)"
kubectl delete cm staging-cm -n staging
log "done"
