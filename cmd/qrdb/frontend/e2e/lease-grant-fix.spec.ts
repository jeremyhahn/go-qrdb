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
 * Regression tests for the lease grant operation bugs found during Chrome
 * browser testing.
 *
 * Bugs fixed:
 *   1. REST client returned `resp.lease` (undefined) instead of the response
 *      object directly, so the granted lease was never stored in the UI state.
 *   2. Field name mismatch: the response used `lease_id` (snake_case) but the
 *      UI read `leaseId` (camelCase), causing the lease ID to be undefined in
 *      success messages and table rows.
 *   3. FormModal action buttons were missing `type="button"`, causing them to
 *      submit the enclosing form and trigger a full page reload instead of
 *      invoking the click handler.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const leaseState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "lease-db", engine: "kv", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigate to the Leases tab within the database workspace. */
async function navigateToLeases(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("lease-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("lease-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page
    .locator("main nav").getByText("Leases", { exact: true })
    .click();
}

/**
 * Open the Grant Lease modal, optionally fill the TTL, and click Grant.
 * Omitting ttl leaves the default value in place.
 */
async function openGrantModal(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.getByRole("button", { name: "Grant Lease" }).click();
  await expect(
    page.locator(".modal").getByText("Grant Lease"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

async function submitGrant(
  page: import("@playwright/test").Page,
  ttl?: string,
): Promise<void> {
  if (ttl !== undefined) {
    await page.locator(".modal input[type='number']").fill(ttl);
  }
  // The Grant button inside FormModal must have type="button" so the click
  // handler fires rather than submitting a form (bug 3).
  await page.locator(".modal").getByRole("button", { name: "Grant" }).click();
}

// ---------------------------------------------------------------------------
// Regression: FormModal button type="button" fix
// ---------------------------------------------------------------------------

test.describe("Lease Grant - Regression (FormModal button fix)", () => {
  test.use({ mockState: leaseState });

  test("Grant button in modal fires click handler without page reload", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);

    // Attach a listener to detect a navigation/reload caused by form submission.
    let navigated = false;
    page.once("framenavigated", () => { navigated = true; });

    await submitGrant(page, "60");

    // A success toast must appear — if the button triggered a form submit the
    // page would reload and no toast would be visible.
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 60s/),
    ).toBeVisible({ timeout: 5_000 });

    // The page must not have navigated away.
    expect(navigated).toBe(false);
  });

  test("Grant button dismisses the modal after success", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);
    await submitGrant(page, "120");

    await expect(
      page.getByText(/Lease granted/),
    ).toBeVisible({ timeout: 5_000 });

    // The modal must be gone — if the button type was "submit" the dialog
    // would have stayed open or the page would have reloaded.
    await expect(
      page.locator(".modal").getByText("Grant Lease"),
    ).not.toBeVisible({ timeout: 5_000 });
  });

  // ---------------------------------------------------------------------------
  // Regression: resp.lease (undefined) instead of resp directly
  // ---------------------------------------------------------------------------

  test("after granting, lease appears in the active leases table", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);
    await submitGrant(page, "120");

    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 120s/),
    ).toBeVisible({ timeout: 5_000 });

    // The table must have a row — if resp.lease was returned (undefined) the
    // UI state would never be updated and the table would remain empty.
    await expect(
      page.locator("table tbody tr").first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("success toast shows a numeric lease ID", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);
    await submitGrant(page, "60");

    // The toast must contain a numeric ID. If leaseId was read from the wrong
    // field name (lease_id vs leaseId) the ID would be "undefined".
    const toastLocator = page.getByText(/Lease granted with ID \d+, TTL 60s/);
    await expect(toastLocator).toBeVisible({ timeout: 5_000 });

    const toastText = await toastLocator.textContent();
    expect(toastText).not.toContain("undefined");
    expect(toastText).toMatch(/Lease granted with ID \d+/);
  });

  // ---------------------------------------------------------------------------
  // Regression: field name leaseId vs lease_id
  // ---------------------------------------------------------------------------

  test("lease table row shows the correct TTL value", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);
    await submitGrant(page, "90");

    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 90s/),
    ).toBeVisible({ timeout: 5_000 });

    // The row must show the TTL — if field mapping was broken the cell would
    // display "undefined" or nothing.
    await expect(
      page.getByText("90s").first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("lease table row shows the granted TTL value", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);
    await submitGrant(page, "300");

    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 300s/),
    ).toBeVisible({ timeout: 5_000 });

    // The "granted TTL" column must also show a numeric value.
    // The wails-mock returns granted_ttl equal to the requested TTL.
    await expect(
      page.getByText("300s").first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("lease detail panel shows correct TTL and granted TTL after selecting row", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);
    await submitGrant(page, "120");

    await expect(
      page.getByText(/Lease granted/),
    ).toBeVisible({ timeout: 5_000 });

    // Click the row to open the detail panel.
    await page.locator("table tbody tr").first().click();

    // The detail panel must show the TTL info — confirms field mapping is correct.
    await expect(page.getByText("Time Remaining")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("120s remaining of 120s")).toBeVisible({ timeout: 5_000 });
  });

  // ---------------------------------------------------------------------------
  // Regression: modal Cancel button also has correct type="button"
  // ---------------------------------------------------------------------------

  test("Cancel button in modal closes dialog without granting a lease", async ({ page }) => {
    await navigateToLeases(page);
    await openGrantModal(page);

    // Click Cancel — must not submit and must close the modal.
    await page.locator(".modal").getByRole("button", { name: "Cancel" }).click();

    // Modal must close.
    await expect(
      page.locator(".modal").getByText("Grant Lease"),
    ).not.toBeVisible({ timeout: 5_000 });

    // Empty state must remain — no lease was created.
    await expect(page.getByText("No active leases.")).toBeVisible();
  });

  // ---------------------------------------------------------------------------
  // Multiple grants
  // ---------------------------------------------------------------------------

  test("granting multiple leases shows each in the table", async ({ page }) => {
    await navigateToLeases(page);

    await openGrantModal(page);
    await submitGrant(page, "60");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 60s/),
    ).toBeVisible({ timeout: 5_000 });

    await openGrantModal(page);
    await submitGrant(page, "120");
    await expect(
      page.getByText(/Lease granted with ID \d+, TTL 120s/),
    ).toBeVisible({ timeout: 5_000 });

    // Both rows must be present.
    const rows = page.locator("table tbody tr");
    await expect(rows).toHaveCount(2, { timeout: LOAD_TIMEOUT });
  });

  test("each granted lease has a non-undefined ID in the table", async ({ page }) => {
    await navigateToLeases(page);

    await openGrantModal(page);
    await submitGrant(page, "60");
    await expect(page.getByText(/Lease granted/).first()).toBeVisible({ timeout: 5_000 });

    await openGrantModal(page);
    await submitGrant(page, "60");
    await expect(page.getByText(/Lease granted/).first()).toBeVisible({ timeout: 5_000 });

    // Read all cell text in the table body — none must contain "undefined".
    const tableText = await page.locator("table tbody").textContent();
    expect(tableText).not.toContain("undefined");
  });
});
