/**
 * Copyright 2026 Jeremy Hahn
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * Tenant trust anchors: the per-tenant CAs that vouch for the tenant's users'
 * bring-your-own login certificates (TenantBinding.AddTrustAnchors,
 * ListTrustAnchors and RemoveTrustAnchor), managed from the tenant detail view.
 */

import { test, expect, secondLaunchEmbeddedState, injectError, clearError } from "./fixtures/wails-mock.ts";
import type { MockState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

// A fixed CA certificate PEM. The mock does not parse it: it derives the
// fingerprint (mock-fp-<n>) and subject (CN=Mock CA) itself.
const CA_PEM = [
  "-----BEGIN CERTIFICATE-----",
  "MIIBfTCCASOgAwIBAgIUQ1JkTW9ja0NBMB4XDTI2MDEwMTAwMDAwMFowETEPMA0G",
  "A1UEAwwGTW9jayBDQTBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABMockCAKey==",
  "-----END CERTIFICATE-----",
].join("\n");

const anchorsState: MockState = {
  ...secondLaunchEmbeddedState,
  trustAnchors: {},
};

async function openTenantDetail(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Tenants").click();
  await expect(page.locator(".breadcrumbs").getByText("Tenants")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("tbody tr").filter({ hasText: "default" }).first().locator("td").nth(1).click();
  await expect(page.getByRole("heading", { name: "Trust Anchors" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Tenant trust anchors", () => {
  test.use({ mockState: anchorsState });

  test("a tenant with no anchors says so", async ({ page }) => {
    await openTenantDetail(page);
    await expect(page.getByText("No trust anchors registered.")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("add-trust-anchors-btn")).toBeDisabled();
  });

  test("adding a CA PEM lists the anchor, and removing it withdraws it", async ({ page }) => {
    await openTenantDetail(page);
    await page.getByTestId("add-trust-anchors-input").fill(CA_PEM);
    await page.getByTestId("add-trust-anchors-btn").click();

    const row = page.getByTestId("trust-anchors-table").locator("tbody tr").filter({ hasText: "mock-fp-1" });
    await expect(row).toContainText("CN=Mock CA", { timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("add-trust-anchors-input")).toHaveValue("");

    await row.getByTitle("Withdraw this trust anchor").click();
    await expect(page.getByText("No trust anchors registered.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("the list survives leaving and reopening the tenant", async ({ page }) => {
    await openTenantDetail(page);
    await page.getByTestId("add-trust-anchors-input").fill(CA_PEM);
    await page.getByTestId("add-trust-anchors-btn").click();
    await expect(page.getByTestId("trust-anchors-table")).toContainText("mock-fp-1", { timeout: LOAD_TIMEOUT });

    // Back to the tenant list and into the tenant again: the detail view
    // reloads the anchors through ListTrustAnchors.
    await page.locator("main").getByRole("button", { name: "Back", exact: true }).first().click();
    await expect(page.getByRole("heading", { name: "Trust Anchors" })).toHaveCount(0, { timeout: LOAD_TIMEOUT });
    await page.locator("tbody tr").filter({ hasText: "default" }).first().locator("td").nth(1).click();
    await expect(page.getByRole("heading", { name: "Trust Anchors" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("trust-anchors-table")).toContainText("mock-fp-1", { timeout: LOAD_TIMEOUT });
  });

  test("a failing add surfaces the error and lists no anchor", async ({ page }) => {
    await openTenantDetail(page);
    await injectError(page, "TenantBinding.AddTrustAnchors", "trust anchor is not a CA certificate");
    await page.getByTestId("add-trust-anchors-input").fill(CA_PEM);
    await page.getByTestId("add-trust-anchors-btn").click();
    await expect(page.locator('[role="alert"]').filter({ hasText: "not a CA certificate" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("trust-anchors-table")).toHaveCount(0);
    await expect(page.getByTestId("add-trust-anchors-input")).toHaveValue(CA_PEM);
    await clearError(page, "TenantBinding.AddTrustAnchors");
  });

  test("a failing remove keeps the anchor listed", async ({ page }) => {
    await openTenantDetail(page);
    await page.getByTestId("add-trust-anchors-input").fill(CA_PEM);
    await page.getByTestId("add-trust-anchors-btn").click();
    const row = page.getByTestId("trust-anchors-table").locator("tbody tr").filter({ hasText: "mock-fp-1" });
    await expect(row).toBeVisible({ timeout: LOAD_TIMEOUT });

    await injectError(page, "TenantBinding.RemoveTrustAnchor", "trust anchor is in use");
    await row.getByTitle("Withdraw this trust anchor").click();
    await expect(page.locator('[role="alert"]').filter({ hasText: "trust anchor is in use" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(row).toBeVisible();
    await clearError(page, "TenantBinding.RemoveTrustAnchor");
  });
});
