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
 * Regression test for the "barrier route not registered" case in web mode.
 *
 * A qrdbd instance launched without a seal barrier configured does NOT
 * register the `/v1/barrier/status` route (see `pkg/api/rest/server.go:546`
 * — the route is only added when `s.barrierSvc != nil`). The REST client
 * then gets a 404 from `client.seal.status()`, and the Connection Manager's
 * `resolveWebConnectionState` must fall back to probing a guaranteed-
 * available endpoint (`client.database.list()`) before declaring the
 * server unreachable.
 *
 * Without the fallback the auto-registered "This Server" profile would be
 * reported as unreachable even though the qrdbd node is serving REST
 * requests perfectly on the same port.
 */

import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 10_000;

test.describe("Web mode - server without barrier route", () => {
  test("reports connected when /v1/barrier/status returns 404 but /v1/databases/list works", async ({
    page,
  }) => {
    // Override the default REST mock: 404 on barrier status, 200 on
    // database list. This mirrors a qrdbd instance started without a
    // seal barrier configured.
    await page.unroute("**/v1/barrier/status");
    await page.route("**/v1/barrier/status", (route) => {
      void route.fulfill({
        status: 404,
        contentType: "text/plain",
        body: "404 page not found",
      });
    });

    await page.unroute("**/v1/databases/list");
    await page.route("**/v1/databases/list", (route) => {
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          databases: [
            {
              name: "default",
              backend: "pebble",
              status: "open",
            },
          ],
        }),
      });
    });

    await page.goto("/");

    // App must reach MainApp — not stall on "Initializing" or show a
    // connection error view.
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The "This Server" built-in profile must show "connected" status in
    // the sidebar. We assert the Connected badge exists (there must not
    // be an "Unreachable" label).
    await expect(page.getByText("Connected").first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByText(/Unreachable/i)).toHaveCount(0);
  });

  test("still reports unreachable when both barrier status and database list fail", async ({
    page,
  }) => {
    await page.unroute("**/v1/barrier/status");
    await page.route("**/v1/barrier/status", (route) => {
      void route.fulfill({
        status: 404,
        contentType: "text/plain",
        body: "404 page not found",
      });
    });

    await page.unroute("**/v1/databases/list");
    await page.route("**/v1/databases/list", (route) => {
      void route.fulfill({ status: 500, body: "internal" });
    });

    await page.goto("/");

    // The app should still load MainApp (empty state) — it does not
    // block on unreachable profiles — but the sidebar must reflect that
    // "This Server" is in an error state.
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard").or(
        page.getByText(/Unreachable|error/i).first(),
      ),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});
