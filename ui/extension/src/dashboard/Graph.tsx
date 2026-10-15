import React from 'react';

import { pendingPlan, sourceSummary } from '../model';
import type { ResourceNode, Terraform } from '../types';
import { groupInventory } from './data';

interface GraphProps {
  tf: Terraform;
  children: ResourceNode[];
}

/**
 * Delivery graph for one Terraform object, left to right:
 * Source → Terraform → managed resources (from the state inventory, i.e.
 * anything the providers created, Kubernetes or not) and the Kubernetes
 * objects the controller keeps around it (plan, outputs, state, runner).
 */
export default function Graph({ tf, children }: GraphProps) {
  const groups = groupInventory(tf);
  const total = groups.reduce((n, g) => n + g.entries.length, 0);
  const inventoryEnabled = tf.spec.enableInventory !== false;
  const state = tf.status?.stateSecretName;
  const outputs = tf.spec.writeOutputsToSecret?.name;
  const k8s = children.filter((c) => !(c.kind === 'Secret' && (c.name === state || c.name === outputs)));

  return (
    <div className="tf-graph">
      <div className="tf-graph__col">
        <div className="tf-graph__col-title">Source</div>
        <div className="tf-graph__node tf-graph__node--source">
          <div className="tf-graph__kind">{tf.spec.source?.git ? 'Git' : tf.spec.source?.oci ? 'OCI' : 'Source'}</div>
          <div className="tf-graph__name">{sourceSummary(tf)}</div>
          <div className="tf-graph__meta">{tf.status?.lastAttemptedRevision ?? '-'}</div>
        </div>
      </div>

      <div className="tf-graph__arrow">→</div>

      <div className="tf-graph__col">
        <div className="tf-graph__col-title">Terraform</div>
        <div className="tf-graph__node tf-graph__node--tf">
          <div className="tf-graph__kind">Terraform · {tf.status?.binary ?? tf.spec.runner?.binary ?? 'auto'}</div>
          <div className="tf-graph__name">
            {tf.metadata.namespace}/{tf.metadata.name}
          </div>
          <div className="tf-graph__meta">
            {pendingPlan(tf) ? `pending ${pendingPlan(tf)}` : tf.status?.plan?.lastApplied ? `applied ${tf.status.plan.lastApplied}` : 'no plan yet'}
          </div>
        </div>
        <div className="tf-graph__col-title" style={{ marginTop: 12 }}>
          Kubernetes objects
        </div>
        {state && (
          <div className="tf-graph__node tf-graph__node--k8s">
            <div className="tf-graph__kind">Secret · state</div>
            <div className="tf-graph__name">{state}</div>
          </div>
        )}
        {outputs && (
          <div className="tf-graph__node tf-graph__node--k8s">
            <div className="tf-graph__kind">Secret · outputs</div>
            <div className="tf-graph__name">{outputs}</div>
            {tf.status?.availableOutputs?.length ? <div className="tf-graph__meta">{tf.status.availableOutputs.join(', ')}</div> : null}
          </div>
        )}
        {k8s.map((c) => (
          <div key={`${c.kind}/${c.namespace}/${c.name}`} className="tf-graph__node tf-graph__node--k8s">
            <div className="tf-graph__kind">
              {c.kind}
              {c.health?.status ? ` · ${c.health.status}` : ''}
            </div>
            <div className="tf-graph__name">{c.name}</div>
          </div>
        ))}
        {!state && !outputs && k8s.length === 0 && <div className="tf-ext__muted">none yet</div>}
      </div>

      <div className="tf-graph__arrow">→</div>

      <div className="tf-graph__col tf-graph__col--wide">
        <div className="tf-graph__col-title">
          Managed resources <span className="tf-ext__muted">({total})</span>
        </div>
        {!inventoryEnabled && <div className="tf-ext__muted">spec.enableInventory is false; managed resources are not recorded.</div>}
        {inventoryEnabled && total === 0 && <div className="tf-ext__muted">Nothing in the state yet. The inventory is recorded after each apply.</div>}
        {groups.map((g) => (
          <div key={g.type} className="tf-graph__group">
            <div className="tf-graph__group-title">
              <span className="tf-graph__provider">{g.provider}</span> {g.type} <span className="tf-ext__muted">({g.entries.length})</span>
            </div>
            {g.entries.map((e) => (
              <div key={e.n} className="tf-graph__node tf-graph__node--res" title={e.id}>
                <div className="tf-graph__name">{e.n}</div>
                {e.id && <div className="tf-graph__meta">{e.id}</div>}
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
