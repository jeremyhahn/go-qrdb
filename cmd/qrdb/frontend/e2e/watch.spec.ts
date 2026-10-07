// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const watchState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "watch-db", backend: "pebble", status: "open" },
  ],
};

/** Navigate to Watch tab. */
async function navigateToWatch(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("watch-db").click();
  await expect(page.locator(".breadcrumbs").getByText("watch-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Watch", { exact: true }).click();
}

test.describe("Watch View", () => {
  test.use({ mockState: watchState });

  test("shows compact creation form with all elements", async ({ page }) => {
    await navigateToWatch(page);

    // Verify inline creation form
    await expect(page.locator("input[placeholder='Exact key...']")).toBeVisible();
    await expect(page.locator("input[placeholder='Start rev']")).toBeVisible();
    // exact: true — the page description also contains "prefixes".
    await expect(page.getByText("Prefix", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Start Watch" })).toBeVisible();
  });

  test("shows event stream with waiting placeholder", async ({ page }) => {
    await navigateToWatch(page);

    await expect(page.getByRole("heading", { name: "Events" })).toBeVisible();
    await expect(page.getByText("Waiting for events...")).toBeVisible();
  });

  test("start watch without key shows warning", async ({ page }) => {
    await navigateToWatch(page);

    await page.getByRole("button", { name: "Start Watch" }).click();
    await expect(page.getByText("Key or prefix is required")).toBeVisible({ timeout: 5_000 });
  });

  test("start watch with key shows success", async ({ page }) => {
    await navigateToWatch(page);

    // Fill key
    await page.locator("input[placeholder='Exact key...']").fill("mykey");

    // Start watch
    await page.getByRole("button", { name: "Start Watch" }).click();

    // Verify success toast
    await expect(page.getByText("Watch started successfully")).toBeVisible({ timeout: 5_000 });

    // Verify active watch chip appears
    await expect(page.getByText("1 active watch")).toBeVisible();
    await expect(page.getByText("mykey")).toBeVisible();
  });

  test("prefix toggle changes input placeholder", async ({ page }) => {
    await navigateToWatch(page);

    // Default: exact key placeholder
    await expect(page.locator("input[placeholder='Exact key...']")).toBeVisible();

    // Toggle prefix on
    await page.locator(".toggle-primary").click();

    // Placeholder should change to prefix
    await expect(page.locator("input[placeholder='Key prefix...']")).toBeVisible();
  });

  test("start multiple watches shows count", async ({ page }) => {
    await navigateToWatch(page);

    // Start first watch
    await page.locator("input[placeholder='Exact key...']").fill("key1");
    await page.getByRole("button", { name: "Start Watch" }).click();
    await expect(page.getByText("Watch started successfully")).toBeVisible({ timeout: 5_000 });

    // Input should be cleared after successful watch
    await expect(page.locator("input[placeholder='Exact key...']")).toHaveValue("");

    // Start second watch
    await page.locator("input[placeholder='Exact key...']").fill("key2");
    await page.getByRole("button", { name: "Start Watch" }).click();

    // Verify count
    await expect(page.getByText("2 active watches")).toBeVisible();
  });

  test("cancel all watches clears active list", async ({ page }) => {
    await navigateToWatch(page);

    // Start a watch
    await page.locator("input[placeholder='Exact key...']").fill("testkey");
    await page.getByRole("button", { name: "Start Watch" }).click();
    await expect(page.getByText("1 active watch")).toBeVisible({ timeout: 5_000 });

    // Cancel all
    await page.getByRole("button", { name: "Cancel All" }).click();

    // Active watches section should disappear
    await expect(page.getByText("active watch")).not.toBeVisible();
  });

  test("clear button is visible in event stream zone", async ({ page }) => {
    await navigateToWatch(page);

    await expect(page.getByRole("button", { name: "Clear" })).toBeVisible();
  });
});
