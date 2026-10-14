import React, { useState } from 'react';

import { getReadablePlan, getTerraform, terraformNodes } from './api';
import { Actions, Badge, Message, usePolling } from './components';
import { approvalMode, phase, shortRevision, timeAgo } from './model';
import { TerraformDetails } from './TerraformTab';
import type { Application, AppViewExtensionProps, Terraform } from './types';
import { HAS_TERRAFORM_LABEL, TERRAFORM_GROUP, TERRAFORM_KIND } from './types';

const POLL_MS = 5000;

/** Shows the view when the Application manages Terraform objects. */
export function shouldDisplay(application?: Application): boolean {
  if (!application) {
    return false;
  }
  const label = application.metadata.labels?.[HAS_TERRAFORM_LABEL];
  if (label !== undefined) {
    return label === 'true';
  }
  return (application.status?.resources ?? []).some((r) => r.group === TERRAFORM_GROUP && r.kind === TERRAFORM_KIND);
}

/** Application-level view listing every Terraform object of the app. */
export default function AppView({ application, tree }: AppViewExtensionProps) {
  const nodes = terraformNodes(tree);
  const [selected, setSelected] = useState<string | null>(null);

  const { value, error, reload } = usePolling(
    () => Promise.all(nodes.map((n) => getTerraform(application, n))),
    POLL_MS,
    [application.metadata.name, nodes.map((n) => `${n.namespace}/${n.name}`).join(',')],
  );

  if (nodes.length === 0) {
    return <div className="tf-ext tf-ext__empty">This Application has no Terraform resources.</div>;
  }
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

  const selectedTF = value.find((tf) => `${tf.metadata.namespace}/${tf.metadata.name}` === selected);
  const awaiting = value.filter((tf) => phase(tf).phase === 'AwaitingApproval').length;

  return (
    <div className="tf-ext" style={{ padding: 16 }}>
      <div className="tf-ext__header">
        <h3 className="tf-ext__title">Terraform</h3>
        <span className="tf-ext__muted">
          {value.length} object{value.length === 1 ? '' : 's'}
          {awaiting > 0 && `, ${awaiting} awaiting approval`}
        </span>
      </div>
      <table className="tf-ext__table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Status</th>
            <th>Approval</th>
            <th>Pending plan</th>
            <th>Applied</th>
            <th>Last plan</th>
            <th>Binary</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {value.map((tf) => {
            const key = `${tf.metadata.namespace}/${tf.metadata.name}`;
            const { phase: p, message } = phase(tf);
            return (
              <tr key={key} style={{ background: selected === key ? '#f5f7f9' : undefined }}>
                <td>
                  <a href="#" onClick={(e) => { e.preventDefault(); setSelected(selected === key ? null : key); }} className="mono">
                    {key}
                  </a>
                </td>
                <td title={message}>
                  <Badge phase={p} />
                </td>
                <td>{approvalMode(tf)}</td>
                <td className="mono">{tf.status?.plan?.pending || '-'}</td>
                <td className="mono">{shortRevision(tf.status?.lastAppliedRevision)}</td>
                <td className="tf-ext__muted">{timeAgo(tf.status?.lastPlanAt)}</td>
                <td>{tf.status?.binary || '-'}</td>
                <td>
                  <Actions application={application} tf={tf} onDone={reload} compact />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {selectedTF && (
        <div className="tf-ext__section">
          <SelectedDetails application={application} tree={tree} tf={selectedTF} onChanged={reload} />
        </div>
      )}
    </div>
  );
}

function SelectedDetails({ application, tree, tf, onChanged }: AppViewExtensionProps & { tf: Terraform; onChanged: () => void }) {
  const { value: plan } = usePolling(() => getReadablePlan(application, tree, tf), POLL_MS, [tf.status?.planConfigMapName, tf.status?.lastPlanAt]);
  return <TerraformDetails application={application} tf={tf} plan={plan ?? null} onChanged={onChanged} />;
}
