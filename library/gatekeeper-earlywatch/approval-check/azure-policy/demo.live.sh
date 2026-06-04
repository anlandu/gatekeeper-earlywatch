#!/usr/bin/env bash
# Live walkthrough for approval-check.
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-approval-check
sign() { printf '%s' "$1" | openssl dgst -sha256 -sigopt rsa_padding_mode:pss -sign keys/priv.pem -binary | base64 -w0; }
GOOD_SIG=$(sign "v1/namespaces/$NS/configmaps/protected-good")
BAD_SIG="AAAA-bogus-signature-AAAA"

# Tail the approval-verifier provider so the audience can see each
# external-data call Gatekeeper makes during admission. Lines are prefixed
# [approval-verifier] and streamed to stderr so they interleave with demo
# output without breaking demo-magic's prompt-then-press-enter flow.
PROVIDER_LOG=$(mktemp)
kubectl -n gatekeeper-system logs -f --since=1s -l app=approval-verifier --max-log-requests=10 \
  >"$PROVIDER_LOG" 2>&1 &
PROVIDER_LOG_PID=$!
( tail -F "$PROVIDER_LOG" 2>/dev/null | sed -u 's/^/[approval-verifier] /' >&2 ) &
PROVIDER_TAIL_PID=$!
cleanup() {
  kill "$PROVIDER_LOG_PID" "$PROVIDER_TAIL_PID" 2>/dev/null || true
  rm -f "$PROVIDER_LOG" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

clear

p "# === EarlyWatch: approval-check ==="
p "# Scenario: DELETEs on protected ConfigMaps must carry a valid"
p "# signature of the canonical delete path. Anyone who can edit YAML"
p "# can't unilaterally delete; only a holder of the signing key can."
p "# The Azure Policy for Kubernetes addon is already actively integrating"
p "# with the approvals team to make this production-ready for customers."
wait

p "# --- live provider log ---"
p "# In parallel, this terminal is tailing the approval-verifier provider."
p "# Every line prefixed [approval-verifier] is one external-data call"
p "# Gatekeeper made to verify a signature during admission."
p "# (Or open another pane: kubectl -n gatekeeper-system logs -f -l app=approval-verifier)"
wait


p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# Standard scope params: deny mode, ns=[ew-approval-check]."
p "# The public key for signature verification is baked into the"
p "# definition.json by demo.sh (PEM substituted into a 'values' field)."
p "# but could also be parameterized so different policy assignemnts "
p "# can have different keys."
wait

p "# --- the signing key (RSA-PSS-SHA256) ---"
pe "ls keys/"
p "# priv.pem signs; pub.pem is embedded in the policy definition."
wait

p "# --- seed two ConfigMaps in $NS, each with an earlywatch.io/approved annotation ---"
p "# good carries a valid sig of v1/namespaces/$NS/configmaps/protected-good"
p "# bad carries a string that is obviously not a signature"
SEED=$(mktemp --suffix=.yaml)
cat >"$SEED" <<EOF
apiVersion: v1
kind: ConfigMap
metadata: {name: protected-good, annotations: {earlywatch.io/approved: "$GOOD_SIG"}}
data: {k: v}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: protected-bad, annotations: {earlywatch.io/approved: "$BAD_SIG"}}
data: {k: v}
EOF
pe "cat $SEED"
pe "kubectl -n $NS apply -f $SEED"
wait

p "# --- BAD: DELETE protected-bad ---"
p "# Rule recomputes the expected sig of the path, compares to annotation,"
p "# rejects: signature doesn't verify."
pe "kubectl -n $NS delete cm protected-bad --wait=false"
pe "kubectl -n $NS get cm protected-bad"
wait

p "# --- GOOD: DELETE protected-good ---"
p "# Same rule, same key; the annotation is a valid PSS-SHA256 signature"
p "# over the canonical delete path -> allowed."
pe "kubectl -n $NS delete cm protected-good --wait=false"
