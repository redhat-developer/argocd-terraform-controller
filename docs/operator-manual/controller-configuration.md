# Controller configuration

The controller manager is configured with command-line flags (and a few
environment variables) on its Deployment. With the manifest install, edit the
Deployment `argocd-terraform-controller-controller-manager` in
`argocd-terraform-controller-system`; with Helm, set the values in the right
column.

## Flags

| Flag | Default | Helm value | Purpose |
|---|---|---|---|
| `--concurrent` | `4` | `controller.concurrent` | Terraform objects reconciled in parallel. Each active reconcile holds one runner Pod; raise it for many small stacks, lower it to cap cluster load. |
| `--runner-image` | `$RUNNER_POD_IMAGE` | `runner.image.repository` / `tag` | Default runner image; overridable per object with `spec.runner.image`. |
| `--runner-grpc-port` | `30000` | `runner.grpcPort` | Port the runner's gRPC server listens on inside the Pod. |
| `--runner-grpc-max-message-size` | `4` (MiB) | `runner.grpcMaxMessageSize` | Upper bound for one gRPC message, which bounds the **size of the source archive** shipped to the runner. Raise it for large repositories or use `spec.source.git.ignore`. |
| `--runner-creation-timeout` | `2m` | `runner.creationTimeout` | How long to wait for a runner Pod to become Ready (image pull, scheduling). |
| `--runner-rpc-timeout` | `30m` | `runner.rpcTimeout` | Timeout for the setup phase (upload, `init`, provider downloads). Plans and applies are not bounded by it. |
| `--cluster-domain` | `cluster.local` | `controller.clusterDomain` | DNS domain used in the runner certificate's SANs. |
| `--no-cross-namespace-refs` | `true` | `controller.noCrossNamespaceRefs` | Deny `spec.dependsOn` and `spec.cliConfigSecretRef` pointing at other namespaces (`AccessDenied`). Set to `false` for a single-tenant cluster that needs cross-namespace dependencies. |
| `--argocd-namespace` | `$ARGOCD_NAMESPACE` | `argocd.namespace` | Namespace of Argo CD; enables `spec.source.git.useArgoCDRepoCredentials`. Empty disables the feature. |
| `--ca-validity-duration` | `168h` | `mtls.caValidityDuration` | Lifetime of the runner mTLS CA. |
| `--cert-rotation-check-frequency` | `30m` | `mtls.rotationCheckFrequency` | How often the CA is checked for rotation. |
| `--cert-lookahead-interval` | `1h` | `mtls.lookaheadInterval` | Rotate the CA this long before it expires. |
| `--leader-elect` | `false` (manifest sets it) | `controller.leaderElection` | Required when running more than one replica. |
| `--metrics-bind-address` | `0` (disabled; manifest sets `:8443`) | — | Metrics endpoint; see [Metrics](metrics.md). |
| `--metrics-secure` | `true` | — | Serve metrics over HTTPS with authn/authz. |
| `--health-probe-bind-address` | `:8081` | — | Liveness/readiness probes. |
| `--zap-log-level`, `--zap-encoder`, … | `info`, `console` | `controller.logLevel`, `controller.extraArgs` | Logging (controller-runtime zap flags). Use `--zap-log-level=debug` while troubleshooting. |

Anything not covered by a value can be passed with `controller.extraArgs` /
`controller.extraEnv` in the chart.

## Environment variables

| Variable | Purpose |
|---|---|
| `RUNNER_POD_IMAGE` | Default for `--runner-image`. The release `install.yaml` pins it to the release's runner image. |
| `ARGOCD_NAMESPACE` | Default for `--argocd-namespace`. |
| `RUNTIME_NAMESPACE` | The controller's own namespace (set from the Pod's `metadata.namespace`); used to garbage-collect stale runner TLS Secrets at startup. |
| `DISABLE_TF_K8S_BACKEND=1` | Use `backend "local"` instead of the Kubernetes backend when the object sets neither `backendConfig` nor `cloud`. **Tests only** — state is lost with the runner Pod. |
| `INSECURE_LOCAL_RUNNER=1` | Talk to an in-process runner without mTLS. **Tests only.** |

## Sizing and availability

- **Replicas.** The manager is a standard leader-elected controller: run 2
  replicas for fast failover; only the leader reconciles. Runner Pods are not
  affected by a manager restart — a run in progress is retried from the start
  by the new leader (plans are idempotent; applies resume from the state).
- **Concurrency.** Memory use is dominated by source archives held during
  upload; CPU by Git fetches. The chart defaults (`100m`/`128Mi` requests,
  `1`/`1Gi` limits) comfortably handle the default `--concurrent=4`.
- **Runner resources.** Terraform's own CPU/memory needs vary wildly with the
  provider. Set `spec.runner.podTemplate.spec.resources` per object or
  namespace defaults with a `LimitRange`; see [Runner](../user-guide/runner.md).
- **Quotas.** If a namespace `ResourceQuota` prevents creating the runner Pod,
  the object reports `RunnerQuotaExhausted` and retries after
  `spec.retryInterval`.

## Namespaces and tenancy

The controller watches `Terraform` objects cluster-wide. What limits a team is
the **runner identity in its namespace** (which Secrets/Leases it may write,
which cloud role it maps to) and `--no-cross-namespace-refs`. Three
ClusterRoles for users ship with the install — `terraform-viewer-role`,
`terraform-editor-role`, `terraform-admin-role` — to bind per namespace or
cluster-wide. See [Security](security.md).
