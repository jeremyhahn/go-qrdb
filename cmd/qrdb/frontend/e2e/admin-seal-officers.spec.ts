// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * admin-seal-officers.spec.ts
 *
 * Mocked-tier Playwright tests for share-holder administration in:
 *   1. ConnectionManager.svelte — the embedded-server creation wizard,
 *      which drives client.ceremony.initialize for every barrier mode
 *      (none, passphrase, shamir): `qrdb init`/ceremony.initialize is the
 *      ONLY way to bring up a ceremony-backed system barrier --
 *      BarrierInitialize and BarrierInitializeShamir both refuse it
 *      outright -- so every Shamir holder is collected locally
 *      (CeremonyOfficerForms) before the server is even created, and the
 *      bootstrap admin's credential (StepAdminCredential) is resolved in
 *      the same call.
 *   2. SealManager.svelte — the Share Holders panel of a running Shamir
 *      barrier: listing, enrolment, rekey and revocation (POST-init holder
 *      administration, via OfficerBinding -- unaffected by the above: a
 *      holder added after the ceremony has already run is a different,
 *      still-valid call).
 *
 * ConnectionManager's calls are served by the Wails mock's CeremonyBinding,
 * which mirrors the Go binding (including that a generated admin key is
 * never returned here, only adminKeyPath). SealManager's calls are served
 * by OfficerBinding (RegisterHolder, ListHolders, StartRekey, RevokeHolder).
 * No share and no holder private key is ever in the page: holders claim and
 * submit from the CLI or the desktop app.
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
const ADMIN_CSR = "-----BEGIN CERTIFICATE REQUEST-----\nmock-admin\n-----END CERTIFICATE REQUEST-----";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Walks the embedded-server wizard from the dashboard to the barrier step,
 *  with the given name and barrier mode selected. */
async function toEmbeddedBarrierStep(page: Page, name: string, barrierLabel: "None" | "Passphrase" | "Shamir"): Promise<void> {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "No servers configured", exact: true }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.getByRole("button", { name: "Create Embedded Server" }).click();
  await expect(page.getByRole("heading", { name: "Create Embedded Server" })).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.locator("#emb-name").fill(name);
  await page.getByRole("button", { name: "Next" }).click();
  await page.locator("button").filter({ hasText: "Standalone" }).first().click();
  await page.getByRole("button", { name: "Next" }).click();
  await page.locator("button").filter({ hasText: barrierLabel }).first().click();
}

/** Fills the admin-credential step with the CSR path and advances. */
async function fillAdminCSRAndNext(page: Page): Promise<void> {
  await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByTestId("admin-mode-csr").check();
  await page.getByTestId("admin-csr-pem").fill(ADMIN_CSR);
  await page.getByTestId("admin-next-btn").click();
}

/**
 * Walks the full embedded-server wizard to "Create Server" with a passphrase
 * barrier and a CSR admin credential, then runs the apply.
 */
async function createEmbeddedPassphraseServer(page: Page, name = "Passphrase Server"): Promise<void> {
  await toEmbeddedBarrierStep(page, name, "Passphrase");
  await page.locator("#emb-passphrase").fill("S3cur3P@ss!");
  await page.locator("#emb-passphrase-confirm").fill("S3cur3P@ss!");
  await page.getByRole("button", { name: "Next" }).click();
  await fillAdminCSRAndNext(page);
  await expect(page.getByText("Review your server configuration before creating it.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Create Server" }).click();
}

/**
 * Walks the embedded-server wizard to the officers step with a Shamir
 * barrier (the 3-of-5 defaults).
 */
