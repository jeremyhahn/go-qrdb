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
 * Database wizard encryption barrier tests.
 *
 * Covers all storage + encryption combinations and Shamir M-of-N validation.
 *
 * The wizard step sequence for standalone servers:
 *   Name → Storage → SQL → Barrier → Summary → Creating → sidebar
 *
 * The Raft Configuration step is skipped entirely for standalone servers.
 * The "Mode" step was also removed from the database wizard.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Timeout for heading waits during async initialization phases. */
const INIT_TIMEOUT = 10_000;

/**
 * Navigate to the database creation wizard from the empty state.
 */
async function openCreateWizard(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  // Wait for the app to load into MainApp
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
  // Click the "New Database" button in the sidebar
  await page.locator("aside").getByText("New Database").click();
  // Verify the wizard's first step appears
  await expect(
    page.getByRole("heading", { name: "Database Name & Engine" }),
  ).toBeVisible();
}

/**
 * Fill in the database name and select an engine, then click Next.
 */
async function fillNameAndAdvance(
  page: import("@playwright/test").Page,
  name: string,
  engine: "Key-Value" | "TimeSeries" | "Vector" = "Key-Value",
): Promise<void> {
  await page.locator("#db-name").fill(name);
  // Click the engine card
  await page.locator("button").filter({ hasText: engine }).click();
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Select a storage backend and advance to the next step.
 */
async function selectStorageAndAdvance(
  page: import("@playwright/test").Page,
  backend: "Memory" | "File" | "Pebble",
): Promise<void> {
  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
  if (backend !== "Pebble") {
    await page.locator("label").filter({ hasText: backend }).click();
  }
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Advance through the SQL Wire Protocols step with defaults.
 */
async function advanceThroughSQL(page: import("@playwright/test").Page): Promise<void> {
  await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Select encryption barrier mode and advance. For Shamir, configures
 * the specified shares required and total before advancing.
 */
async function selectBarrierAndAdvance(
  page: import("@playwright/test").Page,
  mode: "None" | "Passphrase" | "Shamir",
  shamirConfig?: { required: number; total: number },
): Promise<void> {
  await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();

  if (mode === "None") {
    await page.locator("button").filter({ hasText: "None" }).click();
  } else if (mode === "Shamir") {
    await page.locator("button").filter({ hasText: "Shamir" }).click();
    if (shamirConfig) {
      await page.locator("#db-shares-required").fill(String(shamirConfig.required));
      await page.locator("#db-shares-total").fill(String(shamirConfig.total));
    }
  } else if (mode === "Passphrase") {
    // The barrier defaults to none; the passphrase barrier asks for a
    // confirmed passphrase.
    await page.locator("button").filter({ hasText: "Passphrase" }).click();
    await page.locator("#db-barrier-passphrase").fill("e2e-barrier-passphrase");
    await page.locator("#db-barrier-passphrase-confirm").fill("e2e-barrier-passphrase");
  }

  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Navigate through all wizard steps up to the Summary, selecting the
 * given storage backend and encryption barrier mode.
 *
 * Flow: Name → Storage → SQL → Barrier → Summary
 * (Raft Configuration step is skipped for standalone servers.)
 */
async function navigateToSummaryWithConfig(
  page: import("@playwright/test").Page,
  name: string,
  backend: "Memory" | "File" | "Pebble",
  barrierMode: "None" | "Passphrase" | "Shamir",
  shamirConfig?: { required: number; total: number },
): Promise<void> {
  await openCreateWizard(page);
  await fillNameAndAdvance(page, name);
  await selectStorageAndAdvance(page, backend);
  await advanceThroughSQL(page);
  await selectBarrierAndAdvance(page, barrierMode, shamirConfig);
  await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
}

/**
 * Click Create Database and verify the database appears in the sidebar.
 */
async function createAndVerifyComplete(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.locator("main").getByRole("button", { name: "Create Database" }).click();
  const sidebar = page.locator("aside");
  await expect(sidebar.getByText(name)).toBeVisible({ timeout: INIT_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Storage + Encryption Combination Tests
// ---------------------------------------------------------------------------

test.describe("Wizard Encryption Combinations", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  // -------------------------------------------------------------------------
  // Memory combinations
  // -------------------------------------------------------------------------

  test("Memory + no encryption completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-mem-none", "Memory", "None");

    // Verify summary shows correct configuration
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-mem-none");
  });

  test("Memory + passphrase completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-mem-pass", "Memory", "Passphrase");

    // Verify summary shows correct configuration
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-mem-pass");
  });

  test("Memory + Shamir (2-of-3) completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(
      page, "enc-mem-shamir", "Memory", "Shamir", { required: 2, total: 3 },
    );

    // Verify summary shows correct configuration
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();
    await expect(page.getByText("Shamir", { exact: true })).toBeVisible();
    await expect(page.getByText("2-of-3", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-mem-shamir");
  });

  // -------------------------------------------------------------------------
  // File combinations
  // -------------------------------------------------------------------------

  test("File + no encryption completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-file-none", "File", "None");

    // Verify summary shows correct configuration
    await expect(page.getByText("File", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-file-none");
  });

  test("File + passphrase completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-file-pass", "File", "Passphrase");

    // Verify summary shows correct configuration
    await expect(page.getByText("File", { exact: true })).toBeVisible();
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-file-pass");
  });

  test("File + Shamir (2-of-3) completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(
      page, "enc-file-shamir", "File", "Shamir", { required: 2, total: 3 },
    );

    // Verify summary shows correct configuration
    await expect(page.getByText("File", { exact: true })).toBeVisible();
    await expect(page.getByText("Shamir", { exact: true })).toBeVisible();
    await expect(page.getByText("2-of-3", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-file-shamir");
  });
});

