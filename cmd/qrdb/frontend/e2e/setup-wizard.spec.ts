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

/**
 * Connection Manager E2E tests.
 *
 * The Connection Manager is shown when the app first launches. It presents:
 *   - An empty state with "No servers configured" and two action buttons.
 *   - A multi-step embedded server creation wizard triggered by
 *     "Create Embedded Server".
 *   - A simple external connection form triggered by "Connect to Server".
 *
 * Embedded wizard step sequence:
 *   Standalone: name → mode → barrier → admin → summary → applying → main app
 *   Raft:       name → mode → discovery → barrier → admin → summary → applying → main app
 * (officers is inserted between barrier and admin for a Shamir barrier.)
 */

import {
  test,
  expect,
  defaultMockState,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import {
  waitForConnectionManager,
  openEmbeddedWizard,
  stepName,
  stepMode,
  stepDiscoveryMulticast,
  stepBarrier,
  stepAdminCredential,
  waitForSummary,
  createServer,
  acknowledgeCeremonyResult,
} from "./fixtures/embedded-wizard-steps.ts";

/** Timeout for async step transitions. */
const STEP_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// State constants
// ---------------------------------------------------------------------------

/** First-run state: Connection Manager with no profiles should be shown. */
const firstRunState = {
  ...defaultMockState,
};

/** Already-configured state: main app loads directly. */
const configuredState = {
  ...secondLaunchEmbeddedState,
};

// ---------------------------------------------------------------------------
// 1. First-run detection
// ---------------------------------------------------------------------------

test.describe("Connection Manager - first run", () => {
  test.use({ mockState: firstRunState });

  test("shows Connection Manager on first launch", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);

    // The ConnectionManager modal must show the "No servers configured" card.
    await expect(
      page.getByRole("heading", { name: "No servers configured", exact: true }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Both action buttons must be present inside the modal.
    await expect(
      page.getByRole("button", { name: "Create Embedded Server" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Connect to Server" }),
    ).toBeVisible();
  });
});

test.describe("Connection Manager - already configured", () => {
  test.use({ mockState: configuredState });

  test("skips Connection Manager when profiles exist and jumps to main app", async ({ page }) => {
    await page.goto("/");

    // Main app loads directly.
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// 2. Embedded wizard — Standalone with no barrier
// ---------------------------------------------------------------------------

test.describe("Connection Manager - embedded wizard - standalone, no barrier", () => {
  test.use({ mockState: firstRunState });

  test("completes standalone embedded setup with no barrier", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);

    // Open the embedded wizard.
    await openEmbeddedWizard(page);

    // Step 1: Name
    await stepName(page, "My Local Server");

    // Step 2: Mode — Standalone (standalone skips discovery)
    await stepMode(page, "Standalone");

    // Step 3: Barrier — None
    await stepBarrier(page, "None");

    // Step 3.5: Admin credential — Generate (dev/test only)
    await stepAdminCredential(page);

    // Step 4: Summary — verify configuration rows
    await waitForSummary(page);
    await expect(page.getByText("My Local Server")).toBeVisible();
    await expect(page.getByText("Standalone")).toBeVisible();
    await expect(page.getByText("None")).toBeVisible();

    // Step 5: Apply → transitions to main app
    await createServer(page);
  });
});

// ---------------------------------------------------------------------------
// 3. Embedded wizard — Standalone with passphrase barrier
// ---------------------------------------------------------------------------

test.describe("Connection Manager - embedded wizard - standalone, passphrase", () => {
  test.use({ mockState: firstRunState });

  test("completes standalone setup with passphrase barrier", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page, "Passphrase Server");
    await stepMode(page, "Standalone");

    // Barrier step — select Passphrase card then fill inputs
    await expect(
      page.locator("button").filter({ hasText: "Passphrase" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("button").filter({ hasText: "Passphrase" }).first().click();

    // Passphrase inputs must now be visible.
    await expect(page.locator("#emb-passphrase")).toBeVisible();
    await expect(page.locator("#emb-passphrase-confirm")).toBeVisible();

    // Fill matching passphrase to pass validation.
    await page.locator("#emb-passphrase").fill("S3cur3P@ss!");
    await page.locator("#emb-passphrase-confirm").fill("S3cur3P@ss!");

    // Next must be enabled with matching passphrases.
    await expect(
      page.getByRole("button", { name: "Next" }),
    ).not.toBeDisabled();
    await page.getByRole("button", { name: "Next" }).click();

    // Admin credential — Generate (dev/test only)
    await stepAdminCredential(page);

    // Summary shows Passphrase barrier mode.
    await waitForSummary(page);
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();

    await createServer(page);
  });

  test("passphrase mismatch keeps Next disabled", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Standalone");

    await expect(
      page.locator("button").filter({ hasText: "Passphrase" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("button").filter({ hasText: "Passphrase" }).first().click();

    await page.locator("#emb-passphrase").fill("CorrectHorse");
    await page.locator("#emb-passphrase-confirm").fill("BatteryStaple");

    // Validation error must appear.
    await expect(
      page.getByText("Passphrases do not match."),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Next button must be disabled.
    await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  test("passphrase validation alert fully disappears once fields match (Bug 1 regression)", async ({ page }) => {
    // Regression guard: a stale empty .alert-error box must not remain mounted
    // after the mismatch is corrected. Previously the {#if} block stayed in the
    // DOM with an empty span because the Svelte 5 compound &&-expression in the
    // {#if} condition caused the branch-block's schedule() walk to bail early.
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Standalone");

    await expect(
      page.locator("button").filter({ hasText: "Passphrase" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("button").filter({ hasText: "Passphrase" }).first().click();

    // Create a mismatch — error alert must appear.
    await page.locator("#emb-passphrase").fill("CorrectHorse");
    await page.locator("#emb-passphrase-confirm").fill("BatteryStaple");
    await expect(
      page.getByText("Passphrases do not match."),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Fix the confirm field to match — error alert must fully disappear.
    await page.locator("#emb-passphrase-confirm").fill("CorrectHorse");
    await expect(
      page.getByText("Passphrases do not match."),
    ).toHaveCount(0);
    // No empty alert box may remain (the stale-{#if} regression surfaces as an
    // empty .alert.alert-error even after the error text is cleared).
    await expect(page.locator(".alert.alert-error")).toHaveCount(0);

    // Next must be enabled after the mismatch is resolved.
    await expect(page.getByRole("button", { name: "Next" })).not.toBeDisabled();
  });

  test("passphrase required message shows when passphrase field is empty", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Standalone");

    await page.locator("button").filter({ hasText: "Passphrase" }).first().click();

    // Leave both fields empty — the validation error must state passphrase is required.
    await expect(
      page.getByText("Passphrase is required."),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
  });
});

// ---------------------------------------------------------------------------
// 4. Embedded wizard — Raft with multicast discovery
// ---------------------------------------------------------------------------

test.describe("Connection Manager - embedded wizard - raft, multicast discovery", () => {
  test.use({ mockState: firstRunState });

  test("completes raft setup with multicast discovery and no barrier", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page, "Raft Node");

    // Mode: Raft Cluster — will proceed to Discovery step
    await stepMode(page, "Raft Cluster");

    // Discovery step: multicast is selected by default
    await stepDiscoveryMulticast(page, "my-cluster-secret");

    // Barrier step: None
    await stepBarrier(page, "None");

    // Admin credential — Generate (dev/test only)
    await stepAdminCredential(page);

    // Summary: verify raft settings are present
    await waitForSummary(page);
    await expect(page.getByText("Raft Cluster")).toBeVisible();
    await expect(page.getByText("multicast", { exact: false })).toBeVisible();

    await createServer(page);
  });

  test("discovery step shows multicast secret input when multicast is selected", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Raft Cluster");

    // Multicast card is selected by default.
    await expect(page.locator("#emb-multicast-secret")).toBeVisible({
      timeout: STEP_TIMEOUT,
    });
  });

  test("discovery step shows DNS domain input when DNS is selected", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Raft Cluster");

    // Select DNS SRV card.
    await page.locator("button").filter({ hasText: "DNS SRV" }).click();
    await expect(page.locator("#emb-dns-domain")).toBeVisible({
      timeout: STEP_TIMEOUT,
    });
  });

  test("discovery step shows static peers textarea when static is selected", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Raft Cluster");

    // Select Static card.
    await page.locator("button").filter({ hasText: "Static" }).click();
    await expect(page.locator("#emb-static-peers")).toBeVisible({
      timeout: STEP_TIMEOUT,
    });
  });
});

// ---------------------------------------------------------------------------
// 5. Back navigation
// ---------------------------------------------------------------------------

test.describe("Connection Manager - back navigation", () => {
  test.use({ mockState: firstRunState });

  test("back from Mode step returns to Name step", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);

    // Now on Mode step — verify the mode cards are present.
    await expect(
      page.locator("button").filter({ hasText: "Standalone" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await page.getByRole("button", { name: "Back" }).click();

    // Back to Name step — the name input must be visible.
    await expect(page.locator("#emb-name")).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("back from Barrier step returns to Mode step for standalone", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Standalone");

    // Now on Barrier step.
    await expect(
      page.locator("button").filter({ hasText: "None" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await page.getByRole("button", { name: "Back" }).click();

    // Back to Mode step — mode cards must be visible.
    await expect(
      page.locator("button").filter({ hasText: "Standalone" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("back from Discovery step returns to Mode step for raft", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Raft Cluster");

    // Now on Discovery step.
    await expect(page.locator("#emb-multicast-secret")).toBeVisible({
      timeout: STEP_TIMEOUT,
    });

    await page.getByRole("button", { name: "Back" }).click();

    // Back to Mode step.
    await expect(
      page.locator("button").filter({ hasText: "Raft Cluster" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("back from Barrier step returns to Discovery step for raft", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Raft Cluster");
    await stepDiscoveryMulticast(page);

    // Now on Barrier step.
    await expect(
      page.locator("button").filter({ hasText: "None" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await page.getByRole("button", { name: "Back" }).click();

    // Back to Discovery step.
    await expect(page.locator("#emb-multicast-secret")).toBeVisible({
      timeout: STEP_TIMEOUT,
    });
  });

  test("back from Summary step returns to Barrier step", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Standalone");
    await stepBarrier(page, "None");
    await stepAdminCredential(page);

    // Now on Summary step.
    await waitForSummary(page);

    await page.getByRole("button", { name: "Back" }).click();

    // Back to the Admin Credential step — the step immediately before Summary.
    await expect(
      page.getByRole("heading", { name: "Bootstrap Admin Credential" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await page.getByRole("button", { name: "Back" }).click();

    // Back to Barrier step — barrier mode cards must be visible.
    await expect(
      page.locator("button").filter({ hasText: "None" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("Cancel button on Name step closes the wizard", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    // Name step has a Cancel button.
    await page.getByRole("button", { name: "Cancel" }).click();

    // Returns to the ConnectionManager empty state inside the modal.
    await expect(
      page.getByRole("heading", { name: "No servers configured", exact: true }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// 6. External connection form
// ---------------------------------------------------------------------------

test.describe("Connection Manager - external connection", () => {
  test.use({ mockState: firstRunState });

  test("shows external connection form when Connect to Server is clicked", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);

    await page.getByRole("button", { name: "Connect to Server" }).click();

    // The form card heading must appear.
    await expect(
      page.getByRole("heading", { name: "Connect to Server" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Key form fields must be present.
    await expect(page.locator("#ext-name")).toBeVisible();
    await expect(page.locator("#ext-transport")).toBeVisible();
    await expect(page.locator("#ext-address")).toBeVisible();
  });

  test("fills external form and submits connection", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);

    await page.getByRole("button", { name: "Connect to Server" }).click();

    await expect(
      page.getByRole("heading", { name: "Connect to Server" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Fill the name field.
    await page.locator("#ext-name").fill("Production Server");

    // Select gRPC transport.
    await page.locator("#ext-transport").selectOption("grpc");

    // Fill address (address auto-updates when transport changes; override it).
    await page.locator("#ext-address").fill("prod-server.example.com:63003");

    // The Connect button must be enabled with a non-empty address.
    const connectBtn = page.getByRole("button", { name: "Connect" });
    await expect(connectBtn).not.toBeDisabled();
    await connectBtn.click();

    // The mock server is not initialized (first-run state), so the connection
    // attempt results in a "needs-init" status. The modal closes and the main
    // app reflects the new profile state.
    // Verify the modal closed (no "Connect to Server" heading visible).
    await expect(
      page.getByRole("heading", { name: "Connect to Server" }),
    ).not.toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("transport selection auto-fills a default address", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);

    await page.getByRole("button", { name: "Connect to Server" }).click();
    await expect(page.locator("#ext-address")).toBeVisible({ timeout: STEP_TIMEOUT });

    // Select REST transport and verify the address placeholder changes.
    await page.locator("#ext-transport").selectOption("rest");
    await expect(page.locator("#ext-address")).toHaveValue(
      "https://localhost:63002",
    );

    // Select Unix Socket transport.
    await page.locator("#ext-transport").selectOption("unix");
    await expect(page.locator("#ext-address")).toHaveValue(
      "/var/run/qrdbd/qrdbd.sock",
    );
  });

  test("Cancel button on external form closes the panel", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);

    await page.getByRole("button", { name: "Connect to Server" }).click();
    await expect(
      page.getByRole("heading", { name: "Connect to Server" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await page.getByRole("button", { name: "Cancel" }).click();

    // Returns to the ConnectionManager empty state inside the modal.
    await expect(
      page.getByRole("heading", { name: "No servers configured", exact: true }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// 7. Apply error handling
// ---------------------------------------------------------------------------

test.describe("Connection Manager - apply error handling", () => {
  test.use({ mockState: firstRunState });

  test("apply error shows error alert with Retry button", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Standalone");
    await stepBarrier(page, "None");
    await stepAdminCredential(page);
    await waitForSummary(page);

    // Inject a failure on the health-check call.
    await injectError(page, "AdminBinding.Status", "server health check failed");

    await page.getByRole("button", { name: "Create Server" }).click();

    // Error alert must appear on the applying step.
    const alert = page.locator("[role='alert']").filter({ hasText: "server health check failed" });
    await expect(alert).toBeVisible({ timeout: STEP_TIMEOUT });

    // A Retry button must be present.
    await expect(
      page.getByRole("button", { name: "Retry" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await clearError(page, "AdminBinding.Status");
  });

  test("retry after error resumes the apply flow successfully", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);
    await stepMode(page, "Standalone");
    await stepBarrier(page, "None");
    await stepAdminCredential(page);
    await waitForSummary(page);

    await injectError(page, "AdminBinding.Status", "transient error");

    await page.getByRole("button", { name: "Create Server" }).click();

    await expect(
      page.locator("[role='alert']").filter({ hasText: "transient error" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Clear the error so the retry can succeed.
    await clearError(page, "AdminBinding.Status");

    await page.getByRole("button", { name: "Retry" }).click();

    // After a successful retry, acknowledge the ceremony-result panel and
    // the main app loads.
    await acknowledgeCeremonyResult(page);
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// 8. Step indicator
// ---------------------------------------------------------------------------

test.describe("Connection Manager - step indicator", () => {
  test.use({ mockState: firstRunState });

  test("step indicator is visible on the Name step with Name active", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    // The Name input must be visible (we are on the Name step).
    await expect(page.locator("#emb-name")).toBeVisible({ timeout: STEP_TIMEOUT });

    // The step indicator list must be rendered inside the wizard card.
    const steps = page.locator("ul.steps");
    await expect(steps).toBeVisible({ timeout: STEP_TIMEOUT });

    // First step (Name) must carry the step-primary class.
    const firstStep = steps.locator("li.step").first();
    await expect(firstStep).toHaveClass(/step-primary/);
  });

  test("step indicator advances when moving to Mode step", async ({ page }) => {
    await page.goto("/");
    await waitForConnectionManager(page);
    await openEmbeddedWizard(page);

    await stepName(page);

    // Now on Mode step.
    await expect(
      page.locator("button").filter({ hasText: "Standalone" }).first(),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    const steps = page.locator("ul.steps");
    await expect(steps).toBeVisible();

    // Both Name (0) and Mode (1) steps should carry step-primary.
    const stepItems = steps.locator("li.step");
    await expect(stepItems.nth(0)).toHaveClass(/step-primary/);
    await expect(stepItems.nth(1)).toHaveClass(/step-primary/);
  });
});
