// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Web-mode Credential Manager against the REST wire representation.
 *
 * The REST handlers encode credential values as Go []byte, which
 * encoding/json renders as base64 (RFC 4648): a stored "first-secret"
 * arrives as "Zmlyc3Qtc2VjcmV0". The Wails binding returns plaintext, so the
 * desktop-mock specs cannot catch a missing decode; these tests serve the
 * real wire bodies and assert the view shows the plaintext.
 */

import type { Page, Route } from "@playwright/test";
import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 10_000;

/** base64 of "first-secret", "second-secret" and "sekrit-value". */
const FIRST_SECRET_B64 = "Zmlyc3Qtc2VjcmV0";
const SECOND_SECRET_B64 = "c2Vjb25kLXNlY3JldA==";
const CURRENT_SECRET_B64 = "c2Vrcml0LXZhbHVl";

function json(route: Route, body: unknown): void {
  void route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
}

/** Serves the credential routes for one credential, "versioned", with two versions. */
async function routeCredentials(page: Page): Promise<void> {
  await page.route("**/v1/credentials**", (route) => {
    const path = new URL(route.request().url()).pathname;
    switch (path) {
      case "/v1/credentials":
        json(route, { names: ["versioned"] });
        return;
      case "/v1/credentials/strategy":
        json(route, { strategy: "manual" });
        return;
      case "/v1/credentials/versioned/versions":
        json(route, {
          name: "versioned",
          versions: [
            { version: 2, is_current: true, created_at_unix: 1_700_000_100 },
            { version: 1, is_current: false, created_at_unix: 1_700_000_000, grace_until_unix: 4_000_000_000 },
          ],
        });
        return;
      case "/v1/credentials/versioned/versions/1":
        json(route, { value: FIRST_SECRET_B64 });
        return;
      case "/v1/credentials/versioned/versions/2":
        json(route, { value: SECOND_SECRET_B64 });
        return;
      case "/v1/credentials/versioned/value":
        json(route, { value: CURRENT_SECRET_B64 });
        return;
      default:
        void route.fulfill({ status: 404, contentType: "text/plain", body: "404 page not found" });
    }
  });
}

async function openVersioned(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Credentials", { exact: true }).click();
  await expect(page.locator(".breadcrumbs").getByText("Credentials")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("table tbody tr").first().click();
  await expect(page.getByText("Version History")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Web mode - Credential Manager decodes REST credential values", () => {
  test("reveal version shows the decoded plaintext, not the base64 wire value", async ({ page }) => {
    await routeCredentials(page);
    await openVersioned(page);

    const v1 = page.getByTestId("credential-version-value-1");
    await page.getByRole("button", { name: "Reveal version 1" }).click();
    await expect(v1).toHaveText("first-secret", { timeout: 5_000 });
    await expect(page.getByText(FIRST_SECRET_B64)).toHaveCount(0);

    await page.getByRole("button", { name: "Reveal version 2" }).click();
    await expect(page.getByTestId("credential-version-value-2")).toHaveText("second-secret", { timeout: 5_000 });
  });

  test("reveal shows the decoded current value", async ({ page }) => {
    await routeCredentials(page);
    await openVersioned(page);

    await page.getByRole("button", { name: "Reveal", exact: true }).click();
    await expect(page.getByText("sekrit-value")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText(CURRENT_SECRET_B64)).toHaveCount(0);
  });
});
