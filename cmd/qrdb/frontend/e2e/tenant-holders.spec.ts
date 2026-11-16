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
 * Tenant share-holder panel tests (desktop bindings).
 *
 * The TenantManager's Holders action lists a tenant barrier's share holders
 * (OfficerBinding.ListHolders with a tenant scope) and opens rekey and
 * revocation rounds on it (OfficerBinding.StartRekey / RevokeHolder). A quorum
 * of the current holders completes a round from the CLI or the desktop app.
 */

import { test, expect, secondLaunchEmbeddedState, injectError, clearError } from "./fixtures/wails-mock.ts";
import type { MockState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;
const CSR = "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----";

const holdersState: MockState = {
  ...secondLaunchEmbeddedState,
  tenantBarriers: {
    default: { sealed: false, initialized: true, strategy: "shamir", shamir_threshold: 2, shamir_total: 2 },
  },
  tenantOfficers: {
    default: [
      {
        username: "tina",
        role: "so",
        cert_thumbprint: "0a0b",
        share_index: 1,
        designated: "mtls",
        factors: ["mtls"],
        encryption_kem: "dhkem-p256",
        enrolled_at: "2026-01-01T00:00:00Z",
        claimed_at: "2026-01-02T00:00:00Z",
        share_pending: false,
      },
      {
        username: "tom",
        role: "custodian",
        cert_thumbprint: "0c0d",
        share_index: 2,
        designated: "mtls",
        factors: ["mtls"],
        encryption_kem: "dhkem-p256",
        enrolled_at: "2026-01-01T00:00:00Z",
        claimed_at: "0001-01-01T00:00:00Z",
        share_pending: true,
      },
    ],
  },
};

async function openHolders(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Tenants").click();
  await expect(page.locator(".breadcrumbs").getByText("Tenants")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByTestId("tenant-holders-btn-default").click();
  await expect(page.getByTestId("tenant-holders-panel")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Tenant share holders", () => {
  test.use({ mockState: holdersState });

  test("lists the tenant barrier's holders with role and claim state", async ({ page }) => {
    await openHolders(page);
    const table = page.getByTestId("tenant-holders-table");
    await expect(table.locator("tr").filter({ hasText: "tina" })).toContainText("Claimed", { timeout: LOAD_TIMEOUT });
    await expect(table.locator("tr").filter({ hasText: "tom" })).toContainText("custodian");
    await expect(table.locator("tr").filter({ hasText: "tom" })).toContainText("Pending");
  });

  test("the rekey form has no domain or SO PIN for a tenant", async ({ page }) => {
    await openHolders(page);
    await expect(page.getByTestId("rekey-domain")).toHaveCount(0);
    await expect(page.getByTestId("rekey-so-pin")).toHaveCount(0);
    await expect(page.getByTestId("rekey-panel")).toContainText("qrdb tenant barrier-quorum submit --op rekey");
  });

  test("starting a rekey shows the round it opened", async ({ page }) => {
    await openHolders(page);
    await page.getByTestId("rekey-threshold").fill("2");
    await page.getByTestId("start-rekey-btn").click();
    await expect(page.getByTestId("open-round")).toContainText("rekey round on the unseal domain", { timeout: LOAD_TIMEOUT });
  });

  test("revoking a holder opens a round that removes it", async ({ page }) => {
    await openHolders(page);
    await page.getByTestId("revoke-username").fill("tom");
    await page.getByTestId("revoke-holder-btn").click();
    await expect(page.getByTestId("open-round")).toContainText("Removes tom", { timeout: LOAD_TIMEOUT });
  });

  test("a failing rekey surfaces an error toast and opens no round", async ({ page }) => {
    await openHolders(page);
    await injectError(page, "OfficerBinding.StartRekey", "rekey would leave fewer holders than the threshold");
    await page.getByTestId("start-rekey-btn").click();
    await expect(page.locator('[role="alert"]').filter({ hasText: "fewer holders than the threshold" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("open-round")).toHaveCount(0);
    await clearError(page, "OfficerBinding.StartRekey");
  });

  test("enrolling a holder registers it and lists it in the holder table", async ({ page }) => {
    await openHolders(page);
    await page.getByTestId("tenant-holder-enrol-toggle").click();
    await page.getByTestId("officer-username-0").fill("tara");
    await page.getByTestId("officer-role-0").selectOption("custodian");
    await page.getByTestId("officer-csr-0").fill(CSR);
    await page.getByTestId("register-officer-btn-0").click();
    await expect(page.getByTestId("officer-cert-0")).toBeVisible({ timeout: LOAD_TIMEOUT });
    const row = page.getByTestId("tenant-holders-table").locator("tr").filter({ hasText: "tara" });
    await expect(row).toContainText("custodian", { timeout: LOAD_TIMEOUT });
  });

  test("a failing enrolment shows the error and lists no new holder", async ({ page }) => {
    await openHolders(page);
    await injectError(page, "OfficerBinding.RegisterHolder", "ceremony: certificate request signature is invalid");
    await page.getByTestId("tenant-holder-enrol-toggle").click();
    await page.getByTestId("officer-username-0").fill("tara");
    await page.getByTestId("officer-csr-0").fill(CSR);
    await page.getByTestId("register-officer-btn-0").click();
    await expect(page.getByTestId("officer-error-0")).toContainText("signature is invalid", { timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("tenant-holders-table").locator("tr").filter({ hasText: "tara" })).toHaveCount(0);
    await clearError(page, "OfficerBinding.RegisterHolder");
  });
});
