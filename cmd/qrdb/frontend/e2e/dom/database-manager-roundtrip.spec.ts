// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * database-manager-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Server Admin → Databases view against a live qrdbd (seal
 * "none", auth off):
 *
 *   1. Create a database through the Create Database form.
 *   2. It appears in the list, and its detail view shows it.
 *   3. Detail → enable the MySQL wire protocol (database.updateProtocol).
 *   4. Drop the database.
 *
 * Every step is checked after a full page reload, so the state shown comes
 * from the daemon and not from the view's local state.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const DB = "e2edbroundtrip";

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

/** Expects the success toast; on a miss, reports the error toasts on screen. */
async function expectToast(page: Page, text: string): Promise<void> {
  try {
    await expect(page.getByText(text).last()).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const errors = await page.locator(".toast .alert-error").allInnerTexts();
    throw new Error(`expected toast "${text}"; error toasts: ${JSON.stringify(errors)}\n${err}`);
  }
}

/** Opens the database's detail view after a reload. */
async function openDatabase(page: Page): Promise<void> {
  await openDatabases(page);
  await page.getByRole("cell", { name: DB, exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Database Info" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** The MySQL toggle of the detail view's SQL Wire Protocols card. */
function mysqlToggle(page: Page) {
  return page
    .locator(".card", { has: page.getByRole("heading", { name: "SQL Wire Protocols" }) })
    .locator("label", { hasText: "MySQL" })
    .getByRole("checkbox");
}

test("database create, detail, protocol change, drop round-trip (UI ↔ live daemon)", async ({ page }) => {
  expect(daemon).not.toBeNull();

  // --- 1. Create ---------------------------------------------------------------
  await openDatabases(page);
  await page.getByRole("button", { name: "Create Database" }).click();
  await page.getByPlaceholder("e.g. my-database").fill(DB);
  await page.locator("form").getByRole("button", { name: "Create Database" }).click();
  await expectToast(page, `Database "${DB}" created`);

  // --- 2. Listed, and the detail shows it --------------------------------------
  await openDatabase(page);
  await expect(page.locator("p.font-mono", { hasText: DB }).first()).toBeVisible();
  await expect(page.getByText("MySQL off")).toBeVisible();
  await expect(mysqlToggle(page)).not.toBeChecked();

  // --- 3. Enable the MySQL wire protocol ---------------------------------------
  await mysqlToggle(page).check();
  await expectToast(page, "MySQL enabled");

  await openDatabase(page);
  await expect(page.getByText("MySQL on")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(mysqlToggle(page)).toBeChecked();

  // --- 4. Drop -----------------------------------------------------------------
  await page.getByRole("button", { name: "Drop Database" }).click();
  await page.getByRole("button", { name: "Confirm Drop" }).click();
  await expectToast(page, `Database "${DB}" dropped`);

  await openDatabases(page);
  // The list has loaded (the system database is always present) before the
  // absence check.
  await expect(page.getByRole("cell", { name: "system", exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("cell", { name: DB, exact: true })).toHaveCount(0);
});
