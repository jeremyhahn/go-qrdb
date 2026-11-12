// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * barrier-passphrase.spec.ts (real-backend DOM tier)
 *
 * Drives the real Svelte UI against a live passphrase-sealed qrdbd to guard the
 * web-parity fixes:
 *   - A sealed-but-uninitialized server routes to the Server Initialization
 *     wizard (NOT a dead "Connection Error" screen).
 *   - Passphrase initialization completes through the wizard and opens the app.
 *   - Passphrase validation alert fully unmounts (no empty alert box) once both
 *     fields contain identical values (regression: stale {#if} on $derived).
 *   - The Seal Manager unseal panel uses passphrase copy (not Shamir shares).
 *   - A passphrase barrier can be sealed and then unsealed through the UI
 *     (regression guard: unseal used to POST the Shamir endpoint and 500).
 *   - A WRONG passphrase shows the friendly "Incorrect passphrase" message
 *     (NOT the raw "status 500"/"decryption failed" transport dump) and the
 *     barrier stays sealed; a correct passphrase then recovers.
 *   - The full-screen sealed gate (page reload while sealed) shows the
 *     passphrase-mode "Unseal" button, not the Shamir "Submit Share" label.
 */

import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const PASSPHRASE = "dom-passphrase-secret-1";
const SO_PIN = "dom-so-pin-1";
const USER_PIN = "dom-user-pin-1";

/**
 * Completes the wizard's admin step: the dev-only generated admin
 * credential plus the SO PIN and user PIN ceremony.initialize requires.
 */
async function completeAdminStep(page: Page): Promise<void> {
  await expect(
    page.getByRole("heading", { name: "Bootstrap Admin Credential" }),
  ).toBeVisible({ timeout: 10_000 });
  await page.getByTestId("admin-mode-generate").check();
  await page.getByTestId("so-pin-input").fill(SO_PIN);
  await page.getByTestId("so-pin-confirm-input").fill(SO_PIN);
  await page.getByTestId("user-pin-input").fill(USER_PIN);
  await page.getByTestId("user-pin-confirm-input").fill(USER_PIN);
  await page.getByTestId("admin-next-btn").click();
}

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "passphrase" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

test("a sealed, uninitialized server routes to the init wizard (not Connection Error)", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);

  // The init wizard — proves the 503-SEALED connect path routes correctly.
  await expect(
    page.getByRole("heading", { name: "Server Initialization" }),
  ).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Global Encryption Barrier")).toBeVisible();
  // The dead "Connection Error" screen must NOT be shown.
  await expect(page.getByText("Connection Error")).toHaveCount(0);
});

test("passphrase: init via wizard, then seal and unseal through Seal Manager", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);
  await expect(
    page.getByRole("heading", { name: "Server Initialization" }),
  ).toBeVisible({ timeout: 15_000 });

  // Passphrase mode is the default selection. Fill the passphrase first so
  // the mismatch validation fires, then complete the confirmation field.
  await page.getByPlaceholder("Enter a strong passphrase").fill(PASSPHRASE);
  // While only the first field is filled the mismatch error must be visible.
  await expect(page.getByText("Passphrases do not match.")).toBeVisible({
    timeout: 5_000,
  });

  // Fill the confirmation to create a match.
  await page.getByPlaceholder("Confirm passphrase").fill(PASSPHRASE);

  // Regression guard (Bug 1): after matching, the validation alert must fully
  // unmount — no empty .alert-error box and no stale error text must remain.
  await expect(page.getByText("Passphrases do not match.")).toHaveCount(0);
  await expect(page.locator(".alert.alert-error")).toHaveCount(0);

  await page.getByRole("button", { name: "Next" }).click();
  await completeAdminStep(page);

  // Wizard reaches completion (REST initialize() succeeded).
  await expect(
    page.getByRole("heading", { name: "Server Initialized" }),
  ).toBeVisible({ timeout: 20_000 });

  // The daemon resumes bootstrap after init; wait for full health before entering the app.
  await waitForHealthy(daemon!);
  await page.getByRole("button", { name: "Open Database" }).click();

  // App shell loads.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(
    page.getByRole("heading", { name: "Seal Manager" }),
  ).toBeVisible();
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // Seal the barrier.
  await page.getByRole("button", { name: "Seal", exact: true }).click();
  await page.getByRole("textbox", { name: "SO PIN", exact: true }).fill(SO_PIN);
  await page.getByRole("button", { name: "Confirm Seal" }).click();

  // The unseal panel must use PASSPHRASE copy — not Shamir shares.
  await expect(
    page.getByText(
      "Enter the barrier passphrase to unseal the encryption barrier.",
    ),
  ).toBeVisible();
  await expect(page.getByText(/Submit Shamir key shares/)).toHaveCount(0);
  await expect(page.getByText("Sealed").first()).toBeVisible();

  // Unseal with the passphrase (regression: this used to hit the Shamir endpoint).
  await page.getByPlaceholder("Enter passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();

  // Back to unsealed: the Seal action reappears and status reflects it.
  await expect(
    page.getByRole("button", { name: "Seal", exact: true }),
  ).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Unsealed").first()).toBeVisible();
});

