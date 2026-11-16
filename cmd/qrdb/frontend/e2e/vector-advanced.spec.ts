/**
 * Copyright 2026 Jeremy Hahn
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * Mocked-tier E2E for the vector operations beyond basic CRUD and search:
 * getVector (detail panel), updateMetadata, batchUpsert, batchDeleteVectors
 * (bulk delete) and hybridSearch.
 */

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
 * Stateful in-memory VectorBinding (a trimmed copy of the one in
 * vector.spec.ts; Playwright cannot import one spec from another). Every
 * method honours injectError via maybeReject("VectorBinding.<Method>").
 * Vectors carry version/createdAt/updatedAt so getVector returns a full
 * VectorData, and every write bumps the version.
 */
async function injectStatefulVectorBinding(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;

    interface VectorEntry {
      id: string;
      vector: number[];
      metadata: Record<string, unknown>;
      version: number;
      createdAt: number;
      updatedAt: number;
    }

    const namespaceStore = new Map<string, { dimension: number }>();
    const vectorStore = new Map<string, Map<string, VectorEntry>>();
    let clock = 1_700_000_000;

    function maybeReject(method: string): void {
      const s = w.__QRDB_MOCK_STATE__ as { errors: Record<string, string> };
      if (s.errors[method]) {
        throw new Error(s.errors[method]);
      }
    }

    function put(ns: string, id: string, vector: number[], metadata: Record<string, unknown>): void {
      let vecs = vectorStore.get(ns);
      if (!vecs) {
        vecs = new Map();
        vectorStore.set(ns, vecs);
      }
      const now = ++clock;
      const prev = vecs.get(id);
      vecs.set(id, {
        id,
        vector,
        metadata,
        version: (prev?.version ?? 0) + 1,
        createdAt: prev?.createdAt ?? now,
        updatedAt: now,
      });
    }

    function lookup(ns: string, id: string): VectorEntry {
      const vecs = vectorStore.get(ns);
      if (!vecs) throw new Error(`namespace "${ns}" not found`);
      const vec = vecs.get(id);
      if (!vec) throw new Error(`vector "${id}" not found`);
      return vec;
    }

    const go = w.go as {
      main: {
        VectorBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
      };
    };

    go.main.VectorBinding = {
      CreateNamespace: async (req: unknown) => {
        maybeReject("VectorBinding.CreateNamespace");
        const r = req as { namespace: string; dimension: number };
        if (namespaceStore.has(r.namespace)) {
          throw new Error(`namespace "${r.namespace}" already exists`);
        }
        namespaceStore.set(r.namespace, { dimension: r.dimension ?? 128 });
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
        return {
          dimension: info.dimension,
          vectorCount: vectorStore.get(r.namespace)?.size ?? 0,
          distanceMetric: "cosine",
          indexType: "flat",
        };
      },

      UpsertVector: async (req: unknown) => {
        maybeReject("VectorBinding.UpsertVector");
        const r = req as { namespace: string; id: string; vector: number[]; metadata?: Record<string, unknown> };
        put(r.namespace, r.id, r.vector, r.metadata ?? {});
        return {};
      },

      GetVector: async (req: unknown) => {
        maybeReject("VectorBinding.GetVector");
        const r = req as { namespace: string; id: string };
        return { ...lookup(r.namespace, r.id) };
      },

      DeleteVector: async (req: unknown) => {
        maybeReject("VectorBinding.DeleteVector");
        const r = req as { namespace: string; id: string };
        vectorStore.get(r.namespace)?.delete(r.id);
      },

      BatchUpsert: async (req: unknown) => {
        maybeReject("VectorBinding.BatchUpsert");
        const r = req as {
          namespace: string;
          vectors: Array<{ id: string; vector: number[]; metadata?: Record<string, unknown> }>;
        };
        const dim = namespaceStore.get(r.namespace)?.dimension;
        let succeeded = 0;
        const errors: Array<{ id: string; error: string }> = [];
        for (const v of r.vectors) {
          if (dim !== undefined && v.vector.length !== dim) {
            errors.push({ id: v.id, error: `dimension ${v.vector.length} != ${dim}` });
            continue;
          }
          put(r.namespace, v.id, v.vector, v.metadata ?? {});
          succeeded++;
        }
        return { succeeded, failed: errors.length, errors };
      },

      BatchDeleteVectors: async (req: unknown) => {
        maybeReject("VectorBinding.BatchDeleteVectors");
        const r = req as { namespace: string; ids: string[] };
        const vecs = vectorStore.get(r.namespace);
        let deleted = 0;
        const errors: Array<{ id: string; error: string }> = [];
        for (const id of r.ids ?? []) {
          if (vecs?.delete(id)) {
            deleted++;
          } else {
            errors.push({ id, error: "not found" });
          }
        }
        return { deleted, failed: errors.length, errors };
      },

      ListVectors: async (req: unknown) => {
        maybeReject("VectorBinding.ListVectors");
        const r = req as { namespace: string };
        const vecs = vectorStore.get(r.namespace);
        return { vectors: vecs ? Array.from(vecs.values()) : [] };
      },

      UpdateMetadata: async (req: unknown) => {
        maybeReject("VectorBinding.UpdateMetadata");
        const r = req as { namespace: string; id: string; metadata: Record<string, unknown> };
        const vec = lookup(r.namespace, r.id);
        put(r.namespace, r.id, vec.vector, r.metadata);
      },

      HybridSearch: async (req: unknown) => {
        maybeReject("VectorBinding.HybridSearch");
        // The Wails adapter sends the request through toSnake().
        const r = req as {
          namespace: string;
          vector: number[];
          metadata_filters: Record<string, unknown>;
          k: number;
          vector_weight: number;
          metadata_weight: number;
        };
        const vecs = vectorStore.get(r.namespace);
        if (!vecs) return { results: [], took: 0 };
        const filters = r.metadata_filters ?? {};
        const keys = Object.keys(filters);
        const results = Array.from(vecs.values())
          .map((v) => {
            const match = keys.length > 0 && keys.every(
              (key) => String(v.metadata[key]) === String(filters[key]),
            );
            return {
              id: v.id,
              score: r.vector_weight * 0.5 + (match ? r.metadata_weight : 0),
              metadata: v.metadata,
            };
          })
          .sort((a, b) => b.score - a.score)
          .slice(0, r.k);
        return { results, took: 1 };
      },
    };
  });
}

