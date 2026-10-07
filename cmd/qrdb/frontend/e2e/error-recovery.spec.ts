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
 * Error recovery tests — verifies that binding errors produce visible toast
 * feedback and that clearing the error allows normal operation to resume.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async DOM operations. */
const LOAD_TIMEOUT = 10_000;

/** Short timeout for toast visibility — toasts should appear quickly. */
const TOAST_TIMEOUT = 5_000;

// ---------------------------------------------------------------------------
// Mock state — a single KV database so the workspace is available.
// ---------------------------------------------------------------------------

const stateWithKvDb = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "err-db", backend: "pebble", status: "open" },
  ],
};


// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the root and selects the "err-db" database. */
async function navigateToDatabase(page: Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("err-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("err-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Navigates to the named admin section (admin is always visible in sidebar). */
async function navigateToAdmin(page: Page, section: string): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Admin sections are always in the sidebar — no toggle required.
  await page.locator("aside").getByText(section, { exact: true }).click();
  await expect(
    page.locator(".breadcrumbs").getByText(section),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Error Recovery", () => {
  test.use({ mockState: stateWithKvDb });

  test("KV put error shows toast and allows retry", async ({ page }) => {
    await navigateToDatabase(page);

    // Inject a put error.
    await injectError(page, "KVBinding.Put", "storage write error");

    // Attempt to create an entry — this will fail.
    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await expect(page.getByText("Create Entry")).toBeVisible();
    await page.locator("input[placeholder='Enter key']").fill("retry-key");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("retry-value");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();

    // Error toast must appear with the injected message.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: TOAST_TIMEOUT });
    await expect(alert.locator("span")).toContainText("storage write error");

    // Verify error is surfaced to the user — the core assertion.
    // Clear the error for cleanup.
    await clearError(page, "KVBinding.Put");
  });

  test("database list error shows toast", async ({ page }) => {
    // Navigate to the app and wait for it to finish its initial load.
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Inject the list error.
    await injectError(page, "DatabaseBinding.List", "database list unavailable");

    // Navigate to the admin Databases section — its onMount calls
    // DatabaseBinding.List and will surface the error as a toast because by
    // this point the connection is established.
    // Admin sections are always visible in the sidebar — no toggle required.
    await page.locator('aside button[title="Databases"]').click();

    // Error toast must appear with the injected message.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert.locator("span")).toContainText("database list unavailable");

    await clearError(page, "DatabaseBinding.List");
  });

  test("tenant create error shows feedback", async ({ page }) => {
    // Use a state that includes a database so admin views render fully.
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Admin sections are always visible in the sidebar — no toggle required.
    await page.locator("aside").getByText("Tenants", { exact: true }).click();
    await expect(
      page.locator(".breadcrumbs").getByText("Tenants"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const main = page.locator("main");

    // Wait for the CrudTable columns to render — this confirms the view loaded.
    await expect(
      main.locator("th", { hasText: "Name" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The toolbar Create Tenant button should now be visible.
    await expect(
      main.getByRole("button", { name: "Create Tenant" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Inject a create error.
    await injectError(page, "TenantBinding.Create", "tenant creation denied");

    // Open the create form via the CrudTable toolbar.
    await main.getByRole("button", { name: "Create Tenant" }).click();

    // Fill in the required fields.
    await page.locator("#tenant-id").fill("test-tenant");
    await page.locator("#tenant-name").fill("Test Tenant");
    await main.getByTestId("create-tenant-admin-username").fill("test-tenant-admin");
    await main.getByTestId("create-tenant-admin-csr").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----",
    );

    // Submit the form.
    await main.getByTestId("create-tenant-submit").click();

    // Error toast must appear with the injected message.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: TOAST_TIMEOUT });
    await expect(alert.locator("span")).toContainText("tenant creation denied");

    await clearError(page, "TenantBinding.Create");
  });

  test("seal status error shows toast in seal manager", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Inject the error before navigating so the status fetch on mount fails.
    await injectError(page, "SealBinding.Status", "seal status unavailable");

    // Admin sections are always visible in the sidebar — no toggle required.
    await page.locator("aside").getByText("Seal Manager", { exact: true }).click();

    // The toast must surface the backend error.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert.locator("span")).toContainText("seal status unavailable");

    await clearError(page, "SealBinding.Status");
  });
});
