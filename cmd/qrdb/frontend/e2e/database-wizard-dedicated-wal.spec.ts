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
 * Database creation wizard — Dedicated WAL toggle tests.
 *
 * The Dedicated WAL toggle is a raft-only feature:
 *   - It must NOT appear when connected to a standalone server (the Raft step
 *     is skipped entirely, so the toggle is never rendered).
 *   - It must appear inside the Raft Groups container when connected to a raft
 *     server with serverMode === "raft".
 *   - When checked, `dedicatedWal: true` must be present in the
 *     CreateDatabaseRequest forwarded to DatabaseBinding.Create.
 *   - When left unchecked (default), `dedicatedWal` must be absent or falsy.
 *
 * Navigation flow for a raft server:
 *   Name → Storage → Raft (Dedicated WAL toggle here) → SQL → Barrier → Summary → Create
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  raftClusterState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

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
 * Advances from the Name step to the Raft Configuration step.
 * Caller must use a raft-mode mock state to ensure the Raft step renders.
 */
async function advanceToRaftStep(page: Page): Promise<void> {
  await openWizard(page);

  await page.locator("#db-name").fill("wal-test-db");
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Raft Configuration" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/**
 * Advances from the Raft step through all remaining steps to the Summary
 * and clicks "Create Database", triggering the actual database creation.
 */
async function advanceRaftToCreate(page: Page): Promise<void> {
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
  await page.locator("main").getByRole("button", { name: "Create Database" }).click();
}

// ---------------------------------------------------------------------------
// Standalone — toggle must not appear
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Dedicated WAL — standalone server", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Dedicated WAL toggle does not appear on a standalone server", async ({
    page,
  }) => {
    await openWizard(page);

    await page.locator("#db-name").fill("standalone-db");
    await page.getByRole("button", { name: "Next" }).click();

    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.getByRole("button", { name: "Next" }).click();

    // Standalone skips the Raft step; we land directly on Protocols.
    await expect(
      page.getByRole("heading", { name: "Protocols" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(page.getByTestId("db-dedicated-wal-toggle")).not.toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Raft server — toggle renders and propagates to the create request
// ---------------------------------------------------------------------------

test.describe("Database Wizard - Dedicated WAL — raft server", () => {
  test.use({ mockState: raftClusterState });

  test("Dedicated WAL toggle appears inside the Raft Groups section", async ({
    page,
  }) => {
    await advanceToRaftStep(page);

    const main = page.locator("main");
    await expect(
      main.getByText("Raft Groups", { exact: true }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await expect(main.getByTestId("db-dedicated-wal-toggle")).toBeVisible();
  });

  test("Dedicated WAL toggle is unchecked by default", async ({ page }) => {
    await advanceToRaftStep(page);
    await expect(page.getByTestId("db-dedicated-wal-toggle")).not.toBeChecked();
  });

  test("checking Dedicated WAL sends dedicatedWal: true in the create request", async ({
    page,
  }) => {
    // Register the capture wrapper BEFORE page.goto so it runs after
    // injectWailsMock but before the app boots, wrapping the raw mock.
    await page.addInitScript(() => {
      const w = window as unknown as Record<string, unknown>;
      w.__capturedDbCreateRequests = [];
      const db = (
        w.go as Record<string, Record<string, Record<string, unknown>>>
      )?.main?.DatabaseBinding;
      if (db && typeof db.Create === "function") {
        const orig = db.Create as (...args: unknown[]) => Promise<unknown>;
        db.Create = async (...args: unknown[]): Promise<unknown> => {
          (w.__capturedDbCreateRequests as unknown[]).push(
            JSON.parse(JSON.stringify(args[0])),
          );
          return orig(...args);
        };
      }
    });

    await advanceToRaftStep(page);

    const main = page.locator("main");
    await expect(
      main.getByText("Raft Groups", { exact: true }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Check the toggle before advancing.
    await main.getByTestId("db-dedicated-wal-toggle").check();
    await expect(main.getByTestId("db-dedicated-wal-toggle")).toBeChecked();

    await advanceRaftToCreate(page);

    // Wait until DatabaseBinding.Create has been called.
    await page.waitForFunction(
      () =>
        ((window as unknown as Record<string, unknown>)
          .__capturedDbCreateRequests as unknown[]).length > 0,
      { timeout: STEP_TIMEOUT },
    );

    const captured = await page.evaluate(
      () =>
        (window as unknown as Record<string, unknown>)
          .__capturedDbCreateRequests as Record<string, unknown>[],
    );
    expect(captured.length).toBeGreaterThan(0);
    expect(captured[0].dedicatedWal).toBe(true);
  });

  test("leaving Dedicated WAL unchecked omits dedicatedWal from the create request", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      const w = window as unknown as Record<string, unknown>;
      w.__capturedDbCreateRequests = [];
      const db = (
        w.go as Record<string, Record<string, Record<string, unknown>>>
      )?.main?.DatabaseBinding;
      if (db && typeof db.Create === "function") {
        const orig = db.Create as (...args: unknown[]) => Promise<unknown>;
        db.Create = async (...args: unknown[]): Promise<unknown> => {
          (w.__capturedDbCreateRequests as unknown[]).push(
            JSON.parse(JSON.stringify(args[0])),
          );
          return orig(...args);
        };
      }
    });

    await advanceToRaftStep(page);

    // Do NOT check the toggle — leave it at the default (unchecked).
    await expect(page.getByTestId("db-dedicated-wal-toggle")).not.toBeChecked();

    await advanceRaftToCreate(page);

    await page.waitForFunction(
      () =>
        ((window as unknown as Record<string, unknown>)
          .__capturedDbCreateRequests as unknown[]).length > 0,
      { timeout: STEP_TIMEOUT },
    );

    const captured = await page.evaluate(
      () =>
        (window as unknown as Record<string, unknown>)
          .__capturedDbCreateRequests as Record<string, unknown>[],
    );
    expect(captured.length).toBeGreaterThan(0);
    // dedicatedWal must be absent or falsy when the toggle is off.
    expect(captured[0].dedicatedWal).toBeFalsy();
  });
});
