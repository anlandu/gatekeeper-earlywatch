#!/usr/bin/env bash
# Live walkthrough for service-pod-selector-check.
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-service-selector

clear

p "# === EarlyWatch: service-pod-selector-check ==="
p "# Scenario: editing a Service selector so it matches zero Pods quietly"
p "# black-holes traffic. This rule denies the UPDATE if no Pod in sync"
p "# data would match the new selector."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# deny mode, scoped to ns=[ew-service-selector]."
wait

p "# --- seed: svc selects app=kept; kept-pod has label app=kept ---"
pe "cat good.yaml"
pe "kubectl -n $NS apply -f good.yaml"
p "# Wait for kept-pod to land in Gatekeeper's sync cache."
pe "sleep 30"
wait

p "# --- BAD: patch the selector to app=gone (no such pod) ---"
pe "cat bad.yaml"
p "# Rego counts Pods in sync whose labels match the proposed selector;"
p "# zero -> deny. Service stays bound to app=kept."
pe "kubectl -n $NS patch svc svc --type=merge -p '{\"spec\":{\"selector\":{\"app\":\"gone\"}}}'"
pe "kubectl -n $NS get svc svc -o jsonpath='{.spec.selector}{\"\\n\"}'"
wait

p "# --- GOOD: stand up a new pod, then repoint the selector to it ---"
p "# Realistic blue/green-style cutover: deploy v2 with label app=swapped,"
p "# wait for it to sync, then update the Service selector. >0 matching"
p "# pods in sync data -> allowed."
kubectl -n $NS delete pod swapped-pod --ignore-not-found --wait=true >/dev/null 2>&1 || true
pe "kubectl -n $NS run swapped-pod --image=nginx:alpine --labels=app=swapped --restart=Never"
p "# Wait for swapped-pod to land in Gatekeeper's sync cache."
pe "sleep 30"
pe "kubectl -n $NS patch svc svc --type=merge -p '{\"spec\":{\"selector\":{\"app\":\"swapped\"}}}'"
pe "kubectl -n $NS get svc svc -o jsonpath='{.spec.selector}{\"\\n\"}'"
pe "kubectl -n $NS get endpoints svc"
