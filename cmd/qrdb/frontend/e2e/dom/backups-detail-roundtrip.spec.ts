// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * backups-detail-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Detail reads against a live daemon:
 *   - backups.get: BackupManager's detail panel shows the BackupInfo that
 *     GET /v1/backups/{id} returns for a backup created through the UI, and a
 *     delete through the panel removes it from the server's list.
 *   - collection.getDocument: CollectionView's Document Detail shows the body
 *     GET /v1/collections/{db}/{coll}/documents/{id} returns for a document
 *     inserted through the API.
 */

import {
  test,
  expect,
  request as playwrightRequest,
  type Page,
} from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

interface ApiResponse {
  status: number;
  text: string;
  json(): unknown;
}

/** Calls the daemon's REST API directly (self-signed TLS, auth off). */
async function api(
  path: string,
  init?: { method?: string; body?: string },
): Promise<ApiResponse> {
  const ctx = await playwrightRequest.newContext({ ignoreHTTPSErrors: true });
  try {
    const resp = await ctx.fetch(`${daemon!.adminUrl}${path}`, {
      method: init?.method ?? "GET",
      headers: { "Content-Type": "application/json" },
      data: init?.body,
    });
    const text = await resp.text();
    return { status: resp.status(), text, json: () => JSON.parse(text) };
  } finally {
    await ctx.dispose();
  }
}

async function openHome(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Reads the value cell of one row of the Backup Detail table. */
function detailCell(page: Page, label: string) {
  return page
    .locator(".card", {
      has: page.getByRole("heading", { name: "Backup Detail" }),
    })
    .locator("tr", {
      has: page.locator("th", { hasText: new RegExp(`^${label}$`) }),
    })
    .locator("td");
}

test("backup detail shows the server's BackupInfo and delete removes it", async ({
  page,
}) => {
  await openHome(page);
  await page
    .locator("aside")
    .getByText("Backups", { exact: true })
    .first()
    .click();
  await expect(page.getByRole("button", { name: "Create Backup" })).toBeVisible(
    {
      timeout: LOAD_TIMEOUT,
    },
  );

  // --- Create a backup through the UI. ------------------------------------
  const destination = `${daemon!.dataDir}/ui-backup-001`;
  await page.getByRole("button", { name: "Create Backup" }).click();
  const modal = page.locator(".modal", { hasText: "Create Backup" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.locator("input[placeholder^='/var/backups']").fill(destination);
  await modal.locator("select").nth(1).selectOption("none");
  await modal.getByRole("button", { name: "Create", exact: true }).click();
  const created = page
    .locator(".toast .alert")
    .filter({ hasText: /Backup ".+" created/ });
  await expect(created.first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  const id = /Backup "([^"]+)" created/.exec(
    (await created.first().textContent()) ?? "",
  )![1];

  // --- Open its detail; it must equal GET /v1/backups/{id}. ---------------
  await page.locator("table tbody tr", { hasText: id }).first().click();
  await expect(detailCell(page, "ID")).toHaveText(id, {
    timeout: LOAD_TIMEOUT,
  });
  const resp = await api(`/v1/backups/${encodeURIComponent(id)}`);
  expect(resp.status).toBe(200);
  const info = resp.json() as Record<string, unknown>;
  expect(info.id).toBe(id);
  await expect(detailCell(page, "Status")).toHaveText(String(info.status));
  await expect(detailCell(page, "Checksum")).toHaveText(
    String(info.checksum ?? ""),
  );
  await expect(detailCell(page, "Destination")).toHaveText(
    String(info.destination),
  );
  await expect(detailCell(page, "Compression")).toHaveText(
    String(info.compression),
  );
  await expect(detailCell(page, "Encryption")).toHaveText(
    String(info.encryption),
  );
  await expect(detailCell(page, "Shards")).toHaveText(
    String(info.shard_count ?? info.shardCount),
  );

  // --- Delete it through the panel; the server list no longer has it. -----
  await page
    .locator(".card", {
      has: page.getByRole("heading", { name: "Backup Detail" }),
    })
    .getByRole("button", { name: "Delete", exact: true })
    .click();
  await page
    .locator(".modal", { hasText: "Delete Backup" })
    .getByRole("button", { name: "Delete", exact: true })
    .click();
  await expect(
    page
      .locator(".toast .alert")
      .filter({ hasText: `Backup "${id}" deleted.` }),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.locator("table tbody tr", { hasText: id })).toHaveCount(0, {
    timeout: LOAD_TIMEOUT,
  });
  const list = (await api("/v1/backups/list")).json() as {
    backups?: { id: string }[];
  };
  expect((list.backups ?? []).map((b) => b.id)).not.toContain(id);
  expect((await api(`/v1/backups/${encodeURIComponent(id)}`)).status).toBe(404);
});

test("document detail shows the body collection.getDocument returns", async ({
  page,
}) => {
  // --- Create the collection and insert a document through the API. -------
  const db = "system";
  const create = await api("/v1/collections", {
    method: "POST",
    body: JSON.stringify({ database: db, name: "customers" }),
  });
  expect(create.status, create.text).toBeLessThan(300);
  const ins = await api(`/v1/collections/${db}/customers/documents`, {
    method: "POST",
    body: JSON.stringify({
      document: {
        name: "Ada Lovelace",
        email: "ada@example.com",
        tags: ["math", "engines"],
        born: 1815,
      },
    }),
  });
  expect(ins.status, ins.text).toBeLessThan(300);
  const docId = (ins.json() as { id: string }).id;
  const got = await api(
    `/v1/collections/${db}/customers/documents/${encodeURIComponent(docId)}`,
  );
  expect(got.status).toBe(200);
  const stored = got.json() as {
    id: string;
    document: Record<string, unknown>;
  };

  // --- Open the document in CollectionView. --------------------------------
  await openHome(page);
  await page.locator("aside").getByText(db, { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .locator("main nav")
    .getByText("Collections", { exact: true })
    .click();
  await page
    .getByRole("button", { name: /customers/ })
    .first()
    .click();
  await page.getByRole("tab", { name: "Documents" }).click();
  await page
    .locator("table tbody tr", { hasText: "Ada Lovelace" })
    .first()
    .click();

  const body = page.getByTestId("document-detail-body");
  await expect(body).toBeVisible({ timeout: LOAD_TIMEOUT });
  expect(JSON.parse((await body.textContent()) ?? "")).toEqual(stored.document);
  await expect(
    page
      .getByRole("dialog", { name: "Document Detail" })
      .getByText(stored.id, { exact: true }),
  ).toBeVisible();
});
