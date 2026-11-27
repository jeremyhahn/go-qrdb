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
 * Regression tests for the vector namespace display bug.
 *
 * The bug: the frontend used `.name` instead of `.namespace` when extracting
 * the namespace name from objects returned by the API, causing the namespace
 * dropdown and table to show blank/undefined text instead of the actual name.
 *
 * Fix verified: after CreateNamespace, ListNamespaces returns objects with a
 * `.namespace` field, and the UI must render that string—not the raw object.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const vectorState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-vectors", backend: "pebble", status: "open" },
  ],
};

/**
 * Inject a stateful VectorBinding whose ListNamespaces returns objects
 * shaped as { namespace: string, ... } — matching the real API response.
 * This is the shape that exposed the .name vs .namespace field bug.
 */
async function injectStatefulVectorBinding(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;

    interface NamespaceInfo {
      namespace: string;
      dimension: number;
      distanceMetric: number;
      indexType: number;
      vectorCount: number;
    }

    const namespaceStore = new Map<string, NamespaceInfo>();
    const vectorStore = new Map<string, Map<string, { id: string; vector: number[]; metadata: Record<string, unknown> }>>();

    function getState(): { errors: Record<string, string> } {
      return w.__QRDB_MOCK_STATE__ as { errors: Record<string, string> };
    }

    function maybeReject(method: string): void {
      const s = getState();
      if (s.errors[method]) {
        throw new Error(s.errors[method]);
      }
    }

    // Held calls, keyed "Method:namespace": __VEC_HOLD__(key) makes the next
    // such call wait until __VEC_RELEASE__(key). __VEC_ARRIVED__ lists the
    // held calls that are waiting; __VEC_RETURNED__ those released.
    const gates = new Map<string, { wait: Promise<void>; release: () => void }>();
    const arrived: string[] = [];
    const returned: string[] = [];
    w.__VEC_ARRIVED__ = arrived;
    w.__VEC_RETURNED__ = returned;
    w.__VEC_HOLD__ = (key: string) => {
      let release = (): void => {};
      const wait = new Promise<void>((resolve) => {
        release = resolve;
      });
      gates.set(key, { wait, release });
    };
    w.__VEC_RELEASE__ = (key: string) => {
      gates.get(key)?.release();
    };

    async function gate(method: string, namespace: string): Promise<void> {
      const key = `${method}:${namespace}`;
      const g = gates.get(key);
      if (!g) return;
      arrived.push(key);
      await g.wait;
      gates.delete(key);
      returned.push(key);
    }

    const waitForGo = setInterval(() => {
      const go = w.go as Record<string, Record<string, Record<string, unknown>>> | undefined;
      if (!go?.main?.VectorBinding) return;
      clearInterval(waitForGo);

      go.main.VectorBinding = {
        CreateNamespace: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.CreateNamespace");
          const r = req as { namespace: string; dimension?: number; distanceMetric?: number; indexType?: number };
          if (namespaceStore.has(r.namespace)) {
            throw new Error(`namespace "${r.namespace}" already exists`);
          }
          const info: NamespaceInfo = {
            namespace: r.namespace,
            dimension: r.dimension ?? 128,
            distanceMetric: r.distanceMetric ?? 0,
            indexType: r.indexType ?? 0,
            vectorCount: 0,
          };
          namespaceStore.set(r.namespace, info);
          vectorStore.set(r.namespace, new Map());
          return {};
        },

        DeleteNamespace: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.DeleteNamespace");
          const r = req as { namespace: string };
          namespaceStore.delete(r.namespace);
          vectorStore.delete(r.namespace);
        },

        // Return objects shaped as { namespace: string, ... } — the real API shape
        // that exposed the .name vs .namespace bug.
        ListNamespaces: async () => {
          maybeReject("VectorBinding.ListNamespaces");
          return {
            namespaces: Array.from(namespaceStore.values()).map((info) => ({
              namespace: info.namespace,
              dimension: info.dimension,
              distanceMetric: info.distanceMetric,
              indexType: info.indexType,
              vectorCount: info.vectorCount,
            })),
          };
        },

        GetNamespaceInfo: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.GetNamespaceInfo");
          const r = req as { namespace: string };
          await gate("GetNamespaceInfo", r.namespace);
          const info = namespaceStore.get(r.namespace);
          if (!info) throw new Error(`namespace "${r.namespace}" not found`);
          const vecs = vectorStore.get(r.namespace);
          return {
            namespace: info.namespace,
            dimension: info.dimension,
            distanceMetric: "cosine",
            indexType: "flat",
            vectorCount: vecs ? vecs.size : 0,
          };
        },

        UpsertVector: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.UpsertVector");
          const r = req as { namespace: string; id: string; vector: number[]; metadata?: Record<string, unknown> };
          let vecs = vectorStore.get(r.namespace);
          if (!vecs) {
            vecs = new Map();
            vectorStore.set(r.namespace, vecs);
          }
          vecs.set(r.id, { id: r.id, vector: r.vector, metadata: r.metadata ?? {} });
          const info = namespaceStore.get(r.namespace);
          if (info) {
            info.vectorCount = vecs.size;
          }
          return {};
        },

        GetVector: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.GetVector");
          const r = req as { namespace: string; id: string };
          const vecs = vectorStore.get(r.namespace);
          if (!vecs) throw new Error(`namespace "${r.namespace}" not found`);
          const vec = vecs.get(r.id);
          if (!vec) throw new Error(`vector "${r.id}" not found`);
          return { ...vec };
        },

        DeleteVector: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.DeleteVector");
          const r = req as { namespace: string; id: string };
          const vecs = vectorStore.get(r.namespace);
          if (vecs) {
            vecs.delete(r.id);
            const info = namespaceStore.get(r.namespace);
            if (info) info.vectorCount = vecs.size;
          }
        },

        BatchUpsert: async (_database: unknown, req: unknown) => {
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
          if (info) info.vectorCount = vecs.size;
          // The Go BatchUpsertResponse.
          return { ids: r.vectors.map((v) => v.id), success: true, inserted_count: r.vectors.length };
        },

        BatchDeleteVectors: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.BatchDeleteVectors");
          const r = req as { namespace: string; ids: string[] };
          const vecs = vectorStore.get(r.namespace);
          // The Go BatchDeleteVectorsResponse: deleted_count and failed_ids.
          let deletedCount = 0;
          const failedIds: string[] = [];
          if (vecs) {
            for (const id of r.ids) {
              if (vecs.delete(id)) deletedCount++;
              else failedIds.push(id);
            }
            const info = namespaceStore.get(r.namespace);
            if (info) info.vectorCount = vecs.size;
          }
          return { deleted_count: deletedCount, failed_ids: failedIds, success: true };
        },

        ListVectors: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.ListVectors");
          const r = req as { namespace: string; limit?: number; offset?: number };
          await gate("ListVectors", r.namespace);
          const vecs = vectorStore.get(r.namespace);
          if (!vecs) return { vectors: [], total: 0 };
          const all = Array.from(vecs.values());
          const offset = r.offset ?? 0;
          const limit = r.limit ?? 100;
          return { vectors: all.slice(offset, offset + limit), total: all.length };
        },

        Search: async (req: unknown) => {
          maybeReject("VectorBinding.Search");
          const r = req as { namespace: string; vector: number[]; k?: number; limit?: number };
          const vecs = vectorStore.get(r.namespace);
          if (!vecs) return { results: [] };
          return {
            results: Array.from(vecs.values())
              .slice(0, r.k ?? r.limit ?? 10)
              .map((v, i) => ({ id: v.id, score: 1.0 - i * 0.05, metadata: v.metadata })),
          };
        },

        HybridSearch: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.HybridSearch");
          // transport.HybridSearchRequest: query_vector is required.
          const r = req as { namespace: string; query_vector?: number[]; k?: number };
          if (!Array.isArray(r.query_vector) || r.query_vector.length === 0) {
            throw new Error("query vector is required");
          }
          const vecs = vectorStore.get(r.namespace);
          if (!vecs) return { results: [] };
          return {
            results: Array.from(vecs.values())
              .slice(0, r.k ?? 10)
              .map((v, i) => ({ id: v.id, score: 0.9 - i * 0.05, metadata: v.metadata })),
          };
        },

        SearchByID: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.SearchByID");
          const r = req as { namespace: string; id: string; k: number };
          const vecs = vectorStore.get(r.namespace);
          if (!vecs) return { results: [] };
          return {
            results: Array.from(vecs.values())
              .filter((v) => v.id !== r.id)
              .slice(0, r.k ?? 10)
              .map((v, i) => ({ id: v.id, score: 0.95 - i * 0.1, metadata: v.metadata })),
          };
        },

        RebuildIndex: async () => {
          maybeReject("VectorBinding.RebuildIndex");
          return { jobId: "rebuild-1" };
        },

        GetRebuildProgress: async () => {
          maybeReject("VectorBinding.GetRebuildProgress");
          return { progress: 100, status: "completed" };
        },

        GetIndexStats: async (_database: unknown, req: unknown) => {
          maybeReject("VectorBinding.GetIndexStats");
          const r = req as { namespace: string };
          await gate("GetIndexStats", r.namespace);
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

        CompactNamespace: async () => {
          maybeReject("VectorBinding.CompactNamespace");
          return { jobId: "compact-1" };
        },

        GetCompactionProgress: async () => {
          maybeReject("VectorBinding.GetCompactionProgress");
          return { progress: 100, status: "completed" };
        },
      };
    }, 0);
  });
}

