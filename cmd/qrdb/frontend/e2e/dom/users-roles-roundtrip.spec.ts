// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * users-roles-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Users and Roles admin views against a live qrdbd (seal "none",
 * auth off, so the harness runs as the system-tenant admin):
 *
 *   1. Roles → create two custom roles.
 *   2. Users → create a user holding the first role.
 *   3. Both appear in their lists and detail views.
 *   4. User detail → move the user from the first role to the second
 *      (roles.unassign / roles.assign).
 *   5. Delete the user, then both roles.
 *
 * Every step is checked after a full page reload, so the state shown comes
 * from the daemon and not from the view's local state.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const ROLE_A = "e2e-role-a";
const ROLE_B = "e2e-role-b";
const USER = "e2e-user";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens an admin view. */
async function openAdminView(page: Page, label: "Users" | "Roles", createLabel: string): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: label, exact: true }).click();
  await expect(page.getByRole("button", { name: createLabel })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Expects the success toast with the given text. */
async function expectToast(page: Page, text: string): Promise<void> {
  // On a miss, report the error toasts on screen, so a daemon refusal is
  // named by its message rather than as a missing success toast.
  try {
    await expect(page.getByText(text).last()).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const errors = await page.locator(".toast .alert-error").allInnerTexts();
    throw new Error(`expected toast "${text}"; error toasts: ${JSON.stringify(errors)}\n${err}`);
  }
}

/** Creates a custom role through the Role detail form. */
async function createRole(page: Page, name: string): Promise<void> {
  await openAdminView(page, "Roles", "Create Role");
  await page.getByRole("button", { name: "Create Role" }).click();
  await expect(page.getByRole("heading", { name: "Create Role" })).toBeVisible();
  await page.getByPlaceholder("Enter role name").fill(name);
  // A fresh daemon has no "default" tenant: the form starts on System.
  await expect(page.locator("#role-detail-tenant")).toHaveValue("system");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expectToast(page, `Role "${name}" created`);
}

/** Opens the detail view of the named row in the current list. */
async function openRow(page: Page, text: string): Promise<void> {
  await page.getByRole("cell", { name: text, exact: true }).first().click();
  // A row click opens the row; it never copies, so no clipboard toast.
  await expect(page.locator(".toast .alert-error")).toHaveCount(0);
}

/** Opens the user's detail view after a reload. */
async function openUser(page: Page): Promise<void> {
  await openAdminView(page, "Users", "Create User");
  await openRow(page, USER);
  await expect(page.getByPlaceholder("Enter username")).toHaveValue(USER, { timeout: LOAD_TIMEOUT });
}

test("roles + users create, assign, reassign, delete round-trip (UI ↔ live daemon)", async ({ page }) => {
  // Some twenty page loads against a live daemon: past the 30 s default.
  test.setTimeout(120_000);
  expect(daemon).not.toBeNull();

  // --- 1. Two custom roles ---------------------------------------------------
  await createRole(page, ROLE_A);
  await createRole(page, ROLE_B);

  await openAdminView(page, "Roles", "Create Role");
  await expect(page.getByRole("cell", { name: ROLE_A, exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("cell", { name: ROLE_B, exact: true })).toBeVisible();

  // Role detail shows the persisted role.
  await openRow(page, ROLE_A);
  await expect(page.getByRole("heading", { name: "Edit Role" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByPlaceholder("Enter role name")).toHaveValue(ROLE_A);
  await expect(page.getByTestId("role-delete")).toBeVisible();

  // --- 2. A user holding role A ----------------------------------------------
  await openAdminView(page, "Users", "Create User");
  await page.getByRole("button", { name: "Create User" }).click();
  await page.getByPlaceholder("Enter username").fill(USER);
  await page.getByTestId("user-email").fill(`${USER}@example.test`);
  await expect(page.locator("#user-detail-tenant")).toHaveValue("system");
  await page.getByTestId(`user-role-${ROLE_A}`).check();
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expectToast(page, `User "${USER}" created`);

  // --- 3. Listed, and the detail shows role A only ---------------------------
  await openUser(page);
  await expect(page.getByTestId(`user-role-${ROLE_A}`)).toBeChecked();
  await expect(page.getByTestId(`user-role-${ROLE_B}`)).not.toBeChecked();

  // --- 4. Move the user from role A to role B --------------------------------
  await page.getByTestId(`user-role-${ROLE_B}`).check();
  await expectToast(page, "Role assigned");
  await page.getByTestId(`user-role-${ROLE_A}`).uncheck();
  await expectToast(page, "Role unassigned");

  await openUser(page);
  await expect(page.getByTestId(`user-role-${ROLE_A}`)).not.toBeChecked();
  await expect(page.getByTestId(`user-role-${ROLE_B}`)).toBeChecked();

  // --- 5. Delete the user, then both roles -----------------------------------
  await page.getByTestId("user-delete").click();
  await expect(page.getByTestId("user-delete")).toHaveText("Confirm Delete");
  await page.getByTestId("user-delete").click();
  await expectToast(page, `User "${USER}" deleted`);

  await openAdminView(page, "Users", "Create User");
  await expect(page.getByRole("cell", { name: USER, exact: true })).toHaveCount(0);

  for (const role of [ROLE_A, ROLE_B]) {
    await openAdminView(page, "Roles", "Create Role");
    await openRow(page, role);
    await expect(page.getByPlaceholder("Enter role name")).toHaveValue(role, { timeout: LOAD_TIMEOUT });
    await page.getByTestId("role-delete").click();
    await expect(page.getByTestId("role-delete")).toHaveText("Confirm Delete");
    await page.getByTestId("role-delete").click();
    await expectToast(page, `Role "${role}" deleted`);
  }

  await openAdminView(page, "Roles", "Create Role");
  // The list has loaded (system roles are always present) before the absence check.
  await expect(page.getByRole("cell", { name: "System" }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("cell", { name: ROLE_A, exact: true })).toHaveCount(0);
  await expect(page.getByRole("cell", { name: ROLE_B, exact: true })).toHaveCount(0);
});
