# Argo CD integration

Everything here is configuration of a stock Argo CD; no Argo CD code or image
changes are involved. The pieces live in [`config/argocd`](../config/argocd)
and are packaged as a Kustomize **Component**.

## Install

As a component of your Argo CD kustomization:

```yaml
# kustomization.yaml
namespace: argocd
resources:
- https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
components:
- https://github.com/redhat-developer/argocd-terraform-controller/config/argocd?ref=main
```

Or patch an existing installation:

```sh
kubectl patch configmap argocd-cm -n argocd --type merge --patch-file config/argocd/argocd-cm-patch.yaml
kubectl patch deployment argocd-server -n argocd --patch-file config/argocd/argocd-server-extension-patch.yaml
kubectl apply -n argocd -f config/argocd/argocd_repo_reader_binding.yaml
```

With the Argo CD Helm chart, copy the two `resource.customizations.*` keys of
`argocd-cm-patch.yaml` into `configs.cm` and the init container / volume of the
server patch into `server.initContainers`, `server.volumeMounts`, `server.volumes`.

## Health assessment

[`config/argocd/lua/health.lua`](../config/argocd/lua/health.lua) maps a
Terraform object to an Argo CD health:

| Health | When |
|---|---|
| Healthy | `Ready=True` — applied, no drift, outputs written (or plan-only with a plan) |
| Progressing | no status yet, initialising, planning, applying, dependency not ready |
| Suspended | `spec.suspend`, or a plan is waiting for manual approval |
| Degraded | the last run failed, drift detected, state locked, stalled |

*Suspended* holds up later [sync waves](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-waves/),
so an Application can create a database with Terraform in wave 0 and deploy
the workload that needs it in wave 1; the deployment waits for the approval
and the apply.

## Resource actions

[`config/argocd/lua/actions`](../config/argocd/lua/actions) defines actions
available from the resource menu, the Argo CD CLI (`argocd app actions run`)
and the UI extension buttons:

| Action | Effect |
|---|---|
| `approve-plan` | `spec.approvePlan = status.plan.pending` |
| `replan` | sets the `infra.argocd.io/reconcile` annotation; the controller reconciles immediately and discards an unapproved pending plan |
| `suspend` / `resume` | toggles `spec.suspend` |
| `force-unlock` | `spec.tfstate.forceUnlock = auto` (only enabled while `status.lock.pending` is set) |

### GitOps considerations

Actions mutate the live object. If the Application has automated sync with
`selfHeal`, Argo CD reverts the change on the next sync — which is often the
intended "approvals must come from git" policy. To allow approving from the UI
while keeping the object in git, ignore those fields:

```yaml
spec:
  ignoreDifferences:
  - group: infra.argocd.io
    kind: Terraform
    jsonPointers:
    - /spec/approvePlan
    - /spec/suspend
    - /spec/tfstate/forceUnlock
    - /metadata/annotations/infra.argocd.io~1reconcile
  syncPolicy:
    syncOptions:
    - RespectIgnoreDifferences=true
```

Approving from git is simply committing `spec.approvePlan: <plan id>`; the ID
is in the Ready condition message, the `TerraformPlanSucceed` event and the UI.

## UI extension

The extension ([`ui/extension`](../ui/extension)) registers three things:

- a **Terraform** entry in the sidebar (system-level extension at
  `/extensions/terraform`): a dashboard of every Terraform object across all
  Applications you can see — counts by state, search and filters, and a detail
  page per object with a **graph** from the source, through the Terraform
  object and its Kubernetes objects (state, plan, outputs, runner), to every
  resource the state manages — including non-Kubernetes resources such as
  VPCs, buckets or databases, grouped by provider and type with their IDs
  (from `status.inventory`, recorded after each apply when
  `spec.enableInventory` is true, the default). Plan output, outputs,
  conditions, recent events and the action buttons are on the same page.
- a **Terraform** tab on `infra.argocd.io/Terraform` resources inside an
  Application;
- a **Terraform** tab in the Application details panel (next to Summary,
  Parameters, Manifest and Events) and a **Terraform** application view in the
  toolbar, both listing the Terraform objects of one Application.

Everything is read through the Argo CD API (`/api/v1/applications`,
`…/{app}/resource`, `…/{app}/resource-tree`, `…/{app}/events`) and actions run
through `POST …/{app}/resource/actions`; all of it is authorised by Argo CD RBAC
(`applications, get` and `applications, action/infra.argocd.io/Terraform/<action>`).
No backend service and no proxy extension are needed. Consequently the
dashboard shows Terraform objects that are managed by an Argo CD Application;
objects applied outside of Argo CD are not listed.

The human readable plan is stored in a **ConfigMap** (`spec.storeReadablePlan: human`,
the default) because Argo CD redacts Secret data in its API. The ConfigMap is
owned by the Terraform object and therefore appears in the Application tree.

The extension is installed by the
[argocd-extension-installer](https://github.com/argoproj-labs/argocd-extension-installer)
init container in `argocd-server-extension-patch.yaml`, downloading
`argocd-terraform-extension.tar.gz` from a GitHub release. The application view
appears when the Application's resources include a Terraform object or when the
Application has the label `infra.argocd.io/has-terraform: "true"`; the details
panel tab is always present (Argo CD has no per-Application filter for resource
tabs) and says so when the Application manages no Terraform objects.

## Reusing Argo CD repository credentials

Set `spec.source.git.useArgoCDRepoCredentials: true` to let the controller look
up the credentials Argo CD already has for the URL (Secrets labelled
`argocd.argoproj.io/secret-type: repository` or `repo-creds`, plus
`argocd-ssh-known-hosts-cm` for SSH). This requires:

- the controller flag `--argocd-namespace` (set from `ARGOCD_NAMESPACE`), and
- read access to Secrets/ConfigMaps in that namespace
  (`config/argocd/argocd_repo_reader_binding.yaml`, or `argocd.repoCredentials.enabled`
  in the Helm chart).

## RBAC for users

Argo CD users approve plans through the resource action, so grant it in the
Argo CD RBAC policy:

```csv
p, role:platform, applications, action/infra.argocd.io/Terraform/approve-plan, */*, allow
p, role:platform, applications, action/infra.argocd.io/Terraform/replan, */*, allow
```
