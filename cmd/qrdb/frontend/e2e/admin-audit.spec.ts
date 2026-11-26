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
 * Admin Audit Log tests, exercising the real compliance-grade audit query
 * API (client.audit.query -> AuditBinding.Query) rather than the retired
 * KV-prefix-scan placeholder. The mock AuditBinding.Query returns two
 * fixed events (actor "admin"/resource "barrier" and actor "alice"/tenant
 * "acme") and supports server-side filtering by actor/resource/action/
 * outcome/tenant, mirroring the real REST handler's query parameters.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

/** Navigates to the Audit Log admin section. */
async function navigateToAuditLog(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Audit Log", { exact: true }).click();
  await expect(page.locator(".breadcrumbs").getByText("Audit Log")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Audit Log", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("shows audit events from the query API on load", async ({ page }) => {
    await navigateToAuditLog(page);

    const main = page.locator("main");
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByText("alice").first()).toBeVisible();
    await expect(main.getByText("unseal")).toBeVisible();
    await expect(main.getByText("barrier")).toBeVisible();
  });

  test("filter by actor narrows results", async ({ page }) => {
    await navigateToAuditLog(page);

    const main = page.locator("main");
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    await main.getByPlaceholder("Filter by actor").fill("alice");
    await main.getByRole("button", { name: "Apply" }).click();

    await expect(main.getByText("alice").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator("table tbody").getByText("admin")).not.toBeVisible();
  });

  test("filter by outcome via the select narrows results", async ({ page }) => {
    await navigateToAuditLog(page);

    const main = page.locator("main");
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    await main.getByLabel("Outcome").selectOption("success");
    await main.getByRole("button", { name: "Apply" }).click();

    // Both mock events have outcome "success", so both remain visible.
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByText("alice").first()).toBeVisible();
  });

  test("clear filters restores the default query", async ({ page }) => {
    await navigateToAuditLog(page);

    const main = page.locator("main");
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    await main.getByPlaceholder("Filter by actor").fill("alice");
    await main.getByRole("button", { name: "Apply" }).click();
    await expect(page.locator("table tbody").getByText("admin")).not.toBeVisible();

    await main.getByRole("button", { name: "Clear" }).click();

    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByText("alice").first()).toBeVisible();
  });

  test("no-match filter shows the empty message", async ({ page }) => {
    await navigateToAuditLog(page);

    const main = page.locator("main");
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    await main.getByPlaceholder("Filter by actor").fill("nonexistent-actor-xyz");
    await main.getByRole("button", { name: "Apply" }).click();

    await expect(main.getByText("No audit events found.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("clicking an event row opens the detail panel", async ({ page }) => {
    await navigateToAuditLog(page);

    const main = page.locator("main");
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("table tbody tr").first().locator("td").first().click();

    await expect(page.getByRole("heading", { name: "Audit Event", exact: true })).toBeVisible();
    await expect(page.getByText("Request ID")).toBeVisible();
  });

  test("refresh button reloads events", async ({ page }) => {
    await navigateToAuditLog(page);

    const main = page.locator("main");
    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

    await main.getByRole("button", { name: "Refresh" }).click();

    await expect(main.getByText("admin").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByText("alice").first()).toBeVisible();
  });

  test("query error shows a friendly unavailable panel, not a raw error toast", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await injectError(page, "AuditBinding.Query", "audit not implemented");

    await page.locator("aside").getByText("Audit Log", { exact: true }).click();

    await expect(
      page.getByText("The audit log API is not available on this connection."),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator('[role="alert"].alert-error')).toHaveCount(0);

    await clearError(page, "AuditBinding.Query");
  });

  test("generic query error surfaces an error toast", async ({ page }) => {
    await navigateToAuditLog(page);

    await injectError(page, "AuditBinding.Query", "internal server error");
    await page.locator("main").getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("internal server error")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await clearError(page, "AuditBinding.Query");
  });
});
