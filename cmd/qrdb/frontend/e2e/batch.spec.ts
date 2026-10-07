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
    { name: "batch-db", backend: "pebble", status: "open" },
  ],
};

/** Navigate to Batch tab. */
async function navigateToBatch(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("batch-db").click();
  await expect(page.locator(".breadcrumbs").getByText("batch-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Batch", { exact: true }).click();
}

test.describe("Batch View", () => {
  test.use({ mockState: batchState });

  test("shows empty state with no operations", async ({ page }) => {
    await navigateToBatch(page);
    await expect(page.getByText("No operations defined")).toBeVisible();
    await expect(page.getByText("Batch Operations (0)")).toBeVisible();
  });

  test("add put operation shows form fields", async ({ page }) => {
    await navigateToBatch(page);

    await page.getByRole("button", { name: "Add Operation" }).click();

    // Verify operation row appears
    await expect(page.getByText("Batch Operations (1)")).toBeVisible();

    // Verify Type, Key, Value fields
    await expect(page.locator("select").first()).toBeVisible();
    await expect(page.locator("input[placeholder='Key']").first()).toBeVisible();
    await expect(page.locator("input[placeholder='Value']").first()).toBeVisible();
  });

  test("add multiple operations and execute", async ({ page }) => {
    await navigateToBatch(page);

    // Add first PUT op
    await page.getByRole("button", { name: "Add Operation" }).click();
    await page.locator("input[placeholder='Key']").first().fill("key1");
    await page.locator("input[placeholder='Value']").first().fill("val1");

    // Add second PUT op
    await page.getByRole("button", { name: "Add Operation" }).click();
    const keyInputs = page.locator("input[placeholder='Key']");
    const valInputs = page.locator("input[placeholder='Value']");
    await keyInputs.nth(1).fill("key2");
    await valInputs.nth(1).fill("val2");

    await expect(page.getByText("Batch Operations (2)")).toBeVisible();

    // Execute
    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(page.getByText("Batch executed successfully")).toBeVisible({ timeout: 5_000 });

    // Results table should appear
    await expect(page.getByText("Batch Results")).toBeVisible();
  });

  test("execute without operations shows warning", async ({ page }) => {
    await navigateToBatch(page);

    await page.getByRole("button", { name: "Execute Batch" }).click();
    await expect(page.getByText("Add at least one operation")).toBeVisible({ timeout: 5_000 });
  });

  test("execute with empty key shows warning", async ({ page }) => {
    await navigateToBatch(page);

    await page.getByRole("button", { name: "Add Operation" }).click();
    // Leave key empty
    await page.getByRole("button", { name: "Execute Batch" }).click();

    await expect(page.getByText("All operations must have a key")).toBeVisible({ timeout: 5_000 });
  });

  test("remove operation from list", async ({ page }) => {
    await navigateToBatch(page);

    await page.getByRole("button", { name: "Add Operation" }).click();
    await expect(page.getByText("Batch Operations (1)")).toBeVisible();

    await page.getByRole("button", { name: "Remove" }).click();
    await expect(page.getByText("Batch Operations (0)")).toBeVisible();
    await expect(page.getByText("No operations defined")).toBeVisible();
  });

  test("reset clears all operations", async ({ page }) => {
    await navigateToBatch(page);

    // Add two ops
    await page.getByRole("button", { name: "Add Operation" }).click();
    await page.getByRole("button", { name: "Add Operation" }).click();
    await expect(page.getByText("Batch Operations (2)")).toBeVisible();

    // Reset
    await page.getByRole("button", { name: "Reset" }).click();
    await expect(page.getByText("Batch Operations (0)")).toBeVisible();
  });

  test("reorder operations with move buttons", async ({ page }) => {
    await navigateToBatch(page);

    // Add two ops with different keys
    await page.getByRole("button", { name: "Add Operation" }).click();
    await page.locator("input[placeholder='Key']").first().fill("first");

    await page.getByRole("button", { name: "Add Operation" }).click();
    await page.locator("input[placeholder='Key']").nth(1).fill("second");

    // Move second up
    const moveUpButtons = page.locator("[aria-label='Move up']");
    await moveUpButtons.nth(1).click();

    // Verify order swapped: "second" should now be first
    const keyInputs = page.locator("input[placeholder='Key']");
    await expect(keyInputs.first()).toHaveValue("second");
    await expect(keyInputs.nth(1)).toHaveValue("first");
  });

  test("switching operation type shows/hides value field", async ({ page }) => {
    await navigateToBatch(page);

    await page.getByRole("button", { name: "Add Operation" }).click();

    // PUT type shows Value field
    await expect(page.locator("input[placeholder='Value']")).toBeVisible();

    // Switch to DELETE type
    await page.locator("select").first().selectOption("2"); // OP_DELETE = 2

    // Value field should be hidden for DELETE
    await expect(page.locator("input[placeholder='Value']")).not.toBeVisible();

    // Switch to delete_range type
    await page.locator("select").first().selectOption("3"); // OP_RANGE = 3

    // Range End field should appear
    await expect(page.locator("input[placeholder='End key (exclusive)']")).toBeVisible();
  });
});
