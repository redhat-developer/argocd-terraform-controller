import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Terraform } from '../types';
import Dashboard from './Dashboard';

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.replaceState(null, '', '/extensions/terraform');
});

const tf: Terraform = {
  apiVersion: 'infra.argocd.io/v1alpha1',
  kind: 'Terraform',
  metadata: { name: 'network', namespace: 'team-a', uid: 'uid-network' },
  spec: { approvePlan: '', source: { git: { url: 'https://example.com/infra.git', ref: { branch: 'main' } } }, writeOutputsToSecret: { name: 'network-outputs' } },
  status: {
    conditions: [{ type: 'Ready', status: 'Unknown', reason: 'TerraformPlannedWithChanges', message: 'Plan generated: set approvePlan: "plan-main-abc" to approve this plan.' }],
    plan: { pending: 'plan-main-abc' },
    lastAttemptedRevision: 'main@sha1:abcdef0123456789',
    stateSecretName: 'tfstate-default-network',
    planConfigMapName: 'tfplan-default-network-readable',
    binary: 'tofu 1.13.1',
    inventory: {
      entries: [
        { n: 'aws_vpc.main', t: 'aws_vpc', id: 'vpc-0123456789' },
        { n: 'aws_s3_bucket.logs', t: 'aws_s3_bucket', id: 'arn:aws:s3:::acme-logs' },
      ],
    },
  },
};

function stubArgoCD() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://argocd');
      const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200 });
      if (url.pathname === '/api/v1/applications') {
        return json({
          items: [{ metadata: { name: 'infra', namespace: 'argocd' }, spec: { project: 'default' }, status: { resources: [{ group: 'infra.argocd.io', kind: 'Terraform', name: 'network', namespace: 'team-a' }] } }],
        });
      }
      if (url.pathname.endsWith('/resource-tree')) {
        return json({
          nodes: [
            { group: 'infra.argocd.io', kind: 'Terraform', version: 'v1alpha1', name: 'network', namespace: 'team-a', uid: 'uid-network' },
            { group: '', kind: 'ConfigMap', version: 'v1', name: 'tfplan-default-network-readable', namespace: 'team-a', uid: 'cm1', parentRefs: [{ group: 'infra.argocd.io', kind: 'Terraform', name: 'network', namespace: 'team-a', uid: 'uid-network' }] },
            { group: '', kind: 'Pod', version: 'v1', name: 'network-tf-runner', namespace: 'team-a', uid: 'p1', health: { status: 'Healthy' }, parentRefs: [{ group: 'infra.argocd.io', kind: 'Terraform', name: 'network', namespace: 'team-a', uid: 'uid-network' }] },
          ],
        });
      }
      if (url.pathname.endsWith('/events')) {
        return json({ items: [{ type: 'Normal', reason: 'TerraformPlanSucceed', message: 'Planned.', lastTimestamp: new Date().toISOString() }] });
      }
      if (url.pathname.endsWith('/resource')) {
        if (url.searchParams.get('kind') === 'ConfigMap') {
          return json({ manifest: JSON.stringify({ metadata: { name: 'tfplan-default-network-readable' }, data: { tfplan: '  + resource "aws_vpc" "main"\nPlan: 1 to add, 0 to change, 0 to destroy.' } }) });
        }
        return json({ manifest: JSON.stringify(tf) });
      }
      return new Response('not found', { status: 404 });
    }),
  );
}

describe('Dashboard', () => {
  it('lists Terraform objects across Applications and drills into the graph', async () => {
    stubArgoCD();
    render(<Dashboard />);

    await waitFor(() => expect(screen.getByText('team-a/network')).toBeTruthy());
    expect(screen.getByText('1 object across Argo CD Applications')).toBeTruthy();
    expect(screen.getAllByText('Awaiting approval').length).toBeGreaterThan(0);
    expect(screen.getByText('infra')).toBeTruthy();

    await act(async () => {
      fireEvent.click(screen.getByText('team-a/network'));
    });
    expect(window.location.search).toContain('tf=team-a%2Fnetwork');

    // Graph: source, Kubernetes objects and non-Kubernetes managed resources.
    await waitFor(() => expect(screen.getByText('aws_s3_bucket.logs')).toBeTruthy());
    expect(screen.getByText('arn:aws:s3:::acme-logs')).toBeTruthy();
    expect(screen.getByText('aws_vpc.main')).toBeTruthy();
    expect(screen.getByText('tfstate-default-network')).toBeTruthy();
    expect(screen.getByText('network-tf-runner')).toBeTruthy();
    expect(screen.getByText(/Managed resources/)).toBeTruthy();

    // Details and plan output from the ConfigMap.
    await waitFor(() => expect(screen.getAllByText('Plan: 1 to add, 0 to change, 0 to destroy.').length).toBeGreaterThan(0));
    expect(screen.getByText('+ resource "aws_vpc" "main"')).toBeTruthy();
    expect(screen.getAllByText('Approve plan').length).toBeGreaterThan(0);
    expect(screen.getByText('TerraformPlanSucceed')).toBeTruthy();
  });

  it('explains when nothing is managed by Argo CD', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200 })));
    render(<Dashboard />);
    await waitFor(() => expect(screen.getByText(/No Terraform objects found/)).toBeTruthy());
  });
});
