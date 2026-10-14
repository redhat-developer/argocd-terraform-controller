import React from 'react';

import { getReadablePlan, getTerraform } from './api';
import { Actions, Badge, Field, Message, PlanOutput, usePolling } from './components';
import { approvalMode, backendSummary, condition, phase, planSummary, shortRevision, sourceSummary, timeAgo } from './model';
import type { ResourceExtensionProps, Terraform } from './types';

const POLL_MS = 5000;

/** The "Terraform" tab shown in the resource sliding panel. */
export default function TerraformTab({ application, resource, tree }: ResourceExtensionProps) {
  const { value, error, reload } = usePolling(
    async () => {
      const tf = await getTerraform(application, resource);
      const plan = await getReadablePlan(application, tree, tf).catch(() => null);
      return { tf, plan };
    },
    POLL_MS,
    [application.metadata.name, resource.name, resource.namespace, tree.nodes?.length],
  );

  if (error && !value) {
    return (
      <div className="tf-ext">
        <Message kind="error">{error}</Message>
      </div>
    );
  }
  if (!value) {
    return <div className="tf-ext tf-ext__empty">Loading…</div>;
  }
  return <TerraformDetails application={application} tf={value.tf} plan={value.plan} onChanged={reload} />;
}

interface DetailsProps {
  application: ResourceExtensionProps['application'];
  tf: Terraform;
  plan: string | null;
  onChanged: () => void;
}

export function TerraformDetails({ application, tf, plan, onChanged }: DetailsProps) {
  const { phase: p, message } = phase(tf);
  const status = tf.status ?? {};
  const ready = condition(tf, 'Ready');
  const pending = status.plan?.pending;
  const summary = plan ? planSummary(plan) : null;

  return (
    <div className="tf-ext">
      <div className="tf-ext__header">
        <h3 className="tf-ext__title">
          {tf.metadata.namespace}/{tf.metadata.name}
        </h3>
        <Badge phase={p} />
        {status.binary && <span className="tf-ext__chip">{status.binary}</span>}
        <Actions application={application} tf={tf} onDone={onChanged} />
      </div>

      {message && (
        <Message kind={p === 'Degraded' ? 'error' : p === 'AwaitingApproval' ? 'warning' : undefined}>
          {ready?.reason && <strong>{ready.reason}: </strong>}
          {message}
        </Message>
      )}

      <div className="tf-ext__grid">
        <Field label="Source">{sourceSummary(tf)}</Field>
        <Field label="Approval">{approvalMode(tf)}</Field>
        <Field label="State">{backendSummary(tf)}</Field>
        <Field label="Workspace">{tf.spec.workspace || 'default'}</Field>
        <Field label="Last applied revision">{shortRevision(status.lastAppliedRevision)}</Field>
        <Field label="Last attempted revision">{shortRevision(status.lastAttemptedRevision)}</Field>
        <Field label="Last plan">
          {timeAgo(status.lastPlanAt)}
          {status.plan?.lastApplied && <span className="tf-ext__muted"> · applied {status.plan.lastApplied}</span>}
        </Field>
        <Field label="Last successful reconcile">{timeAgo(status.lastSuccessfulReconcileAt)}</Field>
        {status.lastDriftDetectedAt && <Field label="Last drift detected">{timeAgo(status.lastDriftDetectedAt)}</Field>}
        {status.lock?.pending && <Field label="State lock">{status.lock.pending}</Field>}
        {(status.reconciliationFailures ?? 0) > 0 && <Field label="Consecutive failures">{status.reconciliationFailures}</Field>}
        {tf.spec.dependsOn?.length ? (
          <Field label="Depends on">{tf.spec.dependsOn.map((d) => `${d.namespace ?? tf.metadata.namespace}/${d.name}`).join(', ')}</Field>
        ) : null}
      </div>

      <div className="tf-ext__section">
        <h4 className="tf-ext__section-title">
          Plan
          {pending && <span className="tf-ext__chip">{pending}</span>}
          {status.plan?.isDestroyPlan && <span className="tf-ext__badge tf-ext__badge--Degraded">destroy</span>}
          {summary && <span className="tf-ext__muted">{summary}</span>}
        </h4>
        {plan ? (
          <PlanOutput plan={plan} />
        ) : (
          <div className="tf-ext__muted">
            {pending
              ? 'The readable plan is not available. Set spec.storeReadablePlan: human to store it in a ConfigMap.'
              : 'No pending plan.'}
          </div>
        )}
      </div>

      <div className="tf-ext__section">
        <h4 className="tf-ext__section-title">Outputs</h4>
        {status.availableOutputs?.length ? (
          <div>
            {status.availableOutputs.map((o) => (
              <span key={o} className="tf-ext__chip">
                {o}
              </span>
            ))}
            {tf.spec.writeOutputsToSecret && (
              <div className="tf-ext__muted" style={{ marginTop: 6 }}>
                Written to Secret <span className="mono">{tf.spec.writeOutputsToSecret.name}</span>
              </div>
            )}
          </div>
        ) : (
          <div className="tf-ext__muted">No outputs.</div>
        )}
      </div>

      {status.inventory?.entries?.length ? (
        <div className="tf-ext__section">
          <h4 className="tf-ext__section-title">Inventory ({status.inventory.entries.length})</h4>
          <table className="tf-ext__table">
            <thead>
              <tr>
                <th>Type</th>
                <th>Name</th>
                <th>Identifier</th>
              </tr>
            </thead>
            <tbody>
              {status.inventory.entries.map((e) => (
                <tr key={`${e.t}.${e.n}`}>
                  <td className="mono">{e.t}</td>
                  <td className="mono">{e.n}</td>
                  <td className="mono">{e.id}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      <div className="tf-ext__section">
        <h4 className="tf-ext__section-title">Conditions</h4>
        <table className="tf-ext__table">
          <thead>
            <tr>
              <th>Type</th>
              <th>Status</th>
              <th>Reason</th>
              <th>Message</th>
              <th>Since</th>
            </tr>
          </thead>
          <tbody>
            {(status.conditions ?? []).map((c) => (
              <tr key={c.type}>
                <td>{c.type}</td>
                <td>{c.status}</td>
                <td>{c.reason}</td>
                <td style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{c.message}</td>
                <td className="tf-ext__muted">{timeAgo(c.lastTransitionTime)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
