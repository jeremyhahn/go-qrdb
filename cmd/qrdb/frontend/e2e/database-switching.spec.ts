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
} from "./fixtures/wails-mock.ts";

/** Timeout for async DOM operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock state with multiple databases covering different engine types.
// ---------------------------------------------------------------------------

const multiDbState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "alpha", engine: "kv", backend: "pebble", status: "open" },
    { name: "beta", engine: "kv", backend: "memory", status: "open" },
    { name: "gamma", engine: "timeseries", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Database Switching", () => {
  test.use({ mockState: multiDbState });

  test("sidebar shows all databases", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await expect(sidebar.getByText("alpha")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(sidebar.getByText("beta")).toBeVisible();
    await expect(sidebar.getByText("gamma")).toBeVisible();
  });

  test("clicking database loads its workspace", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await sidebar.getByText("alpha").click();

    await expect(
      page.locator(".breadcrumbs").getByText("alpha", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("switching databases updates workspace", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");

    // Select alpha first.
    await sidebar.getByText("alpha").click();
    await expect(
      page.locator(".breadcrumbs").getByText("alpha", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Switch to beta.
    await sidebar.getByText("beta").click();
    await expect(
      page.locator(".breadcrumbs").getByText("beta", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // alpha heading must no longer be the active workspace heading.
    await expect(
      page.locator("main").getByRole("heading", { name: "alpha" }),
    ).not.toBeVisible();
  });

  test("database engine badges display correctly", async ({ page }) => {
    await page.goto("/");

    const sidebar = page.locator("aside");
    await expect(sidebar.getByText("alpha")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // alpha and beta are kv engines — both "kv" badges must appear.
    const kvBadges = sidebar.getByText("kv", { exact: true });
    await expect(kvBadges).toHaveCount(2);

    // gamma is a timeseries engine — sidebar displays the short label "ts".
    await expect(sidebar.getByText("ts", { exact: true })).toBeVisible();
  });

  test("KV operation on selected database works", async ({ page }) => {
    await page.goto("/");

    // Navigate to the alpha database.
    const sidebar = page.locator("aside");
    await sidebar.getByText("alpha").click();
    await expect(
      page.locator(".breadcrumbs").getByText("alpha", { exact: false }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Open the create entry panel.
    await page.getByRole("button", { name: "Create", exact: true }).first().click();
    await expect(page.getByText("Create Entry")).toBeVisible();

    // Fill in key and value.
    await page.locator("input[placeholder='Enter key']").fill("switch-test-key");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("switch-test-value");

    // Submit.
    await page.getByRole("button", { name: "Create", exact: true }).last().click();

    // Verify success toast.
    await expect(
      page.getByText('Created "switch-test-key"'),
    ).toBeVisible({ timeout: 5_000 });

    // Verify the entry is shown in the table.
    await expect(page.getByText("switch-test-key").first()).toBeVisible();
  });
});
