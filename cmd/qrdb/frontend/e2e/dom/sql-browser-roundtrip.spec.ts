// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * sql-browser-roundtrip.spec.ts (real-backend DOM tier)
 *
 * SQL query execution round trip against a live daemon. The mocked tier
 * covers the SQL Browser UI and the real-backend API tier covers the protocol
 * TOGGLE (sql-toggle-real), but until this spec no tier ever executed a SQL
 * query end-to-end through the UI against real data: enable a SQL wire
 * protocol in Settings (the SQL Browser tab only exists when one is enabled),
 * write a KV entry, and prove SELECT * FROM kv returns it through the real
 * query path.
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

test("SQL query executes against live KV data after enabling the protocol in Settings", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("system", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Seed a KV entry the SQL query must find. ---------------------------
  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  await page.locator("input[placeholder='Enter key']").fill("sql/roundtrip");
  await page
    .locator("textarea[placeholder='Enter value (supports JSON)']")
    .fill("sql-visible-value");
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText('Created "sql/roundtrip"').last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- No SQL protocol yet: the SQL Browser tab must be absent. -----------
  const tabList = page.getByRole("tablist", { name: "Database tools" });
  await expect(tabList.getByText("SQL Browser")).toHaveCount(0);

  // --- Enable MySQL in Settings; the tab must appear. ---------------------
  await tabList.getByText("Settings", { exact: true }).click();
  const mysqlToggle = page.locator("input.toggle").first();
  await mysqlToggle.check();
  await expect(page.getByText(/MySQL enabled/i).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(tabList.getByText("SQL Browser")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Execute the default kv query through the real backend. -------------
  await tabList.getByText("SQL Browser").click();
  const queryArea = page.locator("textarea[placeholder='Enter SQL query...']");
  await expect(queryArea).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(queryArea).toHaveValue("SELECT * FROM kv");
  await page.getByRole("button", { name: "Execute" }).click();

  // The seeded row must come back through the live SQL execution path.
  // Scope both assertions to the results table so a lingering "Created"
  // toast (which also contains the key text) can never satisfy them. The
  // value must render as plaintext: the server decodes []byte cells to
  // UTF-8 strings before JSON encoding, so the wire never carries base64.
  const resultsTable = page.locator("table");
  await expect(resultsTable.getByText("sql/roundtrip").first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(resultsTable.getByText("sql-visible-value").first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
});
