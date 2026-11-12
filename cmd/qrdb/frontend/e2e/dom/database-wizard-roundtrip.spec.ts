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

  // A barrier with no data directory is refused by the server (422), so the
  // wizard holds it at the barrier step and says why: Create is never reached.
  await openApp(page);
  await page.getByRole("button", { name: "New Database" }).first().click();
  await page.getByRole("textbox").first().fill(name);
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await page.locator('input[name="db-storage-backend"][value="pebble"]').check();
  await expect(page.locator("#data-dir-input")).toHaveValue("");
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await expect(page.getByText("Per-Database Barrier", { exact: true })).toBeVisible();
  const dirError = page.getByTestId("db-barrier-datadir-error");
  for (const mode of ["passphrase", "shamir"]) {
    await page.getByTestId(`db-seal-mode-${mode}`).click();
    if (mode === "passphrase") {
      await page.locator("#db-barrier-passphrase").fill(PASSPHRASE);
      await page.locator("#db-barrier-passphrase-confirm").fill(PASSPHRASE);
    }
    await expect(dirError).toContainText("data directory");
    await expect(page.getByRole("button", { name: "Next", exact: true })).toBeDisabled();
  }
  await expect(page.getByRole("button", { name: /^Create/ })).toHaveCount(0);
  const absent = await request.get(`${daemon.adminUrl}/v1/databases/${name}`, { ignoreHTTPSErrors: true });
  expect(absent.status(), "no database is created without a data directory").toBe(404);

  // With a data directory set the same barrier is created.
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

test("five new memory databases each open with no error toast", async ({ page }) => {
  test.setTimeout(180_000);
  daemon = await startDaemon({ seal: "none" });
  // Any 5xx the UI meets, and the daemon's stderr, explain a failing open.
  // The one revision probe a memory database gets (501: no revisions) is
  // counted apart: at most one per database, never a retry.
  const failures: string[] = [];
  const revisionProbes = new Map<string, number>();
  page.on("response", (res) => {
    const db = res.request().headers()["x-qrdb-database"] ?? "";
    if (res.url().includes("/kv/revision") && res.status() === 501) {
      revisionProbes.set(db, (revisionProbes.get(db) ?? 0) + 1);
      if ((revisionProbes.get(db) ?? 0) > 1) failures.push(`second revision probe for ${db} -> 501`);
      return;
    }
    if (res.status() >= 500) failures.push(`${res.request().method()} ${res.url()} -> ${res.status()}`);
  });
  const serverLog: string[] = [];
  daemon.proc.stderr?.on("data", (chunk: Buffer) => serverLog.push(chunk.toString()));

  for (let i = 1; i <= 5; i++) {
    const name = `e2ewizmem${i}`;
    await createThroughWizard(page, name, "memory");
    const toasts = await page.locator(".toast .alert-error").allInnerTexts();
    expect(toasts, `error toasts after creating ${name}; 5xx: ${JSON.stringify(failures)}`).toEqual([]);
    await expectOpens(page, name);
    expect(failures, `5xx responses opening ${name}; server log: ${serverLog.join("").slice(-2000)}`).toEqual([]);
  }
});

test("a memory database asks for its revision once across three opens", async ({ page }) => {
  test.setTimeout(120_000);
  daemon = await startDaemon({ seal: "none" });
  const name = "e2erevmem";
  const probes: string[] = [];
  // Answered requests only: a probe the reload in expectOpens aborts got no
  // answer, so asking again is right.
  page.on("requestfinished", (req) => {
    if (req.url().includes("/kv/revision") && req.headers()["x-qrdb-database"] === name) probes.push(req.url());
  });

  await createThroughWizard(page, name, "memory");
  await expectOpens(page, name);
  const aside = page.locator("aside");
  for (let i = 0; i < 2; i++) {
    await aside.getByRole("button", { name: "system", exact: true }).first().click();
    await expect(page.getByText("system / Data")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await aside.getByRole("button", { name, exact: true }).first().click();
    await expect(page.getByText(`${name} / Data`)).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("No data available")).toBeVisible({ timeout: LOAD_TIMEOUT });
  }

  expect(probes.length, `revision requests for ${name}: ${JSON.stringify(probes)}`).toBeLessThanOrEqual(1);
  // The revision controls say why they are unavailable instead of vanishing.
  await expect(page.getByTestId("kv-revisions-unavailable")).toBeVisible();
  await expect(page.getByRole("button", { name: "Compact to revision" })).toBeDisabled();
  expect(await page.locator(".toast .alert-error").allInnerTexts()).toEqual([]);
});
