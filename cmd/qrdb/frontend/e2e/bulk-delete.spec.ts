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
 * Bulk delete (multi-select) coverage for the three CrudTable-backed views:
 *
 *   - KVExplorer        (handleBulkDelete)
 *   - CollectionView    (handleBulkDeleteDocuments)
 *   - VectorView        (handleBulkDeleteVectors)
 *
 * Each suite asserts: selecting rows enables the toolbar Delete button with a
 * count badge, the CrudTable "Confirm Delete" modal appears, confirming
 * deletes exactly the selected rows (table narrows, count updates), and
 * cancelling leaves the data intact.
 *
 * Selection semantics: row checkboxes accumulate. Each checkbox click toggles
 * that row's membership without clearing other selected rows, so a partial
 * multi-selection is built by checking each desired row directly.
 *
 * NOTE: table-content assertions are scoped to page.locator("table") because
 * success toasts contain the key/id text and unscoped getByText would match
 * the toast (see the comment in kv-explorer-regression.spec.ts).
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// KV Explorer bulk delete
// ---------------------------------------------------------------------------

const bulkKvState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "bulk-kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the bulk-kv KV database workspace. */
async function navigateToKV(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("bulk-kv").click();
  await expect(
    page.locator(".breadcrumbs").getByText("bulk-kv", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Create a KV entry via the create panel and wait for the success toast. */
async function createEntry(page: Page, key: string, value: string): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill(value);
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText(`Created "${key}"`).last()).toBeVisible({ timeout: 5_000 });
}

test.describe("KV Explorer bulk delete", () => {
  test.use({ mockState: bulkKvState });

  test("selecting rows enables Delete with count and confirming deletes only the selected entries", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "bulk/a", "val-a");
    await createEntry(page, "bulk/b", "val-b");
    await createEntry(page, "bulk/c", "val-c");
    await expect(page.getByText("3 entries")).toBeVisible();

    // Let the creation toasts clear so they cannot intercept clicks or
    // pollute text assertions.
    await expect(page.getByText('Created "bulk/c"')).not.toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Toolbar Delete button is disabled with no selection.
    const bulkDeleteIdle = page.getByRole("button", { name: "Delete", exact: true });
    await expect(bulkDeleteIdle).toBeDisabled();

    // A single checkbox selection surfaces the count badge.
    await page.getByLabel("Select row 1", { exact: true }).check();
    await expect(page.getByRole("button", { name: "Delete 1" })).toBeEnabled();

    // Hand-pick a second row via its checkbox: checkboxes accumulate, so
    // row 1 stays selected. Entries are sorted by key, so rows 1 and 2 are
    // bulk/a and bulk/b.
    await page.getByLabel("Select row 2", { exact: true }).check();
    await expect(page.getByLabel("Select row 1", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Select row 2", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Select row 3", { exact: true })).not.toBeChecked();

    // The toolbar button now carries the selection count badge.
    const bulkDelete = page.getByRole("button", { name: "Delete 2" });
    await expect(bulkDelete).toBeEnabled();
    await bulkDelete.click();

    // Confirm modal appears with the selection count.
    const modal = page.locator(".modal");
    await expect(modal.getByText("Confirm Delete")).toBeVisible();
    await expect(
      modal.getByText("Are you sure you want to delete 2 selected items?", { exact: false }),
    ).toBeVisible();
    await modal.getByRole("button", { name: "Delete" }).click();

    // Exactly the selected entries are deleted.
    await expect(page.getByText("Deleted 2 entries")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("1 entries")).toBeVisible();

    const table = page.locator("table");
    await expect(table.getByText("bulk/c").first()).toBeVisible();
    await expect(table.getByText("bulk/a")).not.toBeVisible();
    await expect(table.getByText("bulk/b")).not.toBeVisible();
  });

  test("cancelling the confirm modal leaves all entries intact", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "keep/a", "val-a");
    await createEntry(page, "keep/b", "val-b");
    await expect(page.getByText("2 entries")).toBeVisible();
    await expect(page.getByText('Created "keep/b"')).not.toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Select every row via the header select-all checkbox.
    await page.getByLabel("Select all rows").check();
    await page.getByRole("button", { name: "Delete 2" }).click();

    const modal = page.locator(".modal");
    await expect(modal.getByText("Confirm Delete")).toBeVisible();
    await modal.getByRole("button", { name: "Cancel" }).click();
    await expect(modal).not.toBeVisible();

    // Nothing was deleted.
    await expect(page.getByText("2 entries")).toBeVisible();
    const table = page.locator("table");
    await expect(table.getByText("keep/a").first()).toBeVisible();
    await expect(table.getByText("keep/b").first()).toBeVisible();
    await expect(page.getByText("Deleted", { exact: false })).not.toBeVisible();
  });

  test("select-all deletes every entry on confirm", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "wipe/a", "val-a");
    await createEntry(page, "wipe/b", "val-b");
    await expect(page.getByText("2 entries")).toBeVisible();
    await expect(page.getByText('Created "wipe/b"')).not.toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await page.getByLabel("Select all rows").check();
    await page.getByRole("button", { name: "Delete 2" }).click();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText("Deleted 2 entries")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("0 entries")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Collection documents bulk delete
