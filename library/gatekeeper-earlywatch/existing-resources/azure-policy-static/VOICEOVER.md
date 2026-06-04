# existing-resources (static) — voiceover

**1. Scenario.** DELETEs on Services are denied if any Pod in the
namespace carries one of a labelKey/labelValues set chosen at
assignment time (here `tier in {production, staging}`). Stops
someone from tearing down the front door of a tier-1 service even
when no obvious workload-Service binding shows up in the manifests.

**2. The assignment.** `kinds=[Service]`, `operations=[DELETE]`,
`labelKey=tier`, `labelValues=[production, staging]`. The selector is
a parameter — the same policy with a different `labelValues` covers
any team's "these tags are critical" contract.

**3. The seed.** Apply `bad.yaml`: a Service `svc-bad` plus a Pod
`prod-pod` labelled `tier=production`. 45-second sleep waits for
Gatekeeper sync to ingest the Pod.

**4. BAD.** `kubectl delete svc svc-bad`. The Rego enumerates Pods in
the namespace, finds one whose `tier` label is in the allow list,
denies. The Service stays. Note: it's not asking whether the deleted
Service is what `prod-pod` depends on — the assignment's contract is
"namespace has tier=production pods => freeze all Services in that
namespace".

**5. GOOD.** Delete `prod-pod` first, sleep so sync drops it from the
cache, then re-issue the Service delete. With no tier=production pods
left, the constraint sees zero matching Pods and allows.
