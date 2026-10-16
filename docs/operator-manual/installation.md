# Installation and upgrades

Every release publishes the same set of assets on the
[GitHub releases page](https://github.com/redhat-developer/argocd-terraform-controller/releases).
Pick a version once and use it for all of them:

```sh
export VERSION=v0.0.6
export RELEASE=https://github.com/redhat-developer/argocd-terraform-controller/releases/download/${VERSION}
```

| Asset | What it installs | Lives in |
|---|---|---|
| `install.yaml` | `Terraform` CRD, controller RBAC, the controller Deployment (manager and runner images pinned to the release) | namespace `argocd-terraform-controller-system` |
| `argocd-terraform-controller-<ver>.tgz` | Helm chart equivalent of `install.yaml`, plus the runner identity for `runner.namespaces` | wherever you install it |
| `argocd-cm-patch.yaml` | Health assessment and resource actions (Lua) for `infra.argocd.io/Terraform` | ConfigMap `argocd-cm` |
| `argocd-server-extension-patch.yaml` | Init container that downloads the UI extension into `argocd-server` | Deployment `argocd-server` |
| `argocd-terraform-extension.tar.gz`, `checksums.txt` | The UI extension bundle; fetched by the init container above, not applied by hand | — |

## 1. Controller

### Manifest

```sh
kubectl apply -f ${RELEASE}/install.yaml
kubectl -n argocd-terraform-controller-system rollout status deploy/argocd-terraform-controller-controller-manager
```

The Deployment carries two environment variables you may want to change:
`RUNNER_POD_IMAGE` (the runner image containing `terraform` and `tofu`) and
`ARGOCD_NAMESPACE` (defaults to `argocd`; used to reuse Argo CD repository
credentials).

### Helm

```sh
helm install argocd-terraform-controller \
  ${RELEASE}/argocd-terraform-controller-${VERSION#v}.tgz \
  --namespace argocd-terraform-controller-system --create-namespace \
  --set runner.namespaces="{team-a,team-b}"
```

`runner.namespaces` creates the runner identity (next step) in each listed
namespace. Other useful values: `controller.concurrent`, `argocd.namespace`,
`argocd.repoCredentials.enabled`, `runner.extraClusterRoles`, `image.tag`,
`runner.image.tag`. See the chart's `values.yaml`.

### Kustomize

```sh
kubectl apply -k "https://github.com/redhat-developer/argocd-terraform-controller/config/default?ref=${VERSION}"
```

The kustomize base in Git references the `latest` image tags; pin them in
your overlay (`images:` for the manager, the `RUNNER_POD_IMAGE` env for the
runner) or use `install.yaml`, which the release pipeline already pins.

## 2. Runner identity per namespace

Runner Pods run as the `tf-runner` ServiceAccount of the Terraform object's
namespace and write Secrets, ConfigMaps and Leases there (state, plans,
outputs, locks). Create it in **every** namespace that hosts Terraform objects
(the Helm chart does this for `runner.namespaces`):

```sh
kustomize build "https://github.com/redhat-developer/argocd-terraform-controller/config/runner?ref=${VERSION}" \
  | kubectl apply -n team-a -f -
```

This binds `tf-runner` to the `argocd-terraform-controller-runner-role`
ClusterRole, which only grants what the runner itself needs. Permissions for
what the Terraform configuration *manages* are separate:

- Kubernetes provider → bind additional (Cluster)Roles to `tf-runner`, or use
  `runner.extraClusterRoles` in the chart.
- Cloud providers → see [Variables and credentials](../user-guide/variables-and-credentials.md).

## 3. Argo CD

Configuration only; nothing in Argo CD is rebuilt. Details and the RBAC for
users are in [Argo CD integration](argocd-integration.md).

```sh
kubectl patch configmap argocd-cm -n argocd --type merge \
  --patch "$(curl -fsSL ${RELEASE}/argocd-cm-patch.yaml)"

kubectl patch deployment argocd-server -n argocd \
  --patch "$(curl -fsSL ${RELEASE}/argocd-server-extension-patch.yaml)"
kubectl -n argocd rollout status deploy/argocd-server

# Optional: let the controller reuse Argo CD repository credentials
kubectl apply -n argocd -f https://raw.githubusercontent.com/redhat-developer/argocd-terraform-controller/${VERSION}/config/argocd/argocd_repo_reader_binding.yaml
```

Verify the extension landed, then hard-refresh the browser (Argo CD caches
`extensions.js`):

```sh
kubectl -n argocd exec deploy/argocd-server -- ls /tmp/extensions/resources/
# extension-terraform.js
```

## Upgrading

Releases are independent per component, so you rarely need to redo everything.
Read the release notes to see what changed, then apply only the matching
assets with the new `VERSION`:

| Changed in the release | Run |
|---|---|
| Controller, CRD, RBAC, runner (Go code, API types) | `kubectl apply -f ${RELEASE}/install.yaml` (or `helm upgrade … <new chart>`). Rolling update; Terraform objects and state are untouched. |
| Health or actions (`config/argocd/lua`) | The `argocd-cm` patch above. Argo CD reloads it live — no restart. |
| UI extension (`ui/extension`) | The `argocd-server` patch above. Re-applying it re-pins the download URL to the new release **and** triggers a rollout; a plain `rollout restart` would re-download the *old* bundle. Hard-refresh the browser afterwards. |

CRD updates are additive within `v1alpha1`; `kubectl apply` of the new
`install.yaml` is safe with existing objects.

## Uninstalling

```sh
kubectl delete -f ${RELEASE}/install.yaml          # or helm uninstall
```

Deleting the CRD deletes all `Terraform` objects. If any of them set
`spec.destroyResourcesOnDeletion: true`, the controller must still be running
for the destroy to happen — delete the objects first, wait for them to go, then
remove the controller. State Secrets are kept unless
`spec.tfstate.deleteOnDestroy` was set; see [State](state.md). The Argo CD
patches can stay (they are inert without the CRD) or be reverted by removing
the `resource.customizations.*.infra.argocd.io_Terraform` keys from
`argocd-cm` and the `extension-terraform` init container from `argocd-server`.
