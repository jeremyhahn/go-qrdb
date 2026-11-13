// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * server-refusals-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives requests the live qrdbd (seal "none", auth off) refuses through the
 * Server Admin → Databases view, and checks that the UI shows the server's
 * own refusal message, the same text the request's JSON error body carries
 * over REST, not a blank or a generic error:
 *
 *   1. Create a database whose name already exists (409).
 *   2. Create a database with a name the server rejects (400): the form
 *      refuses the same name inline, by the server's rule, before sending.
 *   3. Drop the system database (refused).
 */

import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const DB = "e2erefusals";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens Server Admin → Databases. */
async function openDatabases(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  // The sidebar has two "Databases" buttons: the connection tree's group and
  // the Server Admin → System entry, which comes last.
  await page.getByRole("button", { name: "Databases", exact: true }).last().click();
  await expect(page.getByRole("button", { name: "Create Database" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/**
 * Expects an error toast or inline error alert whose text carries the
 * server's message. The newest error alert is read as soon as it shows, as an
 * error toast is dismissed after a few seconds.
 */
async function expectError(page: Page, text: string): Promise<void> {
  const alert = page.locator(".alert-error").last();
  await expect(alert, `no error alert shown; expected one carrying "${text}"`).toBeVisible({ timeout: LOAD_TIMEOUT });
  expect(await alert.innerText()).toContain(text);
}

/** The message of a JSON error body, whatever envelope the server uses. */
function errorMessage(body: unknown): string {
  const b = body as Record<string, unknown>;
  const e = b?.error;
  if (typeof e === "string") return e;
  if (e && typeof e === "object" && typeof (e as Record<string, unknown>).message === "string") {
    return (e as Record<string, string>).message;
  }
  if (typeof b?.message === "string") return b.message;
  throw new Error(`no error message in ${JSON.stringify(body)}`);
}

/** Sends the request over REST and returns the refusal's status and message. */
async function refusal(
  request: APIRequestContext,
  method: string,
  path: string,
  data?: unknown,
): Promise<{ status: number; message: string }> {
  const res = await request.fetch(`${daemon!.adminUrl}${path}`, { method, data, ignoreHTTPSErrors: true });
  const text = await res.text();
  expect(res.ok(), `${method} ${path} was expected to be refused: ${res.status()} ${text}`).toBe(false);
  return { status: res.status(), message: errorMessage(JSON.parse(text)) };
}

/** Submits the Create Database form with name. */
async function submitCreate(page: Page, name: string): Promise<void> {
  await page.getByRole("button", { name: "Create Database" }).click();
  await page.getByPlaceholder("e.g. my-database").fill(name);
  await page.locator("form").getByRole("button", { name: "Create Database" }).click();
}

test("the UI shows the server's refusal message (UI ↔ live daemon)", async ({ page, request }) => {
  expect(daemon).not.toBeNull();

  // --- 1. Duplicate name: 409 ----------------------------------------------------
  const created = await request.post(`${daemon!.adminUrl}/v1/databases`, { data: { name: DB, backend: "raft" }, ignoreHTTPSErrors: true });
  expect(created.ok(), `create ${DB}: ${created.status()} ${await created.text()}`).toBe(true);

  const dup = await refusal(request, "POST", "/v1/databases", { name: DB, backend: "raft" });
  expect(dup.status).toBe(409);

  await openDatabases(page);
  await submitCreate(page, DB);
  await expectError(page, dup.message);

  // --- 2. Invalid name: 400 ------------------------------------------------------
  const badName = "Bad.Name";
  const bad = await refusal(request, "POST", "/v1/databases", { name: badName, backend: "raft" });
  expect(bad.status).toBe(400);

  expect(bad.message).toContain(`invalid database name: "${badName}"`);

  // The form checks names by the server's own rule (namePattern in
  // pkg/database), so the name the server refuses never leaves the UI: the
  // form shows its inline error and the submit stays disabled.
  await openDatabases(page);
  await page.getByRole("button", { name: "Create Database" }).click();
  await page.getByPlaceholder("e.g. my-database").fill(badName);
  await expect(page.getByText("Only letters, numbers, hyphens, and underscores are allowed.")).toBeVisible();
  await expect(page.locator("form").getByRole("button", { name: "Create Database" })).toBeDisabled();

  // --- 3. Drop the system database: refused --------------------------------------
  const sys = await refusal(request, "DELETE", "/v1/databases/system");

  await openDatabases(page);
  await page.getByRole("cell", { name: "system", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Database Info" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Drop Database" }).click();
  await page.getByRole("button", { name: "Confirm Drop" }).click();
  await expectError(page, sys.message);
});
