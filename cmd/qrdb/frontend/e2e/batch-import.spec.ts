// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const batchState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "batch-db", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to Batch view and click the Import tab. */
async function navigateToImport(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("batch-db").click();
  await expect(page.locator(".breadcrumbs").getByText("batch-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Batch", { exact: true }).click();
  // Click the inner Import tab
  await page.locator("[role='tablist']").getByRole("tab", { name: "Import" }).click();
}

test.describe("Batch Import Tab", () => {
  test.use({ mockState: batchState });

  test("Import tab renders all form elements", async ({ page }) => {
    await navigateToImport(page);

    // File path input
    await expect(page.locator("#import-file-path")).toBeVisible();

    // Format select
    await expect(page.locator("#import-format")).toBeVisible();

    // Key Field input
    await expect(page.locator("#import-key-field")).toBeVisible();

    // Value Mode radios
    await expect(page.getByText("Entire Row")).toBeVisible();
    await expect(page.getByText("Specific Field")).toBeVisible();

    // Target radios
    await expect(page.getByText("Raw KV")).toBeVisible();
    await expect(page.getByText("Collection", { exact: true })).toBeVisible();

    // Skip Header checkbox (checked by default)
    await expect(page.getByText("Skip Header")).toBeVisible();

    // Transactional checkbox (unchecked by default)
    await expect(page.getByText("Transactional")).toBeVisible();

    // Import button
    await expect(page.getByRole("button", { name: "Import" })).toBeVisible();
  });

  test("format dropdown is populated from availableImportFormats", async ({ page }) => {
    await navigateToImport(page);

    const formatSelect = page.locator("#import-format");
    await expect(formatSelect).toBeVisible({ timeout: LOAD_TIMEOUT });

    // All mock formats should appear as options
    for (const fmt of ["auto", "csv", "json", "ndjson", "yaml", "archive"]) {
      await expect(formatSelect.locator(`option[value="${fmt}"]`)).toBeAttached();
    }
  });

  test("Import button is disabled when file path is empty", async ({ page }) => {
    await navigateToImport(page);

    const importBtn = page.getByRole("button", { name: "Import" });
    await expect(importBtn).toBeDisabled();
  });

  test("Import button becomes enabled when file path is filled", async ({ page }) => {
    await navigateToImport(page);

    await page.locator("#import-file-path").fill("/data/records.csv");
    // Key field is also required for a real import, but the button enables as soon as path is non-empty
    await expect(page.getByRole("button", { name: "Import" })).toBeEnabled();
  });

  test("Value Field input is hidden when Value Mode is Entire Row", async ({ page }) => {
    await navigateToImport(page);

    // "row" is the default
    await expect(page.locator("#import-value-field")).not.toBeVisible();
  });

  test("Value Field input appears when Value Mode is Specific Field", async ({ page }) => {
    await navigateToImport(page);

    await page.getByLabel("Specific Field").check();
    await expect(page.locator("#import-value-field")).toBeVisible();
  });

  test("Collection fields appear when Collection target is selected", async ({ page }) => {
    await navigateToImport(page);

    // Initially hidden
    await expect(page.locator("#import-database")).not.toBeVisible();
    await expect(page.locator("#import-collection")).not.toBeVisible();

    // Select collection target
    await page.getByLabel("Collection").check();

    await expect(page.locator("#import-database")).toBeVisible();
    await expect(page.locator("#import-collection")).toBeVisible();
  });

  test("successful import shows result summary with zero failures", async ({ page }) => {
    await navigateToImport(page);

    await page.locator("#import-file-path").fill("/data/records.json");
    await page.locator("#import-key-field").fill("id");

    await page.getByRole("button", { name: "Import" }).click();

    // Success toast
    await expect(page.locator('[role="alert"]').getByText(/Imported 3 of 3/)).toBeVisible({ timeout: 5_000 });

    // Results panel
    await expect(page.getByText("Import Results")).toBeVisible();
    await expect(page.getByText(/Imported.*3.*of.*3/).first()).toBeVisible();
  });

  test("import with failures shows error table", async ({ page, context: _ }) => {
    await navigateToImport(page);

    // Override mock to return failures
    await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as {
        main: {
          BatchBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        };
      };
      go.main.BatchBinding.ImportFile = async () => ({
        total: 5,
        succeeded: 3,
        failed: 2,
        errors: [
          { row: 2, key: "bad-key-1", message: "duplicate key" },
          { row: 4, key: "bad-key-2", message: "value too large" },
        ],
      });
    });

    await page.locator("#import-file-path").fill("/data/records.csv");
    await page.locator("#import-key-field").fill("id");

    await page.getByRole("button", { name: "Import" }).click();

    // Warning toast (partial success)
    await expect(page.locator('[role="alert"]').getByText(/3 of 5/)).toBeVisible({ timeout: 5_000 });

    // Error table visible
    await expect(page.getByText("Import Errors")).toBeVisible();
    await expect(page.getByText("duplicate key")).toBeVisible();
    await expect(page.getByText("value too large")).toBeVisible();
  });

  test("empty key field shows warning without calling import", async ({ page }) => {
    await navigateToImport(page);

    await page.locator("#import-file-path").fill("/data/records.csv");
    // Leave key field empty

    await page.getByRole("button", { name: "Import" }).click();

    await expect(page.getByText("Key Field is required")).toBeVisible({ timeout: 5_000 });
  });

  test("Reset button clears the form", async ({ page }) => {
    await navigateToImport(page);

    await page.locator("#import-file-path").fill("/data/records.json");
    await page.locator("#import-key-field").fill("id");

    await page.getByRole("button", { name: "Reset" }).click();

    await expect(page.locator("#import-file-path")).toHaveValue("");
    await expect(page.locator("#import-key-field")).toHaveValue("");
  });

  test("switching back to Operations tab shows operations builder", async ({ page }) => {
    await navigateToImport(page);

    // Switch back to Operations
    await page.locator("[role='tablist']").getByRole("tab", { name: "Operations" }).click();

    await expect(page.getByText("Batch Operations (0)")).toBeVisible();
    await expect(page.getByText("No operations defined")).toBeVisible();
  });
});
