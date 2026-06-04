#!/usr/bin/env bash
# Live walkthrough for existing-resources (static-selector variant).
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-existing-resources-static

clear

p "# === EarlyWatch: existing-resources (static) ==="
p "# Scenario: any DELETE on a Service is denied if the namespace contains"
p "# a Pod tagged with one of an assignment-time label set (here:"
p "# tier in {production, staging}). Protects 'load-bearing' Services from"
p "# being yanked out while real workloads still depend on them."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# kinds=[Service]  operations=[DELETE]"
p "# labelKey=tier   labelValues=[production, staging]"
p "# Selector lives in the assignment, not the rule -> same policy can"
p "# protect any team's 'these tags are critical' contract."
wait

p "# --- seed: svc-bad alongside prod-pod (tier=production) ---"
pe "cat bad.yaml"
pe "kubectl -n $NS apply -f bad.yaml"
p "# Wait for prod-pod to land in Gatekeeper's sync cache."
pe "sleep 30"
wait

p "# --- BAD: DELETE svc-bad ---"
p "# Rule walks sync inventory for Pods with tier in {production, staging}."
p "# prod-pod hits; ANY Service delete in the namespace is denied."
pe "kubectl -n $NS delete svc svc-bad --wait=false"
pe "kubectl -n $NS get svc svc-bad"
wait

p "# --- GOOD: drain the matching Pod, then DELETE works ---"
pe "kubectl -n $NS delete pod prod-pod --wait=true"
pe "sleep 30  # let sync inventory catch up"
p "# No more tier=production pods in the namespace -> Service delete allowed."
pe "kubectl -n $NS delete svc svc-bad --wait=false"
