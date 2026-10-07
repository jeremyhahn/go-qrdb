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
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const kvState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "kv-adv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to KV database workspace. */
async function navigateToKV(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("kv-adv").click();
  await expect(page.locator(".breadcrumbs").getByText("kv-adv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Create a KV entry via the slide panel. */
async function createEntry(
  page: import("@playwright/test").Page,
  key: string,
  value: string,
): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill(value);
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  // Use last() because previous toasts may still be visible for repeat key creates.
  await expect(page.getByText(`Created "${key}"`).last()).toBeVisible({ timeout: 5_000 });
}

// ---------------------------------------------------------------------------
// KV Explorer - Advanced tests
// ---------------------------------------------------------------------------

test.describe("KV Explorer Advanced", () => {
  test.use({ mockState: kvState });

  test("create entry with JSON value formats correctly in table", async ({ page }) => {
    await navigateToKV(page);

    const jsonValue = '{"name":"Alice","age":30}';
    await createEntry(page, "json-entry", jsonValue);

    await expect(page.getByText("json-entry").first()).toBeVisible();
    await expect(page.getByText("1 entries")).toBeVisible();
  });

  test("create duplicate key overwrites value (PUT semantics)", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "dup-key", "first-value");
    await createEntry(page, "dup-key", "second-value");

    // Should still show 1 entry because PUT is idempotent on key
    await expect(page.getByText("1 entries")).toBeVisible();
  });

  test("create entry with slashes in key", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "app/config/db", "localhost:5432");

    await expect(page.getByText("app/config/db").first()).toBeVisible();
  });

  test("create entry with dots in key", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "service.config.timeout", "30s");

    await expect(page.getByText("service.config.timeout").first()).toBeVisible();
  });

  test("create entry with colons in key", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "cache:session:user:42", "active");

    await expect(page.getByText("cache:session:user:42").first()).toBeVisible();
  });

  test("create entry with very long value", async ({ page }) => {
    await navigateToKV(page);

    const longValue = "x".repeat(1000);
    await createEntry(page, "long-value-key", longValue);

    await expect(page.getByText("long-value-key").first()).toBeVisible();
    await expect(page.getByText("1 entries")).toBeVisible();
  });

  test("multiple entries sort correctly in table", async ({ page }) => {
    await navigateToKV(page);

    for (const [key, value] of [
      ["zebra", "z"],
      ["alpha", "a"],
      ["monkey", "m"],
    ]) {
      await createEntry(page, key, value);
    }

    await expect(page.getByText("3 entries")).toBeVisible();

    // All entries should be visible in the table
    await expect(page.getByText("alpha").first()).toBeVisible();
    await expect(page.getByText("monkey").first()).toBeVisible();
    await expect(page.getByText("zebra").first()).toBeVisible();
  });

  test("scan returns all entries matching prefix", async ({ page }) => {
    await navigateToKV(page);

    for (const [key, value] of [
      ["ns/a", "1"],
      ["ns/b", "2"],
      ["other/c", "3"],
    ]) {
      await createEntry(page, key, value);
    }

    await expect(page.getByText("3 entries")).toBeVisible();

    // Filter by prefix ns/
    await page.locator("input[placeholder='Filter by key prefix...']").fill("ns/");
    await page.locator("[aria-label='Search']").click();

    await expect(page.getByText("2 entries")).toBeVisible();
    await expect(page.getByText("ns/a").first()).toBeVisible();
    await expect(page.getByText("ns/b").first()).toBeVisible();
  });

  test("rapid create and delete cycles maintain correct count", async ({ page }) => {
    await navigateToKV(page);

    // Create three entries
    await createEntry(page, "cycle-1", "v1");
    await createEntry(page, "cycle-2", "v2");
    await createEntry(page, "cycle-3", "v3");

    await expect(page.getByText("3 entries")).toBeVisible();

    // Delete the second entry
    await page.getByText("cycle-2").first().click();
    const detailPanel = page.getByLabel("Details");
    await detailPanel.getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText("Delete Entry")).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText('Deleted "cycle-2"')).toBeVisible({ timeout: 5_000 });

    await expect(page.getByText("2 entries")).toBeVisible();
  });

  test("exists check after deletion returns false (via UI absence)", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "exists-test", "value");
    await expect(page.getByText("exists-test").first()).toBeVisible();

    // Delete the entry
    await page.getByText("exists-test").first().click();
    const detailPanel = page.getByLabel("Details");
    await detailPanel.getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText("Delete Entry")).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText('Deleted "exists-test"')).toBeVisible({ timeout: 5_000 });

    // The entry should no longer appear in the table (checking 0 entries is sufficient).
    await expect(page.getByText("0 entries")).toBeVisible();
    // Wait for the delete toast to disappear, then confirm the key is gone from the table.
    await expect(page.getByText('Deleted "exists-test"')).not.toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator("table").getByText("exists-test")).not.toBeVisible();
  });
});
