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
| `argocd-terraform-controller-helmchart-<ver>.tgz` | Helm chart equivalent of `install.yaml`, plus the runner identity for `runner.namespaces`. Also published as an OCI chart at `oci://quay.io/redhatworkshops/argocd-terraform-controller-helmchart` (tag `<ver>`) | wherever you install it |
| `argocd-cm-patch.yaml` | Health assessment and resource actions (Lua) for `infra.argocd.io/Terraform` | ConfigMap `argocd-cm` |
| `argocd-server-extension-patch.yaml` | Init container that downloads the UI extension into `argocd-server` | Deployment `argocd-server` |
| `argocd-terraform-extension.tar.gz`, `checksums.txt` | The UI extension bundle; fetched by the init container above, not applied by hand | — |

The **Helm chart installs all of it** — controller, runner identity and the
Argo CD integration — and is the recommended path. The other assets exist for
people who prefer plain manifests or need to apply the pieces separately.

## Helm (recommended)

```sh
helm install argocd-terraform-controller \
  oci://quay.io/redhatworkshops/argocd-terraform-controller-helmchart --version ${VERSION#v} \
  --namespace argocd-terraform-controller-system --create-namespace \
  --set runner.namespaces="{team-a,team-b}" \
  --wait
```

What happens:

1. **Preflight.** At render time the chart looks up the Argo CD namespace
   (`argocd.namespace`, default `argocd`). If it is missing the install
   fails before touching anything, with the three ways out printed in the
   error (`--set argocd.namespace=…`, `--set argocd.integration.required=false`,
   or disable the integration). `helm template` and `helm lint` are
   unaffected — the check only runs against a live cluster.
2. **Controller.** CRD, RBAC, Deployment in the release namespace.
3. **Runner identity.** For every namespace in `runner.namespaces` (plus the
   release namespace): the namespace itself if missing
   (`runner.createNamespaces`, kept on uninstall), the `tf-runner`
   ServiceAccount and its RoleBinding. `runner.namespaces` is deliberately
   not defaulted — you decide where Terraform may run.
4. **Argo CD integration** (post-install/post-upgrade hook Job, because
   `argocd-cm` and `argocd-server` belong to Argo CD and cannot be rendered
   by this chart): verifies the ConfigMap and Deployment exist, merges the
   health/actions Lua into `argocd-cm`, adds the `argocd-extension-installer`
   init container to `argocd-server` — detecting the server container name,
   `argocd-server` or `server` — and waits for the rollout.
5. **Repository credentials.** A Role in the Argo CD namespace that lets the
   controller reuse Argo CD's repository Secrets
   (`spec.source.git.useArgoCDRepoCredentials`).

`helm uninstall` runs the same hook in reverse (`argocd.integration.cleanupOnUninstall`):
the Lua keys and the init container are removed and Argo CD is back to how it
was.

### Knobs

Everything has a default that works with a stock Argo CD in `argocd`; override
only what differs in your environment.

| Value | Default | Purpose |
|---|---|---|
| `runner.namespaces` | `[]` | **Required in practice.** Namespaces that may host Terraform objects |
| `runner.createNamespaces` | `true` | Create missing runner namespaces (annotated `helm.sh/resource-policy: keep`) |
| `runner.extraClusterRoles` | `[]` | Extra ClusterRoles to bind to every `tf-runner` (e.g. for the Kubernetes provider) |
| `argocd.namespace` | `argocd` | Where Argo CD lives |
| `argocd.integration.enabled` | `true` | Do the Argo CD integration at all |
| `argocd.integration.required` | `true` | Fail when Argo CD is not found; `false` installs the controller alone and warns |
| `argocd.integration.cleanupOnUninstall` | `true` | Revert the patches on `helm uninstall` |
| `argocd.integration.customizations.enabled` / `.configMapName` | `true` / `argocd-cm` | Health + actions Lua |
| `argocd.integration.uiExtension.enabled` / `.deploymentName` | `true` / `argocd-server` | UI extension |
| `argocd.integration.uiExtension.containerName` | `""` (auto) | Server container to mount into; auto-detects the first container |
| `argocd.integration.uiExtension.version` | chart `appVersion` | Release to download the extension from |
| `argocd.integration.uiExtension.extensionURL` / `.checksumURL` | derived | Full URL overrides for mirrors / air-gapped clusters |
| `argocd.integration.uiExtension.installer.image` | pinned `argocd-extension-installer` | The init container image |
| `argocd.integration.uiExtension.installer.waitForRollout` / `.rolloutTimeout` | `true` / `5m` | Wait for `argocd-server` after patching |
| `argocd.integration.hook.image` | pinned `alpine/k8s` | Image of the hook Job (needs `sh`, `kubectl`, `jq`) |
| `argocd.integration.hook.*` | sane | `backoffLimit`, `activeDeadlineSeconds`, `ttlSecondsAfterFinished`, `resources`, `nodeSelector`, `tolerations`, `podAnnotations` |
| `argocd.repoCredentials.enabled` | `true` | Role to read Argo CD repository Secrets |
| `controller.*` | see `values.yaml` | `concurrent`, `replicas`, `logLevel`, `extraArgs`, `resources`, scheduling … ([Controller configuration](controller-configuration.md)) |
| `image.tag`, `runner.image.tag` | chart `appVersion` | Pin images independently |
| `metrics.*` | enabled, HTTPS | Metrics Service and optional ServiceMonitor ([Metrics](metrics.md)) |

