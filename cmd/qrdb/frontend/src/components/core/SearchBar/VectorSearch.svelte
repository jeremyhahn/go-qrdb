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
    VectorSearchOptions,
    VectorSearchEvent,
    VectorSearchMethod,
    DistanceMetric,
  } from "./types.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    loading = false,
    availableNamespaces = [],
    onSearch,
    onClear,
  }: {
    loading?: boolean;
    availableNamespaces?: string[];
    onSearch: (event: VectorSearchEvent) => void;
    onClear?: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let namespace = $state("");
  let searchMethod = $state<VectorSearchMethod>("vector");

  // By Vector state
  let vectorInput = $state("");

  // By ID state
  let vectorId = $state("");

  // By Metadata state
  let metadataInput = $state("");
  let useJsonBuilder = $state(true);
  let metadataFields = $state<Array<{ key: string; value: string }>>([]);
  let newMetaKey = $state("");
  let newMetaValue = $state("");

  // Common options
  let topK = $state(10);
  let distanceMetric = $state<DistanceMetric>("cosine");

  // ---------------------------------------------------------------------------
  // Constants
  // ---------------------------------------------------------------------------

  const distanceMetrics: Array<{ value: DistanceMetric; label: string }> = [
    { value: "cosine", label: "Cosine Similarity" },
    { value: "euclidean", label: "Euclidean Distance" },
    { value: "dot", label: "Dot Product" },
  ];

  // ---------------------------------------------------------------------------
  // Pure parse helpers — no side effects, safe to call from $derived
  // ---------------------------------------------------------------------------

  function parseVector(input: string): {
    value: number[] | null;
    error: string;
  } {
    if (!input.trim()) return { value: null, error: "" };
    try {
      const values = input
        .split(",")
        .map((v) => v.trim())
        .filter((v) => v.length > 0)
        .map((v) => {
          const num = parseFloat(v);
          if (isNaN(num)) throw new Error(`Invalid number: ${v}`);
          return num;
        });
      if (values.length === 0) return { value: null, error: "" };
      return { value: values, error: "" };
    } catch (e) {
      return {
        value: null,
        error: e instanceof Error ? e.message : "Invalid vector format",
      };
    }
  }

  function parseMetadata(
    input: string,
    useBuilder: boolean,
    fields: Array<{ key: string; value: string }>,
  ): { value: Record<string, unknown> | null; error: string } {
    if (useBuilder) {
      const obj: Record<string, unknown> = {};
      for (const field of fields) {
        if (field.key.trim()) {
          try {
            obj[field.key] = JSON.parse(field.value);
          } catch {
            obj[field.key] = field.value;
          }
        }
      }
      return { value: Object.keys(obj).length > 0 ? obj : null, error: "" };
    }
    if (!input.trim()) return { value: null, error: "" };
    try {
      const parsed = JSON.parse(input);
      if (
        typeof parsed !== "object" ||
        parsed === null ||
        Array.isArray(parsed)
      ) {
        throw new Error("Metadata must be a JSON object");
      }
      return { value: parsed as Record<string, unknown>, error: "" };
    } catch (e) {
      return {
        value: null,
        error: e instanceof Error ? e.message : "Invalid JSON",
      };
    }
  }

  // ---------------------------------------------------------------------------
  // Derived — pure, no $state mutations
  // ---------------------------------------------------------------------------

  const _vectorParse = $derived(parseVector(vectorInput));
  const parsedVector = $derived(_vectorParse.value);
  const vectorError = $derived(_vectorParse.error);

  const _metadataParse = $derived(
    parseMetadata(metadataInput, useJsonBuilder, metadataFields),
  );
  const parsedMetadata = $derived(_metadataParse.value);
  const metadataError = $derived(_metadataParse.error);

  const canSearch = $derived.by(() => {
    if (!namespace.trim()) return false;

    switch (searchMethod) {
      case "vector":
        return parsedVector !== null && parsedVector.length > 0;
      case "id":
        return vectorId.trim().length > 0;
      case "metadata":
        return parsedMetadata !== null;
      default:
        return false;
    }
  });

  const hasValues = $derived.by(() => {
    return (
      namespace.length > 0 ||
      vectorInput.length > 0 ||
      vectorId.length > 0 ||
      metadataInput.length > 0 ||
      metadataFields.length > 0
    );
  });

  const vectorDimensions = $derived(parsedVector?.length ?? 0);

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  function handleSearch(): void {
    if (!canSearch || loading) return;

    const options: VectorSearchOptions = {
      namespace: namespace.trim(),
      topK,
      distanceMetric,
    };

    switch (searchMethod) {
      case "vector":
        if (parsedVector) options.vector = parsedVector;
        break;
      case "id":
        options.vectorId = vectorId.trim();
        break;
      case "metadata":
        if (parsedMetadata) options.metadataFilter = parsedMetadata;
        break;
    }

    onSearch({ method: searchMethod, options });
  }

  function handleClear(): void {
    namespace = "";
    vectorInput = "";
    vectorId = "";
    metadataInput = "";
    metadataFields = [];
    newMetaKey = "";
    newMetaValue = "";
    topK = 10;
    distanceMetric = "cosine";
    onClear?.();
  }

  function handleKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter" && canSearch && !loading) {
      handleSearch();
    }
  }

  function setSearchMethod(method: VectorSearchMethod): void {
    searchMethod = method;
  }

  function addMetadataField(): void {
    if (newMetaKey.trim()) {
      metadataFields = [
        ...metadataFields,
        { key: newMetaKey.trim(), value: newMetaValue },
      ];
      newMetaKey = "";
      newMetaValue = "";
    }
  }

  function removeMetadataField(index: number): void {
    metadataFields = metadataFields.filter((_, i) => i !== index);
  }

  function handleMetaKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      addMetadataField();
    }
  }
