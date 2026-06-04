#!/usr/bin/env bash
# Live walkthrough for name-reference-check.
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-name-reference-check

clear

p "# === EarlyWatch: name-reference-check ==="
p "# Scenario: deleting a ConfigMap that a Pod/Deployment volume-mounts"
p "# leaves the workload broken at next restart. This rule denies the"
p "# DELETE while a referencer still exists in sync inventory."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# deny mode, scoped to ns=[ew-name-reference-check]."
wait

p "# --- BAD setup: cm 'referenced' is mounted by deployment 'web' ---"
pe "cat bad.yaml"
pe "kubectl -n $NS apply -f bad.yaml"
p "# Wait for the Deployment+Pod to land in Gatekeeper's sync cache."
pe "sleep 30"
wait

p "# --- BAD: DELETE the referenced ConfigMap ---"
p "# Rego walks sync inventory for workloads whose volumes/envs name"
p "# this CM, finds 'web', denies."
pe "kubectl -n $NS delete cm referenced --wait=false"
pe "kubectl -n $NS get cm referenced"
wait

p "# --- GOOD: an unreferenced CM ---"
pe "cat good.yaml"
pe "kubectl -n $NS apply -f good.yaml"
p "# No workload mounts 'unreferenced' -> DELETE allowed."
pe "kubectl -n $NS delete cm unreferenced --wait=false"
