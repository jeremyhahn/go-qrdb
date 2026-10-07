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
  injectError,
} from "./fixtures/wails-mock.ts";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// All database tests use the second-launch state so the app boots directly
// into MainApp (wizard already completed, server running).
// ---------------------------------------------------------------------------

test.describe("Database Navigation", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("shows empty state when no databases exist", async ({ page }) => {
    await page.goto("/");

    // Wait for the app to fully load — Dashboard is shown automatically
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Verify the sidebar shows "No databases." under the Databases section
    await expect(
      page.locator("aside").getByText("No databases."),
    ).toBeVisible();

    // Verify "New Database" button is visible in the sidebar
    await expect(
      page.locator("aside").getByText("New Database"),
    ).toBeVisible();
  });

  test("sidebar shows 'No databases.' when list is empty", async ({ page }) => {
    await page.goto("/");
    // The server tree auto-expands the active profile; when no databases exist,
    // it shows the "No databases." message under the expanded server.
    await expect(
      page.getByText("No databases."),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test.describe("with databases", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        databases: [
          { name: "orders", backend: "pebble", status: "open" },
          { name: "metrics", backend: "pebble", status: "open" },
          { name: "embeddings", backend: "pebble", status: "open" },
        ],
      },
    });

    test("sidebar lists databases", async ({ page }) => {
      await page.goto("/");

      // Wait for databases to appear in sidebar
      const sidebar = page.locator("aside");
      await expect(sidebar.getByText("orders")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(sidebar.getByText("metrics")).toBeVisible();
      await expect(sidebar.getByText("embeddings")).toBeVisible();
    });
  });

  test.describe("with single KV database", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        databases: [
          { name: "mydb", backend: "pebble", status: "open" },
        ],
      },
    });

    test("clicking a KV database opens workspace with tabs", async ({ page }) => {
      await page.goto("/");

      // Click the database in sidebar
      const sidebar = page.locator("aside");
      await sidebar.getByText("mydb").click();

      // Verify workspace header shows database name
      await expect(page.locator(".breadcrumbs").getByText("mydb", { exact: false })).toBeVisible();

      // Verify KV tab bar with all 6 tabs
      // DbNavRail: use page.locator("main nav") for tool navigation
      await expect(page.locator("main nav").getByText("Data", { exact: true })).toBeVisible();
      await expect(page.locator("main nav").getByText("Collections", { exact: true })).toBeVisible();
      await expect(page.locator("main nav").getByText("Transactions", { exact: true })).toBeVisible();
      await expect(page.locator("main nav").getByText("Watch", { exact: true })).toBeVisible();
      await expect(page.locator("main nav").getByText("Batch", { exact: true })).toBeVisible();
      await expect(page.locator("main nav").getByText("Leases", { exact: true })).toBeVisible();

      // Data tab is active by default
      await expect(page.locator("main nav").getByText("Data", { exact: true })).toBeVisible();
    });

    test("KV workspace tab switching works", async ({ page }) => {
      await page.goto("/");

      const sidebar = page.locator("aside");
      await sidebar.getByText("mydb").click();

      // DbNavRail: use page.locator("main nav") for tool navigation

      // Switch to Transactions tab
      await page.locator("main nav").getByText("Transactions", { exact: true }).click();
      await expect(page.locator("main nav").getByText("Transactions", { exact: true })).toBeVisible();

      // Verify transaction content is shown (step 1 header)
      await expect(page.getByText("Conditions").first()).toBeVisible();

      // Switch to Watch tab
      await page.locator("main nav").getByText("Watch", { exact: true }).click();
      await expect(page.locator("main nav").getByText("Watch", { exact: true })).toBeVisible();

      // Verify watch content is shown (start watch button)
      await expect(page.getByRole("button", { name: "Start Watch" })).toBeVisible();

      // Switch to Batch tab
      await page.locator("main nav").getByText("Batch", { exact: true }).click();
      await expect(page.locator("main nav").getByText("Batch", { exact: true })).toBeVisible();

      // Verify batch content is shown
      await expect(page.getByRole("button", { name: "Add Operation" })).toBeVisible();
    });

    test("navigating to an admin section clears database selection", async ({ page }) => {
      await page.goto("/");

      // Select a database
      const sidebar = page.locator("aside");
      await sidebar.getByText("mydb").click();
      await expect(page.locator(".breadcrumbs").getByText("mydb", { exact: false })).toBeVisible();

      // Click an admin section — this calls clearSelection() internally.
      await page.locator("aside").getByText("Dashboard").click();
      await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible();

      // Navigate back to the database workspace — selection should be cleared.
      await sidebar.getByText("mydb").click();
      await expect(page.locator(".breadcrumbs").getByText("mydb", { exact: false })).toBeVisible();
    });

    test("breadcrumb shows database name when selected", async ({ page }) => {
      await page.goto("/");

      const sidebar = page.locator("aside");
      await sidebar.getByText("mydb").click();

      // Verify breadcrumb shows the database name.
      const breadcrumb = page.locator(".breadcrumbs");
      await expect(breadcrumb.getByText("mydb")).toBeVisible();
    });
  });

  test.describe("with a database opened on the Time Series tab", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        databases: [
          { name: "metrics", backend: "pebble", status: "open" },
        ],
      },
    });

    test("the Time Series tab renders TimeSeriesView", async ({ page }) => {
      await page.goto("/");

      const sidebar = page.locator("aside");
      await sidebar.getByText("metrics").click();

      // Verify workspace header
      await expect(page.locator(".breadcrumbs").getByText("metrics", { exact: false })).toBeVisible();

      // Every database exposes every engine as a tab.
      const tab = page.getByRole("tab", { name: "Time Series", exact: true });
      await expect(tab).toBeVisible();
      await tab.click();
      await expect(tab).toHaveAttribute("aria-selected", "true");
    });
  });

  test.describe("with a database opened on the Vectors tab", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        databases: [
          { name: "embeddings", backend: "pebble", status: "open" },
        ],
      },
    });

    test("the Vectors tab renders VectorView", async ({ page }) => {
      await page.goto("/");

      const sidebar = page.locator("aside");
      await sidebar.getByText("embeddings").click();

      // Verify workspace header
      await expect(page.locator(".breadcrumbs").getByText("embeddings", { exact: false })).toBeVisible();

      // Every database exposes every engine as a tab.
      const tab = page.getByRole("tab", { name: "Vectors", exact: true });
      await expect(tab).toBeVisible();
      await tab.click();
      await expect(tab).toHaveAttribute("aria-selected", "true");
    });
  });
});

