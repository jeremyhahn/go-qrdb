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
 * Web-mode (RestClient) mocked-tier coverage for the SealManager
 * "Enable Encryption" migration wizard.
 *
 * The wizard appears on a committed "none" barrier and drives the SYSTEM
 * barrier (tenant_id omitted). It POSTs /v1/barrier/enable-encryption, then
 * polls GET /v1/barrier/status until the strategy changes from "none" to the
 * keyed strategy — mirroring the asynchronous, 202-style migration contract.
 */

import { webTest as test, expect, type WebMockState } from "./fixtures/web-mode-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

/** Committed "none" barrier: initialized (initialized_at set) but unencrypted. */
const committedNoneState: WebMockState = {
  sealStatus: {
    sealed: false,
    strategy: "none",
    hardware_backed: false,
    initialized_at: "2026-01-01T00:00:00Z",
  },
  databases: [],
};

/** Navigates to the Seal Manager admin section in web mode. */
async function gotoSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Seal Manager").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Seal Manager"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Web mode - enable encryption wizard", () => {
  test.use({ webMockState: committedNoneState });

  test("migrates a committed 'none' barrier to passphrase and shows success", async ({ page }) => {
    // Staged barrier status emulating the REAL system-scope wire sequence:
    // {sealed:false, strategy:"none"} until the migration POST, after which
    // the daemon recycles the data plane in-process and comes back SEALED
    // under the new keyed strategy — the recycled barrier self-describes its
    // persisted strategy while sealed, so the poll sees
    // {sealed:true, strategy:"passphrase"}. The operator must then unseal.
    let strategy = "none";
    let sealed = false;
    await page.unroute("**/v1/barrier/status");
    await page.route("**/v1/barrier/status", (route) => {
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          sealed,
          strategy,
          hardware_backed: false,
          initialized_at: "2026-01-01T00:00:00Z",
        }),
      });
    });

    let enableCalled = false;
    let enableBody: Record<string, unknown> | null = null;
    await page.route("**/v1/barrier/enable-encryption", (route) => {
      enableCalled = true;
      enableBody = route.request().postDataJSON() as Record<string, unknown>;
      // Migration commits asynchronously; the next status poll sees the
      // recycled, sealed barrier reporting the keyed strategy.
      strategy = "passphrase";
      sealed = true;
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ status: "migration_started", scope: "system" }),
      });
    });

    await gotoSealManager(page);

    // The wizard is offered for a committed "none" barrier.
    await expect(page.getByTestId("enable-encryption-wizard")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Choose the passphrase strategy and supply a passphrase.
    await page.getByTestId("enable-encryption-strategy").selectOption("passphrase");
    await page.getByTestId("enable-encryption-passphrase").fill("hunter2hunter2");
    await page.getByTestId("enable-encryption-submit").click();

    // The UI transitions to the in-progress state...
    await expect(page.getByTestId("enable-encryption-progress")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // ...then the poll observes the keyed strategy and the success state shows.
    await expect(page.getByTestId("enable-encryption-success")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // The status store now reflects the keyed strategy (named in the success copy).
    await expect(page.getByTestId("enable-encryption-success")).toContainText(
      "passphrase",
    );

    // A system-scope migration comes back SEALED: the success state must
    // include guidance that the barrier is sealed and must be unsealed with
    // the new credential.
    await expect(
      page.getByTestId("enable-encryption-unseal-guidance"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      page.getByTestId("enable-encryption-unseal-guidance"),
    ).toContainText("sealed");
    await expect(
      page.getByTestId("enable-encryption-unseal-guidance"),
    ).toContainText("passphrase");

    // The request targeted the SYSTEM scope (no tenant_id) and carried the
    // passphrase under the snake_case wire contract.
    expect(enableCalled).toBe(true);
    expect(enableBody).not.toBeNull();
    const body = enableBody as unknown as Record<string, unknown>;
    expect(body.tenant_id).toBeUndefined();
    expect(body.strategy).toBe("passphrase");
    // The passphrase is []byte on the server so it can be zeroized: base64
    // on the wire.
    expect(body.passphrase).toBe(btoa("hunter2hunter2"));
  });

  test("surfaces a 403 shared-barrier rejection as an error toast", async ({ page }) => {
    await page.route("**/v1/barrier/enable-encryption", (route) => {
      void route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({
          error: "tenant shares the system barrier and cannot change encryption",
        }),
      });
    });

    await gotoSealManager(page);

    await expect(page.getByTestId("enable-encryption-wizard")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await page.getByTestId("enable-encryption-strategy").selectOption("passphrase");
    await page.getByTestId("enable-encryption-passphrase").fill("hunter2hunter2");
    await page.getByTestId("enable-encryption-submit").click();

    // The 403 surfaces as an error toast carrying the status and server message.
    const alert = page.locator('[role="alert"]').filter({ hasText: "403" });
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("cannot change encryption");

    // The wizard must NOT advance to the in-progress or success state.
    await expect(page.getByTestId("enable-encryption-progress")).toHaveCount(0);
    await expect(page.getByTestId("enable-encryption-success")).toHaveCount(0);
  });

  test("surfaces a 409 migration-in-progress rejection as an error toast", async ({ page }) => {
    await page.route("**/v1/barrier/enable-encryption", (route) => {
      void route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({ error: "a migration is already in progress" }),
      });
    });

    await gotoSealManager(page);

    await expect(page.getByTestId("enable-encryption-wizard")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await page.getByTestId("enable-encryption-strategy").selectOption("passphrase");
    await page.getByTestId("enable-encryption-passphrase").fill("hunter2hunter2");
    await page.getByTestId("enable-encryption-submit").click();

    const alert = page.locator('[role="alert"]').filter({ hasText: "409" });
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("already in progress");

    await expect(page.getByTestId("enable-encryption-progress")).toHaveCount(0);
    await expect(page.getByTestId("enable-encryption-success")).toHaveCount(0);
  });
});
