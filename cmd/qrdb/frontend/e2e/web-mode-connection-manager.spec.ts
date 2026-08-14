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
 * Web-mode Connection Manager integration tests.
 *
 * These tests verify that when the app runs in browser (web) mode — that is,
 * without any Wails bindings (window.go is absent) — the Connection Manager
 * sidebar works identically to desktop mode:
 *
 *   - A "This Server" built-in profile is auto-registered for the hosting
 *     qrdbd origin.
 *   - The sidebar shows the server tree (not a flat database list).
 *   - The [+] Add Server control is visible.
 *   - The Add Server dialog does NOT offer an "Embedded Server" option.
 *   - The "This Server" profile cannot be deleted (builtin flag respected).
 *
 * The REST API is mocked via page.route() so no live daemon is required.
 * window.go is deliberately absent so isDesktop() returns false throughout.
 */

import {
  webTest as test,
  expect,
  type WebMockState,
} from "./fixtures/web-mode-mock.ts";

/** Timeout for async DOM operations and app boot. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Shared mock state
// ---------------------------------------------------------------------------

const webState: WebMockState = {
  sealStatus: {
    sealed: false,
    strategy: "none",
    hardware_backed: false,
    initialized_at: "2026-01-01T00:00:00Z",
  },
  databases: [
    { name: "default", engine: "kv", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Helper: wait for the app shell to finish booting
// ---------------------------------------------------------------------------

async function waitForAppReady(
  page: Parameters<typeof test>[2] extends (args: { page: infer P }) => unknown ? P : never,
): Promise<void> {
  // In web mode the active profile is "self" (This Server), so the app
  // navigates to its dashboard and the breadcrumb shows "Dashboard".
  await page.locator(".breadcrumbs").getByText("Dashboard").waitFor({
    state: "visible",
    timeout: LOAD_TIMEOUT,
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Web Mode Connection Manager", () => {
  test.use({ webMockState: webState });

  test("auto-registers This Server built-in profile on first load", async ({
    page,
  }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // The sidebar server tree must contain a "This Server" entry.
    await expect(
      page.locator("aside").getByText("This Server"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("sidebar renders server tree with This Server expanded", async ({
    page,
  }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // The sidebar must show the Servers section header.
    await expect(
      page.locator("aside").getByText("Servers"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The server node for "This Server" must be present.
    await expect(
      page.locator("aside").getByText("This Server"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("sidebar shows database list within the server tree", async ({
    page,
  }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // The "default" database seeded in the mock must appear under the server.
    await expect(
      page.locator("aside").getByText("default"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("sidebar shows Add Server button in expanded mode", async ({ page }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // The [+] Add Server button in the Servers header must be accessible.
    const addServerBtn = page.locator("aside").getByTitle("Add server");
    await expect(addServerBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Add Server dialog does not show Embedded Server option in web mode", async ({
    page,
  }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // Open the Add Server dialog via the sidebar button.
    await page.locator("aside").getByTitle("Add server").click();

    // The modal must open (contains ConnectionManager).
    await expect(page.locator(".modal-box")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // In web mode, the "Embedded" / "Create Embedded Server" button must NOT
    // be present because embeddedAvailable is false.
    await expect(
      page.getByText("Create Embedded Server"),
    ).not.toBeVisible();
    await expect(
      page.getByRole("button", { name: /Embedded/i }),
    ).not.toBeVisible();
  });

  test("This Server profile has no delete button (builtin flag respected)", async ({
    page,
  }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // Open the Add Server / Connection Manager dialog.
    await page.locator("aside").getByTitle("Add server").click();
    await expect(page.locator(".modal-box")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // The profile card for "This Server" must not contain a delete button.
    const serverCard = page.locator(".modal-box").locator(".card", {
      has: page.getByText("This Server"),
    });
    await expect(serverCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The trash / delete button must be absent on the built-in card.
    await expect(
      serverCard.getByTitle("Delete profile"),
    ).not.toBeVisible();
  });

  test("header shows Connected badge after boot", async ({ page }) => {
    await page.goto("/");
    await waitForAppReady(page);

    await expect(page.getByText("Connected")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("admin sections are accessible in web mode", async ({ page }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // Admin items should be visible in the sidebar under the server node.
    await expect(page.locator("aside").getByText("Dashboard")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.locator("aside").getByText("Seal Manager")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("Daemon admin item is not visible in web mode", async ({ page }) => {
    await page.goto("/");
    await waitForAppReady(page);

    // The Daemon view is desktop-only and must not appear in the sidebar.
    await expect(
      page.locator("aside").getByText("Daemon"),
    ).not.toBeVisible();
  });
});