/** Navigate to the vector database workspace. */
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

/** Navigate and inject stateful binding. */
async function setupVector(
  page: import("@playwright/test").Page,
): Promise<void> {
  await injectStatefulVectorBinding(page);
  await navigateToVector(page);
}

/** Create a namespace via the UI modal. */
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
}

// ---------------------------------------------------------------------------
// Regression: .name vs .namespace field extraction
// ---------------------------------------------------------------------------

test.describe("Vector Namespace Display - Regression (.namespace field fix)", () => {
  test.use({ mockState: vectorState });

  test("namespace dropdown shows string name not blank after create", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "test-ns");

    // The dropdown option must display the literal string "test-ns", not blank
    // or "[object Object]" as would happen when using .name on the API object.
    const selectEl = page.locator("select").first();
    await expect(selectEl).toContainText("test-ns");

    // Verify the option text is not undefined/blank: the select element should
    // have at least one option whose text content is exactly "test-ns".
    const optionText = await selectEl.locator("option[value='test-ns']").textContent();
    expect(optionText?.trim()).toBe("test-ns");
  });

  test("namespace dropdown does not show undefined or raw JSON object text", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "test-ns");

    const selectEl = page.locator("select").first();
    // The rendered text must not contain "[object" which would indicate the
    // namespace object was stringified instead of its .namespace field used.
    const allText = await selectEl.textContent();
    expect(allText).not.toContain("[object");
    expect(allText).not.toContain("undefined");
    expect(allText).toContain("test-ns");
  });

  test("namespace table shows string name not raw JSON object", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "test-ns");

    // The table body should contain the literal string "test-ns".
    const tableBody = page.locator("table tbody");
    await expect(tableBody).toBeVisible({ timeout: LOAD_TIMEOUT });

    const cellText = await tableBody.textContent();
    expect(cellText).not.toContain("[object");
    expect(cellText).not.toContain("undefined");
    expect(cellText).toContain("test-ns");
  });

  test("namespace table row displays namespace name as string", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "test-ns");

    // There must be a cell in the table containing exactly "test-ns".
    await expect(
      page.locator("table tbody").getByText("test-ns"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("selecting a namespace from dropdown populates the selected value", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "test-ns");

    const selectEl = page.locator("select").first();
    await selectEl.selectOption("test-ns");

    // The select value must be the namespace string, not an object serialization.
    await expect(selectEl).toHaveValue("test-ns");
  });

  test("creating a new namespace adds it to the dropdown", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "ns-one");

    const selectEl = page.locator("select").first();
    await expect(selectEl).toContainText("ns-one");

    // Create a second namespace and confirm both appear.
    await createNamespace(page, "ns-two");
    await expect(selectEl).toContainText("ns-one");
    await expect(selectEl).toContainText("ns-two");
  });

  test("creating a new namespace adds it to the table", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "ns-alpha");

    const tableBody = page.locator("table tbody");
    await expect(tableBody.getByText("ns-alpha")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("multiple namespaces all render as strings in dropdown", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "embed-v1");
    await createNamespace(page, "embed-v2");
    await createNamespace(page, "embed-v3");

    const selectEl = page.locator("select").first();
    const allText = await selectEl.textContent();

    // None of the three names may appear as an object serialization.
    expect(allText).not.toContain("[object");
    expect(allText).toContain("embed-v1");
    expect(allText).toContain("embed-v2");
    expect(allText).toContain("embed-v3");
  });

  test("clicking a namespace row in the table selects it in the dropdown", async ({ page }) => {
    await setupVector(page);
    await createNamespace(page, "click-ns");

    // Click the namespace row in the DataTable.
    await page.locator("table").getByText("click-ns").click();

    // Dropdown must now show the namespace string as the selected value.
    await expect(page.locator("select").first()).toHaveValue("click-ns");
  });
});

