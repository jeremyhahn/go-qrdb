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

/** Navigate to the admin Credentials view. */
async function navigateToCredentials(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Credentials", { exact: true }).click();
  await expect(page.locator(".breadcrumbs").getByText("Credentials")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Stores a new credential via the modal (default method: store). */
async function storeCredential(page: import("@playwright/test").Page, name: string, value: string): Promise<void> {
  await page.getByRole("button", { name: "New Credential" }).click();
  await expect(page.locator(".modal").getByText("New Credential").first()).toBeVisible();
  await page.locator(".modal input[type='text']").nth(0).fill(name);
  await page.locator(".modal input[type='text']").nth(1).fill(value);
  await page.locator(".modal").getByRole("button", { name: "Store" }).click();
}

test.describe("Credential Manager", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("shows empty state when no credentials exist", async ({ page }) => {
    await navigateToCredentials(page);

    await expect(page.getByText("No credentials found.")).toBeVisible();
    await expect(page.getByText("Select a credential to view details.")).toBeVisible();
  });

  test("shows the active strategy badge", async ({ page }) => {
    await navigateToCredentials(page);

    await expect(page.getByText("Strategy: manual")).toBeVisible();
  });

  test("store a new credential and see it in the list", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "db-password", "hunter2");

    await expect(page.getByText(/Credential "db-password" stored/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("db-password").first()).toBeVisible();
  });

  test("clicking a credential row loads version history", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "api-key", "abc123");
    await expect(page.getByText(/Credential "api-key" stored/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();

    await expect(page.getByText("Version History")).toBeVisible();
    await expect(page.getByText("1").first()).toBeVisible();
  });

  test("reveal shows the credential value", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "secret-1", "sekrit-value");
    await expect(page.getByText(/Credential "secret-1" stored/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Reveal", exact: true }).click();

    await expect(page.getByText("sekrit-value")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("button", { name: "Hide", exact: true })).toBeVisible();
  });

  test("rotate updates the version and shows a success toast", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "rotatable", "v1-value");
    await expect(page.getByText(/Credential "rotatable" stored/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Rotate" }).click();
    await expect(page.locator(".modal").getByText("Rotate Credential")).toBeVisible();
    await page.locator(".modal input[type='text']").fill("v2-value");
    await page.locator(".modal").getByRole("button", { name: "Rotate" }).click();

    await expect(page.getByText(/rotated: v1 -> v2/)).toBeVisible({ timeout: 5_000 });
  });

  test("reveal a past version via credentials.retrieveVersion", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "versioned", "first-secret");
    await expect(page.getByText(/Credential "versioned" stored/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Rotate" }).click();
    await expect(page.locator(".modal").getByText("Rotate Credential")).toBeVisible();
    await page.locator(".modal input[type='text']").fill("second-secret");
    await page.locator(".modal input[type='number']").fill("3600");
    await page.locator(".modal").getByRole("button", { name: "Rotate" }).click();
    await expect(page.getByText(/rotated: v1 -> v2/)).toBeVisible({ timeout: 5_000 });

    const v1 = page.getByTestId("credential-version-value-1");
    await expect(v1).toHaveText("•••••");
    await page.getByRole("button", { name: "Reveal version 1" }).click();
    await expect(v1).toHaveText("first-secret", { timeout: 5_000 });
    await expect(page.getByTestId("credential-version-value-2")).toHaveText("•••••");

    await page.getByRole("button", { name: "Reveal version 2" }).click();
    await expect(page.getByTestId("credential-version-value-2")).toHaveText("second-secret", { timeout: 5_000 });

    await page.getByRole("button", { name: "Hide version 1" }).click();
    await expect(v1).toHaveText("•••••");
  });

  test("error on credentials.retrieveVersion shows an error toast and keeps the value masked", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "versioned-err", "hidden-secret");
    await expect(page.getByText(/Credential "versioned-err" stored/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await expect(page.getByTestId("credential-version-value-1")).toBeVisible({ timeout: 5_000 });

    await injectError(page, "CredentialBinding.RetrieveVersion", "retrieve version failed: sealed");
    await page.getByRole("button", { name: "Reveal version 1" }).click();

    await expect(page.getByText("retrieve version failed: sealed")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByTestId("credential-version-value-1")).toHaveText("•••••");

    await clearError(page, "CredentialBinding.RetrieveVersion");
  });

  test("gc expired reports the reaped count", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "gc-target", "value");
    await expect(page.getByText(/Credential "gc-target" stored/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "GC Expired" }).click();

    await expect(page.getByText(/Reaped \d+ expired version/)).toBeVisible({ timeout: 5_000 });
  });

  test("delete removes the credential from the list", async ({ page }) => {
    await navigateToCredentials(page);

    await storeCredential(page, "to-delete", "value");
    await expect(page.getByText(/Credential "to-delete" stored/)).toBeVisible({ timeout: 5_000 });

    await page.locator("table tbody tr").first().click();
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(page.locator(".modal").getByRole("heading", { name: "Delete Credential" })).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText(/deleted/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("No credentials found.")).toBeVisible();
  });

  test("submit method notifies via a distinct toast", async ({ page }) => {
    await navigateToCredentials(page);

    await page.getByRole("button", { name: "New Credential" }).click();
    await expect(page.locator(".modal").getByText("New Credential").first()).toBeVisible();
    await page.locator(".modal select").selectOption("submit");
    await page.locator(".modal input[type='text']").nth(0).fill("submitted-cred");
    await page.locator(".modal input[type='text']").nth(1).fill("value");
    await page.locator(".modal").getByRole("button", { name: "Submit" }).click();

    await expect(page.getByText(/Credential "submitted-cred" submitted/)).toBeVisible({ timeout: 5_000 });
  });

  test("error on list shows an error toast", async ({ page }) => {
    await navigateToCredentials(page);

    await injectError(page, "CredentialBinding.List", "credential list unavailable");
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("credential list unavailable")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "CredentialBinding.List");
  });

  test("error on store shows an error toast", async ({ page }) => {
    await navigateToCredentials(page);

    await injectError(page, "CredentialBinding.Store", "credential store failed: barrier sealed");

    await storeCredential(page, "will-fail", "value");

    await expect(page.getByText("credential store failed: barrier sealed")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "CredentialBinding.Store");
  });

  test("cancel create modal does not store a credential", async ({ page }) => {
    await navigateToCredentials(page);

    await page.getByRole("button", { name: "New Credential" }).click();
    await expect(page.locator(".modal").getByText("New Credential").first()).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Cancel" }).click();

    await expect(page.getByText("No credentials found.")).toBeVisible();
  });
});
