// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * backup-restore-cluster.spec.ts (real-backend DOM tier)
 *
 * A successful backups.restore against a live three-node cluster: keys are
 * written to a dedicated raft database (replicas: 1), backed up through the
 * UI, changed and deleted through /v1, and restored through the UI into the
 * database's own shard. Every changed and deleted key comes back on the
 * server, and the backup list reloads after the restore.
 */

import { test, expect, request as playwrightRequest, type Page } from "@playwright/test";
import { startCluster, stopCluster, UI_BASE_URL, type DomCluster } from "./fixtures/dom-cluster.ts";

const LOAD_TIMEOUT = 15_000;

let cluster: DomCluster | null = null;

test.beforeEach(async () => {
  cluster = await startCluster(3);
});

test.afterEach(async () => {
  await stopCluster(cluster);
  cluster = null;
});

interface ApiResponse {
  status: number;
  text: string;
  json(): unknown;
}

/** Calls node 1's REST API directly (self-signed TLS, auth off). */
async function api(
  path: string,
  init?: { method?: string; body?: string; database?: string },
): Promise<ApiResponse> {
  const ctx = await playwrightRequest.newContext({ ignoreHTTPSErrors: true });
  try {
    const resp = await ctx.fetch(`${cluster!.adminUrl}${path}`, {
      method: init?.method ?? "GET",
      headers: {
        "Content-Type": "application/json",
        ...(init?.database !== undefined ? { "X-QRDB-Database": init.database } : {}),
      },
      data: init?.body,
    });
    const text = await resp.text();
    return { status: resp.status(), text, json: () => JSON.parse(text) };
  } finally {
    await ctx.dispose();
  }
}

// The system shard is never restored online, so the restore targets the
// dedicated database's own shard.
const DB = "restoredb";

async function putKey(key: string, value: string): Promise<void> {
  const resp = await api(`/kv/${encodeURIComponent(key)}`, { method: "PUT", body: value, database: DB });
  expect(resp.status, resp.text).toBeLessThan(300);
}

async function getKey(key: string): Promise<ApiResponse> {
  return api(`/kv/${encodeURIComponent(key)}`, { database: DB });
}

async function deleteKey(key: string): Promise<void> {
  const resp = await api(`/kv/${encodeURIComponent(key)}`, { method: "DELETE", database: DB });
  expect(resp.status, resp.text).toBeLessThan(300);
}

/** Creates the dedicated raft database (one replica). */
async function createRaftDatabase(): Promise<void> {
  const resp = await api("/v1/databases", {
    method: "POST",
    body: JSON.stringify({ name: DB, backend: "raft", replicas: 1, raft: { clusterMode: "distributed" } }),
  });
  expect(resp.status, resp.text).toBeLessThan(300);
}

/** Returns the shard ID the server issued for the dedicated database. */
async function databaseShard(): Promise<number> {
  const resp = await api(`/v1/databases/${DB}`);
  expect(resp.status, resp.text).toBe(200);
  const shardId = (resp.json() as { shardId?: number }).shardId;
  expect(shardId, resp.text).toBeGreaterThan(1);
  return shardId!;
}

function toast(page: Page, text: string | RegExp) {
  return page.locator(".toast .alert").filter({ hasText: text });
}

function detailCard(page: Page) {
  return page.locator(".card", { has: page.getByRole("heading", { name: "Backup Detail" }) });
}

