// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * transactions-batch-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Multi-key transactions and a batch write against a live daemon:
 *   1. a transaction whose condition holds (tx/a has version 0, i.e. absent)
 *      commits two keys atomically;
 *   2. a second transaction with the same condition now fails (tx/a exists),
 *      so its success op (tx/c) is never applied: the transaction aborts;
 *   3. a batch writes two more keys.
 * The KV Store view, after a reload, lists the committed and batched keys and
 * not the aborted one.
 */

import { test, expect, type Locator, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Opens the default (kv-engine) database workspace on its KV Store tab. */
async function openWorkspace(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .locator("aside")
    .getByText("system", { exact: true })
    .first()
    .click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Switches the workspace to the named tool tab. */
async function openTab(
  page: Page,
  tab: string,
  heading: string,
): Promise<void> {
  await page.getByRole("tab", { name: tab, exact: true }).click();
  await expect(
    page.getByRole("heading", { name: heading, exact: true }),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** The transaction builder card whose step heading is `title`. */
function txnCard(page: Page, title: string): Locator {
  return page
    .locator(".card")
    .filter({ has: page.getByRole("heading", { name: title, exact: true }) });
}

/**
 * Builds and executes a transaction: "key tx/a has version 0" guarding PUTs
 * of `puts` on success. Returns once the result badge is shown.
 */
async function runGuardedTxn(
  page: Page,
  puts: [string, string][],
): Promise<void> {
  await page.getByRole("button", { name: "Reset", exact: true }).click();

  const cond = txnCard(page, "Conditions");
  await cond.getByRole("button", { name: "Add Condition" }).click();
  await cond.locator("input[placeholder='Key']").fill("tx/a");
  await cond.locator("input[placeholder='Expected value']").fill("0");

  const success = txnCard(page, "If Conditions Pass");
  for (const [i, [key, value]] of puts.entries()) {
    await success.getByRole("button", { name: "Add Op" }).click();
    await success.locator("input[placeholder='Key']").nth(i).fill(key);
    await success.locator("input[placeholder='Value']").nth(i).fill(value);
  }

  await page.getByRole("button", { name: "Execute Transaction" }).click();
}

test("transactions commit or abort atomically and a batch writes keys", async ({
  page,
}) => {
  await openWorkspace(page);

  // --- Transaction 1: condition holds, both keys commit. -----------------
  await openTab(page, "Transactions", "Transactions");
  await runGuardedTxn(page, [
    ["tx/a", "alpha"],
    ["tx/b", "bravo"],
  ]);
  await expect(page.getByText("Transaction succeeded.").last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.locator(".badge", { hasText: "Succeeded" })).toBeVisible();

  // --- Transaction 2: tx/a now exists, the condition fails, tx/c is never
  // written. -------------------------------------------------------------
  await runGuardedTxn(page, [["tx/c", "charlie"]]);
  await expect(
    page
      .getByText("Transaction condition failed; failure ops executed.")
      .last(),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.locator(".badge", { hasText: "Failed" })).toBeVisible();

  // --- Batch write of two keys. ------------------------------------------
  await openTab(page, "Batch", "Batch");
  for (const [i, [key, value]] of [
    ["batch/one", "uno"],
    ["batch/two", "dos"],
  ].entries()) {
    await page.getByRole("button", { name: "Add Operation" }).click();
    await page.locator("input[placeholder='Key']").nth(i).fill(key);
    await page.locator("input[placeholder='Value']").nth(i).fill(value);
  }
  await page.getByRole("button", { name: "Execute Batch" }).click();
  await expect(
    page.getByText("Batch executed successfully.").last(),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- The KV Store, read back after a reload, shows only committed keys.
  await page.reload();
  await openWorkspace(page);
  for (const value of ["alpha", "bravo", "uno", "dos"]) {
    await expect(page.getByText(value, { exact: true }).first()).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  }
  for (const key of ["tx/a", "tx/b", "batch/one", "batch/two"]) {
    await expect(page.getByText(key, { exact: true }).first()).toBeVisible();
  }
  await expect(page.getByText("tx/c", { exact: true })).toHaveCount(0);
  await expect(page.getByText("charlie", { exact: true })).toHaveCount(0);
});
