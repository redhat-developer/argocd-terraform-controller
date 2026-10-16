# Sources and credentials

The Terraform object carries its own source: the controller fetches it, archives
the tree and ships it to the runner. Argo CD only syncs the Terraform object, so
the Application repository and the Terraform code repository can differ.

## Git

```yaml
spec:
  source:
    git:
      url: https://github.com/my-org/infra.git      # https://, ssh:// or git@host:path
      ref:
        branch: main          # or tag: v1.2.0 | semver: ">=1.0.0 <2.0.0" | commit: <sha> | name: refs/heads/x
      secretRef:
        name: infra-git-credentials
      useArgoCDRepoCredentials: false
      ignore: |               # .gitignore syntax; .git is always excluded
        *.md
        docs/
      recurseSubmodules: false
    interval: 1m              # how often to check for new revisions (default spec.interval)
  path: ./stacks/network      # directory inside the repository
```

Reference precedence when several are set: `commit` > `name` > `semver` > `tag` > `branch`.
Revisions are reported as `<ref>@sha1:<commit>` (e.g. `main@sha1:0123…`) and
drive the plan ID.

### Credentials Secret

| Keys | Transport |
|---|---|
| `username`, `password` | HTTPS basic auth (use a token as password) |
| `bearerToken` | HTTPS bearer token |
| `identity`, `known_hosts` (optionally `identity.pub`) | SSH; `known_hosts` is required unless `insecureIgnoreHostKey: "true"` |
| `ca.crt` | custom CA for HTTPS |

```sh
kubectl create secret generic infra-git-credentials -n team-a \
  --from-literal=username=git --from-literal=password=$GITHUB_TOKEN
```

### Reusing Argo CD credentials

`useArgoCDRepoCredentials: true` looks up the Argo CD repository Secret whose
`url` matches (exact `repository` Secret first, then the longest `repo-creds`
prefix) and the `argocd-ssh-known-hosts-cm` ConfigMap. Requires the controller
to run with `--argocd-namespace` and to be allowed to read Secrets there
(see [Argo CD integration](../operator-manual/argocd-integration.md)). If nothing matches the
fetch proceeds anonymously.

## OCI artifacts

Package the configuration with `oras push` or any tool
producing an image whose first layer is a tar(+gzip) of the tree:

```yaml
spec:
  source:
    oci:
      url: oci://ghcr.io/my-org/infra
      ref:
        tag: latest            # or semver: "^1.0" | digest: sha256:…
      secretRef:
        name: ghcr-pull        # kubernetes.io/dockerconfigjson
      insecure: false
  path: ./network
```

Revisions are `<tag>@sha256:<digest>`.

## What reaches the runner

The archived tree (minus `.git` and `ignore` matches) is uploaded over gRPC;
`--runner-grpc-max-message-size` (default 4 MiB) bounds its size. The runner
extracts it under `/tmp/<namespace>-<name>` and works in `spec.path`.

Additional inputs:

- `spec.vars`, `spec.varsFrom`, `spec.values` → `generated.auto.tfvars.json`
- `spec.tfVarsFiles` → `-var-file` (paths relative to the source root)
- `spec.fileMappings` → files from Secrets written into the workspace or the
  runner home (`/home/runner`), e.g. provider credentials files
- `spec.cliConfigSecretRef` → a `*.tfrc` CLI configuration (`TF_CLI_CONFIG_FILE`)
- `main.tf.tpl` in the path → rendered with the variables (Helm-like `{{ .Values.x }}`) to `main.tf`
