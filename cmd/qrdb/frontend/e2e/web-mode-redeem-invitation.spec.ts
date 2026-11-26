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
 * Browser-mode invitations.redeem: the REST client can only post to the
 * server that served the page, so the form must show that server read-only
 * and offer no other destination. A bearer invitation token typed for
 * server B must never be posted to the page's server A.
 */

import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 10_000;

const CSR_PEM =
  "-----BEGIN CERTIFICATE REQUEST-----\nMOCKCSR\n-----END CERTIFICATE REQUEST-----";

test.describe("Web Mode - Redeem Invitation", () => {
  test("redeem is constrained to the page's own server", async ({ page }) => {
    const posted: { host: string; body: string }[] = [];
    await page.route("**/invitations/redeem", async (route) => {
      const req = route.request();
      posted.push({ host: new URL(req.url()).host, body: req.postData() ?? "" });
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          username: "erin",
          userID: "user-1",
          tenantID: "default",
          certPEM: "-----BEGIN CERTIFICATE-----\nMOCK-erin\n-----END CERTIFICATE-----",
        }),
      });
    });

    await page.goto("/");
    await page.locator(".breadcrumbs").getByText("Dashboard").waitFor({
      state: "visible",
      timeout: LOAD_TIMEOUT,
    });
    await page.getByRole("button", { name: "Add server", exact: true }).click();
    await page.getByRole("button", { name: "Redeem Invitation" }).first().click();
    await expect(page.getByRole("heading", { name: "Redeem Invitation" })).toBeVisible();

    const pageHost = new URL(page.url()).host;
    const server = page.locator("#redeem-server");
    await expect(server).toHaveValue(pageHost);
    await expect(server).not.toBeEditable();
    await expect(page.getByTestId("redeem-browser-note")).toBeVisible();
    // The browser cannot honor another transport or CA file, so neither is offered.
    await expect(page.locator("#redeem-transport")).toHaveCount(0);
    await expect(page.locator("#redeem-ca-file")).toHaveCount(0);

    await page.locator("#redeem-tenant").fill("default");
    await page.locator("#redeem-token").fill("inv-token-erin");
    await page.locator("#redeem-csr").fill(CSR_PEM);
    await page.getByRole("button", { name: "Redeem", exact: true }).click();

    await expect(page.getByRole("heading", { name: "Invitation redeemed" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    expect(posted).toHaveLength(1);
    // The token went to exactly the server the form displayed.
    expect(posted[0]!.host).toBe(pageHost);
    expect(posted[0]!.body).toContain("inv-token-erin");
  });

  // An initialized auth-enabled server refuses the status read of a visitor
  // with no client certificate. The page must not dead-end on the
  // Connection Error card: an invitee redeems from the start screen.
  test("an unauthenticated status refusal offers invitation redemption", async ({ page }) => {
    const posted: string[] = [];
    await page.route("**/v1/barrier/status", (route) =>
      route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ error: "authentication required" }),
      }),
    );
    await page.route("**/invitations/redeem", async (route) => {
      posted.push(route.request().postData() ?? "");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          username: "erin",
          userID: "user-1",
          tenantID: "default",
          certPEM: "-----BEGIN CERTIFICATE-----\nMOCK-erin\n-----END CERTIFICATE-----",
        }),
      });
    });

    await page.goto("/");
    await expect(page.getByTestId("app-unauthenticated")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByRole("heading", { name: "Connection Error" })).toHaveCount(0);
    await expect(page.getByTestId("app-unauthenticated-retry")).toBeVisible();

    await page.getByTestId("app-redeem-invitation").click();
    await expect(page.getByRole("heading", { name: "Redeem Invitation" })).toBeVisible();
    await page.locator("#redeem-tenant").fill("default");
    await page.locator("#redeem-token").fill("inv-token-erin");
    await page.locator("#redeem-csr").fill(CSR_PEM);
    await page.getByRole("button", { name: "Redeem", exact: true }).click();

    await expect(page.getByRole("heading", { name: "Invitation redeemed" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    expect(posted).toHaveLength(1);
    expect(posted[0]).toContain("inv-token-erin");
  });

  // Any other status failure is still a startup failure: the error card,
  // with no redemption offered.
  test("a server error on the status read still shows the error card", async ({ page }) => {
    await page.route("**/v1/barrier/status", (route) =>
      route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "internal error" }),
      }),
    );

    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Connection Error" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByTestId("app-unauthenticated")).toHaveCount(0);
    await expect(page.getByTestId("app-redeem-invitation")).toHaveCount(0);
  });
});
