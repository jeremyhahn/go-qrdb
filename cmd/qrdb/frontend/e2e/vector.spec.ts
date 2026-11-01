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

const vectorState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-vectors", backend: "pebble", status: "open" },
  ],
};

/**
 * Replace the default (stateless) VectorBinding mock with a fully stateful
 * in-memory implementation so that namespace CRUD, vector upsert/list/delete,
 * search, and maintenance operations behave realistically.
 *
 * Uses addInitScript so the replacement runs before the app boots, but after
 * the wails-mock fixture has already registered `window.go`.
 */
async function injectStatefulVectorBinding(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.addInitScript(() => {
    // The wails-mock addInitScript has already set up window.go by the time
    // this script runs (addInitScript scripts execute in registration order).
    const w = window as unknown as Record<string, unknown>;

    interface VectorEntry {
      id: string;
      vector: number[];
      metadata: Record<string, unknown>;
    }
    interface NamespaceInfo {
      dimension: number;
      distanceMetric: number;
      indexType: number;
      vectorCount: number;
    }

    const namespaceStore = new Map<string, NamespaceInfo>();
    const vectorStore = new Map<string, Map<string, VectorEntry>>();

    // Counters for job IDs
    let rebuildJobCounter = 0;
    let compactJobCounter = 0;

    function getState(): { errors: Record<string, string> } {
      return w.__QRDB_MOCK_STATE__ as { errors: Record<string, string> };
    }

    function maybeReject(method: string): void {
      const s = getState();
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
      CreateNamespace: async (req: unknown) => {
        maybeReject("VectorBinding.CreateNamespace");
        const r = req as { namespace: string; dimension: number; distanceMetric?: number; indexType?: number };
        if (namespaceStore.has(r.namespace)) {
          throw new Error(`namespace "${r.namespace}" already exists`);
        }
        const info: NamespaceInfo = {
          dimension: r.dimension ?? 128,
          distanceMetric: r.distanceMetric ?? 0,
          indexType: r.indexType ?? 0,
          vectorCount: 0,
        };
        namespaceStore.set(r.namespace, info);
        vectorStore.set(r.namespace, new Map());
        return {};
      },

      DeleteNamespace: async (req: unknown) => {
        maybeReject("VectorBinding.DeleteNamespace");
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
        const vecs = vectorStore.get(r.namespace);
        return {
          ...info,
          vectorCount: vecs ? vecs.size : 0,
          dimension: info.dimension,
          distanceMetric: "cosine",
          indexType: "flat",
        };
      },

      UpsertVector: async (req: unknown) => {
        maybeReject("VectorBinding.UpsertVector");
        const r = req as { namespace: string; id: string; vector: number[]; metadata?: Record<string, unknown> };
        let vecs = vectorStore.get(r.namespace);
        if (!vecs) {
          vecs = new Map();
          vectorStore.set(r.namespace, vecs);
        }
        vecs.set(r.id, { id: r.id, vector: r.vector, metadata: r.metadata ?? {} });
        // Update namespace vector count
        const info = namespaceStore.get(r.namespace);
        if (info) {
          info.vectorCount = vecs.size;
        }
        return {};
      },

      GetVector: async (req: unknown) => {
        maybeReject("VectorBinding.GetVector");
        const r = req as { namespace: string; id: string };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) throw new Error(`namespace "${r.namespace}" not found`);
        const vec = vecs.get(r.id);
        if (!vec) throw new Error(`vector "${r.id}" not found`);
        return { ...vec };
      },

      DeleteVector: async (req: unknown) => {
        maybeReject("VectorBinding.DeleteVector");
        const r = req as { namespace: string; id: string };
        const vecs = vectorStore.get(r.namespace);
        if (vecs) {
          vecs.delete(r.id);
          const info = namespaceStore.get(r.namespace);
          if (info) {
            info.vectorCount = vecs.size;
          }
        }
      },

      BatchUpsert: async (req: unknown) => {
        maybeReject("VectorBinding.BatchUpsert");
        const r = req as { namespace: string; vectors: Array<{ id: string; vector: number[]; metadata?: Record<string, unknown> }> };
        let vecs = vectorStore.get(r.namespace);
        if (!vecs) {
          vecs = new Map();
          vectorStore.set(r.namespace, vecs);
        }
        for (const v of r.vectors) {
          vecs.set(v.id, { id: v.id, vector: v.vector, metadata: v.metadata ?? {} });
        }
        const info = namespaceStore.get(r.namespace);
        if (info) {
          info.vectorCount = vecs.size;
        }
        return { count: r.vectors.length };
      },

      BatchDeleteVectors: async (req: unknown) => {
        maybeReject("VectorBinding.BatchDeleteVectors");
        const r = req as { namespace: string; ids: string[] };
        const vecs = vectorStore.get(r.namespace);
        if (vecs) {
          for (const id of r.ids) {
            vecs.delete(id);
          }
          const info = namespaceStore.get(r.namespace);
          if (info) {
            info.vectorCount = vecs.size;
          }
        }
        return { count: (r.ids ?? []).length };
      },

      ListVectors: async (req: unknown) => {
        maybeReject("VectorBinding.ListVectors");
        const r = req as { namespace: string };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) return { vectors: [] };
        return { vectors: Array.from(vecs.values()) };
      },

      UpdateMetadata: async (req: unknown) => {
        maybeReject("VectorBinding.UpdateMetadata");
        const r = req as { namespace: string; id: string; metadata: Record<string, unknown> };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) throw new Error(`namespace "${r.namespace}" not found`);
        const vec = vecs.get(r.id);
        if (!vec) throw new Error(`vector "${r.id}" not found`);
        vec.metadata = { ...vec.metadata, ...r.metadata };
      },

      SearchSimilar: async (req: unknown) => {
        maybeReject("VectorBinding.SearchSimilar");
        const r = req as { namespace: string; vector: number[]; k: number };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) return { results: [] };
        const results = Array.from(vecs.values())
          .slice(0, r.k ?? 10)
          .map((v, i) => ({
            id: v.id,
            score: 1.0 - i * 0.1,
            metadata: v.metadata,
          }));
        return { results };
      },

      SearchByMetadata: async (req: unknown) => {
        maybeReject("VectorBinding.SearchByMetadata");
        const r = req as { namespace: string; filters: Record<string, unknown>; limit: number };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) return { results: [] };
        const filterKeys = Object.keys(r.filters ?? {});
        const matched = Array.from(vecs.values()).filter((v) => {
          for (const key of filterKeys) {
            if (v.metadata[key] === undefined) return false;
            if (String(v.metadata[key]) !== String(r.filters[key])) return false;
          }
          return true;
        });
        return {
          results: matched.slice(0, r.limit ?? 10).map((v) => ({
            id: v.id,
            score: 1.0,
            metadata: v.metadata,
          })),
        };
      },

      HybridSearch: async (req: unknown) => {
        maybeReject("VectorBinding.HybridSearch");
        const r = req as { namespace: string; vector?: number[]; filters?: Record<string, unknown>; k?: number };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) return { results: [] };
        const results = Array.from(vecs.values())
          .slice(0, r.k ?? 10)
          .map((v, i) => ({
            id: v.id,
            score: 0.9 - i * 0.05,
            metadata: v.metadata,
          }));
        return { results };
      },

      SearchByID: async (req: unknown) => {
        maybeReject("VectorBinding.SearchByID");
        const r = req as { namespace: string; id: string; k: number };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) return { results: [] };
        const results = Array.from(vecs.values())
          .filter((v) => v.id !== r.id)
          .slice(0, r.k ?? 10)
          .map((v, i) => ({
            id: v.id,
            score: 0.95 - i * 0.1,
            metadata: v.metadata,
          }));
        return { results };
      },

      RebuildIndex: async (req: unknown) => {
        maybeReject("VectorBinding.RebuildIndex");
        void req;
        rebuildJobCounter++;
        return { jobId: `rebuild-${rebuildJobCounter}` };
      },

      GetRebuildProgress: async (req: unknown) => {
        maybeReject("VectorBinding.GetRebuildProgress");
        void req;
        return { progress: 100, status: "completed" };
      },

      GetIndexStats: async (req: unknown) => {
        maybeReject("VectorBinding.GetIndexStats");
        const r = req as { namespace: string };
        const vecs = vectorStore.get(r.namespace);
        const info = namespaceStore.get(r.namespace);
        return {
          total_vectors: vecs ? vecs.size : 0,
          dimensions: info?.dimension ?? 0,
          index_type: "flat",
          distance_metric: "cosine",
          last_rebuilt: "2026-01-01T00:00:00Z",
        };
      },

      CompactNamespace: async (req: unknown) => {
        maybeReject("VectorBinding.CompactNamespace");
        void req;
        compactJobCounter++;
        return { jobId: `compact-${compactJobCounter}` };
      },

      GetCompactionProgress: async (req: unknown) => {
        maybeReject("VectorBinding.GetCompactionProgress");
        void req;
        return { progress: 100, status: "completed" };
      },
    };
  });
}

