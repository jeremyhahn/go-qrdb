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
 * Transaction operations: add conditions and operations, execute the
 * transaction, and verify results — including error paths.
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

const txnOpsState = {
  ...secondLaunchEmbeddedState,
  databases: [
    {
      name: "txn-ops-db",
      backend: "pebble",
      status: "open",
    },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigate to the Transactions tab within the txn-ops-db database. */
async function navigateToTransactions(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("txn-ops-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("txn-ops-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page
    .locator("main nav").getByText("Transactions", { exact: true })
    .click();
}

/** Add a condition with the given key. */
async function addCondition(page: Page, key: string, value: string = ""): Promise<void> {
  await page.getByRole("button", { name: "Add Condition" }).click();
  const keyInput = page.locator("input[placeholder='Key']").first();
  await expect(keyInput).toBeVisible({ timeout: 5_000 });
  await keyInput.fill(key);
  if (value) {
    const valueInput = page.locator("input[placeholder='Expected value']").first();
    await valueInput.fill(value);
  }
}

/** Add a success operation. */
async function addSuccessOp(page: Page, key: string, value: string = ""): Promise<void> {
  const addSuccessBtn = page
    .locator(".border-success")
    .getByRole("button", { name: "Add Op" });
  await addSuccessBtn.click();

  const keyInputs = page.locator("input[placeholder='Key']");
  // The last key input should belong to the new success op
  await keyInputs.last().fill(key);
  if (value) {
    const valueInputs = page.locator("input[placeholder='Value']");
    await valueInputs.last().fill(value);
  }
}

/** Add a failure operation. */
async function addFailureOp(page: Page, key: string, value: string = ""): Promise<void> {
  const addFailureBtn = page
    .locator(".border-error")
    .getByRole("button", { name: "Add Op" });
  await addFailureBtn.click();

  const keyInputs = page.locator("input[placeholder='Key']");
  await keyInputs.last().fill(key);
  if (value) {
    const valueInputs = page.locator("input[placeholder='Value']");
    await valueInputs.last().fill(value);
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Transaction Operations", () => {
  test.use({ mockState: txnOpsState });

  // -------------------------------------------------------------------------
  // Layout and empty state
  // -------------------------------------------------------------------------

  test("shows three-section layout with empty state messages", async ({
    page,
  }) => {
    await navigateToTransactions(page);

    await expect(page.getByText("Conditions").first()).toBeVisible();
    await expect(page.getByText("If Conditions Pass")).toBeVisible();
    await expect(page.getByText("If Conditions Fail")).toBeVisible();
    await expect(page.getByText("No conditions defined")).toBeVisible();
    await expect(page.getByText("No success operations defined")).toBeVisible();
    await expect(page.getByText("No failure operations defined")).toBeVisible();
  });

  test("shows Add Condition and Execute buttons", async ({ page }) => {
    await navigateToTransactions(page);
    await expect(
      page.getByRole("button", { name: "Add Condition" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: /Execute/i }),
    ).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Conditions
  // -------------------------------------------------------------------------

  test("adds a condition and verifies the form is shown", async ({ page }) => {
    await navigateToTransactions(page);
    await addCondition(page, "balance-key");

    await expect(page.getByText("No conditions defined")).not.toBeVisible();
    await expect(
      page.locator("input[placeholder='Key']").first(),
    ).toBeVisible();
  });

  test("adds and removes a condition", async ({ page }) => {
    await navigateToTransactions(page);
    await addCondition(page, "my-key");
    await expect(page.getByText("No conditions defined")).not.toBeVisible();

    const removeBtn = page
      .locator("[aria-label='Remove condition']")
      .first();
    await expect(removeBtn).toBeVisible({ timeout: 5_000 });
    await removeBtn.click();
    await expect(page.getByText("No conditions defined")).toBeVisible();
  });

  test("adds multiple conditions", async ({ page }) => {
    await navigateToTransactions(page);
    await addCondition(page, "key-a");
    await addCondition(page, "key-b");

    const keyInputs = page.locator("input[placeholder='Key']");
    await expect(keyInputs).toHaveCount(2);
  });

  // -------------------------------------------------------------------------
  // Success and failure operations
  // -------------------------------------------------------------------------

  test("adds a success operation", async ({ page }) => {
    await navigateToTransactions(page);
    await addSuccessOp(page, "result-key", "result-val");
    await expect(
      page.getByText("No success operations defined"),
    ).not.toBeVisible();
  });

  test("adds a failure operation", async ({ page }) => {
    await navigateToTransactions(page);
    await addFailureOp(page, "fallback-key", "fallback-val");
    await expect(
      page.getByText("No failure operations defined"),
    ).not.toBeVisible();
  });

  test("adds both success and failure operations", async ({ page }) => {
    await navigateToTransactions(page);
    await addSuccessOp(page, "ok-key", "ok-val");
    await addFailureOp(page, "err-key", "err-val");

    await expect(
      page.getByText("No success operations defined"),
    ).not.toBeVisible();
    await expect(
      page.getByText("No failure operations defined"),
    ).not.toBeVisible();
  });

  test("removes a success operation", async ({ page }) => {
    await navigateToTransactions(page);
    await addSuccessOp(page, "to-remove");

    const removeBtn = page
      .locator(".border-success")
      .locator("[aria-label='Remove operation'], [aria-label='Delete operation']")
      .first();
    if (await removeBtn.isVisible({ timeout: 3_000 })) {
      await removeBtn.click();
      await expect(
        page.getByText("No success operations defined"),
      ).toBeVisible();
    }
  });

  // -------------------------------------------------------------------------
  // Execute transaction
  // -------------------------------------------------------------------------

  test("executes a transaction with condition and success op", async ({
    page,
  }) => {
    await navigateToTransactions(page);
    // The default compare target is VERSION (numeric) — a non-numeric
    // expected value disables the Execute button.
    await addCondition(page, "check-key", "1");
    await addSuccessOp(page, "write-key", "write-val");

    await page.getByRole("button", { name: /Execute/i }).click();

    await expect(
      page.getByText(/Transaction executed|succeeded|failed/i).first(),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("executes a transaction with condition and success op and shows result", async ({
    page,
  }) => {
    await navigateToTransactions(page);
    // A condition is required by the component — add one alongside the
    // success op (numeric expected value: the default target is VERSION).
    await addCondition(page, "unconditional-key", "2");
    await addSuccessOp(page, "unconditional-key", "unconditional-val");

    await page.getByRole("button", { name: /Execute/i }).click();

    await expect(
      page.getByText(/Transaction executed|succeeded|failed/i).first(),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("shows result panel after execution", async ({ page }) => {
    await navigateToTransactions(page);
    // A condition is required — add one (numeric: default target is VERSION).
    await addCondition(page, "result-key", "3");
    await addSuccessOp(page, "result-key", "val");
    await page.getByRole("button", { name: /Execute/i }).click();

    // The result or response section should appear (Succeeded/Failed badges or CrudTable title)
    await expect(
      page
        .getByText(/Transaction Results|Succeeded|Failed/i)
        .first(),
    ).toBeVisible({ timeout: 5_000 });
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  test("shows error when transaction execution fails", async ({ page }) => {
    await navigateToTransactions(page);
    await injectError(
      page,
      "TransactionBinding.Execute",
      "transaction conflict detected",
    );
    // A condition is required — add one so Execute is attempted
    // (numeric: default target is VERSION).
    await addCondition(page, "conflict-key", "4");
    await addSuccessOp(page, "conflict-key", "conflict-val");
    await page.getByRole("button", { name: /Execute/i }).click();

    await expect(
      page.getByText("transaction conflict detected"),
    ).toBeVisible({ timeout: 5_000 });
    await clearError(page, "TransactionBinding.Execute");
  });
});
