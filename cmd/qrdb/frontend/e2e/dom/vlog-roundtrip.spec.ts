// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * vlog-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Verifiable Logs tab of the "system" database against a live
 * qrdbd (seal "none", auth off): create a log that allows expiring bodies,
 * append three entries, read them back, prove inclusion and consistency,
 * sign the head, rotate the head-signing key and expire bodies.
 *
 * Proofs, signed checkpoints and key versions come from the daemon, so this
 * tier proves the server work the mocked tier (e2e/vlog.spec.ts) stands in for.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens the Verifiable Logs tab of the "system" database. */
async function openVlog(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  // The fresh daemon ships a single kv-engine database named "system".
  await page.locator("aside").getByText("system", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("tab", { name: "Verifiable Logs", exact: true }).click();
  await expect(page.getByTestId("vlog-create-submit")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Runs one assertion; on failure, reports the toasts shown so a daemon error reads in the failure. */
async function expectStep(page: Page, step: string, check: () => Promise<void>): Promise<void> {
  try {
    await check();
  } catch (err) {
    const shown = await page.locator(".toast .alert").allInnerTexts();
    throw new Error(`${step} failed; toasts shown: ${JSON.stringify(shown)}\n${err}`, { cause: err });
  }
}

/** Returns the key version a "Key version N" element shows. */
async function keyVersion(page: Page, testId: string): Promise<number> {
  const text = await page.getByTestId(testId).innerText();
  const match = /Key version (\d+)/.exec(text);
  expect(match, `${testId} shows a key version: ${text}`).not.toBeNull();
  return Number(match![1]);
}

test("verifiable log: create, append, get, prove, sign, rotate and expire round-trip (UI <-> live daemon)", async ({ page }) => {
  const name = `e2e-vlog-${Date.now()}`;
  const origin = `example.com/${name}`;
  const lines = ["first entry", "second entry", "third entry"];

  await openVlog(page);

  // Create: a log with a head-signing key that allows expiring bodies.
  await page.getByLabel("Log name").fill(name);
  await page.getByLabel("Origin").fill(origin);
  await page.getByLabel("Allow expiring bodies").check();
  await page.getByTestId("vlog-create-submit").click();
  const entry = page.getByTestId("vlog-list").getByRole("button", { name, exact: true });
  await expectStep(page, "create", () => expect(entry).toBeVisible({ timeout: LOAD_TIMEOUT }));
  await expectStep(page, "created key", () =>
    expect(page.getByTestId("vlog-created-key")).toContainText("Key version", { timeout: LOAD_TIMEOUT }));
  const createdVersion = await keyVersion(page, "vlog-created-key");
  await entry.click();
  await expectStep(page, "head", () =>
    expect(page.getByTestId("vlog-head")).toContainText("Size", { timeout: LOAD_TIMEOUT }));

  // Append three entries, then read 0..2 back from the daemon.
  await page.getByLabel("Entries to append").fill(lines.join("\n"));
  await page.getByTestId("vlog-append-submit").click();
  await expectStep(page, "append", () =>
    expect(page.getByTestId("vlog-head").locator("tr").filter({ hasText: /^\s*Size\s/ }).locator("td"))
      .toHaveText("3", { timeout: LOAD_TIMEOUT }));
  await page.getByLabel("From", { exact: true }).fill("0");
  await page.getByLabel("Limit", { exact: true }).fill("3");
  await page.getByTestId("vlog-get-submit").click();
  const entries = page.getByTestId("vlog-entries");
  for (const line of lines) {
    await expectStep(page, `get ${line}`, () => expect(entries).toContainText(line, { timeout: LOAD_TIMEOUT }));
  }

  // Inclusion proof of index 1 in the tree of size 3.
  await page.getByLabel("Inclusion index").fill("1");
  await page.getByLabel("Inclusion size").fill("3");
  await page.getByTestId("vlog-inclusion-submit").click();
  const inclusion = page.getByTestId("vlog-inclusion-proof");
  await expectStep(page, "inclusion proof", () => expect(inclusion).not.toBeEmpty({ timeout: LOAD_TIMEOUT }));
  await expect(inclusion).toBeVisible();

  // Consistency proof between sizes 1 and 3.
  await page.getByLabel("Consistency size 1").fill("1");
  await page.getByLabel("Consistency size 2").fill("3");
  await page.getByTestId("vlog-consistency-submit").click();
  const consistency = page.getByTestId("vlog-consistency-proof");
  await expectStep(page, "consistency proof", () => expect(consistency).not.toBeEmpty({ timeout: LOAD_TIMEOUT }));
  await expect(consistency).toBeVisible();

  // Sign the head: the checkpoint names the origin and size 3.
  await page.getByTestId("vlog-sign-head").click();
  const signed = page.getByTestId("vlog-signed-head");
  await expectStep(page, "sign head", () =>
    expect(signed).toContainText(`Key version ${createdVersion}`, { timeout: LOAD_TIMEOUT }));
  await expect(signed.locator("pre")).toHaveText(new RegExp(`${origin.replace(/[.]/g, "\\.")}\\s+3\\s`));

  // Rotate the head-signing key: the new version is past the created one.
  await page.getByTestId("vlog-rotate-submit").click();
  await expectStep(page, "rotate key", () =>
    expect(page.getByTestId("vlog-rotated-key")).toContainText("Key version", { timeout: LOAD_TIMEOUT }));
  expect(await keyVersion(page, "vlog-rotated-key")).toBeGreaterThan(createdVersion);

  // Expire the bodies before index 2, confirming the dialog.
  await page.getByLabel("Cutoff index").fill("2");
  await page.getByTestId("vlog-expire-submit").click();
  const modal = page.locator(".modal").filter({ hasText: "Expire Bodies" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.getByRole("button", { name: "Expire", exact: true }).click();
  await expectStep(page, "expire", () =>
    expect(page.getByText("Expired 2 bodies")).toBeVisible({ timeout: LOAD_TIMEOUT }));
});