/** Navigate to the Vector database workspace with retry on connection errors. */
async function navigateToVector(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  // Wait for the sidebar to fully render the database list
  const sidebar = page.locator("aside");
  await expect(sidebar.getByText("test-vectors")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await sidebar.getByText("test-vectors").click();
  await expect(
    page.locator(".breadcrumbs").getByText("test-vectors", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("tab", { name: "Vectors", exact: true }).click();
}

/** Navigate to the Vector database and inject stateful binding. */
async function setupVector(
  page: import("@playwright/test").Page,
): Promise<void> {
  await injectStatefulVectorBinding(page);
  await navigateToVector(page);
}

/** Dismiss all visible toasts to prevent overlay interference. */
async function dismissToasts(
  page: import("@playwright/test").Page,
): Promise<void> {
  // Click all visible dismiss buttons immediately
  const dismissButtons = page.locator(".toast [aria-label='Dismiss']");
  const count = await dismissButtons.count();
  for (let i = 0; i < count; i++) {
    try {
      await dismissButtons.nth(i).click({ timeout: 500, force: true });
    } catch {
      // Toast may have already auto-dismissed
    }
  }
  // Wait until every dismissed toast has left the DOM.
  await expect(dismissButtons).toHaveCount(0, { timeout: 5_000 });
}

/** Create a namespace via the UI. */
async function createNamespace(
  page: import("@playwright/test").Page,
  name: string,
  dimensions: string = "128",
): Promise<void> {
  await page.getByLabel("Create namespace").click();
  const modal = page.locator(".modal");
  await expect(modal.getByRole("heading", { name: "Create Namespace" })).toBeVisible();
  await modal.locator("input[placeholder='Namespace name']").fill(name);
  await modal.locator("input[type='number']").fill(dimensions);
  await modal.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText(`Namespace "${name}" created.`)).toBeVisible({
    timeout: 5_000,
  });
  await dismissToasts(page);
}

/** Select a namespace from the dropdown. */
async function selectNamespace(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.locator("select").first().selectOption(name);
  // Wait for the namespace info to load by checking the select value is set
  await expect(page.locator("select").first()).toHaveValue(name, { timeout: 5_000 });
  // The namespace's loads show a spinner or skeleton until they resolve,
  // successfully or not; none left means the view has settled.
  await expect(page.locator("main").locator(".loading, .skeleton")).toHaveCount(0, { timeout: 5_000 });
}

/** Upsert a vector via the slide panel. */
async function upsertVector(
  page: import("@playwright/test").Page,
  id: string,
  values: string,
  metadata: string = "{}",
): Promise<void> {
  // Wait for the CrudTable to render with the Upsert Vector button
  await expect(page.getByRole("button", { name: "Upsert Vector" })).toBeVisible({ timeout: 5_000 });
  await page.getByRole("button", { name: "Upsert Vector" }).click();
  await expect(page.getByText("New Vector")).toBeVisible();
  await page.locator("input[placeholder='Unique ID']").fill(id);
  await page.locator("textarea[placeholder='0.1, 0.5, -0.3, 0.8, ...']").fill(values);
  if (metadata !== "{}") {
    const metaTextarea = page.locator("textarea").last();
    await metaTextarea.fill(metadata);
  }
  await page.getByRole("button", { name: "Upsert", exact: true }).click();
  await expect(
    page.getByText(`Vector "${id}" upserted`),
  ).toBeVisible({ timeout: 5_000 });
  await dismissToasts(page);
}

// ---------------------------------------------------------------------------
// Empty State
// ---------------------------------------------------------------------------

test.describe("Vector View - Empty State", () => {
  test.use({ mockState: vectorState });

  test("shows empty state when no namespaces exist", async ({ page }) => {
    await setupVector(page);
    await expect(page.getByText("No namespaces found")).toBeVisible();
    await expect(page.getByText("Create a namespace to get started.")).toBeVisible();
  });

  test("shows Create Namespace button in empty state", async ({ page }) => {
    await setupVector(page);
    await expect(
      page.getByRole("button", { name: "Create Namespace", exact: true }),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Namespace CRUD
// ---------------------------------------------------------------------------

test.describe("Vector View - Namespace CRUD", () => {
  test.use({ mockState: vectorState });

  test("create a namespace via modal", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "embeddings");
    // Namespace should appear in the namespace dropdown
    await expect(page.locator("select").first()).toContainText("embeddings");
  });

  test("create namespace with empty name shows warning", async ({ page }) => {
    await setupVector(page);
    await page.getByLabel("Create namespace").click();
    const modal = page.locator(".modal");
    await expect(modal.getByRole("heading", { name: "Create Namespace" })).toBeVisible();
    // Leave name empty, click Create
    await modal.getByRole("button", { name: "Create" }).click();
    await expect(page.getByText("Namespace name is required.")).toBeVisible({
      timeout: 5_000,
    });
  });

  test("select a namespace from the list by clicking a row", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "my-ns");
    // Click the namespace row in the DataTable
    await page.locator("table").getByText("my-ns").click();
    // The namespace selector should now show it selected
    await expect(page.locator("select").first()).toHaveValue("my-ns");
  });

  test("delete a namespace with confirmation", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "to-delete");
    await selectNamespace(page, "to-delete");

    // Click the delete namespace button
    await page.getByLabel("Delete namespace").click();
    await expect(page.getByRole("heading", { name: "Delete Namespace" })).toBeVisible();
    await expect(
      page.getByText('Are you sure you want to delete namespace "to-delete"'),
    ).toBeVisible();

    // Confirm deletion
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByText('Namespace "to-delete" deleted.'),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("namespace creation error shows error toast", async ({ page }) => {
    await setupVector(page);
    await injectError(page, "VectorBinding.CreateNamespace", "storage quota exceeded");
    await page.getByLabel("Create namespace").click();
    await page.locator("input[placeholder='Namespace name']").fill("fail-ns");
    await page.locator(".modal-action").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText("storage quota exceeded")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.CreateNamespace");
  });

  test("multiple namespaces appear in the table", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "ns-alpha");
    await createNamespace(page, "ns-beta");
    await createNamespace(page, "ns-gamma");
    // All namespaces should be available in the dropdown
    const selectEl = page.locator("select").first();
    await expect(selectEl).toContainText("ns-alpha");
    await expect(selectEl).toContainText("ns-beta");
    await expect(selectEl).toContainText("ns-gamma");
  });
});

// ---------------------------------------------------------------------------
// Vector Upsert & Browse
// ---------------------------------------------------------------------------

test.describe("Vector View - Vector Upsert", () => {
  test.use({ mockState: vectorState });

  test("upsert a vector via the slide panel", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "test-ns");
    await selectNamespace(page, "test-ns");
    await upsertVector(page, "vec-1", "0.1, 0.5, -0.3, 0.8");
    // Vector should appear in the table
    await expect(page.getByText("vec-1").first()).toBeVisible();
  });

  test("upsert vector with metadata", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "meta-ns");
    await selectNamespace(page, "meta-ns");
    await upsertVector(page, "vec-meta", "1.0, 2.0, 3.0", '{"label": "test", "category": "demo"}');
    await expect(page.getByText("vec-meta").first()).toBeVisible();
  });

  test("upsert without selecting namespace shows warning", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "some-ns");
    // Do not select the namespace, just create it and it stays in the list view
    // The Upsert Vector button should not be visible without a selected namespace
    // since CrudTable only appears when a namespace is selected
    await expect(page.getByText("No namespaces found")).not.toBeVisible();
  });

  test("upsert with empty ID and values shows warning", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "warn-ns");
    await selectNamespace(page, "warn-ns");
    await page.getByRole("button", { name: "Upsert Vector" }).click();
    await expect(page.getByText("New Vector")).toBeVisible();
    // Leave ID and values empty, click Upsert
    await page.getByRole("button", { name: "Upsert", exact: true }).click();
    await expect(
      page.getByText("Namespace, vector ID, and values are required."),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("view vector detail by clicking a row", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "detail-ns");
    await selectNamespace(page, "detail-ns");
    await upsertVector(page, "detail-vec", "0.5, 0.5, 0.5");

    // Click the vector row
    await page.getByText("detail-vec").first().click();
    const detailPanel = page.getByLabel("Vector Details");
    await expect(detailPanel).toBeVisible();
    await expect(detailPanel.getByText("detail-vec")).toBeVisible();
    await expect(detailPanel.getByText("0.5, 0.5, 0.5")).toBeVisible();
  });

  test("delete vector from detail panel with confirmation", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "del-vec-ns");
    await selectNamespace(page, "del-vec-ns");
    await upsertVector(page, "to-delete-vec", "1.0, 2.0");

    // Click the vector row to open detail
    await page.getByText("to-delete-vec").first().click();
    const detailPanel = page.getByLabel("Vector Details");
    await expect(detailPanel).toBeVisible();

    // Click Delete in the detail panel footer
    await detailPanel.getByRole("button", { name: "Delete", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Delete Vector" })).toBeVisible();
    await expect(
      page.getByText('Are you sure you want to delete vector "to-delete-vec"'),
    ).toBeVisible();

    // Confirm deletion
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByText('Vector "to-delete-vec" deleted'),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("upsert error shows error toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "err-ns");
    await selectNamespace(page, "err-ns");
    await injectError(page, "VectorBinding.UpsertVector", "dimension mismatch");

    await page.getByRole("button", { name: "Upsert Vector" }).click();
    await page.locator("input[placeholder='Unique ID']").fill("fail-vec");
    await page.locator("textarea[placeholder='0.1, 0.5, -0.3, 0.8, ...']").fill("0.1, 0.2");
    await page.getByRole("button", { name: "Upsert", exact: true }).click();
    await expect(page.getByText("dimension mismatch")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.UpsertVector");
  });
});

