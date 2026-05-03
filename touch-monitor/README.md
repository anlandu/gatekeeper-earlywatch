# ManualTouch audit-webhook producer

`EWManualTouchCheck` reads `ManualTouchEvent` CRs from Gatekeeper inventory and
rejects matching admissions when a recent event exists. Upstream EarlyWatch
creates those CRs from Kubernetes audit webhook events. This directory now ships
the same style of component for the Gatekeeper stack:

- `POST /audit` accepts Kubernetes `audit.k8s.io/v1` `EventList` webhook
  batches.
- Only `ResponseComplete` events are processed.
- Verbs map like upstream: `delete`/`deletecollection` → `DELETE`, `create` →
  `CREATE`, and `update`/`patch` → `UPDATE`.
- `ManualTouchMonitor` CRs are listed across namespaces and matched by
  operation, user-agent regex (default `^kubectl/`), excluded service-account
  usernames, subject API group/resource, and optional namespace selector.
- Matching audit events create EarlyWatch-compatible `ManualTouchEvent` CRs in
  `early-watch-system` by default.
- Duplicate deliveries are idempotent because event names are deterministic
  (`mte-<auditID>-<monitorNamespace>-<monitorName>`) and `AlreadyExists` is
  ignored.

## Build and test

```bash
cd touch-monitor
go test ./...
docker build -t ghcr.io/your-org/earlywatch-touch-monitor:latest .
```

Update `manifests/audit-monitor.yaml` with the image you publish.

## Deploy

The root kustomization includes `touch-monitor/manifests/audit-monitor.yaml`:

```bash
kubectl apply -k .
```

That manifest installs:

- `ManualTouchEvent` and `ManualTouchMonitor` CRDs,
- an `early-watch-system` namespace,
- RBAC for the monitor to list monitors, read namespaces, and create events,
- the `manual-touch-monitor` Deployment and Service, and
- an example `ManualTouchMonitor` for `apps/deployments` `UPDATE` events.

## Configure Kubernetes audit webhook delivery

Kubernetes API server audit webhook configuration is not a normal in-cluster
manifest; it must be configured on the API server. Point the audit webhook
backend at the monitor's `/audit` endpoint, usually through TLS:

```yaml
apiVersion: v1
kind: Config
clusters:
  - name: manual-touch-monitor
    cluster:
      server: https://manual-touch-monitor.early-watch-system.svc.cluster.local:8090/audit
      certificate-authority: /etc/kubernetes/audit/manual-touch-ca.crt
contexts:
  - name: default
    context:
      cluster: manual-touch-monitor
      user: ""
current-context: default
users: []
```

Start the monitor with `--tls-cert-file` and `--tls-private-key-file`, or place
an ingress/proxy in front of the Service that terminates TLS. For local labs you
can run it over HTTP, but production API-server audit webhooks should pin a CA.

A minimal audit policy should emit metadata for the resources you monitor at
least at `RequestResponse` or `Metadata` level with `ResponseComplete` stages
included. The monitor only needs metadata fields: verb, user, userAgent,
sourceIPs, objectRef, auditID, requestReceivedTimestamp, and stage.

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
