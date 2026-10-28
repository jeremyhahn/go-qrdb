// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import { defineConfig, devices } from "@playwright/test";
import { assertFreshBundle } from "./playwright.bundle.ts";
import { HOST, PORT_ENV, PROXY_PORT_ENV, resolvePorts } from "./playwright.port.ts";
import { resolveTLS } from "./playwright.tls.ts";

assertFreshBundle();

// Both ports are per-run, never fixed: concurrent CI jobs on one host share its
// loopback interface (see playwright.port.ts). The proxy port has no listener —
// this tier has no backend — so an unmocked request is refused rather than
// reaching another job's qrdbd.
const [port, proxyPort] = await resolvePorts(PORT_ENV, PROXY_PORT_ENV);
// The origin is https, as web mode's is: qrdbd serves the UI over TLS only,
// and every secret-bearing SDK call refuses an http base URL. The certificate
// is self-signed and generated per run in memory (playwright.tls.ts); only
// this tier's `ignoreHTTPSErrors` makes the browser accept it.
resolveTLS();
const origin = `https://${HOST}:${port}`;

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
    ignoreHTTPSErrors: true,
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
    // time, and every spec pays it. `make test-typescript` builds the bundle
    // as its own step and the config refuses a missing or stale one
    // (playwright.bundle.ts), so the server only serves and Playwright's
    // default start-up timeout holds.
    //
    // --strictPort: if another process takes the port first, fail rather than
    // let Vite pick a different one that baseURL does not point at.
    //
    // The server takes its certificate from the environment resolveTLS set,
    // which Playwright passes to the command (vite.config.ts serverTLS).
    command: `npx vite preview --host ${HOST} --port ${port} --strictPort`,
    url: origin,
    ignoreHTTPSErrors: true,
    reuseExistingServer: !process.env.CI,
    env: { QRDB_PROXY_TARGET: `https://${HOST}:${proxyPort}` },
  },
});
