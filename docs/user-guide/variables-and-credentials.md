# Variables and credentials

Terraform runs inside a **runner Pod** in the Terraform object's namespace, not
on your machine. Everything the configuration needs — input variables and
provider credentials — has to be declared on the object so the controller can
deliver it to that Pod.

## Input variables

| Terraform CLI | Terraform object |
|---|---|
| `-var foo=bar` | `spec.vars: [{name: foo, value: bar}]` |
| `-var foo=$(cat secret)` | `spec.vars: [{name: foo, valueFrom: {secretKeyRef: {name: my-secret, key: foo}}}]` (or `configMapKeyRef`) |
| `TF_VAR_<key>` for every key of a Secret/ConfigMap | `spec.varsFrom: [{kind: Secret, name: my-secret}]`, optionally `varsKeys: [db_password, "old_key:new_name"]` |
| `-var-file env/dev.tfvars` | `spec.tfVarsFiles: [env/dev.tfvars]` (relative to the source root) |
| `-var 'values={...}'` | `spec.values: {…}`; the configuration must declare `variable "values"` |

`value` accepts any JSON type, so complex variables need no HCL quoting:

```yaml
spec:
  vars:
    - name: instance_count
      value: 3
    - name: enable_logging
      value: true
    - name: tags
      value: { team: platform, env: dev }
    - name: cidrs
      value: ["10.0.0.0/24", "10.0.1.0/24"]
    - name: db_password
      valueFrom:
        secretKeyRef:
          name: db-credentials
          key: password
  varsFrom:
    - kind: ConfigMap
      name: region-settings
    - kind: Secret
      name: api-keys
      varsKeys: ["token:api_token"]   # Secret key "token" becomes variable api_token
      optional: true
```

`spec.values`, `spec.vars` and `spec.varsFrom` are written to a single
`generated.auto.tfvars.json` in the working directory, in that order, so for
the same name **`varsFrom` overrides `vars`, which overrides `values`**.
Terraform loads `*.auto.tfvars.json` after explicit `-var-file`s, so all of
them override `spec.tfVarsFiles`.

`vars[].value` and `values` keep their JSON types (numbers, bools, lists,
maps). Everything that comes from a Secret or ConfigMap — `vars[].valueFrom`
and `varsFrom` — is a **string**; declare such variables as `string` and use
`jsondecode()`/`tonumber()`/`tobool()` in the configuration when a structured
value is encoded in it.

A variable that the configuration does not declare produces Terraform's usual
*Value for undeclared variable* warning in the plan output, not an error — handy
for checking the plumbing before the configuration lands.

## Provider credentials

The runner Pod starts with an empty home directory (`/home/runner`, UID 65532)
and no cloud credentials. Pick one of the following; none of them require
changes to the Terraform code.

### Environment variables from a Secret

The conventional Kubernetes way, and what most providers read first
(`AWS_ACCESS_KEY_ID`, `GOOGLE_CREDENTIALS`, `ARM_CLIENT_ID`, …):

```sh
kubectl -n team-a create secret generic aws-creds \
  --from-literal=AWS_ACCESS_KEY_ID=… \
  --from-literal=AWS_SECRET_ACCESS_KEY=…
```

```yaml
spec:
  runner:
    podTemplate:
      spec:
        envFrom:
          - secretRef:
              name: aws-creds
        env:
          - name: AWS_REGION
            value: us-east-1
```

### Credentials files (`spec.fileMappings`)

For providers or configurations that expect a file — an AWS shared credentials
file with a named `profile`, a kubeconfig, a service-account JSON — write a
Secret key to a path in the runner before `init`:

```sh
kubectl -n team-a create secret generic aws-credentials \
  --from-file=credentials=$HOME/.aws/credentials
```

```yaml
spec:
  fileMappings:
    - secretRef:
        name: aws-credentials      # Secret in the object's namespace
        key: credentials           # key whose value becomes the file content
      location: home               # "home" → /home/runner, "workspace" → the Terraform directory
      path: .aws/credentials       # relative to location → /home/runner/.aws/credentials
```

Files are rewritten on every run, so updating the Secret and triggering a
re-plan is enough to rotate them. Keep in mind that a file copied from a
laptop may contain *every* profile in it, and that temporary credentials
(`aws_session_token`) expire — drift detection on `spec.interval` will start
failing when they do.

### Workload identity (no stored secret)

On managed clusters, map the runner's ServiceAccount to a cloud identity and
let the SDK fetch short-lived credentials itself. The ServiceAccount is
`tf-runner` in the object's namespace, or whatever
`spec.runner.serviceAccountName` says.

- **EKS, IRSA**: annotate the ServiceAccount with
  `eks.amazonaws.com/role-arn: <role arn>`; the role's trust policy allows
  `system:serviceaccount:<namespace>:tf-runner`.
- **EKS, Pod Identity**: `aws eks create-pod-identity-association --namespace
  <namespace> --service-account tf-runner --role-arn <role arn> …`.
- **GKE Workload Identity**: annotate with `iam.gke.io/gcp-service-account`.
- **AKS Workload Identity**: annotate with `azure.workload.identity/client-id`
  and label the runner Pod with `azure.workload.identity/use: "true"` through
  `spec.runner.podTemplate.metadata.labels`.

The projected ServiceAccount token that these mechanisms inject is readable
by the runner as is: the runner container runs with `runAsUser: 65532`, and
the kubelet makes projected-volume files owned by the containers' common UID.
Keep it that way if you add `initContainers` to the runner Pod — give them
`securityContext.runAsUser: 65532` too, otherwise the token stays root-owned
and unreadable. (`podTemplate.spec.securityContext` is the *container*
security context; there is no Pod-level `fsGroup` knob.)

Leave any `profile` argument out of the provider block (or make it nullable,
`profile = var.aws_profile != "" ? var.aws_profile : null`) so the SDK falls
through to the injected identity.

### Which one?

| | Static file (`fileMappings`) | Env vars (`envFrom`) | Workload identity |
|---|---|---|---|
| Changes to Terraform code | none | none if the provider reads env | remove hardcoded `profile` |
| Rotation | update Secret, re-plan | update Secret, re-plan | automatic |
| Works on kind / on-prem | yes | yes | no (cloud-managed clusters only) |
| Recommended for | quick tests, tools that insist on a file | most setups | production on EKS/GKE/AKS |

## Kubernetes provider

Resources managed with the `kubernetes` or `helm` providers in the same cluster
need no credentials: the runner's ServiceAccount token is used
(`in_cluster_config`). Grant `tf-runner` the RBAC for whatever the
configuration manages — bind additional (Cluster)Roles in that namespace, or
use `runner.extraClusterRoles` in the Helm chart. The default
`argocd-terraform-controller-runner-role` only covers what the runner itself
needs (state, plan and output Secrets, ConfigMaps, Leases).
