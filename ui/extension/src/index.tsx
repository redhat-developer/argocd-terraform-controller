import './styles.css';
import AppView, { shouldDisplay } from './AppView';
import Dashboard from './dashboard/Dashboard';
import TerraformTab from './TerraformTab';
import { TERRAFORM_GROUP, TERRAFORM_KIND } from './types';
import type { Application } from './types';

// Argo CD exposes the extension registry on window.extensionsAPI; see
// https://argo-cd.readthedocs.io/en/stable/developer-guide/extensions/ui-extensions/
interface ExtensionsAPI {
  registerResourceExtension(component: unknown, group: string, kind: string, tabTitle: string, opts?: { icon?: string }): void;
  registerAppViewExtension(component: unknown, title: string, icon: string, shouldDisplay?: (app?: Application) => boolean): void;
  registerSystemLevelExtension(component: unknown, title: string, path: string, icon: string): void;
}

declare global {
  interface Window {
    extensionsAPI?: ExtensionsAPI;
  }
}

const api = window.extensionsAPI;
if (api) {
  // Per-resource tab and per-Application view. These are pulled from the
  // registry at render time, so they can be registered right away.
  api.registerResourceExtension(TerraformTab, TERRAFORM_GROUP, TERRAFORM_KIND, 'Terraform', { icon: 'fa-cubes' });
  api.registerAppViewExtension(AppView, 'Terraform', 'fa-cubes', shouldDisplay);

  // Sidebar entry: the Terraform dashboard across all Applications.
  //
  // System-level extensions are delivered to Argo CD's <App> through a one-shot
  // 'systemLevel' event whose listener is attached in the App constructor.
  // Since Argo CD v3.5 (React 18, createRoot) that constructor runs
  // asynchronously, so an extension registering synchronously from
  // extensions.js is silently dropped (argoproj/argo-cd#29095). Wait until the
  // app has actually rendered before registering.
  whenAppMounted(() => api.registerSystemLevelExtension(Dashboard, 'Terraform', '/terraform', 'fa-cubes'));
}

function whenAppMounted(register: () => void, deadlineMs = 15000): void {
  const started = Date.now();
  const tick = () => {
    const mounted = (document.getElementById('app')?.childElementCount ?? 0) > 0;
    if (mounted || Date.now() - started > deadlineMs) {
      register();
      return;
    }
    setTimeout(tick, 50);
  };
  // Always yield at least once so the App constructor gets a chance to run first.
  setTimeout(tick, 0);
}
