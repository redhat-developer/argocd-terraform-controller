# Outputs

Terraform outputs are how a stack hands values to whatever comes next: a
database endpoint to a Deployment, a VPC ID to another stack, a URL to a
health check. The controller records output **names** on the object and, when
asked, writes the **values** to a Secret.

## Names: `status.availableOutputs`

After every successful plan/apply the sorted list of output names is stored in
`status.availableOutputs` and shown in the UI (Terraform tab, dashboard). Only
names — values never appear in the object or in Argo CD.

```sh
kubectl -n team-a get tf network -o jsonpath='{.status.availableOutputs}{"\n"}'
```

## Values: `spec.writeOutputsToSecret`

```yaml
spec:
  writeOutputsToSecret:
    name: network-outputs           # Secret in the object's namespace
    outputs:                        # optional: select and/or rename
      - vpc_id
      - db_endpoint:DB_HOST         # output "db_endpoint" stored under key "DB_HOST"
    labels:
      team: platform
    annotations:
      reloader.stakater.com/match: "true"
```

- Without `outputs`, **all** outputs are written, keyed by output name.
- With `outputs`, only the listed ones, optionally renamed with
  `output:key` — useful to produce `UPPER_CASE` keys for `envFrom`.
- The Secret is **owned by the Terraform object**: it appears in the Argo CD
  resource tree, is deleted with the object, and is recreated if removed
  (see [Drift detection › Outputs drift](drift-detection.md#outputs-drift)).
- Sensitive outputs are written like any other — the Secret is the boundary.

### Value encoding

| Output type | Secret key | Stored as |
|---|---|---|
| `string` | `<key>` | the raw string |
| anything else (`number`, `bool`, `list`, `map`, `object`, …) | `<key>` **and** `<key>__type` | JSON value, plus the cty type description so consumers can decode it |

Example for `output "subnets" { value = ["subnet-a", "subnet-b"] }`:

```sh
kubectl -n team-a get secret network-outputs -o jsonpath='{.data.subnets}' | base64 -d
# ["subnet-a","subnet-b"]
kubectl -n team-a get secret network-outputs -o jsonpath='{.data.subnets__type}' | base64 -d
# ["list","string"]
```

A `string` output never gets a `__type` key, so `envFrom` consumers only see
the keys they expect. Convert non-string outputs to strings in Terraform
(`jsonencode(...)`, `join(",", ...)`) when a plain value is needed.

## Reading values

```sh
# all keys, decoded
kubectl -n team-a get secret network-outputs \
  -o go-template='{{range $k,$v := .data}}{{$k}}={{$v | base64decode}}{{"\n"}}{{end}}'

# one value
kubectl -n team-a get secret network-outputs -o jsonpath='{.data.db_endpoint}' | base64 -d; echo
```

Or, with the backend override described in
[State › Working with the state from the CLI](../operator-manual/state.md#working-with-the-state-from-the-cli):
`terraform output -json`.

## Consuming outputs

### In a workload, through sync waves

Because an unapproved plan makes the Terraform object **Suspended** and a
failed one **Degraded**, Argo CD sync waves let a Deployment wait for the
infrastructure it needs:

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: database
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  # …
  writeOutputsToSecret:
    name: database-outputs
    outputs:
      - endpoint:DB_HOST
      - port:DB_PORT
      - username:DB_USER
      - password:DB_PASSWORD
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  annotations:
    argocd.argoproj.io/sync-wave: "1"
spec:
  template:
    spec:
      containers:
        - name: api
          envFrom:
            - secretRef:
                name: database-outputs
```

Argo CD creates the Deployment only after the Terraform object is Healthy —
i.e. after the apply wrote `database-outputs`. If the outputs change later
(a new endpoint after a replacement), a Secret reloader such as
[Reloader](https://github.com/stakater/Reloader) restarts the consumers; the
controller does not.

### In another Terraform object

Use `spec.dependsOn` to order the stacks and `spec.varsFrom` to read the
Secret:

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: app-cluster
spec:
  dependsOn:
    - name: network                # must be Ready, and its outputs Secret must exist
  varsFrom:
    - kind: Secret
      name: network-outputs        # every key becomes a variable: vpc_id, subnets, …
      varsKeys:
        - vpc_id
        - subnets                  # arrives as the JSON text '["subnet-a","subnet-b"]'
  # …
```

`varsFrom` passes every Secret/ConfigMap value to Terraform as a **string**.
String outputs therefore round-trip unchanged; non-string outputs arrive as
their JSON text, so decode them in the consuming configuration:

```hcl
variable "subnets" { type = string }
locals { subnets = jsondecode(var.subnets) }
```

(Alternatively keep complex values as strings on the producing side with
`jsonencode`, which makes the intent explicit in both stacks.) See
[Dependencies and health checks](dependencies-and-health-checks.md) for the
ordering rules.

### In health checks

`spec.healthChecks` templates can reference outputs by name — `{{ .url }}`,
`{{ .host }}:{{ .port }}` — when `writeOutputsToSecret` is set. See
[Dependencies and health checks](dependencies-and-health-checks.md#health-checks-spechealthchecks).

## Failure modes

| Symptom | Cause |
|---|---|
| `OutputsWritingFailed`: "output X not found" | `outputs` lists a name the configuration does not declare (check `status.availableOutputs`) |
| `OutputsWritingFailed`: invalid label | `labels`/`annotations` values must be valid Kubernetes label values |
| Secret exists but is empty | The configuration has no outputs, or `spec.destroy: true` (outputs are not written on destroy) |
| Secret keeps reappearing after deletion | Expected — it is owned and reconciled; remove `writeOutputsToSecret` instead |
