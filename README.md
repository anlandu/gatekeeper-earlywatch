# EarlyWatch on Azure Policy + Gatekeeper

The [EarlyWatch](https://github.com/brendandburns/early-watch) admission
validators, implemented as Azure Policies wrapping Gatekeeper `ConstraintTemplate`s + supporting
infrastructure.

The goal is **same functionality, in Azure Policy** — not API compatibility
with upstream EarlyWatch. Operators author Azure Policy definitions and assignments; approval
public keys are supplied in `EWApprovalCheck` policy definition (or parameterized in the policy assignment) and can be further
restricted by a provider-mounted trusted-key Secret; signature verification
runs in a small external-data provider; everything plugs into Gatekeeper's
audit, dryrun, metrics, and testing pipeline -> compliance results get surfaced via Azure Policy compliance portal.

## What's here

```
library/gatekeeper-earlywatch/  Library with templates, Azure policy definition/assignments, and demo script.
catalog.yaml                    Gatekeeper PolicyCatalog with an EarlyWatch default policy bundle
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

The table below lists the directory for each EarlyWatch validator equivalent. Each scenario directory has the following structure:

```
<scenario>/
├── template.yaml          ConstraintTemplate (Rego and/or CEL/K8sNativeValidation)
├── constraint.yaml        Constraint binding the template to a scope (Azure Policy addon automatically creates)
├── kustomization.yaml     Wires template + constraint(s) into the parity stack
└── azure-policy/
    ├── definition.json    Custom Azure Policy definition
    ├── assignment.json    Custom Azure Policy assignment with working default parameters
    ├── good.yaml          Sample resource that should PASS the policy
    ├── bad.yaml           Sample resource that should be DENIED by the policy
    └── demo.live.sh       Scripted live walkthrough against a real cluster
```

| # | Behavior                                                              | Scenario Directory                                       | Native CEL? |
|---|-----------------------------------------------------------------------|------------------------------------------------|-------------|
| 1 | Deny when dependent resources still exist                             | [existing-resources](/library/gatekeeper-earlywatch/existing-resources)          | No — needs inventory lookups |
| 2 | Deny when other resources still reference this one by name            | [name-reference-check](/library/gatekeeper-earlywatch/name-reference-check)        | No — needs inventory lookups |
| 3 | Require an annotation (optionally with a specific value)              | [annotation-check](/library/gatekeeper-earlywatch/annotation-check)            | Yes |
| 4 | Require a valid RSA-PSS signed approval annotation                    | [approval-check](/library/gatekeeper-earlywatch/approval-check)              | No — needs external data |
| 5 | Honor a lock annotation that blocks delete (optionally update)        | [check-lock](/library/gatekeeper-earlywatch/check-lock)                  | No — needs inventory |
| 6 | Deny based on a translated EarlyWatch ExpressionCheck rule (example)  | [expression-check](/library/gatekeeper-earlywatch/expression-check)            | Yes |
| 7 | Deny when touch-monitor reports a recent manual touch                 | [manual-touch-check](/library/gatekeeper-earlywatch/manual-touch-check)          | No — needs external data provider |
| 8 | Deny Service updates that orphan all matching Pods                    | [service-pod-selector-check](/library/gatekeeper-earlywatch/service-pod-selector-check)  | No — needs inventory lookups |
| 9 | Deny ConfigMap/Secret updates that drop a key still in use            | [data-key-safety-check](/library/gatekeeper-earlywatch/data-key-safety-check)       | No — needs inventory lookups |

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
- **ExpressionCheck is one tiny template per rule, not a parametric DSL.**
  EarlyWatch's ExpressionCheck grammar is `field == 'value'` over
  `operation`/`namespace`/`name` and is strictly less expressive than CEL.
  Neither CEL nor Rego can `eval()` a string supplied as a constraint
  parameter, so a "generic" template would have to re-implement an expression
  evaluator and would still be weaker than just writing CEL. Instead, each
  EarlyWatch ExpressionCheck rule is translated into a dedicated
  `ConstraintTemplate` whose predicate is a one-line CEL `expression`. The
  canonical example is in
  [library/gatekeeper-earlywatch/expression-check/](library/gatekeeper-earlywatch/expression-check/).
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

This repository includes a Gatekeeper `PolicyCatalog` in [`catalog.yaml`](catalog.yaml)
so Gatekeeper tooling can discover the EarlyWatch parity templates and install
the preconfigured `earlywatch-default` policy bundle.

To install the cataloged `ConstraintTemplate`s and default `Constraint`s with
`gator`:

```bash
export GATOR_CATALOG_URL=https://raw.githubusercontent.com/sozercan/gatekeeper-earlywatch/main/catalog.yaml
gator policy update
gator policy install --bundle earlywatch-default
```

You can override the default constraints' enforcement action during bundle
installation:

```bash
gator policy install --bundle earlywatch-default --enforcement-action=warn
```

The bundle includes one catalog-only alias, `ewexistingresources-static`, so
Gator can install both default constraints for `EWExistingResources` (the
field-selector and the static-selector variants).

The installable policy templates live in the Gatekeeper Library-compatible layout
under [`library/gatekeeper-earlywatch/`](library/gatekeeper-earlywatch/). Gator
policy bundles install policy objects only: `ConstraintTemplate`s and the
bundle-specific `Constraint`s. They do **not** install the non-policy runtime
resources required by the full EarlyWatch parity stack, such as Gatekeeper
inventory sync `Config`, external-data `Provider`s, provider `Deployment`s and
Services, or touch-monitor resources. Use the root
[`kustomization.yaml`](kustomization.yaml) when you want the complete default
parity stack:

```bash
kubectl apply -k .
```

Copy individual templates and constraints from `library/` when you need a
narrower rollout.
