# EarlyWatch parity matrix

Last updated: 2026-05-05.

This project targets **functional parity with upstream EarlyWatch behavior using
Gatekeeper**, not CRD/API compatibility with EarlyWatch
`ChangeValidator` / `ClusterChangeValidator`. Operators express policy through
Gatekeeper `ConstraintTemplate`s, `Constraint`s, `Provider`s, and
`Constraint.spec.match`. Supporting Go services are used only where Gatekeeper
policy engines cannot perform the behavior directly.

Upstream reference audited for this pass:
`brendandburns/early-watch@55fe022d6190f93474c7a15cb3408e6540160f5d`.

The live CI parity job pins Gatekeeper to Helm chart `gatekeeper-3.22.2`
(app version `v3.22.x`). The same job also deploys upstream EarlyWatch from the
audited commit above with locally built webhook and audit-monitor images before
installing this repository's Gatekeeper implementation. That upstream
EarlyWatch installation is evidence that CI exercised the audited reference
revision, not a claim that gatekeeper-earlywatch provides EarlyWatch CRD/API compatibility.

## CI parity validation and artifacts

`.github/workflows/ci.yaml` and `scripts/ci-kind-parity.sh` are the canonical
parity gate. The job performs three layers of validation:

1. **Static implementation checks:** Go tests for `provider/`,
   `touch-monitor/`, and `watchctl/`; `gator verify tests/suite.yaml`; and
   `kubectl kustomize .`. Outputs are captured under
   `.ci-artifacts/static/`, including the rendered default stack and
   kustomize shape counts.
2. **Coverage catalog checks:** `write_parity_coverage_artifact` asserts that
   `tests/suite.yaml` still contains the expected Gator scenarios/cases and
   that the ApprovalCheck and ManualTouch provider Go tests still cover the
   required upstream-parity cases. The evidence file is
   `.ci-artifacts/coverage/parity-coverage.json`.
3. **Live kind parity checks:** the script creates a kind cluster, deploys
   upstream EarlyWatch from
   `55fe022d6190f93474c7a15cb3408e6540160f5d`, installs Gatekeeper chart
   `3.22.2`, enables external data with response caching disabled, applies this
   repository's default stack, and runs server-side dry-run admission assertions
   for every implemented EarlyWatch capability. Deployment/version evidence is
   written to `.ci-artifacts/upstream-earlywatch.json`,
   `.ci-artifacts/upstream-earlywatch-validatingwebhook.yaml`,
   `.ci-artifacts/upstream-earlywatch-workloads.txt`,
   `.ci-artifacts/gatekeeper-helm-version.json`, and
   `.ci-artifacts/assertions/`.

## Engine and evidence legend

- **Rego behavior** describes the default parity implementation in this repo.
- **CEL behavior** describes Gatekeeper `K8sNativeValidation` coverage where it
  is feasible. Most referential, crypto, and audit-driven checks are not
  implemented in CEL because native CEL cannot read `data.inventory`, call
  `external_data()`, verify RSA-PSS signatures, or ingest Kubernetes audit
  events.
- `gator verify tests/suite.yaml` validates the offline Gatekeeper fixtures. It
  intentionally does **not** validate `EWManualTouchCheck` because `gator` does
  not provide the Gatekeeper `external_data()` builtin; that path is covered by
  provider Go tests and the live kind parity script.
- `tests/manualtouch/*` are legacy/illustrative fixtures from the old
  inventory-based approach unless they are wired into a live/provider test path.
  The real ManualTouch parity path is
  `policy/07-manual-touch-check-template.yaml` +
  `policy/07-manual-touch-provider.yaml` + `touch-monitor/`.

## Per-scenario parity matrix

