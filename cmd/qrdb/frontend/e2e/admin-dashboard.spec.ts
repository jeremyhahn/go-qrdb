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

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  raftClusterState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock state with rich AdminBinding responses
// ---------------------------------------------------------------------------

/**
 * Replaces the mock AdminBinding methods with ones returning realistic data.
 * It runs in the browser, so it must stay self-contained.
 */
function installAdminBindings(): void {
  const w = window as unknown as Record<string, unknown>;
  const go = w.go as {
    main: {
      AdminBinding: Record<
        string,
        (...args: unknown[]) => Promise<unknown>
      >;
    };
  };

  go.main.AdminBinding.Health = async () => ({
    status: "ok",
    isLeader: true,
  });

  go.main.AdminBinding.Status = async () => ({
    nodeId: 42,
    raftAddress: "10.0.0.1:63001",
    isLeader: true,
    leaderId: 42,
    shardCount: 3,
    isDraining: false,
  });

  go.main.AdminBinding.Capabilities = async () => ({
    kv: true,
    mvcc: true,
    transaction: true,
    batch: true,
    watch: true,
    lease: true,
    index: true,
    timeSeries: false,
    vector: false,
  });

  go.main.AdminBinding.Peers = async () => ({
    shards: [
      {
        shardId: 1,
        peers: [
          { replicaId: 1, address: "10.0.0.1:63001", isLeader: true },
          { replicaId: 2, address: "10.0.0.2:63001", isLeader: false },
        ],
      },
      {
        shardId: 2,
        peers: [
          { replicaId: 1, address: "10.0.0.1:63002", isLeader: false },
          { replicaId: 3, address: "10.0.0.3:63002", isLeader: true },
        ],
      },
    ],
  });
}

/**
 * Installs the realistic AdminBinding methods before the app's scripts run on
 * every later navigation, so the dashboard's first fetch already returns them.
 * Must be called before page.goto(); this init script runs after the mock's.
 */
async function overrideAdminBindingsOnLoad(page: Page): Promise<void> {
  await page.addInitScript(installAdminBindings);
}

/**
 * Installs the realistic AdminBinding methods on the already-loaded page. A
 * mounted view sees them only on its next fetch.
 */
