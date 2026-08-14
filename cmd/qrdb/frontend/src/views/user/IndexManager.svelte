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
  import type { Client, IndexQueryResponse, IndexScanResponse } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { CrudTable } from "../../components/core/CrudTable";
  import FormModal from "../../components/FormModal.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";
  import { Search, ScanLine } from "lucide-svelte";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Row type for the index list table
  // ---------------------------------------------------------------------------

  interface IndexRow {
    readonly name: string;
  }

  // ---------------------------------------------------------------------------
  // Index list state
  // ---------------------------------------------------------------------------

  let indexes: IndexRow[] = $state([]);
  let loading: boolean = $state(false);
  let selectedName: string | null = $state(null);

  // Create form
  let showCreateModal: boolean = $state(false);
  let createLoading: boolean = $state(false);
  let createName: string = $state("");
  let createFieldName: string = $state("");
  let createUnique: boolean = $state(false);
  let createSparse: boolean = $state(false);
  let createNumeric: boolean = $state(false);

  // Drop confirmation
  let dropTarget: string | null = $state(null);

  // ---------------------------------------------------------------------------
  // Query / Scan panel state
  // ---------------------------------------------------------------------------

  // Query
  let queryIndexName: string = $state("");
  let queryValue: string = $state("");
  let queryLoading: boolean = $state(false);
  let queryResult: IndexQueryResponse | null = $state(null);

  // Scan
  let scanIndexName: string = $state("");
  let scanStart: string = $state("");
  let scanEnd: string = $state("");
  let scanLoading: boolean = $state(false);
  let scanResult: IndexScanResponse | null = $state(null);

  // ---------------------------------------------------------------------------
  // Column definitions
  // ---------------------------------------------------------------------------

  const columns: Column<IndexRow>[] = [
    { id: "name", header: "Name", accessor: "name", sortable: true },
  ];

  // ---------------------------------------------------------------------------
  // Data loading
  // ---------------------------------------------------------------------------

  async function loadIndexes(): Promise<void> {
    loading = true;
    try {
      const resp = await client.index.list();
      indexes = resp.names.map((n) => ({ name: n }));
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Create
  // ---------------------------------------------------------------------------

  async function handleCreate(): Promise<void> {
    const name = createName.trim();
    const fieldName = createFieldName.trim();
    if (name === "" || fieldName === "") {
      showToast("warning", "Index name and field name are required.");
      return;
    }
    createLoading = true;
    try {
      await client.index.create({
        name,
        fieldName,
        unique: createUnique,
        sparse: createSparse,
        numeric: createNumeric,
      });
      showToast("success", `Index "${name}" created.`);
      showCreateModal = false;
      resetCreateForm();
      await loadIndexes();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      createLoading = false;
    }
  }

  function resetCreateForm(): void {
    createName = "";
    createFieldName = "";
    createUnique = false;
    createSparse = false;
    createNumeric = false;
  }

  // ---------------------------------------------------------------------------
  // Drop
  // ---------------------------------------------------------------------------

  async function handleDrop(): Promise<void> {
    if (dropTarget === null) return;
    const name = dropTarget;
    dropTarget = null;
    loading = true;
    try {
      await client.index.drop(name);
      if (selectedName === name) {
        selectedName = null;
        queryIndexName = "";
        scanIndexName = "";
      }
      showToast("success", `Index "${name}" dropped.`);
      await loadIndexes();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Row selection
  // ---------------------------------------------------------------------------

  function handleRowClick(event: RowClickEvent<IndexRow>): void {
    selectedName = event.row.name;
    queryIndexName = event.row.name;
    scanIndexName = event.row.name;
    queryResult = null;
    scanResult = null;
  }

  // ---------------------------------------------------------------------------
  // Query
  // ---------------------------------------------------------------------------

  async function handleQuery(): Promise<void> {
    const idxName = queryIndexName.trim();
    const val = queryValue.trim();
    if (idxName === "" || val === "") {
      showToast("warning", "Index name and value are required for query.");
      return;
    }
    queryLoading = true;
    queryResult = null;
    try {
      queryResult = await client.index.query(idxName, val);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      queryLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Scan
  // ---------------------------------------------------------------------------

  async function handleScan(): Promise<void> {
    const idxName = scanIndexName.trim();
    const start = scanStart.trim();
    const end = scanEnd.trim();
    if (idxName === "" || start === "" || end === "") {
      showToast("warning", "Index name, start, and end are required for scan.");
      return;
    }
    scanLoading = true;
    scanResult = null;
    try {
      scanResult = await client.index.scan(idxName, start, end);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      scanLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  $effect(() => {
    loadIndexes();
  });
</script>

<div class="flex flex-col gap-6">

  <PageHeader title="Indexes" description="Manage secondary indexes for this database">
    {#snippet actions()}
      <button class="btn btn-outline btn-sm" onclick={loadIndexes}>Refresh</button>
      <button class="btn btn-primary btn-sm" onclick={() => (showCreateModal = true)}>Create Index</button>
    {/snippet}
  </PageHeader>

  <!-- Index list + query/scan panels -->
  <div class="flex gap-6">

    <!-- Index list CrudTable -->
    <div class="flex-1">
      <CrudTable
        title="Secondary Indexes"
        data={indexes}
        {columns}
        {loading}
        showCreate={false}
        showDelete={false}
        onrowclick={handleRowClick}
        pageSize={10}
        emptyMessage="No secondary indexes defined."
      />
      <!-- Per-row drop button below selection hint -->
      {#if selectedName !== null}
        <div class="mt-3 flex items-center gap-3">
          <span class="text-sm text-base-content/70">Selected: <span class="font-mono font-semibold">{selectedName}</span></span>
          <button
            class="btn btn-error btn-xs"
            onclick={() => { dropTarget = selectedName; }}
          >
            Drop Index
          </button>
        </div>
      {/if}
    </div>

    <!-- Query / Scan side panel -->
    <div class="flex flex-col gap-4 w-80">

      <!-- Query panel -->
      <div class="card bg-base-200 shadow-sm">
        <div class="card-body p-5">
          <h3 class="card-title text-base font-semibold flex items-center gap-2">
            <Search class="h-4 w-4" />
            Query
          </h3>
          <div class="flex flex-col gap-2">
            <label class="form-control w-full">
              <div class="label py-0"><span class="label-text text-xs">Index Name</span></div>
              <select
                class="select select-bordered select-xs w-full font-mono"
                bind:value={queryIndexName}
              >
                <option value="" disabled>Select an index</option>
                {#each indexes as idx (idx.name)}
                  <option value={idx.name}>{idx.name}</option>
                {/each}
              </select>
            </label>
            <label class="form-control w-full">
              <div class="label py-0"><span class="label-text text-xs">Value</span></div>
              <input
                type="text"
                class="input input-bordered input-xs w-full font-mono"
                placeholder="exact value"
                bind:value={queryValue}
              />
            </label>
            <button
              class="btn btn-primary btn-xs mt-1"
              onclick={() => void handleQuery()}
              disabled={queryLoading}
            >
              {#if queryLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Query
            </button>
          </div>
          {#if queryResult !== null}
            <div class="mt-3">
              <p class="text-xs text-base-content/60 mb-1">
                {queryResult.keys.length} key(s) matched
              </p>
              <div class="bg-base-300 rounded p-2 max-h-36 overflow-y-auto">
                {#if queryResult.keys.length === 0}
                  <p class="text-xs text-base-content/50">No matching keys.</p>
                {:else}
                  <ul class="space-y-0.5">
                    {#each queryResult.keys as key (key)}
                      <li class="font-mono text-xs">{key}</li>
                    {/each}
                  </ul>
                {/if}
              </div>
            </div>
          {/if}
        </div>
      </div>

      <!-- Scan panel -->
      <div class="card bg-base-200 shadow-sm">
        <div class="card-body p-5">
          <h3 class="card-title text-base font-semibold flex items-center gap-2">
            <ScanLine class="h-4 w-4" />
            Scan Range
          </h3>
          <div class="flex flex-col gap-2">
            <label class="form-control w-full">
              <div class="label py-0"><span class="label-text text-xs">Index Name</span></div>
              <select
                class="select select-bordered select-xs w-full font-mono"
                bind:value={scanIndexName}
              >
                <option value="" disabled>Select an index</option>
                {#each indexes as idx (idx.name)}
                  <option value={idx.name}>{idx.name}</option>
                {/each}
              </select>
            </label>
            <label class="form-control w-full">
              <div class="label py-0"><span class="label-text text-xs">Start (inclusive)</span></div>
              <input
                type="text"
                class="input input-bordered input-xs w-full font-mono"
                placeholder="start value"
                bind:value={scanStart}
              />
            </label>
            <label class="form-control w-full">
              <div class="label py-0"><span class="label-text text-xs">End (exclusive)</span></div>
              <input
                type="text"
                class="input input-bordered input-xs w-full font-mono"
                placeholder="end value"
                bind:value={scanEnd}
              />
            </label>
            <button
              class="btn btn-primary btn-xs mt-1"
              onclick={() => void handleScan()}
              disabled={scanLoading}
            >
              {#if scanLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Scan
            </button>
          </div>
          {#if scanResult !== null}
            <div class="mt-3">
              <p class="text-xs text-base-content/60 mb-1">
                {scanResult.entries.length} value(s) matched
              </p>
              <div class="bg-base-300 rounded p-2 max-h-36 overflow-y-auto">
                {#if scanResult.entries.length === 0}
                  <p class="text-xs text-base-content/50">No entries in range.</p>
                {:else}
                  {#each scanResult.entries as entry (entry.value)}
                    <div class="mb-1">
                      <span class="font-mono text-xs font-semibold">{entry.value}</span>
                      <ul class="ml-3 space-y-0.5">
                        {#each entry.keys as key (key)}
                          <li class="font-mono text-xs text-base-content/70">{key}</li>
                        {/each}
                      </ul>
                    </div>
                  {/each}
                {/if}
              </div>
            </div>
          {/if}
        </div>
      </div>

    </div>
  </div>
</div>

<!-- Create Index modal -->
<FormModal
  open={showCreateModal}
  title="Create Secondary Index"
  submitLabel="Create"
  loading={createLoading}
  onsubmit={() => void handleCreate()}
  oncancel={() => { showCreateModal = false; resetCreateForm(); }}
>
  {#snippet body()}
    <div class="flex flex-col gap-3">
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Index Name</span></div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="e.g. idx-email"
          bind:value={createName}
        />
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Field Name</span></div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="e.g. email"
          bind:value={createFieldName}
        />
      </label>
      <div class="flex flex-col gap-2 pt-1">
        <label class="label cursor-pointer justify-start gap-3">
          <input type="checkbox" class="checkbox checkbox-sm" bind:checked={createUnique} />
          <span class="label-text">Unique — enforce unique values</span>
        </label>
        <label class="label cursor-pointer justify-start gap-3">
          <input type="checkbox" class="checkbox checkbox-sm" bind:checked={createSparse} />
          <span class="label-text">Sparse — skip entries missing the field</span>
        </label>
        <label class="label cursor-pointer justify-start gap-3">
          <input type="checkbox" class="checkbox checkbox-sm" bind:checked={createNumeric} />
          <span class="label-text">Numeric — fixed-width integer encoding</span>
        </label>
      </div>
    </div>
  {/snippet}
</FormModal>

<!-- Drop Index confirmation -->
<ConfirmModal
  open={dropTarget !== null}
  title="Drop Index"
  message="Drop index &quot;{dropTarget ?? ''}&quot;? This action cannot be undone."
  confirmLabel="Drop"
  variant="error"
  onconfirm={() => void handleDrop()}
  oncancel={() => { dropTarget = null; }}
/>