// ---------------------------------------------------------------------------
// Shamir M-of-N Configuration Variations
// ---------------------------------------------------------------------------

test.describe("Shamir Configuration Variations", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Shamir 3-of-5 displays configuration in summary", async ({ page }) => {
    await navigateToSummaryWithConfig(
      page, "shamir-3of5", "Pebble", "Shamir", { required: 3, total: 5 },
    );

    await expect(page.getByText("Shamir", { exact: true })).toBeVisible();
    await expect(page.getByText("3-of-5", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "shamir-3of5");
  });

  test("Shamir 1-of-1 displays configuration in summary", async ({ page }) => {
    await navigateToSummaryWithConfig(
      page, "shamir-1of1", "Pebble", "Shamir", { required: 1, total: 1 },
    );

    await expect(page.getByText("Shamir", { exact: true })).toBeVisible();
    await expect(page.getByText("1-of-1", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "shamir-1of1");
  });
});

// ---------------------------------------------------------------------------
// Shamir Validation Tests
// ---------------------------------------------------------------------------

test.describe("Shamir Validation", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Shamir threshold exceeding total shares shows error", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "shamir-invalid");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.
    await advanceThroughSQL(page);

    // Arrive at Encryption Barrier step
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "Shamir" }).click();

    // Set threshold > total (5 required, 3 total)
    await page.locator("#db-shares-required").fill("5");
    await page.locator("#db-shares-total").fill("3");

    // Verify error message appears
    await expect(
      page.getByText("Shares required cannot exceed total shares."),
    ).toBeVisible();

    // Verify Next button is disabled
    const nextButton = page.getByRole("button", { name: "Next" });
    await expect(nextButton).toBeDisabled();
  });

  test("Shamir zero values show error", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "shamir-zero");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.
    await advanceThroughSQL(page);

    // Arrive at Encryption Barrier step
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "Shamir" }).click();

    // Set both values to zero
    await page.locator("#db-shares-required").fill("0");
    await page.locator("#db-shares-total").fill("0");

    // Verify error message appears
    await expect(
      page.getByText("Both values must be greater than zero."),
    ).toBeVisible();

    // Verify Next button is disabled
    const nextButton = page.getByRole("button", { name: "Next" });
    await expect(nextButton).toBeDisabled();
  });

  test("Shamir correcting invalid values clears error and enables Next", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "shamir-corrected");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.
    await advanceThroughSQL(page);

    // Arrive at Encryption Barrier step
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "Shamir" }).click();

    // Set invalid values first (threshold > total)
    await page.locator("#db-shares-required").fill("5");
    await page.locator("#db-shares-total").fill("3");

    // Verify error is shown and Next is disabled
    await expect(
      page.getByText("Shares required cannot exceed total shares."),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();

    // Correct the values
    await page.locator("#db-shares-required").fill("2");
    await page.locator("#db-shares-total").fill("5");

    // Verify error is cleared and Next is enabled
    await expect(
      page.getByText("Shares required cannot exceed total shares."),
    ).not.toBeVisible();
    await expect(page.getByRole("button", { name: "Next" })).toBeEnabled();
  });
});
