# EarlyWatch on Gatekeeper

The [EarlyWatch](https://github.com/brendandburns/early-watch) admission
validators, implemented as Gatekeeper `ConstraintTemplate`s + supporting
infrastructure.

The goal is **same functionality, in Gatekeeper** — not API compatibility
with upstream EarlyWatch. Operators author Gatekeeper `Constraint`s; signing
keys live in a Secret; signature verification runs in a small external-data
provider; everything plugs into Gatekeeper's audit, dryrun, metrics, and
testing pipeline.

## What's here

```
00-config-sync.yaml          Gatekeeper Config: which kinds get synced into data.inventory
01..09-*.yaml                ConstraintTemplate + Constraint pairs. Templates carry
                             both `code: [{engine: Rego}]` and (where feasible)
                             `code: [{engine: K8sNativeValidation}]` so Gatekeeper
                             picks the CEL path when VAP is enabled, Rego otherwise.
provider/                    External-data provider for ApprovalCheck (Go service)
touch-monitor/               CronJob that produces ManualTouchEvent CRs
tests/                       gator verify suite + AdmissionReview/inventory fixtures
docs/                        Threat model, GitOps notes
watchctl/                    CLI for signing approvals (ApprovalCheck)
kustomization.yaml           Single-entrypoint apply order
```

## Validators

| # | Behavior                                                              | Template                                       | CEL? |
|---|-----------------------------------------------------------------------|------------------------------------------------|------|
| 1 | Deny when dependent resources still exist                             | `policy/01-existing-resources-template.yaml`          | — referential |
| 2 | Deny when other resources still reference this one by name            | `policy/02-name-reference-check-template.yaml`        | — referential |
| 3 | Require an annotation (optionally with a specific value)              | `policy/03-annotation-check-template.yaml`            | ✅   |
| 4 | Require a valid RSA-PSS signed approval annotation                    | `policy/04-approval-check-template.yaml`              | — needs crypto |
| 5 | Honor a lock annotation that blocks delete (optionally update)        | `policy/05-check-lock-template.yaml`                  | ✅   |
| 6 | Deny based on a CEL expression (parametric predicate set)             | `policy/06-expression-check-template.yaml`            | ✅   |
| 6 | Deny based on a literal CEL expression (one template per expression)  | `policy/06-expression-check-real-cel-template.yaml`   | ✅ CEL-only |
| 7 | Require a recent `ManualTouchEvent` CR before allowing the change     | `policy/07-manual-touch-check-template.yaml`          | — referential |
| 7 | Annotation-based recent-touch check (no inventory)                    | `policy/07-manual-touch-check-cel-template.yaml`      | ✅ CEL-only |
| 8 | Deny Service updates that orphan all matching Pods                    | `policy/08-service-pod-selector-check-template.yaml`  | — referential |
| 9 | Deny ConfigMap/Secret updates that drop a key still in use            | `policy/09-data-key-safety-check-template.yaml`       | — referential |

## Quick start

```bash
helm install gatekeeper gatekeeper/gatekeeper -n gatekeeper-system --create-namespace
kubectl apply -k earlywatch-gatekeeper/

# Run the test suite. Gatekeeper picks CEL when VAP is enabled, Rego otherwise.
go run ./cmd/gator verify ./earlywatch-gatekeeper/tests/...
```

## Design choices

- **One Constraint per rule.** Gatekeeper's native model. Each validator is a
  `ConstraintTemplate` you parameterize per use case via a `Constraint`.
- **Referential checks read `data.inventory`.** Each referential template
  carries a `metadata.gatekeeper.sh/requires-sync-data` annotation listing
  the GVKs that must be synced via `policy/00-config-sync.yaml`.
- **ApprovalCheck delegates crypto to an external-data Provider.** Pure Rego
  / CEL cannot do RSA-PSS or canonical merge-patch computation. The provider
  in `provider/` is a small distroless HTTPS service that runs alongside
  Gatekeeper. Trusted public keys come from a mounted Secret
  (`--trusted-keys-dir`), not from constraint parameters.
- **Signed canonical path includes `metadata.uid`.** A captured signature
  cannot be replayed against a recreated namesake. See
  [docs/threat-model.md](docs/threat-model.md).
- **`requireValue` boolean on AnnotationCheck.** Explicit "key must be
  present" vs "key must equal this value." Cleaner than nil-vs-empty
  string handling.
- **ExpressionCheck offers two flavors.** A parametric Rego predicate set
  (operationIn / namespaceIn / regex) for common patterns, and a true-CEL
  one-template-per-expression pattern for the cases the predicate set can't
  express. CEL has no `eval(string)`, so a fully parametric CEL version is
  not possible.
- **`GuardRule.message` is not a parameter today.** Templates produce a
  default denial message; to customize, fork the template.

## Operational features Gatekeeper provides

- Per-constraint `enforcementAction: dryrun | warn | deny` for safe rollout.
  See `policy/05-check-lock-constraint-dryrun.yaml`.
- Audit pod populates `Constraint.status.violations[]`.
- Prometheus metrics for constraint evaluations.
- `gator verify` unit tests with AdmissionReview + inventory fixtures
  (full suite under `tests/`).
- Mutation webhooks if you want to *fix* instead of *reject*.
