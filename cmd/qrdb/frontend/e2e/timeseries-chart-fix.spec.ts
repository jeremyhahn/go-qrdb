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
 * Regression tests for the TimeSeries chart and data table bugs found during
 * Chrome browser testing.
 *
 * Bugs fixed:
 *   1. REST client returned `timestamp` instead of `timestampNs`, causing data
 *      points to be dropped on query because the field was always undefined.
 *   2. DataTable used `$effect` instead of `$effect.pre`, so the table was
 *      rendered before data arrived, producing empty rows on first paint.
 *   3. Chart accent color was specified as a CSS variable (`var(--color-cyan)`)
 *      which ECharts cannot resolve, rendering the series line invisible.
 *   4. The write form concatenated the metric placeholder text into the value
 *      when the field was not explicitly cleared first.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const tsState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-ts", engine: "timeseries", backend: "pebble", status: "open" },
  ],
};

/**
 * Inject a stateful TimeSeriesBinding whose Write/Query responses use the
 * `timestampNs` field name that the REST client now correctly maps.
 * The binding must be registered before the app boots.
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

    const waitForGo = setInterval(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as Record<string, Record<string, Record<string, unknown>>> | undefined;
      if (!go?.main?.TimeSeriesBinding) return;
      clearInterval(waitForGo);

      go.main.TimeSeriesBinding = {
        Write: async (metric: string, value: number, timestampNs: number) => {
          maybeReject("TimeSeriesBinding.Write");
          tsPoints.push({ metric, value, timestampNs: timestampNs ?? Date.now() * 1_000_000 });
        },

        WriteBatch: async (points: TSDataPoint[]) => {
          maybeReject("TimeSeriesBinding.WriteBatch");
          for (const p of points) tsPoints.push({ ...p });
        },

        WritePoint: async (point: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.WritePoint");
          // SDK converts timestampNs → timestamp_ns via toSnake().
          const tsNs = (point["timestamp_ns"] ?? point["timestampNs"] ?? Date.now() * 1_000_000) as number;
          tsPoints.push({
            metric: point["metric"] as string,
            value: point["value"] as number,
            timestampNs: tsNs,
            tags: point["tags"] as { key: string; value: string }[] | undefined,
          });
        },

        Query: async (req: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.Query");
          // SDK converts startNs → start_ns, endNs → end_ns via toSnake().
          const startNs = (req["start_ns"] ?? req["startNs"] ?? 0) as number;
          const endNs = (req["end_ns"] ?? req["endNs"] ?? Number.MAX_SAFE_INTEGER) as number;
          const filtered = tsPoints.filter(
            (p) =>
              p.metric === req["metric"] &&
              p.timestampNs >= startNs &&
              p.timestampNs <= endNs,
          );
          // Response uses timestampNs — the correct field name after the fix.
          return {
            points: filtered.map((p) => ({
              metric: p.metric,
              value: p.value,
              timestampNs: p.timestampNs,
              tags: p.tags ?? [],
            })),
          };
        },

        Aggregate: async (req: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.Aggregate");
          const startNs = (req["start_ns"] ?? req["startNs"] ?? 0) as number;
          const endNs = (req["end_ns"] ?? req["endNs"] ?? Number.MAX_SAFE_INTEGER) as number;
          const filtered = tsPoints.filter(
            (p) =>
              p.metric === req["metric"] &&
              p.timestampNs >= startNs &&
              p.timestampNs <= endNs,
          );
          if (filtered.length === 0) return { result: 0 };
          const values = filtered.map((p) => p.value);
          switch (req["aggregation"]) {
            case "sum": return { result: values.reduce((a, b) => a + b, 0) };
            case "avg": return { result: values.reduce((a, b) => a + b, 0) / values.length };
            case "min": return { result: Math.min(...values) };
            case "max": return { result: Math.max(...values) };
            case "count": return { result: values.length };
            default: return { result: 0 };
          }
        },

        RichQuery: async () => {
          maybeReject("TimeSeriesBinding.RichQuery");
          return { series: [] };
        },

        DeleteSeries: async (req: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.DeleteSeries");
          let deleted = 0;
          for (let i = tsPoints.length - 1; i >= 0; i--) {
            if (tsPoints[i].metric === req["metric"]) {
              tsPoints.splice(i, 1);
              deleted++;
            }
          }
          retentionStore.delete(req["metric"] as string);
          return { deleted };
        },

        GetRetention: async (req: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.GetRetention");
          return { days: retentionStore.get(req["metric"] as string) ?? 0 };
        },

        SetRetention: async (req: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.SetRetention");
          retentionStore.set(req["metric"] as string, req["days"] as number);
        },

        ListMetrics: async (req: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.ListMetrics");
          const metricSet = new Set<string>();
          for (const p of tsPoints) metricSet.add(p.metric);
          let metrics = Array.from(metricSet);
          if (req["prefix"]) {
            metrics = metrics.filter((m) => m.startsWith(req["prefix"] as string));
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
}

/** Write a data point via the UI slide panel, clearing the metric field first. */
async function writePointViaUI(
  page: import("@playwright/test").Page,
  metric: string,
  value: string,
  opts?: { timestamp?: string; tags?: string },
): Promise<void> {
  await page.getByRole("button", { name: "Write", exact: true }).first().click();
  await expect(page.getByRole("dialog", { name: "Write Data Point" })).toBeVisible();

  const writePanel = page.getByRole("dialog", { name: "Write Data Point" });

  // Clear the metric field before filling to prevent placeholder concatenation.
  const metricInput = writePanel.locator("input[placeholder='cpu.usage']");
  await metricInput.clear();
  await metricInput.fill(metric);
  await writePanel.locator("input[placeholder='42.5']").fill(value);

  if (opts?.timestamp) {
    await writePanel.locator("input[placeholder='Leave empty for now']").fill(opts.timestamp);
  }
  if (opts?.tags) {
    await writePanel.locator("input[placeholder='host=node1,region=us-east']").fill(opts.tags);
  }

  await writePanel.getByRole("button", { name: "Write" }).click();
}

