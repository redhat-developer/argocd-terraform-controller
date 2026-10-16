# FAQ

## General

### How is this different from the Flux tofu-controller?

It is deliberately similar — same runner-Pod model, same state and approval
concepts — but built for Argo CD: the `Terraform` object is a plain manifest
an Application syncs, health and actions come from `argocd-cm`, and the UI is
an Argo CD extension. There is no dependency on Flux's source controller; the
object carries its own `spec.source`. See [Architecture](operator-manual/architecture.md).

### How is it different from Crossplane, or from a provider's Kubernetes operator?

Those model each cloud resource as a Kubernetes object with their own
providers. This controller runs **your existing Terraform/OpenTofu code**
unchanged — modules, providers, state and all — and gives Argo CD a single
object per stack to approve, apply and watch. Use whichever matches how your
team already works; they can coexist.

### Does it change Argo CD?

No. It installs a CRD and a controller, adds two keys to `argocd-cm` (health
and actions Lua) and an init container to `argocd-server` that downloads the
UI extension. Everything uses Argo CD's documented extension points; see
[Argo CD integration](operator-manual/argocd-integration.md).

### Terraform or OpenTofu?

Both are in the runner image; choose per object with `spec.runner.binary`.
`auto` prefers OpenTofu. See [Terraform vs OpenTofu](operator-manual/binaries.md).

### Is it production-ready?

It is **alpha**: the API (`infra.argocd.io/v1alpha1`) may still change, and
it has had limited exposure. The controller is designed conservatively
(manual approval by default, infrastructure kept on deletion by default,
hardened Pods), so the blast radius of a bug is small, but evaluate it on
non-critical stacks first.

## Behaviour

### Why does Argo CD show my Application as Suspended?

A plan is waiting for approval. That is the default (`approvePlan: ""`) and it
intentionally blocks later sync waves. Approve it in the UI, with `kubectl`, or
by committing the plan ID — or use `approvePlan: auto`. See
[Plan approval](user-guide/approval-workflow.md).

### I approved in the UI and the plan went back to pending.

Argo CD with `selfHeal` reverted `spec.approvePlan` to the value in Git. Either
approve from Git or add the `ignoreDifferences` described in
[Argo CD integration › GitOps considerations](operator-manual/argocd-integration.md#gitops-considerations).

### Where is my state?

By default in Secret `tfstate-<workspace>-<name>` in the object's namespace,
via Terraform's `kubernetes` backend (`status.stateSecretName`). Any other
backend works through `spec.backendConfig`. See [State](operator-manual/state.md).

### Can I adopt infrastructure that already has a state file?

Yes. Either point `spec.backendConfig.customConfiguration` at the existing
backend, or import the state into the Kubernetes backend once (restore
procedure in [State › Backup and restore](operator-manual/state.md#backup-and-restore-kubernetes-backend)),
or run `terraform import` from a laptop against the Secret
([State › Working with the state from the CLI](operator-manual/state.md#working-with-the-state-from-the-cli)).

### What happens when I delete the object?

Plan and outputs objects are removed; the infrastructure and the state are
**kept** unless `spec.destroyResourcesOnDeletion: true` (and
`spec.tfstate.deleteOnDestroy: true` for the state). See
[Destroy and deletion](user-guide/destroy-and-deletion.md).

### Why is the object Degraded with `DriftDetected`?

Something changed the infrastructure outside Terraform. In manual mode the
controller reports and waits; press **Re-plan** and approve to reconcile it,
or use `approvePlan: auto` to let it self-heal. See
[Drift detection](user-guide/drift-detection.md).

### How do I pass variables and credentials?

`spec.vars`/`varsFrom`/`values`/`tfVarsFiles` for variables;
`envFrom`, `fileMappings` or workload identity on the runner ServiceAccount
for credentials. See [Variables and credentials](user-guide/variables-and-credentials.md).

### How do I see output values?

Names are in `status.availableOutputs`; values are only in the Secret named by
`spec.writeOutputsToSecret` (`kubectl get secret … -o go-template=…`). The UI
never shows values. See [Outputs](user-guide/outputs.md).

### Can two objects share one state or one set of resources?

Don't. One state per object; use `dependsOn` and outputs to connect stacks.
Two objects on the same state would fight over the lock and each other's
resources.

### Can I run the same configuration for several environments?

Yes — several objects with the same `source` and `path` but different
`vars`, `workspace` or `backendConfig.secretSuffix`. Each gets its own state
Secret (`tfstate-<workspace>-<suffix>`).

### How big can my repository be?

The source is archived and sent to the runner in one gRPC message, bounded by
`--runner-grpc-max-message-size` (4 MiB default). Use `spec.source.git.ignore`
to exclude what the stack does not need, or raise the limit. See
[Controller configuration](operator-manual/controller-configuration.md).

## UI

### The Terraform sidebar entry / tab is missing.

The extension is not installed or the browser cached the old `extensions.js`.
`kubectl -n argocd exec deploy/argocd-server -- ls /tmp/extensions/resources/`
should list `extension-terraform.js`; then hard-refresh. See
[Troubleshooting › Nothing in the Argo CD UI](operator-manual/troubleshooting.md#nothing-in-the-argo-cd-ui).

### The dashboard does not show an object I created with kubectl.

The dashboard aggregates through the Argo CD Applications API, so it lists
Terraform objects that belong to Applications you are allowed to see. Objects
applied outside Argo CD are not shown (they still work).

### Why are plan outputs visible to anyone who can see the Application?

The readable plan is stored in a ConfigMap so Argo CD can display it (Argo CD
redacts Secret data). If plans may contain sensitive values, set
`spec.storeReadablePlan: none`. See [Security](operator-manual/security.md).

## Project

### Where do I report bugs or ask for features?

[GitHub issues](https://github.com/redhat-developer/argocd-terraform-controller/issues).
Security issues: please use a
[private security advisory](https://github.com/redhat-developer/argocd-terraform-controller/security/advisories/new).

### How do I contribute?

See the [Developer Guide](developer-guide/index.md): the repository builds with
`make`, tests run with envtest and kind, and the UI builds in a container.
