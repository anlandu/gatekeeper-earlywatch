# check-lock — voiceover

**1. Scenario.** A `earlywatch.io/lock="true"` annotation freezes the
object against the operations listed in the assignment (default
`["DELETE","UPDATE"]`). Useful when an SRE is investigating a hot
deployment and doesn't want the autoscaler, a teammate's `kubectl edit`,
or a GC sweep to perturb it. Lock-annotation-only edits stay allowed
so the lock can be released without a recreate.

**2. The assignment.** Plain shared scope (deny, `ew-check-lock`).
The restricted-operation set is a parameter on the policy definition
(`values.operations`) — re-publishing with `["DELETE"]` would freeze
teardown while letting scale/edit through.

**3. BAD.** `locked-gf` is created with the lock annotation, then:
- `kubectl scale` — scale is a subresource UPDATE on the parent
  Deployment. The Rego resolves the parent via sync inventory, sees
  the lock, denies. `get` confirms replicas is still 1.
- `kubectl delete` — DELETE hits the same constraint, also denied.
  `get -o name` shows the Deployment is still there.

**4. GOOD.** `unlocked-gf` has no lock annotation. Same two commands
(scale, then delete), same constraint, no annotation match — both
operations go through.
