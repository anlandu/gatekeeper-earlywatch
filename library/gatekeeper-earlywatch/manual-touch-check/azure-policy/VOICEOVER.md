# manual-touch-check — voiceover

**1. Scenario.** GitOps clusters drift when someone `kubectl edit`s an
object out-of-band. This rule cooperates with a side-channel
("touch-monitor") that watches apiserver audit events: within N
seconds of a manual UPDATE, further UPDATEs to the same object are
denied. The deny window forces the reconciler to bring the object
back to its declared state before any more changes land.

**2. The assignment.** Standard scope. The interesting part is what
the rule reads — not the object itself, but a Gatekeeper external-data
Provider (`manual-touch-provider`) served by touch-monitor, which
keeps an in-memory cache of recent audit events.

**3. The seed.** Two Deployments, `touched-gf` and `untouched-gf`,
otherwise identical. Apply both.

**4. The synthetic touch.** In production a Kubernetes audit webhook
feeds the touch-monitor. For the demo we port-forward to the monitor
and POST a fabricated audit record marking `touched-gf` as manually
updated by `alice`. Then we kill the port-forward; the monitor holds
the record in its cache, keyed by group/resource/ns/name.

**5. BAD.** `kubectl annotate` is an UPDATE. The Rego builds a
`manualtouch|...` key and calls the external-data Provider; the
provider answers `touched`, the constraint denies.

**6. GOOD.** Same `kubectl annotate` against `untouched-gf`. Provider
responds `untouched`, no violation. The only difference is which
object the audit event named.
