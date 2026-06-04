# data-key-safety-check — voiceover

**1. Scenario.** A ConfigMap key referenced by a Pod via
`configMapKeyRef` can be silently dropped — kubectl reports success,
the Pod's next restart fails with `CreateContainerConfigError`. This
rule denies the UPDATE that would orphan a referenced key. Requires
cross-object knowledge: the CM being mutated has to be cross-checked
against every Pod in the namespace. Gatekeeper caches this cross-object
knowledge to prevent the overhead of every admission request requiring a
fresh API server call to list all pods on the cluster.

**2. The assignment.** Plain scope (deny, `ew-datakey`). The
interesting machinery is in `definition.json`: Gatekeeper's sync
controller keeps a live cache of Pods, and the Rego walks
`data.inventory.namespace[ns].v1.Pod[*]` to enumerate `configMapKeyRef`
entries pointing at the CM under review.

**3. The seed.** Apply `good.yaml`: ConfigMap `shared-gf` with K1+K2,
Pod `refs-gf` whose env uses `configMapKeyRef: {name: shared-gf, key:
K1}`. A 30-second sleep gives the sync controller time to ingest the
Pod into Gatekeeper's data cache — without that, the constraint sees
an empty inventory and would allow the bad case.

**4. BAD.** Re-apply `shared-gf` with K1 removed. The constraint sees
`refs-gf` in sync data, references K1, K1 absent in the new spec ->
deny. The `get` shows K1+K2 still intact in etcd.

**5. GOOD.** Re-apply the original `good.yaml` (K1+K2). No referenced
key is being dropped, the constraint never fires, UPDATE proceeds.
