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
    SearchEngine,
    SearchEvent,
    KVSearchEvent,
    TimeSeriesSearchEvent,
    VectorSearchEvent,
  } from "./types.ts";
  import KVSearch from "./KVSearch.svelte";
  import TimeSeriesSearch from "./TimeSeriesSearch.svelte";
  import VectorSearch from "./VectorSearch.svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    engine,
    loading = false,
    availableMetrics = [],
    availableNamespaces = [],
    onSearch,
    onClear,
  }: {
    engine: SearchEngine;
    loading?: boolean;
    availableMetrics?: string[];
    availableNamespaces?: string[];
    onSearch: (event: SearchEvent) => void;
    onClear?: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const engineClass = $derived.by(() => {
    switch (engine) {
      case "kv":
        return "engine-kv";
      case "timeseries":
        return "engine-timeseries";
      case "vector":
        return "engine-vectors";
      default:
        return "";
    }
  });

  const engineLabel = $derived.by(() => {
    switch (engine) {
      case "kv":
        return "KV Store";
      case "timeseries":
        return "Time Series";
      case "vector":
        return "Vector";
      default:
        return "Search";
    }
  });

  const engineIcon = $derived.by(() => {
    switch (engine) {
      case "kv":
        // Key icon
        return "M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z";
      case "timeseries":
        // Chart icon
        return "M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z";
      case "vector":
        // Cube icon
        return "M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4";
      default:
        return "M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z";
    }
  });

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  function handleKVSearch(event: KVSearchEvent): void {
    onSearch({ engine: "kv", data: event });
  }

  function handleTimeSeriesSearch(event: TimeSeriesSearchEvent): void {
    onSearch({ engine: "timeseries", data: event });
  }

  function handleVectorSearch(event: VectorSearchEvent): void {
    onSearch({ engine: "vector", data: event });
  }

  function handleClear(): void {
    onClear?.();
  }
</script>

<div
  class="bg-base-100 rounded-lg border shadow-md {engineClass}-border"
  style="border-color: var(--nav-accent, var(--color-{engine}));"
>
  <!-- Header -->
  <div
    class="flex items-center gap-3 border-b px-4 py-3"
    style="border-color: var(--nav-accent, var(--color-{engine}))20;"
  >
    <div
      class="flex h-8 w-8 items-center justify-center rounded-lg {engineClass}-bg"
      style="background-color: color-mix(in oklch, var(--nav-accent, var(--color-{engine})) 15%, transparent);"
    >
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-5 w-5"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        style="color: var(--nav-accent, var(--color-{engine}));"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="1.5"
          d={engineIcon}
        />
      </svg>
    </div>
    <h3 class="font-semibold">{engineLabel} Search</h3>
  </div>

  <!-- Content -->
  <div class="p-4">
    {#if engine === "kv"}
      <KVSearch {loading} onSearch={handleKVSearch} onClear={handleClear} />
    {:else if engine === "timeseries"}
      <TimeSeriesSearch
        {loading}
        {availableMetrics}
        onSearch={handleTimeSeriesSearch}
        onClear={handleClear}
      />
    {:else if engine === "vector"}
      <VectorSearch
        {loading}
        {availableNamespaces}
        onSearch={handleVectorSearch}
        onClear={handleClear}
      />
    {/if}
  </div>
</div>
