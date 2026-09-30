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
 * Per-tenant barrier unseal tests (desktop bindings).
 *
 * The TenantManager unseal panel reads the barrier status
 * (TenantBinding.BarrierStatus) and then:
 *   - for a passphrase barrier, unseals with the passphrase
 *     (TenantBinding.BarrierUnseal, the passphrase base64 in `key`);
 *   - for a Shamir barrier, shows which command its holders run: a quorum of
 *     them unseals it from the CLI or the desktop app, and the page takes no
 *     share.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { MockState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations and view transitions. */
const LOAD_TIMEOUT = 10_000;

function sealedTenant(strategy: "passphrase" | "shamir"): MockState {
  return {
    ...secondLaunchEmbeddedState,
    tenantBarriers: {
      default: strategy === "shamir"
        ? { sealed: true, initialized: true, strategy, shamir_threshold: 2, shamir_total: 3 }
        : { sealed: true, initialized: true, strategy, passphrase: "tenant-pass" },
    },
  };
}

/** Opens the per-tenant unseal panel for the default tenant. */
async function openUnsealPanel(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Tenants").click();
  await expect(page.locator(".breadcrumbs").getByText("Tenants")).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.locator("main").getByRole("button", { name: "Unseal" }).first().click();
  await expect(page.locator("h2").filter({ hasText: "Unseal Barrier — default" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Tenant barrier unseal (Shamir)", () => {
  test.use({ mockState: sealedTenant("shamir") });

  test("points to the holder quorum and takes no share", async ({ page }) => {
    await openUnsealPanel(page);
    const main = page.locator("main");

    const note = main.getByTestId("tenant-unseal-quorum");
    await expect(note).toContainText("2 of 3 must", { timeout: LOAD_TIMEOUT });
    await expect(note).toContainText("qrdb tenant barrier-quorum submit --op unseal");
    await expect(main.getByTestId("tenant-unseal-passphrase")).toHaveCount(0);
    await expect(main.getByRole("button", { name: "Submit Share" })).toHaveCount(0);
  });
});

test.describe("Admin - Tenant barrier unseal (passphrase)", () => {
  test.use({ mockState: sealedTenant("passphrase") });

  test("the passphrase unseals the barrier and returns to the list", async ({ page }) => {
    await openUnsealPanel(page);
    const main = page.locator("main");

    await main.getByTestId("tenant-unseal-passphrase").fill("tenant-pass");
    await main.getByRole("button", { name: "Unseal", exact: true }).click();

    await expect(page.locator('[role="alert"]').filter({ hasText: 'Tenant "default" barrier unsealed' })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator("h2").filter({ hasText: "Unseal Barrier — default" })).toHaveCount(0);
  });

  test("a wrong passphrase keeps the panel open with an error", async ({ page }) => {
    await openUnsealPanel(page);
    const main = page.locator("main");

    await main.getByTestId("tenant-unseal-passphrase").fill("wrong");
    await main.getByRole("button", { name: "Unseal", exact: true }).click();

    await expect(page.locator('[role="alert"]').filter({ hasText: "invalid credentials" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator("h2").filter({ hasText: "Unseal Barrier — default" })).toBeVisible();
  });

  test("a failing unseal binding surfaces an error toast", async ({ page }) => {
    await openUnsealPanel(page);
    await injectError(page, "TenantBinding.BarrierUnseal", "tenant barrier unavailable");

    const main = page.locator("main");
    await main.getByTestId("tenant-unseal-passphrase").fill("tenant-pass");
    await main.getByRole("button", { name: "Unseal", exact: true }).click();

    await expect(page.locator('[role="alert"]').filter({ hasText: "tenant barrier unavailable" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await clearError(page, "TenantBinding.BarrierUnseal");
  });
});
