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
 * Lease operations: grant, list, keep-alive, and revoke — testing the full
 * lifecycle end-to-end including error paths for each operation.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

const leaseOpsState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "lease-ops-db", engine: "kv", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigate to the Leases tab within the lease-ops-db database. */
async function navigateToLeases(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("lease-ops-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("lease-ops-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page
    .locator("main nav").getByText("Leases", { exact: true })
    .click();
}

/** Grant a lease with the given TTL via the Grant Lease modal. */
async function grantLease(page: Page, ttl: string): Promise<void> {
  await page.getByRole("button", { name: "Grant Lease" }).click();
  await expect(page.locator(".modal").getByText("Grant Lease")).toBeVisible({
    timeout: 5_000,
  });
  const ttlInput = page.locator(".modal input[type='number']");
  await ttlInput.fill(ttl);
  await page.locator(".modal").getByRole("button", { name: "Grant" }).click();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Lease Operations", () => {
  test.use({ mockState: leaseOpsState });

  // -------------------------------------------------------------------------
  // Empty state
  // -------------------------------------------------------------------------

  test("shows empty state and Grant Lease button on initial load", async ({
    page,
  }) => {
    await navigateToLeases(page);
    await expect(page.getByRole("heading", { name: "Active Leases" })).toBeVisible();
    await expect(page.getByText("No active leases.")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Grant Lease" }),
    ).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Grant lease
  // -------------------------------------------------------------------------

  test("grants a lease with TTL 120 and verifies it appears in the table", async ({
    page,
  }) => {
    await navigateToLeases(page);
    await grantLease(page, "120");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 120s/),
    ).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("120s").first()).toBeVisible();
  });

  test("grants a lease with TTL 300 and verifies it appears in the table", async ({
    page,
  }) => {
    await navigateToLeases(page);
    await grantLease(page, "300");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 300s/),
    ).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("300s").first()).toBeVisible();
  });

  test("shows error alert when grant fails", async ({ page }) => {
    await navigateToLeases(page);
    await injectError(page, "LeaseBinding.Grant", "lease store unavailable");
    await grantLease(page, "60");
    await expect(page.getByText("lease store unavailable")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "LeaseBinding.Grant");
  });

  // -------------------------------------------------------------------------
  // Keepalive lease
  // -------------------------------------------------------------------------

  test("keepalive a lease sends success toast", async ({ page }) => {
    await navigateToLeases(page);
    await grantLease(page, "60");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 60s/),
    ).toBeVisible({ timeout: 5_000 });

    // Select the lease row to reveal the detail panel
    await page.locator("table tbody tr").first().click();
    await expect(page.getByRole("button", { name: "KeepAlive" })).toBeVisible({
      timeout: 5_000,
    });
    await page.getByRole("button", { name: "KeepAlive" }).click();

    await expect(page.getByText(/kept alive|keep.alive/i).first()).toBeVisible({
      timeout: 5_000,
    });
  });

  test("shows error when keepalive fails", async ({ page }) => {
    await navigateToLeases(page);
    await grantLease(page, "60");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 60s/),
    ).toBeVisible({ timeout: 5_000 });

    await injectError(page, "LeaseBinding.KeepAlive", "keepalive rejected");

    await page.locator("table tbody tr").first().click();
    await expect(page.getByRole("button", { name: "KeepAlive" })).toBeVisible({
      timeout: 5_000,
    });
    await page.getByRole("button", { name: "KeepAlive" }).click();

    await expect(page.getByText("keepalive rejected")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "LeaseBinding.KeepAlive");
  });

  // -------------------------------------------------------------------------
  // Revoke lease
  // -------------------------------------------------------------------------

  test("revokes a lease and verifies it is removed from the list", async ({
    page,
  }) => {
    await navigateToLeases(page);
    await grantLease(page, "90");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 90s/),
    ).toBeVisible({ timeout: 5_000 });

    // Select the lease row to reveal the detail panel
    await page.locator("table tbody tr").first().click();
    await expect(page.getByRole("button", { name: "Revoke" })).toBeVisible({
      timeout: 5_000,
    });
    await page.getByRole("button", { name: "Revoke" }).click();

    // Confirm if a confirmation dialog is shown
    const confirmBtn = page.locator(".modal").getByRole("button", {
      name: /Revoke|Confirm/,
    });
    if (await confirmBtn.isVisible()) {
      await confirmBtn.click();
    }

    await expect(page.getByText(/revoked|Lease \d+ revoked/i).first()).toBeVisible({
      timeout: 5_000,
    });
    // Lease row should be gone from the list. Scoped to the table body: the
    // success toast at line 177 reads "...TTL 90s." too and can still be
    // visible here, which an unscoped getByText("90s") also matches.
    await expect(
      page.locator("table tbody").getByText("90s"),
    ).not.toBeVisible({ timeout: 5_000 });
  });

  test("shows error when revoke fails", async ({ page }) => {
    await navigateToLeases(page);
    await grantLease(page, "45");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 45s/),
    ).toBeVisible({ timeout: 5_000 });

    await injectError(page, "LeaseBinding.Revoke", "revoke denied");

    await page.locator("table tbody tr").first().click();
    await expect(page.getByRole("button", { name: "Revoke" })).toBeVisible({
      timeout: 5_000,
    });
    await page.getByRole("button", { name: "Revoke" }).click();

    const confirmBtn = page.locator(".modal").getByRole("button", {
      name: /Revoke|Confirm/,
    });
    if (await confirmBtn.isVisible()) {
      await confirmBtn.click();
    }

    await expect(page.getByText("revoke denied")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "LeaseBinding.Revoke");
  });

  // -------------------------------------------------------------------------
  // Multiple leases and refresh
  // -------------------------------------------------------------------------

  test("grants two leases and both appear in the table", async ({ page }) => {
    await navigateToLeases(page);
    await grantLease(page, "60");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 60s/),
    ).toBeVisible({ timeout: 5_000 });

    await grantLease(page, "180");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 180s/),
    ).toBeVisible({ timeout: 5_000 });

    await expect(page.getByText("60s").first()).toBeVisible();
    await expect(page.getByText("180s").first()).toBeVisible();
  });

  test("refresh button reloads the lease list", async ({ page }) => {
    await navigateToLeases(page);
    await grantLease(page, "75");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 75s/),
    ).toBeVisible({ timeout: 5_000 });

    await page.getByRole("button", { name: "Refresh" }).click();
    await expect(page.getByText("75s").first()).toBeVisible();
  });
});
