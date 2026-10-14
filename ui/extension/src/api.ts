// Thin wrappers over the Argo CD API server. Requests are same-origin and
// authenticated by the Argo CD session cookie, and authorised by Argo CD
// RBAC for the Application, so the extension needs no backend of its own.

import type { Application, ConfigMap, ResourceNode, Terraform } from './types';
import { TERRAFORM_GROUP, TERRAFORM_KIND, TERRAFORM_VERSION } from './types';

function appPath(app: Application): string {
  return `/api/v1/applications/${encodeURIComponent(app.metadata.name)}`;
}

function resourceParams(app: Application, res: Pick<ResourceNode, 'group' | 'kind' | 'version' | 'name' | 'namespace'>): URLSearchParams {
  const params = new URLSearchParams({
    name: app.metadata.name,
    namespace: res.namespace,
    resourceName: res.name,
    version: res.version,
    kind: res.kind,
    group: res.group,
  });
  if (app.metadata.namespace) {
    params.set('appNamespace', app.metadata.namespace);
  }
  return params;
}

async function readError(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { message?: string; error?: string };
    return body.message || body.error || response.statusText;
  } catch {
    return response.statusText;
  }
}

/** Fetches the live manifest of a resource in the Application. */
export async function getResource<T>(app: Application, res: Pick<ResourceNode, 'group' | 'kind' | 'version' | 'name' | 'namespace'>): Promise<T> {
  const response = await fetch(`${appPath(app)}/resource?${resourceParams(app, res)}`, {
    headers: { Accept: 'application/json' },
  });
  if (!response.ok) {
    throw new Error(`failed to load ${res.kind} ${res.namespace}/${res.name}: ${await readError(response)}`);
  }
  const body = (await response.json()) as { manifest: string };
  return JSON.parse(body.manifest) as T;
}

export function getTerraform(app: Application, node: Pick<ResourceNode, 'name' | 'namespace'>): Promise<Terraform> {
  return getResource<Terraform>(app, {
    group: TERRAFORM_GROUP,
    kind: TERRAFORM_KIND,
    version: TERRAFORM_VERSION,
    name: node.name,
    namespace: node.namespace,
  });
}

/** Reads the human readable plan from the ConfigMap(s) written by the controller. */
export async function getReadablePlan(app: Application, tree: { nodes: ResourceNode[] }, tf: Terraform): Promise<string | null> {
  const name = tf.status?.planConfigMapName;
  if (!name) {
    return null;
  }
  const namespace = tf.metadata.namespace ?? '';
  // Large plans are chunked into <name>-0, <name>-1, ...; find them in the tree.
  const chunks = tree.nodes
    .filter((n) => n.kind === 'ConfigMap' && n.group === '' && n.namespace === namespace && (n.name === name || n.name.startsWith(`${name}-`)))
    .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
  if (chunks.length === 0) {
    return null;
  }
  const parts = await Promise.all(chunks.map((n) => getResource<ConfigMap>(app, n)));
  return parts.map((cm) => cm.data?.tfplan ?? '').join('');
}

/** Runs an Argo CD resource action (defined in argocd-cm) on a Terraform object. */
export async function runAction(app: Application, tf: Terraform, action: string): Promise<void> {
  const params = resourceParams(app, {
    group: TERRAFORM_GROUP,
    kind: TERRAFORM_KIND,
    version: TERRAFORM_VERSION,
    name: tf.metadata.name,
    namespace: tf.metadata.namespace ?? '',
  });
  const response = await fetch(`${appPath(app)}/resource/actions?${params}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(action),
  });
  if (!response.ok) {
    throw new Error(`action ${action} failed: ${await readError(response)}`);
  }
}

/** Lists the Terraform resources of an Application from its resource tree. */
export function terraformNodes(tree: { nodes?: ResourceNode[] } | undefined): ResourceNode[] {
  return (tree?.nodes ?? []).filter((n) => n.group === TERRAFORM_GROUP && n.kind === TERRAFORM_KIND);
}
