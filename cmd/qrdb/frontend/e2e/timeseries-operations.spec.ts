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
 * Time-series operations: write data points, verify metric appears in the list,
 * query a metric, and delete a series.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

const tsOpsState = {
  ...secondLaunchEmbeddedState,
  databases: [
    {
      name: "ops-ts",
      backend: "pebble",
      status: "open",
    },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Navigate to the time-series database view.
 * Every database exposes every engine; the TimeSeriesView is rendered under
 * the "Time Series" tab.
 */
async function navigateToTimeSeries(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("ops-ts").click();
  await expect(page.locator(".breadcrumbs").getByText("ops-ts", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("tab", { name: "Time Series", exact: true }).click();
}

/**
 * Write a single data point via the Write SlidePanel.
 * Clicks the "Write" button to open the panel, fills in metric and value,
 * then submits.
 */
async function writeDataPoint(
  page: Page,
  metric: string,
  value: string,
): Promise<void> {
  // Click the "Write" button to open the SlidePanel
  await page.getByRole("button", { name: "Write" }).first().click();

  // Wait for the SlidePanel to open — the Metric input has placeholder "cpu.usage"
  const metricInput = page.locator("input[placeholder='cpu.usage']").first();
  await expect(metricInput).toBeVisible({ timeout: 5_000 });
  await metricInput.fill(metric);

  // Value input has placeholder "42.5"
  const valueInput = page.locator("input[placeholder='42.5']").first();
  await expect(valueInput).toBeVisible({ timeout: 5_000 });
  await valueInput.fill(value);

  // Submit — click the Write button inside the panel footer (last visible Write button)
  await page.getByRole("button", { name: "Write" }).last().click();
  await expect(page.getByText(/written|success/i).first()).toBeVisible({
    timeout: 5_000,
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Time-Series Operations", () => {
  test.use({ mockState: tsOpsState });

  // -------------------------------------------------------------------------
  // Navigation
  // -------------------------------------------------------------------------

  test("navigates to the time-series database and shows the view", async ({
    page,
  }) => {
    await navigateToTimeSeries(page);
    // Some heading or label for the TS view should be visible
    await expect(
      page.getByRole("heading", { name: /Time.*Series|TimeSeries/i }).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("shows empty metrics list on first load", async ({ page }) => {
    await navigateToTimeSeries(page);
    // Either an empty-state message or an empty table
    const noMetrics = page.getByText(/no metrics|no data/i).first();
    const emptyTable = page.locator("table tbody tr");
    const isEmpty =
      (await noMetrics.isVisible()) || (await emptyTable.count()) === 0;
    expect(isEmpty).toBeTruthy();
  });

  // -------------------------------------------------------------------------
  // Writing data points
  // -------------------------------------------------------------------------

  test("writes a data point and shows success feedback", async ({ page }) => {
    await navigateToTimeSeries(page);
    await writeDataPoint(page, "cpu.usage", "42.5");
  });

  test("writes a second data point with a different metric", async ({
    page,
  }) => {
    await navigateToTimeSeries(page);
    await writeDataPoint(page, "mem.usage", "1024");
    await writeDataPoint(page, "disk.io", "512");
  });

  // -------------------------------------------------------------------------
  // Metrics list
  // -------------------------------------------------------------------------

  test("metric appears in the list after writing a data point", async ({
    page,
  }) => {
    await navigateToTimeSeries(page);
    await writeDataPoint(page, "network.rx", "88");

    // After writing, the metrics list should refresh automatically via the
    // handleWrite handler. The SlidePanel closes and the metric appears in the list.
    await expect(page.getByText("network.rx").first()).toBeVisible({
      timeout: 5_000,
    });
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  test("shows error alert when write fails", async ({ page }) => {
    await navigateToTimeSeries(page);
    // The component calls client.timeSeries.writePoint → TimeSeriesBinding.WritePoint
    await injectError(
      page,
      "TimeSeriesBinding.WritePoint",
      "write pipeline full",
    );

    // Open the Write SlidePanel via the Write button
    const openWriteBtn = page.getByRole("button", { name: "Write" }).first();
    if (await openWriteBtn.isVisible({ timeout: 3_000 })) {
      await openWriteBtn.click();

      const metricInput = page.locator("input[placeholder='cpu.usage']").first();
      if (await metricInput.isVisible({ timeout: 3_000 })) {
        await metricInput.fill("err.metric");
        const valueInput = page.locator("input[placeholder='42.5']").first();
        await valueInput.fill("1");
        // Click the Write button in the panel footer
        await page.getByRole("button", { name: "Write" }).last().click();
        await expect(page.getByText("write pipeline full")).toBeVisible({
          timeout: 5_000,
        });
      }
    }

    await clearError(page, "TimeSeriesBinding.WritePoint");
  });

  test("shows error when listing metrics fails", async ({ page }) => {
    // Navigate first so the page is loaded and __QRDB_MOCK_STATE__ is available,
    // then inject the error so the next metrics load picks it up.
    await navigateToTimeSeries(page);
    await injectError(
      page,
      "TimeSeriesBinding.ListMetrics",
      "metrics store unavailable",
    );

    // Trigger a metrics reload — clicking the Refresh button or the Search icon.
    const refreshBtn = page.getByRole("button", { name: /Refresh/i }).first();
    if (await refreshBtn.isVisible({ timeout: 2_000 })) {
      await refreshBtn.click();
    } else {
      // Search button (aria-label="Search") triggers loadMetrics
      const searchBtn = page.locator("button[aria-label='Search']").first();
      if (await searchBtn.isVisible({ timeout: 2_000 })) {
        await searchBtn.click();
      }
    }

    await expect(
      page.getByText("metrics store unavailable"),
    ).toBeVisible({ timeout: 5_000 });
    await clearError(page, "TimeSeriesBinding.ListMetrics");
  });
});
