# Runner Pods

Every run of a Terraform object happens in a dedicated Pod,
`<name>-tf-runner`, in the object's namespace. `spec.runner` controls what that
Pod looks like.

```yaml
spec:
  runner:
    binary: auto                     # auto | terraform | tofu
    image: ""                        # override the controller's default runner image
    serviceAccountName: tf-runner    # identity in the namespace (RBAC, workload identity)
    alwaysCleanup: true              # delete the Pod after each run
    terminationGracePeriodSeconds: 30
    podTemplate:
      metadata:
        labels: {}
        annotations: {}
      spec:
        env: []
        envFrom: []
        volumes: []
        volumeMounts: []
        initContainers: []
        nodeSelector: {}
        affinity: {}
        tolerations: []
        hostAliases: []
        priorityClassName: ""
        securityContext: {}          # replaces the hardened default when set
        resources: {}
```

## Lifecycle

1. The controller creates the Pod when a run starts (`RunnerConnectionFailed`
   after `--runner-creation-timeout` if it does not become Ready).
2. The source archive is uploaded over gRPC/mTLS; Terraform runs in
   `/tmp/<namespace>-<name>/<path>`; plans, state and outputs are written to
   the Kubernetes API with the Pod's ServiceAccount.
3. With `alwaysCleanup: true` (default) the Pod is deleted when the run ends,
   successfully or not. The next run starts a fresh Pod.

> [!IMPORTANT]
> The Pod's filesystem is **ephemeral**: the working directory and
> `/home/runner` are `emptyDir` volumes that disappear with the Pod. Anything
> the configuration writes to disk as a *managed resource* (`local_file`,
> `local_sensitive_file`, files from `local-exec`) will be missing on the
> next run and reported as drift every time. Hand such values out as
> outputs instead; see [Drift that never goes away](drift-detection.md#drift-that-never-goes-away).

With `alwaysCleanup: false` the Pod stays between runs and is reused, which
saves the image pull and provider download on each reconcile (providers are
cached in `TF_PLUGIN_CACHE_DIR=/tmp/plugin-cache` inside the Pod). The
controller still replaces it when the TLS certificate rotates or when the Pod
has exited; a change to `spec.runner` (image, env, resources) only takes
effect on the next fresh Pod — delete the Pod by hand to force it. Prefer the
default unless
reconcile latency matters more than the Pod's footprint; a long-lived Pod also
keeps credentials in memory longer.

Pods carry labels `app.kubernetes.io/name=tf-runner`,
`app.kubernetes.io/instance=tf-runner-<revision>`,
`infra.argocd.io/terraform=<namespace>`, plus whatever `podTemplate.metadata.labels`
adds — handy for NetworkPolicies and cost allocation.

## Image and binary

The default image comes from the controller (`RUNNER_POD_IMAGE`, pinned per
release) and contains both `terraform` and `tofu`. `binary` picks one per
object; `auto` prefers `tofu`. `status.binary` records what ran.

Use `image` for a derived image with extra tools (`aws`, `gcloud`, `kubectl`,
`sops`), pinned provider mirrors, or a single binary — see
[Terraform vs OpenTofu › Custom runner images](../operator-manual/binaries.md#custom-runner-images).
The image must keep the runner entrypoint, UID `65532`, and
`/home/runner` + `/tmp` writable (they are `emptyDir` mounts).

Private registries: add `imagePullSecrets` to the `tf-runner` ServiceAccount
(Kubernetes applies them to every Pod using it).

## Environment

The controller sets `POD_NAME`, `POD_NAMESPACE` and forwards its own
`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` so runners behind the same egress proxy
work without configuration. Add your own with `env`/`envFrom`: provider
credentials, `TF_LOG=DEBUG` for verbose runner logs, `TF_VAR_*` (although
`spec.vars` is the preferred way), registry mirrors via `TF_CLI_CONFIG_FILE`
(see `spec.cliConfigSecretRef`).

```yaml
spec:
  runner:
    podTemplate:
      spec:
        env:
          - name: TF_LOG
            value: INFO
          - name: AWS_REGION
            value: eu-west-1
        envFrom:
          - secretRef:
              name: aws-creds
```

## Resources and scheduling

No requests or limits are set by default. Terraform's footprint is
provider-dependent — the AWS and Google providers can use several hundred MiB;
large states and `-parallelism` push it further. Set them explicitly for
production stacks, or rely on a namespace `LimitRange`:

```yaml
spec:
  runner:
    podTemplate:
      spec:
        resources:
          requests: { cpu: 250m, memory: 512Mi }
          limits:   { memory: 2Gi }
        nodeSelector:
          workload: infra
        tolerations:
          - key: infra
            operator: Exists
        priorityClassName: infra-runs
```

If a `ResourceQuota` rejects the Pod, the object reports
`RunnerQuotaExhausted` and retries; see
[Troubleshooting](../operator-manual/troubleshooting.md#the-runner-pod-is-pending-or-crash-looping).

## Volumes and init containers

Mount what the configuration needs — a CA bundle, a kubeconfig for a remote
cluster used by the Kubernetes provider, a pre-populated provider mirror —
and prepare it with init containers:

```yaml
spec:
  runner:
    podTemplate:
      spec:
        volumes:
          - name: corp-ca
            configMap: { name: corp-ca-bundle }
          - name: providers
            emptyDir: {}
        volumeMounts:
          - name: corp-ca
            mountPath: /etc/ssl/certs/corp-ca.crt
            subPath: ca.crt
            readOnly: true
          - name: providers
            mountPath: /mirror
        initContainers:
          - name: fetch-providers
            image: registry.example.com/tools/oras:latest
            command: [oras, pull, registry.example.com/tf/providers:2026.10, -o, /mirror]
            volumeMounts: [{ name: providers, mountPath: /mirror }]
```

For small files from Secrets, `spec.fileMappings` is simpler than a volume
(see [Variables and credentials](variables-and-credentials.md#credentials-files-specfilemappings)).

## Security context

The default is hardened: UID 65532, non-root, read-only root filesystem, all
capabilities dropped, no privilege escalation, `RuntimeDefault` seccomp. This
is the **container** security context; setting `podTemplate.spec.securityContext`
**replaces** the whole block, so restate the restrictive fields when you
change one:

```yaml
spec:
  runner:
    podTemplate:
      spec:
        securityContext:
          runAsNonRoot: true
          runAsUser: 65532
          readOnlyRootFilesystem: true
          allowPrivilegeEscalation: false
          capabilities: { drop: [ALL] }
          seccompProfile: { type: RuntimeDefault }
```

There is no Pod-level security context (`fsGroup`, `supplementalGroups`).
Projected ServiceAccount tokens (workload identity) are still readable because
the kubelet assigns projected files to the containers' common `runAsUser`;
if you add `initContainers`, set `runAsUser: 65532` on them as well so that
rule keeps applying.

Pod Security Admission `restricted` is satisfied by the defaults; policy
engines that mutate Pods (adding sidecars, labels) are fine as long as the
runner container itself and the TLS volume are untouched.

## Networking

The controller connects to the runner on port `30000` by Pod IP; the runner
needs egress to the Kubernetes API, to provider APIs, to the Terraform
registry (or your mirror) and to the source of any remote modules. The runner
does **not** need to reach Git — the controller fetches sources. A
NetworkPolicy for a Terraform namespace therefore typically allows: ingress
`30000/tcp` from the controller namespace, egress to the API server, DNS and
`0.0.0.0/0:443` (or your proxy).
