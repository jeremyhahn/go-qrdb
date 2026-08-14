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
 * Exercises full admin sidebar navigation in a single sequential pass,
 * verifying breadcrumb updates for every admin section.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  raftClusterState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async DOM operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the root and waits for the landing heading to appear. */
async function goToRoot(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/**
 * Admin sections are always visible in the sidebar — no toggle required.
 * This helper is a no-op kept for call-site readability.
 */
async function enterAdminMode(_page: Page): Promise<void> {
  // Admin items are always visible; no toggle needed in the new sidebar.
}

/** Clicks a sidebar item and waits for the breadcrumb to show the section name. */
async function navigateToSection(page: Page, section: string): Promise<void> {
  // Use title attribute to uniquely target admin nav buttons — avoids strict-mode
  // violations when "Databases" appears both as a section header and an admin nav item.
  await page.locator(`aside button[title="${section}"]`).click();
  await expect(
    page.locator(".breadcrumbs").getByText(section),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// The "all admin sections" test visits the Cluster section which is only
// visible when serverMode is "raft". Use raftClusterState for that test.
test.describe("Admin Sidebar - Complete Navigation (raft)", () => {
  test.use({ mockState: raftClusterState });

  test("all admin sections are accessible from sidebar", async ({ page }) => {
    await goToRoot(page);
    await enterAdminMode(page);

    // Walk every admin section in sidebar order and confirm breadcrumb updates.
    // Cluster is included because raftClusterState sets serverMode: "raft".
    const sections = [
      "Dashboard",
      "Cluster",
      "Rebalance",
      "Seal Manager",
      "Tenants",
      "Users",
      "Roles",
      "Certificates",
      "Credentials",
      "Databases",
      "Backups",
      "Audit Log",
      "Settings",
    ] as const;

    for (const section of sections) {
      await navigateToSection(page, section);
    }
  });
});

// Non-cluster sidebar tests use the default standalone state.
test.describe("Admin Sidebar - Complete Navigation", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("admin sections are always present in sidebar", async ({ page }) => {
    await goToRoot(page);

    // Verify a representative set of admin sidebar items is always present.
    await expect(
      page.locator("aside").getByText("Seal Manager", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator("aside").getByText("Dashboard", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator("aside").getByText("Users", { exact: true }),
    ).toBeVisible();
  });

  test("breadcrumb shows section name when admin item clicked", async ({ page }) => {
    await goToRoot(page);
    await enterAdminMode(page);
    await navigateToSection(page, "Seal Manager");

    const breadcrumb = page.locator(".breadcrumbs");

    // The section name must appear in the breadcrumb.
    await expect(breadcrumb.getByText("Seal Manager")).toBeVisible();
  });
});
