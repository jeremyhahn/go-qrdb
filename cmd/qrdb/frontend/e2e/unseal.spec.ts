// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import type { Page } from "@playwright/test";
import {
  test,
  expect,
  injectError,
  clearError,
  defaultMockState,
  type MockState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

/**
 * In desktop mode (Wails) the sealed state is surfaced through the Seal
 * Manager. A passphrase barrier is unsealed there with its passphrase; a
 * Shamir barrier is unsealed by a quorum of its share holders from the CLI or
 * the desktop app, so the page shows which command they run and takes no
 * share.
 */
function sealedState(strategy: "passphrase" | "shamir"): MockState {
  const shamir = strategy === "shamir";
  return {
    ...defaultMockState,
    isFirstRun: false,
    configInitialized: true,
    sealStatus: {
      sealed: true,
      initialized: true,
      strategy,
      sharesRequired: shamir ? 2 : 0,
      sharesProvided: 0,
      sharesTotal: shamir ? 3 : 0,
    },
    unsealProgress: { sealed: true, sharesProvided: 0, sharesRequired: 0 },
  };
}

async function openSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Seal Manager").click();
  await expect(page.locator(".breadcrumbs").getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.locator("main").locator(".badge-warning").getByText("Sealed")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Unseal flow", () => {
  test.describe("passphrase barrier", () => {
    test.use({ mockState: sealedState("passphrase") });

    test("the passphrase unseals the barrier", async ({ page }) => {
      await openSealManager(page);
      const main = page.locator("main");

      await main.getByPlaceholder("Enter passphrase").fill("correct horse");
      await main.getByRole("button", { name: "Unseal", exact: true }).click();

      await expect(main.locator(".badge-success").getByText("Unsealed")).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("an empty passphrase keeps Unseal disabled", async ({ page }) => {
      await openSealManager(page);
      await expect(page.locator("main").getByRole("button", { name: "Unseal", exact: true })).toBeDisabled();
    });

    test("a wrong passphrase shows a clean error and allows a retry", async ({ page }) => {
      await openSealManager(page);
      const main = page.locator("main");

      await injectError(page, "SealBinding.Unseal", "decryption failed: invalid credentials");
      await main.getByPlaceholder("Enter passphrase").fill("wrong");
      await main.getByRole("button", { name: "Unseal", exact: true }).click();
      await expect(page.locator('[role="alert"]').filter({ hasText: "Incorrect passphrase" })).toBeVisible({ timeout: LOAD_TIMEOUT });
      await clearError(page, "SealBinding.Unseal");

      await main.getByPlaceholder("Enter passphrase").fill("correct horse");
      await main.getByRole("button", { name: "Unseal", exact: true }).click();
      await expect(main.locator(".badge-success").getByText("Unsealed")).toBeVisible({ timeout: LOAD_TIMEOUT });
    });
  });

  test.describe("Shamir barrier", () => {
    test.use({ mockState: sealedState("shamir") });

    test("the page points to the holder quorum and takes no share", async ({ page }) => {
      await openSealManager(page);
      const main = page.locator("main");

      const note = page.getByTestId("unseal-quorum-note");
      await expect(note).toContainText("2 of 3 must submit");
      await expect(note).toContainText("qrdb barrier quorum submit --op unseal");
      await expect(main.getByPlaceholder("Enter passphrase")).toHaveCount(0);
      await expect(main.getByRole("button", { name: "Submit Share" })).toHaveCount(0);
    });
  });
});
