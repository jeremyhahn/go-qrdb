/**
 * A plain tenant user against a live daemon (#1478).
 *
 * The fixture boots an auth-enabled daemon, provisions tenant "acme" and its
 * admin, and has the admin invite a plain user holding only kv roles; the Vite
 * proxy presents that user's certificate. The spec checks what the UI offers
 * such a caller and what it does with the server's refusals:
 *
 * - the sidebar has no Server Admin section (the server refuses
 *   /admin/topology with 403);
 * - the tenant database lists and opens, and a key written in the KV explorer
 *   reads back over REST as the same user;
 * - the Dashboard shows the refusal instead of empty data.
 *
 * Each step is checked against the server over REST, as the system or tenant
 * admin.
 */
import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, daemonRequest, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 30_000;
const TENANT = "acme";
const DB = "acmedb";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  test.setTimeout(120_000);
  daemon = await startDaemon({ seal: "none-committed", tenantUser: { tenant: TENANT } });
  const d = daemon;
  const created = await daemonRequest(d.adminUrl, "POST", "/v1/databases", { name: DB, backend: "pebble" }, d.tenantAdmin);
  expect(created.status, created.text).toBeLessThan(300);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

async function expectToast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test("a plain tenant user sees no server admin views and works in its database", async ({ page }) => {
  test.setTimeout(120_000);
  const d = daemon!;

  // The server refuses this caller the server-wide views the UI must hide.
  for (const path of ["/admin/topology", "/v1/tenants/list"]) {
    const r = await daemonRequest(d.adminUrl, "GET", path, undefined, d.tenantUser);
    expect(r.status, `${path}: ${r.text}`).toBe(403);
  }
  // The system admin is not refused them, so the 403 is about the caller.
  const sys = await daemonRequest(d.adminUrl, "GET", "/v1/tenants/list", undefined, d.systemAdmin);
  expect(sys.status, sys.text).toBe(200);
  // The user does see its tenant's database.
  const dbs = await daemonRequest(d.adminUrl, "GET", "/v1/databases/list", undefined, d.tenantUser);
  expect(dbs.status, dbs.text).toBe(200);
  expect(dbs.text).toContain(DB);

  await page.goto(UI_BASE_URL);
  const aside = page.locator("aside");
  await expect(aside.getByText(DB, { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // No Server Admin section and none of its views.
  await expect(aside.getByText("Server Admin")).toHaveCount(0);
  await expect(aside.getByText("Seal Manager")).toHaveCount(0);
  await expect(aside.getByText("Tenants", { exact: true })).toHaveCount(0);

  // The Dashboard shows the refusal, not empty data.
  await expect(page.locator("main").getByText("Failed to load dashboard data")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expectToast(page, /403|forbidden|permission/i);

  // The tenant database opens and a key written in the KV explorer reads back.
  await aside.getByText(DB, { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  const key = "tenant-user/written-in-ui";
  const value = "hello from acme";
  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill(value);
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText(`Created "${key}"`).last()).toBeVisible({ timeout: LOAD_TIMEOUT });

  const back = await daemonRequest(d.adminUrl, "GET", `/kv/${encodeURIComponent(key)}`, undefined, d.tenantUser, {
    "X-QRDB-Database": DB,
  });
  expect(back.status, back.text).toBe(200);
  expect(back.text).toContain(value);
});

test("a refused tenant list shows the refusal, not an empty list", async ({ page }) => {
  test.setTimeout(120_000);
  const d = daemon!;
  const refused = await daemonRequest(d.adminUrl, "GET", "/v1/tenants/list", undefined, d.tenantUser);
  expect(refused.status, refused.text).toBe(403);

  // A server without the topology endpoint (404) keeps the admin views on
  // offer, so this caller can open Tenants; the tenant list's 403 is the live
  // server's.
  await page.route("**/admin/topology", (route) =>
    route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"not found"}' }),
  );
  await page.goto(UI_BASE_URL);
  await page.locator('aside button[title="Tenants"]').click();
  const main = page.locator("main");
  await expect(main.getByRole("heading", { name: "Tenants" }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(main.getByText(/403|forbidden|permission/i).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(main.getByText("No tenants found.")).toHaveCount(0);
});