test("passphrase: a wrong passphrase shows a friendly error and stays sealed, then recovers", async ({
  page,
}) => {
  // -------------------------------------------------------------------------
  // Step 1: Initialize via wizard and open the app.
  // -------------------------------------------------------------------------
  await page.goto(UI_BASE_URL);
  await expect(
    page.getByRole("heading", { name: "Server Initialization" }),
  ).toBeVisible({ timeout: 15_000 });

  await page.getByPlaceholder("Enter a strong passphrase").fill(PASSPHRASE);
  await page.getByPlaceholder("Confirm passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Next" }).click();
  await completeAdminStep(page);
  await expect(
    page.getByRole("heading", { name: "Server Initialized" }),
  ).toBeVisible({ timeout: 20_000 });

  await waitForHealthy(daemon!);
  await page.getByRole("button", { name: "Open Database" }).click();
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });

  // -------------------------------------------------------------------------
  // Step 2: Seal the barrier via Seal Manager.
  // -------------------------------------------------------------------------
  await page.getByText("Seal Manager").first().click();
  await expect(
    page.getByRole("heading", { name: "Seal Manager" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Seal", exact: true }).click();
  await page.getByRole("textbox", { name: "SO PIN", exact: true }).fill(SO_PIN);
  await page.getByRole("button", { name: "Confirm Seal" }).click();
  await expect(page.getByText("Sealed").first()).toBeVisible({
    timeout: 10_000,
  });

  // -------------------------------------------------------------------------
  // Step 3: Submit a WRONG passphrase. The server maps it to 403; the UI must
  // show the clean friendly message, NOT the raw transport dump, and the
  // barrier must remain sealed.
  // -------------------------------------------------------------------------
  await page
    .getByPlaceholder("Enter passphrase")
    .fill("totally-wrong-passphrase");
  await page.getByRole("button", { name: "Unseal", exact: true }).click();

  await expect(
    page.getByText("Incorrect passphrase. Please try again."),
  ).toBeVisible({ timeout: 10_000 });
  // The raw transport strings must NOT leak to the user.
  await expect(page.getByText(/status 500/i)).toHaveCount(0);
  await expect(page.getByText(/decryption failed/i)).toHaveCount(0);
  // Barrier stays sealed after a failed unseal.
  await expect(page.getByText("Sealed").first()).toBeVisible();

  // -------------------------------------------------------------------------
  // Step 4: Submit the CORRECT passphrase — the barrier recovers.
  // -------------------------------------------------------------------------
  await page.getByPlaceholder("Enter passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();

  await expect(
    page.getByRole("button", { name: "Seal", exact: true }),
  ).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Unsealed").first()).toBeVisible();
});

test("passphrase: full-screen sealed gate shows mode-aware Unseal button (Bug 3)", async ({
  page,
  request,
}) => {
  // -------------------------------------------------------------------------
  // Step 1: Initialize via wizard.
  // -------------------------------------------------------------------------
  await page.goto(UI_BASE_URL);
  await expect(
    page.getByRole("heading", { name: "Server Initialization" }),
  ).toBeVisible({ timeout: 15_000 });

  await page.getByPlaceholder("Enter a strong passphrase").fill(PASSPHRASE);
  await page.getByPlaceholder("Confirm passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Next" }).click();
  await completeAdminStep(page);
  await expect(
    page.getByRole("heading", { name: "Server Initialized" }),
  ).toBeVisible({ timeout: 20_000 });

  await waitForHealthy(daemon!);
  await page.getByRole("button", { name: "Open Database" }).click();
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });

  // -------------------------------------------------------------------------
  // Step 2: Seal the barrier via Seal Manager.
  // -------------------------------------------------------------------------
  await page.getByText("Seal Manager").first().click();
  await expect(
    page.getByRole("heading", { name: "Seal Manager" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Seal", exact: true }).click();
  await page.getByRole("textbox", { name: "SO PIN", exact: true }).fill(SO_PIN);
  await page.getByRole("button", { name: "Confirm Seal" }).click();
  await expect(page.getByText("Sealed").first()).toBeVisible({
    timeout: 10_000,
  });

  // -------------------------------------------------------------------------
  // Step 3: Reload the page — the full-screen sealed gate must appear.
  // -------------------------------------------------------------------------
  // The app reads the seal state once on load: reload only after the server
  // itself reports sealed, or the reload races the seal and opens the shell.
  await expect
    .poll(
      async () => {
        const res = await request.get(`${daemon!.adminUrl}/v1/barrier/status`, {
          ignoreHTTPSErrors: true,
        });
        if (res.status() !== 200) return false;
        return ((await res.json()) as { sealed?: boolean }).sealed === true;
      },
      { timeout: 10_000 },
    )
    .toBe(true);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "QRDB is Sealed" }),
  ).toBeVisible({ timeout: 20_000 });

  // Passphrase copy must be shown (not Shamir).
  await expect(
    page.getByText(
      "Enter the barrier passphrase to unseal the encryption barrier.",
    ),
  ).toBeVisible();

  // Regression guard (Bug 3): submit button must read "Unseal" in passphrase
  // mode, NOT the Shamir-mode label "Submit Share".
  await expect(
    page.getByRole("button", { name: "Unseal", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Submit Share" })).toHaveCount(
    0,
  );

  // -------------------------------------------------------------------------
  // Step 4: Unseal through the full-screen gate.
  // -------------------------------------------------------------------------
  await page.locator("#unseal-share").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();

  // After unseal, the app shell must load.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 30_000 });
});