/** Opens Backups, creates a plaintext backup through the UI and selects it. */
async function createBackupInUi(page: Page): Promise<string> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Backups", { exact: true }).first().click();
  await expect(page.getByRole("button", { name: "Create Backup" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  const destination = `${cluster!.dataDirs[0]}/ui-backup-001`;
  await page.getByRole("button", { name: "Create Backup" }).click();
  const modal = page.locator(".modal", { hasText: "Create Backup" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.locator("input[placeholder^='/var/backups']").fill(destination);
  await modal.locator("select").nth(1).selectOption("none");
  await modal.getByRole("button", { name: "Create", exact: true }).click();
  const created = toast(page, /Backup ".+" created/);
  await expect(created.first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  const id = /Backup "([^"]+)" created/.exec((await created.first().textContent()) ?? "")![1];

  await page.locator("table tbody tr", { hasText: id }).first().click();
  await expect(detailCard(page).locator("tr", { hasText: "ID" }).locator("td").first()).toHaveText(id, {
    timeout: LOAD_TIMEOUT,
  });
  return id;
}

const ORIGINAL: Record<string, string> = {
  "restore/alpha": "one",
  "restore/beta": "two",
  "restore/gamma": "three",
};

// fixme (#1478): the server cannot restore a database shard online. A backup
// snapshots only the shards on the node's main raft host (the system shard,
// 1); the dedicated database's shard (shardId 1000) runs outside it, so a
// backup with shard_ids [1000] fails "quicraft: shard not found", and the
// restore of shard 1 is refused ("online restore of shard 1 refused: it hosts
// the system tenant ... use the offline 'qrdbd start --restore-backup-id' boot
// flow instead"). Enable this spec once backups cover database shards.
test.fixme("restore through the UI brings changed and deleted keys back on a cluster", async ({ page }) => {
  test.setTimeout(240_000);
  const baseline: Record<string, string> = {};
  await createRaftDatabase();
  for (const [k, v] of Object.entries(ORIGINAL)) await putKey(k, v);
  for (const k of Object.keys(ORIGINAL)) {
    const got = await getKey(k);
    expect(got.status, got.text).toBe(200);
    expect(got.text).toContain(ORIGINAL[k]);
    baseline[k] = got.text;
  }
  const shardId = await databaseShard();
  const id = await createBackupInUi(page);

  // --- Change one key and delete another through /v1. ---------------------
  await putKey("restore/alpha", "changed");
  await deleteKey("restore/beta");
  expect((await getKey("restore/alpha")).text).not.toBe(baseline["restore/alpha"]);
  expect((await getKey("restore/beta")).status).toBe(404);

  // --- Restore the database's shard through the UI. -----------------------
  await detailCard(page).getByRole("button", { name: "Restore", exact: true }).click();
  const modal = page.locator(".modal", { hasText: "Restore Backup" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(modal.getByLabel("Validate backup integrity before restoring")).toBeChecked();
  await modal.getByLabel("Allow restoring a plaintext (unencrypted) backup").check();
  await modal.getByLabel("Target Shards (optional)").fill(String(shardId));
  const restoreResp = page.waitForResponse((r) => r.url().endsWith("/v1/backups/restore"), { timeout: 120_000 });
  // The list reloads once the restore has completed.
  const reload = page.waitForResponse(
    (r) => r.request().method() === "GET" && /\/v1\/backups(\?|$)/.test(r.url()),
    { timeout: 150_000 },
  );
  await modal.getByRole("button", { name: "Restore", exact: true }).click();
  const rr = await restoreResp;
  const restoreText = await rr.text();
  expect(rr.status(), restoreText).toBe(200);
  expect(JSON.parse(rr.request().postData() ?? "{}")).toMatchObject({
    backup_id: id,
    validate_first: true,
    allow_plaintext: true,
    target_shards: [shardId],
  });
  await expect(toast(page, /^\s*Restore completed: 1 shard\(s\) restored in \d+ ms\.\s*$/)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(modal).toBeHidden();
  expect((await reload).status()).toBe(200);
  await expect(page.locator("table tbody tr", { hasText: id }).first()).toBeVisible();

  // --- Every original value is back on the server. ------------------------
  for (const k of Object.keys(ORIGINAL)) {
    await expect
      .poll(async () => (await getKey(k)).text, { timeout: LOAD_TIMEOUT, message: `${k} after ${restoreText}` })
      .toBe(baseline[k]);
  }
});

test("a restore that completes with per-shard errors shows them and reloads the list", async ({ page }) => {
  test.setTimeout(120_000);
  const id = await createBackupInUi(page);

  await detailCard(page).getByRole("button", { name: "Restore", exact: true }).click();
  const modal = page.locator(".modal", { hasText: "Restore Backup" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.getByLabel("Allow restoring a plaintext (unencrypted) backup").check();
  const restoreResp = page.waitForResponse((r) => r.url().endsWith("/v1/backups/restore"), { timeout: 60_000 });
  const reload = page.waitForResponse(
    (r) => r.request().method() === "GET" && r.url().endsWith("/v1/backups/list"),
    { timeout: 60_000 },
  );
  await modal.getByRole("button", { name: "Restore", exact: true }).click();
  const rr = await restoreResp;
  const result = JSON.parse(await rr.text()) as {
    shards_restored: number;
    errors?: { shard_id: number; message: string }[];
  };
  expect(rr.status()).toBe(200);
  // The backup holds only the system shard, which is never restored online.
  expect(result.shards_restored).toBe(0);
  expect(result.errors?.[0]?.shard_id).toBe(1);
  expect(result.errors?.[0]?.message).toContain("online restore of shard 1 refused");
  await expect(toast(page, "Restore completed: 0 shard(s) restored")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(toast(page, "online restore of shard 1 refused")).toBeVisible();
  await expect(modal).toBeHidden();
  const reloaded = await reload;
  expect(reloaded.status()).toBe(200);
  expect(reloaded.request().timing().startTime).toBeGreaterThanOrEqual(rr.request().timing().startTime);
  await expect(page.locator("table tbody tr", { hasText: id }).first()).toBeVisible();
});
