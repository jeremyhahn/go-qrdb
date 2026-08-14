// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Playwright configuration for the real-backend DOM test tier.
 *
 * These specs serve the actual Svelte UI via a dedicated Vite dev server
 * (port 5273) whose API proxy targets a live qrdbd on the fixed admin port
 * 63012 (QRDB_PROXY_TARGET). Each spec boots a fresh daemon in the desired
 * seal mode via e2e/dom/fixtures/dom-daemon.ts and drives the real DOM in
 * Chromium — exercising the setup wizard, Seal Manager, and barrier flows
 * end-to-end against real crypto.
 *
 * Single worker: the daemon owns a fixed port (63012), so only one may run at
 * a time. Specs are sequential.
 */

import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e/dom",
  testIgnore: ["**/fixtures/**"],
  fullyParallel: false,
  forbidOnly: !!process.env["CI"],
  retries: 0,
  workers: 1,
  reporter: [["html", { outputFolder: "playwright-report-dom" }], ["list"]],
  use: {
    baseURL: "http://localhost:5273",
    ignoreHTTPSErrors: true,
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "chromium-dom",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  webServer: {
    // Dedicated dev server for the DOM tier: distinct port (5273) and an API
    // proxy aimed at the fixed DOM daemon port so it never collides with an
    // interactive dev session on 5173.
    command: "npx vite --port 5273 --strictPort",
    url: "http://localhost:5273",
    reuseExistingServer: false,
    timeout: 60_000,
    env: { QRDB_PROXY_TARGET: "https://127.0.0.1:63012" },
  },
});
