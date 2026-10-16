# Plan approval workflow

`spec.approvePlan` selects the mode:

| Value | Behaviour |
|---|---|
| `""` (default) | **Manual.** Every plan with changes waits until `spec.approvePlan` matches its ID. |
| `auto` | Every plan is applied immediately; drift is remediated automatically. |
| `<plan id>` | Approves that plan (prefix match allowed, e.g. `plan-main`). |
| `disable` | Drift detection only: plans run but are never stored or applied. |

Related switches: `spec.planOnly` stops after planning regardless of approval,
`spec.force` re-plans and applies unconditionally, `spec.destroy` plans a
destroy, `spec.suspend` pauses reconciliation.

## Plan IDs

A plan ID is derived from the source revision: `plan-<ref>-<10 hex chars>`,
e.g. `plan-main-0123456789` for `main@sha1:0123456789abcdef…`. It is shown in:

- `status.plan.pending`
- the Ready condition message: `Plan generated: set approvePlan: "plan-main-0123456789" to approve this plan.`
- the `TerraformPlanSucceed` event
- the Argo CD UI (Terraform tab and application view)

The same source revision always produces the same ID, so approving from git is
deterministic.

## Manual approval walkthrough

1. The controller plans and stores the binary plan in Secret
   `tfplan-<workspace>-<name>` and the readable plan in ConfigMap
   `tfplan-<workspace>-<name>-readable`. `status.plan.pending` is set and the
   Ready condition is `Unknown/TerraformPlannedWithChanges`. Argo CD shows the
   object as **Suspended**.
2. Review the plan: in the UI (Terraform tab) or
   `kubectl get cm tfplan-default-<name>-readable -o jsonpath='{.data.tfplan}'`.
3. Approve:
   - **git**: commit `spec.approvePlan: plan-main-0123456789` (recommended);
   - **UI**: *Approve plan* button (Argo CD resource action);
   - **kubectl**: `kubectl patch terraform <name> --type merge -p '{"spec":{"approvePlan":"plan-main-0123456789"}}'`.
4. Before applying, the controller plans again and only applies if the plan ID
   is unchanged — a source or spec change between approval and apply is never
   applied silently.
5. After the apply, `status.plan.lastApplied` holds the ID and the object is
   **Healthy**.

## When pending plans are discarded

The controller clears `status.plan.pending` and re-plans when:

- the source revision changes while the plan is unapproved;
- the object spec changes (generation) while the plan is unapproved;
- a reconcile is requested (`infra.argocd.io/reconcile` annotation, the
  *Re-plan* action) while the plan is unapproved;
- `spec.planOnly` and the revision changes.

## Approving from a pipeline

Because the plan ID is deterministic for a revision, approval can be automated
without giving up the gate — for example, a CI job that runs after a human
review of the plan in the Argo CD UI, or a ChatOps command:

```sh
PLAN=$(kubectl -n team-a get tf network -o jsonpath='{.status.plan.pending}')
test -n "$PLAN" || { echo "nothing to approve"; exit 0; }
kubectl -n team-a patch tf network --type merge -p "{\"spec\":{\"approvePlan\":\"$PLAN\"}}"
```

Or, GitOps-style, have the pipeline commit the ID to the manifest in Git and
let Argo CD sync it. The prefix match (`plan-main`) is convenient for
"approve whatever `main` currently plans" in lower environments, but it also
approves *future* plans for that ref — treat it like `auto` with a delay.

## Plan-only mode

`spec.planOnly: true` produces and stores plans but never applies, regardless
of `approvePlan`. The object is **Healthy** with the plan attached, so an
Application can show "here is what would change" without blocking anything.
Use it for review environments, or as a dry-run before flipping to manual
approval.

## Destroy

`spec.destroy: true` plans a destroy of everything in the state; the same
approval rules apply. Deleting an object with
`spec.destroyResourcesOnDeletion: true` does the same during finalization.
Both are covered in [Destroy and deletion](destroy-and-deletion.md).

## Failures and retries

Failed runs set `Ready=False` with a reason such as `TFExecPlanFailed`,
`TFExecApplyFailed`, `SourceFetchFailed`, `LockHeld` or `DriftDetected`, emit a
Warning event with the error, and retry after `spec.retryInterval` (default
15s; `spec.retryStrategy: ExponentialBackoff` doubles it per failure up to
`spec.maxRetryInterval`). `spec.remediation.retries` caps the attempts; the
object is then `Stalled` until the spec or source changes.
