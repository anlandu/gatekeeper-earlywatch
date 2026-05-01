# GitOps notes (Flux / ArgoCD)

Gatekeeper has dependency ordering that GitOps tools don't infer
automatically. Use sync waves / health checks to enforce it.

## Required apply order

1. Gatekeeper itself (CRDs + webhook + audit pods).
2. `Provider` CR (so the constraint can resolve `external_data` provider name).
3. `ConstraintTemplate` (so the `kind` exists for the constraint).
4. `Constraint`.
5. `Config` for inventory sync (can come earlier; just must exist before
   any referential constraint *fires*).

If you push everything in one apply, Gatekeeper retries reconciles, so
eventually it converges — but for the first ~30s some constraints will be
in `notReady`.

## ArgoCD

```yaml
# annotation on each ConstraintTemplate
argocd.argoproj.io/sync-wave: "1"
# annotation on each Constraint
argocd.argoproj.io/sync-wave: "2"
# annotation on Provider
argocd.argoproj.io/sync-wave: "1"
```

Use a custom health check for `ConstraintTemplate` and `Constraint`:

```lua
-- ConstraintTemplate ready when status.created == true
hs = {}
if obj.status ~= nil and obj.status.created == true then
  hs.status = "Healthy"
else
  hs.status = "Progressing"
end
return hs
```

## Flux

```yaml
# Kustomization for templates
kind: Kustomization
spec:
  dependsOn:
    - name: gatekeeper
  healthChecks:
    - apiVersion: templates.gatekeeper.sh/v1
      kind: ConstraintTemplate
      name: ewapprovalcheck

# Separate Kustomization for constraints, dependsOn the templates one.
```

## Drift detection footgun

Gatekeeper rewrites `status.byPod[]` constantly. Configure your GitOps tool
to ignore that field, otherwise every reconcile reports drift:

```yaml
# Flux
spec:
  patches:
    - patch: |
        - op: remove
          path: /status
      target:
        kind: Constraint
```
