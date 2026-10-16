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

/**
 * Multi-tenancy admin UI — web-mode (REST) tests.
 *
 * These tests run in pure web mode (no Wails bindings) so the SDK takes the
 * RestClient path and all server calls go over fetch(), intercepted with
 * page.route(). They assert the request payloads the GUI sends carry the
 * shipped multi-tenant fields in their canonical snake_case wire form:
 *
 *   (a) the tenant-detail quota form POSTs the new quota fields
 *       (max_storage_bytes, max_database_size_bytes, max_replicas_per_group,
 *       max_total_replicas) to POST /v1/tenants/{id}/quotas;
 *   (b) the entitlements section PATCHes /v1/tenants/{id}/entitlements with
 *       snake_case entitlement fields;
 *   (c) the isolation-type selector offers all four modes
 *       (prefix / barrier / volume / raft).
 */

import { webTest as test, expect, injectWebRestMock } from "./fixtures/web-mode-mock.ts";
import type { Page, Route } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

const TENANT_ID = "acme";

/** A snake_case tenant record as returned by the REST server. */
const tenantRecord = {
  id: TENANT_ID,
  name: "Acme Corp",
  isolation_type: "barrier",
  quotas: {
    max_databases: 2,
    max_raft_groups: 1,
    max_replicas_per_group: 0,
    max_total_replicas: 0,
    max_collections: 0,
    max_keys: 0,
    max_storage_bytes: 0,
    max_database_size_bytes: 0,
    max_users: 0,
    max_roles: 0,
    max_certificates: 0,
    max_backups: 0,
    max_vector_dims: 0,
    max_ts_metrics: 0,
  },
  entitlements: {
    allowed_isolation_modes: ["prefix", "barrier"],
    allow_dedicated_raft: false,
    allow_volume_storage: false,
    allow_system_managed_barrier: false,
  },
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
};

interface CapturedBody {
  body: Record<string, unknown> | null;
}

/**
 * Installs the tenant REST routes and returns capture handles for the quota
 * POST and entitlement PATCH request bodies.
 */
async function installTenantRoutes(
  page: Page,
): Promise<{ quota: CapturedBody; entitlements: CapturedBody }> {
  const quota: CapturedBody = { body: null };
  const entitlements: CapturedBody = { body: null };

  const parseBody = (route: Route): Record<string, unknown> | null => {
    const data = route.request().postData();
    if (data === null) return null;
    try {
      return JSON.parse(data) as Record<string, unknown>;
    } catch {
      return null;
    }
  };

  await page.route("**/v1/tenants/list", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ tenants: [tenantRecord], total: 1 }),
    });
  });

  await page.route(`**/v1/tenants/${TENANT_ID}/quotas`, (route) => {
    quota.body = parseBody(route);
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(tenantRecord),
    });
  });

  await page.route(`**/v1/tenants/${TENANT_ID}/entitlements`, (route) => {
    entitlements.body = parseBody(route);
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(tenantRecord),
    });
  });

  // GET /v1/tenants/{id} (bare tenant) — must be registered AFTER the more
  // specific /quotas and /entitlements routes so those win.
  await page.route(`**/v1/tenants/${TENANT_ID}`, (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(tenantRecord),
    });
  });

  return { quota, entitlements };
}

/** Boots the app and navigates to the Tenants admin view. */
async function openTenants(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Tenants").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Tenants"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Opens the tenant detail (quota/entitlements editor) for the seeded tenant. */
async function openTenantDetail(page: Page): Promise<void> {
  await openTenants(page);
  await page.locator("tr").filter({ hasText: TENANT_ID }).first().click();
  await expect(
    page.locator("h2").filter({ hasText: `Tenant Quotas — ${TENANT_ID}` }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Multi-tenancy admin (web mode / REST payloads)", () => {
  test("(a) quota form POSTs the new quota fields in snake_case", async ({
    page,
  }) => {
    const captured = await installTenantRoutes(page);
    await injectWebRestMock(page, {
      sealStatus: { sealed: false, initialized: false, strategy: "none", hardware_backed: false },
      databases: [],
    });

    await openTenantDetail(page);
    const main = page.locator("main");

    await main.getByLabel("Max Storage (bytes)").fill("1048576");
    await main.getByLabel("Max Database Size (bytes)").fill("524288");
    await main.getByLabel("Max Replicas / Group").fill("3");
    await main.getByLabel("Max Total Replicas").fill("9");

    await main.getByRole("button", { name: "Save Quotas" }).click();

    await expect
      .poll(() => captured.quota.body, { timeout: LOAD_TIMEOUT })
      .not.toBeNull();

    const body = captured.quota.body as Record<string, unknown>;
    // Flat snake_case body (no wrapper, no tenant_id) per the REST contract.
    expect(body["quotas"]).toBeUndefined();
    expect(body["tenant_id"]).toBeUndefined();
    expect(body["max_storage_bytes"]).toBe(1048576);
    expect(body["max_database_size_bytes"]).toBe(524288);
    expect(body["max_replicas_per_group"]).toBe(3);
    expect(body["max_total_replicas"]).toBe(9);
  });

  test("(b) entitlements section PATCHes snake_case entitlement fields", async ({
    page,
  }) => {
    const captured = await installTenantRoutes(page);
    await injectWebRestMock(page, {
      sealStatus: { sealed: false, initialized: false, strategy: "none", hardware_backed: false },
      databases: [],
    });

    await openTenantDetail(page);
    const main = page.locator("main");

    // Toggle the dedicated-raft and volume-storage entitlements on, plus the
    // "raft" allowed-isolation mode.
    await main.getByTestId("entitlement-allow-dedicated-raft").check();
    await main.getByTestId("entitlement-allow-volume-storage").check();
    await main.getByTestId("allowed-mode-raft").check();

    await main.getByTestId("save-entitlements-btn").click();

    await expect
      .poll(() => captured.entitlements.body, { timeout: LOAD_TIMEOUT })
      .not.toBeNull();

    const body = captured.entitlements.body as Record<string, unknown>;
    expect(body["allow_dedicated_raft"]).toBe(true);
    expect(body["allow_volume_storage"]).toBe(true);
    expect(body["allowed_isolation_modes"]).toContain("raft");
    // camelCase originals must not appear on the wire.
    expect(body["allowDedicatedRaft"]).toBeUndefined();
  });

  test("(c) isolation selector offers all four modes", async ({ page }) => {
    await installTenantRoutes(page);
    await injectWebRestMock(page, {
      sealStatus: { sealed: false, initialized: false, strategy: "none", hardware_backed: false },
      databases: [],
    });

    await openTenantDetail(page);
    const select = page.locator("main").getByTestId("tenant-isolation-select");
    await expect(select).toBeVisible({ timeout: LOAD_TIMEOUT });

    const values = await select.locator("option").evaluateAll((opts) =>
      opts.map((o) => (o as HTMLOptionElement).value),
    );
    expect(values).toEqual(["prefix", "barrier", "volume", "raft"]);
  });
});
