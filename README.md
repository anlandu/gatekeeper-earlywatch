# EarlyWatch on Gatekeeper

The [EarlyWatch](https://github.com/brendandburns/early-watch) admission
validators, implemented as Gatekeeper `ConstraintTemplate`s + supporting
infrastructure.

The goal is **same functionality, in Gatekeeper** — not API compatibility
with upstream EarlyWatch. Operators author Gatekeeper `Constraint`s; approval
public keys are supplied on `EWApprovalCheck` constraints and can be further
restricted by a provider-mounted trusted-key Secret; signature verification
runs in a small external-data provider; everything plugs into Gatekeeper's
audit, dryrun, metrics, and testing pipeline.

## What's here

```
00-config-sync.yaml          Gatekeeper Config: which kinds get synced into data.inventory
01..09-*.yaml                Core parity ConstraintTemplate + Constraint pairs
provider/                    External-data provider for ApprovalCheck (Go service)
touch-monitor/               Audit-webhook service + ManualTouch external-data provider
tests/                       gator verify suite + AdmissionReview/inventory fixtures
docs/                        Threat model, GitOps notes, parity matrix
watchctl/                    CLI for signing approvals (ApprovalCheck)
kustomization.yaml           Single-entrypoint default parity stack
```

Core templates use Rego everywhere and add Gatekeeper `K8sNativeValidation`
(CEL) only where the behavior is feasible without inventory, external data,
crypto, or audit-event ingestion. See
[docs/earlywatch-parity.md](docs/earlywatch-parity.md) for the per-check Rego
/ CEL coverage matrix.

## Default parity validators

These templates and their default constraints are included in `kustomization.yaml`:

| # | Behavior                                                              | Template                                       | CEL? |
|---|-----------------------------------------------------------------------|------------------------------------------------|------|
| 1 | Deny when dependent resources still exist                             | `policy/01-existing-resources-template.yaml`          | — referential |
| 2 | Deny when other resources still reference this one by name            | `policy/02-name-reference-check-template.yaml`        | — referential |
| 3 | Require an annotation (optionally with a specific value)              | `policy/03-annotation-check-template.yaml`            | ✅ |
| 4 | Require a valid RSA-PSS signed approval annotation                    | `policy/04-approval-check-template.yaml`              | — needs crypto/provider |
| 5 | Honor a lock annotation that blocks delete (optionally update)        | `policy/05-check-lock-template.yaml`                  | — Rego diff |
| 6 | Deny based on structured expression predicates                        | `policy/06-expression-check-template.yaml`            | ✅ |
| 7 | Deny when touch-monitor reports a recent manual touch                 | `policy/07-manual-touch-check-template.yaml`          | — external-data/provider |
| 8 | Deny Service updates that orphan all matching Pods                    | `policy/08-service-pod-selector-check-template.yaml`  | — referential |
| 9 | Deny ConfigMap/Secret updates that drop a key still in use            | `policy/09-data-key-safety-check-template.yaml`       | — referential |

## Optional examples not applied by default

These files remain useful examples/tests, but are intentionally excluded from
`kustomization.yaml` so the default stack contains only parity-critical
resources:

| File(s) | Purpose |
|---|---|
| `policy/06-expression-check-real-cel-template.yaml`, `policy/06-expression-check-real-cel-constraint.yaml` | Optional authored native-CEL ExpressionCheck example. Use this pattern when an arbitrary CEL expression must be compiled into a dedicated Gatekeeper template. |
| `policy/07-manual-touch-check-cel-template.yaml`, `policy/07-manual-touch-check-cel-constraint.yaml` | Optional annotation/timestamp Rego variant. Despite the filename, it is not native-CEL enforcement and is not the EarlyWatch ManualTouch parity path. |

## Quick start

```bash
helm install gatekeeper gatekeeper/gatekeeper -n gatekeeper-system --create-namespace
kubectl apply -k .

# On a fresh Gatekeeper install, ConstraintTemplates create their constraint
# CRDs asynchronously; if constraint resources race those CRDs, wait for the
# templates to report status.created=true and re-run the same apply.
kubectl wait --for=jsonpath='{.status.created}'=true constrainttemplates.templates.gatekeeper.sh --all --timeout=120s
kubectl apply -k .

# Run the offline Gatekeeper test suite from the repo root.
gator verify tests/suite.yaml
```

## Design choices

- **Reusable templates, many constraints.** Each validator type is a
  `ConstraintTemplate`; operators can create as many `Constraint`s as needed
  from that template, each with its own match scope, parameters, and
  `enforcementAction`.
- **Referential checks read `data.inventory`.** Referential templates carry a
  `metadata.gatekeeper.sh/requires-sync-data` annotation listing the GVKs that
  must be synced via `policy/00-config-sync.yaml`. `EWManualTouchCheck` is the
  exception: it queries a Gatekeeper external-data provider backed by
  `touch-monitor/`'s in-memory audit-event cache.
- **ApprovalCheck delegates crypto to an external-data Provider.** Pure Rego
  / CEL cannot do RSA-PSS or canonical merge-patch computation. The provider
  in `provider/` is a small distroless HTTPS service that runs alongside
  Gatekeeper. To preserve Gatekeeper-native constraints, the EarlyWatch public
  key is passed as an `EWApprovalCheck` parameter; for stricter RBAC separation
  the provider can also require that key to appear in a mounted trusted-key
  Secret via `--trusted-keys-dir`.
- **ManualTouchMonitor is implemented as an audit-webhook service and
  external-data provider.** Gatekeeper policies cannot natively consume
  Kubernetes audit webhook events, so `touch-monitor/` receives audit
  `EventList` batches at `/audit`, matches `ManualTouchMonitor` CRs, stores
  matching touches in memory, and serves `EWManualTouchCheck` lookups at
  `/validate-manual-touch`. Creating EarlyWatch-compatible `ManualTouchEvent`
  CRs is optional (`--record-events`) for compatibility. API-server audit
  webhook configuration remains an out-of-band cluster-admin step; see
  [touch-monitor/README.md](touch-monitor/README.md).
- **ApprovalCheck uses upstream-compatible signing payloads.** DELETE approvals
  sign EarlyWatch `ResourcePath` strings (no UID), while UPDATE approvals sign
  the upstream-normalized RFC 7396 merge patch after stripping status,
  server-managed metadata, and the change-approval annotation. See
  [docs/threat-model.md](docs/threat-model.md).
- **`requireValue` boolean on AnnotationCheck.** Explicit "key must be
  present" vs "key must equal this value." Cleaner than nil-vs-empty
  string handling.
- **ExpressionCheck has two supported forms.** `EWExpressionCheck` exposes a
  portable structured schema (`operationIn`, `namespaceIn`, `namespaceRegex`,
  `nameIn`, `nameRegex`) with both Rego and native CEL implementations for
  common predicates. For arbitrary CEL, author one native-CEL
  `ConstraintTemplate` per expression, as shown by
  `policy/06-expression-check-real-cel-template.yaml`. Gatekeeper cannot
  dynamically `eval()` a CEL expression passed as a constraint parameter, so
  arbitrary expressions live in templates instead of runtime strings.
- **`GuardRule.message` is not a parameter today.** Templates produce a
  default denial message; to customize, fork the template.

## EarlyWatch parity

The current parity matrix, evidence, and known limitations are tracked in
[docs/earlywatch-parity.md](docs/earlywatch-parity.md). That document also
summarizes the 2026-05-05 live validation run against the `kind-kind` cluster.
Two important operator notes:

- For ApprovalCheck DELETE parity, Gatekeeper's validating webhook must include
  the `DELETE` operation (some installs default to CREATE/UPDATE only).
- For ManualTouchMonitor parity, configure the Kubernetes API server audit
  webhook backend to deliver `ResponseComplete` audit batches to
  `manual-touch-monitor`'s `/audit` endpoint, configure the
  `manual-touch-provider` CA bundle so Gatekeeper can call
  `/validate-manual-touch`, and disable Gatekeeper's external-data provider
  response cache with `--external-data-provider-response-cache-ttl=0s`.
  Manual-touch lookups are time-windowed and mutable; a cached `untouched`
  response can otherwise allow an update after a matching audit touch.

## Operational features Gatekeeper provides

- Per-constraint `enforcementAction: dryrun | warn | deny` for safe rollout.
  See `policy/05-check-lock-constraint-dryrun.yaml`.
- Audit pod populates `Constraint.status.violations[]`.
- Prometheus metrics for constraint evaluations.
- `gator verify` unit tests with AdmissionReview + inventory fixtures
  (full suite under `tests/`).
- Mutation webhooks if you want to *fix* instead of *reject*.
