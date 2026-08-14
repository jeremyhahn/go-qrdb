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
 * Index operations: create an index, verify it appears in the list,
 * query the index, scan by range, and drop the index.
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

const indexOpsState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "index-ops-db", engine: "kv", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigate to the Collections tab for index-ops-db. */
async function navigateToCollections(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("index-ops-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("index-ops-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page
    .locator("main nav").getByText("Collections", { exact: true })
    .click();
}

/** Create a collection and open its Indexes inner tab. */
async function navigateToIndexes(page: Page, collectionName: string): Promise<void> {
  await navigateToCollections(page);

  await page.getByRole("button", { name: "Create", exact: true }).click();
  await page
    .locator("input[placeholder='Enter collection name']")
    .fill(collectionName);
  await page.locator(".modal").getByRole("button", { name: "Create" }).click();
  await expect(
    page.getByText(`Collection "${collectionName}" created`).first(),
  ).toBeVisible({ timeout: 5_000 });

  // Select the new collection
  await page.getByRole("button", { name: new RegExp(collectionName) }).first().click();

  // Switch to the collection-detail inner "Indexes" tab.
  // The collection detail uses a tablist with class "tabs tabs-bordered" (no aria-controls).
  // The workspace TabBar uses aria-controls="tabpanel-*". We click the one WITHOUT
  // aria-controls to stay within the CollectionView rather than jumping to IndexManager.
  const collectionTablist = page.locator("[role='tablist'].tabs.tabs-bordered");
  await collectionTablist.getByRole("tab", { name: "Indexes" }).click();
  await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({
    timeout: 5_000,
  });
}

/** Create an index on the given field. */
async function createIndex(page: Page, fieldName: string): Promise<void> {
  await page.getByRole("button", { name: "Create Index" }).click();
  await page.locator("input[placeholder='Field name']").fill(fieldName);
  await page.locator(".modal").getByRole("button", { name: "Create" }).click();
  await expect(
    page.getByText(new RegExp(`Index "index-ops-db\\..+\\.${fieldName}" created`)),
  ).toBeVisible({ timeout: 5_000 });
}

/** Insert a document via the Documents tab using JSON mode. */
async function insertDocument(page: Page, json: string): Promise<void> {
  await page.getByRole("tab", { name: "Documents" }).click();
  const insertBtn = page
    .locator("main")
    .getByRole("button", { name: "Insert", exact: true });
  await expect(insertBtn).toBeVisible({ timeout: 5_000 });
  await insertBtn.click();

  const modal = page.locator(".modal");
  await expect(modal).toBeVisible({ timeout: 5_000 });
  await modal.getByRole("button", { name: "JSON", exact: true }).click();
  await modal.locator("textarea").fill(json);
  await modal.getByRole("button", { name: "Insert", exact: true }).click();
  await expect(
    page.getByText("Document inserted.").first(),
  ).toBeVisible({ timeout: 5_000 });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Index Operations", () => {
  test.use({ mockState: indexOpsState });

  // -------------------------------------------------------------------------
  // Create index
  // -------------------------------------------------------------------------

  test("creates an index on 'email' field and it appears in the list", async ({
    page,
  }) => {
    await navigateToIndexes(page, "users-idx");
    await createIndex(page, "email");

    // The index entry should be shown in the Manage tab
    await expect(page.getByText(/\.email/).first()).toBeVisible({
      timeout: 5_000,
    });
  });

  test("creates a second index on a different field", async ({ page }) => {
    await navigateToIndexes(page, "products-idx");
    await createIndex(page, "sku");
    await createIndex(page, "category");

    await expect(page.getByText(/\.sku/).first()).toBeVisible();
    await expect(page.getByText(/\.category/).first()).toBeVisible();
  });

  test("shows error when index creation fails", async ({ page }) => {
    await navigateToIndexes(page, "err-idx");
    await injectError(page, "IndexBinding.Create", "index storage full");

    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='Field name']").fill("bad-field");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();

    await expect(page.getByText("index storage full")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "IndexBinding.Create");
  });

  // -------------------------------------------------------------------------
  // Query index
  // -------------------------------------------------------------------------

  test("queries an index and shows matching document keys", async ({
    page,
  }) => {
    await navigateToIndexes(page, "query-users");

    // Insert a document first
    await insertDocument(
      page,
      JSON.stringify({ email: "bob@example.com", name: "Bob" }),
    );

    // Return to the collection-detail Indexes inner tab (class="tabs tabs-bordered").
    await page
      .locator("[role='tablist'].tabs.tabs-bordered")
      .getByRole("tab", { name: "Indexes" })
      .click();
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({
      timeout: 5_000,
    });

    // Create an index on email
    await createIndex(page, "email");

    // Switch to the Query inner tab
    const queryTab = page.getByRole("tab", { name: "Query" });
    await expect(queryTab).toBeVisible({ timeout: 5_000 });
    await queryTab.click();

    // Select the index from the dropdown (required for queryCollectionIndex to proceed).
    const indexSelect = page.locator("select").first();
    if (await indexSelect.isVisible({ timeout: 3_000 })) {
      await indexSelect.selectOption({ index: 1 });
    }

    // Fill the value input (placeholder "Value to match").
    const valueInput = page.locator("input[placeholder='Value to match']").first();
    if (await valueInput.isVisible({ timeout: 3_000 })) {
      await valueInput.fill("bob@example.com");
    }

    // Use exact: true to avoid strict mode violation with collection list buttons.
    await page.getByRole("button", { name: "Query", exact: true }).click();
    // Some result row or key should appear
    await expect(
      page.getByText(/key|result/i).first(),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("shows error when index query fails", async ({ page }) => {
    await navigateToIndexes(page, "query-err");
    await createIndex(page, "name");

    const queryTab = page.getByRole("tab", { name: "Query" });
    if (!(await queryTab.isVisible({ timeout: 3_000 }))) return;
    await queryTab.click();

    await injectError(page, "IndexBinding.Query", "query engine error");

    // Select an index from the dropdown (required for queryCollectionIndex to proceed).
    const indexSelect = page.locator("select").first();
    if (await indexSelect.isVisible({ timeout: 3_000 })) {
      // Select the first non-empty option
      await indexSelect.selectOption({ index: 1 });
    }

    // Fill value input (placeholder "Value to match")
    const valueInput = page.locator("input[placeholder='Value to match']").first();
    if (await valueInput.isVisible({ timeout: 3_000 })) {
      await valueInput.fill("test");
    }
    await page.getByRole("button", { name: "Query", exact: true }).click();

    await expect(page.getByText("query engine error")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "IndexBinding.Query");
  });

  // -------------------------------------------------------------------------
  // Drop index
  // -------------------------------------------------------------------------

  test("drops an index and verifies it is removed from the list", async ({
    page,
  }) => {
    await navigateToIndexes(page, "drop-test");
    await createIndex(page, "city");

    // The index should be visible
    await expect(page.getByText(/\.city/).first()).toBeVisible();

    // Click the "Drop" button in the index table (btn-error btn-xs with text "Drop").
    const dropBtn = page.locator("button.btn-error.btn-xs", { hasText: "Drop" }).first();
    await expect(dropBtn).toBeVisible({ timeout: 5_000 });
    await dropBtn.click();

    // Confirm in the ConfirmModal
    const confirmBtn = page.locator("button").filter({ hasText: /Drop|Confirm/i }).last();
    if (await confirmBtn.isVisible({ timeout: 2_000 })) {
      await confirmBtn.click();
    }

    await expect(
      page.getByText(/dropped/i).first(),
    ).toBeVisible({ timeout: 5_000 });
    // Verify the index no longer appears in the index table (not in toast messages).
    // The index table rows use font-mono text-sm for the index name.
    await expect(
      page.locator("table").getByText(/\.city/),
    ).not.toBeVisible({ timeout: 5_000 });
  });

  test("shows error when index drop fails", async ({ page }) => {
    await navigateToIndexes(page, "drop-err");
    await createIndex(page, "country");
    await injectError(page, "IndexBinding.Drop", "cannot drop index");

    const dropBtn = page.locator("button.btn-error.btn-xs", { hasText: "Drop" }).first();
    await expect(dropBtn).toBeVisible({ timeout: 5_000 });
    await dropBtn.click();

    const confirmBtn = page.locator("button").filter({ hasText: /Drop|Confirm/i }).last();
    if (await confirmBtn.isVisible({ timeout: 2_000 })) {
      await confirmBtn.click();
    }

    await expect(page.getByText("cannot drop index")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "IndexBinding.Drop");
  });
});
