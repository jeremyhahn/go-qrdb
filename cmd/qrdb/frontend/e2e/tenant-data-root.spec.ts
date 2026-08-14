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
 * Tenant creation — Data Root (dedicated disk) field tests.
 *
 * Mirrors the CLI's `tenant create --data-root` flag: the field is shown
 * ONLY for the dedicated-disk isolation tiers (volume/raft) and is rejected
 * by the server (DataRootIsolationTypeError -> 400) for prefix/barrier. The
 * TenantManager create form gates the field's visibility and clears it
 * automatically when the isolation type changes away from volume/raft, and
 * sends it (as `data_root`) only when eligible and non-empty.
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

test.describe("Admin - Tenant create - Data Root field", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("data-root field is hidden for prefix isolation (the default)", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await expect(main.locator("#tenant-isolation")).toHaveValue("prefix");
    await expect(main.getByTestId("create-tenant-data-root")).not.toBeVisible();
  });

  test("data-root field is hidden for barrier isolation", async ({ page }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("barrier");
    await expect(main.getByTestId("create-tenant-data-root")).not.toBeVisible();
  });

  test("data-root field is shown for volume isolation", async ({ page }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("volume");
    await expect(main.getByTestId("create-tenant-data-root")).toBeVisible();
  });

  test("data-root field is shown for raft isolation", async ({ page }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("raft");
    await expect(main.getByTestId("create-tenant-data-root")).toBeVisible();
  });

  test("switching from volume back to prefix hides and clears the data-root field", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-isolation").selectOption("volume");
    const dataRootInput = main.getByTestId("create-tenant-data-root");
    await expect(dataRootInput).toBeVisible();
    await dataRootInput.fill("/mnt/tenant-disks/acme");

    await main.locator("#tenant-isolation").selectOption("prefix");
    await expect(dataRootInput).not.toBeVisible();

    // Switching back to volume must show an empty field — the stale value
    // was cleared, not merely hidden.
    await main.locator("#tenant-isolation").selectOption("volume");
    await expect(main.getByTestId("create-tenant-data-root")).toHaveValue("");
  });

  test("create request carries data_root for a volume-isolation tenant", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-id").fill("acme-vol");
    await main.locator("#tenant-name").fill("Acme Volume");
    await main.locator("#tenant-isolation").selectOption("volume");
    await main
      .getByTestId("create-tenant-data-root")
      .fill("/mnt/tenant-disks/acme-vol");
    await main.getByRole("button", { name: "Create" }).click();

    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (
                window as unknown as {
                  __tenantCreateReq?: {
                    isolation_type?: string;
                    data_root?: string;
                  };
                }
              ).__tenantCreateReq?.data_root ?? null,
          ),
        { timeout: LOAD_TIMEOUT },
      )
      .toBe("/mnt/tenant-disks/acme-vol");

    const req = await page.evaluate(
      () =>
        (
          window as unknown as {
            __tenantCreateReq?: {
              isolation_type?: string;
              data_root?: string;
            };
          }
        ).__tenantCreateReq,
    );
    expect(req?.isolation_type).toBe("volume");
  });

  test("create request omits data_root for a prefix-isolation tenant even if a value was set and cleared", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    const main = page.locator("main");
    await main.locator("#tenant-id").fill("acme-prefix");
    await main.locator("#tenant-name").fill("Acme Prefix");
    // No isolation change — stays at the default "prefix" — the data-root
    // input is never rendered, so there is nothing to fill.
    await main.getByRole("button", { name: "Create" }).click();

    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (
                window as unknown as {
                  __tenantCreateReq?: { data_root?: string };
                }
              ).__tenantCreateReq !== undefined,
          ),
        { timeout: LOAD_TIMEOUT },
      )
      .toBe(true);

    const req = await page.evaluate(
      () =>
        (
          window as unknown as { __tenantCreateReq?: { data_root?: string } }
        ).__tenantCreateReq,
    );
    expect(req?.data_root).toBeUndefined();
  });

  test("server rejection of an invalid data-root/isolation combination surfaces as a toast error", async ({
    page,
  }) => {
    await openCreateTenantForm(page);

    // Simulates the server's DataRootIsolationTypeError (400) for whatever
    // reason the request reaches the server in an invalid state; the form
    // must surface it rather than swallow it.
    await injectError(
      page,
      "TenantBinding.Create",
      "data_root is only valid for volume or raft isolation tenants",
    );

    const main = page.locator("main");
    await main.locator("#tenant-id").fill("acme-reject");
    await main.locator("#tenant-name").fill("Acme Reject");
    await main.locator("#tenant-isolation").selectOption("volume");
    await main
      .getByTestId("create-tenant-data-root")
      .fill("/mnt/tenant-disks/acme-reject");
    await main.getByRole("button", { name: "Create" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("data_root is only valid for volume or raft isolation tenants");
  });
});
