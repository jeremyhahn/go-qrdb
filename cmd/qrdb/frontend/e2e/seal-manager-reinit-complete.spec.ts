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
 * Seal Manager complete re-initialization flow tests.
 *
 * These tests exercise the full end-to-end re-initialization flow in the
 * Seal Manager admin view, including passphrase and Shamir modes, validation
 * error paths, and seal status updates after a successful operation.
 *
 * The re-initialize section is only rendered when the barrier is both
 * initialized and unsealed (initialized=true, sealed=false).
 *
 * Test coverage:
 *   1. Passphrase reinit completes successfully and shows a success toast.
 *   2. Shamir reinit completes successfully and shows a success toast.
 *   3. A passphrase shorter than 8 characters shows an error toast.
 *   4. Mismatched passphrases show an error toast.
 *   5. After a successful passphrase reinit the seal status stats reflect
 *      the updated "Unsealed" / "Yes" state.
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
 * Re-initialize Barrier section is visible and the reinit flow can be exercised.
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

/**
 * Opens the reinit form by clicking Configure, then waits until the
 * passphrase radio button is visible (form is fully expanded).
 */
async function openReinitForm(page: Page): Promise<void> {
  const main = page.locator("main");
  await main.getByRole("button", { name: "Configure" }).click();
  await expect(
    main.locator('input[name="reinit-mode"][value="passphrase"]'),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Seal Manager Re-initialization Complete Flow", () => {
  test.use({ mockState: unsealedInitializedState });

  // -------------------------------------------------------------------------
  // Passphrase success path
  // -------------------------------------------------------------------------

  test("passphrase reinit completes successfully", async ({ page }) => {
    await navigateToSealManager(page);
    await openReinitForm(page);

    const main = page.locator("main");

    // Passphrase is selected by default — fill both fields with a valid value.
    await main.locator("#reinit-passphrase").fill("secure-passphrase-1!");
    await main.locator("#reinit-passphrase-confirm").fill("secure-passphrase-1!");

    // Advance to the confirmation step.
    await main.getByRole("button", { name: "Re-initialize" }).click();

    // The warning alert must appear before the final confirmation.
    await expect(
      main.getByText("This will replace the existing barrier configuration", {
        exact: false,
      }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Confirm the operation.
    await main.getByRole("button", { name: "Confirm Re-initialize" }).click();

    // A success toast must confirm the re-initialization.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("re-initialized successfully");
  });

  // -------------------------------------------------------------------------
  // Shamir success path
  // -------------------------------------------------------------------------

  test("shamir reinit completes successfully", async ({ page }) => {
    await navigateToSealManager(page);
    await openReinitForm(page);

    const main = page.locator("main");

    // Switch to Shamir mode.
    await main.locator('input[name="reinit-mode"][value="shamir"]').click();

    // Wait for the Shamir inputs to appear, then set threshold=2 and shares=4.
    await expect(main.locator("#reinit-threshold")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await main.locator("#reinit-threshold").fill("2");
    await main.locator("#reinit-shares").fill("4");

    // Advance to the confirmation step.
    await main.getByRole("button", { name: "Re-initialize" }).click();

    // The warning alert must appear.
    await expect(
      main.getByText("This will replace the existing barrier configuration", {
        exact: false,
      }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Confirm the operation.
    await main.getByRole("button", { name: "Confirm Re-initialize" }).click();

    // A success toast must confirm the re-initialization.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("re-initialized successfully");
  });

  // -------------------------------------------------------------------------
  // Passphrase validation error paths
  // -------------------------------------------------------------------------

  test("passphrase too short shows error toast", async ({ page }) => {
    await navigateToSealManager(page);
    await openReinitForm(page);

    const main = page.locator("main");

    // Fill a passphrase that is under the 8-character minimum (both fields must
    // match so the mismatch check passes and the length check fires).
    await main.locator("#reinit-passphrase").fill("short");
    await main.locator("#reinit-passphrase-confirm").fill("short");

    // Advance to the confirmation step.
    await main.getByRole("button", { name: "Re-initialize" }).click();

    // Wait for the confirmation warning, then confirm to invoke handleReinitialize.
    await expect(
      main.getByRole("button", { name: "Confirm Re-initialize" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await main.getByRole("button", { name: "Confirm Re-initialize" }).click();

    // An error toast about the minimum length must appear.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("at least 8 characters");
  });

  test("passphrase mismatch shows error toast", async ({ page }) => {
    await navigateToSealManager(page);
    await openReinitForm(page);

    const main = page.locator("main");

    // Fill two different passphrases that satisfy the length requirement.
    await main.locator("#reinit-passphrase").fill("passphrase-one-1!");
    await main.locator("#reinit-passphrase-confirm").fill("passphrase-two-2!");

    // Advance to the confirmation step.
    await main.getByRole("button", { name: "Re-initialize" }).click();

    // Wait for the confirmation warning, then confirm to invoke handleReinitialize.
    await expect(
      main.getByRole("button", { name: "Confirm Re-initialize" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await main.getByRole("button", { name: "Confirm Re-initialize" }).click();

    // An error toast about the mismatch must appear.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("do not match");
  });

  // -------------------------------------------------------------------------
  // Seal status update after successful reinit
  // -------------------------------------------------------------------------

  test("seal status updates after reinit", async ({ page }) => {
    await navigateToSealManager(page);

    const main = page.locator("main");

    // Confirm the initial state shows Unsealed and Initialized = Yes before reinit.
    await expect(
      main.locator(".badge-success").getByText("Unsealed"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.locator(".badge-success").getByText("Yes"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Perform a successful passphrase reinit.
    await openReinitForm(page);
    await main.locator("#reinit-passphrase").fill("new-secure-passphrase-1!");
    await main.locator("#reinit-passphrase-confirm").fill("new-secure-passphrase-1!");
    await main.getByRole("button", { name: "Re-initialize" }).click();
    await expect(
      main.getByRole("button", { name: "Confirm Re-initialize" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await main.getByRole("button", { name: "Confirm Re-initialize" }).click();

    // Wait for the success toast.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("re-initialized successfully");

    // After reinit the status stats must still reflect Unsealed and Initialized.
    await expect(
      main.locator(".badge-success").getByText("Unsealed"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.locator(".badge-success").getByText("Yes"),
    ).toBeVisible();
  });
});
