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

/** Navigate to the admin Backups view. */
async function navigateToBackups(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Backups", { exact: true }).click();
  await expect(page.locator(".breadcrumbs").getByText("Backups")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Creates a backup via the modal. */
async function createBackup(page: import("@playwright/test").Page, destination: string): Promise<void> {
  await page.getByRole("button", { name: "Create Backup" }).click();
  await expect(page.locator(".modal").getByText("Create Backup").first()).toBeVisible();
  await page.locator(".modal input[type='text']").first().fill(destination);
  await page.locator(".modal").getByRole("button", { name: "Create" }).click();
}

test.describe("Backup Manager", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("shows empty state when no backups exist", async ({ page }) => {
    await navigateToBackups(page);

    await expect(page.getByText("No backups found.")).toBeVisible();
    await expect(page.getByText("Select a backup to view details.")).toBeVisible();
  });

  test("create a backup and see it in the table", async ({ page }) => {
    await navigateToBackups(page);

    await createBackup(page, "/var/backups/qrdb/b1");

    await expect(page.getByText(/Backup "backup-\d+" created/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("backup-001").first()).toBeVisible();
  });

  test("clicking a backup row shows the detail panel", async ({ page }) => {
    await navigateToBackups(page);

    await createBackup(page, "/var/backups/qrdb/b2");
    await expect(page.getByText(/Backup "backup-\d+" created/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();

    await expect(page.getByRole("button", { name: "Verify" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Restore" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Delete" })).toBeVisible();
  });

  test("verify shows a success alert", async ({ page }) => {
    await navigateToBackups(page);

    await createBackup(page, "/var/backups/qrdb/b3");
    await expect(page.getByText(/Backup "backup-\d+" created/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Verify" }).click();

    await expect(page.getByText(/verified OK/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Checksum verified.")).toBeVisible();
  });

  test("restore completes and shows a success toast", async ({ page }) => {
    await navigateToBackups(page);

    await createBackup(page, "/var/backups/qrdb/b4");
    await expect(page.getByText(/Backup "backup-\d+" created/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Restore" }).click();
    await expect(page.locator(".modal").getByText("Restore Backup")).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Restore" }).click();

    await expect(page.getByText(/Restore completed/)).toBeVisible({ timeout: 5_000 });
  });

  test("delete removes the backup from the table", async ({ page }) => {
    await navigateToBackups(page);

    await createBackup(page, "/var/backups/qrdb/b5");
    await expect(page.getByText(/Backup "backup-\d+" created/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(page.locator(".modal").getByRole("heading", { name: "Delete Backup" })).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText(/deleted/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("No backups found.")).toBeVisible();
  });

  test("error on create shows an error toast", async ({ page }) => {
    await navigateToBackups(page);

    await injectError(page, "BackupBinding.Create", "backup create failed: destination unreachable");

    await createBackup(page, "/bad/destination");

    await expect(page.getByText("backup create failed: destination unreachable")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "BackupBinding.Create");
  });

  test("clicking a backup row fetches its full detail via backups.get", async ({ page }) => {
    await navigateToBackups(page);

    await createBackup(page, "/var/backups/qrdb/b-get");
    await expect(page.getByText(/Backup "backup-\d+" created/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();

    const detail = page.getByTestId("backup-detail");
    await expect(detail.getByText("/var/backups/qrdb/b-get")).toBeVisible({ timeout: 5_000 });
    await expect(detail.getByText("Destination")).toBeVisible();
    await expect(detail.getByText("Compression")).toBeVisible();
    await expect(detail.getByText("Encryption")).toBeVisible();
    await expect(detail.getByText(/^deadbeef\d+$/)).toBeVisible();
    await expect(page.getByTestId("backup-detail-error")).toHaveCount(0);
  });

  test("error on backups.get shows the detail error state", async ({ page }) => {
    await navigateToBackups(page);

    await createBackup(page, "/var/backups/qrdb/b-get-err");
    await expect(page.getByText(/Backup "backup-\d+" created/)).toBeVisible({ timeout: 5_000 });

    await injectError(page, "BackupBinding.Get", "backup get failed: manifest unreadable");
    await page.locator("table tbody tr").first().click();

    await expect(page.getByTestId("backup-detail-error")).toContainText("backup get failed: manifest unreadable", {
      timeout: 5_000,
    });

    await clearError(page, "BackupBinding.Get");
  });

  test("error on list shows an error toast", async ({ page }) => {
    await navigateToBackups(page);

    await injectError(page, "BackupBinding.List", "backup list unavailable");
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("backup list unavailable")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "BackupBinding.List");
  });

  test("cancel create modal does not create a backup", async ({ page }) => {
    await navigateToBackups(page);

    await page.getByRole("button", { name: "Create Backup" }).click();
    await expect(page.locator(".modal").getByText("Create Backup").first()).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Cancel" }).click();

    await expect(page.getByText("No backups found.")).toBeVisible();
  });
});