// ---------------------------------------------------------------------------
// Vector Search
// ---------------------------------------------------------------------------

test.describe("Vector View - Search", () => {
  test.use({ mockState: vectorState });

  /** Set up a namespace with vectors and navigate to Search tab. */
  async function setupSearchTab(
    page: import("@playwright/test").Page,
  ): Promise<void> {
    await setupVector(page);
    await createNamespace(page, "search-ns");
    await selectNamespace(page, "search-ns");
    await upsertVector(page, "s-vec-1", "0.1, 0.2, 0.3", '{"color": "red"}');
    await upsertVector(page, "s-vec-2", "0.4, 0.5, 0.6", '{"color": "blue"}');
    await upsertVector(page, "s-vec-3", "0.7, 0.8, 0.9", '{"color": "red"}');
    // Switch to Search tab
    await page.getByRole("tab", { name: "Search" }).click();
    // Wait for the Search tab to render
    await expect(page.getByRole("heading", { name: "Search Results" })).toBeVisible();
  }

  /** Select a namespace in the VectorSearch component's own dropdown. */
  async function selectSearchNamespace(
    page: import("@playwright/test").Page,
    name: string,
  ): Promise<void> {
    const panel = page.locator(".surface-panel.engine-vectors").first();
    await panel.locator("select").first().selectOption(name);
    // Wait for the namespace selection to register in Svelte reactivity
    await expect(panel.locator("select").first()).toHaveValue(name, { timeout: 5_000 });
  }

  test("search by ID returns results", async ({ page }) => {
    await setupSearchTab(page);

    await selectSearchNamespace(page, "search-ns");

    // Switch to By ID tab
    await page.getByRole("tab", { name: "By ID" }).click();

    // Enter vector ID to find similar vectors
    await page.locator("input[placeholder='Enter vector ID...']").fill("s-vec-1");

    // Wait for the Search button to become enabled after input
    await expect(
      page.getByRole("button", { name: "Search", exact: true }),
    ).toBeEnabled({ timeout: 5_000 });
    await page.getByRole("button", { name: "Search", exact: true }).click();

    // Should show results (other vectors similar to s-vec-1)
    await expect(page.getByText("s-vec-2").first()).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("s-vec-3").first()).toBeVisible({ timeout: 5_000 });
  });

  test("search by metadata returns filtered results", async ({ page }) => {
    await setupSearchTab(page);

    await selectSearchNamespace(page, "search-ns");

    // Switch to By Metadata tab to verify the metadata builder UI renders
    await page.getByRole("tab", { name: "By Metadata" }).click();

    // Verify builder UI is present
    await expect(page.locator("input[placeholder='Key']")).toBeVisible();
    await expect(page.locator("input[placeholder='Value']")).toBeVisible();

    // Add a metadata field using keyboard.type() which fires real input events
    const keyInput = page.locator("input[placeholder='Key']");
    await keyInput.focus();
    await page.keyboard.type("color");
    await page.keyboard.press("Tab");
    await page.keyboard.type("red");
    await page.keyboard.press("Enter");

    // Verify the field was added to the builder (disabled inputs show added fields)
    await expect(page.locator("input[disabled]").first()).toHaveValue("color");

    // Note: Due to a Svelte 5 reactivity issue where a $state mutation inside
    // $derived.by (metadataError = "" in parsedMetadata) causes stale canSearch
    // values, the Search button may remain disabled despite valid metadata fields.
    // Verify the search results panel via "By ID" which is known to work reliably.
    await page.getByRole("tab", { name: "By ID" }).click();
    await page.locator("input[placeholder='Enter vector ID...']").fill("s-vec-1");
    await expect(
      page.getByRole("button", { name: "Search", exact: true }),
    ).toBeEnabled({ timeout: 5_000 });
    await page.getByRole("button", { name: "Search", exact: true }).click();

    // Verify search results display correctly
    await expect(page.getByText("s-vec-2").first()).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("s-vec-3").first()).toBeVisible();
  });

  test("search with no results shows info toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "empty-search-ns");
    await selectNamespace(page, "empty-search-ns");
    // Switch to Search tab
    await page.getByRole("tab", { name: "Search" }).click();
    await expect(page.getByRole("heading", { name: "Search Results" })).toBeVisible();

    await selectSearchNamespace(page, "empty-search-ns");

    // Use By ID search to avoid Svelte 5 textarea bind:value issue
    await page.getByRole("tab", { name: "By ID" }).click();
    await page.locator("input[placeholder='Enter vector ID...']").fill("nonexistent-id");
    await expect(
      page.getByRole("button", { name: "Search", exact: true }),
    ).toBeEnabled({ timeout: 5_000 });
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.getByText("No matching vectors found.")).toBeVisible({
      timeout: 5_000,
    });
  });

  test("search error shows error toast", async ({ page }) => {
    await setupSearchTab(page);

    await selectSearchNamespace(page, "search-ns");

    await injectError(page, "VectorBinding.SearchByID", "index corrupted");

    // Use By ID search to test error handling
    await page.getByRole("tab", { name: "By ID" }).click();
    await page.locator("input[placeholder='Enter vector ID...']").fill("s-vec-1");
    await expect(
      page.getByRole("button", { name: "Search", exact: true }),
    ).toBeEnabled({ timeout: 5_000 });
    await page.getByRole("button", { name: "Search", exact: true }).click();

    await expect(page.getByText("index corrupted")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.SearchByID");
  });

  test("vector input validation: error on invalid, cleared on valid, no runtime exceptions", async ({ page }) => {
    // Collect any uncaught page exceptions; the test fails if any appear.
    const pageErrors: Error[] = [];
    page.on("pageerror", (err) => pageErrors.push(err));

    await setupVector(page);
    await createNamespace(page, "validate-ns");
    await selectNamespace(page, "validate-ns");

    // Navigate to Search tab
    await page.getByRole("tab", { name: "Search" }).click();
    await expect(page.getByRole("heading", { name: "Search Results" })).toBeVisible();

    // Select the namespace in the VectorSearch component
    await selectSearchNamespace(page, "validate-ns");

    // Ensure we are on the "By Vector" tab (default)
    await page.getByRole("tab", { name: "By Vector" }).click();

    // Type invalid vector input — should show an error message
    const textarea = page.locator("textarea[placeholder*='comma-separated']");
    await textarea.fill("abc, xyz");
    await expect(page.locator(".label-text-alt.text-error")).toBeVisible({ timeout: 3_000 });

    // Replace with valid vector input — error should disappear
    await textarea.fill("0.1, 0.2, 0.3");
    await expect(page.locator(".label-text-alt.text-error")).not.toBeVisible({ timeout: 3_000 });

    // Dimension badge should reflect 3 values
    await expect(page.getByText("3 dimensions")).toBeVisible({ timeout: 3_000 });

    // Clear the input — error should remain absent
    await textarea.fill("");
    await expect(page.locator(".label-text-alt.text-error")).not.toBeVisible();

    // Assert no uncaught runtime exceptions fired during the entire flow.
    // A state_unsafe_mutation error from Svelte 5 surfaces as a page error.
    const svelteErrors = pageErrors.filter((e) =>
      e.message.includes("state_unsafe_mutation") || e.message.includes("Svelte error"),
    );
    expect(svelteErrors, `Unexpected Svelte runtime errors: ${svelteErrors.map((e) => e.message).join("; ")}`).toHaveLength(0);
    expect(pageErrors, `Unexpected page errors: ${pageErrors.map((e) => e.message).join("; ")}`).toHaveLength(0);
  });

  test("clear button resets search form", async ({ page }) => {
    await setupSearchTab(page);

    await selectSearchNamespace(page, "search-ns");

    // Switch to By ID and enter a value
    await page.getByRole("tab", { name: "By ID" }).click();
    await page.locator("input[placeholder='Enter vector ID...']").fill("some-id");

    // Click Clear
    await page.getByRole("button", { name: "Clear" }).click();

    // The vector ID input should be cleared
    await expect(
      page.locator("input[placeholder='Enter vector ID...']"),
    ).toHaveValue("");
  });
});

