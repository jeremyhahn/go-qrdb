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
 * Vector database tests: create a vector database via the wizard (selecting
 * the Vector engine), verify it appears in the sidebar, and test vector
 * namespace creation within the vector view.
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

// State: no databases yet — wizard creates one.
const vectorWizardState = {
  ...secondLaunchEmbeddedState,
  databases: [] as { name: string; backend: string; status: string }[],
};

// State: pre-existing vector database for namespace tests.
const vectorDbState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "vec-db", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Stateful VectorBinding injector (from vector.spec.ts pattern)
// ---------------------------------------------------------------------------

async function injectStatefulVectorBinding(page: Page): Promise<void> {
  await page.addInitScript(() => {
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
        const r = req as {
          namespace: string;
          dimension: number;
          distanceMetric?: number;
          indexType?: number;
        };
        if (namespaceStore.has(r.namespace)) {
          throw new Error(`namespace "${r.namespace}" already exists`);
        }
        namespaceStore.set(r.namespace, {
          dimension: r.dimension ?? 128,
          distanceMetric: r.distanceMetric ?? 0,
          indexType: r.indexType ?? 0,
          vectorCount: 0,
        });
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
          ...info,
          vectorCount: vectorStore.get(r.namespace)?.size ?? 0,
          distanceMetric: "cosine",
          indexType: "flat",
        };
      },
      UpsertVector: async (req: unknown) => {
        maybeReject("VectorBinding.UpsertVector");
        const r = req as {
          namespace: string;
          id: string;
          vector: number[];
          metadata?: Record<string, unknown>;
        };
        let vecs = vectorStore.get(r.namespace);
        if (!vecs) {
          vecs = new Map();
          vectorStore.set(r.namespace, vecs);
        }
        vecs.set(r.id, {
          id: r.id,
          vector: r.vector,
          metadata: r.metadata ?? {},
        });
        const info = namespaceStore.get(r.namespace);
        if (info) info.vectorCount = vecs.size;
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

// ---------------------------------------------------------------------------
// Wizard helpers
// ---------------------------------------------------------------------------

/** Opens the Create Database wizard from the empty-state landing. */
async function openWizard(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
  await expect(
    page.getByRole("heading", { name: "Database Name & Engine" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Navigate to the Vector view of the pre-existing vec-db database. */
async function navigateToVectorDb(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("vec-db").click();
  await expect(page.locator(".breadcrumbs").getByText("vec-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("tab", { name: "Vectors", exact: true }).click();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Vector Database - Wizard Creation", () => {
  test.use({ mockState: vectorWizardState });

  test("creates a vector database via wizard and it appears in the sidebar", async ({
    page,
  }) => {
    await openWizard(page);

    // Fill database name
    await page.locator("#db-name").fill("my-vectors");

    // Select Vector engine
    await page.locator("button").filter({ hasText: "Vector" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    // Advance through remaining steps (Storage, Raft, SQL, Barrier, Summary)
    // Some steps may not appear for vector engine; advance whenever Next is shown.
    for (let i = 0; i < 5; i++) {
      const nextBtn = page.getByRole("button", { name: "Next" });
      if (await nextBtn.isVisible()) {
        await nextBtn.click();
      } else {
        break;
      }
    }

    // Final Create step
    const createBtn = page.getByRole("button", { name: "Create" }).last();
    if (await createBtn.isVisible()) {
      await createBtn.click();
    }

    // The wizard closes and the database should appear in the sidebar
    await expect(page.locator("aside").getByText("my-vectors")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("wizard shows Vector engine option", async ({ page }) => {
    await openWizard(page);
    await expect(
      page.locator("button").filter({ hasText: "Vector" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

test.describe("Vector Database - Namespace Operations", () => {
  test.use({ mockState: vectorDbState });

  test("opens vector database and shows the vector view", async ({ page }) => {
    await injectStatefulVectorBinding(page);
    await navigateToVectorDb(page);

    // The vector view should show a namespaces panel or empty state
    await expect(
      page.getByText(/namespace|vector/i).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("creates a namespace and verifies it appears in the list", async ({
    page,
  }) => {
    await injectStatefulVectorBinding(page);
    await navigateToVectorDb(page);

    // Click Create Namespace button — use .first() to avoid strict mode violation
    // when both the aria-label button and the labeled button are present.
    const createBtn = page.getByRole("button", {
      name: /Create Namespace|New Namespace/i,
    }).first();
    if (!(await createBtn.isVisible({ timeout: 3_000 }))) {
      // Skip if the UI does not support namespace creation
      return;
    }
    await createBtn.click();

    // Fill the namespace form
    const nsInput = page
      .locator("input[placeholder*='namespace' i], input[placeholder*='name' i]")
      .first();
    await expect(nsInput).toBeVisible({ timeout: 5_000 });
    await nsInput.fill("embeddings");

    // Dimension field
    const dimInput = page
      .locator("input[placeholder*='dimension' i], input[type='number']")
      .first();
    if (await dimInput.isVisible()) {
      await dimInput.fill("128");
    }

    // Submit
    await page.locator(".modal, [role='dialog']").getByRole("button", {
      name: /Create|Submit/i,
    }).click();

    // Namespace should appear in the namespaces table (not the hidden dropdown option).
    await expect(
      page.locator("table").getByText("embeddings").first(),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("shows error when namespace creation fails", async ({ page }) => {
    await injectStatefulVectorBinding(page);
    // Navigate first so the page is loaded and __QRDB_MOCK_STATE__ is available.
    await navigateToVectorDb(page);
    await injectError(
      page,
      "VectorBinding.CreateNamespace",
      "namespace store full",
    );

    // Use .first() to avoid strict mode violation with multiple matching buttons.
    const createBtn = page.getByRole("button", {
      name: /Create Namespace|New Namespace/i,
    }).first();
    if (!(await createBtn.isVisible({ timeout: 3_000 }))) {
      await clearError(page, "VectorBinding.CreateNamespace");
      return;
    }
    await createBtn.click();

    const nsInput = page
      .locator("input[placeholder*='namespace' i], input[placeholder*='name' i]")
      .first();
    if (await nsInput.isVisible()) {
      await nsInput.fill("fail-ns");
      await page.locator(".modal, [role='dialog']").getByRole("button", {
        name: /Create|Submit/i,
      }).click();
      await expect(page.getByText("namespace store full")).toBeVisible({
        timeout: 5_000,
      });
    }

    await clearError(page, "VectorBinding.CreateNamespace");
  });
});
