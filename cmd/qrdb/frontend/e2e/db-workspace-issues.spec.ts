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
 * Tests for the UI issue fixes:
 *   1. Horizontal tab bar replaces DbNavRail (no vertical nav rail in workspace).
 *   2. Database Settings tab renders without errors.
 *   3. IndexManager query/scan dropdowns are populated from the index list.
 *   4. Connection status is consistent between header and Settings view.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

/** Timeout for async DOM operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Shared mock states
// ---------------------------------------------------------------------------

const kvDbState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-kv", engine: "kv", backend: "pebble", status: "open" },
  ],
};

const tsDbState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-ts", engine: "timeseries", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Issue 1: Horizontal tab bar — no vertical nav rail
// ---------------------------------------------------------------------------

test.describe("Issue 1: Horizontal tab bar — KV workspace", () => {
  test.use({ mockState: kvDbState });

  test("KV database workspace shows horizontal tab bar with all tools", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Horizontal tab bar must be present inside main
    const tabList = page.locator("main [role='tablist']");
    await expect(tabList).toBeVisible({ timeout: LOAD_TIMEOUT });

    // All KV tabs must be visible in the tablist
    await expect(tabList.getByText("Data", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Collections", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Indexes", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Transactions", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Batch", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Watch", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Leases", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Settings", { exact: true })).toBeVisible();
  });

  test("no vertical nav rail (w-40 sidebar) exists in the workspace", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Verify no element with the old DbNavRail width class exists in main
    const navRailCheck = page.locator("main .w-40");
    await expect(navRailCheck).not.toBeVisible();
  });

  test("tab switching via horizontal bar works", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click Transactions
    await page.locator("main [role='tablist']").getByText("Transactions", { exact: true }).click();
    await expect(page.getByText("Conditions").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click Watch
    await page.locator("main [role='tablist']").getByText("Watch", { exact: true }).click();
    await expect(page.getByRole("button", { name: "Start Watch" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

test.describe("Issue 1: Horizontal tab bar — TimeSeries workspace", () => {
  test.use({ mockState: tsDbState });

  test("timeseries workspace shows only Data, Batch, Settings tabs", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-ts").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-ts", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const tabList = page.locator("main [role='tablist']");
    await expect(tabList).toBeVisible({ timeout: LOAD_TIMEOUT });

    // TimeSeries tabs must be present
    await expect(tabList.getByText("Data", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Batch", { exact: true })).toBeVisible();
    await expect(tabList.getByText("Settings", { exact: true })).toBeVisible();

    // KV-only tabs must NOT be present
    await expect(tabList.getByText("Collections", { exact: true })).not.toBeVisible();
    await expect(tabList.getByText("Transactions", { exact: true })).not.toBeVisible();
    await expect(tabList.getByText("Indexes", { exact: true })).not.toBeVisible();
    await expect(tabList.getByText("Watch", { exact: true })).not.toBeVisible();
    await expect(tabList.getByText("Leases", { exact: true })).not.toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Issue 2: Database Settings tab renders without errors
// ---------------------------------------------------------------------------

test.describe("Issue 2: Database Settings tab — KV database", () => {
  test.use({ mockState: kvDbState });

  test("Settings tab renders database info card", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click Settings tab
    await page.locator("main [role='tablist']").getByText("Settings", { exact: true }).click();

    // Database Info card must be visible
    const infoCard = page.locator(".card", { hasText: "Database Info" });
    await expect(infoCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Database name inside the card (font-mono paragraph)
    await expect(infoCard.locator("p.font-mono", { hasText: "test-kv" })).toBeVisible();

    // Engine and backend
    await expect(infoCard.locator(".badge", { hasText: "kv" })).toBeVisible();
    await expect(infoCard.getByText("pebble")).toBeVisible();
  });

  test("Settings tab shows SQL Wire Protocols toggles", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("main [role='tablist']").getByText("Settings", { exact: true }).click();

    // Find the SQL Wire Protocols card and check toggles
    const sqlCard = page.locator(".card", { hasText: "SQL Wire Protocols" }).first();
    await expect(sqlCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // MySQL and PostgreSQL labels (exact match on the font-medium span)
    await expect(sqlCard.locator("span.font-medium", { hasText: "MySQL" })).toBeVisible();
    await expect(sqlCard.locator("span.font-medium", { hasText: "PostgreSQL" })).toBeVisible();

    // Both toggle inputs are present
    const toggles = sqlCard.locator("input[type='checkbox']");
    expect(await toggles.count()).toBe(2);
  });

  test("SQL Browser tab appears immediately after enabling a protocol (no reload)", async ({
    page,
  }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const tabList = page.locator("main [role='tablist']");
    // Regression guard: with no SQL protocol enabled the tab must be absent...
    await expect(tabList.getByText("SQL Browser")).toHaveCount(0);

    // ...enable MySQL in Settings...
    await tabList.getByText("Settings", { exact: true }).click();
    const sqlCard = page.locator(".card", { hasText: "SQL Wire Protocols" }).first();
    await expect(sqlCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await sqlCard
      .locator("label")
      .filter({ hasText: "MySQL" })
      .locator("input[type='checkbox']")
      .first()
      .check();
    await expect(page.getByText(/MySQL enabled/i).first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    // ...and the SQL Browser tab must appear WITHOUT any reload or
    // re-navigation. Before the databaseStore.updateDatabase propagation fix,
    // the workspace derived the tab from a stale database snapshot and the
    // tab only appeared after a full database-list reload.
    await expect(tabList.getByText("SQL Browser")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

test.describe("Issue 2: Database Settings tab — TimeSeries database", () => {
  test.use({ mockState: tsDbState });

  test("Settings tab in timeseries workspace renders without errors", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-ts").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-ts", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("main [role='tablist']").getByText("Settings", { exact: true }).click();

    // Database Info card must be visible
    const infoCard = page.locator(".card", { hasText: "Database Info" });
    await expect(infoCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(infoCard.locator("p.font-mono", { hasText: "test-ts" })).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Issue 3: IndexManager dropdowns
// ---------------------------------------------------------------------------

test.describe("Issue 3: IndexManager query/scan dropdowns", () => {
  test.use({ mockState: kvDbState });

  test("IndexManager Indexes tab shows index-name select elements in query and scan panels", async ({
    page,
  }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("main [role='tablist']").getByText("Indexes", { exact: true }).click();

    // Wait for IndexManager to render the table heading
    await expect(
      page.getByRole("heading", { name: "Secondary Indexes" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Both query and scan panels must use <select> with the "Select an index" placeholder.
    // There are at least 2 selects with the disabled placeholder option.
    const disabledOptions = page.locator("select option[disabled]", { hasText: "Select an index" });
    await expect(disabledOptions.first()).toBeAttached({ timeout: LOAD_TIMEOUT });
    const count = await disabledOptions.count();
    expect(count).toBeGreaterThanOrEqual(2);
  });

  test("index name selects show 'Select an index' as first option", async ({
    page,
  }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("main [role='tablist']").getByText("Indexes", { exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Secondary Indexes" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Query panel select must have the disabled placeholder
    const queryCard = page.locator(".card", { hasText: "Query" }).first();
    const querySelect = queryCard.locator("select");
    await expect(querySelect).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(querySelect.locator("option[disabled]")).toHaveText("Select an index");

    // Scan panel select must have the disabled placeholder
    const scanCard = page.locator(".card", { hasText: "Scan Range" });
    const scanSelect = scanCard.locator("select");
    await expect(scanSelect).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(scanSelect.locator("option[disabled]")).toHaveText("Select an index");
  });

  test("creating an index adds it as an option in the query and scan dropdowns", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("test-kv").click();
    await expect(
      page.locator(".breadcrumbs").getByText("test-kv", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("main [role='tablist']").getByText("Indexes", { exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Secondary Indexes" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Create an index via the Create Index button (opens the FormModal)
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='e.g. idx-email']").fill("idx-dropdown-test");
    await page.locator("input[placeholder='e.g. email']").fill("email");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(
      page.getByText(/Index "idx-dropdown-test" created/),
    ).toBeVisible({ timeout: 5_000 });

    // After creation the index list reloads. The query panel select should include
    // the new index as an option (matched by text content, not value attribute).
    const queryCard = page.locator(".card", { hasText: "Query" }).first();
    const querySelect = queryCard.locator("select");
    await expect(
      querySelect.locator("option", { hasText: "idx-dropdown-test" }),
    ).toBeAttached({ timeout: 5_000 });

    // Same for scan panel
    const scanCard = page.locator(".card", { hasText: "Scan Range" });
    const scanSelect = scanCard.locator("select");
    await expect(
      scanSelect.locator("option", { hasText: "idx-dropdown-test" }),
    ).toBeAttached({ timeout: 5_000 });
  });
});

// ---------------------------------------------------------------------------
// Issue 5: Connection status consistency
// ---------------------------------------------------------------------------

test.describe("Issue 5: Connection status consistency", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("header badge and Settings view show the same connection status", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Read connection status from the header badge
    const header = page.locator("header");
    const headerBadge = header.locator(".badge", { hasText: /Connected|Disconnected/ });
    await expect(headerBadge).toBeVisible({ timeout: LOAD_TIMEOUT });
    const headerStatus = await headerBadge.textContent();

    // Navigate to Settings
    await page.locator("aside").getByText("Settings").click();
    await expect(
      page.locator(".breadcrumbs").getByText("Settings"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Read connection status from the Settings card
    const connectionCard = page.locator(".card", { hasText: "Connection" });
    await expect(connectionCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    const settingsBadge = connectionCard.locator(".badge-lg");
    await expect(settingsBadge).toBeVisible({ timeout: LOAD_TIMEOUT });
    const settingsStatus = await settingsBadge.textContent();

    // Both must agree
    expect(headerStatus?.trim()).toBe(settingsStatus?.trim());
  });

  test("header shows Connected badge when active profile is connected", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(
      page.locator("header").locator(".badge", { hasText: "Connected" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});
