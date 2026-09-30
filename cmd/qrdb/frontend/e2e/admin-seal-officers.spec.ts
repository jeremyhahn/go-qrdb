// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * admin-seal-officers.spec.ts
 *
 * Mocked-tier Playwright tests for share-holder administration in:
 *   1. ConnectionManager.svelte — the embedded-server wizard's Shamir mode,
 *      which pauses after starting the server so the operator enrols exactly
 *      the configured number of holders before the barrier is initialized.
 *   2. SealManager.svelte — the Share Holders panel of a running Shamir
 *      barrier: listing, enrolment, rekey and revocation.
 *
 * All API calls are served by the Wails mock, which mirrors the Go
 * OfficerBinding (RegisterHolder, ListHolders, StartRekey, RevokeHolder) and
 * refuses a Shamir initialization whose share count differs from the enrolled
 * holders, as the server does. No share and no holder private key is ever in
 * the page: holders claim and submit from the CLI or the desktop app.
 */

import {
  test,
  expect,
  defaultMockState,
  secondLaunchEmbeddedState,
  shamirSealState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;
const CSR = "-----BEGIN CERTIFICATE REQUEST-----\nmock\n-----END CERTIFICATE REQUEST-----";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Walks the embedded-server wizard to "Create Server" with a Shamir barrier
 * (the 3-of-5 defaults). The apply then pauses on holder enrolment.
 */
async function createEmbeddedShamirServer(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "No servers configured", exact: true }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.getByRole("button", { name: "Create Embedded Server" }).click();
  await expect(page.getByRole("heading", { name: "Create Embedded Server" })).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.locator("#emb-name").fill("Shamir Server");
  await page.getByRole("button", { name: "Next" }).click();
  await page.locator("button").filter({ hasText: "Standalone" }).first().click();
  await page.getByRole("button", { name: "Next" }).click();
  await page.locator("button").filter({ hasText: "Shamir" }).first().click();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByText("Review your server configuration before creating it.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Create Server" }).click();

  await expect(page.getByTestId("embedded-holder-enrolment")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Enrols holders from..to-1 through the shared enrolment forms. */
async function enrolHolders(page: Page, to: number, from = 0): Promise<void> {
  for (let i = from; i < to; i++) {
    await page.getByTestId(`officer-username-${i}`).fill(`holder-${i}`);
    await page.getByTestId(`officer-csr-${i}`).fill(CSR);
    await page.getByTestId(`register-officer-btn-${i}`).click();
    await expect(page.getByTestId(`officer-cert-${i}`)).toBeVisible({ timeout: LOAD_TIMEOUT });
  }
}

async function navigateToSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Seal Manager").click();
  await expect(page.locator(".breadcrumbs").getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// 1. ConnectionManager — embedded Shamir creation
// ---------------------------------------------------------------------------

test.describe("ConnectionManager — embedded Shamir holder enrolment", () => {
  test.use({ mockState: defaultMockState });

  test("the apply pauses for enrolment with Initialize disabled", async ({ page }) => {
    await createEmbeddedShamirServer(page);

    const initialize = page.getByTestId("embedded-initialize-btn");
    await expect(initialize).toBeDisabled();
    await expect(initialize).toContainText("0/5");
    await expect(page.getByTestId("officer-registration-count")).toHaveText("0 / 5");
  });

  test("Initialize stays disabled until every holder is enrolled", async ({ page }) => {
    await createEmbeddedShamirServer(page);

    await enrolHolders(page, 4);
    await expect(page.getByTestId("embedded-initialize-btn")).toBeDisabled();

    await enrolHolders(page, 5, 4);
    await expect(page.getByTestId("embedded-initialize-btn")).toBeEnabled();
  });

  test("an enrolment error shows on its form and enrolls nothing", async ({ page }) => {
    await createEmbeddedShamirServer(page);
    await injectError(page, "OfficerBinding.RegisterHolder", "holder already enrolled");

    await page.getByTestId("officer-username-0").fill("alice");
    await page.getByTestId("officer-csr-0").fill(CSR);
    await page.getByTestId("register-officer-btn-0").click();

    await expect(page.getByTestId("officer-error-0")).toContainText("holder already enrolled", { timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("officer-registration-count")).toHaveText("0 / 5");
    await clearError(page, "OfficerBinding.RegisterHolder");
  });

  test("initializing shows the share-free result, with no share and no root token", async ({ page }) => {
    await createEmbeddedShamirServer(page);
    await enrolHolders(page, 5);
    await page.getByTestId("embedded-initialize-btn").click();

    const alert = page.locator('[role="alert"]').filter({ hasText: "Shamir scheme" });
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("3-of-5");
    await expect(alert).toContainText("qrdb barrier officer claim");
    await expect(alert).toContainText("No share is ever displayed here");
    await expect(page.getByText("Root Token")).toHaveCount(0);
  });

  test("Continue to Server transitions to the main app", async ({ page }) => {
    await createEmbeddedShamirServer(page);
    await enrolHolders(page, 5);
    await page.getByTestId("embedded-initialize-btn").click();

    await page.getByRole("button", { name: "Continue to Server" }).click({ timeout: LOAD_TIMEOUT });
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// 2. SealManager — Share Holders panel
// ---------------------------------------------------------------------------

test.describe("SealManager — Share Holders panel (shamir)", () => {
  test.use({ mockState: shamirSealState });

  test("shows the Share Holders panel for a Shamir barrier", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(page.getByRole("heading", { name: "Share Holders" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("lists the holders with role, factors and claim state", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(page.getByTestId("seal-manager-officers-table")).toBeVisible({ timeout: LOAD_TIMEOUT });

    const alice = page.locator("tr").filter({ hasText: "alice" });
    await expect(alice).toContainText("so");
    await expect(alice.getByText("Claimed")).toBeVisible();

    // bob's claimed_at is Go's zero time: reported as not claimed, pending.
    const bob = page.locator("tr").filter({ hasText: "bob" });
    await expect(bob).toContainText("custodian");
    await expect(bob).toContainText("mtls, fido2");
    await expect(bob.getByText("Pending")).toBeVisible();
  });

  test("Enrol Holder toggles the enrolment form", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(page.getByTestId("so-username-input")).not.toBeVisible();

    await page.getByTestId("toggle-register-officer-btn").click();
    await expect(page.getByTestId("so-username-input")).toBeVisible();
    await expect(page.getByTestId("so-role-select")).toBeVisible();
    await expect(page.getByTestId("so-csr-input")).toBeVisible();
    await expect(page.getByTestId("so-register-btn")).toBeDisabled();

    await page.getByTestId("toggle-register-officer-btn").click();
    await expect(page.getByTestId("so-username-input")).not.toBeVisible();
  });

  test("enrolling a custodian shows the issued certificate and lists it", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-register-officer-btn").click();
    await page.getByTestId("so-username-input").fill("charlie");
    await page.getByTestId("so-role-select").selectOption("custodian");
    await page.getByTestId("so-csr-input").fill(CSR);
    await page.getByTestId("so-register-btn").click();

    await expect(page.getByTestId("so-reg-cert-output")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("so-copy-cert-btn")).toBeVisible();
    await expect(page.getByTestId("so-download-cert-btn")).toBeVisible();
    await expect(page.locator("tr").filter({ hasText: "charlie" })).toContainText("custodian", { timeout: LOAD_TIMEOUT });
  });

  test("an enrolment error shows an inline alert", async ({ page }) => {
    await navigateToSealManager(page);
    await injectError(page, "OfficerBinding.RegisterHolder", "username already taken");

    await page.getByTestId("toggle-register-officer-btn").click();
    await page.getByTestId("so-username-input").fill("existing");
    await page.getByTestId("so-csr-input").fill(CSR);
    await page.getByTestId("so-register-btn").click();

    await expect(page.getByTestId("so-reg-error")).toContainText("username already taken", { timeout: LOAD_TIMEOUT });
    await clearError(page, "OfficerBinding.RegisterHolder");
  });

  test("Refresh reloads the holders", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("refresh-officers-btn").click();
    await expect(page.getByTestId("seal-manager-officers-table")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("starting a rekey shows the round it opened", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-rekey-btn").click();
    await page.getByTestId("rekey-so-pin").fill("so-pin");
    await page.getByTestId("rekey-threshold").fill("2");
    await page.getByTestId("start-rekey-btn").click();

    const round = page.getByTestId("open-round");
    await expect(round).toContainText("rekey round on the unseal domain", { timeout: LOAD_TIMEOUT });
  });

  test("revoking a holder opens a round that removes it", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-rekey-btn").click();
    await page.getByTestId("revoke-username").fill("bob");
    await page.getByTestId("revoke-holder-btn").click();

    await expect(page.getByTestId("open-round")).toContainText("Removes bob", { timeout: LOAD_TIMEOUT });
  });

  test("revoking an unknown holder surfaces the error", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-rekey-btn").click();
    await page.getByTestId("revoke-username").fill("mallory");
    await page.getByTestId("revoke-holder-btn").click();

    await expect(page.locator('[role="alert"]').filter({ hasText: "not found" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("open-round")).toHaveCount(0);
  });
});

test.describe("SealManager — no Share Holders panel for a passphrase barrier", () => {
  test.use({
    mockState: {
      ...secondLaunchEmbeddedState,
      sealStatus: { sealed: false, initialized: true, strategy: "passphrase", sharesRequired: 0, sharesProvided: 0, sharesTotal: 0 },
    },
  });

  test("the panel is absent when the strategy is not shamir", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(page.getByRole("heading", { name: "Share Holders" })).not.toBeVisible();
  });
});
