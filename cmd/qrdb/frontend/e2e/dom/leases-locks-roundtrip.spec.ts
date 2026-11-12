// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * leases-locks-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Leases and Locks views of the "system" database against a live
 * qrdbd (seal "none", auth off):
 *
 *   Leases: grant a lease, send a keep-alive, revoke it.
 *   Locks:  acquire a lock, release it.
 *
 * Every step is checked after a full page reload, so the state shown comes
 * from the daemon and not from the view's local state.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const LOCK_KEY = "e2e/lock-roundtrip";

let daemon: DomDaemon | null = null;

test.beforeEach(async ({}, testInfo) => {
  // Lock list and status need an MVCC-backed shard; on the default (pebble)
  // state machine the daemon refuses them, so the lock test boots on mvcc.
  const isLockTest = testInfo.title.startsWith("lock ");
  daemon = await startDaemon({ seal: "none", ...(isLockTest ? { sm: "mvcc" as const } : {}) });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens a tool of the "system" database. */
async function openTool(page: Page, tool: "Leases" | "Locks"): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  // The fresh daemon ships a single kv-engine database named "system".
  await page.locator("aside").getByText("system", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("tab", { name: tool, exact: true }).or(
    page.getByRole("button", { name: tool, exact: true }),
  ).first().click();
  await expect(page.getByRole("heading", { name: tool, exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Expects the success toast; on a miss, reports the error toasts on screen. */
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

test("lease grant, keep-alive, revoke round-trip (UI ↔ live daemon)", async ({ page }) => {
  expect(daemon).not.toBeNull();

  // --- Grant ---------------------------------------------------------------------
  await openTool(page, "Leases");
  await page.getByRole("button", { name: "Grant Lease" }).click();
  await page.getByPlaceholder("60").fill("300");
  await page.getByRole("button", { name: "Grant", exact: true }).click();
  const granted = await expectToast(page, /Lease granted with ID \S+, TTL 300s\./);
  const leaseId = /ID (\S+),/.exec(granted)?.[1] ?? "";
  expect(leaseId).not.toBe("");

  // Listed after a reload; the detail shows the granted TTL.
  await openTool(page, "Leases");
  const row = page.getByRole("cell", { name: leaseId, exact: true });
  await expect(row.first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await row.first().click();
  const detail = page.locator(".card", { has: page.getByRole("heading", { name: "Lease Detail" }) });
  await expect(detail.getByRole("cell", { name: leaseId, exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(detail.getByRole("row", { name: /Granted TTL/ })).toContainText("300s");

  // --- Keep-alive ----------------------------------------------------------------
  await detail.getByRole("button", { name: "KeepAlive" }).click();
  await expectToast(page, `Lease ${leaseId} keep-alive sent.`);

  // --- Revoke --------------------------------------------------------------------
  await detail.getByRole("button", { name: "Revoke" }).click();
  await expectToast(page, `Lease ${leaseId} revoked.`);

  await openTool(page, "Leases");
  await expect(page.getByText("No active leases.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("cell", { name: leaseId, exact: true })).toHaveCount(0);
});

test("lock acquire, release round-trip (UI ↔ live daemon)", async ({ page }) => {
  expect(daemon).not.toBeNull();

  // --- Acquire -------------------------------------------------------------------
  await openTool(page, "Locks");
  await page.getByRole("button", { name: "Acquire Lock" }).click();
  await page.getByPlaceholder("resource key").fill(LOCK_KEY);
  await page.getByPlaceholder("60").fill("300");
  await page.getByRole("button", { name: "Acquire", exact: true }).click();
  await expectToast(page, `Lock "${LOCK_KEY}" acquired`);

  // Listed after a reload; the detail shows the key.
  await openTool(page, "Locks");
  const row = page.getByRole("cell", { name: LOCK_KEY, exact: true });
  await expect(row.first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await row.first().click();
  const detail = page.locator(".card", { has: page.getByRole("heading", { name: "Lock Detail" }) });
  await expect(detail.getByRole("cell", { name: LOCK_KEY, exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Release -------------------------------------------------------------------
  await detail.getByRole("button", { name: "Release" }).click();
  await expectToast(page, `Lock "${LOCK_KEY}" released.`);

  await openTool(page, "Locks");
  await expect(page.getByText("No active locks.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("cell", { name: LOCK_KEY, exact: true })).toHaveCount(0);
});
