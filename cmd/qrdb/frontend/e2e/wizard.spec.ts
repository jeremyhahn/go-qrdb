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
 * Database creation wizard core tests.
 *
 * The database wizard step sequence for standalone servers:
 *   Name → Storage → SQL → Barrier → Summary → Creating → sidebar
 *
 * The Raft Configuration step is skipped entirely for standalone servers.
 * The "Mode" step (Embedded / Remote) was also removed. The database always
 * inherits its connection context from the active server connection, which
 * is managed by the Connection Manager.
 */

import {
  test,
  expect,
  injectError,
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
 * Navigate through wizard from the Name step to the summary step using
 * defaults (Pebble storage, no barrier).
 *
 * Step flow: Name → Storage → SQL → Barrier → Summary
 * (Raft Configuration step is skipped for standalone servers.)
 */
async function navigateToSummary(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await openCreateWizard(page);
  await fillNameAndAdvance(page, name);

  // Storage Backend step -- Pebble is default
  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();

  // SQL Wire Protocols step -- Raft step is skipped for standalone servers
  await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();

  // Encryption Barrier step -- the default (none)
  await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();

  // Should be on Summary
  await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
}

// ---------------------------------------------------------------------------
// Database Creation Wizard Tests
// ---------------------------------------------------------------------------

test.describe("Database Creation Wizard", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Pebble + no encryption completes successfully", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "test-pebble-none");

    // Storage Backend step -- Pebble (default)
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- Raft step skipped for standalone
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Encryption Barrier -- select None
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "None" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // Summary -- verify Pebble + Disabled
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("Pebble", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    // Should complete and return to workspace with the database in sidebar
    const sidebar = page.locator("aside");
    await expect(sidebar.getByText("test-pebble-none")).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("Pebble + passphrase completes successfully", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "test-pebble-pass");

    // Storage Backend -- Pebble (default)
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- Raft step skipped for standalone
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Encryption Barrier -- choose Passphrase and confirm it
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "Passphrase" }).click();
    await page.locator("#db-barrier-passphrase").fill("e2e-barrier-passphrase");
    await page.locator("#db-barrier-passphrase-confirm").fill("e2e-barrier-passphrase");
    await page.getByRole("button", { name: "Next" }).click();

    // Summary -- verify Passphrase badge
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    // Complete
    const sidebar = page.locator("aside");
    await expect(sidebar.getByText("test-pebble-pass")).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("Memory + no encryption completes successfully", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "test-mem-none");

    // Storage Backend -- select Memory
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.locator("label").filter({ hasText: "Memory" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- Raft step skipped for standalone
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Encryption Barrier -- select None
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "None" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // Summary -- verify Memory and Disabled
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    // Complete
    const sidebar = page.locator("aside");
    await expect(sidebar.getByText("test-mem-none")).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("File + no encryption completes successfully", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "test-file-none");

    // Storage Backend -- select File
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.locator("label").filter({ hasText: "File" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- Raft step skipped for standalone
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Encryption Barrier -- select None
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "None" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // Summary -- verify File
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("File", { exact: true })).toBeVisible();
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    // Complete
    const sidebar = page.locator("aside");
    await expect(sidebar.getByText("test-file-none")).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  // -------------------------------------------------------------------------
  // Navigation
  // -------------------------------------------------------------------------

  test("Back navigation preserves selections across steps", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "test-back-nav");

    // Storage Backend -- select Memory
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.locator("label").filter({ hasText: "Memory" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL step (Raft skipped for standalone) -- click Back to return to Storage
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Back", exact: true }).click();

    // Verify we are back on Storage Backend and Memory is still selected
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    const memoryRadio = page
      .locator("label")
      .filter({ hasText: "Memory" })
      .locator("input[type='radio']");
    await expect(memoryRadio).toBeChecked();

    // Advance again: Next to SQL
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- skip with defaults
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Select None encryption and advance to Summary
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "None" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // Verify Summary shows Memory + Disabled
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    // Navigate back from Summary to Encryption
    await page.getByRole("button", { name: "Back", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  test("Error during initialization displays error alert", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "test-error");

    // Storage step
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- Raft step skipped for standalone
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Barrier step -- None
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.locator("button").filter({ hasText: "None" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // On Summary page: inject error BEFORE clicking Create Database
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await injectError(page, "DatabaseBinding.Create", "Create failed");
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    // Verify the error alert is displayed on the Creating step
    await expect(page.getByRole("heading", { name: "Creating Database" })).toBeVisible();
    const alert = page.locator("[role='alert']").filter({ hasText: "Create failed" });
    await expect(alert).toBeVisible({ timeout: INIT_TIMEOUT });
  });
});
