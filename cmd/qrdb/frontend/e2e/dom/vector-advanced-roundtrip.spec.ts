/**
 * DOM tier: advanced vector operations through VectorView against a live
 * daemon (coverage-matrix group 8): batchUpsert, getVector, updateMetadata,
 * hybridSearch and batchDeleteVectors. After each UI step the spec reads the
 * server over REST, on the same per-database base URL the UI itself uses.
 */
import { test, expect, type APIRequestContext, type Page, type Request } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon";

const LOAD_TIMEOUT = 30_000;

let daemon: DomDaemon | undefined;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  if (daemon) await stopDaemon(daemon);
  daemon = undefined;
});

interface VectorBase {
  base: string;
  headers: Record<string, string>;
}

/** Records the base URL and routing headers of the UI's vector requests. */
function captureVectorBase(page: Page): () => VectorBase {
  let captured: VectorBase | undefined;
  page.on("request", (req: Request) => {
    const url = req.url();
    const at = url.indexOf("/vector/");
    if (at < 0 || captured) return;
    const headers: Record<string, string> = {};
    for (const [k, v] of Object.entries(req.headers())) {
      const key = k.toLowerCase();
      if (key.startsWith("x-") || key === "authorization") headers[k] = v;
    }
    // The UI talks through the dev-server proxy; read the daemon directly.
    const path = new URL(url.slice(0, at)).pathname.replace(/\/+$/, "");
    captured = { base: `${new URL(daemon!.adminUrl).origin}${path}`, headers };
  });
  return () => {
    if (!captured) throw new Error("the UI sent no /vector/ request yet");
    return captured;
  };
}

async function getVector(
  request: APIRequestContext,
  vb: VectorBase,
  id: string,
): Promise<{ status: number; body: Record<string, unknown> }> {
  const url = `${vb.base}/vector/get?namespace=docs&id=${encodeURIComponent(id)}`;
  const resp = await request.get(url, { headers: vb.headers, ignoreHTTPSErrors: true });
  const text = await resp.text();
  let body: Record<string, unknown> = {};
  try {
    body = JSON.parse(text) as Record<string, unknown>;
  } catch {
    body = { raw: text };
  }
  return { status: resp.status(), body };
}

