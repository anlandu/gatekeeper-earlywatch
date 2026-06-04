#!/usr/bin/env bash
set -euo pipefail
RULE=existing-resources-static
NS=ew-existing-resources-static
DEFNAME=ew-existingresourcesstatic
HERE=$(cd "$(dirname "$0")" && pwd)
SUB=${SUB:-43771321-626c-4a17-983b-f87080a5f77f}
RG=${RG:-anlantest}
SCOPE="/subscriptions/$SUB/resourceGroups/$RG"
ASSIGN_NAME="ew-$RULE"
log() { printf '\n=== [%s] %s ===\n' "$RULE" "$*"; }

# Idempotent ns create (plain `kubectl create ns` errors if it already exists).
kubectl create ns "$NS" --dry-run=client -o yaml | kubectl apply -f -

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
for _ in $(seq 1 30); do kubectl get constraints.gatekeeper.sh 2>/dev/null | grep -q "$DEFNAME" && break; sleep 10; done

if [ -f "$HERE/bad.yaml" ]; then
  log "greenfield BAD seed (CREATE svc+production-pod allowed)"
  kubectl -n "$NS" apply -f "$HERE/bad.yaml"
  log "wait for prod-pod to land in Gatekeeper sync inventory"
  sleep 60
  log "greenfield BAD DELETE svc-bad (expect deny: prod-pod with tier=production is in sync data)"
  if kubectl -n "$NS" delete svc svc-bad --wait=false 2>&1 | tee /tmp/$RULE.bad.log; then
    echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2; exit 1
  fi
fi
if [ -f "$HERE/good.yaml" ]; then
  log "greenfield GOOD (expect allow)"
  kubectl -n "$NS" apply -f "$HERE/good.yaml"
fi
log "done"
