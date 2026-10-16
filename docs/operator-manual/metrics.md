# Metrics

The controller exposes Prometheus metrics from the controller-runtime metrics
server. With the manifest install the endpoint is enabled on **`:8443`
(HTTPS)** behind a Service `argocd-terraform-controller-controller-manager-metrics-service`
in `argocd-terraform-controller-system`; scraping requires a token with
`get` on the `/metrics` non-resource URL (the install ships a
`metrics-reader` ClusterRole for that).

## Scraping

With the Prometheus Operator, apply the ServiceMonitor from the repository's
`config/prometheus` (it targets the `https` port and uses the Prometheus
ServiceAccount token):

```sh
kubectl apply -k "https://github.com/redhat-developer/argocd-terraform-controller/config/prometheus?ref=${VERSION}"
kubectl create clusterrolebinding argocd-terraform-metrics-reader \
  --clusterrole=argocd-terraform-controller-metrics-reader \
  --serviceaccount=monitoring:prometheus-k8s
```

The shipped ServiceMonitor uses `insecureSkipVerify: true` for the self-signed
serving certificate. For production, mount a certificate from cert-manager into
the manager (`--metrics-cert-path`) and set `serverName`/`ca` in the
`tlsConfig` instead — the commented `monitor_tls_patch.yaml` in the same
directory shows the fields.

The install also contains a `NetworkPolicy` that admits traffic to the metrics
port only from namespaces labelled `metrics: enabled`:

```sh
kubectl label namespace monitoring metrics=enabled
```

To scrape over plain HTTP in a test cluster, pass
`--metrics-bind-address=:8080 --metrics-secure=false` to the manager.

## What is exported

Everything controller-runtime provides, labelled with
`controller="terraform"` where applicable:

| Metric | Use it for |
|---|---|
| `controller_runtime_reconcile_total{result=…}` | Reconcile rate; `result="error"` for failing runs |
| `controller_runtime_reconcile_errors_total` | Errors over time |
| `controller_runtime_reconcile_time_seconds` (histogram) | How long runs take — Terraform plan/apply duration dominates |
| `controller_runtime_max_concurrent_reconciles` | Confirms `--concurrent` |
| `controller_runtime_active_workers` | How many objects are being reconciled right now (each holds a runner Pod) |
| `workqueue_depth{name="terraform"}` | Backlog: objects waiting for a worker |
| `workqueue_queue_duration_seconds` | How long objects wait before a worker picks them up |
| `rest_client_requests_total` | API server traffic |
| `leader_election_master_status` | 1 on the active replica |
| `go_*`, `process_*` | Runtime health |

A useful starting alert set:

```yaml
- alert: TerraformReconcileErrors
  expr: rate(controller_runtime_reconcile_errors_total{controller="terraform"}[15m]) > 0
  for: 30m
- alert: TerraformBacklog
  expr: workqueue_depth{name="terraform"} > 10
  for: 15m
- alert: TerraformControllerDown
  expr: absent(up{job=~".*argocd-terraform-controller.*"} == 1)
  for: 5m
```

## Per-object status instead of metrics

There are no per-Terraform-object metrics yet (drift count, pending approvals,
last apply age). That information is in the object's `status` and in Argo CD:

- pending approvals: `kubectl get tf -A -o json | jq '.items[] | select(.status.plan.pending != "") | .metadata.name'`,
  or the counters at the top of the **Terraform** dashboard in Argo CD;
- drift: Ready condition reason `DriftDetected`, Argo CD health **Degraded**,
  and a Warning event with the drift plan;
- staleness: `status.lastSuccessfulReconcileAt`, `status.lastDriftDetectedAt`.

Argo CD's own `argocd_app_info{health_status=…}` metric therefore reflects
Terraform objects too: an Application that contains a Terraform object waiting
for approval reports `health_status="Suspended"`, a failing one `Degraded`.
Alerting on Argo CD health is often the simplest way to cover Terraform runs.

Per-object metrics are a reasonable future addition; please open an issue with
the labels you would need.
