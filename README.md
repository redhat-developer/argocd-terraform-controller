# Argo CD Terraform Controller

Run Terraform and OpenTofu from Argo CD, GitOps style, with plan approval, drift
detection and a UI extension. It is an **add-on**: no changes to Argo CD are
required. The design follows the Flux [tofu-controller](https://github.com/flux-iac/tofu-controller).

```
Argo CD Application ──sync──▶ Terraform (infra.argocd.io)  ◀── UI extension / health / actions
                                   │
                                   ▼ controller: fetch source, plan, approve, apply, drift
                                   │ gRPC + mTLS
                                   ▼
                              runner Pod: tofu | terraform  ──▶ state Secret, plan, outputs
```

## Features

- **Terraform object** (`infra.argocd.io/v1alpha1`) describing a Git or OCI source,
  variables, backend, approval mode, outputs and health checks. Argo CD syncs the
  object; the controller does the rest.
- **Terraform and OpenTofu** in one runner image; choose per object with
  `spec.runner.binary: auto | terraform | tofu`.
- **Plan / approve / apply** with manual approval by plan ID (from git, `kubectl`
  or the Argo CD UI), `auto` approval, plan-only and drift-detection-only modes.
- **Drift detection** on every interval, with automatic remediation in `auto` mode.
- **State** in a Kubernetes Secret through Terraform's `kubernetes` backend by
  default, or any other backend (`s3`, `gcs`, `azurerm`, …) and Terraform Cloud.
- **Outputs to a Secret**, health checks, `dependsOn` between stacks, destroy on
  deletion (optionally deleting the state too), state force-unlock.
- **Argo CD integration** through supported extension points only: health
  assessment and resource actions in `argocd-cm`, and a UI extension with a
  *Terraform* tab (plan output, approve button) and an application view.
- **Isolation**: the controller holds source credentials and talks to a
  per-object runner Pod over mTLS; the runner only executes the binary with its
  own ServiceAccount.

## Quick start

Install the controller (CRD, RBAC, Deployment):

```sh
kubectl apply -f https://github.com/redhat-developer/argocd-terraform-controller/releases/latest/download/install.yaml
```

Create the runner identity in every namespace that will host Terraform objects:

```sh
kustomize build https://github.com/redhat-developer/argocd-terraform-controller/config/runner | kubectl apply -n my-namespace -f -
```

Integrate with Argo CD (health, actions, UI extension) — see
[docs/argocd-integration.md](docs/argocd-integration.md):

```sh
kubectl patch configmap argocd-cm -n argocd --type merge --patch-file config/argocd/argocd-cm-patch.yaml
kubectl patch deployment argocd-server -n argocd --patch-file config/argocd/argocd-server-extension-patch.yaml
```

Then put a Terraform object in a repository synced by an Argo CD Application:

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: network
  namespace: my-namespace
spec:
  source:
    git:
      url: https://github.com/my-org/infra
      ref:
        branch: main
  path: ./network
  interval: 10m
  approvePlan: ""          # manual: approve in the UI or set the plan ID here
  runner:
    binary: tofu
  writeOutputsToSecret:
    name: network-outputs
```

The Application shows the object as *Suspended* while a plan waits for approval,
*Progressing* while applying and *Healthy* once applied, so later sync waves can
depend on the infrastructure.

## Documentation

- [Getting started](docs/getting-started.md)
- [Argo CD integration](docs/argocd-integration.md) — health, actions, UI extension, RBAC, GitOps considerations
- [Plan approval workflow](docs/approval-workflow.md)
- [State backends, backup and restore](docs/state.md)
- [Terraform vs OpenTofu](docs/binaries.md)
- [Sources and credentials](docs/sources.md)
- [API reference](docs/api.md)
- [Development](docs/development.md)

## Status

Alpha. The API group `infra.argocd.io/v1alpha1` may change before v1.

## License

Apache-2.0. Copyright The Argo CD Terraform Controller Authors.

Portions of this software are derived from the Flux IaC
[tofu-controller](https://github.com/flux-iac/tofu-controller), originally
created by Weaveworks Ltd. and the Flux IaC contributors, licensed under the
Apache License, Version 2.0.
