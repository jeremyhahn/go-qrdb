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
  import type { Column, SortState, SelectionMode } from "./types.ts";
  import { ChevronUp, ChevronDown, ArrowUpDown, Filter } from "lucide-svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    columns,
    sortState = [],
    columnWidths = {},
    selectable = false,
    selectionMode = "none",
    allSelected = false,
    someSelected = false,
    onsort,
    onresize,
    onreorder,
    onselectall,
    onfiltertoggle,
  }: {
    columns: Column<T>[];
    sortState?: SortState[];
    columnWidths?: Record<string, string>;
    selectable?: boolean;
    selectionMode?: SelectionMode;
    allSelected?: boolean;
    someSelected?: boolean;
    onsort?: (columnId: string, multiSort: boolean) => void;
    onresize?: (columnId: string, width: string) => void;
    onreorder?: (fromIndex: number, toIndex: number) => void;
    onselectall?: () => void;
    onfiltertoggle?: (columnId: string) => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let draggedColumnId = $state<string | null>(null);
  let dragOverColumnId = $state<string | null>(null);
  let resizingColumnId = $state<string | null>(null);
  let resizeStartX = $state(0);
  let resizeStartWidth = $state(0);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Gets the sort state for a column. */
  function getSortInfo(
    columnId: string
  ): { direction: "asc" | "desc"; priority: number } | null {
    const index = sortState.findIndex((s) => s.columnId === columnId);
    if (index === -1) {
      return null;
    }
    return {
      direction: sortState[index]!.direction,
      priority: sortState.length > 1 ? index + 1 : 0,
    };
  }

  /** Gets the effective width for a column. */
  function getColumnWidth(column: Column<T>): string | undefined {
    return columnWidths[column.id] ?? column.width;
  }

  // ---------------------------------------------------------------------------
  // Sort Handlers
  // ---------------------------------------------------------------------------

  /** Handles click on a column header for sorting. */
  function handleSortClick(columnId: string, event: MouseEvent): void {
    const column = columns.find((c) => c.id === columnId);
    if (column === undefined || column.sortable === false) {
      return;
    }
    onsort?.(columnId, event.shiftKey);
  }

  // ---------------------------------------------------------------------------
  // Drag & Drop Handlers (Column Reorder)
  // ---------------------------------------------------------------------------

  /** Handles drag start for column reorder. */
  function handleDragStart(columnId: string, event: DragEvent): void {
    draggedColumnId = columnId;
    if (event.dataTransfer !== null) {
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", columnId);
    }
  }

  /** Handles drag over for column reorder. */
  function handleDragOver(columnId: string, event: DragEvent): void {
    event.preventDefault();
    if (draggedColumnId !== null && columnId !== draggedColumnId) {
      dragOverColumnId = columnId;
    }
  }

  /** Handles drag leave. */
  function handleDragLeave(): void {
    dragOverColumnId = null;
  }

  /** Handles drop for column reorder. */
  function handleDrop(columnId: string, event: DragEvent): void {
    event.preventDefault();
    if (
      draggedColumnId !== null &&
      draggedColumnId !== columnId &&
      onreorder !== undefined
    ) {
      const fromIndex = columns.findIndex((c) => c.id === draggedColumnId);
      const toIndex = columns.findIndex((c) => c.id === columnId);
      if (fromIndex !== -1 && toIndex !== -1) {
        onreorder(fromIndex, toIndex);
      }
    }
    draggedColumnId = null;
    dragOverColumnId = null;
  }

  /** Handles drag end. */
  function handleDragEnd(): void {
    draggedColumnId = null;
    dragOverColumnId = null;
  }

  // ---------------------------------------------------------------------------
  // Resize Handlers
  // ---------------------------------------------------------------------------

  /** Handles mouse down on resize handle. */
  function handleResizeStart(
    columnId: string,
    event: MouseEvent
  ): void {
    event.preventDefault();
    event.stopPropagation();

    resizingColumnId = columnId;
    resizeStartX = event.clientX;

    // Get current width
    const th = (event.target as HTMLElement).closest("th");
    if (th !== null) {
      resizeStartWidth = th.offsetWidth;
    }

    // Add global listeners
    window.addEventListener("mousemove", handleResizeMove);
    window.addEventListener("mouseup", handleResizeEnd);
  }

  /** Handles mouse move during resize. */
  function handleResizeMove(event: MouseEvent): void {
    if (resizingColumnId === null) {
      return;
    }

    const diff = event.clientX - resizeStartX;
    const newWidth = Math.max(50, resizeStartWidth + diff);
    onresize?.(resizingColumnId, `${newWidth}px`);
  }

  /** Handles mouse up to end resize. */
  function handleResizeEnd(): void {
    resizingColumnId = null;
    window.removeEventListener("mousemove", handleResizeMove);
    window.removeEventListener("mouseup", handleResizeEnd);
  }

  // ---------------------------------------------------------------------------
  // Select All Handler
  // ---------------------------------------------------------------------------

  /** Handles select all checkbox change. */
  function handleSelectAllChange(): void {
    onselectall?.();
  }
</script>

<thead class="bg-base-100 sticky top-0 z-10 border-b border-base-content/6">
  <tr>
    <!-- Select all checkbox -->
    {#if selectable && selectionMode !== "none"}
      <th class="w-10 px-3 py-2">
        {#if selectionMode === "multiple"}
          <input
            type="checkbox"
            class="checkbox checkbox-sm checkbox-primary"
            checked={allSelected}
            indeterminate={someSelected}
            onchange={handleSelectAllChange}
            aria-label="Select all rows"
          />
        {/if}
      </th>
    {/if}

    <!-- Column headers -->
    {#each columns as column (column.id)}
      {@const sortInfo = getSortInfo(column.id)}
      {@const width = getColumnWidth(column)}
      {@const isSortable = column.sortable !== false}
      {@const isFilterable = column.filterable === true}
      {@const isDraggedOver = dragOverColumnId === column.id}
      {@const isDragging = draggedColumnId === column.id}

      <th
        class="relative select-none px-3 py-2 text-left text-xs text-base-content/50 uppercase tracking-wider font-medium transition-colors
               {isSortable ? 'cursor-pointer hover:bg-base-300' : ''}
               {isDraggedOver ? 'bg-primary/20' : ''}
               {isDragging ? 'opacity-50' : ''}"
        style={width !== undefined ? `width: ${width}` : ""}
        data-column-id={column.id}
        draggable={true}
        ondragstart={(e) => handleDragStart(column.id, e)}
        ondragover={(e) => handleDragOver(column.id, e)}
        ondragleave={handleDragLeave}
        ondrop={(e) => handleDrop(column.id, e)}
        ondragend={handleDragEnd}
        onclick={(e) => handleSortClick(column.id, e)}
        role={isSortable ? "columnheader" : undefined}
        aria-sort={sortInfo !== null
          ? sortInfo.direction === "asc"
            ? "ascending"
            : "descending"
          : undefined}
      >
        <div class="flex items-center gap-1">
          <!-- Column header text -->
          <span class="flex-1 overflow-hidden text-ellipsis whitespace-nowrap">
            {column.header}
          </span>

          <!-- Sort indicator -->
          {#if sortInfo !== null}
            <span class="flex items-center gap-0.5">
              {#if sortInfo.direction === "asc"}
                <ChevronUp class="h-4 w-4 text-primary" />
              {:else}
                <ChevronDown class="h-4 w-4 text-primary" />
              {/if}
              {#if sortInfo.priority > 0}
                <span class="text-primary text-xs font-bold">
                  {sortInfo.priority}
                </span>
              {/if}
            </span>
          {:else if isSortable}
            <span class="h-4 w-4 opacity-0 transition-opacity group-hover:opacity-30">
              <ArrowUpDown class="h-4 w-4" />
            </span>
          {/if}

          <!-- Filter toggle button -->
          {#if isFilterable}
            <button
              type="button"
              class="btn btn-ghost btn-xs opacity-50 hover:opacity-100"
              onclick={(e) => {
                e.stopPropagation();
                onfiltertoggle?.(column.id);
              }}
              title="Filter this column"
            >
              <Filter class="h-3.5 w-3.5" />
            </button>
          {/if}
        </div>

        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <!-- Resize handle (mouse-only interaction delegated to window listeners) -->
        <div
          class="absolute bottom-0 right-0 top-0 w-1 cursor-col-resize bg-transparent hover:bg-primary/50"
          onmousedown={(e) => handleResizeStart(column.id, e)}
          role="separator"
          aria-orientation="vertical"
        ></div>
      </th>
    {/each}
  </tr>
</thead>
