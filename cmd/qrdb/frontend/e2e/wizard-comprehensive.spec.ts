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
 * Comprehensive database creation wizard tests.
 *
 * Covers engine type combinations, storage + barrier combinations, SQL wire
 * protocol options, and validation edge cases.
 *
 * The wizard step sequence for standalone servers:
 *   Name → Storage → SQL → Barrier → Summary → Creating → sidebar
 *
 * The Raft Configuration step is skipped entirely for standalone servers.
 * The "Mode" step (Embedded / Remote) was also removed. All databases created
 * through the wizard are associated with the active server connection,
 * which is always an embedded standalone server in the secondLaunchEmbeddedState mock.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import {
  captureDatabaseCreates,
  capturedDatabaseCreates,
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
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: INIT_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
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
  await page.locator("button").filter({ hasText: engine }).click();
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Select a storage backend and advance.
 */
async function selectStorageAndAdvance(
  page: import("@playwright/test").Page,
  backend: "Memory" | "File" | "Pebble",
  dataDir = "",
): Promise<void> {
  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
  if (backend !== "Pebble") {
    await page.locator("label").filter({ hasText: backend }).click();
  }
  // A per-database barrier needs the database's own data directory.
  if (dataDir !== "") await page.locator("#data-dir-input").fill(dataDir);
  await page.getByRole("button", { name: "Next" }).click();
}

/**
 * Advance through the SQL Wire Protocols step with defaults (both disabled).
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
  await expect(
    page.getByRole("heading", { name: "Database Encryption Barrier" }),
  ).toBeVisible();

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
 * Click Create Database on the summary page and verify the database appears
 * in the sidebar.
 */
async function createAndVerifyComplete(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.locator("main").getByRole("button", { name: "Create Database" }).click();
  const sidebar = page.locator("aside");
  await expect(sidebar.getByText(name)).toBeVisible({ timeout: INIT_TIMEOUT });
}

/**
 * Navigate through the full embedded wizard to the Summary using the
 * provided engine, storage backend, and encryption barrier mode.
 *
 * Flow: Name → Storage → SQL → Barrier → Summary
 * (Raft Configuration step is skipped for standalone servers.)
 */
async function navigateToSummaryEmbedded(
  page: import("@playwright/test").Page,
  name: string,
  engine: "Key-Value" | "TimeSeries" | "Vector",
  backend: "Memory" | "File" | "Pebble",
  barrierMode: "None" | "Passphrase",
): Promise<void> {
  await openCreateWizard(page);
  await fillNameAndAdvance(page, name, engine);
  await selectStorageAndAdvance(
    page,
    backend,
    barrierMode === "Passphrase" ? `/tmp/qrdb-e2e-${name}` : "",
  );
  await advanceThroughSQL(page);
  await selectBarrierAndAdvance(page, barrierMode);
  await expect(
    page.getByRole("heading", { name: "Review Configuration" }),
  ).toBeVisible();
}

// ---------------------------------------------------------------------------
// Engine Type Combinations
// ---------------------------------------------------------------------------

test.describe("Engine Type Combinations", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("TimeSeries engine + Pebble + no encryption shows in summary and completes", async ({ page }) => {
    await navigateToSummaryEmbedded(
      page, "ts-pebble-none", "TimeSeries", "Pebble", "None",
    );

    // Summary must display engine and backend selections.
    await expect(page.getByText("TimeSeries", { exact: true })).toBeVisible();
    await expect(page.getByText("Pebble", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "ts-pebble-none");
  });

  // A per-database barrier needs a data directory, so a memory backend cannot
  // carry one (wizard-encryption.spec.ts drives that block).
  test("TimeSeries engine + Pebble + passphrase shows in summary and completes", async ({ page }) => {
    await navigateToSummaryEmbedded(
      page, "ts-pebble-pass", "TimeSeries", "Pebble", "Passphrase",
    );

    await expect(page.getByText("TimeSeries", { exact: true })).toBeVisible();
    await expect(page.getByText("Pebble", { exact: true })).toBeVisible();
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "ts-pebble-pass");
  });

  test("Vector engine + Pebble + no encryption shows in summary and completes", async ({ page }) => {
    await navigateToSummaryEmbedded(
      page, "vec-pebble-none", "Vector", "Pebble", "None",
    );

    await expect(page.getByText("Vector", { exact: true })).toBeVisible();
    await expect(page.getByText("Pebble", { exact: true })).toBeVisible();
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "vec-pebble-none");
  });

  test("Vector engine + File + passphrase shows in summary, sends all three, and completes", async ({ page }) => {
    await captureDatabaseCreates(page);
    await navigateToSummaryEmbedded(
      page, "vec-file-pass", "Vector", "File", "Passphrase",
    );

    await expect(page.getByText("Vector", { exact: true })).toBeVisible();
    await expect(page.getByText("File", { exact: true })).toBeVisible();
    await expect(page.getByText("Passphrase", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "vec-file-pass");
    const creates = await capturedDatabaseCreates(page);
    expect(creates).toHaveLength(1);
    expect(creates[0]).toMatchObject({
      name: "vec-file-pass",
      backend: "file",
      barrier: { enabled: true, mode: "passphrase", secret: "e2e-barrier-passphrase" },
    });
  });

  test("Switching engine type preserves database name", async ({ page }) => {
    await openCreateWizard(page);

    // Fill name and select Key-Value (default).
    await page.locator("#db-name").fill("engine-switch-test");
    await page.locator("button").filter({ hasText: "Key-Value" }).click();

    // Switch to TimeSeries.
    await page.locator("button").filter({ hasText: "TimeSeries" }).click();

    // Switch to Vector.
    await page.locator("button").filter({ hasText: "Vector" }).click();

    // Name must still be populated.
    await expect(page.locator("#db-name")).toHaveValue("engine-switch-test");
  });
});

