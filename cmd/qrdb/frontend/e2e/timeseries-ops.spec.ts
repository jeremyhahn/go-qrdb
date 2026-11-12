/**
 * Copyright 2026 Jeremy Hahn
 * SPDX-License-Identifier: Apache-2.0
 *
 * Mocked e2e for the time series operations the TimeSeries view reaches
 * beyond list/query/delete: write, writeBatch, aggregate, richQuery,
 * getRetention and setRetention.
 *
 * The binding double below follows the Go TimeSeriesBinding: every request
 * arrives as one snake_case object (the SDK's Wails adapter runs toSnake),
 * and every response has the SDK's camelCase shape.
 */

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
    { name: "ts-ops", backend: "pebble", status: "open" },
  ],
};

/**
 * Inject a stateful in-memory time series store that replaces the default
 * TimeSeriesBinding mock. Records each binding method called in
 * window.__TS_CALLS__. Must be called before `page.goto()`.
 */
async function injectStatefulTimeSeriesBinding(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.addInitScript(() => {
    interface Tag {
      key: string;
      value: string;
    }
    interface Point {
      metric: string;
      value: number;
      timestampNs: number;
      tags: Tag[];
    }
    interface Policy {
      durationNs: number;
      resolutionNs?: number;
      aggregation?: number;
    }
    type Req = Record<string, unknown>;

    const tsPoints: Point[] = [];
    const retentionStore = new Map<string, Policy>();
    const w = window as unknown as Record<string, unknown>;
    const calls: string[] = [];
    w.__TS_CALLS__ = calls;

    function maybeReject(method: string): void {
      calls.push(method);
      const s = w.__QRDB_MOCK_STATE__ as { errors: Record<string, string> };
      if (s.errors[`TimeSeriesBinding.${method}`]) {
        throw new Error(s.errors[`TimeSeriesBinding.${method}`]);
      }
    }

    function toPoint(p: Req): Point {
      return {
        metric: p["metric"] as string,
        value: p["value"] as number,
        timestampNs: (p["timestamp_ns"] ?? Date.now() * 1_000_000) as number,
        tags: ((p["tags"] as Tag[] | undefined) ?? []).map((t) => ({ key: t.key, value: t.value })),
      };
    }

    function inRange(req: Req): Point[] {
      const startNs = req["start_ns"] as number;
      const endNs = req["end_ns"] as number;
      return tsPoints
        .filter((p) => p.metric === req["metric"] && p.timestampNs >= startNs && p.timestampNs <= endNs)
        .sort((a, b) => a.timestampNs - b.timestampNs);
    }

    // AggFunc: 0 None, 1 Avg, 2 Sum, 3 Min, 4 Max, 5 Count, 6 Percentile, 7 First, 8 Last.
    function apply(fn: number, values: number[], percentile = 50): number {
      if (values.length === 0) return 0;
      switch (fn) {
        case 1: return values.reduce((a, b) => a + b, 0) / values.length;
        case 2: return values.reduce((a, b) => a + b, 0);
        case 3: return Math.min(...values);
        case 4: return Math.max(...values);
        case 5: return values.length;
        case 6: {
          const sorted = [...values].sort((a, b) => a - b);
          const rank = Math.max(1, Math.ceil((percentile / 100) * sorted.length));
          return sorted[rank - 1] as number;
        }
        case 7: return values[0] as number;
        default: return values[values.length - 1] as number;
      }
    }

    function bucket(
      points: Point[],
      startNs: number,
      intervalNs: number,
      fn: number,
      percentile?: number,
    ): { timestampNs: number; value: number; count: number }[] {
      const buckets = new Map<number, number[]>();
      for (const p of points) {
        const key = intervalNs > 0 ? p.timestampNs - (p.timestampNs % intervalNs) : startNs;
        const vals = buckets.get(key) ?? [];
        vals.push(p.value);
        buckets.set(key, vals);
      }
      return [...buckets.entries()]
        .sort((a, b) => a[0] - b[0])
        .map(([ts, vals]) => ({ timestampNs: ts, value: apply(fn, vals, percentile), count: vals.length }));
    }

    const waitForGo = setInterval(() => {
      const go = w.go as Record<string, Record<string, Record<string, unknown>>> | undefined;
      if (!go?.main?.TimeSeriesBinding) return;
      clearInterval(waitForGo);

      go.main.TimeSeriesBinding = {
        Write: async (point: Req) => {
          maybeReject("Write");
          tsPoints.push(toPoint(point));
        },
        WriteBatch: async (points: Req[]) => {
          maybeReject("WriteBatch");
          for (const p of points) tsPoints.push(toPoint(p));
        },
        WritePoint: async (point: Req) => {
          maybeReject("WritePoint");
          tsPoints.push(toPoint(point));
        },
        Query: async (req: Req) => {
          maybeReject("Query");
          return { points: inRange(req) };
        },
        Aggregate: async (req: Req) => {
          maybeReject("Aggregate");
          return {
            results: bucket(
              inRange(req),
              req["start_ns"] as number,
              (req["interval_ns"] as number | undefined) ?? 0,
              req["function"] as number,
            ),
          };
        },
        RichQuery: async (req: Req) => {
          maybeReject("RichQuery");
          const filterTags = (req["tags"] as Tag[] | undefined) ?? [];
          let points = inRange(req).filter((p) =>
            filterTags.every((f) => p.tags.some((t) => t.key === f.key && t.value === f.value)),
          );
          const limit = req["limit"] as number | undefined;
          if (limit !== undefined && limit > 0) points = points.slice(0, limit);
          const fn = (req["aggregation"] as number | undefined) ?? 0;
          const percentile = req["percentile"] as number | undefined;
          const intervalNs = (req["interval_ns"] as number | undefined) ?? 0;
          const shape = (ps: Point[]): Point[] =>
            fn !== 0 && intervalNs > 0
              ? bucket(ps, req["start_ns"] as number, intervalNs, fn, percentile).map((b) => ({
                  metric: req["metric"] as string,
                  value: b.value,
                  timestampNs: b.timestampNs,
                  tags: [],
                }))
              : ps;
          const agg = (ps: Point[]): { aggregatedValue?: number } =>
            fn !== 0 ? { aggregatedValue: apply(fn, ps.map((p) => p.value), percentile) } : {};
          const groupBy = (req["group_by"] as string[] | undefined) ?? [];
          const resp: Record<string, unknown> = {
            metric: req["metric"],
            points: shape(points),
            ...agg(points),
          };
          if (groupBy.length > 0) {
            const groups = new Map<string, { tags: Tag[]; points: Point[] }>();
            for (const p of points) {
              const tags = groupBy.map((k) => ({ key: k, value: p.tags.find((t) => t.key === k)?.value ?? "" }));
              const id = JSON.stringify(tags);
              const g = groups.get(id) ?? { tags, points: [] };
              g.points.push(p);
              groups.set(id, g);
            }
            resp.groupedResults = [...groups.values()].map((g) => ({
              tags: g.tags,
              points: shape(g.points),
              ...agg(g.points),
            }));
          }
          return resp;
        },
        DeleteSeries: async (req: Req) => {
          maybeReject("DeleteSeries");
          let deleted = 0;
          for (let i = tsPoints.length - 1; i >= 0; i--) {
            if (tsPoints[i]!.metric === req["metric"]) {
              tsPoints.splice(i, 1);
              deleted++;
            }
          }
          retentionStore.delete(req["metric"] as string);
          return { deleted };
        },
        GetRetention: async (req: Req) => {
          maybeReject("GetRetention");
          return { policy: retentionStore.get(req["metric"] as string) ?? null };
        },
        SetRetention: async (req: Req) => {
          maybeReject("SetRetention");
          const p = req["policy"] as Req;
          retentionStore.set(req["metric"] as string, {
            durationNs: p["duration_ns"] as number,
            ...(p["resolution_ns"] !== undefined ? { resolutionNs: p["resolution_ns"] as number } : {}),
            ...(p["aggregation"] !== undefined ? { aggregation: p["aggregation"] as number } : {}),
          });
        },
        ListMetrics: async (req: Req) => {
          maybeReject("ListMetrics");
          let metrics = [...new Set(tsPoints.map((p) => p.metric))];
          const prefix = req["prefix"] as string | undefined;
          if (prefix) metrics = metrics.filter((m) => m.startsWith(prefix));
          return { metrics };
        },
      };
    }, 0);
  });
}

