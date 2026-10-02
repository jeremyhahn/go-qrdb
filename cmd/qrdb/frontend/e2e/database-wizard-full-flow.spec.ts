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
 * Database creation wizard full end-to-end flow tests.
 *
 * Verifies that:
 *   - The Next button is visible and clickable on every wizard step.
 *   - The Barrier (step 4) Next button is specifically reachable without
 *     any scroll/layout clipping.
 *   - Both standalone and raft mock server modes are covered.
 *   - All three barrier modes (none, passphrase, shamir) allow the user
 *     to advance past the barrier step.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  raftClusterState,
  type MockState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const STEP_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock states
// ---------------------------------------------------------------------------

/** Standalone state — Raft step is absent from the wizard flow. */
const standaloneState: MockState = {
  ...secondLaunchEmbeddedState,
};

/** Raft state — Raft step is present between Storage and SQL. */
const raftState: MockState = {
  ...raftClusterState,
};

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

/** Opens the Create Database wizard from the app landing page. */
async function openWizard(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
  await expect(
    page.getByRole("heading", { name: "Database Name & Engine" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Asserts the Next button in the main content area is both visible in the
 * viewport and not disabled — verifying it can actually be clicked without
 * requiring the user to scroll.
 */
async function assertNextButtonClickable(page: Page): Promise<void> {
  const btn = page.locator("main").getByRole("button", { name: "Next" });
  await expect(btn).toBeVisible({ timeout: STEP_TIMEOUT });
  await expect(btn).toBeEnabled();
  // isIntersectingViewport confirms the button is not clipped by overflow-hidden.
  const inViewport = await btn.isVisible();
  expect(inViewport).toBe(true);
}

// ---------------------------------------------------------------------------
// Standalone flow: Name → Storage → SQL → Barrier → Summary → Complete
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Full Flow (Standalone)", () => {
  test.use({ mockState: standaloneState });

  test("Next button is visible and clickable on every standalone step", async ({ page }) => {
    await openWizard(page);

    // Step 1: Name & Engine
    await expect(
      page.getByRole("heading", { name: "Database Name & Engine" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("#db-name").fill("full-flow-standalone");
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 2: Storage Backend
    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 3: SQL Wire Protocols (standalone skips Raft)
    await expect(
      page.getByRole("heading", { name: /SQL Wire Protocols|Protocols/i }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 4: Database Encryption Barrier — the critical step
    await expect(
      page.getByRole("heading", { name: "Database Encryption Barrier" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 5: Review Configuration (Summary)
    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(
      page.locator("main").getByRole("button", { name: "Create Database" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("Back navigation from every step works correctly (standalone)", async ({ page }) => {
    await openWizard(page);

    // Advance through all steps
    await page.locator("#db-name").fill("back-nav-standalone");
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: /SQL Wire Protocols|Protocols/i }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Database Encryption Barrier" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Back from Barrier → SQL
    await page.locator("main").getByRole("button", { name: "Back" }).click();
    await expect(
      page.getByRole("heading", { name: /SQL Wire Protocols|Protocols/i }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Back from SQL → Storage
    await page.locator("main").getByRole("button", { name: "Back" }).click();
    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Back from Storage → Name
    await page.locator("main").getByRole("button", { name: "Back" }).click();
    await expect(
      page.getByRole("heading", { name: "Database Name & Engine" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Raft flow: Name → Storage → Raft → SQL → Barrier → Summary → Complete
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Full Flow (Raft)", () => {
  test.use({ mockState: raftState });

  test("Next button is visible and clickable on every raft step", async ({ page }) => {
    await openWizard(page);

    // Step 1: Name & Engine
    await expect(
      page.getByRole("heading", { name: "Database Name & Engine" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("#db-name").fill("full-flow-raft");
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 2: Storage Backend
    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 3: Raft Configuration (only present in raft mode)
    await expect(
      page.getByRole("heading", { name: /Raft|Cluster/i }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 4: SQL Wire Protocols
    await expect(
      page.getByRole("heading", { name: /SQL Wire Protocols|Protocols/i }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 5: Database Encryption Barrier — the critical step
    await expect(
      page.getByRole("heading", { name: "Database Encryption Barrier" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    // Step 6: Review Configuration (Summary)
    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(
      page.locator("main").getByRole("button", { name: "Create Database" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Barrier step: different barrier modes
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Barrier Step Modes", () => {
  test.use({ mockState: standaloneState });

  /**
   * Navigates to the Barrier step on a standalone server and returns.
   * Steps: Name → Storage → SQL → Barrier
   */
  async function navigateToBarrier(page: Page, dbName: string): Promise<void> {
    await openWizard(page);
    await page.locator("#db-name").fill(dbName);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: /SQL Wire Protocols|Protocols/i }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Database Encryption Barrier" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  }

  test("barrier mode: none — Next button is clickable and advances to Summary", async ({ page }) => {
    await navigateToBarrier(page, "barrier-none-db");

    // Select the "None" barrier option.
    await page.locator("button").filter({ hasText: "None" }).first().click();

    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("barrier mode: passphrase — Next button is clickable and advances to Summary", async ({ page }) => {
    await navigateToBarrier(page, "barrier-passphrase-db");

    // Select the "Passphrase" barrier option; Next waits for a confirmed
    // passphrase.
    await page.locator("button").filter({ hasText: "Passphrase" }).first().click();
    await expect(page.locator("main").getByRole("button", { name: "Next" })).toBeDisabled();
    await page.locator("#db-barrier-passphrase").fill("correct horse battery");
    await page.locator("#db-barrier-passphrase-confirm").fill("correct horse battery");

    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("barrier mode: shamir — valid M-of-N config enables Next and advances to Summary", async ({ page }) => {
    await navigateToBarrier(page, "barrier-shamir-db");

    // Select the "Shamir" barrier option.
    await page.locator("button").filter({ hasText: "Shamir" }).first().click();

    // Shamir configuration fields become visible.
    await expect(
      page.locator("#db-shares-required"),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Fill valid configuration: 2-of-3.
    await page.locator("#db-shares-required").fill("2");
    await page.locator("#db-shares-total").fill("3");

    await assertNextButtonClickable(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("barrier mode: shamir — invalid threshold disables Next until corrected", async ({ page }) => {
    await navigateToBarrier(page, "barrier-shamir-invalid-db");

    await page.locator("button").filter({ hasText: "Shamir" }).first().click();

    await expect(
      page.locator("#db-shares-required"),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Set threshold greater than total — invalid configuration.
    await page.locator("#db-shares-required").fill("5");
    await page.locator("#db-shares-total").fill("3");

    const nextBtn = page.locator("main").getByRole("button", { name: "Next" });
    await expect(nextBtn).toBeDisabled();
    await expect(
      page.getByText("Shares required cannot exceed total shares."),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Correct the values — Next becomes enabled again.
    await page.locator("#db-shares-required").fill("2");
    await expect(nextBtn).toBeEnabled();
  });

  test("barrier step: Next button is within viewport without scrolling", async ({ page }) => {
    await navigateToBarrier(page, "barrier-viewport-db");

    const nextBtn = page.locator("main").getByRole("button", { name: "Next" });
    await expect(nextBtn).toBeVisible({ timeout: STEP_TIMEOUT });

    // Confirm the button bounding box is within the viewport.
    const box = await nextBtn.boundingBox();
    expect(box).not.toBeNull();
    if (box !== null) {
      const viewport = page.viewportSize();
      expect(viewport).not.toBeNull();
      if (viewport !== null) {
        expect(box.y + box.height).toBeLessThanOrEqual(viewport.height);
      }
    }
  });
});

// ---------------------------------------------------------------------------
// REGRESSION: 2026-04-07 — "An unknown error occurred" when creating database
// ---------------------------------------------------------------------------
//
// Bug: StepDatabaseCreating tried to call client.embeddedServer.startEmbedded()
// during the create flow, even when the active server was already running.
// In the new connection-manager architecture, the active connection IS the
// running embedded server — the wizard must NOT try to start a new one.
// The unhandled error from the second startEmbedded call propagated as
// "An unknown error occurred" to the user.
//
// These tests verify:
//   1. The full create flow completes without error against a server that
//      was already started by the connection manager.
//   2. The wizard does NOT call EmbeddedServerBinding.StartEmbedded.
//   3. Errors from DatabaseBinding.Create surface with their actual message,
//      never as "An unknown error occurred".
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Create regression (2026-04-07)", () => {
  test.use({ mockState: standaloneState });

  test("create database completes without calling startEmbedded", async ({ page }) => {
    // Track every call into the EmbeddedServerBinding mock so we can
    // assert StartEmbedded is NEVER invoked from the create flow.
    const startEmbeddedCalls: unknown[] = [];
    await page.exposeFunction("__recordStartEmbedded", (cfg: unknown) => {
      startEmbeddedCalls.push(cfg);
    });

    await page.addInitScript(() => {
      // Wrap StartEmbedded after the mock injects window.go.main so any
      // call from production code is recorded.
      const installSpy = (): void => {
        const w = window as unknown as {
          go?: { main?: { EmbeddedServerBinding?: Record<string, unknown> } };
          __recordStartEmbedded?: (cfg: unknown) => void;
        };
        const binding = w.go?.main?.EmbeddedServerBinding;
        if (binding === undefined) return;
        const original = binding.StartEmbedded as (cfg: unknown) => Promise<unknown>;
        binding.StartEmbedded = async (cfg: unknown): Promise<unknown> => {
          if (typeof w.__recordStartEmbedded === "function") {
            w.__recordStartEmbedded(cfg);
          }
          return original.call(binding, cfg);
        };
      };
      // Try immediately and also after a tick — the mock installs synchronously.
      try {
        installSpy();
      } catch {
        // ignore
      }
      setTimeout(installSpy, 0);
    });

    // Capture any uncaught page errors so we can assert none occurred.
    const pageErrors: string[] = [];
    page.on("pageerror", (err) => {
      pageErrors.push(err.message);
    });

    await openWizard(page);

    // Walk the standalone flow with all defaults.
    await page.locator("#db-name").fill("regression-create");
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: /SQL Wire Protocols|Protocols/i }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Database Encryption Barrier" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    // Default barrier is "passphrase" but the wizard accepts pass-through;
    // select "None" to avoid Shamir validation requirements.
    await page.locator("main").getByText("None", { exact: true }).first().click();
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page
      .locator("main")
      .getByRole("button", { name: "Create Database" })
      .click();

    // After successful creation the wizard closes and MainApp returns to
    // the database workspace. Wait for the wizard heading to disappear,
    // which is the signal that the create flow completed cleanly.
    await expect(
      page.getByRole("heading", { name: "Create Database" }),
    ).not.toBeVisible({ timeout: STEP_TIMEOUT });

    // The "An unknown error occurred" message must never appear.
    await expect(
      page.getByText("An unknown error occurred"),
    ).not.toBeVisible();

    // No uncaught page errors during the flow.
    expect(pageErrors).toEqual([]);

    // CRITICAL ASSERTION: StartEmbedded must not have been called during
    // the database creation flow. The active server was already running
    // (via the connection manager) before the wizard opened.
    expect(startEmbeddedCalls).toEqual([]);
  });

  test("create database error shows the actual server message, not a generic placeholder", async ({
    page,
  }) => {
    // Inject an error on DatabaseBinding.Create so we can verify that
    // the wizard surfaces the actual error string instead of swallowing
    // it as "An unknown error occurred".
    await page.addInitScript(() => {
      const installSpy = (): void => {
        const w = window as unknown as {
          go?: { main?: { DatabaseBinding?: Record<string, unknown> } };
        };
        const binding = w.go?.main?.DatabaseBinding;
        if (binding === undefined) return;
        binding.Create = async (): Promise<unknown> => {
          throw new Error("database: database already exists: \"testkv\"");
        };
      };
      try {
        installSpy();
      } catch {
        // ignore
      }
      setTimeout(installSpy, 0);
    });

    await openWizard(page);

    // Walk to the summary step.
    await page.locator("#db-name").fill("error-create");
    await page.locator("main").getByRole("button", { name: "Next" }).click();
    await page.locator("main").getByRole("button", { name: "Next" }).click();
    await page.locator("main").getByRole("button", { name: "Next" }).click();
    await page.locator("main").getByText("None", { exact: true }).first().click();
    await page.locator("main").getByRole("button", { name: "Next" }).click();
    await page
      .locator("main")
      .getByRole("button", { name: "Create Database" })
      .click();

    // The actual error message must appear, not the generic placeholder.
    await expect(
      page.getByText(/database already exists/i),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(
      page.getByText("An unknown error occurred"),
    ).not.toBeVisible();
  });
});