// ---------------------------------------------------------------------------

const bulkCollectionState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "bulk-col-db", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the Collections tab of the bulk-col-db database. */
async function navigateToCollections(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("bulk-col-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("bulk-col-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("main nav").getByText("Collections", { exact: true }).click();
}

/** Create a collection with the given name. */
async function createCollection(page: Page, name: string): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Create Collection" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("input[placeholder='Enter collection name']").fill(name);
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText(`Collection "${name}" created`)).toBeVisible({
    timeout: 5_000,
  });
}

/** Select a collection from the left panel. */
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

/** Seed a collection with three documents and open the Documents tab. */
async function seedDocuments(page: Page, collection: string): Promise<void> {
  await navigateToCollections(page);
  await createCollection(page, collection);
  await selectCollection(page, collection);
  await insertDocument(page, '{"name": "doc1"}');
  await insertDocument(page, '{"name": "doc2"}');
  await insertDocument(page, '{"name": "doc3"}');
  // Wait for all stacked insert toasts to clear before interacting with the
  // table (toHaveCount(0) — multiple toasts violate not.toBeVisible strict mode).
  await expect(page.getByText("Document inserted.")).toHaveCount(0, {
    timeout: LOAD_TIMEOUT,
  });
  const table = page.locator("table");
  await expect(table.getByText("doc1").first()).toBeVisible({ timeout: 5_000 });
  await expect(table.getByText("doc3").first()).toBeVisible();
}

test.describe("Collection documents bulk delete", () => {
  test.use({ mockState: bulkCollectionState });

  test("selecting documents enables Delete with count and confirming deletes only the selected documents", async ({ page }) => {
    await seedDocuments(page, "bulkdocs");

    // Toolbar Delete is disabled with no selection.
    const bulkDeleteIdle = page.getByRole("button", { name: "Delete", exact: true });
    await expect(bulkDeleteIdle).toBeDisabled();

    // A single checkbox selection surfaces the count badge.
    await page.getByLabel("Select row 1", { exact: true }).check();
    await expect(page.getByRole("button", { name: "Delete 1" })).toBeEnabled();

    // Hand-pick a second row via its checkbox: checkboxes accumulate, so
    // row 1 stays selected. Documents are listed in insertion order, so
    // rows 1 and 2 are doc1 and doc2.
    await page.getByLabel("Select row 2", { exact: true }).check();
    await expect(page.getByLabel("Select row 1", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Select row 2", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Select row 3", { exact: true })).not.toBeChecked();

    const bulkDelete = page.getByRole("button", { name: "Delete 2" });
    await expect(bulkDelete).toBeEnabled();
    await bulkDelete.click();

    const modal = page.locator(".modal");
    await expect(modal.getByText("Confirm Delete")).toBeVisible();
    await expect(
      modal.getByText("Are you sure you want to delete 2 selected items?", { exact: false }),
    ).toBeVisible();
    await modal.getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText("Deleted 2 documents.")).toBeVisible({ timeout: 5_000 });

    const table = page.locator("table");
    await expect(table.getByText("doc3").first()).toBeVisible({ timeout: 5_000 });
    await expect(table.getByText("doc1")).not.toBeVisible();
    await expect(table.getByText("doc2")).not.toBeVisible();
  });

  test("cancelling the confirm modal leaves all documents intact", async ({ page }) => {
    await seedDocuments(page, "canceldocs");

    await page.getByLabel("Select all rows").check();
    await page.getByRole("button", { name: "Delete 3" }).click();

    const modal = page.locator(".modal");
    await expect(modal.getByText("Confirm Delete")).toBeVisible();
    await modal.getByRole("button", { name: "Cancel" }).click();
    await expect(modal).not.toBeVisible();

    const table = page.locator("table");
    await expect(table.getByText("doc1").first()).toBeVisible();
    await expect(table.getByText("doc2").first()).toBeVisible();
    await expect(table.getByText("doc3").first()).toBeVisible();
    await expect(page.getByText("Deleted", { exact: false })).not.toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Vector bulk delete
// ---------------------------------------------------------------------------

const bulkVectorState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "bulk-vec-db", backend: "pebble", status: "open" },
  ],
};