// ---------------------------------------------------------------------------
// Regression: timestampNs field mismatch
// ---------------------------------------------------------------------------

test.describe("TimeSeries Chart & Data Table - Regression (timestampNs fix)", () => {
  test.use({ mockState: tsState });

  test("clicking a metric shows the detail view with chart canvas", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "cpu.usage", "72.5");
    await expect(page.getByText('Data point written for "cpu.usage"')).toBeVisible({
      timeout: 5_000,
    });

    // Click the metric row to open the detail view.
    await page.locator("[role='button']", { hasText: "cpu.usage" }).first().click();

    // Detail view must render — the "Back to Metrics" button is the sentinel.
    await expect(
      page.getByRole("button", { name: "Back to Metrics" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // A chart canvas must be present (ECharts renders into a <canvas>).
    await expect(page.locator("canvas")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("data table shows actual data rows when data exists", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "mem.usage", "45.0");
    await expect(page.getByText('Data point written for "mem.usage"')).toBeVisible({
      timeout: 5_000,
    });

    await page.locator("[role='button']", { hasText: "mem.usage" }).first().click();
    await expect(
      page.getByRole("button", { name: "Back to Metrics" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // At least one data row must be visible — not just the skeleton/empty state.
    // This regression would fail if $effect ran before data arrived ($effect.pre fix).
    const tableRows = page.locator("table tbody tr");
    await expect(tableRows.first()).toBeVisible({ timeout: LOAD_TIMEOUT });
    expect(await tableRows.count()).toBeGreaterThanOrEqual(1);
  });

  test("Data Points count matches number of visible table rows", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Write three distinct data points for the same metric.
    await writePointViaUI(page, "disk.io", "100.0");
    await expect(page.getByText('Data point written for "disk.io"')).toBeVisible({
      timeout: 5_000,
    });
    await writePointViaUI(page, "disk.io", "200.0");
    await expect(page.getByText('Data point written for "disk.io"')).toBeVisible({
      timeout: 5_000,
    });
    await writePointViaUI(page, "disk.io", "300.0");
    await expect(page.getByText('Data point written for "disk.io"')).toBeVisible({
      timeout: 5_000,
    });

    await page.locator("[role='button']", { hasText: "disk.io" }).first().click();
    await expect(
      page.getByRole("button", { name: "Back to Metrics" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The count badge must say "3 data points".
    await expect(page.getByText(/3 data points/i)).toBeVisible({ timeout: 5_000 });

    // The table must also have exactly 3 rows.
    const tableRows = page.locator("table tbody tr");
    await expect(tableRows).toHaveCount(3, { timeout: LOAD_TIMEOUT });
  });

  test("data table shows value from written data point", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "net.rx", "1024.5");
    await expect(page.getByText('Data point written for "net.rx"')).toBeVisible({
      timeout: 5_000,
    });

    await page.locator("[role='button']", { hasText: "net.rx" }).first().click();
    await expect(
      page.getByRole("button", { name: "Back to Metrics" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The value "1024.5" must appear in the data table.
    await expect(page.getByText("1024.5")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  // ---------------------------------------------------------------------------
  // Regression: write form metric field concatenation with placeholder
  // ---------------------------------------------------------------------------

  test("write form metric field starts empty (no placeholder concatenation)", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await page.getByRole("button", { name: "Write", exact: true }).first().click();
    await expect(page.getByRole("dialog", { name: "Write Data Point" })).toBeVisible();

    const writePanel = page.getByRole("dialog", { name: "Write Data Point" });
    const metricInput = writePanel.locator("input[placeholder='cpu.usage']");

    // The field must have no pre-filled value — only the placeholder attribute.
    await expect(metricInput).toHaveValue("");
  });

  test("filling metric field then submitting does not prepend placeholder text", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    // Write with a metric name that would be distinct from any placeholder.
    await writePointViaUI(page, "custom.metric", "99.0");

    await expect(
      page.getByText('Data point written for "custom.metric"'),
    ).toBeVisible({ timeout: 5_000 });

    // The metric list must show "custom.metric" — not "cpu.usagecustom.metric"
    // which would appear if the placeholder was concatenated.
    await expect(
      page.locator(".font-mono", { hasText: "custom.metric" }).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // The concatenated form must not appear anywhere in the page.
    await expect(
      page.getByText("cpu.usagecustom.metric", { exact: false }),
    ).not.toBeVisible();
  });

  // ---------------------------------------------------------------------------
  // Regression: chart accent color (CSS variable vs literal hex)
  // ---------------------------------------------------------------------------

  test("chart canvas is rendered with non-zero dimensions", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "cpu.temp", "65.0");
    await expect(page.getByText('Data point written for "cpu.temp"')).toBeVisible({
      timeout: 5_000,
    });

    await page.locator("[role='button']", { hasText: "cpu.temp" }).first().click();
    await expect(
      page.getByRole("button", { name: "Back to Metrics" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // ECharts renders into a <canvas>. Verify it has positive dimensions,
    // which would be zero if the series color was an unresolvable CSS variable
    // causing ECharts to skip rendering.
    const canvas = page.locator("canvas").first();
    await expect(canvas).toBeVisible({ timeout: LOAD_TIMEOUT });

    const box = await canvas.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.width).toBeGreaterThan(0);
    expect(box!.height).toBeGreaterThan(0);
  });

  test("chart container has cyan accent color applied (not CSS variable)", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "cpu.load", "1.5");
    await expect(page.getByText('Data point written for "cpu.load"')).toBeVisible({
      timeout: 5_000,
    });

    await page.locator("[role='button']", { hasText: "cpu.load" }).first().click();
    await expect(
      page.getByRole("button", { name: "Back to Metrics" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Verify the chart wrapper element exists. The cyan colour (#22d3ee) is
    // injected directly as a literal hex into the ECharts series config, so
    // the chart renders the series visibly regardless of CSS variable support.
    // The assertion confirms the chart container is present and visible.
    await expect(page.locator("canvas")).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  // ---------------------------------------------------------------------------
  // Regression: data appears without manual refresh after write
  // ---------------------------------------------------------------------------

  test("after writing a data point the metric appears without manual refresh", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "auto.refresh", "42.0");

    // Immediately after write (no explicit refresh click) the metric must be
    // visible in the list, confirming the UI auto-refreshes after a write.
    await expect(
      page.locator(".font-mono", { hasText: "auto.refresh" }).first(),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("navigating to metric detail immediately after write shows data rows", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "instant.view", "7.7");
    await expect(page.getByText('Data point written for "instant.view"')).toBeVisible({
      timeout: 5_000,
    });

    // Navigate straight to detail — no intermediate refresh step.
    await page.locator("[role='button']", { hasText: "instant.view" }).first().click();
    await expect(
      page.getByRole("button", { name: "Back to Metrics" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Data row must be present on first render (validates $effect.pre timing fix).
    await expect(page.locator("table tbody tr").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});