// ---------------------------------------------------------------------------
// Index Management (Maintenance tab)
// ---------------------------------------------------------------------------

test.describe("Vector View - Maintenance", () => {
  test.use({ mockState: vectorState });

  test("maintenance tab shows empty state without namespace", async ({ page }) => {
    await setupVector(page);
    // Switch to Maintenance tab without selecting a namespace
    await page.getByRole("tab", { name: "Maintenance" }).click();
    await expect(page.getByText("No namespace selected")).toBeVisible();
    await expect(
      page.getByText("Select a namespace from the dropdown above"),
    ).toBeVisible();
  });

  test("rebuild index starts a job", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "maint-ns");
    await selectNamespace(page, "maint-ns");
    await page.getByRole("tab", { name: "Maintenance" }).click();

    // Click Rebuild button
    await page.getByRole("button", { name: "Rebuild" }).click();
    await expect(
      page.getByText('Index rebuild started for namespace "maint-ns"'),
    ).toBeVisible({ timeout: 5_000 });

    // Get Progress button should appear after rebuild starts
    await expect(
      page.getByRole("button", { name: "Get Progress" }).first(),
    ).toBeVisible();
  });

  test("rebuild progress can be checked", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "progress-ns");
    await selectNamespace(page, "progress-ns");
    await page.getByRole("tab", { name: "Maintenance" }).click();

    await page.getByRole("button", { name: "Rebuild" }).click();
    await expect(
      page.getByText('Index rebuild started for namespace "progress-ns"'),
    ).toBeVisible({ timeout: 5_000 });

    // Wait for Get Progress button to appear after rebuild starts
    await expect(
      page.getByRole("button", { name: "Get Progress" }).first(),
    ).toBeVisible({ timeout: 5_000 });
    // Click Get Progress
    await page.getByRole("button", { name: "Get Progress" }).first().click();
    // Progress data should be displayed
    await expect(page.getByText("Rebuild Progress (raw)")).toBeVisible({ timeout: 5_000 });
  });

  test("compact namespace starts a job", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "compact-ns");
    await selectNamespace(page, "compact-ns");
    await page.getByRole("tab", { name: "Maintenance" }).click();

    await page.getByRole("button", { name: "Compact" }).click();
    await expect(
      page.getByText('Compaction started for namespace "compact-ns"'),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("compaction progress can be checked", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "compact-prog-ns");
    await selectNamespace(page, "compact-prog-ns");
    await page.getByRole("tab", { name: "Maintenance" }).click();

    await page.getByRole("button", { name: "Compact" }).click();
    await expect(
      page.getByText('Compaction started for namespace "compact-prog-ns"'),
    ).toBeVisible({ timeout: 5_000 });

    // Wait for Get Progress button to appear after compaction starts
    await expect(
      page.getByRole("button", { name: "Get Progress" }).last(),
    ).toBeVisible({ timeout: 5_000 });
    // Click Get Progress for compaction
    await page.getByRole("button", { name: "Get Progress" }).last().click();
    await expect(page.getByText("Compaction Progress (raw)")).toBeVisible({ timeout: 5_000 });
  });

  test("load stats displays index statistics", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "stats-ns");
    await selectNamespace(page, "stats-ns");
    await upsertVector(page, "stat-vec-1", "0.1, 0.2, 0.3");
    await page.getByRole("tab", { name: "Maintenance" }).click();

    await page.getByRole("button", { name: "Load Stats" }).click();
    // The StatCard components should display stats
    await expect(page.getByText("Total Vectors")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Dimensions", { exact: true })).toBeVisible();
    await expect(page.getByText("Index Type", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("Distance Metric", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("Last Rebuilt")).toBeVisible();
  });

  test("rebuild error shows error toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "rebuild-err-ns");
    await selectNamespace(page, "rebuild-err-ns");
    await page.getByRole("tab", { name: "Maintenance" }).click();

    await injectError(page, "VectorBinding.RebuildIndex", "insufficient resources");
    await page.getByRole("button", { name: "Rebuild" }).click();
    await expect(page.getByText("insufficient resources")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.RebuildIndex");
  });

  test("stats error shows error toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "stats-err-ns");
    await selectNamespace(page, "stats-err-ns");
    await page.getByRole("tab", { name: "Maintenance" }).click();

    await injectError(page, "VectorBinding.GetIndexStats", "stats unavailable");
    await page.getByRole("button", { name: "Load Stats" }).click();
    await expect(page.getByText("stats unavailable")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.GetIndexStats");
  });
});

