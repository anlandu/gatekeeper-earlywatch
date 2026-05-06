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
library/gatekeeper-earlywatch/  Gatekeeper Library layout with templates, default constraints, and Config sync
catalog.yaml                    gator-generated PolicyCatalog for the library templates
provider/                       External-data provider for ApprovalCheck (Go service)
touch-monitor/                  Audit-webhook service + ManualTouch external-data provider
tests/                          gator verify suite + AdmissionReview/inventory fixtures
docs/                           Threat model, GitOps notes, parity matrix
watchctl/                       CLI for signing approvals (ApprovalCheck)
kustomization.yaml              Single-entrypoint default parity stack
```

Core templates use Rego everywhere and add Gatekeeper `K8sNativeValidation`
(CEL) only where the behavior is feasible without inventory, external data,
crypto, or audit-event ingestion. See
[docs/earlywatch-parity.md](docs/earlywatch-parity.md) for the per-check Rego
/ CEL coverage matrix.

## Default parity validators

These templates and their default constraints are included in `kustomization.yaml`:

| # | Behavior                                                              | Template                                       | Native CEL? |
|---|-----------------------------------------------------------------------|------------------------------------------------|-------------|
| 1 | Deny when dependent resources still exist                             | `library/gatekeeper-earlywatch/existing-resources/template.yaml`          | No — needs inventory lookups |
| 2 | Deny when other resources still reference this one by name            | `library/gatekeeper-earlywatch/name-reference-check/template.yaml`        | No — needs inventory lookups |
| 3 | Require an annotation (optionally with a specific value)              | `library/gatekeeper-earlywatch/annotation-check/template.yaml`            | Yes |
| 4 | Require a valid RSA-PSS signed approval annotation                    | `library/gatekeeper-earlywatch/approval-check/template.yaml`              | No — needs crypto/provider |
| 5 | Honor a lock annotation that blocks delete (optionally update)        | `library/gatekeeper-earlywatch/check-lock/template.yaml`                  | No — compares old/new objects in Rego |
| 6 | Deny based on structured expression predicates                        | `library/gatekeeper-earlywatch/expression-check/template.yaml`            | Yes |
| 7 | Deny when touch-monitor reports a recent manual touch                 | `library/gatekeeper-earlywatch/manual-touch-check/template.yaml`          | No — needs external-data provider |
| 8 | Deny Service updates that orphan all matching Pods                    | `library/gatekeeper-earlywatch/service-pod-selector-check/template.yaml`  | No — needs inventory lookups |
| 9 | Deny ConfigMap/Secret updates that drop a key still in use            | `library/gatekeeper-earlywatch/data-key-safety-check/template.yaml`       | No — needs inventory lookups |

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
  must be synced via `library/gatekeeper-earlywatch/config-sync.yaml`. `EWManualTouchCheck` is the
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
- **ExpressionCheck uses structured predicates.** `EWExpressionCheck` exposes a
  portable structured schema (`operationIn`, `namespaceIn`, `namespaceRegex`,
  `nameIn`, `nameRegex`) with both Rego and native CEL implementations for
  common EarlyWatch predicates. Gatekeeper cannot dynamically `eval()` a CEL
  expression passed as a constraint parameter; custom arbitrary CEL should live
  in separately authored `ConstraintTemplate`s outside the default parity stack.
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
  See `library/gatekeeper-earlywatch/check-lock/constraint-dryrun.yaml`.
- Audit pod populates `Constraint.status.violations[]`.
- Prometheus metrics for constraint evaluations.
- `gator verify` unit tests with AdmissionReview + inventory fixtures
  (full suite under `tests/`).
- Mutation webhooks if you want to *fix* instead of *reject*.

## Policy catalog

This repository includes a Gatekeeper `PolicyCatalog` in [`catalog.yaml`](catalog.yaml),
generated with `gator policy generate-catalog`.

The canonical policy source lives in the Gatekeeper Library-compatible layout
under [`library/`](library/). [`scripts/generate-catalog.sh`](scripts/generate-catalog.sh)
runs the `gator` CLI directly against that layout.

To regenerate the catalog:

```bash
./scripts/generate-catalog.sh
```

Optional overrides:

```bash
CATALOG_NAME=gatekeeper-earlywatch \
CATALOG_VERSION=v0.1.0 \
CATALOG_BASE_URL=https://raw.githubusercontent.com/sozercan/gatekeeper-earlywatch/main \
CATALOG_REPOSITORY=https://github.com/sozercan/gatekeeper-earlywatch \
./scripts/generate-catalog.sh
```
