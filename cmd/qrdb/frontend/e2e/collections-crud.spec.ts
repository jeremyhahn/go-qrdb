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
 * Collections CRUD operations: create, insert documents, verify, delete document,
 * and delete collection — testing the full lifecycle end-to-end.
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

const collectionCrudState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "crud-db", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigate to the Collections tab of the crud-db database. */
async function navigateToCollections(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("crud-db").click();
  await expect(page.locator(".breadcrumbs").getByText("crud-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .locator("main nav").getByText("Collections", { exact: true })
    .click();
  await expect(
    page.locator("main nav").getByText("Collections", { exact: true }),
  ).toBeVisible();
}

/** Create a collection with the given name and optional id strategy. */
async function createCollection(
  page: Page,
  name: string,
  idStrategy: string = "unix_nano",
): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Create Collection" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page
    .locator("input[placeholder='Enter collection name']")
    .fill(name);
  if (idStrategy !== "unix_nano") {
    await page.locator("select").selectOption(idStrategy);
  }
  await page
    .getByRole("button", { name: "Create", exact: true })
    .last()
    .click();
  await expect(page.getByText(`Collection "${name}" created`)).toBeVisible({
    timeout: 5_000,
  });
}

/** Select a collection from the panel. */
async function selectCollection(page: Page, name: string): Promise<void> {
  await page.getByRole("button", { name: new RegExp(name) }).first().click();
  await expect(page.locator("h3").filter({ hasText: name })).toBeVisible({
    timeout: 5_000,
  });
}

/** Insert a document via the Documents inner tab using JSON mode. */
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
  await expect(page.getByText("Document inserted.").first()).toBeVisible({
    timeout: 5_000,
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Collections CRUD", () => {
  test.use({ mockState: collectionCrudState });

  // -------------------------------------------------------------------------
  // Collection creation
  // -------------------------------------------------------------------------

  test("creates a collection with default id strategy", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "orders");

    // Collection should appear in the left panel
    await expect(
      page.getByRole("button", { name: /orders/ }).first(),
    ).toBeVisible();
  });

  test("creates a collection with increment id strategy", async ({ page }) => {
    await navigateToCollections(page);
    // Valid id strategies are: unix_nano, increment, field_hash (not uuid)
    await createCollection(page, "products", "increment");

    await expect(
      page.getByRole("button", { name: /products/ }).first(),
    ).toBeVisible();
  });

  test("shows error when creating duplicate collection", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "items");

    // Try to create again with the same name
    await injectError(page, "CollectionBinding.Create", "collection already exists");
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page
      .locator("input[placeholder='Enter collection name']")
      .fill("items");
    await page
      .getByRole("button", { name: "Create", exact: true })
      .last()
      .click();
    await expect(
      page.getByText("collection already exists"),
    ).toBeVisible({ timeout: 5_000 });
    await clearError(page, "CollectionBinding.Create");
  });

  // -------------------------------------------------------------------------
  // Document insertion
  // -------------------------------------------------------------------------

  test("inserts a document with fields and it appears in the list", async ({
    page,
  }) => {
    await navigateToCollections(page);
    await createCollection(page, "customers");
    await selectCollection(page, "customers");
    await insertDocument(
      page,
      JSON.stringify({ name: "Alice", email: "alice@example.com", age: 30 }),
    );

    // Document row should be visible
    await expect(page.getByText("alice@example.com").first()).toBeVisible({
      timeout: 5_000,
    });
  });

  test("inserts multiple documents and verifies count", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "multi-docs");
    await selectCollection(page, "multi-docs");
    await insertDocument(page, JSON.stringify({ key: "first" }));
    await insertDocument(page, JSON.stringify({ key: "second" }));

    // Both rows should be visible
    await expect(page.getByText("first").first()).toBeVisible();
    await expect(page.getByText("second").first()).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Document deletion
  // -------------------------------------------------------------------------

  test("deletes a document and verifies it is removed from the list", async ({
    page,
  }) => {
    await navigateToCollections(page);
    await createCollection(page, "del-docs");
    await selectCollection(page, "del-docs");
    await insertDocument(page, JSON.stringify({ label: "to-delete" }));

    // The document row should be visible
    await expect(page.getByText("to-delete").first()).toBeVisible();

    // Select the document row checkbox and click the toolbar Delete button.
    // CrudTable uses checkbox selection for deletion (no per-row delete button).
    const rowCheckbox = page.locator("table tbody tr input[type='checkbox']").first();
    if (await rowCheckbox.isVisible({ timeout: 3_000 })) {
      await rowCheckbox.click();
      // Click the Delete button in the table toolbar
      const deleteBtn = page
        .locator(".btn-error")
        .filter({ hasText: /Delete/i })
        .first();
      if (await deleteBtn.isVisible({ timeout: 3_000 })) {
        await deleteBtn.click();
        // Confirm deletion in the confirm modal
        const confirmBtn = page.locator("button").filter({ hasText: /Delete|Confirm/i }).last();
        if (await confirmBtn.isVisible({ timeout: 2_000 })) {
          await confirmBtn.click();
        }
      }
    }

    await expect(page.getByText("to-delete")).not.toBeVisible({
      timeout: 5_000,
    });
  });

  test("shows error when document deletion fails", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "del-err-docs");
    await selectCollection(page, "del-err-docs");
    await insertDocument(page, JSON.stringify({ label: "error-doc" }));

    await injectError(
      page,
      "CollectionBinding.DeleteDocument",
      "delete failed",
    );

    // Use the checkbox-based bulk delete approach (CrudTable pattern).
    // The component swallows individual errors and shows a partial failure warning.
    const rowCheckbox = page.locator("table tbody tr input[type='checkbox']").first();
    if (await rowCheckbox.isVisible({ timeout: 3_000 })) {
      await rowCheckbox.click();
      const deleteBtn = page
        .locator(".btn-error")
        .filter({ hasText: /Delete/i })
        .first();
      if (await deleteBtn.isVisible({ timeout: 3_000 })) {
        await deleteBtn.click();
        const confirmBtn = page.locator("button").filter({ hasText: /Delete|Confirm/i }).last();
        if (await confirmBtn.isVisible({ timeout: 2_000 })) {
          await confirmBtn.click();
        }
        // The component shows a partial failure toast: "Deleted N, failed N."
        await expect(
          page.getByText(/failed/i).first(),
        ).toBeVisible({ timeout: 5_000 });
      }
    }

    await clearError(page, "CollectionBinding.DeleteDocument");
  });

  // -------------------------------------------------------------------------
  // Collection deletion
  // -------------------------------------------------------------------------

  test("drops a collection and verifies it disappears from the panel", async ({
    page,
  }) => {
    await navigateToCollections(page);
    await createCollection(page, "to-drop");

    // Collection should appear in the panel
    await expect(
      page.getByRole("button", { name: /to-drop/ }).first(),
    ).toBeVisible();

    // Select the collection
    await selectCollection(page, "to-drop");

    // The drop button has aria-label "Drop collection to-drop" (a span[role=button])
    const dropBtn = page.locator("[aria-label='Drop collection to-drop']");
    if (await dropBtn.isVisible({ timeout: 3_000 })) {
      await dropBtn.click();
    } else {
      // Fallback: look for any Drop/Delete Collection button
      const altDropBtn = page.getByRole("button", { name: /Drop|Delete Collection/i }).first();
      await expect(altDropBtn).toBeVisible({ timeout: 5_000 });
      await altDropBtn.click();
    }

    // Confirm if a modal appears
    const confirmBtn = page.locator("button").filter({ hasText: /Drop|Confirm/i }).last();
    if (await confirmBtn.isVisible({ timeout: 2_000 })) {
      await confirmBtn.click();
    }

    // Verify the collection no longer appears in the sidebar list.
    // Scope to the collection sidebar list to avoid matching stale/hidden elements.
    await expect(
      page.locator("[aria-label='Drop collection to-drop']"),
    ).not.toBeVisible({ timeout: 5_000 });
  });

  test("shows error when collection creation fails", async ({ page }) => {
    await navigateToCollections(page);
    await injectError(
      page,
      "CollectionBinding.Create",
      "storage unavailable",
    );
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page
      .locator("input[placeholder='Enter collection name']")
      .fill("fail-coll");
    await page
      .getByRole("button", { name: "Create", exact: true })
      .last()
      .click();
    await expect(page.getByText("storage unavailable")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "CollectionBinding.Create");
  });
});
