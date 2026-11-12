// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * mvcc-compact-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives mvcc.compact from the KV Explorer of the "system" database against a
 * live qrdbd on the mvcc state machine (seal "none", auth off):
 *
 *   put a key twice through the daemon's REST API, read it back at its first
 *   revision, compact to the second revision with "Compact to revision" in the
 *   UI, and check that the daemon no longer serves the first revision while
 *   the latest one still reads.
 */

import {
  test,
  expect,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const KEY = "e2e/compact-roundtrip";

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
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .locator("aside")
    .getByText("system", { exact: true })
    .first()
    .click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Expects the success toast; on a miss, reports the error toasts on screen. */
async function expectToast(page: Page, text: string | RegExp): Promise<string> {
  const toast = page.locator(".toast .alert").filter({ hasText: text }).last();
  try {
    await expect(toast).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const shown = await page.locator(".toast .alert").allInnerTexts();
    throw new Error(
      `expected toast ${String(text)}; toasts shown: ${JSON.stringify(shown)}\n${err}`,
    );
  }
  return toast.innerText();
}

/** Puts KEY=value on the daemon and returns the key's mod revision after the write. */
async function putAndRevision(
  request: APIRequestContext,
  value: string,
): Promise<number> {
  const url = `${daemon!.adminUrl}/kv/${encodeURIComponent(KEY)}`;
  const put = await request.put(url, { data: value, ignoreHTTPSErrors: true });
  expect(
    put.ok(),
    `PUT ${KEY}: ${put.status()} ${await put.text()}`,
  ).toBeTruthy();
  const get = await request.get(`${url}?revision=true`, {
    ignoreHTTPSErrors: true,
  });
  const body = await get.text();
  expect(
    get.ok(),
    `GET ${KEY}?revision=true: ${get.status()} ${body}`,
  ).toBeTruthy();
  const rev = Number(
    (JSON.parse(body) as { mod_revision?: number }).mod_revision,
  );
  expect(
    Number.isInteger(rev) && rev > 0,
    `mod_revision in ${body}`,
  ).toBeTruthy();
  return rev;
}

/** GETs KEY at a past revision and returns the HTTP status and body. */
async function getAt(
  request: APIRequestContext,
  revision: number,
): Promise<{ status: number; body: string }> {
  const resp = await request.get(
    `${daemon!.adminUrl}/kv/${encodeURIComponent(KEY)}?at-revision=${revision}`,
    { ignoreHTTPSErrors: true },
  );
  return { status: resp.status(), body: await resp.text() };
}

test("mvcc compact round-trip (UI ↔ live daemon)", async ({
  page,
  request,
}) => {
  expect(daemon).not.toBeNull();

  // --- Two revisions of one key; the first still reads. --------------------------
  const first = await putAndRevision(request, "v1");
  const second = await putAndRevision(request, "v2");
  expect(second).toBeGreaterThan(first);
  const before = await getAt(request, first);
  expect(
    before.status,
    `at-revision ${first} before compaction: ${before.body}`,
  ).toBe(200);

  // --- Compact to the second revision in the UI. ---------------------------------
  await openKV(page);
  await page.getByLabel("Compact revision").fill(String(second));
  await page.getByRole("button", { name: "Compact to revision" }).click();
  const modal = page.locator(".modal-box", {
    has: page.getByText("Compact History"),
  });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(modal).toContainText(`older than ${second}`);
  await modal.getByRole("button", { name: "Compact", exact: true }).click();
  await expectToast(page, `Compacted history to revision ${second}`);

  // --- The daemon dropped the first revision; the latest still reads. ------------
  const after = await getAt(request, first);
  expect(
    after.status,
    `at-revision ${first} after compaction: ${after.body}`,
  ).not.toBe(200);
  const latest = await request.get(
    `${daemon!.adminUrl}/kv/${encodeURIComponent(KEY)}`,
    { ignoreHTTPSErrors: true },
  );
  expect(latest.status(), `GET ${KEY} after compaction`).toBe(200);
  expect(await latest.text()).toBe("v2");

  // A second compaction to the same revision is refused by the daemon and the UI says so.
  await openKV(page);
  await page.getByLabel("Compact revision").fill(String(first));
  await page.getByRole("button", { name: "Compact to revision" }).click();
  await page
    .locator(".modal-box", { has: page.getByText("Compact History") })
    .getByRole("button", { name: "Compact", exact: true })
    .click();
  await expect(page.locator(".toast .alert-error").last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
});
