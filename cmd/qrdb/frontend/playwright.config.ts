// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import { defineConfig, devices } from "@playwright/test";
import { HOST, PORT_ENV, PROXY_PORT_ENV, resolvePorts } from "./playwright.port.ts";

// Both ports are per-run, never fixed: concurrent CI jobs on one host share its
// loopback interface (see playwright.port.ts). The proxy port has no listener —
// this tier has no backend — so an unmocked request is refused rather than
// reaching another job's qrdbd.
const [port, proxyPort] = await resolvePorts(PORT_ENV, PROXY_PORT_ENV);
const origin = `http://${HOST}:${port}`;

export default defineConfig({
  testDir: "./e2e",
  // Exclude the real-backend and DOM tiers — both require a live qrdbd and
  // use playwright.real.config.ts / playwright.dom.config.ts instead.
  testIgnore: ["**/real-backend/**", "**/dom/**"],
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: "html",
  use: {
    baseURL: origin,
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  webServer: {
    // The tier runs against the production bundle, the one the desktop app
    // embeds, served by `vite preview` (which proxies the API prefixes exactly
    // as the dev server does). The dev server compiled and served the app as
    // ~140 separate modules, with Svelte's dev-mode runtime checks, on every
    // test's fresh browser context; the bundle is 8 requests of static files.
    // On a loaded CI runner that per-test boot was where specs ran out of
    // time, and every spec pays it.
    //
    // --strictPort: if another process takes the port first, fail rather than
    // let Vite pick a different one that baseURL does not point at.
    command: `npx vite build --logLevel warn && npx vite preview --host ${HOST} --port ${port} --strictPort`,
    // Budgets the production build above, which runs before the server
    // listens; Playwright's 60 s default is for a server alone.
    timeout: 300_000,
    url: origin,
    reuseExistingServer: !process.env.CI,
    env: { QRDB_PROXY_TARGET: `https://${HOST}:${proxyPort}` },
  },
});
