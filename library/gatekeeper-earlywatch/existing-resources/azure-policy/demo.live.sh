#!/usr/bin/env bash
# Live walkthrough for existing-resources (field-selector variant).
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).
#
# Mirrors the original Brendan EarlyWatch demo:
#   ChangeValidator { subject: services, operations: [DELETE],
#                     existingResources: pods,
#                     labelSelectorFromField: spec.selector,
#                     sameNamespace: true }
# i.e. "you can't delete a Service while Pods still match its spec.selector".

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-existing-resources

clear

p "# === EarlyWatch: existing-resources (field-selector) ==="
p "# DELETE on a Service is denied while any Pod in the same namespace"
p "# still matches the Service's spec.selector. The selector comes from"
p "# the subject itself (labelSelectorFromField: spec.selector), so the"
p "# rule needs no per-team configuration."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# operations=[DELETE]; no labelKey/labelValues to maintain."
p "# The selector is pulled live from the Service being deleted, then"
p "# matched against Pods cached in Gatekeeper's sync inventory."
wait

p "# --- seed: svc-bad selects app=webx, web-pod has that label ---"
pe "cat bad.yaml"
pe "kubectl -n $NS apply -f bad.yaml"
p "# Wait for web-pod to land in Gatekeeper sync."
pe "sleep 30"
wait

p "# --- BAD: DELETE svc-bad ---"
p "# Rego reads svc-bad.spec.selector = {app: webx}, walks sync inventory,"
p "# finds web-pod with that label, denies the delete."
pe "kubectl -n $NS delete svc svc-bad --wait=false"
pe "kubectl -n $NS get svc svc-bad"
wait

p "# --- GOOD #1: svc-good selects app=missing, no Pods match ---"
pe "cat good.yaml"
pe "kubectl -n $NS apply -f good.yaml"
pe "sleep 10"
p "# Empty match set -> delete allowed."
pe "kubectl -n $NS delete svc svc-good --wait=false"
wait

p "# --- GOOD #2: drain the matching Pod, then svc-bad becomes deletable ---"
pe "kubectl -n $NS delete pod web-pod --wait=true"
pe "sleep 30  # let sync inventory drop the pod"
pe "kubectl -n $NS delete svc svc-bad --wait=false"
