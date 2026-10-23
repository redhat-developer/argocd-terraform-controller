# Drift detection

Infrastructure changes outside Terraform — a console edit, an autoscaler, a
colleague's `terraform apply` from a laptop. Drift detection finds those
changes on a schedule and either reports them or puts them back.

## How it works

On every `spec.interval`, when **nothing else changed** — same source
revision as the last apply, same object generation, no plan pending — the
controller runs a **refresh-only plan** (`plan -refresh=true -out tfdrift`)
in a runner Pod and compares the real infrastructure with the state:

| Result | Ready condition | Argo CD health | Event |
|---|---|---|---|
| No differences | `True / NoDrift` — "No drift" | Healthy | — |
| Differences | `False / DriftDetected` with the plan text as message | **Degraded** | Warning `DriftDetected` with the full plan |
| Plan failed (credentials, lock, provider) | `False / DriftDetectionFailed` | Degraded | Warning `DriftDetectionFailed` |

What happens next depends on `spec.approvePlan`:

| Mode | On drift |
|---|---|
| `auto` (or `spec.force`) | The run continues: a normal plan is produced and **applied immediately**. The object goes Degraded → Progressing → Healthy within one reconcile. `status.lastAppliedByDriftDetectionAt` records it |
| manual (`""` or a plan ID) | The run **stops** and reports Degraded, then re-checks on the next `interval`. Drift is a finding, not a failure: `status.reconciliationFailures` is not incremented and `remediation.retries` is unaffected. Review the drift in the event or the UI, then trigger **Re-plan**: the controller produces a real plan for the remediation, which waits for approval like any other change — unless `spec.approvePlan` still holds this revision's plan ID, in which case it matches and is applied in the same run (see below) |
| `disable` | Detection only — plans are run but never stored or applied. The object alternates between `NoDrift` and `DriftDetected`; use it for auditing stacks that are applied elsewhere |

Drift detection is skipped when `spec.disableDriftDetection: true`, when
`spec.destroy: true`, and while a plan is pending — a pending plan already
means "the world differs from the desired state".

## Interval

`spec.interval` is both the drift-detection period and the source polling
period (unless `spec.source.interval` sets a faster one). Every detection run
costs a runner Pod start, a provider `init` and a refresh of every resource,
so pick it per stack:

| Stack | Suggested interval |
|---|---|
| Network, IAM, shared databases (drift is dangerous) | `10m`–`30m` |
| Application infrastructure | `1h` |
| Expensive-to-refresh stacks (hundreds of resources, rate-limited APIs) | `6h`–`24h`, or `disableDriftDetection` plus an occasional manual re-plan |

When drift detection *fails* (expired credentials are the usual cause,
`DriftDetectionFailed`), `spec.retryInterval` and `spec.retryStrategy` govern
the retries like for any other failure. *Detected* drift, by contrast, is
re-checked on `spec.interval` only: checking sooner cannot change the outcome
until someone acts on it, and each check costs a runner Pod and provider
API calls.

## Remediating in manual mode

After `DriftDetected`, press **Re-plan** in the Argo CD UI (or set the
`infra.argocd.io/reconcile` annotation). Instead of detecting the same drift
again, the controller now plans: the resulting plan is the remediation and is
stored as `status.plan.pending`.

- If `spec.approvePlan` is empty or holds a different ID, the object becomes
  **Suspended** with the plan ID in its message. Review and approve it as
  usual.
- If `spec.approvePlan` still holds the plan ID of the **current revision** —
  the normal situation after approving once from Git — the remediation plan
  gets the same ID (plan IDs are derived from the revision), matches, and is
  applied in the same run. Re-plan is then a one-click remediation. To review
  the remediation first, clear `spec.approvePlan` before pressing Re-plan.

Any change to the source or to the object spec also produces a fresh plan
that includes the accumulated drift, so committing a fix remediates drift as
a side effect.

## Drift that never goes away

If the drift event lists the **same resource** every time and applying it
does not make the drift stop, the resource's "real world" is the runner Pod
itself. Each run executes in a fresh Pod whose working directory and home are
`emptyDir` volumes, so resources that *are* local files — `local_file`,
`local_sensitive_file`, files written by `local-exec` — disappear when the
run ends and are reported missing on the next check, forever.

Keep such values in state and expose them as **outputs** (which land in the
`spec.writeOutputsToSecret` Secret) instead of writing them to disk; a
`tls_private_key` saved with `local_sensitive_file` is the classic case. Files
only needed *during* a run are fine. See
[Troubleshooting](../operator-manual/troubleshooting.md#the-object-flaps-between-degraded-and-progressing-re-planning-the-same-resource)
for a worked example and [Outputs](outputs.md).

## Outputs drift

After a clean "no drift" result the controller also verifies that the outputs
Secret (`spec.writeOutputsToSecret`) still exists and has exactly the expected
keys; if it was deleted or keys were removed, it is rewritten from the state
without an apply. Edited *values* are not detected — the Secret is a
projection of the state, not a source of truth.

## Seeing drift

- **Argo CD**: the Application turns Degraded; the Terraform tab, the
  Application tab and the sidebar dashboard show the drift plan under the
  Ready message. The `Re-plan` button starts remediation (manual mode asks
  for approval next).
- **kubectl**:

    ```sh
    kubectl -n team-a get tf network -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
    kubectl -n team-a get events --field-selector reason=DriftDetected --sort-by=.lastTimestamp
    kubectl -n team-a get tf network -o jsonpath='{.status.lastDriftDetectedAt}'
    ```

- **Prometheus**: Argo CD's `argocd_app_info{health_status="Degraded"}`
  covers it at the Application level; see [Metrics](../operator-manual/metrics.md).

## Drift vs. source changes

Drift detection only runs when the source revision is unchanged. A new commit
always produces a regular plan, which naturally includes any drift that
accumulated — there is no separate "drift" step in that case. Likewise a spec
change (new variables, a different workspace) triggers a plan, not a drift
check.

## Examples

Self-healing network stack:

```yaml
spec:
  interval: 15m
  approvePlan: auto
```

Audit-only on a stack applied by a CI pipeline elsewhere (the state backend
is shared, see [State](../operator-manual/state.md#other-backends)):

```yaml
spec:
  interval: 1h
  approvePlan: disable
  backendConfig:
    customConfiguration: |
      backend "s3" { … }
```

Manual gate with a loud alarm:

```yaml
spec:
  interval: 30m
  approvePlan: ""          # drift ⇒ Degraded until a human re-plans and approves
```
