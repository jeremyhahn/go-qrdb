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

const collectionState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "coll-adv", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the Collections tab. */
async function navigateToCollections(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("coll-adv").click();
  await expect(page.locator(".breadcrumbs").getByText("coll-adv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  // DbNavRail: use page.locator("main nav") for tool navigation
  await page.locator("main nav").getByText("Collections", { exact: true }).click();
  await expect(page.locator("main nav").getByText("Collections", { exact: true })).toBeVisible();
}

/** Create a collection via the UI. */
async function createCollection(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Create Collection" })).toBeVisible();
  await page.locator("input[placeholder='Enter collection name']").fill(name);
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText(`Collection "${name}" created`)).toBeVisible({ timeout: 5_000 });
}

/** Select a collection from the left panel by name. */
async function selectCollection(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.getByRole("button", { name: new RegExp(name) }).first().click();
  await expect(page.locator("h3").filter({ hasText: name })).toBeVisible({ timeout: 5_000 });
}

/** Insert a document via JSON mode. */
async function insertDocumentJson(
  page: import("@playwright/test").Page,
  json: string,
): Promise<void> {
  await page.getByRole("button", { name: "Insert", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Insert Document" })).toBeVisible();
  await page.locator(".join").getByRole("button", { name: "JSON" }).click();
  await page.locator("textarea").fill(json);
  await page.getByRole("button", { name: "Insert", exact: true }).last().click();
  await expect(page.getByText("Document inserted").first()).toBeVisible({ timeout: 5_000 });
  await expect(page.getByText("Document inserted")).toHaveCount(0, { timeout: 10_000 });
}

// ---------------------------------------------------------------------------
// Collection Advanced - Document CRUD
// ---------------------------------------------------------------------------

test.describe("Collection Advanced", () => {
  test.use({ mockState: collectionState });

  test("edit existing document changes its fields", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "edit-coll");
    await selectCollection(page, "edit-coll");

    await insertDocumentJson(page, '{"name": "Alice", "score": 10}');

    // Click the document row to open the detail slide panel.
    await page.locator("tr").filter({ hasText: "Alice" }).click();

    // Wait for the slide panel to appear before interacting.
    await expect(page.locator('[role="dialog"]').getByText("Document Detail")).toBeVisible({ timeout: 5_000 });

    // Click the Edit button scoped to the slide panel dialog.
    await page.locator('[role="dialog"]').getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Edit Document" })).toBeVisible();

    // Switch to JSON mode so the textarea becomes visible, then update the document.
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();
    const editTextarea = page.locator("textarea");
    await editTextarea.fill('{"name": "Bob", "score": 99}');

    await page.getByRole("button", { name: "Save" }).click();

    // Toast message includes the document ID; match with a partial text check.
    await expect(page.getByText(/Document .+ updated/)).toBeVisible({ timeout: 5_000 });
  });

  test("delete document from detail panel removes it from table", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "del-detail-coll");
    await selectCollection(page, "del-detail-coll");

    await insertDocumentJson(page, '{"item": "to-remove"}');

    // Click the document row to open the detail slide panel.
    await page.locator("tr").filter({ hasText: "to-remove" }).click();

    // Wait for the slide panel to appear before clicking Delete.
    await expect(page.locator('[role="dialog"]').getByText("Document Detail")).toBeVisible({ timeout: 5_000 });

    // Click Delete scoped to the slide panel dialog.
    await page.locator('[role="dialog"]').getByRole("button", { name: "Delete", exact: true }).click();

    // A confirm modal appears; click Delete again to confirm.
    await expect(page.locator(".modal-box")).toBeVisible({ timeout: 5_000 });
    await page.locator(".modal-box").getByRole("button", { name: "Delete" }).click();

    // Toast message includes the document ID; match with a partial text check.
    await expect(page.getByText(/Document .+ deleted/)).toBeVisible({ timeout: 5_000 });

    // Document should no longer appear in the table.
    await expect(page.getByText("0 documents")).toBeVisible();
  });

  test("collection with multiple documents shows correct count", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "multi-doc-coll");
    await selectCollection(page, "multi-doc-coll");

    await insertDocumentJson(page, '{"x": 1}');
    await insertDocumentJson(page, '{"x": 2}');
    await insertDocumentJson(page, '{"x": 3}');

    await expect(page.getByText("3 documents")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("document with nested JSON displays correctly", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "nested-coll");
    await selectCollection(page, "nested-coll");

    await insertDocumentJson(page, '{"address": {"city": "Springfield", "zip": "12345"}}');

    await expect(page.getByText("1 documents")).toBeVisible();
  });

  test("empty document fields handled gracefully", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "empty-field-coll");
    await selectCollection(page, "empty-field-coll");

    // A document with a null field value
    await insertDocumentJson(page, '{"name": "test", "optional": null}');

    await expect(page.getByText("1 documents")).toBeVisible();
  });

  test("collection schema updates when new fields are inserted", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "schema-coll");
    await selectCollection(page, "schema-coll");

    // Insert first document with one field set
    await insertDocumentJson(page, '{"name": "Alice"}');

    // Insert second document with a new field
    await insertDocumentJson(page, '{"name": "Bob", "age": 30}');

    // Both documents are in the collection
    await expect(page.getByText("2 documents")).toBeVisible();
  });

  test("error on document update shows error toast", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "update-err-coll");
    await selectCollection(page, "update-err-coll");

    await insertDocumentJson(page, '{"name": "Error Subject"}');

    // Click the document row to open the detail slide panel.
    await page.locator("tr").filter({ hasText: "Error Subject" }).click();

    // Wait for the slide panel to appear.
    await expect(page.locator('[role="dialog"]').getByText("Document Detail")).toBeVisible({ timeout: 5_000 });

    // Inject error before clicking Edit.
    await injectError(page, "CollectionBinding.UpdateDocument", "update conflict");

    // Click Edit scoped to the slide panel dialog.
    await page.locator('[role="dialog"]').getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Edit Document" })).toBeVisible();

    // Switch to JSON mode so the textarea is visible.
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();
    const editTextarea = page.locator("textarea");
    await editTextarea.fill('{"name": "Updated"}');
    await page.getByRole("button", { name: "Save" }).click();

    await expect(page.getByText("update conflict")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "CollectionBinding.UpdateDocument");
  });

  test("error on document delete shows error toast", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "delete-err-coll");
    await selectCollection(page, "delete-err-coll");

    await insertDocumentJson(page, '{"name": "Delete Me"}');

    // Click the document row to open the detail slide panel.
    await page.locator("tr").filter({ hasText: "Delete Me" }).click();

    // Wait for the slide panel to appear.
    await expect(page.locator('[role="dialog"]').getByText("Document Detail")).toBeVisible({ timeout: 5_000 });

    // Inject error before attempting delete.
    await injectError(page, "CollectionBinding.DeleteDocument", "document locked");

    // Click Delete scoped to the slide panel dialog.
    await page.locator('[role="dialog"]').getByRole("button", { name: "Delete", exact: true }).click();

    // A confirm modal appears; click Delete again to confirm.
    await expect(page.locator(".modal-box")).toBeVisible({ timeout: 5_000 });
    await page.locator(".modal-box").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText("document locked")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "CollectionBinding.DeleteDocument");
  });

  test("update document field type from string to number", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "type-change-coll");
    await selectCollection(page, "type-change-coll");

    // Insert with a string value for "count".
    await insertDocumentJson(page, '{"count": "five"}');

    // Click the document row to open the detail slide panel.
    await page.locator("tr").filter({ hasText: "five" }).click();

    // Wait for the slide panel to appear.
    await expect(page.locator('[role="dialog"]').getByText("Document Detail")).toBeVisible({ timeout: 5_000 });

    // Click Edit scoped to the slide panel dialog.
    await page.locator('[role="dialog"]').getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Edit Document" })).toBeVisible();

    // Switch to JSON mode so the textarea is visible.
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();
    const editTextarea = page.locator("textarea");
    await editTextarea.fill('{"count": 5}');
    await page.getByRole("button", { name: "Save" }).click();

    // Toast message includes the document ID; match with a partial text check.
    await expect(page.getByText(/Document .+ updated/)).toBeVisible({ timeout: 5_000 });
  });
});
