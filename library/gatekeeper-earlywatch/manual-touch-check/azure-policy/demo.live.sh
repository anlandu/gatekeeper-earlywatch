#!/usr/bin/env bash
# Live walkthrough for manual-touch-check.
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint + touch-monitor already deployed.

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-manual-touch-check

# Tail the manual-touch-monitor so the audience can see each external-data
# call Gatekeeper makes (one per UPDATE admission) plus the /audit POSTs
# that record manual edits. Lines are prefixed [touch-monitor] and streamed
# to stderr so they interleave with demo output without breaking demo-magic.
PROVIDER_LOG=$(mktemp)
kubectl -n early-watch-system logs -f --since=1s -l app.kubernetes.io/component=manual-touch-monitor --max-log-requests=10 \
  >"$PROVIDER_LOG" 2>&1 &
PROVIDER_LOG_PID=$!
( tail -F "$PROVIDER_LOG" 2>/dev/null | sed -u 's/^/[touch-monitor] /' >&2 ) &
PROVIDER_TAIL_PID=$!
cleanup() {
  kill "$PROVIDER_LOG_PID" "$PROVIDER_TAIL_PID" 2>/dev/null || true
  rm -f "$PROVIDER_LOG" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

clear

p "# === EarlyWatch: manual-touch-check ==="
p "# Scenario: a side channel (audit webhook -> touch-monitor service)"
p "# records who manually edited what. Within a configurable window after"
p "# a manual UPDATE, further UPDATEs to the same object are denied."
p "# Forces GitOps reconciliation to recover the object before more edits."
wait

p "# --- live provider log ---"
p "# In parallel, this terminal is tailing the manual-touch-monitor."
p "# Every line prefixed [touch-monitor] is either an /audit POST"
p "# (the API server reporting a manual edit) or an external-data call"
p "# from Gatekeeper during admission asking 'was this object touched?'"
p "# (Or open another pane: kubectl -n early-watch-system logs -f -l app.kubernetes.io/component=manual-touch-monitor)"
wait


p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# Standard scope; the rule calls touch-monitor through a Gatekeeper"
p "# external-data Provider (manual-touch-provider) at admission time."
wait

p "# --- seed: two Deployments, identical except for name ---"
pe "cat bad.yaml good.yaml"
pe "kubectl -n $NS apply -f bad.yaml -f good.yaml"
wait

p "# --- inject a synthetic 'manual edit' audit event for touched-gf only ---"
p "# In production this comes from the apiserver audit webhook. Here we"
p "# port-forward the touch-monitor and POST a fabricated audit record."
pe "kubectl -n early-watch-system port-forward svc/manual-touch-monitor 18443:8443 >/tmp/pf.log 2>&1 &"
pe "sleep 3"
NOW=$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)
pe "curl -sk -m 10 -X POST https://localhost:18443/audit -H 'Content-Type: application/json' -d '{\"items\":[{\"auditID\":\"live-$(date +%s)\",\"stage\":\"ResponseComplete\",\"verb\":\"update\",\"user\":{\"username\":\"alice\",\"groups\":[\"system:authenticated\"]},\"userAgent\":\"kubectl/v1.30 (demo)\",\"sourceIPs\":[\"127.0.0.1\"],\"objectRef\":{\"resource\":\"deployments\",\"namespace\":\"$NS\",\"name\":\"touched-gf\",\"apiGroup\":\"apps\",\"apiVersion\":\"v1\"},\"requestReceivedTimestamp\":\"$NOW\"}]}'"
pe "pkill -f 'port-forward svc/manual-touch-monitor' || true"
pe "sleep 2"
wait

p "# --- BAD: UPDATE touched-gf (annotate is an UPDATE) ---"
p "# Constraint calls the manual-touch external-data provider (served by"
p "# touch-monitor): was this object touched in the last windowDuration?"
p "# Provider says 'touched' -> deny."
pe "kubectl -n $NS annotate deploy touched-gf demo-bump=$(date +%s) --overwrite"
wait

p "# --- GOOD: UPDATE untouched-gf ---"
p "# No audit event recorded for untouched-gf -> allowed."
pe "kubectl -n $NS annotate deploy untouched-gf demo-bump=$(date +%s) --overwrite"
