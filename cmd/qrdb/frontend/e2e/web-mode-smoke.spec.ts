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
 * Core web mode smoke tests.
 *
 * These tests verify that the application loads correctly in web/REST mode:
 * no DesktopOnlyError surfaces, the header shows "Connected", the server tree
 * shows the configured server, admin sections are always accessible, and the
 * footer reflects seal status.
 *
 * The mock fixture simulates a second-launch embedded state so that the app
 * boots straight into MainApp without triggering the first-run wizard.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  type MockState,
} from "./fixtures/wails-mock.ts";

/** Timeout for async boot / DOM operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock state
// ---------------------------------------------------------------------------

/** State with a "default" database pre-populated in the sidebar. */
const webModeState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "default", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Smoke tests
// ---------------------------------------------------------------------------

test.describe("Web Mode Smoke", () => {
  test.use({ mockState: webModeState });

  test("app loads without DesktopOnlyError", async ({ page }) => {
    await page.goto("/");

    // The app must reach its main content view without showing any
    // DesktopOnlyError message.
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // No DesktopOnlyError text anywhere on the page.
    await expect(
      page.getByText("DesktopOnlyError", { exact: false }),
    ).not.toBeVisible();
    await expect(
      page.getByText("desktop only", { exact: false }),
    ).not.toBeVisible();
  });

  test("header shows Connected badge", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The header connection badge must read "Connected".
    await expect(page.getByText("Connected")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("sidebar shows databases list with default database", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The "default" database entry must appear in the sidebar.
    await expect(page.locator("aside").getByText("default")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("admin sections are always visible in sidebar", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Admin sections are always present — no mode toggle required.
    await expect(
      page.locator("aside").getByText("Dashboard"),
    ).toBeVisible();
    await expect(
      page.locator("aside").getByText("Seal Manager"),
    ).toBeVisible();
  });

  test("clicking admin section updates breadcrumb", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click the Dashboard admin section.
    await page.locator("aside").getByText("Dashboard").click();

    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("clicking admin section then database workspace returns to database view", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Enter admin section.
    await page.locator("aside").getByText("Dashboard").click();
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click a database to return to workspace view.
    await page.locator("aside").getByText("default").click();
    await expect(
      page.locator(".breadcrumbs").getByText("default", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("footer shows seal status", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const footer = page.locator("footer");
    await expect(footer.getByText("Unsealed")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });
});
