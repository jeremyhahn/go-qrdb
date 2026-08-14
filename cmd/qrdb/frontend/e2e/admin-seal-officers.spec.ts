// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * admin-seal-officers.spec.ts
 *
 * Mocked-tier Playwright tests for the Security Officers UI in both:
 *   1. ConnectionManager.svelte (embedded shamir creation: the share-free
 *      officer enrollment panel shown after barrier initialization).
 *   2. SealManager.svelte (Security Officers panel for the running system).
 *
 * Desktop note: ServerInitWizard.svelte (the officer-registration step
 * during initialization) is the WEB-mode surface — App.svelte only routes
 * to it when running outside the Wails runtime — so it is intentionally
 * unreachable in this mocked desktop tier. Its officers step is covered by
 * the real-backend DOM tier (e2e/dom/barrier-shamir.spec.ts). On desktop,
 * Shamir initialization happens in the ConnectionManager embedded wizard,
 * which never displays share bytes (INV-1): officers are registered via the
 * SealManager panel and claim their share through /officer/claim.
 *
 * All API calls are served by the Wails mock (OfficerBinding/SealBinding) —
 * no live daemon needed. Tests validate:
 *   - Embedded shamir creation lands on the share-free enrollment panel
 *     (M-of-N notice, /officer/claim routing, no share bytes rendered).
 *   - Root token display with copy affordance.
 *   - "Continue to Server" transitions into the main app.
 *   - SealManager: Security Officers panel renders for shamir barriers.
 *   - SealManager: No Security Officers panel for non-shamir barriers.
 *   - SealManager: Register form appears when "Register Officer" is toggled.
 *   - SealManager: Register success shows certificate output and toasts.
 *   - SealManager: Register error shows inline error alert.
 *   - SealManager: Refresh button reloads officer list.
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

// ---------------------------------------------------------------------------
// Helpers — ConnectionManager embedded shamir creation
// ---------------------------------------------------------------------------

/**
 * Walks the ConnectionManager embedded-server wizard from first run through
 * "Create Server" with a Shamir M-of-N barrier (default 3-of-5), landing on
 * the share-free officer enrollment panel.
 */
