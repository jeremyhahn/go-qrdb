// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Playwright configuration for the real-backend test tier.
 *
 * These specs spawn an actual qrdbd daemon and drive the REST API directly via
 * Playwright's APIRequestContext (ignoreHTTPSErrors=true for self-signed certs).
 * They run in a separate directory (e2e/real-backend/) and use a distinct npm
 * script (test:e2e:real) so they never interfere with the existing mocked tier.
 *
 * No Vite dev server is needed — specs assert API state directly. DOM rendering
 * assertions are out of scope for this tier; the goal is contract coverage of
 * the real server + SDK serialization.
 */

import { defineConfig, devices } from "@playwright/test";
import { shutdownDaemon } from "./e2e/real-backend/fixtures/daemon.ts";

export default defineConfig({
  testDir: "./e2e/real-backend",
  // Exclude the fixtures directory — it contains helpers, not specs.
  testIgnore: ["**/fixtures/**"],
  fullyParallel: false,
  forbidOnly: !!process.env["CI"],
  retries: 0,
  // Single worker: the shared daemon singleton is per-worker; one worker avoids
  // port contention and keeps the suite under 3 minutes.
  workers: 1,
  reporter: [["html", { outputFolder: "playwright-report-real" }], ["list"]],
  use: {
    // No baseURL — specs use the daemon URL directly.
    ignoreHTTPSErrors: true,
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "chromium-real",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  globalTeardown: "./e2e/real-backend/global-teardown.ts",
});
