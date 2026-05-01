# ManualTouchEvent producer

`ManualTouchCheck` denies admissions that don't have a recent
`ManualTouchEvent` CR. The producer of those CRs upstream is EarlyWatch's
`ManualTouchMonitor` controller. This directory ships a **stopgap**
CronJob-based producer so the validator can be exercised without the upstream
monitor.

Strategy: every 5 minutes, scan the cluster's events for `Updated` reasons and
stamp a `ManualTouchEvent` CR for each into `early-watch-system`. This is
intentionally crude — for production parity, install the upstream
`ManualTouchMonitor` controller and disable this CronJob.

```bash
kubectl apply -f manifests/cronjob.yaml
```

For full audit-log fidelity (capturing the *user* who issued the change), use
the upstream monitor — it watches the audit log webhook, not Events.
