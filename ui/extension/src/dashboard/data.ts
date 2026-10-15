// Data access for the sidebar dashboard. Argo CD only exposes
// Application-scoped APIs to the UI, so the dashboard aggregates over every
// Application the user may see: Terraform objects are discovered through
// status.resources and read with the resource API. Argo CD RBAC applies as
// usual; Terraform objects that no Application manages are not shown.

import { getTerraform } from '../api';
import type { Application, ResourceNode, Terraform } from '../types';
import { TERRAFORM_GROUP, TERRAFORM_KIND, TERRAFORM_VERSION } from '../types';

/** A Terraform object together with the Application that manages it. */
export interface TerraformEntry {
  key: string; // namespace/name
  application: Application;
  node: ResourceNode;
  tf: Terraform;
}

export interface K8sEvent {
  reason: string;
  message: string;
  type: string;
  count?: number;
  lastTimestamp?: string;
  eventTime?: string;
}

async function getJSON<T>(url: string): Promise<T> {
  const response = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!response.ok) {
    let msg = response.statusText;
    try {
      const body = (await response.json()) as { message?: string; error?: string };
      msg = body.message || body.error || msg;
    } catch {
      // ignore
    }
    throw new Error(`${url}: ${msg}`);
  }
  return (await response.json()) as T;
}

/** Lists the Applications visible to the user (metadata, project, resources). */
export async function listApplications(): Promise<Application[]> {
  const fields = [
    'items.metadata.name',
    'items.metadata.namespace',
    'items.metadata.labels',
    'items.spec.project',
    'items.status.resources',
  ].join(',');
  const body = await getJSON<{ items?: Application[] }>(`/api/v1/applications?fields=${encodeURIComponent(fields)}`);
  return body.items ?? [];
}

/** Returns the Terraform resources an Application manages, from status.resources. */
export function terraformResourcesOf(app: Application): ResourceNode[] {
  return (app.status?.resources ?? [])
    .filter((r) => r.group === TERRAFORM_GROUP && r.kind === TERRAFORM_KIND)
    .map((r) => ({ group: r.group, kind: r.kind, version: TERRAFORM_VERSION, name: r.name, namespace: r.namespace }));
}

/** Runs fn over items with at most limit in flight, preserving order. */
export async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const results: R[] = new Array(items.length);
  let next = 0;
  const workers = Array.from({ length: Math.max(1, Math.min(limit, items.length)) }, async () => {
    for (;;) {
      const i = next++;
      if (i >= items.length) {
        return;
      }
      results[i] = await fn(items[i]);
    }
  });
  await Promise.all(workers);
  return results;
}

/** Discovers and loads every Terraform object across all Applications. */
export async function loadAllTerraform(): Promise<{ entries: TerraformEntry[]; errors: string[] }> {
  const apps = await listApplications();
  const refs = apps.flatMap((application) => terraformResourcesOf(application).map((node) => ({ application, node })));
  const errors: string[] = [];
  const loaded = await mapLimit(refs, 6, async ({ application, node }) => {
    try {
      const tf = await getTerraform(application, node);
      return { key: `${node.namespace}/${node.name}`, application, node, tf } as TerraformEntry;
    } catch (e) {
      errors.push(e instanceof Error ? e.message : String(e));
      return null;
    }
  });
  const entries = loaded.filter((e): e is TerraformEntry => e !== null).sort((a, b) => a.key.localeCompare(b.key));
  return { entries, errors };
}

/** Loads the Application resource tree. */
export async function getResourceTree(app: Application): Promise<{ nodes: ResourceNode[] }> {
  const params = new URLSearchParams();
  if (app.metadata.namespace) {
    params.set('appNamespace', app.metadata.namespace);
  }
  const body = await getJSON<{ nodes?: ResourceNode[] }>(`/api/v1/applications/${encodeURIComponent(app.metadata.name)}/resource-tree?${params}`);
  return { nodes: body.nodes ?? [] };
}

/** Kubernetes objects owned (transitively) by the Terraform object, from the tree. */
export function childrenOf(tree: { nodes: ResourceNode[] }, tf: Terraform): ResourceNode[] {
  const out: ResourceNode[] = [];
  const seen = new Set<string>();
  const isRoot = (p: NonNullable<ResourceNode['parentRefs']>[number]) =>
    (tf.metadata.uid && p.uid === tf.metadata.uid) || (p.kind === TERRAFORM_KIND && p.name === tf.metadata.name && p.namespace === tf.metadata.namespace);
  const visit = (match: (p: NonNullable<ResourceNode['parentRefs']>[number]) => boolean) => {
    for (const n of tree.nodes) {
      const id = n.uid ?? `${n.kind}/${n.namespace}/${n.name}`;
      if (!seen.has(id) && (n.parentRefs ?? []).some(match)) {
        seen.add(id);
        out.push(n);
        if (n.uid) {
          visit((p) => p.uid === n.uid);
        }
      }
    }
  };
  visit(isRoot);
  return out.sort((a, b) => `${a.kind}/${a.name}`.localeCompare(`${b.kind}/${b.name}`));
}

/** Recent Kubernetes events for the Terraform object. */
export async function getEvents(app: Application, tf: Terraform): Promise<K8sEvent[]> {
  const params = new URLSearchParams({
    resourceName: tf.metadata.name,
    resourceNamespace: tf.metadata.namespace ?? '',
  });
  if (tf.metadata.uid) {
    params.set('resourceUID', tf.metadata.uid);
  }
  if (app.metadata.namespace) {
    params.set('appNamespace', app.metadata.namespace);
  }
  const body = await getJSON<{ items?: K8sEvent[] }>(`/api/v1/applications/${encodeURIComponent(app.metadata.name)}/events?${params}`);
  return (body.items ?? []).sort((a, b) => (b.lastTimestamp ?? b.eventTime ?? '').localeCompare(a.lastTimestamp ?? a.eventTime ?? ''));
}

/** Groups inventory entries by Terraform resource type (e.g. aws_s3_bucket). */
export function groupInventory(tf: Terraform): { type: string; provider: string; entries: { n: string; t: string; id: string }[] }[] {
  const groups = new Map<string, { n: string; t: string; id: string }[]>();
  for (const e of tf.status?.inventory?.entries ?? []) {
    const list = groups.get(e.t) ?? [];
    list.push(e);
    groups.set(e.t, list);
  }
  return [...groups.entries()]
    .map(([type, entries]) => ({ type, provider: type.split('_')[0], entries: entries.sort((a, b) => a.n.localeCompare(b.n)) }))
    .sort((a, b) => a.type.localeCompare(b.type));
}
