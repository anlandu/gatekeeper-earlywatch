#!/usr/bin/env bash
# EarlyWatch Azure Policy demo for rule: data-key-safety-check
# Renders an end-to-end deny-mode demo against the current az/kubectl context.
set -euo pipefail
RULE=data-key-safety-check
NS=ew-datakey
DEFNAME=ew-datakeysafetycheck
HERE=$(cd "$(dirname "$0")" && pwd)
SUB=${SUB:-43771321-626c-4a17-983b-f87080a5f77f}
RG=${RG:-anlantest}
SCOPE="/subscriptions/$SUB/resourceGroups/$RG"
ASSIGN_NAME="ew-$RULE"

log() { printf '\n=== [%s] %s ===\n' "$RULE" "$*"; }

log "ensuring namespace"
kubectl create ns "$NS" --dry-run=client -o yaml | kubectl apply -f -

log "brownfield seed (pre-assignment, allowed)"
[ -f "$HERE/fixtures/brownfield/bad.yaml" ] && kubectl -n "$NS" apply -f "$HERE/fixtures/brownfield/bad.yaml"
[ -f "$HERE/fixtures/brownfield/good.yaml" ] && kubectl -n "$NS" apply -f "$HERE/fixtures/brownfield/good.yaml"

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

if [ -f "$HERE/fixtures/greenfield/bad.yaml" ]; then
  log "reset shared CM to K1+K2 so the BAD apply is an actual UPDATE that drops K1"
  kubectl -n "$NS" apply -f "$HERE/fixtures/greenfield/good.yaml"
  log "wait for pod 'refs' to appear in Gatekeeper sync inventory (best-effort)"
  sleep 60
  log "greenfield BAD (expect deny - drops K1 still referenced by pod refs)"
  if kubectl -n "$NS" apply -f "$HERE/fixtures/greenfield/bad.yaml" 2>&1 | tee /tmp/$RULE.bad.log; then
    echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2
    exit 1
  fi
  grep -qi 'denied' /tmp/$RULE.bad.log || echo "warn: denial output did not include 'denied'"
fi

if [ -f "$HERE/fixtures/greenfield/good.yaml" ]; then
  log "greenfield GOOD (expect allow)"
  kubectl -n "$NS" apply -f "$HERE/fixtures/greenfield/good.yaml"
fi

log "done (brownfield ARG compliance verified by top-level orchestrator)"
