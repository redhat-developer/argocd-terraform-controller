# Operator Manual

This section is for the people who install and run the controller and wire it
into Argo CD.

- [Architecture](architecture.md) — components, what happens on each
  reconciliation, the status model and the isolation boundaries.
- [Installation and upgrades](installation.md) — install with a manifest,
  Helm or Kustomize, create the runner identity per namespace, upgrade release
  by release knowing which piece changed, uninstall safely.
- [Controller configuration](controller-configuration.md) — every flag and
  environment variable, Helm value mapping, sizing and availability.
- [Argo CD integration](argocd-integration.md) — health assessment, resource
  actions, the UI extension, reusing Argo CD repository credentials and RBAC
  for users.
- [State backends, backup and restore](state.md) — the default Kubernetes
  backend, other backends and Terraform Cloud, locks, backup and restore,
  working with the state from the CLI.
- [Terraform vs OpenTofu](binaries.md) — choosing the binary, mixing them,
  custom runner images.
- [Security](security.md) — trust boundaries, hardening, tenancy, RBAC for
  people, secrets, supply chain.
- [Metrics](metrics.md) — scraping the controller, useful metrics and alerts.
- [Troubleshooting](troubleshooting.md) — by Ready reason and by symptom.

For a first end-to-end run on a test cluster, use
[Getting started](../getting-started.md) instead; it is the copy-and-paste
version of this manual.
