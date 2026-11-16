/*
 * Copyright 2026 Jeremy Hahn
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// KV Explorer: every KV and MVCC operation the explorer reaches beyond the
// basic browse/create/edit/delete -- mvcc.getRevision, mvcc.getAtRevision,
// mvcc.currentRevision, mvcc.compact, mvcc.putWithLease, kv.exists, kv.get
// and kv.list -- one test per operation, plus error paths.

import type { Page } from "@playwright/test";
import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const kvState = {
  ...secondLaunchEmbeddedState,
  databases: [{ name: "kv-mvcc", backend: "pebble", status: "open" }],
};

async function navigateToKV(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("kv-mvcc").click();
  await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Creates an entry through the Create form; a lease id saves through putWithLease. */
async function createEntry(page: Page, key: string, value: string, leaseId?: string): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill(value);
  if (leaseId !== undefined) {
    await page.locator("input[placeholder='Attach the key to a lease; blank for none']").fill(leaseId);
  }
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  const toast = leaseId ? `Created "${key}" with lease ${leaseId}` : `Created "${key}"`;
  await expect(page.getByText(toast).last()).toBeVisible({ timeout: 5_000 });
}

async function openRow(page: Page, key: string): Promise<ReturnType<Page["getByLabel"]>> {
  await page.locator("table").getByText(key, { exact: true }).first().click();
  const panel = page.getByLabel("Details");
  await expect(panel.getByText(key, { exact: true }).first()).toBeVisible();
  return panel;
}

test.describe("KV Explorer MVCC operations", () => {
  test.use({ mockState: kvState });

  test("currentRevision is shown and advances after each write", async ({ page }) => {
    await navigateToKV(page);
    const line = page.getByTestId("kv-current-revision");
    await expect(line).toHaveText(/Current revision:\s*0/);
    await createEntry(page, "rev/a", "one");
    await expect(line).toHaveText(/Current revision:\s*1/);
    await createEntry(page, "rev/b", "two");
    await expect(line).toHaveText(/Current revision:\s*2/);
  });

  test("getRevision fills the selected entry's revision details", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "meta/key", "v1");
    const panel = await openRow(page, "meta/key");
    await expect(panel.getByTestId("kv-version")).toHaveText("1");
    await expect(panel.getByTestId("kv-lease")).toHaveText("none");
  });

  test("getAtRevision shows the value a key held at an earlier revision", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "hist/key", "first");
    await createEntry(page, "hist/key", "second");
    const panel = await openRow(page, "hist/key");
    await panel.getByLabel("Value at revision").fill("1");
    await panel.getByRole("button", { name: "Look up" }).click();
    await expect(panel.getByTestId("kv-at-revision-value")).toHaveText("first");
    await panel.getByLabel("Value at revision").fill("2");
    await panel.getByRole("button", { name: "Look up" }).click();
    await expect(panel.getByTestId("kv-at-revision-value")).toHaveText("second");
  });

  test("compact discards history below the revision after confirmation", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "cmp/key", "old");
    await createEntry(page, "cmp/key", "new");
    await page.getByLabel("Compact revision").fill("2");
    await page.getByRole("button", { name: "Compact to revision" }).click();
    const modal = page.locator(".modal");
    await expect(modal.getByText("Compact History")).toBeVisible();
    await modal.getByRole("button", { name: "Compact", exact: true }).click();
    await expect(page.getByText("Compacted history to revision 2")).toBeVisible({ timeout: 5_000 });
    expect(
      await page.evaluate(
        () => (window as unknown as { __mvccCompactedRevision?: number }).__mvccCompactedRevision,
      ),
    ).toBe(2);

    const panel = await openRow(page, "cmp/key");
    await panel.getByLabel("Value at revision").fill("1");
    await panel.getByRole("button", { name: "Look up" }).click();
    await expect(panel.getByTestId("kv-at-revision-error")).toContainText("has been compacted");
  });

  test("compact beyond the current revision is refused before the call", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "cmp/one", "x");
    await page.getByLabel("Compact revision").fill("9");
    await page.getByRole("button", { name: "Compact to revision" }).click();
    await expect(page.getByText("Revision 9 is beyond the current revision 1.")).toBeVisible();
    await expect(page.locator(".modal").getByText("Compact History")).not.toBeVisible();
  });

  test("putWithLease saves an entry with a lease id", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "lease/key", "leased", "42");
    await expect(page.locator("table").getByText("lease/key")).toBeVisible();
    await expect(page.getByTestId("kv-current-revision")).toHaveText(/Current revision:\s*1/);
  });

  test("go to key opens an existing key through exists and get", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "goto/key", "found-it");
    await page.getByLabel("Go to key").fill("goto/key");
    await page.getByRole("button", { name: "Go", exact: true }).click();
    const panel = page.getByLabel("Details");
    await expect(panel.getByText("found-it")).toBeVisible();
  });

  test("go to key reports a key that does not exist", async ({ page }) => {
    await navigateToKV(page);
    await page.getByLabel("Go to key").fill("missing/key");
    await page.getByRole("button", { name: "Go", exact: true }).click();
    await expect(page.getByText('Key "missing/key" does not exist.')).toBeVisible();
  });

  test("keys only lists names through kv.list and fetches a value on open", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "ko/a", "value-a");
    await createEntry(page, "ko/b", "value-b");
    await expect(page.locator("table").getByText("value-a")).toBeVisible();
    await page.getByLabel("Keys only").check();
    await expect(page.locator("table").getByText("ko/b")).toBeVisible();
    await expect(page.locator("table").getByText("value-a")).not.toBeVisible();
    await expect(page.getByText("2 entries")).toBeVisible();
    const panel = await openRow(page, "ko/a");
    await expect(panel.getByText("value-a")).toBeVisible();
  });

  test.describe("Error handling", () => {
    test("a getRevision failure shows in the entry panel", async ({ page }) => {
      await navigateToKV(page);
      await createEntry(page, "err/key", "v");
      await injectError(page, "MVCCBinding.GetRevision", "revision lookup failed");
      const panel = await openRow(page, "err/key");
      await expect(panel.getByTestId("kv-entry-revision-error")).toHaveText("revision lookup failed");
    });

    test("a compact failure shows an error toast", async ({ page }) => {
      await navigateToKV(page);
      await createEntry(page, "err/cmp", "v");
      await injectError(page, "MVCCBinding.Compact", "compaction refused");
      await page.getByLabel("Compact revision").fill("1");
      await page.getByRole("button", { name: "Compact to revision" }).click();
      await page.locator(".modal").getByRole("button", { name: "Compact", exact: true }).click();
      await expect(page.getByText("compaction refused")).toBeVisible({ timeout: 5_000 });
    });
  });
});
