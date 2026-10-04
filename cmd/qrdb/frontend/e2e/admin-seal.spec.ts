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
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the Seal Manager admin section. */
async function navigateToSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator("aside").getByText("Seal Manager").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Seal Manager"),
  ).toBeVisible();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Seal Manager", () => {
  test.describe("unsealed initialized state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          sharesRequired: 3,
          sharesProvided: 0,
          sharesTotal: 5,
        },
      },
    });

    test("shows current seal status as Unsealed", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.locator(".stat-title", { hasText: "Seal Status" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await expect(
        main.locator(".badge-success").getByText("Unsealed"),
      ).toBeVisible();
    });

    test("shows total shares count", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.locator(".stat-title", { hasText: "Total Shares" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // sharesTotal is 5.
      await expect(main.getByText("5", { exact: true })).toBeVisible();
    });

    test("hides single-admin Seal button and points to the holder quorum", async ({ page }) => {
      // sharesRequired > 0 marks this barrier as Shamir-protected. A single
      // admin cannot seal it (the server refuses BarrierSeal), and the page
      // takes no share: its holders seal it through a quorum from the CLI or
      // the desktop app.
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toHaveCount(0);
      const note = page.getByTestId("seal-quorum-note");
      await expect(note).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(note).toContainText("quorum of its share holders");
      await expect(note).toContainText("qrdb barrier quorum submit --op seal");
      await expect(page.getByTestId("seal-vote-input")).toHaveCount(0);
    });

    test("status error shows error toast", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await injectError(page, "SealBinding.Status", "seal status unavailable");

      await page.locator("aside").getByText("Seal Manager").click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(alert.locator("span")).toContainText(
        "seal status unavailable",
      );

      await clearError(page, "SealBinding.Status");
    });
  });

  test.describe("unsealed single-admin (non-shamir) state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
          strategy: "passphrase",
        },
      },
    });

    test("shows single-admin Seal button when unsealed", async ({ page }) => {
      // sharesRequired === 0 and a non-shamir strategy mark this barrier as
      // single-admin sealable, so the Seal button renders instead of the
      // holder-quorum note.
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByTestId("seal-vote-input")).toHaveCount(0);
    });

    test("seal button shows confirmation dialog", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Seal", exact: true }).click();

      // String matcher: the copy wraps across source lines.
      await expect(
        main.getByText("Are you sure? The server will reject all operations"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(
        main.getByRole("button", { name: "Confirm Seal" }),
      ).toBeVisible();
    });

    test("seal sends the SO PIN entered in the confirmation", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Seal", exact: true }).click();
      await page.getByTestId("seal-so-pin").fill("SO123");
      await main.getByRole("button", { name: "Confirm Seal" }).click();

      await expect.poll(() => page.evaluate(() => {
        const w = window as unknown as { __QRDB_MOCK_STATE__: { sealSoPins?: string[] } };
        return w.__QRDB_MOCK_STATE__.sealSoPins ?? [];
      }), { timeout: LOAD_TIMEOUT }).toEqual(["SO123"]);
    });

    test("seal cancel does not seal", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Seal", exact: true }).click();
      await expect(
        main.getByRole("button", { name: "Confirm Seal" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await main.getByRole("button", { name: "Cancel", exact: true }).click();

      // The confirmation dialog dismisses and the Seal button returns;
      // the status badge still reads Unsealed.
      await expect(
        main.getByRole("button", { name: "Confirm Seal" }),
      ).toHaveCount(0);
      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toBeVisible();
      await expect(
        main.locator(".badge-success").getByText("Unsealed"),
      ).toBeVisible();
    });

    test("seal error shows error toast", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await injectError(page, "SealBinding.Seal", "seal failed: barrier busy");

      await main.getByRole("button", { name: "Seal", exact: true }).click();
      await main.getByRole("button", { name: "Confirm Seal" }).click();

      const alert = page.locator('[role="alert"]').filter({
        hasText: "seal failed: barrier busy",
      });
      await expect(alert).toBeVisible({ timeout: 5_000 });

      await clearError(page, "SealBinding.Seal");
    });
  });

  test.describe("sealed initialized Shamir state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: true,
          initialized: true,
          sharesRequired: 2,
          sharesProvided: 0,
          sharesTotal: 3,
        },
        unsealProgress: {
          sealed: true,
          sharesProvided: 0,
          sharesRequired: 2,
        },
      },
    });

    test("shows seal status as Sealed in Seal Manager when sealed", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // In desktop mode, navigate to Seal Manager to see seal status.
      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      await expect(
        main.locator(".stat-title", { hasText: "Seal Status" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Status badge should show Sealed.
      await expect(
        main.locator(".badge-warning").getByText("Sealed"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("a sealed Shamir barrier points to the holder quorum and takes no share", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");
      const note = page.getByTestId("unseal-quorum-note");
      await expect(note).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(note).toContainText("2 of 3 must submit");
      await expect(note).toContainText("qrdb barrier quorum submit --op unseal");
      await expect(main.getByRole("button", { name: "Submit Share" })).toHaveCount(0);
    });
  });

  test.describe("unseal error handling", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: true,
          initialized: true,
          strategy: "passphrase",
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
        },
        unsealProgress: {
          sealed: true,
          sharesProvided: 0,
          sharesRequired: 2,
        },
      },
    });

    test("unseal error shows error toast", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Navigate to Seal Manager.
      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      await injectError(page, "SealBinding.Unseal", "unseal operation failed");

      await main.getByPlaceholder("Enter passphrase").fill("bad-passphrase");
      await main.getByRole("button", { name: "Unseal", exact: true }).click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("unseal operation failed");

      await clearError(page, "SealBinding.Unseal");
    });
  });

  // -------------------------------------------------------------------------
  // Encryption not configured ("none" strategy)
  //
  // A "none"-strategy barrier performs no encryption at rest, so there is
  // nothing to seal. The Seal Manager must hide the Seal/Unseal/re-init
  // controls and present an "encryption not configured" panel instead.
  // Regression guard for the bug where a none/unconfigured barrier offered a
  // single-admin Seal button that flipped the server into a meaningless
  // sealed state.
  // -------------------------------------------------------------------------

  test.describe("not configured — committed 'none' barrier", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          strategy: "none",
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
        },
      },
    });

    test("shows 'Not configured' status and hides seal controls", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      // Seal-state stat reads "Not configured" rather than Sealed/Unsealed.
      await expect(page.getByTestId("seal-status-not-configured")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // The not-configured panel is shown.
      await expect(
        page.getByTestId("encryption-not-configured-card"),
      ).toBeVisible();

      // No Seal / Unseal controls (there is nothing to seal).
      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toHaveCount(0);
      await expect(
        main.getByRole("button", { name: "Unseal", exact: true }),
      ).toHaveCount(0);
      await expect(page.getByTestId("seal-vote-input")).toHaveCount(0);
      // The re-initialize card is hidden for an unconfigured barrier.
      await expect(
        main.getByRole("button", { name: "Re-initialize" }),
      ).toHaveCount(0);
    });

    test("an initialized 'none' barrier shows the enable-encryption wizard, not an in-place form", async ({ page }) => {
      await navigateToSealManager(page);

      // A committed "none" barrier supports an online enable-encryption
      // migration, so the wizard is offered (strategy select + submit) rather
      // than the one-time in-place Configure Encryption button.
      await expect(
        page.getByTestId("enable-encryption-wizard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByTestId("enable-encryption-strategy")).toBeVisible();
      await expect(page.getByTestId("enable-encryption-submit")).toBeVisible();
      await expect(page.getByTestId("configure-encryption-btn")).toHaveCount(0);
    });
  });

  test.describe("not configured — uninitialized barrier", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: false,
          strategy: "none",
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
        },
      },
    });

    test("offers in-place Configure Encryption with passphrase/shamir options", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        page.getByTestId("encryption-not-configured-card"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // No seal controls.
      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toHaveCount(0);

      // The Configure Encryption affordance is present; clicking it reveals the
      // strategy-selection form (passphrase by default, with a Shamir option).
      const configureBtn = page.getByTestId("configure-encryption-btn");
      await expect(configureBtn).toBeVisible();
      await configureBtn.click();

      await expect(page.getByTestId("configure-encryption-form")).toBeVisible();
      await expect(main.getByText("New Passphrase")).toBeVisible();
      await expect(main.getByText("Shamir Secret Sharing")).toBeVisible();
      await expect(
        page.getByTestId("configure-encryption-submit"),
      ).toBeVisible();
    });
  });
});
