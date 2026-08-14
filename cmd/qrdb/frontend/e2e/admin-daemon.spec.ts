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
  secondLaunchExternalState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the Daemon admin section. */
async function navigateToDaemon(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator("aside").getByText("Daemon").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Daemon"),
  ).toBeVisible();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Daemon", () => {
  test.use({ mockState: secondLaunchExternalState });

  test("shows daemon status as Stopped initially", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(
      main.locator(".stat-title", { hasText: "Daemon Status" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The mock starts with running=false.
    await expect(
      main.locator(".badge-error").getByText("Stopped"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shows PID as '--' when daemon is stopped", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(
      main.locator(".stat-title", { hasText: "PID" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // pid is null when stopped, so the display value is "--".
    await expect(main.getByText("--").first()).toBeVisible();
  });

  test("shows config path input with default value", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.locator("#daemon-config-path")).toHaveValue(
      "/etc/qrdb/qrdbd.yaml",
    );
  });

  test("start daemon with valid config path shows success toast", async ({
    page,
  }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // The default config path is already filled; click Start.
    await main.getByRole("button", { name: "Start", exact: true }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      "Daemon started successfully",
    );
  });

  test("start daemon updates status to Running and shows PID", async ({
    page,
  }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await main.getByRole("button", { name: "Start", exact: true }).click();

    // Wait for success toast.
    await expect(page.locator('[role="alert"]')).toBeVisible({ timeout: 5_000 });

    // Status should now show Running.
    await expect(
      main.locator(".badge-success").getByText("Running"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // PID should show 12345.
    await expect(main.getByText("12345")).toBeVisible();
  });

  test("start daemon with empty config path shows warning", async ({
    page,
  }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Clear the config path.
    await main.locator("#daemon-config-path").fill("");
    await main.getByRole("button", { name: "Start", exact: true }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      "config path is required",
    );
  });

  test("stop daemon shows success toast and status becomes Stopped", async ({
    page,
  }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Start the daemon first.
    await main.getByRole("button", { name: "Start", exact: true }).click();
    await expect(page.locator('[role="alert"]')).toBeVisible({ timeout: 5_000 });
    await expect(
      main.locator(".badge-success").getByText("Running"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Stop the daemon.
    await main.getByRole("button", { name: "Stop" }).click();

    // Use last() because the start success toast may still be visible.
    const stopAlert = page.locator('[role="alert"]').last();
    await expect(stopAlert).toBeVisible({ timeout: 5_000 });
    await expect(stopAlert.locator("span")).toContainText(
      "Daemon stopped successfully",
    );

    // Status should return to Stopped.
    await expect(
      main.locator(".badge-error").getByText("Stopped"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("restart daemon shows success toast", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Start the daemon first so Restart becomes available.
    await main.getByRole("button", { name: "Start", exact: true }).click();
    await expect(page.locator('[role="alert"]')).toBeVisible({ timeout: 5_000 });
    await expect(
      main.locator(".badge-success").getByText("Running"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Restart.
    await main.getByRole("button", { name: "Restart" }).click();

    // Use last() because the start success toast may still be visible.
    const restartAlert = page.locator('[role="alert"]').last();
    await expect(restartAlert).toBeVisible({ timeout: 5_000 });
    await expect(restartAlert.locator("span")).toContainText(
      "Daemon restarted successfully",
    );
  });

  test("restart button disabled when daemon is stopped", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    // Daemon is stopped by default.
    await expect(
      main.locator(".badge-error").getByText("Stopped"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(
      main.getByRole("button", { name: "Restart" }),
    ).toBeDisabled();
  });

  test("stop button disabled when daemon is stopped", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(
      main.locator(".badge-error").getByText("Stopped"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(
      main.getByRole("button", { name: "Stop" }),
    ).toBeDisabled();
  });

  test("refresh status button reloads daemon status", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(
      main.locator(".stat-title", { hasText: "Daemon Status" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click Refresh Status.
    await main.getByRole("button", { name: "Refresh Status" }).click();

    // Status should still show Stopped after refresh.
    await expect(
      main.locator(".badge-error").getByText("Stopped"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("log viewer shows 'No log output available.' initially", async ({
    page,
  }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.getByText("Logs")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The pre element should display the empty-log message.
    await expect(main.locator("pre")).toContainText(
      "No log output available.",
    );
  });

  test("log viewer shows log content after daemon start", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Start the daemon to populate logs.
    await main.getByRole("button", { name: "Start", exact: true }).click();
    await expect(page.locator('[role="alert"]')).toBeVisible({ timeout: 5_000 });

    // The log pre element should contain output.
    await expect(main.locator("pre")).toContainText(
      "Daemon started successfully",
    );
  });

  test("max lines selector changes visible option", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    const selector = main.locator("select[aria-label='Max log lines']");
    await expect(selector).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Default is 100.
    await expect(selector).toHaveValue("100");

    // Change to 200.
    await selector.selectOption("200");
    await expect(selector).toHaveValue("200");
  });

  test("error on start shows error toast", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await injectError(page, "DaemonBinding.Start", "daemon start failed");

    await page.locator("aside").getByText("Daemon").click();

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await main.getByRole("button", { name: "Start", exact: true }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText("daemon start failed");

    await clearError(page, "DaemonBinding.Start");
  });

  test("error on stop shows error toast", async ({ page }) => {
    await navigateToDaemon(page);

    const main = page.locator("main");

    await expect(main.locator("#daemon-config-path")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Start first.
    await main.getByRole("button", { name: "Start", exact: true }).click();
    await expect(page.locator('[role="alert"]')).toBeVisible({ timeout: 5_000 });
    await expect(
      main.locator(".badge-success").getByText("Running"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Now inject a stop error.
    await injectError(page, "DaemonBinding.Stop", "daemon stop failed");

    await main.getByRole("button", { name: "Stop" }).click();

    // Use last() because the start success toast may still be visible.
    const stopAlert = page.locator('[role="alert"]').last();
    await expect(stopAlert).toBeVisible({ timeout: 5_000 });
    await expect(stopAlert.locator("span")).toContainText("daemon stop failed");

    await clearError(page, "DaemonBinding.Stop");
  });
});