// ---------------------------------------------------------------------------
// Backend + Standalone Combinations
//
// These tests use the default secondLaunchEmbeddedState which provides a
// standalone server. The Raft Configuration step is skipped entirely and
// the wizard proceeds from Storage directly to SQL.
// ---------------------------------------------------------------------------

test.describe("Backend + Standalone Combinations", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Memory + standalone server completes successfully", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "mem-standalone");
    await selectStorageAndAdvance(page, "Memory");

    // Raft step is skipped for standalone; SQL step appears directly.
    await advanceThroughSQL(page);
    await selectBarrierAndAdvance(page, "None");

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("Memory", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "mem-standalone");
  });

  test("File + standalone server completes successfully", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "file-standalone");
    await selectStorageAndAdvance(page, "File");

    // Raft step is skipped for standalone; SQL step appears directly.
    await advanceThroughSQL(page);
    await selectBarrierAndAdvance(page, "None");

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("File", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "file-standalone");
  });

  test("Pebble + standalone server completes successfully", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "pebble-standalone");
    await selectStorageAndAdvance(page, "Pebble");

    // Raft step is skipped for standalone; SQL step appears directly.
    await advanceThroughSQL(page);
    await selectBarrierAndAdvance(page, "None");

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("Pebble", { exact: true })).toBeVisible();

    await createAndVerifyComplete(page, "pebble-standalone");
  });
});

// ---------------------------------------------------------------------------
// SQL Wire Protocol Combinations
// ---------------------------------------------------------------------------

