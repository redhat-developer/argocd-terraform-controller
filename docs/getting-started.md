# Getting started

Everything below is copy-and-paste-able and only needs `kubectl` pointed at a
cluster that already runs Argo CD in the `argocd` namespace. Set the two
variables first.

```sh
export VERSION=v0.0.6          # release of the Argo CD Terraform Controller
export TF_NAMESPACE=team-a     # namespace that will host Terraform objects
```

## 1. Install the controller

```sh
kubectl apply -f https://github.com/redhat-developer/argocd-terraform-controller/releases/download/${VERSION}/install.yaml
kubectl -n argocd-terraform-controller-system rollout status deploy/argocd-terraform-controller-controller-manager
```

This creates the `Terraform` CRD, RBAC and the controller Deployment in
`argocd-terraform-controller-system`. The Deployment carries two environment
variables you may want to change later: `RUNNER_POD_IMAGE` (the runner image,
which contains `terraform` and `tofu`) and `ARGOCD_NAMESPACE` (defaults to
`argocd`, used to reuse Argo CD repository credentials).

Alternatively, with Helm:

```sh
helm install argocd-terraform-controller \
  https://github.com/redhat-developer/argocd-terraform-controller/releases/download/${VERSION}/argocd-terraform-controller-${VERSION#v}.tgz \
  --namespace argocd-terraform-controller-system --create-namespace \
  --set runner.namespaces="{${TF_NAMESPACE}}"
```

## 2. Create the runner identity in the Terraform namespace

Runner Pods run as the `tf-runner` ServiceAccount of the Terraform object's
namespace and need to write Secrets, ConfigMaps and Leases there (state,
plans, outputs, locks). Repeat for every namespace that hosts Terraform
objects (the Helm chart does this for `runner.namespaces`):

```sh
kubectl create namespace ${TF_NAMESPACE} --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n ${TF_NAMESPACE} -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tf-runner
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: tf-runner
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: argocd-terraform-controller-runner-role
subjects:
- kind: ServiceAccount
  name: tf-runner
EOF
```

The sample used below manages a Namespace and a ConfigMap with the Kubernetes
provider, so for this walkthrough also let `tf-runner` do that (use a narrower
ClusterRole in real deployments; cloud providers instead get credentials through
`spec.runner.podTemplate.spec.env` / `envFrom`, workload identity or `spec.fileMappings`):

```sh
kubectl create clusterrolebinding tf-runner-${TF_NAMESPACE}-sample \
  --clusterrole=cluster-admin --serviceaccount=${TF_NAMESPACE}:tf-runner
```

## 3. Integrate with Argo CD

Three configuration-only changes to a stock Argo CD (details in
[Argo CD integration](operator-manual/argocd-integration.md)):

```sh
# Health assessment and resource actions (approve-plan, replan, suspend, resume, force-unlock)
kubectl patch configmap argocd-cm -n argocd --type merge \
  --patch "$(curl -fsSL https://github.com/redhat-developer/argocd-terraform-controller/releases/download/${VERSION}/argocd-cm-patch.yaml)"

# UI extension, installed by an init container on argocd-server
kubectl patch deployment argocd-server -n argocd \
  --patch "$(curl -fsSL https://github.com/redhat-developer/argocd-terraform-controller/releases/download/${VERSION}/argocd-server-extension-patch.yaml)"
kubectl -n argocd rollout status deploy/argocd-server

# Optional: let the controller reuse Argo CD repository credentials
kubectl apply -n argocd -f https://raw.githubusercontent.com/redhat-developer/argocd-terraform-controller/${VERSION}/config/argocd/argocd_repo_reader_binding.yaml
```

Verify the extension was installed:

```sh
kubectl -n argocd exec deploy/argocd-server -- ls /tmp/extensions/resources/
# extension-terraform.js
```

Argo CD loads extensions at startup and browsers cache them; hard-refresh the
UI (Ctrl/Cmd+Shift+R) after the rollout.

