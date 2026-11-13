/**
 * Copyright 2026 Jeremy Hahn
 * SPDX-License-Identifier: Apache-2.0
 *
 * Web-mode e2e for the Time Series single-point write: the Write panel's
 * single mode calls timeSeries.writePoint, which the REST client sends as
 * POST /ts/write. The REST API is mocked via page.route(): the test asserts
 * the exact request body, the success toast, and the error toast a 400
 * produces.
 */

import type { Page, Route } from "@playwright/test";
import { webTest as test, expect, type WebMockState } from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 15_000;

interface Captured {
  bodies: Record<string, unknown>[];
}

/**
 * Mocks the time-series REST surface. Reads answer empty; POST /ts/write
 * records its body and answers `writeStatus` (204, or 400 with an error).
 */
async function mockTimeSeries(page: Page, writeStatus: number): Promise<Captured> {
  const captured: Captured = { bodies: [] };
  // The database workspace opens on the KV explorer before the Time Series tab.
  await page.route("**/kv/scan**", (route: Route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ kvs: [], more: false }) }),
  );
  await page.route("**/kv/revision**", (route: Route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ revision: 1 }) }),
  );
  await page.route("**/ts/**", (route: Route) => {
    const url = route.request().url();
    if (url.includes("/ts/metrics")) {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ metrics: [] }) });
    }
    if (url.includes("/ts/retention")) {
      return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ points: [] }) });
  });
  await page.route("**/ts/write", (route: Route) => {
    captured.bodies.push(route.request().postDataJSON() as Record<string, unknown>);
    if (writeStatus >= 400) {
      return route.fulfill({
        status: writeStatus,
        contentType: "application/json",
        body: JSON.stringify({ error: "metric name is invalid" }),
      });
    }
    return route.fulfill({ status: 204, body: "" });
  });
  return captured;
}

async function openTimeSeries(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("default").click();
  await expect(page.locator(".breadcrumbs").getByText("default", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("tab", { name: "Time Series", exact: true }).click();
}

async function writeOnePoint(page: Page, metric: string, value: string, tags: string, timestamp?: string): Promise<void> {
  await page.getByRole("button", { name: "Write", exact: true }).first().click();
  const panel = page.getByRole("dialog", { name: "Write Data Point" });
  await expect(panel).toBeVisible();
  await panel.locator("input[placeholder='cpu.usage']").fill(metric);
  await panel.locator("input[placeholder='42.5']").fill(value);
  await panel.locator("input[placeholder='host=node1,region=us-east']").fill(tags);
  if (timestamp !== undefined) {
    await panel.locator("input[placeholder='Leave empty for now']").fill(timestamp);
  }
  await panel.getByRole("button", { name: "Write" }).click();
}

const webState: WebMockState = {
  sealStatus: {
    sealed: false,
    initialized: true,
    strategy: "none",
    hardware_backed: false,
    initialized_at: "2026-01-01T00:00:00Z",
  },
  databases: [{ name: "default", backend: "pebble", status: "open" }],
};

test.describe("Time Series write one point (web mode)", () => {
  test.use({ webMockState: webState });

  test("sends the point to POST /ts/write and shows the success toast", async ({ page }) => {
    const captured = await mockTimeSeries(page, 204);
    await openTimeSeries(page);

    await writeOnePoint(page, "cpu.usage", "42.5", "host=node1,region=us-east", "1700000000000000000");
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({ timeout: LOAD_TIMEOUT });

    expect(captured.bodies).toHaveLength(1);
    expect(captured.bodies[0]).toEqual({
      metric: "cpu.usage",
      value: 42.5,
      timestamp: 1700000000000000000,
      tags: { host: "node1", region: "us-east" },
    });
  });

  test("defaults the timestamp to now", async ({ page }) => {
    const captured = await mockTimeSeries(page, 204);
    await openTimeSeries(page);

    const before = Date.now() * 1_000_000;
    await writeOnePoint(page, "mem.free", "7", "host=a");
    await expect(page.getByText('Data point written for "mem.free"')).toBeVisible({ timeout: LOAD_TIMEOUT });
    const after = Date.now() * 1_000_000;

    expect(captured.bodies).toHaveLength(1);
    const ts = captured.bodies[0]["timestamp"] as number;
    expect(ts).toBeGreaterThanOrEqual(before - 60_000 * 1_000_000);
    expect(ts).toBeLessThanOrEqual(after + 60_000 * 1_000_000);
  });

  test("a 400 shows the server's error toast", async ({ page }) => {
    const captured = await mockTimeSeries(page, 400);
    await openTimeSeries(page);

    await writeOnePoint(page, "bad metric", "1", "host=a");
    await expect(page.getByText("metric name is invalid")).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByText('Data point written for "bad metric"')).toHaveCount(0);
    expect(captured.bodies).toHaveLength(1);
  });
});
