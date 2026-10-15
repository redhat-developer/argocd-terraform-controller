// Loads the built bundle (when present) the way Argo CD does: React on
// window and extensionsAPI registering the components.
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import React from 'react';
import { describe, expect, it } from 'vitest';

describe('built bundle', () => {
  const file = path.resolve(__dirname, '..', 'dist', 'extension-terraform.js');
  it.skipIf(!existsSync(file))('registers the resource tab and app view', () => {
    const calls: unknown[][] = [];
    const w = window as unknown as Record<string, unknown>;
    w.React = React;
    w.extensionsAPI = {
      registerSystemLevelExtension: (...a: unknown[]) => calls.push(['system', a[1], a[2], a[3]]),
      registerResourceExtension: (...a: unknown[]) => calls.push(['resource', a[1], a[2], a[3]]),
      registerAppViewExtension: (...a: unknown[]) => calls.push(['appview', a[1], a[2], typeof a[3]]),
    };
    new Function(readFileSync(file, 'utf8'))();
    expect(calls).toEqual([
      ['system', 'Terraform', '/terraform', 'fa-cubes'],
      ['resource', 'infra.argocd.io', 'Terraform', 'Terraform'],
      ['appview', 'Terraform', 'fa-cubes', 'function'],
    ]);
    expect(document.head.querySelector('style')?.textContent).toContain('.tf-ext');
  });
});
