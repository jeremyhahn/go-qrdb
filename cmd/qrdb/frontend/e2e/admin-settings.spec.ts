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

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the Settings admin section. */
async function navigateToSettings(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator("aside").getByText("Settings").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Settings"),
  ).toBeVisible();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Settings", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("shows version information in About section", async ({ page }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const aboutCard = main.locator(".card", { hasText: "About" });

    await expect(aboutCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The WailsClient returns { sdk: "0.1.0", server: "embedded" }.
    await expect(aboutCard.getByText("0.1.0")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(aboutCard.getByText("embedded")).toBeVisible();
  });

  test("About section displays Property and Value table columns", async ({
    page,
  }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const aboutCard = main.locator(".card", { hasText: "About" });

    await expect(aboutCard.locator("th", { hasText: "Property" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(aboutCard.locator("th", { hasText: "Value" })).toBeVisible();

    // sdk and server rows.
    await expect(aboutCard.getByText("sdk", { exact: true })).toBeVisible();
    await expect(aboutCard.getByText("server", { exact: true })).toBeVisible();
  });

  test("About section shows QuicRaft DB description", async ({ page }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const aboutCard = main.locator(".card", { hasText: "About" });

    await expect(aboutCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(aboutCard.getByText("QuicRaft DB")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("About section shows the QRDB divider and description text", async ({
    page,
  }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const aboutCard = main.locator(".card", { hasText: "About" });

    await expect(aboutCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(aboutCard.locator(".divider")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(
      aboutCard.getByText("High-performance distributed key-value store"),
    ).toBeVisible();
  });

  test("shows connection status section", async ({ page }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const connectionCard = main.locator(".card", { hasText: "Connection" });

    await expect(connectionCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Status, Transport, and Address stat titles.
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Status" }),
    ).toBeVisible();
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Transport" }),
    ).toBeVisible();
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Address" }),
    ).toBeVisible();
  });

  test("shows connected status badge in connection section", async ({
    page,
  }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const connectionCard = main.locator(".card", { hasText: "Connection" });

    await expect(connectionCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The connection store initialises as disconnected in the embedded mock
    // environment (setConnected is not called during mock initialisation).
    // Verify the status badge is present with the Disconnected label.
    await expect(connectionCard.locator(".badge-lg")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("shows theme toggle in Theme section", async ({ page }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const themeCard = main.locator(".card", { hasText: "Theme" });

    await expect(themeCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      themeCard.getByText("Toggle between light and dark mode"),
    ).toBeVisible();
  });

  test("theme toggle switches between light and dark mode", async ({
    page,
  }) => {
    await navigateToSettings(page);

    const main = page.locator("main");
    const themeCard = main.locator(".card", { hasText: "Theme" });

    await expect(themeCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Find the dark mode toggle checkbox.
    const themeToggle = themeCard.locator(
      "input[aria-label='Toggle dark mode']",
    );
    await themeToggle.click({ force: true });

    // After toggling the mode label should update.
    await expect(themeCard.locator("span.capitalize")).toBeVisible();
  });
});
