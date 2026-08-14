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

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const leaseState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "lease-db", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the Leases tab within the database workspace. */
async function navigateToLeases(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("lease-db").click();
  await expect(page.locator(".breadcrumbs").getByText("lease-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Leases", { exact: true }).click();
}

/** Grant a lease via the modal with the given TTL string. */
async function grantLease(page: import("@playwright/test").Page, ttl: string): Promise<void> {
  await page.getByRole("button", { name: "Grant Lease" }).click();
  await expect(page.locator(".modal").getByText("Grant Lease")).toBeVisible();
  const ttlInput = page.locator(".modal input[type='number']");
  await ttlInput.fill(ttl);
  await page.locator(".modal").getByRole("button", { name: "Grant" }).click();
}

// ---------------------------------------------------------------------------
// Lease Manager E2E Tests
// ---------------------------------------------------------------------------

test.describe("Lease Manager", () => {
  test.use({ mockState: leaseState });

  // -------------------------------------------------------------------------
  // Empty state
  // -------------------------------------------------------------------------

  test("shows empty state when no leases exist", async ({ page }) => {
    await navigateToLeases(page);

    await expect(page.getByRole("heading", { name: "Active Leases" })).toBeVisible();
    await expect(page.getByText("No active leases.")).toBeVisible();
  });

  test("shows lease detail placeholder when no lease is selected", async ({ page }) => {
    await navigateToLeases(page);

    await expect(page.getByText("Lease Detail")).toBeVisible();
    await expect(page.getByText("Select a lease to view details.")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Grant lease
  // -------------------------------------------------------------------------

  test("grant a lease with valid TTL", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "120");

    // Verify success toast
    await expect(page.getByText(/Lease granted with ID \d+, TTL 120s/)).toBeVisible({
      timeout: 5_000,
    });

    // Verify lease appears in table
    await expect(page.getByText("120s").first()).toBeVisible();
  });

  test("grant a second lease shows both in the table", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted with ID \d+, TTL 60s/)).toBeVisible({
      timeout: 5_000,
    });

    await grantLease(page, "300");
    await expect(page.getByText(/Lease granted with ID \d+, TTL 300s/)).toBeVisible({
      timeout: 5_000,
    });

    // Both leases should appear
    await expect(page.getByText("60s").first()).toBeVisible();
    await expect(page.getByText("300s").first()).toBeVisible();
  });

  test("grant lease with default TTL of 60", async ({ page }) => {
    await navigateToLeases(page);

    // Open modal and submit without changing the default value
    await page.getByRole("button", { name: "Grant Lease" }).click();
    await expect(page.locator(".modal").getByText("Grant Lease")).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Grant" }).click();

    await expect(page.getByText(/Lease granted with ID \d+, TTL 60s/)).toBeVisible({
      timeout: 5_000,
    });
  });

  // -------------------------------------------------------------------------
  // List leases
  // -------------------------------------------------------------------------

  test("refresh button reloads lease list", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "90");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Click refresh
    await page.getByRole("button", { name: "Refresh" }).click();

    // Lease should still be visible
    await expect(page.getByText("90s").first()).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Lease detail (row click / TTL display)
  // -------------------------------------------------------------------------

  test("clicking a lease row shows detail panel with TTL info", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "120");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Click the lease row in the table
    await page.locator("table tbody tr").first().click();

    // Verify detail panel shows lease info
    await expect(page.getByText("Time Remaining")).toBeVisible();
    await expect(page.getByText("120s remaining of 120s")).toBeVisible();
    await expect(page.locator(".font-mono.text-xs.w-12").getByText("100%")).toBeVisible();
  });

  test("detail panel shows KeepAlive and Revoke buttons", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Click the lease row
    await page.locator("table tbody tr").first().click();

    // Verify action buttons
    await expect(page.getByRole("button", { name: "KeepAlive" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Revoke" })).toBeVisible();
  });

  test("detail panel shows progress bar with correct color for full TTL", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Click the lease row
    await page.locator("table tbody tr").first().click();

    // At 100%, progress bar should have the success class
    const progress = page.locator("progress");
    await expect(progress).toBeVisible();
    await expect(progress).toHaveClass(/progress-success/);
  });

  // -------------------------------------------------------------------------
  // Keep-alive
  // -------------------------------------------------------------------------

  test("keep-alive sends success toast and refreshes list", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Click the lease row to select it
    await page.locator("table tbody tr").first().click();
    await expect(page.getByRole("button", { name: "KeepAlive" })).toBeVisible();

    // Click KeepAlive
    await page.getByRole("button", { name: "KeepAlive" }).click();

    // Verify success toast
    await expect(page.getByText(/Lease \d+ keep-alive sent/)).toBeVisible({
      timeout: 5_000,
    });
  });

  // -------------------------------------------------------------------------
  // Revoke lease
  // -------------------------------------------------------------------------

  test("revoke lease removes it from the table", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Click the lease row to select it
    await page.locator("table tbody tr").first().click();
    await expect(page.getByRole("button", { name: "Revoke" })).toBeVisible();

    // Click Revoke
    await page.getByRole("button", { name: "Revoke" }).click();

    // Verify success toast
    await expect(page.getByText(/Lease \d+ revoked/)).toBeVisible({
      timeout: 5_000,
    });

    // Verify lease is gone and empty state returns
    await expect(page.getByText("No active leases.")).toBeVisible();
  });

  test("revoke clears the detail panel", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Select the lease
    await page.locator("table tbody tr").first().click();
    await expect(page.getByText("Time Remaining")).toBeVisible();

    // Revoke it
    await page.getByRole("button", { name: "Revoke" }).click();
    await expect(page.getByText(/Lease \d+ revoked/)).toBeVisible({ timeout: 5_000 });

    // Detail panel should reset to placeholder
    await expect(page.getByText("Select a lease to view details.")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  test("error on grant shows error toast", async ({ page }) => {
    await navigateToLeases(page);

    await injectError(page, "LeaseBinding.Grant", "lease grant failed: quota exceeded");

    await grantLease(page, "60");

    await expect(page.getByText("lease grant failed: quota exceeded")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "LeaseBinding.Grant");
  });

  test("error on list shows error toast", async ({ page }) => {
    await navigateToLeases(page);

    await injectError(page, "LeaseBinding.List", "lease list unavailable");

    // Trigger a refresh to invoke List
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("lease list unavailable")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "LeaseBinding.List");
  });

  test("error on revoke shows error toast", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Select the lease
    await page.locator("table tbody tr").first().click();

    // Inject revoke error
    await injectError(page, "LeaseBinding.Revoke", "lease revoke denied");

    await page.getByRole("button", { name: "Revoke" }).click();

    await expect(page.getByText("lease revoke denied")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "LeaseBinding.Revoke");
  });

  test("error on keep-alive shows error toast", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Select the lease
    await page.locator("table tbody tr").first().click();

    // Inject keep-alive error
    await injectError(page, "LeaseBinding.KeepAlive", "keep-alive timeout");

    await page.getByRole("button", { name: "KeepAlive" }).click();

    await expect(page.getByText("keep-alive timeout")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "LeaseBinding.KeepAlive");
  });

  test("error on time-to-live shows error toast when clicking row", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await expect(page.getByText(/Lease granted/)).toBeVisible({ timeout: 5_000 });

    // Inject TTL error
    await injectError(page, "LeaseBinding.TimeToLive", "ttl lookup failed");

    // Click the lease row (triggers handleTimeToLive)
    await page.locator("table tbody tr").first().click();

    await expect(page.getByText("ttl lookup failed")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "LeaseBinding.TimeToLive");
  });

  // -------------------------------------------------------------------------
  // Auto-refresh toggle
  // -------------------------------------------------------------------------

  test("auto-refresh toggle is present and unchecked by default", async ({ page }) => {
    await navigateToLeases(page);

    const toggle = page.locator("input[type='checkbox'].toggle");
    await expect(toggle).toBeVisible();
    await expect(toggle).not.toBeChecked();
  });

  // -------------------------------------------------------------------------
  // Grant modal cancel
  // -------------------------------------------------------------------------

  test("cancel grant modal does not create a lease", async ({ page }) => {
    await navigateToLeases(page);

    // Open the grant modal
    await page.getByRole("button", { name: "Grant Lease" }).click();
    await expect(page.locator(".modal").getByText("Grant Lease")).toBeVisible();

    // Cancel via the Cancel button
    await page.locator(".modal").getByRole("button", { name: "Cancel" }).click();

    // No lease should be created, empty state persists
    await expect(page.getByText("No active leases.")).toBeVisible();
  });
});
