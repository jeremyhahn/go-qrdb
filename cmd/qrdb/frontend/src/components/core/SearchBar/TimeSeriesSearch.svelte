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
    TimeSeriesSearchOptions,
    TimeSeriesSearchEvent,
    TimeSeriesAggregation,
    TimeRangePreset,
  } from "./types.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    loading = false,
    availableMetrics = [],
    onSearch,
    onClear,
  }: {
    loading?: boolean;
    availableMetrics?: string[];
    onSearch: (event: TimeSeriesSearchEvent) => void;
    onClear?: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let metric = $state("");
  let metricSuggestions = $state<string[]>([]);
  let showSuggestions = $state(false);

  let selectedPreset = $state<TimeRangePreset>("1h");
  let customStartTime = $state("");
  let customEndTime = $state("");

  let tags = $state<Array<{ key: string; value: string }>>([]);
  let newTagKey = $state("");
  let newTagValue = $state("");

  let aggregation = $state<TimeSeriesAggregation>("none");
  let interval = $state("5m");

  // ---------------------------------------------------------------------------
  // Constants
  // ---------------------------------------------------------------------------

  const presets: Array<{ value: TimeRangePreset; label: string }> = [
    { value: "5m", label: "Last 5m" },
    { value: "15m", label: "Last 15m" },
    { value: "1h", label: "Last 1h" },
    { value: "6h", label: "Last 6h" },
    { value: "24h", label: "Last 24h" },
    { value: "7d", label: "Last 7d" },
    { value: "30d", label: "Last 30d" },
    { value: "custom", label: "Custom" },
  ];

  const aggregations: Array<{ value: TimeSeriesAggregation; label: string }> = [
    { value: "none", label: "None (Raw)" },
    { value: "avg", label: "Average" },
    { value: "sum", label: "Sum" },
    { value: "min", label: "Minimum" },
    { value: "max", label: "Maximum" },
    { value: "count", label: "Count" },
  ];

  const intervalOptions = ["1m", "5m", "15m", "30m", "1h", "6h", "12h", "24h"];

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const canSearch = $derived(metric.trim().length > 0);

  const hasValues = $derived.by(() => {
    return (
      metric.length > 0 ||
      tags.length > 0 ||
      aggregation !== "none" ||
      selectedPreset !== "1h"
    );
  });

  // ---------------------------------------------------------------------------
  // Effects
  // ---------------------------------------------------------------------------

  $effect(() => {
    if (metric.length > 0 && availableMetrics.length > 0) {
      const filtered = availableMetrics.filter((m) =>
        m.toLowerCase().includes(metric.toLowerCase()),
      );
      metricSuggestions = filtered.slice(0, 10);
    } else {
      metricSuggestions = [];
    }
  });

  // ---------------------------------------------------------------------------
  // Helper Functions
  // ---------------------------------------------------------------------------

  function getTimeRange(): {
    startTime: Date | undefined;
    endTime: Date | undefined;
  } {
    const now = new Date();

    if (selectedPreset === "custom") {
      return {
        startTime: customStartTime ? new Date(customStartTime) : undefined,
        endTime: customEndTime ? new Date(customEndTime) : undefined,
      };
    }

    const presetMs: Record<TimeRangePreset, number> = {
      "5m": 5 * 60 * 1000,
      "15m": 15 * 60 * 1000,
      "1h": 60 * 60 * 1000,
      "6h": 6 * 60 * 60 * 1000,
      "24h": 24 * 60 * 60 * 1000,
      "7d": 7 * 24 * 60 * 60 * 1000,
      "30d": 30 * 24 * 60 * 60 * 1000,
      custom: 0,
    };

    const ms = presetMs[selectedPreset];
    return {
      startTime: new Date(now.getTime() - ms),
      endTime: now,
    };
  }

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  function handleSearch(): void {
    if (!canSearch || loading) return;

    const timeRange = getTimeRange();
    const tagRecord: Record<string, string> = {};
    for (const tag of tags) {
      tagRecord[tag.key] = tag.value;
    }

    const options: TimeSeriesSearchOptions = {
      metric: metric.trim(),
      startTime: timeRange.startTime,
      endTime: timeRange.endTime,
      tags: Object.keys(tagRecord).length > 0 ? tagRecord : undefined,
      aggregation: aggregation !== "none" ? aggregation : undefined,
      interval: aggregation !== "none" ? interval : undefined,
    };

    onSearch({ options });
  }

  function handleClear(): void {
    metric = "";
    selectedPreset = "1h";
    customStartTime = "";
    customEndTime = "";
    tags = [];
    newTagKey = "";
    newTagValue = "";
    aggregation = "none";
    interval = "5m";
    onClear?.();
  }

  function handleKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter" && canSearch && !loading) {
      showSuggestions = false;
      handleSearch();
    } else if (event.key === "Escape") {
      showSuggestions = false;
    }
  }

  function selectMetric(m: string): void {
    metric = m;
    showSuggestions = false;
  }

  function addTag(): void {
    if (newTagKey.trim() && newTagValue.trim()) {
      tags = [...tags, { key: newTagKey.trim(), value: newTagValue.trim() }];
      newTagKey = "";
      newTagValue = "";
    }
  }

  function removeTag(index: number): void {
    tags = tags.filter((_, i) => i !== index);
  }

  function handleTagKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      addTag();
    }
  }