| EarlyWatch behavior | Gatekeeper Rego behavior | Gatekeeper CEL behavior | Gaps / intentional non-parity | Evidence |
|---|---|---|---|---|
| **ClusterChangeValidator / ChangeValidator attachment and scoping.** EarlyWatch attaches checks to matched resources, namespaces, labels, and operations through its validator CRDs. | Gatekeeper uses `Constraint.spec.match` for kind, namespace, excluded namespace, scope, object label, and namespace label scoping. Each reusable template also accepts an `operations` parameter where EarlyWatch would have run a guard only for selected admission operations. | Not a custom CEL check. Gatekeeper match filtering happens before Rego or native-CEL policy evaluation; templates that include CEL receive the same matched admissions. | Intentional API non-parity: there are no EarlyWatch validator CRDs. Equivalent behavior is represented as Gatekeeper constraints. EarlyWatch-specific status/ordering APIs are replaced by Gatekeeper status, audit, dryrun/warn/deny, and metrics. | Default constraints under `policy/*-constraint.yaml`; `gator verify tests/suite.yaml`; `kubectl kustomize .` emits the default stack. |
| **ExistingResourcesCheck.** Deny a requested operation when existing dependent resources still match the configured selector. Supports selector from a subject field, static `metav1.LabelSelector`, no-selector/empty-selector match-all semantics, same-namespace defaulting, and Namespace delete scoping. | `policy/01-existing-resources-template.yaml` (`EWExistingResources`) reads synced objects from `data.inventory`, defaults `version` to `v1`, defaults `sameNamespace` to true, gives `labelSelectorFromField` precedence over static selectors, implements `matchLabels` / `In` / `NotIn` / `Exists` / `DoesNotExist`, and treats a Namespace DELETE as a search inside the namespace being deleted. | No CEL parity path. Native CEL cannot query arbitrary synced cluster inventory. | Requires every dependent GVK used by constraints to be synced in `policy/00-config-sync.yaml`. This is Gatekeeper referential-policy setup, not EarlyWatch API compatibility. | `tests/existing-resources/*`; `tests/existing-resources-static/*`; `tests/existing-resources-parity/*`; `gator verify tests/suite.yaml`. |
| **NameReferenceCheck.** Deny an operation, typically delete, when configured referrer resources still name the target resource through one of the configured object paths. Paths can cross arrays. | `policy/02-name-reference-check-template.yaml` (`EWNameReferenceCheck`) scans configured referrer kinds in `data.inventory`, defaults omitted referrer version to `v1`, defaults `sameNamespace` to true, and uses `walk()` with array indices stripped so dot paths such as volume or env references can be matched inside arrays. | No CEL parity path because the check is referential and needs `data.inventory`. | Requires synced referrer kinds in `policy/00-config-sync.yaml`. Cross-namespace mode is represented by template parameters plus Gatekeeper constraints, not EarlyWatch CRDs. | `tests/name-reference/*`; `tests/name-reference/default-version-constraint.yaml`; `gator verify tests/suite.yaml`. |
| **AnnotationCheck.** Require an annotation key, optionally requiring an exact value. EarlyWatch models the optional value as a pointer, so absent value means “any value is acceptable.” | `policy/03-annotation-check-template.yaml` (`EWAnnotationCheck`) reads `object` for CREATE/UPDATE and `oldObject` for DELETE, enforces key presence, and enforces exact value when `requireValue: true`. | Same template includes `K8sNativeValidation` that enforces the same key/value semantics for matched requests. This is one of the default parity templates with CEL coverage. | Intentional schema difference: Gatekeeper OpenAPI parameters cannot cleanly preserve EarlyWatch's nil-vs-empty-string pointer distinction, so the template uses explicit `requireValue` to distinguish “key only” from “key must equal this value,” including empty string. | `tests/annotation/*`; `tests/malformed/ns-delete-no-annotations-map.yaml`; `gator verify tests/suite.yaml`. |
| **ApprovalCheck.** DELETE requires a valid `earlywatch.io/approved` signature over the EarlyWatch canonical resource path. UPDATE requires a valid `earlywatch.io/change-approved` signature over the upstream-normalized RFC 7396 merge patch with managed metadata/status and the approval annotation stripped. | `policy/04-approval-check-template.yaml` (`EWApprovalCheck`) constructs DELETE and UPDATE provider keys and calls Gatekeeper `external_data()` against the `approval-verifier` Provider. The Rego template fails closed on missing signatures, provider system errors, provider errors, and empty responses. The Go provider performs RSA-PSS verification and upstream-compatible normalization/patch computation; `watchctl/` produces the signatures. | No CEL parity path. Native CEL cannot perform RSA-PSS verification, canonical JSON merge-patch generation, or external provider calls. | Requires Gatekeeper's validating webhook to include `DELETE`, because many installs only validate CREATE/UPDATE by default. Public keys are provided through constraints and can additionally be restricted by provider-mounted trusted keys. This is functional parity, not EarlyWatch approval API compatibility. | `cd provider && go test ./...`; `cd watchctl && go test ./...`; live kind validation covered DELETE missing/bad/valid approval and UPDATE missing/bad/valid change-approval; Gatekeeper webhook verified to include `DELETE`. |
| **CheckLock.** A non-empty lock annotation blocks DELETE and, when configured, UPDATE. Upstream allows updates whose only change is the lock annotation itself, so an operator can clear/change the lock. | `policy/05-check-lock-template.yaml` (`EWCheckLock`) checks `oldObject.metadata.annotations`, defaults the annotation to `earlywatch.io/lock`, denies DELETE when locked, denies UPDATE when `lockOnMutate: true` and any non-lock change is present, and normalizes absent-vs-empty annotation maps when comparing objects without the lock annotation. | No CEL parity path in the default template. The object-diff behavior is implemented in Rego. | No EarlyWatch CRD compatibility. The Rego intentionally preserves upstream's strict “only the lock annotation changed” unlock behavior; status or other metadata changes still count as additional changes. | `tests/checklock/*`; `gator verify tests/suite.yaml`. |
| **ExpressionCheck.** EarlyWatch evaluates expressions against admission context to deny matching changes. | `policy/06-expression-check-template.yaml` (`EWExpressionCheck`) provides a reusable structured expression surface: `operationIn`, `namespaceIn`, `namespaceRegex`, `nameIn`, and `nameRegex`. Rego denies when any configured predicate set matches. | The same structured template includes `K8sNativeValidation` for equivalent CEL enforcement. For arbitrary CEL, the supported Gatekeeper pattern is to author one native-CEL `ConstraintTemplate` per expression; `policy/06-expression-check-real-cel-template.yaml` demonstrates that pattern. | Intentional non-parity for runtime expression strings: Gatekeeper compiles CEL at template creation and does not dynamically `eval()` CEL supplied as constraint parameters. The authored native-CEL example files (`policy/06-expression-check-real-cel-template.yaml` and constraint) are **not** in the default `kustomization.yaml`; they remain optional examples/tests. | `tests/expression/*`; `policy/06-expression-check-constraint-regex.yaml`; optional `expression-check-real-cel` fixture in `tests/suite.yaml`; `gator verify tests/suite.yaml`. |
| **ManualTouchCheck / ManualTouchMonitor.** EarlyWatch denies selected operations if a matching manual touch was observed recently by the audit monitor. The monitor processes Kubernetes audit events, filters to relevant users/user agents/resources/namespaces, and records touches for later admission checks. | Real parity path is `policy/07-manual-touch-check-template.yaml` (`EWManualTouchCheck`) + `policy/07-manual-touch-provider.yaml` + `touch-monitor/`. The Rego template calls `external_data()` with operation, API group, resource, namespace, name, and `windowDuration`; it denies on provider value `touched`. `touch-monitor/` receives audit `EventList` batches at `/audit`, matches `ManualTouchMonitor` CRs, records matching touches in an in-memory cache, and answers `/validate-manual-touch` provider lookups. `failureMode: Ignore` is the default fail-open mode; `failureMode: Deny` makes provider failures fail closed. | No CEL parity path. Native CEL cannot consume audit events, call `external_data()`, or query the mutable time-windowed touch cache. `policy/07-manual-touch-check-cel-template.yaml` is not a native-CEL implementation; it is an optional annotation/timestamp Rego variant and is not EarlyWatch parity-critical. | API-server audit webhook configuration is out of band. Gatekeeper's external-data response cache must be disabled for this provider (`--external-data-provider-response-cache-ttl=0s`) because `untouched` can become `touched` for the same key as audit events arrive and windows expire. `ManualTouchEvent` CR creation is optional compatibility behavior, not the default admission data source; `ManualTouchMonitor.status.touchesDetected` is not updated. `gator verify` intentionally omits this check because `gator` lacks `external_data()`. | `cd touch-monitor && go test ./...` covers audit filtering, service-account exclusion, namespace selector match/mismatch, PATCH→UPDATE, duplicate idempotency, cache/provider touched/untouched/error behavior, and disabled event recording. Live Gatekeeper validation covered `untouched` → audit touch → `touched` denial with response cache disabled. |
| **ServicePodSelectorCheck.** Deny Service UPDATEs that change a previously safe selector into one that matches zero Pods, with upstream's headless-service and empty-selector behavior. | `policy/08-service-pod-selector-check-template.yaml` (`EWServicePodSelectorCheck`) reads Pods from `data.inventory`, evaluates old and new selectors in the Service namespace, treats an absent selector as selecting no Pods, treats an empty selector map as match-all, exempts Services that were already headless, and only applies to UPDATE. | No CEL parity path because the check must see existing Pods in inventory. | Requires Pod sync in `policy/00-config-sync.yaml`. This is Gatekeeper referential policy rather than EarlyWatch API compatibility. | `tests/service-selector/*`; `gator verify tests/suite.yaml`. |
| **DataKeySafetyCheck.** Deny ConfigMap/Secret UPDATEs that remove a `data` or `binaryData` key still referenced by configured referrer fields such as `configMapKeyRef` / `secretKeyRef`. | `policy/09-data-key-safety-check-template.yaml` (`EWDataKeySafetyCheck`) computes removed keys from old vs new object, scans configured referrer kinds in `data.inventory`, defaults omitted referrer version to `v1`, defaults `sameNamespace` to true, traverses configured reference paths through arrays, and defaults `nameSubField` / `keySubField` to `name` / `key`. | No CEL parity path because the check is referential and needs `data.inventory`. | Requires synced referrer kinds. Currently modeled as UPDATE-only, matching the safety behavior for removed keys. | `tests/datakey/*`; `tests/datakey/default-version-constraint.yaml`; `gator verify tests/suite.yaml`. |

