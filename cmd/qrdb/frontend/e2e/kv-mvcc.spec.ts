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
  clearError,
  holdCall,
  heldCallCount,
  mvccCalls,
  releaseCall,
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

  test("go to key reports a lookup failure instead of a missing key", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "goto/sealed", "hidden");
    // The underlying Get fails (a refusal, a sealed store); Exists must
    // carry that failure, not answer "does not exist".
    await injectError(page, "KVBinding.Get", "permission denied: kv read");
    await page.getByLabel("Go to key").fill("goto/sealed");
    await page.getByRole("button", { name: "Go", exact: true }).click();
    await expect(page.getByText("permission denied: kv read")).toBeVisible();
    await expect(page.getByText('Key "goto/sealed" does not exist.')).toHaveCount(0);
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

  test("Edit waits for the entry's metadata and keeps its lease", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "lease/edit", "v1", "42");
    await holdCall(page, "MVCCBinding.GetRevision");
    const panel = await openRow(page, "lease/edit");
    const edit = panel.getByRole("button", { name: "Edit" });
    // The lease is unknown while getRevision is in flight: Edit must not
    // snapshot an empty lease and release the binding on save.
    await expect(edit).toBeDisabled();
    expect(await releaseCall(page, "MVCCBinding.GetRevision")).toBe(1);
    await expect(panel.getByTestId("kv-lease")).toHaveText("42");
    await expect(edit).toBeEnabled();
    await edit.click();
    const leaseInput = page.locator("input[placeholder='Attach the key to a lease; blank for none']");
    await expect(leaseInput).toHaveValue("42");
    await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill("v2");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByText('Updated "lease/edit" with lease 42').last()).toBeVisible({ timeout: 5_000 });
    const reopened = await openRow(page, "lease/edit");
    await expect(reopened.getByTestId("kv-lease")).toHaveText("42");
  });

  test("Edit after a failed metadata read says the lease is unknown", async ({ page }) => {
    await navigateToKV(page);
    await createEntry(page, "lease/unknown", "v1", "7");
    await injectError(page, "MVCCBinding.GetRevision", "revision lookup failed");
    const panel = await openRow(page, "lease/unknown");
    await expect(panel.getByTestId("kv-entry-revision-error")).toHaveText("revision lookup failed");
    await panel.getByRole("button", { name: "Edit" }).click();
    await expect(page.getByTestId("kv-lease-unknown")).toBeVisible();
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

test.describe("KV Explorer revision support is remembered per database", () => {
  test.use({
    mockState: {
      ...secondLaunchEmbeddedState,
      databases: [
        { name: "kv-plain", backend: "memory", status: "open" },
        { name: "kv-mvcc", backend: "pebble", status: "open" },
      ],
    },
  });

  test("a store without revisions does not disable revisions on another database", async ({ page }) => {
    await page.goto("/");
    await injectError(page, "MVCCBinding.CurrentRevision", "501 Not Implemented");
    await page.locator("aside").getByText("kv-plain").click();
    await expect(page.getByTestId("kv-revisions-unavailable")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByLabel("Compact revision")).toBeDisabled();

    await clearError(page, "MVCCBinding.CurrentRevision");
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByTestId("kv-current-revision")).toHaveText(/Current revision:\s*0/);
    await expect(page.getByTestId("kv-revisions-unavailable")).toHaveCount(0);
    await expect(page.getByLabel("Compact revision")).toBeEnabled();
  });
});

test.describe("KV Explorer MVCC calls are scoped to the selected database", () => {
  test.use({
    mockState: {
      ...secondLaunchEmbeddedState,
      databases: [
        { name: "kv-plain", backend: "memory", status: "open" },
        { name: "kv-mvcc", backend: "pebble", status: "open" },
      ],
      noRevisionDatabases: ["kv-plain"],
    },
  });

  test("compaction and the revision probe carry the selected database", async ({ page }) => {
    await page.goto("/");
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.getByTestId("kv-current-revision")).toHaveText(/Current revision:\s*0/, {
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "scoped/key", "v");
    await page.getByLabel("Compact revision").fill("1");
    await page.getByRole("button", { name: "Compact to revision" }).click();
    await page.locator(".modal").getByRole("button", { name: "Compact", exact: true }).click();
    await expect(page.getByText("Compacted history to revision 1")).toBeVisible({ timeout: 5_000 });

    const calls = await mvccCalls(page);
    expect(calls.filter((c) => c.method === "Compact")).toEqual([{ method: "Compact", database: "kv-mvcc" }]);
    expect(calls.some((c) => c.method === "CurrentRevision")).toBe(true);
    expect(calls.filter((c) => c.database !== "kv-mvcc")).toEqual([]);
  });

  test("a delayed probe from one database neither answers nor disables the next", async ({ page }) => {
    const method = "MVCCBinding.CurrentRevision";
    await page.goto("/");
    await holdCall(page, method);
    await page.locator("aside").getByText("kv-plain").click();
    await expect.poll(() => heldCallCount(page, method), { timeout: LOAD_TIMEOUT }).toBe(1);

    // kv-plain's probe is still in flight: kv-mvcc must ask for itself.
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect.poll(() => heldCallCount(page, method), { timeout: LOAD_TIMEOUT }).toBe(2);
    await releaseCall(page, method);

    await expect(page.getByTestId("kv-current-revision")).toHaveText(/Current revision:\s*0/);
    await expect(page.getByTestId("kv-revisions-unavailable")).toHaveCount(0);
    await expect(page.getByLabel("Compact revision")).toBeEnabled();

    // The late 501 is remembered for the database that answered it.
    await page.locator("aside").getByText("kv-plain").click();
    await expect(page.getByTestId("kv-revisions-unavailable")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("a delayed key read from one database is discarded after a switch", async ({ page }) => {
    const method = "KVBinding.Get";
    await page.goto("/");
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "shared/key", "from-plain-b");
    await page.locator("aside").getByText("kv-plain").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-plain", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "shared/key", "from-plain-a");

    // A keys-only open in kv-plain: its kv.get is still in flight when the
    // user switches to kv-mvcc.
    await page.getByLabel("Keys only").check();
    await expect(page.locator("table").getByText("shared/key")).toBeVisible();
    await holdCall(page, method);
    await page.locator("table").getByText("shared/key", { exact: true }).first().click();
    await expect.poll(() => heldCallCount(page, method), { timeout: LOAD_TIMEOUT }).toBe(1);
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await holdCall(page, "MVCCBinding.GetRevision");
    expect(await releaseCall(page, method)).toBe(1);
    // Let the released read and any render it causes settle.
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));

    // kv-plain's value never opens in kv-mvcc, and no metadata is fetched
    // through kv-mvcc's client for it.
    await expect(page.getByLabel("Details", { exact: true })).toHaveCount(0);
    await expect(page.getByText("from-plain-a")).toHaveCount(0);
    expect(await releaseCall(page, "MVCCBinding.GetRevision")).toBe(0);
    expect((await mvccCalls(page)).filter((c) => c.method === "GetRevision")).toEqual([]);
  });

  test("a delayed keys-only listing from one database is discarded after a switch", async ({ page }) => {
    const method = "KVBinding.List@kv-plain";
    await page.goto("/");
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "only/in-mvcc", "b");
    await page.locator("aside").getByText("kv-plain").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-plain", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "only/in-plain", "a");

    // kv-plain's keys-only listing is still in flight when the user
    // switches to kv-mvcc, and kv-mvcc's own listing completes first.
    await page.getByLabel("Keys only").check();
    await expect(page.locator("table").getByText("only/in-plain")).toBeVisible();
    await holdCall(page, method);
    await page.getByRole("button", { name: "Refresh" }).click();
    await expect.poll(() => heldCallCount(page, method), { timeout: LOAD_TIMEOUT }).toBe(1);
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.locator("table").getByText("only/in-mvcc")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // kv-plain's late answer arrives while kv-mvcc is shown.
    expect(await releaseCall(page, method)).toBe(1);
    // Let the released listing and any render it causes settle.
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));

    // kv-mvcc still lists only its own keys: kv-plain's listing never
    // replaces it, so a bulk delete cannot act on kv-plain's names here.
    await expect(page.locator("table").getByText("only/in-plain")).toHaveCount(0);
    await expect(page.locator("table").getByText("only/in-mvcc")).toBeVisible();
  });

  test("a bulk delete stays in its database after a switch", async ({ page }) => {
    const method = "KVBinding.Delete@kv-plain";
    await page.goto("/");
    // Both databases hold the same key names.
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "same/a", "mvcc-a");
    await createEntry(page, "same/b", "mvcc-b");
    await page.locator("aside").getByText("kv-plain").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-plain", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await createEntry(page, "same/a", "plain-a");
    await createEntry(page, "same/b", "plain-b");
    await expect(page.getByText("2 entries")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText('Created "same/b"')).not.toBeVisible({ timeout: LOAD_TIMEOUT });

    // kv-plain's first delete is still in flight when the user switches.
    await page.getByLabel("Select row 1", { exact: true }).check();
    await page.getByLabel("Select row 2", { exact: true }).check();
    await holdCall(page, method);
    await page.getByRole("button", { name: "Delete 2" }).click();
    await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
    await expect.poll(() => heldCallCount(page, method), { timeout: LOAD_TIMEOUT }).toBe(1);
    await page.locator("aside").getByText("kv-mvcc").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-mvcc", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.locator("table").getByText("mvcc-b")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The held delete finishes and the second one follows it.
    expect(await releaseCall(page, method)).toBe(1);
    await expect(page.getByText("Deleted 2 entries")).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Both deletes went to kv-plain: kv-mvcc keeps both keys.
    await page.getByRole("button", { name: "Refresh" }).click();
    await expect(page.locator("table").getByText("mvcc-a")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.locator("table").getByText("mvcc-b")).toBeVisible();
    await page.locator("aside").getByText("kv-plain").click();
    await expect(page.locator(".breadcrumbs").getByText("kv-plain", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.locator("table").getByText("plain-a")).toHaveCount(0, { timeout: LOAD_TIMEOUT });
    await expect(page.locator("table").getByText("plain-b")).toHaveCount(0);
  });
});
