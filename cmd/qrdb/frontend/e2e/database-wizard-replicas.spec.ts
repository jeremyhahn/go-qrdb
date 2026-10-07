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
 * Database creation wizard — replication factor (replicas) test.
 *
 * On a Raft-mode server the wizard's Raft Configuration step exposes a
 * Replication Factor (Replicas) input. This test drives the wizard to create a
 * database on a (mocked) raft-mode server and asserts that the assembled
 * CreateDatabaseRequest carries the `replicas` field. `replicas` is a single
 * word, so its camelCase form equals its snake_case wire form.
 *
 * The connection-manager binding is overridden to present a raft-mode server
 * (mirroring database-wizard-raft-config.spec.ts) so the Raft step renders, and
 * DatabaseBinding.Create is overridden to capture the request the SDK submits.
 */

import { test, expect, secondLaunchEmbeddedState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const STEP_TIMEOUT = 10_000;

/**
 * Overrides the Wails bindings so the active connection is a raft-mode server
 * and DatabaseBinding.Create records the submitted request on window.
 */
async function installRaftAndCapture(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;
    const origGo = w.go as Record<string, unknown>;
    if (!origGo) return;
    const main = origGo.main as Record<string, Record<string, unknown>>;
    if (!main) return;

    // Present a raft-mode server so the wizard shows the Raft step and sets
    // serverMode === "raft".
    const cmb = main.ConnectionManagerBinding as Record<string, unknown>;
    if (cmb) {
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
    }

    // Capture the create request the SDK submits.
    const dbb = main.DatabaseBinding as Record<string, unknown>;
    if (dbb) {
      dbb.Create = async (req: Record<string, unknown>) => {
        w.__dbCreateReq = req;
        return {
          name: req.name,
          backend: (req.backend as string) || "pebble",
          status: "open",
        };
      };
    }
  });
}

test.describe("Database Wizard - replicas (raft server)", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("wizard sends replicas in the create request for a raft database", async ({
    page,
  }) => {
    await installRaftAndCapture(page);

    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.locator("aside").getByText("New Database").click();
    await expect(
      page.getByRole("heading", { name: "Database Name & Engine" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });

    // Step 1 — Name.
    await page.locator("#db-name").fill("replicas-test");
    await page.getByRole("button", { name: "Next" }).click();

    // Step 2 — Storage Backend (Pebble default).
    await expect(
      page.getByRole("heading", { name: "Storage Backend" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.getByRole("button", { name: "Next" }).click();

    // Step 3 — Raft Configuration: set the replication factor (replicas).
    await expect(
      page.getByRole("heading", { name: "Raft Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    const main = page.locator("main");
    await expect(main.locator("#db-replication-factor")).toBeVisible();
    await main.locator("#db-replication-factor").fill("3");
    await page.getByRole("button", { name: "Next" }).click();

    // Step 4 — Protocols.
    await expect(
      page.getByRole("heading", { name: "Protocols" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.getByRole("button", { name: "Next" }).click();

    // Step 5 — Barrier (default mode is valid; advance).
    await expect(
      page.getByRole("heading", { name: "Database Encryption Barrier" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.getByRole("button", { name: "Next" }).click();

    // Step 6 — Summary: trigger creation.
    await expect(
      page.getByRole("heading", { name: "Review Configuration" }),
    ).toBeVisible({ timeout: STEP_TIMEOUT });
    await page.getByRole("button", { name: "Create Database" }).click();

    // The captured request must carry the replication factor.
    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (window as unknown as { __dbCreateReq?: { replicas?: number } })
                .__dbCreateReq?.replicas ?? null,
          ),
        { timeout: STEP_TIMEOUT },
      )
      .toBe(3);
  });
});
