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
  import type { PaginationState } from "./types.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    pagination,
    totalPages = 1,
    exportable = false,
    pageSizeOptions = [10, 25, 50, 100, -1],
    onpagechange,
    onpagesizechange,
    onexportcsv,
    onexportjson,
  }: {
    pagination: PaginationState;
    totalPages?: number;
    exportable?: boolean;
    pageSizeOptions?: number[];
    onpagechange?: (page: number) => void;
    onpagesizechange?: (pageSize: number) => void;
    onexportcsv?: () => void;
    onexportjson?: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let jumpToPage = $state("");

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Calculate display range. */
  const displayRange = $derived.by(() => {
    const { page, pageSize, totalRows } = pagination;

    if (totalRows === 0) {
      return { start: 0, end: 0, total: 0 };
    }

    // Handle "all" page size
    if (pageSize <= 0) {
      return { start: 1, end: totalRows, total: totalRows };
    }

    const start = (page - 1) * pageSize + 1;
    const end = Math.min(page * pageSize, totalRows);
    return { start, end, total: totalRows };
  });

  /** Generate visible page numbers. */
  const visiblePages = $derived.by(() => {
    const pages: (number | "...")[] = [];
    const current = pagination.page;
    const total = totalPages;

    if (total <= 7) {
      // Show all pages
      for (let i = 1; i <= total; i++) {
        pages.push(i);
      }
    } else {
      // Always show first page
      pages.push(1);

      if (current > 3) {
        pages.push("...");
      }

      // Show pages around current
      const start = Math.max(2, current - 1);
      const end = Math.min(total - 1, current + 1);

      for (let i = start; i <= end; i++) {
        pages.push(i);
      }

      if (current < total - 2) {
        pages.push("...");
      }

      // Always show last page
      pages.push(total);
    }

    return pages;
  });

  // ---------------------------------------------------------------------------
  // Handlers
  // ---------------------------------------------------------------------------

  /** Handles page change. */
  function handlePageChange(page: number): void {
    if (page >= 1 && page <= totalPages && page !== pagination.page) {
      onpagechange?.(page);
    }
  }

  /** Handles page size change. */
  function handlePageSizeChange(event: Event): void {
    const target = event.target as HTMLSelectElement;
    const newSize = parseInt(target.value, 10);
    onpagesizechange?.(newSize);
  }

  /** Handles jump to page. */
  function handleJumpToPage(): void {
    const page = parseInt(jumpToPage, 10);
    if (!isNaN(page) && page >= 1 && page <= totalPages) {
      handlePageChange(page);
    }
    jumpToPage = "";
  }

  /** Handles jump to page on Enter key. */
  function handleJumpKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      handleJumpToPage();
    }
  }

  /** Gets display label for page size option. */
  function getPageSizeLabel(size: number): string {
    return size <= 0 ? "All" : String(size);
  }
</script>

<div
  class="bg-base-200 flex flex-wrap items-center justify-between gap-4 border-t px-4 py-3"
>
  <!-- Row count display -->
  <div class="text-base-content/70 text-sm">
    {#if displayRange.total === 0}
      No results
    {:else}
      Showing <span class="font-medium">{displayRange.start}</span>
      -
      <span class="font-medium">{displayRange.end}</span>
      of
      <span class="font-medium">{displayRange.total}</span>
      rows
    {/if}
  </div>

  <!-- Pagination controls -->
  <div class="flex items-center gap-2">
    <!-- Page size selector -->
    <div class="flex items-center gap-2">
      <label for="page-size" class="text-base-content/70 text-sm">
        Rows per page:
      </label>
      <select
        id="page-size"
        class="select select-bordered select-sm w-20"
        value={pagination.pageSize}
        onchange={handlePageSizeChange}
      >
        {#each pageSizeOptions as size}
          <option value={size}>{getPageSizeLabel(size)}</option>
        {/each}
      </select>
    </div>

    <!-- Divider -->
    <div class="bg-base-content/20 mx-2 h-6 w-px"></div>

    <!-- Page navigation -->
    <div class="join">
      <!-- First page -->
      <button
        type="button"
        class="btn btn-sm join-item"
        onclick={() => handlePageChange(1)}
        disabled={pagination.page === 1}
        title="First page"
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
            d="M11 19l-7-7 7-7m8 14l-7-7 7-7"
          />
        </svg>
      </button>

      <!-- Previous page -->
      <button
        type="button"
        class="btn btn-sm join-item"
        onclick={() => handlePageChange(pagination.page - 1)}
        disabled={pagination.page === 1}
        title="Previous page"
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
            d="M15 19l-7-7 7-7"
          />
        </svg>
      </button>

      <!-- Page numbers -->
      {#each visiblePages as pageNum}
        {#if pageNum === "..."}
          <button type="button" class="btn btn-sm join-item btn-disabled">
            ...
          </button>
        {:else}
          <button
            type="button"
            class="btn btn-sm join-item {pageNum === pagination.page
              ? 'btn-primary'
              : ''}"
            onclick={() => handlePageChange(pageNum)}
          >
            {pageNum}
          </button>
        {/if}
      {/each}

      <!-- Next page -->
      <button
        type="button"
        class="btn btn-sm join-item"
        onclick={() => handlePageChange(pagination.page + 1)}
        disabled={pagination.page === totalPages}
        title="Next page"
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
            d="M9 5l7 7-7 7"
          />
        </svg>
      </button>

      <!-- Last page -->
      <button
        type="button"
        class="btn btn-sm join-item"
        onclick={() => handlePageChange(totalPages)}
        disabled={pagination.page === totalPages}
        title="Last page"
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
            d="M13 5l7 7-7 7M5 5l7 7-7 7"
          />
        </svg>
      </button>
    </div>

    <!-- Jump to page -->
    <div class="flex items-center gap-1">
      <label for="jump-to-page" class="text-base-content/70 text-sm">
        Go to:
      </label>
      <input
        id="jump-to-page"
        type="number"
        class="input input-bordered input-sm w-16 text-center"
        min="1"
        max={totalPages}
        placeholder={String(pagination.page)}
        bind:value={jumpToPage}
        onkeydown={handleJumpKeyDown}
        onblur={handleJumpToPage}
      />
    </div>

    <!-- Export buttons -->
    {#if exportable}
      <div class="bg-base-content/20 mx-2 h-6 w-px"></div>
      <div class="dropdown dropdown-end dropdown-top">
        <button
          type="button"
          tabindex="0"
          class="btn btn-sm btn-outline"
          title="Export data"
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
              d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"
            />
          </svg>
          Export
        </button>
        <ul
          tabindex="-1"
          class="menu dropdown-content bg-base-100 rounded-box z-50 w-32 p-2 shadow"
        >
          <li>
            <button
              type="button"
              onclick={() => onexportcsv?.()}
              class="flex items-center gap-2"
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
                  d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
                />
              </svg>
              CSV
            </button>
          </li>
          <li>
            <button
              type="button"
              onclick={() => onexportjson?.()}
              class="flex items-center gap-2"
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
                  d="M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4"
                />
              </svg>
              JSON
            </button>
          </li>
        </ul>
      </div>
    {/if}
  </div>
</div>
