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
 * Database wizard encryption barrier tests.
 *
 * Covers all storage + encryption combinations; Shamir is shown disabled until
 * per-database share holders ship.
 *
 * The wizard step sequence for standalone servers:
 *   Name → Storage → SQL → Barrier → Summary → Creating → sidebar
 *
 * The Raft Configuration step is skipped entirely for standalone servers.
 * The "Mode" step was also removed from the database wizard.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import {
  captureDatabaseCreates,
  capturedDatabaseCreates,
  databaseCreatesSoFar,
} from "./fixtures/database-create-capture.ts";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Timeout for heading waits during async initialization phases. */
const INIT_TIMEOUT = 10_000;

/**
 * Navigate to the database creation wizard from the empty state.
 */
async function openCreateWizard(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  // Wait for the app to load into MainApp
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
  // Click the "New Database" button in the sidebar
  await page.locator("aside").getByText("New Database").click();
  // Verify the wizard's first step appears
  await expect(
    page.getByRole("heading", { name: "Database Name & Engine" }),
  ).toBeVisible();
}

/**
 * Fill in the database name and select an engine, then click Next.
 */
async function fillNameAndAdvance(
  page: import("@playwright/test").Page,
  name: string,
  engine: "Key-Value" | "TimeSeries" | "Vector" = "Key-Value",
): Promise<void> {
  await page.locator("#db-name").fill(name);
  // Click the engine card
  await page.locator("button").filter({ hasText: engine }).click();
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Select a storage backend and advance to the next step.
 */
async function selectStorageAndAdvance(
  page: import("@playwright/test").Page,
  backend: "Memory" | "File" | "Pebble",
): Promise<void> {
  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
  if (backend !== "Pebble") {
    await page.locator("label").filter({ hasText: backend }).click();
  }
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Advance through the SQL Wire Protocols step with defaults.
 */
async function advanceThroughSQL(page: import("@playwright/test").Page): Promise<void> {
  await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Select encryption barrier mode and advance.
 */
async function selectBarrierAndAdvance(
  page: import("@playwright/test").Page,
  mode: "None" | "Passphrase",
): Promise<void> {
  await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();

  if (mode === "None") {
    await page.locator("button").filter({ hasText: "None" }).click();
  } else if (mode === "Passphrase") {
    // The barrier defaults to none; the passphrase barrier asks for a
    // confirmed passphrase.
    await page.locator("button").filter({ hasText: "Passphrase" }).click();
    await page.locator("#db-barrier-passphrase").fill("e2e-barrier-passphrase");
    await page.locator("#db-barrier-passphrase-confirm").fill("e2e-barrier-passphrase");
  }

  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Navigate through all wizard steps up to the Summary, selecting the
 * given storage backend and encryption barrier mode.
 *
 * Flow: Name → Storage → SQL → Barrier → Summary
 * (Raft Configuration step is skipped for standalone servers.)
 */
async function navigateToSummaryWithConfig(
  page: import("@playwright/test").Page,
  name: string,
  backend: "Memory" | "File" | "Pebble",
  barrierMode: "None" | "Passphrase",
): Promise<void> {
  await openCreateWizard(page);
  await fillNameAndAdvance(page, name);
  await selectStorageAndAdvance(page, backend);
  await advanceThroughSQL(page);
  await selectBarrierAndAdvance(page, barrierMode);
  await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
}

/**
 * Click Create Database and verify the database appears in the sidebar.
 */
async function createAndVerifyComplete(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.locator("main").getByRole("button", { name: "Create Database" }).click();
  const sidebar = page.locator("aside");
  await expect(sidebar.getByText(name)).toBeVisible({ timeout: INIT_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Storage + Encryption Combination Tests
// ---------------------------------------------------------------------------

test.describe("Wizard Encryption Combinations", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  // -------------------------------------------------------------------------
  // Memory combinations
  // -------------------------------------------------------------------------

  test("Memory + no encryption completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-mem-none", "Memory", "None");

    // Verify summary shows correct configuration
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-mem-none");
  });

  test("Memory + passphrase completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-mem-pass", "Memory", "Passphrase");

    // Verify summary shows correct configuration
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-mem-pass");
  });

  // -------------------------------------------------------------------------
  // File combinations
  // -------------------------------------------------------------------------

  test("File + no encryption completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-file-none", "File", "None");

    // Verify summary shows correct configuration
    await expect(page.getByText("File", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-file-none");
  });

  test("File + passphrase completes successfully", async ({ page }) => {
    await navigateToSummaryWithConfig(page, "enc-file-pass", "File", "Passphrase");

    // Verify summary shows correct configuration
    await expect(page.getByText("File", { exact: true })).toBeVisible();
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "enc-file-pass");
  });


  // Per-database Shamir is refused by the server until per-database share
  // holders ship (database.UnsupportedBarrierStrategyError, 400), so the
  // wizard does not offer it: the card is disabled and says why. A Shamir
  // create request is never sent.
  test("File + Shamir is not offered: the card is disabled and no Shamir create is sent", async ({ page }) => {
    await captureDatabaseCreates(page);
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "enc-file-shamir");
    await selectStorageAndAdvance(page, "File");
    await advanceThroughSQL(page);
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();

    const shamir = page.getByTestId("db-seal-mode-shamir");
    await expect(shamir).toBeDisabled();
    await expect(shamir).toHaveAttribute("title", /per-database Shamir share holders/);
    await shamir.click({ force: true });
    await expect(page.locator("input[name='db-seal-mode'][value='shamir']")).not.toBeChecked();
    await expect(page.locator("#db-shares-required")).toHaveCount(0);

    // The wizard can still finish -- without a Shamir barrier.
    await page.getByRole("button", { name: "Next" }).click();
    await createAndVerifyComplete(page, "enc-file-shamir");
    const creates = await capturedDatabaseCreates(page);
    expect(creates).toHaveLength(1);
    expect((creates[0] as { barrier?: { mode?: string } }).barrier?.mode).not.toBe("shamir");
  });

  // The create step refuses a seal mode the wizard cannot offer even when the
  // barrier step lets one through. The barrier step cannot be made to, so the
  // spec stands in for that regression by writing the store directly -- the
  // same module instance the app runs -- after a valid barrier step.
  test("the create step refuses an unavailable seal mode the barrier step let through", async ({ page }) => {
    await captureDatabaseCreates(page);
    await navigateToSummaryWithConfig(page, "enc-backstop", "File", "Passphrase");

    await page.evaluate(async () => {
      const storeModule = "/src/stores/database-wizard.svelte.ts";
      const { dbWizardStore } = await import(/* @vite-ignore */ storeModule);
      dbWizardStore.sealMode = "shamir";
      dbWizardStore.sealConfig = { mode: "shamir", sharesRequired: 2, sharesTotal: 3 };
    });

    await page.locator("main").getByRole("button", { name: "Create Database" }).click();
    await expect(
      page.getByText(/The shamir barrier is not available: Available once per-database Shamir share holders ship\./),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
    expect(await databaseCreatesSoFar(page)).toHaveLength(0);

    // The refusal still takes the passphrase out of the store.
    const left = await page.evaluate(async () => {
      const storeModule = "/src/stores/database-wizard.svelte.ts";
      const { dbWizardStore } = await import(/* @vite-ignore */ storeModule);
      return dbWizardStore.barrierPassphrase as string;
    });
    expect(left).toBe("");
  });
});

