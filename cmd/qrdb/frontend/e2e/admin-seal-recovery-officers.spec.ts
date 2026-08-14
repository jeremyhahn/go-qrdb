// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * admin-seal-recovery-officers.spec.ts
 *
 * Mocked-tier Playwright tests for the Recovery Officers panel in
 * SealManager.svelte. Recovery officers hold shares of a distinct recovery
 * key set, separate from the unseal officer set covered by
 * admin-seal-officers.spec.ts. The SDK exposes registration
 * (registerRecoveryOfficer) and the begin/complete challenge-response claim
 * flow (recoveryClaimBegin / recoveryClaimComplete) but no list/status
 * query — so this panel (unlike the Security Officers panel) has no
 * pre-populated table.
 *
 * All API calls are served by the Wails mock (OfficerBinding) — no live
 * daemon needed.
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

test.describe("SealManager — Recovery Officers panel (shamir)", () => {
  test.use({ mockState: shamirSealState });

  test("shows Recovery Officers panel heading for shamir barriers", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(page.getByRole("heading", { name: "Recovery Officers" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Register Recovery Officer button toggles the registration form", async ({ page }) => {
    await navigateToSealManager(page);

    await expect(page.getByTestId("ro-username-input")).not.toBeVisible();

    await page.getByTestId("toggle-register-recovery-officer-btn").click();
    await expect(page.getByTestId("ro-username-input")).toBeVisible();
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

    await injectError(page, "OfficerBinding.RegisterRecoveryOfficer", "username already taken");

    await page.getByTestId("toggle-register-recovery-officer-btn").click();
    await page.getByTestId("ro-username-input").fill("existing-recovery-officer");
    await page.getByTestId("ro-csr-input").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock\n-----END CERTIFICATE REQUEST-----",
    );
    await page.getByTestId("ro-register-btn").click();

    await expect(page.getByTestId("ro-reg-error")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("ro-reg-error")).toContainText("username already taken");

    await clearError(page, "OfficerBinding.RegisterRecoveryOfficer");
  });

  test("Claim Share toggles the claim challenge-response tester", async ({ page }) => {
    await navigateToSealManager(page);

    await expect(page.getByTestId("ro-claim-username-input")).not.toBeVisible();

    await page.getByTestId("toggle-recovery-claim-btn").click();
    await expect(page.getByTestId("ro-claim-username-input")).toBeVisible();
    await expect(page.getByTestId("ro-claim-begin-btn")).toBeVisible();

    await page.getByTestId("toggle-recovery-claim-btn").click();
    await expect(page.getByTestId("ro-claim-username-input")).not.toBeVisible();
  });

  test("begin claim shows the challenge nonce and signature input", async ({ page }) => {
    await navigateToSealManager(page);

    await page.getByTestId("toggle-recovery-claim-btn").click();
    await page.getByTestId("ro-claim-username-input").fill("recovery-alice");
    await page.getByTestId("ro-claim-begin-btn").click();

    await expect(page.getByText("Challenge Nonce (hex)")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("ro-claim-signature-input")).toBeVisible();
    await expect(page.getByTestId("ro-claim-complete-btn")).toBeVisible();
  });

  test("complete claim shows the one-time share bytes", async ({ page }) => {
    await navigateToSealManager(page);

    await page.getByTestId("toggle-recovery-claim-btn").click();
    await page.getByTestId("ro-claim-username-input").fill("recovery-alice");
    await page.getByTestId("ro-claim-begin-btn").click();
    await expect(page.getByTestId("ro-claim-signature-input")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.getByTestId("ro-claim-signature-input").fill("bW9jay1zaWduYXR1cmU=");
    await page.getByTestId("ro-claim-complete-btn").click();

    await expect(page.getByTestId("ro-claim-share-output")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("ro-copy-share-btn")).toBeVisible();
    await expect(page.getByText(/Share #\d+ \(returned exactly once\)/)).toBeVisible();
  });

  test("claim begin error shows inline alert", async ({ page }) => {
    await navigateToSealManager(page);

    await injectError(page, "OfficerBinding.RecoveryClaimBegin", "officer not registered");

    await page.getByTestId("toggle-recovery-claim-btn").click();
    await page.getByTestId("ro-claim-username-input").fill("unknown-officer");
    await page.getByTestId("ro-claim-begin-btn").click();

    await expect(page.getByTestId("ro-claim-error")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("ro-claim-error")).toContainText("officer not registered");

    await clearError(page, "OfficerBinding.RecoveryClaimBegin");
  });

  test("claim complete error shows inline alert", async ({ page }) => {
    await navigateToSealManager(page);

    await page.getByTestId("toggle-recovery-claim-btn").click();
    await page.getByTestId("ro-claim-username-input").fill("recovery-bob");
    await page.getByTestId("ro-claim-begin-btn").click();
    await expect(page.getByTestId("ro-claim-signature-input")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await injectError(page, "OfficerBinding.RecoveryClaimComplete", "signature verification failed");

    await page.getByTestId("ro-claim-signature-input").fill("aW52YWxpZA==");
    await page.getByTestId("ro-claim-complete-btn").click();

    await expect(page.getByTestId("ro-claim-error")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("ro-claim-error")).toContainText("signature verification failed");

    await clearError(page, "OfficerBinding.RecoveryClaimComplete");
  });
});

test.describe("SealManager — Recovery Officers panel absent for passphrase", () => {
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

  test("no Recovery Officers panel when strategy is not shamir", async ({ page }) => {
    await navigateToSealManager(page);

    await expect(page.getByRole("heading", { name: "Recovery Officers" })).not.toBeVisible();
  });
});
