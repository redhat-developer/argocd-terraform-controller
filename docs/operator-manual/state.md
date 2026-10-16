# State backends, backup and restore

## Default: Kubernetes backend

When `spec.backendConfig` and `spec.cloud` are unset, the controller writes a
`backend_override.tf` into the working directory configuring Terraform's native
[`kubernetes` backend](https://developer.hashicorp.com/terraform/language/backend/kubernetes):

```hcl
terraform {
  backend "kubernetes" {
    secret_suffix     = "<object name>"
    in_cluster_config = true
    namespace         = "<object namespace>"
    labels            = { }
  }
}
```

Consequences:

- State lives **gzip-encoded in Secret `tfstate-<workspace>-<name>`** in the
  object's namespace (`status.stateSecretName`). Each workspace gets its own
  Secret.
- Locking uses the **Lease `lock-tfstate-<workspace>-<name>`**; the lock
  identifier is reported in `status.lock.pending` when a run cannot acquire it.
- The **runner** writes the state with its ServiceAccount (`tf-runner`), which
  is why it needs Secrets/Leases permissions in that namespace.
- The Secret is visible in the Argo CD resource tree (data redacted) because
  it lives next to the Terraform object, but it is not owned by it: deleting
  the object keeps the state unless `spec.tfstate.deleteOnDestroy` is set.

`spec.backendConfig` tunes the generated block:

```yaml
backendConfig:
  secretSuffix: shared-network   # -> tfstate-default-shared-network
  inClusterConfig: true
  configPath: ""                 # kubeconfig path inside the runner (external cluster)
  labels:
    team: platform               # added to the state Secret by the backend
```

Setting `DISABLE_TF_K8S_BACKEND=1` on the controller switches the default to
`backend "local"` (only useful for tests).

## Other backends

Any backend block can be supplied verbatim and is placed inside `terraform {}`:

```yaml
spec:
  backendConfig:
    customConfiguration: |
      backend "s3" {
        bucket         = "my-tfstate"
        key            = "network/terraform.tfstate"
        region         = "us-east-1"
        dynamodb_table = "my-tfstate-locks"
      }
  backendConfigsFrom:            # -backend-config key=value pairs from Secrets/ConfigMaps
  - kind: Secret
    name: s3-backend-credentials
    keys: [access_key, secret_key]
```

Provider/backends credentials can also come from the runner environment
(`spec.runner.podTemplate.spec.env|envFrom`) or workload identity annotations on
`tf-runner`.

`spec.backendConfig.disable: true` writes no backend block at all (state stays
in the runner's working directory and is lost after the run — only for
stateless modules).

## Terraform Cloud / Enterprise

```yaml
spec:
  cloud:
    organization: my-org
    hostname: app.terraform.io   # optional
    workspaces:
      name: network              # or tags: [network, prod]
  cliConfigSecretRef:            # Secret with a *.tfrc key holding the credentials block
    name: tfc-credentials
```

`spec.cloud` disables the Kubernetes backend; state lives in the TFC workspace.

## Locks

If a run is interrupted the state may stay locked. The controller reports it:

```
Ready=False  LockHeld  Terraform Locked with Lock Identifier: f2ab685b-…
status.lock.pending: f2ab685b-…
```

Unlock by setting (or using the Argo CD *Force unlock* action):

```yaml
spec:
  tfstate:
    forceUnlock: yes            # requires lockIdentifier to equal status.lock.pending
    lockIdentifier: f2ab685b-…
    # or: forceUnlock: auto     # unlock whatever is pending — only if nothing else uses the state
    lockTimeout: 2m             # how long plan waits for the lock before failing
```

Reset `forceUnlock` to `no` afterwards.

## Backup and restore (Kubernetes backend)

Back up the state Secret like any other Secret (Velero, etc.) or by hand:

```sh
kubectl get secret tfstate-default-<name> -n <ns> -o jsonpath='{.data.tfstate}' | base64 -d | gzip -d > terraform.tfstate
```

Restore:

```sh
gzip -k terraform.tfstate
kubectl create secret generic tfstate-default-<name> -n <ns> --from-file=tfstate=terraform.tfstate.gz \
  --dry-run=client -o yaml | kubectl apply -f -
```

Migrating to another backend: change `spec.backendConfig`; `init` runs with
`-force-copy`, so Terraform migrates the state to the new backend on the next
run. Take a backup first.

## Working with the state from the CLI

Sometimes you need `terraform state`, `terraform import` or `terraform
taint` by hand. The Kubernetes backend works from a laptop too; the only
difference from the runner is `in_cluster_config = false`, which makes the
backend use your kubeconfig.

1. **Suspend the object** so the controller does not run concurrently:

    ```sh
    kubectl -n team-a patch tf network --type merge -p '{"spec":{"suspend":true}}'
    ```

2. **Point a working copy at the same Secret.** Clone the object's source
   and, in its `path`, add an override file (do not commit it):

    ```hcl
    # backend_override.tf
    terraform {
      backend "kubernetes" {
        secret_suffix     = "network"   # spec.backendConfig.secretSuffix, default: object name
        namespace         = "team-a"
        in_cluster_config = false
        # labels          = { … }       # only if spec.backendConfig.labels is set
      }
    }
    ```

    ```sh
    terraform init                       # or: tofu init — use the same family as status.binary
    terraform workspace select default   # only if spec.workspace is set to something else
    ```

    Your kubeconfig user needs `get`/`create`/`update` on Secrets and Leases
    in that namespace; the `argocd-terraform-controller-runner-role`
    ClusterRole is a convenient reference. Provider credentials must be in
    your shell as usual.

3. **Operate**:

    ```sh
    terraform state list
    terraform state show aws_s3_bucket.logs
    terraform import aws_s3_bucket.legacy legacy-bucket-name
    terraform state rm aws_instance.old
    terraform force-unlock <lock id>
    ```

4. **Resume**:

    ```sh
    kubectl -n team-a patch tf network --type merge -p '{"spec":{"suspend":false}}'
    ```

    The next reconcile plans against the modified state. If you changed the
    state in a way that the configuration does not reflect, that plan will
    show it — which is the point.

> [!WARNING]
> There is no local copy: the Secret *is* the state. Anything you do with the
> CLI is immediately what the controller sees. Take a backup first (above).
> And resist `terraform destroy` from the CLI unless you also delete or change
> the object — otherwise the controller recreates everything on the next run
> because the source still declares it. The GitOps way to destroy is
> `spec.destroy: true` or deleting the object with
> `destroyResourcesOnDeletion`; see [Destroy and deletion](../user-guide/destroy-and-deletion.md).

## Security notes

- Enable [encryption at rest](https://kubernetes.io/docs/tasks/administer-cluster/encrypt-data/)
  for Secrets in the cluster: the state contains every attribute of every
  resource, including sensitive ones.
- Restrict who can read Secrets in namespaces hosting Terraform objects; Argo
  CD users with `applications, get` see the Secret's existence but not its data.
- The runner only has permissions in its own namespace. Give it cloud
  credentials with the least privilege the configuration needs.

## Plan storage

Plans are separate from state: the binary plan is gzip-encoded in Secret(s)
`tfplan-<workspace>-<name>` (chunked into `-0`, `-1`, … when larger than 1 MiB),
with the plan ID in the `savedPlan` annotation. `spec.storeReadablePlan: human`
(default) also writes the `terraform show` text to ConfigMap
`tfplan-<workspace>-<name>-readable` for the UI; `json` writes the JSON plan to a
Secret; `none` stores only the binary plan. Plan objects are owned by the
Terraform object and are deleted with it.
