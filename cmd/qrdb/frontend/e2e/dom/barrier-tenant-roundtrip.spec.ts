/**
 * A barrier-isolated tenant and its share holders against a live qrdbd (DOM tier).
 *
 * Boots a real daemon over a committed no-seal barrier with one configured
 * "dir" volume pool (a barrier tenant's own barrier lives on a volume, and the
 * volume service exists only over a barrier), initializes it through the
 * ceremony API, then drives the UI: the pool is entered in the catalog from the
 * Volume Pools view, a barrier-isolated tenant is created from the Tenants view,
 * and a share holder is enrolled from the tenant's Holders panel. After each
 * step the daemon's own REST answer is checked, so a UI that only claims
 * success fails here.
 *
 * Coverage: tenants.create (isolation "barrier"), officers.registerTenantOfficer,
 * officers.listTenantOfficers.
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
const TENANT = "vault-co";
const POOL = "fast";

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
    so_pin: "so-pin-barrier-tenant-dom-e2e",
    user_pin: "user-pin-barrier-tenant-dom-e2e",
    admin_generate: true,
  });
  if (r.status >= 300) throw new Error(`ceremony initialize returned ${r.status}: ${r.text.slice(0, 300)}`);
}

/** A PKCS#10 CSR for CN=username; the fresh P-256 key is handed over in a scratch dir removed at once. */
function makeCSR(username: string): string {
  const { privateKey } = generateKeyPairSync("ec", { namedCurve: "prime256v1" });
  const pem = privateKey.export({ type: "pkcs8", format: "pem" }).toString();
  const dir = mkdtempSync(join(tmpdir(), "qrdb-barrier-tenant-"));
  try {
    const keyPath = join(dir, "key.pem");
    writeFileSync(keyPath, pem, { mode: 0o600 });
    return execFileSync("openssl", ["req", "-new", "-sha256", "-key", keyPath, "-subj", `/CN=${username}`]).toString();
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** The usernames of the tenant barrier's share holders, as the daemon lists them. */
async function serverHolderNames(d: DomDaemon): Promise<string[]> {
  const r = await api(d, "GET", `/v1/tenants/${TENANT}/barrier/officers`);
  expect(r.status, r.text).toBe(200);
  const j = r.json as { username?: string }[] | Record<string, { username?: string }[] | undefined> | null;
  const list = Array.isArray(j) ? j : (j?.["officers"] ?? j?.["items"] ?? []);
  return list.map((h) => h.username ?? "").sort();
}

/** Toast texts, to explain a failed wait. */
async function toasts(page: Page): Promise<string> {
  return JSON.stringify(await page.locator(".toast .alert").allInnerTexts());
}

async function expectToast(page: Page, text: string): Promise<void> {
  try {
    await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  } catch (err) {
    throw new Error(`toast "${text}" not shown; toasts ${await toasts(page)}\n${err}`, { cause: err });
  }
}

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none-committed", volumePools: [POOL] });
  await initializeCeremony(daemon);
  await waitForHealthy(daemon);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

test("barrier-isolated tenant is created and enrols a share holder (UI ↔ live daemon)", async ({ page }) => {
  test.setTimeout(90_000);
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  expect(daemon).not.toBeNull();
  const d = daemon!;

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });

  // --- Pool: the node's configured "dir" pool enters the catalog --------------------
  await page.getByRole("button", { name: "Volume Pools", exact: true }).click();
  await expect(page.getByText("No volume pools in the catalog.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Create Pool" }).first().click();
  await page.locator("#volume-pool-name").fill(POOL);
  await page.locator("#volume-pool-provider").fill("dir");
  await page.getByTestId("volume-pool-create-submit").click();
  await expectToast(page, `Volume pool "${POOL}" created`);
  expect((await api(d, "GET", `/v1/volume-pools/${POOL}`)).status).toBe(200);

  // --- Create: a barrier-isolated tenant on the pool, from the Tenants view ----------
  await page.getByRole("button", { name: "Tenants", exact: true }).click();
  await page.getByRole("button", { name: "Create Tenant" }).click();
  await expect(page.getByRole("heading", { name: "Create Tenant" })).toBeVisible();
  await page.locator("#tenant-id").fill(TENANT);
  await page.locator("#tenant-name").fill(TENANT);
  await page.getByTestId("create-tenant-isolation-select").selectOption("barrier");
  await page.getByRole("textbox", { name: "Admin Username" }).fill("vault-admin");
  await page.getByRole("textbox", { name: "Admin CSR PEM" }).fill(makeCSR("vault-admin"));
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expectToast(page, `Tenant "${TENANT}" created.`);
  const record = await api(d, "GET", `/v1/tenants/${TENANT}`);
  expect(record.status, record.text).toBe(200);
  expect(record.json).toMatchObject({ id: TENANT, isolation_type: "barrier" });
  expect(await serverHolderNames(d)).toEqual([]);
  // The result page hands over the admin certificate, signed by the server.
  await expect(page.getByRole("heading", { name: "Tenant Created" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: /Admin Certificate PEM/ })).toHaveValue(/BEGIN CERTIFICATE/);
  await page.getByRole("button", { name: "Done", exact: true }).click();

  // --- Holders: enrol a share holder of the tenant's own barrier ---------------------
  await page.getByTestId(`tenant-holders-btn-${TENANT}`).click();
  const panel = page.getByTestId("tenant-holders-panel");
  await expect(panel.getByText("No share holders enrolled.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await panel.getByTestId("tenant-holder-enrol-toggle").click();
  await panel.getByTestId("officer-username-0").fill("alice");
  await panel.getByTestId("officer-csr-0").fill(makeCSR("alice"));
  await panel.getByTestId("register-officer-btn-0").click();
  try {
    await expect(panel.getByTestId("officer-cert-0")).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const alerts = await panel.locator('[role="alert"]').allInnerTexts();
    throw new Error(`enrol alice: alerts ${JSON.stringify(alerts)} toasts ${await toasts(page)}\n${err}`, { cause: err });
  }
  await expect(panel.getByTestId("tenant-holders-table")).toContainText("alice", { timeout: LOAD_TIMEOUT });
  expect(await serverHolderNames(d)).toEqual(["alice"]);

  expect(errors).toEqual([]);
});
