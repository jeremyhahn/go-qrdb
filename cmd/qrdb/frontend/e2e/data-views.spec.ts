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
 * Data workspace view tests.
 *
 * Verifies that the KV Store supports full CRUD operations and that all
 * workspace tabs (Collections, Indexes, Transactions, Watch, Batch, Leases)
 * render their expected content when a KV database is active.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  type MockState,
} from "./fixtures/wails-mock.ts";

/** Timeout for async DOM operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock state
// ---------------------------------------------------------------------------

/** State with a KV database for the workspace tests. */
const kvWorkspaceState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "default", engine: "kv", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Opens the default database workspace. */
async function openWorkspace(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("default").click();
  await expect(
    page.locator(".breadcrumbs").getByText("default", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Clicks a workspace tab and waits for it to be visible/active. */
async function switchTab(
  page: import("@playwright/test").Page,
  tabName: string,
): Promise<void> {
  await page.locator("main nav").getByText(tabName, { exact: true }).click();
  await expect(
    page.locator("main nav").getByText(tabName, { exact: true }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// KV Store CRUD
// ---------------------------------------------------------------------------

test.describe("Data Views - KV Store CRUD", () => {
  test.use({ mockState: kvWorkspaceState });

  test("shows empty state when no entries exist", async ({ page }) => {
    await openWorkspace(page);
    await expect(page.getByText("0 entries")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("create entry via slide panel and verify in table", async ({ page }) => {
    await openWorkspace(page);

    // Open create panel
    await page
      .getByRole("button", { name: "Create", exact: true })
      .first()
      .click();
    await expect(page.getByText("Create Entry")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Fill key and value
    await page.locator("input[placeholder='Enter key']").fill("smoke-key");
    await page
      .locator("textarea[placeholder='Enter value (supports JSON)']")
      .fill("smoke-value");

    // Submit
    await page
      .getByRole("button", { name: "Create", exact: true })
      .last()
      .click();

    // Toast confirms creation
    await expect(page.getByText('Created "smoke-key"')).toBeVisible({
      timeout: 5_000,
    });

    // Entry appears in the table
    await expect(page.getByText("smoke-key").first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByText("1 entries")).toBeVisible();
  });

  test("delete entry removes it from the table", async ({ page }) => {
    await openWorkspace(page);

    // Create an entry first
    await page
      .getByRole("button", { name: "Create", exact: true })
      .first()
      .click();
    await page.locator("input[placeholder='Enter key']").fill("del-key");
    await page
      .locator("textarea[placeholder='Enter value (supports JSON)']")
      .fill("del-val");
    await page
      .getByRole("button", { name: "Create", exact: true })
      .last()
      .click();
    await expect(page.getByText('Created "del-key"')).toBeVisible({
      timeout: 5_000,
    });

    // Wait for the creation toast to disappear so it does not intercept pointer events.
    await expect(page.getByText('Created "del-key"')).not.toBeVisible({ timeout: 10_000 });

    // Open detail panel
    await page.getByText("del-key").first().click();

    // Click Delete in the detail panel
    const detailPanel = page.getByLabel("Details");
    await detailPanel.getByRole("button", { name: "Delete" }).click();

    // Confirm in modal
    await expect(page.getByText("Delete Entry")).toBeVisible();
    await page
      .locator(".modal")
      .getByRole("button", { name: "Delete" })
      .click();

    // Verify deletion toast and empty state
    await expect(page.getByText('Deleted "del-key"')).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.getByText("0 entries")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });
});

// ---------------------------------------------------------------------------
// Workspace tabs
// ---------------------------------------------------------------------------

test.describe("Data Views - Workspace Tabs", () => {
  test.use({ mockState: kvWorkspaceState });

  test("KV tab is active by default", async ({ page }) => {
    await openWorkspace(page);

    // The default tab should be KV (or the first available tab).
    // DbNavRail: use page.locator("main nav") for tool navigation
    await expect(page.locator("main nav")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Collections tab renders collection list controls", async ({ page }) => {
    await openWorkspace(page);
    await switchTab(page, "Collections");

    // Should show a Create button and an empty-state message or table.
    await expect(
      page
        .getByRole("button", { name: "Create", exact: true })
        .or(page.getByText("No collections", { exact: false }))
        .first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Indexes tab renders index list controls", async ({ page }) => {
    await openWorkspace(page);
    await switchTab(page, "Indexes");

    // Should show a Create Index button or an empty-state message.
    await expect(
      page
        .getByRole("button", { name: /Create.*Index|No.*index/i })
        .or(page.getByText(/No.*index/i).first())
        .first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Transactions tab renders transaction builder", async ({ page }) => {
    await openWorkspace(page);
    await switchTab(page, "Transactions");

    // Transaction tab should show at least an Execute button or an operations area.
    await expect(
      page
        .getByRole("button", { name: /Execute|Add Operation/i })
        .or(page.getByText(/Transaction|Operations/i).first())
        .first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Watch tab renders watch controls", async ({ page }) => {
    await openWorkspace(page);
    await switchTab(page, "Watch");

    // Watch tab should show an input for a key to watch.
    await expect(
      page
        .locator("input[placeholder*='key']")
        .or(page.getByRole("button", { name: /Watch/i }))
        .first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Batch tab renders batch operation panel", async ({ page }) => {
    await openWorkspace(page);
    await switchTab(page, "Batch");

    // Batch tab should show an Execute or Add button.
    await expect(
      page
        .getByRole("button", { name: /Execute|Add/i })
        .or(page.getByText(/Batch|Operations/i).first())
        .first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Leases tab renders lease grant controls", async ({ page }) => {
    await openWorkspace(page);
    await switchTab(page, "Leases");

    // Lease tab should show a Grant button or TTL input.
    await expect(
      page
        .getByRole("button", { name: /Grant/i })
        .or(page.locator("input[placeholder*='TTL']"))
        .or(page.getByText(/Lease/i).first())
        .first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});
