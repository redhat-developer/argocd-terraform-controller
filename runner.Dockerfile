# Runner image: the gRPC runner binary plus both IaC CLIs (terraform and tofu).
# The controller selects the binary per Terraform object (spec.runner.binary).
#
# Build:  make docker-build-runner RUNNER_IMG=<img> TERRAFORM_VERSION=x TOFU_VERSION=y
ARG BASE_IMAGE=golang:1.26
ARG TERRAFORM_VERSION=1.16.6
ARG TOFU_VERSION=1.13.1

FROM ${BASE_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a \
      -ldflags "-X main.Version=${VERSION} -X main.Commit=${COMMIT}" \
      -o runner ./cmd/runner

# OpenTofu ships official images; copy the static binary out.
FROM ghcr.io/opentofu/opentofu:${TOFU_VERSION}-minimal AS tofu

# Terraform is distributed as a zip from releases.hashicorp.com.
FROM alpine:3.23 AS terraform
ARG TERRAFORM_VERSION
ARG TARGETARCH
RUN apk add --no-cache curl unzip && \
    ARCH="${TARGETARCH:-amd64}" && ZIP="terraform_${TERRAFORM_VERSION}_linux_${ARCH}.zip" && cd /tmp && \
    curl -fsSLO "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/${ZIP}" && \
    curl -fsSLo SHA256SUMS "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_SHA256SUMS" && \
    grep " ${ZIP}\$" SHA256SUMS | sha256sum -c - && \
    unzip -q "${ZIP}" terraform -d /usr/local/bin && chmod 0755 /usr/local/bin/terraform

FROM alpine:3.23

LABEL org.opencontainers.image.source="https://github.com/redhat-developer/argocd-terraform-controller"
LABEL org.opencontainers.image.description="Argo CD Terraform Controller runner (terraform + tofu)"

# git and openssh are needed for module sources; gnupg for provider
# signature verification; tini reaps provider child processes.
RUN apk add --no-cache ca-certificates git gnupg openssh-client tini libc6-compat && \
    addgroup -g 65532 -S runner && adduser -u 65532 -S runner -G runner -h /home/runner

COPY --from=builder   /workspace/runner        /usr/local/bin/runner
COPY --from=tofu      /usr/local/bin/tofu      /usr/local/bin/tofu
COPY --from=terraform /usr/local/bin/terraform /usr/local/bin/terraform

# Provider plugin cache shared across runs within the Pod lifetime.
ENV TF_PLUGIN_CACHE_DIR=/tmp/plugin-cache \
    HOME=/home/runner
RUN mkdir -p /tmp/plugin-cache && chown -R 65532:65532 /tmp/plugin-cache /home/runner

USER 65532:65532
WORKDIR /home/runner

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/runner"]
