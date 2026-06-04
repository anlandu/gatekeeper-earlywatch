#!/usr/bin/env bash
# Live walkthrough for check-lock.
# Run from this directory: bash demo.live.sh
# Assumes policy + constraint already deployed (run demo.sh once).

bash ../../../../scripts/bootstrap-demo-magic.sh >/dev/null
. ../../../../scripts/demo-magic.sh -n

NS=ew-check-lock

clear

p "# === EarlyWatch: check-lock ==="
p "# Scenario: a Deployment annotated earlywatch.io/lock=\"true\" is frozen."
p "# Operations in the assignment's 'operations' list (default DELETE+UPDATE)"
p "# are denied while the lock is on. Lock-only annotation edits are still"
p "# allowed so an operator can take the lock off without bouncing the object."
wait

p "# --- the assignment parameters ---"
pe "cat assignment.json"
p "# deny mode, scoped to ns=[ew-check-lock]. The restricted operation set"
p "# lives in the policy definition's 'values.operations' (DELETE+UPDATE);"
p "# an operator could re-publish with values.operations=[\"DELETE\"] to"
p "# allow scale/edit while still preventing teardown."
wait

p "# --- BAD: locked-gf is created with the lock annotation ---"
pe "cat bad.yaml"
pe "kubectl -n $NS apply -f bad.yaml"
p "# Scaling triggers an UPDATE on the Deployment's scale subresource."
p "# Gatekeeper resolves the parent Deployment from sync inventory, sees"
p "# the lock annotation, denies."
pe "kubectl -n $NS scale deployment locked-gf --replicas=2"
pe "kubectl -n $NS get deploy locked-gf -o jsonpath='{.spec.replicas}{\"\\n\"}'"
p "# DELETE goes through the same constraint -- denied while locked."
pe "kubectl -n $NS delete deploy locked-gf --wait=false"
pe "kubectl -n $NS get deploy locked-gf -o name"
wait

p "# --- GOOD: unlocked-gf has no lock annotation ---"
pe "cat good.yaml"
pe "kubectl -n $NS apply -f good.yaml"
p "# No lock annotation -> UPDATE allowed."
pe "kubectl -n $NS scale deployment unlocked-gf --replicas=2"
pe "kubectl -n $NS get deploy unlocked-gf -o jsonpath='{.spec.replicas}{\"\\n\"}'"
p "# DELETE on the same unlocked object: allowed."
pe "kubectl -n $NS delete deploy unlocked-gf --wait=false"
