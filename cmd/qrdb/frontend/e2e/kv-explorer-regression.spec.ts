// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const kvState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "regression-kv", engine: "kv", backend: "pebble", status: "open" },
    { name: "regression-kv-2", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the primary KV regression database workspace. */
async function navigateToKV(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("regression-kv", { exact: true }).click();
  await expect(page.locator(".breadcrumbs").getByText("regression-kv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Create a KV entry via the slide panel and wait for the success toast. */
async function createEntry(
  page: import("@playwright/test").Page,
  key: string,
  value: string,
): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill(value);
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText(`Created "${key}"`).last()).toBeVisible({ timeout: 5_000 });
}

// ---------------------------------------------------------------------------
// Regression: Bug 1 — KV Explorer initial load shows skeleton forever
// ---------------------------------------------------------------------------

test.describe("KV Explorer regression — Bug 1: initial load", () => {
  test.use({ mockState: kvState });

  test("entries load on first render without clicking Refresh", async ({ page }) => {
    // Seed the KV store before navigation so there are entries to display.
    await page.goto("/");
    const sidebar = page.locator("aside");
    await sidebar.getByText("regression-kv", { exact: true }).click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Create an entry so the store is non-empty.
    await createEntry(page, "initial-load-key", "initial-load-value");
    await expect(page.getByText("1 entries")).toBeVisible();

    // Navigate away to a second database, then back. The component must
    // reload data automatically without requiring a manual Refresh click.
    await sidebar.getByText("regression-kv-2").click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv-2", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await sidebar.getByText("regression-kv", { exact: true }).click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Data must appear WITHOUT clicking Refresh. If Bug 1 is present, this
    // assertion fails because the table shows 0 entries (skeleton stuck).
    await expect(page.getByText("1 entries")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("initial-load-key").first()).toBeVisible();
  });

  test("entries are visible immediately on first navigation to KV database", async ({ page }) => {
    // Seed two entries before the initial navigation.
    await page.goto("/");
    const sidebar = page.locator("aside");
    await sidebar.getByText("regression-kv", { exact: true }).click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "auto-load-a", "value-a");
    await createEntry(page, "auto-load-b", "value-b");

    // Navigate away and return — entries must load without a Refresh click.
    await sidebar.getByText("regression-kv-2").click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv-2", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await sidebar.getByText("regression-kv", { exact: true }).click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await expect(page.getByText("2 entries")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("auto-load-a").first()).toBeVisible();
    await expect(page.getByText("auto-load-b").first()).toBeVisible();
  });

  test("loading state resolves even when list returns zero entries", async ({ page }) => {
    // Navigate to a fresh KV database that has no entries.
    await page.goto("/");
    const sidebar = page.locator("aside");
    await sidebar.getByText("regression-kv", { exact: true }).click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // The entry count must resolve to 0 — not stay at a skeleton state
    // with loading spinner active and the Refresh button disabled.
    await expect(page.getByText("0 entries")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByRole("button", { name: "Refresh" })).not.toBeDisabled();
  });

  test("error during initial list resolves loading state", async ({ page }) => {
    await page.goto("/");
    await injectError(page, "KVBinding.Scan", "simulated storage error");

    const sidebar = page.locator("aside");
    await sidebar.getByText("regression-kv", { exact: true }).click();
    await expect(page.locator(".breadcrumbs").getByText("regression-kv", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // An error toast must appear and loading must clear (Refresh enabled).
    await expect(page.getByText("simulated storage error")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByRole("button", { name: "Refresh" })).not.toBeDisabled();
    await expect(page.getByText("0 entries")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Regression: Bug 2 — KV keys displayed as base64 instead of plain strings
// ---------------------------------------------------------------------------

test.describe("KV Explorer regression — Bug 2: key display encoding", () => {
  test.use({ mockState: kvState });

  test("plain-text key is displayed as-is, not base64-encoded", async ({ page }) => {
    await navigateToKV(page);

    // The key "batch1" should be stored and displayed as "batch1", NOT as
    // its base64 encoding "YmF0Y2gx".
    await createEntry(page, "batch1", "some-value");

    await expect(page.getByText("batch1").first()).toBeVisible();
    // Verify the base64 encoding of "batch1" does NOT appear in the table.
    await expect(page.getByText("YmF0Y2gx")).not.toBeVisible();
  });

  test("key with special characters is displayed without re-encoding", async ({ page }) => {
    await navigateToKV(page);

    const key = "app/config:timeout";
    await createEntry(page, key, "30s");

    // The key must appear verbatim in the table, not URL-encoded or base64.
    await expect(page.getByText(key).first()).toBeVisible();
  });

  test("keys are plain strings after Refresh", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "scantest", "scan-value");
    await createEntry(page, "batch1", "batch-value");

    // Click Refresh and confirm plain-text keys remain visible.
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("scantest").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("batch1").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("YmF0Y2gx")).not.toBeVisible();
  });

  test("get returns plain value when key is plain string", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "plain-key", "plain-value");

    // Click the row to open the detail panel — the value must be the
    // original plain string, not a base64-encoded version.
    await page.getByText("plain-key").first().click();

    const detailPanel = page.getByLabel("Details");
    await expect(detailPanel.getByText("plain-value")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Regression: combined CRUD flow
// ---------------------------------------------------------------------------

test.describe("KV Explorer regression — CRUD flow", () => {
  test.use({ mockState: kvState });

  test("create entry and verify it appears without Refresh", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "crud-key", "crud-value");

    // The entry must appear in the table immediately after creation,
    // and the entry count must update without a manual Refresh.
    await expect(page.getByText("crud-key").first()).toBeVisible();
    await expect(page.getByText("1 entries")).toBeVisible();
  });

  test("Refresh button reloads the entry list", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "refresh-key", "refresh-value");
    await expect(page.getByText("1 entries")).toBeVisible();

    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("refresh-key").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText("1 entries")).toBeVisible();
  });

  test("filter by prefix narrows the displayed entries", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "filter/a", "val-a");
    await createEntry(page, "filter/b", "val-b");
    await createEntry(page, "other/c", "val-c");

    await expect(page.getByText("3 entries")).toBeVisible();

    await page.locator("input[placeholder='Filter by key prefix...']").fill("filter/");
    await page.locator("[aria-label='Search']").click();

    await expect(page.getByText("2 entries")).toBeVisible({ timeout: LOAD_TIMEOUT });
    // Scope to the table: the "Created \"other/c\"" success toast also contains
    // the key text, so an unscoped getByText matches the toast and the
    // not-visible assertion fails (or the visible ones pass vacuously).
    const table = page.locator("table");
    await expect(table.getByText("filter/a").first()).toBeVisible();
    await expect(table.getByText("filter/b").first()).toBeVisible();
    await expect(table.getByText("other/c")).not.toBeVisible();
  });

  test("delete removes entry from table", async ({ page }) => {
    await navigateToKV(page);

    await createEntry(page, "delete-me", "temporary");
    await expect(page.getByText("1 entries")).toBeVisible();

    // Wait for the creation success toast to clear so it does not intercept clicks.
    await expect(page.getByText('Created "delete-me"')).not.toBeVisible({ timeout: LOAD_TIMEOUT });

    // Open the detail panel and delete via the panel footer.
    await page.getByText("delete-me").first().click();
    const detailPanel = page.getByLabel("Details");
    await detailPanel.getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText("Delete Entry")).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText('Deleted "delete-me"')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("0 entries")).toBeVisible();
    await expect(page.getByText("delete-me", { exact: true })).not.toBeVisible();
  });
});
