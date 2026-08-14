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
 * Per-tenant Shamir unseal-quorum tests (desktop bindings).
 *
 * These tests exercise the TenantManager per-tenant unseal flow against the
 * mock Wails TenantBinding.BarrierStatus / BarrierUnsealShare bindings,
 * covering:
 *   1. Opening the unseal form fetches the barrier status and renders the
 *      Shamir quorum requirement (threshold / total).
 *   2. Submitting a share below the threshold reports quorum progress and
 *      keeps the form open.
 *   3. Reaching the threshold unseals the barrier, shows the success toast,
 *      and returns to the tenant list.
 *   4. A failing BarrierUnsealShare binding surfaces an error toast.
 *
 * The state extends secondLaunchEmbeddedState with a sealed Shamir barrier
 * (threshold 2 of 3) on the pre-seeded "default" tenant.
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

/** Mock state with a sealed 2-of-3 Shamir barrier on the default tenant. */
const sealedTenantBarrierState: MockState = {
  ...secondLaunchEmbeddedState,
  tenantBarriers: {
    default: {
      sealed: true,
      initialized: true,
      strategy: "shamir",
      shamir_threshold: 2,
      shamir_total: 3,
    },
  },
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the Tenants admin section. */
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

/** Opens the per-tenant unseal form for the default tenant. */
async function openUnsealForm(page: Page): Promise<void> {
  await navigateToTenants(page);

  const main = page.locator("main");
  await main.getByRole("button", { name: "Unseal" }).first().click();
  await expect(
    page.locator("h2").filter({ hasText: "Unseal Barrier — default" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Fills the share input and submits it toward the quorum. */
async function submitShare(page: Page, share: string): Promise<void> {
  const main = page.locator("main");
  await main.getByTestId("tenant-unseal-share").fill(share);
  await main.getByRole("button", { name: "Submit Share" }).click();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Tenant barrier unseal quorum", () => {
  test.use({ mockState: sealedTenantBarrierState });

  test("unseal form shows Shamir quorum requirement from barrier status", async ({
    page,
  }) => {
    await openUnsealForm(page);

    const main = page.locator("main");

    // BarrierStatus drives the requirement copy: threshold 2 of total 3.
    await expect(
      main.getByText("2 of 3 shares are required"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Quorum counter starts at 0 of the required 2.
    await expect(main.getByText("0 / 2")).toBeVisible();
  });

  test("share below threshold reports progress and keeps the form open", async ({
    page,
  }) => {
    await openUnsealForm(page);

    await submitShare(page, "share-one");

    // The accepted-share toast reports quorum progress 1/2.
    await expect(
      page.getByText("Share accepted (1/2)"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The form stays open with the updated counter.
    const main = page.locator("main");
    await expect(
      page.locator("h2").filter({ hasText: "Unseal Barrier — default" }),
    ).toBeVisible();
    await expect(main.getByText("1 / 2")).toBeVisible();
  });

  test("reaching the threshold unseals the barrier and returns to the list", async ({
    page,
  }) => {
    await openUnsealForm(page);

    await submitShare(page, "share-one");
    await expect(
      page.getByText("Share accepted (1/2)"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await submitShare(page, "share-two");

    // Quorum reached: success toast + back to the tenant list view.
    await expect(
      page.getByText('Tenant "default" barrier unsealed.'),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      page.locator("main").getByRole("button", { name: "Unseal" }).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("failing unseal-share binding surfaces an error toast", async ({
    page,
  }) => {
    await openUnsealForm(page);

    await injectError(
      page,
      "TenantBinding.BarrierUnsealShare",
      "invalid share",
    );
    await submitShare(page, "bogus-share");

    await expect(page.getByText("invalid share")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await clearError(page, "TenantBinding.BarrierUnsealShare");
  });
});
