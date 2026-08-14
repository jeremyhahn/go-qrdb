// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  injectError,
  clearError,
  defaultMockState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

/**
 * In desktop mode (Wails), the sealed state is surfaced through the
 * Seal Manager admin section rather than a standalone unseal screen.
 * The server tree shows the "sealed" indicator and clicking Seal Manager
 * presents the unseal form.
 */
test.describe("Unseal flow", () => {
  test.describe("single-share threshold", () => {
    test.use({
      mockState: {
        ...defaultMockState,
        isFirstRun: false,
        configInitialized: true,
        sealStatus: {
          sealed: true,
          initialized: true,
          sharesRequired: 1,
          sharesProvided: 0,
          sharesTotal: 1,
        },
        unsealProgress: {
          sealed: true,
          sharesProvided: 0,
          sharesRequired: 1,
        },
      },
    });

    test("successful single-share unseal via Seal Manager", async ({ page }) => {
      await page.goto("/");

      // Wait for MainApp to load.
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Navigate to Seal Manager to perform unseal.
      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      // Verify seal status shows as Sealed.
      await expect(
        main.locator(".stat-title", { hasText: "Seal Status" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Verify progress shows 0 / 1.
      await expect(
        main.locator(".font-mono").filter({ hasText: "0 / 1" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Fill share and submit.
      await main.locator("input[type='text'], input[type='password']").last().fill("test-share-1");
      await main.getByRole("button", { name: "Submit Share" }).click();

      // Progress should update after submit.
      await expect(
        main.locator(".badge-success").getByText("Unsealed"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });
  });

  test.describe("multi-share threshold (2-of-3)", () => {
    test.use({
      mockState: {
        ...defaultMockState,
        isFirstRun: false,
        configInitialized: true,
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

    test("multi-share unseal completes after threshold reached", async ({
      page,
    }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      // Verify progress shows 0 / 2.
      await expect(
        main.locator(".font-mono").filter({ hasText: "0 / 2" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const shareInput = main.locator("input[type='text'], input[type='password']").last();

      // Submit first share.
      await shareInput.fill("share-1");
      await main.getByRole("button", { name: "Submit Share" }).click();

      // Verify progress updates to 1 / 2.
      await expect(
        main.locator(".font-mono").filter({ hasText: "1 / 2" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Submit second share to reach threshold.
      await shareInput.fill("share-2");
      await main.getByRole("button", { name: "Submit Share" }).click();

      // Server is now unsealed.
      await expect(
        main.locator(".badge-success").getByText("Unsealed"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("invalid share displays error and allows retry", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      // Inject an error for the Unseal binding.
      await injectError(page, "SealBinding.Unseal", "invalid share format");

      const shareInput = main.locator("input[type='text'], input[type='password']").last();

      // Submit a bad share.
      await shareInput.fill("bad-share");
      await main.getByRole("button", { name: "Submit Share" }).click();

      // Verify error toast appears with the expected message.
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("invalid share format");

      // Clear the injected error.
      await clearError(page, "SealBinding.Unseal");

      // Retry with a valid share.
      await shareInput.fill("valid-share-1");
      await main.getByRole("button", { name: "Submit Share" }).click();

      // Progress updates to show 1 / 2 after successful share.
      await expect(
        main.locator(".font-mono").filter({ hasText: "1 / 2" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });
  });
});
