// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * database-wizard-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the database creation wizard against a live qrdbd:
 *
 *   1. One database for each storage backend the wizard offers (seal "none").
 *   2. One database with a barrier passphrase on a passphrase-sealed daemon.
 *
 * After each, GET /v1/databases/<name> on the daemon must report the chosen
 * backend (and barrier), and the database must open in the UI.
 */

import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, waitForHealthy, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

/** The per-database barrier passphrase typed into the wizard. */
const PASSPHRASE = "wizard-barrier-passphrase-1";

let daemon: DomDaemon | null = null;

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon. */
async function openApp(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** GET /v1/databases/<name> on the daemon. */
async function getDatabase(request: APIRequestContext, name: string): Promise<Record<string, unknown>> {
  const res = await request.get(`${daemon!.adminUrl}/v1/databases/${encodeURIComponent(name)}`, {
    ignoreHTTPSErrors: true,
  });
  const body = await res.text();
  expect(res.status(), body).toBe(200);
  return JSON.parse(body) as Record<string, unknown>;
}

/** Drives the wizard from the sidebar's New Database entry to completion. */
async function createThroughWizard(
  page: Page,
  name: string,
  backend: string,
  opts: { dataDir?: string; passphrase?: string } = {},
): Promise<void> {
  const { dataDir, passphrase } = opts;
  await openApp(page);
  await page.getByRole("button", { name: "New Database" }).first().click();
  await expect(page.getByText("Configure and create a new QRDB database")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Name
  await page.getByRole("textbox").first().fill(name);
  await page.getByRole("button", { name: "Next", exact: true }).click();

  // Storage
  await page.locator(`input[name="db-storage-backend"][value="${backend}"]`).check();
  if (dataDir) await page.locator("#data-dir-input").fill(dataDir);
  await page.getByRole("button", { name: "Next", exact: true }).click();

  // SQL: defaults.
  await expect(page.getByTestId("db-seal-mode-none")).toBeHidden();
  await page.getByRole("button", { name: "Next", exact: true }).click();

  // Barrier: none, or a passphrase barrier.
  await expect(page.getByText("Per-Database Barrier", { exact: true })).toBeVisible();
  if (passphrase) {
    await page.getByTestId("db-seal-mode-passphrase").click();
    await page.locator("#db-barrier-passphrase").fill(passphrase);
    await page.locator("#db-barrier-passphrase-confirm").fill(passphrase);
  } else {
    await page.getByTestId("db-seal-mode-none").click();
  }
  await page.getByRole("button", { name: "Next", exact: true }).click();

  // Summary → Create.
  await page.getByRole("button", { name: /^Create/ }).click();
  // On completion the wizard opens the new database's Data tool.
  try {
    await expect(page.getByText(`${name} / Data`)).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const alerts = await page.locator("main [role=alert]").allInnerTexts();
    throw new Error(`wizard did not complete; alerts: ${JSON.stringify(alerts)}\n${err}`);
  }
}

/** The database is listed in the sidebar and opens. */
async function expectOpens(page: Page, name: string): Promise<void> {
  await openApp(page);
  await page.getByRole("button", { name, exact: true }).first().click();
  await expect(page.getByText(`${name} / Data`)).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("heading", { name: "Key-Value Entries" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByText("No data available")).toBeVisible({ timeout: LOAD_TIMEOUT });
  const errors = await page.locator(".toast .alert-error").allInnerTexts();
  expect(errors, "error toasts after opening the database").toEqual([]);
}

for (const backend of ["memory", "file", "pebble"]) {
  test(`database wizard creates a ${backend} database (UI ↔ live daemon)`, async ({ page, request }) => {
    daemon = await startDaemon({ seal: "none" });
    const name = `e2ewiz${backend}`;
    await createThroughWizard(page, name, backend);

    const db = await getDatabase(request, name);
    expect(db.backend).toBe(backend);
    expect(db.barrier, "no per-database barrier").toBeUndefined();

    await expectOpens(page, name);
  });
}

test("database wizard creates a database with a barrier passphrase on a passphrase-sealed daemon", async ({
  page,
  request,
}) => {
  daemon = await startDaemon({ seal: "passphrase" });
  const init = await request.post(`${daemon.adminUrl}/v1/ceremony/initialize`, {
    data: {
      so_pin: "so-pin-wizard-e2e",
      user_pin: "user-pin-wizard-e2e",
      admin_generate: true,
      secret: Buffer.from("node-passphrase-wizard-e2e").toString("base64"),
    },
    ignoreHTTPSErrors: true,
  });
  expect(init.ok(), `ceremony init: ${init.status()} ${await init.text()}`).toBeTruthy();
  await waitForHealthy(daemon);

  const name = "e2ewizbarrier";
  // A barrier needs a dedicated store: an explicit data directory on the server.
  const dataDir = `${daemon.dataDir}/wizard-barrier-db`;
  await createThroughWizard(page, name, "pebble", { dataDir, passphrase: PASSPHRASE });

  const db = await getDatabase(request, name);
  expect(db.backend).toBe("pebble");
  expect(db.dataDir).toBe(dataDir);
  expect(db.barrier).toEqual({ enabled: true, mode: "passphrase" });

  const base = `${daemon.adminUrl}/v1/databases/${name}`;
  const status = async (): Promise<Record<string, unknown>> => {
    const res = await request.get(`${base}/barrier/status`, { ignoreHTTPSErrors: true });
    expect(res.status(), await res.text()).toBe(200);
    return (await res.json()) as Record<string, unknown>;
  };
  const st = await status();
  expect(st.strategy).toBe("passphrase");
  expect(st.sealed).toBe(false);

  // The barrier was initialised with the passphrase typed into the wizard:
  // seal it, a wrong passphrase is refused, the wizard's one unseals it.
  const seal = await request.post(`${base}/seal`, { ignoreHTTPSErrors: true });
  expect(seal.ok(), `seal: ${seal.status()} ${await seal.text()}`).toBeTruthy();
  expect((await status()).sealed).toBe(true);
  const wrong = await request.post(`${base}/unseal`, { data: { secret: "not-the-passphrase" }, ignoreHTTPSErrors: true });
  expect(wrong.ok(), `unseal with a wrong passphrase: ${wrong.status()}`).toBeFalsy();
  const unseal = await request.post(`${base}/unseal`, { data: { secret: PASSPHRASE }, ignoreHTTPSErrors: true });
  expect(unseal.ok(), `unseal: ${unseal.status()} ${await unseal.text()}`).toBeTruthy();
  expect((await status()).sealed).toBe(false);

  await expectOpens(page, name);
});
