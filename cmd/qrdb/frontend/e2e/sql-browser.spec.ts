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

const sqlKvState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "sql-kv", backend: "pebble", status: "open", protocol: { mysql: true, postgresql: false } },
  ],
};

/** Navigate to the SQL tab of the given database. */
async function navigateToSQL(
  page: import("@playwright/test").Page,
  dbName: string,
): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText(dbName).click();
  await expect(page.locator(".breadcrumbs").getByText(dbName, { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("nav").getByText("SQL Browser").click();
}

/** Execute the current query by clicking the Execute button. */
async function clickExecute(page: import("@playwright/test").Page): Promise<void> {
  await page.getByRole("button", { name: "Execute" }).click();
}

// ---------------------------------------------------------------------------
// SQL Browser - default query
// ---------------------------------------------------------------------------

test.describe("SQL Browser - default query", () => {
  test.use({ mockState: sqlKvState });

  test("shows SQL Browser heading with database name badge", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await expect(page.locator("main").getByRole("heading", { name: "SQL Browser" })).toBeVisible();
    await expect(page.locator(".badge.font-mono", { hasText: "sql-kv" })).toBeVisible();
  });

  test("shows the default query text", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    const textarea = page.locator("textarea[placeholder='Enter SQL query...']");
    await expect(textarea).toBeVisible();
    await expect(textarea).toHaveValue("SELECT * FROM kv");
  });

  test("execute button runs query and displays results table", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await clickExecute(page);

    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });
  });

  test("results table shows correct column headers for kv query", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    // Put an entry so the query returns rows.
    await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as Record<string, Record<string, Record<string, (...args: unknown[]) => Promise<unknown>>>>;
      return go.main.KVBinding.Put("test-key", "test-value");
    });

    await clickExecute(page);

    await expect(page.getByRole("columnheader", { name: "key" })).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("columnheader", { name: "value" })).toBeVisible();
  });

  test("results table shows correct row data", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    // Seed a known entry.
    await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as Record<string, Record<string, Record<string, (...args: unknown[]) => Promise<unknown>>>>;
      return go.main.KVBinding.Put("row-key", "row-value");
    });

    await clickExecute(page);

    await expect(page.getByText("row-key").first()).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("row-value").first()).toBeVisible();
  });

  test("empty results shows row count of 0", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    // Ensure the KV store is empty by using SELECT 1 which returns 1 row,
    // then use a fresh query against an empty store.
    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT * FROM kv");
    await clickExecute(page);

    await expect(page.getByText("0 rows")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Query returned no rows.")).toBeVisible();
  });

  test("query error displays error alert", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await page.locator("textarea[placeholder='Enter SQL query...']").fill("INVALID SQL @@");
    await clickExecute(page);

    await expect(page.locator(".alert.alert-error").first()).toBeVisible({ timeout: 5_000 });
  });

  test("clear button resets results", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT 1");
    await clickExecute(page);

    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });

    await page.getByRole("button", { name: "Clear" }).click();

    await expect(page.getByText("Results")).not.toBeVisible();
    await expect(page.locator(".alert.alert-error")).not.toBeVisible();
  });

  test("query history appears after executing a query", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT 1");
    await clickExecute(page);

    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });

    // Expand the history collapse (DaisyUI collapse uses a hidden checkbox; force click)
    await page.locator(".collapse").getByText(/Query History/).click({ force: true });

    await expect(page.getByText("SELECT 1").first()).toBeVisible({ timeout: 5_000 });
  });

  test("clicking history item loads it back into editor", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    // Execute a query to add it to history
    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT 1");
    await clickExecute(page);
    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });

    // Change the current query
    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT * FROM kv");

    // Expand history and click the old item (DaisyUI collapse uses hidden checkbox; force click)
    await page.locator(".collapse").getByText(/Query History/).click({ force: true });
    await page.getByRole("button", { name: "SELECT 1", exact: true }).click();

    // Verify the editor now shows the history item
    await expect(page.locator("textarea[placeholder='Enter SQL query...']")).toHaveValue("SELECT 1");
  });

  test("execute button is disabled when query is empty", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await page.locator("textarea[placeholder='Enter SQL query...']").fill("");
    await expect(page.getByRole("button", { name: "Execute" })).toBeDisabled();
  });

  test("Ctrl+Enter keyboard shortcut executes query", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    const textarea = page.locator("textarea[placeholder='Enter SQL query...']");
    await textarea.fill("SELECT 1");
    await textarea.press("Control+Enter");

    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });
  });

  test("error on execute shows error toast", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await injectError(page, "SQLBinding.Execute", "connection refused");

    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT 1");
    await clickExecute(page);

    await expect(page.getByText("connection refused").first()).toBeVisible({ timeout: 5_000 });

    await clearError(page, "SQLBinding.Execute");
  });

  test("multiple queries accumulate in history without duplicates", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    // Execute two distinct queries
    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT 1");
    await clickExecute(page);
    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });

    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT * FROM kv");
    await clickExecute(page);
    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });

    // Expand history; both queries should be present (DaisyUI collapse uses hidden checkbox; force click)
    await page.locator(".collapse").getByText(/Query History \(2\)/).click({ force: true });
    await expect(page.getByRole("button", { name: "SELECT 1", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "SELECT * FROM kv", exact: true })).toBeVisible();
  });

  test("executing the same query twice does not duplicate history entry", async ({ page }) => {
    await navigateToSQL(page, "sql-kv");

    await page.locator("textarea[placeholder='Enter SQL query...']").fill("SELECT 1");
    await clickExecute(page);
    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });

    await clickExecute(page);
    await expect(page.getByText("Results")).toBeVisible({ timeout: 5_000 });

    // History should still show exactly 1 entry (DaisyUI collapse uses hidden checkbox; force click)
    await page.locator(".collapse").getByText(/Query History \(1\)/).click({ force: true });
    const historyItems = page.getByRole("button", { name: "SELECT 1", exact: true });
    await expect(historyItems).toHaveCount(1);
  });
});
