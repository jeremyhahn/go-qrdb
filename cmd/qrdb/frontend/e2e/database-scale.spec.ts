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
 * Database detail view — replica scaling ("Scale replicas") control.
 *
 * Mirrors admin-databases.spec.ts's navigation pattern: open the Databases
 * admin list, click into a dedicated-raft database's detail view, and drive
 * the "Replication" card's Target Replicas input + Scale Replicas button.
 * DatabaseBinding.Scale is the Wails binding under test (mocked in
 * fixtures/wails-mock.ts).
 */

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

const raftDbState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "raftdb", engine: "kv", backend: "pebble", status: "open", replicas: 1 },
  ],
};

const nonRaftDbState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "localdb", engine: "kv", backend: "pebble", status: "open" },
  ],
};

test.describe("Database Detail - replica scaling", () => {
  test.describe("dedicated-raft database", () => {
    test.use({ mockState: raftDbState });

    test("shows Replication card with current replication factor", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");
      await expect(main.getByText("raftdb")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await main.getByText("raftdb").click();

      await expect(main.getByRole("heading", { name: "Replication" })).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(main.locator("#db-target-replicas")).toHaveValue("1");
    });

    test("scaling replicas succeeds and shows a success toast", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");
      await main.getByText("raftdb").click();

      await expect(main.locator("#db-target-replicas")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await main.locator("#db-target-replicas").fill("3");
      await main.getByRole("button", { name: "Scale Replicas" }).click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText(
        'Database "raftdb" scaled to 3 replicas',
      );

      // The displayed replication factor should reflect the new value.
      await expect(main.getByText("3", { exact: true })).toBeVisible();
    });

    test("scale button is disabled for a target below 1", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");
      await main.getByText("raftdb").click();

      await expect(main.locator("#db-target-replicas")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await main.locator("#db-target-replicas").fill("0");

      await expect(
        main.getByRole("button", { name: "Scale Replicas" }),
      ).toBeDisabled();
    });

    test("error scaling replicas shows an error toast", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");
      await main.getByText("raftdb").click();
      await expect(main.locator("#db-target-replicas")).toBeVisible({ timeout: LOAD_TIMEOUT });

      await injectError(page, "DatabaseBinding.Scale", "quota exceeded");

      await main.locator("#db-target-replicas").fill("5");
      await main.getByRole("button", { name: "Scale Replicas" }).click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("quota exceeded");

      await clearError(page, "DatabaseBinding.Scale");
    });
  });

  test.describe("non-raft database", () => {
    test.use({ mockState: nonRaftDbState });

    test("does not show the Replication card for a non-raft database", async ({ page }) => {
      await navigateToDatabases(page);

      const main = page.locator("main");
      await expect(main.getByText("localdb")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await main.getByText("localdb").click();

      await expect(main.getByRole("button", { name: "Back" })).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(main.getByRole("heading", { name: "Replication" })).not.toBeVisible();
    });
  });
});