## Live validation evidence (kind-kind, 2026-05-05)

The default Gatekeeper stack was applied to the `kind-kind` cluster and the
parity scenarios below were exercised against live admission. The Gatekeeper
validating webhook was patched and verified to include `DELETE`; the verified
operation list was `["CREATE","UPDATE","DELETE"]`. Evidence directories are
local artifacts under `/tmp` from the validation run.

| Area | Evidence directory | Live assertions |
|---|---|---|
| ApprovalCheck | `/tmp/gatekeeper-earlywatch-approval-live-20260505061928` | DELETE missing/bogus signatures denied; valid DELETE signature allowed; UPDATE missing change-approval denied; valid change signature allowed; signed cleanup delete allowed. |
| AnnotationCheck | `/tmp/gatekeeper-earlywatch-annotation-live-20260505061944` | Namespace DELETE denied when `earlywatch.io/confirm-delete` was missing or wrong; allowed when the annotation value was `yes`. |
| ExpressionCheck | `/tmp/gatekeeper-earlywatch-expression-live-20260505062022` | ConfigMap DELETE denied in `production`; `critical-*` ConfigMap DELETE denied outside production; non-matching ConfigMap DELETE allowed. |
| ManualTouchCheck / ManualTouchMonitor | `/tmp/gatekeeper-earlywatch-manualtouch-live-20260505062556` | Untouched Deployment UPDATE allowed; synthetic Kubernetes audit `EventList` posted to `/audit`; subsequent matching Deployment UPDATE denied through `manual-touch-provider`. |
| ExistingResourcesCheck | `/tmp/gatekeeper-earlywatch-existing-live-20260505062806` | Service DELETE denied while a Pod matched `spec.selector`; allowed after the Pod was removed and inventory caught up; static `In(production,staging)` selector behaved the same. |
| NameReferenceCheck | `/tmp/gatekeeper-earlywatch-nameref-live-20260505062956` | ConfigMap DELETE denied while a Deployment still referenced it through volume/envFrom paths; allowed after the Deployment was deleted and inventory caught up. |
| CheckLock | `/tmp/gatekeeper-earlywatch-checklock-live-20260505063722` | Locked Deployment normal UPDATE, scale-subresource UPDATE, and DELETE denied; removing only the lock annotation allowed; DELETE after unlock allowed. |
| ServicePodSelectorCheck | `/tmp/gatekeeper-earlywatch-service-selector-live-20260505063830` | Non-headless Service selector update to zero matching Pods denied; headless Service selector update allowed; Service whose old selector already matched zero Pods could update. |
| DataKeySafetyCheck | `/tmp/gatekeeper-earlywatch-datakey-live-20260505064032` | Referenced ConfigMap key removal denied; unreferenced key removal allowed. |
| Scoping parity and webhook operations | `/tmp/gatekeeper-earlywatch-scoping-live-20260505064107` | ApprovalCheck-scoped ConfigMap UPDATE outside `default` allowed without approval; Namespace DELETE without confirm annotation denied and allowed after annotation; webhook operations verified as `["CREATE","UPDATE","DELETE"]`. |

