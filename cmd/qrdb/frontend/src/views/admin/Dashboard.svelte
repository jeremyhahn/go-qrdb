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
  } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { XCircle, RefreshCw } from "lucide-svelte";
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
        ]
      : [],
  );

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

  onMount(() => {
    void fetchDashboardData();
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
  </div>
{/if}
