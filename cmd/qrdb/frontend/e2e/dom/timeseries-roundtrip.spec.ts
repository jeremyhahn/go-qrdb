// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * timeseries-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Time-series round trip against a live daemon: create a TimeSeries-engine
 * database through the REAL creation wizard, write a data point through the
 * Write panel, and read it back through the metric list. The mocked tier
 * covers the TS view exhaustively but no tier ever proved a TS point survives
 * the real wire path (wizard-created engine -> REST write -> query read-back),
 * nor that the wizard actually provisions a working ts-engine database.
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

test("TS data point round-trips through a wizard-created ts database against the live backend", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Create a TimeSeries database through the real wizard. -------------
  await page.locator("aside").getByText("New Database").click();
  await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("#db-name").fill("metrics");
  await page.locator("button").filter({ hasText: "TimeSeries" }).click();
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

  // The REAL backend must provision the database: it appears in the sidebar.
  await expect(page.locator("aside").getByText("metrics", { exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Open it and write a data point through the real REST path. --------
  await page.locator("aside").getByText("metrics", { exact: true }).click();
  await expect(
    page.getByRole("heading", { name: /Time.*Series|TimeSeries/i }).first(),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.getByRole("button", { name: "Write" }).first().click();
  const metricInput = page.locator("input[placeholder='cpu.usage']").first();
  await expect(metricInput).toBeVisible({ timeout: LOAD_TIMEOUT });
  await metricInput.fill("cpu.load");
  await page.locator("input[placeholder='42.5']").first().fill("73.25");
  await page.getByRole("button", { name: "Write" }).last().click();
  await expect(page.getByText(/written|success/i).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Read it back: metric list shows the REAL stored series. ------------
  await expect(page.getByText("cpu.load").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // A server refresh must still list the metric (persistence, not local echo).
  await page.getByRole("button", { name: "Refresh" }).first().click();
  await expect(page.getByText("cpu.load").first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Open the metric detail: the stored value must surface in the data table.
  await page.getByText("cpu.load").first().click();
  await expect(page.getByText("73.25").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
});
