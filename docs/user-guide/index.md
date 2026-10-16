# User Guide

This section is for the people who write `Terraform` objects and the
configurations they point at.

- [Plan approval workflow](approval-workflow.md) — manual approval by plan ID,
  `auto`, plan-only, when pending plans are discarded, failures and retries.
- [Sources and credentials](sources.md) — Git and OCI sources, credential
  Secrets, reusing Argo CD repository credentials, what reaches the runner.
- [Variables and credentials](variables-and-credentials.md) — passing
  variables (`-var`, `-var-file` equivalents) and giving the runner cloud
  provider credentials.
- [Drift detection](drift-detection.md) — how drift is found, what happens in
  each approval mode, choosing an interval.
- [Outputs](outputs.md) — output names and values, the outputs Secret, feeding
  workloads and other stacks.
- [Dependencies and health checks](dependencies-and-health-checks.md) —
  ordering stacks with `dependsOn`, verifying them with `healthChecks`, how
  that composes with sync waves.
- [Destroy and deletion](destroy-and-deletion.md) — `spec.destroy`, what
  deleting the object does, keeping or removing the state.
- [Runner Pods](runner.md) — image, binary, identity, environment, resources,
  volumes, security context, networking.
- [Examples](examples.md) — complete objects for common situations.
- [Terraform Specification Reference](api.md) — every `spec` and `status`
  field. The generated CRD schema is also browsable at
  [doc.crds.dev](https://doc.crds.dev/github.com/redhat-developer/argocd-terraform-controller).

A minimal object looks like this; Argo CD syncs it from your repository like
any other manifest:

```yaml
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: network
  namespace: team-a
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
