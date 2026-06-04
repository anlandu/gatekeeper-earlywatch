# service-pod-selector-check — voiceover

**1. Scenario.** A Service whose selector matches zero Pods accepts
traffic and routes it nowhere. The API server is happy, kubectl is
happy, and 50x errors show up on the consumer side. This rule denies
the UPDATE that would orphan a Service from any matching Pod.

**2. The assignment.** Standard scope (deny, `ew-service-selector`).
The constraint reads Pod labels from sync inventory, intersects them
against the proposed new selector, denies if the intersection is empty.

**3. The seed.** Apply `good.yaml`: Service `svc` with selector
`app=kept`, Pod `kept-pod` labelled `app=kept`. The 45-second sleep
gives sync time to ingest the Pod into Gatekeeper's data cache.

**4. BAD.** Patch `svc.spec.selector` to `app=gone`. The Rego counts
Pods in the namespace whose labels satisfy the proposed selector,
finds zero, denies. `get` shows the selector is still `app=kept`.

**5. GOOD.** Re-patch the selector to `app=kept` (effectively a no-op,
but it's still an UPDATE that goes through the webhook). The
constraint sees `kept-pod` as a match, allows.
