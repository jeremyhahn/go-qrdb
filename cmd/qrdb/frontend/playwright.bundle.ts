// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import { existsSync, readdirSync, realpathSync, statSync } from "node:fs";
import { join } from "node:path";

// The mocked tier serves the production bundle with `vite preview`, which
// builds nothing: `make test-typescript` runs `npm run build` first. A run
// started any other way must not test a bundle older than its sources, so the
// config refuses to start one rather than serve it.

const root = import.meta.dirname;
const bundleIndex = join(root, "dist", "index.html");

// Every input the bundle is built from: the app, its build configuration, its
// locked dependencies and the built SDK it imports.
const inputs = [
  "src",
  "index.html",
  "vite.config.ts",
  "svelte.config.js",
  "tsconfig.json",
  "package-lock.json",
  "node_modules/@qrdb/sdk/dist",
];

/** Returns the newest modification time under path, or 0 if it is absent. */
function newestMtime(path: string): number {
  if (!existsSync(path)) {
    return 0;
  }
  const real = realpathSync(path);
  const stat = statSync(real);
  if (!stat.isDirectory()) {
    return stat.mtimeMs;
  }
  let newest = stat.mtimeMs;
  for (const entry of readdirSync(real, { recursive: true, withFileTypes: true })) {
    if (entry.isFile()) {
      newest = Math.max(newest, statSync(join(entry.parentPath, entry.name)).mtimeMs);
    }
  }
  return newest;
}

/**
 * Throws unless dist/ holds a bundle built after the last change to any of
 * its inputs.
 */
export function assertFreshBundle(): void {
  const fix = "run `npm run build` in cmd/qrdb/frontend (make test-typescript does)";
  if (!existsSync(bundleIndex)) {
    throw new Error(`mocked Playwright tier: no production bundle at dist/: ${fix}`);
  }
  const built = statSync(bundleIndex).mtimeMs;
  for (const input of inputs) {
    if (newestMtime(join(root, input)) > built) {
      throw new Error(`mocked Playwright tier: dist/ is older than ${input}: ${fix}`);
    }
  }
}
