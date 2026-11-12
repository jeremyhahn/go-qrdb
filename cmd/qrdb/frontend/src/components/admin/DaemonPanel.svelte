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
  import type { Client } from "@qrdb/sdk";
  import { isDesktop } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { Play, Square, RotateCw, RefreshCw, XCircle } from "lucide-svelte";
  import {
    daemonStore,
    updateStatus,
    setLogs,
    setLoading,
    setError,
    clearError,
  } from "$stores/daemon.svelte.ts";
  import { showToast } from "$stores/toast.svelte.ts";

  let { client }: { client: Client } = $props();

  let configPath: string = $state("/etc/qrdb/qrdbd.yaml");
  let maxLines: number = $state(100);
  let autoRefresh: boolean = $state(false);
  let statusLoading: boolean = $state(true);
  let logRefreshTimer: ReturnType<typeof setInterval> | null = null;
  let logContainer: HTMLPreElement | null = $state(null);

  // ---------------------------------------------------------------------------
  // Derived state
  // ---------------------------------------------------------------------------

  /** Available max-lines options for the log viewer. */
  const maxLinesOptions: readonly number[] = [50, 100, 200, 500] as const;

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the current daemon status from the server. */
  async function fetchStatus(): Promise<void> {
    try {
      const status = await client.daemon.daemonStatus();
      updateStatus(status);
      clearError();
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to fetch daemon status";
      setError(msg);
      showToast("error", msg);
    } finally {
      statusLoading = false;
    }
  }

  /** Fetches recent daemon logs. */
  async function fetchLogs(): Promise<void> {
    try {
      const logs = await client.daemon.daemonLogs(maxLines);
      setLogs(logs);
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to fetch daemon logs";
      showToast("error", msg);
    }
  }

  /** Starts the daemon process. */
  async function handleStart(): Promise<void> {
    if (configPath.trim() === "") {
      showToast("warning", "A config path is required to start the daemon");
      return;
    }
    try {
      setLoading(true);
      clearError();
      await client.daemon.startDaemon(configPath.trim());
      showToast("success", "Daemon started successfully");
      await fetchStatus();
      await fetchLogs();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to start daemon";
      setError(msg);
      showToast("error", msg);
    } finally {
      setLoading(false);
    }
  }

  /** Stops the running daemon process. */
  async function handleStop(): Promise<void> {
    try {
      setLoading(true);
      clearError();
      await client.daemon.stopDaemon();
      showToast("success", "Daemon stopped successfully");
      await fetchStatus();
      await fetchLogs();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to stop daemon";
      setError(msg);
      showToast("error", msg);
    } finally {
      setLoading(false);
    }
  }

  /** Restarts the daemon process. */
  async function handleRestart(): Promise<void> {
    if (configPath.trim() === "") {
      showToast("warning", "A config path is required to restart the daemon");
      return;
    }
    try {
      setLoading(true);
      clearError();
      await client.daemon.restartDaemon(configPath.trim());
      showToast("success", "Daemon restarted successfully");
      await fetchStatus();
      await fetchLogs();
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to restart daemon";
      setError(msg);
      showToast("error", msg);
    } finally {
      setLoading(false);
    }
  }

  /** Refreshes both status and logs. */
  async function handleRefresh(): Promise<void> {
    try {
      setLoading(true);
      await Promise.all([fetchStatus(), fetchLogs()]);
    } finally {
      setLoading(false);
    }
  }

  // ---------------------------------------------------------------------------
  // Auto-refresh polling
  // ---------------------------------------------------------------------------

  function startLogPolling(): void {
    stopLogPolling();
    logRefreshTimer = setInterval(() => {
      void fetchLogs();
    }, 5000);
  }

  function stopLogPolling(): void {
    if (logRefreshTimer !== null) {
      clearInterval(logRefreshTimer);
      logRefreshTimer = null;
    }
  }

  // ---------------------------------------------------------------------------
  // Effects
  // ---------------------------------------------------------------------------

  /** Toggle auto-refresh polling when the checkbox changes. */
  $effect(() => {
    if (autoRefresh) {
      startLogPolling();
    } else {
      stopLogPolling();
    }
  });

  /** Auto-scroll to bottom when new log content arrives. */
  $effect(() => {
    // Access logs to track the dependency.
    void daemonStore.logs;
    if (logContainer !== null) {
      logContainer.scrollTop = logContainer.scrollHeight;
    }
  });

  onMount(() => {
    if (!isDesktop()) {
      return;
    }
    void fetchStatus();
    void fetchLogs();

    return () => {
      stopLogPolling();
    };
  });
</script>

