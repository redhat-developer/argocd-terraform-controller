# Terraform vs OpenTofu

The runner image ships both CLIs:

| Binary | Version build arg | Source |
|---|---|---|
| `tofu` | `TOFU_VERSION` | `ghcr.io/opentofu/opentofu:<version>-minimal` |
| `terraform` | `TERRAFORM_VERSION` | `releases.hashicorp.com` (checksum verified) |

Select per object:

```yaml
spec:
  runner:
    binary: auto        # default: tofu if present in the image, otherwise terraform
    # binary: terraform
    # binary: tofu
```

`status.binary` records what actually ran, e.g. `tofu 1.13.1`.

Both binaries are driven through
[terraform-exec](https://github.com/hashicorp/terraform-exec) with identical
commands (`init -upgrade -force-copy`, `plan -out`, `show`, `apply`, `output`,
`force-unlock`), so every feature of the controller works the same with either.
Variables, backend and `.tfrc` handling are also identical; the only difference
users see is the version line in `status.binary` and the plan text.

## Mixing binaries

Different Terraform objects can use different binaries, including objects that
depend on each other. The state format is compatible between current Terraform
and OpenTofu releases, but switching an existing object from one to the other
is a one-way migration in practice (newer OpenTofu state versions may not be
readable by Terraform). Pin `binary` explicitly for production stacks.

## Custom runner images

Use `spec.runner.image` or the controller's `RUNNER_POD_IMAGE` to point at a
derived image, e.g. with extra tooling or a single binary:

```Dockerfile
FROM quay.io/redhatworkshops/argocd-terraform-controller-runner:latest
USER root
RUN rm /usr/local/bin/terraform && apk add --no-cache aws-cli
USER 65532:65532
```

Build the stock image with other versions:

```sh
make docker-build-runner RUNNER_IMG=my/runner:dev TERRAFORM_VERSION=1.9.8 TOFU_VERSION=1.8.5
```

Licensing: Terraform is distributed under the BUSL-1.1; OpenTofu under the MPL-2.0.
Pick the binary that fits your usage; `auto` prefers OpenTofu.
