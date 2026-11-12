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
 * DOM tier: the admin Settings view against a real qrdbd.
 *
 * Checks that the values the view shows (server version, connection status,
 * server initialized state) match what the server itself reports, and drives
 * Disconnect from the view, then reconnects from the sidebar's Connect button
 * and by a page load, and checks the view reports Connected again while the
 * server still answers.
 */

import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** GETs a route on the daemon's admin listener and returns the parsed JSON body. */
async function getV1<T>(request: APIRequestContext, path: string): Promise<{ status: number; body: T }> {
  const res = await request.get(`${daemon!.adminUrl}${path}`, { ignoreHTTPSErrors: true });
  return { status: res.status(), body: (await res.json()) as T };
}

/** Opens the admin Settings view the way admin-views-no-pageerror.spec.ts does. */
async function openSettings(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator('aside button[title="Settings"]').click();
  await expect(page.locator(".breadcrumbs").getByText("Settings")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Returns the About table's value cell for a version property. */
function versionCell(page: Page, key: string) {
  return page.locator("main table tbody tr").filter({ has: page.locator("td", { hasText: new RegExp(`^${key}$`) }) }).locator("td").nth(1);
}

test.describe("Settings view on an unsealed server (seal none)", () => {
  test.beforeEach(async () => {
    daemon = await startDaemon({ seal: "none" });
  });

  test("shows the server's version, connected and initialized state", async ({ page, request }) => {
    await openSettings(page);

    // The About table's server version is the build version GET /status
    // reports (what `qrdbd version` prints), not the node id.
    const status = await getV1<{ version: string; node_id: string }>(request, "/status");
    expect(status.status).toBe(200);
    expect(status.body.version).not.toBe("");
    expect(status.body.version).not.toBe(status.body.node_id);
    await expect(versionCell(page, "server")).toHaveText(status.body.version, { timeout: LOAD_TIMEOUT });
    await expect(versionCell(page, "sdk")).not.toBeEmpty();

    await expect(page.getByTestId("settings-connection-status")).toHaveText("Connected", { timeout: LOAD_TIMEOUT });

    const init = await getV1<{ initialized: boolean }>(request, "/v1/config/initialized");
    expect(init.status).toBe(200);
    await expect(page.getByTestId("settings-server-initialized")).toHaveText(init.body.initialized ? "Yes" : "No", {
      timeout: LOAD_TIMEOUT,
    });
    // A browser (REST) client never has an embedded server.
    await expect(page.getByTestId("settings-server-embedded")).toHaveText("No");
    await expect(page.getByTestId("settings-server-error")).toHaveCount(0);
  });

  test("Disconnect shows the disconnected state; Connect reconnects", async ({ page, request }) => {
    await openSettings(page);
    const statusBadge = page.getByTestId("settings-connection-status");
    await expect(statusBadge).toHaveText("Connected", { timeout: LOAD_TIMEOUT });

    await page.getByTestId("settings-disconnect").click();
    await expect(page.getByTestId("settings-disconnect-error")).toHaveCount(0);
    await expect(statusBadge).toHaveText("Disconnected", { timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("settings-disconnect")).toBeDisabled();

    // Disconnect is client-side only: the server keeps answering.
    const health = await request.get(`${daemon!.adminUrl}/health`, { ignoreHTTPSErrors: true });
    expect(health.status()).toBe(200);

    await page.getByRole("button", { name: "Connect", exact: true }).click();
    await expect(statusBadge).toHaveText("Connected", { timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("settings-disconnect")).toBeEnabled();

    // A fresh page load connects again too.
    await openSettings(page);
    await expect(page.getByTestId("settings-connection-status")).toHaveText("Connected", { timeout: LOAD_TIMEOUT });
  });
});
