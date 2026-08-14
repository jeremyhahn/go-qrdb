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
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to admin mode and clicks the Roles sidebar link. */
async function navigateToRoles(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Toggle to admin mode

  // Click Roles in sidebar
  await page.locator("aside").getByText("Roles").click();
  await expect(page.locator(".breadcrumbs").getByText("Roles")).toBeVisible();
}

/**
 * Seeds a role via the mock binding and returns the created role ID.
 */
async function seedRole(
  page: Page,
  name: string,
  permissions: string[] = [],
  tenant: string = "default",
): Promise<string> {
  return page.evaluate(
    ({ n, p, t }: { n: string; p: string[]; t: string }) => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as {
        main: {
          RoleBinding: {
            Create: (req: {
              name: string;
              tenant?: string;
              permissions?: string[];
            }) => Promise<{ id: string }>;
          };
        };
      };
      return go.main.RoleBinding.Create({
        name: n,
        permissions: p,
        tenant: t,
      }).then((r) => r.id);
    },
    { n: name, p: permissions, t: tenant },
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Role Management", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test.describe("Role list", () => {
    test("displays empty state when no roles exist", async ({ page }) => {
      await navigateToRoles(page);

      // CrudTable shows empty message
      await expect(page.getByText("No roles found.")).toBeVisible();

      // Badge should show count of 0
      await expect(page.locator(".badge-ghost").getByText("0")).toBeVisible();
    });

    test("displays roles in the table after creation", async ({ page }) => {
      await navigateToRoles(page);

      // Seed roles via mock binding
      await seedRole(page, "admin", ["*"]);
      await seedRole(page, "viewer", ["kv:read"]);

      // Refresh the list by navigating away and back
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Roles").click();

      // Wait for roles to appear in the table
      await expect(page.getByText("admin", { exact: true })).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await expect(page.getByText("viewer", { exact: true })).toBeVisible();

      // Badge should show count of 2
      await expect(page.locator(".badge-ghost").getByText("2")).toBeVisible();
    });

    test("shows permissions count in the table", async ({ page }) => {
      await navigateToRoles(page);

      // Seed a role with 3 permissions
      await seedRole(page, "editor", ["kv:read", "kv:write", "kv:delete"]);

      // Refresh
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Roles").click();

      await expect(page.getByText("editor")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Should show "3" for permissions count
      await expect(page.getByText("3")).toBeVisible();
    });

    test("shows Create Role button in toolbar", async ({ page }) => {
      await navigateToRoles(page);

      await expect(
        page.getByRole("button", { name: "Create Role" }),
      ).toBeVisible();
    });

    test("shows Custom type for non-system roles", async ({ page }) => {
      await navigateToRoles(page);

      await seedRole(page, "custom-role", []);

      // Refresh
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Roles").click();

      await expect(page.getByText("custom-role")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Should show "Custom" type in the table
      await expect(page.getByText("Custom", { exact: true })).toBeVisible();
    });
  });

  test.describe("Create role", () => {
    test("opens create form when Create Role button is clicked", async ({
      page,
    }) => {
      await navigateToRoles(page);

      await page.getByRole("button", { name: "Create Role" }).click();

      // RoleDetail should show "Create Role" heading
      await expect(page.getByText("Create Role")).toBeVisible();

      // Back button should be visible
      await expect(
        page.getByRole("button", { name: /^Back$/ }),
      ).toBeVisible();

      // Name input should be present
      await expect(page.getByPlaceholder("Enter role name")).toBeVisible();
    });

    test("creates a new role and returns to role list", async ({ page }) => {
      await navigateToRoles(page);

      // Click create
      await page.getByRole("button", { name: "Create Role" }).click();

      // Fill the form
      await page.getByPlaceholder("Enter role name").fill("operator");

      // Submit
      await page.getByRole("button", { name: "Create" }).click();

      // Toast should confirm creation
      await expect(page.locator('[role="alert"]')).toBeVisible();

      // Should return to role list and show the new role in the table
      await expect(
        page.getByText("operator", { exact: true }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("shows validation when role name is empty", async ({ page }) => {
      await navigateToRoles(page);

      await page.getByRole("button", { name: "Create Role" }).click();

      // The Create button should be disabled when name is empty
      const createBtn = page.getByRole("button", { name: "Create" });
      await expect(createBtn).toBeDisabled();
    });

    test("cancel returns to role list without creating", async ({ page }) => {
      await navigateToRoles(page);

      await page.getByRole("button", { name: "Create Role" }).click();
      await expect(page.getByPlaceholder("Enter role name")).toBeVisible();

      // Click Cancel
      await page.getByRole("button", { name: "Cancel" }).click();

      // Should return to role list
      await expect(page.getByText("No roles found.")).toBeVisible();
    });

    test("tenant ID defaults to 'default' in create form", async ({
      page,
    }) => {
      await navigateToRoles(page);

      await page.getByRole("button", { name: "Create Role" }).click();

      // The tenant field is a TenantSelect dropdown preselected to "default".
      const tenantSelect = page.locator("select#role-detail-tenant");
      await expect(tenantSelect).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(tenantSelect).toHaveValue("default");
    });
  });

  test.describe("Edit role", () => {
    test("clicking a role row opens the edit form", async ({ page }) => {
      await navigateToRoles(page);

      // Seed a role
      await seedRole(page, "auditor", ["audit:read"]);

      // Refresh
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Roles").click();

      // Wait for the role to appear
      await expect(page.getByText("auditor")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Click the row
      await page.getByText("auditor").click();

      // Should show the edit form
      await expect(page.getByText("Edit Role")).toBeVisible();

      // Name field should be populated
      const nameInput = page.getByPlaceholder("Enter role name");
      await expect(nameInput).toHaveValue("auditor");
    });

    test("back button returns to role list from edit form", async ({
      page,
    }) => {
      await navigateToRoles(page);

      await seedRole(page, "mgr-role");

      // Refresh
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Roles").click();
      await expect(page.getByText("mgr-role")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Open edit
      await page.getByText("mgr-role").click();
      await expect(page.getByText("Edit Role")).toBeVisible();

      // Click Back
      await page.getByRole("button", { name: /^Back$/ }).click();

      // Should return to role list
      await expect(
        page.locator(".badge-ghost").getByText("1"),
      ).toBeVisible();
    });

    test("edit form shows Details and Permissions sections", async ({
      page,
    }) => {
      await navigateToRoles(page);

      await seedRole(page, "test-role", ["kv:read"]);

      // Refresh
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Roles").click();

      await expect(page.getByText("test-role")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Open edit
      await page.getByText("test-role").click();

      // Verify sections
      await expect(
        page.getByRole("heading", { name: "Details" }),
      ).toBeVisible();
      await expect(
        page.getByRole("heading", { name: "Permissions" }),
      ).toBeVisible();

      // Save button should show "Save" (not "Create")
      await expect(
        page.getByRole("button", { name: "Save" }),
      ).toBeVisible();
    });
  });

  test.describe("Error handling", () => {
    test("displays error toast when role list fails to load", async ({
      page,
    }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Inject error for role listing
      await injectError(page, "RoleBinding.List", "access denied");

      // Navigate to admin > Roles
      await page.locator("aside").getByText("Roles").click();

      // Error toast should appear
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("access denied");
    });

    test("displays error toast when role creation fails", async ({
      page,
    }) => {
      await navigateToRoles(page);

      // Inject error for role creation
      await injectError(page, "RoleBinding.Create", "role name conflict");

      // Open create form
      await page.getByRole("button", { name: "Create Role" }).click();
      await page.getByPlaceholder("Enter role name").fill("conflicting");

      // Submit
      await page.getByRole("button", { name: "Create" }).click();

      // Error toast should appear
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("role name conflict");

      // Clear the error
      await clearError(page, "RoleBinding.Create");
    });

    test("recovers from list error after clearing", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Inject error
      await injectError(page, "RoleBinding.List", "service unavailable");

      await page.locator("aside").getByText("Roles").click();

      // Error toast
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });

      // Clear the error
      await clearError(page, "RoleBinding.List");

      // Navigate away and back
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Roles").click();

      // Should show empty state
      await expect(page.getByText("No roles found.")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
    });
  });
});