/**
 * Injects a stateful VectorBinding (the shared wails-mock VectorBinding is
 * stateless) pre-seeded with namespace "bulk-ns" containing three vectors.
 * Mirrors the injector pattern in vector-database.spec.ts.
 */
async function injectSeededVectorBinding(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;

    interface VectorEntry {
      id: string;
      vector: number[];
      metadata: Record<string, unknown>;
    }
    interface NamespaceInfo {
      dimension: number;
      vectorCount: number;
    }

    const namespaceStore = new Map<string, NamespaceInfo>();
    const vectorStore = new Map<string, Map<string, VectorEntry>>();

    // Pre-seed one namespace with three vectors.
    namespaceStore.set("bulk-ns", { dimension: 3, vectorCount: 3 });
    const seeded = new Map<string, VectorEntry>();
    for (const id of ["vec-a", "vec-b", "vec-c"]) {
      seeded.set(id, { id, vector: [1, 2, 3], metadata: { label: id } });
    }
    vectorStore.set("bulk-ns", seeded);

    function maybeReject(method: string): void {
      const s = w.__QRDB_MOCK_STATE__ as { errors: Record<string, string> };
      if (s.errors[method]) {
        throw new Error(s.errors[method]);
      }
    }

    const go = w.go as {
      main: {
        VectorBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
      };
    };

    go.main.VectorBinding = {
      CreateNamespace: async () => ({}),
      DeleteNamespace: async (req: unknown) => {
        const r = req as { namespace: string };
        namespaceStore.delete(r.namespace);
        vectorStore.delete(r.namespace);
      },
      ListNamespaces: async () => {
        maybeReject("VectorBinding.ListNamespaces");
        return { namespaces: Array.from(namespaceStore.keys()) };
      },
      GetNamespaceInfo: async (req: unknown) => {
        maybeReject("VectorBinding.GetNamespaceInfo");
        const r = req as { namespace: string };
        const info = namespaceStore.get(r.namespace);
        if (!info) throw new Error(`namespace "${r.namespace}" not found`);
        return {
          ...info,
          vectorCount: vectorStore.get(r.namespace)?.size ?? 0,
          distanceMetric: "cosine",
          indexType: "flat",
        };
      },
      UpsertVector: async () => ({}),
      GetVector: async () => ({}),
      DeleteVector: async (req: unknown) => {
        maybeReject("VectorBinding.DeleteVector");
        const r = req as { namespace: string; id: string };
        const vecs = vectorStore.get(r.namespace);
        if (vecs) {
          vecs.delete(r.id);
          const info = namespaceStore.get(r.namespace);
          if (info) info.vectorCount = vecs.size;
        }
      },
      BatchUpsert: async () => ({}),
      BatchDeleteVectors: async () => ({}),
      ListVectors: async (req: unknown) => {
        maybeReject("VectorBinding.ListVectors");
        const r = req as { namespace: string };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) return { vectors: [] };
        return {
          vectors: Array.from(vecs.values()).map((v) => ({
            id: v.id,
            vector: v.vector,
            metadata: v.metadata,
          })),
        };
      },
      UpdateMetadata: async () => {},
      SearchSimilar: async () => ({ results: [] }),
      SearchByMetadata: async () => ({ results: [] }),
      HybridSearch: async () => ({ results: [] }),
      SearchByID: async () => ({ results: [] }),
      RebuildIndex: async () => ({}),
      GetRebuildProgress: async () => ({}),
      GetIndexStats: async () => ({}),
      CompactNamespace: async () => ({}),
      GetCompactionProgress: async () => ({}),
    };
  });
}