async function navigateToVector(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await expect(sidebar.getByText("test-vectors")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await sidebar.getByText("test-vectors").click();
  await expect(
    page.locator(".breadcrumbs").getByText("test-vectors", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("tab", { name: "Vectors", exact: true }).click();
}

async function dismissToasts(
  page: import("@playwright/test").Page,
): Promise<void> {
  const dismissButtons = page.locator(".toast [aria-label='Dismiss']");
  const count = await dismissButtons.count();
  for (let i = 0; i < count; i++) {
    try {
      await dismissButtons.nth(i).click({ timeout: 500, force: true });
    } catch {
      // Toast may have already auto-dismissed
    }
  }
  await expect(dismissButtons).toHaveCount(0, { timeout: 5_000 });
}

async function createNamespace(
  page: import("@playwright/test").Page,
  name: string,
  dimensions: string,
): Promise<void> {
  await page.getByLabel("Create namespace").click();
  const modal = page.locator(".modal");
  await expect(modal.getByRole("heading", { name: "Create Namespace" })).toBeVisible();
  await modal.locator("input[placeholder='Namespace name']").fill(name);
  await modal.locator("input[type='number']").fill(dimensions);
  await modal.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText(`Namespace "${name}" created.`)).toBeVisible({ timeout: 5_000 });
  await dismissToasts(page);
}

async function selectNamespace(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.locator("select").first().selectOption(name);
  await expect(page.locator("select").first()).toHaveValue(name, { timeout: 5_000 });
  await expect(page.locator("main").locator(".loading, .skeleton")).toHaveCount(0, { timeout: 5_000 });
}

async function upsertVector(
  page: import("@playwright/test").Page,
  id: string,
  values: string,
  metadata: string,
): Promise<void> {
  await page.getByRole("button", { name: "Upsert Vector" }).click();
  await expect(page.getByText("New Vector")).toBeVisible();
  await page.locator("input[placeholder='Unique ID']").fill(id);
  await page.locator("textarea[placeholder='0.1, 0.5, -0.3, 0.8, ...']").fill(values);
  await page.locator("textarea[placeholder='{\"label\": \"example\"}']").fill(metadata);
  await page.getByRole("button", { name: "Upsert", exact: true }).click();
  await expect(page.getByText(`Vector "${id}" upserted`)).toBeVisible({ timeout: 5_000 });
  await dismissToasts(page);
}

async function batchUpsert(
  page: import("@playwright/test").Page,
  json: string,
): Promise<import("@playwright/test").Locator> {
  await page.getByRole("button", { name: "Batch upsert" }).click();
  const panel = page.getByLabel("Batch Upsert");
  await expect(panel).toBeVisible();
  await panel.getByTestId("batch-upsert-input").fill(json);
  await panel.getByRole("button", { name: "Upsert batch" }).click();
  await expect(panel.getByTestId("batch-upsert-result")).toBeVisible({ timeout: 5_000 });
  return panel;
}

const threeVectors = JSON.stringify([
  { id: "b1", vector: [0.1, 0.2, 0.3], metadata: { color: "red" } },
  { id: "b2", vector: [0.4, 0.5, 0.6], metadata: { color: "blue" } },
  { id: "b3", vector: [0.7, 0.8, 0.9] },
]);

test.describe("Vector View - advanced operations", () => {
  test.use({ mockState: vectorState });

  test.beforeEach(async ({ page }) => {
    await injectStatefulVectorBinding(page);
    await navigateToVector(page);
    await createNamespace(page, "adv-ns", "3");
    await selectNamespace(page, "adv-ns");
  });

  test("row click loads the vector with getVector", async ({ page }) => {
    await upsertVector(page, "v1", "0.1, 0.2, 0.3", '{"label": "first"}');

    await page.getByText("v1", { exact: true }).first().click();
    const detail = page.getByLabel("Vector Details");
    await expect(detail).toBeVisible();
    await expect(detail.getByTestId("vector-detail-version")).toHaveText("1", { timeout: 5_000 });
    await expect(detail.getByTestId("vector-detail-created")).toHaveText("1700000001");
    await expect(detail.getByTestId("vector-detail-updated")).toHaveText("1700000001");
    await expect(detail.getByTestId("vector-detail-metadata-json")).toContainText('"label": "first"');
  });

  test("Save metadata calls updateMetadata and refreshes the detail", async ({ page }) => {
    await upsertVector(page, "v1", "0.1, 0.2, 0.3", '{"label": "first"}');
    await page.getByText("v1", { exact: true }).first().click();
    const detail = page.getByLabel("Vector Details");
    await expect(detail.getByTestId("vector-detail-version")).toHaveText("1", { timeout: 5_000 });

    await detail.getByTestId("vector-detail-metadata-input").fill('{"label": "second", "tier": 2}');
    await detail.getByRole("button", { name: "Save metadata" }).click();

    await expect(page.getByText('Metadata of vector "v1" updated.')).toBeVisible({ timeout: 5_000 });
    await expect(detail.getByTestId("vector-detail-version")).toHaveText("2");
    await expect(detail.getByTestId("vector-detail-metadata-json")).toContainText('"label": "second"');
    await expect(detail.getByTestId("vector-detail-metadata-json")).toContainText('"tier": 2');
  });

  test("Save metadata rejects JSON that is not an object", async ({ page }) => {
    await upsertVector(page, "v1", "0.1, 0.2, 0.3", "{}");
    await page.getByText("v1", { exact: true }).first().click();
    const detail = page.getByLabel("Vector Details");
    await expect(detail.getByTestId("vector-detail-version")).toHaveText("1", { timeout: 5_000 });

    await detail.getByTestId("vector-detail-metadata-input").fill("[1, 2]");
    await detail.getByRole("button", { name: "Save metadata" }).click();
    await expect(page.getByText("Metadata must be a JSON object.")).toBeVisible({ timeout: 5_000 });
    await expect(detail.getByTestId("vector-detail-version")).toHaveText("1");
  });

  test("batch upsert reports succeeded, failed and per-vector errors", async ({ page }) => {
    const mixed = JSON.stringify([
      { id: "b1", vector: [0.1, 0.2, 0.3] },
      { id: "b2", vector: [0.4, 0.5, 0.6], metadata: { color: "blue" } },
      { id: "bad", vector: [0.1, 0.2] },
    ]);
    const panel = await batchUpsert(page, mixed);
    await expect(panel.getByTestId("batch-upsert-succeeded")).toHaveText("2");
    await expect(panel.getByTestId("batch-upsert-failed")).toHaveText("1");
    await expect(panel.getByTestId("batch-upsert-errors")).toContainText("bad: dimension 2 != 3");
    await expect(page.getByText("Batch upsert: 2 succeeded, 1 failed.")).toBeVisible();

    await panel.getByLabel("Close", { exact: true }).click();
    await expect(page.getByText("b1", { exact: true })).toBeVisible();
    await expect(page.getByText("b2", { exact: true })).toBeVisible();
    await expect(page.getByText("bad", { exact: true })).toHaveCount(0);
  });

  test("bulk delete removes the selected vectors with batchDeleteVectors", async ({ page }) => {
    const panel = await batchUpsert(page, threeVectors);
    await expect(panel.getByTestId("batch-upsert-succeeded")).toHaveText("3");
    await panel.getByLabel("Close", { exact: true }).click();
    await dismissToasts(page);

    for (const id of ["b1", "b3"]) {
      await page.getByRole("row").filter({ hasText: id }).getByRole("checkbox").check();
    }
    await page.getByRole("button", { name: /^Delete(?! namespace)/ }).first().click();
    const confirm = page.locator(".modal");
    if (await confirm.count()) {
      await confirm.getByRole("button", { name: /Delete/ }).last().click();
    }

    await expect(page.getByText("Deleted 2 vectors")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("b1", { exact: true })).toHaveCount(0);
    await expect(page.getByText("b3", { exact: true })).toHaveCount(0);
    await expect(page.getByText("b2", { exact: true })).toBeVisible();
  });

  test("hybrid search ranks the metadata match first", async ({ page }) => {
    const panel = await batchUpsert(page, threeVectors);
    await panel.getByLabel("Close", { exact: true }).click();
    await dismissToasts(page);

    await page.getByRole("tab", { name: "Search" }).click();
    const card = page.getByTestId("hybrid-search-card");
    await card.getByLabel("Hybrid vector", { exact: true }).fill("0.1, 0.2, 0.3");
    await card.getByLabel("Hybrid metadata filter").fill('{"color": "blue"}');
    await card.getByLabel("Hybrid k").fill("2");
    await card.getByLabel("Hybrid vector weight").fill("0.6");
    await card.getByLabel("Hybrid metadata weight").fill("0.4");
    await card.getByRole("button", { name: "Hybrid search" }).click();

    const rows = page.getByRole("row").filter({ hasText: /^.*b\d/ });
    await expect(rows).toHaveCount(2, { timeout: 5_000 });
    await expect(rows.first()).toContainText("b2");
  });

  test("hybrid search failure shows the error and leaves results empty", async ({ page }) => {
    await upsertVector(page, "v1", "0.1, 0.2, 0.3", "{}");
    await page.getByRole("tab", { name: "Search" }).click();

    await injectError(page, "VectorBinding.HybridSearch", "hybrid index offline");
    const card = page.getByTestId("hybrid-search-card");
    await card.getByLabel("Hybrid vector", { exact: true }).fill("0.1, 0.2, 0.3");
    await card.getByRole("button", { name: "Hybrid search" }).click();
    await expect(page.getByText("hybrid index offline")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("row").filter({ hasText: "v1" })).toHaveCount(0);

    await clearError(page, "VectorBinding.HybridSearch");
    await dismissToasts(page);
    await card.getByRole("button", { name: "Hybrid search" }).click();
    await expect(page.getByRole("row").filter({ hasText: "v1" })).toHaveCount(1, { timeout: 5_000 });
  });

  test("getVector failure is shown inside the detail panel", async ({ page }) => {
    await upsertVector(page, "v1", "0.1, 0.2, 0.3", "{}");
    await injectError(page, "VectorBinding.GetVector", "vector store unavailable");
    await page.getByText("v1", { exact: true }).first().click();
    const detail = page.getByLabel("Vector Details");
    await expect(detail.getByTestId("vector-detail-error")).toContainText("vector store unavailable", {
      timeout: 5_000,
    });
    await expect(detail.getByTestId("vector-detail-version")).toHaveCount(0);
  });
});
