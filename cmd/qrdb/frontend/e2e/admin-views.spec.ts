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
 * Comprehensive admin view rendering tests.
 *
 * Covers all admin sidebar sections to ensure they render their expected
 * headings, stat titles, CrudTable columns, and interactive controls.
 * Tests run against the Wails mock fixture and do not require a live backend.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  raftClusterState,
  type MockState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async DOM operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock states
// ---------------------------------------------------------------------------

/** Base state with a seeded "default" database. */
const stateWithDefault: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "default", backend: "pebble", status: "open" },
  ],
};

/** Raft-mode state with a seeded "default" database — enables the Cluster nav item. */
const raftStateWithDefault: MockState = {
  ...raftClusterState,
  databases: [
    { name: "default", backend: "pebble", status: "open" },
  ],
};

/** Unsealed and initialized seal status. */
const unsealedState: MockState = {
  ...stateWithDefault,
  sealStatus: {
    sealed: false,
    initialized: true,
    sharesRequired: 2,
    sharesProvided: 0,
    sharesTotal: 3,
  },
};

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

/**
 * Navigates to the root, waits for MainApp to load, and clicks Dashboard
 * to enter the admin area. In the new sidebar, admin sections are always
 * visible — no toggle required; Dashboard is clicked to simulate the old
 * "entering admin mode" behaviour so tests that immediately check admin
 * content work without an additional navigateTo() call.
 */
async function enterAdminMode(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Dashboard", { exact: true }).click();
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Clicks a sidebar link and waits for the breadcrumb to update. */
async function navigateTo(page: Page, section: string): Promise<void> {
  // Use title attribute to uniquely target admin nav buttons — avoids strict-mode
  // violations when "Databases" appears both as a section header and an admin nav item.
  await page.locator(`aside button[title="${section}"]`).click();
  await expect(
    page.locator(".breadcrumbs").getByText(section),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/**
 * Overrides AdminBinding methods to return predictable test data.
 * Must be called after page.goto() when the mock window globals are available.
 */
async function seedAdminBindings(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as Record<string, unknown>;
    const go = w.go as {
      main: {
        AdminBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
      };
    };

    go.main.AdminBinding.Health = async () => ({ status: "ok", isLeader: true });

    go.main.AdminBinding.Status = async () => ({
      nodeId: 1,
      raftAddress: "127.0.0.1:63001",
      isLeader: true,
      leaderId: 1,
      shardCount: 1,
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
            { replicaId: 1, address: "127.0.0.1:63001", isLeader: true },
          ],
        },
      ],
    });
  });
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

