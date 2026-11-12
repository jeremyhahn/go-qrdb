// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * timeseries-advanced-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Advanced time-series operations against a live daemon, all driven through
 * the TimeSeries view: a batch write (timeSeries.writeBatch), an aggregate
 * (timeSeries.aggregate), a rich query with a tag filter (timeSeries.richQuery)
 * and a retention policy set and read back (timeSeries.setRetention /
 * timeSeries.getRetention). Each value the UI shows is compared with the REST
 * answer the server gave, and the retention policy is read back from the
 * server independently of the page.
 */

import { test, expect, request, type Page, type Response } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const METRIC = "cpu.batch";
const SEVEN_DAYS_NS = 7 * 24 * 3600 * 1_000_000_000;
const ONE_HOUR_NS = 3600 * 1_000_000_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

async function createTimeSeriesDatabase(page: Page, name: string): Promise<void> {
  await page.locator("aside").getByText("New Database").click();
  await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("#db-name").fill(name);
  await page.locator("button").filter({ hasText: "TimeSeries" }).click();
  await page.getByRole("button", { name: "Next" }).click();
  for (const heading of [
    "Storage Backend",
    "SQL Wire Protocols",
    "Database Encryption Barrier",
  ]) {
    await expect(page.getByRole("heading", { name: heading })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.getByRole("button", { name: "Next" }).click();
  }
  await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: /Create/ }).click();
  await expect(page.locator("aside").getByText(name, { exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

function isTS(path: string, method: string): (r: Response) => boolean {
  return (r) => new URL(r.url()).pathname.endsWith(path) && r.request().method() === method;
}

test("batch write, aggregate, rich query and retention round-trip against the live backend", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await createTimeSeriesDatabase(page, "metrics");

  await page.locator("aside").getByText("metrics", { exact: true }).click();
  await page.getByRole("tab", { name: "Time Series", exact: true }).click();
  await expect(page.getByText("0 metrics")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- writeBatch: three points, two tagged host=a, one host=b. ----------
  const baseNs = (Date.now() - 120_000) * 1_000_000;
  const lines = [
    `${baseNs},10,host=a`,
    `${baseNs + 1_000_000_000},20,host=b`,
    `${baseNs + 2_000_000_000},30,host=a`,
  ];
  await page.getByRole("button", { name: "Write", exact: true }).first().click();
  const writePanel = page.getByRole("dialog", { name: "Write Data Point" });
  await expect(writePanel).toBeVisible({ timeout: LOAD_TIMEOUT });
  await writePanel.getByTestId("ts-write-mode-batch").click();
  await writePanel.locator("input[placeholder='cpu.usage']").fill(METRIC);
  await writePanel.getByTestId("ts-write-batch").fill(lines.join("\n"));
  const batchResp = page.waitForResponse(isTS("/ts/write-batch", "POST"));
  await writePanel.getByRole("button", { name: "Write" }).click();
  expect((await batchResp).ok()).toBe(true);
  await expect(writePanel).not.toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByText("1 metrics")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Open the metric: the server's retention answer is captured. -------
  const retGet = page.waitForResponse(isTS(`/ts/series/${METRIC}/retention`, "GET"));
  await page.locator("[role='button']", { hasText: METRIC }).first().click();
  await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  const retGetResp = await retGet;
  expect(retGetResp.ok()).toBe(true);
  const retentionUrl = retGetResp.url();
  const retentionHeaders = await retGetResp.request().allHeaders();
  await expect(page.getByText("3 data points", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("ts-retention-none")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- aggregate: Sum over 1h buckets equals the REST buckets. -----------
  await page.getByTestId("ts-agg-function").selectOption({ label: "Sum" });
  await page.getByTestId("ts-agg-interval").fill("1h");
  const aggResp = page.waitForResponse(isTS("/ts/aggregate", "GET"));
  await page.getByTestId("ts-agg-run").click();
  const aggBody = (await (await aggResp).json()) as {
    results: { timestamp: number; value: number; count: number }[] | null;
  };
  const buckets = aggBody.results ?? [];
  expect(buckets.length).toBeGreaterThan(0);
  expect(buckets.reduce((s, b) => s + b.value, 0)).toBe(60);
  expect(buckets.reduce((s, b) => s + b.count, 0)).toBe(3);
  await expect(page.getByTestId("ts-agg-value")).toHaveText(buckets.map((b) => String(b.value)));

  // --- richQuery: host=a filter, Sum aggregation. ------------------------
  await page.getByTestId("ts-rich-tags").fill("host=a");
  await page.getByTestId("ts-rich-aggregation").selectOption({ label: "Sum" });
  const rqResp = page.waitForResponse(isTS("/ts/query-rich", "POST"));
  await page.getByTestId("ts-rich-run").click();
  const rqBody = (await (await rqResp).json()) as {
    aggregated_value?: number;
    points: { value: number; tags?: Record<string, string> }[] | null;
  };
  const rqPoints = rqBody.points ?? [];
  expect(rqBody.aggregated_value).toBe(40);
  expect(rqPoints.every((p) => p.tags?.host === "a")).toBe(true);
  await expect(page.getByTestId("ts-rich-aggregated")).toHaveText(String(rqBody.aggregated_value));
  await expect(page.getByTestId("ts-rich-point-count")).toHaveText(`${rqPoints.length} points`);

  // --- setRetention, then getRetention straight from the server. ---------
  await page.getByTestId("ts-retention-duration-input").fill("7d");
  await page.getByTestId("ts-retention-resolution-input").fill("1h");
  await page.getByTestId("ts-retention-aggregation-input").selectOption({ label: "Avg" });
  const retSet = page.waitForResponse(isTS(`/ts/series/${METRIC}/retention`, "PUT"));
  await page.getByTestId("ts-retention-save").click();
  expect((await retSet).ok()).toBe(true);
  await expect(page.getByTestId("ts-retention-duration")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("ts-retention-aggregation")).toHaveText("Avg");

  // Read the policy back from the server, outside the page, with the same
  // database scope the SDK sent.
  const { host: _host, ...scopeHeaders } = retentionHeaders;
  const api = await request.newContext({ ignoreHTTPSErrors: true });
  const fromServer = await api.get(daemon!.adminUrl + new URL(retentionUrl).pathname, {
    headers: scopeHeaders,
  });
  expect(fromServer.ok()).toBe(true);
  const serverBody = (await fromServer.json()) as {
    policy: { duration_ns: number; resolution_ns?: number; aggregation?: string } | null;
  };
  expect(serverBody.policy).toEqual({
    duration_ns: SEVEN_DAYS_NS,
    resolution_ns: ONE_HOUR_NS,
    aggregation: "avg",
  });
  await expect(page.getByTestId("ts-retention-current")).toContainText(`${SEVEN_DAYS_NS} ns`);
  await expect(page.getByTestId("ts-retention-current")).toContainText(`${ONE_HOUR_NS} ns`);
});
