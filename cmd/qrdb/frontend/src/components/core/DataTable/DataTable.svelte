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
    SelectionMode,
    VirtualScrollConfig,
    ExportConfig,
    RowClickEvent,
    SelectionChangeEvent,
    SortChangeEvent,
    FilterChangeEvent,
    PaginationChangeEvent,
  } from "./types.ts";
  import { useDataTable } from "./useDataTable.svelte.ts";
  import DataTableHeader from "./DataTableHeader.svelte";
  import DataTableRow from "./DataTableRow.svelte";
  import DataTableFooter from "./DataTableFooter.svelte";
  import DataTableFilters from "./DataTableFilters.svelte";
  import { untrack } from "svelte";
  import { Inbox } from "lucide-svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    data,
    columns,
    loading = false,
    emptyMessage = "No data available",
    selectable = false,
    selectionMode = "none",
    exportable = false,
    exportFilename = "data-export",
    pageSize = 25,
    pageSizeOptions = [10, 25, 50, 100, -1],
    virtualScroll = undefined,
    rowKey = undefined,
    striped = false,
    bordered = false,
    compact = false,
    containerClass = "",
    tableClass = "",
    onrowclick = undefined,
    onrowdblclick = undefined,
    onselectionchange = undefined,
    onsortchange = undefined,
    onfilterchange = undefined,
    onpaginationchange = undefined,
  }: {
    data: T[];
    columns: Column<T>[];
    loading?: boolean | undefined;
    emptyMessage?: string | undefined;
    selectable?: boolean | undefined;
    selectionMode?: SelectionMode | undefined;
    exportable?: boolean | undefined;
    exportFilename?: string | undefined;
    pageSize?: number | undefined;
    pageSizeOptions?: number[] | undefined;
    virtualScroll?: VirtualScrollConfig | undefined;
    rowKey?: (keyof T | ((row: T) => string | number)) | undefined;
    striped?: boolean | undefined;
    bordered?: boolean | undefined;
    compact?: boolean | undefined;
    containerClass?: string | undefined;
    tableClass?: string | undefined;
    onrowclick?: ((event: RowClickEvent<T>) => void) | undefined;
    onrowdblclick?: ((event: RowClickEvent<T>) => void) | undefined;
    onselectionchange?: ((event: SelectionChangeEvent) => void) | undefined;
    onsortchange?: ((event: SortChangeEvent) => void) | undefined;
    onfilterchange?: ((event: FilterChangeEvent) => void) | undefined;
    onpaginationchange?: ((event: PaginationChangeEvent) => void) | undefined;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  // Initialize table with useDataTable composable.
  // Props are read inside untrack() because useDataTable captures initial values only;
  // reactive updates are handled via table.setData() in the $effect below.
  const table = untrack(() => useDataTable({
    data,
    columns,
    pageSize,
    selectionMode: selectable ? selectionMode : "none",
    virtualScroll,
    rowKey,
  }));

  let containerRef: HTMLDivElement | null = $state(null);
  let activeFilterColumnId = $state<string | null>(null);
  let filterAnchorRect = $state<DOMRect | undefined>(undefined);
  let focusedRowIndex = $state<number | null>(null);
  let lastShiftClickIndex = $state<number | null>(null);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Table CSS classes. */
  const tableClasses = $derived.by(() => {
    const classes = ["table", "w-full"];
    if (striped) classes.push("table-zebra");
    if (compact) classes.push("table-sm");
    if (bordered) classes.push("table-bordered");
    if (tableClass) classes.push(tableClass);
    return classes.join(" ");
  });

  /** Container styles for virtual scroll. */
  const containerStyle = $derived.by(() => {
    if (virtualScroll === undefined) {
      return "";
    }
    return `height: ${virtualScroll.containerHeight}px; overflow-y: auto;`;
  });

  /** Spacer height for virtual scroll. */
  const spacerTopStyle = $derived.by(() => {
    if (virtualScroll === undefined) {
      return "";
    }
    return `height: ${table.virtualScroll.offsetY}px;`;
  });

  /** Total height style for virtual scroll. */
  const spacerBottomStyle = $derived.by(() => {
    if (virtualScroll === undefined) {
      return "";
    }
    const bottomHeight =
      table.virtualScroll.totalHeight -
      table.virtualScroll.offsetY -
      table.visibleData.length * virtualScroll.rowHeight;
    return `height: ${Math.max(0, bottomHeight)}px;`;
  });

  /** Gets active filter column. */
  const activeFilterColumn = $derived(
    activeFilterColumnId !== null
      ? columns.find((c) => c.id === activeFilterColumnId)
      : undefined
  );

  /** Gets current filter for active column. */
  const activeColumnFilter = $derived(
    activeFilterColumnId !== null
      ? table.filterState.find((f) => f.columnId === activeFilterColumnId)
      : undefined
  );

  /**
   * Columns ordered according to `table.columnOrder`, but derived from the
   * reactive `columns` prop so that dynamically added/removed columns are
   * always reflected. New columns that are not yet in `columnOrder` are
   * appended at the end.
   */
  const renderedColumns = $derived.by(() => {
    const orderMap = new Map(table.columnOrder.map((id: string, index: number) => [id, index]));
    return [...columns].sort((a, b) => {
      const indexA = orderMap.get(a.id) ?? Number.MAX_SAFE_INTEGER;
      const indexB = orderMap.get(b.id) ?? Number.MAX_SAFE_INTEGER;
      return indexA - indexB;
    });
  });

  // ---------------------------------------------------------------------------
  // Effects
  // ---------------------------------------------------------------------------

  // Update data when prop changes.
  // $effect.pre runs before the DOM is updated, ensuring table.visibleData
  // reflects the new prop value during the same render pass that triggered
  // the prop change. Using plain $effect would cause a one-render lag where
  // the internal data state is still stale during the first render with new
  // prop data, resulting in an empty table body despite data being present.
  $effect.pre(() => {
    table.setData(data);
  });

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  /** Handles sort change from header. */
  function handleSort(columnId: string, multiSort: boolean): void {
    table.toggleSort(columnId, multiSort);
    onsortchange?.({
      sortState: table.sortState,
      multiSort,
    });
  }

  /** Handles column resize from header. */
  function handleResize(columnId: string, width: string): void {
    table.setColumnWidth(columnId, width);
  }

  /** Handles column reorder from header. */
  function handleReorder(fromIndex: number, toIndex: number): void {
    table.reorderColumn(fromIndex, toIndex);
  }

  /** Handles select all from header. */
  function handleSelectAll(): void {
    table.toggleAll();
    onselectionchange?.({
      selectedRows: table.selectedRows,
      cleared: table.selectedRows.size === 0,
    });
  }

  /** Handles filter toggle from header. */
  function handleFilterToggle(columnId: string): void {
    if (activeFilterColumnId === columnId) {
      activeFilterColumnId = null;
      filterAnchorRect = undefined;
    } else {
      activeFilterColumnId = columnId;
      // Find the column header element to anchor the popup
      const th = containerRef?.querySelector(
        `th[data-column-id="${columnId}"]`
      ) as HTMLElement | null;
      if (th !== null) {
        filterAnchorRect = th.getBoundingClientRect();
      }
    }
  }

  /** Handles filter application. */
  function handleFilter(
    columnId: string,
    value: import("./types.ts").FilterValue,
    operator?: import("./types.ts").FilterOperator
  ): void {
    table.setFilter(columnId, value, operator);
    onfilterchange?.({
      filterState: table.filterState,
      columnId,
    });
  }

  /** Handles filter clear. */
  function handleFilterClear(columnId: string): void {
    table.clearFilter(columnId);
    onfilterchange?.({
      filterState: table.filterState,
      columnId,
    });
  }

  /** Handles filter close. */
  function handleFilterClose(): void {
    activeFilterColumnId = null;
    filterAnchorRect = undefined;
  }

  /** Handles row click. */
  function handleRowClick(index: number, event: MouseEvent): void {
    const row = table.visibleData[index];
    if (row === undefined) return;

    // Handle selection
    if (selectable && selectionMode !== "none") {
      if (event.shiftKey && selectionMode === "multiple" && lastShiftClickIndex !== null) {
        // Range selection
        const absoluteStart = virtualScroll !== undefined
          ? table.virtualScroll.startIndex + lastShiftClickIndex
          : (table.pagination.page - 1) * table.pagination.pageSize + lastShiftClickIndex;
        const absoluteEnd = virtualScroll !== undefined
          ? table.virtualScroll.startIndex + index
          : (table.pagination.page - 1) * table.pagination.pageSize + index;
        table.selectRange(absoluteStart, absoluteEnd);
      } else {
        const absoluteIndex = virtualScroll !== undefined
          ? table.virtualScroll.startIndex + index
          : (table.pagination.page - 1) * table.pagination.pageSize + index;
        table.toggleRow(absoluteIndex, event.ctrlKey || event.metaKey);
        lastShiftClickIndex = index;
      }

      onselectionchange?.({
        selectedRows: table.selectedRows,
        cleared: table.selectedRows.size === 0,
      });
    }

    // Find original index
    const originalIndex = data.indexOf(row);

    onrowclick?.({
      row,
      index,
      originalIndex,
    });
  }

  /** Handles row double-click. */
  function handleRowDblClick(index: number, _event: MouseEvent): void {
    const row = table.visibleData[index];
    if (row === undefined) return;

    const originalIndex = data.indexOf(row);

    onrowdblclick?.({
      row,
      index,
      originalIndex,
    });
  }

  /** Handles row selection from checkbox. */
  function handleRowSelect(index: number, multi: boolean): void {
    const absoluteIndex = virtualScroll !== undefined
      ? table.virtualScroll.startIndex + index
      : (table.pagination.page - 1) * table.pagination.pageSize + index;

    table.toggleRow(absoluteIndex, multi);
    lastShiftClickIndex = index;

    onselectionchange?.({
      selectedRows: table.selectedRows,
      cleared: table.selectedRows.size === 0,
    });
  }

  /** Handles page change. */
  function handlePageChange(page: number): void {
    table.setPage(page);
    onpaginationchange?.({
      pagination: table.pagination,
      changed: "page",
    });
  }

  /** Handles page size change. */
  function handlePageSizeChange(pageSize: number): void {
    table.setPageSize(pageSize);
    onpaginationchange?.({
      pagination: table.pagination,
      changed: "pageSize",
    });
  }

  /** Handles CSV export. */
  function handleExportCSV(): void {
    const config: ExportConfig = {
      filename: exportFilename,
      selectedOnly: table.hasSelection,
    };
    table.exportCSV(config);
  }

  /** Handles JSON export. */
  function handleExportJSON(): void {
    const config: ExportConfig = {
      filename: exportFilename,
      selectedOnly: table.hasSelection,
    };
    table.exportJSON(config);
  }

  /** Handles virtual scroll. */
  function handleScroll(event: Event): void {
    if (virtualScroll === undefined) return;

    const target = event.target as HTMLElement;
    table.handleScroll(target.scrollTop);
  }

  /** Handles keyboard navigation. */
  function handleKeyDown(event: KeyboardEvent): void {
    if (!selectable || table.visibleData.length === 0) return;

    const currentIndex = focusedRowIndex ?? 0;

    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        focusedRowIndex = Math.min(currentIndex + 1, table.visibleData.length - 1);
        focusRow(focusedRowIndex);
        break;

      case "ArrowUp":
        event.preventDefault();
        focusedRowIndex = Math.max(currentIndex - 1, 0);
        focusRow(focusedRowIndex);
        break;

      case "PageDown":
        event.preventDefault();
        focusedRowIndex = Math.min(currentIndex + 10, table.visibleData.length - 1);
        focusRow(focusedRowIndex);
        break;

      case "PageUp":
        event.preventDefault();
        focusedRowIndex = Math.max(currentIndex - 10, 0);
        focusRow(focusedRowIndex);
        break;

      case "Home":
        if (event.ctrlKey) {
          event.preventDefault();
          focusedRowIndex = 0;
          focusRow(focusedRowIndex);
        }
        break;

      case "End":
        if (event.ctrlKey) {
          event.preventDefault();
          focusedRowIndex = table.visibleData.length - 1;
          focusRow(focusedRowIndex);
        }
        break;

      case "Enter":
      case " ":
        if (focusedRowIndex !== null) {
          event.preventDefault();
          handleRowClick(focusedRowIndex, event as unknown as MouseEvent);
        }
        break;

      case "a":
        if ((event.ctrlKey || event.metaKey) && selectionMode === "multiple") {
          event.preventDefault();
          table.selectAll();
          onselectionchange?.({
            selectedRows: table.selectedRows,
            cleared: false,
          });
        }
        break;

      case "Escape":
        if (table.hasSelection) {
          event.preventDefault();
          table.deselectAll();
          onselectionchange?.({
            selectedRows: table.selectedRows,
            cleared: true,
          });
        }
        break;
    }
  }

  /** Focuses a row by index. */
  function focusRow(index: number): void {
    const row = containerRef?.querySelector(`tr[data-index="${index}"]`) as HTMLElement | null;
    row?.focus();

    // Scroll into view if using virtual scroll
    if (virtualScroll !== undefined) {
      table.scrollToRow(table.virtualScroll.startIndex + index);
    }
  }

  /** Checks if a row is selected. */
  function isRowSelected(visibleIndex: number): boolean {
    const absoluteIndex = virtualScroll !== undefined
      ? table.virtualScroll.startIndex + visibleIndex
      : (table.pagination.page - 1) * table.pagination.pageSize + visibleIndex;
    return table.selectedRows.has(absoluteIndex);
  }

  /** Gets the row key for rendering. */
  function getRowKey(row: T, index: number): string | number {
    if (rowKey === undefined) {
      return index;
    }
    if (typeof rowKey === "function") {
      return rowKey(row);
    }
    return row[rowKey] as string | number;
  }
