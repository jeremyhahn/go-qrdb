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
 * Web-mode (RestClient) mocked-tier coverage for the TenantManager per-tenant
 * "Enable Encryption" migration wizard.
 *
 * This mirrors the system-scope wizard test (web-mode-enable-encryption.spec.ts)
 * but drives a single tenant whose DEDICATED barrier was committed with the
 * "none" strategy. The wizard POSTs /v1/barrier/enable-encryption carrying that
 * tenant's tenant_id, then polls GET /v1/tenants/{id}/barrier/status until the
 * reported strategy changes from "none" to the keyed strategy — mirroring the
 * asynchronous, 202-style migration contract.
 */

import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

const TENANT_ID = "acme";

/** A snake_case tenant record (dedicated barrier isolation) as returned by REST. */
const tenantRecord = {
  id: TENANT_ID,
  name: "Acme Corp",
  isolation_type: "barrier",
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
};

/** Installs the tenant list route. */
async function installTenantList(page: Page): Promise<void> {
  await page.route("**/v1/tenants/list", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ tenants: [tenantRecord], total: 1 }),
    });
  });
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

test.describe("Web mode - tenant enable encryption wizard", () => {
  test("migrates a tenant 'none' barrier to passphrase and shows success", async ({
    page,
  }) => {
    await installTenantList(page);

    // Staged tenant barrier status: returns "none" until the migration POST
    // flips it to "passphrase", emulating the asynchronous server-side migration.
    let strategy = "none";
    await page.route(`**/v1/tenants/${TENANT_ID}/barrier/status`, (route) => {
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          tenant_id: TENANT_ID,
          sealed: false,
          initialized: true,
          strategy,
          hardware_backed: false,
        }),
      });
    });

    let enableCalled = false;
    let enableBody: Record<string, unknown> | null = null;
    await page.route("**/v1/barrier/enable-encryption", (route) => {
      enableCalled = true;
      enableBody = route.request().postDataJSON() as Record<string, unknown>;
      // Migration completes asynchronously; the next status poll sees the key.
      strategy = "passphrase";
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ status: "migration_started", scope: "tenant" }),
      });
    });

    await openTenants(page);

    // The per-tenant Enable Encryption trigger lives in the Barrier Management
    // card and is suffixed with the tenant id.
    await page.getByTestId(`tenant-enable-encryption-btn-${TENANT_ID}`).click();

    // The wizard opens for this tenant.
    await expect(page.getByTestId("tenant-enable-encryption-wizard")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Choose the passphrase strategy and supply a passphrase.
    await page
      .getByTestId("tenant-enable-encryption-strategy")
      .selectOption("passphrase");
    await page
      .getByTestId("tenant-enable-encryption-passphrase")
      .fill("hunter2hunter2");
    await page.getByTestId("tenant-enable-encryption-submit").click();

    // The UI transitions to the in-progress state...
    await expect(page.getByTestId("tenant-enable-encryption-progress")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // ...then the poll observes the keyed strategy and the success state shows.
    await expect(page.getByTestId("tenant-enable-encryption-success")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByTestId("tenant-enable-encryption-success")).toContainText(
      "passphrase",
    );

    // The request targeted THIS tenant (tenant_id set) and carried the
    // passphrase + strategy under the snake_case wire contract.
    expect(enableCalled).toBe(true);
    expect(enableBody).not.toBeNull();
    const body = enableBody as unknown as Record<string, unknown>;
    expect(body.tenant_id).toBe(TENANT_ID);
    expect(body.strategy).toBe("passphrase");
    expect(body.passphrase).toBe("hunter2hunter2");
  });

  test("surfaces a 409 migration-in-progress rejection as an error toast", async ({
    page,
  }) => {
    await installTenantList(page);
    await page.route(`**/v1/tenants/${TENANT_ID}/barrier/status`, (route) => {
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          tenant_id: TENANT_ID,
          sealed: false,
          initialized: true,
          strategy: "none",
          hardware_backed: false,
        }),
      });
    });
    await page.route("**/v1/barrier/enable-encryption", (route) => {
      void route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({ error: "a migration is already in progress" }),
      });
    });

    await openTenants(page);

    await page.getByTestId(`tenant-enable-encryption-btn-${TENANT_ID}`).click();
    await expect(page.getByTestId("tenant-enable-encryption-wizard")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await page
      .getByTestId("tenant-enable-encryption-strategy")
      .selectOption("passphrase");
    await page
      .getByTestId("tenant-enable-encryption-passphrase")
      .fill("hunter2hunter2");
    await page.getByTestId("tenant-enable-encryption-submit").click();

    // The 409 surfaces as an error toast carrying the status and server message.
    const alert = page.locator('[role="alert"]').filter({ hasText: "409" });
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("already in progress");

    // The wizard must NOT advance to the in-progress or success state.
    await expect(page.getByTestId("tenant-enable-encryption-progress")).toHaveCount(0);
    await expect(page.getByTestId("tenant-enable-encryption-success")).toHaveCount(0);
  });
});
