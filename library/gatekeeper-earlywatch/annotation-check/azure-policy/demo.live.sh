#!/usr/bin/env bash
# Live walkthrough for annotation-check (Azure built-in K8sRequiredAnnotations).
# Run from this directory: bash demo.live.sh
# Assumes assignment already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n -s 5

clear

p "# === EarlyWatch: annotation-check ==="
p "# Scenario: every Namespace opted into the contract must carry a"
p "# earlywatch.io/owner annotation matching a regex. Azure Policy"
p "# already has a built-in policy 'Kubernetes cluster resources should have"
p "# the specified annotations' out of the box-- no custom Rego."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# Built-in policyDefinitionId, Deny effect, kind=Namespace,"
p "# annotation key=earlywatch.io/owner with allowedRegex='.+'."
p "# labelSelector scopes us to namespaces opting in via"
p "# earlywatch.io/anno-check=true so we don't reject every kubectl"
p "# create ns in the cluster."
wait

p "# --- BAD: a Namespace that opts in but has no owner annotation ---"
pe "cat bad.yaml"
p "# CREATE is intercepted; missing annotation -> deny."
pe "kubectl apply -f bad.yaml"
pe "kubectl get ns ew-anno-gf-bad"
wait

p "# --- GOOD: opted in, owner annotation present ---"
pe "cat good.yaml"
p "# annotation matches '.+' -> allow."
pe "kubectl apply -f good.yaml"
pe "kubectl get ns ew-anno-gf-good"
