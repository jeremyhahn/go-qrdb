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
  import type { Column, SelectionMode } from "./types.ts";
  import DataTableCell from "./DataTableCell.svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    row,
    index,
    columns,
    columnWidths = {},
    selected = false,
    selectable = false,
    selectionMode = "none",
    rowHeight = undefined,
    onclick = undefined,
    onselect = undefined,
    ondblclick = undefined,
  }: {
    row: T;
    index: number;
    columns: Column<T>[];
    columnWidths?: Record<string, string> | undefined;
    selected?: boolean | undefined;
    selectable?: boolean | undefined;
    selectionMode?: SelectionMode | undefined;
    rowHeight?: number | undefined;
    onclick?: ((index: number, event: MouseEvent) => void) | undefined;
    onselect?: ((index: number, multi: boolean) => void) | undefined;
    ondblclick?: ((index: number, event: MouseEvent) => void) | undefined;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let isHovered = $state(false);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Row class based on state. */
  const rowClass = $derived.by(() => {
    const classes = ["transition-colors"];

    if (selected) {
      classes.push("bg-primary/8");
    } else if (isHovered) {
      classes.push("bg-base-content/3");
    }

    if (selectable) {
      classes.push("cursor-pointer");
    }

    return classes.join(" ");
  });

  /** Row style for fixed height. */
  const rowStyle = $derived(
    rowHeight !== undefined ? `height: ${rowHeight}px` : ""
  );

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  /** Gets the cell value from a row using the column accessor. */
  function getCellValue(row: T, column: Column<T>): unknown {
    if (typeof column.accessor === "function") {
      return column.accessor(row);
    }
    return row[column.accessor];
  }

  /** Gets the rendered value for a cell. */
  function getRenderedValue(
    value: unknown,
    row: T,
    column: Column<T>
  ): string | undefined {
    if (column.render !== undefined) {
      return column.render(value, row);
    }
    return undefined;
  }

  /** Gets the effective width for a column. */
  function getColumnWidth(column: Column<T>): string | undefined {
    return columnWidths[column.id] ?? column.width;
  }

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  /** Handles row click. */
  function handleClick(event: MouseEvent): void {
    onclick?.(index, event);
  }

  /** Handles checkbox change. */
  function handleCheckboxChange(event: Event): void {
    event.stopPropagation();
    // Checkboxes accumulate: toggle this row's membership without clearing
    // other selected rows. Single-selection mode still replaces because
    // selectRow enforces it regardless of the multi flag.
    onselect?.(index, true);
  }

  /** Handles row double-click. */
  function handleDblClick(event: MouseEvent): void {
    ondblclick?.(index, event);
  }

  /** Handles keyboard events on the row. */
  function handleKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onclick?.(index, event as unknown as MouseEvent);
    }
  }
</script>

<tr
  class={rowClass}
  style={rowStyle}
  onclick={handleClick}
  ondblclick={handleDblClick}
  onkeydown={handleKeyDown}
  onmouseenter={() => {
    isHovered = true;
  }}
  onmouseleave={() => {
    isHovered = false;
  }}
  tabindex={selectable ? 0 : -1}
  role={selectable ? "row" : undefined}
  aria-selected={selectable ? selected : undefined}
  data-index={index}
>
  <!-- Selection checkbox -->
  {#if selectable && selectionMode !== "none"}
    <td class="w-10 px-3 py-2">
      <input
        type="checkbox"
        class="checkbox checkbox-sm checkbox-primary"
        checked={selected}
        onchange={handleCheckboxChange}
        onclick={(e) => e.stopPropagation()}
        aria-label="Select row {index + 1}"
      />
    </td>
  {/if}

  <!-- Data cells -->
  {#each columns as column (column.id)}
    {@const value = getCellValue(row, column)}
    {@const rendered = getRenderedValue(value, row, column)}
    <DataTableCell
      {value}
      {rendered}
      align={column.align}
      width={getColumnWidth(column)}
      minWidth={column.minWidth}
      maxWidth={column.maxWidth}
    />
  {/each}
</tr>
