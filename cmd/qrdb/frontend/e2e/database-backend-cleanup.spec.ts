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
 * Database wizard backend and Raft configuration tests.
 *
 * These tests verify:
 *   1. The Storage Backend step only surfaces Memory, File, and Pebble options —
 *      "Raft" must never appear there (Raft is a consensus layer, not a storage
 *      backend).
 *   2. The Raft Configuration step is skipped entirely for standalone servers.
 *      After Storage, the wizard lands directly on SQL Wire Protocols.
 *   3. Back navigation from SQL returns to Storage (not Raft) on standalone.
 *
 * Wizard step sequence for standalone:
 *   name → storage → sql → barrier → summary
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for heading and step transitions. */
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
 * Advances through the wizard from the Name step to the Storage Backend step.
 * The caller lands on the Storage Backend heading.
 */
async function navigateToStorage(page: Page): Promise<void> {
  await openWizard(page);

  // Step 1 — Name & Engine: fill name and proceed.
  await page.locator("#db-name").fill("backend-test");
  await page.getByRole("button", { name: "Next" }).click();

  // Step 2 — Storage Backend.
  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Advances through the wizard from the Name step to the SQL Wire Protocols
 * step via Pebble storage.  The Raft Configuration step is skipped for
 * standalone servers, so the caller lands on the SQL Wire Protocols heading.
 */
async function navigateToSQL(page: Page): Promise<void> {
  await navigateToStorage(page);

  // Storage step: Pebble is already the default; just proceed.
  await page.getByRole("button", { name: "Next" }).click();

  // SQL Wire Protocols step — Raft is skipped for standalone servers.
  await expect(
    page.getByRole("heading", { name: "SQL Wire Protocols" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Backend & Standalone Navigation", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  // -------------------------------------------------------------------------
  // Storage backend options
  // -------------------------------------------------------------------------

  test("database backend options do not include Raft", async ({ page }) => {
    await navigateToStorage(page);

    const main = page.locator("main");

    // All three legitimate backends must be present.
    await expect(
      main.locator("label").filter({ hasText: "Memory" }),
    ).toBeVisible();
    await expect(
      main.locator("label").filter({ hasText: "File" }),
    ).toBeVisible();
    await expect(
      main.locator("label").filter({ hasText: "Pebble" }),
    ).toBeVisible();

    // "Raft" must never appear as a storage backend option card label.
    const backendLabels = main.locator(
      'label:has(input[name="db-storage-backend"])',
    );
    await expect(backendLabels).toHaveCount(3);
    const labelTexts: string[] = await backendLabels.evaluateAll(
      (els: Element[]) => els.map((el) => el.textContent ?? ""),
    );
    for (const text of labelTexts) {
      expect(text.toLowerCase()).not.toContain("raft");
    }

    // The three radio inputs must correspond to memory / file / pebble.
    const radios = main.locator('input[name="db-storage-backend"]');
    await expect(radios).toHaveCount(3);

    // None of the radio values should be "raft".
    const values: string[] = await radios.evaluateAll(
      (els: Element[]) =>
        els.map((el) => (el as HTMLInputElement).value),
    );
    expect(values).not.toContain("raft");
    expect(values).toContain("memory");
    expect(values).toContain("file");
    expect(values).toContain("pebble");
  });

  // -------------------------------------------------------------------------
  // Standalone server — Raft step is skipped entirely
  // -------------------------------------------------------------------------

  test("standalone server skips Raft step and lands on SQL after Storage", async ({
    page,
  }) => {
    await navigateToSQL(page);

    // The Raft Configuration heading must not be visible — it is skipped.
    await expect(
      page.getByRole("heading", { name: "Raft Configuration" }),
    ).not.toBeVisible();
  });

  test("standalone server next button from Storage lands on SQL step", async ({
    page,
  }) => {
    await navigateToStorage(page);

    await page.getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "SQL Wire Protocols" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("back button from SQL step returns to Storage step on standalone", async ({
    page,
  }) => {
    await navigateToSQL(page);

    await page.getByRole("button", { name: "Back", exact: true }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
  });

  test("back button from SQL step does not show Raft Configuration heading", async ({
    page,
  }) => {
    await navigateToSQL(page);

    await page.getByRole("button", { name: "Back", exact: true }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(
      page.getByRole("heading", { name: "Raft Configuration" }),
    ).not.toBeVisible();
  });
});
