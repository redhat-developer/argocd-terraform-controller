# API reference: `infra.argocd.io/v1alpha1` Terraform

Short names: `tf`. Namespaced. Printer columns: Ready, Plan (pending), Status, Age.

## spec

### Source and schedule

| Field | Type | Description |
|---|---|---|
| `source.git` | object | Git source: `url`, `ref` (`branch`, `tag`, `semver`, `name`, `commit`), `secretRef`, `useArgoCDRepoCredentials`, `ignore`, `recurseSubmodules`. |
| `source.oci` | object | OCI source: `url` (`oci://…`), `ref` (`tag`, `semver`, `digest`), `secretRef`, `provider`, `insecure`. |
| `source.interval` | duration | Source poll interval. Defaults to `interval`. |
| `path` | string | Directory with the `.tf` files, relative to the source root. |
| `interval` | duration | **Required.** Reconcile / drift-detection interval. |
| `retryInterval` | duration | Requeue after a failure (default `15s`). |
| `retryStrategy` | `StaticInterval` \| `ExponentialBackoff` | Backoff `2^failures × retryInterval`, capped by `maxRetryInterval` (default 24h). |
| `remediation.retries` | int | Attempts before `Stalled`; 0 none, negative unlimited. |
| `suspend` | bool | Pause reconciliation. |
| `force` | bool | Unconditionally re-plan and apply. |

### Plan and apply

| Field | Type | Description |
|---|---|---|
| `approvePlan` | string | `""` manual, `auto`, `disable`, or a plan ID (prefix match). |
| `planOnly` | bool | Never apply. |
| `destroy` | bool | Plan a destroy. |
| `destroyResourcesOnDeletion` | bool | Destroy when the object is deleted. |
| `disableDriftDetection` | bool | Skip the refresh-only plan between changes. |
| `workspace` | string | Terraform workspace (default `default`). |
| `targets` | []string | `-target` addresses. |
| `parallelism` | int | Apply parallelism (0 = default). |
| `refreshBeforeApply` | bool | `-refresh=true` on apply. |
| `upgradeOnInit` | bool | `init -upgrade` (default true). |
| `storeReadablePlan` | `none` \| `json` \| `human` | Readable plan copy (default `human`, in a ConfigMap). |
| `enableInventory` | bool | Record applied resources in `status.inventory`. |

### Inputs

| Field | Type | Description |
|---|---|---|
| `vars[]` | `{name, value \| valueFrom}` | Variables; `valueFrom` supports `secretKeyRef` / `configMapKeyRef`. |
| `varsFrom[]` | `{kind, name, varsKeys, optional}` | All (or selected, `key:newName`) keys of a Secret/ConfigMap as variables. Later entries win. |
| `values` | JSON | Arbitrary object bound to variable `values`. |
| `tfVarsFiles[]` | []string | `-var-file` paths relative to the source root. |
| `fileMappings[]` | `{secretRef{name,key}, location: home \| workspace, path}` | Files written into the runner. |
| `cliConfigSecretRef` | SecretReference | Secret with one `*.tfrc` key. |

### State

| Field | Type | Description |
|---|---|---|
| `backendConfig` | object | `disable`, `secretSuffix`, `inClusterConfig`, `configPath`, `customConfiguration` (verbatim backend block), `labels`. |
| `backendConfigsFrom[]` | `{kind, name, keys, optional}` | `-backend-config key=value` from Secrets/ConfigMaps. |
| `cloud` | object | Terraform Cloud/Enterprise: `organization`, `hostname`, `token`, `workspaces{name \| tags}`. |
| `tfstate` | object | `forceUnlock` (`no`/`yes`/`auto`), `lockIdentifier`, `lockTimeout`, `deleteOnDestroy`. |

### Outputs, health and dependencies

| Field | Type | Description |
|---|---|---|
| `writeOutputsToSecret` | `{name, outputs[], labels, annotations}` | Outputs Secret; `outputs` selects/renames (`output:key`). Non-string outputs are JSON with a `<key>__type` companion. |
| `healthChecks[]` | `{name, type: tcp \| http, address \| url, timeout}` | Checks after apply; Go templates over output values. |
| `dependsOn[]` | `{name, namespace}` | Terraform objects that must be Ready first; they cannot be deleted while this object exists. |

### Runner

| Field | Type | Description |
|---|---|---|
| `runner.binary` | `auto` \| `terraform` \| `tofu` | Binary to execute (default `auto`). |
| `runner.image` | string | Runner image override. |
| `runner.serviceAccountName` | string | Default `tf-runner`. |
| `runner.alwaysCleanup` | bool | Delete the Pod after each run (default true). |
| `runner.terminationGracePeriodSeconds` | int | Default 30. |
| `runner.podTemplate` | object | `metadata{labels, annotations}`, `spec{env, envFrom, volumes, volumeMounts, initContainers, nodeSelector, affinity, tolerations, hostAliases, priorityClassName, securityContext, resources}`. |

## status

| Field | Description |
|---|---|
| `conditions[]` | `Ready`, `Reconciling`, `Stalled`, `Plan`, `Apply`, `Output`, `HealthCheck`, `StateLocked`. |
| `observedGeneration`, `lastHandledReconcileAt` | Bookkeeping; the latter mirrors the `infra.argocd.io/reconcile` annotation. |
| `lastAttemptedRevision`, `lastPlannedRevision`, `lastAppliedRevision` | Source revisions (`<ref>@sha1:<sha>`). |
| `lastPlanAt`, `lastSuccessfulReconcileAt`, `lastDriftDetectedAt`, `lastAppliedByDriftDetectionAt` | Timestamps. |
| `plan` | `pending`, `lastApplied`, `isDestroyPlan`, `isDriftDetectionPlan`. |
| `lock` | `pending`, `lastApplied` lock identifiers. |
| `availableOutputs[]` | Output names of the last run. |
| `inventory.entries[]` | `{n, t, id}` when `enableInventory`. |
| `reconciliationFailures` | Consecutive failures. |
| `binary` | Binary and version used, e.g. `tofu 1.13.1`. |
| `stateSecretName` | Kubernetes backend state Secret, when used. |
| `planConfigMapName` | ConfigMap with the human readable plan. |

## Annotations

| Annotation | Effect |
|---|---|
| `infra.argocd.io/reconcile: <value>` | A new value triggers an immediate reconcile; with an unapproved pending plan it re-plans. |

## Labels on created objects

| Object | Labels / annotations |
|---|---|
| Runner Pod `<name>-tf-runner` | `app.kubernetes.io/name=tf-runner`, `infra.argocd.io/terraform=<ns>`, `infra.argocd.io/tls-secret-name` |
| Plan Secrets/ConfigMaps | `infra.argocd.io/plan-name`, `infra.argocd.io/plan-workspace`; annotations `savedPlan`, `encoding: gzip`, `infra.argocd.io/plan-chunk` |
| Runner TLS Secret `terraform-runner.tls-<unix>` | `infra.argocd.io/runner-tls=true` |
