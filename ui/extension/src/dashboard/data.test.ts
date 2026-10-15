import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Application, ResourceNode, Terraform } from '../types';
import { childrenOf, groupInventory, loadAllTerraform, mapLimit, terraformResourcesOf } from './data';

afterEach(() => vi.unstubAllGlobals());

const tfObj = (name: string, ns: string, extra: Partial<Terraform> = {}): Terraform => ({
  apiVersion: 'infra.argocd.io/v1alpha1',
  kind: 'Terraform',
  metadata: { name, namespace: ns, uid: `uid-${name}` },
  spec: {},
  ...extra,
});

describe('terraformResourcesOf', () => {
  it('picks Terraform resources out of status.resources', () => {
    const app: Application = {
      metadata: { name: 'infra' },
      spec: { project: 'default' },
      status: {
        resources: [
          { group: 'apps', kind: 'Deployment', name: 'web', namespace: 'a' },
          { group: 'infra.argocd.io', kind: 'Terraform', name: 'net', namespace: 'a' },
        ],
      },
    };
    expect(terraformResourcesOf(app)).toEqual([{ group: 'infra.argocd.io', kind: 'Terraform', version: 'v1alpha1', name: 'net', namespace: 'a' }]);
  });
});

describe('mapLimit', () => {
  it('preserves order and bounds concurrency', async () => {
    let inFlight = 0;
    let max = 0;
    const out = await mapLimit([1, 2, 3, 4, 5], 2, async (n) => {
      inFlight++;
      max = Math.max(max, inFlight);
      await new Promise((r) => setTimeout(r, 5));
      inFlight--;
      return n * 2;
    });
    expect(out).toEqual([2, 4, 6, 8, 10]);
    expect(max).toBeLessThanOrEqual(2);
  });
});

describe('loadAllTerraform', () => {
  it('aggregates Terraform objects across Applications and reports per-object errors', async () => {
    const apps = {
      items: [
        { metadata: { name: 'infra', namespace: 'argocd' }, spec: { project: 'p' }, status: { resources: [
          { group: 'infra.argocd.io', kind: 'Terraform', name: 'net', namespace: 'team-a' },
          { group: 'infra.argocd.io', kind: 'Terraform', name: 'db', namespace: 'team-a' },
        ] } },
        { metadata: { name: 'web' }, spec: { project: 'p' }, status: { resources: [{ group: 'apps', kind: 'Deployment', name: 'web', namespace: 'web' }] } },
      ],
    };
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://argocd');
        if (url.pathname === '/api/v1/applications') {
          return new Response(JSON.stringify(apps), { status: 200 });
        }
        const name = url.searchParams.get('resourceName');
        if (name === 'db') {
          return new Response(JSON.stringify({ message: 'permission denied' }), { status: 403 });
        }
        return new Response(JSON.stringify({ manifest: JSON.stringify(tfObj(name!, 'team-a')) }), { status: 200 });
      }),
    );
    const { entries, errors } = await loadAllTerraform();
    expect(entries.map((e) => e.key)).toEqual(['team-a/net']);
    expect(entries[0].application.metadata.name).toBe('infra');
    expect(errors).toHaveLength(1);
    expect(errors[0]).toMatch(/permission denied/);
  });
});

describe('childrenOf', () => {
  it('returns objects owned transitively by the Terraform object', () => {
    const tf = tfObj('net', 'a');
    const nodes: ResourceNode[] = [
      { group: 'infra.argocd.io', kind: 'Terraform', version: 'v1alpha1', name: 'net', namespace: 'a', uid: 'uid-net' },
      { group: '', kind: 'Secret', version: 'v1', name: 'tfplan-default-net', namespace: 'a', uid: 's1', parentRefs: [{ group: 'infra.argocd.io', kind: 'Terraform', name: 'net', namespace: 'a', uid: 'uid-net' }] },
      { group: '', kind: 'Pod', version: 'v1', name: 'net-tf-runner', namespace: 'a', uid: 'p1', parentRefs: [{ group: 'infra.argocd.io', kind: 'Terraform', name: 'net', namespace: 'a', uid: 'uid-net' }] },
      { group: '', kind: 'ConfigMap', version: 'v1', name: 'nested', namespace: 'a', uid: 'c1', parentRefs: [{ group: '', kind: 'Pod', name: 'net-tf-runner', namespace: 'a', uid: 'p1' }] },
      { group: 'apps', kind: 'Deployment', version: 'v1', name: 'unrelated', namespace: 'a', uid: 'd1' },
    ];
    expect(childrenOf({ nodes }, tf).map((n) => n.name)).toEqual(['nested', 'net-tf-runner', 'tfplan-default-net']);
  });
});

describe('groupInventory', () => {
  it('groups by resource type and derives the provider', () => {
    const tf = tfObj('net', 'a', {
      status: {
        inventory: {
          entries: [
            { n: 'aws_s3_bucket.logs', t: 'aws_s3_bucket', id: 'arn:aws:s3:::logs' },
            { n: 'aws_vpc.main', t: 'aws_vpc', id: 'vpc-1' },
            { n: 'aws_s3_bucket.data', t: 'aws_s3_bucket', id: 'arn:aws:s3:::data' },
            { n: 'kubernetes_namespace.demo', t: 'kubernetes_namespace', id: 'demo' },
          ],
        },
      },
    });
    const groups = groupInventory(tf);
    expect(groups.map((g) => [g.type, g.provider, g.entries.length])).toEqual([
      ['aws_s3_bucket', 'aws', 2],
      ['aws_vpc', 'aws', 1],
      ['kubernetes_namespace', 'kubernetes', 1],
    ]);
    expect(groups[0].entries.map((e) => e.n)).toEqual(['aws_s3_bucket.data', 'aws_s3_bucket.logs']);
  });
});
