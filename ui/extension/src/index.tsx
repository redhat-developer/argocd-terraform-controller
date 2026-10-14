import './styles.css';
import AppView, { shouldDisplay } from './AppView';
import TerraformTab from './TerraformTab';
import { TERRAFORM_GROUP, TERRAFORM_KIND } from './types';
import type { Application } from './types';

// Argo CD exposes the extension registry on window.extensionsAPI; see
// https://argo-cd.readthedocs.io/en/stable/developer-guide/extensions/ui-extensions/
interface ExtensionsAPI {
  registerResourceExtension(component: unknown, group: string, kind: string, tabTitle: string, opts?: { icon?: string }): void;
  registerAppViewExtension(component: unknown, title: string, icon: string, shouldDisplay?: (app?: Application) => boolean): void;
}

declare global {
  interface Window {
    extensionsAPI?: ExtensionsAPI;
  }
}

const api = window.extensionsAPI;
if (api) {
  api.registerResourceExtension(TerraformTab, TERRAFORM_GROUP, TERRAFORM_KIND, 'Terraform', { icon: 'fa-cubes' });
  api.registerAppViewExtension(AppView, 'Terraform', 'fa-cubes', shouldDisplay);
}
