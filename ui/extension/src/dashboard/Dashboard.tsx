import React, { useCallback, useEffect, useMemo, useState } from 'react';

import { getReadablePlan } from '../api';
import { Actions, Badge, Message, usePolling } from '../components';
import { approvalMode, phase, shortRevision, timeAgo, type Phase } from '../model';
import { TerraformDetails } from '../TerraformTab';
import type { ResourceNode } from '../types';
import { childrenOf, getEvents, getResourceTree, loadAllTerraform, type K8sEvent, type TerraformEntry } from './data';
import Graph from './Graph';

const POLL_MS = 10000;
const PARAM = 'tf';

const PHASE_ORDER: Phase[] = ['Degraded', 'AwaitingApproval', 'Progressing', 'Suspended', 'Healthy', 'Unknown'];
function useQueryParam(name: string): [string, (v: string) => void] {
  const read = () => new URLSearchParams(window.location.search).get(name) ?? '';
  const [value, setValue] = useState(read);
  useEffect(() => {
    const onPop = () => setValue(read());
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const set = useCallback(
    (v: string) => {
      const url = new URL(window.location.href);
      if (v) {
        url.searchParams.set(name, v);
      } else {
        url.searchParams.delete(name);
      }
      window.history.pushState(null, '', url.toString());
      setValue(v);
    },
    [name],
  );
  return [value, set];
}

/** Sidebar page: every Terraform object across all Applications. */
export default function Dashboard() {
  const { value, error, reload } = usePolling(loadAllTerraform, POLL_MS, []);
  const [selected, setSelected] = useQueryParam(PARAM);
  const [filter, setFilter] = useState('');
  const [phaseFilter, setPhaseFilter] = useState<Phase | ''>('');

  const entries = useMemo(() => value?.entries ?? [], [value]);
  const counts = useMemo(() => {
    const c: Partial<Record<Phase, number>> = {};
    for (const e of entries) {
      const p = phase(e.tf).phase;
      c[p] = (c[p] ?? 0) + 1;
    }
    return c;
  }, [entries]);

  const visible = entries.filter((e) => {
    if (phaseFilter && phase(e.tf).phase !== phaseFilter) {
      return false;
    }
    if (!filter) {
      return true;
    }
    const q = filter.toLowerCase();
    return e.key.toLowerCase().includes(q) || e.application.metadata.name.toLowerCase().includes(q) || (e.tf.status?.binary ?? '').toLowerCase().includes(q);
  });

  const current = entries.find((e) => e.key === selected);
  if (selected && current) {
    return <Detail entry={current} onBack={() => setSelected('')} onChanged={reload} />;
  }

  return (
    <div className="tf-ext tf-dash">
      <div className="tf-ext__header">
        <h3 className="tf-ext__title">Terraform</h3>
        <span className="tf-ext__muted">
          {value ? `${entries.length} object${entries.length === 1 ? '' : 's'} across Argo CD Applications` : 'Loading…'}
        </span>
      </div>

      <div className="tf-dash__summary">
        {PHASE_ORDER.filter((p) => counts[p]).map((p) => (
          <button key={p} className={`tf-dash__stat${phaseFilter === p ? ' tf-dash__stat--active' : ''}`} onClick={() => setPhaseFilter(phaseFilter === p ? '' : p)}>
            <span className="tf-dash__stat-count">{counts[p]}</span>
            <Badge phase={p} />
          </button>
        ))}
        <input className="tf-dash__search" placeholder="Filter by name, namespace, application…" value={filter} onChange={(e) => setFilter(e.target.value)} />
      </div>

      {error && <Message kind="error">{error}</Message>}
      {value?.errors.length ? <Message kind="warning">{value.errors.join('\n')}</Message> : null}

      {value && entries.length === 0 && (
        <div className="tf-ext__empty">
          No Terraform objects found in the Applications you can see. Objects must be synced by an Argo CD Application to appear here.
        </div>
      )}

      {visible.length > 0 && (
        <table className="tf-ext__table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>Application</th>
              <th>Approval</th>
              <th>Pending plan</th>
              <th>Applied</th>
              <th>Resources</th>
              <th>Last plan</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {visible.map((e) => {
              const { phase: p, message } = phase(e.tf);
              return (
                <tr key={e.key}>
                  <td>
                    <a
                      href="#"
                      className="mono"
                      onClick={(ev) => {
                        ev.preventDefault();
                        setSelected(e.key);
                      }}
                    >
                      {e.key}
                    </a>
                  </td>
                  <td title={message}>
                    <Badge phase={p} />
                  </td>
                  <td>
                    <a href={`/applications/${e.application.metadata.namespace ?? 'argocd'}/${e.application.metadata.name}`}>{e.application.metadata.name}</a>
                  </td>
                  <td>{approvalMode(e.tf)}</td>
                  <td className="mono">{e.tf.status?.plan?.pending || '-'}</td>
                  <td className="mono">{shortRevision(e.tf.status?.lastAppliedRevision)}</td>
                  <td>{e.tf.status?.inventory?.entries?.length ?? '-'}</td>
                  <td className="tf-ext__muted">{timeAgo(e.tf.status?.lastPlanAt)}</td>
                  <td>
                    <Actions application={e.application} tf={e.tf} onDone={reload} compact />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

interface DetailProps {
  entry: TerraformEntry;
  onBack: () => void;
  onChanged: () => void;
}

function Detail({ entry, onBack, onChanged }: DetailProps) {
  const { application, tf } = entry;
  const { value } = usePolling(
    async () => {
      const [tree, events] = await Promise.all([getResourceTree(application), getEvents(application, tf).catch(() => [] as K8sEvent[])]);
      const plan = await getReadablePlan(application, tree, tf).catch(() => null);
      return { children: childrenOf(tree, tf), events, plan };
    },
    POLL_MS,
    [application.metadata.name, tf.metadata.uid, tf.status?.lastPlanAt, tf.status?.planConfigMapName],
  );
  const children: ResourceNode[] = value?.children ?? [];

  return (
    <div className="tf-ext tf-dash">
      <div className="tf-dash__crumbs">
        <a
          href="#"
          onClick={(e) => {
            e.preventDefault();
            onBack();
          }}
        >
          Terraform
        </a>
        <span className="tf-ext__muted"> / </span>
        <span className="mono">{entry.key}</span>
        <span className="tf-ext__muted"> · Application </span>
        <a href={`/applications/${application.metadata.namespace ?? 'argocd'}/${application.metadata.name}`}>{application.metadata.name}</a>
      </div>

      <div className="tf-ext__section">
        <h4 className="tf-ext__section-title">Graph</h4>
        <Graph tf={tf} children={children} />
      </div>

      <TerraformDetails application={application} tf={tf} plan={value?.plan ?? null} onChanged={onChanged} hideInventory />

      <div className="tf-ext__section">
        <h4 className="tf-ext__section-title">Events</h4>
        {value?.events.length ? (
          <table className="tf-ext__table">
            <thead>
              <tr>
                <th>When</th>
                <th>Type</th>
                <th>Reason</th>
                <th>Message</th>
              </tr>
            </thead>
            <tbody>
              {value.events.slice(0, 20).map((ev, i) => (
                <tr key={i}>
                  <td className="tf-ext__muted">
                    {timeAgo(ev.lastTimestamp ?? ev.eventTime)}
                    {ev.count && ev.count > 1 ? ` (×${ev.count})` : ''}
                  </td>
                  <td>{ev.type}</td>
                  <td>{ev.reason}</td>
                  <td style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{ev.message}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <div className="tf-ext__muted">No recent events.</div>
        )}
      </div>
    </div>
  );
}
