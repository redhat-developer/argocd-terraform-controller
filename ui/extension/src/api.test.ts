import { afterEach, describe, expect, it, vi } from 'vitest';

import { getReadablePlan, getTerraform, runAction, terraformNodes } from './api';
import type { Application, ResourceNode, Terraform } from './types';

const app: Application = { metadata: { name: 'infra', namespace: 'argocd' }, spec: { project: 'default' } };

function mockFetch(handler: (url: string, init?: RequestInit) => unknown) {
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const body = handler(String(input), init);
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

afterEach(() => vi.unstubAllGlobals());

describe('api', () => {
  it('loads the live Terraform manifest through the Argo CD resource API', async () => {
    const tf: Terraform = { apiVersion: 'infra.argocd.io/v1alpha1', kind: 'Terraform', metadata: { name: 'db', namespace: 'team-a' }, spec: {} };
    const fetch = mockFetch(() => ({ manifest: JSON.stringify(tf) }));

    const got = await getTerraform(app, { name: 'db', namespace: 'team-a' });
    expect(got.metadata.name).toBe('db');
    const url = new URL(String(fetch.mock.calls[0][0]), 'http://argocd');
    expect(url.pathname).toBe('/api/v1/applications/infra/resource');
    expect(Object.fromEntries(url.searchParams)).toMatchObject({
      appNamespace: 'argocd',
      namespace: 'team-a',
      resourceName: 'db',
      group: 'infra.argocd.io',
      kind: 'Terraform',
      version: 'v1alpha1',
    });
  });

  it('reassembles a chunked readable plan from the tree', async () => {
    const tf: Terraform = {
      apiVersion: 'infra.argocd.io/v1alpha1',
      kind: 'Terraform',
      metadata: { name: 'db', namespace: 'team-a' },
      spec: {},
      status: { planConfigMapName: 'tfplan-default-db-readable' },
    };
    const nodes: ResourceNode[] = [
      { group: '', kind: 'ConfigMap', version: 'v1', name: 'tfplan-default-db-readable-1', namespace: 'team-a' },
      { group: '', kind: 'ConfigMap', version: 'v1', name: 'tfplan-default-db-readable-0', namespace: 'team-a' },
      { group: '', kind: 'ConfigMap', version: 'v1', name: 'unrelated', namespace: 'team-a' },
    ];
    mockFetch((url) => {
      const name = new URL(url, 'http://argocd').searchParams.get('resourceName');
      return { manifest: JSON.stringify({ metadata: { name }, data: { tfplan: name?.endsWith('-0') ? 'first ' : 'second' } }) };
    });
    expect(await getReadablePlan(app, { nodes }, tf)).toBe('first second');
  });

  it('posts resource actions as a JSON string', async () => {
    const tf: Terraform = { apiVersion: 'infra.argocd.io/v1alpha1', kind: 'Terraform', metadata: { name: 'db', namespace: 'team-a' }, spec: {} };
    const fetch = mockFetch(() => ({}));
    await runAction(app, tf, 'approve-plan');
    const [url, init] = fetch.mock.calls[0];
    expect(new URL(String(url), 'http://argocd').pathname).toBe('/api/v1/applications/infra/resource/actions');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe('"approve-plan"');
  });

  it('surfaces API errors', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ message: 'permission denied' }), { status: 403 })));
    await expect(getTerraform(app, { name: 'db', namespace: 'team-a' })).rejects.toThrow(/permission denied/);
  });

  it('filters Terraform nodes from the tree', () => {
    const nodes: ResourceNode[] = [
      { group: 'infra.argocd.io', kind: 'Terraform', version: 'v1alpha1', name: 'a', namespace: 'ns' },
      { group: 'apps', kind: 'Deployment', version: 'v1', name: 'b', namespace: 'ns' },
    ];
    expect(terraformNodes({ nodes }).map((n) => n.name)).toEqual(['a']);
    expect(terraformNodes(undefined)).toEqual([]);
  });
});
