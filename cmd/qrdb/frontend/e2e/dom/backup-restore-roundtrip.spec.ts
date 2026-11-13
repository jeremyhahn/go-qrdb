// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * backup-restore-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Verify and restore against a live daemon:
 *   - backups.verify: BackupManager's Verify button checks a backup created
 *     through the UI and shows the server's verdict.
 *   - backups.restore: after keys are changed and deleted through /v1, a
 *     restore through the UI (validate first on) brings every original value
 *     back on the server.
 *   - a restore the server refuses (the backup was deleted) shows the
 *     server's own error in the UI.
 */

import { test, expect, request as playwrightRequest, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

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
  init?: { method?: string; body?: string; database?: string },
): Promise<ApiResponse> {
  const ctx = await playwrightRequest.newContext({ ignoreHTTPSErrors: true });
  try {
    const resp = await ctx.fetch(`${daemon!.adminUrl}${path}`, {
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

// A raft-backed database has its own shard. The system shard is never
// restored online (the server sends the operator to the offline boot flow),
// so the restore targets this database's shard.
const DB = "restoredb";

async function putKey(key: string, value: string, database?: string): Promise<void> {
  const resp = await api(`/kv/${encodeURIComponent(key)}`, { method: "PUT", body: value, database });
  expect(resp.status, resp.text).toBeLessThan(300);
}

async function getKey(key: string, database?: string): Promise<ApiResponse> {
  return api(`/kv/${encodeURIComponent(key)}`, { database });
}

async function deleteKey(key: string, database?: string): Promise<void> {
  const resp = await api(`/kv/${encodeURIComponent(key)}`, { method: "DELETE", database });
  expect(resp.status, resp.text).toBeLessThan(300);
}

/** Creates the raft database and returns the shard ID the server issued. */
async function createRaftDatabase(): Promise<number> {
  const resp = await api("/v1/databases", { method: "POST", body: JSON.stringify({ name: DB, backend: "raft" }) });
  expect(resp.status, resp.text).toBeLessThan(300);
  // The node's raft host carries the database's own shard beside the system
  // shard (1); GET /status lists both once the shard is up.
  let shards: number[] = [];
  await expect
    .poll(
      async () => {
        const status = (await api("/status")).json() as { shards?: { shard_id: number }[] };
        shards = (status.shards ?? []).map((s) => s.shard_id).filter((id) => id !== 1);
        return shards.length;
      },
      { timeout: LOAD_TIMEOUT },
    )
    .toBe(1);
  return shards[0];
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

  const destination = `${daemon!.dataDir}/ui-backup-001`;
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

test("verify checks a backup and restore brings changed and deleted keys back", async ({ page }) => {
  // The server's own reads of the original keys are the baseline.
  const baseline: Record<string, string> = {};
  const shardId = await createRaftDatabase();
  for (const [k, v] of Object.entries(ORIGINAL)) await putKey(k, v, DB);
  for (const k of Object.keys(ORIGINAL)) {
    const got = await getKey(k, DB);
    expect(got.status, got.text).toBe(200);
    expect(got.text).toContain(ORIGINAL[k]);
    baseline[k] = got.text;
  }
  const id = await createBackupInUi(page);

  // --- Verify through the UI. ---------------------------------------------
  await detailCard(page).getByRole("button", { name: "Verify", exact: true }).click();
  await expect(toast(page, `Backup "${id}" verified OK.`)).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(detailCard(page).getByText("Checksum verified.")).toBeVisible();
  const verified = await api(`/v1/backups/${encodeURIComponent(id)}/verify`, { method: "POST" });
  expect(verified.status, verified.text).toBe(200);
  expect((verified.json() as { valid: boolean }).valid).toBe(true);

  // --- Change one key and delete another through /v1. ---------------------
  await putKey("restore/alpha", "changed", DB);
  await deleteKey("restore/beta", DB);
  expect((await getKey("restore/alpha", DB)).text).not.toBe(baseline["restore/alpha"]);
  expect((await getKey("restore/beta", DB)).status).toBe(404);

  // --- Restore through the UI with validate-first on. ---------------------
  await detailCard(page).getByRole("button", { name: "Restore", exact: true }).click();
  const modal = page.locator(".modal", { hasText: "Restore Backup" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  const validate = modal.getByLabel("Validate backup integrity before restoring");
  await expect(validate).toBeChecked();
  const allowPlaintext = modal.getByLabel("Allow restoring a plaintext (unencrypted) backup");
  await expect(allowPlaintext).not.toBeChecked();

  // Without the plaintext opt-in the server refuses; the UI shows its error.
  const refusedResp = page.waitForResponse((r) => r.url().endsWith("/v1/backups/restore"));
  await modal.getByRole("button", { name: "Restore", exact: true }).click();
  const refused = await refusedResp;
  expect(refused.status()).toBe(400);
  const refusal = (JSON.parse(await refused.text()) as { error: string }).error;
  expect(refusal).toContain("plaintext");
  await expect(page.locator(".toast .alert-error").filter({ hasText: refusal })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(modal).toBeVisible();
  expect((await getKey("restore/beta", DB)).status).toBe(404);

  // With the opt-in ticked the restore of the database's shard runs.
  await allowPlaintext.check();
  await modal.getByLabel("Target Shards (optional)").fill(String(shardId));
  const restoreResp = page.waitForResponse((r) => r.url().endsWith("/v1/backups/restore"), { timeout: 90_000 });
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

  // --- Every original value is back on the server. ------------------------
  for (const k of Object.keys(ORIGINAL)) {
    const got = await getKey(k, DB);
    expect(got.status, `${k}: ${got.text} after ${restoreText}`).toBe(200);
    expect(got.text, `${k} after ${restoreText}`).toBe(baseline[k]);
  }
});

test("a restore the server refuses shows the server's error", async ({ page }) => {
  await putKey("restore/alpha", "one");
  const before = (await getKey("restore/alpha")).text;
  const id = await createBackupInUi(page);

  // Delete the backup behind the UI's back; the detail panel still shows it.
  expect((await api(`/v1/backups/${encodeURIComponent(id)}`, { method: "DELETE" })).status).toBeLessThan(300);
  const refused = await api("/v1/backups/restore", {
    method: "POST",
    body: JSON.stringify({ backup_id: id, validate_first: true }),
  });
  expect(refused.status, refused.text).toBeGreaterThanOrEqual(400);
  const serverError = String((refused.json() as { error?: string; message?: string }).error ??
    (refused.json() as { message?: string }).message);

  await detailCard(page).getByRole("button", { name: "Restore", exact: true }).click();
  const modal = page.locator(".modal", { hasText: "Restore Backup" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.getByRole("button", { name: "Restore", exact: true }).click();
  await expect(page.locator(".toast .alert-error").filter({ hasText: serverError })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  // The modal stays open so the operator can correct and retry.
  await expect(modal).toBeVisible();
  expect((await getKey("restore/alpha")).text).toBe(before);
});
