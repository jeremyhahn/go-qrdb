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
 * DataTable component exports.
 *
 * A comprehensive, high-performance data table component for Svelte 5
 * with sorting, filtering, pagination, selection, virtual scrolling,
 * column reordering, column resizing, and data export capabilities.
 *
 * @example
 * ```svelte
 * <script lang="ts">
 *   import { DataTable, type Column } from '$components/core/DataTable';
 *
 *   interface User {
 *     id: number;
 *     name: string;
 *     email: string;
 *     role: string;
 *   }
 *
 *   const columns: Column<User>[] = [
 *     { id: 'id', header: 'ID', accessor: 'id', sortable: true },
 *     { id: 'name', header: 'Name', accessor: 'name', sortable: true, filterable: true },
 *     { id: 'email', header: 'Email', accessor: 'email', sortable: true, filterable: true },
 *     { id: 'role', header: 'Role', accessor: 'role', filterable: true, filterType: 'select', filterOptions: ['admin', 'user'] },
 *   ];
 *
 *   const users: User[] = [
 *     { id: 1, name: 'Alice', email: 'alice@example.com', role: 'admin' },
 *     { id: 2, name: 'Bob', email: 'bob@example.com', role: 'user' },
 *   ];
 * </script>
 *
 * <DataTable
 *   data={users}
 *   columns={columns}
 *   selectable
 *   selectionMode="multiple"
 *   exportable
 *   pageSize={25}
 * />
 * ```
 */

// Main component
export { default as DataTable } from "./DataTable.svelte";

// Sub-components (for advanced customization)
export { default as DataTableHeader } from "./DataTableHeader.svelte";
export { default as DataTableRow } from "./DataTableRow.svelte";
export { default as DataTableCell } from "./DataTableCell.svelte";
export { default as DataTableFooter } from "./DataTableFooter.svelte";
export { default as DataTableFilters } from "./DataTableFilters.svelte";

// Composable
export { useDataTable } from "./useDataTable.svelte.ts";
export type {
  UseDataTableOptions,
  UseDataTableReturn,
} from "./useDataTable.svelte.ts";

// Types
export type {
  // Column configuration
  Column,
  FilterType,
  TextFilterOperator,
  NumericFilterOperator,
  FilterOperator,
  CellAlignment,
  SortDirection,

  // State types
  SortState,
  FilterState,
  FilterValue,
  PaginationState,
  SelectionMode,
  DataTableState,

  // Virtual scroll
  VirtualScrollConfig,
  VirtualScrollState,

  // Export
  ExportFormat,
  ExportConfig,

  // Events
  RowClickEvent,
  CellClickEvent,
  SelectionChangeEvent,
  SortChangeEvent,
  FilterChangeEvent,
  PaginationChangeEvent,
  ColumnResizeEvent,
  ColumnReorderEvent,
} from "./types.ts";
