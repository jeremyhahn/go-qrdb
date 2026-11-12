// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Real-server roundtrip for the Audit Log view (AuditLog -> audit.query ->
// GET /v1/audit/events) against a live qrdbd. Audited changes are made
// through the REST API (backups, each recording a "permitted" decision and a
// "success" outcome, on top of the daemon's own bootstrap events); refused
// requests are made too and must not appear. Every filter the view offers is
// then set in the UI and the rows shown are checked against the server's own
// answer to the same query; the table's paging and an event's detail panel
// are checked against the server's records.

import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const BACKUPS = 7;
const PAGE_SIZE = 20;
/** The view's server page size (AUDIT_PAGE_SIZE in AuditLog.svelte). */
const SERVER_PAGE_SIZE = 100;
const TENANT_ID = "audit-tenant";

let daemon: DomDaemon | null = null;

const SO_PIN = "audit-log-roundtrip-so-pin-0123";
const USER_PIN = "audit-log-roundtrip-user-pin-0123";

// Tenants are onboarded only over a barrier (seal "none" answers 501), so the
// tenant-filter test boots a committed no-seal barrier and initializes it
// through the ceremony API; the other test runs on seal "none".
test.beforeEach(async ({ request }, testInfo) => {
  if (!testInfo.title.startsWith("audit log tenant")) {
    daemon = await startDaemon({ seal: "none" });
    await waitForHealthy(daemon);
    return;
  }
  daemon = await startDaemon({ seal: "none-committed" });
  const init = await request.post(`${daemon.adminUrl}/v1/ceremony/initialize`, {
    data: { so_pin: SO_PIN, user_pin: USER_PIN, admin_generate: true },
    ignoreHTTPSErrors: true,
  });
  expect(init.ok(), `ceremony init: ${init.status()} ${await init.text()}`).toBeTruthy();
  await waitForHealthy(daemon);
});