async function createEmbeddedShamirServer(page: Page): Promise<void> {
  await page.goto("/");
  // First-run: connection manager shows first.
  await expect(
    page.getByRole("heading", { name: "No servers configured", exact: true }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Open embedded wizard.
  await page.getByRole("button", { name: "Create Embedded Server" }).click();
  await expect(
    page.getByRole("heading", { name: "Create Embedded Server" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Name step.
  await page.locator("#emb-name").fill("Shamir Server");
  await page.getByRole("button", { name: "Next" }).click();

  // Mode step — Standalone.
  await page.locator("button").filter({ hasText: "Standalone" }).first().click();
  await page.getByRole("button", { name: "Next" }).click();

  // Barrier step — Shamir (M-of-N), keeping the 3-of-5 defaults.
  await page.locator("button").filter({ hasText: "Shamir" }).first().click();
  await page.getByRole("button", { name: "Next" }).click();

  // Summary step.
  await expect(
    page.getByText("Review your server configuration before creating it."),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.getByRole("button", { name: "Create Server" }).click();
}

// ---------------------------------------------------------------------------
// State variants
// ---------------------------------------------------------------------------

/** Mock state: shamir initialized and unsealed with 2 pre-registered officers. */
const shamirRunningState = {
  ...shamirSealState,
};

// ---------------------------------------------------------------------------
// 1. ConnectionManager — embedded shamir creation (share-free, INV-1)
// ---------------------------------------------------------------------------

test.describe("ConnectionManager — embedded shamir officer enrollment panel", () => {
  test.use({ mockState: defaultMockState });

  test("shamir creation shows share-free officer enrollment panel", async ({ page }) => {
    await createEmbeddedShamirServer(page);

    // Enrollment panel: M-of-N notice routing officers to the claim flow.
    const alert = page.locator('[role="alert"]').filter({ hasText: "Shamir scheme" });
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(alert).toContainText("3-of-5");
    await expect(alert).toContainText("/officer/claim");
    await expect(alert).toContainText("Share bytes are never displayed here");
  });

  test("share bytes from the init response are never rendered", async ({ page }) => {
    await createEmbeddedShamirServer(page);

    await expect(
      page.getByRole("button", { name: "Continue to Server" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The mock init response carries share strings (share-1-mock ...); the
    // INV-1 share-free contract requires the UI to discard them.
    await expect(page.getByText("share-1-mock")).toHaveCount(0);
  });

  test("no root token is displayed at initialization", async ({ page }) => {
    await createEmbeddedShamirServer(page);

    await expect(
      page.getByRole("button", { name: "Continue to Server" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // INV-1: root-token generation requires a post-claim share quorum, so
    // the init response carries no token and the Root Token panel must not
    // render.
    await expect(page.getByText("Root Token")).toHaveCount(0);
    await expect(page.getByTitle("Copy root token")).toHaveCount(0);
  });

  test("Continue to Server transitions to the main app", async ({ page }) => {
    await createEmbeddedShamirServer(page);

    await page.getByRole("button", { name: "Continue to Server" }).click();

    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// 2. SealManager — Security Officers panel
// ---------------------------------------------------------------------------

test.describe("SealManager — Security Officers panel (shamir)", () => {
  test.use({ mockState: shamirRunningState });

  async function navigateToSealManager(page: Page): Promise<void> {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.locator("aside").getByText("Seal Manager").click();
    await expect(
      page.locator(".breadcrumbs").getByText("Seal Manager"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  }

  test("shows Security Officers panel heading for shamir barriers", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(
      page.getByRole("heading", { name: "Security Officers" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("lists pre-seeded officers in the table", async ({ page }) => {
    await navigateToSealManager(page);
    await expect(page.getByTestId("seal-manager-officers-table")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("alice")).toBeVisible();
    await expect(page.getByText("bob")).toBeVisible();
  });

  test("shows Claimed badge for officer with claimedAt set", async ({ page }) => {
    await navigateToSealManager(page);
    // Alice has claimedAt set in shamirSealState.
    const aliceRow = page.locator("tr").filter({ hasText: "alice" });
    await expect(aliceRow.getByText("Claimed")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shows Pending badge for officer without claimedAt", async ({ page }) => {
    await navigateToSealManager(page);
    // Bob has sharePending=true and claimedAt="" in shamirSealState.
    const bobRow = page.locator("tr").filter({ hasText: "bob" });
    await expect(bobRow.getByText("Pending")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("Register Officer button toggles the registration form", async ({ page }) => {
    await navigateToSealManager(page);

    // Form should not be visible initially.
    await expect(page.getByTestId("so-username-input")).not.toBeVisible();

    // Click to open.
    await page.getByTestId("toggle-register-officer-btn").click();
    await expect(page.getByTestId("so-username-input")).toBeVisible();
    await expect(page.getByTestId("so-csr-input")).toBeVisible();
    await expect(page.getByTestId("so-register-btn")).toBeVisible();

    // Click to close.
    await page.getByTestId("toggle-register-officer-btn").click();
    await expect(page.getByTestId("so-username-input")).not.toBeVisible();
  });

  test("Register button disabled when form is empty", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-register-officer-btn").click();
    await expect(page.getByTestId("so-register-btn")).toBeDisabled();
  });

  test("successful registration shows certificate output", async ({ page }) => {
    await navigateToSealManager(page);
    await page.getByTestId("toggle-register-officer-btn").click();
    await page.getByTestId("so-username-input").fill("charlie");
    await page.getByTestId("so-csr-input").fill("-----BEGIN CERTIFICATE REQUEST-----\nmock\n-----END CERTIFICATE REQUEST-----");
    await page.getByTestId("so-register-btn").click();

    await expect(page.getByTestId("so-reg-cert-output")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("so-copy-cert-btn")).toBeVisible();
    await expect(page.getByTestId("so-download-cert-btn")).toBeVisible();
  });

  test("registration error shows inline alert", async ({ page }) => {
    await navigateToSealManager(page);

    await injectError(page, "OfficerBinding.RegisterOfficer", "username already taken");

    await page.getByTestId("toggle-register-officer-btn").click();
    await page.getByTestId("so-username-input").fill("existing-officer");
    await page.getByTestId("so-csr-input").fill("-----BEGIN CERTIFICATE REQUEST-----\nmock\n-----END CERTIFICATE REQUEST-----");
    await page.getByTestId("so-register-btn").click();

    await expect(page.getByTestId("so-reg-error")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("so-reg-error")).toContainText("username already taken");

    await clearError(page, "OfficerBinding.RegisterOfficer");
  });

  test("Refresh button reloads officer list", async ({ page }) => {
    await navigateToSealManager(page);
    // The refresh button must be present and clickable.
    await expect(page.getByTestId("refresh-officers-btn")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.getByTestId("refresh-officers-btn").click();
    // After refresh the table should still show the officers.
    await expect(page.getByTestId("seal-manager-officers-table")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

test.describe("SealManager — Security Officers panel absent for passphrase", () => {
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

  test("no Security Officers panel when strategy is not shamir", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.locator("aside").getByText("Seal Manager").click();
    await expect(
      page.locator(".breadcrumbs").getByText("Seal Manager"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(
      page.getByRole("heading", { name: "Security Officers" }),
    ).not.toBeVisible();
  });
});