async function expectToast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("advanced vector operations round-trip through VectorView against the live backend", async ({
  page,
  request,
}) => {
  test.setTimeout(180_000);
  const vectorBase = captureVectorBase(page);
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Create a Vector database through the real wizard. -----------------
  await page.locator("aside").getByText("New Database").click();
  await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("#db-name").fill("embeddings");
  await page.locator("button").filter({ hasText: "Vector" }).click();
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: /Create/ }).click();
  await expect(page.locator("aside").getByText("embeddings", { exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Create a namespace through the real REST path. --------------------
  await page.locator("aside").getByText("embeddings", { exact: true }).click();
  await page.getByRole("tab", { name: "Vectors", exact: true }).click();
  await page
    .getByRole("button", { name: /Create Namespace|New Namespace/i })
    .first()
    .click();
  const modal = page.locator(".modal");
  await expect(modal.getByRole("heading", { name: "Create Namespace" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await modal.locator("input[placeholder='Namespace name']").fill("docs");
  await modal.locator("input[type='number']").fill("4");
  await modal.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText('Namespace "docs" created.')).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  await page.locator("select").first().selectOption("docs");
  await expect(page.locator("select").first()).toHaveValue("docs", { timeout: LOAD_TIMEOUT });

  // --- batchUpsert: three vectors through the Batch upsert panel. --------
  await page.getByRole("button", { name: "Batch upsert" }).click();
  await page.getByTestId("batch-upsert-input").fill(
    JSON.stringify([
      { id: "11", vector: [1, 0, 0, 0], metadata: { label: "alpha" } },
      { id: "12", vector: [0, 1, 0, 0], metadata: { label: "beta" } },
      { id: "13", vector: [0, 0, 1, 0], metadata: { label: "gamma" } },
    ]),
  );
  await page.getByRole("button", { name: "Upsert batch" }).click();
  await expect(page.getByTestId("batch-upsert-succeeded")).toHaveText("3", { timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("batch-upsert-failed")).toHaveText("0");
  await expectToast(page, "Batch upsert: 3 succeeded.");
  await page.getByRole("button", { name: "Close", exact: true }).first().click();

  const vb = vectorBase();
  for (const id of ["11", "12", "13"]) {
    const got = await getVector(request, vb, id);
    expect(got.status, JSON.stringify(got.body)).toBe(200);
  }
  // --- Namespace cards and the Dimensions column follow the server. -----
  const infoResp = await request.get(`${vb.base}/vector/namespace/info?namespace=docs`, {
    headers: vb.headers,
    ignoreHTTPSErrors: true,
  });
  expect(infoResp.status()).toBe(200);
  const info = (await infoResp.json()) as {
    dimension: number;
    vector_count: number;
    distance_metric: string;
    index_type: string;
  };
  expect(info).toMatchObject({ dimension: 4, vector_count: 3, distance_metric: "COSINE", index_type: "HNSW" });
  const statCard = (label: string) =>
    page
      .locator(".surface-panel")
      .filter({ has: page.getByText(label, { exact: true }) })
      .locator("span.font-mono")
      .first();
  await expect(statCard("Vectors")).toHaveText(String(info.vector_count), { timeout: LOAD_TIMEOUT });
  await expect(statCard("Dimensions")).toHaveText(String(info.dimension));
  await expect(statCard("Distance Metric")).toHaveText(/^cosine$/i);
  await expect(statCard("Index Type")).toHaveText(info.index_type);
  const dimCells = page.locator("table tbody tr");
  await expect(dimCells).toHaveCount(3, { timeout: LOAD_TIMEOUT });
  for (const row of await dimCells.all()) {
    await expect(row.locator("td").filter({ hasText: new RegExp(`^\\s*${info.dimension}\\s*$`) })).toHaveCount(1);
  }

  // --- getVector: open vector 12; the detail panel loads it by id. -------
  const table = page.locator("table");
  await expect(table.getByText("13", { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await table.getByText("12", { exact: true }).first().click();
  await expect(page.getByTestId("vector-detail")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("vector-detail-version")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("vector-detail-metadata-json")).toContainText("beta");

  // --- updateMetadata: edit and save the metadata of vector 12. ----------
  await page.getByTestId("vector-detail-metadata-input").fill('{"label": "beta", "tier": "gold"}');
  await page.getByRole("button", { name: "Save metadata" }).click();
  await expectToast(page, 'Metadata of vector "12" updated.');
  await expect(page.getByTestId("vector-detail-metadata-json")).toContainText("gold", {
    timeout: LOAD_TIMEOUT,
  });
  await expect
    .poll(async () => JSON.stringify((await getVector(request, vb, "12")).body), { timeout: LOAD_TIMEOUT })
    .toContain("gold");
  await page.getByRole("button", { name: "Close", exact: true }).locator("visible=true").first().click();
  await expect(page.getByTestId("vector-detail")).toBeHidden({ timeout: LOAD_TIMEOUT });

  // --- hybridSearch: vector similarity plus the metadata match. ----------
  await page.getByRole("tab", { name: "Search" }).click();
  const hybrid = page.getByTestId("hybrid-search-card");
  await hybrid.getByLabel("Hybrid vector", { exact: true }).fill("0, 1, 0, 0");
  await hybrid.getByLabel("Hybrid metadata filter").fill('{"tier": "gold"}');
  await hybrid.getByLabel("Hybrid k", { exact: true }).fill("3");
  await hybrid.getByRole("button", { name: "Hybrid search" }).click();
  await expect(page.locator("table").getByText("12", { exact: true }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  const hybridResp = await request.post(`${vb.base}/vector/search/hybrid`, {
    headers: vb.headers,
    ignoreHTTPSErrors: true,
    data: {
      namespace: "docs",
      query_vector: [0, 1, 0, 0],
      metadata_filter: { tier: "gold" },
      k: 3,
    },
  });
  const hybridBody = await hybridResp.text();
  expect(hybridResp.status(), hybridBody).toBe(200);
  expect(hybridBody).toContain("12");

  // --- batchDeleteVectors: select 11 and 13 and delete them. -------------
  await page.getByRole("tab", { name: "Browse" }).click();
  const rows = page.locator("table tbody tr");
  for (const id of ["11", "13"]) {
    await rows
      .filter({ has: page.getByText(id, { exact: true }) })
      .first()
      .locator("input[type='checkbox']")
      .check();
  }
  // The table's own Delete button carries the selection count badge; the
  // red icon beside the namespace selector deletes the whole namespace.
  const deleteSelected = page.locator("button.btn-error", { hasText: "Delete" }).filter({
    has: page.locator(".badge"),
  });
  await expect(deleteSelected).toContainText("2");
  await deleteSelected.click();
  await page.locator(".modal").getByRole("button", { name: "Delete", exact: true }).click();
  await expectToast(page, "Deleted 2 vectors");

  for (const id of ["11", "13"]) {
    expect((await getVector(request, vb, id)).status).toBe(404);
  }
  expect((await getVector(request, vb, "12")).status).toBe(200);
});
