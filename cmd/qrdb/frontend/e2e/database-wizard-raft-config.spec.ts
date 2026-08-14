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
 * Database creation wizard Raft Configuration step tests.
 *
 * These tests cover two scenarios:
 *
 *   Standalone server (secondLaunchEmbeddedState, serverMode "standalone"):
 *     The Raft Configuration step is skipped entirely. After Storage Backend,
 *     the wizard lands directly on SQL Wire Protocols. The Raft step heading
 *     is never shown and Back from SQL returns to Storage (not Raft).
 *
 *   Raft server (mock profile overridden to serverMode "raft"):
 *     The Raft Configuration step IS shown. It exposes "Raft Cluster Detected"
 *     with the "Replication Factor" and "Number of Shards" inputs under a
 *     "Raft Groups" divider.
 *
 * Navigation for standalone:
 *   Name → Storage → SQL   (Raft step absent)
 *
 * Navigation for raft:
 *   Name → Storage → Raft → SQL
 */

import { test, expect, secondLaunchEmbeddedState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Maximum wait time for step transitions and heading visibility. */
const STEP_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Opens the Create Database wizard from the landing page. */
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
 * Navigates from the Name step to the SQL Wire Protocols step on a standalone
 * server.  The Raft step is skipped entirely — Storage is followed immediately
 * by SQL.
 */
async function navigateToSQL(page: Page): Promise<void> {
  await openWizard(page);

  // Step 1 — Name & Engine: use a unique name with the default KV engine.
  await page.locator("#db-name").fill("raft-config-test");
  await page.getByRole("button", { name: "Next" }).click();

  // Step 2 — Storage Backend: Pebble is the default; advance without changes.
  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  // Confirm we are on the SQL step (Raft is skipped for standalone).
  await expect(
    page.getByRole("heading", { name: "Protocols" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Navigates to the Raft Configuration step using a mock profile override
 * that presents a raft-mode server.  The caller must have already injected
 * the raft profile via page.addInitScript before calling this.
 */
async function navigateToRaftWithRaftServer(page: Page): Promise<void> {
  await openWizard(page);

  // Step 1 — Name & Engine.
  await page.locator("#db-name").fill("raft-config-test");
  await page.getByRole("button", { name: "Next" }).click();

  // Step 2 — Storage Backend.
  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  // Confirm we are on the Raft Configuration step.
  await expect(
    page.getByRole("heading", { name: "Raft Configuration" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Advances from the Raft step through SQL and Barrier to reach Summary.
 */
async function advanceRaftToSummary(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Protocols" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Database Encryption Barrier" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Review Configuration" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Raft Configuration", () => {
  // The secondLaunchEmbeddedState mock connects to a standalone embedded
  // server (serverMode === "standalone"), so the Raft step is skipped.
  test.use({ mockState: secondLaunchEmbeddedState });

  // -------------------------------------------------------------------------
  // Standalone server — Raft step is skipped entirely
  // -------------------------------------------------------------------------

  test("standalone server skips Raft Configuration step entirely", async ({ page }) => {
    await openWizard(page);

    await page.locator("#db-name").fill("raft-skip-test");
    await page.getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.getByRole("button", { name: "Next" }).click();

    // The Raft Configuration heading must NOT appear for standalone.
    await expect(
      page.getByRole("heading", { name: "Protocols" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(
      page.getByRole("heading", { name: "Raft Configuration" }),
    ).not.toBeVisible();
  });

  test("standalone server Storage Next button lands on SQL step", async ({ page }) => {
    await navigateToSQL(page);

    // We must be on SQL — already asserted inside navigateToSQL.
    await expect(
      page.getByRole("heading", { name: "Protocols" }),
    ).toBeVisible();
  });

  test("standalone server Back from SQL returns to Storage Backend", async ({ page }) => {
    await navigateToSQL(page);

    await page.getByRole("button", { name: "Back", exact: true }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("standalone server Back from SQL does not land on Raft Configuration", async ({ page }) => {
    await navigateToSQL(page);

    await page.getByRole("button", { name: "Back", exact: true }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(
      page.getByRole("heading", { name: "Raft Configuration" }),
    ).not.toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Raft groups inputs (present when connected to a raft-mode server)
  // -------------------------------------------------------------------------

  test("raft server shows Raft Groups divider and inputs", async ({ page }) => {
    // Patch the mock state to present a raft-mode server connection.
    await page.addInitScript(() => {
      const w = window as unknown as Record<string, unknown>;
      // Override ConnectionManagerBinding to return raft server info.
      const origGo = w.go as Record<string, unknown>;
      if (!origGo) return;
      const main = origGo.main as Record<string, Record<string, unknown>>;
      if (!main) return;
      const cmb = main.ConnectionManagerBinding as Record<string, unknown>;
      if (!cmb) return;
      cmb.GetActiveProfileID = async () => "raft-profile";
      cmb.ListProfiles = async () => [
        {
          id: "raft-profile",
          name: "Raft Server",
          type: "embedded",
          serverMode: "raft",
          embedded: {
            raftEnabled: true,
            dataDir: "/data",
            sealConfig: { mode: "none" },
          },
          createdAt: new Date().toISOString(),
        },
      ];
      cmb.GetState = async () => ({
        profileId: "raft-profile",
        status: "connected",
        isEmbedded: true,
      });
    });

    await navigateToRaftWithRaftServer(page);

    const main = page.locator("main");

    // When the server is raft-enabled the Raft Groups section is visible.
    await expect(
      main.getByText("Raft Groups", { exact: true }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    await expect(main.locator("#db-replication-factor")).toBeVisible();
    await expect(main.locator("#db-shard-count")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Raft group config flows through to summary
  // -------------------------------------------------------------------------

  test("raft group values appear in summary after advancing", async ({ page }) => {
    // Override the mock profile to raft mode.
    await page.addInitScript(() => {
      const w = window as unknown as Record<string, unknown>;
      const origGo = w.go as Record<string, unknown>;
      if (!origGo) return;
      const main = origGo.main as Record<string, Record<string, unknown>>;
      if (!main) return;
      const cmb = main.ConnectionManagerBinding as Record<string, unknown>;
      if (!cmb) return;
      cmb.GetActiveProfileID = async () => "raft-profile";
      cmb.ListProfiles = async () => [
        {
          id: "raft-profile",
          name: "Raft Server",
          type: "embedded",
          serverMode: "raft",
          embedded: {
            raftEnabled: true,
            dataDir: "/data",
            sealConfig: { mode: "none" },
          },
          createdAt: new Date().toISOString(),
        },
      ];
      cmb.GetState = async () => ({
        profileId: "raft-profile",
        status: "connected",
        isEmbedded: true,
      });
    });

    await navigateToRaftWithRaftServer(page);

    const main = page.locator("main");

    // Wait for the raft groups section to be visible.
    await expect(
      main.getByText("Raft Groups", { exact: true }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Set shard count to 3 and replication factor to 3.
    await main.locator("#db-shard-count").fill("3");
    await main.locator("#db-replication-factor").fill("3");

    // Advance through the remaining steps to the Summary.
    await advanceRaftToSummary(page);

    // The Summary step's Raft Cluster section must be visible.
    await expect(main.getByText("Raft Cluster", { exact: true })).toBeVisible();
  });
});
