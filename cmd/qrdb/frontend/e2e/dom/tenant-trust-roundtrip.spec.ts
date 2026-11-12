/**
 * Tenant trust anchors and tenant share holders against a live qrdbd (DOM tier).
 *
 * Boots a real daemon, provisions a tenant over REST, then drives the UI:
 * the tenant detail's trust-anchor card against strict tenant isolation (the
 * system caller is refused, and the UI must say so), and enrolment of a share
 * holder from the Holders panel. After each step the daemon's own REST answer
 * is checked, so a UI that only claims success fails here.
 *
 * Coverage: tenants.addTrustAnchors, tenants.listTrustAnchors,
 * tenants.removeTrustAnchor, officers.registerTenantOfficer.
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
const TENANT = "acme";

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

/** Runs openssl with a fresh in-memory P-256 key handed over in a scratch dir removed at once. */
function opensslWithFreshKey(args: (keyPath: string) => string[]): string {
  const { privateKey } = generateKeyPairSync("ec", { namedCurve: "prime256v1" });
  const pem = privateKey.export({ type: "pkcs8", format: "pem" }).toString();
  const dir = mkdtempSync(join(tmpdir(), "qrdb-trust-"));
  try {
    const keyPath = join(dir, "key.pem");
    writeFileSync(keyPath, pem, { mode: 0o600 });
    return execFileSync("openssl", args(keyPath)).toString();
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** A self-signed CA certificate (PEM) for subject CN=cn. */
function makeCACert(cn: string): string {
  return opensslWithFreshKey((key) => [
    "req", "-x509", "-new", "-sha256", "-key", key, "-days", "30", "-subj", `/CN=${cn}`,
    "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign",
  ]);
}

/** A PKCS#10 CSR for CN=username. */
function makeCSR(username: string): string {
  return opensslWithFreshKey((key) => ["req", "-new", "-sha256", "-key", key, "-subj", `/CN=${username}`]);
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

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none-committed" });
  await initializeCeremony(daemon);
  await waitForHealthy(daemon);
  const r = await api(daemon, "POST", "/v1/tenants", {
    id: TENANT,
    name: TENANT,
    // Barrier isolation needs a volume pool this daemon does not configure;
    // a prefix tenant still has holders of its own (registerTenantOfficer).
    isolation_type: "prefix",
    // A tenant is created with its first admin's credential or not at all.
    admin_username: "acme-admin",
    admin_csr_pem: makeCSR("acme-admin"),
  });
  expect(r.status, `create tenant ${TENANT}: ${r.text.slice(0, 300)}`).toBeLessThan(300);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens the Tenants view. */
async function openTenants(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.locator('aside button[title="Tenants"]').click();
  await expect(page.getByRole("button", { name: "Create Tenant" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Opens the tenant's detail view from the tenants table. */
async function openTenantDetail(page: Page): Promise<void> {
  await openTenants(page);
  const main = page.locator("main");
  await expect(main.locator("table").getByText(TENANT, { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await main.locator("table").getByText(TENANT, { exact: true }).first().click();
  await expect(page.getByText("Trust Anchors", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// Strict tenant isolation (owner ruling 2026-10-01, pkg/tenant Confine): only
// the tenant's own admin manages its trust anchors; the system tenant the UI
// acts as in this tier (auth off, Vite proxy) is refused list, add and remove.
// The spec pins that the UI reports the refusal and never shows a refused list
// as an empty one, and that nothing reaches the daemon.
test("tenant trust anchors: a cross-tenant caller is refused and the UI says so (UI ↔ live daemon)", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  expect(daemon).not.toBeNull();
  const d = daemon!;
  const refused = `not permitted to list trust anchors in tenant ${TENANT}`;
  expect((await api(d, "GET", `/v1/tenants/${TENANT}/trust-anchors`)).text).toContain(refused);

  // --- List: the refusal is shown in place of the table ----------------------------
  await openTenantDetail(page);
  await expect(page.getByTestId("trust-anchors-error")).toContainText(refused, { timeout: LOAD_TIMEOUT });
  await expect(page.getByText("No trust anchors registered.")).toHaveCount(0);
  await expect(page.getByTestId("trust-anchors-table")).toHaveCount(0);

  // --- Add: refused, the PEM stays in the form for a retry --------------------------
  const pem = makeCACert("Acme Login CA");
  await page.getByTestId("add-trust-anchors-input").fill(pem);
  const addResp = page.waitForResponse(
    (r) => r.url().includes(`/v1/tenants/${TENANT}/trust-anchors`) && r.request().method() === "POST",
  );
  await page.getByTestId("add-trust-anchors-btn").click();
  expect((await addResp).status()).toBe(403);
  await expect(page.locator(".toast .alert").filter({ hasText: "not permitted" }).last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByTestId("add-trust-anchors-input")).toHaveValue(pem);
  await expect(page.getByTestId("trust-anchors-table")).toHaveCount(0);

  // --- Remove: refused on the wire as well -----------------------------------------
  const del = await api(d, "DELETE", `/v1/tenants/${TENANT}/trust-anchors/${"0".repeat(64)}`);
  expect(del.status, del.text).toBe(403);

  expect(errors).toEqual([]);
});

test("tenant share holder enrols from the Holders panel (UI ↔ live daemon)", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  expect(daemon).not.toBeNull();
  const d = daemon!;
  expect(await serverHolderNames(d)).toEqual([]);

  await openTenants(page);
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
    throw new Error(`enrol alice: alerts ${JSON.stringify(alerts)} toasts ${await toasts(page)}\n${err}`);
  }
  // The panel reloads its list from the daemon, which now holds alice.
  await expect(panel.getByTestId("tenant-holders-table")).toContainText("alice", { timeout: LOAD_TIMEOUT });
  expect(await serverHolderNames(d)).toEqual(["alice"]);

  expect(errors).toEqual([]);
});
