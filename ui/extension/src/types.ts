// Types for the subset of Argo CD and Terraform objects the extension uses.

export interface ObjectMeta {
  name: string;
  namespace?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  uid?: string;
}

export interface Condition {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

export interface TerraformSpec {
  approvePlan?: string;
  planOnly?: boolean;
  suspend?: boolean;
  force?: boolean;
  destroy?: boolean;
  destroyResourcesOnDeletion?: boolean;
  interval?: string;
  path?: string;
  workspace?: string;
  source?: {
    git?: { url: string; ref?: Record<string, string> };
    oci?: { url: string; ref?: Record<string, string> };
  };
  runner?: { binary?: string; image?: string };
  enableInventory?: boolean;
  writeOutputsToSecret?: { name: string; outputs?: string[] };
  tfstate?: { forceUnlock?: string; lockIdentifier?: string; deleteOnDestroy?: boolean };
  backendConfig?: { disable?: boolean; customConfiguration?: string; secretSuffix?: string };
  cloud?: { organization: string };
  dependsOn?: { name: string; namespace?: string }[];
}

export interface TerraformStatus {
  observedGeneration?: number;
  conditions?: Condition[];
  lastAppliedRevision?: string;
  lastAttemptedRevision?: string;
  lastPlannedRevision?: string;
  lastPlanAt?: string;
  lastSuccessfulReconcileAt?: string;
  lastDriftDetectedAt?: string;
  availableOutputs?: string[];
  plan?: { pending?: string; lastApplied?: string; isDestroyPlan?: boolean; isDriftDetectionPlan?: boolean };
  lock?: { pending?: string; lastApplied?: string };
  inventory?: { entries: { n: string; t: string; id: string }[] };
  reconciliationFailures?: number;
  binary?: string;
  stateSecretName?: string;
  planConfigMapName?: string;
}

export interface Terraform {
  apiVersion: string;
  kind: 'Terraform';
  metadata: ObjectMeta;
  spec: TerraformSpec;
  status?: TerraformStatus;
}

export interface ConfigMap {
  metadata: ObjectMeta;
  data?: Record<string, string>;
}

// Argo CD resource tree node (ui/src/app/shared/models.ts ResourceNode).
export interface ResourceNode {
  group: string;
  kind: string;
  version: string;
  name: string;
  namespace: string;
  uid?: string;
  parentRefs?: { group: string; kind: string; name: string; namespace: string; uid?: string }[];
  health?: { status: string; message?: string };
}

export interface ApplicationTree {
  nodes: ResourceNode[];
}

export interface Application {
  metadata: ObjectMeta;
  spec: { project: string };
  status?: {
    resources?: { group: string; kind: string; name: string; namespace: string }[];
  };
}

/**
 * Argo CD passes the live Kubernetes object (`State`) as `resource` to
 * resource tab extensions (resource-details.tsx), not the tree node.
 * Older builds and some docs show a tree node, so both are accepted.
 */
export type ResourceExtensionResource = { metadata: ObjectMeta } | ResourceNode;

export interface ResourceExtensionProps {
  application: Application;
  resource: ResourceExtensionResource;
  tree: ApplicationTree;
}

/** Name/namespace of a resource tab `resource` prop, whatever shape it has. */
export function resourceRef(resource: ResourceExtensionResource): { name: string; namespace: string } {
  if ('metadata' in resource && resource.metadata) {
    return { name: resource.metadata.name, namespace: resource.metadata.namespace ?? '' };
  }
  const node = resource as ResourceNode;
  return { name: node.name, namespace: node.namespace };
}

export interface AppViewExtensionProps {
  application: Application;
  tree: ApplicationTree;
}

export const TERRAFORM_GROUP = 'infra.argocd.io';
export const TERRAFORM_KIND = 'Terraform';
export const TERRAFORM_VERSION = 'v1alpha1';
export const HAS_TERRAFORM_LABEL = 'infra.argocd.io/has-terraform';
