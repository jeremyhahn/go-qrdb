// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * join-token-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives admin.mintJoinToken from the Cluster view against a live qrdbd in
 * raft mode. Join tokens live in the store member-side CSR join backs, and
 * cluster_join needs a barrier, so the daemon boots the passthrough ("none")
 * seal strategy enabled, with cluster_join turned on through its QRDB_*
 * environment overrides, and is initialized through the ceremony:
 *
 *   mint a join token for a node id with a TTL in the "Mint Join Token" card,
 *   check the UI shows the token and its expiry, then mint through the
 *   daemon's REST API with the same node id and TTL and check the response
 *   has the same shape (a token of the same form, an expiry the same TTL out).
 */

import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const NODE_ID = "e2e-join-node";
const TTL_SECONDS = 600;
/** Slack between the UI's mint and the API's mint, and for clock rounding. */
const EXPIRY_SLACK_MS = 60_000;

/** cluster_join config, as the daemon's QRDB_* environment overrides. */
const CLUSTER_JOIN_ENV: Record<string, string> = {
  QRDB_CLUSTER_JOIN_ENABLED: "true",
  QRDB_CLUSTER_JOIN_LISTEN_HOST: "127.0.0.1",
  QRDB_CLUSTER_JOIN_LISTEN_PORT: "63094",
};

let daemon: DomDaemon | null = null;

/** Runs the first-boot ceremony so the sealed-window daemon finishes booting. */
async function initializeCeremony(d: DomDaemon): Promise<void> {
  const https = await import("https");
  await new Promise<void>((resolve, reject) => {
    const body = JSON.stringify({
      so_pin: "so-pin-dom-e2e",
      user_pin: "user-pin-dom-e2e",
      admin_generate: true,
    });
    const req = https.default.request(
      `${d.adminUrl}/v1/ceremony/initialize`,
      {
        method: "POST",
        rejectUnauthorized: false,
        headers: { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(body) },
      },
      (res) => {
        let text = "";
        res.on("data", (c: Buffer) => (text += c.toString()));
        res.on("end", () => {
          if (res.statusCode && res.statusCode < 300) resolve();
          else reject(new Error(`ceremony initialize returned ${res.statusCode}: ${text.slice(0, 300)}`));
        });
      },
    );
    req.on("error", reject);
    req.end(body);
  });
}

test.beforeEach(async () => {
  Object.assign(process.env, CLUSTER_JOIN_ENV);
  try {
    daemon = await startDaemon({ seal: "none-committed" });
  } finally {
    for (const k of Object.keys(CLUSTER_JOIN_ENV)) delete process.env[k];
  }
  await initializeCeremony(daemon);
  await waitForHealthy(daemon);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens the Cluster admin view. */
async function openCluster(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Cluster", { exact: true }).click();
  await expect(page.getByRole("heading", { name: "Mint Join Token" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Expects the toast; on a miss, reports the toasts on screen. */
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

/** The shape of a token: its length and the character classes it uses. */
function tokenShape(token: string): { length: number; alphabet: string } {
  const classes: string[] = [];
  if (/[a-z]/.test(token)) classes.push("lower");
  if (/[A-Z]/.test(token)) classes.push("upper");
  if (/[0-9]/.test(token)) classes.push("digit");
  const other = [...new Set(token.replace(/[A-Za-z0-9]/g, ""))].sort().join("");
  if (other !== "") classes.push(other);
  return { length: token.length, alphabet: classes.join(",") };
}

test("mint join token round-trip (UI ↔ live daemon)", async ({ page, request }) => {
  expect(daemon).not.toBeNull();

  // --- Mint in the UI. --------------------------------------------------------------
  await openCluster(page);
  const card = page.getByTestId("join-token-card");
  await card.getByLabel("Join node ID").fill(NODE_ID);
  await card.getByLabel("Join token TTL seconds").fill(String(TTL_SECONDS));
  const uiMintedAt = Date.now();
  const uiMint = page.waitForResponse((r) => r.url().includes("/v1/cluster/join-tokens"), { timeout: LOAD_TIMEOUT });
  await card.getByRole("button", { name: "Mint Token" }).click();
  const uiResp = await uiMint;
  expect(uiResp.status(), `UI mint: ${await uiResp.text()}`).toBe(201);
  await expectToast(page, `Join token minted for ${NODE_ID}`);

  const result = card.getByTestId("join-token-result");
  await expect(result).toBeVisible({ timeout: LOAD_TIMEOUT });
  const uiToken = (await result.getByTestId("join-token-value").innerText()).trim();
  expect(uiToken.length, "token shown in the UI").toBeGreaterThan(0);
  const expiryText = (await result.getByText(/^Expires /).innerText()).replace(/^Expires\s+/, "").trim();
  const uiExpiry = Date.parse(expiryText);
  expect(Number.isNaN(uiExpiry), `UI expiry "${expiryText}" parses as a time`).toBeFalsy();
  expect(Math.abs(uiExpiry - (uiMintedAt + TTL_SECONDS * 1000)), `UI expiry ${expiryText} is the TTL out`)
    .toBeLessThan(EXPIRY_SLACK_MS);

  // --- The daemon's answer to the same mint call has the same shape. ----------------
  const apiMintedAt = Date.now();
  const resp = await request.post(`${daemon!.adminUrl}/v1/cluster/join-tokens`, {
    data: { node_id: NODE_ID, ttl_seconds: TTL_SECONDS },
    ignoreHTTPSErrors: true,
  });
  const body = await resp.text();
  expect(resp.status(), `POST /v1/cluster/join-tokens: ${body}`).toBe(201);
  const minted = JSON.parse(body) as { token?: string; expires_at?: string };
  expect(Object.keys(minted).sort()).toEqual(["expires_at", "token"]);
  expect(typeof minted.token).toBe("string");
  expect(tokenShape(minted.token!)).toEqual(tokenShape(uiToken));
  // Join tokens are single-use secrets: a second mint is a new token.
  expect(minted.token).not.toBe(uiToken);
  const apiExpiry = Date.parse(minted.expires_at!);
  expect(Math.abs(apiExpiry - (apiMintedAt + TTL_SECONDS * 1000)), `API expiry ${minted.expires_at}`)
    .toBeLessThan(EXPIRY_SLACK_MS);
  expect(Math.abs(apiExpiry - uiExpiry), "UI and API expiries are the same TTL out").toBeLessThan(EXPIRY_SLACK_MS);

  // A mint without a node id is refused in the UI before any call.
  await card.getByLabel("Join node ID").fill("");
  await card.getByRole("button", { name: "Mint Token" }).click();
  await expectToast(page, "Node ID is required");
});
