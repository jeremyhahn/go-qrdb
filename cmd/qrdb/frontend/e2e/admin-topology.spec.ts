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
 * Admin Topology view tests.
 *
 * The Topology admin view is only accessible in raft cluster mode
 * (serverMode === "raft"). It renders:
 *   - A "Node" summary card showing node ID, Raft address, and shard count.
 *   - A "Shard Placement" table with one row per shard, showing shard ID,
 *     replica ID, leader/follower role badge, leader ID, and member nodes.
 *
 * These tests cover:
 *   1. Navigation visibility — "Topology" item absent in standalone mode,
 *      present in raft mode.
 *   2. Node summary card renders with the values from AdminBinding.GetTopology.
 *   3. Shard table renders with the correct number of rows and shard data.
 *   4. Leader badge appears on the leader shard row.
 *   5. Refresh button triggers a re-fetch without clearing data.
 *   6. Error state when GetTopology rejects shows an error alert.
 *
 * The mock fixture (wails-mock.ts, AdminBinding.GetTopology) returns:
 *   { node_id: "node-1", raft_address: "127.0.0.1:63001", shard_count: 1,
 *     shards: [{ shard_id: 1, replica_id: 1, is_leader: true, leader_id: 1,
 *                nodes: { "1": "127.0.0.1:63001" } }] }
 * After the SDK's deepSnakeToCamel transform these become camelCase fields.
 */

import {
  test,
  expect,
  raftClusterState,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to root and waits for the app shell to reach the Dashboard. */
async function loadApp(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Clicks the Topology sidebar item and waits for the breadcrumb. */
async function navigateToTopology(page: Page): Promise<void> {
  await page.locator("aside").getByText("Topology").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Topology"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Navigation visibility — standalone (no Topology nav item)
// ---------------------------------------------------------------------------

test.describe("Admin - Topology nav visibility — standalone mode", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Topology nav item is NOT visible in standalone mode", async ({
    page,
  }) => {
    await loadApp(page);
    await expect(
      page.locator("aside").getByText("Topology"),
    ).not.toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Navigation visibility — raft mode (Topology item must appear)
// ---------------------------------------------------------------------------

test.describe("Admin - Topology nav visibility — raft mode", () => {
  test.use({ mockState: raftClusterState });

  test("Topology nav item IS visible in raft mode", async ({ page }) => {
    await loadApp(page);
    await expect(
      page.locator("aside").getByText("Topology"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Topology view — node summary and shard table
// ---------------------------------------------------------------------------

test.describe("Admin - Topology view", () => {
  test.use({ mockState: raftClusterState });

  test("navigating to Topology shows the view without JavaScript errors", async ({
    page,
  }) => {
    const errors: Error[] = [];
    page.on("pageerror", (err) => errors.push(err));

    await loadApp(page);
    await navigateToTopology(page);

    await page.waitForTimeout(300);
    expect(errors).toHaveLength(0);
  });

  test("node summary card is visible after successful fetch", async ({
    page,
  }) => {
    await loadApp(page);
    await navigateToTopology(page);

    await expect(
      page.locator("main").getByTestId("topology-node-summary"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("node summary shows node ID from mock fixture", async ({ page }) => {
    await loadApp(page);
    await navigateToTopology(page);

    await expect(
      page.locator("main").getByTestId("topology-node-id"),
    ).toContainText("node-1", { timeout: LOAD_TIMEOUT });
  });

  test("node summary shows raft address from mock fixture", async ({ page }) => {
    await loadApp(page);
    await navigateToTopology(page);

    await expect(
      page.locator("main").getByTestId("topology-raft-address"),
    ).toContainText("127.0.0.1:63001", { timeout: LOAD_TIMEOUT });
  });

  test("node summary shows shard count of 1 from mock fixture", async ({
    page,
  }) => {
    await loadApp(page);
    await navigateToTopology(page);

    await expect(
      page.locator("main").getByTestId("topology-shard-count"),
    ).toContainText("1", { timeout: LOAD_TIMEOUT });
  });

  test("Shard Placement heading is visible", async ({ page }) => {
    await loadApp(page);
    await navigateToTopology(page);

    await expect(
      page.locator("main").getByRole("heading", { name: "Shard Placement" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shards table renders one row for the single mock shard", async ({
    page,
  }) => {
    await loadApp(page);
    await navigateToTopology(page);

    const main = page.locator("main");
    await expect(
      main.getByTestId("topology-shards-table"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(main.getByTestId("topology-shard-row")).toHaveCount(1);
  });

  test("shard row shows Leader badge for the leader replica", async ({
    page,
  }) => {
    await loadApp(page);
    await navigateToTopology(page);

    const main = page.locator("main");
    await expect(
      main.getByTestId("topology-shards-table"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Mock returns is_leader: true → badge text "Leader".
    await expect(
      main
        .getByTestId("topology-shard-row")
        .locator(".badge")
        .filter({ hasText: "Leader" }),
    ).toBeVisible();
  });

  test("shard row does not show Follower badge when replica is leader", async ({
    page,
  }) => {
    await loadApp(page);
    await navigateToTopology(page);

    const main = page.locator("main");
    await expect(
      main.getByTestId("topology-shards-table"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(
      main
        .getByTestId("topology-shard-row")
        .locator(".badge")
        .filter({ hasText: "Follower" }),
    ).not.toBeVisible();
  });

  test("Refresh button on node summary card triggers re-fetch and data stays", async ({
    page,
  }) => {
    await loadApp(page);
    await navigateToTopology(page);

    const main = page.locator("main");
    const nodeSummary = main.getByTestId("topology-node-summary");
    await expect(nodeSummary).toBeVisible({ timeout: LOAD_TIMEOUT });

    await nodeSummary.getByRole("button", { name: "Refresh" }).click();

    // Data must still be present after the refresh cycle completes.
    await expect(main.getByTestId("topology-node-id")).toContainText(
      "node-1",
      { timeout: LOAD_TIMEOUT },
    );
    await expect(main.getByTestId("topology-shards-table")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Error state
  // -------------------------------------------------------------------------

  test("GetTopology error shows alert-error inside Shard Placement card", async ({
    page,
  }) => {
    await loadApp(page);
    await injectError(page, "AdminBinding.GetTopology", "topology unavailable");

    await navigateToTopology(page);

    await expect(
      page.locator("main").locator(".alert-error"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await clearError(page, "AdminBinding.GetTopology");
  });
});
