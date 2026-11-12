/**
 * DOM e2e: the UI over a server that asks for TLS client certificates.
 *
 * The daemon boots with --tls-client-auth=request: the TLS handshake asks for
 * a client certificate but does not require one. The browser (through the
 * Vite proxy) and the REST checks below present none, so every step here
 * proves the UI works end to end on such a server without a client
 * certificate: server initialization, seal and unseal, and opening a
 * database.
 *
 * Refs #1478.
 */
import * as https from "node:https";
import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon";

const PASSPHRASE = "tls-request-passphrase-0123";
const SO_PIN = "TlsSoPin-123456";
const USER_PIN = "TlsUserPin-123456";
const LOAD_TIMEOUT = 20_000;

let daemon: DomDaemon | null = null;

/** GETs a path on the daemon over TLS with no client certificate. */
function getWithoutClientCert(path: string): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const req = https.get(`${daemon!.adminUrl}${path}`, { rejectUnauthorized: false }, (res) => {
      let body = "";
      res.setEncoding("utf8");
      res.on("data", (chunk: string) => {
        body += chunk;
      });
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body }));
    });
    req.on("error", reject);
  });
}

async function completeAdminStep(page: Page): Promise<void> {
  await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible({ timeout: 10_000 });
  await page.getByTestId("admin-mode-generate").check();
  await page.getByTestId("so-pin-input").fill(SO_PIN);
  await page.getByTestId("so-pin-confirm-input").fill(SO_PIN);
  await page.getByTestId("user-pin-input").fill(USER_PIN);
  await page.getByTestId("user-pin-confirm-input").fill(USER_PIN);
  await page.getByTestId("admin-next-btn").click();
}

/** Opens a tool of the "system" database from the app shell. */
async function openSystemTool(page: Page, tool: string): Promise<void> {
  await page.locator("aside").getByText("system", { exact: true }).first().click();
  await page.getByRole("tab", { name: tool, exact: true }).or(
    page.getByRole("button", { name: tool, exact: true }),
  ).first().click();
  await expect(page.getByRole("heading", { name: tool, exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

test("tls client auth request: init, seal, unseal and open a database with no client certificate", async ({ page }) => {
  daemon = await startDaemon({ seal: "passphrase", tlsClientAuth: "request" });

  // The handshake completes for a client that presents no certificate: the
  // sealed, uninitialized server answers its health probe with 503.
  const health = await getWithoutClientCert("/health");
  expect(health.status).toBe(503);

  await page.goto(UI_BASE_URL);
  await expect(page.getByRole("heading", { name: "Server Initialization" })).toBeVisible({ timeout: 15_000 });
  await page.getByPlaceholder("Enter a strong passphrase").fill(PASSPHRASE);
  await page.getByPlaceholder("Confirm passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Next" }).click();
  await completeAdminStep(page);
  await expect(page.getByRole("heading", { name: "Server Initialized" })).toBeVisible({ timeout: LOAD_TIMEOUT });

  await waitForHealthy(daemon);
  await page.getByRole("button", { name: "Open Database" }).click();
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Seal and unseal through the Seal Manager.
  await page.getByText("Seal Manager").first().click();
  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();
  await expect(page.getByText("Unsealed").first()).toBeVisible();
  await page.getByRole("button", { name: "Seal", exact: true }).click();
  await page.getByRole("textbox", { name: "SO PIN", exact: true }).fill(SO_PIN);
  await page.getByRole("button", { name: "Confirm Seal" }).click();
  await expect(page.getByText("Sealed").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByPlaceholder("Enter passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();
  await expect(page.getByRole("button", { name: "Seal", exact: true })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // The server is unsealed and healthy for a client with no certificate.
  await waitForHealthy(daemon);
  const after = await getWithoutClientCert("/health");
  expect(after.status).toBe(200);

  // Open the system database.
  await openSystemTool(page, "Leases");
});

test("tls client auth request: an auth-off server opens its database with no client certificate", async ({ page }) => {
  daemon = await startDaemon({ seal: "none", tlsClientAuth: "request" });
  await waitForHealthy(daemon);

  const health = await getWithoutClientCert("/health");
  expect(health.status).toBe(200);

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await openSystemTool(page, "Leases");
  await openSystemTool(page, "Locks");
});
