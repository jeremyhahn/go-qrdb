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
 * DataTable type definitions.
 *
 * Provides comprehensive TypeScript interfaces for the DataTable component
 * including column configuration, sorting, filtering, pagination, and selection.
 */

// ---------------------------------------------------------------------------
// Column Configuration
// ---------------------------------------------------------------------------

/** Filter types supported by the DataTable. */
export type FilterType = "text" | "select" | "range" | "date";

/** Text filter operators. */
export type TextFilterOperator =
  "contains" | "equals" | "startsWith" | "endsWith";

/** Numeric filter operators. */
export type NumericFilterOperator = "gt" | "lt" | "between" | "equals";

/** Combined filter operators. */
export type FilterOperator = TextFilterOperator | NumericFilterOperator;

/** Cell alignment options. */
export type CellAlignment = "left" | "center" | "right";

/** Sort direction. */
export type SortDirection = "asc" | "desc";

/**
 * Column definition for the DataTable.
 *
 * @typeParam T - The type of data row in the table.
 */
export interface Column<T> {
  /** Unique identifier for this column. */
  readonly id: string;
  /** Display header text. */
  readonly header: string;
  /** Property key or accessor function to get cell value. */
  readonly accessor: keyof T | ((row: T) => unknown);
  /** Whether this column can be sorted. Defaults to true. */
  readonly sortable?: boolean;
  /** Whether this column can be filtered. Defaults to false. */
  readonly filterable?: boolean;
  /** Type of filter input to display. */
  readonly filterType?: FilterType;
  /** Options for select-type filters. */
  readonly filterOptions?: readonly string[];
  /** CSS width value (e.g., "100px", "20%"). */
  readonly width?: string;
  /** CSS min-width value. */
  readonly minWidth?: string;
  /** CSS max-width value. */
  readonly maxWidth?: string;
  /** Cell content alignment. Defaults to 'left'. */
  readonly align?: CellAlignment;
  /** Custom render function for cell content. */
  readonly render?: (value: unknown, row: T) => string;
}

// ---------------------------------------------------------------------------
// Sort State
// ---------------------------------------------------------------------------

/** Represents a single column sort configuration. */
export interface SortState {
  /** Column ID being sorted. */
  readonly columnId: string;
  /** Sort direction. */
  readonly direction: SortDirection;
}

// ---------------------------------------------------------------------------
// Filter State
// ---------------------------------------------------------------------------

/** Filter value can be a string, numeric range tuple, or date array. */
export type FilterValue = string | [number, number] | Date[];

/** Represents a single column filter configuration. */
export interface FilterState {
  /** Column ID being filtered. */
  readonly columnId: string;
  /** Filter value. */
  readonly value: FilterValue;
  /** Filter operator for comparison. */
  readonly operator?: FilterOperator | undefined;
}

// ---------------------------------------------------------------------------
// Pagination State
// ---------------------------------------------------------------------------

/** Pagination configuration and state. */
export interface PaginationState {
  /** Current page number (1-indexed). */
  page: number;
  /** Number of rows per page. */
  pageSize: number;
  /** Total number of rows (after filtering). */
  totalRows: number;
}

// ---------------------------------------------------------------------------
// Selection State
// ---------------------------------------------------------------------------

/** Selection mode for the table. */
export type SelectionMode = "none" | "single" | "multiple";

// ---------------------------------------------------------------------------
// Combined DataTable State
// ---------------------------------------------------------------------------

/**
 * Complete state for a DataTable instance.
 *
 * @typeParam T - The type of data row in the table.
 */
export interface DataTableState<T> {
  /** Original data array. */
  readonly data: T[];
  /** Column definitions. */
  readonly columns: Column<T>[];
  /** Active sort configurations (supports multi-column sort). */
  sortState: SortState[];
  /** Active filter configurations. */
  filterState: FilterState[];
  /** Pagination configuration. */
  pagination: PaginationState;
  /** Set of selected row indices. */
  selectedRows: Set<number>;
  /** Ordered column IDs (for reordering). */
  columnOrder: string[];
  /** Custom column widths by column ID. */
  columnWidths: Record<string, string>;
}

// ---------------------------------------------------------------------------
// Virtual Scroll Configuration
// ---------------------------------------------------------------------------

/** Virtual scroll configuration for large datasets. */
export interface VirtualScrollConfig {
  /** Fixed height per row in pixels. */
  readonly rowHeight: number;
  /** Number of rows to render above/below viewport. */
  readonly overscan: number;
  /** Container height in pixels. */
  readonly containerHeight: number;
}

/** Virtual scroll state. */
export interface VirtualScrollState {
  /** Index of first visible row. */
  startIndex: number;
  /** Index of last visible row (exclusive). */
  endIndex: number;
  /** Current scroll offset in pixels. */
  scrollTop: number;
  /** Total scrollable height in pixels. */
  totalHeight: number;
  /** Offset for the visible window. */
  offsetY: number;
}

// ---------------------------------------------------------------------------
// Export Configuration
// ---------------------------------------------------------------------------

/** Export format options. */
export type ExportFormat = "csv" | "json";

/** Export configuration. */
export interface ExportConfig {
  /** Filename without extension. */
  readonly filename?: string;
  /** Whether to include headers in CSV export. */
  readonly includeHeaders?: boolean;
  /** Column IDs to include (defaults to all visible columns). */
  readonly columns?: readonly string[];
  /** Whether to export only selected rows. */
  readonly selectedOnly?: boolean;
}

// ---------------------------------------------------------------------------
// Event Payloads
// ---------------------------------------------------------------------------

/** Payload for row click events. */
export interface RowClickEvent<T> {
  /** The row data. */
  readonly row: T;
  /** Row index in the filtered/sorted data. */
  readonly index: number;
  /** Original row index in the source data. */
  readonly originalIndex: number;
}

/** Payload for cell click events. */
export interface CellClickEvent<T> {
  /** The row data. */
  readonly row: T;
  /** Column ID. */
  readonly columnId: string;
  /** Cell value. */
  readonly value: unknown;
  /** Row index. */
  readonly rowIndex: number;
}

/** Payload for selection change events. */
export interface SelectionChangeEvent {
  /** Set of selected row indices (in filtered/sorted data). */
  readonly selectedRows: Set<number>;
  /** Whether the selection was cleared. */
  readonly cleared: boolean;
}

/** Payload for sort change events. */
export interface SortChangeEvent {
  /** New sort state. */
  readonly sortState: readonly SortState[];
  /** Whether Shift was held (multi-sort). */
  readonly multiSort: boolean;
}

/** Payload for filter change events. */
export interface FilterChangeEvent {
  /** New filter state. */
  readonly filterState: readonly FilterState[];
  /** Column ID that was filtered. */
  readonly columnId: string;
}

/** Payload for pagination change events. */
export interface PaginationChangeEvent {
  /** New pagination state. */
  readonly pagination: PaginationState;
  /** What changed: page number or page size. */
  readonly changed: "page" | "pageSize";
}

/** Payload for column resize events. */
export interface ColumnResizeEvent {
  /** Column ID. */
  readonly columnId: string;
  /** New width value. */
  readonly width: string;
}

/** Payload for column reorder events. */
export interface ColumnReorderEvent {
  /** Column ID being moved. */
  readonly columnId: string;
  /** Original index. */
  readonly fromIndex: number;
  /** New index. */
  readonly toIndex: number;
}
