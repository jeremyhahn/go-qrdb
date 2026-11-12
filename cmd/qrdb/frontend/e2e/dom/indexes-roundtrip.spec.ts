// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * indexes-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Secondary-index round trip against a live daemon: create an index through
 * the Indexes view, write JSON KV entries through the KV Store view, query the
 * index for an exact value, drop the index, and reload the page after each
 * state change to prove the list is read back from the server.
 */

import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const INDEX = "idx-email";

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

/** Creates a KV entry via the KV Store slide panel. */
async function createEntry(
  page: Page,
  key: string,
  value: string,
): Promise<void> {
  await page
    .getByRole("button", { name: "Create", exact: true })
    .first()
    .click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page
    .locator("textarea[placeholder='Enter value (supports JSON)']")
    .fill(value);
  await page
    .getByRole("button", { name: "Create", exact: true })
    .last()
    .click();
  await expect(page.getByText(`Created "${key}"`).last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("secondary index round-trips create -> query -> drop against the live backend", async ({
  page,
}) => {
  await openWorkspace(page);
  await openTab(page, "Indexes", "Indexes");
  await expect(page.getByText("No secondary indexes defined.")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Create the index through the real REST path. ----------------------
  await page.getByRole("button", { name: "Create Index", exact: true }).click();
  const modal = page
    .locator(".modal")
    .filter({ hasText: "Create Secondary Index" });
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.locator("input[placeholder='e.g. idx-email']").fill(INDEX);
  await modal.locator("input[placeholder='e.g. email']").fill("email");
  await modal.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByText(`Index "${INDEX}" created.`)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(
    page.locator("table").getByText(INDEX, { exact: true }),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // A reload reads the index list back from the server.
  await page.reload();
  await openWorkspace(page);
  await openTab(page, "Indexes", "Indexes");
  await expect(
    page.locator("table").getByText(INDEX, { exact: true }),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Write indexed entries through the KV Store view. ------------------
  await openTab(page, "Data", "KV Store");
  await createEntry(page, "users/ada", '{"email": "ada@example.com"}');
  await createEntry(page, "users/bob", '{"email": "bob@example.com"}');

  // --- Query the index for one exact value. ------------------------------
  await openTab(page, "Indexes", "Indexes");
  await page.locator("table").getByText(INDEX, { exact: true }).click();
  await expect(page.getByText(`Selected: ${INDEX}`)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .locator("input[placeholder='exact value']")
    .fill("ada@example.com");
  await page.getByRole("button", { name: "Query", exact: true }).click();
  await expect(page.getByText("1 key(s) matched")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(
    page.locator("li").filter({ hasText: "users/ada" }),
  ).toBeVisible();
  await expect(page.locator("li").filter({ hasText: "users/bob" })).toHaveCount(
    0,
  );

  // --- Drop the index and prove it is gone server-side. ------------------
  await page.getByRole("button", { name: "Drop Index", exact: true }).click();
  await page.getByRole("button", { name: "Drop", exact: true }).click();
  await expect(page.getByText(`Index "${INDEX}" dropped.`)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByText("No secondary indexes defined.")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  await page.reload();
  await openWorkspace(page);
  await openTab(page, "Indexes", "Indexes");
  await expect(page.getByText("No secondary indexes defined.")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(
    page.locator("table").getByText(INDEX, { exact: true }),
  ).toHaveCount(0);
});
