# Argo CD UI extension

A [UI extension](https://argo-cd.readthedocs.io/en/stable/developer-guide/extensions/ui-extensions/)
that adds:

- a **Terraform** sidebar entry (`src/dashboard`): a dashboard of all Terraform
  objects across Applications, with a per-object graph of the source, the
  Kubernetes objects around it and every resource the state manages (from
  `status.inventory`, Kubernetes or not), plan output, outputs, conditions,
  events and the action buttons;
- a **Terraform** tab on every `infra.argocd.io/Terraform` resource with the
  status, revisions, binary, human readable plan, outputs, inventory and
  conditions, plus *Approve plan*, *Re-plan*, *Suspend/Resume* and *Force
  unlock* buttons;
- a **Terraform** application view listing all Terraform objects of an
  Application and the ones waiting for approval.

The extension talks only to the Argo CD API server (`/api/v1/applications/…/resource`
and `…/resource/actions`), so it is subject to Argo CD RBAC and needs no
backend. The buttons run the resource actions defined in
[`config/argocd/lua`](../../config/argocd/lua), which must be installed in
`argocd-cm` (see [`config/argocd`](../../config/argocd)).

## Build

Node.js is not required on the host; everything runs in a `node:22` container:

```sh
make ui-test          # type-check, lint, unit tests
make ui-build         # ui/extension/dist/extension-terraform.js
make build-extension  # dist/argocd-terraform-extension.tar.gz + checksums.txt
make ui-shell         # a shell in the container, e.g. to add dependencies
```

Or with Docker only: `docker build -f ui/extension/Dockerfile -o ui/extension/dist ui/extension`.

## Install

Releases ship `argocd-terraform-extension.tar.gz` (containing
`resources/extension-terraform.js`, the layout the installer expects) and
`checksums.txt`, consumed by the
[argocd-extension-installer](https://github.com/argoproj-labs/argocd-extension-installer)
init container in [`config/argocd/argocd-server-extension-patch.yaml`](../../config/argocd/argocd-server-extension-patch.yaml).

For a quick local test copy the file into the server Pod:

```sh
kubectl cp ui/extension/dist/extension-terraform.js \
  argocd-server-xxx:/tmp/extensions/resources/extension-terraform.js -n argocd
kubectl rollout restart deployment/argocd-server -n argocd
```

## Development

`make ui-shell` then `npm run dev` rebuilds on change. Argo CD only loads
extensions at startup, so restart `argocd-server` (or re-copy the file) to
pick up changes.
