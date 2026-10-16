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
| manual (`""` or a plan ID) | The run **stops** and reports Degraded. Review the drift in the event or the UI, then trigger **Re-plan**; the resulting plan waits for approval like any other change |
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

When drift detection fails repeatedly (expired credentials are the usual
cause), `spec.retryInterval` and `spec.retryStrategy` govern the retries like
for any other failure.

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
