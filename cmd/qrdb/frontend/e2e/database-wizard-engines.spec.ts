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
 * Regression tests for database creation through the wizard for all three
 * engine types (Key-Value, TimeSeries, Vector).
 *
 * Bug fixed: the backend field was left empty on creation instead of defaulting
 * to "pebble", which caused the created database to have no storage backend set.
 *
 * These tests confirm:
 *   - Each engine type can be created end-to-end through the wizard.
 *   - The created database appears in the sidebar after creation.
 *   - The backend defaults to "pebble" when not explicitly set.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const INIT_TIMEOUT = 10_000;

// Start with an empty database list so wizard creation is observable.
const emptyState = {
  ...secondLaunchEmbeddedState,
  databases: [] as typeof secondLaunchEmbeddedState.databases,
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Opens the Create Database wizard from the empty-state landing page. */
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

/** Fills the database name, selects an engine and advances to the Storage step. */
async function stepName(
  page: import("@playwright/test").Page,
  name: string,
  engine: "Key-Value" | "TimeSeries" | "Vector",
): Promise<void> {
  await page.locator("#db-name").fill(name);
  await page.locator("button").filter({ hasText: engine }).click();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
}

/**
 * Navigate through the wizard from the Storage step to the Summary step using
 * defaults.  Steps: Storage → SQL → Barrier → Summary.
 *
 * The Raft Configuration step is skipped for standalone servers
 * (secondLaunchEmbeddedState uses serverMode "standalone").
 */
async function stepsThroughToSummary(
  page: import("@playwright/test").Page,
): Promise<void> {
  // Storage Backend step — leave default (Pebble).
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

/**
 * Complete the full wizard flow for a given engine and database name, then
 * click "Create Database" on the summary step.
 */
async function createDatabaseViaWizard(
  page: import("@playwright/test").Page,
  name: string,
  engine: "Key-Value" | "TimeSeries" | "Vector",
): Promise<void> {
  await openWizard(page);
  await stepName(page, name, engine);
  await stepsThroughToSummary(page);
  await page
    .locator("main")
    .getByRole("button", { name: "Create Database" })
    .click();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Engine Creation Regression", () => {
  test.use({ mockState: emptyState });

  // -------------------------------------------------------------------------
  // Step 1 — Name & Engine selection
  // -------------------------------------------------------------------------

  test("wizard shows all three engine cards", async ({ page }) => {
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

  test("wizard step 2 shows storage backend options", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "probe-db", "Key-Value");

    // After advancing from Name, the Storage Backend step must be shown.
    await expect(
      page.locator('input[name="db-storage-backend"]'),
    ).toHaveCount(3);
  });

  // -------------------------------------------------------------------------
  // Key-Value engine
  // -------------------------------------------------------------------------

  test("create KV database reaches summary with correct name", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "test-kv", "Key-Value");
    await stepsThroughToSummary(page);

    // Summary must show the chosen database name.
    await expect(page.getByText("test-kv")).toBeVisible();
  });

  test("create KV database appears in sidebar after creation", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-kv", "Key-Value");

    // After creation the sidebar must list the new database.
    await expect(
      page.locator("aside").getByText("test-kv"),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("create KV database shows engine type in summary", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "test-kv", "Key-Value");
    await stepsThroughToSummary(page);

    // The summary step must reflect the Key-Value engine choice.
    // Accept either the display label "Key-Value" or the engine identifier "kv".
    const summaryText = await page.locator("main").textContent();
    expect(
      summaryText?.includes("Key-Value") || summaryText?.includes("kv"),
    ).toBe(true);
  });

  test("created KV database uses pebble backend by default", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-kv", "Key-Value");

    // Verify the mock DatabaseBinding.Create was called with backend "pebble"
    // by checking the stored state after creation.
    const backend = await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const state = w.__QRDB_MOCK_STATE__ as { databases: Array<{ name: string; backend: string }> };
      return state.databases.find((d) => d.name === "test-kv")?.backend ?? "";
    });
    expect(backend).toBe("pebble");
  });

  // -------------------------------------------------------------------------
  // TimeSeries engine
  // -------------------------------------------------------------------------

  test("create TimeSeries database reaches summary with correct name", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "test-ts", "TimeSeries");
    await stepsThroughToSummary(page);

    await expect(page.getByText("test-ts")).toBeVisible();
  });

  test("create TimeSeries database appears in sidebar after creation", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-ts", "TimeSeries");

    await expect(
      page.locator("aside").getByText("test-ts"),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("create TimeSeries database shows engine type in summary", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "test-ts", "TimeSeries");
    await stepsThroughToSummary(page);

    const summaryText = await page.locator("main").textContent();
    expect(
      summaryText?.includes("TimeSeries") || summaryText?.includes("timeseries"),
    ).toBe(true);
  });

  test("created TimeSeries database uses pebble backend by default", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-ts", "TimeSeries");

    const backend = await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const state = w.__QRDB_MOCK_STATE__ as { databases: Array<{ name: string; backend: string }> };
      return state.databases.find((d) => d.name === "test-ts")?.backend ?? "";
    });
    expect(backend).toBe("pebble");
  });

  // -------------------------------------------------------------------------
  // Vector engine
  // -------------------------------------------------------------------------

  test("create Vector database reaches summary with correct name", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "test-vec", "Vector");
    await stepsThroughToSummary(page);

    await expect(page.getByText("test-vec")).toBeVisible();
  });

  test("create Vector database appears in sidebar after creation", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-vec", "Vector");

    await expect(
      page.locator("aside").getByText("test-vec"),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("create Vector database shows engine type in summary", async ({ page }) => {
    await openWizard(page);
    await stepName(page, "test-vec", "Vector");
    await stepsThroughToSummary(page);

    const summaryText = await page.locator("main").textContent();
    expect(
      summaryText?.includes("Vector") || summaryText?.includes("vector"),
    ).toBe(true);
  });

  test("created Vector database uses pebble backend by default", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-vec", "Vector");

    const backend = await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const state = w.__QRDB_MOCK_STATE__ as { databases: Array<{ name: string; backend: string }> };
      return state.databases.find((d) => d.name === "test-vec")?.backend ?? "";
    });
    expect(backend).toBe("pebble");
  });

  // -------------------------------------------------------------------------
  // All three engines in sequence
  // -------------------------------------------------------------------------

  test("create all three engine types and all appear in sidebar", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-kv", "Key-Value");
    await expect(
      page.locator("aside").getByText("test-kv"),
    ).toBeVisible({ timeout: INIT_TIMEOUT });

    await createDatabaseViaWizard(page, "test-ts", "TimeSeries");
    await expect(
      page.locator("aside").getByText("test-ts"),
    ).toBeVisible({ timeout: INIT_TIMEOUT });

    await createDatabaseViaWizard(page, "test-vec", "Vector");
    await expect(
      page.locator("aside").getByText("test-vec"),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });

  test("all three created databases use pebble backend", async ({ page }) => {
    await createDatabaseViaWizard(page, "test-kv", "Key-Value");
    await createDatabaseViaWizard(page, "test-ts", "TimeSeries");
    await createDatabaseViaWizard(page, "test-vec", "Vector");

    const backends = await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const state = w.__QRDB_MOCK_STATE__ as { databases: Array<{ name: string; backend: string }> };
      return state.databases.map((d) => ({ name: d.name, backend: d.backend }));
    });

    for (const db of backends) {
      expect(db.backend).toBe("pebble");
    }
  });
});
