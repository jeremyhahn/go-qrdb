// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * DataTable composable for Svelte 5.
 *
 * Provides comprehensive state management for sorting, filtering, pagination,
 * selection, column reordering, column resizing, virtual scrolling, and data export.
 */

import type {
    Column,
    SortState,
    SortDirection,
    FilterState,
    FilterValue,
    FilterOperator,
    PaginationState,
    SelectionMode,
    VirtualScrollConfig,
    VirtualScrollState,
    ExportConfig,
} from './types.ts';

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

/** Options for initializing the DataTable state. */
export interface UseDataTableOptions<T> {
    /** Initial data array. */
    readonly data: T[];
    /** Column definitions. */
    readonly columns: Column<T>[];
    /** Initial page size. Defaults to 25. */
    readonly pageSize?: number | undefined;
    /** Selection mode. Defaults to 'none'. */
    readonly selectionMode?: SelectionMode | undefined;
    /** Virtual scroll configuration. Required for large datasets. */
    readonly virtualScroll?: VirtualScrollConfig | undefined;
    /** Unique key accessor for stable row identity. */
    readonly rowKey?: (keyof T | ((row: T) => string | number)) | undefined;
}

// ---------------------------------------------------------------------------
// Return Type
// ---------------------------------------------------------------------------

/** Return type for the useDataTable composable. */
export interface UseDataTableReturn<T> {
    // Reactive state (read-only access)
    readonly sortState: SortState[];
    readonly filterState: FilterState[];
    readonly pagination: PaginationState;
    readonly selectedRows: Set<number>;
    readonly columnOrder: string[];
    readonly columnWidths: Record<string, string>;
    readonly virtualScroll: VirtualScrollState;

    // Derived data
    readonly filteredData: T[];
    readonly sortedData: T[];
    readonly paginatedData: T[];
    readonly visibleData: T[];
    readonly orderedColumns: Column<T>[];
    readonly totalPages: number;
    readonly hasSelection: boolean;
    readonly allSelected: boolean;
    readonly someSelected: boolean;
    readonly lastSelectedIndex: number | null;

    // Sort actions
    toggleSort: (columnId: string, multiSort?: boolean) => void;
    setSort: (columnId: string, direction: SortDirection) => void;
    clearSort: () => void;

    // Filter actions
    setFilter: (columnId: string, value: FilterValue, operator?: FilterOperator) => void;
    clearFilter: (columnId: string) => void;
    clearAllFilters: () => void;

    // Pagination actions
    setPage: (page: number) => void;
    setPageSize: (pageSize: number) => void;
    goToFirstPage: () => void;
    goToLastPage: () => void;
    goToNextPage: () => void;
    goToPreviousPage: () => void;

    // Selection actions
    selectRow: (index: number, multi?: boolean) => void;
    deselectRow: (index: number) => void;
    toggleRow: (index: number, multi?: boolean) => void;
    selectAll: () => void;
    deselectAll: () => void;
    toggleAll: () => void;
    selectRange: (startIndex: number, endIndex: number) => void;

    // Column actions
    reorderColumn: (fromIndex: number, toIndex: number) => void;
    setColumnWidth: (columnId: string, width: string) => void;
    resetColumnWidths: () => void;
    resetColumnOrder: () => void;

    // Virtual scroll actions
    handleScroll: (scrollTop: number) => void;
    scrollToRow: (index: number) => void;

    // Export actions
    exportCSV: (config?: ExportConfig) => void;
    exportJSON: (config?: ExportConfig) => void;

    // Data update
    setData: (data: T[]) => void;
}

// ---------------------------------------------------------------------------
// Implementation
// ---------------------------------------------------------------------------

/**
 * Creates a DataTable state manager using Svelte 5 runes.
 *
 * @typeParam T - The type of data row in the table.
 * @param options - Configuration options.
 * @returns State and action object for managing the DataTable.
 */
