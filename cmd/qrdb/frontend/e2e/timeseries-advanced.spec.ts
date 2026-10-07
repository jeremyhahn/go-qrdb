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
    { name: "ts-adv", backend: "pebble", status: "open" },
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

    const waitForGo = setInterval(() => {
      const w = window as unknown as Record<string, unknown>;
      const go = w.go as Record<string, Record<string, Record<string, unknown>>> | undefined;
      if (!go?.main?.TimeSeriesBinding) return;
      clearInterval(waitForGo);

      go.main.TimeSeriesBinding = {
        Write: async (metric: string, value: number, timestampNs: number) => {
          maybeReject("TimeSeriesBinding.Write");
          tsPoints.push({ metric, value, timestampNs });
        },
        WriteBatch: async (points: TSDataPoint[]) => {
          maybeReject("TimeSeriesBinding.WriteBatch");
          for (const p of points) {
            tsPoints.push({ ...p });
          }
        },
        WritePoint: async (point: Record<string, unknown>) => {
          maybeReject("TimeSeriesBinding.WritePoint");
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
        Aggregate: async () => {
          maybeReject("TimeSeriesBinding.Aggregate");
          return { result: 0 };
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
async function navigateToTimeSeries(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("ts-adv").click();
  await expect(page.locator(".breadcrumbs").getByText("ts-adv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("tab", { name: "Time Series", exact: true }).click();
}

/** Write a data point through the UI slide panel. */
async function writePointViaUI(
  page: import("@playwright/test").Page,
  metric: string,
  value: string,
  opts?: { timestamp?: string; tags?: string },
): Promise<void> {
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

  await writePanel.getByRole("button", { name: "Write" }).click();
}

// ---------------------------------------------------------------------------
// TimeSeries Advanced tests
// ---------------------------------------------------------------------------

test.describe("TimeSeries Advanced", () => {
  test.use({ mockState: tsState });

  test("navigate into metric detail view and back", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "cpu.nav-test", "50.0");
    await expect(page.getByText('Data point written for "cpu.nav-test"')).toBeVisible({
      timeout: 5_000,
    });

    await page.locator("[role='button']", { hasText: "cpu.nav-test" }).first().click();
    await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await page.getByRole("button", { name: "Back to Metrics" }).click();
    await expect(page.getByText("1 metrics")).toBeVisible();
  });

  test("delete series from list removes metric", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "disk.delete-test", "100.0");
    await expect(page.getByText('Data point written for "disk.delete-test"')).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.locator(".font-mono", { hasText: "disk.delete-test" }).first()).toBeVisible();

    const metricRow = page.locator("[role='button']", { hasText: "disk.delete-test" }).first();
    const deleteBtn = metricRow.locator("[aria-label='Delete metric']");
    await deleteBtn.click({ force: true });

    await expect(page.getByText("Delete Metric")).toBeVisible();
    await page.locator("dialog").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText('Deleted metric "disk.delete-test"')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("0 metrics")).toBeVisible();
    await expect(page.locator(".font-mono", { hasText: "disk.delete-test" })).not.toBeVisible();
  });

  test("list metrics shows all written series", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    const now = Date.now();
    await writePointViaUI(page, "list.alpha", "1.0", { timestamp: String(now * 1_000_000) });
    await expect(page.getByText('Data point written for "list.alpha"')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("dialog", { name: "Write Data Point" })).not.toBeVisible();

    await writePointViaUI(page, "list.beta", "2.0", { timestamp: String(now * 1_000_000) });
    await expect(page.getByText('Data point written for "list.beta"')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("dialog", { name: "Write Data Point" })).not.toBeVisible();

    await writePointViaUI(page, "list.gamma", "3.0", { timestamp: String(now * 1_000_000) });
    await expect(page.getByText('Data point written for "list.gamma"')).toBeVisible({ timeout: 5_000 });

    await expect(page.getByText("3 metrics")).toBeVisible();
    await expect(page.locator(".font-mono", { hasText: "list.alpha" }).first()).toBeVisible();
    await expect(page.locator(".font-mono", { hasText: "list.beta" }).first()).toBeVisible();
    await expect(page.locator(".font-mono", { hasText: "list.gamma" }).first()).toBeVisible();
  });

  test("error on list metrics shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await injectError(page, "TimeSeriesBinding.ListMetrics", "metrics store unavailable");
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(page.getByText("metrics store unavailable")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "TimeSeriesBinding.ListMetrics");
  });

  test("error on write shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await injectError(page, "TimeSeriesBinding.WritePoint", "write quota exceeded");

    await writePointViaUI(page, "err.metric", "1.0");
    await expect(page.getByText("write quota exceeded")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "TimeSeriesBinding.WritePoint");
  });

  test("error on delete series shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "del.err-metric", "5.0");
    await expect(page.getByText('Data point written for "del.err-metric"')).toBeVisible({
      timeout: 5_000,
    });

    await injectError(page, "TimeSeriesBinding.DeleteSeries", "delete operation failed");

    const metricRow = page.locator("[role='button']", { hasText: "del.err-metric" }).first();
    const deleteBtn = metricRow.locator("[aria-label='Delete metric']");
    await deleteBtn.click({ force: true });

    await page.locator("dialog").getByRole("button", { name: "Delete" }).click();

    await expect(page.getByText("delete operation failed")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "TimeSeriesBinding.DeleteSeries");
  });

  test("query data points for a metric in detail view", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "query.adv-metric", "77.0");
    await expect(page.getByText('Data point written for "query.adv-metric"')).toBeVisible({
      timeout: 5_000,
    });

    await page.locator("[role='button']", { hasText: "query.adv-metric" }).first().click();
    await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await expect(page.getByText("1 data points")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("77")).toBeVisible();
  });

  test("error on query shows error toast", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "query.err-metric", "5.0");
    await expect(page.getByText('Data point written for "query.err-metric"')).toBeVisible({
      timeout: 5_000,
    });

    await injectError(page, "TimeSeriesBinding.Query", "query timeout");

    await page.locator("[role='button']", { hasText: "query.err-metric" }).first().click();

    await expect(page.getByText("query timeout")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "TimeSeriesBinding.Query");
  });
});
