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
 * Database creation wizard step flow tests.
 *
 * The database wizard step sequence for standalone servers:
 *   Name → Storage → SQL → Barrier → Summary → Creating → Complete
 *
 * The Raft Configuration step is skipped entirely for standalone servers.
 * The Mode step was also removed — the database inherits its mode from the
 * active connection profile managed by the Connection Manager.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
} from "./fixtures/wails-mock.ts";

/** Timeout for heading / step transitions. */
const INIT_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Opens the Create Database wizard from the main landing page. */
async function openWizard(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
  await expect(
    page.getByRole("heading", { name: "Database Name & Engine" }),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
}

/** Fills the database name, selects an engine and advances to the next step. */
async function stepName(
  page: import("@playwright/test").Page,
  name: string,
  engine: "Key-Value" | "TimeSeries" | "Vector" = "Key-Value",
): Promise<void> {
  await page.locator("#db-name").fill(name);
  await page.locator("button").filter({ hasText: engine }).click();
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Navigates through the wizard to the Summary step with default settings.
 * The Raft Configuration step is skipped for standalone servers.
 */
async function navigateToSummary(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await openWizard(page);
  await stepName(page, name);

  // Storage Backend step — use default (Pebble).
  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  // SQL Wire Protocols step — Raft step is skipped for standalone servers.
  await expect(
    page.getByRole("heading", { name: "SQL Wire Protocols" }),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  // Encryption Barrier step.
  await expect(
    page.getByRole("heading", { name: "Database Encryption Barrier" }),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  // Summary.
  await expect(
    page.getByRole("heading", { name: "Review Configuration" }),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Step Flow", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  // -------------------------------------------------------------------------
  // Step 1 — Name & Engine
  // -------------------------------------------------------------------------

  test("wizard opens from Create Database button", async ({ page }) => {
    await openWizard(page);

    await expect(
      page.getByRole("heading", { name: "Database Name & Engine" }),
    ).toBeVisible();
    await expect(page.locator("#db-name")).toBeVisible();
  });

  test("step 1 shows engine selection cards", async ({ page }) => {
    await openWizard(page);

    await expect(
      page.locator("button").filter({ hasText: "Key-Value" }),
    ).toBeVisible();
    await expect(
      page.locator("button").filter({ hasText: "TimeSeries" }),
    ).toBeVisible();
    await expect(
      page.locator("button").filter({ hasText: "Vector" }),
    ).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Step 2 — Storage Backend (no Mode step)
  // -------------------------------------------------------------------------

  test("step 2 shows Storage Backend heading", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "storage-test");

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("step 2 shows three storage backend options", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "storage-opts-test");

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });

    // All three backends should be present.
    const radios = page.locator("input[name='db-storage-backend']");
    await expect(radios).toHaveCount(3);
  });

  // -------------------------------------------------------------------------
  // Step 3 — SQL Wire Protocols (Raft step is skipped for standalone servers)
  // -------------------------------------------------------------------------

  test("step 3 shows SQL Wire Protocols heading (Raft skipped on standalone)", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "sql-step-test");

    // Storage step — default pebble. Raft step is not shown for standalone.
    await page.getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "SQL Wire Protocols" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  // -------------------------------------------------------------------------
  // Step 4 — Encryption Barrier
  // -------------------------------------------------------------------------

  test("step 4 shows Database Encryption Barrier heading", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "barrier-step-test");

    // Storage → SQL (Raft skipped for standalone).
    await page.getByRole("button", { name: "Next" }).click();
    await expect(
      page.getByRole("heading", { name: "SQL Wire Protocols" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
    await page.getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Database Encryption Barrier" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  // -------------------------------------------------------------------------
  // Step 6 — Summary
  // -------------------------------------------------------------------------

  test("summary shows Review Configuration heading and Create Database button", async ({
    page,
  }) => {
    await navigateToSummary(page, "summary-test");

    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible();
    await expect(
      page.locator("main").getByRole("button", { name: "Create Database" }),
    ).toBeVisible();
  });

  test("no DesktopOnlyError surfaced during creation flow", async ({
    page,
  }) => {
    await navigateToSummary(page, "no-error-test");

    await page
      .locator("main")
      .getByRole("button", { name: "Create Database" })
      .click();

    await expect(
      page.getByText("DesktopOnlyError", { exact: false }),
    ).not.toBeVisible({ timeout: INIT_TIMEOUT });
  });

  // -------------------------------------------------------------------------
  // Back navigation
  // -------------------------------------------------------------------------

  test("Back from Storage step returns to Name step", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "back-nav-test");

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });

    await page.getByRole("button", { name: "Back", exact: true }).click();

    await expect(
      page.getByRole("heading", { name: "Database Name & Engine" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("Back from SQL step returns to Storage step (Raft skipped on standalone)", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "back-sql-test");

    // Advance past Storage — Raft is skipped for standalone, so SQL comes next.
    await page.getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "SQL Wire Protocols" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });

    await page.getByRole("button", { name: "Back", exact: true }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  test("error during creation displays error alert", async ({ page }) => {
    await navigateToSummary(page, "error-create-test");

    await injectError(page, "DatabaseBinding.Create", "disk full");

    await page
      .locator("main")
      .getByRole("button", { name: "Create Database" })
      .click();

    await expect(page.getByText("disk full")).toBeVisible({
      timeout: INIT_TIMEOUT,
    });
  });
});
