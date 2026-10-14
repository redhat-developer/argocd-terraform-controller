# Getting started

## Prerequisites

- Kubernetes 1.28+ and Argo CD 2.12+ (3.x recommended for the UI extension).
- Cluster network access from the controller to your Git/OCI sources and from
  runner Pods to the provider registries your configuration uses.

## 1. Install the controller

With the release manifest:

```sh
kubectl apply -f https://github.com/redhat-developer/argocd-terraform-controller/releases/latest/download/install.yaml
```

With Helm:

```sh
helm install argocd-terraform-controller charts/argocd-terraform-controller \
  --namespace argocd-terraform-controller-system --create-namespace \
  --set runner.namespaces='{team-a,team-b}'
```

From source:

```sh
make deploy IMG=quay.io/redhatworkshops/argocd-terraform-controller-manager:latest
```

The controller reads two environment variables on its Deployment:

| Variable | Purpose |
|---|---|
| `RUNNER_POD_IMAGE` | Default runner image (contains `terraform` and `tofu`). |
| `ARGOCD_NAMESPACE` | Namespace of Argo CD, for `spec.source.git.useArgoCDRepoCredentials`. |

## 2. Create the runner identity per namespace

Runner Pods run as the `tf-runner` ServiceAccount of the Terraform object's
namespace and need to write Secrets, ConfigMaps and Leases there (state, plans,
outputs, locks):

```sh
make deploy-runner-rbac NAMESPACE=team-a
# or
kustomize build config/runner | kubectl apply -n team-a -f -
```

If your configuration uses the `kubernetes` provider with the in-cluster
ServiceAccount, grant `tf-runner` the additional permissions it needs (see
[examples/kube-namespace](../examples/kube-namespace)). Cloud providers are
usually authenticated with credentials passed through `spec.runner.podTemplate.spec.env`,
`envFrom`, workload identity (IRSA, Workload Identity, …) or `spec.fileMappings`.

## 3. Integrate with Argo CD

See [argocd-integration.md](argocd-integration.md). At minimum apply the
`argocd-cm` patch so Argo CD understands the health of Terraform objects:

```sh
make deploy-argocd ARGOCD_NAMESPACE=argocd
```

## 4. Create a Terraform object

Commit a Terraform object to a repository that an Argo CD Application syncs:

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: hello
  namespace: team-a
spec:
  source:
    git:
      url: https://github.com/redhat-developer/argocd-terraform-controller
      ref:
        branch: main
  path: ./examples/kube-namespace
  interval: 10m
  approvePlan: auto
  runner:
    binary: tofu
  writeOutputsToSecret:
    name: hello-outputs
  destroyResourcesOnDeletion: true
```

Watch it progress:

```sh
kubectl get terraform -n team-a -w
kubectl get terraform hello -n team-a -o jsonpath='{.status.conditions}' | jq
kubectl get secret tfstate-default-hello hello-outputs -n team-a
```

In the Argo CD UI, open the Application, click the Terraform resource and the
**Terraform** tab, or switch to the **Terraform** application view.

## What happens on each reconciliation

1. The source is resolved (`git ls-remote` / registry HEAD) and fetched when
   the revision changed; the tree is archived and shipped to the runner.
2. A runner Pod `<name>-tf-runner` is created (or reused) in the object's
   namespace and connected over mTLS.
3. The backend configuration, variables and files are generated; `init` and
   workspace selection run.
4. If nothing changed since the last apply, a refresh-only plan detects drift.
5. Otherwise a plan is produced and stored in a Secret (plus a readable copy
   in a ConfigMap). With manual approval the object waits here.
6. The approved plan is applied, outputs are written, health checks run.
7. The runner Pod is deleted (`spec.runner.alwaysCleanup`) and the object is
   requeued after `spec.interval`.
