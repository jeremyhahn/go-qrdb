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
import { webTest } from "./fixtures/web-mode-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to admin mode and clicks the Users sidebar link. */
async function navigateToUsers(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Toggle to admin mode

  // Click Users in sidebar
  await page.locator("aside").getByText("Users").click();
  await expect(page.locator(".breadcrumbs").getByText("Users")).toBeVisible();
}

/**
 * Seeds a user via the mock binding and returns the created user ID.
 * Must be called before page.goto() or after ensuring mock state is accessible.
 */
async function seedUser(
  page: Page,
  username: string,
  tenant: string = "default",
): Promise<string> {
  return page.evaluate(
    ({ u, t }: { u: string; t: string }) => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as {
        main: {
          UserBinding: {
            Create: (req: {
              username: string;
              tenant?: string;
            }) => Promise<{ id: string }>;
          };
        };
      };
      return go.main.UserBinding.Create({ username: u, tenant: t }).then(
        (r) => r.id,
      );
    },
    { u: username, t: tenant },
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - User Management", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test.describe("User list", () => {
    test("displays empty state when no users exist", async ({ page }) => {
      await navigateToUsers(page);

      // CrudTable shows empty message
      await expect(page.getByText("No users found.")).toBeVisible();

      // Badge should show count of 0
      await expect(page.locator(".badge-ghost").getByText("0")).toBeVisible();
    });

    test("displays users in the table after creation", async ({ page }) => {
      await navigateToUsers(page);

      // Seed users via mock binding
      await seedUser(page, "alice");
      await seedUser(page, "bob");

      // Refresh the page to trigger re-fetch
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Users").click();

      // Wait for users to appear in the table
      await expect(page.getByText("alice")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByText("bob")).toBeVisible();

      // Badge should show count of 2
      await expect(page.locator(".badge-ghost").getByText("2")).toBeVisible();
    });

    test("shows Create User button in toolbar", async ({ page }) => {
      await navigateToUsers(page);

      await expect(
        page.getByRole("button", { name: "Create User" }),
      ).toBeVisible();
    });
  });

  test.describe("Create user", () => {
    test("opens create form when Create User button is clicked", async ({
      page,
    }) => {
      await navigateToUsers(page);

      await page.getByRole("button", { name: "Create User" }).click();

      // UserDetail should show "Create User" heading
      await expect(
        page.getByText("Create User", { exact: false }),
      ).toBeVisible();

      // Back button should be visible
      await expect(
        page.getByRole("button", { name: /^Back$/ }),
      ).toBeVisible();

      // Username input should be present
      await expect(page.getByPlaceholder("Enter username")).toBeVisible();
    });

    test("creates a new user and returns to user list", async ({ page }) => {
      await navigateToUsers(page);

      // Click create
      await page.getByRole("button", { name: "Create User" }).click();

      // Fill the form
      await page.getByPlaceholder("Enter username").fill("charlie");

      // Submit
      await page.getByRole("button", { name: "Create" }).click();

      // Toast should confirm creation
      await expect(page.locator('[role="alert"]')).toBeVisible();

      // Should return to user list and show the new user in the table
      await expect(
        page.getByText("charlie", { exact: true }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("shows validation when username is empty", async ({ page }) => {
      await navigateToUsers(page);

      await page.getByRole("button", { name: "Create User" }).click();

      // Leave username empty - the Create button should be disabled
      const createBtn = page.getByRole("button", { name: "Create" });
      await expect(createBtn).toBeDisabled();
    });

    test("cancel returns to user list without creating", async ({ page }) => {
      await navigateToUsers(page);

      await page.getByRole("button", { name: "Create User" }).click();
      await expect(page.getByPlaceholder("Enter username")).toBeVisible();

      // Click Cancel
      await page.getByRole("button", { name: "Cancel" }).click();

      // Should return to user list
      await expect(page.getByText("No users found.")).toBeVisible();
    });
  });

  test.describe("Create user without a default tenant", () => {
    // A fresh daemon lists only the System tenant; there is no "default".
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        tenants: [{ id: "system", name: "System" }],
      },
    });

    test("tenant defaults to the first tenant the server lists", async ({
      page,
    }) => {
      await navigateToUsers(page);
      await page.getByRole("button", { name: "Create User" }).click();

      const tenantSelect = page.locator("select#user-detail-tenant");
      await expect(tenantSelect).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(tenantSelect).toHaveValue("system");

      await page.getByPlaceholder("Enter username").fill("dana");
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByText("dana", { exact: true })).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
    });
  });

  test.describe("Edit user", () => {
    test("clicking a user row opens the edit form", async ({ page }) => {
      await navigateToUsers(page);

      // Seed a user
      await seedUser(page, "diana");

      // Refresh the list
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Users").click();

      // Wait for user to appear
      await expect(page.getByText("diana")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Click the row
      await page.getByText("diana").click();

      // Should show the edit form
      await expect(page.getByText("Edit User")).toBeVisible();

      // Username field should be populated
      const usernameInput = page.getByPlaceholder("Enter username");
      await expect(usernameInput).toHaveValue("diana");
    });

    test("back button returns to user list from edit form", async ({
      page,
    }) => {
      await navigateToUsers(page);

      await seedUser(page, "eve");

      // Refresh
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Users").click();
      await expect(page.getByText("eve")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // Open edit
      await page.getByText("eve").click();
      await expect(page.getByText("Edit User")).toBeVisible();

      // Click Back
      await page.getByRole("button", { name: /^Back$/ }).click();

      // Should return to user list
      await expect(
        page.locator(".badge-ghost").getByText("1"),
      ).toBeVisible();
    });
  });

  test.describe("User lifecycle display", () => {
    test("shows the lifecycle state for active users", async ({ page }) => {
      await navigateToUsers(page);

      // Seed an active user
      await seedUser(page, "frank");

      // Refresh
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Users").click();

      await expect(page.getByText("frank")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // The Lifecycle column should show "active"
      await expect(page.getByRole("cell", { name: "active", exact: true })).toBeVisible();
    });
  });

  test.describe("User operations", () => {
    /** Reads the mock's stored user record through the Wails binding. */
    async function mockUser(
      page: Page,
      id: string,
    ): Promise<{ lifecycle: string; roleIds: string[] }> {
      return page.evaluate((uid: string) => {
        const w = window as unknown as {
          go: {
            main: {
              UserBinding: {
                Get: (id: string) => Promise<{ lifecycle: string; roleIds: string[] }>;
              };
            };
          };
        };
        return w.go.main.UserBinding.Get(uid);
      }, id);
    }

    /** Creates a role through the mock binding and returns its id. */
    async function seedRole(page: Page, name: string): Promise<string> {
      return page.evaluate((n: string) => {
        const w = window as unknown as {
          go: {
            main: {
              RoleBinding: {
                Create: (req: { name: string }) => Promise<{ id: string }>;
              };
            };
          };
        };
        return w.go.main.RoleBinding.Create({ name: n }).then((r) => r.id);
      }, name);
    }

    /** Binds a thumbprint to a user through the mock binding. */
    async function seedBinding(page: Page, id: string, thumbprint: string): Promise<void> {
      await page.evaluate(
        ({ uid, tp }: { uid: string; tp: string }) => {
          const w = window as unknown as {
            go: {
              main: {
                UserBinding: {
                  AddCertBinding: (id: string, tp: string, n: number) => Promise<unknown>;
                };
              };
            };
          };
          return w.go.main.UserBinding.AddCertBinding(uid, tp, 0);
        },
        { uid: id, tp: thumbprint },
      );
    }

    /** Re-fetches the user list and opens the named user's detail view. */
    async function openUser(page: Page, name: string): Promise<void> {
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Users").click();
      await expect(page.getByText(name, { exact: true })).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await page.getByText(name, { exact: true }).click();
      await expect(page.getByText("Edit User")).toBeVisible();
    }

    test("disables an active user", async ({ page }) => {
      await navigateToUsers(page);
      const id = await seedUser(page, "dis-user");
      await openUser(page, "dis-user");

      await page.getByTestId("user-disable").click();
      await expect(page.getByTestId("user-enable")).toBeVisible();
      expect((await mockUser(page, id)).lifecycle).toBe("disabled");
    });

    test("enables a disabled user", async ({ page }) => {
      await navigateToUsers(page);
      const id = await seedUser(page, "en-user");
      await page.evaluate((uid: string) => {
        const w = window as unknown as {
          go: { main: { UserBinding: { Disable: (id: string) => Promise<void> } } };
        };
        return w.go.main.UserBinding.Disable(uid);
      }, id);
      await openUser(page, "en-user");

      await page.getByTestId("user-enable").click();
      await expect(page.getByTestId("user-disable")).toBeVisible();
      expect((await mockUser(page, id)).lifecycle).toBe("active");
    });

    test("deletes a user after confirmation", async ({ page }) => {
      await navigateToUsers(page);
      await seedUser(page, "del-user");
      await openUser(page, "del-user");

      const del = page.getByTestId("user-delete");
      await del.click();
      await expect(del).toHaveText("Confirm Delete");
      await del.click();
      await expect(page.getByText("No users found.")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
    });

    test("adds a certificate binding", async ({ page }) => {
      await navigateToUsers(page);
      await seedUser(page, "bind-user");
      await openUser(page, "bind-user");

      await page.getByTestId("user-binding-thumbprint").fill("tp-add-1");
      await page.getByTestId("user-binding-add").click();
      await expect(page.getByTestId("user-binding-row")).toContainText("tp-add-1");
      await expect(page.locator('[role="alert"]').first()).toContainText(
        "Certificate binding added",
      );
    });

    test("removes a certificate binding", async ({ page }) => {
      await navigateToUsers(page);
      await seedUser(page, "unbind-user");
      await openUser(page, "unbind-user");

      await page.getByTestId("user-binding-thumbprint").fill("tp-rm-1");
      await page.getByTestId("user-binding-add").click();
      await expect(page.getByTestId("user-binding-row")).toHaveCount(1);
      await page.getByRole("button", { name: "Remove binding tp-rm-1" }).click();
      await expect(page.getByTestId("user-binding-row")).toHaveCount(0);

      // The mock no longer resolves the thumbprint.
      await page.getByRole("button", { name: /^Back$/ }).click();
      await page.getByTestId("user-thumbprint-input").fill("tp-rm-1");
      await page.getByTestId("user-thumbprint-submit").click();
      await expect(page.locator('[role="alert"]').last()).toContainText("no user bound");
    });

    test("finds a user by certificate thumbprint", async ({ page }) => {
      await navigateToUsers(page);
      const id = await seedUser(page, "found-user");
      await seedBinding(page, id, "tp-find-1");
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Users").click();

      await page.getByTestId("user-thumbprint-input").fill("tp-find-1");
      await page.getByTestId("user-thumbprint-submit").click();
      await expect(page.getByText("Edit User")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByPlaceholder("Enter username")).toHaveValue("found-user");
    });

    test("assigns a role to an existing user at once", async ({ page }) => {
      await navigateToUsers(page);
      const id = await seedUser(page, "assign-user");
      const roleId = await seedRole(page, "assign-role");
      await openUser(page, "assign-user");

      await page.getByTestId("user-role-assign-role").check();
      await expect(page.getByText("Role assigned")).toBeVisible();
      expect((await mockUser(page, id)).roleIds).toContain(roleId);
    });

    test("unassigns a role from an existing user at once", async ({ page }) => {
      await navigateToUsers(page);
      const id = await seedUser(page, "unassign-user");
      const roleId = await seedRole(page, "unassign-role");
      await page.evaluate(
        ({ uid, rid }: { uid: string; rid: string }) => {
          const w = window as unknown as {
            go: {
              main: { RoleBinding: { Assign: (u: string, r: string) => Promise<void> } };
            };
          };
          return w.go.main.RoleBinding.Assign(uid, rid);
        },
        { uid: id, rid: roleId },
      );
      await openUser(page, "unassign-user");

      const box = page.getByTestId("user-role-unassign-role");
      await expect(box).toBeChecked();
      await box.uncheck();
      await expect(page.getByText("Role unassigned")).toBeVisible();
      expect((await mockUser(page, id)).roleIds).not.toContain(roleId);
    });

    test("shows an error toast when adding a binding fails", async ({ page }) => {
      await navigateToUsers(page);
      await seedUser(page, "bind-err-user");
      await openUser(page, "bind-err-user");

      await injectError(page, "UserBinding.AddCertBinding", "thumbprint already bound");
      await page.getByTestId("user-binding-thumbprint").fill("tp-err-1");
      await page.getByTestId("user-binding-add").click();
      await expect(page.locator('[role="alert"]').last()).toContainText(
        "thumbprint already bound",
      );
      await expect(page.getByTestId("user-binding-row")).toHaveCount(0);
      await clearError(page, "UserBinding.AddCertBinding");
    });
  });

  test.describe("Error handling", () => {
    test("displays error toast when user list fails to load", async ({
      page,
    }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Inject error for user listing
      await injectError(page, "UserBinding.List", "permission denied");

      // Navigate to admin > Users
      await page.locator("aside").getByText("Users").click();

      // Error toast should appear
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("permission denied");
    });

    test("displays error toast when user creation fails", async ({
      page,
    }) => {
      await navigateToUsers(page);

      // Inject error for user creation
      await injectError(page, "UserBinding.Create", "username already exists");

      // Open create form
      await page.getByRole("button", { name: "Create User" }).click();
      await page.getByPlaceholder("Enter username").fill("duplicate");

      // Submit
      await page.getByRole("button", { name: "Create" }).click();

      // Error toast should appear
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText(
        "username already exists",
      );

      // Clear the error for subsequent tests
      await clearError(page, "UserBinding.Create");
    });

    test("recovers from list error after clearing", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Inject error
      await injectError(page, "UserBinding.List", "temporary failure");

      await page.locator("aside").getByText("Users").click();

      // Error toast should appear
      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });

      // Clear the error
      await clearError(page, "UserBinding.List");

      // Navigate away and back to trigger re-fetch
      await page.locator("aside").getByText("Dashboard").click();
      await page.locator("aside").getByText("Users").click();

      // Should now show the empty state without error
      await expect(page.getByText("No users found.")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
    });
  });
});

