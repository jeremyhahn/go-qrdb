// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * user-views-no-pageerror.spec.ts (real-backend DOM tier — single node)
 *
 * The user-side counterpart of admin-views-no-pageerror.spec.ts. It boots a
 * fresh qrdbd whose Vite proxy presents a tenant admin's client certificate
 * (dom-daemon.ts `tenantAdmin`), so the UI runs as that tenant's session. One
 * database is created for the tenant over REST, then every view under
 * src/views/user is opened through the database workspace's tabs on the
 * tenant's fresh, empty database: the state in which a list field the server
 * tags `omitempty` is absent from the wire, and an unguarded `.length` or
 * `#each` throws.
 *
 * Each view must mount visible content with no unhandled "pageerror" and no
 * console error.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, daemonRequest, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const TENANT = "acme";
const DATABASE = "userviews";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none-committed", tenantAdmin: { tenant: TENANT } });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/**
 * Collects page errors, console errors and server faults for the lifetime of
 * the page. A tenant admin holds no data permissions (strict tenant isolation:
 * it cannot grant itself any, and the system admin cannot create roles in its
 * tenant), so the daemon refuses the data views' reads with 403 and the
 * browser logs "Failed to load resource" for each: that log line is the
 * browser's, not the app's, and a 4xx refusal is the view's input here. A 5xx
 * is a fault and fails the sweep.
 */
function attachErrorCollector(page: Page): string[] {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(`pageerror: ${err.message}`));
  page.on("console", (msg) => {
    if (msg.type() !== "error") return;
    if (msg.text().startsWith("Failed to load resource: the server responded with a status of 4")) return;
    errors.push(`console.error: ${msg.text()}`);
  });
  page.on("response", async (resp) => {
    if (resp.status() < 500) return;
    const u = new URL(resp.url());
    const body = await resp.text().catch(() => "");
    errors.push(`${resp.status()} ${resp.request().method()} ${u.pathname}${u.search}: ${body.slice(0, 200)}`);
  });
  return errors;
}

/**
 * The database workspace's tabs, one per view under src/views/user
 * (DatabaseWorkspace hosts them; Data is KVExplorer). SQL Browser
 * (SQLBrowserScoped) is offered only once a SQL wire protocol is enabled and
 * is visited after the sweep.
 */
const KV_TABS: readonly string[] = [
  "Data",
  "Collections",
  "Indexes",
  "Transactions",
  "Batch",
  "Watch",
  "Leases",
  "Locks",
  "Time Series",
  "Vectors",
  "Settings",
] as const;

/** Creates a database as the tenant admin and checks the daemon now lists it. */
async function createDatabase(d: DomDaemon, name: string, body: Record<string, unknown>): Promise<void> {
  const r = await daemonRequest(d.adminUrl, "POST", "/v1/databases", { name, ...body }, d.tenantAdmin);
  expect(r.status, `create database ${name}: ${r.text.slice(0, 300)}`).toBeLessThan(300);
}

/** Opens the app shell and the named database's workspace from the sidebar. */
async function openDatabase(page: Page, name: string): Promise<void> {
  await page.goto(UI_BASE_URL);
  const db = page.locator("aside").getByText(name, { exact: true }).first();
  await expect(db).toBeVisible({ timeout: 20_000 });
  await db.click();
}

async function visitTabs(page: Page, errors: string[], tabs: readonly string[]): Promise<void> {
  const main = page.locator("main");
  for (const label of tabs) {
    await main.getByRole("tab", { name: label, exact: true }).click();
    await page.waitForLoadState("networkidle", { timeout: LOAD_TIMEOUT });
    if (errors.length > 0) {
      throw new Error(`error(s) after opening the "${label}" tab as a tenant admin:\n  ${errors.join("\n  ")}`);
    }
    await expect(main).not.toBeEmpty();
  }
}

test("every user view renders for a tenant admin on an empty database without a page or console error", async ({
  page,
}) => {
  // Eleven views, each waited to network idle on a live daemon.
  test.setTimeout(120_000);
  const errors = attachErrorCollector(page);
  await createDatabase(daemon!, DATABASE, { backend: "pebble" });
  await openDatabase(page, DATABASE);
  await visitTabs(page, errors, KV_TABS);

  // SQL Browser (SQLBrowserScoped) is offered once a SQL wire protocol is
  // enabled in Settings, the tab the sweep ends on.
  const tabList = page.getByRole("tablist", { name: "Database tools" });
  await page.locator("main input.toggle").first().check();
  await expect(page.getByText(/MySQL enabled/i).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(tabList.getByText("SQL Browser")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await visitTabs(page, errors, ["SQL Browser"]);
  expect(errors).toEqual([]);
});
