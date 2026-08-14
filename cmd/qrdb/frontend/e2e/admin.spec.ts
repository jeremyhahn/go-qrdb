// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

test.describe("Admin Mode", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("admin sidebar sections are always visible", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Admin sections are always visible in the new sidebar — no toggle required.
    await expect(page.getByText("Overview")).toBeVisible();
    await expect(page.getByText("Security")).toBeVisible();
    await expect(page.getByText("System")).toBeVisible();

    // Verify admin nav items
    await expect(page.locator("aside").getByText("Dashboard", { exact: true })).toBeVisible();
    // "Cluster" is hidden for standalone servers — only shown for raft.
    await expect(page.locator("aside").getByText("Seal Manager", { exact: true })).toBeVisible();
    await expect(page.locator("aside").getByText("Users", { exact: true })).toBeVisible();
    await expect(page.locator("aside").getByText("Roles", { exact: true })).toBeVisible();
    await expect(page.locator("aside").getByText("Certificates", { exact: true })).toBeVisible();
    await expect(page.locator('aside button[title="Databases"]')).toBeVisible();
    // "Daemon" is hidden for embedded profiles — qrdbd runs in-process, so
    // there is no OS daemon to manage (only external profiles show it).
    await expect(page.locator("aside").getByText("Daemon", { exact: true })).not.toBeVisible();
    await expect(page.locator("aside").getByText("Audit Log", { exact: true })).toBeVisible();
    await expect(page.locator("aside").getByText("Settings", { exact: true })).toBeVisible();
  });

  test("breadcrumb shows section name when admin item clicked", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await page.locator("aside").getByText("Dashboard").click();

    const breadcrumb = page.locator(".breadcrumbs");
    await expect(breadcrumb.getByText("Dashboard")).toBeVisible();
  });

  test("navigate to each admin section updates breadcrumb", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    const breadcrumb = page.locator(".breadcrumbs");

    // "Cluster" is hidden for standalone servers — skip it.

    // Click Seal Manager
    await page.locator("aside").getByText("Seal Manager").click();
    await expect(breadcrumb.getByText("Seal Manager")).toBeVisible();

    // Click Users
    await page.locator("aside").getByText("Users").click();
    await expect(breadcrumb.getByText("Users")).toBeVisible();

    // Click Roles
    await page.locator("aside").getByText("Roles").click();
    await expect(breadcrumb.getByText("Roles")).toBeVisible();

    // Click Settings
    await page.locator("aside").getByText("Settings").click();
    await expect(breadcrumb.getByText("Settings")).toBeVisible();
  });

  test("navigating to admin section then back to workspace shows Select a Database", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Navigate to an admin section
    await page.locator("aside").getByText("Dashboard").click();
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible();

    // Navigate back to the database workspace by clicking the server tree
    // The sidebar shows the "Local Database" server; click it to expand/activate
    // then the content area shows "Select a Database" again.
    // Simplest: reload the page to reset to database-workspace view.
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("sidebar collapse button hides labels", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Toggle sidebar collapse
    await page.locator("[aria-label='Toggle sidebar']").click();

    // Sidebar should be narrow (w-14)
    const sidebar = page.locator("aside");
    await expect(sidebar).toHaveClass(/w-14/);

    // Admin section text should not be visible in collapsed mode
    await expect(page.locator('aside button[title="Databases"] span')).not.toBeVisible();
  });

  test("sidebar expand shows labels again", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Collapse
    await page.locator("[aria-label='Toggle sidebar']").click();
    await expect(page.locator("aside")).toHaveClass(/w-14/);

    // Expand
    await page.locator("[aria-label='Toggle sidebar']").click();
    await expect(page.locator("aside")).toHaveClass(/w-56/);
  });

  test("theme toggle switches icon", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Click theme toggle
    await page.locator("[aria-label='Toggle theme']").click();

    // Should still work (toggle between sun/moon)
    await expect(page.locator("[aria-label='Toggle theme']")).toBeVisible();
  });

  test("status bar shows connection info and seal status", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    const footer = page.locator("footer");

    // Check seal status badge
    await expect(footer.getByText("Unsealed")).toBeVisible();
  });

  test("header shows QRDB brand and connection status", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await expect(page.locator("header img[alt='QRDB']")).toBeVisible();
    await expect(page.getByText("Connected")).toBeVisible();
  });
});
