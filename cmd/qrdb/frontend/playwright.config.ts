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
    // --strictPort: if another process takes the port first, fail rather than
    // let Vite pick a different one that baseURL does not point at.
    command: `npx vite --host ${HOST} --port ${port} --strictPort`,
    url: origin,
    reuseExistingServer: !process.env.CI,
    env: { QRDB_PROXY_TARGET: `https://${HOST}:${proxyPort}` },
  },
});