// ---------------------------------------------------------------------------
// Create Database flow
// ---------------------------------------------------------------------------

test.describe("Create Database", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("create button in sidebar opens create form", async ({ page }) => {
    await page.goto("/");

    // Wait for app to load
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click the "+ New Database" button in the server tree sidebar.
    await page.locator("aside").getByText("New Database", { exact: true }).click();

    // Verify create form is shown (DatabaseWizard first step)
    await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible();
  });

  test("empty state CTA opens create form", async ({ page }) => {
    await page.goto("/");

    // Wait for Dashboard to load, then click "New Database" in the sidebar
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.locator("aside").getByText("New Database").click();

    // Verify create form (DatabaseWizard first step)
    await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible();
  });

  test("engine cards show engine types", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.locator("aside").getByText("New Database").click();

    // Verify engine labels are shown (exact match avoids matching descriptions)
    await expect(page.getByText("Key-Value", { exact: true })).toBeVisible();
    await expect(page.getByText("TimeSeries", { exact: true })).toBeVisible();
    await expect(page.getByText("Vector", { exact: true })).toBeVisible();
  });

  test("successful database creation returns to workspace", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Open create form via sidebar
    await page.locator("aside").getByText("New Database").click();

    // Fill database name
    await page.locator("#db-name").fill("test-db");

    // Key-Value engine is selected by default
    // Click Next to proceed to Storage Backend step
    await page.getByRole("button", { name: "Next" }).click();

    // Storage Backend step -- Pebble is default, click Next
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- Raft step skipped for standalone
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Encryption Barrier step -- click Next (passphrase is default)
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Summary step -- click Create Database
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    // Should return to workspace view and database list should now include test-db
    const sidebar = page.locator("aside");
    await expect(sidebar.getByText("test-db")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("create database with invalid name shows error", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.locator("aside").getByText("New Database").click();

    // Fill an invalid name with special characters
    await page.locator("#db-name").fill("Bad Name!");

    // Error message should appear
    await expect(page.getByText("Name must be lowercase alphanumeric")).toBeVisible();
  });

  test("create database error shows toast", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.locator("aside").getByText("New Database").click();

    // Inject error
    await injectError(page, "DatabaseBinding.Create", "Permission denied");

    // Fill name and navigate through wizard
    await page.locator("#db-name").fill("fail-db");
    await page.getByRole("button", { name: "Next" }).click();

    // Storage step
    await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // SQL Wire Protocols step -- Raft step skipped for standalone
    await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Barrier step
    await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();

    // Summary step -- click Create Database
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    // Error should appear on the creating step
    await expect(page.getByText("Permission denied")).toBeVisible({ timeout: 10_000 });
  });
});

