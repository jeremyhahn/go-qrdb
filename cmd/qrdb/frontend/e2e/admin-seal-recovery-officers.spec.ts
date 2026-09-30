// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * admin-seal-recovery-officers.spec.ts
 *
 * Mocked-tier Playwright tests for the Recovery Holders panel in
 * SealManager.svelte. Recovery holders hold shares of a distinct recovery
 * key, separate from the unseal holders covered by
 * admin-seal-officers.spec.ts. The panel enrols them; they claim their
 * envelopes from the CLI or the desktop app, never from the page, so the
 * panel has no claim form.
 *
 * All API calls are served by the Wails mock (OfficerBinding.RegisterHolder
 * with a recovery-domain scope) — no live daemon needed.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  shamirSealState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

async function navigateToSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Seal Manager").click();
  await expect(page.locator(".breadcrumbs").getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("SealManager — Recovery Holders panel (shamir)", () => {
  test.use({ mockState: shamirSealState });

  test("shows the Recovery Holders panel for a Shamir barrier, with no claim form", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(page.getByRole("heading", { name: "Recovery Holders" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    // Holders claim with their own keys, outside the page.
    await expect(page.getByTestId("toggle-recovery-claim-btn")).toHaveCount(0);
  });

  test("Enrol Recovery Holder toggles the enrolment form", async ({ page }) => {
    await navigateToSealManager(page);

    await expect(page.getByTestId("ro-username-input")).not.toBeVisible();

    await page.getByTestId("toggle-register-recovery-officer-btn").click();
    await expect(page.getByTestId("ro-username-input")).toBeVisible();
    await expect(page.getByTestId("ro-role-select")).toBeVisible();
    await expect(page.getByTestId("ro-csr-input")).toBeVisible();
    await expect(page.getByTestId("ro-register-btn")).toBeVisible();

    await page.getByTestId("toggle-register-recovery-officer-btn").click();
    await expect(page.getByTestId("ro-username-input")).not.toBeVisible();
  });

  test("Register button disabled when form is empty", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-register-recovery-officer-btn").click();
    await expect(page.getByTestId("ro-register-btn")).toBeDisabled();
  });

  test("successful registration shows certificate output", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-register-recovery-officer-btn").click();
    await page.getByTestId("ro-username-input").fill("recovery-charlie");
    await page.getByTestId("ro-csr-input").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock\n-----END CERTIFICATE REQUEST-----",
    );
    await page.getByTestId("ro-register-btn").click();

    await expect(page.getByTestId("ro-reg-cert-output")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("ro-copy-cert-btn")).toBeVisible();
    await expect(page.getByTestId("ro-download-cert-btn")).toBeVisible();
  });

  test("registration error shows inline alert", async ({ page }) => {
    await navigateToSealManager(page);

    await injectError(page, "OfficerBinding.RegisterHolder", "username already taken");

    await page.getByTestId("toggle-register-recovery-officer-btn").click();
    await page.getByTestId("ro-username-input").fill("existing-recovery-officer");
    await page.getByTestId("ro-csr-input").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock\n-----END CERTIFICATE REQUEST-----",
    );
    await page.getByTestId("ro-register-btn").click();

    await expect(page.getByTestId("ro-reg-error")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("ro-reg-error")).toContainText("username already taken");

    await clearError(page, "OfficerBinding.RegisterHolder");
  });
});

test.describe("SealManager — Recovery Holders panel absent for passphrase", () => {
  test.use({
    mockState: {
      ...secondLaunchEmbeddedState,
      sealStatus: {
        sealed: false,
        initialized: true,
        sharesRequired: 0,
        sharesProvided: 0,
        sharesTotal: 0,
      },
    },
  });

  test("no Recovery Holders panel when strategy is not shamir", async ({ page }) => {
    await navigateToSealManager(page);

    await expect(page.getByRole("heading", { name: "Recovery Holders" })).not.toBeVisible();
  });
});