</script>

<div class="engine-vectors flex flex-col gap-4">
  <!-- Namespace Selector -->
  <label class="form-control w-full">
    <div class="label">
      <span class="label-text">Namespace</span>
    </div>
    {#if availableNamespaces.length > 0}
      <select
        class="select select-bordered w-full"
        bind:value={namespace}
        disabled={loading}
      >
        <option value="" disabled>Select a namespace...</option>
        {#each availableNamespaces as ns}
          <option value={ns}>{ns}</option>
        {/each}
      </select>
    {:else}
      <input
        type="text"
        class="input input-bordered w-full"
        placeholder="Enter namespace..."
        bind:value={namespace}
        onkeydown={handleKeyDown}
        disabled={loading}
      />
    {/if}
  </label>

  <!-- Search Method Tabs -->
  <div role="tablist" class="tabs tabs-bordered">
    <button
      type="button"
      role="tab"
      class="tab"
      class:tab-active={searchMethod === "vector"}
      onclick={() => setSearchMethod("vector")}
    >
      By Vector
    </button>
    <button
      type="button"
      role="tab"
      class="tab"
      class:tab-active={searchMethod === "id"}
      onclick={() => setSearchMethod("id")}
    >
      By ID
    </button>
    <button
      type="button"
      role="tab"
      class="tab"
      class:tab-active={searchMethod === "metadata"}
      onclick={() => setSearchMethod("metadata")}
    >
      By Metadata
    </button>
  </div>

  <!-- Search Method Content -->
  <div class="min-h-32">
    {#if searchMethod === "vector"}
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Vector Values</span>
          {#if vectorDimensions > 0}
            <span class="label-text-alt badge badge-sm">
              {vectorDimensions} dimensions
            </span>
          {/if}
        </div>
        <textarea
          class="textarea textarea-bordered h-24 font-mono text-sm"
          class:textarea-error={vectorError.length > 0}
          placeholder="Enter comma-separated values, e.g.: 0.1, 0.5, -0.3, 0.8"
          bind:value={vectorInput}
          disabled={loading}></textarea>
        {#if vectorError}
          <div class="label">
            <span class="label-text-alt text-error">{vectorError}</span>
          </div>
        {:else}
          <div class="label">
            <span class="label-text-alt">
              Comma-separated floating point numbers
            </span>
          </div>
        {/if}
      </label>
    {:else if searchMethod === "id"}
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Vector ID</span>
        </div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="Enter vector ID..."
          bind:value={vectorId}
          onkeydown={handleKeyDown}
          disabled={loading}
        />
      </label>
    {:else if searchMethod === "metadata"}
      <div class="flex flex-col gap-3">
        <div class="flex items-center gap-2">
          <span class="label-text">Input Mode:</span>
          <div class="join">
            <button
              type="button"
              class="join-item btn btn-sm"
              class:btn-active={useJsonBuilder}
              onclick={() => (useJsonBuilder = true)}
            >
              Builder
            </button>
            <button
              type="button"
              class="join-item btn btn-sm"
              class:btn-active={!useJsonBuilder}
              onclick={() => (useJsonBuilder = false)}
            >
              JSON
            </button>
          </div>
        </div>

        {#if useJsonBuilder}
          <!-- Key-Value Builder -->
          {#if metadataFields.length > 0}
            <div class="flex flex-col gap-2">
              {#each metadataFields as field, index}
                <div class="flex items-center gap-2">
                  <input
                    type="text"
                    class="input input-bordered input-sm flex-1 font-mono"
                    value={field.key}
                    disabled
                  />
                  <span>:</span>
                  <input
                    type="text"
                    class="input input-bordered input-sm flex-1 font-mono"
                    value={field.value}
                    disabled
                  />
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm btn-square"
                    onclick={() => removeMetadataField(index)}
                    disabled={loading}
                    aria-label="Remove field"
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

          <div class="flex items-center gap-2">
            <input
              type="text"
              class="input input-bordered input-sm flex-1"
              placeholder="Key"
              bind:value={newMetaKey}
              onkeydown={handleMetaKeyDown}
              disabled={loading}
            />
            <span>:</span>
            <input
              type="text"
              class="input input-bordered input-sm flex-1"
              placeholder="Value"
              bind:value={newMetaValue}
              onkeydown={handleMetaKeyDown}
              disabled={loading}
            />
            <button
              type="button"
              class="btn btn-sm btn-ghost"
              onclick={addMetadataField}
              disabled={loading || !newMetaKey.trim()}
            >
              Add
            </button>
          </div>
        {:else}
          <!-- JSON Input -->
          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Metadata Filter (JSON)</span>
            </div>
            <textarea
              class="textarea textarea-bordered h-24 font-mono text-sm"
              class:textarea-error={metadataError.length > 0}
              placeholder={`{"field": "value", "count": 5}`}
              bind:value={metadataInput}
              disabled={loading}></textarea>
            {#if metadataError}
              <div class="label">
                <span class="label-text-alt text-error">{metadataError}</span>
              </div>
            {/if}
          </label>
        {/if}
      </div>
    {/if}
  </div>

  <!-- Common Options -->
  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
    <label class="form-control w-full">
      <div class="label">
        <span class="label-text">Top K</span>
      </div>
      <input
        type="number"
        class="input input-bordered w-full"
        min="1"
        max="1000"
        bind:value={topK}
        disabled={loading}
      />
    </label>

    <label class="form-control w-full">
      <div class="label">
        <span class="label-text">Distance Metric</span>
      </div>
      <select
        class="select select-bordered w-full"
        bind:value={distanceMetric}
        disabled={loading}
      >
        {#each distanceMetrics as metric}
          <option value={metric.value}>{metric.label}</option>
        {/each}
      </select>
    </label>
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