async function overrideAdminBindings(page: Page): Promise<void> {
  await page.evaluate(installAdminBindings);
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to admin mode and waits for the Dashboard to load. */
async function navigateToDashboard(page: Page): Promise<void> {
  // Dashboard is the landing view, so its first fetch must see the overrides.
  await overrideAdminBindingsOnLoad(page);

  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Navigates to admin mode and clicks a specific sidebar link. */
async function navigateToAdminSection(
  page: Page,
  section: string,
): Promise<void> {
  await overrideAdminBindingsOnLoad(page);

  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Admin sections are always in the sidebar — no toggle required.
  await page.locator("aside").getByText(section).click();
  await expect(
    page.locator(".breadcrumbs").getByText(section),
  ).toBeVisible();
}

// ---------------------------------------------------------------------------
// Dashboard Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Dashboard", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("displays health status badge", async ({ page }) => {
    await navigateToDashboard(page);

    // Scope to main content area to avoid collisions with footer/header
    const main = page.locator("main");

    // Wait for the dashboard data to load
    await expect(main.locator(".stat-title", { hasText: "Health" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Should show "ok" status badge
    await expect(main.locator(".badge-success").getByText("ok")).toBeVisible();
  });

  test("displays node ID and raft address", async ({ page }) => {
    await navigateToDashboard(page);

    const main = page.locator("main");

    // Node ID stat
    await expect(main.getByText("Node ID")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.getByText("42", { exact: true })).toBeVisible();

    // Raft address in the stat description
    await expect(main.getByText("10.0.0.1:63001")).toBeVisible();
  });

  test("displays leader status", async ({ page }) => {
    await navigateToDashboard(page);

    const main = page.locator("main");

    // The stat-title "Leader"
    await expect(main.locator(".stat-title", { hasText: "Leader" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // isLeader is true, so should show "Yes" badge
    await expect(
      main.locator(".badge-primary").getByText("Yes"),
    ).toBeVisible();
  });

  test("displays shard count", async ({ page }) => {
    await navigateToDashboard(page);

    const main = page.locator("main");

    await expect(main.locator(".stat-title", { hasText: "Shards" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.getByText("Loaded on this node")).toBeVisible();
  });

  test("displays draining status", async ({ page }) => {
    await navigateToDashboard(page);

    const main = page.locator("main");

    await expect(main.locator(".stat-title", { hasText: "Draining" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // isDraining is false
    await expect(main.getByText("Drain mode status")).toBeVisible();
  });

  test("displays capabilities list", async ({ page }) => {
    await navigateToDashboard(page);

    const main = page.locator("main");

    await expect(
      main.getByRole("heading", { name: "Capabilities" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Enabled capabilities
    await expect(main.getByText("KV", { exact: true })).toBeVisible();
    await expect(main.getByText("MVCC", { exact: true })).toBeVisible();
    await expect(main.getByText("Transactions")).toBeVisible();
    await expect(main.getByText("Batch", { exact: true })).toBeVisible();
    await expect(main.getByText("Watch", { exact: true })).toBeVisible();
    await expect(main.getByText("Leases")).toBeVisible();

    // Disabled capabilities should still appear (with ghost badge)
    await expect(main.getByText("Time Series")).toBeVisible();
    await expect(main.getByText("Vectors")).toBeVisible();
  });

  test.describe("with databases", () => {
    // Seeded through the fixture, so the dashboard's first fetch lists them.
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        databases: [
          { name: "mydb", backend: "pebble", status: "open" },
          { name: "tsdb", backend: "pebble", status: "open" },
        ],
      },
    });

    test("displays databases table when databases exist", async ({ page }) => {
      await navigateToDashboard(page);

      const main = page.locator("main");

      // Wait for database heading to render in main content
      await expect(
        main.getByRole("heading", { name: "Databases" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(main.getByText("mydb")).toBeVisible();
      await expect(main.getByText("tsdb")).toBeVisible();
    });
  });

  test("shows error alert when dashboard data fails to load", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Inject error on AdminBinding.Health (which is part of the 3-call fetch)
    await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as {
        main: {
          AdminBinding: Record<
            string,
            (...args: unknown[]) => Promise<unknown>
          >;
        };
      };
      go.main.AdminBinding.Health = async () => {
        throw new Error("connection refused");
      };
      go.main.AdminBinding.Status = async () => {
        throw new Error("connection refused");
      };
      go.main.AdminBinding.Capabilities = async () => {
        throw new Error("connection refused");
      };
    });

    // Navigate to Dashboard — this triggers the data fetch that will fail.
    await page.locator("aside").getByText("Dashboard", { exact: true }).click();
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Error alert or toast should appear
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("retry button refetches dashboard data after error", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Navigate away first so that navigating back to Dashboard triggers a remount.
    // Use Seal Manager (always visible in standalone mode) rather than Cluster.
    await page.locator(`aside button[title="Seal Manager"]`).click();
    await expect(
      page.locator(".breadcrumbs").getByText("Seal Manager"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Inject errors
    await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as {
        main: {
          AdminBinding: Record<
            string,
            (...args: unknown[]) => Promise<unknown>
          >;
        };
      };
      go.main.AdminBinding.Health = async () => {
        throw new Error("temporary error");
      };
      go.main.AdminBinding.Status = async () => {
        throw new Error("temporary error");
      };
      go.main.AdminBinding.Capabilities = async () => {
        throw new Error("temporary error");
      };
    });

    // Navigate to Dashboard — this triggers the data fetch that will fail.
    await page.locator(`aside button[title="Dashboard"]`).click();
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Wait for error state - use main scope to find the retry button
    const main = page.locator("main");
    const errorAlert = main.locator(".alert-error");
    await expect(errorAlert).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Fix the bindings
    await overrideAdminBindings(page);

    // Click Retry
    await errorAlert.getByRole("button", { name: "Retry" }).click();

    // Dashboard should load successfully
    await expect(main.locator(".stat-title", { hasText: "Health" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.locator(".badge-success").getByText("ok")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Cluster Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Cluster", () => {
  test.use({ mockState: raftClusterState });

  test("displays cluster peers table", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");

    // Should show the Cluster Peers card
    await expect(main.getByText("Cluster Peers")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Should show peer data from the overridden bindings
    await expect(main.getByText("10.0.0.1:63001")).toBeVisible();
    await expect(main.getByText("10.0.0.2:63001")).toBeVisible();
    await expect(main.getByText("10.0.0.3:63002")).toBeVisible();

    // Should show Leader and Follower badges
    await expect(main.locator(".badge-primary").getByText("Leader").first()).toBeVisible();
    await expect(main.locator(".badge-ghost").getByText("Follower").first()).toBeVisible();
  });

  test("displays peer table columns", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");

    await expect(main.getByText("Cluster Peers")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Verify table headers using th elements
    const peersCard = main.locator(".card", { hasText: "Cluster Peers" });
    await expect(peersCard.locator("th", { hasText: "Shard ID" })).toBeVisible();
    await expect(peersCard.locator("th", { hasText: "Replica ID" })).toBeVisible();
    await expect(peersCard.locator("th", { hasText: "Address" })).toBeVisible();
    await expect(peersCard.locator("th", { hasText: "Role" })).toBeVisible();
  });

  test("shows Refresh button for peers", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");

    await expect(main.getByText("Cluster Peers")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await expect(
      main.getByRole("button", { name: "Refresh" }),
    ).toBeVisible();
  });

  test("shows Add Member form", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");
    const addCard = main.locator(".card", { hasText: "Add Member" });

    await expect(addCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Should show input fields within the Add Member card
    await expect(addCard.getByPlaceholder("Shard ID")).toBeVisible();
    await expect(addCard.getByPlaceholder("Replica ID")).toBeVisible();
    await expect(
      addCard.getByPlaceholder("Raft Address (e.g. 10.0.0.2:63001)"),
    ).toBeVisible();

    // Should show the Add Member button
    await expect(
      addCard.getByRole("button", { name: "Add Member" }),
    ).toBeVisible();
  });

  test("shows Remove Member form", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");
    const removeCard = main.locator(".card", { hasText: "Remove Member" });

    await expect(removeCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Should show the Remove Member button
    await expect(
      removeCard.getByRole("button", { name: "Remove Member" }),
    ).toBeVisible();
  });

  test("add member submits successfully", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");
    const addCard = main.locator(".card", { hasText: "Add Member" });

    await expect(addCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Fill in the add member form
    await addCard.getByPlaceholder("Shard ID").fill("3");
    await addCard.getByPlaceholder("Replica ID").fill("1");
    await addCard
      .getByPlaceholder("Raft Address (e.g. 10.0.0.2:63001)")
      .fill("10.0.0.4:63003");

    // Click Add Member
    await addCard.getByRole("button", { name: "Add Member" }).click();

    // Toast should confirm addition
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
  });

  test("remove member submits successfully", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");
    const removeCard = main.locator(".card", { hasText: "Remove Member" });

    await expect(removeCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Fill in the remove member form
    await removeCard.getByPlaceholder("Shard ID").fill("1");
    await removeCard.getByPlaceholder("Replica ID").fill("2");

    // Click Remove Member
    await removeCard
      .getByRole("button", { name: "Remove Member" })
      .click();

    // Toast should confirm removal
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
  });

  test("shows Request Snapshot form", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");
    const snapshotCard = main.locator(".card", { hasText: "Request Snapshot" });

    await expect(snapshotCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(
      snapshotCard.getByRole("button", { name: "Request Snapshot" }),
    ).toBeVisible();
  });

  test("shows Drain Node section with confirmation", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");
    const drainCard = main.locator(".card", { hasText: "Drain Node" });

    await expect(drainCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click Drain button to show confirmation
    await drainCard
      .getByRole("button", { name: "Drain", exact: true })
      .click();

    // Confirmation dialog should appear
    await expect(drainCard.getByText("Are you sure?")).toBeVisible();
    await expect(
      drainCard.getByRole("button", { name: "Confirm Drain" }),
    ).toBeVisible();
    await expect(
      drainCard.getByRole("button", { name: "Cancel" }),
    ).toBeVisible();
  });

  test("drain cancel hides confirmation", async ({ page }) => {
    await navigateToAdminSection(page, "Cluster");

    const main = page.locator("main");
    const drainCard = main.locator(".card", { hasText: "Drain Node" });

    await expect(drainCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Show confirmation
    await drainCard
      .getByRole("button", { name: "Drain", exact: true })
      .click();
    await expect(
      drainCard.getByRole("button", { name: "Confirm Drain" }),
    ).toBeVisible();

    // Cancel
    await drainCard.getByRole("button", { name: "Cancel" }).click();

    // Confirmation should be hidden, Drain button should be back
    await expect(
      drainCard.getByRole("button", { name: "Drain", exact: true }),
    ).toBeVisible();
    await expect(
      drainCard.getByRole("button", { name: "Confirm Drain" }),
    ).not.toBeVisible();
  });

  test("shows error when peers fail to load", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Override Peers to fail
    await page.evaluate(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as {
        main: {
          AdminBinding: Record<
            string,
            (...args: unknown[]) => Promise<unknown>
          >;
        };
      };
      go.main.AdminBinding.Peers = async () => {
        throw new Error("cluster unavailable");
      };
    });

    await page.locator("aside").getByText("Cluster").click();

    // Error toast or alert should appear
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Seal Manager Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Seal Manager", () => {
  test.describe("unsealed initialized state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          sharesRequired: 2,
          sharesProvided: 0,
          sharesTotal: 3,
        },
      },
    });

    test("displays seal status stats", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();

      const main = page.locator("main");

      // Should show Seal Status stat title
      await expect(
        main.locator(".stat-title", { hasText: "Seal Status" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Should show Unsealed badge in main content
      await expect(main.locator(".badge-success").getByText("Unsealed")).toBeVisible();

      // Should show Initialized stat
      await expect(
        main.locator(".stat-title", { hasText: "Initialized" }),
      ).toBeVisible();

      // Should show Total Shares stat
      await expect(
        main.locator(".stat-title", { hasText: "Total Shares" }),
      ).toBeVisible();
    });
  });

  // Single-admin sealing only exists for non-shamir barriers: when
  // sharesRequired > 0 the Seal Manager shows the holder-quorum note
  // instead of the Seal button (a Shamir barrier refuses a single-admin
  // BarrierSeal; a quorum of its share holders seals it).
  test.describe("unsealed single-admin (non-shamir) state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
          strategy: "passphrase",
        },
      },
    });

    test("shows seal button when unsealed", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();

      const main = page.locator("main");

      // Should show the Seal section card
      await expect(
        main.locator(".card-title", { hasText: "Seal" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Seal button should be available
      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toBeVisible();
    });

    test("seal button shows confirmation dialog", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();

      const main = page.locator("main");

      // Click Seal
      await main
        .getByRole("button", { name: "Seal", exact: true })
        .click();

      // Confirmation should appear
      await expect(main.getByText("Are you sure?")).toBeVisible();
      await expect(
        main.getByRole("button", { name: "Confirm Seal" }),
      ).toBeVisible();
      await expect(
        main.getByRole("button", { name: "Cancel" }),
      ).toBeVisible();
    });

    test("seal cancel hides confirmation", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();

      const main = page.locator("main");

      // Wait for the Seal card to render
      const sealCard = main.locator(".card", { hasText: /^Seal/ });
      await expect(sealCard).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Show confirmation
      await sealCard
        .getByRole("button", { name: "Seal", exact: true })
        .click();
      await expect(
        sealCard.getByRole("button", { name: "Confirm Seal" }),
      ).toBeVisible();

      // Cancel
      await sealCard.getByRole("button", { name: "Cancel" }).click();

      // Confirmation should hide
      await expect(
        sealCard.getByRole("button", { name: "Confirm Seal" }),
      ).not.toBeVisible();

      // The Seal button should reappear
      await expect(
        sealCard.getByRole("button", { name: "Seal", exact: true }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("seal error displays toast", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Inject seal error
      await injectError(page, "SealBinding.Seal", "seal operation failed");

      await page.locator("aside").getByText("Seal Manager").click();

      const main = page.locator("main");

      // Click Seal and confirm
      await main
        .getByRole("button", { name: "Seal", exact: true })
        .click();
      await main.getByRole("button", { name: "Confirm Seal" }).click();

      // Error toast should appear
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText(
        "seal operation failed",
      );

      await clearError(page, "SealBinding.Seal");
    });
  });

  test.describe("sealed initialized state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        isFirstRun: false,
        sealStatus: {
          sealed: true,
          initialized: true,
          sharesRequired: 2,
          sharesProvided: 0,
          sharesTotal: 3,
        },
        unsealProgress: {
          sealed: true,
          sharesProvided: 0,
          sharesRequired: 2,
        },
      },
    });

    test("shows unseal section when sealed", async ({ page }) => {
      await page.goto("/");

      // In desktop mode, the sealed state is surfaced via the Seal Manager,
      // not a standalone unseal screen. Navigate directly to Seal Manager.
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Navigate to Seal Manager to see the sealed state.
      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      // Should show the seal status as Sealed.
      await expect(
        main.locator(".stat-title", { hasText: "Seal Status" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(
        main.locator(".badge-warning").getByText("Sealed"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });
  });

  test.describe("not configured (uninitialized) state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: false,
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
        },
      },
    });

    test("shows 'encryption not configured' panel with Configure affordance", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();

      // No encryption barrier → the "not configured" panel is shown (replacing
      // the old "seal subsystem has not been initialized" alert) and an
      // in-place Configure Encryption affordance is offered.
      await expect(
        page.getByTestId("encryption-not-configured-card"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByTestId("seal-status-not-configured")).toBeVisible();
      await expect(page.getByTestId("configure-encryption-btn")).toBeVisible();
    });
  });

  test.describe("seal status error handling", () => {
    test.use({ mockState: secondLaunchEmbeddedState });

    test("displays error toast when seal status fails", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await injectError(
        page,
        "SealBinding.Status",
        "seal subsystem unavailable",
      );

      await page.locator("aside").getByText("Seal Manager").click();

      // Error toast should appear
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(alert.locator("span")).toContainText(
        "seal subsystem unavailable",
      );

      await clearError(page, "SealBinding.Status");
    });
  });
});

// ---------------------------------------------------------------------------
// Settings Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Settings", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("displays connection status", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("aside").getByText("Settings").click();

    const main = page.locator("main");

    // Should show Connection section
    const connectionCard = main.locator(".card", { hasText: "Connection" });
    await expect(connectionCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Should show status stat title
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Status" }),
    ).toBeVisible();

    // Should show transport stat
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Transport" }),
    ).toBeVisible();

    // Should show address stat
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Address" }),
    ).toBeVisible();
  });

  test("displays theme toggle", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("aside").getByText("Settings").click();

    const main = page.locator("main");

    // Should show Theme section
    const themeCard = main.locator(".card", { hasText: "Theme" });
    await expect(themeCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Should show theme toggle description
    await expect(
      themeCard.getByText("Toggle between light and dark mode"),
    ).toBeVisible();
  });

  test("displays About section with version info", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("aside").getByText("Settings").click();

    const main = page.locator("main");

    // Should show About section
    const aboutCard = main.locator(".card", { hasText: "About" });
    await expect(aboutCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Should show version data from SDK (sdk: "0.1.0", server: "embedded")
    await expect(aboutCard.getByText("0.1.0")).toBeVisible();
    await expect(aboutCard.getByText("embedded")).toBeVisible();

    // Should show QRDB description
    await expect(aboutCard.getByText("QuicRaft DB")).toBeVisible();
  });

  test("displays version table with property/value columns", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("aside").getByText("Settings").click();

    const main = page.locator("main");
    const aboutCard = main.locator(".card", { hasText: "About" });

    // Wait for version info to load (SDK returns sdk + server keys)
    await expect(aboutCard.getByText("0.1.0")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Should show Property and Value columns
    await expect(aboutCard.locator("th", { hasText: "Property" })).toBeVisible();
    await expect(aboutCard.locator("th", { hasText: "Value" })).toBeVisible();

    // Should show version properties from the WailsClient SDK
    await expect(aboutCard.getByText("sdk", { exact: true })).toBeVisible();
    await expect(aboutCard.getByText("server", { exact: true })).toBeVisible();
    await expect(aboutCard.getByText("0.1.0")).toBeVisible();
    await expect(aboutCard.getByText("embedded")).toBeVisible();
  });

  test("theme toggle changes mode label", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("aside").getByText("Settings").click();

    const main = page.locator("main");
    const themeCard = main.locator(".card", { hasText: "Theme" });

    await expect(themeCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Use the aria-label to find the toggle checkbox and force click it
    // (DaisyUI swap component may have overlapping SVG icons)
    const themeToggle = themeCard.locator(
      "input[aria-label='Toggle dark mode']",
    );
    await themeToggle.click({ force: true });

    // The mode label should still be visible (e.g. "dark mode" or "light mode")
    await expect(themeCard.locator("span.capitalize")).toBeVisible();
  });

  test("shows QRDB divider and description", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("aside").getByText("Settings").click();

    const main = page.locator("main");

    // Should show QRDB divider in About card
    const aboutCard = main.locator(".card", { hasText: "About" });
    await expect(aboutCard.locator(".divider")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Should show the description text
    await expect(
      aboutCard.getByText("High-performance distributed key-value store"),
    ).toBeVisible();
  });
});
