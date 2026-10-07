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

const LOAD_TIMEOUT = 10_000;

const watchState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "watch-adv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to Watch tab. */
async function navigateToWatch(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("watch-adv").click();
  await expect(page.locator(".breadcrumbs").getByText("watch-adv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Watch", { exact: true }).click();
}

/** Start an exact-key watch. */
async function startWatch(
  page: import("@playwright/test").Page,
  key: string,
): Promise<void> {
  await page.locator("input[placeholder='Exact key...']").fill(key);
  await page.getByRole("button", { name: "Start Watch" }).click();
  await expect(page.getByText("Watch started successfully").first()).toBeVisible({ timeout: 5_000 });
}

/** Start a prefix watch. */
async function startPrefixWatch(
  page: import("@playwright/test").Page,
  prefix: string,
): Promise<void> {
  // Enable prefix toggle
  await page.locator(".toggle-primary").click();
  await expect(page.locator("input[placeholder='Key prefix...']")).toBeVisible();
  await page.locator("input[placeholder='Key prefix...']").fill(prefix);
  await page.getByRole("button", { name: "Start Watch" }).click();
  await expect(page.getByText("Watch started successfully").first()).toBeVisible({ timeout: 5_000 });
}

// ---------------------------------------------------------------------------
// Watch View - Advanced tests
// ---------------------------------------------------------------------------

test.describe("Watch View Advanced", () => {
  test.use({ mockState: watchState });

  test("unwatch (stop) individual watch by ID removes chip", async ({ page }) => {
    await navigateToWatch(page);
    await startWatch(page, "remove-me");

    await expect(page.getByText("1 active watch")).toBeVisible();

    // Find the cancel button for the chip using the aria-label from WatchView
    await page.locator("[aria-label='Cancel watch remove-me']").click();

    // Active watches section should disappear (0 watches)
    await expect(page.getByText("active watch")).not.toBeVisible({ timeout: 5_000 });
  });

  test("start prefix watch with toggle creates active watch", async ({ page }) => {
    await navigateToWatch(page);
    await startPrefixWatch(page, "events/");

    await expect(page.getByText("1 active watch")).toBeVisible();
    await expect(page.getByText("events/")).toBeVisible();
  });

  test("error on watch creation shows error toast", async ({ page }) => {
    await navigateToWatch(page);

    await injectError(page, "WatchBinding.Watch", "watch limit reached");

    await page.locator("input[placeholder='Exact key...']").fill("fail-key");
    await page.getByRole("button", { name: "Start Watch" }).click();

    await expect(page.getByText("watch limit reached")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "WatchBinding.Watch");
  });

  test("error on prefix watch shows error toast", async ({ page }) => {
    await navigateToWatch(page);

    await injectError(page, "WatchBinding.WatchPrefix", "prefix watch unavailable");

    await page.locator(".toggle-primary").click();
    await expect(page.locator("input[placeholder='Key prefix...']")).toBeVisible();
    await page.locator("input[placeholder='Key prefix...']").fill("fail/prefix/");
    await page.getByRole("button", { name: "Start Watch" }).click();

    await expect(page.getByText("prefix watch unavailable")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "WatchBinding.WatchPrefix");
  });

  test("cancel watch removes chip even when backend call is fire-and-forget", async ({ page }) => {
    await navigateToWatch(page);
    await startWatch(page, "unwatch-fail-key");

    await expect(page.getByText("1 active watch")).toBeVisible();

    // The component calls handle.cancel() without awaiting, so backend errors
    // do not block UI updates. The chip is removed immediately.
    await page.locator("[aria-label='Cancel watch unwatch-fail-key']").click();

    await expect(page.getByText("active watch")).not.toBeVisible({ timeout: 5_000 });
  });

  test("start watch with empty key shows warning", async ({ page }) => {
    await navigateToWatch(page);

    await page.getByRole("button", { name: "Start Watch" }).click();

    await expect(page.getByText("Key or prefix is required")).toBeVisible({ timeout: 5_000 });
  });

  test("multiple active watches each get a chip", async ({ page }) => {
    await navigateToWatch(page);

    await startWatch(page, "key-alpha");
    await startWatch(page, "key-beta");
    await startWatch(page, "key-gamma");

    await expect(page.getByText("3 active watches")).toBeVisible();
    await expect(page.getByText("key-alpha")).toBeVisible();
    await expect(page.getByText("key-beta")).toBeVisible();
    await expect(page.getByText("key-gamma")).toBeVisible();
  });

  test("stopping one watch decrements count", async ({ page }) => {
    await navigateToWatch(page);

    await startWatch(page, "decrement-a");
    await startWatch(page, "decrement-b");
    await expect(page.getByText("2 active watches")).toBeVisible();

    // Stop the first watch using the aria-label from WatchView
    await page.locator("[aria-label='Cancel watch decrement-a']").click();

    await expect(page.getByText("1 active watch")).toBeVisible({ timeout: 5_000 });
  });

  test("Clear button removes all events from event stream", async ({ page }) => {
    await navigateToWatch(page);

    // The Clear button should be visible even without events
    await expect(page.getByRole("button", { name: "Clear" })).toBeVisible();
    await page.getByRole("button", { name: "Clear" }).click();

    // After clearing, the waiting placeholder should be shown
    await expect(page.getByText("Waiting for events...")).toBeVisible();
  });
});
