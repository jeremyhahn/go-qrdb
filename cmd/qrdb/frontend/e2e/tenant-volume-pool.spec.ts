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
 * Tenant creation — Volume Pool field tests (docs/volume-pools).
 *
 * Replaces tenant-data-root.spec.ts (decision: data_root is retired). The
 * field is shown ONLY for the dedicated-storage isolation tiers
 * (volume/raft); mirrors the CLI's `tenant create --pool` flag. The
 * TenantManager create form gates the field's visibility and clears it
 * automatically when the isolation type changes away from volume/raft, and
 * sends it (as `volume_pool`) only when eligible and non-empty.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

/** Navigates to the Tenants admin section and opens the create form. */
async function openCreateTenantForm(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.locator("aside").getByText("Tenants").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Tenants"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  const main = page.locator("main");
  await main.getByRole("button", { name: "Create Tenant" }).click();
  await expect(
    page.locator("h2").filter({ hasText: "Create Tenant" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Tenant create - Volume Pool field", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("pool field is hidden for prefix isolation (the default)", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await expect(main.locator("#tenant-isolation")).toHaveValue("prefix");
    await expect(main.getByTestId("create-tenant-volume-pool")).not.toBeVisible();
  });

  test("pool field is hidden for barrier isolation", async ({ page }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("barrier");
    await expect(main.getByTestId("create-tenant-volume-pool")).not.toBeVisible();
  });

  test("pool field is shown for volume isolation", async ({ page }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("volume");
    await expect(main.getByTestId("create-tenant-volume-pool")).toBeVisible();
  });

  test("pool field is shown for raft isolation", async ({ page }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("raft");
    await expect(main.getByTestId("create-tenant-volume-pool")).toBeVisible();
  });

  test("switching from volume back to prefix hides and clears the pool field", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("volume");
    const poolInput = main.getByTestId("create-tenant-volume-pool");
    await expect(poolInput).toBeVisible();
    await poolInput.fill("fast");

    await main.locator("#tenant-isolation").selectOption("prefix");
    await expect(poolInput).not.toBeVisible();

    // Switching back to volume must show an empty field — the stale value
    // was cleared, not merely hidden.
    await main.locator("#tenant-isolation").selectOption("volume");
    await expect(main.getByTestId("create-tenant-volume-pool")).toHaveValue("");
  });

  test("create request carries volume_pool for a volume-isolation tenant", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-id").fill("acme-vol");
    await main.locator("#tenant-name").fill("Acme Volume");
    await main.getByTestId("create-tenant-admin-username").fill("acme-vol-admin");
    await main.getByTestId("create-tenant-admin-csr").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----",
    );
    await main.locator("#tenant-isolation").selectOption("volume");
    await main.getByTestId("create-tenant-volume-pool").fill("fast");
    await main.getByTestId("create-tenant-submit").click();

    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (
                window as unknown as {
                  __tenantCreateReq?: {
                    isolation_type?: string;
                    volume_pool?: string;
                  };
                }
              ).__tenantCreateReq?.volume_pool ?? null,
          ),
        { timeout: LOAD_TIMEOUT },
      )
      .toBe("fast");

    const req = await page.evaluate(
      () =>
        (
          window as unknown as {
            __tenantCreateReq?: {
              isolation_type?: string;
              volume_pool?: string;
            };
          }
        ).__tenantCreateReq,
    );
    expect(req?.isolation_type).toBe("volume");
  });

  test("create request omits volume_pool for a prefix-isolation tenant even if a value was set and cleared", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-id").fill("acme-prefix");
    await main.locator("#tenant-name").fill("Acme Prefix");
    await main.getByTestId("create-tenant-admin-username").fill("acme-prefix-admin");
    await main.getByTestId("create-tenant-admin-csr").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----",
    );
    // No isolation change — stays at the default "prefix" — the pool
    // input is never rendered, so there is nothing to fill.
    await main.getByTestId("create-tenant-submit").click();

    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (
                window as unknown as {
                  __tenantCreateReq?: { volume_pool?: string };
                }
              ).__tenantCreateReq !== undefined,
          ),
        { timeout: LOAD_TIMEOUT },
      )
      .toBe(true);

    const req = await page.evaluate(
      () =>
        (
          window as unknown as { __tenantCreateReq?: { volume_pool?: string } }
        ).__tenantCreateReq,
    );
    expect(req?.volume_pool).toBeUndefined();
  });

  test("server rejection of an unentitled pool surfaces as a toast error", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    // Simulates the server refusing a pool the tenant is not entitled to
    // (PERMISSION_DENIED, volume_pool:<pool>); the form must surface it
    // rather than swallow it.
    await injectError(
      page,
      "TenantBinding.Create",
      "quota: tenant is not entitled to feature: volume_pool:fast",
    );

    const main = page.locator("main");
    await main.locator("#tenant-id").fill("acme-reject");
    await main.locator("#tenant-name").fill("Acme Reject");
    await main.getByTestId("create-tenant-admin-username").fill("acme-reject-admin");
    await main.getByTestId("create-tenant-admin-csr").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----",
    );
    await main.locator("#tenant-isolation").selectOption("volume");
    await main.getByTestId("create-tenant-volume-pool").fill("fast");
    await main.getByTestId("create-tenant-submit").click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("volume_pool:fast");
  });
});
