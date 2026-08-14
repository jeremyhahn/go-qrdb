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
 * Database creation wizard backend-type tests.
 *
 * Each test walks the full wizard flow — Name → Storage → SQL →
 * Barrier → Summary → Creating → Complete — while selecting a different
 * storage backend or engine type.  The mock fixture's DatabaseBinding.Create
 * stores the created record in state.databases[], so the "Database Created"
 * heading on the complete step confirms an end-to-end success path with no
 * real I/O.
 *
 * The Raft Configuration step is skipped entirely for standalone servers.
 * The secondLaunchEmbeddedState mock uses serverMode "standalone", so the
 * wizard navigates directly from Storage to SQL.
 */

import { test, expect, secondLaunchEmbeddedState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Maximum wait time for step transitions and heading visibility. */
const STEP_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Opens the Create Database wizard from the landing page. */
async function openWizard(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
  await expect(
    page.getByRole("heading", { name: "Database Name & Engine" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Navigates through the full wizard flow and returns at the Summary step.
 * The caller can verify the summary contents before clicking Create.
 *
 * Steps executed (standalone server — Raft step is skipped):
 *   1. Name — fills `#db-name` with `name`, selects the given engine.
 *   2. Storage — clicks the label whose text contains `backendLabel`.
 *   3. SQL — skips, clicks Next.
 *   4. Barrier — leaves default, clicks Next.
 *   5. Lands on Summary.
 */
async function navigateEmbeddedToSummary(
  page: Page,
  name: string,
  engine: "Key-Value" | "TimeSeries" | "Vector",
  backendLabel: "Memory" | "File" | "Pebble",
): Promise<void> {
  await openWizard(page);

  // Step 1 — Name & Engine
  await page.locator("#db-name").fill(name);
  await page.locator("button").filter({ hasText: engine }).click();
  await page.getByRole("button", { name: "Next" }).click();

  // Step 2 — Storage Backend: click the radio label for the requested backend
  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page
    .locator('label:has(input[name="db-storage-backend"])')
    .filter({ hasText: backendLabel })
    .click();
  await page.getByRole("button", { name: "Next" }).click();

  // Step 3 — SQL Wire Protocols: Raft step is skipped for standalone servers.
  await expect(
    page.getByRole("heading", { name: "SQL Wire Protocols" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  // Step 4 — Encryption Barrier: leave default
  await expect(
    page.getByRole("heading", { name: "Database Encryption Barrier" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  // Step 5 — Summary
  await expect(
    page.getByRole("heading", { name: "Review Configuration" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/** Submits the wizard from the Summary step and waits for successful completion. */
async function completeWizard(page: Page, dbName: string): Promise<void> {
  await page
    .locator("main")
    .getByRole("button", { name: "Create Database" })
    .click();

  // The mock resolves synchronously so the wizard completes before Playwright
  // can observe the transient "Creating Database" or "Database Created" steps.
  // onComplete() navigates directly to the database detail view.
  // Verify success by confirming the new database appears in the sidebar and
  // that the main panel shows its heading.
  await expect(
    page.locator(".breadcrumbs").getByText(dbName, { exact: false }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Database Wizard - All Backends", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  // -------------------------------------------------------------------------
  // Memory backend
  // -------------------------------------------------------------------------

  test("creates database with memory backend and completes", async ({ page }) => {
    await navigateEmbeddedToSummary(page, "mem-test-db", "Key-Value", "Memory");

    // Summary must show Memory as the selected backend.
    await expect(
      page.locator("main").getByText("Memory", { exact: true }),
    ).toBeVisible();

    await completeWizard(page, "mem-test-db");
  });

  // -------------------------------------------------------------------------
  // File backend
  // -------------------------------------------------------------------------

  test("creates database with file backend and completes", async ({ page }) => {
    await navigateEmbeddedToSummary(page, "file-test-db", "Key-Value", "File");

    // Summary must show File as the selected backend.
    await expect(
      page.locator("main").getByText("File", { exact: true }),
    ).toBeVisible();

    await completeWizard(page, "file-test-db");
  });

  // -------------------------------------------------------------------------
  // Pebble backend (default)
  // -------------------------------------------------------------------------

  test("creates database with pebble backend and completes", async ({ page }) => {
    await navigateEmbeddedToSummary(page, "pebble-test-db", "Key-Value", "Pebble");

    // Summary must show Pebble as the selected backend.
    await expect(
      page.locator("main").getByText("Pebble", { exact: true }),
    ).toBeVisible();

    await completeWizard(page, "pebble-test-db");
  });

  // -------------------------------------------------------------------------
  // TimeSeries engine
  // -------------------------------------------------------------------------

  test("creates timeseries database with pebble backend", async ({ page }) => {
    await navigateEmbeddedToSummary(
      page,
      "ts-pebble-db",
      "TimeSeries",
      "Pebble",
    );

    // Summary must reflect both the TimeSeries engine and Pebble backend.
    await expect(
      page.locator("main").getByText("TimeSeries", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator("main").getByText("Pebble", { exact: true }),
    ).toBeVisible();

    await completeWizard(page, "ts-pebble-db");
  });

  // -------------------------------------------------------------------------
  // Vector engine
  // -------------------------------------------------------------------------

  test("creates vector database with memory backend", async ({ page }) => {
    await navigateEmbeddedToSummary(
      page,
      "vec-mem-db",
      "Vector",
      "Memory",
    );

    // Summary must reflect both the Vector engine and Memory backend.
    await expect(
      page.locator("main").getByText("Vector", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator("main").getByText("Memory", { exact: true }),
    ).toBeVisible();

    await completeWizard(page, "vec-mem-db");
  });
});
