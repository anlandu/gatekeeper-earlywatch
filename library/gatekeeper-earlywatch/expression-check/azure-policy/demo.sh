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

# Greenfield namespaces: prod-eu matches default namespacePattern ^prod.*;
# ew-expression-check is the negative-control namespace (no match).
kubectl create ns prod-eu --dry-run=client -o yaml | kubectl apply -f -
kubectl create ns ew-expression-check --dry-run=client -o yaml | kubectl apply -f -

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
for _ in $(seq 1 30); do kubectl get constraints 2>/dev/null | grep -qi expressioncheck && break; sleep 10; done

log "greenfield seed (cm-1 in prod-eu; cm-ok in default)"
kubectl apply -f "$HERE/bad.yaml"
kubectl apply -f "$HERE/good.yaml"

log "greenfield BAD: delete cm-1 in prod-eu (namespace matches ^prod.*, expect deny)"
if kubectl delete cm cm-1 -n prod-eu 2>&1 | tee /tmp/$RULE.bad.log; then
  echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2; exit 1
fi
log "greenfield GOOD: delete cm-ok in ew-expression-check (no match, expect allow)"
kubectl delete cm cm-ok -n ew-expression-check
log "done"
