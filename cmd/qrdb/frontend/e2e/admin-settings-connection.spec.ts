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

// Settings: connection status (ConnectionClient.isConnected), disconnect
// (ConnectionClient.disconnect) and the Server section
// (ServerConfigClient.initialized / isEmbeddedAvailable).

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

/** Loads the app on the Dashboard. */
async function openApp(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Opens the Settings view from the sidebar. */
async function openSettings(page: Page): Promise<void> {
  await page.locator("aside").getByText("Settings").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Settings"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Settings connection and server", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("shows the connected badge from the client", async ({ page }) => {
    await openApp(page);
    await openSettings(page);

    const badge = page.getByTestId("settings-connection-status");
    await expect(badge).toHaveText("Connected", { timeout: LOAD_TIMEOUT });
    await expect(badge).toHaveClass(/badge-success/);
    await expect(page.getByTestId("settings-disconnect")).toBeEnabled();
  });

  test("disconnect closes the connection and leaves the app", async ({ page }) => {
    await openApp(page);
    await openSettings(page);

    const button = page.getByTestId("settings-disconnect");
    await expect(button).toBeEnabled({ timeout: LOAD_TIMEOUT });
    await button.click();

    // The active profile is now disconnected: the badge flips and the
    // sidebar is the connection manager again, offering Connect.
    const badge = page.getByTestId("settings-connection-status");
    await expect(badge).toHaveText("Disconnected", { timeout: LOAD_TIMEOUT });
    await expect(badge).toHaveClass(/badge-error/);
    await expect(button).toBeDisabled();
    await expect(
      page.locator("aside").getByRole("button", { name: "Connect", exact: true }),
    ).toBeVisible();
  });

  test("server section shows initialized and embedded availability", async ({ page }) => {
    await openApp(page);
    await openSettings(page);

    const server = page.getByTestId("settings-server");
    await expect(server.getByRole("heading", { name: "Server" })).toBeVisible();
    await expect(page.getByTestId("settings-server-initialized")).toHaveText("Yes", {
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByTestId("settings-server-embedded")).toHaveText("Yes");
  });

  test("server section shows the error when Initialized fails", async ({ page }) => {
    await openApp(page);
    await injectError(page, "ConfigBinding.Initialized", "config store unavailable");
    await openSettings(page);

    await expect(page.getByTestId("settings-server-error")).toContainText(
      "config store unavailable",
      { timeout: LOAD_TIMEOUT },
    );
    await expect(page.getByTestId("settings-server-initialized")).toHaveCount(0);

    await clearError(page, "ConfigBinding.Initialized");
  });
});
