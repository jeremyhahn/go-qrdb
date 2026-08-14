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
 * Seal Manager re-initialization tests.
 *
 * These tests exercise the "Re-initialize Barrier" section of the Seal Manager
 * admin view.  The section is only rendered when the barrier is both initialized
 * and unsealed (sealed=false, initialized=true).
 *
 * Test coverage:
 *   1. Re-initialize Barrier heading is visible in the correct state.
 *   2. Configure button toggles the reinit form on and off.
 *   3. Passphrase mode shows passphrase / confirm inputs and a warning on submit.
 *   4. Shamir mode shows Threshold and Total Shares inputs.
 *   5. "Reconfigure Server" button is absent when MainApp renders SealManager
 *      without an onReconfigure prop (the production wiring).
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations and view transitions. */
const LOAD_TIMEOUT = 10_000;

/**
 * Mock state for an initialized, unsealed barrier — the state in which the
 * Re-initialize Barrier section is visible.
 */
const unsealedInitializedState = {
  ...secondLaunchEmbeddedState,
  sealStatus: {
    sealed: false,
    initialized: true,
    sharesRequired: 3,
    sharesProvided: 0,
    sharesTotal: 5,
  },
};

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

/** Navigates to the Seal Manager admin section. */
async function navigateToSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator("aside").getByText("Seal Manager").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Seal Manager"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Seal Manager Re-initialization", () => {
  test.describe("unsealed initialized state", () => {
    test.use({ mockState: unsealedInitializedState });

    // -------------------------------------------------------------------------
    // Section visibility
    // -------------------------------------------------------------------------

    test("re-initialize section is visible when unsealed and initialized", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      // The Re-initialize Barrier card heading must be visible.
      await expect(
        main.locator("h2").filter({ hasText: "Re-initialize Barrier" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // The Configure button must be present (form is collapsed by default).
      await expect(
        main.getByRole("button", { name: "Configure" }),
      ).toBeVisible();
    });

    test("re-initialize section description is visible", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      // The explanatory text should help the operator understand the operation.
      await expect(
        main.getByText("Re-initialize the encryption barrier", { exact: false }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    // -------------------------------------------------------------------------
    // Configure button toggle
    // -------------------------------------------------------------------------

    test("configure button shows reinit form when clicked", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      // The mode radio buttons must not be visible before the form is opened.
      await expect(
        main.locator('input[name="reinit-mode"]').first(),
      ).not.toBeVisible();

      // Click Configure to expand the form.
      await main.getByRole("button", { name: "Configure" }).click();

      // The passphrase radio button must now be visible.
      await expect(
        main.locator('input[name="reinit-mode"][value="passphrase"]'),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // The button label changes to "Cancel" when the form is open.
      await expect(
        main.getByRole("button", { name: "Cancel" }),
      ).toBeVisible();
    });

    test("configure button hides reinit form on second click", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      // Open the form.
      await main.getByRole("button", { name: "Configure" }).click();
      await expect(
        main.locator('input[name="reinit-mode"][value="passphrase"]'),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Close the form by clicking Cancel (the same button, relabelled).
      await main.getByRole("button", { name: "Cancel" }).click();

      // The radio buttons must be hidden again.
      await expect(
        main.locator('input[name="reinit-mode"]').first(),
      ).not.toBeVisible();

      // The button label must revert to "Configure".
      await expect(
        main.getByRole("button", { name: "Configure" }),
      ).toBeVisible();
    });

    // -------------------------------------------------------------------------
    // Passphrase mode
    // -------------------------------------------------------------------------

    test("passphrase reinit shows passphrase and confirm fields", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Configure" }).click();

      // Passphrase is the default mode — both inputs must appear.
      await expect(main.locator("#reinit-passphrase")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await expect(main.locator("#reinit-passphrase-confirm")).toBeVisible();
    });

    test("passphrase reinit shows confirmation warning on Re-initialize click", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Configure" }).click();

      // Fill in valid passphrase fields.
      await main.locator("#reinit-passphrase").fill("secure-passphrase-1!");
      await main
        .locator("#reinit-passphrase-confirm")
        .fill("secure-passphrase-1!");

      // Click Re-initialize to advance to the confirmation step.
      await main.getByRole("button", { name: "Re-initialize" }).click();

      // The warning alert about replacing the existing configuration must appear.
      await expect(
        main.getByText("This will replace the existing barrier configuration", {
          exact: false,
        }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // The destructive "Confirm Re-initialize" button must be visible.
      await expect(
        main.getByRole("button", { name: "Confirm Re-initialize" }),
      ).toBeVisible();
    });

    test("passphrase reinit cancel from confirmation hides warning", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Configure" }).click();

      await main.locator("#reinit-passphrase").fill("secure-passphrase-1!");
      await main
        .locator("#reinit-passphrase-confirm")
        .fill("secure-passphrase-1!");
      await main.getByRole("button", { name: "Re-initialize" }).click();

      // Confirm warning is visible.
      await expect(
        main.getByRole("button", { name: "Confirm Re-initialize" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Click the inline Cancel to dismiss the confirmation.
      const reinitCard = main.locator(".card", {
        hasText: "Re-initialize Barrier",
      });
      await reinitCard.locator("button", { hasText: /^Cancel$/ }).click();

      // The confirmation warning and Confirm button must disappear.
      await expect(
        main.getByRole("button", { name: "Confirm Re-initialize" }),
      ).not.toBeVisible();

      // The Re-initialize button must return.
      await expect(
        main.getByRole("button", { name: "Re-initialize" }),
      ).toBeVisible();
    });

    // -------------------------------------------------------------------------
    // Shamir mode
    // -------------------------------------------------------------------------

    test("shamir reinit shows threshold and total shares fields", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Configure" }).click();

      // Select the Shamir radio button.
      await main
        .locator('input[name="reinit-mode"][value="shamir"]')
        .click();

      // Threshold and Total Shares inputs must appear.
      await expect(main.locator("#reinit-threshold")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await expect(main.locator("#reinit-shares")).toBeVisible();

      // The Threshold label should be present.
      await expect(
        main.locator(".label-text").filter({ hasText: "Threshold (M)" }),
      ).toBeVisible();

      // The Total Shares label should be present.
      await expect(
        main.locator(".label-text").filter({ hasText: "Total Shares (N)" }),
      ).toBeVisible();
    });

    test("shamir reinit hides passphrase fields when shamir is selected", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Configure" }).click();

      // Switch to Shamir mode.
      await main
        .locator('input[name="reinit-mode"][value="shamir"]')
        .click();

      // Passphrase inputs must be hidden in Shamir mode.
      await expect(main.locator("#reinit-passphrase")).not.toBeVisible();
      await expect(main.locator("#reinit-passphrase-confirm")).not.toBeVisible();

      // Shamir inputs must be visible.
      await expect(main.locator("#reinit-threshold")).toBeVisible();
      await expect(main.locator("#reinit-shares")).toBeVisible();
    });

    // -------------------------------------------------------------------------
    // Reconfigure Server button
    // -------------------------------------------------------------------------

    test("reconfigure server button is absent when onReconfigure is not provided", async ({
      page,
    }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      // MainApp renders <SealManager {client} /> without the onReconfigure prop,
      // so the "Reconfigure Server" card and button must never be rendered.
      await expect(
        main.getByRole("button", { name: "Reconfigure Server" }),
      ).not.toBeVisible({ timeout: LOAD_TIMEOUT });

      await expect(
        main.getByText("Server Configuration", { exact: true }),
      ).not.toBeVisible();
    });
  });
});
