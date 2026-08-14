// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * admin-views-no-pageerror.spec.ts (real-backend DOM tier — single node)
 *
 * Generalizes the "hotspots" crash class (see e2e/dom/cluster-topology.spec.ts
 * item 6): several REST DTOs tag array-typed response fields
 * `json:"...,omitempty"` on the Go side, which drops the key entirely from the
 * wire whenever the slice is empty — the COMMON, freshly-booted state. Mocked
 * Playwright fixtures never reproduce this because they always hand the
 * component the exact shape it expects (e.g. `{ tenants: [] }`), never an
 * omitted key. Only a real qrdbd round trip exercises the true wire encoding.
 *
 * This spec boots a single fresh, unsealed, empty qrdbd (dom-daemon.ts) and
 * sweeps every standalone-mode admin nav view, asserting each renders with:
 *   - no unhandled JS "pageerror" (the symptom of an unguarded `.length` /
 *     `.map` / `#each` on an omitted-then-undefined array field), and
 *   - visible, non-blank content (the view actually mounted).
 *
 * The daemon is freshly initialized with nothing provisioned beyond the
 * built-in "system" database qrdbd always boots with, so every other list
 * endpoint (backups, tenants, users, roles, certs, credentials, audit
 * events) is in its true empty-array state — precisely the state that
 * triggers Go's `omitempty` to drop the field. The Databases view itself is
 * covered separately below in its minimal-but-nonempty (one row) state.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Registers a pageerror collector active for the lifetime of the page. */
function attachPageErrorCollector(page: Page): Error[] {
  const errors: Error[] = [];
  page.on("pageerror", (err) => errors.push(err));
  return errors;
}

/**
 * Clicks an admin nav item by its `title` attribute, scoped to the sidebar.
 * A bare role/text locator is ambiguous: the sidebar database-tree section
 * also renders a "Databases" section-header button distinct from the
 * AdminNav "Databases" item, so `getByRole("button", { name: "Databases" })`
 * matches two elements.
 */
async function clickAdminNavItem(page: Page, label: string): Promise<void> {
  await page.locator(`aside button[title="${label}"]`).click();
}

/** Standalone-mode admin nav items (no live cluster, so Cluster/Topology/
 * Rebalance are absent — those are covered against a real cluster in
 * cluster-topology.spec.ts). */
const STANDALONE_ADMIN_ITEMS: readonly string[] = [
  "Dashboard",
  "Seal Manager",
  "Tenants",
  "Users",
  "Roles",
  "Certificates",
  "Credentials",
  "Databases",
  "Backups",
  "Audit Log",
  "Settings",
] as const;

test("standalone admin views render against a real empty backend without a JS page error", async ({ page }) => {
  const pageErrors = attachPageErrorCollector(page);

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  for (const item of STANDALONE_ADMIN_ITEMS) {
    await clickAdminNavItem(page, item);
    await expect(page.locator(".breadcrumbs").getByText(item)).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Let any async list-fetch (databases/backups/tenants/etc.) settle before
    // checking for accumulated errors and moving to the next view.
    await page.waitForTimeout(250);

    if (pageErrors.length > 0) {
      const messages = pageErrors.map((e) => e.message).join("\n  ");
      throw new Error(`JavaScript exception(s) after navigating to "${item}" against a real empty backend:\n  ${messages}`);
    }

    // The view actually mounted content, not a blank error boundary.
    await expect(page.locator("main")).not.toBeEmpty();
  }
});

test("Databases view lists the built-in system database against a real backend without crashing", async ({ page }) => {
  // qrdbd always boots with one built-in "system" kv database, so this is
  // the minimal-but-nonempty list state rather than a true empty state (see
  // the standalone-mode sweep above for pageerror coverage on views that
  // really do start at zero items: Backups, Tenants, Users, Roles, etc.).
  const pageErrors = attachPageErrorCollector(page);

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  await clickAdminNavItem(page, "Databases");
  await expect(page.locator(".breadcrumbs").getByText("Databases")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.locator("main").getByText("system")).toBeVisible({ timeout: LOAD_TIMEOUT });

  expect(pageErrors).toEqual([]);
});

test("Backups view lists zero backups against a real empty backend without crashing", async ({ page }) => {
  const pageErrors = attachPageErrorCollector(page);

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  await clickAdminNavItem(page, "Backups");
  await expect(page.locator(".breadcrumbs").getByText("Backups")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByText("No backups found.")).toBeVisible({ timeout: LOAD_TIMEOUT });

  expect(pageErrors).toEqual([]);
});

test("Tenants view lists zero tenants against a real empty backend without crashing", async ({ page }) => {
  const pageErrors = attachPageErrorCollector(page);

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  await clickAdminNavItem(page, "Tenants");
  await expect(page.getByRole("button", { name: "Create Tenant" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.waitForTimeout(250);

  expect(pageErrors).toEqual([]);
});
