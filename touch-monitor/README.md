# ManualTouch audit-webhook producer and provider

`EWManualTouchCheck` now queries a Gatekeeper `external_data` provider instead
of reading synced `ManualTouchEvent` CRs from `data.inventory`. This directory
ships the provider and audit sink used by that policy:

- `POST /audit` accepts Kubernetes `audit.k8s.io/v1` `EventList` webhook
  batches.
- Only `ResponseComplete` events are processed.
- Verbs map like upstream: `delete`/`deletecollection` → `DELETE`, `create` →
  `CREATE`, and `update`/`patch` → `UPDATE`.
- `ManualTouchMonitor` CRs are listed across namespaces and matched by
  operation, user-agent regex (default `^kubectl/`), excluded service-account
  usernames, subject API group/resource, and optional namespace selector.
- Matching audit events populate an in-memory manual-touch cache.
- `POST /validate-manual-touch` implements the Gatekeeper external-data
  provider API and returns `touched` or `untouched` for each requested key.
- Creating EarlyWatch-compatible `ManualTouchEvent` CRs is optional for older
  inventory-based consumers; start the service with `--record-events` to enable
  that compatibility path.

Provider keys use this format, where every field after `manualtouch` is
base64-encoded. The optional `requestUID` field is ignored by the provider and
exists only as a cache-buster for Gatekeeper external-data lookups, because
manual-touch state can change between admission requests:

```text
manualtouch|<operation>|<apiGroup>|<resource>|<namespace>|<name>|<windowDuration>[|<requestUID>]
```

The policy template builds keys with `base64.encode()` and includes the
AdmissionReview UID as the optional cache-buster. The provider normalizes
operation, API group, and resource the same way the audit path does. Namespace
and name remain case-sensitive.

## Build and test

```bash
cd touch-monitor
go test ./...
docker build -t ghcr.io/your-org/earlywatch-touch-monitor:latest .
```

Update `manifests/audit-monitor.yaml` with the image you publish.

## Deploy

The root kustomization includes `touch-monitor/manifests/audit-monitor.yaml`
and `policy/07-manual-touch-provider.yaml`:

```bash
kubectl apply -k .
```

That manifest installs:

- `ManualTouchEvent` and `ManualTouchMonitor` CRDs,
- an `early-watch-system` namespace,
- RBAC for the monitor to list monitors, read namespaces, and optionally create
  events when `--record-events` is enabled,
- the `manual-touch-monitor` HTTPS Deployment and Service on port `8443`, and
- an example `ManualTouchMonitor` for `apps/deployments` `UPDATE` events.

The deployment expects a TLS Secret named `manual-touch-monitor-tls` containing
`tls.crt` and `tls.key`. Configure the `caBundle` in
`policy/07-manual-touch-provider.yaml` with the base64-encoded CA certificate
that signed that serving certificate so Gatekeeper can call
`/validate-manual-touch`.

Manual-touch provider answers are intentionally non-idempotent: an object can be
`untouched` before the audit webhook receives a matching event and `touched`
immediately afterward. The shipped policy includes the AdmissionReview UID in
the key so each admission lookup is distinct. If you customize the key format,
keep an equivalent per-request cache-buster or disable Gatekeeper's
external-data provider response cache.

## Configure Kubernetes audit webhook delivery

Kubernetes API server audit webhook configuration is not a normal in-cluster
manifest; it must be configured on the API server. Point the audit webhook
backend at the monitor's `/audit` endpoint over TLS:

```yaml
apiVersion: v1
kind: Config
clusters:
  - name: manual-touch-monitor
    cluster:
      server: https://manual-touch-monitor.early-watch-system.svc.cluster.local:8443/audit
      certificate-authority: /etc/kubernetes/audit/manual-touch-ca.crt
contexts:
  - name: default
    context:
      cluster: manual-touch-monitor
      user: ""
current-context: default
users: []
```

A minimal audit policy should emit metadata for the resources you monitor at
least at `RequestResponse` or `Metadata` level with `ResponseComplete` stages
included. The monitor only needs metadata fields: verb, user, userAgent,
sourceIPs, objectRef, auditID, requestReceivedTimestamp, and stage.

## Runtime flags

Relevant flags:

- `--listen-address=:8443` — HTTPS listen address.
- `--tls-cert-file` and `--tls-private-key-file` — serving certificate files.
- `--event-namespace=early-watch-system` — namespace for optional
  `ManualTouchEvent` records.
- `--record-events=false` — set true to create `ManualTouchEvent` CRs for
  compatibility.
- `--touch-cache-ttl=24h` — maximum age retained in the provider cache.
- `--touch-cache-max-records=10000` — maximum cache entries before oldest-entry
  eviction.

For local labs you can omit TLS flags and use HTTP, but production API-server
audit webhooks and Gatekeeper external-data Providers should use TLS with a
pinned CA.

## Monitor examples

Default kubectl detection for Deployment updates:

```yaml
apiVersion: earlywatch.io/v1alpha1
kind: ManualTouchMonitor
metadata:
  name: deployments-update
  namespace: early-watch-system
spec:
  subjects:
    - apiGroup: apps
      resource: deployments
  operations: ["UPDATE"]
```

Restrict to namespaces labeled `environment=prod` and ignore a CI service
account:

```yaml
apiVersion: earlywatch.io/v1alpha1
kind: ManualTouchMonitor
metadata:
  name: prod-configmaps
  namespace: early-watch-system
spec:
  subjects:
    - apiGroup: ""
      resource: configmaps
      namespaceSelector:
        matchLabels:
          environment: prod
  operations: ["CREATE", "UPDATE", "DELETE"]
  excludeServiceAccounts:
    - system:serviceaccount:ci:deployer
```