// ---------------------------------------------------------------------------
// Transaction View (step-by-step layout)
// ---------------------------------------------------------------------------

test.describe("Transaction View", () => {
  test.use({
    mockState: {
      ...secondLaunchEmbeddedState,
      databases: [
        { name: "txn-db", backend: "pebble", status: "open" },
      ],
    },
  });

  test("shows step-by-step layout with numbered sections", async ({ page }) => {
    await page.goto("/");

    // Navigate to the database and click Transactions tab
    const sidebar = page.locator("aside");
    await sidebar.getByText("txn-db").click();
    await page.locator("main nav").getByText("Transactions", { exact: true }).click();

    // Verify step badges
    await expect(page.getByText("Conditions").first()).toBeVisible();
    await expect(page.getByText("If Conditions Pass")).toBeVisible();
    await expect(page.getByText("If Conditions Fail")).toBeVisible();

    // Step 3 should be marked optional
    await expect(page.getByText("Optional")).toBeVisible();
  });

  test("renamed fields use friendly labels", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await sidebar.getByText("txn-db").click();
    await page.locator("main nav").getByText("Transactions", { exact: true }).click();

    // Add a condition to see the renamed fields
    await page.getByRole("button", { name: "Add Condition" }).click();

    // Verify friendly labels instead of old ones
    await expect(page.getByText("Property")).toBeVisible();
    await expect(page.getByText("Comparison")).toBeVisible();
    await expect(page.getByText("Expected Value")).toBeVisible();
  });

  test("execute without conditions shows warning", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await sidebar.getByText("txn-db").click();
    await page.locator("main nav").getByText("Transactions", { exact: true }).click();

    // Click execute without any conditions
    await page.getByRole("button", { name: "Execute Transaction" }).click();

    // Warning toast should appear
    await expect(page.getByText("At least one compare is required")).toBeVisible({ timeout: 5_000 });
  });
});

// ---------------------------------------------------------------------------
// Watch View (three-zone layout)
// ---------------------------------------------------------------------------

test.describe("Watch View", () => {
  test.use({
    mockState: {
      ...secondLaunchEmbeddedState,
      databases: [
        { name: "watch-db", backend: "pebble", status: "open" },
      ],
    },
  });

  test("shows compact creation form", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await sidebar.getByText("watch-db").click();
    await page.locator("main nav").getByText("Watch", { exact: true }).click();

    // Verify inline creation form elements
    await expect(page.locator("input[placeholder='Exact key...']")).toBeVisible();
    await expect(page.locator("input[placeholder='Start rev']")).toBeVisible();
    // exact: true — the page description also contains "prefixes".
    await expect(page.getByText("Prefix", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Start Watch" })).toBeVisible();
  });

  test("shows event stream with 'Waiting for events' placeholder", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await sidebar.getByText("watch-db").click();
    await page.locator("main nav").getByText("Watch", { exact: true }).click();

    // Verify event stream zone
    await expect(page.getByText("Waiting for events...")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Events" })).toBeVisible();
  });

  test("start watch without key shows warning", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await sidebar.getByText("watch-db").click();
    await page.locator("main nav").getByText("Watch", { exact: true }).click();

    // Click start without filling key
    await page.getByRole("button", { name: "Start Watch" }).click();

    // Warning toast
    await expect(page.getByText("Key or prefix is required")).toBeVisible({ timeout: 5_000 });
  });
});
