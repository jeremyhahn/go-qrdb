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

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const tsState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-ts", backend: "pebble", status: "open" },
  ],
};

/**
 * Inject a stateful in-memory time series store that replaces the default
 * static TimeSeriesBinding mock. Must be called before `page.goto()` so
 * the init script is registered before the app boots.
 */
async function injectStatefulTimeSeriesBinding(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.addInitScript(() => {
    interface TSDataPoint {
      metric: string;
      value: number;
      timestampNs: number;
      tags?: { key: string; value: string }[];
    }

    interface TSQueryRequest {
      metric: string;
      startNs: number;
      endNs: number;
    }

    interface TSAggregateRequest {
      metric: string;
      startNs: number;
      endNs: number;
      aggregation: string;
    }

    interface TSDeleteRequest {
      metric: string;
    }

    interface TSGetRetentionRequest {
      metric: string;
    }

    interface TSSetRetentionRequest {
      metric: string;
      days: number;
    }

    interface TSListMetricsRequest {
      prefix?: string;
    }

    const tsPoints: TSDataPoint[] = [];
    const retentionStore = new Map<string, number>();

    function getState(): { errors: Record<string, string> } {
      const w = window as unknown as Record<string, unknown>;
      return w.__QRDB_MOCK_STATE__ as { errors: Record<string, string> };
    }

    function maybeReject(method: string): void {
      const s = getState();
      if (s.errors[method]) {
        throw new Error(s.errors[method]);
      }
    }

    // Wait for the mock namespace to exist, then override the binding.
    // The base wails-mock addInitScript runs first and creates window.go.
    const waitForGo = setInterval(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as Record<string, Record<string, Record<string, unknown>>> | undefined;
      if (!go?.main?.TimeSeriesBinding) return;
      clearInterval(waitForGo);

      go.main.TimeSeriesBinding = {
        Write: async (point: Record<string, unknown>) => {
          // TimeSeriesBinding.Write(point *TSDataPoint): one snake_case object.
          maybeReject("TimeSeriesBinding.Write");
          tsPoints.push({
            metric: point["metric"] as string,
            value: point["value"] as number,
            timestampNs: (point["timestamp_ns"] ?? point["timestampNs"] ?? Date.now() * 1_000_000) as number,
            tags: point["tags"] as { key: string; value: string }[] | undefined,
          });
        },
        WriteBatch: async (points: TSDataPoint[]) => {
          maybeReject("TimeSeriesBinding.WriteBatch");
          for (const p of points) {
            tsPoints.push({ ...p });
          }
        },
        WritePoint: async (point: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.WritePoint");
          // SDK's toSnake() converts timestampNs → timestamp_ns before calling the binding.
          const tsNs = (point["timestamp_ns"] ?? point["timestampNs"]) as number;
          tsPoints.push({
            metric: point["metric"] as string,
            value: point["value"] as number,
            timestampNs: tsNs,
            tags: point["tags"] as { key: string; value: string }[] | undefined,
          });
        },
        Query: async (req: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.Query");
          // SDK's toSnake() converts startNs → start_ns, endNs → end_ns.
          const startNs = (req["start_ns"] ?? req["startNs"]) as number;
          const endNs = (req["end_ns"] ?? req["endNs"]) as number;
          const filtered = tsPoints.filter(
            (p) =>
              p.metric === req["metric"] &&
              p.timestampNs >= startNs &&
              p.timestampNs <= endNs,
          );
          return {
            points: filtered.map((p) => ({
              metric: p.metric,
              value: p.value,
              timestampNs: p.timestampNs,
              tags: p.tags ?? [],
            })),
          };
        },
        Aggregate: async (req: TSAggregateRequest) => {
          maybeReject("TimeSeriesBinding.Aggregate");
          // SDK's toSnake() converts startNs → start_ns, endNs → end_ns.
          const startNs = ((req as unknown as Record<string, unknown>)["start_ns"] ?? req.startNs) as number;
          const endNs = ((req as unknown as Record<string, unknown>)["end_ns"] ?? req.endNs) as number;
          const filtered = tsPoints.filter(
            (p) =>
              p.metric === req.metric &&
              p.timestampNs >= startNs &&
              p.timestampNs <= endNs,
          );
          if (filtered.length === 0) return { result: 0 };
          const values = filtered.map((p) => p.value);
          switch (req.aggregation) {
            case "sum":
              return { result: values.reduce((a, b) => a + b, 0) };
            case "avg":
              return { result: values.reduce((a, b) => a + b, 0) / values.length };
            case "min":
              return { result: Math.min(...values) };
            case "max":
              return { result: Math.max(...values) };
            case "count":
              return { result: values.length };
            default:
              return { result: 0 };
          }
        },
        RichQuery: async () => {
          maybeReject("TimeSeriesBinding.RichQuery");
          return { series: [] };
        },
        DeleteSeries: async (req: TSDeleteRequest) => {
          maybeReject("TimeSeriesBinding.DeleteSeries");
          let deleted = 0;
          for (let i = tsPoints.length - 1; i >= 0; i--) {
            if (tsPoints[i].metric === req.metric) {
              tsPoints.splice(i, 1);
              deleted++;
            }
          }
          retentionStore.delete(req.metric);
          return { deleted };
        },
        GetRetention: async (req: TSGetRetentionRequest) => {
          maybeReject("TimeSeriesBinding.GetRetention");
          return { days: retentionStore.get(req.metric) ?? 0 };
        },
        SetRetention: async (req: TSSetRetentionRequest) => {
          maybeReject("TimeSeriesBinding.SetRetention");
          retentionStore.set(req.metric, req.days);
        },
        ListMetrics: async (req: TSListMetricsRequest) => {
          maybeReject("TimeSeriesBinding.ListMetrics");
          const metricSet = new Set<string>();
          for (const p of tsPoints) {
            metricSet.add(p.metric);
          }
          let metrics = Array.from(metricSet);
          if (req.prefix) {
            metrics = metrics.filter((m) => m.startsWith(req.prefix!));
          }
          return { metrics };
        },
      };
    }, 0);
  });
}