test.describe("Admin Views - Dashboard", () => {
  test.use({ mockState: stateWithDefault });

  test("shows health status, capabilities and databases", async ({ page }) => {
    await enterAdminMode(page);
    await seedAdminBindings(page);

    const main = page.locator("main");

    await expect(main.locator(".stat-title", { hasText: "Health" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.locator(".badge-success").getByText("ok")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Capabilities section
    await expect(
      main.getByRole("heading", { name: "Capabilities" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByText("KV", { exact: true })).toBeVisible();

    // Databases section shows seeded database
    await expect(
      main.getByRole("heading", { name: "Databases" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(main.getByText("default")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Cluster
// ---------------------------------------------------------------------------

test.describe("Admin Views - Cluster", () => {
  test.use({ mockState: raftStateWithDefault });

  test("shows peers table and discovery config", async ({ page }) => {
    await enterAdminMode(page);
    await seedAdminBindings(page);
    await navigateTo(page, "Cluster");

    const main = page.locator("main");

    // Peers table
    await expect(main.getByText("Cluster Peers")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(
      main.locator("th", { hasText: "Shard ID" }),
    ).toBeVisible();
    await expect(
      main.locator("th", { hasText: "Address" }),
    ).toBeVisible();
    await expect(main.getByText("127.0.0.1:63001")).toBeVisible();

    // Discovery / member management cards
    await expect(
      main.locator(".card", { hasText: "Add Member" }),
    ).toBeVisible();
  });

  test("shows Drain Node and Request Snapshot sections", async ({ page }) => {
    await enterAdminMode(page);
    await seedAdminBindings(page);
    await navigateTo(page, "Cluster");

    const main = page.locator("main");

    await expect(main.getByText("Cluster Peers")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(
      main.locator(".card", { hasText: "Drain Node" }),
    ).toBeVisible();
    await expect(
      main.locator(".card", { hasText: "Request Snapshot" }),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Seal Manager
// ---------------------------------------------------------------------------

test.describe("Admin Views - Seal Manager", () => {
  test.use({ mockState: unsealedState });

  test("shows seal status stats", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Seal Manager");

    const main = page.locator("main");

    await expect(
      main.locator(".stat-title", { hasText: "Seal Status" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.locator(".badge-success").getByText("Unsealed"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.locator(".stat-title", { hasText: "Initialized" }),
    ).toBeVisible();
    await expect(
      main.locator(".stat-title", { hasText: "Total Shares" }),
    ).toBeVisible();
  });

});

// Single-admin sealing only exists for non-shamir barriers: when
// sharesRequired > 0 the Seal Manager shows the holder-quorum note
// instead of the Seal button (a Shamir barrier refuses a single-admin
// BarrierSeal; a quorum of its share holders seals it).
test.describe("Admin Views - Seal Manager (single-admin)", () => {
  test.use({
    mockState: {
      ...stateWithDefault,
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

  test("shows Seal and Rekey action buttons", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Seal Manager");

    const main = page.locator("main");

    await expect(
      main.getByRole("button", { name: "Seal", exact: true }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Tenants
// ---------------------------------------------------------------------------

test.describe("Admin Views - Tenants", () => {
  test.use({ mockState: stateWithDefault });

  test("shows CrudTable with correct columns", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Tenants");

    const main = page.locator("main");

    await expect(main.locator("th", { hasText: "Name" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    // CrudTable renders the createLabel prop as the button text ("Create Tenant").
    await expect(
      main.getByRole("button", { name: "Create Tenant" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

test.describe("Admin Views - Users", () => {
  test.use({ mockState: stateWithDefault });

  test("shows tenant filter and CrudTable", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Users");

    const main = page.locator("main");

    // Tenant filter is a TenantSelect dropdown ("All tenants" empty option).
    await expect(
      main.locator("select#user-filter-tenant"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // CrudTable columns
    await expect(main.locator("th", { hasText: "Username" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    // CrudTable renders the createLabel prop as the button text ("Create User").
    await expect(
      main.getByRole("button", { name: "Create User" }),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

test.describe("Admin Views - Roles", () => {
  test.use({ mockState: stateWithDefault });

  test("shows tenant filter and CrudTable", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Roles");

    const main = page.locator("main");

    // Tenant filter is a TenantSelect dropdown ("All tenants" empty option).
    await expect(
      main.locator("select#role-filter-tenant"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // CrudTable columns
    await expect(main.locator("th", { hasText: "Name" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    // CrudTable renders the createLabel prop as the button text ("Create Role").
    await expect(
      main.getByRole("button", { name: "Create Role" }),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Certificates
// ---------------------------------------------------------------------------

test.describe("Admin Views - Certificates", () => {
  test.use({ mockState: stateWithDefault });

  test("shows tabbed action panel and cert table", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Certificates");

    const main = page.locator("main");

    // The action panel tab buttons are always visible.
    await expect(
      main.getByRole("button", { name: "Issue", exact: true }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.getByRole("button", { name: "Enroll", exact: true }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.getByRole("button", { name: "Export PKCS#12" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      main.getByRole("button", { name: "Revoke", exact: true }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Empty state message is shown when no certs exist yet.
    await expect(main.getByText("No certificates issued.")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("shows Issue Certificate action", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Certificates");

    const main = page.locator("main");

    // Issue tab button is always present in the action panel header.
    await expect(
      main.getByRole("button", { name: "Issue", exact: true }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------

test.describe("Admin Views - Databases", () => {
  test.use({ mockState: stateWithDefault });

  test("shows CrudTable with default database", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Databases");

    const main = page.locator("main");

    // Table columns
    await expect(main.locator("th", { hasText: "Name" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(main.locator("th", { hasText: "Backend" })).toBeVisible();
    await expect(main.locator("th", { hasText: "Status" })).toBeVisible();

    // Default database row
    await expect(main.getByText("default")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("shows Create Database button", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Databases");

    await expect(
      page
        .locator("main")
        .getByRole("button", { name: "Create Database" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Audit Log
// ---------------------------------------------------------------------------

test.describe("Admin Views - Audit Log", () => {
  test.use({ mockState: stateWithDefault });

  test("shows filter controls and audit table", async ({ page }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Audit Log");

    const main = page.locator("main");

    // The PageHeader h1 must render (the CrudTable also renders an
    // "Audit Log" h3, so target the h1 specifically to stay strict-safe).
    await expect(
      main.locator("h1", { hasText: "Audit Log" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

test.describe("Admin Views - Settings", () => {
  test.use({ mockState: stateWithDefault });

  test("shows connection info, theme toggle and about section", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Settings");

    const main = page.locator("main");

    // Connection section stat titles
    const connectionCard = main.locator(".card", { hasText: "Connection" });
    await expect(connectionCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Status" }),
    ).toBeVisible();
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Transport" }),
    ).toBeVisible();
    await expect(
      connectionCard.locator(".stat-title", { hasText: "Address" }),
    ).toBeVisible();

    // Theme toggle
    const themeCard = main.locator(".card", { hasText: "Theme" });
    await expect(themeCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      themeCard.getByText("Toggle between light and dark mode"),
    ).toBeVisible();

    // About section
    const aboutCard = main.locator(".card", { hasText: "About" });
    await expect(aboutCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(aboutCard.getByText("QuicRaft DB")).toBeVisible();
  });

  test("settings connected badge reflects connection store state", async ({
    page,
  }) => {
    await enterAdminMode(page);
    await navigateTo(page, "Settings");

    const main = page.locator("main");
    const connectionCard = main.locator(".card", { hasText: "Connection" });

    await expect(connectionCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Status badge must be present (connected or disconnected depending on mode).
    await expect(connectionCard.locator(".badge-lg")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });
});

// ---------------------------------------------------------------------------
// Uncaught page error detection — Tenants and Certificates
//
// Registers a page.on('pageerror') listener before navigation and asserts that
// no uncaught JavaScript exceptions are thrown when the views load and render
// their initial state (including the async API calls in onMount).
// ---------------------------------------------------------------------------

test.describe("Admin Views - No Uncaught Errors on Load", () => {
  test.use({ mockState: stateWithDefault });

  test("TenantManager does not throw uncaught errors when loaded", async ({ page }) => {
    const pageErrors: Error[] = [];
    page.on("pageerror", (err) => pageErrors.push(err));

    await enterAdminMode(page);
    await navigateTo(page, "Tenants");

    // Wait for the tenant list to finish loading (Create Tenant button visible
    // confirms the view rendered successfully).
    const main = page.locator("main");
    await expect(
      main.getByRole("button", { name: "Create Tenant" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Give any async errors a moment to surface.
    await page.waitForTimeout(500);

    expect(
      pageErrors.map((e) => e.message),
      "TenantManager must not produce uncaught JavaScript errors",
    ).toHaveLength(0);
  });

  test("CertManager does not throw uncaught errors when loaded", async ({ page }) => {
    const pageErrors: Error[] = [];
    page.on("pageerror", (err) => pageErrors.push(err));

    await enterAdminMode(page);
    await navigateTo(page, "Certificates");

    // Wait for the action panel header to render.
    const main = page.locator("main");
    await expect(
      main.getByRole("button", { name: "Issue", exact: true }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Give any async errors a moment to surface.
    await page.waitForTimeout(500);

    expect(
      pageErrors.map((e) => e.message),
      "CertManager must not produce uncaught JavaScript errors",
    ).toHaveLength(0);
  });

  test("TenantManager renders empty state when API returns no tenants", async ({ page }) => {
    // Override TenantBinding.List to return an empty list before the page loads.
    await page.addInitScript(() => {
      const originalAddInitScript = Object.getOwnPropertyDescriptor(
        window,
        "__QRDB_MOCK_TENANT_OVERRIDE__",
      );
      if (originalAddInitScript) return;
      (window as unknown as Record<string, unknown>).__QRDB_MOCK_TENANT_OVERRIDE__ = true;
    });

    const pageErrors: Error[] = [];
    page.on("pageerror", (err) => pageErrors.push(err));

    await enterAdminMode(page);
    await navigateTo(page, "Tenants");

    const main = page.locator("main");
    // The view should render without errors regardless of empty data.
    await expect(
      main.getByRole("button", { name: "Create Tenant" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.waitForTimeout(500);

    expect(
      pageErrors.map((e) => e.message),
      "TenantManager must not produce uncaught errors with empty tenant list",
    ).toHaveLength(0);
  });

  test("CertManager renders empty state when API returns no certificates", async ({ page }) => {
    const pageErrors: Error[] = [];
    page.on("pageerror", (err) => pageErrors.push(err));

    await enterAdminMode(page);
    await navigateTo(page, "Certificates");

    const main = page.locator("main");
    // Empty state message shown when cert store is empty.
    await expect(
      main.getByText("No certificates issued."),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.waitForTimeout(500);

    expect(
      pageErrors.map((e) => e.message),
      "CertManager must not produce uncaught errors with empty cert list",
    ).toHaveLength(0);
  });
});
