import React, { useCallback, useEffect, useState } from 'react';

import { runAction } from './api';
import { awaitingApproval, isLocked, pendingPlan, phase, type Phase } from './model';
import type { Application, Terraform } from './types';

export function Badge({ phase: p }: { phase: Phase }) {
  const label = p === 'AwaitingApproval' ? 'Awaiting approval' : p;
  return <span className={`tf-ext__badge tf-ext__badge--${p}`}>{label}</span>;
}

export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="tf-ext__field">
      <span className="tf-ext__label">{label}</span>
      <span className="tf-ext__value">{children}</span>
    </div>
  );
}

export function Message({ kind, children }: { kind?: 'error' | 'warning'; children: React.ReactNode }) {
  return <div className={`tf-ext__message${kind ? ` tf-ext__message--${kind}` : ''}`}>{children}</div>;
}

/** Highlights +/-/~ lines of a human readable plan. */
export function PlanOutput({ plan }: { plan: string }) {
  const lines = plan.split('\n').map((line, i) => {
    const trimmed = line.trimStart();
    let cls: string | undefined;
    if (trimmed.startsWith('+') || trimmed.startsWith('# ') && /will be created/.test(trimmed)) {
      cls = 'add';
    } else if (trimmed.startsWith('-') || /will be destroyed/.test(trimmed)) {
      cls = 'destroy';
    } else if (trimmed.startsWith('~') || /will be updated|must be replaced/.test(trimmed)) {
      cls = 'change';
    }
    return (
      <div key={i} className={cls}>
        {line}
      </div>
    );
  });
  return <pre className="tf-ext__plan">{lines}</pre>;
}

interface ActionsProps {
  application: Application;
  tf: Terraform;
  onDone?: () => void;
  compact?: boolean;
}

/** Action buttons backed by the Argo CD resource actions from argocd-cm. */
export function Actions({ application, tf, onDone, compact }: ActionsProps) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const run = useCallback(
    async (action: string, confirmText?: string) => {
      if (confirmText && !window.confirm(confirmText)) {
        return;
      }
      setBusy(action);
      setError(null);
      try {
        await runAction(application, tf, action);
        onDone?.();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(null);
      }
    },
    [application, tf, onDone],
  );

  const pending = pendingPlan(tf);
  const { phase: p } = phase(tf);
  return (
    <div className="tf-ext__actions">
      {awaitingApproval(tf) && (
        <button
          className="tf-ext__button tf-ext__button--primary"
          disabled={busy !== null}
          onClick={() => run('approve-plan', `Approve plan ${pending}? The controller will apply it.`)}
          title={`Set spec.approvePlan to ${pending}`}
        >
          {busy === 'approve-plan' ? 'Approving…' : 'Approve plan'}
        </button>
      )}
      {!tf.spec.suspend && (
        <button className="tf-ext__button" disabled={busy !== null} onClick={() => run('replan')} title="Reconcile now; discards an unapproved plan">
          {busy === 'replan' ? 'Requesting…' : pending && !compact ? 'Re-plan' : 'Reconcile'}
        </button>
      )}
      {!compact &&
        (tf.spec.suspend ? (
          <button className="tf-ext__button" disabled={busy !== null} onClick={() => run('resume')}>
            Resume
          </button>
        ) : (
          <button className="tf-ext__button" disabled={busy !== null} onClick={() => run('suspend')}>
            Suspend
          </button>
        ))}
      {!compact && isLocked(tf) && (
        <button
          className="tf-ext__button tf-ext__button--danger"
          disabled={busy !== null}
          onClick={() =>
            run('force-unlock', `Force unlock state lock ${tf.status?.lock?.pending}? Only do this if no other process is using the state.`)
          }
        >
          Force unlock
        </button>
      )}
      {error && <Message kind="error">{error}</Message>}
      {p === 'Degraded' && compact && <span className="tf-ext__muted">needs attention</span>}
    </div>
  );
}

/** Polls fn every intervalMs and exposes the latest value. */
export function usePolling<T>(fn: () => Promise<T>, intervalMs: number, deps: unknown[]): { value: T | null; error: string | null; reload: () => void } {
  const [value, setValue] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const v = await fn();
        if (!cancelled) {
          setValue(v);
          setError(null);
        }
      } catch (e) {
        if (!cancelled) {
          setError(e instanceof Error ? e.message : String(e));
        }
      }
    };
    void load();
    const id = window.setInterval(load, intervalMs);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick, intervalMs]);

  return { value, error, reload };
}
