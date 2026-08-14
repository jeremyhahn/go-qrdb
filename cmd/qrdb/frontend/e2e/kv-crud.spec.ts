// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const kvState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-kv", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to KV database workspace. */
async function navigateToKV(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("test-kv").click();
  await expect(page.locator(".breadcrumbs").getByText("test-kv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

// ---------------------------------------------------------------------------
// KV Explorer CRUD
// ---------------------------------------------------------------------------

test.describe("KV Explorer CRUD", () => {
  test.use({ mockState: kvState });

  test("shows empty state when no entries exist", async ({ page }) => {
    await navigateToKV(page);
    await expect(page.getByText("0 entries")).toBeVisible();
  });

  test("create entry via slide panel", async ({ page }) => {
    await navigateToKV(page);

    // Open create panel via toolbar Create button
    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await expect(page.getByText("Create Entry")).toBeVisible();

    // Fill key and value
    await page.locator("input[placeholder='Enter key']").fill("greeting");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("hello world");

    // Submit via the panel footer Create button
    await page.getByRole("button", { name: "Create", exact: true }).last().click();

    // Verify success toast
    await expect(page.getByText('Created "greeting"')).toBeVisible({ timeout: 5_000 });

    // Verify entry appears in table
    await expect(page.getByText("greeting").first()).toBeVisible();
    await expect(page.getByText("1 entries")).toBeVisible();
  });

  test("read entry in detail panel", async ({ page }) => {
    await navigateToKV(page);

    // Create an entry first
    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await page.locator("input[placeholder='Enter key']").fill("mykey");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("myvalue");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();
    await expect(page.getByText('Created "mykey"')).toBeVisible({ timeout: 5_000 });

    // Click the row to open detail
    await page.getByText("mykey").first().click();

    // Verify detail panel shows key and value
    const detailPanel = page.getByLabel("Details");
    await expect(detailPanel.getByText("mykey")).toBeVisible();
    await expect(detailPanel.getByText("myvalue")).toBeVisible();
  });

  test("update entry via edit panel", async ({ page }) => {
    await navigateToKV(page);

    // Create entry
    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await page.locator("input[placeholder='Enter key']").fill("editable");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("original");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();
    await expect(page.getByText('Created "editable"')).toBeVisible({ timeout: 5_000 });

    // Click row to open detail
    await page.getByText("editable").first().click();

    // Click Edit button in detail footer
    await page.getByRole("button", { name: "Edit" }).click();

    // Verify edit mode
    await expect(page.getByText("Edit Entry")).toBeVisible();

    // Key should be disabled in edit mode
    const keyInput = page.locator("input[placeholder='Enter key']");
    await expect(keyInput).toBeDisabled();

    // Change value
    const valueTextarea = page.locator("textarea[placeholder='Enter value (supports JSON)']");
    await valueTextarea.fill("updated");

    // Save
    await page.getByRole("button", { name: "Save" }).click();

    // Verify success toast
    await expect(page.getByText('Updated "editable"')).toBeVisible({ timeout: 5_000 });
  });

  test("delete entry with confirmation", async ({ page }) => {
    await navigateToKV(page);

    // Create entry
    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await page.locator("input[placeholder='Enter key']").fill("to-delete");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("temp");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();
    await expect(page.getByText('Created "to-delete"')).toBeVisible({ timeout: 5_000 });

    // Click row to open detail
    await page.getByText("to-delete").first().click();

    // Click Delete in the detail panel
    const detailPanel = page.getByLabel("Details");
    await detailPanel.getByRole("button", { name: "Delete" }).click();

    // Confirm in modal
    await expect(page.getByText("Delete Entry")).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();

    // Verify success toast
    await expect(page.getByText('Deleted "to-delete"')).toBeVisible({ timeout: 5_000 });

    // Verify entry is gone
    await expect(page.getByText("0 entries")).toBeVisible();
  });

  test("prefix filter narrows results", async ({ page }) => {
    await navigateToKV(page);

    // Create entries with different prefixes
    for (const [key, value] of [["app/config", "v1"], ["app/name", "test"], ["user/id", "123"]]) {
      await page.getByRole("button", { name: "Create", exact: true }).first().click();
      await page.locator("input[placeholder='Enter key']").fill(key);
      await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill(value);
      await page.getByRole("button", { name: "Create", exact: true }).last().click();
      await expect(page.getByText(`Created "${key}"`)).toBeVisible({ timeout: 5_000 });
    }

    // Verify all 3 entries
    await expect(page.getByText("3 entries")).toBeVisible();

    // Filter by prefix
    await page.locator("input[placeholder='Filter by key prefix...']").fill("app/");
    await page.locator("[aria-label='Search']").click();

    // Verify filtered results
    await expect(page.getByText("2 entries")).toBeVisible();
  });

  test("create entry with empty key shows warning", async ({ page }) => {
    await navigateToKV(page);

    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    // Leave key empty, fill value
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("some value");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();

    await expect(page.getByText("Key is required")).toBeVisible({ timeout: 5_000 });
  });

  test("create entry with empty value shows warning", async ({ page }) => {
    await navigateToKV(page);

    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await page.locator("input[placeholder='Enter key']").fill("novalue");
    // Leave value empty
    await page.getByRole("button", { name: "Create", exact: true }).last().click();

    await expect(page.getByText("Value is required")).toBeVisible({ timeout: 5_000 });
  });

  test("error on put shows error toast", async ({ page }) => {
    await navigateToKV(page);

    // Inject error
    await injectError(page, "KVBinding.Put", "storage write error");

    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await page.locator("input[placeholder='Enter key']").fill("fail-key");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("fail-value");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();

    await expect(page.getByText("storage write error")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "KVBinding.Put");
  });

  test("refresh button reloads entries", async ({ page }) => {
    await navigateToKV(page);

    // Create an entry
    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await page.locator("input[placeholder='Enter key']").fill("refresh-test");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("data");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();
    await expect(page.getByText('Created "refresh-test"')).toBeVisible({ timeout: 5_000 });

    // Click refresh
    await page.getByRole("button", { name: "Refresh" }).click();

    // Entry should still be visible
    await expect(page.getByText("refresh-test").first()).toBeVisible();
    await expect(page.getByText("1 entries")).toBeVisible();
  });
});
