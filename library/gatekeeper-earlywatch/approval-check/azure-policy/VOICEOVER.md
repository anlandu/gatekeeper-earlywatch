# approval-check — voiceover

**1. Scenario.** DELETEs on protected ConfigMaps require a valid
signature of the canonical delete path. RBAC alone says "this user
can delete CMs"; this adds "and a holder of the signing key approved
this exact delete". Anyone with cluster edit rights still can't drop
a protected object without the key. The Azure Policy for Kubernetes addon is already actively integrating with the approvals team to make this production ready for customers across not just DELETEs, but also CREATEs and UPDATEs.

**2. The assignment.** Plain scope parameters (deny mode, scoped to
`ew-approval-check`). The real cryptographic state lives in
`definition.json` — `demo.sh` substitutes the PEM-encoded public key
into a `values.publicKey` field so the Rego can verify against it.

**3. The key.** `keys/priv.pem` signs (would live in HSM / KMS in
real life); `keys/pub.pem` is what gets baked into the policy
definition. Rotating means re-publishing the definition.

**4. The seed.** Two ConfigMaps in `ew-approval-check`. `protected-good`
has an `earlywatch.io/approved` annotation containing a real RSA-PSS-
SHA256 signature over `v1/namespaces/ew-approval-check/configmaps/protected-good`.
`protected-bad` has a junk string in the same annotation slot.

**5. BAD.** `kubectl delete cm protected-bad` — Gatekeeper recomputes
the canonical path the request is targeting, verifies the annotation
against the embedded pubkey, fails verification, returns deny. The
follow-up `get` shows the object still exists.

**6. GOOD.** `kubectl delete cm protected-good` — same code path,
signature verifies, no violation, delete proceeds. The only thing that
changed is the contents of the annotation.
