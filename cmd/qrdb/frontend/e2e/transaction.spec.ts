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

const txnState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "txn-db", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to Transactions tab. */
async function navigateToTransactions(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("txn-db").click();
  await expect(page.locator(".breadcrumbs").getByText("txn-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Transactions", { exact: true }).click();
}

test.describe("Transaction View", () => {
  test.use({ mockState: txnState });

  test("shows three-step layout with numbered sections", async ({ page }) => {
    await navigateToTransactions(page);

    // Verify step headers
    await expect(page.getByText("Conditions").first()).toBeVisible();
    await expect(page.getByText("If Conditions Pass")).toBeVisible();
    await expect(page.getByText("If Conditions Fail")).toBeVisible();

    // Step 3 is optional
    await expect(page.getByText("Optional")).toBeVisible();

    // Empty state messages
    await expect(page.getByText("No conditions defined")).toBeVisible();
    await expect(page.getByText("No success operations defined")).toBeVisible();
    await expect(page.getByText("No failure operations defined")).toBeVisible();
  });

  test("add condition shows form fields with friendly labels", async ({ page }) => {
    await navigateToTransactions(page);

    await page.getByRole("button", { name: "Add Condition" }).click();

    // Verify friendly field labels
    await expect(page.getByText("Key").first()).toBeVisible();
    await expect(page.getByText("Property")).toBeVisible();
    await expect(page.getByText("Comparison")).toBeVisible();
    await expect(page.getByText("Expected Value")).toBeVisible();

    // Verify inputs
    await expect(page.locator("input[placeholder='Key']").first()).toBeVisible();
    await expect(page.locator("input[placeholder='Expected value']")).toBeVisible();

    // Verify dropdowns have correct options
    const propertySelect = page.locator("select").first();
    await expect(propertySelect).toBeVisible();
  });

  test("add and remove condition", async ({ page }) => {
    await navigateToTransactions(page);

    // Add condition
    await page.getByRole("button", { name: "Add Condition" }).click();
    await expect(page.locator("input[placeholder='Key']").first()).toBeVisible();

    // Remove condition
    await page.locator("[aria-label='Remove condition']").click();
    await expect(page.getByText("No conditions defined")).toBeVisible();
  });

  test("add success and failure operations", async ({ page }) => {
    await navigateToTransactions(page);

    // Add success op
    const addSuccessBtn = page.locator(".border-success").getByRole("button", { name: "Add Op" });
    await addSuccessBtn.click();
    await expect(page.getByText("No success operations defined")).not.toBeVisible();

    // Add failure op
    const addFailureBtn = page.locator(".border-error").getByRole("button", { name: "Add Op" });
    await addFailureBtn.click();
    await expect(page.getByText("No failure operations defined")).not.toBeVisible();
  });

  test("remove success operation", async ({ page }) => {
    await navigateToTransactions(page);

    // Add success op
    const addSuccessBtn = page.locator(".border-success").getByRole("button", { name: "Add Op" });
    await addSuccessBtn.click();
    await expect(page.getByText("No success operations defined")).not.toBeVisible();

    // Remove it
    await page.locator("[aria-label='Remove success operation']").click();
    await expect(page.getByText("No success operations defined")).toBeVisible();
  });

  test("remove failure operation", async ({ page }) => {
    await navigateToTransactions(page);

    // Add failure op
    const addFailureBtn = page.locator(".border-error").getByRole("button", { name: "Add Op" });
    await addFailureBtn.click();
    await expect(page.getByText("No failure operations defined")).not.toBeVisible();

    // Remove it
    await page.locator("[aria-label='Remove failure operation']").click();
    await expect(page.getByText("No failure operations defined")).toBeVisible();
  });

  test("execute without conditions shows warning", async ({ page }) => {
    await navigateToTransactions(page);

    await page.getByRole("button", { name: "Execute Transaction" }).click();
    await expect(page.getByText("At least one compare is required")).toBeVisible({ timeout: 5_000 });
  });

  test("execute with conditions but no ops shows warning", async ({ page }) => {
    await navigateToTransactions(page);

    // Add a condition
    await page.getByRole("button", { name: "Add Condition" }).click();
    await page.locator("input[placeholder='Key']").first().fill("mykey");
    await page.locator("input[placeholder='Expected value']").fill("1");

    // Execute without any ops
    await page.getByRole("button", { name: "Execute Transaction" }).click();
    await expect(page.getByText("At least one success or failure operation is required")).toBeVisible({ timeout: 5_000 });
  });

  test("execute transaction with conditions and success ops", async ({ page }) => {
    await navigateToTransactions(page);

    // Add condition
    await page.getByRole("button", { name: "Add Condition" }).click();
    await page.locator("input[placeholder='Key']").first().fill("counter");
    await page.locator("input[placeholder='Expected value']").fill("1");

    // Add success op
    const addSuccessBtn = page.locator(".border-success").getByRole("button", { name: "Add Op" });
    await addSuccessBtn.click();
    const successSection = page.locator(".border-success");
    await successSection.locator("input[placeholder='Key']").fill("counter");
    await successSection.locator("input[placeholder='Value']").fill("2");

    // Execute
    await page.getByRole("button", { name: "Execute Transaction" }).click();

    // Verify success result
    await expect(page.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Transaction Results")).toBeVisible();
  });

  test("reset clears all form state", async ({ page }) => {
    await navigateToTransactions(page);

    // Add condition, success op, and failure op
    await page.getByRole("button", { name: "Add Condition" }).click();
    const addSuccessBtn = page.locator(".border-success").getByRole("button", { name: "Add Op" });
    await addSuccessBtn.click();
    const addFailureBtn = page.locator(".border-error").getByRole("button", { name: "Add Op" });
    await addFailureBtn.click();

    // Click Reset
    await page.getByRole("button", { name: "Reset" }).click();

    // Verify all empty states
    await expect(page.getByText("No conditions defined")).toBeVisible();
    await expect(page.getByText("No success operations defined")).toBeVisible();
    await expect(page.getByText("No failure operations defined")).toBeVisible();
  });

  test("success op type switching shows/hides value field", async ({ page }) => {
    await navigateToTransactions(page);

    // Add success op (defaults to PUT with Value field)
    const addSuccessBtn = page.locator(".border-success").getByRole("button", { name: "Add Op" });
    await addSuccessBtn.click();
    const successSection = page.locator(".border-success");
    await expect(successSection.locator("input[placeholder='Value']")).toBeVisible();

    // Switch to DELETE type
    await successSection.locator("select").first().selectOption("2"); // OP_DELETE = 2

    // Value field should be hidden for DELETE
    await expect(successSection.locator("input[placeholder='Value']")).not.toBeVisible();
  });

  test("error on execute shows error toast", async ({ page }) => {
    await navigateToTransactions(page);

    // Inject error
    await injectError(page, "TransactionBinding.Execute", "transaction conflict");

    // Add condition and success op. The default compare target is VERSION
    // (numeric) — a non-numeric expected value disables the Execute button.
    await page.getByRole("button", { name: "Add Condition" }).click();
    await page.locator("input[placeholder='Key']").first().fill("k1");
    await page.locator("input[placeholder='Expected value']").fill("1");
    const addSuccessBtn = page.locator(".border-success").getByRole("button", { name: "Add Op" });
    await addSuccessBtn.click();
    const successSection = page.locator(".border-success");
    await successSection.locator("input[placeholder='Key']").fill("k1");
    await successSection.locator("input[placeholder='Value']").fill("v2");

    // Execute
    await page.getByRole("button", { name: "Execute Transaction" }).click();
    await expect(page.getByText("transaction conflict")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "TransactionBinding.Execute");
  });
});
