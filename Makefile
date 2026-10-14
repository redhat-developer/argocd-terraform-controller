# Image URLs to use for all building/pushing image targets
IMAGE_REPO ?= quay.io/redhatworkshops
IMAGE_TAG ?= latest
IMG ?= $(IMAGE_REPO)/argocd-terraform-controller-manager:$(IMAGE_TAG)
RUNNER_IMG ?= $(IMAGE_REPO)/argocd-terraform-controller-runner:$(IMAGE_TAG)
# IaC binary versions baked into the runner image
TERRAFORM_VERSION ?= 1.16.6
TOFU_VERSION ?= 1.13.1
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen argocd-cm ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen proto ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: proto
proto: buf protoc-gen-go protoc-gen-go-grpc ## Generate gRPC code for the runner protocol.
	PATH="$(LOCALBIN):$$PATH" "$(BUF)" generate

.PHONY: argocd-cm
argocd-cm: ## Render config/argocd/lua into the argocd-cm patch.
	go run ./hack/argocd-cm

HELM ?= helm
CHART_DIR ?= charts/argocd-terraform-controller

.PHONY: helm-sync-crds
helm-sync-crds: manifests ## Copy the generated CRDs into the Helm chart.
	cp config/crd/bases/*.yaml $(CHART_DIR)/crds/

.PHONY: helm-lint
helm-lint: helm-sync-crds ## Lint and render the Helm chart.
	$(HELM) lint $(CHART_DIR)
	$(HELM) template test $(CHART_DIR) --namespace test --set runner.namespaces='{a,b}' > /dev/null

.PHONY: verify-argocd-overlay
verify-argocd-overlay: kustomize ## Build the Argo CD component against the upstream install manifests.
	"$(KUSTOMIZE)" build hack/argocd-overlay-test > /dev/null

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# kubectl kuberc is disabled by default for test isolation; enable with:
# - KUBECTL_KUBERC=true
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= argocd-terraform-controller-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout 45m
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager and runner binaries.
	go build -o bin/manager ./cmd/manager
	go build -o bin/runner ./cmd/runner

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/manager

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# Override BASE_IMAGE to build from another registry, e.g.
# make docker-build IMG=<img> BASE_IMAGE=docker.io/library/golang:1.26
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build $(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE)) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-build-runner
docker-build-runner: ## Build the runner image (contains terraform and tofu).
	$(CONTAINER_TOOL) build --build-arg TERRAFORM_VERSION=$(TERRAFORM_VERSION) --build-arg TOFU_VERSION=$(TOFU_VERSION) -t ${RUNNER_IMG} -f runner.Dockerfile .

.PHONY: docker-push-runner
docker-push-runner: ## Push the runner image.
	$(CONTAINER_TOOL) push ${RUNNER_IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name argocd-terraform-controller-builder
	$(CONTAINER_TOOL) buildx use argocd-terraform-controller-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) $(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE)) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm argocd-terraform-controller-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ UI extension (runs in a container; no host Node.js needed)

UI_DIR ?= ui/extension
NODE_IMAGE ?= docker.io/library/node:22
# ui-run executes a command inside the Node image with the extension sources
# mounted at /work, as the current user so files stay owned by you.
define ui-run
$(CONTAINER_TOOL) run --rm $(UI_TTY) -v "$(shell pwd)/$(UI_DIR):/work:z" -w /work -u "$(shell id -u):$(shell id -g)" -e HOME=/tmp -e npm_config_cache=/work/.npm-cache $(NODE_IMAGE) $(1)
endef

.PHONY: ui-install
ui-install: ## Install UI dependencies (npm ci, or npm install when there is no lock file).
	$(call ui-run,sh -c 'if [ -f package-lock.json ]; then npm ci; else npm install; fi')

.PHONY: ui-build
ui-build: ui-install ## Build the UI extension to ui/extension/dist/extension-terraform.js.
	$(call ui-run,npm run build)

.PHONY: ui-test
ui-test: ui-install ## Type-check, lint and unit test the UI extension.
	$(call ui-run,npm run type-check)
	$(call ui-run,npm run lint)
	$(call ui-run,npm test)

.PHONY: ui-shell
ui-shell: ## Open a shell in the Node container (for npm add/update etc).
	$(call ui-run,bash)
ui-shell: UI_TTY = -it

.PHONY: build-extension
build-extension: ui-build ui-test ## Package the UI extension release tarball and checksums into dist/.
	mkdir -p dist
	tar -czf dist/argocd-terraform-extension.tar.gz -C $(UI_DIR)/dist extension-terraform.js
	cd dist && sha256sum argocd-terraform-extension.tar.gz > checksums.txt
	@echo "built dist/argocd-terraform-extension.tar.gz"

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: deploy-runner-rbac
deploy-runner-rbac: kustomize ## Create the tf-runner ServiceAccount and RoleBinding in NAMESPACE (default: default).
	"$(KUSTOMIZE)" build config/runner | "$(KUBECTL)" apply -n $(or $(NAMESPACE),default) -f -

ARGOCD_NS = $(or $(ARGOCD_NAMESPACE),argocd)

.PHONY: deploy-extension-dev
deploy-extension-dev: ui-build ## Mount the locally built UI extension into argocd-server through a ConfigMap (ARGOCD_NAMESPACE, default: argocd).
	"$(KUBECTL)" create configmap argocd-terraform-extension -n $(ARGOCD_NS) \
		--from-file=extension-terraform.js=$(UI_DIR)/dist/extension-terraform.js --dry-run=client -o yaml | "$(KUBECTL)" apply -f -
	"$(KUBECTL)" patch deployment argocd-server -n $(ARGOCD_NS) --patch-file hack/argocd-server-extension-configmap-patch.yaml
	"$(KUBECTL)" rollout restart deployment/argocd-server -n $(ARGOCD_NS)
	"$(KUBECTL)" rollout status deployment/argocd-server -n $(ARGOCD_NS) --timeout=5m

.PHONY: undeploy-extension-dev
undeploy-extension-dev: ## Remove the ConfigMap-mounted UI extension from argocd-server.
	"$(KUBECTL)" patch deployment argocd-server -n $(ARGOCD_NS) --patch-file hack/argocd-server-extension-configmap-unpatch.yaml
	"$(KUBECTL)" delete configmap argocd-terraform-extension -n $(ARGOCD_NS) --ignore-not-found

.PHONY: deploy-argocd
deploy-argocd: ## Patch an existing Argo CD installation in ARGOCD_NAMESPACE (default: argocd) with the health/actions config, UI extension and RBAC.
	"$(KUBECTL)" patch configmap argocd-cm -n $(or $(ARGOCD_NAMESPACE),argocd) --type merge --patch-file config/argocd/argocd-cm-patch.yaml
	"$(KUBECTL)" patch deployment argocd-server -n $(or $(ARGOCD_NAMESPACE),argocd) --patch-file config/argocd/argocd-server-extension-patch.yaml
	"$(KUBECTL)" apply -n $(or $(ARGOCD_NAMESPACE),argocd) -f config/argocd/argocd_repo_reader_binding.yaml

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
BUF ?= $(LOCALBIN)/buf
PROTOC_GEN_GO ?= $(LOCALBIN)/protoc-gen-go
PROTOC_GEN_GO_GRPC ?= $(LOCALBIN)/protoc-gen-go-grpc

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.22.0

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.14.0
BUF_VERSION ?= v1.66.0
PROTOC_GEN_GO_VERSION ?= $(shell v='$(call gomodver,google.golang.org/protobuf)'; [ -n "$$v" ] && printf '%s\n' "$$v" || echo latest)
PROTOC_GEN_GO_GRPC_VERSION ?= v1.5.1
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: buf
buf: $(BUF) ## Download buf locally if necessary.
$(BUF): $(LOCALBIN)
	$(call go-install-tool,$(BUF),github.com/bufbuild/buf/cmd/buf,$(BUF_VERSION))

.PHONY: protoc-gen-go
protoc-gen-go: $(PROTOC_GEN_GO) ## Download protoc-gen-go locally if necessary.
$(PROTOC_GEN_GO): $(LOCALBIN)
	$(call go-install-tool,$(PROTOC_GEN_GO),google.golang.org/protobuf/cmd/protoc-gen-go,$(PROTOC_GEN_GO_VERSION))

.PHONY: protoc-gen-go-grpc
protoc-gen-go-grpc: $(PROTOC_GEN_GO_GRPC) ## Download protoc-gen-go-grpc locally if necessary.
$(PROTOC_GEN_GO_GRPC): $(LOCALBIN)
	$(call go-install-tool,$(PROTOC_GEN_GO_GRPC),google.golang.org/grpc/cmd/protoc-gen-go-grpc,$(PROTOC_GEN_GO_GRPC_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
