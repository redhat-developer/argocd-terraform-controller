# Troubleshooting

Start with the object itself — almost every failure is explained by its
`Ready` condition and the last Warning event:

```sh
kubectl -n <ns> get tf <name> -o wide
kubectl -n <ns> get tf <name> -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'
kubectl -n <ns> get events --field-selector involvedObject.kind=Terraform,involvedObject.name=<name> --sort-by=.lastTimestamp
```

Then, if needed, the two logs:

```sh
# Controller: source fetching, runner lifecycle, decisions (why it planned / skipped / waited)
kubectl -n argocd-terraform-controller-system logs deploy/argocd-terraform-controller-controller-manager -c manager --tail=200 | grep <name>

# Runner: Terraform's own output. The Pod exists only while a run is in progress.
kubectl -n <ns> logs <name>-tf-runner
```

Turn on debug logging with `--zap-log-level=debug` on the manager when the
decision logic is unclear.

## By Ready reason

| Reason | What it means | What to do |
|---|---|---|
| `SourceFetchFailed` | Git/OCI fetch or `ls-remote` failed | Check the URL and ref; check `secretRef` keys (`username`/`password`, `identity`+`known_hosts`); for `useArgoCDRepoCredentials` confirm `--argocd-namespace` and the repo-reader RoleBinding; look for the exact error in the event |
| `RunnerConnectionFailed` | Runner Pod did not become Ready within `--runner-creation-timeout`, or gRPC could not connect | `kubectl -n <ns> describe pod <name>-tf-runner`: ImagePullBackOff, Pending (resources, nodeSelector, PSA rejection), or a NetworkPolicy blocking the controller → runner port `30000` |
| `RunnerQuotaExhausted` | A `ResourceQuota` refused the runner Pod | Raise the quota or set `spec.runner.podTemplate.spec.resources` so the Pod fits; it retries automatically |
| `AccessDenied` | `dependsOn` or `cliConfigSecretRef` points at another namespace | Keep references in-namespace, or run the controller with `--no-cross-namespace-refs=false` |
| `DependencyNotReady` | A `dependsOn` target is not Ready, not at the same revision, or its outputs Secret is missing | Fix the dependency first; this object continues automatically |
| `TFExecInitFailed` | `init` failed | Provider download (egress, registry mirror, `.tfrc`), backend configuration, module sources, Terraform version constraints — read the runner log |
| `BackendConfigurationFailed` | Generated backend block or `backendConfigsFrom` is invalid | Check `spec.backendConfig.customConfiguration` HCL and the referenced Secret keys |
| `VarsGenerationFailed` / `TemplateGenerationFailed` | `vars`/`varsFrom`/`values` could not be resolved, or `main.tf.tpl` failed to render | A referenced Secret/ConfigMap key is missing (mark it `optional` if that's expected); check template syntax |
| `SelectWorkspaceFailed` | Terraform workspace could not be selected/created | Backend does not support workspaces, or the state is locked |
| `TFExecPlanFailed` | `plan` failed | Usually provider authentication or configuration errors — the message is Terraform's; see [Provider credentials](#provider-credentials) |
| `TerraformPlannedWithChanges` (`Ready=Unknown`) | A plan is **waiting for approval** | Not an error. Approve it (UI, Git or `kubectl`), or set `approvePlan: auto` |
| `TFExecApplyFailed` / `TFExecDestroyFailed` | `apply`/`destroy` failed part-way | State is consistent up to the failure. Fix the cause; the next run re-plans. Check for partially created resources |
| `LockHeld` | State is locked (interrupted run, another process) | `status.lock.pending` has the ID. Use the **Force unlock** action or `spec.tfstate.forceUnlock`; see [State › Locks](state.md#locks) |
| `DriftDetected` | Infrastructure differs from state | In manual mode: review the drift in the event, then **Re-plan** and approve. In `auto` mode this is transient while remediating |
| `DriftDetectionFailed` | The refresh-only plan itself failed | Same causes as `TFExecPlanFailed`; often expired credentials |
| `OutputsWritingFailed` | Outputs Secret could not be written | `writeOutputsToSecret.outputs` names an output that does not exist, or a label/annotation is invalid; or `tf-runner` lacks Secret permissions |
| `HealthChecksFailed` | A `healthChecks` entry failed | The endpoint is not up yet or the template references a missing output; see the event for the rendered address |
| `RetryLimitReached` (`Stalled`) | `spec.remediation.retries` exhausted | Fix the cause, then change the spec or source (or **Re-plan**) to restart |
| `DeletionBlockedByDependants` | Object is being deleted but others `dependsOn` it | Delete the dependants first; Argo CD prune order follows the same rule |
| `Suspended` | `spec.suspend: true` | Resume when ready |

## Common situations

### "Nothing happens" after creating the object

- `kubectl -n <ns> get tf`: is `Ready` empty? The controller may not be
  running, or the CRD/RBAC are from a different release:
  `kubectl -n argocd-terraform-controller-system get deploy`.
- Is there a `tf-runner` ServiceAccount and RoleBinding **in the object's
  namespace**? Without it the runner Pod fails to start or cannot write
  Secrets (`forbidden` in the runner log). See
  [Installation › Runner identity](installation.md#2-runner-identity-per-namespace).
- `spec.suspend: true`? `dependsOn` not Ready?

### Argo CD shows the object as Suspended

That is a plan **waiting for approval** (manual mode, the default). It is
deliberate: Suspended blocks later sync waves until a human approves. The plan
ID is in the Ready message, the event and the UI. Approve it, or use
`approvePlan: auto` for environments where that is acceptable. See
[Plan approval](../user-guide/approval-workflow.md).

### Approved in the UI, but Argo CD reverted it

The Application has automated sync with `selfHeal`, so Argo CD put
`spec.approvePlan` back to what Git says. Either approve from Git (commit the
plan ID), or add the `ignoreDifferences` for the action-mutated fields shown in
[Argo CD integration › GitOps considerations](argocd-integration.md#gitops-considerations).

### Provider credentials

`Error: failed to get shared config profile`, `No valid credential sources
found`, `ProviderConfigurationError`, `Unable to locate credentials`: the
runner Pod has no credentials. It starts with an empty home and no
environment. Provide them with `envFrom`, `fileMappings` or workload identity
— see [Variables and credentials](../user-guide/variables-and-credentials.md).
If they used to work and stopped, temporary credentials (session tokens)
expired.

With workload identity: check the runner Pod got the injected env vars
(`kubectl -n <ns> get pod <name>-tf-runner -o yaml | grep -A1 AWS_` while a
run is in progress). If the token file is unreadable (`permission denied` on
`/var/run/secrets/eks.amazonaws.com/…`), an added `initContainer` without
`runAsUser: 65532` is the usual cause — all containers must share the UID for
the kubelet to make the projected token readable.

### The runner Pod is Pending or crash-looping

`kubectl -n <ns> describe pod <name>-tf-runner`:

- `ImagePullBackOff`: the runner image (`RUNNER_POD_IMAGE` or
  `spec.runner.image`) is not reachable; private registries need an
  `imagePullSecrets` entry on the `tf-runner` ServiceAccount.
- `Pending` with `Insufficient cpu/memory`: lower
  `spec.runner.podTemplate.spec.resources` or add capacity.
- Rejected by Pod Security Admission or a policy engine: the default security
  context satisfies the `restricted` profile; if you overrode
  `podTemplate.spec.securityContext`, keep the restrictive fields.
- Exits immediately with a TLS error: the per-namespace `terraform-runner.tls`
  Secret is stale (controller was reinstalled); delete it and re-plan — the
  controller recreates it.

### Source archive too large

`grpc: received message larger than max`: the archived source exceeds
`--runner-grpc-max-message-size` (4 MiB). Exclude what Terraform does not need
with `spec.source.git.ignore` (docs, tests, other stacks), point `path` at a
smaller repository, or raise the flag.

### Plan is huge / plan Secret chunked

Plans over 1 MiB are stored in several Secrets (`tfplan-…-0`, `-1`, …); this is
normal. If the readable ConfigMap is missing, `spec.storeReadablePlan` is
`none`, or the plan text exceeded ConfigMap limits — the binary plan is still
applied.

### State is locked after a crash

See [State › Locks](state.md#locks). The **Force unlock** action in Argo CD is
only enabled while `status.lock.pending` is set, and
`spec.tfstate.forceUnlock: yes` requires `lockIdentifier` to match — this
prevents unlocking someone else's run by accident.

### I need to run `terraform state …` or `terraform import` by hand

Suspend the object, then point a local Terraform at the same backend — the
Kubernetes backend works from a laptop with `in_cluster_config = false`.
Instructions in [State › Working with the state from the CLI](state.md#working-with-the-state-from-the-cli).
Resume afterwards.

### The object flaps between Degraded and Progressing, re-planning the same resource

Symptom: `Ready=False / DriftDetected` on every interval, the drift event
always lists the **same resource** to be created (or changed), applying it
succeeds, and the next check finds the same drift again.

The configuration writes something to the **runner's filesystem**. Every run
executes in a fresh Pod whose working directory and home are `emptyDir`
volumes that vanish when the Pod is deleted, so any resource whose "real
world" is a local file — `local_file`, `local_sensitive_file`, a `null_resource`
with a `local-exec` that writes a file, a `tls_private_key` saved with
`local_file` — ceases to exist the moment the run ends. Terraform then sees a
resource in state that is missing from the world, and reports drift forever.

Example of the pattern (an SSH key pair written next to the configuration):

```hcl
resource "tls_private_key" "ssh" { algorithm = "ED25519" }

resource "local_sensitive_file" "private_key" {        # drifts on every run
  filename = "${path.root}/${var.name}-key.pem"
  content  = tls_private_key.ssh.private_key_openssh
}
```

Fix: keep the value in Terraform's state and **hand it out as an output**
instead of a file. Outputs land in the Secret named by
`spec.writeOutputsToSecret`, which is where secrets belong on Kubernetes:

```hcl
output "private_key" {
  value     = tls_private_key.ssh.private_key_openssh
  sensitive = true
}
```

```sh
kubectl -n team-a get secret <outputs-secret> -o jsonpath='{.data.private_key}' | base64 -d > key.pem
```

The same applies to anything a laptop workflow leaves on disk: generated
kubeconfigs, certificates, rendered templates, downloaded archives. If a file
is genuinely needed by a later resource in the same run, that still works —
the file exists for the duration of the run — just do not make it a managed
resource that must survive between runs. See
[Drift detection](../user-guide/drift-detection.md#drift-that-never-goes-away)
and [Outputs](../user-guide/outputs.md).

### Installing the UI extension fails with `containers[0].image: Required value`

```
The Deployment "argocd-server" is invalid: spec.template.spec.containers[0].image: Required value
```

The server-extension patch merges into the server container by name
(`- name: argocd-server`). The upstream `install.yaml` names it `argocd-server`,
but the Argo CD **Helm chart** names it `server`. When the name does not match,
strategic merge appends a new container that has only the volume mount and no
image, and the API rejects it. Detect the real name and rewrite the patch:

```sh
SERVER_CONTAINER=$(kubectl get deploy argocd-server -n argocd -o jsonpath='{.spec.template.spec.containers[0].name}')
curl -fsSL ${RELEASE}/argocd-server-extension-patch.yaml \
  | sed "s/- name: argocd-server/- name: ${SERVER_CONTAINER}/" \
  | kubectl patch deployment argocd-server -n argocd --patch-file /dev/stdin
```

### Nothing in the Argo CD UI

- No **Terraform** entry in the sidebar and no tab: the extension is not
  installed. `kubectl -n argocd exec deploy/argocd-server -- ls /tmp/extensions/resources/`
  must list `extension-terraform.js`. If it does, **hard-refresh** the browser
  — Argo CD caches `extensions.js`. If it doesn't, check the
  `extension-terraform` init container log on `argocd-server`.
- Sidebar entry present but empty: the dashboard lists Terraform objects that
  belong to **Applications you can see**. Objects applied with raw `kubectl`
  are not shown; neither are Applications your Argo CD RBAC hides.
- Health shows `Progressing`/`Missing` for everything: the `argocd-cm` patch
  is not applied, or Argo CD has not reloaded it (it does so within a minute,
  no restart needed). `kubectl -n argocd get cm argocd-cm -o yaml | grep infra.argocd.io_Terraform`.
- Action buttons fail with a Lua error: `argocd-cm` and the extension are from
  different releases; re-apply both patches from the same release
  ([Installation › Upgrading](installation.md#upgrading)).

### Helm or Argo CD prune deletes the object before the controller destroyed anything

Deletion is handled by a finalizer; the object stays until the destroy
(with `destroyResourcesOnDeletion`) finishes. If the **controller is gone**
(uninstalled first), the finalizer never completes and the object hangs in
`Terminating`. Reinstall the controller, or — if the resources are already
gone or you accept leaking them — remove the finalizers manually:

```sh
kubectl -n <ns> patch tf <name> --type merge -p '{"metadata":{"finalizers":null}}'
```

Uninstall order is covered in [Installation › Uninstalling](installation.md#uninstalling).

## Collecting a support bundle

When opening an issue, include (with secrets redacted):

```sh
kubectl -n <ns> get tf <name> -o yaml
kubectl -n <ns> get events --field-selector involvedObject.name=<name> --sort-by=.lastTimestamp
kubectl -n argocd-terraform-controller-system logs deploy/argocd-terraform-controller-controller-manager -c manager --since=1h
kubectl -n <ns> logs <name>-tf-runner            # if the Pod is still there
kubectl -n argocd-terraform-controller-system get deploy -o jsonpath='{.items[0].spec.template.spec.containers[0].image}'
```