</script>

<div
  bind:this={containerRef}
  class="bg-base-100 flex flex-col overflow-hidden rounded-lg shadow {containerClass}"
  role="grid"
  aria-label="Data table"
  tabindex="0"
  onkeydown={handleKeyDown}
>
  <!-- Table container -->
  <div
    class="relative flex-1 overflow-auto"
    style={containerStyle}
    onscroll={handleScroll}
  >
    <table class={tableClasses}>
      <!-- Header -->
      <DataTableHeader
        columns={renderedColumns}
        sortState={table.sortState}
        columnWidths={table.columnWidths}
        {selectable}
        {selectionMode}
        allSelected={table.allSelected}
        someSelected={table.someSelected}
        onsort={handleSort}
        onresize={handleResize}
        onreorder={handleReorder}
        onselectall={handleSelectAll}
        onfiltertoggle={handleFilterToggle}
      />

      <!-- Body -->
      <tbody>
        {#if loading}
          <!-- Loading skeleton -->
          {#each Array(5) as _, ri (ri)}
            <tr>
              {#if selectable && selectionMode !== "none"}
                <td class="w-10 px-3 py-2">
                  <div class="skeleton h-4 w-4 rounded"></div>
                </td>
              {/if}
              {#each columns as _col, ci (ci)}
                <td class="px-3 py-2">
                  <div class="skeleton h-4 w-full rounded"></div>
                </td>
              {/each}
            </tr>
          {/each}
        {:else if table.visibleData.length === 0}
          <!-- Empty state -->
          <tr>
            <td
              colspan={columns.length + (selectable && selectionMode !== "none" ? 1 : 0)}
              class="py-12 text-center"
            >
              <div class="flex flex-col items-center justify-center">
                <Inbox class="text-base-content/30 mb-4 h-12 w-12" strokeWidth={1.5} />
                <p class="text-base-content/50 text-sm">{emptyMessage}</p>
              </div>
            </td>
          </tr>
        {:else}
          <!-- Virtual scroll top spacer -->
          {#if virtualScroll !== undefined && table.virtualScroll.offsetY > 0}
            <tr>
              <td
                colspan={columns.length + (selectable && selectionMode !== "none" ? 1 : 0)}
                style={spacerTopStyle}
              ></td>
            </tr>
          {/if}

          <!-- Data rows -->
          {#each table.visibleData as row, index (getRowKey(row, index))}
            <DataTableRow
              {row}
              {index}
              columns={renderedColumns}
              columnWidths={table.columnWidths}
              selected={isRowSelected(index)}
              {selectable}
              {selectionMode}
              rowHeight={virtualScroll?.rowHeight}
              onclick={handleRowClick}
              onselect={handleRowSelect}
              ondblclick={handleRowDblClick}
            />
          {/each}

          <!-- Virtual scroll bottom spacer -->
          {#if virtualScroll !== undefined}
            <tr>
              <td
                colspan={columns.length + (selectable && selectionMode !== "none" ? 1 : 0)}
                style={spacerBottomStyle}
              ></td>
            </tr>
          {/if}
        {/if}
      </tbody>
    </table>
  </div>

  <!-- Footer with pagination -->
  {#if !loading && table.sortedData.length > 0}
    <DataTableFooter
      pagination={table.pagination}
      totalPages={table.totalPages}
      {exportable}
      {pageSizeOptions}
      onpagechange={handlePageChange}
      onpagesizechange={handlePageSizeChange}
      onexportcsv={handleExportCSV}
      onexportjson={handleExportJSON}
    />
  {/if}
</div>

<!-- Filter popup -->
{#if activeFilterColumn !== undefined}
  <DataTableFilters
    column={activeFilterColumn}
    currentFilter={activeColumnFilter}
    anchorRect={filterAnchorRect}
    onfilter={handleFilter}
    onclear={handleFilterClear}
    onclose={handleFilterClose}
  />
{/if}
