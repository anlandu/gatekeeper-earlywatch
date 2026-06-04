#!/usr/bin/env bash
# EarlyWatch Azure Policy demo for rule: check-lock
# Renders an end-to-end deny-mode demo against the current az/kubectl context.
set -euo pipefail
RULE=check-lock
NS=ew-check-lock
DEFNAME=ew-checklock
HERE=$(cd "$(dirname "$0")" && pwd)
SUB=${SUB:-43771321-626c-4a17-983b-f87080a5f77f}
RG=${RG:-anlantest}
SCOPE="/subscriptions/$SUB/resourceGroups/$RG"
ASSIGN_NAME="ew-$RULE"

log() { printf '\n=== [%s] %s ===\n' "$RULE" "$*"; }

log "ensuring namespace"
kubectl create ns "$NS" --dry-run=client -o yaml | kubectl apply -f -


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

if [ -f "$HERE/bad.yaml" ]; then
  log "greenfield BAD seed (unlock+delete leftover so re-apply is clean CREATE)"
  kubectl -n "$NS" patch deployment locked-gf --type=json -p='[{"op":"remove","path":"/metadata/annotations/earlywatch.io~1lock"}]' >/dev/null 2>&1 || true
  kubectl -n "$NS" delete deployment locked-gf --ignore-not-found --wait=true >/dev/null 2>&1 || true
  kubectl -n "$NS" apply -f "$HERE/bad.yaml"
  log "wait for locked-gf to appear in Gatekeeper sync inventory (best-effort)"
  sleep 60
  log "greenfield BAD scale (expect deny: scale subresource handled via inventory lookup)"
  if kubectl -n "$NS" scale deployment locked-gf --replicas=2 2>&1 | tee /tmp/$RULE.bad.log; then
    echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2
    exit 1
  fi
  grep -qi 'denied\|locked' /tmp/$RULE.bad.log || echo "warn: denial output did not include expected text"
fi

if [ -f "$HERE/good.yaml" ]; then
  log "greenfield GOOD (expect allow)"
  kubectl -n "$NS" apply -f "$HERE/good.yaml"
fi

log "done"
