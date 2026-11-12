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
  import type { SearchMode, SearchOptions, KVSearchEvent } from "./types.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    loading = false,
    onSearch,
    onClear,
  }: {
    loading?: boolean;
    onSearch: (event: KVSearchEvent) => void;
    onClear?: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let searchMode = $state<SearchMode>("prefix");
  let showOptions = $state(false);

  // Prefix mode state
  let prefix = $state("");

  // Range mode state
  let startKey = $state("");
  let endKey = $state("");

  // Exact mode state
  let exactKey = $state("");

  // Common options
  let limit = $state(100);
  let caseSensitive = $state(false);

  // MVCC options
  let revision = $state<number | undefined>(undefined);
  let revisionInput = $state("");
  let includeDeleted = $state(false);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const canSearch = $derived.by(() => {
    switch (searchMode) {
      case "prefix":
        return prefix.trim().length > 0;
      case "range":
        return startKey.trim().length > 0 || endKey.trim().length > 0;
      case "exact":
        return exactKey.trim().length > 0;
      default:
        return false;
    }
  });

  const hasValues = $derived.by(() => {
    return (
      prefix.length > 0 ||
      startKey.length > 0 ||
      endKey.length > 0 ||
      exactKey.length > 0
    );
  });

  // ---------------------------------------------------------------------------
  // Effects
  // ---------------------------------------------------------------------------

  $effect(() => {
    const parsed = parseInt(revisionInput, 10);
    revision = isNaN(parsed) || parsed < 0 ? undefined : parsed;
  });

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  function handleSearch(): void {
    if (!canSearch || loading) return;

    let searchPrefix: string | undefined;
    let searchStartKey: string | undefined;
    let searchEndKey: string | undefined;

    switch (searchMode) {
      case "prefix":
        searchPrefix = prefix.trim();
        break;
      case "range":
        if (startKey.trim()) searchStartKey = startKey.trim();
        if (endKey.trim()) searchEndKey = endKey.trim();
        break;
      case "exact":
        searchPrefix = exactKey.trim();
        break;
    }

    const options: SearchOptions = {
      prefix: searchPrefix,
      startKey: searchStartKey,
      endKey: searchEndKey,
      limit,
      caseSensitive,
      revision,
      includeDeleted,
    };

    onSearch({ mode: searchMode, options });
  }

  function handleClear(): void {
    prefix = "";
    startKey = "";
    endKey = "";
    exactKey = "";
    limit = 100;
    caseSensitive = false;
    revisionInput = "";
    revision = undefined;
    includeDeleted = false;
    onClear?.();
  }

  function handleKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter" && canSearch && !loading) {
      handleSearch();
    }
  }

  function setMode(mode: SearchMode): void {
    searchMode = mode;
  }
</script>

<div class="engine-kv flex flex-col gap-4">
  <!-- Search Mode Toggle -->
  <div class="join w-full">
    <button
      type="button"
      class="join-item btn btn-sm flex-1"
      class:btn-active={searchMode === "prefix"}
      onclick={() => setMode("prefix")}
    >
      Prefix
    </button>
    <button
      type="button"
      class="join-item btn btn-sm flex-1"
      class:btn-active={searchMode === "range"}
      onclick={() => setMode("range")}
    >
      Range
    </button>
    <button
      type="button"
      class="join-item btn btn-sm flex-1"
      class:btn-active={searchMode === "exact"}
      onclick={() => setMode("exact")}
    >
      Exact
    </button>
  </div>

  <!-- Search Inputs -->
  <div class="flex flex-col gap-3">
    {#if searchMode === "prefix"}
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Key Prefix</span>
        </div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="Enter key prefix..."
          bind:value={prefix}
          onkeydown={handleKeyDown}
          disabled={loading}
        />
      </label>
    {:else if searchMode === "range"}
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label class="form-control w-full">
          <div class="label">
            <span class="label-text">Start Key</span>
            <span class="label-text-alt">inclusive</span>
          </div>
          <input
            type="text"
            class="input input-bordered w-full"
            placeholder="Start key..."
            bind:value={startKey}
            onkeydown={handleKeyDown}
            disabled={loading}
          />
        </label>
        <label class="form-control w-full">
          <div class="label">
            <span class="label-text">End Key</span>
            <span class="label-text-alt">exclusive</span>
          </div>
          <input
            type="text"
            class="input input-bordered w-full"
            placeholder="End key..."
            bind:value={endKey}
            onkeydown={handleKeyDown}
            disabled={loading}
          />
        </label>
      </div>
    {:else if searchMode === "exact"}
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Exact Key</span>
        </div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="Enter exact key..."
          bind:value={exactKey}
          onkeydown={handleKeyDown}
          disabled={loading}
        />
      </label>
    {/if}

    <!-- Limit -->
    <label class="form-control w-full max-w-xs">
      <div class="label">
        <span class="label-text">Limit</span>
      </div>
      <input
        type="number"
        class="input input-bordered w-full"
        placeholder="100"
        min="1"
        max="10000"
        bind:value={limit}
        disabled={loading}
      />
    </label>
  </div>

  <!-- Options Panel (Collapsible) -->
  <div class="collapse collapse-arrow bg-base-200 rounded-lg">
    <input type="checkbox" bind:checked={showOptions} class="peer" />
    <div class="collapse-title font-medium">Advanced Options</div>
    <div class="collapse-content flex flex-col gap-4">
      <!-- Case Sensitivity -->
      <label class="label cursor-pointer justify-start gap-3">
        <input
          type="checkbox"
          class="checkbox checkbox-sm"
          bind:checked={caseSensitive}
          disabled={loading}
        />
        <span class="label-text">Case sensitive</span>
      </label>

      <!-- MVCC Options Section -->
      <div class="divider text-sm">MVCC Options</div>

      <label class="form-control w-full max-w-xs">
        <div class="label">
          <span class="label-text">At Revision</span>
          <span class="label-text-alt">optional</span>
        </div>
        <input
          type="number"
          class="input input-bordered input-sm w-full"
          placeholder="Latest"
          min="0"
          bind:value={revisionInput}
          disabled={loading}
        />
      </label>

      <label class="label cursor-pointer justify-start gap-3">
        <input
          type="checkbox"
          class="checkbox checkbox-sm"
          bind:checked={includeDeleted}
          disabled={loading}
        />
        <span class="label-text">Include deleted keys</span>
      </label>
    </div>
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
