// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Dashboard Metrics card (GET /metrics, Prometheus text exposition).
 *
 * Web mode: /metrics is routed with page.route() to a fixed exposition, so
 * the card's table, filter, expanded family, refresh and 500 error toast run
 * against the real RestClient parser. Desktop mode: the Wails binding has no
 * metrics, so the card says so.
 */

import type { Page, Route } from "@playwright/test";
import {
  webTest,
  expect,
  type WebMockState,
} from "./fixtures/web-mode-mock.ts";
import {
  test as desktopTest,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

/** Timeout for async DOM operations and app boot. */
const LOAD_TIMEOUT = 10_000;

const EXPOSITION = [
  "# HELP qrdb_requests_total Total REST requests.",
  "# TYPE qrdb_requests_total counter",
  'qrdb_requests_total{method="GET",path="/kv/\\"a\\""} 1027',
  'qrdb_requests_total{method="PUT",path="/kv"} 3',
  "# HELP qrdb_request_seconds Request latency.",
  "# TYPE qrdb_request_seconds histogram",
  'qrdb_request_seconds_bucket{le="0.1"} 5',
  'qrdb_request_seconds_bucket{le="+Inf"} 7',
  "qrdb_request_seconds_sum 0.25",
  "qrdb_request_seconds_count 7",
  "# HELP go_goroutines Number of goroutines that currently exist.",
  "# TYPE go_goroutines gauge",
  "go_goroutines 42",
  "",
].join("\n");

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

/** Routes /metrics; the handler decides each response. */
async function routeMetrics(
  page: Page,
  respond: (route: Route) => Promise<void>,
): Promise<void> {
  await page.route("**/metrics", (route) => respond(route));
}

function fulfillExposition(route: Route): Promise<void> {
  return route.fulfill({
    status: 200,
    contentType: "text/plain; version=0.0.4; charset=utf-8",
    body: EXPOSITION,
  });
}

async function gotoDashboard(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator(".breadcrumbs").getByText("Dashboard").waitFor({
    state: "visible",
    timeout: LOAD_TIMEOUT,
  });
}

webTest.describe("Dashboard Metrics card (web mode)", () => {
  webTest.use({ webMockState: webState });

  webTest("lists metric families with type, help and sample count", async ({ page }) => {
    await routeMetrics(page, fulfillExposition);
    await gotoDashboard(page);

    const card = page.getByTestId("dashboard-metrics");
    const table = card.getByTestId("metrics-table");
    await expect(table).toBeVisible({ timeout: LOAD_TIMEOUT });

    const rows = table.getByTestId("metrics-family");
    await expect(rows).toHaveCount(3);

    const requests = table.locator('[data-testid="metrics-family"][data-family="qrdb_requests_total"]');
    await expect(requests).toContainText("counter");
    await expect(requests).toContainText("Total REST requests.");
    await expect(requests.locator("td").last()).toHaveText("2");

    const histogram = table.locator('[data-testid="metrics-family"][data-family="qrdb_request_seconds"]');
    await expect(histogram).toContainText("histogram");
    await expect(histogram.locator("td").last()).toHaveText("4");
  });

  webTest("filter narrows the families by name, type or help", async ({ page }) => {
    await routeMetrics(page, fulfillExposition);
    await gotoDashboard(page);

    const card = page.getByTestId("dashboard-metrics");
    const rows = card.getByTestId("metrics-family");
    await expect(rows).toHaveCount(3, { timeout: LOAD_TIMEOUT });

    const filter = card.getByTestId("metrics-filter");
    await filter.fill("goroutines");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toHaveAttribute("data-family", "go_goroutines");

    await filter.fill("histogram");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toHaveAttribute("data-family", "qrdb_request_seconds");

    await filter.fill("no-such-metric");
    await expect(rows).toHaveCount(0);
    await expect(card.getByTestId("metrics-empty")).toBeVisible();

    await filter.fill("");
    await expect(rows).toHaveCount(3);
  });

  webTest("expanding a family shows its samples with decoded labels", async ({ page }) => {
    await routeMetrics(page, fulfillExposition);
    await gotoDashboard(page);

    const card = page.getByTestId("dashboard-metrics");
    await expect(card.getByTestId("metrics-table")).toBeVisible({ timeout: LOAD_TIMEOUT });

    const toggle = card.getByRole("button", { name: "Toggle qrdb_request_seconds" });
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    const samples = card.locator('[data-testid="metrics-samples"][data-family="qrdb_request_seconds"]');
    await expect(samples.getByTestId("metrics-sample")).toHaveCount(4);
    await expect(samples).toContainText('qrdb_request_seconds_bucket{le="+Inf"}');
    await expect(samples).toContainText("qrdb_request_seconds_sum");
    await expect(samples).toContainText("0.25");

    await card.getByRole("button", { name: "Toggle qrdb_requests_total" }).click();
    const requestSamples = card.locator('[data-testid="metrics-samples"][data-family="qrdb_requests_total"]');
    await expect(requestSamples).toContainText('path="/kv/"a""');
    await expect(requestSamples).toContainText("1027");

    await toggle.click();
    await expect(samples).toHaveCount(0);
  });

  webTest("refresh re-fetches /metrics", async ({ page }) => {
    let calls = 0;
    await routeMetrics(page, (route) => {
      calls++;
      const body =
        calls === 1
          ? EXPOSITION
          : "# TYPE qrdb_refreshed gauge\nqrdb_refreshed 1\n";
      return route.fulfill({ status: 200, contentType: "text/plain", body });
    });
    await gotoDashboard(page);

    const card = page.getByTestId("dashboard-metrics");
    const rows = card.getByTestId("metrics-family");
    await expect(rows).toHaveCount(3, { timeout: LOAD_TIMEOUT });

    await card.getByTestId("metrics-refresh").click();
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toHaveAttribute("data-family", "qrdb_refreshed");
  });

  webTest("a 500 from /metrics shows an error toast", async ({ page }) => {
    await routeMetrics(page, (route) =>
      route.fulfill({ status: 500, contentType: "text/plain", body: "gatherer exploded" }),
    );
    await gotoDashboard(page);

    await expect(page.locator(".toast").getByText(/^Metrics: /)).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    const card = page.getByTestId("dashboard-metrics");
    await expect(card.getByTestId("metrics-error")).toBeVisible();
    await expect(card.getByTestId("metrics-table")).toHaveCount(0);
  });
});

desktopTest.describe("Dashboard Metrics card (desktop mode)", () => {
  desktopTest.use({ mockState: secondLaunchEmbeddedState });

  desktopTest("shows that metrics are not available on desktop", async ({ page }) => {
    await gotoDashboard(page);

    const card = page.getByTestId("dashboard-metrics");
    await expect(card.getByTestId("metrics-unavailable")).toHaveText(
      /not available on desktop/,
      { timeout: LOAD_TIMEOUT },
    );
    await expect(card.getByTestId("metrics-filter")).toHaveCount(0);
  });
});
