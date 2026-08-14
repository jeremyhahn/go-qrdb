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
    { name: "lease-adv", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the Leases tab within the database workspace. */
async function navigateToLeases(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("lease-adv").click();
  await expect(page.locator(".breadcrumbs").getByText("lease-adv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Leases", { exact: true }).click();
}

/** Grant a lease via the modal with the given TTL. */
async function grantLease(page: import("@playwright/test").Page, ttl: string): Promise<void> {
  await page.getByRole("button", { name: "Grant Lease" }).click();
  await expect(page.locator(".modal").getByText("Grant Lease")).toBeVisible();
  const ttlInput = page.locator(".modal input[type='number']");
  await ttlInput.fill(ttl);
  await page.locator(".modal").getByRole("button", { name: "Grant" }).click();
  // Use last() because multiple grant toasts may be visible simultaneously.
  await expect(page.getByText(/Lease granted with ID \d+/).last()).toBeVisible({ timeout: 5_000 });
}

/** Grant a lease and click its row to select it. */
async function grantAndSelect(page: import("@playwright/test").Page, ttl: string): Promise<void> {
  await grantLease(page, ttl);
  await page.locator("table tbody tr").first().click();
  await expect(page.getByText("Time Remaining")).toBeVisible({ timeout: 5_000 });
}

// ---------------------------------------------------------------------------
// Lease Manager - Advanced tests
// ---------------------------------------------------------------------------

test.describe("Lease Manager Advanced", () => {
  test.use({ mockState: leaseState });

  test("keep-alive resets TTL on a lease", async ({ page }) => {
    await navigateToLeases(page);
    await grantAndSelect(page, "120");

    await page.getByRole("button", { name: "KeepAlive" }).click();

    await expect(page.getByText(/Lease \d+ keep-alive sent/)).toBeVisible({ timeout: 5_000 });
    // After keep-alive the TTL percentage should remain at or near 100%
    await expect(page.locator(".font-mono.text-xs.w-12", { hasText: "100%" })).toBeVisible();
  });

  test("revoke lease removes it from the list", async ({ page }) => {
    await navigateToLeases(page);
    await grantAndSelect(page, "60");

    await page.getByRole("button", { name: "Revoke" }).click();

    await expect(page.getByText(/Lease \d+ revoked/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("No active leases.")).toBeVisible();
  });

  test("time-to-live shows correct remaining TTL in detail panel", async ({ page }) => {
    await navigateToLeases(page);
    await grantAndSelect(page, "300");

    // The detail panel should report the remaining TTL equal to the granted TTL
    await expect(page.getByText("300s remaining of 300s")).toBeVisible();
  });

  test("lease detail shows granted TTL", async ({ page }) => {
    await navigateToLeases(page);
    await grantAndSelect(page, "90");

    // Verify the granted TTL column in the detail / table shows 90s
    await expect(page.getByText("90s").first()).toBeVisible();
  });

  test("keep-alive error shows error toast", async ({ page }) => {
    await navigateToLeases(page);
    await grantAndSelect(page, "60");

    await injectError(page, "LeaseBinding.KeepAlive", "keep-alive failed: lease expired");

    await page.getByRole("button", { name: "KeepAlive" }).click();

    await expect(page.getByText("keep-alive failed: lease expired")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "LeaseBinding.KeepAlive");
  });

  test("revoke error shows error toast", async ({ page }) => {
    await navigateToLeases(page);
    await grantAndSelect(page, "60");

    await injectError(page, "LeaseBinding.Revoke", "revoke denied: insufficient permissions");

    await page.getByRole("button", { name: "Revoke" }).click();

    await expect(page.getByText("revoke denied: insufficient permissions")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "LeaseBinding.Revoke");
  });

  test("time-to-live error shows error toast", async ({ page }) => {
    await navigateToLeases(page);
    await grantAndSelect(page, "60");

    await injectError(page, "LeaseBinding.TimeToLive", "ttl query failed");

    // The TTL is refreshed when clicking the row; click a second lease row to
    // trigger another TTL call on the selected lease.
    // Instead, trigger an explicit TTL refresh if the UI provides a Refresh TTL
    // button; otherwise rely on the periodic auto-refresh or keep-alive path.
    // Use the Refresh button in the toolbar to reload the list and re-select.
    await page.getByRole("button", { name: "Refresh" }).click();

    // After refresh, clicking the row again invokes TimeToLive
    await page.locator("table tbody tr").first().click();

    await expect(page.getByText("ttl query failed")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "LeaseBinding.TimeToLive");
  });

  test("revoking one lease leaves others intact", async ({ page }) => {
    await navigateToLeases(page);

    await grantLease(page, "60");
    await grantLease(page, "300");

    await expect(page.getByText("60s").first()).toBeVisible();
    await expect(page.getByText("300s").first()).toBeVisible();

    // Select and revoke the first lease (60s)
    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Revoke" }).click();
    await expect(page.getByText(/Lease \d+ revoked/)).toBeVisible({ timeout: 5_000 });

    // 300s lease should still be visible
    await expect(page.getByText("300s").first()).toBeVisible();
  });
});