// ---------------------------------------------------------------------------
// Web-mode regression: user routes must respond with non-404 regardless of
// whether --auth-enabled is set on the server (Bug 2 regression).
// ---------------------------------------------------------------------------

/**
 * Web-mode tests that intercept REST calls and verify the frontend correctly
 * handles 2xx responses from /users routes — specifically confirming that no
 * 404 "route not registered" toast surfaces when --auth-enabled=false.
 */
webTest.describe("Admin - User Management (web mode / auth-disabled regression)", () => {
  const LOAD_TIMEOUT = 10_000;

  webTest(
    "POST /users returns 201 and frontend shows success (auth-disabled regression)",
    async ({ page }) => {
      // Intercept user creation to simulate an auth-disabled server
      // that now has /users routes registered unconditionally.
      await page.route("**/users", async (route) => {
        if (route.request().method() === "POST") {
          await route.fulfill({
            status: 201,
            contentType: "application/json",
            body: JSON.stringify({
              id: "web-user-001",
              username: "auth-disabled-test",
              tenantID: "default",
              roleIDs: [],
              lifecycle: "active",
              system: false,
              createdAt: "2026-01-01T00:00:00Z",
              updatedAt: "2026-01-01T00:00:00Z",
            }),
          });
        } else {
          await route.fallback();
        }
      });

      // Intercept the user list refresh.
      await page.route("**/users/list**", (route) => {
        void route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            users: [
              {
                id: "web-user-001",
                username: "auth-disabled-test",
                tenantID: "default",
                roleIDs: [],
                lifecycle: "active",
                system: false,
                createdAt: "2026-01-01T00:00:00Z",
                updatedAt: "2026-01-01T00:00:00Z",
              },
            ],
            total: 1,
            hasMore: false,
          }),
        });
      });

      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const usersSidebar = page.locator("aside").getByText("Users");
      if (!(await usersSidebar.isVisible())) {
        return; // Not in web-admin mode — skip.
      }

      await usersSidebar.click();
      await expect(
        page.locator(".breadcrumbs").getByText("Users"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Click Create User button.
      const createBtn = page.getByRole("button", { name: "Create User" });
      if (!(await createBtn.isVisible())) {
        return;
      }
      await createBtn.click();

      // Fill the form.
      const usernameInput = page.getByPlaceholder("Enter username");
      if (await usernameInput.isVisible()) {
        await usernameInput.fill("auth-disabled-test");
        await page.getByRole("button", { name: "Create" }).click();
      }

      // Assert: no 404 toast must appear.
      const alerts = page.locator('[role="alert"]');
      const alertCount = await alerts.count();
      for (let i = 0; i < alertCount; i++) {
        const text = await alerts.nth(i).textContent();
        if (text?.includes("404") || text?.includes("not found")) {
          throw new Error(
            `User creation surfaced a 404 toast: "${text}". POST /users route is not registered when auth-enabled=false.`,
          );
        }
      }
    },
  );

  webTest(
    "GET /users/list returns 200 when auth is disabled (route registered unconditionally)",
    async ({ page }) => {
      let listCalled = false;
      await page.route("**/users/list**", (route) => {
        listCalled = true;
        void route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ users: [], total: 0, hasMore: false }),
        });
      });

      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const usersSidebar = page.locator("aside").getByText("Users");
      if (!(await usersSidebar.isVisible())) {
        return;
      }

      await usersSidebar.click();
      await expect(
        page.locator(".breadcrumbs").getByText("Users"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      if (!listCalled) {
        const alert = page.locator('[role="alert"]');
        if (await alert.isVisible()) {
          const text = await alert.textContent();
          if (text?.includes("404") || text?.includes("not found")) {
            throw new Error(
              `User list surfaced a 404 toast: "${text}". GET /users/list is not registered when auth-enabled=false.`,
            );
          }
        }
      }
    },
  );
});
