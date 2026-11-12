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
  import type {
    Client,
    DatabaseInfo,
    HealthResponse,
    StatusResponse,
    CapabilitiesResponse,
    MetricFamily,
  } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { XCircle, RefreshCw, ChevronRight, ChevronDown } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client }: { client: Client } = $props();

  let health = $state<HealthResponse | null>(null);
  let status = $state<StatusResponse | null>(null);
  let capabilities = $state<CapabilitiesResponse | null>(null);
  let databases = $state<DatabaseInfo[]>([]);
  let loading: boolean = $state(true);
  let hasError: boolean = $state(false);
  let pollTimer: ReturnType<typeof setInterval> | null = null;

  /** Capability entries derived from the capabilities response. */
  let capabilityEntries = $derived<ReadonlyArray<{ name: string; enabled: boolean }>>(
    capabilities !== null
      ? [
          { name: "KV", enabled: capabilities.kv },
          { name: "MVCC", enabled: capabilities.mvcc },
          { name: "Transactions", enabled: capabilities.transaction },
          { name: "Batch", enabled: capabilities.batch },
          { name: "Watch", enabled: capabilities.watch },
          { name: "Leases", enabled: capabilities.lease },
          { name: "Time Series", enabled: capabilities.timeSeries },
          { name: "Vectors", enabled: capabilities.vector },
          { name: "Index", enabled: capabilities.index },
        ]
      : [],
  );

  /** Per-shard detail from status.shards, typed defensively (the SDK types it as unknown). */
  type ShardView =
    | { kind: "empty" }
    | { kind: "rows"; columns: string[]; rows: Record<string, unknown>[] }
    | { kind: "json"; text: string };

  function isRecord(v: unknown): v is Record<string, unknown> {
    return typeof v === "object" && v !== null && !Array.isArray(v);
  }

  function shardView(shards: unknown): ShardView {
    if (shards === null || shards === undefined) return { kind: "empty" };
    if (Array.isArray(shards)) {
      if (shards.length === 0) return { kind: "empty" };
      if (shards.every(isRecord)) {
        const columns: string[] = [];
        for (const row of shards) {
          for (const key of Object.keys(row)) {
            if (!columns.includes(key)) columns.push(key);
          }
        }
        return { kind: "rows", columns, rows: shards };
      }
    } else if (isRecord(shards) && Object.keys(shards).length === 0) {
      return { kind: "empty" };
    }
    return { kind: "json", text: JSON.stringify(shards, null, 2) };
  }

  function cellText(v: unknown): string {
    if (v === null || v === undefined) return "";
    if (typeof v === "object") return JSON.stringify(v);
    return String(v);
  }

  let shardsView = $derived<ShardView>(shardView(status?.shards));

  /**
   * Fetches health, status, and capabilities from the admin API.
   * Errors are captured but do not halt polling.
   */
  async function fetchDashboardData(): Promise<void> {
    try {
      const [h, s, c] = await Promise.all([
        client.admin.health(),
        client.admin.status(),
        client.admin.capabilities(),
      ]);
      health = h;
      status = s;
      capabilities = c;
      hasError = false;

      try {
        databases = await client.database.list();
      } catch {
        // Database listing is optional — may not be available
      }
    } catch (err: unknown) {
      hasError = true;
      showToast("error", err instanceof Error ? err.message : "Failed to fetch dashboard data");
    } finally {
      loading = false;
    }
  }

  /** Starts the 5-second polling interval. */
  function startPolling(): void {
    stopPolling();
    pollTimer = setInterval(() => {
      void fetchDashboardData();
    }, 5000);
  }

  /** Clears the polling interval if active. */
  function stopPolling(): void {
    if (pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  // ---- Metrics (GET /metrics, Prometheus exposition) ----

  let metrics = $state<MetricFamily[]>([]);
  let metricsLoading: boolean = $state(false);
  let metricsLoaded: boolean = $state(false);
  let metricsUnavailable: boolean = $state(false);
  let metricsError = $state<string | null>(null);
  let metricsFilter: string = $state("");
  let expandedFamilies = $state<Record<string, boolean>>({});

  /** Families whose name, type or help contains the filter (case-insensitive). */
  let filteredMetrics = $derived<MetricFamily[]>(
    metricsFilter.trim() === ""
      ? metrics
      : metrics.filter((f) => {
          const q = metricsFilter.trim().toLowerCase();
          return (
            f.name.toLowerCase().includes(q) ||
            f.type.toLowerCase().includes(q) ||
            f.help.toLowerCase().includes(q)
          );
        }),
  );

  function isNotImplemented(err: unknown): boolean {
    return err instanceof Error && err.name === "NotImplementedError";
  }

  /** Loads the node's Prometheus metrics; desktop mode has none. */
  async function fetchMetrics(): Promise<void> {
    metricsLoading = true;
    try {
      metrics = await client.admin.metrics();
      metricsError = null;
      metricsUnavailable = false;
      metricsLoaded = true;
    } catch (err: unknown) {
      if (isNotImplemented(err)) {
        metricsUnavailable = true;
      } else {
        const msg = err instanceof Error ? err.message : "Failed to fetch metrics";
        metricsError = msg;
        showToast("error", `Metrics: ${msg}`);
      }
    } finally {
      metricsLoading = false;
    }
  }

  function toggleFamily(name: string): void {
    expandedFamilies[name] = !expandedFamilies[name];
  }

  function labelText(labels: Record<string, string>): string {
    const parts = Object.entries(labels).map(([k, v]) => `${k}="${v}"`);
    return parts.length === 0 ? "" : `{${parts.join(", ")}}`;
  }

  function valueText(v: number): string {
    if (Number.isNaN(v)) return "NaN";
    if (v === Number.POSITIVE_INFINITY) return "+Inf";
    if (v === Number.NEGATIVE_INFINITY) return "-Inf";
    return String(v);
  }

  onMount(() => {
    void fetchDashboardData();
    void fetchMetrics();
    startPolling();

    return () => {
      stopPolling();
    };
  });
</script>

<PageHeader
  title="Dashboard"
  description="Cluster health, node status, and capabilities at a glance"
>
  {#snippet actions()}
    <button class="btn btn-ghost btn-sm" onclick={() => void fetchDashboardData()} disabled={loading}>
      {#if loading}
        <span class="loading loading-spinner loading-xs"></span>
      {:else}
        <RefreshCw class="h-4 w-4" />
      {/if}
      Refresh
    </button>
  {/snippet}
</PageHeader>

{#if loading}
  <div class="flex items-center justify-center py-16">
    <span class="loading loading-spinner loading-lg text-primary"></span>
  </div>
{:else if hasError && health === null}
  <div class="alert alert-error">
    <XCircle class="h-6 w-6 shrink-0" />
    <span>Failed to load dashboard data. Check the toast notifications for details.</span>
    <button class="btn btn-sm" onclick={() => void fetchDashboardData()}>
      <RefreshCw class="h-4 w-4" />
      Retry
    </button>
  </div>
{:else}
  <div class="flex flex-col gap-6">
    <!-- Health + Status stats row -->
    <div class="stats bg-base-200 shadow">
      <div class="stat">
        <div class="stat-title">Health</div>
        <div class="stat-value text-xl">
          {#if health !== null}
            <span class="badge" class:badge-success={health.status === "ok"} class:badge-error={health.status !== "ok"}>
              {health.status}
            </span>
          {:else}
            <span class="badge badge-ghost">Unknown</span>
          {/if}
        </div>
        <div class="stat-desc">
          {#if health !== null}
            {health.isLeader ? "Leader" : "Follower"}
          {/if}
        </div>
      </div>

      <div class="stat">
        <div class="stat-title">Node ID</div>
        <div class="stat-value text-xl font-mono">
          {status !== null ? status.nodeId : "--"}
        </div>
        <div class="stat-desc">
          {status !== null ? status.raftAddress : ""}
        </div>
      </div>

      <div class="stat">
        <div class="stat-title">Leader</div>
        <div class="stat-value text-xl">
          {#if status !== null}
            <span class="badge" class:badge-primary={status.isLeader} class:badge-ghost={!status.isLeader}>
              {status.isLeader ? "Yes" : "No"}
            </span>
          {:else}
            <span class="badge badge-ghost">--</span>
          {/if}
        </div>
        <div class="stat-desc">
          {#if status !== null}
            Leader ID: {status.leaderId}
          {/if}
        </div>
      </div>

      <div class="stat">
        <div class="stat-title">Shards</div>
        <div class="stat-value text-xl">
          {status !== null ? status.shardCount : "--"}
        </div>
        <div class="stat-desc">Loaded on this node</div>
      </div>

      <div class="stat">
        <div class="stat-title">Draining</div>
        <div class="stat-value text-xl">
          {#if status !== null}
            <span class="badge" class:badge-warning={status.isDraining} class:badge-success={!status.isDraining}>
              {status.isDraining ? "Yes" : "No"}
            </span>
          {:else}
            <span class="badge badge-ghost">--</span>
          {/if}
        </div>
        <div class="stat-desc">Drain mode status</div>
      </div>
    </div>

    <!-- Capabilities card -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Capabilities</h2>
        <div class="flex flex-wrap gap-2 pt-2">
          {#each capabilityEntries as cap (cap.name)}
            <span
              class="badge badge-lg"
              class:badge-success={cap.enabled}
              class:badge-ghost={!cap.enabled}
            >
              {cap.name}
            </span>
          {/each}
        </div>
      </div>
    </div>

    <!-- Shards card -->
    <div class="card bg-base-200 shadow" data-testid="dashboard-shards">
      <div class="card-body p-5">
        <h2 class="card-title text-lg">Shards</h2>
        {#if shardsView.kind === "empty"}
          <p class="text-base-content/50 py-4 text-sm" data-testid="dashboard-shards-empty">none</p>
        {:else if shardsView.kind === "rows"}
          <div class="overflow-x-auto">
            <table class="table table-sm" data-testid="dashboard-shards-table">
              <thead>
                <tr>
                  {#each shardsView.columns as col (col)}
                    <th>{col}</th>
                  {/each}
                </tr>
              </thead>
              <tbody>
                {#each shardsView.rows as row, i (i)}
                  <tr>
                    {#each shardsView.columns as col (col)}
                      <td class="font-mono text-xs">{cellText(row[col])}</td>
                    {/each}
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else}
          <pre class="bg-base-300 rounded p-3 text-xs overflow-x-auto" data-testid="dashboard-shards-json">{shardsView.text}</pre>
        {/if}
      </div>
    </div>

    <!-- Databases card -->
    <div class="card bg-base-200 shadow">
      <div class="card-body p-5">
        <h2 class="card-title text-lg">Databases</h2>
        {#if databases.length === 0}
          <p class="text-base-content/50 py-4 text-sm">No databases found. Create one in the Databases view.</p>
        {:else}
          <div class="overflow-x-auto">
            <table class="table table-sm">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Backend</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {#each databases as db (db.name)}
                  <tr>
                    <td class="font-mono text-sm">{db.name}</td>
                    <td class="text-xs">{db.backend}</td>
                    <td>
                      <span
                        class="badge badge-sm"
                        class:badge-success={db.status === "open"}
                        class:badge-warning={db.status === "closed"}
                        class:badge-error={db.status === "error"}
                      >
                        {db.status}
                      </span>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    </div>

    <!-- Metrics card (GET /metrics) -->
    <div class="card bg-base-200 shadow" data-testid="dashboard-metrics">
      <div class="card-body p-5">
        <div class="flex items-center justify-between gap-2">
          <h2 class="card-title text-lg">Metrics</h2>
          {#if !metricsUnavailable}
            <button
              class="btn btn-ghost btn-sm"
              data-testid="metrics-refresh"
              onclick={() => void fetchMetrics()}
              disabled={metricsLoading}
            >
              {#if metricsLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {:else}
                <RefreshCw class="h-4 w-4" />
              {/if}
              Refresh
            </button>
          {/if}
        </div>
        {#if metricsUnavailable}
          <p class="text-base-content/50 py-4 text-sm" data-testid="metrics-unavailable">
            Metrics are not available on desktop.
          </p>
        {:else}
          <input
            type="search"
            class="input input-bordered input-sm w-full max-w-sm"
            placeholder="Filter by name, type or help"
            aria-label="Filter metrics"
            data-testid="metrics-filter"
            bind:value={metricsFilter}
          />
          {#if metricsError !== null && !metricsLoaded}
            <p class="text-error py-4 text-sm" data-testid="metrics-error">{metricsError}</p>
          {:else if metricsLoaded && filteredMetrics.length === 0}
            <p class="text-base-content/50 py-4 text-sm" data-testid="metrics-empty">No metrics match.</p>
          {:else if metricsLoaded}
            <div class="overflow-x-auto">
              <table class="table table-sm" data-testid="metrics-table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Type</th>
                    <th>Help</th>
                    <th class="text-right">Samples</th>
                  </tr>
                </thead>
                <tbody>
                  {#each filteredMetrics as fam (fam.name)}
                    <tr data-testid="metrics-family" data-family={fam.name}>
                      <td class="font-mono text-xs">
                        <button
                          class="btn btn-ghost btn-xs"
                          aria-expanded={expandedFamilies[fam.name] === true}
                          aria-label={`Toggle ${fam.name}`}
                          onclick={() => toggleFamily(fam.name)}
                        >
                          {#if expandedFamilies[fam.name]}
                            <ChevronDown class="h-3 w-3" />
                          {:else}
                            <ChevronRight class="h-3 w-3" />
                          {/if}
                        </button>
                        {fam.name}
                      </td>
                      <td><span class="badge badge-sm badge-ghost">{fam.type}</span></td>
                      <td class="text-xs">{fam.help}</td>
                      <td class="text-right font-mono text-xs">{fam.samples.length}</td>
                    </tr>
                    {#if expandedFamilies[fam.name]}
                      <tr data-testid="metrics-samples" data-family={fam.name}>
                        <td colspan="4">
                          <table class="table table-xs">
                            <tbody>
                              {#each fam.samples as sample, i (i)}
                                <tr data-testid="metrics-sample">
                                  <td class="font-mono text-xs">{sample.name}{labelText(sample.labels)}</td>
                                  <td class="text-right font-mono text-xs">{valueText(sample.value)}</td>
                                </tr>
                              {/each}
                            </tbody>
                          </table>
                        </td>
                      </tr>
                    {/if}
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        {/if}
      </div>
    </div>
  </div>
{/if}
