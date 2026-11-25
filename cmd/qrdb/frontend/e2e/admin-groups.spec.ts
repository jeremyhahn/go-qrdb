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
 * Admin groups view: reached from AdminNav "Groups", driven over the
 * GroupBinding mock in fixtures/wails-mock.ts, which speaks the Go transport
 * group wire keys (tenantID, roleIDs, groupID, userID).
 */

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

/** Wire group as the mock GroupBinding returns it. */
interface GroupWire {
  id: string;
  tenantID: string;
  name: string;
  description: string;
  roleIDs: string[];
}

/** Waits for the app shell to load. */
async function waitForShell(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Clicks the Groups entry of the admin sidebar. */
async function openGroupsView(page: Page): Promise<void> {
  await page.locator("aside").getByText("Groups", { exact: true }).click();
  await expect(
    page.locator(".breadcrumbs").getByText("Groups", { exact: true }),
  ).toBeVisible();
}

/** Loads the app and navigates to the Groups view. */
async function navigateToGroups(page: Page): Promise<void> {
  await waitForShell(page);
  await openGroupsView(page);
}

/** Seeds a role via the mock RoleBinding and returns its id. */
async function seedRole(page: Page, name: string): Promise<string> {
  return page.evaluate((n: string) => {
    const w = window as unknown as Record<string, unknown>;
    const go = w.go as {
      main: {
        RoleBinding: {
          Create: (req: { name: string; permissions?: string[] }) => Promise<{ id: string }>;
        };
      };
    };
    return go.main.RoleBinding.Create({ name: n, permissions: ["kv:read"] }).then((r) => r.id);
  }, name);
}

/** Seeds a group via the mock GroupBinding, in wire form, and returns its id. */
async function seedGroup(page: Page, name: string, roleIDs: string[] = []): Promise<string> {
  return page.evaluate(
    ({ n, r }: { n: string; r: string[] }) => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as {
        main: {
          GroupBinding: {
            Create: (req: { name: string; tenantID?: string; roleIDs?: string[] }) => Promise<{ id: string }>;
          };
        };
      };
      return go.main.GroupBinding.Create({ name: n, tenantID: "default", roleIDs: r }).then((g) => g.id);
    },
    { n: name, r: roleIDs },
  );
}

/** Reads a group back from the mock GroupBinding by name. */
async function getGroupByName(page: Page, name: string): Promise<GroupWire | null> {
  return page.evaluate((n: string) => {
    const w = window as unknown as Record<string, unknown>;
    const go = w.go as {
      main: {
        GroupBinding: {
          GetByName: (req: { name: string; tenantID?: string }) => Promise<GroupWire>;
        };
      };
    };
    return go.main.GroupBinding.GetByName({ name: n, tenantID: "default" }).catch(() => null);
  }, name);
}