/** Navigate to the timeseries database workspace. */
async function navigateToTimeSeries(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("test-ts").click();
  await expect(page.locator(".breadcrumbs").getByText("test-ts", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("tab", { name: "Time Series", exact: true }).click();
}

/**
 * Write a data point through the UI slide panel.
 */
async function writePointViaUI(
  page: import("@playwright/test").Page,
  metric: string,
  value: string,
  opts?: { timestamp?: string; tags?: string },
): Promise<void> {
  // Click the header Write button (not the one inside the dialog)
  await page.getByRole("button", { name: "Write", exact: true }).first().click();
  await expect(page.getByRole("dialog", { name: "Write Data Point" })).toBeVisible();

  const writePanel = page.getByRole("dialog", { name: "Write Data Point" });

  await writePanel.locator("input[placeholder='cpu.usage']").fill(metric);
  await writePanel.locator("input[placeholder='42.5']").fill(value);

  if (opts?.timestamp) {
    await writePanel.locator("input[placeholder='Leave empty for now']").fill(opts.timestamp);
  }

  if (opts?.tags) {
    await writePanel.locator("input[placeholder='host=node1,region=us-east']").fill(opts.tags);
  }

  // Click the Write button inside the panel footer
  await writePanel.getByRole("button", { name: "Write" }).click();
}

// ---------------------------------------------------------------------------
// Time Series E2E Tests
// ---------------------------------------------------------------------------

test.describe("TimeSeries Explorer", () => {
  test.use({ mockState: tsState });

  test("shows empty state when no metrics exist", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await expect(page.getByText("No metrics found")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByText("0 metrics")).toBeVisible();
  });

  test("write data point and see metric in list", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    await writePointViaUI(page, "cpu.usage", "72.5", { timestamp: tsNs });

    // Verify success toast
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Metric should appear in the list
    await expect(page.locator(".font-mono", { hasText: "cpu.usage" }).first()).toBeVisible();
    await expect(page.getByText("1 metrics")).toBeVisible();
  });

  test("write multiple metrics and verify list", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();

    // Write first metric
    await writePointViaUI(page, "cpu.usage", "72.5", {
      timestamp: String(now * 1_000_000),
    });
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Write second metric
    await writePointViaUI(page, "mem.usage", "45.0", {
      timestamp: String(now * 1_000_000),
    });
    await expect(page.getByText('Data point written for "mem.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Both metrics should appear
    await expect(page.getByText("2 metrics")).toBeVisible();
    await expect(page.locator(".font-mono", { hasText: "cpu.usage" }).first()).toBeVisible();
    await expect(page.locator(".font-mono", { hasText: "mem.usage" }).first()).toBeVisible();
  });

  test("write data point with tags", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    await writePointViaUI(page, "disk.io", "128.0", {
      timestamp: tsNs,
      tags: "host=node1,region=us-east",
    });

    await expect(page.getByText('Data point written for "disk.io"')).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.locator(".font-mono", { hasText: "disk.io" }).first()).toBeVisible();
  });

  test("write data point without timestamp uses auto-generated timestamp", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "net.rx", "1024.0");

    await expect(page.getByText('Data point written for "net.rx"')).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.locator(".font-mono", { hasText: "net.rx" }).first()).toBeVisible();
  });

  test("query data points for a metric", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Write a data point with an auto-generated timestamp (avoids nanosecond
    // precision issues that arise when passing explicit timestamps as strings)
    await writePointViaUI(page, "cpu.usage", "72.5");
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Click the metric to view data points
    await page.locator("[role='button']", { hasText: "cpu.usage" }).first().click();

    // Should show the data points view with "Back to Metrics" button
    await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Should display data point in the table
    await expect(page.getByText("1 data points")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("72.5")).toBeVisible();
  });

  test("query shows empty state when no data points in range", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Write a data point far in the past (outside default 1h range)
    const pastTs = String((Date.now() - 7_200_000) * 1_000_000); // 2 hours ago
    await writePointViaUI(page, "old.metric", "10.0", { timestamp: pastTs });
    await expect(page.getByText('Data point written for "old.metric"')).toBeVisible({
      timeout: 5_000,
    });

    // Click the metric
    await page.locator("[role='button']", { hasText: "old.metric" }).first().click();

    await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Data point is outside the default 1h range, so should show no data
    await expect(page.getByText("No data points found")).toBeVisible();
  });

  test("click data point row opens detail panel", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Write with auto-generated timestamp to avoid nanosecond precision issues
    await writePointViaUI(page, "cpu.usage", "72.5", {
      tags: "host=node1",
    });
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Navigate to metric detail
    await page.locator("[role='button']", { hasText: "cpu.usage" }).first().click();
    await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Wait for the data point to appear in the table
    await expect(page.getByText("72.5")).toBeVisible({ timeout: 5_000 });

    // Click the data point row (the value cell)
    await page.getByText("72.5").click();

    // Detail panel should show data point info (exact: true avoids matching "Write Data Point")
    const detailPanel = page.getByRole("dialog", { name: "Data Point", exact: true });
    await expect(detailPanel).toBeVisible({ timeout: 5_000 });
    await expect(detailPanel.getByText("cpu.usage")).toBeVisible();
    // Timestamp is auto-generated so only verify it contains "ns" suffix
    await expect(detailPanel.getByText(/ ns$/)).toBeVisible();
    await expect(detailPanel.getByText("host=node1")).toBeVisible();
  });

  test("delete metric with confirmation", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    // Write a data point
    await writePointViaUI(page, "to-delete", "99.9", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "to-delete"')).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.getByText("1 metrics")).toBeVisible();

    // Find the metric row and click the delete button
    const metricRow = page.locator("[role='button']", { hasText: "to-delete" }).first();
    // The delete button is within the metric row; force click since it may be hidden
    const deleteBtn = metricRow.locator("[aria-label='Delete metric']");
    await deleteBtn.click({ force: true });

    // Confirm deletion in modal
    await expect(page.getByText("Delete Metric")).toBeVisible();
    await expect(page.getByText('Are you sure you want to delete all data for "to-delete"')).toBeVisible();
    await page.locator("dialog").getByRole("button", { name: "Delete" }).click();

    // Verify success toast
    await expect(page.getByText('Deleted metric "to-delete"')).toBeVisible({
      timeout: 5_000,
    });

    // Metric should be gone
    await expect(page.getByText("0 metrics")).toBeVisible();
  });

  test("back to metrics button returns to metric list", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    await writePointViaUI(page, "cpu.usage", "72.5", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Navigate to metric detail
    await page.locator("[role='button']", { hasText: "cpu.usage" }).first().click();
    await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Go back
    await page.getByRole("button", { name: "Back to Metrics" }).click();

    // Should see the metric list again
    await expect(page.getByText("1 metrics")).toBeVisible();
    await expect(page.getByText("No metrics found")).not.toBeVisible();
  });

  test("prefix filter narrows metrics list", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    // Write metrics with different prefixes
    await writePointViaUI(page, "cpu.usage", "72.5", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    await writePointViaUI(page, "cpu.temp", "55.0", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "cpu.temp"')).toBeVisible({
      timeout: 5_000,
    });

    await writePointViaUI(page, "mem.usage", "45.0", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "mem.usage"')).toBeVisible({
      timeout: 5_000,
    });

    await expect(page.getByText("3 metrics")).toBeVisible();

    // Filter by prefix
    const filterInput = page.locator("input[placeholder='Filter by metric prefix...']");
    await filterInput.fill("cpu.");
    await page.locator("[aria-label='Search']").click();

    // Only cpu metrics should remain
    await expect(page.getByText("2 metrics")).toBeVisible();
  });

  test("refresh button reloads metrics", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    await writePointViaUI(page, "cpu.usage", "72.5", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Click refresh
    await page.getByRole("button", { name: "Refresh" }).click();

    // Metric should still be visible
    await expect(page.locator(".font-mono", { hasText: "cpu.usage" }).first()).toBeVisible();
    await expect(page.getByText("1 metrics")).toBeVisible();
  });

  test("chart type buttons toggle between line, area, and scatter", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Write with auto-generated timestamp to avoid nanosecond precision issues
    await writePointViaUI(page, "cpu.usage", "72.5");
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Navigate to metric data view
    await page.locator("[role='button']", { hasText: "cpu.usage" }).first().click();
    await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Wait for data to load before checking chart controls
    await expect(page.getByText("1 data points")).toBeVisible({ timeout: 5_000 });

    // Chart toggle button should be visible
    const chartToggle = page.getByRole("button", { name: "Chart", exact: true });
    await expect(chartToggle).toBeVisible();

    // Chart type buttons
    const lineBtn = page.getByRole("button", { name: "Line chart" });
    const areaBtn = page.getByRole("button", { name: "Area chart" });
    const scatterBtn = page.getByRole("button", { name: "Scatter chart" });

    await expect(lineBtn).toBeVisible();
    await expect(areaBtn).toBeVisible();
    await expect(scatterBtn).toBeVisible();

    // Click area chart
    await areaBtn.click();
    await expect(areaBtn).toHaveClass(/btn-active/);

    // Click scatter chart
    await scatterBtn.click();
    await expect(scatterBtn).toHaveClass(/btn-active/);

    // Click line chart
    await lineBtn.click();
    await expect(lineBtn).toHaveClass(/btn-active/);
  });
});

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

