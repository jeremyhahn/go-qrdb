// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Shared step helpers for driving the ConnectionManager embedded-server
 * creation wizard (name → mode → [discovery] → barrier → [officers] →
 * admin → summary → applying) and waiting for the resulting main app shell.
 * Every spec that walks the wizard end-to-end imports from here instead of
 * re-declaring these steps locally.
 */

import { expect, type Page } from "@playwright/test";

const STEP_TIMEOUT = 10_000;

/**
 * Waits for the Add Server modal to be visible. On first launch with no
 * profiles, the modal auto-opens. The ConnectionManager renders inside
 * the modal with its "No servers configured" card heading.
 */
export async function waitForConnectionManager(page: Page): Promise<void> {
  await expect(
    page.getByRole("heading", { name: "No servers configured", exact: true }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Opens the embedded server creation wizard by clicking
 * "Create Embedded Server" from within the Connection Manager modal.
 */
export async function openEmbeddedWizard(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Create Embedded Server" }).click();
  await expect(
    page.getByRole("heading", { name: "Create Embedded Server" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Fills the Name step and advances to the Mode step.
 * Clears the default value and types the provided name.
 */
export async function stepName(page: Page, name = "Test Server"): Promise<void> {
  await expect(page.locator("#emb-name")).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.locator("#emb-name").fill(name);
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Selects a server mode card by label ("Standalone" or "Raft Cluster") and
 * advances to the next step.
 */
export async function stepMode(
  page: Page,
  mode: "Standalone" | "Raft Cluster",
): Promise<void> {
  await expect(
    page.locator("button").filter({ hasText: mode }).first(),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.locator("button").filter({ hasText: mode }).first().click();
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Configures the Discovery step for multicast and advances.
 * Optionally fills in a multicast secret.
 */
export async function stepDiscoveryMulticast(
  page: Page,
  secret?: string,
): Promise<void> {
  // Multicast is the default selection; the input is already visible.
  await expect(
    page.locator("#emb-multicast-secret"),
  ).toBeVisible({ timeout: STEP_TIMEOUT });

  if (secret !== undefined) {
    await page.locator("#emb-multicast-secret").fill(secret);
  }

  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Selects a barrier mode card by label ("None", "Passphrase", or
 * "Shamir (M-of-N)") and advances to the next step. When mode is
 * "Passphrase", the caller must supply a matching passphrase string.
 */
export async function stepBarrier(
  page: Page,
  mode: "None" | "Passphrase" | "Shamir (M-of-N)",
  passphrase?: string,
): Promise<void> {
  await expect(
    page.locator("button").filter({ hasText: mode }).first(),
  ).toBeVisible({ timeout: STEP_TIMEOUT });

  await page.locator("button").filter({ hasText: mode }).first().click();

  if (mode === "Passphrase" && passphrase !== undefined) {
    await page.locator("#emb-passphrase").fill(passphrase);
    await page.locator("#emb-passphrase-confirm").fill(passphrase);
  }

  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Resolves the Admin Credential step by selecting "Generate" (the only mode
 * needing no input) and advancing. Every non-Shamir barrier selection lands
 * here next, since ceremony.initialize always resolves a bootstrap admin
 * credential before Summary.
 */
export async function stepAdminCredential(page: Page): Promise<void> {
  await expect(
    page.getByRole("heading", { name: "Bootstrap Admin Credential" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByTestId("admin-mode-generate").click();
  await page.getByTestId("admin-next-btn").click();
}

/** Waits for the Summary step. */
export async function waitForSummary(page: Page): Promise<void> {
  await expect(
    page.getByText("Review your server configuration before creating it."),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/** Waits for the main app shell to load (the breadcrumb reads "Dashboard"). */
export async function waitForDashboard(page: Page): Promise<void> {
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Acknowledges the ceremony.initialize result panel (the issued admin
 * certificate, and for Shamir every holder's certificate) by clicking
 * "Continue to Server". The panel always appears after a successful apply --
 * ceremony.initialize issues the bootstrap admin's credential together with
 * the barrier for every mode, including "none" -- and the credential
 * material is never auto-dismissed, so the wizard waits here for an explicit
 * click before connecting.
 */
export async function acknowledgeCeremonyResult(page: Page): Promise<void> {
  await expect(
    page.getByTestId("embedded-ceremony-result"),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByTestId("embedded-apply-ack-btn").click();
}

/**
 * Clicks "Create Server" on the Summary step, acknowledges the
 * ceremony-result panel, and waits for the main app to load.
 */
export async function createServer(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Create Server" }).click();
  await acknowledgeCeremonyResult(page);
  await waitForDashboard(page);
}

/**
 * Walks the embedded wizard end-to-end with a Standalone mode, "None"
 * barrier and the dev/test "Generate" admin credential, then creates the
 * server and waits for the Dashboard. Assumes the Connection Manager modal
 * is already open; use openEmbeddedWizard (or waitForConnectionManager, for
 * first launch) first.
 */
export async function completeEmbeddedWizardNoBarrier(
  page: Page,
  name = "Test Local Server",
): Promise<void> {
  await openEmbeddedWizard(page);
  await stepName(page, name);
  await stepMode(page, "Standalone");
  await stepBarrier(page, "None");
  await stepAdminCredential(page);
  await waitForSummary(page);
  await createServer(page);
}
