// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * vector-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Vector round trip against a live daemon: create a Vector-engine database
 * through the REAL creation wizard, create a namespace, upsert a vector, and
 * find it again via search-by-ID. The mocked tier covers the Vector view
 * exhaustively but no tier ever proved a vector survives the real wire path
 * (wizard-provisioned engine -> REST upsert -> real index read-back).
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

test("vector round-trips through a wizard-created vector database against the live backend", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Create a Vector database through the real wizard. -----------------
  await page.locator("aside").getByText("New Database").click();
  await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("#db-name").fill("embeddings");
  await page.locator("button").filter({ hasText: "Vector" }).click();
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: /Create/ }).click();
  await expect(page.locator("aside").getByText("embeddings", { exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Create a namespace through the real REST path. --------------------
  await page.locator("aside").getByText("embeddings", { exact: true }).click();
  await page
    .getByRole("button", { name: /Create Namespace|New Namespace/i })
    .first()
    .click();
  const modal = page.locator(".modal");
  await expect(modal.getByRole("heading", { name: "Create Namespace" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await modal.locator("input[placeholder='Namespace name']").fill("docs");
  await modal.locator("input[type='number']").fill("4");
  await modal.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText('Namespace "docs" created.')).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Upsert a vector and read it back from the live index. -------------
  await page.locator("select").first().selectOption("docs");
  await expect(page.locator("select").first()).toHaveValue("docs", { timeout: LOAD_TIMEOUT });

  // Vector IDs are uint64 across the whole stack (proto string IDs are
  // parsed with strconv.ParseUint; REST/transport structs use uint64), so
  // the round-trip must use a numeric ID.
  await expect(page.getByRole("button", { name: "Upsert Vector" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Upsert Vector" }).click();
  await expect(page.getByText("New Vector")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("input[placeholder='Unique ID']").fill("41");
  await page.locator("textarea[placeholder='0.1, 0.5, -0.3, 0.8, ...']").fill("0.1, 0.2, 0.3, 0.4");
  await page.getByRole("button", { name: "Upsert", exact: true }).click();
  await expect(
    page.getByText('Vector "41" upserted in namespace "docs".'),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  // The vectors table lists the REAL stored vector.
  await expect(page.locator("table").getByText("41", { exact: true }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Search by ID via the Search tab: the live index must return it. ---
  await page.getByRole("tab", { name: "Search" }).click();
  // The search form keeps its own namespace selector, separate from the
  // shared selector above the tabs.
  await page.locator(".surface-panel select").first().selectOption("docs");
  await page.getByRole("tab", { name: "By ID" }).click();
  await page.locator("input[placeholder='Enter vector ID...']").fill("41");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(
    page.locator("table").getByText("41", { exact: true }).first(),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
});
