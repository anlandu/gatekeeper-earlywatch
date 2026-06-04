#!/usr/bin/env bash
set -euo pipefail
RULE=annotation-check
HERE=$(cd "$(dirname "$0")" && pwd)
SUB=${SUB:-43771321-626c-4a17-983b-f87080a5f77f}
RG=${RG:-anlantest}
SCOPE="/subscriptions/$SUB/resourceGroups/$RG"
ASSIGN_NAME="ew-$RULE"
log() { printf '\n=== [%s] %s ===\n' "$RULE" "$*"; }

POLICY_ID=$(jq -r .policyDefinitionId "$HERE/assignment.json")
log "create assignment (built-in $POLICY_ID)"
PARAMS=$(jq -c .parameters "$HERE/assignment.json")
if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  az policy assignment create \
    --name "$ASSIGN_NAME" \
    --display-name "EarlyWatch $RULE (built-in)" \
    --scope "$SCOPE" \
    --policy "$POLICY_ID" \
    --params "$PARAMS" >/dev/null
fi

if [ -z "${SKIP_POLICY_SETUP:-}" ]; then
  bash "/mnt/c/Users/anlandu/workspace/gatekeeper-earlywatch/scripts/force-syncpolicies.sh" || true
fi

for _ in $(seq 1 30); do
  kubectl get k8sazurev2containerallowedimages.constraints.gatekeeper.sh 2>/dev/null || true
  kubectl get constraints.gatekeeper.sh 2>/dev/null | grep -qi annotation && break
  sleep 10
done

log "greenfield BAD (expect deny)"
if kubectl apply -f "$HERE/bad.yaml" 2>&1 | tee /tmp/$RULE.bad.log; then
  echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2
  exit 1
fi
log "greenfield GOOD (expect allow)"
kubectl apply -f "$HERE/good.yaml"
log "done"