export function useDataTable<T>(options: UseDataTableOptions<T>): UseDataTableReturn<T> {
    const {
        columns,
        pageSize: initialPageSize = 25,
        selectionMode = 'none',
        virtualScroll: virtualScrollConfig,
        rowKey: _rowKey,
    } = options;
    // Note: rowKey is reserved for future row identity tracking
    void _rowKey;

    // ---------------------------------------------------------------------------
    // Core Reactive State
    // ---------------------------------------------------------------------------

    let data = $state<T[]>([...options.data]);
    let sortState = $state<SortState[]>([]);
    let filterState = $state<FilterState[]>([]);
    let pagination = $state<PaginationState>({
        page: 1,
        pageSize: initialPageSize,
        totalRows: options.data.length,
    });
    let selectedRows = $state<Set<number>>(new Set());
    let columnOrder = $state<string[]>(columns.map((c) => c.id));
    let columnWidths = $state<Record<string, string>>({});
    let virtualScroll = $state<VirtualScrollState>({
        startIndex: 0,
        endIndex: virtualScrollConfig
            ? Math.min(
                  Math.ceil(virtualScrollConfig.containerHeight / virtualScrollConfig.rowHeight) +
                      virtualScrollConfig.overscan * 2,
                  options.data.length
              )
            : options.data.length,
        scrollTop: 0,
        totalHeight: virtualScrollConfig
            ? options.data.length * virtualScrollConfig.rowHeight
            : 0,
        offsetY: 0,
    });

    // Track last selected index for range selection
    // Note: This is set when selecting rows for potential future Shift+click range selections
    // The value is tracked internally but not exposed in the return type
    let lastSelectedIndex: number | null = $state(null);

    // ---------------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------------

    /** Gets the value from a row using the column accessor. */
    function getCellValue(row: T, column: Column<T>): unknown {
        if (typeof column.accessor === 'function') {
            return column.accessor(row);
        }
        return row[column.accessor];
    }

    /** Compares two values for sorting. */
    function compareValues(a: unknown, b: unknown, direction: SortDirection): number {
        // Handle nulls and undefined
        if (a === null || a === undefined) {
            return direction === 'asc' ? 1 : -1;
        }
        if (b === null || b === undefined) {
            return direction === 'asc' ? -1 : 1;
        }

        let result: number;

        if (typeof a === 'number' && typeof b === 'number') {
            result = a - b;
        } else if (a instanceof Date && b instanceof Date) {
            result = a.getTime() - b.getTime();
        } else {
            result = String(a).localeCompare(String(b), undefined, {
                numeric: true,
                sensitivity: 'base',
            });
        }

        return direction === 'asc' ? result : -result;
    }

    /** Checks if a value matches a filter. */
    function matchesFilter(value: unknown, filter: FilterState): boolean {
        const { value: filterValue, operator = 'contains' } = filter;

        if (filterValue === '' || filterValue === null || filterValue === undefined) {
            return true;
        }

        // Handle string filters
        if (typeof filterValue === 'string') {
            const strValue = String(value).toLowerCase();
            const strFilter = filterValue.toLowerCase();

            switch (operator) {
                case 'equals':
                    return strValue === strFilter;
                case 'startsWith':
                    return strValue.startsWith(strFilter);
                case 'endsWith':
                    return strValue.endsWith(strFilter);
                case 'contains':
                default:
                    return strValue.includes(strFilter);
            }
        }

        // Handle range filters
        if (Array.isArray(filterValue) && filterValue.length === 2) {
            const numValue = typeof value === 'number' ? value : parseFloat(String(value));

            if (isNaN(numValue)) {
                return false;
            }

            // Check if it's a date range
            if (filterValue[0] instanceof Date && filterValue[1] instanceof Date) {
                const dateValue = value instanceof Date ? value : new Date(String(value));
                return dateValue >= filterValue[0] && dateValue <= filterValue[1];
            }

            // Numeric range
            const [min, max] = filterValue as [number, number];
            switch (operator) {
                case 'gt':
                    return numValue > min;
                case 'lt':
                    return numValue < max;
                case 'between':
                default:
                    return numValue >= min && numValue <= max;
            }
        }

        return true;
    }

    // ---------------------------------------------------------------------------
    // Derived Data (computed with $derived)
    // ---------------------------------------------------------------------------

    /** Data after applying filters. */
    const filteredData = $derived.by(() => {
        if (filterState.length === 0) {
            return data;
        }

        return data.filter((row) => {
            return filterState.every((filter) => {
                const column = columns.find((c) => c.id === filter.columnId);
                if (column === undefined) {
                    return true;
                }
                const value = getCellValue(row, column);
                return matchesFilter(value, filter);
            });
        });
    });

    /** Data after applying sorting. */
    const sortedData = $derived.by(() => {
        if (sortState.length === 0) {
            return filteredData;
        }

        // Create a shallow copy to avoid mutating filteredData
        return [...filteredData].sort((a, b) => {
            for (const sort of sortState) {
                const column = columns.find((c) => c.id === sort.columnId);
                if (column === undefined) {
                    continue;
                }

                const valueA = getCellValue(a, column);
                const valueB = getCellValue(b, column);
                const result = compareValues(valueA, valueB, sort.direction);

                if (result !== 0) {
                    return result;
                }
            }
            return 0;
        });
    });

    /** Data after applying pagination. */
    const paginatedData = $derived.by(() => {
        // When using virtual scroll, return all sorted data
        if (virtualScrollConfig !== undefined) {
            return sortedData;
        }

        // Handle "all" page size
        if (pagination.pageSize <= 0 || pagination.pageSize >= sortedData.length) {
            return sortedData;
        }

        const startIndex = (pagination.page - 1) * pagination.pageSize;
        const endIndex = startIndex + pagination.pageSize;
        return sortedData.slice(startIndex, endIndex);
    });

    /** Data visible in the viewport (for virtual scroll). */
    const visibleData = $derived.by(() => {
        if (virtualScrollConfig === undefined) {
            return paginatedData;
        }
        return paginatedData.slice(virtualScroll.startIndex, virtualScroll.endIndex);
    });

    /** Columns ordered according to columnOrder. */
    const orderedColumns = $derived.by(() => {
        const orderMap = new Map(columnOrder.map((id, index) => [id, index]));
        return [...columns].sort((a, b) => {
            const indexA = orderMap.get(a.id) ?? Number.MAX_SAFE_INTEGER;
            const indexB = orderMap.get(b.id) ?? Number.MAX_SAFE_INTEGER;
            return indexA - indexB;
        });
    });

    /** Total number of pages. */
    const totalPages = $derived(
        pagination.pageSize <= 0
            ? 1
            : Math.max(1, Math.ceil(filteredData.length / pagination.pageSize))
    );

    /** Whether any rows are selected. */
    const hasSelection = $derived(selectedRows.size > 0);

    /** Whether all rows in the current view are selected. */
    const allSelected = $derived(
        paginatedData.length > 0 &&
            paginatedData.every((_, index) => {
                const absoluteIndex =
                    virtualScrollConfig !== undefined
                        ? index
                        : (pagination.page - 1) * pagination.pageSize + index;
                return selectedRows.has(absoluteIndex);
            })
    );

    /** Whether some (but not all) rows are selected. */
    const someSelected = $derived(hasSelection && !allSelected);

    // ---------------------------------------------------------------------------
    // Effects
    // ---------------------------------------------------------------------------

    // Update pagination total when filtered data changes
    $effect(() => {
        pagination.totalRows = filteredData.length;

        // Ensure current page is valid
        const maxPage = Math.max(1, Math.ceil(filteredData.length / pagination.pageSize));
        if (pagination.page > maxPage) {
            pagination.page = maxPage;
        }
    });

    // Update virtual scroll state when data or config changes
    $effect(() => {
        if (virtualScrollConfig === undefined) {
            return;
        }

        virtualScroll.totalHeight = sortedData.length * virtualScrollConfig.rowHeight;
    });

    // ---------------------------------------------------------------------------
    // Sort Actions
    // ---------------------------------------------------------------------------

    /** Toggles sort on a column. Shift+click adds to multi-sort. */
    function toggleSort(columnId: string, multiSort = false): void {
        const column = columns.find((c) => c.id === columnId);
        if (column === undefined || column.sortable === false) {
            return;
        }

        const existingIndex = sortState.findIndex((s) => s.columnId === columnId);

        if (existingIndex === -1) {
            // Add new sort
            const newSort: SortState = { columnId, direction: 'asc' };
            if (multiSort) {
                sortState = [...sortState, newSort];
            } else {
                sortState = [newSort];
            }
        } else {
            const existing = sortState[existingIndex]!;

            if (existing.direction === 'asc') {
                // Toggle to descending
                const updated = { ...existing, direction: 'desc' as const };
                sortState = [
                    ...sortState.slice(0, existingIndex),
                    updated,
                    ...sortState.slice(existingIndex + 1),
                ];
            } else {
                // Remove sort
                sortState = [
                    ...sortState.slice(0, existingIndex),
                    ...sortState.slice(existingIndex + 1),
                ];
            }
        }
    }

    /** Sets sort on a column with a specific direction. */
    function setSort(columnId: string, direction: SortDirection): void {
        const column = columns.find((c) => c.id === columnId);
        if (column === undefined || column.sortable === false) {
            return;
        }

        const existingIndex = sortState.findIndex((s) => s.columnId === columnId);
        const newSort: SortState = { columnId, direction };

        if (existingIndex === -1) {
            sortState = [newSort];
        } else {
            sortState = [
                ...sortState.slice(0, existingIndex),
                newSort,
                ...sortState.slice(existingIndex + 1),
            ];
        }
    }

    /** Clears all sorting. */
    function clearSort(): void {
        sortState = [];
    }

    // ---------------------------------------------------------------------------
    // Filter Actions
    // ---------------------------------------------------------------------------

    /** Sets a filter on a column. */
    function setFilter(columnId: string, value: FilterValue, operator?: FilterOperator): void {
        const column = columns.find((c) => c.id === columnId);
        if (column === undefined || column.filterable !== true) {
            return;
        }

        const existingIndex = filterState.findIndex((f) => f.columnId === columnId);
        const newFilter: FilterState = { columnId, value, operator };

        if (existingIndex === -1) {
            filterState = [...filterState, newFilter];
        } else {
            filterState = [
                ...filterState.slice(0, existingIndex),
                newFilter,
                ...filterState.slice(existingIndex + 1),
            ];
        }

        // Reset to first page when filter changes
        pagination.page = 1;
    }

    /** Clears filter on a specific column. */
    function clearFilter(columnId: string): void {
        filterState = filterState.filter((f) => f.columnId !== columnId);
        pagination.page = 1;
    }

    /** Clears all filters. */
    function clearAllFilters(): void {
        filterState = [];
        pagination.page = 1;
    }

    // ---------------------------------------------------------------------------
    // Pagination Actions
    // ---------------------------------------------------------------------------

    /** Sets the current page. */
    function setPage(page: number): void {
        const validPage = Math.max(1, Math.min(page, totalPages));
        pagination.page = validPage;
    }

    /** Sets the page size. */
    function setPageSize(pageSize: number): void {
        pagination.pageSize = pageSize;
        pagination.page = 1;
    }

    /** Navigates to the first page. */
    function goToFirstPage(): void {
        pagination.page = 1;
    }

    /** Navigates to the last page. */
    function goToLastPage(): void {
        pagination.page = totalPages;
    }

    /** Navigates to the next page. */
    function goToNextPage(): void {
        if (pagination.page < totalPages) {
            pagination.page += 1;
        }
    }

    /** Navigates to the previous page. */
    function goToPreviousPage(): void {
        if (pagination.page > 1) {
            pagination.page -= 1;
        }
    }

    // ---------------------------------------------------------------------------
    // Selection Actions
    // ---------------------------------------------------------------------------

    /** Selects a row by index. */
    function selectRow(index: number, multi = false): void {
        if (selectionMode === 'none') {
            return;
        }

        if (selectionMode === 'single' || !multi) {
            selectedRows = new Set([index]);
        } else {
            const newSet = new Set(selectedRows);
            newSet.add(index);
            selectedRows = newSet;
        }

        lastSelectedIndex = index;
    }

    /** Deselects a row by index. */
    function deselectRow(index: number): void {
        const newSet = new Set(selectedRows);
        newSet.delete(index);
        selectedRows = newSet;
    }

    /** Toggles selection on a row. */
    function toggleRow(index: number, multi = false): void {
        if (selectionMode === 'none') {
            return;
        }

        if (selectedRows.has(index)) {
            deselectRow(index);
        } else {
            selectRow(index, multi);
        }
    }

    /** Selects all rows in the current view. */
    function selectAll(): void {
        if (selectionMode === 'none' || selectionMode === 'single') {
            return;
        }

        const newSet = new Set(selectedRows);
        const startIndex =
            virtualScrollConfig !== undefined ? 0 : (pagination.page - 1) * pagination.pageSize;
        const endIndex = startIndex + paginatedData.length;

        for (let i = startIndex; i < endIndex; i++) {
            newSet.add(i);
        }

        selectedRows = newSet;
    }

    /** Deselects all rows. */
    function deselectAll(): void {
        selectedRows = new Set();
        lastSelectedIndex = null;
    }

    /** Toggles selection on all rows in the current view. */
    function toggleAll(): void {
        if (allSelected) {
            deselectAll();
        } else {
            selectAll();
        }
    }

    /** Selects a range of rows (for Shift+click). */
    function selectRange(startIndex: number, endIndex: number): void {
        if (selectionMode !== 'multiple') {
            return;
        }

        const newSet = new Set(selectedRows);
        const min = Math.min(startIndex, endIndex);
        const max = Math.max(startIndex, endIndex);

        for (let i = min; i <= max; i++) {
            newSet.add(i);
        }

        selectedRows = newSet;
        lastSelectedIndex = endIndex;
    }

    // ---------------------------------------------------------------------------
    // Column Actions
    // ---------------------------------------------------------------------------

    /** Reorders a column by moving it from one index to another. */
    function reorderColumn(fromIndex: number, toIndex: number): void {
        if (fromIndex === toIndex) {
            return;
        }

        const newOrder = [...columnOrder];
        const [moved] = newOrder.splice(fromIndex, 1);
        if (moved !== undefined) {
            newOrder.splice(toIndex, 0, moved);
            columnOrder = newOrder;
        }
    }

    /** Sets the width of a column. */
    function setColumnWidth(columnId: string, width: string): void {
        columnWidths = { ...columnWidths, [columnId]: width };
    }

    /** Resets all column widths to default. */
    function resetColumnWidths(): void {
        columnWidths = {};
    }

    /** Resets column order to original. */
    function resetColumnOrder(): void {
        columnOrder = columns.map((c) => c.id);
    }

    // ---------------------------------------------------------------------------
    // Virtual Scroll Actions
    // ---------------------------------------------------------------------------

    /** Handles scroll events for virtual scrolling. */
    function handleScroll(scrollTop: number): void {
        if (virtualScrollConfig === undefined) {
            return;
        }

        const { rowHeight, overscan, containerHeight } = virtualScrollConfig;

        const startIndex = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
        const visibleCount = Math.ceil(containerHeight / rowHeight);
        const endIndex = Math.min(sortedData.length, startIndex + visibleCount + overscan * 2);

        virtualScroll = {
            startIndex,
            endIndex,
            scrollTop,
            totalHeight: sortedData.length * rowHeight,
            offsetY: startIndex * rowHeight,
        };
    }

    /** Scrolls to a specific row index. */
    function scrollToRow(index: number): void {
        if (virtualScrollConfig === undefined) {
            return;
        }

        const scrollTop = index * virtualScrollConfig.rowHeight;
        handleScroll(scrollTop);
    }

    // ---------------------------------------------------------------------------
    // Export Actions
    // ---------------------------------------------------------------------------

    /** Gets data for export based on configuration. */
    function getExportData(config?: ExportConfig): T[] {
        if (config?.selectedOnly && selectedRows.size > 0) {
            return sortedData.filter((_, index) => selectedRows.has(index));
        }
        return sortedData;
    }

    /** Gets columns for export based on configuration. */
    function getExportColumns(config?: ExportConfig): Column<T>[] {
        if (config?.columns !== undefined && config.columns.length > 0) {
            return orderedColumns.filter((c) => config.columns!.includes(c.id));
        }
        return orderedColumns;
    }

    /** Exports data as CSV. */
    function exportCSV(config?: ExportConfig): void {
        const exportData = getExportData(config);
        const exportColumns = getExportColumns(config);
        const includeHeaders = config?.includeHeaders ?? true;
        const filename = config?.filename ?? 'export';

        const rows: string[] = [];

        // Headers
        if (includeHeaders) {
            const headerRow = exportColumns.map((c) => escapeCSV(c.header)).join(',');
            rows.push(headerRow);
        }

        // Data rows
        for (const row of exportData) {
            const values = exportColumns.map((col) => {
                const value = getCellValue(row, col);
                return escapeCSV(String(value ?? ''));
            });
            rows.push(values.join(','));
        }

        const csv = rows.join('\n');
        downloadFile(csv, `${filename}.csv`, 'text/csv');
    }

    /** Exports data as JSON. */
    function exportJSON(config?: ExportConfig): void {
        const exportData = getExportData(config);
        const exportColumns = getExportColumns(config);
        const filename = config?.filename ?? 'export';

        const jsonData = exportData.map((row) => {
            const obj: Record<string, unknown> = {};
            for (const col of exportColumns) {
                obj[col.id] = getCellValue(row, col);
            }
            return obj;
        });

        const json = JSON.stringify(jsonData, null, 2);
        downloadFile(json, `${filename}.json`, 'application/json');
    }

    /** Escapes a value for CSV output. */
    function escapeCSV(value: string): string {
        if (value.includes(',') || value.includes('"') || value.includes('\n')) {
            return `"${value.replace(/"/g, '""')}"`;
        }
        return value;
    }

    /** Triggers a file download in the browser. */
    function downloadFile(content: string, filename: string, mimeType: string): void {
        const blob = new Blob([content], { type: mimeType });
        const url = URL.createObjectURL(blob);

        const link = document.createElement('a');
        link.href = url;
        link.download = filename;
        link.style.display = 'none';

        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);

        URL.revokeObjectURL(url);
    }

    // ---------------------------------------------------------------------------
    // Data Update
    // ---------------------------------------------------------------------------

    /** Updates the data array. */
    function setData(newData: T[]): void {
        data = [...newData];
        deselectAll();
        pagination.page = 1;
    }

    // ---------------------------------------------------------------------------
    // Return
    // ---------------------------------------------------------------------------

    return {
        // State (as getters for reactivity)
        get sortState() {
            return sortState;
        },
        get filterState() {
            return filterState;
        },
        get pagination() {
            return pagination;
        },
        get selectedRows() {
            return selectedRows;
        },
        get columnOrder() {
            return columnOrder;
        },
        get columnWidths() {
            return columnWidths;
        },
        get virtualScroll() {
            return virtualScroll;
        },

        // Derived
        get filteredData() {
            return filteredData;
        },
        get sortedData() {
            return sortedData;
        },
        get paginatedData() {
            return paginatedData;
        },
        get visibleData() {
            return visibleData;
        },
        get orderedColumns() {
            return orderedColumns;
        },
        get totalPages() {
            return totalPages;
        },
        get hasSelection() {
            return hasSelection;
        },
        get allSelected() {
            return allSelected;
        },
        get someSelected() {
            return someSelected;
        },
        get lastSelectedIndex() {
            return lastSelectedIndex;
        },

        // Actions
        toggleSort,
        setSort,
        clearSort,
        setFilter,
        clearFilter,
        clearAllFilters,
        setPage,
        setPageSize,
        goToFirstPage,
        goToLastPage,
        goToNextPage,
        goToPreviousPage,
        selectRow,
        deselectRow,
        toggleRow,
        selectAll,
        deselectAll,
        toggleAll,
        selectRange,
        reorderColumn,
        setColumnWidth,
        resetColumnWidths,
        resetColumnOrder,
        handleScroll,
        scrollToRow,
        exportCSV,
        exportJSON,
        setData,
    };
}
