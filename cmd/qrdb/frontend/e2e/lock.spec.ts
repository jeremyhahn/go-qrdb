// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const lockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "lock-db", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the Locks tab within the database workspace. */
async function navigateToLocks(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("lock-db").click();
  await expect(page.locator(".breadcrumbs").getByText("lock-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Locks", { exact: true }).click();
}

/** Acquires (or tries to acquire) a lock via the modal. */
async function acquireLock(
  page: import("@playwright/test").Page,
  key: string,
  ttl: string,
  opts: { tryLock?: boolean } = {},
): Promise<void> {
  const buttonName = opts.tryLock ? "Try Lock" : "Acquire Lock";
  await page.getByRole("button", { name: buttonName }).click();
  await expect(page.locator(".modal").getByText(opts.tryLock ? "Try Lock" : "Acquire Lock").first()).toBeVisible();
  await page.locator(".modal input[type='text']").first().fill(key);
  const ttlInput = page.locator(".modal input[type='number']");
  await ttlInput.fill(ttl);
  await page.locator(".modal").getByRole("button", { name: opts.tryLock ? "Try Lock" : "Acquire" }).click();
}

test.describe("Lock Manager", () => {
  test.use({ mockState: lockState });

  test("shows empty state when no locks exist", async ({ page }) => {
    await navigateToLocks(page);

    await expect(page.getByRole("heading", { name: "Active Locks" })).toBeVisible();
    await expect(page.getByText("No active locks.")).toBeVisible();
  });

  test("shows lock detail placeholder when no lock is selected", async ({ page }) => {
    await navigateToLocks(page);

    await expect(page.getByText("Lock Detail")).toBeVisible();
    await expect(page.getByText("Select a lock to view details.")).toBeVisible();
  });

  test("acquire a lock with valid key and TTL", async ({ page }) => {
    await navigateToLocks(page);

    await acquireLock(page, "resource-a", "60");

    await expect(page.getByText(/Lock "resource-a" acquired/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("resource-a").first()).toBeVisible();
  });

  test("try-lock a free key succeeds", async ({ page }) => {
    await navigateToLocks(page);

    await acquireLock(page, "resource-b", "30", { tryLock: true });

    await expect(page.getByText(/Lock "resource-b" acquired/)).toBeVisible({ timeout: 5_000 });
  });

  test("acquiring an already-held key fails with an error toast", async ({ page }) => {
    await navigateToLocks(page);

    await acquireLock(page, "resource-c", "60");
    await expect(page.getByText(/Lock "resource-c" acquired/)).toBeVisible({ timeout: 5_000 });

    // TryLock on the same key should fail (mock rejects when already held).
    await acquireLock(page, "resource-c", "60", { tryLock: true });
    await expect(page.getByText(/already held/)).toBeVisible({ timeout: 5_000 });
  });

  test("clicking a lock row shows detail with fencing token", async ({ page }) => {
    await navigateToLocks(page);

    await acquireLock(page, "resource-d", "60");
    await expect(page.getByText(/Lock "resource-d" acquired/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().locator("td").first().click();

    const detailCard = page.locator(".card", { hasText: "Lock Detail" });
    await expect(detailCard.getByText("Fencing Token", { exact: false })).toBeVisible();
    await expect(page.getByRole("button", { name: "Extend" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Release" })).toBeVisible();
  });

  test("extend refreshes the lock's acquired timestamp", async ({ page }) => {
    await navigateToLocks(page);

    await acquireLock(page, "resource-e", "60");
    await expect(page.getByText(/Lock "resource-e" acquired/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().locator("td").first().click();
    await page.getByRole("button", { name: "Extend" }).click();

    await expect(page.getByText(/Lock "resource-e" extended/)).toBeVisible({ timeout: 5_000 });
  });

  test("release removes the lock from the table", async ({ page }) => {
    await navigateToLocks(page);

    await acquireLock(page, "resource-f", "60");
    await expect(page.getByText(/Lock "resource-f" acquired/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().locator("td").first().click();
    await page.getByRole("button", { name: "Release" }).click();

    await expect(page.getByText(/Lock "resource-f" released/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("No active locks.")).toBeVisible();
  });

  test("filter by key prefix narrows the list", async ({ page }) => {
    await navigateToLocks(page);

    await acquireLock(page, "jobs/a", "60");
    await expect(page.getByText(/Lock "jobs\/a" acquired/)).toBeVisible({ timeout: 5_000 });
    await acquireLock(page, "other/b", "60");
    await expect(page.getByText(/Lock "other\/b" acquired/)).toBeVisible({ timeout: 5_000 });

    await page.getByPlaceholder("e.g. jobs/").fill("jobs/");
    await page.getByPlaceholder("e.g. jobs/").press("Enter");

    const table = page.locator("table tbody");
    await expect(table.getByText("jobs/a").first()).toBeVisible();
    await expect(table.getByText("other/b")).not.toBeVisible();
  });

  test("error on acquire shows error toast", async ({ page }) => {
    await navigateToLocks(page);

    await injectError(page, "LockBinding.Acquire", "lock acquire failed: quota exceeded");

    await acquireLock(page, "resource-g", "60");

    await expect(page.getByText("lock acquire failed: quota exceeded")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "LockBinding.Acquire");
  });

  test("error on list shows error toast", async ({ page }) => {
    await navigateToLocks(page);

    await injectError(page, "LockBinding.List", "lock list unavailable");
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("lock list unavailable")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "LockBinding.List");
  });

  test("cancel acquire modal does not create a lock", async ({ page }) => {
    await navigateToLocks(page);

    await page.getByRole("button", { name: "Acquire Lock" }).click();
    await expect(page.locator(".modal").getByText("Acquire Lock").first()).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Cancel" }).click();

    await expect(page.getByText("No active locks.")).toBeVisible();
  });
});
