# EarlyWatch parity map

Last updated: 2026-05-03.

This project targets **functional parity in Gatekeeper**, not compatibility with
EarlyWatch `ChangeValidator` / `ClusterChangeValidator` APIs. Operators express
rules as Gatekeeper `Constraint`s; supporting components exist only where
Gatekeeper cannot perform the behavior natively (RSA verification and audit
webhook ingestion).

Upstream audit reference used for this pass: `brendandburns/early-watch` at
`55fe022d6190f93474c7a15cb3408e6540160f5d`.

## Summary

| Upstream capability | Keymaster implementation | Evidence | Notes / limitations |
|---|---|---|---|
| Namespaced and cluster-scoped validator attachment | Gatekeeper `Constraint.spec.match` plus one reusable `ConstraintTemplate` per check | `gator verify tests/suite.yaml` | Not API-compatible with EarlyWatch `ChangeValidator` / `ClusterChangeValidator`; same enforcement behavior is configured through Gatekeeper. |
| ExistingResourcesCheck | `policy/01-existing-resources-template.yaml` (`EWExistingResources`) | `tests/existing-resources/*`; parity fixtures in `tests/existing-resources-parity/*` | Referential; requires synced inventory through `policy/00-config-sync.yaml`. |
| NameReferenceCheck | `policy/02-name-reference-check-template.yaml` (`EWNameReferenceCheck`) | `tests/name-reference/*`; default-version fixture `tests/name-reference/default-version-constraint.yaml` | Defaults omitted resource version to `v1`, matching upstream behavior. |
| AnnotationCheck | `policy/03-annotation-check-template.yaml` (`EWAnnotationCheck`) | `tests/annotation/*`; malformed input fixture | Rego and native CEL implementations are provided. |
| ApprovalCheck | `policy/04-approval-check-template.yaml` + `provider/` external-data service + `watchctl/` signing CLI | `cd provider && go test ./...`; `cd watchctl && go test ./...`; live smoke covered DELETE missing/bad/valid approval and UPDATE missing/bad/valid change-approval | Gatekeeper validating webhook must include `DELETE` for DELETE approval parity. Public keys live on Gatekeeper constraints; provider can further restrict to mounted trusted keys. |
| CheckLock | `policy/05-check-lock-template.yaml` (`EWCheckLock`) | `tests/checklock/*` including lock removal and status-change cases | Allows updates whose only relevant change is clearing/removing the lock annotation, matching upstream unlock behavior. |
| ExpressionCheck | `policy/06-expression-check-template.yaml` (`EWExpressionCheck`) for reusable structured predicates; `policy/06-expression-check-real-cel-template.yaml` (`EWExpressionCheckCELProtectedDelete`) as the native-CEL arbitrary-expression pattern | `tests/expression/*`; regex constraint fixture; `expression-check-real-cel` fixture proves object-field CEL over `oldObject.metadata.labels` | Two forms are supported: portable structured predicates (`operationIn`, `namespaceIn`, `namespaceRegex`, `nameIn`, `nameRegex`) with Rego + native CEL paths, and one authored native-CEL `ConstraintTemplate` per arbitrary expression. Runtime `eval()` of CEL strings is not available in Gatekeeper, so arbitrary CEL is compiled into templates. |
| ManualTouchCheck admission behavior | `policy/07-manual-touch-check-template.yaml` (`EWManualTouchCheck`) reads `ManualTouchEvent` CRs from inventory | `tests/manualtouch/*`; `gator verify tests/suite.yaml` | Referential; requires `ManualTouchEvent` sync. |
| ManualTouchMonitor audit-event production | `touch-monitor/` audit-webhook service + `ManualTouchMonitor` / `ManualTouchEvent` CRDs in `touch-monitor/manifests/audit-monitor.yaml` | `cd touch-monitor && go test ./...`; tests cover default kubectl user-agent, non-`ResponseComplete` filtering, service-account exclusion, namespace selector match/mismatch, PATCH→UPDATE, duplicate idempotency | API-server audit webhook configuration is an external cluster-admin step. The service implements the audited upstream recorder/detector semantics but does not update `ManualTouchMonitor.status.touchesDetected`; upstream's audited recorder path also persists events as the primary side effect. |
| ServicePodSelectorCheck | `policy/08-service-pod-selector-check-template.yaml` (`EWServicePodSelectorCheck`) | `tests/service-selector/*` | Referential; requires synced Pods. |
| DataKeySafetyCheck | `policy/09-data-key-safety-check-template.yaml` (`EWDataKeySafetyCheck`) | `tests/datakey/*`; default-version fixture `tests/datakey/default-version-constraint.yaml` | Defaults omitted resource version to `v1`, matching upstream behavior. |
| Safe rollout, audit, metrics | Native Gatekeeper `enforcementAction`, audit controller status, and metrics | Gatekeeper runtime behavior; dryrun example `policy/05-check-lock-constraint-dryrun.yaml` | Uses Gatekeeper operational model rather than EarlyWatch controller status APIs. |

## ManualTouchMonitor parity details

`touch-monitor/` mirrors the upstream audit-monitor behavior that was the last
parity blocker:

- HTTP(S) server exposes `GET /healthz`, `GET /readyz`, and `POST /audit`.
- `/audit` decodes Kubernetes audit `EventList` JSON.
- Only events with `stage == "ResponseComplete"` are processed.
- Verb mapping is upstream-compatible:
  - `delete`, `deletecollection` → `DELETE`
  - `create` → `CREATE`
  - `update`, `patch` → `UPDATE`
- `ManualTouchMonitor` resources are listed across namespaces.
- Matching semantics include:
  - operation membership,
  - user-agent regexes, defaulting to `^kubectl/`,
  - exact excluded service-account username matching,
  - subject API-group equality,
  - subject resource case-insensitive equality, and
  - optional namespace selector evaluation against Namespace labels.
- Created `ManualTouchEvent` resources use upstream-compatible labels:
  - `earlywatch.io/resource`
  - `earlywatch.io/resource-namespace`
  - `earlywatch.io/resource-name`
  - `earlywatch.io/api-group`
  - `earlywatch.io/operation`
- Event specs contain timestamp, user, userAgent, operation, apiGroup, resource,
  resourceName, resourceNamespace, sourceIP, auditID, monitorName, and
  monitorNamespace.
- Event names are deterministic and sanitized from
  `mte-<auditID>-<monitorNamespace>-<monitorName>`.
- Duplicate creates are idempotent via `AlreadyExists` ignore.

## Required validation commands

Run these before cutting a release or claiming a parity-complete build:

```bash
cd provider && go test ./...
cd ../watchctl && go test ./...
cd ../touch-monitor && go test ./...
cd .. && gator verify tests/suite.yaml
```

For live ApprovalCheck DELETE parity, verify the installed Gatekeeper validating
webhook includes `DELETE` in its operation list before testing DELETE approvals.

## Known operational differences

1. **No EarlyWatch API compatibility.** Keymaster uses Gatekeeper CRDs and
   constraints instead of EarlyWatch validator CRDs. For ExpressionCheck, the
   arbitrary-CEL form is authored as a native-CEL `ConstraintTemplate` instead
   of a runtime expression string parameter, while the reusable
   `EWExpressionCheck` helper covers the common structured predicates.
2. **Audit webhook installation is out of band.** Kubernetes API-server audit
   webhook configuration cannot be installed by a normal in-cluster manifest;
   see `touch-monitor/README.md`.
3. **Alerting/status fields on `ManualTouchMonitor` are not side effects.** The
   implemented parity-critical behavior is creation of `ManualTouchEvent` CRs,
   which is what `ManualTouchCheck` consumes.
