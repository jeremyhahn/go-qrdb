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
 * Admin - Volume Pools (docs/volume-pools/api.md), mocked Wails tier.
 *
 * Drives each VolumePoolClient operation (list, get, create, update,
 * delete, listTenants) from the VolumePoolManager view and asserts both
 * the UI and the mock VolumePoolBinding's stored catalog; then the
 * TenantManager pool picker filled from volumePools.list(), and the
 * error paths (delete refused while tenants are assigned, list failure).
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

type StoredPool = {
  name: string;
  provider: string;
  description?: string;
  capacityBytes: number;
  overcommit: number;
  tenantCount: number;
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Loads the app and waits for the dashboard. */
async function openApp(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Clicks Volume Pools in the admin sidebar. */
async function gotoVolumePools(page: Page): Promise<void> {
  await page.locator("aside").getByText("Volume Pools").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Volume Pools"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Creates a pool directly through the mock binding. */
async function seedPool(
  page: Page,
  req: { name: string; description?: string; capacityBytes?: number; overcommit?: number },
): Promise<void> {
  await page.evaluate(async (r) => {
    const w = window as unknown as {
      go: { main: { VolumePoolBinding: { Create: (req: unknown) => Promise<unknown> } } };
    };
    await w.go.main.VolumePoolBinding.Create(r);
  }, req);
}

/** Assigns a tenant to a pool in the mock catalog. */
async function assignTenant(page: Page, pool: string, tenantId: string, reservedBytes: number): Promise<void> {
  await page.evaluate(
    ({ p, t, b }) => {
      const w = window as unknown as {
        __volumePoolAssign: (pool: string, tenant: string, bytes: number) => void;
      };
      w.__volumePoolAssign(p, t, b);
    },
    { p: pool, t: tenantId, b: reservedBytes },
  );
}

/** Reads the mock binding's stored catalog. */
async function storedPools(page: Page): Promise<StoredPool[]> {
  return page.evaluate(async () => {
    const w = window as unknown as {
      go: { main: { VolumePoolBinding: { List: () => Promise<unknown> } } };
    };
    return (await w.go.main.VolumePoolBinding.List()) as StoredPool[];
  });
}

/** Opens a pool's detail panel by clicking its row. */
async function openPool(page: Page, name: string): Promise<void> {
  const main = page.locator("main");
  await main.getByRole("cell", { name, exact: true }).click();
  await expect(main.getByTestId("volume-pool-detail-name")).toHaveText(name, { timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Volume Pools", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("list shows every pool in the catalog", async ({ page }) => {
    await openApp(page);
    await seedPool(page, { name: "fast", capacityBytes: 2 * 1024 ** 3, overcommit: 1.5 });
    await seedPool(page, { name: "bulk" });
    await assignTenant(page, "fast", "acme", 1024);
    await gotoVolumePools(page);

    const main = page.locator("main");
    const fastRow = main.locator("tr").filter({ has: page.getByRole("cell", { name: "fast", exact: true }) });
    await expect(fastRow).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(fastRow).toContainText("2.0 GiB");
    await expect(fastRow).toContainText("1.5x");
    const bulkRow = main.locator("tr").filter({ has: page.getByRole("cell", { name: "bulk", exact: true }) });
    await expect(bulkRow).toContainText("derived");

    const stored = await storedPools(page);
    expect(stored.map((p) => p.name)).toEqual(["bulk", "fast"]);
    expect(stored.find((p) => p.name === "fast")?.tenantCount).toBe(1);
  });

  test("create adds a pool from the form", async ({ page }) => {
    await openApp(page);
    await gotoVolumePools(page);
    const main = page.locator("main");

    await main.getByRole("button", { name: "Create Pool" }).click();
    const form = main.getByTestId("volume-pool-create-form");
    await expect(form).toBeVisible();
    await form.locator("#volume-pool-name").fill("archive");
    await form.locator("#volume-pool-description").fill("cold tier");
    await form.locator("#volume-pool-capacity").fill("1048576");
    await form.locator("#volume-pool-overcommit").fill("2");
    await main.getByTestId("volume-pool-create-submit").click();

    await expect(main.getByRole("cell", { name: "archive", exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(form).not.toBeVisible();

    const stored = await storedPools(page);
    expect(stored).toHaveLength(1);
    expect(stored[0]).toMatchObject({
      name: "archive",
      provider: "dir",
      description: "cold tier",
      capacityBytes: 1048576,
      overcommit: 2,
    });
  });

  test("get opens the detail panel with node reports", async ({ page }) => {
    await openApp(page);
    await seedPool(page, { name: "fast", capacityBytes: 4096, overcommit: 1.5, description: "nvme" });
    await gotoVolumePools(page);
    await openPool(page, "fast");

    const main = page.locator("main");
    await expect(main.getByTestId("volume-pool-capacity")).toHaveText("4.0 KiB");
    await expect(main.getByTestId("volume-pool-overcommit")).toHaveText("1.5x");
    await expect(main.getByTestId("volume-pool-description")).toHaveText("nvme");
    const nodes = main.getByTestId("volume-pool-nodes");
    await expect(nodes.getByRole("cell", { name: "node-1", exact: true })).toBeVisible();
    await expect(nodes.getByRole("cell", { name: "ready", exact: true })).toBeVisible();
  });

  test("listTenants shows the tenants assigned to the pool", async ({ page }) => {
    await openApp(page);
    await seedPool(page, { name: "fast" });
    await seedPool(page, { name: "bulk" });
    await assignTenant(page, "fast", "acme", 2048);
    await assignTenant(page, "fast", "globex", 1024);
    await gotoVolumePools(page);
    await openPool(page, "fast");

    const tenants = page.locator("main").getByTestId("volume-pool-tenants");
    await expect(tenants.getByRole("cell", { name: "acme", exact: true })).toBeVisible();
    await expect(tenants.getByRole("cell", { name: "globex", exact: true })).toBeVisible();
    await expect(tenants.getByRole("cell", { name: "2.0 KiB", exact: true })).toBeVisible();

    await page.locator("main").getByTestId("volume-pool-back").click();
    await openPool(page, "bulk");
    await expect(tenants).toContainText("No tenants are assigned to this pool.");
  });

  test("update changes description and overcommit", async ({ page }) => {
    await openApp(page);
    await seedPool(page, { name: "fast", capacityBytes: 4096 });
    await gotoVolumePools(page);
    await openPool(page, "fast");

    const main = page.locator("main");
    await main.locator("#volume-pool-edit-description").fill("hot tier");
    await main.locator("#volume-pool-edit-overcommit").fill("3");
    await main.getByTestId("volume-pool-save").click();

    await expect(main.getByTestId("volume-pool-overcommit")).toHaveText("3x", { timeout: LOAD_TIMEOUT });
    await expect(main.getByTestId("volume-pool-description")).toHaveText("hot tier");

    const stored = await storedPools(page);
    expect(stored[0]).toMatchObject({ name: "fast", description: "hot tier", overcommit: 3, capacityBytes: 4096 });
  });

  test("delete removes the pool after confirmation", async ({ page }) => {
    await openApp(page);
    await seedPool(page, { name: "fast" });
    await seedPool(page, { name: "bulk" });
    await gotoVolumePools(page);
    await openPool(page, "bulk");

    const main = page.locator("main");
    await main.getByTestId("volume-pool-delete").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText('Delete volume pool "bulk"');
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();

    await expect(main.getByTestId("volume-pool-detail")).not.toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByRole("cell", { name: "fast", exact: true })).toBeVisible();
    await expect(main.getByRole("cell", { name: "bulk", exact: true })).not.toBeVisible();
    expect((await storedPools(page)).map((p) => p.name)).toEqual(["fast"]);
  });

  test("delete is refused while tenants are assigned", async ({ page }) => {
    await openApp(page);
    await seedPool(page, { name: "fast" });
    await assignTenant(page, "fast", "acme", 1024);
    await gotoVolumePools(page);
    await openPool(page, "fast");

    const main = page.locator("main");
    await main.getByTestId("volume-pool-delete").click();
    await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();

    await expect(page.getByText(/has assigned tenants: acme/)).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByTestId("volume-pool-detail-name")).toHaveText("fast");
    expect((await storedPools(page)).map((p) => p.name)).toEqual(["fast"]);
  });

  test("a list failure surfaces as an error toast", async ({ page }) => {
    await openApp(page);
    await injectError(page, "VolumePoolBinding.List", "volume catalog unavailable");
    await gotoVolumePools(page);

    await expect(page.getByText("volume catalog unavailable")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator("main")).toContainText("No volume pools in the catalog.");
  });
});

test.describe("Admin - Tenant create - volume pool picker", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  /** Opens the tenant create form with volume isolation selected. */
  async function openVolumeTenantForm(page: Page): Promise<void> {
    await page.locator("aside").getByText("Tenants").click();
    await expect(
      page.locator(".breadcrumbs").getByText("Tenants"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    const main = page.locator("main");
    await main.getByRole("button", { name: "Create Tenant" }).click();
    await expect(
      page.locator("h2").filter({ hasText: "Create Tenant" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await main.locator("#tenant-isolation").selectOption("volume");
  }

  test("the pool field is a select of the catalog's pools", async ({ page }) => {
    await openApp(page);
    await seedPool(page, { name: "fast" });
    await seedPool(page, { name: "bulk" });
    await openVolumeTenantForm(page);

    const main = page.locator("main");
    const picker = main.getByTestId("create-tenant-volume-pool");
    await expect(picker).toBeVisible();
    await expect.poll(() => picker.evaluate((el) => el.tagName), { timeout: LOAD_TIMEOUT }).toBe("SELECT");
    await expect(picker.locator("option")).toHaveText(["Default pool", "bulk", "fast"]);

    await main.locator("#tenant-id").fill("acme-vol");
    await main.locator("#tenant-name").fill("Acme Volume");
    await main.getByTestId("create-tenant-admin-username").fill("acme-vol-admin");
    await main.getByTestId("create-tenant-admin-csr").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----",
    );
    await picker.selectOption("bulk");
    await main.getByTestId("create-tenant-submit").click();

    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (window as unknown as { __tenantCreateReq?: { volume_pool?: string } })
                .__tenantCreateReq?.volume_pool ?? null,
          ),
        { timeout: LOAD_TIMEOUT },
      )
      .toBe("bulk");
  });

  test("the pool field stays free text when the catalog is empty", async ({ page }) => {
    await openApp(page);
    await openVolumeTenantForm(page);

    const picker = page.locator("main").getByTestId("create-tenant-volume-pool");
    await expect(picker).toBeVisible();
    expect(await picker.evaluate((el) => el.tagName)).toBe("INPUT");
    await picker.fill("unlisted");
    await expect(picker).toHaveValue("unlisted");
  });
});
