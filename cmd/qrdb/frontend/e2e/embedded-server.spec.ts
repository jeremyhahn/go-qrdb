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
 * EmbeddedServerPanel e2e (Wails mock): the desktop-only panel in the
 * Connection Manager drives EmbeddedServerClient — isEmbedded,
 * hasSavedConfig, stopEmbedded, restoreEmbedded and clearSavedConfig —
 * and opens the embedded wizard for a new server.
 */

import {
  test,
  expect,
  defaultMockState,
  injectError,
} from "./fixtures/wails-mock.ts";
import { waitForConnectionManager } from "./fixtures/embedded-wizard-steps.ts";
import type { Page } from "@playwright/test";

const STEP_TIMEOUT = 10_000;

/** First-run state (no profiles) with a saved embedded server config. */
const savedState = {
  ...defaultMockState,
  embeddedSavedConfig: true,
} as typeof defaultMockState;

function panel(page: Page) {
  return page.getByTestId("embedded-server-panel");
}

async function openPanel(page: Page): Promise<void> {
  await page.goto("/");
  await waitForConnectionManager(page);
  await expect(panel(page)).toBeVisible({ timeout: STEP_TIMEOUT });
}

test.describe("Embedded server panel - no saved config", () => {
  test.use({ mockState: { ...defaultMockState } });

  test("shows stopped with no saved config", async ({ page }) => {
    await openPanel(page);
    await expect(panel(page).getByTestId("embedded-running")).toHaveText("Stopped");
    await expect(panel(page).getByTestId("embedded-saved")).toHaveText("No saved config");
    await expect(panel(page).getByRole("button", { name: "Stop" })).toBeDisabled();
    await expect(
      panel(page).getByRole("button", { name: "Clear saved config" }),
    ).toBeDisabled();
  });

  test("restore with none saved shows the error", async ({ page }) => {
    await openPanel(page);
    await panel(page).getByRole("button", { name: "Restore saved config" }).click();
    await expect(panel(page).getByTestId("embedded-error")).toContainText(
      "no saved embedded server config",
    );
    await expect(panel(page).getByTestId("embedded-running")).toHaveText("Stopped");
  });

  test("Start new opens the embedded wizard", async ({ page }) => {
    await openPanel(page);
    await panel(page).getByRole("button", { name: "Start new" }).click();
    await expect(
      page.getByRole("heading", { name: "Create Embedded Server" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });
});

test.describe("Embedded server panel - saved config", () => {
  test.use({ mockState: savedState });

  test("restore then stop", async ({ page }) => {
    await openPanel(page);
    await expect(panel(page).getByTestId("embedded-saved")).toHaveText("Saved config");

    await panel(page).getByRole("button", { name: "Restore saved config" }).click();
    await expect(panel(page).getByTestId("embedded-running")).toHaveText("Running");

    await panel(page).getByRole("button", { name: "Stop" }).click();
    await expect(panel(page).getByTestId("embedded-running")).toHaveText("Stopped");
    await expect(panel(page).getByTestId("embedded-error")).toHaveCount(0);
  });

  test("clear saved config asks to confirm", async ({ page }) => {
    await openPanel(page);
    await panel(page).getByRole("button", { name: "Clear saved config" }).click();

    // Cancel keeps the config.
    await panel(page).getByRole("button", { name: "Cancel" }).click();
    await expect(panel(page).getByTestId("embedded-saved")).toHaveText("Saved config");

    await panel(page).getByRole("button", { name: "Clear saved config" }).click();
    await panel(page).getByRole("button", { name: "Confirm clear" }).click();
    await expect(panel(page).getByTestId("embedded-saved")).toHaveText("No saved config");
    await expect(
      panel(page).getByRole("button", { name: "Clear saved config" }),
    ).toBeDisabled();
  });

  test("stop failure is shown and the server stays running", async ({ page }) => {
    await openPanel(page);
    await panel(page).getByRole("button", { name: "Restore saved config" }).click();
    await expect(panel(page).getByTestId("embedded-running")).toHaveText("Running");

    await injectError(page, "EmbeddedServerBinding.StopEmbedded", "embedded: stop failed");
    await panel(page).getByRole("button", { name: "Stop" }).click();
    await expect(panel(page).getByTestId("embedded-error")).toContainText(
      "embedded: stop failed",
    );
    await expect(panel(page).getByTestId("embedded-running")).toHaveText("Running");
  });
});