## Optional/example files not in the default kustomization

The default `kustomization.yaml` now includes only parity-critical templates,
constraints, Providers, and deployments. These files remain in the repository as
examples or experiments but are intentionally excluded from the default stack:

| File(s) | Status |
|---|---|
| `policy/06-expression-check-real-cel-template.yaml`, `policy/06-expression-check-real-cel-constraint.yaml` | Optional authored native-CEL ExpressionCheck example. Demonstrates the correct Gatekeeper pattern for arbitrary CEL: compile the expression into a `ConstraintTemplate` instead of passing it as a runtime string parameter. Included in `tests/suite.yaml` as an optional pattern test, not applied by default. |
| `policy/07-manual-touch-check-cel-template.yaml`, `policy/07-manual-touch-check-cel-constraint.yaml` | Optional annotation/timestamp Rego variant. Despite the filename, it is not a native-CEL enforcement path and is not the EarlyWatch ManualTouch parity implementation. The parity implementation is the external-data provider path described above. |

## ManualTouchMonitor parity details

`touch-monitor/` mirrors the upstream audit-monitor behavior needed by
ManualTouchCheck admission parity:

- HTTPS server exposes `GET /healthz`, `GET /readyz`, `POST /audit`, and
  `POST /validate-manual-touch` on port `8443` by default.