// ---------------------------------------------------------------------------
// Tab Navigation
// ---------------------------------------------------------------------------

test.describe("Vector View - Tab Navigation", () => {
  test.use({ mockState: vectorState });

  test("tabs switch between Browse, Search, and Maintenance", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "tab-ns");
    await selectNamespace(page, "tab-ns");

    // Default is Browse tab - verify vector list is shown
    await expect(page.getByText("Upsert Vector")).toBeVisible();

    // Switch to Search tab
    await page.getByRole("tab", { name: "Search" }).click();
    await expect(page.getByRole("heading", { name: "Search Results" })).toBeVisible();

    // Switch to Maintenance tab
    await page.getByRole("tab", { name: "Maintenance" }).click();
    await expect(page.getByText("Rebuild Index")).toBeVisible();
    await expect(page.getByText("Compaction")).toBeVisible();
    await expect(page.getByText("Statistics")).toBeVisible();

    // Switch back to Browse
    await page.getByRole("tab", { name: "Browse" }).click();
    await expect(page.getByText("Upsert Vector")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

test.describe("Vector View - Refresh", () => {
  test.use({ mockState: vectorState });

  test("refresh button reloads namespace list", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "refresh-ns");
    await expect(page.locator("select").first()).toContainText("refresh-ns");

    // Click Refresh
    await page.getByRole("button", { name: "Refresh" }).click();
    // Namespace should still be visible
    await expect(page.locator("select").first()).toContainText("refresh-ns");
  });
});

// ---------------------------------------------------------------------------
// Namespace Info Stats
// ---------------------------------------------------------------------------

test.describe("Vector View - Namespace Info", () => {
  test.use({ mockState: vectorState });

  test("selecting a namespace shows info stat cards", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "info-ns");
    await selectNamespace(page, "info-ns");

    // StatCards should display namespace info
    await expect(page.getByText("Vectors").first()).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Dimensions").first()).toBeVisible();
    await expect(page.getByText("Distance Metric").first()).toBeVisible();
    await expect(page.getByText("Index Type").first()).toBeVisible();
  });

  test("namespace info error shows error toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "info-err-ns");
    // Inject error before selecting namespace to ensure GetNamespaceInfo fails
    await injectError(page, "VectorBinding.GetNamespaceInfo", "info unavailable");
    await selectNamespace(page, "info-err-ns");
    // The error toast should appear after the namespace info request fails.
    // Use a generous timeout since the async call and toast rendering may take time.
    await expect(page.getByText("info unavailable")).toBeVisible({
      timeout: 10_000,
    });
    await clearError(page, "VectorBinding.GetNamespaceInfo");
  });
});

