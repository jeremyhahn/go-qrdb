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
  clearError,
  type MockState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to admin mode and clicks the Databases sidebar link. */
async function navigateToDatabases(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator('aside button[title="Databases"]').click();
  await expect(
    page.locator(".breadcrumbs").getByText("Databases"),
  ).toBeVisible();
}

/** Opens the Create Database form from the Databases list view. */
async function openCreateForm(page: Page): Promise<void> {
  await navigateToDatabases(page);
  const main = page.locator("main");
  await expect(main.getByText("No databases.").or(main.getByText("No databases found.")).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await main.getByRole("button", { name: "Create Database" }).click();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Databases", () => {
  test.describe("empty state", () => {
    test.use({ mockState: secondLaunchEmbeddedState });

    test("shows empty state when no databases exist", async ({ page }) => {
      await navigateToDatabases(page);

      await expect(
        page.locator("main").getByText("No databases found."),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("shows 'Create Database' button", async ({ page }) => {
      await navigateToDatabases(page);

      await expect(
        page.locator("main").getByRole("button", { name: "Create Database" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });
  });

  test.describe("database list", () => {
    const stateWithDbs: MockState = {
      ...secondLaunchEmbeddedState,
      databases: [
        { name: "mykvdb", backend: "pebble", status: "open" },
        {
          name: "mytsdb",
          backend: "pebble",
          status: "open",
        },
        { name: "myvdb", backend: "memory", status: "open" },
      ],
    };

    test.use({ mockState: stateWithDbs });

    test("shows database list table with Name, Backend, Status columns", async ({
      page,
    }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");

      // Table headers.
      await expect(main.locator("th", { hasText: "Name" })).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await expect(main.locator("th", { hasText: "Engine" })).toHaveCount(0);
      await expect(main.locator("th", { hasText: "Backend" })).toBeVisible();
      await expect(main.locator("th", { hasText: "Status" })).toBeVisible();
    });

    test("shows correct database names in the table", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");

      await expect(main.getByText("mykvdb")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await expect(main.getByText("mytsdb")).toBeVisible();
      await expect(main.getByText("myvdb")).toBeVisible();
    });

    test("shows correct backend values", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");

      await expect(main.getByText("pebble").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(main.getByText("memory")).toBeVisible();
    });

    test("clicking a database row opens DatabaseDetail", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");

      await expect(main.getByText("mykvdb")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Click the row for mykvdb.
      await main.getByText("mykvdb").click();

      // The detail view should render with a back button and the db name.
      await expect(
        main.getByRole("button", { name: "Back" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(main.getByText("mykvdb").first()).toBeVisible();
    });

    test("back button from detail returns to list", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");

      await expect(main.getByText("mykvdb")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await main.getByText("mykvdb").click();

      // In the detail view, click Back.
      await main.getByRole("button", { name: "Back" }).click();

      // The list should be visible again.
      await expect(main.getByText("mykvdb")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await expect(main.getByText("mytsdb")).toBeVisible();
    });
  });

  test.describe("create database", () => {
    test.use({ mockState: secondLaunchEmbeddedState });

    test("create database button opens detail for new database", async ({
      page,
    }) => {
      await openCreateForm(page);

      const main = page.locator("main");

      // The detail create form should be visible.
      await expect(
        main.getByRole("heading", { name: "Create Database", level: 2 }).or(
          main.locator("h2", { hasText: "Create Database" }),
        ),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // The name input should be present.
      await expect(
        main.getByPlaceholder("e.g. my-database"),
      ).toBeVisible();
    });

    test("create database with valid inputs succeeds", async ({ page }) => {
      await openCreateForm(page);

      const main = page.locator("main");

      await expect(
        main.getByPlaceholder("e.g. my-database"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await main.getByPlaceholder("e.g. my-database").fill("newdb");

      // Backend defaults to pebble — submit directly.
      await main.getByRole("button", { name: "Create Database" }).click();

      // Success toast should appear.
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText(
        'Database "newdb" created',
      );
    });

    test("create form has no engine selector", async ({ page }) => {
      await openCreateForm(page);

      const main = page.locator("main");

      await expect(
        main.getByPlaceholder("e.g. my-database"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(main.getByRole("heading", { name: "Engine" })).toHaveCount(0);
      await expect(main.getByRole("button", { name: /Time Series/ })).toHaveCount(0);
      await expect(main.getByRole("button", { name: /^Vector/ })).toHaveCount(0);
    });

    test("create database name validation rejects invalid characters", async ({
      page,
    }) => {
      await openCreateForm(page);

      const main = page.locator("main");

      await expect(
        main.getByPlaceholder("e.g. my-database"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Type an invalid name with a space.
      await main.getByPlaceholder("e.g. my-database").fill("my db!");

      // Validation error should appear.
      await expect(
        main.getByText(
          "Only letters, numbers, hyphens, and underscores are allowed.",
        ),
      ).toBeVisible();

      // Create button should be disabled.
      await expect(
        main.getByRole("button", { name: "Create Database" }),
      ).toBeDisabled();
    });

    test("cancel button returns to list without creating", async ({ page }) => {
      await openCreateForm(page);

      const main = page.locator("main");

      await expect(
        main.getByPlaceholder("e.g. my-database"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Click Cancel.
      await main.getByRole("button", { name: "Cancel" }).click();

      // Should return to the list view showing empty state.
      await expect(
        main.getByText("No databases found."),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("error on database list fetch shows error toast", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await injectError(page, "DatabaseBinding.List", "database list failed");

      await page.locator('aside button[title="Databases"]').click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(alert.locator("span")).toContainText("database list failed");

      await clearError(page, "DatabaseBinding.List");
    });

    test("error on database create shows error toast", async ({ page }) => {
      await openCreateForm(page);

      await injectError(page, "DatabaseBinding.Create", "create failed");

      const main = page.locator("main");

      await expect(
        main.getByPlaceholder("e.g. my-database"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await main.getByPlaceholder("e.g. my-database").fill("faildb");
      await main.getByRole("button", { name: "Create Database" }).click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("create failed");

      await clearError(page, "DatabaseBinding.Create");
    });
  });

  test.describe("drop database", () => {
    const stateWithDb: MockState = {
      ...secondLaunchEmbeddedState,
      databases: [
        { name: "dropme", backend: "pebble", status: "open" },
      ],
    };

    test.use({ mockState: stateWithDb });

    test("drop database from detail view succeeds after confirmation", async ({
      page,
    }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");

      // Click the database row to open detail.
      await expect(main.getByText("dropme")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await main.getByText("dropme").click();

      // Danger Zone section should be visible.
      await expect(main.getByText("Danger Zone")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Click Drop Database to show confirmation.
      await main.getByRole("button", { name: "Drop Database" }).click();
      await expect(
        main.getByRole("button", { name: "Confirm Drop" }),
      ).toBeVisible();

      // Confirm drop.
      await main.getByRole("button", { name: "Confirm Drop" }).click();

      // Success toast.
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText(
        'Database "dropme" dropped',
      );
    });
  });
});