- `/audit` decodes Kubernetes audit `EventList` JSON.
- Only events with `stage == "ResponseComplete"` are processed.
- Verb mapping is upstream-compatible:
  - `delete`, `deletecollection` → `DELETE`
  - `create` → `CREATE`
  - `update`, `patch` → `UPDATE`
- `ManualTouchMonitor` resources are listed across namespaces.
- Matching audit events populate an in-memory cache. The provider lookup encodes
  operation, API group, resource, namespace, name, and `windowDuration` into a
  Gatekeeper external-data key and returns `touched` when a cached event falls
  within that window.
- Provider responses set `idempotent: false`; Gatekeeper's external-data
  provider response cache must be disabled (`--external-data-provider-response-cache-ttl=0s`).
- Matching semantics include:
  - operation membership,
  - user-agent regexes, defaulting to `^kubectl/`,
  - exact excluded service-account username matching,
  - subject API-group equality,
  - subject resource case-insensitive equality, and
  - optional namespace selector evaluation against Namespace labels.
- When `--record-events` is enabled, created `ManualTouchEvent` resources use
  upstream-compatible labels:
  - `earlywatch.io/resource`
  - `earlywatch.io/resource-namespace`
  - `earlywatch.io/resource-name`
  - `earlywatch.io/api-group`
  - `earlywatch.io/operation`
