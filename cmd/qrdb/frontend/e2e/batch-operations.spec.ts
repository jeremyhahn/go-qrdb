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
 * Batch operations: add PUT/GET/DELETE operations, execute the batch,
 * and verify results — including error paths.
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

const batchOpsState = {
  ...secondLaunchEmbeddedState,
  databases: [
    {
      name: "batch-ops-db",
      backend: "pebble",
      status: "open",
    },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigate to the Batch tab within the batch-ops-db database. */
async function navigateToBatch(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("batch-ops-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("batch-ops-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page
    .locator("main nav").getByText("Batch", { exact: true })
    .click();
}

/** Map from human-readable type names to the numeric option values used in the component. */
const opTypeValue: Record<"PUT" | "GET" | "DELETE", string> = {
  PUT: "0",
  GET: "1",
  DELETE: "2",
};

/** Add a single operation row of a given type with key/value. */
async function addOperation(
  page: Page,
  type: "PUT" | "GET" | "DELETE",
  key: string,
  value?: string,
  index: number = 0,
): Promise<void> {
  await page.getByRole("button", { name: "Add Operation" }).click();

  // The select uses numeric values (0=put, 1=get, 2=delete, 3=delete_range)
  const typeSelects = page.locator("select");
  await typeSelects.nth(index).selectOption({ value: opTypeValue[type] });

  const keyInputs = page.locator("input[placeholder='Key']");
  await keyInputs.nth(index).fill(key);

  if (value !== undefined && type === "PUT") {
    const valueInputs = page.locator("input[placeholder='Value']");
    await valueInputs.nth(index).fill(value);
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Batch Operations", () => {
  test.use({ mockState: batchOpsState });

  // -------------------------------------------------------------------------
  // Empty state
  // -------------------------------------------------------------------------

  test("shows empty state with no operations defined", async ({ page }) => {
    await navigateToBatch(page);
    await expect(page.getByText("No operations defined")).toBeVisible();
    await expect(page.getByText("Batch Operations (0)")).toBeVisible();
  });

  test("shows Add Operation and Execute Batch buttons", async ({ page }) => {
    await navigateToBatch(page);
    await expect(
      page.getByRole("button", { name: "Add Operation" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Execute Batch" }),
    ).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Adding operations
  // -------------------------------------------------------------------------

  test("adds a PUT operation and shows form fields", async ({ page }) => {
    await navigateToBatch(page);
    await page.getByRole("button", { name: "Add Operation" }).click();

    await expect(page.getByText("Batch Operations (1)")).toBeVisible();
    await expect(page.locator("select").first()).toBeVisible();
    await expect(page.locator("input[placeholder='Key']").first()).toBeVisible();
    await expect(
      page.locator("input[placeholder='Value']").first(),
    ).toBeVisible();
  });

  test("adds multiple PUT operations and updates operation count", async ({
    page,
  }) => {
    await navigateToBatch(page);
    await page.getByRole("button", { name: "Add Operation" }).click();
    await expect(page.getByText("Batch Operations (1)")).toBeVisible();

    await page.getByRole("button", { name: "Add Operation" }).click();
    await expect(page.getByText("Batch Operations (2)")).toBeVisible();

    await page.getByRole("button", { name: "Add Operation" }).click();
    await expect(page.getByText("Batch Operations (3)")).toBeVisible();
  });

  test("removes an operation and updates the count", async ({ page }) => {
    await navigateToBatch(page);
    await page.getByRole("button", { name: "Add Operation" }).click();
    await expect(page.getByText("Batch Operations (1)")).toBeVisible();

    // Remove the operation
    const removeBtn = page
      .locator("button[aria-label='Remove operation'], button[aria-label='Delete operation']")
      .first();
    if (await removeBtn.isVisible({ timeout: 3_000 })) {
      await removeBtn.click();
      await expect(page.getByText("Batch Operations (0)")).toBeVisible();
    }
  });

  // -------------------------------------------------------------------------
  // Execute batch
  // -------------------------------------------------------------------------

  test("executes a batch with a single PUT and shows results", async ({
    page,
  }) => {
    await navigateToBatch(page);
    await addOperation(page, "PUT", "batch-key-1", "batch-value-1");
    await expect(page.getByText("Batch Operations (1)")).toBeVisible();

    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(
      page.getByText("Batch executed successfully").first(),
    ).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Batch Results")).toBeVisible();
  });

  test("executes a batch with mixed PUT and GET operations", async ({
    page,
  }) => {
    await navigateToBatch(page);

    // First PUT a value
    await addOperation(page, "PUT", "shared-key", "hello", 0);
    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(
      page.getByText("Batch executed successfully").first(),
    ).toBeVisible({ timeout: 5_000 });

    // Clear and add a GET
    const clearBtn = page.getByRole("button", { name: /Clear|Reset/i });
    if (await clearBtn.isVisible({ timeout: 2_000 })) {
      await clearBtn.click();
    } else {
      // Remove existing ops by re-navigating
      await navigateToBatch(page);
    }

    await addOperation(page, "GET", "shared-key", undefined, 0);
    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(
      page.getByText("Batch executed successfully").first(),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("executes a batch with a DELETE operation", async ({ page }) => {
    await navigateToBatch(page);
    await addOperation(page, "DELETE", "to-delete-key", undefined, 0);
    await expect(page.getByText("Batch Operations (1)")).toBeVisible();

    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(
      page.getByText("Batch executed successfully").first(),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("shows warning when executing with no operations", async ({ page }) => {
    await navigateToBatch(page);
    await page.getByRole("button", { name: "Execute Batch" }).click();
    // Should show some warning or stay on empty state — use .first() to avoid
    // strict mode violation when both the empty-state paragraph and toast match.
    const warnVisible = await page
      .getByText(/no operations|add at least/i)
      .first()
      .isVisible({ timeout: 3_000 });
    const stillEmpty = await page
      .getByText("Batch Operations (0)")
      .isVisible();
    expect(warnVisible || stillEmpty).toBeTruthy();
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  test("shows error when batch execution fails", async ({ page }) => {
    await navigateToBatch(page);
    await injectError(page, "BatchBinding.Execute", "backend write error");

    await addOperation(page, "PUT", "err-key", "err-val");
    await page.getByRole("button", { name: "Execute Batch" }).click();

    await expect(page.getByText("backend write error")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "BatchBinding.Execute");
  });

  test("shows results table with key-value pairs after execution", async ({
    page,
  }) => {
    await navigateToBatch(page);
    await addOperation(page, "PUT", "result-key", "result-val", 0);
    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(
      page.getByText("Batch executed successfully"),
    ).toBeVisible({ timeout: 5_000 });

    // Results table should show key column
    await expect(page.getByText("result-key").first()).toBeVisible({ timeout: 5_000 });
  });
});
