# existing-resources (field-selector) — voiceover

**1. Scenario.** Rebuild of Brendan's original EarlyWatch
`ChangeValidator` example: deleting a Service is denied while any
Pod in the same namespace still matches the Service's own
`spec.selector`. The selector flows live off the subject
(`labelSelectorFromField: spec.selector`), so the rule itself is
zero-configuration — it works for every Service in the cluster.

**2. The assignment.** Just `operations=[DELETE]`. No labelKey /
labelValues knobs to drift over time; the selector is whatever the
Service currently advertises.

**3. The seed.** `bad.yaml` applies `svc-bad` (selector `app=webx`)
plus a Pod `web-pod` with `app=webx`. 45-second sleep lets Gatekeeper
sync ingest the Pod into inventory.

**4. BAD.** `kubectl delete svc svc-bad`. The Rego reads
`svc-bad.spec.selector`, walks Pods in `ew-existing-resources` from
sync inventory, sees `web-pod` matches, denies. The Service stays.
Message points at the offending Pod name.

**5. GOOD #1.** `svc-good` selects `app=missing`. No Pods match, so
deletion goes through immediately — proving the rule is selector-
specific, not "any pod in the namespace freezes any service" (that's
the static-selector variant next door).

**6. GOOD #2.** Drain `web-pod`, sleep so sync drops it, retry the
`svc-bad` delete — now the selector matches nothing, allowed.
