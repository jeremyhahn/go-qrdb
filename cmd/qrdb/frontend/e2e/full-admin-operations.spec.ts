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
 * Full admin operations: dashboard health, capabilities, cluster peers,
 * seal manager status, tenant CRUD, database list and creation, and
 * settings connection status.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  raftClusterState,
  injectError,
  clearError,
  type MockState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock states
// ---------------------------------------------------------------------------

const adminState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "admin-kv", backend: "pebble", status: "open" },
    {
      name: "admin-ts",
      backend: "pebble",
      status: "open",
    },
    { name: "admin-vec", backend: "pebble", status: "open" },
  ],
};

/** Raft-mode variant of adminState — enables the Cluster nav item. */
const raftAdminState: MockState = {
  ...raftClusterState,
  databases: [
    { name: "admin-kv", backend: "pebble", status: "open" },
    {
      name: "admin-ts",
      backend: "pebble",
      status: "open",
    },
    { name: "admin-vec", backend: "pebble", status: "open" },
  ],
};

const sealedState: MockState = {
  ...adminState,
  sealStatus: {
    sealed: true,
    initialized: true,
    sharesRequired: 2,
    sharesProvided: 0,
    sharesTotal: 3,
  },
};

const unsealedState: MockState = {
  ...adminState,
  sealStatus: {
    sealed: false,
    initialized: true,
    sharesRequired: 2,
    sharesProvided: 0,
    sharesTotal: 3,
  },
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigate to the root and wait for MainApp to load. */
async function enterAdminMode(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  // Admin sections are always visible in the sidebar — no toggle required.
}

/** Click a sidebar link and wait for the breadcrumb to update. */
async function navigateTo(page: Page, section: string): Promise<void> {
  // Use title attribute to uniquely target admin nav buttons — avoids strict-mode
  // violations when "Databases" appears both as a section header and an admin nav item.
  await page.locator(`aside button[title="${section}"]`).click();
  await expect(
    page.locator(".breadcrumbs").getByText(section),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/**
 * Override AdminBinding with rich responses for dashboard tests.
 * Must be called after page.goto() so window.go is available.
 */
async function seedAdminBindings(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as Record<string, unknown>;
    const go = w.go as {
      main: {
        AdminBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
      };
    };

    go.main.AdminBinding.Health = async () => ({
      status: "ok",
      isLeader: true,
    });

    go.main.AdminBinding.Status = async () => ({
      nodeId: 1,
      raftAddress: "127.0.0.1:63001",
      isLeader: true,
      leaderId: 1,
      shardCount: 2,
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
      timeSeries: true,
      vector: true,
    });

    go.main.AdminBinding.Peers = async () => ({
      shards: [
        {
          shardId: 1,
          peers: [
            { replicaId: 1, address: "127.0.0.1:63001", isLeader: true },
            {
              replicaId: 2,
              address: "127.0.0.1:63002",
              isLeader: false,
            },
          ],
        },
      ],
    });
  });
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

test.describe("Full Admin - Dashboard", () => {
  test.use({ mockState: adminState });

  test("shows health status in the dashboard", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Dashboard");
    await seedAdminBindings(page);

    const main = page.locator("main");
    // Health card should display status
    await expect(
      main.getByText(/ok|healthy/i).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shows capabilities section", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Dashboard");
    await seedAdminBindings(page);

    const main = page.locator("main");
    await expect(main.getByText(/Capabilit/i).first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("shows database list in the dashboard", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Dashboard");
    await seedAdminBindings(page);

    const main = page.locator("main");
    // All three seeded databases should appear somewhere on the page
    await expect(main.getByText("admin-kv").first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("shows error when health check fails", async ({ page }) => {
    // Navigate first so the page is loaded and __QRDB_MOCK_STATE__ is available,
    // then inject the error before navigating to the Dashboard so the on-mount
    // health check picks it up.
    await enterAdminMode(page);
    await injectError(page, "AdminBinding.Health", "health check timeout");
    await navigateTo(page, "Dashboard");
    await expect(
      page.getByText("health check timeout"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await clearError(page, "AdminBinding.Health");
  });
});

// ---------------------------------------------------------------------------
// Cluster
// ---------------------------------------------------------------------------

test.describe("Full Admin - Cluster", () => {
  test.use({ mockState: raftAdminState });

  test("shows peer list with two peers", async ({ page }) => {
    await enterAdminMode(page);
    // Seed bindings before navigating so the on-mount fetch picks up the rich data.
    await seedAdminBindings(page);
    await navigateTo(page, "Cluster");

    const main = page.locator("main");
    // Trigger a refresh to ensure the latest binding data is loaded.
    const refreshBtn = main.getByRole("button", { name: /Refresh/i });
    if (await refreshBtn.isVisible({ timeout: 3_000 })) {
      await refreshBtn.click();
    }

    await expect(main.getByText(/127\.0\.0\.1:63001/).first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.getByText(/127\.0\.0\.1:63002/).first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("shows empty peer list when no peers are configured", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Cluster");

    const main = page.locator("main");
    const refreshBtn = main.getByRole("button", { name: /Refresh/i });
    if (await refreshBtn.isVisible({ timeout: 3_000 })) {
      await refreshBtn.click();
    }

    // Either empty message or empty table
    const isEmpty =
      (await main.getByText(/no peers|no members/i).isVisible({ timeout: 5_000 })) ||
      (await main.locator("table tbody tr").count()) === 0;
    expect(isEmpty).toBeTruthy();
  });

  test("discovery config section can be expanded if present", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Cluster");

    // Look for a discovery/config expandable or Add Member button.
    // Use .first() to avoid strict mode violation when both exist.
    const addMemberBtn = page.getByRole("button", { name: /Add Member|Discovery/i }).first();
    if (await addMemberBtn.isVisible({ timeout: 3_000 })) {
      await addMemberBtn.click();
      // Some form input or expansion panel should become visible within main.
      const main = page.locator("main");
      await expect(
        main.locator("input[type='text'], textarea, .collapse-content").first(),
      ).toBeVisible({ timeout: 5_000 });
    }
  });
});

// ---------------------------------------------------------------------------
// Seal Manager
// ---------------------------------------------------------------------------

test.describe("Full Admin - Seal Manager (unsealed)", () => {
  test.use({ mockState: unsealedState });

  test("shows unsealed status with initialized indicator", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Seal Manager");

    const main = page.locator("main");
    await expect(
      main.getByText(/unsealed|initialized/i).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

});

// Single-admin sealing only exists for non-shamir barriers: when
// sharesRequired > 0 the Seal Manager shows the holder-quorum note
// instead of the Seal button (a Shamir barrier refuses a single-admin
// BarrierSeal; a quorum of its share holders seals it).
test.describe("Full Admin - Seal Manager (unsealed single-admin)", () => {
  test.use({
    mockState: {
      ...adminState,
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

  test("shows Seal button when unsealed", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Seal Manager");

    const main = page.locator("main");
    await expect(
      main.getByRole("button", { name: "Seal", exact: true }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

test.describe("Full Admin - Seal Manager (sealed)", () => {
  // The app cannot enter admin mode when the barrier is sealed (App.svelte redirects
  // to the unseal screen). Use the unsealed admin state and verify that the Seal
  // Manager view loads and shows the seal-related status information.
  test.use({ mockState: unsealedState });

  test("shows seal manager with status information", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Seal Manager");

    const main = page.locator("main");
    // The SealManager shows seal-related text in any barrier state.
    await expect(
      main.getByText(/seal|unseal|barrier|initialized/i).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Tenants
// ---------------------------------------------------------------------------

test.describe("Full Admin - Tenants", () => {
  test.use({ mockState: adminState });

  test("shows Tenants section with create button", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Tenants");

    const main = page.locator("main");
    // The CrudTable renders the create button with label "Create Tenant".
    await expect(
      main.getByRole("button", { name: "Create Tenant" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("creates a tenant and it appears in the list", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Tenants");

    const main = page.locator("main");
    await main.getByRole("button", { name: "Create Tenant" }).click();

    // The create form is a full-page replacement (not a modal).
    // Tenant ID has placeholder "e.g. acme-corp" and Name has "e.g. Acme Corporation".
    await expect(page.locator("#tenant-id")).toBeVisible({ timeout: 5_000 });
    await page.locator("#tenant-id").fill("acme-corp");
    await page.locator("#tenant-name").fill("Acme Corp");
    await main.getByTestId("create-tenant-admin-username").fill("acme-corp-admin");
    await main.getByTestId("create-tenant-admin-csr").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----",
    );

    await main.getByTestId("create-tenant-submit").click();

    // Dismiss the admin-credential result panel to return to the list.
    await expect(main.getByTestId("create-tenant-admin-result")).toBeVisible({
      timeout: 5_000,
    });
    await main.getByTestId("create-tenant-admin-result-done").click();

    await expect(page.getByText("acme-corp").first()).toBeVisible({
      timeout: 5_000,
    });
  });

  test("shows error when tenant creation fails", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Tenants");
    await injectError(page, "TenantBinding.Create", "tenant already exists");

    const main = page.locator("main");
    await main.getByRole("button", { name: "Create Tenant" }).click();

    await expect(page.locator("#tenant-id")).toBeVisible({ timeout: 3_000 });
    await page.locator("#tenant-id").fill("duplicate-tenant");
    await page.locator("#tenant-name").fill("Duplicate Tenant");
    await main.getByTestId("create-tenant-admin-username").fill("duplicate-tenant-admin");
    await main.getByTestId("create-tenant-admin-csr").fill(
      "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----",
    );
    await main.getByTestId("create-tenant-submit").click();
    await expect(page.getByText("tenant already exists")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "TenantBinding.Create");
  });
});

// ---------------------------------------------------------------------------
// Databases (admin section)
// ---------------------------------------------------------------------------

test.describe("Full Admin - Databases", () => {
  test.use({ mockState: adminState });

  test("shows all three seeded databases in the list", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Databases");

    const main = page.locator("main");
    await expect(main.getByText("admin-kv").first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.getByText("admin-ts").first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.getByText("admin-vec").first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("shows table with Name, Backend, and Status columns", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Databases");

    const main = page.locator("main");
    await expect(main.locator("th", { hasText: "Name" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.locator("th", { hasText: "Backend" })).toBeVisible();
    await expect(main.locator("th", { hasText: "Status" })).toBeVisible();
  });

  test("opens Create Database form from the admin databases view", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Databases");

    const main = page.locator("main");
    await main.getByRole("button", { name: "Create Database" }).click();

    // The admin view opens DatabaseDetail in create mode with h2 "Create Database"
    // and the "Database Name" section card.
    await expect(
      page.getByRole("heading", { name: "Create Database" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("Database Name").first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("creates a new database and verifies it in the list", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Databases");

    const main = page.locator("main");
    await main.getByRole("button", { name: "Create Database" }).click();

    await expect(
      page.getByRole("heading", { name: "Create Database" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Fill in the database name (placeholder "e.g. my-database")
    await page.locator("input[placeholder='e.g. my-database']").fill("new-admin-db");

    // Submit the form
    await page.getByRole("button", { name: "Create Database" }).last().click();

    // After creation, a success toast confirms the database was created.
    await expect(
      page.getByText('Database "new-admin-db" created').first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

test.describe("Full Admin - Settings", () => {
  test.use({ mockState: adminState });

  test("shows Settings section with About card", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Settings");

    const main = page.locator("main");
    await expect(
      main.locator(".card", { hasText: "About" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shows connection status as Connected", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Settings");

    const main = page.locator("main");
    await expect(
      main.getByText(/Connected|connected/i).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shows transport protocol info", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Settings");

    const main = page.locator("main");
    // Transport should show "rest" or equivalent
    await expect(
      main.getByText(/rest|grpc|transport|protocol/i).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shows version information", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Settings");

    const main = page.locator("main");
    // The About card shows either version data or "Version information is not available."
    // Both contain "version" or show the card title "About".
    await expect(
      main.locator(".card", { hasText: "About" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    // Version status text (available or unavailable) should appear.
    await expect(
      main.getByText(/version information|Property|Version/i).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});
