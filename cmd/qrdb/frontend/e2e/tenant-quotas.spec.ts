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
 * Tenant quota management tests.
 *
 * These tests exercise the TenantManager list view and TenantDetail quota
 * editor via the mock Wails bindings, covering:
 *   1. The tenant list renders a "Default" badge for tenants without quotas.
 *   2. Clicking a tenant row opens the quota editor with the correct fields.
 *   3. Saving quotas calls UpdateQuotas and shows a success toast.
 *   4. After saving, returning to the list shows a "Custom" badge.
 *
 * The secondLaunchEmbeddedState fixture supplies one pre-seeded tenant:
 *   { id: "default", name: "Default Tenant", quotas: null }
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations and view transitions. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the Tenants admin section. */
async function navigateToTenants(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Click "Tenants" in the admin sidebar.
  await page.locator("aside").getByText("Tenants").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Tenants"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/**
 * Navigates to the Tenants admin section, then clicks the first tenant row to
 * open the quota editor.  The caller lands on the "Tenant Quotas" detail view.
 */
async function openQuotaEditor(page: Page): Promise<void> {
  await navigateToTenants(page);

  // Click the row for the default tenant.  The CrudTable renders rows as
  // clickable <tr> elements; the tenant ID "default" is in the first cell.
  await page
    .locator("tr")
    .filter({ hasText: "default" })
    .first()
    .click();

  // The detail view heading should appear.
  await expect(
    page.locator("h2").filter({ hasText: "Tenant Quotas" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Tenant Quotas", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  // -------------------------------------------------------------------------
  // Tenant list
  // -------------------------------------------------------------------------

  test("tenant list shows Default badge when no quotas", async ({ page }) => {
    await navigateToTenants(page);

    const main = page.locator("main");

    // The default tenant row must be visible.
    await expect(
      main.locator("tr").filter({ hasText: "default" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The Quotas column renders a "Default" badge for tenants with null quotas.
    await expect(
      main.locator(".badge").filter({ hasText: "Default" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // No "Custom" badge should be present yet.
    await expect(
      main.locator(".badge").filter({ hasText: "Custom" }),
    ).not.toBeVisible();
  });

  test("tenant detail copy reflects shipped multi-node raft (regression: no stale 'in development')", async ({ page }) => {
    // Regression guard for a stale-copy bug found via chrometest: the tenant
    // UI described multi-node Raft replication as "in development" / running
    // "single-replica today", which is no longer true — it ships (replicas are
    // placed across cluster nodes, RF configurable).
    await openQuotaEditor(page);
    const main = page.locator("main");
    await expect(main).not.toContainText("in development");
    await expect(main).not.toContainText("single-replica today");
    // The entitlements section affirmatively states multi-node Raft ships.
    await expect(
      main.getByText(/Multi-node Raft replication ships/i),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("tenant list renders tenant id and name columns", async ({ page }) => {
    await navigateToTenants(page);

    const main = page.locator("main");

    // Both the tenant ID and name must be visible in the table.
    await expect(
      main.locator("td").filter({ hasText: "default" }).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.locator("td").filter({ hasText: "Default Tenant" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  // -------------------------------------------------------------------------
  // Quota editor
  // -------------------------------------------------------------------------

  test("clicking tenant row opens quota editor", async ({ page }) => {
    await openQuotaEditor(page);

    const main = page.locator("main");

    // The detail heading must reference the correct tenant ID.
    await expect(
      main.locator("h2").filter({ hasText: "Tenant Quotas — default" }),
    ).toBeVisible();

    // Core quota fields must be present.
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Databases" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Users" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Roles" }),
    ).toBeVisible();

    // The Save Quotas action button must be present.
    await expect(
      main.getByRole("button", { name: "Save Quotas" }),
    ).toBeVisible();
  });

  test("quota editor shows all quota field groups", async ({ page }) => {
    await openQuotaEditor(page);

    const main = page.locator("main");

    // Storage group.
    await expect(main.getByRole("heading", { name: "Storage" })).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Databases" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Raft Groups" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Collections" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Keys" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Storage (bytes)" }),
    ).toBeVisible();

    // Access group.
    await expect(main.getByRole("heading", { name: "Access" })).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Certificates" }),
    ).toBeVisible();

    // Data group.
    await expect(main.getByRole("heading", { name: "Data" })).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Backups" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Vector Dims" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max TS Metrics" }),
    ).toBeVisible();

    // Raft group quota fields.
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Replicas / Group" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Total Replicas" }),
    ).toBeVisible();
    await expect(
      main.locator(".label-text").filter({ hasText: "Max Dedicated WAL Groups" }),
    ).toBeVisible();
  });

  test("saving quotas updates tenant and shows success toast", async ({
    page,
  }) => {
    await openQuotaEditor(page);

    const main = page.locator("main");

    // Set Max Databases to 10 — locate the input by its accessible label.
    const maxDbInput = main.getByLabel("Max Databases");
    await expect(maxDbInput).toBeVisible({ timeout: LOAD_TIMEOUT });
    await maxDbInput.fill("10");

    // Click Save Quotas.
    await main.getByRole("button", { name: "Save Quotas" }).click();

    // A success toast must appear confirming the update.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText('Quotas updated for tenant "default"');
  });

  test("saving quotas persists max_dedicated_wal_groups to mock state", async ({
    page,
  }) => {
    await openQuotaEditor(page);

    const main = page.locator("main");

    // Fill the Max Dedicated WAL Groups field with a recognisable value.
    const walInput = main.getByTestId("quota-max-dedicated-wal-groups");
    await expect(walInput).toBeVisible({ timeout: LOAD_TIMEOUT });
    await walInput.fill("7");

    await main.getByRole("button", { name: "Save Quotas" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText('Quotas updated for tenant "default"');

    // Verify the mock state captured the snake_case key sent by the SDK
    // (deepCamelToSnake transforms maxDedicatedWalGroups → max_dedicated_wal_groups).
    const savedQuotas = await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const state = w.__QRDB_MOCK_STATE__ as {
        tenants: Array<{
          id: string;
          quotas: Record<string, unknown> | null;
        }>;
      };
      const tenant = state.tenants.find((t) => t.id === "default");
      return tenant?.quotas ?? null;
    });
    expect(savedQuotas).not.toBeNull();
    expect((savedQuotas as Record<string, unknown>).max_dedicated_wal_groups).toBe(7);
  });

  test("tenant shows Custom badge after quotas are saved", async ({ page }) => {
    await openQuotaEditor(page);

    const main = page.locator("main");

    // Fill in at least one quota value.
    const maxDbInput = main.getByLabel("Max Databases");
    await maxDbInput.fill("5");

    // Save.
    await main.getByRole("button", { name: "Save Quotas" }).click();

    // Wait for the success toast, then navigate back to the tenant list.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click the Back button to return to the list.
    await main.getByRole("button", { name: "Back" }).click();

    // The tenant list should now show a "Custom" badge in place of "Default".
    await expect(
      main.locator(".badge").filter({ hasText: "Custom" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});
