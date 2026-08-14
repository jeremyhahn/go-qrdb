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
<script lang="ts" generics="T">
  import type {
    Column,
    FilterState,
    FilterValue,
    FilterOperator,
    TextFilterOperator,
    FilterType,
  } from "./types.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    column,
    currentFilter = undefined,
    anchorRect = undefined,
    onfilter = undefined,
    onclear = undefined,
    onclose = undefined,
  }: {
    column: Column<T>;
    currentFilter?: FilterState | undefined;
    anchorRect?: DOMRect | undefined;
    onfilter?: ((
      columnId: string,
      value: FilterValue,
      operator?: FilterOperator
    ) => void) | undefined;
    onclear?: ((columnId: string) => void) | undefined;
    onclose?: (() => void) | undefined;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let textValue = $state("");
  let selectValue = $state("");
  let rangeMin = $state("");
  let rangeMax = $state("");
  let dateFrom = $state("");
  let dateTo = $state("");
  let textOperator = $state<TextFilterOperator>("contains");

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Filter type for this column. */
  const filterType = $derived<FilterType>(column.filterType ?? "text");

  /** Position styles for the popup. */
  const positionStyle = $derived.by(() => {
    if (anchorRect === undefined) {
      return "";
    }
    return `top: ${anchorRect.bottom + 4}px; left: ${anchorRect.left}px;`;
  });

  /** Whether the filter has a value set. */
  const hasValue = $derived.by(() => {
    switch (filterType) {
      case "text":
        return textValue.trim() !== "";
      case "select":
        return selectValue !== "";
      case "range":
        return rangeMin !== "" || rangeMax !== "";
      case "date":
        return dateFrom !== "" || dateTo !== "";
      default:
        return false;
    }
  });

  // ---------------------------------------------------------------------------
  // Initialization
  // ---------------------------------------------------------------------------

  $effect(() => {
    // Initialize from current filter
    if (currentFilter !== undefined) {
      const { value, operator } = currentFilter;

      if (typeof value === "string") {
        if (filterType === "text") {
          textValue = value;
          textOperator = (operator as TextFilterOperator) ?? "contains";
        } else if (filterType === "select") {
          selectValue = value;
        }
      } else if (Array.isArray(value)) {
        if (
          filterType === "range" &&
          typeof value[0] === "number" &&
          typeof value[1] === "number"
        ) {
          rangeMin = String(value[0]);
          rangeMax = String(value[1]);
        } else if (
          filterType === "date" &&
          value[0] instanceof Date &&
          value[1] instanceof Date
        ) {
          dateFrom = formatDateForInput(value[0]);
          dateTo = formatDateForInput(value[1]);
        }
      }
    }
  });

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  /** Formats a Date for datetime-local input. */
  function formatDateForInput(date: Date): string {
    return date.toISOString().slice(0, 16);
  }

  // ---------------------------------------------------------------------------
  // Handlers
  // ---------------------------------------------------------------------------

  /** Applies the current filter. */
  function applyFilter(): void {
    let value: FilterValue;
    let operator: FilterOperator | undefined;

    switch (filterType) {
      case "text":
        value = textValue.trim();
        operator = textOperator;
        break;

      case "select":
        value = selectValue;
        break;

      case "range": {
        const min = rangeMin !== "" ? parseFloat(rangeMin) : -Infinity;
        const max = rangeMax !== "" ? parseFloat(rangeMax) : Infinity;
        value = [min, max];
        operator = "between";
        break;
      }

      case "date": {
        const from = dateFrom !== "" ? new Date(dateFrom) : new Date(0);
        const to = dateTo !== "" ? new Date(dateTo) : new Date(8640000000000000);
        value = [from, to];
        operator = "between";
        break;
      }

      default:
        return;
    }

    onfilter?.(column.id, value, operator);
    onclose?.();
  }

  /** Clears the filter. */
  function clearFilter(): void {
    textValue = "";
    selectValue = "";
    rangeMin = "";
    rangeMax = "";
    dateFrom = "";
    dateTo = "";
    textOperator = "contains";
    onclear?.(column.id);
    onclose?.();
  }

  /** Handles backdrop click. */
  function handleBackdropClick(): void {
    onclose?.();
  }

  /** Handles key down for apply on Enter. */
  function handleKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      applyFilter();
    } else if (event.key === "Escape") {
      event.preventDefault();
      onclose?.();
    }
  }
</script>

<!-- Backdrop -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="fixed inset-0 z-40"
  onclick={handleBackdropClick}
></div>

<!-- Filter popup -->
<div
  class="bg-base-100 fixed z-50 min-w-64 rounded-lg p-4 shadow-xl"
  style={positionStyle}
  role="dialog"
  aria-label="Filter {column.header}"
>
  <div class="mb-3 flex items-center justify-between">
    <h3 class="font-semibold">Filter: {column.header}</h3>
    <button
      type="button"
      class="btn btn-ghost btn-xs"
      onclick={() => onclose?.()}
      aria-label="Close filter"
    >
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-4 w-4"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M6 18L18 6M6 6l12 12"
        />
      </svg>
    </button>
  </div>

  <!-- Text filter -->
  {#if filterType === "text"}
    <div class="space-y-2">
      <select
        class="select select-bordered select-sm w-full"
        bind:value={textOperator}
      >
        <option value="contains">Contains</option>
        <option value="equals">Equals</option>
        <option value="startsWith">Starts with</option>
        <option value="endsWith">Ends with</option>
      </select>
      <input
        type="text"
        class="input input-bordered input-sm w-full"
        placeholder="Enter filter value..."
        bind:value={textValue}
        onkeydown={handleKeyDown}
      />
    </div>
  {/if}

  <!-- Select filter -->
  {#if filterType === "select"}
    <select
      class="select select-bordered select-sm w-full"
      bind:value={selectValue}
    >
      <option value="">All values</option>
      {#if column.filterOptions !== undefined}
        {#each column.filterOptions as option}
          <option value={option}>{option}</option>
        {/each}
      {/if}
    </select>
  {/if}

  <!-- Range filter -->
  {#if filterType === "range"}
    <div class="space-y-2">
      <div class="flex items-center gap-2">
        <input
          type="number"
          class="input input-bordered input-sm w-full"
          placeholder="Min"
          bind:value={rangeMin}
          onkeydown={handleKeyDown}
        />
        <span class="text-base-content/50">to</span>
        <input
          type="number"
          class="input input-bordered input-sm w-full"
          placeholder="Max"
          bind:value={rangeMax}
          onkeydown={handleKeyDown}
        />
      </div>
    </div>
  {/if}

  <!-- Date filter -->
  {#if filterType === "date"}
    <div class="space-y-2">
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text text-xs">From</span>
        </div>
        <input
          type="datetime-local"
          class="input input-bordered input-sm w-full"
          bind:value={dateFrom}
          onkeydown={handleKeyDown}
        />
      </label>
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text text-xs">To</span>
        </div>
        <input
          type="datetime-local"
          class="input input-bordered input-sm w-full"
          bind:value={dateTo}
          onkeydown={handleKeyDown}
        />
      </label>
    </div>
  {/if}

  <!-- Actions -->
  <div class="mt-4 flex justify-end gap-2">
    <button
      type="button"
      class="btn btn-ghost btn-sm"
      onclick={clearFilter}
      disabled={!hasValue && currentFilter === undefined}
    >
      Clear
    </button>
    <button
      type="button"
      class="btn btn-primary btn-sm"
      onclick={applyFilter}
    >
      Apply
    </button>
  </div>
</div>