- Optional event specs contain timestamp, user, userAgent, operation, apiGroup,
  resource, resourceName, resourceNamespace, sourceIP, auditID, monitorName,
  and monitorNamespace.
- Optional event names are deterministic and sanitized from
  `mte-<auditID>-<monitorNamespace>-<monitorName>`.
- Duplicate event creation is idempotent through `AlreadyExists` ignore.

## Required validation commands

Run these before cutting a release or claiming a parity-complete build:

```bash
(cd provider && go test ./...)
(cd touch-monitor && go test ./...)
(cd watchctl && go test ./...)
gator verify tests/suite.yaml
kubectl kustomize . >/tmp/gatekeeper-earlywatch-kustomize.yaml
grep -c '^kind: ConstraintTemplate$' /tmp/gatekeeper-earlywatch-kustomize.yaml
grep -c '^kind: Provider$' /tmp/gatekeeper-earlywatch-kustomize.yaml

GATEKEEPER_CHART_VERSION=3.22.2 \
EARLYWATCH_UPSTREAM_COMMIT=55fe022d6190f93474c7a15cb3408e6540160f5d \
bash scripts/ci-kind-parity.sh
```

Expected default kustomization shape after the optional/example cleanup:

- `ConstraintTemplate` count: `9` core parity templates.
- `Provider` count: `2` (`approval-verifier` and `manual-touch-provider`).

The CI script performs the live verification automatically: it patches the
Gatekeeper validating webhook to include `DELETE`, verifies the Helm release is
`gatekeeper-3.22.2`, disables Gatekeeper external-data response caching, posts a
representative `ResponseComplete` audit batch to `touch-monitor`, and verifies
that Gatekeeper can call the `manual-touch-provider` Provider.

## Known operational differences

1. **No EarlyWatch API compatibility.** gatekeeper-earlywatch uses Gatekeeper CRDs and
   constraints instead of EarlyWatch validator CRDs.
2. **Runtime arbitrary-CEL strings are intentionally not supported.** Gatekeeper
   compiles native CEL at template creation. Use the structured
   `EWExpressionCheck` template for common predicates, or author one
   native-CEL `ConstraintTemplate` per arbitrary expression.
3. **Referential checks need inventory sync.** ExistingResources,
   NameReference, ServicePodSelector, and DataKeySafety use `data.inventory` and
   require the relevant GVKs in `policy/00-config-sync.yaml`.
4. **Audit webhook installation is out of band.** Kubernetes API-server audit
   webhook configuration cannot be installed by a normal in-cluster manifest;
   see `touch-monitor/README.md`.
5. **Gatekeeper external-data response caching must be disabled for
   ManualTouch.** `EWManualTouchCheck` queries a mutable, time-windowed cache,
   so Gatekeeper must run with
   `--external-data-provider-response-cache-ttl=0s` to avoid stale admissions.
6. **ManualTouchEvent persistence is optional compatibility behavior.** The
   parity-critical behavior is recording matching audit events in the provider
   cache, which is what `EWManualTouchCheck` consumes. `ManualTouchMonitor`
   status counters are not updated.