/** Generates a P-256 CSR for `cn` with openssl; the key is discarded. */
function makeCSR(cn: string): string {
  const dir = mkdtempSync(join(tmpdir(), "qrdb-csr-"));
  try {
    return execFileSync(
      "openssl",
      [
        "req", "-new",
        "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-nodes", "-keyout", join(dir, "key.pem"),
        "-subj", `/CN=${cn}`,
      ],
      { encoding: "utf8" },
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

interface ServerEvent {
  timestamp: string;
  actor: string;
  tenant_id?: string;
  role?: string;
  resource: string;
  action: string;
  outcome: string;
  attributes?: Record<string, unknown>;
  client_ip?: string;
  request_id?: string;
  prev_hash?: string;
}

interface ServerPage {
  events: ServerEvent[];
  next?: string;
  total: number;
}

/** The server's answer to the query the SDK's audit.query sends. */
async function serverQuery(request: APIRequestContext, params: Record<string, string>): Promise<ServerPage> {
  const qs = new URLSearchParams(params).toString();
  const r = await request.get(`${daemon!.adminUrl}/v1/audit/events${qs === "" ? "" : `?${qs}`}`, {
    ignoreHTTPSErrors: true,
  });
  expect(r.ok(), `audit query ${qs}: ${r.status()}`).toBeTruthy();
  return (await r.json()) as ServerPage;
}

/** A datetime-local input value for `d` in the browser's local time. */
function localInput(d: Date): string {
  const p = (n: number): string => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

interface UIFilter {
  tenant?: string;
  actor?: string;
  resource?: string;
  action?: string;
  outcome?: string;
  from?: string;
  to?: string;
}

/** Sets every filter input in the view (unset ones cleared) and applies. */
async function applyFilter(page: Page, f: UIFilter): Promise<void> {
  await page.locator("#audit-filter-tenant").selectOption(f.tenant ?? "");
  await page.getByPlaceholder("Filter by actor").fill(f.actor ?? "");
  await page.getByPlaceholder("Filter by resource").fill(f.resource ?? "");
  await page.getByPlaceholder("Filter by action").fill(f.action ?? "");
  await page.locator("select").filter({ has: page.locator('option[value="denied"]') }).selectOption(f.outcome ?? "");
  const dates = page.locator('input[type="datetime-local"]');
  await dates.nth(0).fill(f.from ?? "");
  await dates.nth(1).fill(f.to ?? "");
  await page.getByRole("button", { name: "Apply", exact: true }).click();
}

/** The data rows of the audit table (header and empty-state rows excluded). */
function dataRows(page: Page) {
  return page.locator("tbody tr").filter({ has: page.locator("td") }).filter({ hasNotText: "No " });
}

/** Asserts the view shows exactly the server's events for the filter. */
async function expectRowsMatch(page: Page, server: ServerPage): Promise<void> {
  expect(server.next ?? "").toBe("");
  const n = server.events.length;
  // The server reports total -1 when it does not know the count.
  expect([n, -1]).toContain(server.total);
  if (n > 0) {
    const summary = server.total > 0 ? `Showing ${n} of ${server.total} events` : `Showing ${n} events`;
    await expect(page.getByText(summary, { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  }
  const rows = dataRows(page);
  await expect(rows).toHaveCount(Math.min(n, PAGE_SIZE), { timeout: LOAD_TIMEOUT });
  if (n > PAGE_SIZE) return;
  // Each server event has its own row: resource, action and outcome agree.
  for (const ev of server.events) {
    const matching = server.events.filter(
      (o) => o.resource === ev.resource && o.action === ev.action && o.outcome === ev.outcome,
    ).length;
    await expect(
      rows.filter({ hasText: ev.resource }).filter({ hasText: ev.action }).filter({ hasText: ev.outcome }),
    ).toHaveCount(matching);
  }
}

test("audit log: every filter, paging and the event detail match the server (UI ↔ live daemon)", async ({ page, request }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  const opts = { ignoreHTTPSErrors: true };
  const base = daemon!.adminUrl;

  // Audited changes through the API: plaintext local backups.
  const ids: string[] = [];
  for (let i = 0; i < BACKUPS; i++) {
    const r = await request.post(`${base}/v1/backups`, {
      ...opts,
      data: { destination: `${daemon!.dataDir}/audit-backup-${i}`, encryption: "none" },
    });
    expect(r.status(), await r.text()).toBe(201);
    ids.push(((await r.json()) as { id: string }).id);
  }
  // A database is created too, and refused requests are made: none of them
  // reaches the audit log, so the server's totals below are unchanged by them.
  const db = await request.post(`${base}/v1/databases`, { ...opts, data: { name: "auditdb", backend: "memory" } });
  expect(db.status()).toBe(201);
  expect((await request.post(`${base}/v1/backups`, { ...opts, data: { destination: "", encryption: "none" } })).status()).toBe(400);
  expect((await request.post(`${base}/v1/backups/restore`, { ...opts, data: { backup_id: "no-such-backup" } })).status()).toBe(404);

  const all = await serverQuery(request, {});
  expect(all.events.length).toBeGreaterThan(PAGE_SIZE);
  expect(all.events.filter((e) => e.action === "backup.create")).toHaveLength(2 * BACKUPS);
  expect(all.events.some((e) => e.resource.includes("no-such-backup"))).toBe(false);

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByRole("button", { name: "Audit Log", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Audit Log", exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Unfiltered: every event, the table paged at 20 rows.
  await expectRowsMatch(page, all);

  // Resource.
  const resource = `backup:${ids[0]}`;
  await applyFilter(page, { resource });
  const byResource = await serverQuery(request, { resource });
  expect(byResource.events).toHaveLength(2);
  await expectRowsMatch(page, byResource);

  // Action.
  await applyFilter(page, { action: "backup.create" });
  await expectRowsMatch(page, await serverQuery(request, { action: "backup.create" }));

  // Outcome (the select), combined with the action.
  await applyFilter(page, { action: "backup.create", outcome: "success" });
  const succeeded = await serverQuery(request, { action: "backup.create", outcome: "success" });
  expect(succeeded.events).toHaveLength(BACKUPS);
  await expectRowsMatch(page, succeeded);

  // Outcome alone.
  await applyFilter(page, { outcome: "success" });
  await expectRowsMatch(page, await serverQuery(request, { outcome: "success" }));

  // Actor: the daemon's own posture change is recorded as "system".
  await applyFilter(page, { actor: "system" });
  const bySystem = await serverQuery(request, { actor: "system" });
  expect(bySystem.events.length).toBeGreaterThan(0);
  await expectRowsMatch(page, bySystem);


  // From / To: the view sends local datetime-local values as RFC 3339.
  const now = new Date();
  const future = localInput(new Date(now.getTime() + 5 * 60_000));
  const past = localInput(new Date(now.getTime() - 60 * 60_000));
  await applyFilter(page, { from: future });
  const fromFuture = await serverQuery(request, { from: new Date(future).toISOString() });
  expect(fromFuture.events).toHaveLength(0);
  await expectRowsMatch(page, fromFuture);
  await applyFilter(page, { action: "backup.create", from: past, to: future });
  const window = await serverQuery(request, {
    action: "backup.create",
    from: new Date(past).toISOString(),
    to: new Date(future).toISOString(),
  });
  expect(window.events).toHaveLength(2 * BACKUPS);
  await expectRowsMatch(page, window);
  await applyFilter(page, { to: past });
  await expectRowsMatch(page, await serverQuery(request, { to: new Date(past).toISOString() }));

  // Clear: back to every event; the second table page holds the rest.
  await page.getByRole("button", { name: "Clear", exact: true }).click();
  await expectRowsMatch(page, all);
  await page.getByRole("button", { name: /next/i }).first().click();
  await expect(dataRows(page)).toHaveCount(all.events.length - PAGE_SIZE, { timeout: LOAD_TIMEOUT });

  // Detail: the success event of the first backup shows the server's record.
  await applyFilter(page, { resource, outcome: "success" });
  const detail = await serverQuery(request, { resource, outcome: "success" });
  expect(detail.events).toHaveLength(1);
  await expectRowsMatch(page, detail);
  const ev = detail.events[0]!;
  await dataRows(page).first().click();
  const field = (name: string) => 
    page.locator("tr").filter({ has: page.locator("th", { hasText: new RegExp(`^${name}$`) }) }).locator("td");
  await expect(field("Timestamp")).toHaveText(ev.timestamp);
  await expect(field("Actor")).toHaveText(ev.actor);
  await expect(field("Resource")).toHaveText(ev.resource);
  await expect(field("Action")).toHaveText(ev.action);
  await expect(field("Outcome")).toHaveText(ev.outcome);
  if (ev.client_ip) await expect(field("Client IP")).toHaveText(ev.client_ip);
  if (ev.request_id) await expect(field("Request ID")).toHaveText(ev.request_id);
  if (ev.prev_hash) await expect(field("Prev Hash")).toHaveText(ev.prev_hash);
  for (const [k, v] of Object.entries(ev.attributes ?? {})) {
    await expect(page.locator("pre").filter({ hasText: `"${k}"` })).toContainText(JSON.stringify(v));
  }

  expect(errors).toEqual([]);
});

test("audit log load more: the view pages through the server's cursor until every event is shown (UI ↔ live daemon)", async ({ page, request }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  const opts = { ignoreHTTPSErrors: true };
  const base = daemon!.adminUrl;

  // More audited changes than one server page. KV puts are not audited, so
  // backups are made through the REST API: each records two events (the
  // "permitted" decision and the "success" outcome).
  for (let i = 0; 2 * i <= SERVER_PAGE_SIZE; i++) {
    const r = await request.post(`${base}/v1/backups`, {
      ...opts,
      data: { destination: `${daemon!.dataDir}/audit-load-more-${i}`, encryption: "none" },
    });
    expect(r.status(), await r.text()).toBe(201);
  }
  // The server's own count: follow its cursor to the last page.
  const first = await serverQuery(request, { limit: String(SERVER_PAGE_SIZE) });
  expect(first.events).toHaveLength(SERVER_PAGE_SIZE);
  expect(first.next ?? "").not.toBe("");
  let n = first.events.length;
  for (let cursor = first.next ?? ""; cursor !== ""; ) {
    const next = await serverQuery(request, { limit: String(SERVER_PAGE_SIZE), cursor });
    n += next.events.length;
    cursor = next.next ?? "";
  }
  expect(n).toBeGreaterThan(SERVER_PAGE_SIZE);
  const totalText = (shown: number) =>
    first.total > 0 ? `Showing ${shown} of ${first.total} events` : `Showing ${shown} events`;

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByRole("button", { name: "Audit Log", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Audit Log", exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // First page: exactly one server page, and a Load more button.
  await expect(page.getByText(totalText(SERVER_PAGE_SIZE), { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  const loadMore = page.getByRole("button", { name: "Load more" });
  await expect(loadMore).toBeVisible();

  // Load more until the server returns no cursor: every event is then shown.
  for (let shown = SERVER_PAGE_SIZE; shown < n; ) {
    await loadMore.click();
    shown = Math.min(shown + SERVER_PAGE_SIZE, n);
    await expect(page.getByText(totalText(shown), { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  }
  await expect(loadMore).toHaveCount(0);
  if (first.total > 0) expect(first.total).toBe(n);

  // The table holds every appended event, paged at 20 rows.
  await expect(page.getByText(`Showing 1 - ${PAGE_SIZE} of ${n} rows`)).toBeVisible();
  await expect(dataRows(page)).toHaveCount(PAGE_SIZE);

  expect(errors).toEqual([]);
});

test("audit log tenant filter: the picker lists the server's tenants and a cross-tenant query shows the server's refusal (UI ↔ live daemon)", async ({ page, request }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });

  // An audited change: a tenant created from the Tenants view.
  await page.getByRole("button", { name: "Tenants", exact: true }).click();
  await page.getByRole("button", { name: "Create Tenant" }).click();
  await expect(page.getByRole("heading", { name: "Create Tenant" })).toBeVisible();
  await page.locator("#tenant-id").fill(TENANT_ID);
  await page.locator("#tenant-name").fill(TENANT_ID);
  await page.getByRole("textbox", { name: "Admin Username" }).fill("audit-admin");
  await page.getByRole("textbox", { name: "Admin CSR PEM" }).fill(makeCSR("audit-admin"));
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.locator(".toast .alert").filter({ hasText: `Tenant "${TENANT_ID}" created.` }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  await page.getByRole("button", { name: "Audit Log", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Audit Log", exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expectRowsMatch(page, await serverQuery(request, {}));

  await expect(page.locator(`#audit-filter-tenant option[value="${TENANT_ID}"]`)).toHaveCount(1, { timeout: LOAD_TIMEOUT });
  // The server confines an audit query to the caller's own tenant: the
  // cluster caller asking for another tenant's events is refused (403), and
  // the view reports the server's refusal instead of showing rows.
  const refused = await request.get(`${daemon!.adminUrl}/v1/audit/events?tenant_id=${TENANT_ID}`, {
    ignoreHTTPSErrors: true,
  });
  expect(refused.status()).toBe(403);
  const reason = ((await refused.json()) as { error: string }).error;
  expect(reason).not.toBe("");
  await applyFilter(page, { tenant: TENANT_ID });
  await expect(page.locator(".toast .alert").filter({ hasText: "403" }).filter({ hasText: reason }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(dataRows(page)).toHaveCount(0);

  // Back to "Any": the caller's own events again.
  await applyFilter(page, {});
  await expectRowsMatch(page, await serverQuery(request, {}));

  expect(errors).toEqual([]);
});
