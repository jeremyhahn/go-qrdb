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
  import type { Snippet } from "svelte";
  import type {
    Column,
    RowClickEvent,
    SelectionChangeEvent,
  } from "../DataTable/types.ts";
  import DataTable from "../DataTable/DataTable.svelte";
  import FormModal from "../../FormModal.svelte";
  import ConfirmModal from "../../ConfirmModal.svelte";
  import { Plus, Trash2 } from "lucide-svelte";

  let {
    title,
    data,
    columns,
    loading = false,
    pageSize = 10,
    pageSizeOptions = [10, 25, 50, 100],
    showCreate = true,
    showDelete = true,
    createLabel = "Create",
    deleteLabel = "Delete",
    oncreateclick,
    ondeleteselected,
    onrowclick,
    formModal,
    formModalTitle = "Create",
    formModalSubmitLabel = "Create",
    formModalLoading = false,
    onformsubmit,
    emptyMessage = "No data available",
    rowKey,
    compact = true,
    striped = true,
  }: {
    title: string;
    data: T[];
    columns: Column<T>[];
    loading?: boolean;
    pageSize?: number;
    pageSizeOptions?: number[];
    showCreate?: boolean;
    showDelete?: boolean;
    createLabel?: string;
    deleteLabel?: string;
    oncreateclick?: () => void;
    ondeleteselected?: (indices: Set<number>) => void;
    onrowclick?: (event: RowClickEvent<T>) => void;
    formModal?: Snippet;
    formModalTitle?: string;
    formModalSubmitLabel?: string;
    formModalLoading?: boolean;
    onformsubmit?: () => void;
    emptyMessage?: string;
    rowKey?: (keyof T | ((row: T) => string | number)) | undefined;
    compact?: boolean;
    striped?: boolean;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let selectedRows: Set<number> = $state(new Set());
  let showFormModal: boolean = $state(false);
  let showDeleteConfirm: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const selectedCount = $derived(selectedRows.size);
  const hasSelection = $derived(selectedCount > 0);

  // ---------------------------------------------------------------------------
  // Handlers
  // ---------------------------------------------------------------------------

  function handleSelectionChange(event: SelectionChangeEvent): void {
    selectedRows = new Set(event.selectedRows);
  }

  function handleCreateClick(): void {
    if (formModal !== undefined) {
      showFormModal = true;
    } else if (oncreateclick !== undefined) {
      oncreateclick();
    }
  }

  function handleDeleteClick(): void {
    if (!hasSelection) return;
    showDeleteConfirm = true;
  }

  function handleDeleteConfirm(): void {
    showDeleteConfirm = false;
    ondeleteselected?.(new Set(selectedRows));
    selectedRows = new Set();
  }

  function handleFormSubmit(): void {
    onformsubmit?.();
  }

  function handleFormCancel(): void {
    showFormModal = false;
  }

  /** Allows parent to programmatically close the form modal. */
  export function closeFormModal(): void {
    showFormModal = false;
  }

  /** Allows parent to programmatically open the form modal. */
  export function openFormModal(): void {
    showFormModal = true;
  }

  /** Allows parent to clear selection. */
  export function clearSelection(): void {
    selectedRows = new Set();
  }
</script>

<div class="flex flex-col">
  <!-- Toolbar -->
  <div class="flex items-center justify-between rounded-t-lg bg-base-200 px-4 py-3">
    <div class="flex items-center gap-2">
      <h3 class="text-sm font-semibold">{title}</h3>
      <span class="badge badge-sm badge-ghost">{data.length}</span>
    </div>
    <div class="flex items-center gap-2">
      {#if showDelete && ondeleteselected !== undefined}
        <button
          class="btn btn-error btn-sm"
          disabled={!hasSelection}
          onclick={handleDeleteClick}
        >
          <Trash2 class="h-4 w-4" />
          {deleteLabel}
          {#if hasSelection}
            <span class="badge badge-sm">{selectedCount}</span>
          {/if}
        </button>
      {/if}
      {#if showCreate}
        <button
          class="btn btn-primary btn-sm"
          onclick={handleCreateClick}
        >
          <Plus class="h-4 w-4" />
          {createLabel}
        </button>
      {/if}
    </div>
  </div>

  <!-- DataTable -->
  <DataTable
    {data}
    {columns}
    {loading}
    {emptyMessage}
    {pageSize}
    {pageSizeOptions}
    {rowKey}
    {compact}
    {striped}
    selectable={showDelete && ondeleteselected !== undefined}
    selectionMode={showDelete && ondeleteselected !== undefined ? "multiple" : "none"}
    onrowclick={onrowclick}
    onselectionchange={handleSelectionChange}
  />
</div>

<!-- Form modal -->
{#if formModal !== undefined}
  <FormModal
    open={showFormModal}
    title={formModalTitle}
    submitLabel={formModalSubmitLabel}
    loading={formModalLoading}
    onsubmit={handleFormSubmit}
    oncancel={handleFormCancel}
  >
    {#snippet body()}
      {@render formModal()}
    {/snippet}
  </FormModal>
{/if}

<!-- Delete confirmation modal -->
<ConfirmModal
  open={showDeleteConfirm}
  title="Confirm Delete"
  message={`Are you sure you want to delete ${selectedCount} selected item${selectedCount !== 1 ? "s" : ""}? This action cannot be undone.`}
  confirmLabel="Delete"
  variant="error"
  onconfirm={handleDeleteConfirm}
  oncancel={() => (showDeleteConfirm = false)}
/>
