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

// kv-explorer-restore.spec.ts covers KVExplorer.svelte's two selective
// point-in-time restore surfaces against the MOCKED Wails backend
// (fixtures/wails-mock.ts's MVCCBinding, extended for this feature with a
// shared KV/MVCC revision history -- see that file's comments):
//
//   - The "As of revision" browse: a read-only historical view that
//     disables Create/Edit/Delete while active.
//   - The Restore workflow modal: ALWAYS previews (a dry run) before an
//     explicit "Confirm & Apply", renders the plan, reports Applied/
//     Skipped/Conflicted, and offers "Retry with Force" on conflicts.
//
// Every entry created via the mock's KVBinding.Put in a fresh test bumps
// the SAME shared revision counter the mock's MVCCBinding reads, starting
// at 0, so the Nth kv.put/kv.delete in a test is deterministically
// revision N -- this file relies on that determinism to type exact target
// revisions into the Restore form without any extra plumbing.
import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const kvState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "kv-restore", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the KV database workspace. */
async function navigateToKV(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("kv-restore").click();
  await expect(page.locator(".breadcrumbs").getByText("kv-restore", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Create (or overwrite) a KV entry via the slide panel. */
async function putEntry(
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

/** The Restore workflow modal, scoped so button/table queries never match the main page. */
function restoreModal(page: import("@playwright/test").Page) {
  return page.locator(".modal");
}

// ---------------------------------------------------------------------------
// As-of (historical) browse
// ---------------------------------------------------------------------------

test.describe("KV Explorer — as-of revision browse", () => {
  test.use({ mockState: kvState });

  test("viewing an earlier revision shows the historical banner and the value as of that revision", async ({ page }) => {
    await navigateToKV(page);

    await putEntry(page, "asof-key", "v1"); // revision 1
    await putEntry(page, "asof-key", "v2"); // revision 2

    // Live view shows the current value.
    await expect(page.getByText("v2").first()).toBeVisible();

    await page.locator("#kv-as-of-revision").fill("1");
    await page.getByRole("button", { name: "View", exact: true }).click();

    await expect(page.getByRole("alert").getByText("Viewing historical state as of revision", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.locator("table").getByText("v1").first()).toBeVisible();
    await expect(page.locator("table").getByText("v2")).not.toBeVisible();
  });

  test("historical browse disables Create and Delete", async ({ page }) => {
    await navigateToKV(page);

    await putEntry(page, "disabled-key", "v1"); // revision 1

    await page.locator("#kv-as-of-revision").fill("1");
    await page.getByRole("button", { name: "View", exact: true }).click();
    await expect(page.getByRole("alert").getByText("Viewing historical state as of revision", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // showCreate={!isHistorical} / showDelete={!isHistorical} — CrudTable's
    // own toolbar Create/Delete-selected buttons must not render at all.
    await expect(page.getByRole("button", { name: "Create", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Delete", exact: true })).toHaveCount(0);

    // The detail panel offers "Restore This Key" instead of Edit/Delete.
    await page.getByText("disabled-key").first().click();
    const detailPanel = page.getByLabel("Historical Details");
    await expect(detailPanel.getByRole("button", { name: "Restore This Key" })).toBeVisible();
    await expect(detailPanel.getByRole("button", { name: "Edit" })).toHaveCount(0);
    await expect(detailPanel.getByRole("button", { name: "Delete" })).toHaveCount(0);
  });

  test("Back to Live clears the historical view and restores mutation controls", async ({ page }) => {
    await navigateToKV(page);

    await putEntry(page, "live-key", "v1");
    await putEntry(page, "live-key", "v2");

    await page.locator("#kv-as-of-revision").fill("1");
    await page.getByRole("button", { name: "View", exact: true }).click();
    await expect(page.getByRole("alert").getByText("Viewing historical state as of revision", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await page.getByRole("button", { name: "Back to Live" }).click();
    await expect(page.getByRole("alert").getByText("Viewing historical state as of revision", { exact: false })).not.toBeVisible();
    await expect(page.locator("table").getByText("v2").first()).toBeVisible();
    await expect(page.getByRole("button", { name: "Create", exact: true }).first()).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Restore workflow: preview-before-apply, plan rendering, Applied/Skipped/
// Conflicted reporting
// ---------------------------------------------------------------------------

test.describe("KV Explorer — restore workflow", () => {
  test.use({ mockState: kvState });

  test("restore always previews before applying, and the preview writes nothing", async ({ page }) => {
    await navigateToKV(page);

    await putEntry(page, "restore-revert", "orig"); // revision 1
    await putEntry(page, "restore-revert", "changed"); // revision 2

    await page.getByRole("button", { name: "Restore...", exact: true }).click();
    const modal = restoreModal(page);
    await expect(modal.getByText("Restore to Revision")).toBeVisible();

    await modal.locator('input[type="number"]').first().fill("1");
    await modal.getByRole("button", { name: "By Keys" }).click();
    await modal.locator("textarea").fill("restore-revert");

    // Preview (a dry run): the modal must show the plan, and nothing must
    // be written yet.
    await modal.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(modal.getByText("Confirm Restore Plan")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(modal.getByText("This is a preview. Nothing has been written yet", { exact: false })).toBeVisible();

    const planRow = modal.locator("table tbody tr").filter({ hasText: "restore-revert" });
    await expect(planRow).toBeVisible();
    await expect(planRow.locator(".badge")).toHaveText("revert");
    await expect(planRow.locator("td").nth(2)).toHaveText("2"); // From Rev
    await expect(planRow.locator("td").nth(3)).toHaveText("1"); // To Rev

    // Cancel out without confirming: the live value must be unaffected.
    await modal.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(modal).not.toBeVisible();
    await expect(page.locator("table").getByText("changed").first()).toBeVisible();
  });

  test("confirming the plan applies it and reports Applied with no conflicts", async ({ page }) => {
    await navigateToKV(page);

    await putEntry(page, "restore-apply", "orig"); // revision 1
    await putEntry(page, "restore-apply", "changed"); // revision 2

    await page.getByRole("button", { name: "Restore...", exact: true }).click();
    const modal = restoreModal(page);
    await modal.locator('input[type="number"]').first().fill("1");
    await modal.getByRole("button", { name: "By Keys" }).click();
    await modal.locator("textarea").fill("restore-apply");
    await modal.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(modal.getByText("Confirm Restore Plan")).toBeVisible({ timeout: LOAD_TIMEOUT });

    await modal.getByRole("button", { name: "Confirm & Apply", exact: true }).click();
    await expect(modal.getByText("Restore Result")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(modal.getByText("Restore applied with no conflicts.")).toBeVisible();

    const stats = modal.locator(".stats");
    await expect(stats.getByText("Applied")).toBeVisible();
    await expect(stats.locator(".stat-value").nth(0)).toHaveText("1");
    await expect(stats.locator(".stat-value").nth(1)).toHaveText("0"); // Skipped
    await expect(stats.locator(".stat-value").nth(2)).toHaveText("0"); // Conflicted

    await expect(page.getByText("Restore applied: 1 key(s) updated.")).toBeVisible({ timeout: 5_000 });

    await modal.getByRole("button", { name: "Close", exact: true }).click();
    await expect(modal).not.toBeVisible();
    await expect(page.locator("table").getByText("orig").first()).toBeVisible();
  });

  test("bulk-delete recovery: a prefix restore recreates a deleted key without touching its untouched sibling", async ({ page }) => {
    await navigateToKV(page);

    await putEntry(page, "bulk/alpha", "alpha-orig"); // revision 1
    await putEntry(page, "bulk/bravo", "bravo-orig"); // revision 2
    // Delete alpha via the UI (bulk-delete scenario).
    await page.getByText("bulk/alpha").first().click();
    let detailPanel = page.getByLabel("Details");
    await detailPanel.getByRole("button", { name: "Delete" }).click();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText('Deleted "bulk/alpha"')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("1 entries")).toBeVisible();

    // Restore the WHOLE "bulk/" prefix to revision 2 (after alpha existed,
    // before it was deleted): alpha must come back, bravo must be
    // untouched.
    await page.getByRole("button", { name: "Restore...", exact: true }).click();
    const modal = restoreModal(page);
    await modal.locator('input[type="number"]').first().fill("2");
    // "By Prefix" is already the default scope; fill the prefix explicitly.
    await modal.locator('input[placeholder="e.g. users/"]').fill("bulk/");
    await modal.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(modal.getByText("Confirm Restore Plan")).toBeVisible({ timeout: LOAD_TIMEOUT });

    const recreateRow = modal.locator("table tbody tr").filter({ hasText: "bulk/alpha" });
    await expect(recreateRow).toBeVisible();
    await expect(recreateRow.locator(".badge")).toHaveText("recreate");

    await modal.getByRole("button", { name: "Confirm & Apply", exact: true }).click();
    await expect(modal.getByText("Restore Result")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await modal.getByRole("button", { name: "Close", exact: true }).click();

    await expect(page.getByText("2 entries")).toBeVisible();
    await expect(page.locator("table").getByText("bulk/alpha").first()).toBeVisible();
    await expect(page.locator("table").getByText("alpha-orig").first()).toBeVisible();
    await expect(page.locator("table").getByText("bravo-orig").first()).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Conflicts and "Retry with Force"
// ---------------------------------------------------------------------------

test.describe("KV Explorer — restore conflicts", () => {
  test.use({ mockState: { ...kvState, restoreSimulateConflict: true } });

  test("a conflicted apply reports Conflicted and offers Retry with Force, which then converges", async ({ page }) => {
    await navigateToKV(page);

    await putEntry(page, "restore-conflict", "orig"); // revision 1
    await putEntry(page, "restore-conflict", "changed"); // revision 2

    await page.getByRole("button", { name: "Restore...", exact: true }).click();
    const modal = restoreModal(page);
    await modal.locator('input[type="number"]').first().fill("1");
    await modal.getByRole("button", { name: "By Keys" }).click();
    await modal.locator("textarea").fill("restore-conflict");
    await modal.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(modal.getByText("Confirm Restore Plan")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Non-force apply: the mock is configured to report this single change
    // as a conflict.
    await modal.getByRole("button", { name: "Confirm & Apply", exact: true }).click();
    await expect(modal.getByText("Restore Result")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      modal.getByText("key(s) changed since the plan was computed and were left", { exact: false }),
    ).toBeVisible();

    const stats = modal.locator(".stats");
    await expect(stats.locator(".stat-value").nth(0)).toHaveText("0"); // Applied
    await expect(stats.locator(".stat-value").nth(2)).toHaveText("1"); // Conflicted

    // The conflicts table lists the untouched key.
    const conflictRow = modal.locator("table tbody tr").filter({ hasText: "restore-conflict" });
    await expect(conflictRow).toBeVisible();

    // The live value must be UNCHANGED -- a conflict means the write did
    // not happen.
    await expect(page.locator("table").getByText("changed").first()).toBeVisible();

    // "Retry with Force" is offered, and applying it converges the key to
    // the restored value.
    const retryButton = modal.getByRole("button", { name: "Retry with Force", exact: true });
    await expect(retryButton).toBeVisible();
    await retryButton.click();

    await expect(modal.getByText("Restore applied with no conflicts.")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(stats.locator(".stat-value").nth(0)).toHaveText("1"); // Applied
    await expect(stats.locator(".stat-value").nth(2)).toHaveText("0"); // Conflicted

    await modal.getByRole("button", { name: "Close", exact: true }).click();
    await expect(modal).not.toBeVisible();
    await expect(page.locator("table").getByText("orig").first()).toBeVisible();
  });
});
