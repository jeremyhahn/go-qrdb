// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * database-holders-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Share Holders panel (DatabaseHoldersPanel) of a Shamir database
 * against a live qrdbd: enrol holders with a CSR generated in the test,
 * remove one before initialization, initialize the custody split, then start
 * a rekey round and open a revocation round. After each step the daemon's
 * /v1 state (holder list, barrier status) matches what the panel shows.
 *
 * The database is created through the REST API (the wizard has its own
 * specs); every holder operation goes through the UI.
 */

import { test, expect, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const DB = "vault";

let daemon: DomDaemon | null = null;

/** Calls the daemon's REST API directly (self-signed TLS); returns status and parsed body. */
async function api(
  d: DomDaemon,
  method: string,
  path: string,
  body?: unknown,
): Promise<{ status: number; json: unknown; text: string }> {
  const https = await import("https");
  return new Promise((resolve, reject) => {
    const payload = body === undefined ? "" : JSON.stringify(body);
    const req = https.default.request(
      `${d.adminUrl}${path}`,
      {
        method,
        rejectUnauthorized: false,
        headers: payload
          ? { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(payload) }
          : {},
      },
      (res) => {
        let text = "";
        res.on("data", (c: Buffer) => (text += c.toString()));
        res.on("end", () => {
          let json: unknown = null;
          try {
            json = text ? JSON.parse(text) : null;
          } catch {
            json = null;
          }
          resolve({ status: res.statusCode ?? 0, json, text });
        });
      },
    );
    req.on("error", reject);
    req.end(payload);
  });
}

/** Runs the first-boot ceremony (what `qrdb init` does); the generated admin credential is discarded. */
async function initializeCeremony(d: DomDaemon): Promise<void> {
  const r = await api(d, "POST", "/v1/ceremony/initialize", {
    so_pin: "so-pin-dom-e2e",
    user_pin: "user-pin-dom-e2e",
    admin_generate: true,
  });
  if (r.status >= 300) throw new Error(`ceremony initialize returned ${r.status}: ${r.text.slice(0, 300)}`);
}

/** A PKCS#10 CSR for CN=username over a fresh in-memory P-256 key (the key lives only in a scratch dir removed at once). */
function makeCSR(username: string): string {
  const { privateKey } = generateKeyPairSync("ec", { namedCurve: "prime256v1" });
  const pem = privateKey.export({ type: "pkcs8", format: "pem" }).toString();
  // openssl cannot read a key from a pipe on every platform; hand it a private scratch file.
  const dir = mkdtempSync(join(tmpdir(), "qrdb-csr-"));
  try {
    const keyPath = join(dir, "key.pem");
    writeFileSync(keyPath, pem, { mode: 0o600 });
    return execFileSync("openssl", ["req", "-new", "-sha256", "-key", keyPath, "-subj", `/CN=${username}`]).toString();
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

interface Holder {
  username?: string;
  share_index?: number;
  shareIndex?: number;
}

async function serverHolders(d: DomDaemon): Promise<Holder[]> {
  const r = await api(d, "GET", `/v1/databases/${DB}/barrier/officers`);
  expect(r.status, r.text).toBe(200);
  const j = r.json as Holder[] | { officers?: Holder[]; items?: Holder[] };
  return Array.isArray(j) ? j : (j.officers ?? j.items ?? []);
}

async function serverHolderNames(d: DomDaemon): Promise<string[]> {
  return (await serverHolders(d)).map((h) => h.username ?? "").sort();
}

async function serverInitializedAt(d: DomDaemon): Promise<string> {
  const r = await api(d, "GET", `/v1/databases/${DB}/barrier/status`);
  expect(r.status, r.text).toBe(200);
  const j = r.json as Record<string, unknown>;
  const at = (j["initialized_at"] ?? j["initializedAt"] ?? "") as string;
  return at.startsWith("0001-") ? "" : at;
}

/** Shares in the test database's split: 2-of-2, or 2-of-3 where a revocation must leave a quorum. */
let sharesTotal = 2;

test.beforeEach(async ({}, testInfo) => {
  sharesTotal = testInfo.title.startsWith("revoking") ? 3 : 2;
  daemon = await startDaemon({ seal: "none-committed" });
  await initializeCeremony(daemon);
  await waitForHealthy(daemon);
  const r = await api(daemon, "POST", "/v1/databases", {
    name: DB,
    backend: "file",
    // An encryption barrier needs a dedicated store, not the shared default one;
    // databases/<name> is reserved by the server, so the store lives beside it.
    dataDir: `${daemon.dataDir}/stores/${DB}`,
    barrier: { enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal },
  });
  expect(r.status, `create ${DB}: ${r.text.slice(0, 300)}`).toBeLessThan(300);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens the Shamir database's detail. */
async function openHolders(page: Page): Promise<ReturnType<Page["getByTestId"]>> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.locator('aside button[title="Databases"]').click();
  const main = page.locator("main");
  await expect(main.getByText(DB, { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await main.getByText(DB, { exact: true }).first().click();
  const panel = page.getByTestId("db-holders-panel");
  await expect(panel).toBeVisible({ timeout: LOAD_TIMEOUT });
  return panel;
}

/** Enrols one holder through enrolment form i and waits for the issued certificate. */
async function enrolInUI(panel: ReturnType<Page["getByTestId"]>, i: number, username: string): Promise<void> {
  await panel.getByTestId(`officer-username-${i}`).fill(username);
  await panel.getByTestId(`officer-csr-${i}`).fill(makeCSR(username));
  await panel.getByTestId(`register-officer-btn-${i}`).click();
  try {
    await expect(panel.getByTestId(`officer-cert-${i}`)).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const alerts = await panel.locator('[role="alert"]').allInnerTexts();
    throw new Error(`enrol ${username}: alerts ${JSON.stringify(alerts)}\n${err}`);
  }
}

/** Enrols alice and bob and initializes custody, checking the daemon after each step. */
async function enrolAndInitialize(page: Page, d: DomDaemon): Promise<ReturnType<Page["getByTestId"]>> {
  const names = ["alice", "bob", "carol"].slice(0, sharesTotal);
  let panel = await openHolders(page);
  await expect(panel.getByText("No share holders enrolled.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(panel.getByTestId("db-holders-panel-initialize")).toContainText(`(0/${sharesTotal})`);
  for (const [i, name] of names.entries()) await enrolInUI(panel, i, name);
  expect(await serverHolderNames(d)).toEqual(names);
  expect(await serverInitializedAt(d)).toBe("");

  // Listed after a reload: every holder, no share yet.
  panel = await openHolders(page);
  const table = panel.getByTestId("db-holders-table");
  for (const name of names) await expect(table).toContainText(name, { timeout: LOAD_TIMEOUT });

  const initialize = panel.getByTestId("db-holders-panel-initialize");
  await expect(initialize).toBeEnabled();
  await initialize.click();
  await expect(panel.getByTestId("db-holders-initialized")).toContainText(`2-of-${sharesTotal}, ${sharesTotal}`, {
    timeout: LOAD_TIMEOUT,
  });
  await expect(panel.getByTestId("db-holders-resume")).toHaveCount(0);

  // The daemon reports the split: an initialization time and a share index per holder.
  expect(await serverInitializedAt(d)).not.toBe("");
  const holders = await serverHolders(d);
  expect(holders.map((h) => h.share_index ?? h.shareIndex ?? 0).sort()).toEqual(names.map((_, i) => i + 1));
  return panel;
}

test("holders enrol, list, unenrol, initialize, rekey round-trip (UI ↔ live daemon)", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  expect(daemon).not.toBeNull();
  const d = daemon!;

  // --- Enrol, then unenrol before initialization ---------------------------------
  let panel = await openHolders(page);
  await enrolInUI(panel, 0, "alice");
  await enrolInUI(panel, 1, "carol");
  expect(await serverHolderNames(d)).toEqual(["alice", "carol"]);

  panel = await openHolders(page);
  await expect(panel.getByTestId("db-holders-table")).toContainText("carol", { timeout: LOAD_TIMEOUT });
  await panel.getByTestId("db-holder-unenrol-carol").click();
  await expect(panel.getByTestId("db-holders-table")).not.toContainText("carol", { timeout: LOAD_TIMEOUT });
  await expect(panel.getByTestId("db-holders-panel-initialize")).toContainText("(1/2)");
  expect(await serverHolderNames(d)).toEqual(["alice"]);

  // --- Enrol the second holder and initialize -----------------------------------
  await enrolInUI(panel, 0, "bob");
  expect(await serverHolderNames(d)).toEqual(["alice", "bob"]);
  const initialize = panel.getByTestId("db-holders-panel-initialize");
  await expect(initialize).toBeEnabled({ timeout: LOAD_TIMEOUT });
  await initialize.click();
  await expect(panel.getByTestId("db-holders-initialized")).toContainText("2-of-2, 2", { timeout: LOAD_TIMEOUT });
  expect(await serverInitializedAt(d)).not.toBe("");

  // After a reload the panel shows the initialized custody: share indices, rekey panel.
  panel = await openHolders(page);
  const table = panel.getByTestId("db-holders-table");
  await expect(table.locator("tr").filter({ hasText: "alice" })).toContainText("Pending", { timeout: LOAD_TIMEOUT });
  await expect(table.locator("tr").filter({ hasText: "bob" })).toContainText("Pending");
  await expect(panel.getByTestId("db-holder-unenrol-alice")).toHaveCount(0);
  const indices = (await serverHolders(d)).map((h) => h.share_index ?? h.shareIndex ?? 0).sort();
  expect(indices).toEqual([1, 2]);
  for (const idx of indices) {
    await expect(table.locator("td.text-center").filter({ hasText: String(idx) })).toHaveCount(1);
  }

  // --- Start a rekey round ------------------------------------------------------
  await expect(page.getByTestId("rekey-panel")).toContainText(
    `qrdb database barrier-quorum submit --database ${DB} --op rekey`,
  );
  await page.getByTestId("rekey-threshold").fill("2");
  await page.getByTestId("start-rekey-btn").click();
  try {
    await expect(page.getByTestId("open-round")).toContainText("rekey round on the unseal domain", {
      timeout: LOAD_TIMEOUT,
    });
  } catch (err) {
    const alerts = await panel.locator('[role="alert"]').allInnerTexts();
    throw new Error(`start rekey: alerts ${JSON.stringify(alerts)}\n${err}`);
  }
  // A rekey round changes no holder until a quorum submits.
  expect(await serverHolderNames(d)).toEqual(["alice", "bob"]);

  expect(errors).toEqual([]);
});

test("revoking an initialized holder opens a removal round (UI ↔ live daemon)", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  expect(daemon).not.toBeNull();
  const d = daemon!;

  await enrolAndInitialize(page, d);
  await openHolders(page);
  await page.getByTestId("revoke-username").fill("bob");
  await page.getByTestId("revoke-holder-btn").click();
  try {
    await expect(page.getByTestId("open-round")).toContainText("Removes bob", { timeout: LOAD_TIMEOUT });
  } catch (err) {
    const toasts = await page.locator(".toast .alert").allInnerTexts();
    throw new Error(`revoke: toasts ${JSON.stringify(toasts)}\n${err}`);
  }
  // The revocation takes effect only once a quorum submits: bob still holds a share.
  expect(await serverHolderNames(d)).toEqual(["alice", "bob", "carol"]);

  expect(errors).toEqual([]);
});
