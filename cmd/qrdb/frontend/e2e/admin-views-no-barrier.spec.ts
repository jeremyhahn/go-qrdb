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
 * Regression tests for admin views rendered against a qrdbd instance that has
 * no seal barrier configured.
 *
 * When barrier-dependent services are absent, the REST server does not register
 * the associated routes (/v1/tenants, /users/list, /kv/scan). Those endpoints
 * return HTTP 404 or 501. The admin views must handle these responses
 * gracefully: no error toast must appear and the loading state must clear,
 * leaving the view in its empty-state presentation.
 */

import {
  webTest as test,
  expect,
  injectWebRestMock,
  type WebMockState,
} from "./fixtures/web-mode-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async DOM operations and app boot. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock state: server with no barrier configured
// ---------------------------------------------------------------------------

/** Simulates a qrdbd node with no seal barrier — not initialized, initialized_at absent. */
const noBarrierState: WebMockState = {
  sealStatus: {
    sealed: false,
    initialized: false,
    strategy: "none",
    hardware_backed: false,
  },
  databases: [
    { name: "default", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Boots the app via page.goto and waits for the main shell to finish
 * rendering the Dashboard breadcrumb.
 */
async function waitForAppReady(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible(
    { timeout: LOAD_TIMEOUT },
  );
}

/**
 * Asserts that no element with role="alert" is currently visible on the page.
 * Uses a short timeout because absence is asserted — the element should never
 * appear.
 */
async function assertNoErrorToast(page: Page): Promise<void> {
  await expect(page.locator('[role="alert"]')).toHaveCount(0, {
    timeout: 3_000,
  });
}

/**
 * Asserts that no loading spinner (the DaisyUI `.loading` class) is visible
 * within the main content area.
 */
async function assertNoLoadingSpinner(page: Page): Promise<void> {
  await expect(
    page.locator("main .loading"),
  ).toHaveCount(0, { timeout: 5_000 });
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

test.describe("Admin views - no barrier configured", () => {
  test.use({ webMockState: noBarrierState });

  // -------------------------------------------------------------------------
  // Tenants view — GET /v1/tenants returns 404
  // -------------------------------------------------------------------------

  test.describe("Tenants view", () => {
    test("shows empty state and no error toast when /v1/tenants returns 404", async ({
      page,
    }) => {
      await injectWebRestMock(page, noBarrierState);

      await page.route("**/v1/tenants/list**", (route) => {
        void route.fulfill({
          status: 404,
          contentType: "text/plain",
          body: "404 page not found",
        });
      });

      await waitForAppReady(page);
      await page.locator("aside").getByText("Tenants").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Tenants"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Loading must clear — no stuck spinner.
      await assertNoLoadingSpinner(page);

      // Empty state message must appear.
      await expect(page.getByText("No tenants found.")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // No error toast.
      await assertNoErrorToast(page);
    });

    test("shows empty state and no error toast when /v1/tenants returns 501", async ({
      page,
    }) => {
      await injectWebRestMock(page, noBarrierState);

      await page.route("**/v1/tenants/list**", (route) => {
        void route.fulfill({
          status: 501,
          contentType: "application/json",
          body: JSON.stringify({ error: "rest: service not implemented: tenant" }),
        });
      });

      await waitForAppReady(page);
      await page.locator("aside").getByText("Tenants").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Tenants"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await assertNoLoadingSpinner(page);
      await expect(page.getByText("No tenants found.")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await assertNoErrorToast(page);
    });
  });

  // -------------------------------------------------------------------------
  // Users view — GET /users/list returns 404 or 501
  // -------------------------------------------------------------------------

  test.describe("Users view", () => {
    test("shows empty state and no error toast when /users/list returns 404", async ({
      page,
    }) => {
      await injectWebRestMock(page, noBarrierState);

      await page.route("**/users/list**", (route) => {
        void route.fulfill({
          status: 404,
          contentType: "text/plain",
          body: "404 page not found",
        });
      });

      await waitForAppReady(page);
      await page.locator("aside").getByText("Users").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Users"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await assertNoLoadingSpinner(page);
      await expect(page.getByText("No users found.")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await assertNoErrorToast(page);
    });

    test("shows empty state and no error toast when /users/list returns 501", async ({
      page,
    }) => {
      await injectWebRestMock(page, noBarrierState);

      await page.route("**/users/list**", (route) => {
        void route.fulfill({
          status: 501,
          contentType: "application/json",
          body: JSON.stringify({ error: "rest: service not implemented: user" }),
        });
      });

      await waitForAppReady(page);
      await page.locator("aside").getByText("Users").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Users"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await assertNoLoadingSpinner(page);
      await expect(page.getByText("No users found.")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await assertNoErrorToast(page);
    });
  });

  // -------------------------------------------------------------------------
  // Audit Log view — GET /v1/audit/events returns 404 or 501
  // -------------------------------------------------------------------------

  test.describe("Audit Log view", () => {
    test("shows the unavailable panel and no stuck spinner when /v1/audit/events returns 404", async ({
      page,
    }) => {
      await injectWebRestMock(page, noBarrierState);

      await page.route("**/v1/audit/events**", (route) => {
        void route.fulfill({
          status: 404,
          contentType: "text/plain",
          body: "404 page not found",
        });
      });

      await waitForAppReady(page);
      await page.locator("aside").getByText("Audit Log").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Audit Log"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Loading must clear — no stuck spinner.
      await assertNoLoadingSpinner(page);

      // Friendly "not available" panel must appear (not the empty-table state).
      await expect(
        page.getByText("The audit log API is not available on this connection."),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // No error toast.
      await assertNoErrorToast(page);
    });

    test("shows the unavailable panel and no stuck spinner when /v1/audit/events returns 501", async ({
      page,
    }) => {
      await injectWebRestMock(page, noBarrierState);

      await page.route("**/v1/audit/events**", (route) => {
        void route.fulfill({
          status: 501,
          contentType: "application/json",
          body: JSON.stringify({ error: "audit not configured" }),
        });
      });

      await waitForAppReady(page);
      await page.locator("aside").getByText("Audit Log").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Audit Log"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await assertNoLoadingSpinner(page);
      await expect(
        page.getByText("The audit log API is not available on this connection."),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await assertNoErrorToast(page);
    });
  });
});
