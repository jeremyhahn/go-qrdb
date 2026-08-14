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
  import type { Client, DatabaseInfo, SQLQueryResult } from "@qrdb/sdk";
  import { Play, Terminal, Clock, RotateCcw } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client, database }: { client: Client; database: DatabaseInfo } = $props();

  // ---------------------------------------------------------------------------
  // Default queries by engine type
  // ---------------------------------------------------------------------------

  const DEFAULT_QUERIES: Readonly<Record<string, string>> = {
    kv: "SELECT * FROM kv",
    timeseries: "SELECT * FROM datapoints",
    vector: "SELECT * FROM namespaces",
  };

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let query: string = $state("SELECT 1");
  let result: SQLQueryResult | null = $state(null);
  let queryError: string | null = $state(null);
  let executing: boolean = $state(false);
  let queryHistory: string[] = $state([]);

  // ---------------------------------------------------------------------------
  // Reset state when the database changes
  // ---------------------------------------------------------------------------

  $effect(() => {
    void database.name;
    query = DEFAULT_QUERIES[database.engine] ?? "SELECT 1";
    result = null;
    queryError = null;
    queryHistory = [];
  });

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function executeQuery(): Promise<void> {
    if (!query.trim()) return;

    executing = true;
    queryError = null;
    result = null;

    try {
      result = await client.sql.query(query, database.name);

      if (!queryHistory.includes(query.trim())) {
        queryHistory = [query.trim(), ...queryHistory.slice(0, 19)];
      }
    } catch (err: unknown) {
      queryError = err instanceof Error ? err.message : "Query failed";
      showToast("error", queryError);
    } finally {
      executing = false;
    }
  }

  function handleKeydown(event: KeyboardEvent): void {
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      event.preventDefault();
      executeQuery();
    }
  }

  function loadFromHistory(q: string): void {
    query = q;
  }

  function clearResults(): void {
    result = null;
    queryError = null;
  }
</script>

<PageHeader title="SQL Browser" description="Query databases using SQL">
  {#snippet actions()}
    <div class="flex items-center gap-2">
      <span class="badge badge-sm badge-outline font-mono">{database.name}</span>
      <span class="badge badge-sm {database.engine === 'kv' ? 'badge-secondary' : database.engine === 'timeseries' ? 'badge-warning' : 'badge-accent'}">
        {database.engine}
      </span>
      <button
        class="btn btn-ghost btn-sm"
        onclick={clearResults}
        disabled={executing || (result === null && queryError === null)}
      >
        <RotateCcw class="h-4 w-4" />
        Clear
      </button>
    </div>
  {/snippet}
</PageHeader>

<!-- Query Editor -->
<div class="card bg-base-200 mb-6">
  <div class="card-body p-5">
    <div class="flex items-center gap-3 mb-3">
      <div class="flex-1"></div>
      <button
        class="btn btn-primary btn-sm"
        onclick={executeQuery}
        disabled={executing || !query.trim()}
      >
        {#if executing}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <Play class="h-4 w-4" />
        {/if}
        Execute
      </button>
    </div>

    <textarea
      class="textarea textarea-bordered w-full font-mono text-sm min-h-[120px]"
      placeholder="Enter SQL query..."
      bind:value={query}
      onkeydown={handleKeydown}
      disabled={executing}
    ></textarea>

    <div class="text-xs text-base-content/50 mt-1">
      Ctrl+Enter to execute
    </div>
  </div>
</div>

<!-- Error Display -->
{#if queryError !== null}
  <div class="alert alert-error mb-6">
    <span class="font-mono text-sm">{queryError}</span>
  </div>
{/if}

<!-- Results Table -->
{#if result !== null}
  <div class="card bg-base-200 mb-6">
    <div class="card-body p-5">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold">Results</h2>
        <div class="flex items-center gap-3 text-xs text-base-content/60">
          <span>{result.rowCount} row{result.rowCount !== 1 ? "s" : ""}</span>
          {#if result.duration}
            <span class="flex items-center gap-1">
              <Clock class="h-3 w-3" />
              {result.duration}
            </span>
          {/if}
        </div>
      </div>

      {#if result.rowCount === 0}
        <div class="text-center py-8 text-base-content/50 text-sm">
          Query returned no rows.
        </div>
      {:else}
        <div class="overflow-x-auto max-h-[500px] overflow-y-auto">
          <table class="table table-xs table-pin-rows">
            <thead>
              <tr>
                {#each result.columns as col}
                  <th class="font-mono text-xs">{col}</th>
                {/each}
              </tr>
            </thead>
            <tbody>
              {#each result.rows as row}
                <tr class="hover">
                  {#each result.columns as col}
                    <td class="font-mono text-sm">{row[col] ?? ""}</td>
                  {/each}
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  </div>
{/if}

<!-- Query History -->
{#if queryHistory.length > 0}
  <div class="collapse collapse-arrow bg-base-200">
    <input type="checkbox" />
    <div class="collapse-title text-sm font-semibold flex items-center gap-2">
      <Clock class="h-4 w-4" />
      Query History ({queryHistory.length})
    </div>
    <div class="collapse-content">
      <ul class="space-y-1 pt-2">
        {#each queryHistory as historyQuery}
          <li>
            <button
              class="btn btn-ghost btn-xs w-full justify-start font-mono text-xs truncate"
              onclick={() => loadFromHistory(historyQuery)}
              title={historyQuery}
            >
              {historyQuery}
            </button>
          </li>
        {/each}
      </ul>
    </div>
  </div>
{/if}
