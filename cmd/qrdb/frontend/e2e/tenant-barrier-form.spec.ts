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
 * TenantManager barrier-form guards.
 *
 * Regression guards for the per-tenant barrier UI:
 *   - Create Tenant defaults the isolation type to "prefix" (works without a
 *     configured volume pool; barrier isolation requires storage.volume_pools).
 *   - The Initialize Barrier form offers a strategy selector (none/passphrase/
 *     shamir) per the platform architecture, defaulting to passphrase. The
 *     passphrase strategy collects + confirms a passphrase (the form previously
 *     sent only threshold/shares with no passphrase, which the server rejected);
 *     the shamir strategy collects M-of-N shares.
 */

import { test, expect, secondLaunchEmbeddedState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

async function navigateToTenants(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Tenants").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Tenants"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Tenant barrier form", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Create Tenant defaults isolation type to prefix", async ({ page }) => {
    await navigateToTenants(page);
    const main = page.locator("main");

    await main.getByRole("button", { name: "Create Tenant" }).click();
    await expect(page.locator("h2").filter({ hasText: "Create Tenant" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // The isolation <select> must default to the pool-free "prefix" strategy.
    await expect(main.locator("select")).toHaveValue("prefix");
  });

  test("Initialize Barrier form offers a strategy selector; fields adapt per strategy", async ({
    page,
  }) => {
    await navigateToTenants(page);
    const main = page.locator("main");

    // The seeded tenant exposes an "Init Barrier" action (the tenant id is
    // rendered as a separate label next to the per-tenant button row).
    await main.getByRole("button", { name: "Init Barrier" }).first().click();
    await expect(
      page.locator("h2").filter({ hasText: /Initialize Barrier/ }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // All three strategy choices are offered (none / passphrase / shamir).
    await expect(main.getByTestId("barrier-strategy-none")).toBeVisible();
    await expect(main.getByTestId("barrier-strategy-passphrase")).toBeVisible();
    await expect(main.getByTestId("barrier-strategy-shamir")).toBeVisible();

    // Default is passphrase: passphrase fields shown, shamir inputs hidden.
    await expect(main.locator("#barrier-passphrase")).toBeVisible();
    await expect(main.locator("#barrier-passphrase-confirm")).toBeVisible();
    await expect(main.locator("#barrier-threshold")).toHaveCount(0);
    await expect(main.locator("#barrier-shares")).toHaveCount(0);

    // Submit gated until a valid (>=8 char, matching) passphrase is entered.
    const submit = main.getByRole("button", { name: "Initialize Barrier" });
    await expect(submit).toBeDisabled();
    await main.locator("#barrier-passphrase").fill("tenant-secret-1");
    await main.locator("#barrier-passphrase-confirm").fill("tenant-secret-1");
    await expect(submit).toBeEnabled();

    // Switch to Shamir: M/N inputs appear, passphrase inputs disappear.
    await main.getByTestId("barrier-strategy-shamir").click();
    await expect(main.locator("#barrier-threshold")).toBeVisible();
    await expect(main.locator("#barrier-shares")).toBeVisible();
    await expect(main.locator("#barrier-passphrase")).toHaveCount(0);
    await expect(submit).toBeEnabled();

    // Switch to None: no credential inputs; submit is allowed.
    await main.getByTestId("barrier-strategy-none").click();
    await expect(main.locator("#barrier-passphrase")).toHaveCount(0);
    await expect(main.locator("#barrier-threshold")).toHaveCount(0);
    await expect(submit).toBeEnabled();
  });
});