{#if !isDesktop()}
  <div class="flex flex-col gap-6">
    <div role="alert" class="alert alert-info">
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-6 w-6 shrink-0 stroke-current"
        fill="none"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
      <span>
        Daemon management is only available in desktop mode. The qrdbd server
        manages its own lifecycle when accessed via the web interface.
      </span>
    </div>
  </div>
{:else}
  <div class="flex flex-col gap-6">
    <!-- Error display -->
    {#if daemonStore.error !== null}
      <div class="alert alert-error">
        <XCircle class="h-6 w-6 shrink-0" />
        <span>{daemonStore.error}</span>
        <button class="btn btn-ghost btn-sm" onclick={clearError}
          >Dismiss</button
        >
      </div>
    {/if}

    <!-- Status bar -->
    <div class="stats bg-base-200 shadow">
      <div class="stat">
        <div class="stat-title">Daemon Status</div>
        <div class="stat-value text-xl">
          {#if statusLoading}
            <span class="loading loading-spinner loading-sm"></span>
          {:else}
            <span class="inline-flex items-center gap-2">
              <span
                class="inline-block h-3 w-3 rounded-full"
                class:bg-success={daemonStore.running}
                class:animate-pulse={daemonStore.running}
                class:bg-error={!daemonStore.running}
              ></span>
              <span
                class="badge badge-lg"
                class:badge-success={daemonStore.running}
                class:badge-error={!daemonStore.running}
              >
                {daemonStore.running ? "Running" : "Stopped"}
              </span>
            </span>
          {/if}
        </div>
        <div class="stat-desc">Process lifecycle state</div>
      </div>

      <div class="stat">
        <div class="stat-title">PID</div>
        <div class="stat-value text-xl font-mono">
          {daemonStore.pid !== null ? daemonStore.pid : "--"}
        </div>
        <div class="stat-desc">Process identifier</div>
      </div>

      <div class="stat">
        <div class="stat-title">Log File</div>
        <div
          class="stat-value text-sm font-mono truncate max-w-xs"
          title={daemonStore.logFile ?? ""}
        >
          {daemonStore.logFile !== null ? daemonStore.logFile : "--"}
        </div>
        <div class="stat-desc">Output log path</div>
      </div>
    </div>

    <!-- Control buttons -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Controls</h2>

        <!-- Config path input -->
        <div class="form-control">
          <label class="label" for="daemon-config-path">
            <span class="label-text">Config Path</span>
          </label>
          <input
            id="daemon-config-path"
            type="text"
            class="input input-bordered input-sm font-mono"
            placeholder="/etc/qrdb/qrdbd.yaml"
            bind:value={configPath}
          />
        </div>

        <!-- Action buttons -->
        <div class="mt-3 flex flex-wrap gap-2">
          <button
            class="btn btn-success btn-sm"
            onclick={() => void handleStart()}
            disabled={daemonStore.loading || daemonStore.running}
          >
            {#if daemonStore.loading && !daemonStore.running}
              <span class="loading loading-spinner loading-xs"></span>
            {:else}
              <Play class="h-4 w-4" />
            {/if}
            Start
          </button>

          <button
            class="btn btn-error btn-sm"
            onclick={() => void handleStop()}
            disabled={daemonStore.loading || !daemonStore.running}
          >
            {#if daemonStore.loading && daemonStore.running}
              <span class="loading loading-spinner loading-xs"></span>
            {:else}
              <Square class="h-4 w-4" />
            {/if}
            Stop
          </button>

          <button
            class="btn btn-warning btn-sm"
            onclick={() => void handleRestart()}
            disabled={daemonStore.loading || !daemonStore.running}
          >
            <RotateCw class="h-4 w-4" />
            Restart
          </button>

          <button
            class="btn btn-ghost btn-sm"
            onclick={() => void handleRefresh()}
            disabled={daemonStore.loading}
          >
            {#if daemonStore.loading}
              <span class="loading loading-spinner loading-xs"></span>
            {:else}
              <RefreshCw class="h-4 w-4" />
            {/if}
            Refresh Status
          </button>
        </div>
      </div>
    </div>

    <!-- Log viewer -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <div class="flex items-center justify-between">
          <h2 class="card-title text-lg">Logs</h2>
          <div class="flex items-center gap-3">
            <!-- Max lines selector -->
            <div class="form-control">
              <select
                class="select select-bordered select-xs"
                bind:value={maxLines}
                onchange={() => void fetchLogs()}
                aria-label="Max log lines"
              >
                {#each maxLinesOptions as opt (opt)}
                  <option value={opt}>{opt} lines</option>
                {/each}
              </select>
            </div>

            <!-- Auto-refresh toggle -->
            <label class="label cursor-pointer gap-2">
              <span class="label-text text-xs">Auto-refresh</span>
              <input
                type="checkbox"
                class="toggle toggle-xs toggle-primary"
                bind:checked={autoRefresh}
              />
            </label>

            <!-- Manual refresh -->
            <button
              class="btn btn-ghost btn-xs"
              onclick={() => void fetchLogs()}
              aria-label="Refresh logs"
            >
              <RefreshCw class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>

        <pre
          bind:this={logContainer}
          class="mt-2 max-h-96 overflow-auto rounded-lg bg-neutral p-4 text-xs leading-relaxed text-neutral-content font-mono">{daemonStore.logs ||
            "No log output available."}</pre>
      </div>
    </div>
  </div>
{/if}
