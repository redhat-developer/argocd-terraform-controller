# Examples

Complete, realistic objects you can adapt. All of them assume the controller
is installed, the namespace has the runner identity, and Argo CD syncs the
manifests (see [Getting started](../getting-started.md)). Replace
repositories, names and ARNs with your own.

## 1. Kubernetes resources with the `kubernetes` provider

The getting-started sample: no cloud credentials, state in the cluster, auto
approval, destroy on deletion. The runner's ServiceAccount needs RBAC for
what the configuration manages (here a Namespace and a ConfigMap).

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: terraform-sample
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
    binary: auto
  writeOutputsToSecret:
    name: terraform-sample-outputs
  destroyResourcesOnDeletion: true
```

## 2. AWS stack with IRSA, manual approval, drift alerts

Production posture: credentials come from the ServiceAccount's IAM role, plans
wait for approval, drift makes the Application Degraded, outputs feed a health
check. The Terraform code pins `profile = null` (or omits it).

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tf-runner
  namespace: platform
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/argocd-terraform-platform
---
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: network
  namespace: platform
spec:
  source:
    git:
      url: git@github.com:acme/infra.git
      ref:
        tag: network-v1.4.2            # promote by moving the tag
      secretRef:
        name: infra-git-ssh            # identity + known_hosts
  path: ./stacks/network
  interval: 30m
  approvePlan: ""                      # manual
  runner:
    binary: tofu
    podTemplate:
      spec:
        env:
          - name: AWS_REGION
            value: us-east-1
        resources:
          requests: { cpu: 250m, memory: 512Mi }
          limits:   { memory: 2Gi }
  vars:
    - name: environment
      value: prod
    - name: cidr
      value: 10.42.0.0/16
  writeOutputsToSecret:
    name: network-outputs
  healthChecks:
    - name: nat
      type: tcp
      address: "{{ .nat_gateway_ip }}:443"
```

Approve a plan from Git by committing `approvePlan: plan-network-v1.4.2-…`
(the ID is in the Ready message), or from the Argo CD UI with the
`ignoreDifferences` shown in
[Argo CD integration](../operator-manual/argocd-integration.md#gitops-considerations).

## 3. Two stacks in order, outputs flowing between them

The database stack waits for the network stack, reads its outputs, and exposes
its own to a workload in the next sync wave.

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: database
  namespace: platform
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  source:
    git:
      url: git@github.com:acme/infra.git
      ref: { branch: main }
      secretRef: { name: infra-git-ssh }
  path: ./stacks/database
  interval: 1h
  approvePlan: auto
  dependsOn:
    - name: network
  varsFrom:
    - kind: Secret
      name: network-outputs
      varsKeys:
        - vpc_id
        - private_subnet_ids:subnet_ids_json    # list output arrives as JSON text
  vars:
    - name: db_password
      valueFrom:
        secretKeyRef: { name: database-master, key: password }
  writeOutputsToSecret:
    name: database-outputs
    outputs:
      - endpoint:DB_HOST
      - port:DB_PORT
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: platform
  annotations:
    argocd.argoproj.io/sync-wave: "1"
spec:
  selector: { matchLabels: { app: api } }
  template:
    metadata: { labels: { app: api } }
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:1.8.0
          envFrom:
            - secretRef: { name: database-outputs }
            - secretRef: { name: database-master }
```

In `stacks/database`:

```hcl
variable "subnet_ids_json" { type = string }
locals { subnet_ids = jsondecode(var.subnet_ids_json) }
```

## 4. Existing S3 state, drift detection only

A stack that a CI pipeline applies today. The controller watches it and turns
the Application Degraded on drift, without ever applying.

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: legacy-dns
  namespace: platform
spec:
  source:
    git:
      url: https://github.com/acme/legacy-infra.git
      ref: { branch: main }
      useArgoCDRepoCredentials: true   # reuse the credentials Argo CD already has
  path: ./dns
  interval: 1h
  approvePlan: disable
  backendConfig:
    customConfiguration: |
      backend "s3" {
        bucket         = "acme-tfstate"
        key            = "dns/terraform.tfstate"
        region         = "us-east-1"
        dynamodb_table = "acme-tfstate-locks"
      }
  runner:
    podTemplate:
      spec:
        envFrom:
          - secretRef: { name: aws-readonly }    # read-only credentials are enough
```

## 5. Per-pull-request environments from an ApplicationSet

Each PR gets a namespace, a Terraform object with `auto` approval, and
everything is destroyed — state included — when the PR closes and Argo CD
prunes the Application.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: preview-envs
  namespace: argocd
spec:
  generators:
    - pullRequest:
        github: { owner: acme, repo: shop, labels: [preview] }
  template:
    metadata:
      name: "preview-{{number}}"
    spec:
      project: previews
      source:
        repoURL: https://github.com/acme/shop.git
        targetRevision: "{{head_sha}}"
        path: deploy/preview
        helm:
          parameters:
            - name: pr
              value: "{{number}}"
      destination:
        server: https://kubernetes.default.svc
        namespace: "preview-{{number}}"
      syncPolicy:
        automated: { prune: true, selfHeal: true }
        syncOptions: [CreateNamespace=true]
```

`deploy/preview` renders the runner identity (ServiceAccount + RoleBinding to
`argocd-terraform-controller-runner-role`) and:

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: preview-{{ .Values.pr }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  source:
    git:
      url: https://github.com/acme/shop.git
      ref: { branch: main }
      useArgoCDRepoCredentials: true
  path: ./infra/preview
  interval: 1h
  approvePlan: auto
  vars:
    - name: pr_number
      value: {{ .Values.pr }}
  writeOutputsToSecret:
    name: preview-outputs
  destroyResourcesOnDeletion: true
  tfstate:
    deleteOnDestroy: true
  runner:
    podTemplate:
      spec:
        envFrom:
          - secretRef: { name: preview-cloud-creds }
```

## 6. Terraform Cloud workspace

State and runs stay in TFC; the controller orchestrates and reports.

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: billing
  namespace: finance
spec:
  source:
    oci:
      url: oci://ghcr.io/acme/tf-billing     # configuration shipped as an OCI artifact
      ref: { semver: "^2.0" }
      secretRef: { name: ghcr-pull }
  interval: 2h
  approvePlan: ""
  cloud:
    organization: acme
    workspaces: { name: billing-prod }
  cliConfigSecretRef:
    name: tfc-credentials                     # key *.tfrc with the credentials block
  runner:
    binary: terraform                         # TFC requires Terraform
```

## 7. Kubernetes provider against another cluster

A kubeconfig for the target cluster is mounted from a Secret with
`fileMappings`; the configuration's provider reads it from the runner home.

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: edge-cluster-addons
  namespace: platform
spec:
  source:
    git:
      url: https://github.com/acme/infra.git
      ref: { branch: main }
      useArgoCDRepoCredentials: true
  path: ./clusters/edge/addons
  interval: 30m
  approvePlan: auto
  fileMappings:
    - secretRef: { name: edge-kubeconfig, key: kubeconfig }
      location: home
      path: .kube/config              # provider "kubernetes" { config_path = "~/.kube/config" }
```

More on each knob: [Variables and credentials](variables-and-credentials.md),
[Sources](sources.md), [Plan approval](approval-workflow.md),
[Outputs](outputs.md), [Dependencies and health checks](dependencies-and-health-checks.md),
[Destroy and deletion](destroy-and-deletion.md), [Runner Pods](runner.md),
[State](../operator-manual/state.md).
