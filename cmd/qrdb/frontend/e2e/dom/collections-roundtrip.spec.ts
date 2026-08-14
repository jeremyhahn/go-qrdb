// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * collections-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Collection-document round trip against a live daemon. The mocked tier
 * exercises the Collections UI extensively, but until this spec no tier ever
 * proved a collection document survives create -> insert -> read -> delete
 * through the REAL wire path (REST proxy -> service layer -> engine): the
 * mocked tier stubs the bindings, and the real-backend API tier covers raw KV
 * and indexes but never collection documents.
 */

import { test, expect } from "@playwright/test";
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

/** Opens the default (kv-engine) database's Collections tab. */
async function openCollections(page: import("@playwright/test").Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("system", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Collections", { exact: true }).click();
  await expect(page.getByRole("heading", { name: "Collections" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("collection document round-trips create -> insert -> read -> delete against the live backend", async ({
  page,
}) => {
  await openCollections(page);

  // --- Create a collection through the real REST path. -------------------
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Create Collection" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("input[placeholder='Enter collection name']").fill("customers");
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText('Collection "customers" created')).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // Select it in the left panel.
  await page.getByRole("button", { name: /customers/ }).first().click();
  await expect(page.locator("h3").filter({ hasText: "customers" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Insert a document (JSON mode) and read it back. -------------------
  await page.getByRole("tab", { name: "Documents" }).click();
  const insertBtn = page.locator("main").getByRole("button", { name: "Insert", exact: true });
  await expect(insertBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
  await insertBtn.click();

  const modal = page.locator(".modal");
  await expect(modal).toBeVisible({ timeout: LOAD_TIMEOUT });
  await modal.getByRole("button", { name: "JSON", exact: true }).click();
  await modal.locator("textarea").fill('{"name": "Ada Lovelace", "email": "ada@example.com"}');
  await modal.getByRole("button", { name: "Insert", exact: true }).click();
  await expect(page.getByText("Document inserted.").first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // The document row must render the REAL stored field values (round trip
  // through the live engine, not a mock echo).
  await expect(page.getByText("Ada Lovelace").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByText("ada@example.com").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // A reload-from-server (Refresh via tab re-entry) must still show it —
  // proving persistence, not just optimistic local state.
  await page.getByRole("tab", { name: /Secondary indexes/ }).click();
  await page.getByRole("tab", { name: "Documents" }).click();
  await expect(page.getByText("Ada Lovelace").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Delete the document and prove it is gone server-side. -------------
  // CrudTable deletion is checkbox-selection + toolbar Delete (no per-row
  // delete button). Hard assertions — unlike the mocked-tier sibling, this
  // must fail loudly if the deletion surface changes.
  await page.locator("table tbody tr input[type='checkbox']").first().check();
  await page.locator(".btn-error").filter({ hasText: /Delete/i }).first().click();
  await page
    .locator(".modal, .modal-box")
    .locator("button")
    .filter({ hasText: /Delete|Confirm/i })
    .last()
    .click();
  await expect(page.locator("table").getByText("Ada Lovelace")).toHaveCount(0, {
    timeout: LOAD_TIMEOUT,
  });

  // Re-enter the tab: still gone after a fresh server read.
  await page.getByRole("tab", { name: /Secondary indexes/ }).click();
  await page.getByRole("tab", { name: "Documents" }).click();
  await expect(page.locator("table").getByText("Ada Lovelace")).toHaveCount(0, {
    timeout: LOAD_TIMEOUT,
  });
});