test.describe("TimeSeries Validation", () => {
  test.use({ mockState: tsState });

  test("write with empty metric shows warning", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await page.getByRole("button", { name: "Write", exact: true }).first().click();
    const writePanel = page.getByRole("dialog", { name: "Write Data Point" });
    await expect(writePanel).toBeVisible();

    // Leave metric empty, fill value
    await writePanel.locator("input[placeholder='42.5']").fill("10.0");
    await writePanel.getByRole("button", { name: "Write" }).click();

    await expect(page.getByText("Metric and value are required")).toBeVisible({
      timeout: 5_000,
    });
  });

  test("write with empty value shows warning", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await page.getByRole("button", { name: "Write", exact: true }).first().click();
    const writePanel = page.getByRole("dialog", { name: "Write Data Point" });
    await expect(writePanel).toBeVisible();

    // Fill metric, leave value empty
    await writePanel.locator("input[placeholder='cpu.usage']").fill("test.metric");
    await writePanel.getByRole("button", { name: "Write" }).click();

    await expect(page.getByText("Metric and value are required")).toBeVisible({
      timeout: 5_000,
    });
  });

  test("write with invalid value shows warning", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await page.getByRole("button", { name: "Write", exact: true }).first().click();
    const writePanel = page.getByRole("dialog", { name: "Write Data Point" });
    await expect(writePanel).toBeVisible();

    await writePanel.locator("input[placeholder='cpu.usage']").fill("test.metric");
    await writePanel.locator("input[placeholder='42.5']").fill("not-a-number");
    await writePanel.getByRole("button", { name: "Write" }).click();

    await expect(page.getByText("Value must be a valid number")).toBeVisible({
      timeout: 5_000,
    });
  });

  test("cancel button closes write panel", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await page.getByRole("button", { name: "Write", exact: true }).first().click();
    const writePanel = page.getByRole("dialog", { name: "Write Data Point" });
    await expect(writePanel).toBeVisible();

    await writePanel.getByRole("button", { name: "Cancel" }).click();

    await expect(writePanel).not.toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Error Handling
// ---------------------------------------------------------------------------

test.describe("TimeSeries Error Handling", () => {
  test.use({ mockState: tsState });

  test("write error shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Inject error for WritePoint after page load
    await injectError(page, "TimeSeriesBinding.Write", "storage write failed");

    const now = Date.now();
    const tsNs = String(now * 1_000_000);
    await writePointViaUI(page, "fail.metric", "10.0", { timestamp: tsNs });

    await expect(page.getByText("storage write failed")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "TimeSeriesBinding.Write");
  });

  test("list metrics error shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Inject error and then trigger a refresh to call listMetrics again
    await injectError(page, "TimeSeriesBinding.ListMetrics", "metrics unavailable");
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("metrics unavailable")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "TimeSeriesBinding.ListMetrics");
  });

  test("delete series error shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    // Write a metric first
    await writePointViaUI(page, "del-fail", "50.0", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "del-fail"')).toBeVisible({
      timeout: 5_000,
    });

    // Inject error for DeleteSeries
    await injectError(page, "TimeSeriesBinding.DeleteSeries", "delete not permitted");

    // Attempt delete
    const metricRow = page.locator("[role='button']", { hasText: "del-fail" }).first();
    const deleteBtn = metricRow.locator("[aria-label='Delete metric']");
    await deleteBtn.click({ force: true });

    await page.locator("dialog").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText("delete not permitted")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "TimeSeriesBinding.DeleteSeries");
  });

  test("query error shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    const tsNs = String(now * 1_000_000);

    await writePointViaUI(page, "query-fail", "25.0", { timestamp: tsNs });
    await expect(page.getByText('Data point written for "query-fail"')).toBeVisible({
      timeout: 5_000,
    });

    // Inject error for Query before clicking the metric
    await injectError(page, "TimeSeriesBinding.Query", "query timeout");

    // Click the metric row (use role=button locator to avoid toast text match)
    await page.locator("[role='button']", { hasText: "query-fail" }).first().click();

    await expect(page.getByText("query timeout")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "TimeSeriesBinding.Query");
  });
});