// A response applies only while its namespace selection is current: a late
// reply for namespace A must not overwrite what is shown for namespace B.
test.describe("Vector namespace selection - stale responses", () => {
  test.use({ mockState: vectorState });

  type HeldWindow = {
    __VEC_HOLD__: (key: string) => void;
    __VEC_RELEASE__: (key: string) => void;
    __VEC_ARRIVED__: string[];
    __VEC_RETURNED__: string[];
    go: { main: { VectorBinding: Record<string, (db: unknown, req: unknown) => Promise<unknown>> } };
  };

  async function seedTwoNamespaces(page: import("@playwright/test").Page): Promise<void> {
    await setupVector(page);
    await createNamespace(page, "ns-a", "3");
    await createNamespace(page, "ns-b", "5");
    await page.evaluate(async () => {
      const w = window as unknown as HeldWindow;
      await w.go.main.VectorBinding.UpsertVector("", { namespace: "ns-a", id: "a-vec", vector: [0.1, 0.2, 0.3] });
      await w.go.main.VectorBinding.UpsertVector("", {
        namespace: "ns-b",
        id: "b-vec",
        vector: [0.1, 0.2, 0.3, 0.4, 0.5],
      });
    });
  }

  async function hold(page: import("@playwright/test").Page, key: string): Promise<void> {
    await page.evaluate((k) => (window as unknown as HeldWindow).__VEC_HOLD__(k), key);
  }

  async function release(page: import("@playwright/test").Page, key: string): Promise<void> {
    await page.evaluate((k) => (window as unknown as HeldWindow).__VEC_RELEASE__(k), key);
  }

  async function waitArrived(page: import("@playwright/test").Page, key: string): Promise<void> {
    await page.waitForFunction((k) => (window as unknown as HeldWindow).__VEC_ARRIVED__.includes(k), key);
  }

  async function waitReturned(page: import("@playwright/test").Page, key: string): Promise<void> {
    await page.waitForFunction((k) => (window as unknown as HeldWindow).__VEC_RETURNED__.includes(k), key);
  }

  function statValue(page: import("@playwright/test").Page, label: string) {
    return page
      .locator("div.surface-panel.rounded-lg.p-4", { hasText: label })
      .locator("span.font-mono.text-2xl");
  }

  test("a late vector list and namespace info for the previous namespace are dropped", async ({ page }) => {
    await seedTwoNamespaces(page);
    const select = page.locator("select").first();

    await hold(page, "ListVectors:ns-a");
    await hold(page, "GetNamespaceInfo:ns-a");
    await select.selectOption("ns-a");
    await waitArrived(page, "ListVectors:ns-a");

    await select.selectOption("ns-b");
    await expect(page.locator("table").getByText("b-vec")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(statValue(page, "Dimensions")).toHaveText("5");

    await release(page, "ListVectors:ns-a");
    await release(page, "GetNamespaceInfo:ns-a");
    await waitReturned(page, "ListVectors:ns-a");

    await expect(select).toHaveValue("ns-b");
    await expect(page.locator("table").getByText("a-vec")).toHaveCount(0);
    await expect(page.locator("table").getByText("b-vec")).toBeVisible();
    await expect(statValue(page, "Dimensions")).toHaveText("5");
    await expect(statValue(page, "Vectors")).toHaveText("1");
  });

  test("late index stats for the previous namespace are dropped", async ({ page }) => {
    await seedTwoNamespaces(page);
    const select = page.locator("select").first();

    // Hold A's list so the stats request is sent while A is still loading.
    await hold(page, "ListVectors:ns-a");
    await select.selectOption("ns-a");
    await waitArrived(page, "ListVectors:ns-a");

    await page.getByRole("tab", { name: "Maintenance" }).click();
    await hold(page, "GetIndexStats:ns-a");
    await page.getByRole("button", { name: "Load Stats" }).click();
    await waitArrived(page, "GetIndexStats:ns-a");

    // A's list and info finish; the selector is usable while A's stats wait.
    await release(page, "ListVectors:ns-a");
    await waitReturned(page, "ListVectors:ns-a");
    await expect(select).toBeEnabled();
    await select.selectOption("ns-b");
    await expect(select).toBeEnabled();

    await release(page, "GetIndexStats:ns-a");
    await waitReturned(page, "GetIndexStats:ns-a");
    await expect(page.getByText("Total Vectors")).toHaveCount(0);

    await page.getByRole("button", { name: "Load Stats" }).click();
    await expect(statValue(page, "Total Vectors")).toHaveText("1");
    await expect(statValue(page, "Dimensions")).toHaveText("5");
  });
});
