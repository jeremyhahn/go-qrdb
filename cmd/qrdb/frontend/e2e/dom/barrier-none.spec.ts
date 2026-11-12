// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * barrier-none.spec.ts (real-backend DOM tier)
 *
 * With the seal subsystem disabled (no barrier), the UI must skip the
 * initialization/unseal flow entirely and load straight into the app, and the
 * Seal Manager must report "encryption not configured" rather than erroring on
 * the 404 that GET /v1/barrier/status returns when seal is off.
 *
 * Critically, when no encryption barrier exists there is nothing to seal, so
 * the Seal Manager must NOT present a Seal control (regression guard for the
 * bug where a "none"/unconfigured barrier showed a single-admin Seal button
 * that flipped the daemon into a meaningless sealed state).
 */

import { test, expect } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

test("no barrier: UI loads straight into the app (no init/unseal gate)", async ({ page }) => {
  await page.goto(UI_BASE_URL);

  // Goes directly to the app shell — no Server Initialization wizard, no
  // unseal screen, no Connection Error.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("heading", { name: "Server Initialization" })).toHaveCount(0);
  await expect(page.getByText("Connection Error")).toHaveCount(0);
});

test("no barrier: Seal Manager shows 'not configured' and no Seal control", async ({ page }) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 15_000 });
  await page.getByText("Seal Manager").first().click();

  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();

  // 404 from /v1/barrier/status is mapped to strategy "none" by the SDK, so the
  // page shows the "encryption not configured" panel rather than a transport
  // error — and the seal-state stat reads "Not configured", not "Unsealed".
  await expect(page.getByTestId("encryption-not-configured-card")).toBeVisible();
  await expect(page.getByTestId("seal-status-not-configured")).toBeVisible();

  // There is nothing to seal: the single-admin Seal button must be absent.
  await expect(page.getByRole("button", { name: "Seal", exact: true })).toHaveCount(0);
  // ...and there is no Unseal / submit-share control either.
  await expect(page.getByRole("button", { name: "Unseal", exact: true })).toHaveCount(0);

  // The daemon is bootstrapped (`qrdb init` ran with the "none" strategy),
  // so the strategy is fixed: enabling encryption is the online migration,
  // not the in-place "Configure Encryption" affordance of a fresh barrier.
  await expect(page.getByTestId("configure-encryption-btn")).toHaveCount(0);
  await expect(page.getByTestId("enable-encryption-submit")).toBeVisible();
});

test("no barrier: footer status badge reads 'Not encrypted'", async ({ page }) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 15_000 });

  // The persistent footer must not claim the server is "Unsealed" (which
  // implies an active, open encryption barrier) when no barrier is configured.
  const footer = page.locator("footer");
  await expect(footer.getByText("Not encrypted")).toBeVisible({ timeout: 15_000 });
  await expect(footer.getByText("Unsealed")).toHaveCount(0);
});
