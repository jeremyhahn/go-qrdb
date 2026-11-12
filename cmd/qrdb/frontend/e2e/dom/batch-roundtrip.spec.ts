// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * batch-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Batch view against a live seal:"none" daemon: a batch of puts
 * and a delete executes through the real DOM, the server is then read back
 * over REST to prove each put landed and the delete removed its key, and a
 * batch the server refuses surfaces the server's own error in the UI.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const DATABASE = "system";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Opens the default database's Batch tool. */
async function openBatch(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText(DATABASE, { exact: true }).first().click();
  await page.getByRole("tab", { name: "Batch", exact: true }).or(
    page.getByRole("button", { name: "Batch", exact: true }),
  ).first().click();
  await expect(page.getByRole("heading", { name: "Batch", exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

async function expectToast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

type OpType = "put" | "get" | "delete" | "delete_range";

/** Appends one operation row to the builder and fills it in. */
async function addOp(page: Page, type: OpType, key: string, extra = ""): Promise<void> {
  await page.getByRole("button", { name: "Add Operation" }).click();
  const row = page.locator("select.select").filter({ has: page.locator("option[value]") }).last();
  await row.selectOption({ label: type });
  await page.getByPlaceholder("Key", { exact: true }).last().fill(key);
  if (type === "put") {
    await page.getByPlaceholder("Value", { exact: true }).last().fill(extra);
  } else if (type === "delete_range") {
    await page.getByPlaceholder("End key (exclusive)").last().fill(extra);
  }
}

/** Reads a key straight from the server; null when it does not exist. */
async function serverGet(page: Page, key: string): Promise<string | null> {
  const resp = await page.request.get(`${daemon!.adminUrl}/kv/${encodeURIComponent(key)}`, {
    headers: { "X-QRDB-Database": DATABASE },
    ignoreHTTPSErrors: true,
  });
  if (resp.status() === 404) return null;
  expect(resp.ok(), `GET ${key}: HTTP ${String(resp.status())}`).toBe(true);
  return resp.text();
}

/** Writes a key straight to the server. */
async function serverPut(page: Page, key: string, value: string): Promise<void> {
  const resp = await page.request.put(`${daemon!.adminUrl}/kv/${encodeURIComponent(key)}`, {
    headers: { "X-QRDB-Database": DATABASE },
    data: value,
    ignoreHTTPSErrors: true,
  });
  expect(resp.ok(), `PUT ${key}: HTTP ${String(resp.status())}`).toBe(true);
}

test("a batch of puts and a delete lands on the server", async ({ page }) => {
  await serverPut(page, "batch/doomed", "old");

  await openBatch(page);
  await addOp(page, "put", "batch/a", "alpha");
  await addOp(page, "put", "batch/b", "bravo");
  await addOp(page, "put", "batch/c", "charlie");
  await addOp(page, "delete", "batch/doomed");
  await page.getByRole("button", { name: "Execute Batch" }).click();
  await expectToast(page, "Batch executed successfully.");
  await expect(page.getByText("Batch Results")).toBeVisible({ timeout: LOAD_TIMEOUT });

  expect(await serverGet(page, "batch/a")).toBe("alpha");
  expect(await serverGet(page, "batch/b")).toBe("bravo");
  expect(await serverGet(page, "batch/c")).toBe("charlie");
  expect(await serverGet(page, "batch/doomed")).toBeNull();
});

test("a batch the server refuses shows the server's error", async ({ page }) => {
  await openBatch(page);
  // A range whose end sorts before its start is refused by the server.
  await addOp(page, "delete_range", "batch/z", "batch/a");
  const refused = page.waitForResponse(
    (r) => new URL(r.url()).pathname.endsWith("/batch") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Execute Batch" }).click();
  const resp = await refused;
  expect(resp.ok(), "the server must refuse this batch").toBe(false);
  const body = (await resp.json()) as { error?: string; message?: string };
  const serverMessage = body.error ?? body.message ?? "";
  expect(serverMessage).not.toBe("");
  await expect(
    page.locator(".toast .alert-error").filter({ hasText: serverMessage }).first(),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByText("Batch executed successfully.")).toHaveCount(0);
  expect(await serverGet(page, "batch/z")).toBeNull();
});
