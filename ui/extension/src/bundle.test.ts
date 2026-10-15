// Loads the built bundle (when present) the way Argo CD does: React on
// window and extensionsAPI registering the components.
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';

describe('built bundle', () => {
  const file = path.resolve(__dirname, '..', 'dist', 'extension-terraform.js');
  it.skipIf(!existsSync(file))('registers the resource tab, app view and (once Argo CD has rendered) the sidebar entry', async () => {
    vi.useFakeTimers();
    const calls: unknown[][] = [];
    const w = window as unknown as Record<string, unknown>;
    w.React = React;
    w.extensionsAPI = {
      registerSystemLevelExtension: (...a: unknown[]) => calls.push(['system', a[1], a[2], a[3]]),
      registerResourceExtension: (...a: unknown[]) => calls.push(['resource', a[1], a[2], a[3]]),
      registerAppViewExtension: (...a: unknown[]) => calls.push(['appview', a[1], a[2], typeof a[3]]),
    };
    const app = document.createElement('div');
    app.id = 'app';
    document.body.appendChild(app);

    new Function(readFileSync(file, 'utf8'))();
    // Resource/app-view extensions are read on demand; registered immediately.
    expect(calls).toEqual([
      ['resource', 'infra.argocd.io', 'Terraform', 'Terraform'],
      ['appview', 'Terraform', 'fa-cubes', 'function'],
    ]);
    expect(document.head.querySelector('style')?.textContent).toContain('.tf-ext');

    // The sidebar entry waits for Argo CD's <App> to mount (argoproj/argo-cd#29095).
    await vi.advanceTimersByTimeAsync(500);
    expect(calls).toHaveLength(2);
    app.appendChild(document.createElement('div'));
    await vi.advanceTimersByTimeAsync(100);
    expect(calls[2]).toEqual(['system', 'Terraform', '/terraform', 'fa-cubes']);
    vi.useRealTimers();
    app.remove();
  });
});
