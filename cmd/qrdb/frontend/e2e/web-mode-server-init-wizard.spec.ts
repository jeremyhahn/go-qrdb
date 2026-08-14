// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * web-mode-server-init-wizard.spec.ts (mocked tier)
 *
 * Verifies StepGlobalBarrier passphrase validation in the ServerInitWizard
 * rendered by App.svelte when the server is sealed and uninitialized.
 *
 * Bug 1 regression: after a passphrase mismatch is corrected the validation
 * alert must fully disappear — no stale empty .alert-error box may remain
 * (the root cause was a Svelte 5 fine-grained reactivity issue with a compound
 * &&-expression inside a {#if} condition that depended on a $derived value).
 *
 * No live qrdbd is required; all REST calls are intercepted via page.route().
 */

import { test as base, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Route injection — sealed + uninitialized server
// ---------------------------------------------------------------------------

/**
 * Registers page.route() handlers that simulate a passphrase-sealed,
 * uninitialized qrdbd (the state that causes App.svelte to render
 * ServerInitWizard instead of the main app shell).
 *
 * window.go is NOT injected, so isDesktop() returns false throughout.
 */
async function injectSealedUninitializedMock(page: Page): Promise<void> {
  // A sealed server returns 503 on /health. The SDK's connect() treats a 503
  // as "connected-but-sealed" so the app can proceed to the init/unseal flow.
  await page.route("**/health", (route) => {
    void route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: "sealed" }),
    });
  });

  // Barrier status: sealed and NOT yet initialized (initializedAt absent).
  // The REST client maps missing/empty initializedAt to initialized: false,
  // which triggers the "init" route in App.svelte.
  await page.route("**/v1/barrier/status", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        sealed: true,
        strategy: "passphrase",
        hardware_backed: false,
      }),
    });
  });
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

const test = base.extend<Record<string, never>>({
  page: async ({ page }, use) => {
    await injectSealedUninitializedMock(page);
    await use(page);
  },
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Web mode — ServerInitWizard (StepGlobalBarrier)", () => {
  test("routes to Server Initialization wizard on sealed+uninitialized server", async ({ page }) => {
    await page.goto("/");

    await expect(
      page.getByRole("heading", { name: "Server Initialization" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The Global Encryption Barrier step must be the first step shown.
    await expect(page.getByText("Global Encryption Barrier")).toBeVisible();
  });

  test("passphrase validation alert fully disappears once fields match (Bug 1 regression)", async ({ page }) => {
    // Regression guard: after correcting a mismatch the validation alert
    // must fully unmount — no empty .alert-error box may remain.
    await page.goto("/");
    await expect(
      page.getByRole("heading", { name: "Server Initialization" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The wizard lands on the barrier step. Passphrase is the default mode —
    // the passphrase inputs are rendered immediately without requiring a click
    // on the Passphrase card button. Wait for the inputs directly.
    await expect(page.locator("#setup-passphrase")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator("#setup-passphrase-confirm")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Pristine form: no validation alert may be shown before any interaction.
    await expect(page.locator(".alert.alert-error")).toHaveCount(0);

    // Fill the passphrase field to trigger the mismatch state.
    await page.locator("#setup-passphrase").fill("S3cur3P@ss!");
    // While confirm is still empty the error text must be visible.
    await expect(
      page.getByText("Passphrases do not match."),
    ).toBeVisible({ timeout: 5_000 });

    // Fix the confirm field — both fields now match.
    await page.locator("#setup-passphrase-confirm").fill("S3cur3P@ss!");

    // The error text must disappear entirely.
    await expect(
      page.getByText("Passphrases do not match."),
    ).toHaveCount(0);

    // No empty .alert-error box may remain (stale-{#if} regression).
    await expect(page.locator(".alert.alert-error")).toHaveCount(0);

    // Next must be enabled.
    await expect(
      page.getByRole("button", { name: "Next" }),
    ).not.toBeDisabled();
  });
});
