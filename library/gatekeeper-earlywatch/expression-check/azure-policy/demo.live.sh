#!/usr/bin/env bash
# Live walkthrough for expression-check.
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

clear

p "# === EarlyWatch: expression-check ==="
p "# Scenario: a platform team wants to block destructive ops on resources"
p "# whose namespace OR name matches a regex picked at assignment time."
p "# One generic policy; per-environment regex parameters."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# kinds=[ConfigMap]  operations=[DELETE]"
p "# namespacePattern=^prod.*   namePattern=^critical-.*"
p "# 'namespaces' scopes the constraint to prod-eu and ew-expression-check"
p "# so we don't trip the other rules sharing this cluster."
wait

p "# --- BAD case: cm-1 in prod-eu ---"
pe "cat bad.yaml"
pe "kubectl apply -f bad.yaml"
p "# 'prod-eu' matches ^prod.* -> DELETE is rejected at admission."
p "# Object stays in etcd; error message names the constraint."
pe "kubectl delete cm cm-1 -n prod-eu"
pe "kubectl get cm cm-1 -n prod-eu"
wait

p "# --- GOOD case: cm-ok in ew-expression-check ---"
pe "cat good.yaml"
pe "kubectl apply -f good.yaml"
p "# Namespace doesn't match ^prod.*, name doesn't match ^critical-.*"
p "# -> neither half of the OR fires, DELETE goes through."
pe "kubectl delete cm cm-ok -n ew-expression-check"