test.describe("SQL Wire Protocol Combinations", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Enable MySQL only shows MySQL Enabled in summary", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "sql-mysql-only");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.

    // SQL step: enable only MySQL via its label.
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.locator("label").filter({ hasText: "MySQL" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    await selectBarrierAndAdvance(page, "None");

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    // SQL Wire Protocols section must be present when any protocol is enabled.
    await expect(page.getByText("SQL Wire Protocols")).toBeVisible();
    await expect(page.getByText("Enabled", { exact: true }).first()).toBeVisible();

    await createAndVerifyComplete(page, "sql-mysql-only");
  });

  test("Enable PostgreSQL only shows PostgreSQL Enabled in summary", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "sql-pgsql-only");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.

    // SQL step: enable only PostgreSQL via its label.
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.locator("label").filter({ hasText: "PostgreSQL" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    await selectBarrierAndAdvance(page, "None");

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("SQL Wire Protocols")).toBeVisible();
    await expect(page.getByText("Enabled", { exact: true }).first()).toBeVisible();

    await createAndVerifyComplete(page, "sql-pgsql-only");
  });

  test("Enable both MySQL and PostgreSQL shows both Enabled in summary", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "sql-both");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.

    // SQL step: enable both via their labels.
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.locator("label").filter({ hasText: "MySQL" }).click();
    await page.locator("label").filter({ hasText: "PostgreSQL" }).click();
    await page.getByRole("button", { name: "Next" }).click();

    await selectBarrierAndAdvance(page, "None");

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await expect(page.getByText("SQL Wire Protocols")).toBeVisible();
    // Both badges should read "Enabled".
    await expect(page.getByText("Enabled", { exact: true })).toHaveCount(2);

    await createAndVerifyComplete(page, "sql-both");
  });

  test("Disable both SQL protocols does not show SQL section in summary", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "sql-none");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.
    await advanceThroughSQL(page); // default: both unchecked
    await selectBarrierAndAdvance(page, "None");

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    // The SQL Wire Protocols section is only rendered when at least one is enabled.
    await expect(page.getByText("SQL Wire Protocols")).not.toBeVisible();

    await createAndVerifyComplete(page, "sql-none");
  });

  test("SQL step shows info alert when no protocol is selected", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "sql-info-alert");
    await selectStorageAndAdvance(page, "Pebble");
    // Raft step skipped for standalone — SQL step appears directly.

    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    // When both checkboxes are unchecked the info alert must be visible.
    await expect(
      page.getByText("No wire protocols selected."),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Standalone Step Sequence (Raft Skipped)
// ---------------------------------------------------------------------------

test.describe("Standalone Step Sequence", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("standalone server skips Raft step and lands on SQL after Storage", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "raft-standalone-skip");
    await selectStorageAndAdvance(page, "Pebble");

    // The Raft Configuration step must NOT appear; SQL comes directly after Storage.
    await expect(
      page.getByRole("heading", { name: "SQL Wire Protocols" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
    await expect(
      page.getByRole("heading", { name: "Raft Configuration" }),
    ).not.toBeVisible();
  });

  test("back from SQL step returns to Storage step on standalone", async ({ page }) => {
    await openCreateWizard(page);
    await fillNameAndAdvance(page, "raft-back-nav");
    await selectStorageAndAdvance(page, "Pebble");

    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Back", exact: true }).click();

    // Back from SQL must return to Storage (not Raft) on a standalone server.
    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Validation Edge Cases
// ---------------------------------------------------------------------------

test.describe("Validation Edge Cases", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Empty database name keeps Next button disabled", async ({ page }) => {
    await openCreateWizard(page);
    // Name input is initially empty -- Next should be disabled.
    const nextButton = page.getByRole("button", { name: "Next" });
    await expect(nextButton).toBeDisabled();
  });

  test("Database name with invalid characters shows validation error", async ({ page }) => {
    await openCreateWizard(page);
    // Names with uppercase letters are invalid per the namePattern.
    await page.locator("#db-name").fill("My_Database!");
    await expect(
      page.getByText(
        "Name must be lowercase alphanumeric with hyphens only, starting with a letter or number.",
      ),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  test("Database name with only whitespace shows validation error", async ({ page }) => {
    await openCreateWizard(page);
    // Whitespace-only name fails the namePattern (no match on first char).
    await page.locator("#db-name").fill("   ");
    await expect(
      page.getByText(
        "Name must be lowercase alphanumeric with hyphens only, starting with a letter or number.",
      ),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  test("Valid database name enables Next button", async ({ page }) => {
    await openCreateWizard(page);
    await page.locator("#db-name").fill("valid-db-name");
    await expect(page.getByRole("button", { name: "Next" })).toBeEnabled();
  });
});
