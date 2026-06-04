#!/usr/bin/env bash
# Live walkthrough for data-key-safety-check.
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-datakey

clear

p "# === EarlyWatch: data-key-safety-check ==="
p "# Scenario: dropping a ConfigMap key while a Pod still references it"
p "# silently breaks the Pod at next restart. This rule denies the UPDATE."
p "# Cross-object: needs sync inventory to see who references whom."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# deny mode, scoped to ns=[ew-datakey]. The rule walks sync inventory"
p "# for Pods that referenced the old key set via configMapKeyRef."
wait

p "# --- seed: ConfigMap shared-gf with K1+K2, Pod refs-gf bound to K1 ---"
pe "cat good.yaml"
pe "kubectl -n $NS apply -f good.yaml"
p "# (give Gatekeeper sync a moment to ingest the Pod's env refs)"
pe "sleep 30"
wait

p "# --- BAD: re-apply shared-gf with K1 dropped ---"
pe "cat bad.yaml"
p "# UPDATE on the CM; Rego sees refs-gf in sync data with"
p "# configMapKeyRef -> shared-gf/K1; K1 is no longer present -> deny."
pe "kubectl -n $NS apply -f bad.yaml"
pe "kubectl -n $NS get cm shared-gf -o jsonpath='{.data}{\"\\n\"}'"
wait

p "# --- GOOD: re-apply the original CM keeping K1 ---"
p "# K1 still present, no referencing pod is broken -> UPDATE allowed."
pe "kubectl -n $NS apply -f good.yaml"
pe "kubectl -n $NS get cm shared-gf -o jsonpath='{.data}{\"\\n\"}'"
