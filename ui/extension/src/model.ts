// Pure helpers over a Terraform object, shared by the tab and the app view.

import type { Condition, Terraform } from './types';

export type Phase = 'Healthy' | 'Progressing' | 'AwaitingApproval' | 'Degraded' | 'Suspended' | 'Unknown';

export function condition(tf: Terraform, type: string): Condition | undefined {
  return tf.status?.conditions?.find((c) => c.type === type);
}

export function pendingPlan(tf: Terraform): string {
  return tf.status?.plan?.pending ?? '';
}

export function isApproved(tf: Terraform): boolean {
  const pending = pendingPlan(tf);
  const approve = tf.spec.approvePlan ?? '';
  if (tf.spec.force || approve === 'auto') {
    return true;
  }
  return approve !== '' && pending !== '' && pending.startsWith(approve);
}

export function awaitingApproval(tf: Terraform): boolean {
  return pendingPlan(tf) !== '' && !isApproved(tf) && !tf.spec.planOnly && !tf.spec.suspend;
}

export function isLocked(tf: Terraform): boolean {
  return (tf.status?.lock?.pending ?? '') !== '';
}

/** Mirrors config/argocd/lua/health.lua so the tab and Argo CD agree. */
export function phase(tf: Terraform): { phase: Phase; message: string } {
  if (tf.spec.suspend) {
    return { phase: 'Suspended', message: 'Reconciliation is suspended' };
  }
  const stalled = condition(tf, 'Stalled');
  if (stalled?.status === 'True') {
    return { phase: 'Degraded', message: stalled.message ?? 'Stalled' };
  }
  const ready = condition(tf, 'Ready');
  if (!ready) {
    return { phase: 'Progressing', message: 'Waiting for the Terraform controller' };
  }
  const message = ready.message ?? '';
  if (ready.status === 'True') {
    return { phase: 'Healthy', message };
  }
  if (ready.status === 'False') {
    if (ready.reason === 'DependencyNotReady') {
      return { phase: 'Progressing', message };
    }
    return { phase: 'Degraded', message };
  }
  if (awaitingApproval(tf) && ready.reason === 'TerraformPlannedWithChanges') {
    return { phase: 'AwaitingApproval', message: `Plan ${pendingPlan(tf)} is waiting for approval` };
  }
  if (tf.spec.planOnly && pendingPlan(tf) !== '') {
    return { phase: 'Healthy', message: `Plan ${pendingPlan(tf)} generated (plan only mode)` };
  }
  return { phase: 'Progressing', message };
}

/** Shortens "main@sha1:0123456789abcdef..." to "main@0123456". */
export function shortRevision(rev?: string): string {
  if (!rev) {
    return '-';
  }
  const [ref, hash] = rev.split('@');
  if (!hash) {
    return rev.length > 12 ? rev.slice(0, 12) : rev;
  }
  const digest = hash.includes(':') ? hash.split(':')[1] : hash;
  return `${ref}@${digest.slice(0, 7)}`;
}

export function sourceSummary(tf: Terraform): string {
  const git = tf.spec.source?.git;
  if (git) {
    const ref = git.ref ? Object.entries(git.ref).map(([k, v]) => `${k}=${v}`).join(',') : 'HEAD';
    return `${git.url} (${ref})${tf.spec.path ? ` ${tf.spec.path}` : ''}`;
  }
  const oci = tf.spec.source?.oci;
  if (oci) {
    const ref = oci.ref ? Object.values(oci.ref).join('') : 'latest';
    return `${oci.url}:${ref}${tf.spec.path ? ` ${tf.spec.path}` : ''}`;
  }
  return '-';
}

export function backendSummary(tf: Terraform): string {
  if (tf.spec.cloud) {
    return `Terraform Cloud (${tf.spec.cloud.organization})`;
  }
  const bc = tf.spec.backendConfig;
  if (bc?.disable) {
    return 'disabled';
  }
  if (bc?.customConfiguration) {
    const match = bc.customConfiguration.match(/backend\s+"([^"]+)"/);
    return match ? `${match[1]} backend` : 'custom backend';
  }
  return tf.status?.stateSecretName ? `Kubernetes Secret ${tf.status.stateSecretName}` : 'Kubernetes backend';
}

export function approvalMode(tf: Terraform): string {
  if (tf.spec.planOnly) {
    return 'plan only';
  }
  if (tf.spec.force) {
    return 'force';
  }
  if (tf.spec.approvePlan === 'auto') {
    return 'auto approve';
  }
  if (tf.spec.approvePlan === 'disable') {
    return 'drift detection only';
  }
  return 'manual approval';
}

export function timeAgo(iso?: string, now: Date = new Date()): string {
  if (!iso) {
    return '-';
  }
  const seconds = Math.max(0, Math.round((now.getTime() - new Date(iso).getTime()) / 1000));
  if (seconds < 60) {
    return `${seconds}s ago`;
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m ago`;
  }
  const hours = Math.round(minutes / 60);
  if (hours < 48) {
    return `${hours}h ago`;
  }
  return `${Math.round(hours / 24)}d ago`;
}

/** Summarises a human readable plan: "Plan: 1 to add, 0 to change, 0 to destroy." */
export function planSummary(plan: string): string | null {
  const match = plan.match(/Plan: (\d+) to add, (\d+) to change, (\d+) to destroy\./);
  if (match) {
    return match[0];
  }
  if (/No changes\./.test(plan)) {
    return 'No changes.';
  }
  return null;
}
