// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * kv-mvcc-advanced-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the advanced KV and MVCC reads of the KV Explorer of the "system"
 * database against a live qrdbd on the mvcc state machine (seal "none", auth
 * off), and checks every UI answer against the daemon's REST answer:
 *
 *   put a key twice through the UI (mvcc.currentRevision after each write),
 *   open it (kv.get + mvcc.getRevision), read it at its first revision
 *   (mvcc.getAtRevision), list a prefix (kv.list), "Go to key" on a present
 *   and a missing key (kv.exists), and create a key with a lease
 *   (mvcc.putWithLease).
 */

import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const PREFIX = "e2e/adv/";
const KEY = `${PREFIX}versioned`;
const OTHER = `${PREFIX}other`;
const OUTSIDE = "e2e/outside";
const LEASED = `${PREFIX}leased`;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none", sm: "mvcc" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens the KV Store of the "system" database. */
async function openKV(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("system", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Expects a toast; on a miss, reports the toasts on screen. */
async function expectToast(page: Page, text: string | RegExp): Promise<string> {
  const toast = page.locator(".toast .alert").filter({ hasText: text }).last();
  try {
    await expect(toast).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const shown = await page.locator(".toast .alert").allInnerTexts();
    throw new Error(`expected toast ${String(text)}; toasts shown: ${JSON.stringify(shown)}\n${err}`, { cause: err });
  }
  return toast.innerText();
}

/** Decodes a base64 []byte field of the daemon's JSON. */
function b64(value: string): string {
  return Buffer.from(value, "base64").toString("utf8");
}

/** GETs a REST path on the daemon and returns status and body. */
async function rest(request: APIRequestContext, path: string): Promise<{ status: number; body: string }> {
  const resp = await request.get(`${daemon!.adminUrl}${path}`, { ignoreHTTPSErrors: true });
  return { status: resp.status(), body: await resp.text() };
}

async function restRevision(request: APIRequestContext): Promise<number> {
  const r = await rest(request, "/kv/revision");
  expect(r.status, `GET /kv/revision: ${r.body}`).toBe(200);
  return Number((JSON.parse(r.body) as { revision: number }).revision);
}

/** Creates key=value through the UI's Create Entry modal, optionally with a lease. */
async function createInUI(page: Page, key: string, value: string, leaseId?: number): Promise<void> {
  const details = page.getByLabel("Details");
  if (await details.isVisible()) {
    await details.getByRole("button", { name: "Close", exact: true }).click();
    await expect(details).toBeHidden({ timeout: LOAD_TIMEOUT });
  }
  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  const modal = page.locator(".modal-box", { has: page.getByText("Create Entry") });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.getByPlaceholder("Enter key").fill(key);
  await modal.getByPlaceholder("Enter value (supports JSON)").fill(value);
  if (leaseId !== undefined) {
    await modal.getByPlaceholder("Attach the key to a lease; blank for none").fill(String(leaseId));
  }
  const text = leaseId !== undefined ? `Created "${key}" with lease ${leaseId}` : `Created "${key}"`;
  // A toast from an earlier write of the same key may still be shown: wait for
  // this write's own toast, not a stale one.
  const toasts = page.locator(".toast .alert").filter({ hasText: text });
  const before = await toasts.count();
  await modal.getByRole("button", { name: "Create", exact: true }).click();
  await expect(toasts).toHaveCount(before + 1, { timeout: LOAD_TIMEOUT });
  await expectToast(page, text);
}

/** Expects the UI's "Current revision" to equal the daemon's. */
async function expectCurrentRevision(page: Page, request: APIRequestContext): Promise<number> {
  const rev = await restRevision(request);
  await expect(page.getByTestId("kv-current-revision")).toContainText(String(rev), { timeout: LOAD_TIMEOUT });
  return rev;
}

/** Opens key's detail panel through "Go to key" (kv.exists then kv.get). */
async function goToKey(page: Page, key: string): Promise<void> {
  await page.getByLabel("Go to key").fill(key);
  await page.getByRole("button", { name: "Go", exact: true }).click();
}

test("advanced kv and mvcc reads round-trip (UI ↔ live daemon)", async ({ page, request }) => {
  expect(daemon).not.toBeNull();
  await openKV(page);

  // --- Put KEY twice in the UI; the current revision follows the daemon. --------
  await createInUI(page, KEY, "v1");
  const first = await expectCurrentRevision(page, request);
  await createInUI(page, KEY, "v2");
  const second = await expectCurrentRevision(page, request);
  expect(second).toBeGreaterThan(first);

  // --- Open KEY: value and revision metadata match the daemon. ------------------
  const meta = await rest(request, `/kv/${encodeURIComponent(KEY)}?revision=true`);
  expect(meta.status, `GET ${KEY}?revision=true: ${meta.body}`).toBe(200);
  const restMeta = JSON.parse(meta.body) as { mod_revision: number; create_revision: number; version: number };
  expect(restMeta.mod_revision).toBe(second);
  await goToKey(page, KEY);
  const details = page.getByLabel("Details");
  await expect(details.getByTestId("kv-mod-revision")).toHaveText(String(restMeta.mod_revision), {
    timeout: LOAD_TIMEOUT,
  });
  await expect(details.getByTestId("kv-create-revision")).toHaveText(String(restMeta.create_revision));
  await expect(details.getByTestId("kv-version")).toHaveText(String(restMeta.version));
  await expect(details.getByTestId("kv-lease")).toHaveText("none");

  // --- Read KEY at its first revision: the old value, as the daemon says. --------
  const atFirst = await rest(request, `/kv/${encodeURIComponent(KEY)}?at-revision=${first}`);
  expect(atFirst.status, `at-revision ${first}: ${atFirst.body}`).toBe(200);
  expect(b64((JSON.parse(atFirst.body) as { value: string }).value)).toBe("v1");
  await details.getByPlaceholder("revision").fill(String(first));
  await details.getByRole("button", { name: "Look up" }).click();
  await expect(details.getByTestId("kv-at-revision-value")).toHaveText("v1", { timeout: LOAD_TIMEOUT });

  // --- List a prefix: the UI shows exactly the daemon's keys. --------------------
  await createInUI(page, OTHER, "o");
  await createInUI(page, OUTSIDE, "x");
  const listed = await rest(request, `/kv?prefix=${encodeURIComponent(PREFIX)}`);
  expect(listed.status, `GET /kv?prefix: ${listed.body}`).toBe(200);
  const keys = (JSON.parse(listed.body) as { keys: string[] }).keys;
  expect([...keys].sort()).toEqual([OTHER, KEY].sort());
  await page.getByPlaceholder("Filter by key prefix...").fill(PREFIX);
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.getByText(`${keys.length} entries`)).toBeVisible({ timeout: LOAD_TIMEOUT });
  const table = page.locator("table");
  for (const k of keys) await expect(table.getByText(k, { exact: true })).toBeVisible();
  await expect(table.getByText(OUTSIDE, { exact: true })).toHaveCount(0);

  // --- Exists: a missing key warns, a present key opens. -------------------------
  const missing = `${PREFIX}missing`;
  expect((await rest(request, `/kv/${encodeURIComponent(missing)}`)).status).toBe(404);
  await goToKey(page, missing);
  await expectToast(page, `Key "${missing}" does not exist.`);
  expect((await rest(request, `/kv/${encodeURIComponent(OTHER)}`)).status).toBe(200);
  await goToKey(page, OTHER);
  await expect(details.getByTestId("kv-mod-revision")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(details).toContainText(OTHER);

  // --- Put with a lease: the daemon attaches the key to the lease. ---------------
  const grant = await request.post(`${daemon!.adminUrl}/lease/grant`, {
    data: { ttl: 300 },
    ignoreHTTPSErrors: true,
  });
  const grantBody = await grant.text();
  expect(grant.ok(), `POST /lease/grant: ${grant.status()} ${grantBody}`).toBeTruthy();
  const leaseId = Number((JSON.parse(grantBody) as { id: number }).id);
  expect(leaseId).toBeGreaterThan(0);
  await createInUI(page, LEASED, "with-lease", leaseId);
  const leased = await rest(request, `/kv/${encodeURIComponent(LEASED)}?revision=true`);
  expect(leased.status, `GET ${LEASED}?revision=true: ${leased.body}`).toBe(200);
  const leasedMeta = JSON.parse(leased.body) as { value: string; lease?: number; lease_id?: number };
  expect(b64(leasedMeta.value)).toBe("with-lease");
  expect(leasedMeta.lease ?? leasedMeta.lease_id, `lease in ${leased.body}`).toBe(leaseId);
  await goToKey(page, LEASED);
  await expect(details.getByTestId("kv-lease")).toHaveText(String(leaseId), { timeout: LOAD_TIMEOUT });
});
