# Developer Guide

## Layout

```
api/v1alpha1/          CRD types, status helpers; api/planid plan ID derivation
cmd/manager            controller binary        cmd/runner   runner binary
internal/controller    reconciliation (setup, plan, apply, drift, outputs, finalizer)
internal/runner        gRPC server executing terraform/tofu (runner.proto)
internal/mtls          CA + per-namespace runner certificates
internal/source        git / OCI fetchers, Argo CD credential lookup
internal/plan          plan <-> Secret/ConfigMap encoding (gzip, chunking)
internal/tarball       source archiving / safe extraction
internal/argocd        tests for config/argocd/lua
config/                kustomize: crd, rbac, manager, runner (per-namespace), argocd (component), samples
charts/                Helm chart (CRD synced from config/crd)
ui/extension           Argo CD UI extension (TypeScript, built in a Node container)
hack/argocd-cm         renders config/argocd/lua into argocd-cm-patch.yaml
test/e2e               kind end-to-end tests
```

## Everyday commands

```sh
make generate manifests     # deepcopy, CRD, RBAC, gRPC code, argocd-cm patch
make test                   # unit + envtest (needs terraform or tofu in PATH)
make lint                   # golangci-lint
make build                  # bin/manager, bin/runner
make docker-build docker-build-runner
make test-e2e               # kind: builds images, deploys, applies with tofu and terraform
make ui-test ui-build build-extension   # UI, in a node:22 container
make helm-lint verify-argocd-overlay
make build-docs serve-docs   # documentation site, in a python:3.12 container
```

The envtest suite starts the controller with `INSECURE_LOCAL_RUNNER=1` and an
in-process runner on `localhost`, using the local `tofu`/`terraform` binary and
a `local` backend stored outside the working directory. The e2e suite serves the
local checkout from a `git daemon` Pod inside kind unless `E2E_SOURCE_URL`
(with `E2E_SOURCE_REF`, `E2E_SOURCE_PATH`) points elsewhere.

## Running locally against a cluster

```sh
make install                                 # CRD
INSECURE_LOCAL_RUNNER=1 go run ./cmd/runner --grpc-port 30000 &   # no TLS, local binary
INSECURE_LOCAL_RUNNER=1 DISABLE_TF_K8S_BACKEND=1 go run ./cmd/manager --runner-grpc-port 30000
```

`INSECURE_LOCAL_RUNNER=1` makes the controller dial `localhost` instead of
creating runner Pods (it still issues the mTLS secret with a `localhost` SAN
when the runner is started with `--tls-secret-name`).

## Changing the gRPC protocol

Edit `internal/runner/runner.proto`, run `make proto` (buf + protoc-gen-go,
installed into `bin/`), implement the server method in `internal/runner` and
the client call in `internal/controller`.

## Changing the Argo CD customizations

Edit `config/argocd/lua/**` and run `make argocd-cm` (also part of
`make manifests`) to regenerate `config/argocd/argocd-cm-patch.yaml`. Tests in
`internal/argocd` exercise the scripts with gopher-lua. The UI mirrors the
health logic in `ui/extension/src/model.ts`; keep them in sync.

## Releasing

Bump `export VERSION=vX.Y.Z` in `docs/getting-started.md` so the guide stays
copy-pasteable (`make verify-release-version TAG=vX.Y.Z` checks it; the Release
workflow fails early if it does not match the tag), then tag `vX.Y.Z`. CI
pushes the `quay.io/redhatworkshops/argocd-terraform-controller-{manager,runner}` images, builds the
UI extension tarball, the install manifest, the Argo CD patches and the Helm
chart, and attaches them to the GitHub release.

## Documentation site

The docs are MkDocs with the Material theme, configured like the Argo CD docs:
`mkdocs.yml` at the repository root, sources under `docs/`, Python
requirements in `docs/requirements.txt`. Nothing needs to be installed on the
host:

```sh
make serve-docs             # live reload on http://localhost:8000
make build-docs             # strict build into site/ (what CI runs)
```

`strict: true` turns broken links, missing nav entries and unknown relative
links into build failures. Links from docs to files in the repository (code,
manifests) must be absolute GitHub URLs; links between pages are relative
`.md` paths. GitHub-style callouts (`> [!NOTE]`) render on both GitHub and the
site through the `gh-admonitions` plugin.

The `Docs` workflow (`.github/workflows/docs.yml`) runs only when `docs/**`,
`mkdocs.yml` or `.readthedocs.yaml` change: pull requests get a strict build;
pushes to `main` also deploy to the `gh-pages` branch, served at
<https://redhat-developer.github.io/argocd-terraform-controller/>. The other
workflows ignore documentation changes.

The configuration is Read the Docs-ready (`.readthedocs.yaml`, like
`argoproj/argo-cd`). To move the site there: import the repository on
readthedocs.org, which picks up `.readthedocs.yaml` and sets
`READTHEDOCS_CANONICAL_URL` (used by `site_url` in `mkdocs.yml`), then remove
the `deploy` job from `docs.yml` and delete the `gh-pages` branch.
