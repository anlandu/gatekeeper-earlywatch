#!/usr/bin/env bash
# EarlyWatch Azure Policy demo for rule: manual-touch-check
# Renders an end-to-end deny-mode demo against the current az/kubectl context.
set -euo pipefail
RULE=manual-touch-check
NS=ew-manual-touch-check
DEFNAME=ew-manualtouchcheck
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

log "greenfield seed (CREATE deployments allowed)"
kubectl -n "$NS" apply -f "$HERE/bad.yaml"
[ -f "$HERE/good.yaml" ] && kubectl -n "$NS" apply -f "$HERE/good.yaml"

log "record synthetic manual-touch audit event for 'touched' deployment"
kubectl -n early-watch-system port-forward svc/manual-touch-monitor 18443:8443 >/tmp/$RULE.pf.log 2>&1 &
PF_PID=$!
trap "kill $PF_PID 2>/dev/null || true" EXIT
sleep 3
NOW_TS=$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)
curl -sk -m 10 -X POST https://localhost:18443/audit \
  -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"auditID\":\"demo-$(date +%s)\",\"stage\":\"ResponseComplete\",\"verb\":\"update\",\"user\":{\"username\":\"demo-user\",\"groups\":[\"system:authenticated\"]},\"userAgent\":\"kubectl/v1.30.0 (linux/amd64) demo\",\"sourceIPs\":[\"127.0.0.1\"],\"objectRef\":{\"resource\":\"deployments\",\"namespace\":\"$NS\",\"name\":\"touched-gf\",\"apiGroup\":\"apps\",\"apiVersion\":\"v1\"},\"requestReceivedTimestamp\":\"$NOW_TS\"}]}" | tee /tmp/$RULE.audit.log
kill $PF_PID 2>/dev/null || true
trap - EXIT
sleep 2

log "greenfield BAD UPDATE 'touched-gf' via annotate (expect deny: touched in window)"
if kubectl -n "$NS" annotate deployment touched-gf ew-bump=$(date +%s) --overwrite 2>&1 | tee /tmp/$RULE.bad.log; then
  echo "EXPECTED DENY BUT GOT ALLOW for $RULE" >&2
  exit 1
fi
grep -qi 'denied\|touched\|manually' /tmp/$RULE.bad.log || echo "warn: denial output did not include expected text"

if [ -f "$HERE/good.yaml" ]; then
  log "greenfield GOOD UPDATE 'untouched-gf' (expect allow: no touch recorded)"
  kubectl -n "$NS" annotate deployment untouched-gf ew-bump=$(date +%s) --overwrite
fi

log "done"