</script>

<div class="engine-timeseries flex flex-col gap-4">
  <!-- Metric Input with Autocomplete -->
  <div class="form-control w-full">
    <label class="label" for="metric-input">
      <span class="label-text">Metric Name</span>
    </label>
    <div class="relative">
      <input
        id="metric-input"
        type="text"
        class="input input-bordered w-full"
        placeholder="Enter metric name..."
        bind:value={metric}
        onfocus={() => (showSuggestions = true)}
        onblur={() => setTimeout(() => (showSuggestions = false), 200)}
        onkeydown={handleKeyDown}
        disabled={loading}
        autocomplete="off"
      />
      {#if showSuggestions && metricSuggestions.length > 0}
        <ul
          class="menu bg-base-200 rounded-box absolute z-50 mt-1 max-h-60 w-full overflow-auto shadow-lg"
        >
          {#each metricSuggestions as suggestion}
            <li>
              <button
                type="button"
                class="w-full text-left"
                onclick={() => selectMetric(suggestion)}
              >
                {suggestion}
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>

  <!-- Time Range Picker -->
  <div class="form-control w-full">
    <div class="label">
      <span class="label-text">Time Range</span>
    </div>
    <div class="flex flex-wrap gap-2">
      {#each presets as preset}
        <button
          type="button"
          class="btn btn-sm"
          class:btn-active={selectedPreset === preset.value}
          onclick={() => (selectedPreset = preset.value)}
          disabled={loading}
        >
          {preset.label}
        </button>
      {/each}
    </div>

    {#if selectedPreset === "custom"}
      <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label class="form-control w-full">
          <div class="label">
            <span class="label-text">Start Time</span>
          </div>
          <input
            type="datetime-local"
            class="input input-bordered input-sm w-full"
            bind:value={customStartTime}
            disabled={loading}
          />
        </label>
        <label class="form-control w-full">
          <div class="label">
            <span class="label-text">End Time</span>
          </div>
          <input
            type="datetime-local"
            class="input input-bordered input-sm w-full"
            bind:value={customEndTime}
            disabled={loading}
          />
        </label>
      </div>
    {/if}
  </div>

  <!-- Tags Section -->
  <div class="form-control w-full">
    <div class="label">
      <span class="label-text">Tag Filters</span>
      <span class="label-text-alt">{tags.length} active</span>
    </div>

    {#if tags.length > 0}
      <div class="mb-2 flex flex-wrap gap-2">
        {#each tags as tag, index}
          <div class="badge badge-lg gap-2">
            <span class="font-mono text-sm">{tag.key}={tag.value}</span>
            <button
              type="button"
              class="btn btn-ghost btn-xs"
              onclick={() => removeTag(index)}
              disabled={loading}
              aria-label="Remove tag"
            >
              <svg
                xmlns="http://www.w3.org/2000/svg"
                class="h-4 w-4"
                viewBox="0 0 20 20"
                fill="currentColor"
              >
                <path
                  fill-rule="evenodd"
                  d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z"
                  clip-rule="evenodd"
                />
              </svg>
            </button>
          </div>
        {/each}
      </div>
    {/if}

    <div class="flex gap-2">
      <input
        type="text"
        class="input input-bordered input-sm flex-1"
        placeholder="Key"
        bind:value={newTagKey}
        onkeydown={handleTagKeyDown}
        disabled={loading}
      />
      <span class="self-center">=</span>
      <input
        type="text"
        class="input input-bordered input-sm flex-1"
        placeholder="Value"
        bind:value={newTagValue}
        onkeydown={handleTagKeyDown}
        disabled={loading}
      />
      <button
        type="button"
        class="btn btn-sm btn-ghost"
        onclick={addTag}
        disabled={loading || !newTagKey.trim() || !newTagValue.trim()}
      >
        Add
      </button>
    </div>
  </div>

  <!-- Aggregation -->
  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
    <label class="form-control w-full">
      <div class="label">
        <span class="label-text">Aggregation</span>
      </div>
      <select
        class="select select-bordered w-full"
        bind:value={aggregation}
        disabled={loading}
      >
        {#each aggregations as agg}
          <option value={agg.value}>{agg.label}</option>
        {/each}
      </select>
    </label>

    {#if aggregation !== "none"}
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Interval</span>
        </div>
        <select
          class="select select-bordered w-full"
          bind:value={interval}
          disabled={loading}
        >
          {#each intervalOptions as opt}
            <option value={opt}>{opt}</option>
          {/each}
        </select>
      </label>
    {/if}
  </div>

  <!-- Action Buttons -->
  <div class="flex gap-2">
    <button
      type="button"
      class="btn btn-primary flex-1"
      disabled={!canSearch || loading}
      onclick={handleSearch}
    >
      {#if loading}
        <span class="loading loading-spinner loading-sm"></span>
      {/if}
      Search
    </button>
    {#if hasValues}
      <button
        type="button"
        class="btn btn-ghost"
        disabled={loading}
        onclick={handleClear}
      >
        Clear
      </button>
    {/if}
  </div>
</div>
