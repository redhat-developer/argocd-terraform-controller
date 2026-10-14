import { describe, expect, it } from 'vitest';

import { approvalMode, awaitingApproval, backendSummary, phase, planSummary, shortRevision, timeAgo } from './model';
import type { Terraform } from './types';

function tf(spec: Terraform['spec'] = {}, status: Terraform['status'] = {}): Terraform {
  return { apiVersion: 'infra.argocd.io/v1alpha1', kind: 'Terraform', metadata: { name: 'x', namespace: 'ns' }, spec, status };
}

describe('phase', () => {
  it('is Progressing without status', () => {
    expect(phase(tf()).phase).toBe('Progressing');
  });
  it('is Suspended when suspended', () => {
    expect(phase(tf({ suspend: true })).phase).toBe('Suspended');
  });
  it('is Healthy when Ready=True', () => {
    expect(phase(tf({}, { conditions: [{ type: 'Ready', status: 'True', message: 'ok' }] }))).toEqual({ phase: 'Healthy', message: 'ok' });
  });
  it('is AwaitingApproval for an unapproved pending plan', () => {
    const t = tf({}, { plan: { pending: 'plan-main-abc' }, conditions: [{ type: 'Ready', status: 'Unknown', reason: 'TerraformPlannedWithChanges' }] });
    expect(awaitingApproval(t)).toBe(true);
    expect(phase(t)).toEqual({ phase: 'AwaitingApproval', message: 'Plan plan-main-abc is waiting for approval' });
  });
  it('is Progressing once the plan is approved by id or auto', () => {
    const status: Terraform['status'] = { plan: { pending: 'plan-main-abc' }, conditions: [{ type: 'Ready', status: 'Unknown', reason: 'TerraformPlannedWithChanges', message: 'Plan generated' }] };
    expect(phase(tf({ approvePlan: 'plan-main-abc' }, status)).phase).toBe('Progressing');
    expect(phase(tf({ approvePlan: 'plan-main' }, status)).phase).toBe('Progressing');
    expect(phase(tf({ approvePlan: 'auto' }, status)).phase).toBe('Progressing');
    expect(phase(tf({ approvePlan: 'plan-other' }, status)).phase).toBe('AwaitingApproval');
  });
  it('is Healthy in plan only mode with a plan', () => {
    const t = tf({ planOnly: true }, { plan: { pending: 'plan-1' }, conditions: [{ type: 'Ready', status: 'Unknown', reason: 'TerraformPlannedWithChanges' }] });
    expect(phase(t).phase).toBe('Healthy');
  });
  it('is Degraded on failure, Progressing on dependency wait, Degraded when stalled', () => {
    expect(phase(tf({}, { conditions: [{ type: 'Ready', status: 'False', reason: 'TFExecApplyFailed' }] })).phase).toBe('Degraded');
    expect(phase(tf({}, { conditions: [{ type: 'Ready', status: 'False', reason: 'DependencyNotReady' }] })).phase).toBe('Progressing');
    expect(phase(tf({}, { conditions: [{ type: 'Ready', status: 'False' }, { type: 'Stalled', status: 'True', message: 'nope' }] }))).toEqual({ phase: 'Degraded', message: 'nope' });
  });
});

describe('formatting', () => {
  it('shortens revisions', () => {
    expect(shortRevision('main@sha1:0123456789abcdef')).toBe('main@0123456');
    expect(shortRevision('v1.2.3@sha256:abcdef0123456789')).toBe('v1.2.3@abcdef0');
    expect(shortRevision(undefined)).toBe('-');
  });
  it('describes approval mode and backend', () => {
    expect(approvalMode(tf({ approvePlan: 'auto' }))).toBe('auto approve');
    expect(approvalMode(tf({}))).toBe('manual approval');
    expect(approvalMode(tf({ planOnly: true }))).toBe('plan only');
    expect(backendSummary(tf({ backendConfig: { customConfiguration: 'backend "s3" {\n bucket = "x"\n}' } }))).toBe('s3 backend');
    expect(backendSummary(tf({}, { stateSecretName: 'tfstate-default-x' }))).toBe('Kubernetes Secret tfstate-default-x');
    expect(backendSummary(tf({ cloud: { organization: 'acme' } }))).toBe('Terraform Cloud (acme)');
  });
  it('formats relative times', () => {
    const now = new Date('2026-01-01T00:10:00Z');
    expect(timeAgo('2026-01-01T00:09:30Z', now)).toBe('30s ago');
    expect(timeAgo('2026-01-01T00:00:00Z', now)).toBe('10m ago');
    expect(timeAgo('2025-12-31T00:10:00Z', now)).toBe('24h ago');
  });
  it('summarises plans', () => {
    expect(planSummary('...\nPlan: 1 to add, 0 to change, 2 to destroy.\n')).toBe('Plan: 1 to add, 0 to change, 2 to destroy.');
    expect(planSummary('No changes. Your infrastructure matches the configuration.')).toBe('No changes.');
    expect(planSummary('garbage')).toBeNull();
  });
});
