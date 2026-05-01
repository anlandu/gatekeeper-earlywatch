# ApprovalCheck threat model

## Trust boundaries

```
       ┌─────────────────────┐
write→ │ EWApprovalCheck CR  │  RBAC: update access = trust the publicKey field
       └──────────┬──────────┘
                  │ (publicKey, provider, annotationKey)
                  ▼
       ┌─────────────────────┐
       │ Gatekeeper webhook  │  Reads admission review + constraint params
       └──────────┬──────────┘
                  │ external_data(provider, key)
                  ▼ HTTPS + mTLS (caBundle)
       ┌─────────────────────┐
       │ approval-verifier   │  RSA-PSS verify, computes merge-patch
       │   (provider/)       │  Optional: trusted-keys-dir gates publicKey
       └─────────────────────┘
```

## What each component must trust

| Component | Trusts | Why |
|---|---|---|
| Operator | RBAC: `update` on `EWApprovalCheck` constraints | Anyone with this RBAC can swap the trusted public key. **Lock down.** |
| Operator | RBAC: `update` on `Secret approval-verifier-trusted-keys` | Anyone with this RBAC can add/remove trusted PEMs (mounted-secret mode). |
| Gatekeeper | Provider's `caBundle` in the `Provider` CR | TLS pin; rotate alongside the verifier cert. |
| Provider | Optionally: PEMs in `--trusted-keys-dir` | Filters which keys may be passed inline. |
| Approver | Their private key | Standard signing-key custody. |

## Defense-in-depth controls implemented

- **UID binding**: signed canonical path includes `oldObject.metadata.uid`
  so a captured signature cannot be replayed against a same-named recreated
  object.
- **Optional `notAfter`**: annotation `earlywatch.io/approval-not-after` lets
  approvers issue short-lived approvals; expired ones deny.
- **Sanitized merge-patch**: server-managed metadata (`resourceVersion`,
  `uid`, `generation`, etc.) plus the change-approval annotation itself are
  stripped before computing the signed patch — otherwise every change-by-
  Gatekeeper-mutation would invalidate signatures.
- **Canonical JSON**: provider canonicalizes the merge-patch (sorted keys)
  before verification so signers using different JSON libraries still
  produce verifiable signatures.
- **Fail-closed**: any `system_error`, `errors`, or empty `responses` from
  the provider yields a violation. Provider down ≠ admission allowed.
- **mTLS provider transport**: HTTPS with a `caBundle` pinned in the
  `Provider` CR.
- **NetworkPolicy**: only Gatekeeper pods can reach the verifier on `:8443`.

## Residual risks

| Risk | Mitigation today | Suggested follow-up |
|---|---|---|
| Constraint-author privesc | Use `--trusted-keys-dir`; tight RBAC on constraint kind | Move trusted key list out of constraint params entirely |
| Provider compromise | NetworkPolicy + readonly rootfs + nonroot | Sign provider image; admission policy on the verifier image digest |
| Replay across clusters | UID binding makes per-object replay impossible; cluster name is *not* in the canonical path | Include cluster name / API server URL in canonical path |
| Side-channel: notAfter not enforced if annotation missing | Document; enable a strict-mode flag if mandatory | Add `parameters.requireNotAfter: true` |
| JSON canonicalization of patches with floats | Subset of JCS only | Adopt full RFC 8785 if floats appear in payload |