type Page = import("@playwright/test").Page;

/** Navigate to the timeseries database workspace. */
async function navigateToTimeSeries(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("ts-ops").click();
  await expect(page.locator(".breadcrumbs").getByText("ts-ops", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("tab", { name: "Time Series", exact: true }).click();
}

function writePanel(page: Page) {
  return page.getByRole("dialog", { name: "Write Data Point" });
}

/** Write one data point through the slide panel's single mode. */
async function writePointViaUI(page: Page, metric: string, value: string, timestamp?: string): Promise<void> {
  await page.getByRole("button", { name: "Write", exact: true }).first().click();
  await expect(writePanel(page)).toBeVisible();
  await writePanel(page).locator("input[placeholder='cpu.usage']").fill(metric);
  await writePanel(page).locator("input[placeholder='42.5']").fill(value);
  if (timestamp) {
    await writePanel(page).locator("input[placeholder='Leave empty for now']").fill(timestamp);
  }
  await writePanel(page).getByRole("button", { name: "Write" }).click();
}

/** Write `timestamp,value[,k=v;k=v]` lines through the slide panel's batch mode. */
async function writeBatchViaUI(page: Page, metric: string, lines: string[]): Promise<void> {
  await page.getByRole("button", { name: "Write", exact: true }).first().click();
  await expect(writePanel(page)).toBeVisible();
  await writePanel(page).getByTestId("ts-write-mode-batch").click();
  await writePanel(page).locator("input[placeholder='cpu.usage']").fill(metric);
  await writePanel(page).getByTestId("ts-write-batch").fill(lines.join("\n"));
  await writePanel(page).getByRole("button", { name: "Write" }).click();
  await expect(page.getByText(`${lines.length} data points written for "${metric}"`)).toBeVisible({
    timeout: 5_000,
  });
}

async function openMetric(page: Page, metric: string): Promise<void> {
  await page.locator("[role='button']", { hasText: metric }).first().click();
  await expect(page.getByRole("button", { name: "Back to Metrics" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

async function bindingCalls(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __TS_CALLS__: string[] }).__TS_CALLS__);
}

/** Exact nanosecond timestamp string for a millisecond instant. */
function ns(ms: number): string {
  return (BigInt(ms) * 1_000_000n).toString();
}

/** A minute-aligned instant 30 minutes ago, inside the detail view's default 1h range. */
function minuteBase(): number {
  const ms = Date.now() - 30 * 60_000;
  return ms - (ms % 60_000);
}

/** Points for the aggregate and rich query tests: two hosts, two minutes. */
function seriesLines(base: number): string[] {
  return [
    `${ns(base)},1,host=a`,
    `${ns(base + 10_000)},3,host=a`,
    `${ns(base + 70_000)},5,host=b`,
  ];
}

test.describe("TimeSeries operations", () => {
  test.use({ mockState: tsState });

  test("single write saves through write", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writePointViaUI(page, "ops.single", "12.5", ns(minuteBase()));
    await expect(page.getByText('Data point written for "ops.single"')).toBeVisible({ timeout: 5_000 });
    expect(await bindingCalls(page)).toContain("Write");
    expect(await bindingCalls(page)).not.toContain("WritePoint");

    await openMetric(page, "ops.single");
    await expect(page.getByText("1 data points")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("12.5").first()).toBeVisible();
  });

  test("batch write saves every line through writeBatch", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await writeBatchViaUI(page, "ops.batch", seriesLines(minuteBase()));
    expect(await bindingCalls(page)).toContain("WriteBatch");

    await openMetric(page, "ops.batch");
    await expect(page.getByText("3 data points", { exact: true })).toBeVisible({ timeout: 5_000 });
  });

  test("batch write rejects a malformed line", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);

    await page.getByRole("button", { name: "Write", exact: true }).first().click();
    await writePanel(page).getByTestId("ts-write-mode-batch").click();
    await writePanel(page).locator("input[placeholder='cpu.usage']").fill("ops.bad");
    await writePanel(page).getByTestId("ts-write-batch").fill(`${ns(minuteBase())},1\nnot-a-line`);
    await writePanel(page).getByRole("button", { name: "Write" }).click();
    await expect(page.getByText("Line 2: expected timestamp,value[,k=v;k=v].")).toBeVisible();
    expect(await bindingCalls(page)).not.toContain("WriteBatch");
  });

  test("aggregate buckets by interval and applies the function", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);
    await writeBatchViaUI(page, "ops.agg", seriesLines(minuteBase()));
    await openMetric(page, "ops.agg");

    const card = page.getByTestId("ts-aggregate-card");
    await card.getByTestId("ts-agg-function").selectOption({ label: "Sum" });
    await card.getByTestId("ts-agg-interval").fill("1m");
    await expect(card.getByText("1 minute", { exact: true })).toBeVisible();
    await card.getByTestId("ts-agg-run").click();

    const values = card.getByTestId("ts-agg-value");
    await expect(values).toHaveText(["4", "5"]);
    await expect(card.getByTestId("ts-agg-results").locator("tbody tr").first()).toContainText("2");

    await card.getByTestId("ts-agg-function").selectOption({ label: "Max" });
    await card.getByTestId("ts-agg-interval").fill("");
    await card.getByTestId("ts-agg-run").click();
    await expect(values).toHaveText(["5"]);
    expect(await bindingCalls(page)).toContain("Aggregate");
  });

  test("rich query filters by tag, aggregates and groups", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);
    await writeBatchViaUI(page, "ops.rich", seriesLines(minuteBase()));
    await openMetric(page, "ops.rich");

    const card = page.getByTestId("ts-rich-card");
    await expect(card.getByTestId("ts-rich-percentile")).toHaveCount(0);

    await card.getByTestId("ts-rich-tags").fill("host=a");
    await card.getByTestId("ts-rich-aggregation").selectOption({ label: "Sum" });
    await card.getByTestId("ts-rich-run").click();
    await expect(card.getByTestId("ts-rich-point-count")).toHaveText("2 points");
    await expect(card.getByTestId("ts-rich-aggregated")).toHaveText("4");

    await card.getByTestId("ts-rich-tags").fill("");
    await card.getByTestId("ts-rich-aggregation").selectOption({ label: "Max" });
    await card.getByTestId("ts-rich-groupby").fill("host");
    await card.getByTestId("ts-rich-run").click();
    const groups = card.getByTestId("ts-rich-group-row");
    await expect(groups).toHaveCount(2);
    await expect(groups.nth(0)).toContainText("host=a");
    await expect(groups.nth(0)).toContainText("3");
    await expect(groups.nth(1)).toContainText("host=b");
    await expect(groups.nth(1)).toContainText("5");

    await card.getByTestId("ts-rich-groupby").fill("");
    await card.getByTestId("ts-rich-limit").fill("2");
    await card.getByTestId("ts-rich-aggregation").selectOption({ label: "Percentile" });
    await card.getByTestId("ts-rich-percentile").fill("100");
    await card.getByTestId("ts-rich-run").click();
    await expect(card.getByTestId("ts-rich-point-count")).toHaveText("2 points");
    await expect(card.getByTestId("ts-rich-aggregated")).toHaveText("3");
    expect(await bindingCalls(page)).toContain("RichQuery");
  });

  test("retention reads none, sets a policy and re-reads it", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);
    await writePointViaUI(page, "ops.retention", "1", ns(minuteBase()));
    await expect(page.getByText('Data point written for "ops.retention"')).toBeVisible({ timeout: 5_000 });
    await openMetric(page, "ops.retention");

    const card = page.getByTestId("ts-retention-card");
    await expect(card.getByTestId("ts-retention-none")).toHaveText("Current policy: none");

    await card.getByTestId("ts-retention-duration-input").fill("2d");
    await card.getByTestId("ts-retention-resolution-input").fill("1h");
    await card.getByTestId("ts-retention-aggregation-input").selectOption({ label: "Avg" });
    await card.getByTestId("ts-retention-save").click();

    await expect(page.getByText('Retention set for "ops.retention".')).toBeVisible({ timeout: 5_000 });
    await expect(card.getByTestId("ts-retention-duration")).toHaveText("2 days");
    await expect(card.getByText("172800000000000 ns").first()).toBeVisible();
    await expect(card.getByTestId("ts-retention-resolution")).toHaveText("1 hour");
    await expect(card.getByTestId("ts-retention-aggregation")).toHaveText("Avg");

    const calls = await bindingCalls(page);
    expect(calls.filter((c) => c === "GetRetention").length).toBeGreaterThanOrEqual(2);
    expect(calls).toContain("SetRetention");
  });

  test("aggregate and set retention errors show error toasts", async ({ page }) => {
    await injectStatefulTimeSeriesBinding(page);
    await navigateToTimeSeries(page);
    await writePointViaUI(page, "ops.err", "1", ns(minuteBase()));
    await expect(page.getByText('Data point written for "ops.err"')).toBeVisible({ timeout: 5_000 });
    await openMetric(page, "ops.err");

    await injectError(page, "TimeSeriesBinding.Aggregate", "aggregate backend down");
    await page.getByTestId("ts-agg-run").click();
    await expect(page.getByText("aggregate backend down")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByTestId("ts-agg-results")).toHaveCount(0);
    await clearError(page, "TimeSeriesBinding.Aggregate");

    await injectError(page, "TimeSeriesBinding.SetRetention", "retention denied");
    await page.getByTestId("ts-retention-save").click();
    await expect(page.getByText("retention denied")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByTestId("ts-retention-none")).toBeVisible();
    await clearError(page, "TimeSeriesBinding.SetRetention");
  });
});
