# Dependencies and health checks

Two features turn a set of Terraform objects into an ordered, verifiable
rollout: `spec.dependsOn` sequences stacks, `spec.healthChecks` proves that
what a stack created actually answers.

## Dependencies: `spec.dependsOn`

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: app-cluster
  namespace: team-a
spec:
  dependsOn:
    - name: network                 # same namespace unless namespace: is set
    - name: shared-dns
      namespace: platform           # requires --no-cross-namespace-refs=false on the controller
  # …
```

Before each run the controller checks every dependency and waits
(`Ready=Unknown / DependencyNotReady`, Argo CD **Progressing**, retried after
`spec.retryInterval`) until all of them are:

1. **Ready** at their current generation — a dependency whose own plan is
   waiting for approval, or whose last apply failed, blocks its dependants;
2. **at the same revision**, when both objects use the same source URL and
   ref — so a commit that touches two stacks is applied in order rather than
   the dependant running against the dependency's previous revision;
3. **done writing outputs** — if the dependency has `writeOutputsToSecret`,
   that Secret must exist.

Nothing is passed implicitly. To consume a dependency's values, read its
outputs Secret with `varsFrom` (see [Outputs › In another Terraform object](outputs.md#in-another-terraform-object)).

### Deletion protection

When `A` depends on `B`, the controller adds a finalizer
`dependency.infra.argocd.io/A` to `B`. Deleting `B` first leaves it in
`Terminating` with `Ready=False / DeletionBlockedByDependants` until `A` is
gone, then `B`'s own deletion (and destroy, if configured) proceeds. This
makes `kubectl delete -f whole-directory/` and Argo CD prune safe regardless
of order: dependants are always destroyed before their dependencies.

### Dependencies vs. sync waves

Both order things; they operate at different levels:

| | `spec.dependsOn` | Argo CD sync waves |
|---|---|---|
| Orders | Terraform objects among themselves, continuously (every reconcile) | Any resources within one Application, once per sync |
| Blocks on | Dependency Ready + same revision + outputs present | Previous wave Healthy |
| Works across Applications | yes (same cluster) | no |
| Deletion | dependants first, enforced by finalizers | reverse wave order during prune |

Use `dependsOn` between stacks, and sync waves between a stack and the
Kubernetes workloads that consume it. They compose: a Terraform object in
wave 0 that itself `dependsOn` another object simply stays Progressing — which
holds wave 1 — until its dependency is Ready.

### Example: three-tier rollout

```yaml
# wave 0: network (no dependencies)
kind: Terraform
metadata: { name: network, annotations: { argocd.argoproj.io/sync-wave: "0" } }
spec:
  writeOutputsToSecret: { name: network-outputs }
---
# wave 0: database, waits for network through dependsOn
kind: Terraform
metadata: { name: database, annotations: { argocd.argoproj.io/sync-wave: "0" } }
spec:
  dependsOn: [{ name: network }]
  varsFrom: [{ kind: Secret, name: network-outputs, varsKeys: [vpc_id, "private_subnets:subnet_ids"] }]
  writeOutputsToSecret: { name: database-outputs, outputs: ["endpoint:DB_HOST", "password:DB_PASSWORD"] }
  healthChecks:
    - name: db
      type: tcp
      address: "{{ .DB_HOST }}:5432"
---
# wave 1: the application, only after both Terraform objects are Healthy
kind: Deployment
metadata: { name: api, annotations: { argocd.argoproj.io/sync-wave: "1" } }
spec:
  template:
    spec:
      containers:
        - name: api
          envFrom: [{ secretRef: { name: database-outputs } }]
```

## Health checks: `spec.healthChecks`

After a successful apply (and after outputs are written) the controller runs
each check from inside the controller Pod. All must pass for the object to
become Ready; a failure sets `Ready=False / HealthChecksFailed` (Argo CD
**Degraded**), emits a Warning event with the rendered target, and the run is
retried after `spec.retryInterval`.

```yaml
spec:
  writeOutputsToSecret:
    name: web-outputs               # required for templates to see outputs
  healthChecks:
    - name: lb
      type: http
      url: "https://{{ .lb_hostname }}/healthz"
      timeout: 30s                  # default 20s
    - name: db
      type: tcp
      address: "{{ .db_host }}:{{ .db_port }}"
```

| Field | Notes |
|---|---|
| `type: http` + `url` | `GET`; any `2xx`/`3xx` status passes. Follows redirects. No custom headers or TLS options — use a dedicated health endpoint |
| `type: tcp` + `address` | `host:port`; passes when the TCP connection succeeds |
| `timeout` | Per check, default `20s` |
| templates | Go `text/template` over the **outputs Secret keys** (post-rename). A missing key is an error (`missingkey=error`), not an empty string |

Health checks run **from the controller**, so the target must be reachable
from the controller's namespace: a public load balancer, a Service in the
cluster, or a private endpoint the cluster can route to. Checks are re-run on
the next reconcile only if the previous attempt failed; a Healthy object is
not re-checked until something changes.

### When to use them

- Prove that a managed database, load balancer or cluster is actually
  accepting connections before Argo CD proceeds to the next sync wave.
- Catch provider "success" that is not really ready yet (DNS propagation,
  eventual consistency) and keep the object Degraded until it is.

Don't use them as a monitoring system: they run after applies, not
continuously. For ongoing checks use your monitoring stack and
[Drift detection](drift-detection.md) for configuration drift.