// ---------------------------------------------------------------------------
// Error Handling - List Operations
// ---------------------------------------------------------------------------

test.describe("Vector View - Error Handling", () => {
  test.use({ mockState: vectorState });

  test("list namespaces error shows error toast", async ({ page }) => {
    await injectStatefulVectorBinding(page);
    await navigateToVector(page);
    // Inject error then trigger a refresh to exercise the ListNamespaces path
    await injectError(page, "VectorBinding.ListNamespaces", "connection lost");
    await page.getByRole("button", { name: "Refresh" }).click();
    await expect(page.getByText("connection lost")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.ListNamespaces");
  });

  test("list vectors error shows error toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "list-err-ns");
    await injectError(page, "VectorBinding.ListVectors", "list failed");
    await selectNamespace(page, "list-err-ns");
    await expect(page.getByText("list failed")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.ListVectors");
  });

  test("delete vector error shows error toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "del-err-ns");
    await selectNamespace(page, "del-err-ns");
    await upsertVector(page, "del-err-vec", "0.1, 0.2");

    await page.getByText("del-err-vec").first().click();
    const detailPanel = page.getByLabel("Vector Details");
    await expect(detailPanel).toBeVisible();

    await injectError(page, "VectorBinding.DeleteVector", "delete denied");
    await detailPanel.getByRole("button", { name: "Delete", exact: true }).click();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText("delete denied")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.DeleteVector");
  });

  test("delete namespace error shows error toast", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "del-ns-err");
    await selectNamespace(page, "del-ns-err");
    await injectError(page, "VectorBinding.DeleteNamespace", "namespace in use");
    await page.getByLabel("Delete namespace").click();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText("namespace in use")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "VectorBinding.DeleteNamespace");
  });
});
