import { render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import TerraformTab from './TerraformTab';
import type { Application, Terraform } from './types';

afterEach(() => vi.unstubAllGlobals());

const application: Application = { metadata: { name: 'infra', namespace: 'argocd' }, spec: { project: 'default' } };
const tf: Terraform = {
  apiVersion: 'infra.argocd.io/v1alpha1',
  kind: 'Terraform',
  metadata: { name: 'db', namespace: 'team-a' },
  spec: { approvePlan: 'auto', source: { git: { url: 'https://example.com/infra.git', ref: { branch: 'main' } } } },
  status: { conditions: [{ type: 'Ready', status: 'True', reason: 'ReconciliationSucceeded', message: 'Applied successfully' }] },
};

describe('TerraformTab', () => {
  it('reads name/namespace from the live object Argo CD passes as `resource`', async () => {
    const fetch = vi.fn<(input: RequestInfo | URL) => Promise<Response>>(async () => new Response(JSON.stringify({ manifest: JSON.stringify(tf) }), { status: 200 }));
    vi.stubGlobal('fetch', fetch);

    // Argo CD's resource-details passes the live Kubernetes object, not a tree node.
    render(<TerraformTab application={application} resource={tf} tree={{ nodes: [] }} />);

    expect(await screen.findByText('team-a/db')).toBeTruthy();
    const url = new URL(String(fetch.mock.calls[0][0]), 'http://argocd');
    expect(url.searchParams.get('resourceName')).toBe('db');
    expect(url.searchParams.get('namespace')).toBe('team-a');
  });

  it('still accepts a resource tree node', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ manifest: JSON.stringify(tf) }), { status: 200 })));
    render(
      <TerraformTab
        application={application}
        resource={{ group: 'infra.argocd.io', kind: 'Terraform', version: 'v1alpha1', name: 'db', namespace: 'team-a' }}
        tree={{ nodes: [] }}
      />,
    );
    expect(await screen.findByText('team-a/db')).toBeTruthy();
  });
});