## 4. Create an Application that syncs a Terraform object

The UI extension renders inside Applications, so sync the Terraform object
through Argo CD rather than applying it directly. This Application syncs the
sample from this repository:

```sh
kubectl apply -n argocd -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: terraform-sample
spec:
  project: default
  source:
    repoURL: https://github.com/redhat-developer/argocd-terraform-controller
    targetRevision: ${VERSION}
    path: config/samples
  destination:
    server: https://kubernetes.default.svc
    namespace: ${TF_NAMESPACE}
  syncPolicy:
    automated: {}
EOF
```

The sample ([config/samples/infra_v1alpha1_terraform.yaml](https://github.com/redhat-developer/argocd-terraform-controller/blob/main/config/samples/infra_v1alpha1_terraform.yaml))
fetches [examples/kube-namespace](https://github.com/redhat-developer/argocd-terraform-controller/blob/main/examples/kube-namespace) from Git, runs
it with `approvePlan: auto`, writes the outputs to the Secret
`terraform-sample-outputs` and destroys the resources when the object is deleted.

Watch it progress:

```sh
kubectl get terraform -n ${TF_NAMESPACE} -w
```

After a couple of minutes (provider download included):

```sh
kubectl get terraform terraform-sample -n ${TF_NAMESPACE} -o jsonpath='{.status.binary}{"\n"}{.status.lastAppliedRevision}{"\n"}'
kubectl get secret -n ${TF_NAMESPACE} tfstate-default-terraform-sample terraform-sample-outputs
kubectl get namespace terraform-demo
kubectl get configmap tfplan-default-terraform-sample-readable -n ${TF_NAMESPACE} -o jsonpath='{.data.tfplan}'
```

In the Argo CD UI click **Terraform** in the left sidebar: the dashboard lists
`team-a/terraform-sample` with its state; click it for the graph (Git source →
Terraform → the state/plan/outputs objects → the `kubernetes_namespace` and
`kubernetes_config_map` resources it manages, with their IDs), the plan output,
outputs, conditions and events. Inside the `terraform-sample` Application the
Terraform node also has a **Terraform** tab, and the view switcher gains a
**Terraform** view.

### Try manual approval

```sh
kubectl patch terraform terraform-sample -n ${TF_NAMESPACE} --type merge \
  -p '{"spec":{"approvePlan":"","vars":[{"name":"namespace","value":"terraform-demo-2"}]}}'
```

The next plan has changes and waits: the object is **Suspended** in Argo CD
with the plan ID in its message. Approve it with the **Approve plan** button
in the Terraform tab, or:

```sh
PLAN=$(kubectl get terraform terraform-sample -n ${TF_NAMESPACE} -o jsonpath='{.status.plan.pending}')
kubectl patch terraform terraform-sample -n ${TF_NAMESPACE} --type merge -p "{\"spec\":{\"approvePlan\":\"${PLAN}\"}}"
```

(The Application shows the patched object as OutOfSync; with
`automated.selfHeal` Argo CD would revert it — see the GitOps considerations in
[Argo CD integration](operator-manual/argocd-integration.md).)

## 5. Clean up

```sh
kubectl delete application terraform-sample -n argocd --cascade=foreground   # prunes the Terraform object, which destroys terraform-demo
kubectl delete clusterrolebinding tf-runner-${TF_NAMESPACE}-sample
kubectl delete -f https://github.com/redhat-developer/argocd-terraform-controller/releases/download/${VERSION}/install.yaml
```

## Using cloud credentials

The sample above only needs Kubernetes access, so the runner's ServiceAccount
is enough. Real stacks need provider credentials delivered to the runner Pod:
a credentials file through `spec.fileMappings`, environment variables through
`spec.runner.podTemplate.spec.envFrom`, or workload identity on the `tf-runner`
ServiceAccount. All three, with copy-and-paste examples, are in
[Variables and credentials](user-guide/variables-and-credentials.md).

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
