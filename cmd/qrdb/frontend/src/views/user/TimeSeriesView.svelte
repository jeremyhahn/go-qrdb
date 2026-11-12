<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import type { Component } from "svelte";
  import type {
    AggFunc,
    Client,
    TSDataPoint,
    TSTag,
  } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { showToast } from "../../stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import EmptyState from "../../components/EmptyState.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import SlidePanel from "../../components/core/SlidePanel.svelte";
  import TimeSeriesChart from "../../components/core/TimeSeriesChart.svelte";
  import {
    Plus,
    Trash2,
    RefreshCw,
    Search,
    TrendingUp,
    LineChart,
    AreaChart,
    ScatterChart as ScatterChartIcon,
  } from "lucide-svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  interface TimeSeriesRow {
    metric: string;
    timestampNs: number;
    value: number;
    tags: readonly TSTag[];
  }

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let metrics: string[] = $state([]);
  let loading: boolean = $state(false);
  let prefixFilter: string = $state("");

  // Selected metric and its data points
  let selectedMetric: string | null = $state(null);
  let dataPoints: TimeSeriesRow[] = $state([]);
  let pointsLoading: boolean = $state(false);

  // Date range state (milliseconds)
  let rangeStartMs: number = $state(Date.now() - 3600_000);
  let rangeEndMs: number = $state(Date.now());
  let rangeStartInput: string = $state(toDateTimeLocal(Date.now() - 3600_000));
  let rangeEndInput: string = $state(toDateTimeLocal(Date.now()));

  // Chart controls
  let showChart: boolean = $state(true);
  let chartType: "line" | "area" | "scatter" = $state("line");

  // Write SlidePanel
  let showWritePanel: boolean = $state(false);
  let writeMetric: string = $state("");
  let writeValue: string = $state("");
  let writeTimestamp: string = $state("");
  let writeTags: string = $state("");
  let writeLoading: boolean = $state(false);
  let writeMode: "single" | "batch" = $state("single");
  let writeBatchText: string = $state("");

  // Delete confirmation
  let deleteTarget: string | null = $state(null);

  // Detail panel for a single data point
  let selectedPoint: TimeSeriesRow | null = $state(null);

  // AggFunc names, indexed by their numeric value (SDK AggFunc 0..8).
  const AGG_FUNC_NAMES = [
    "None",
    "Avg",
    "Sum",
    "Min",
    "Max",
    "Count",
    "Percentile",
    "First",
    "Last",
  ] as const;

  // Aggregate card
  let aggFunction: number = $state(1);
  let aggInterval: string = $state("1m");
  let aggResults: { timestampNs: number; value: number; count: number }[] = $state([]);
  let aggLoading: boolean = $state(false);
  let aggRan: boolean = $state(false);

  // Rich query card
  interface RichGroup {
    tags: readonly TSTag[];
    points: readonly TSDataPoint[];
    aggregatedValue?: number;
  }
  interface RichResult {
    points: readonly TSDataPoint[];
    aggregatedValue?: number;
    groupedResults: RichGroup[];
  }
  let rqTags: string = $state("");
  let rqAggregation: number = $state(0);
  let rqInterval: string = $state("");
  let rqGroupBy: string = $state("");
  let rqLimit: string = $state("");
  let rqPercentile: string = $state("95");
  let rqResult: RichResult | null = $state(null);
  let rqLoading: boolean = $state(false);

  // Retention card
  interface RetentionView {
    durationNs: number;
    resolutionNs?: number;
    aggregation?: number;
  }
  let retention: RetentionView | null = $state(null);
  let retentionLoaded: boolean = $state(false);
  let retentionLoading: boolean = $state(false);
  let retDuration: string = $state("7d");
  let retResolution: string = $state("");
  let retAggregation: number = $state(0);

  const aggChartData = $derived(
    aggResults.map((b) => ({
      timestamp: Math.round(b.timestampNs / 1_000_000),
      value: b.value,
      metric: `${selectedMetric ?? ""} ${AGG_FUNC_NAMES[aggFunction] ?? ""}`,
    }))
  );

  // ---------------------------------------------------------------------------
  // Column Definitions
  // ---------------------------------------------------------------------------

  const pointColumns: Column<TimeSeriesRow>[] = [
    {
      id: "metric",
      header: "Metric",
      accessor: "metric",
      sortable: true,
      filterable: true,
      filterType: "text",
      width: "25%",
    },
    {
      id: "timestamp",
      header: "Timestamp",
      accessor: (row) => formatTimestamp(row.timestampNs / 1_000_000),
      sortable: true,
      width: "30%",
    },
    {
      id: "value",
      header: "Value",
      accessor: "value",
      sortable: true,
      align: "right",
      width: "20%",
    },
    {
      id: "tags",
      header: "Tags",
      accessor: (row) => formatTags(row.tags),
      sortable: false,
      width: "25%",
    },
  ];

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const chartData = $derived(
    dataPoints.map((dp) => ({
      timestamp: Math.round(dp.timestampNs / 1_000_000),
      value: dp.value,
      metric: dp.metric,
    }))
  );

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function formatTimestamp(ms: number): string {
    return new Date(ms).toLocaleString();
  }

  function formatTags(tags: readonly TSTag[]): string {
    if (!tags || tags.length === 0) return "";
    return tags.map((t) => `${t.key}=${t.value}`).join(", ");
  }

  function toDateTimeLocal(ms: number): string {
    const d = new Date(ms);
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  function fromDateTimeLocal(s: string): number {
    return new Date(s).getTime();
  }

  function setPresetRange(durationMs: number): void {
    const now = Date.now();
    rangeStartMs = now - durationMs;
    rangeEndMs = now;
    rangeStartInput = toDateTimeLocal(rangeStartMs);
    rangeEndInput = toDateTimeLocal(rangeEndMs);
    if (selectedMetric !== null) {
      loadDataPoints(selectedMetric);
    }
  }

  function applyCustomRange(): void {
    rangeStartMs = fromDateTimeLocal(rangeStartInput);
    rangeEndMs = fromDateTimeLocal(rangeEndInput);
    if (selectedMetric !== null) {
      loadDataPoints(selectedMetric);
    }
  }

  function parseTags(raw: string): TSTag[] {
    const tags: TSTag[] = [];
    if (raw.trim() === "") return tags;
    const pairs = raw.split(",");
    for (const pair of pairs) {
      const idx = pair.indexOf("=");
      if (idx > 0) {
        const k = pair.substring(0, idx).trim();
        const v = pair.substring(idx + 1).trim();
        if (k !== "") {
          tags.push({ key: k, value: v });
        }
      }
    }
    return tags;
  }

  const DURATION_UNITS: { suffix: string; label: string; ns: number }[] = [
    { suffix: "d", label: "days", ns: 86_400_000_000_000 },
    { suffix: "h", label: "hours", ns: 3_600_000_000_000 },
    { suffix: "m", label: "minutes", ns: 60_000_000_000 },
    { suffix: "s", label: "seconds", ns: 1_000_000_000 },
    { suffix: "ms", label: "milliseconds", ns: 1_000_000 },
  ];

  /**
   * Parses a duration such as "90s", "5m", "2h", "7d", "250ms" or a bare
   * number of nanoseconds. Returns null when empty or invalid.
   */
  function parseDurationNs(raw: string): number | null {
    const s = raw.trim();
    if (s === "") return null;
    const m = /^(\d+(?:\.\d+)?)\s*(ns|ms|s|m|h|d)?$/.exec(s);
    if (m === null) return null;
    const n = parseFloat(m[1] ?? "");
    const unit = m[2] ?? "ns";
    if (unit === "ns") return Math.round(n);
    const u = DURATION_UNITS.find((d) => d.suffix === unit);
    return u ? Math.round(n * u.ns) : null;
  }

  /** Renders nanoseconds in the largest whole human unit ("2 hours"). */
  function formatDurationNs(ns: number): string {
    if (ns <= 0) return "0";
    for (const u of DURATION_UNITS) {
      if (ns >= u.ns) {
        const v = ns / u.ns;
        const shown = Number.isInteger(v) ? String(v) : v.toFixed(2);
        return `${shown} ${v === 1 ? u.label.slice(0, -1) : u.label}`;
      }
    }
    return `${ns} ns`;
  }

  function errMessage(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function loadRetention(metric: string): Promise<void> {
    retentionLoading = true;
    retentionLoaded = false;
    try {
      const resp = await client.timeSeries!.getRetention({ metric });
      const p = resp?.policy ?? null;
      retention = p === null
        ? null
        : {
            durationNs: p.durationNs,
            ...(p.resolutionNs !== undefined ? { resolutionNs: p.resolutionNs } : {}),
            ...(p.aggregation !== undefined ? { aggregation: p.aggregation as number } : {}),
          };
      retentionLoaded = true;
    } catch (err: unknown) {
      showToast("error", errMessage(err));
      retention = null;
    } finally {
      retentionLoading = false;
    }
  }

  async function saveRetention(): Promise<void> {
    if (selectedMetric === null) return;
    const durationNs = parseDurationNs(retDuration);
    if (durationNs === null || durationNs <= 0) {
      showToast("warning", "Duration must be a positive duration like 12h or 30d.");
      return;
    }
    const resolutionNs = parseDurationNs(retResolution);
    if (retResolution.trim() !== "" && resolutionNs === null) {
      showToast("warning", "Resolution must be a duration like 1m or 1h.");
      return;
    }
    const metric = selectedMetric;
    retentionLoading = true;
    try {
      await client.timeSeries!.setRetention({
        metric,
        policy: {
          durationNs,
          ...(resolutionNs !== null ? { resolutionNs } : {}),
          ...(retAggregation !== 0 ? { aggregation: retAggregation as AggFunc } : {}),
        },
      });
      showToast("success", `Retention set for "${metric}".`);
    } catch (err: unknown) {
      showToast("error", errMessage(err));
      retentionLoading = false;
      return;
    }
    await loadRetention(metric);
  }

  function resetMetricCards(): void {
    aggResults = [];
    aggRan = false;
    rqResult = null;
    retention = null;
    retentionLoaded = false;
  }

  async function runRichQuery(): Promise<void> {
    if (selectedMetric === null) return;
    const intervalNs = parseDurationNs(rqInterval);
    if (rqInterval.trim() !== "" && intervalNs === null) {
      showToast("warning", "Interval must be a duration like 30s, 5m, 1h or 1d.");
      return;
    }
    const limit = rqLimit.trim() === "" ? null : parseInt(rqLimit, 10);
    if (limit !== null && (Number.isNaN(limit) || limit < 0)) {
      showToast("warning", "Limit must be a non-negative integer.");
      return;
    }
    const percentile = rqAggregation === 6 ? parseFloat(rqPercentile) : null;
    if (percentile !== null && (Number.isNaN(percentile) || percentile < 0 || percentile > 100)) {
      showToast("warning", "Percentile must be between 0 and 100.");
      return;
    }
    const tags = parseTags(rqTags);
    const groupBy = rqGroupBy.split(",").map((s) => s.trim()).filter((s) => s !== "");
    rqLoading = true;
    try {
      const resp = await client.timeSeries!.richQuery({
        metric: selectedMetric,
        startNs: rangeStartMs * 1_000_000,
        endNs: rangeEndMs * 1_000_000,
        ...(tags.length > 0 ? { tags } : {}),
        ...(rqAggregation !== 0 ? { aggregation: rqAggregation as AggFunc } : {}),
        ...(intervalNs !== null ? { intervalNs } : {}),
        ...(groupBy.length > 0 ? { groupBy } : {}),
        ...(limit !== null ? { limit } : {}),
        ...(percentile !== null ? { percentile } : {}),
      });
      rqResult = {
        points: resp?.points ?? [],
        ...(resp?.aggregatedValue !== undefined ? { aggregatedValue: resp.aggregatedValue } : {}),
        groupedResults: (resp?.groupedResults ?? []).map((g) => ({
          tags: g.tags ?? [],
          points: g.points ?? [],
          ...(g.aggregatedValue !== undefined ? { aggregatedValue: g.aggregatedValue } : {}),
        })),
      };
    } catch (err: unknown) {
      showToast("error", errMessage(err));
      rqResult = null;
    } finally {
      rqLoading = false;
    }
  }

  async function runAggregate(): Promise<void> {
    if (selectedMetric === null) return;
    const intervalNs = parseDurationNs(aggInterval);
    if (aggInterval.trim() !== "" && intervalNs === null) {
      showToast("warning", "Interval must be a duration like 30s, 5m, 1h or 1d.");
      return;
    }
    aggLoading = true;
    try {
      const resp = await client.timeSeries!.aggregate({
        metric: selectedMetric,
        startNs: rangeStartMs * 1_000_000,
        endNs: rangeEndMs * 1_000_000,
        function: aggFunction as AggFunc,
        ...(intervalNs !== null ? { intervalNs } : {}),
      });
      aggResults = (resp?.results ?? []).map((r) => ({
        timestampNs: r.timestampNs,
        value: r.value,
        count: r.count,
      }));
      aggRan = true;
    } catch (err: unknown) {
      showToast("error", errMessage(err));
      aggResults = [];
    } finally {
      aggLoading = false;
    }
  }

  async function loadMetrics(): Promise<void> {

    loading = true;
    try {
      const resp = await client.timeSeries!.listMetrics({ prefix: prefixFilter });
      metrics = resp?.metrics ? [...resp.metrics] : [];
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      metrics = [];
    } finally {
      loading = false;
    }
  }

  async function selectMetric(metric: string): Promise<void> {
    const now = Date.now();
    rangeStartMs = now - 3600_000;
    rangeEndMs = now;
    rangeStartInput = toDateTimeLocal(rangeStartMs);
    rangeEndInput = toDateTimeLocal(rangeEndMs);
    // Switch to the detail view first, then load data. This ensures the
    // view transitions immediately on click rather than waiting for the
    // async query to complete.
    selectedMetric = metric;
    resetMetricCards();
    await Promise.all([loadDataPoints(metric), loadRetention(metric)]);
  }

  async function loadDataPoints(metric: string): Promise<void> {

    pointsLoading = true;
    try {
      const resp = await client.timeSeries!.query({
        metric,
        startNs: rangeStartMs * 1_000_000,
        endNs: rangeEndMs * 1_000_000,
      });
      const points = resp?.points ?? [];
      dataPoints = points.map((dp) => ({
        metric: dp.metric,
        timestampNs: dp.timestampNs,
        value: dp.value,
        tags: dp.tags ?? [],
      }));
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      dataPoints = [];
    } finally {
      pointsLoading = false;
    }
  }

  function backToMetrics(): void {
    selectedMetric = null;
    dataPoints = [];
    selectedPoint = null;
    resetMetricCards();
  }

  function openWritePanel(): void {
    writeMetric = "";
    writeValue = "";
    writeTimestamp = "";
    writeTags = "";
    writeMode = "single";
    writeBatchText = "";
    showWritePanel = true;
  }

  /**
   * Parses batch lines of the form `timestamp,value[,k=v;k=v]`. Returns the
   * points or an error message naming the first bad line.
   */
  function parseBatch(metric: string, raw: string): TSDataPoint[] | string {
    const points: TSDataPoint[] = [];
    const lines = raw.split("\n");
    for (let i = 0; i < lines.length; i++) {
      const line = (lines[i] ?? "").trim();
      if (line === "") continue;
      const parts = line.split(",");
      if (parts.length < 2 || parts.length > 3) {
        return `Line ${i + 1}: expected timestamp,value[,k=v;k=v].`;
      }
      const tsRaw = (parts[0] ?? "").trim();
      if (!/^\d+$/.test(tsRaw)) return `Line ${i + 1}: timestamp must be Unix nanoseconds.`;
      const value = parseFloat((parts[1] ?? "").trim());
      if (Number.isNaN(value)) return `Line ${i + 1}: value must be a valid number.`;
      const tags = parts.length === 3 ? parseTags((parts[2] ?? "").replaceAll(";", ",")) : [];
      points.push({
        metric,
        value,
        timestampNs: parseInt(tsRaw, 10),
        ...(tags.length > 0 ? { tags } : {}),
      });
    }
    if (points.length === 0) return "Enter at least one line.";
    return points;
  }

  async function handleWriteBatch(): Promise<void> {
    if (writeMetric.trim() === "") {
      showToast("warning", "Metric is required.");
      return;
    }
    const parsed = parseBatch(writeMetric.trim(), writeBatchText);
    if (typeof parsed === "string") {
      showToast("warning", parsed);
      return;
    }
    writeLoading = true;
    try {
      await client.timeSeries!.writeBatch(parsed);
      showToast("success", `${parsed.length} data points written for "${writeMetric}".`);
      showWritePanel = false;
      await loadMetrics();
      if (selectedMetric !== null) {
        await loadDataPoints(selectedMetric);
      }
    } catch (err: unknown) {
      showToast("error", errMessage(err));
    } finally {
      writeLoading = false;
    }
  }

  async function handleWrite(): Promise<void> {

    if (writeMetric.trim() === "" || writeValue.trim() === "") {
      showToast("warning", "Metric and value are required.");
      return;
    }
    const val = parseFloat(writeValue);
    if (Number.isNaN(val)) {
      showToast("warning", "Value must be a valid number.");
      return;
    }

    writeLoading = true;
    try {
      const ts = writeTimestamp.trim() !== ""
        ? parseInt(writeTimestamp, 10)
        : Date.now() * 1_000_000;
      const tags = parseTags(writeTags);
      const point: TSDataPoint = {
        metric: writeMetric,
        value: val,
        timestampNs: ts,
        ...(tags.length > 0 ? { tags } : {}),
      };
      await client.timeSeries!.write(point);
      showToast("success", `Data point written for "${writeMetric}".`);
      showWritePanel = false;

      // Refresh: reload metrics list and current metric data
      await loadMetrics();
      if (selectedMetric !== null) {
        await loadDataPoints(selectedMetric);
      }
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      writeLoading = false;
    }
  }

  function requestDeleteMetric(metric: string): void {
    deleteTarget = metric;
  }

  async function confirmDeleteMetric(): Promise<void> {
    if (deleteTarget === null || client.timeSeries === null) return;
    const metric = deleteTarget;
    deleteTarget = null;
    loading = true;
    try {
      await client.timeSeries!.deleteSeries({ metric });
      showToast("success", `Deleted metric "${metric}"`);
      if (selectedMetric === metric) {
        backToMetrics();
      }
      await loadMetrics();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function handlePointClick(event: RowClickEvent<TimeSeriesRow>): void {
    selectedPoint = event.row;
  }

  async function handleRefresh(): Promise<void> {
    await loadMetrics();
    if (selectedMetric !== null) {
      await loadDataPoints(selectedMetric);
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    loadMetrics();
  });
</script>

<PageHeader
    title="TimeSeries"
    description={selectedMetric !== null
      ? `Browsing data points for "${selectedMetric}"`
      : "Browse, write, and manage time series data"}
    accentColor="#22d3ee"
  >
    {#snippet actions()}
      <div class="flex items-center gap-2">
        {#if selectedMetric !== null}
          <button class="btn btn-ghost btn-sm" onclick={backToMetrics}>
            Back to Metrics
          </button>
        {/if}
        <button class="btn btn-ghost btn-sm" onclick={handleRefresh} disabled={loading || pointsLoading}>
          {#if loading || pointsLoading}
            <span class="loading loading-spinner loading-xs"></span>
          {:else}
            <RefreshCw class="h-4 w-4" />
          {/if}
          Refresh
        </button>
        <button class="btn btn-primary btn-sm" onclick={openWritePanel}>
          <Plus class="h-4 w-4" />
          Write
        </button>
      </div>
    {/snippet}
  </PageHeader>

  {#if selectedMetric === null}
    <!-- ===== Metrics Browser ===== -->

    <!-- Search/Filter Bar -->
    <div class="surface-panel mb-4 rounded-xl p-4">
      <div class="flex items-center gap-3">
        <div class="flex-1">
          <div class="join w-full max-w-md">
            <input
              type="text"
              class="input join-item input-bordered input-sm w-full font-mono"
              placeholder="Filter by metric prefix..."
              bind:value={prefixFilter}
              onkeydown={(e) => e.key === "Enter" && loadMetrics()}
            />
            <button class="btn btn-sm join-item" onclick={loadMetrics} disabled={loading} aria-label="Search">
              <Search class="h-4 w-4" />
            </button>
          </div>
        </div>
        <div class="text-sm text-base-content/60">
          {metrics.length} metrics
        </div>
      </div>
    </div>

    <!-- Metrics List -->
    <div class="surface-panel rounded-xl overflow-hidden">
      {#if metrics.length === 0 && !loading}
        <div class="p-8">
          <EmptyState
            icon={TrendingUp as unknown as Component}
            title="No metrics found"
            description="Write your first data point to get started."
          />
        </div>
      {:else}
        <div class="divide-y divide-base-content/5">
          {#each metrics as metric (metric)}
            <div
              class="flex items-center justify-between px-4 py-3 hover:bg-base-content/3 cursor-pointer transition-colors"
              role="button"
              tabindex="0"
              onclick={() => selectMetric(metric)}
              onkeydown={(e) => e.key === "Enter" && selectMetric(metric)}
            >
              <div class="flex items-center gap-3">
                <TrendingUp class="h-4 w-4 text-base-content/40" />
                <span class="font-mono text-sm">{metric}</span>
              </div>
              <div class="flex items-center gap-2">
                <button
                  class="btn btn-ghost btn-xs btn-square opacity-0 group-hover:opacity-100"
                  onclick={(e: MouseEvent) => { e.stopPropagation(); requestDeleteMetric(metric); }}
                  aria-label="Delete metric"
                >
                  <Trash2 class="h-3.5 w-3.5 text-error" />
                </button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>

  {:else}
    <!-- ===== Data Points Browser for Selected Metric ===== -->

    <!-- Date Range Controls -->
    <div class="surface-panel mb-4 rounded-xl p-4">
      <div class="flex flex-wrap items-center gap-3">
        <!-- Preset buttons -->
        <div class="flex items-center gap-1">
          <span class="text-sm text-base-content/60 mr-1">Range:</span>
          {#each [
            { label: "1h", ms: 3600_000 },
            { label: "6h", ms: 21600_000 },
            { label: "24h", ms: 86400_000 },
            { label: "7d", ms: 604800_000 },
            { label: "30d", ms: 2592000_000 },
          ] as preset (preset.label)}
            <button
              class="btn btn-xs {rangeEndMs - rangeStartMs === preset.ms ? 'btn-primary' : 'btn-ghost'}"
              onclick={() => setPresetRange(preset.ms)}
            >
              {preset.label}
            </button>
          {/each}
        </div>

        <div class="divider divider-horizontal mx-0"></div>

        <!-- Custom date range inputs -->
        <div class="flex items-center gap-2">
          <input
            type="datetime-local"
            class="input input-bordered input-sm font-mono text-xs"
            bind:value={rangeStartInput}
          />
          <span class="text-base-content/40">to</span>
          <input
            type="datetime-local"
            class="input input-bordered input-sm font-mono text-xs"
            bind:value={rangeEndInput}
          />
          <button
            class="btn btn-sm btn-primary"
            onclick={applyCustomRange}
            disabled={pointsLoading}
          >
            <Search class="h-3.5 w-3.5" />
            Query
          </button>
        </div>
      </div>
    </div>

    <!-- Chart / Table Toggle -->
    {#if dataPoints.length > 0}
      <div class="mb-4 flex items-center justify-between">
        <div class="flex items-center gap-2">
          <button
            class="btn btn-sm {showChart ? 'btn-active' : 'btn-ghost'}"
            onclick={() => (showChart = !showChart)}
          >
            <TrendingUp class="h-4 w-4" />
            Chart
          </button>
          {#if showChart}
            <div class="join ml-2">
              <button
                class="join-item btn btn-sm btn-square {chartType === 'line' ? 'btn-active' : ''}"
                onclick={() => (chartType = "line")}
                aria-label="Line chart"
              >
                <LineChart class="h-4 w-4" />
              </button>
              <button
                class="join-item btn btn-sm btn-square {chartType === 'area' ? 'btn-active' : ''}"
                onclick={() => (chartType = "area")}
                aria-label="Area chart"
              >
                <AreaChart class="h-4 w-4" />
              </button>
              <button
                class="join-item btn btn-sm btn-square {chartType === 'scatter' ? 'btn-active' : ''}"
                onclick={() => (chartType = "scatter")}
                aria-label="Scatter chart"
              >
                <ScatterChartIcon class="h-4 w-4" />
              </button>
            </div>
          {/if}
        </div>
        <div class="text-sm text-base-content/60">
          {dataPoints.length} data points
        </div>
      </div>
    {/if}

    <!-- Chart -->
    {#if showChart && dataPoints.length > 0}
      <div class="surface-panel mb-4 rounded-xl p-5">
        <TimeSeriesChart
          data={chartData}
          {chartType}
          height={280}
          loading={pointsLoading}
          accentColor="#22d3ee"
        />
      </div>
    {/if}

    <!-- Data Points Table -->
    <CrudTable
      title="Data Points"
      data={dataPoints}
      columns={pointColumns}
      loading={pointsLoading}
      showCreate={false}
      showDelete={false}
      onrowclick={handlePointClick}
      rowKey={(row) => `${row.metric}-${row.timestampNs}`}
      pageSize={10}
      emptyMessage="No data points found"
    />

    <!-- Aggregate -->
    <div class="surface-panel mt-4 rounded-xl p-5" data-testid="ts-aggregate-card">
      <h3 class="mb-3 text-sm font-semibold">Aggregate</h3>
      <div class="flex flex-wrap items-end gap-3">
        <label class="form-control">
          <div class="label"><span class="label-text">Function</span></div>
          <select
            class="select select-bordered select-sm"
            bind:value={aggFunction}
            data-testid="ts-agg-function"
          >
            {#each AGG_FUNC_NAMES as name, i (name)}
              <option value={i}>{name}</option>
            {/each}
          </select>
        </label>
        <label class="form-control">
          <div class="label"><span class="label-text">Interval</span></div>
          <input
            type="text"
            class="input input-bordered input-sm w-28 font-mono"
            placeholder="1m"
            bind:value={aggInterval}
            data-testid="ts-agg-interval"
          />
        </label>
        <button
          class="btn btn-sm btn-primary"
          onclick={runAggregate}
          disabled={aggLoading}
          data-testid="ts-agg-run"
        >
          {#if aggLoading}<span class="loading loading-spinner loading-xs"></span>{/if}
          Aggregate
        </button>
        {#if parseDurationNs(aggInterval) !== null}
          <div class="text-xs text-base-content/50">
            <span>{formatDurationNs(parseDurationNs(aggInterval) ?? 0)}</span>
            <div class="font-mono">{parseDurationNs(aggInterval)} ns</div>
          </div>
        {/if}
      </div>
      <p class="mt-2 text-xs text-base-content/50">Uses the selected time range.</p>

      {#if aggRan}
        {#if aggResults.length === 0}
          <p class="mt-3 text-sm text-base-content/60" data-testid="ts-agg-empty">No buckets.</p>
        {:else}
          <div class="mt-4">
            <TimeSeriesChart data={aggChartData} chartType="line" height={200} accentColor="#a78bfa" />
          </div>
          <table class="table table-sm mt-3" data-testid="ts-agg-results">
            <thead>
              <tr><th>Bucket</th><th class="text-right">Value</th><th class="text-right">Count</th></tr>
            </thead>
            <tbody>
              {#each aggResults as b (b.timestampNs)}
                <tr>
                  <td class="font-mono text-xs">
                    {formatTimestamp(b.timestampNs / 1_000_000)}
                    <div class="text-base-content/40">{b.timestampNs} ns</div>
                  </td>
                  <td class="text-right font-mono" data-testid="ts-agg-value">{b.value}</td>
                  <td class="text-right font-mono">{b.count}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {/if}
    </div>

    <!-- Rich query -->
    <div class="surface-panel mt-4 rounded-xl p-5" data-testid="ts-rich-card">
      <h3 class="mb-3 text-sm font-semibold">Rich query</h3>
      <div class="grid grid-cols-1 gap-3 md:grid-cols-3">
        <label class="form-control">
          <div class="label"><span class="label-text">Tag filters</span></div>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            placeholder="host=node1,region=us-east"
            bind:value={rqTags}
            data-testid="ts-rich-tags"
          />
        </label>
        <label class="form-control">
          <div class="label"><span class="label-text">Aggregation</span></div>
          <select
            class="select select-bordered select-sm"
            bind:value={rqAggregation}
            data-testid="ts-rich-aggregation"
          >
            {#each AGG_FUNC_NAMES as name, i (name)}
              <option value={i}>{name}</option>
            {/each}
          </select>
        </label>
        <label class="form-control">
          <div class="label"><span class="label-text">Interval</span></div>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            placeholder="e.g. 5m"
            bind:value={rqInterval}
            data-testid="ts-rich-interval"
          />
        </label>
        <label class="form-control">
          <div class="label"><span class="label-text">Group by tags</span></div>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            placeholder="host,region"
            bind:value={rqGroupBy}
            data-testid="ts-rich-groupby"
          />
        </label>
        <label class="form-control">
          <div class="label"><span class="label-text">Limit</span></div>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            placeholder="no limit"
            bind:value={rqLimit}
            data-testid="ts-rich-limit"
          />
        </label>
        {#if rqAggregation === 6}
          <label class="form-control">
            <div class="label"><span class="label-text">Percentile</span></div>
            <input
              type="text"
              class="input input-bordered input-sm font-mono"
              placeholder="95"
              bind:value={rqPercentile}
              data-testid="ts-rich-percentile"
            />
          </label>
        {/if}
      </div>
      <div class="mt-3 flex items-center gap-3">
        <button
          class="btn btn-sm btn-primary"
          onclick={runRichQuery}
          disabled={rqLoading}
          data-testid="ts-rich-run"
        >
          {#if rqLoading}<span class="loading loading-spinner loading-xs"></span>{/if}
          Run rich query
        </button>
        <span class="text-xs text-base-content/50">Uses the selected time range.</span>
      </div>

      {#if rqResult}
        <div class="mt-4 space-y-3" data-testid="ts-rich-result">
          {#if rqResult.aggregatedValue !== undefined}
            <div class="text-sm">
              Aggregated value:
              <span class="font-mono" data-testid="ts-rich-aggregated">{rqResult.aggregatedValue}</span>
            </div>
          {/if}
          <div class="text-sm" data-testid="ts-rich-point-count">{rqResult.points.length} points</div>
          {#if rqResult.points.length > 0}
            <table class="table table-sm" data-testid="ts-rich-points">
              <thead><tr><th>Timestamp</th><th class="text-right">Value</th><th>Tags</th></tr></thead>
              <tbody>
                {#each rqResult.points as p, i (`${p.timestampNs}-${i}`)}
                  <tr>
                    <td class="font-mono text-xs">{p.timestampNs}</td>
                    <td class="text-right font-mono">{p.value}</td>
                    <td class="font-mono text-xs">{formatTags(p.tags ?? [])}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
          {#if rqResult.groupedResults.length > 0}
            <table class="table table-sm" data-testid="ts-rich-groups">
              <thead>
                <tr><th>Group</th><th class="text-right">Points</th><th class="text-right">Aggregated</th></tr>
              </thead>
              <tbody>
                {#each rqResult.groupedResults as g, i (i)}
                  <tr data-testid="ts-rich-group-row">
                    <td class="font-mono text-xs">{formatTags(g.tags)}</td>
                    <td class="text-right font-mono">{g.points.length}</td>
                    <td class="text-right font-mono">{g.aggregatedValue ?? ""}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/if}
    </div>

    <!-- Retention -->
    <div class="surface-panel mt-4 rounded-xl p-5" data-testid="ts-retention-card">
      <h3 class="mb-3 text-sm font-semibold">Retention</h3>
      <div class="mb-3 text-sm" data-testid="ts-retention-current">
        {#if retentionLoading && !retentionLoaded}
          <span class="loading loading-spinner loading-xs"></span>
        {:else if retention === null}
          <span data-testid="ts-retention-none">Current policy: none</span>
        {:else}
          <div>
            Duration:
            <span data-testid="ts-retention-duration">{formatDurationNs(retention.durationNs)}</span>
            <span class="font-mono text-xs text-base-content/40">{retention.durationNs} ns</span>
          </div>
          {#if retention.resolutionNs !== undefined && retention.resolutionNs > 0}
            <div>
              Resolution:
              <span data-testid="ts-retention-resolution">{formatDurationNs(retention.resolutionNs)}</span>
              <span class="font-mono text-xs text-base-content/40">{retention.resolutionNs} ns</span>
            </div>
          {/if}
          {#if retention.aggregation !== undefined && retention.aggregation !== 0}
            <div>
              Downsample aggregation:
              <span data-testid="ts-retention-aggregation">{AGG_FUNC_NAMES[retention.aggregation] ?? retention.aggregation}</span>
            </div>
          {/if}
        {/if}
      </div>
      <div class="flex flex-wrap items-end gap-3">
        <label class="form-control">
          <div class="label"><span class="label-text">Duration</span></div>
          <input
            type="text"
            class="input input-bordered input-sm w-28 font-mono"
            placeholder="7d"
            bind:value={retDuration}
            data-testid="ts-retention-duration-input"
          />
        </label>
        <label class="form-control">
          <div class="label"><span class="label-text">Resolution</span></div>
          <input
            type="text"
            class="input input-bordered input-sm w-28 font-mono"
            placeholder="optional, e.g. 1h"
            bind:value={retResolution}
            data-testid="ts-retention-resolution-input"
          />
        </label>
        <label class="form-control">
          <div class="label"><span class="label-text">Aggregation</span></div>
          <select
            class="select select-bordered select-sm"
            bind:value={retAggregation}
            data-testid="ts-retention-aggregation-input"
          >
            {#each AGG_FUNC_NAMES as name, i (name)}
              <option value={i}>{name}</option>
            {/each}
          </select>
        </label>
        <button
          class="btn btn-sm btn-primary"
          onclick={saveRetention}
          disabled={retentionLoading}
          data-testid="ts-retention-save"
        >
          Set retention
        </button>
      </div>
      {#if parseDurationNs(retDuration) !== null}
        <p class="mt-2 text-xs text-base-content/50">
          {formatDurationNs(parseDurationNs(retDuration) ?? 0)}
          <span class="font-mono">({parseDurationNs(retDuration)} ns)</span>
        </p>
      {/if}
    </div>
  {/if}

<!-- Data Point Detail SlidePanel -->
<SlidePanel
  open={selectedPoint !== null}
  title="Data Point"
  onclose={() => (selectedPoint = null)}
>
  {#snippet children()}
    {#if selectedPoint}
      <div class="space-y-5">
        <div>
          <span class="text-sm font-medium text-base-content/70 block mb-1">Metric</span>
          <div class="font-mono text-sm bg-base-200 rounded px-3 py-2">{selectedPoint.metric}</div>
        </div>
        <div>
          <span class="text-sm font-medium text-base-content/70 block mb-1">Value</span>
          <div class="font-mono text-sm bg-base-200 rounded px-3 py-2">{selectedPoint.value}</div>
        </div>
        <div>
          <span class="text-sm font-medium text-base-content/70 block mb-1">Timestamp</span>
          <div class="font-mono text-sm bg-base-200 rounded px-3 py-2">
            {formatTimestamp(selectedPoint.timestampNs / 1_000_000)}
          </div>
          <div class="text-xs text-base-content/40 mt-1">{selectedPoint.timestampNs} ns</div>
        </div>
        {#if selectedPoint.tags.length > 0}
          <div>
            <span class="text-sm font-medium text-base-content/70 block mb-1">Tags</span>
            <div class="flex flex-wrap gap-1">
              {#each selectedPoint.tags as tag}
                <span class="badge badge-sm badge-outline font-mono">
                  {tag.key}={tag.value}
                </span>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    {/if}
  {/snippet}
</SlidePanel>

<!-- Write Data Point SlidePanel -->
<SlidePanel
  open={showWritePanel}
  title="Write Data Point"
  onclose={() => (showWritePanel = false)}
>
  {#snippet children()}
    <div class="space-y-5">
      <div class="join" role="tablist">
        <button
          class="join-item btn btn-sm {writeMode === 'single' ? 'btn-active' : ''}"
          onclick={() => (writeMode = "single")}
          data-testid="ts-write-mode-single"
        >
          Single
        </button>
        <button
          class="join-item btn btn-sm {writeMode === 'batch' ? 'btn-active' : ''}"
          onclick={() => (writeMode = "batch")}
          data-testid="ts-write-mode-batch"
        >
          Batch
        </button>
      </div>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Metric</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono"
          placeholder="cpu.usage"
          bind:value={writeMetric}
        />
      </label>
      {#if writeMode === "single"}
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Value</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono"
          placeholder="42.5"
          bind:value={writeValue}
        />
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Timestamp (ns)</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono"
          placeholder="Leave empty for now"
          bind:value={writeTimestamp}
        />
        <div class="label">
          <span class="label-text-alt text-base-content/50">Unix nanoseconds. Empty uses current time.</span>
        </div>
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Tags</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono"
          placeholder="host=node1,region=us-east"
          bind:value={writeTags}
        />
        <div class="label">
          <span class="label-text-alt text-base-content/50">Comma-separated key=value pairs.</span>
        </div>
      </label>
      {:else}
        <label class="form-control w-full">
          <div class="label"><span class="label-text">Points</span></div>
          <textarea
            class="textarea textarea-bordered w-full font-mono text-xs"
            rows="8"
            placeholder={"1700000000000000000,42.5,host=node1;region=us\n1700000060000000000,43"}
            bind:value={writeBatchText}
            data-testid="ts-write-batch"
          ></textarea>
          <div class="label">
            <span class="label-text-alt text-base-content/50">
              One point per line: timestamp_ns,value[,k=v;k=v].
            </span>
          </div>
        </label>
      {/if}
    </div>
  {/snippet}
  {#snippet footer()}
    <div class="flex justify-end gap-2">
      <button class="btn btn-ghost" onclick={() => (showWritePanel = false)} disabled={writeLoading}>
        Cancel
      </button>
      <button
        class="btn btn-primary"
        onclick={writeMode === "batch" ? handleWriteBatch : handleWrite}
        disabled={writeLoading}
      >
        {#if writeLoading}
          <span class="loading loading-spinner loading-sm"></span>
        {/if}
        Write
      </button>
    </div>
  {/snippet}
</SlidePanel>

<!-- Delete confirmation modal -->
<ConfirmModal
  open={deleteTarget !== null}
  title="Delete Metric"
  message={`Are you sure you want to delete all data for "${deleteTarget ?? ""}"? This action cannot be undone.`}
  confirmLabel="Delete"
  variant="error"
  onconfirm={confirmDeleteMetric}
  oncancel={() => (deleteTarget = null)}
/>