Using the **Argo CD Helm chart** and preferring to keep the integration in
its values? Set `argocd.integration.enabled=false` and copy the two
`resource.customizations.*` keys from the release's `argocd-cm-patch.yaml`
into `configs.cm`, and the init container / volume from
`argocd-server-extension-patch.yaml` into `server.initContainers`,
`server.volumeMounts`, `server.volumes`.

The chart tarball attached to each release
(`argocd-terraform-controller-helmchart-<ver>.tgz`) is the same chart for
air-gapped installs — mirror it and the hook image first.

## Without Helm

The same pieces, applied by hand.

### 1. Controller

From the release manifest:

```sh
kubectl apply -f ${RELEASE}/install.yaml
kubectl -n argocd-terraform-controller-system rollout status deploy/argocd-terraform-controller-controller-manager
```

The Deployment carries two environment variables you may want to change:
`RUNNER_POD_IMAGE` (the runner image containing `terraform` and `tofu`) and
`ARGOCD_NAMESPACE` (defaults to `argocd`; used to reuse Argo CD repository
credentials).

Or with Kustomize:

```sh
kubectl apply -k "https://github.com/redhat-developer/argocd-terraform-controller/config/default?ref=${VERSION}"
```

The kustomize base in Git references the `latest` image tags; pin them in
your overlay (`images:` for the manager, the `RUNNER_POD_IMAGE` env for the
runner) or use `install.yaml`, which the release pipeline already pins.

### 2. Runner identity per namespace

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

### 3. Argo CD

Configuration only; nothing in Argo CD is rebuilt. Details and the RBAC for
users are in [Argo CD integration](argocd-integration.md).

```sh
kubectl patch configmap argocd-cm -n argocd --type merge \
  --patch "$(curl -fsSL ${RELEASE}/argocd-cm-patch.yaml)"

# The server-extension patch mounts the extension into the argocd-server
# container. Its name depends on the install method (upstream install.yaml:
# "argocd-server"; Helm chart: "server"), so detect it and rewrite the patch.
SERVER_CONTAINER=$(kubectl get deploy argocd-server -n argocd -o jsonpath='{.spec.template.spec.containers[0].name}')
curl -fsSL ${RELEASE}/argocd-server-extension-patch.yaml \
  | sed "s/- name: argocd-server/- name: ${SERVER_CONTAINER}/" > /tmp/argocd-terraform-server-patch.yaml
kubectl patch deployment argocd-server -n argocd --patch-file /tmp/argocd-terraform-server-patch.yaml
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

> [!NOTE]
> The server-extension patch merges into the server container by name. The
> upstream `install.yaml` names it `argocd-server`; the Argo CD **Helm chart**
> names it `server`. If the name does not match, strategic merge appends a
> container with no image and the API rejects the patch with
> `spec.template.spec.containers[0].image: Required value`. The `SERVER_CONTAINER`
> detection above handles both.

## Upgrading

With Helm, one command upgrades everything and the hook re-applies the Argo CD
integration (re-pinning the UI extension to the new release and rolling
`argocd-server`):

```sh
helm upgrade argocd-terraform-controller \
  oci://quay.io/redhatworkshops/argocd-terraform-controller-helmchart --version ${VERSION#v} \
  --namespace argocd-terraform-controller-system --reuse-values --wait
```

Hard-refresh the Argo CD UI afterwards (browsers cache `extensions.js`).
Rolling update; Terraform objects and state are untouched. CRD updates are
additive within `v1alpha1`.

Without Helm, releases are independent per component; read the release notes
and apply only the assets that changed:

| Changed in the release | Run |
|---|---|
| Controller, CRD, RBAC, runner (Go code, API types) | `kubectl apply -f ${RELEASE}/install.yaml` |
| Health or actions (`config/argocd/lua`) | The `argocd-cm` patch above. Argo CD reloads it live — no restart. |
| UI extension (`ui/extension`) | The `argocd-server` patch above. Re-applying it re-pins the download URL to the new release **and** triggers a rollout; a plain `rollout restart` would re-download the *old* bundle. |

## Uninstalling

Delete the `Terraform` objects (or the Argo CD Applications that own them)
**first** and wait for them to go: destroys run through the controller's
finalizer, so the controller must still be present. State Secrets are kept
unless `spec.tfstate.deleteOnDestroy` was set; see [State](state.md).

```sh
helm uninstall argocd-terraform-controller -n argocd-terraform-controller-system
```

The pre-delete hook removes the Lua keys from `argocd-cm` and the init
container from `argocd-server` (unless `argocd.integration.cleanupOnUninstall=false`).
Runner namespaces created by the chart are kept; delete them yourself. The
CRD is kept too (Helm never deletes CRDs) — `kubectl delete crd terraforms.infra.argocd.io`
removes it and every remaining `Terraform` object with it.

Without Helm: `kubectl delete -f ${RELEASE}/install.yaml`, then revert the
Argo CD patches by hand if wanted (remove the
`resource.customizations.*.infra.argocd.io_Terraform` keys from `argocd-cm`
and the `extension-terraform` init container, `extensions` volume and mount
from `argocd-server`). They are inert without the CRD.
