# name-reference-check — voiceover

**1. Scenario.** Deleting a ConfigMap or Secret that a workload mounts
or environments doesn't fail — the API server accepts the delete, the
running Pod is fine, and the next restart fails. This rule denies the
DELETE while any sync-known workload still references the object by
name.

**2. The assignment.** Standard scope (deny, `ew-name-reference-check`).
The cross-reference comes from Gatekeeper's sync inventory of Pods,
Deployments, StatefulSets, etc.

**3. The seed.** Apply `bad.yaml`: ConfigMap `referenced` plus
Deployment `web` whose Pod template volume-mounts it. The 45-second
sleep is the price of correctness — the constraint reads from sync
data, and sync data is eventually consistent; without the wait, the
Pod may not be in the cache yet and the deny won't fire.

**4. BAD.** `kubectl delete cm referenced`. The Rego enumerates
workloads in sync, sees `web` mounts `referenced`, denies.
`get` confirms the ConfigMap is still in etcd.

**5. GOOD.** Apply `good.yaml`: an unrelated ConfigMap `unreferenced`
that no workload mentions. Same `kubectl delete`, sync inventory has
no matching referencer, allowed.