/** Opens the edit view of a listed group. */
async function openGroup(page: Page, name: string): Promise<void> {
  await page.getByRole("row").filter({ hasText: name }).first().click();
  await expect(page.getByText("Edit Group")).toBeVisible();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Group Management", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Groups is reachable from the admin navigation and starts empty", async ({ page }) => {
    await navigateToGroups(page);
    await expect(page.getByText("No groups found.")).toBeVisible();
  });

  test("lists seeded groups", async ({ page }) => {
    await waitForShell(page);
    await seedGroup(page, "engineering");
    await seedGroup(page, "auditors");
    await openGroupsView(page);

    await expect(page.getByRole("cell", { name: "engineering", exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: "auditors", exact: true })).toBeVisible();
    await expect(page.getByText("No groups found.")).not.toBeVisible();
  });

  test("creates a group with a role", async ({ page }) => {
    await waitForShell(page);
    const roleID = await seedRole(page, "reader");
    await openGroupsView(page);

    await page.getByRole("button", { name: "Create Group" }).click();
    await expect(page.getByText("Create Group").last()).toBeVisible();
    await page.getByTestId("group-name").fill("analysts");
    await page.getByTestId("group-description").fill("read-only analysts");
    await page.getByText("reader", { exact: true }).click();
    await page.getByRole("button", { name: "Create", exact: true }).click();

    await expect(page.getByRole("cell", { name: "analysts", exact: true })).toBeVisible();
    const group = await getGroupByName(page, "analysts");
    expect(group).not.toBeNull();
    expect(group!.roleIDs).toEqual([roleID]);
    expect(group!.description).toBe("read-only analysts");
  });

  test.describe("with a second tenant", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        tenants: [
          ...secondLaunchEmbeddedState.tenants,
          { ...secondLaunchEmbeddedState.tenants[0], id: "acme", name: "Acme Tenant" },
        ],
      },
    });

    test("switching the tenant reloads roles and clears the selection", async ({ page }) => {
      await waitForShell(page);
      const seed = (name: string, tenantId: string): Promise<string> =>
        page.evaluate(
          ([n, t]: [string, string]) => {
            const w = window as unknown as Record<string, unknown>;
            const go = w.go as {
              main: {
                RoleBinding: {
                  Create: (req: { name: string; tenantId: string; permissions?: string[] }) => Promise<{ id: string }>;
                };
              };
            };
            return go.main.RoleBinding.Create({ name: n, tenantId: t, permissions: ["kv:read"] }).then((r) => r.id);
          },
          [name, tenantId] as [string, string],
        );
      await seed("reader", "default");
      const auditorID = await seed("auditor", "acme");
      await openGroupsView(page);

      await page.getByRole("button", { name: "Create Group" }).click();
      await expect(page.getByText("Create Group").last()).toBeVisible();
      await page.getByTestId("group-name").fill("compliance");
      await page.getByText("reader", { exact: true }).click();
      await expect(page.locator("label", { hasText: "reader" }).locator("input[type=checkbox]")).toBeChecked();

      await page.locator("#group-detail-tenant").selectOption("acme");

      await expect(page.getByText("reader", { exact: true })).toHaveCount(0);
      await expect(page.getByText("auditor", { exact: true })).toBeVisible();
      await expect(page.locator("label", { hasText: "auditor" }).locator("input[type=checkbox]")).not.toBeChecked();
      await page.getByText("auditor", { exact: true }).click();
      await page.getByRole("button", { name: "Create", exact: true }).click();

      const stored = (): Promise<GroupWire | null> =>
        page.evaluate((n: string) => {
          const w = window as unknown as Record<string, unknown>;
          const go = w.go as {
            main: {
              GroupBinding: {
                GetByName: (req: { name: string; tenantID?: string }) => Promise<GroupWire>;
              };
            };
          };
          return go.main.GroupBinding.GetByName({ name: n, tenantID: "acme" }).catch(() => null);
        }, "compliance");
      await expect.poll(async () => (await stored())?.roleIDs ?? null).toEqual([auditorID]);
      expect((await stored())!.tenantID).toBe("acme");
    });
  });

  test("edits the roles of a group", async ({ page }) => {
    await waitForShell(page);
    const readerID = await seedRole(page, "reader");
    const writerID = await seedRole(page, "writer");
    await seedGroup(page, "ops", [readerID]);
    await openGroupsView(page);
    await openGroup(page, "ops");

    // Drop reader, add writer.
    await page.getByText("reader", { exact: true }).click();
    await page.getByText("writer", { exact: true }).click();
    await page.getByRole("button", { name: "Save", exact: true }).click();

    await expect(page.getByRole("cell", { name: "ops", exact: true })).toBeVisible();
    const group = await getGroupByName(page, "ops");
    expect(group!.roleIDs).toEqual([writerID]);
  });

  test("adds then removes a member", async ({ page }) => {
    await waitForShell(page);
    await seedGroup(page, "oncall");
    await openGroupsView(page);
    await openGroup(page, "oncall");

    const members = page.getByTestId("group-members");
    await page.getByTestId("group-member-id").fill("42");
    await page.getByRole("button", { name: "Add member" }).click();
    await expect(members.getByRole("listitem")).toHaveCount(1);
    await expect(members).toContainText("42");

    await members.getByRole("button", { name: "Remove" }).click();
    // The empty state is itself a list item, so count the member rows by
    // their Remove buttons and assert the empty state is shown.
    await expect(members.getByRole("button", { name: "Remove" })).toHaveCount(0);
    await expect(members).toContainText("No members.");
    await expect(members).not.toContainText("42");
  });

  test("deletes a group after confirmation", async ({ page }) => {
    await waitForShell(page);
    await seedGroup(page, "retired");
    await openGroupsView(page);
    await openGroup(page, "retired");

    const del = page.getByTestId("group-delete");
    await del.click();
    await expect(del).toHaveText("Confirm Delete");
    await del.click();

    await expect(page.getByText("No groups found.")).toBeVisible();
    expect(await getGroupByName(page, "retired")).toBeNull();
  });

  test("shows the error when deleting a group fails", async ({ page }) => {
    await waitForShell(page);
    await seedGroup(page, "guarded");
    await openGroupsView(page);
    await openGroup(page, "guarded");

    await injectError(page, "GroupBinding.Delete", "removing the Security Officer role is refused");
    const del = page.getByTestId("group-delete");
    await del.click();
    await del.click();
    await expect(page.locator('[role="alert"]').last()).toContainText(
      "removing the Security Officer role is refused",
    );
    await expect(page.getByText("Edit Group")).toBeVisible();
    await clearError(page, "GroupBinding.Delete");
  });
});
