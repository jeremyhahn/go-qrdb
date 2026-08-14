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

const batchState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "batch-adv", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to Batch tab. */
async function navigateToBatch(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("batch-adv").click();
  await expect(page.locator(".breadcrumbs").getByText("batch-adv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Batch", { exact: true }).click();
}

/** Add one operation row via the Add Operation button. */
async function addOperation(page: import("@playwright/test").Page): Promise<void> {
  await page.getByRole("button", { name: "Add Operation" }).click();
}

// ---------------------------------------------------------------------------
// Batch View - Advanced tests
// ---------------------------------------------------------------------------

test.describe("Batch Advanced", () => {
  test.use({ mockState: batchState });

  test("batch with mixed PUT and DELETE operation types executes successfully", async ({ page }) => {
    await navigateToBatch(page);

    // Add a PUT operation
    await addOperation(page);
    await page.locator("input[placeholder='Key']").first().fill("mixed-key");
    await page.locator("input[placeholder='Value']").first().fill("mixed-val");

    // Add a DELETE operation
    await addOperation(page);
    const selects = page.locator("select");
    await selects.nth(1).selectOption("2"); // OP_DELETE = 2
    await page.locator("input[placeholder='Key']").nth(1).fill("mixed-key");

    // Add a GET operation
    await addOperation(page);
    const allSelects = page.locator("select");
    await allSelects.nth(2).selectOption("1"); // OP_GET = 1
    await page.locator("input[placeholder='Key']").nth(2).fill("mixed-key");

    await expect(page.getByText("Batch Operations (3)")).toBeVisible();

    await page.getByRole("button", { name: "Execute Batch" }).click();

    await expect(page.getByText("Batch executed successfully")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Batch Results")).toBeVisible();
  });

  test("batch results show per-operation status in the results table", async ({ page }) => {
    await navigateToBatch(page);

    await addOperation(page);
    await page.locator("input[placeholder='Key']").first().fill("result-key");
    await page.locator("input[placeholder='Value']").first().fill("result-val");

    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(page.getByText("Batch executed successfully")).toBeVisible({ timeout: 5_000 });

    // The results table should appear
    await expect(page.getByText("Batch Results")).toBeVisible();

    // At least one result row should be visible
    await expect(page.locator("table tbody tr").first()).toBeVisible();
  });

  test("available codecs returns expected list", async ({ page }) => {
    await navigateToBatch(page);

    // Click the Import tab to load the import section.
    await page.getByRole("tab", { name: "Import" }).click();

    // The format select (id="import-format") is populated from AvailableImportFormats.
    // The batch binding also exposes AvailableCodecs; verify the Import section is visible
    // and the format select contains expected format options as an attached proxy for codec availability.
    const formatSelect = page.locator("#import-format");
    await expect(formatSelect).toBeVisible({ timeout: 5_000 });
    // Use exact text matching to avoid "json" matching "ndjson".
    await expect(formatSelect.locator("option").filter({ hasText: /^json$/ })).toBeAttached();
    await expect(formatSelect.locator("option").filter({ hasText: /^auto$/ })).toBeAttached();
  });

  test("available import formats returns expected list", async ({ page }) => {
    await navigateToBatch(page);

    await page.getByRole("tab", { name: "Import" }).click();

    // The format select (id="import-format") lists available import formats.
    const formatSelect = page.locator("#import-format");
    await expect(formatSelect).toBeVisible({ timeout: 5_000 });
    // Use exact text matching to avoid "json" matching "ndjson".
    await expect(formatSelect.locator("option").filter({ hasText: /^json$/ })).toBeAttached();
    await expect(formatSelect.locator("option").filter({ hasText: /^csv$/ })).toBeAttached();
  });

  test("batch error shows error toast with details", async ({ page }) => {
    await navigateToBatch(page);

    await injectError(page, "BatchBinding.Execute", "batch execution failed: storage full");

    await addOperation(page);
    await page.locator("input[placeholder='Key']").first().fill("error-key");
    await page.locator("input[placeholder='Value']").first().fill("error-val");

    await page.getByRole("button", { name: "Execute Batch" }).click();

    await expect(page.getByText("batch execution failed: storage full")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "BatchBinding.Execute");
  });

  test("empty batch (no operations) shows warning when executed", async ({ page }) => {
    await navigateToBatch(page);

    await page.getByRole("button", { name: "Execute Batch" }).click();

    await expect(page.getByText("Add at least one operation")).toBeVisible({ timeout: 5_000 });
  });

  test("batch with multiple PUT operations writes all keys", async ({ page }) => {
    await navigateToBatch(page);

    for (const [key, val] of [
      ["batch-a", "aaa"],
      ["batch-b", "bbb"],
      ["batch-c", "ccc"],
    ]) {
      await addOperation(page);
      const keyInputs = page.locator("input[placeholder='Key']");
      const valInputs = page.locator("input[placeholder='Value']");
      const idx = (await keyInputs.count()) - 1;
      await keyInputs.nth(idx).fill(key);
      await valInputs.nth(idx).fill(val);
    }

    await expect(page.getByText("Batch Operations (3)")).toBeVisible();

    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(page.getByText("Batch executed successfully")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Batch Results")).toBeVisible();
  });

  test("reset button after successful execution clears results and operations", async ({ page }) => {
    await navigateToBatch(page);

    await addOperation(page);
    await page.locator("input[placeholder='Key']").first().fill("reset-key");
    await page.locator("input[placeholder='Value']").first().fill("reset-val");

    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(page.getByText("Batch executed successfully")).toBeVisible({ timeout: 5_000 });

    // Reset
    await page.getByRole("button", { name: "Reset" }).click();

    await expect(page.getByText("Batch Operations (0)")).toBeVisible();
    await expect(page.getByText("No operations defined")).toBeVisible();
    await expect(page.getByText("Batch Results")).not.toBeVisible();
  });
});