async function toEmbeddedShamirOfficersStep(page: Page, name = "Shamir Server"): Promise<void> {
  await toEmbeddedBarrierStep(page, name, "Shamir");
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByText("Each holder — a Security Officer or a key custodian")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Fills every officer form from..to-1 on the (already-visible) officers step. */
async function fillOfficers(page: Page, to: number, from = 0): Promise<void> {
  for (let i = from; i < to; i++) {
    await page.getByTestId(`officer-username-${i}`).fill(`holder-${i}`);
    await page.getByTestId(`officer-csr-${i}`).fill(CSR);
  }
}

/**
 * Walks the full embedded-server wizard to "Create Server" with a Shamir
 * barrier, every holder collected on the officers step, and a CSR admin
 * credential, then runs the apply.
 */
async function createEmbeddedShamirServer(page: Page, name = "Shamir Server"): Promise<void> {
  await toEmbeddedShamirOfficersStep(page, name);
  await fillOfficers(page, 5);
  await page.getByTestId("embedded-officers-next-btn").click();
  await fillAdminCSRAndNext(page);
  await expect(page.getByText("Review your server configuration before creating it.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Create Server" }).click();
}

async function navigateToSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Seal Manager").click();
  await expect(page.locator(".breadcrumbs").getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// 1. ConnectionManager — embedded server creation via ceremony.initialize
// ---------------------------------------------------------------------------

test.describe("ConnectionManager — embedded server creation (passphrase)", () => {
  test.use({ mockState: defaultMockState });

  test("creates a passphrase-barrier server and shows the issued admin certificate", async ({ page }) => {
    await createEmbeddedPassphraseServer(page);

    await expect(page.getByTestId("embedded-ceremony-result")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("admin-cert-pem-issued")).toHaveValue(/BEGIN CERTIFICATE/);
    await expect(page.getByTestId("admin-key-saved-notice")).toHaveCount(0);
    // No Shamir-only UI for a passphrase barrier.
    await expect(page.getByText(/Shamir scheme/)).toHaveCount(0);
  });

  test("Continue to Server transitions to the main app", async ({ page }) => {
    await createEmbeddedPassphraseServer(page);
    await expect(page.getByTestId("embedded-ceremony-result")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.getByTestId("embedded-apply-ack-btn").click();
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

test.describe("ConnectionManager — embedded server creation (shamir)", () => {
  test.use({ mockState: defaultMockState });

  test("the officers step collects holders locally; Next is disabled until every one is filled in", async ({ page }) => {
    await toEmbeddedShamirOfficersStep(page);

    const next = page.getByTestId("embedded-officers-next-btn");
    await expect(next).toBeDisabled();
    await expect(next).toContainText("0/5");

    await fillOfficers(page, 4);
    await expect(next).toBeDisabled();
    await expect(next).toContainText("4/5");

    await fillOfficers(page, 5, 4);
    await expect(next).toBeEnabled();
  });

  test("creates a shamir-barrier server, enrolling every holder in the one ceremony.initialize call", async ({ page }) => {
    await createEmbeddedShamirServer(page);

    await expect(page.getByTestId("embedded-ceremony-result")).toBeVisible({ timeout: LOAD_TIMEOUT });
    const alert = page.locator('[role="alert"]').filter({ hasText: "Shamir scheme" });
    await expect(alert).toContainText("3-of-5");
    await expect(alert).toContainText("qrdb barrier officer claim");
    await expect(alert).toContainText("No share is ever displayed here");

    // Every holder's certificate was issued in the same response -- no
    // separate per-holder registration call happened to produce them.
    for (let i = 0; i < 5; i++) {
      await expect(page.getByTestId(`holder-cert-holder-${i}`)).toHaveValue(/BEGIN CERTIFICATE/);
    }
  });

  test("a ceremony refusal is shown on the applying step, and no server is left connected", async ({ page }) => {
    await toEmbeddedShamirOfficersStep(page);
    await fillOfficers(page, 5);
    await page.getByTestId("embedded-officers-next-btn").click();
    await fillAdminCSRAndNext(page);
    await expect(page.getByText("Review your server configuration before creating it.")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await injectError(page, "CeremonyBinding.Initialize", "ceremony: duplicate username");
    await page.getByRole("button", { name: "Create Server" }).click();

    await expect(page.getByText("ceremony: duplicate username")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByRole("button", { name: "Retry" })).toBeVisible();
    await expect(page.getByTestId("embedded-ceremony-result")).toHaveCount(0);
    await clearError(page, "CeremonyBinding.Initialize");
  });

  test("Continue to Server transitions to the main app", async ({ page }) => {
    await createEmbeddedShamirServer(page);
    await expect(page.getByTestId("embedded-ceremony-result")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.getByTestId("embedded-apply-ack-btn").click();
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

test.describe("ConnectionManager — admin-generate path saves the key locally", () => {
  test.use({ mockState: defaultMockState });

  test("never shows the generated admin key on the page", async ({ page }) => {
    await toEmbeddedBarrierStep(page, "Generate Admin Server", "None");
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.getByTestId("admin-mode-generate").check();
    await expect(page.getByTestId("admin-generate-warning")).toContainText("dev/test/evaluation ONLY");
    await page.getByTestId("admin-next-btn").click();
    await expect(page.getByText("Review your server configuration before creating it.")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.getByRole("button", { name: "Create Server" }).click();

    await expect(page.getByTestId("embedded-ceremony-result")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("admin-key-saved-notice")).toBeVisible();
    await expect(page.getByTestId("admin-key-path")).toContainText("admin-key");
    await expect(page.locator("body")).not.toContainText("PRIVATE KEY");
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