/** Navigate to the vector database and select the seeded namespace. */
async function navigateToSeededNamespace(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("bulk-vec-db").click();
  await expect(
    page.locator(".breadcrumbs").getByText("bulk-vec-db", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("tab", { name: "Vectors", exact: true }).click();

  // Select the namespace from the shared namespace dropdown; this triggers
  // the vector list load ($effect on selectedNamespace).
  await page.locator("main select").first().selectOption("bulk-ns");

  const table = page.locator("table");
  await expect(table.getByText("vec-a").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(table.getByText("vec-c").first()).toBeVisible();
}

test.describe("Vector bulk delete", () => {
  test.use({ mockState: bulkVectorState });

  test("selecting vectors enables Delete with count and confirming deletes only the selected vectors", async ({ page }) => {
    await injectSeededVectorBinding(page);
    await navigateToSeededNamespace(page);

    // Toolbar Delete is disabled with no selection (the namespace delete
    // button has the distinct accessible name "Delete namespace").
    const bulkDeleteIdle = page.getByRole("button", { name: "Delete", exact: true });
    await expect(bulkDeleteIdle).toBeDisabled();

    // A single checkbox selection surfaces the count badge.
    await page.getByLabel("Select row 1", { exact: true }).check();
    await expect(page.getByRole("button", { name: "Delete 1" })).toBeEnabled();

    // Hand-pick a second row via its checkbox: checkboxes accumulate, so
    // row 1 stays selected. Vectors are listed in insertion order, so
    // rows 1 and 2 are vec-a and vec-b.
    await page.getByLabel("Select row 2", { exact: true }).check();
    await expect(page.getByLabel("Select row 1", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Select row 2", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Select row 3", { exact: true })).not.toBeChecked();

    const bulkDelete = page.getByRole("button", { name: "Delete 2" });
    await expect(bulkDelete).toBeEnabled();
    await bulkDelete.click();

    const modal = page.locator(".modal");
    await expect(modal.getByText("Confirm Delete")).toBeVisible();
    await expect(
      modal.getByText("Are you sure you want to delete 2 selected items?", { exact: false }),
    ).toBeVisible();
    await modal.getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText("Deleted 2 vectors")).toBeVisible({ timeout: 5_000 });

    const table = page.locator("table");
    await expect(table.getByText("vec-c").first()).toBeVisible({ timeout: 5_000 });
    await expect(table.getByText("vec-a")).not.toBeVisible();
    await expect(table.getByText("vec-b")).not.toBeVisible();
  });

  test("cancelling the confirm modal leaves all vectors intact", async ({ page }) => {
    await injectSeededVectorBinding(page);
    await navigateToSeededNamespace(page);

    await page.getByLabel("Select all rows").check();
    await page.getByRole("button", { name: "Delete 3" }).click();

    const modal = page.locator(".modal");
    await expect(modal.getByText("Confirm Delete")).toBeVisible();
    await modal.getByRole("button", { name: "Cancel" }).click();
    await expect(modal).not.toBeVisible();

    const table = page.locator("table");
    await expect(table.getByText("vec-a").first()).toBeVisible();
    await expect(table.getByText("vec-b").first()).toBeVisible();
    await expect(table.getByText("vec-c").first()).toBeVisible();
    await expect(page.getByText("Deleted", { exact: false })).not.toBeVisible();
  });
});
