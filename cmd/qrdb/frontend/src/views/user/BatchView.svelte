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
  import type {
    Client,
    Op,
    OpType,
    BatchResponse,
    ImportResult,
  } from "@qrdb/sdk";
  import type { Column } from "../../components/core/DataTable/types.ts";
  import { CrudTable } from "../../components/core/CrudTable";
  import { ChevronUp, ChevronDown, FileUp } from "lucide-svelte";
  import PageHeader from "../../components/PageHeader.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";
  import HelpTip from "../../components/HelpTip.svelte";
  import { onMount } from "svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Inner tab state
  // ---------------------------------------------------------------------------

  type BatchTab = "operations" | "import";
  let activeTab: BatchTab = $state("operations");

  // ---------------------------------------------------------------------------
  // Operations tab — types
  // ---------------------------------------------------------------------------

  /** Numeric OpType constants matching the SDK wire format. */
  const OP_PUT: OpType = 0;
  const OP_GET: OpType = 1;
  const OP_DELETE: OpType = 2;
  const OP_RANGE: OpType = 3;

  /** Human-readable labels for display. */
  const opTypeLabels: Record<OpType, string> = {
    [OP_PUT]: "put",
    [OP_GET]: "get",
    [OP_DELETE]: "delete",
    [OP_RANGE]: "delete_range",
  };

  interface OpRow {
    readonly id: number;
    type: OpType;
    key: string;
    value: string;
    rangeEnd: string;
  }

  interface BatchResultRow {
    index: number;
    type: string;
    key: string;
    result: string;
  }

  // ---------------------------------------------------------------------------
  // Operations tab — state
  // ---------------------------------------------------------------------------

  let nextId: number = $state(0);
  let ops: OpRow[] = $state([]);
  let response: BatchResponse | null = $state(null);
  let opsLoading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Operations tab — derived
  // ---------------------------------------------------------------------------

  const resultTableData: BatchResultRow[] = $derived(
    response !== null
      ? ((response as BatchResponse).responses ?? []).map(
          (result: import("@qrdb/sdk").OpResponse, i: number) => ({
            index: i + 1,
            type: ops[i] !== undefined ? opTypeLabels[ops[i].type] : "?",
            key: ops[i]?.key ?? "?",
            result: JSON.stringify(result),
          }),
        )
      : [],
  );

  const resultColumns: Column<BatchResultRow>[] = [
    {
      id: "index",
      header: "#",
      accessor: "index",
      sortable: true,
      width: "60px",
      align: "center",
    },
    { id: "type", header: "Type", accessor: "type", sortable: true },
    {
      id: "key",
      header: "Key",
      accessor: "key",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    { id: "result", header: "Result", accessor: "result", sortable: false },
  ];

  // ---------------------------------------------------------------------------
  // Operations tab — helpers
  // ---------------------------------------------------------------------------

  function allocId(): number {
    return nextId++;
  }

  function addOp(): void {
    ops = [
      ...ops,
      { id: allocId(), type: OP_PUT, key: "", value: "", rangeEnd: "" },
    ];
  }

  function removeOp(id: number): void {
    ops = ops.filter((o) => o.id !== id);
  }

  function moveUp(index: number): void {
    if (index <= 0) return;
    const updated = [...ops];
    const temp = updated[index - 1]!;
    updated[index - 1] = updated[index]!;
    updated[index] = temp;
    ops = updated;
  }

  function moveDown(index: number): void {
    if (index >= ops.length - 1) return;
    const updated = [...ops];
    const temp = updated[index + 1]!;
    updated[index + 1] = updated[index]!;
    updated[index] = temp;
    ops = updated;
  }

  function buildOps(): Op[] {
    return ops.map((o): Op => {
      if (o.type === OP_PUT) {
        return { type: o.type, key: o.key, value: o.value };
      }
      if (o.type === OP_RANGE) {
        return { type: o.type, key: o.key, rangeEnd: o.rangeEnd };
      }
      return { type: o.type, key: o.key };
    });
  }

  async function handleExecute(): Promise<void> {
    if (ops.length === 0) {
      showToast("warning", "Add at least one operation to the batch.");
      return;
    }
    const emptyKeys = ops.some((o) => o.key.trim() === "");
    if (emptyKeys) {
      showToast("warning", "All operations must have a key.");
      return;
    }
    opsLoading = true;
    try {
      response = await client.batch.execute(buildOps());
      showToast("success", "Batch executed successfully.");
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      opsLoading = false;
    }
  }

  function resetForm(): void {
    ops = [];
    response = null;
  }

  // ---------------------------------------------------------------------------
  // Import tab — state
  // ---------------------------------------------------------------------------

  type ValueMode = "row" | "field";
  type ImportTarget = "kv" | "collection";

  let importFilePath: string = $state("");
  let importFormat: string = $state("auto");
  let importKeyField: string = $state("");
  let importValueMode: ValueMode = $state("row");
  let importValueField: string = $state("");
  let importTarget: ImportTarget = $state("kv");
  let importDatabase: string = $state("");
  let importCollection: string = $state("");
  let importSkipHeader: boolean = $state(true);
  let importTransactional: boolean = $state(false);
  let importLoading: boolean = $state(false);
  let importResult: ImportResult | null = $state(null);

  let availableFormats: readonly string[] = $state([]);

  // ---------------------------------------------------------------------------
  // Value codecs (batch.availableCodecs), loaded on mount
  // ---------------------------------------------------------------------------

  /** Shown when the server's codec list cannot be fetched. */
  const FALLBACK_CODECS: readonly string[] = ["json"];
  let codecs: readonly string[] = $state(FALLBACK_CODECS);
  let codecsFallback: boolean = $state(false);

  onMount(() => {
    client.batch
      .availableCodecs()
      .then((list) => {
        codecs = list.length > 0 ? list : FALLBACK_CODECS;
        codecsFallback = list.length === 0;
      })
      .catch((err: unknown) => {
        codecs = FALLBACK_CODECS;
        codecsFallback = true;
        showToast("error", err instanceof Error ? err.message : String(err));
      });
  });

  // ---------------------------------------------------------------------------
  // Import tab — derived
  // ---------------------------------------------------------------------------

  const importDisabled: boolean = $derived(
    importFilePath.trim() === "" || importLoading,
  );

  interface ImportErrorRow {
    row: number;
    key: string;
    message: string;
  }

  const importErrorRows: ImportErrorRow[] = $derived(
    importResult !== null
      ? ((importResult as ImportResult).errors ?? []).map(
          (e: import("@qrdb/sdk").ImportError) => ({
            row: e.row,
            key: e.key,
            message: e.message,
          }),
        )
      : [],
  );

  const importErrorColumns: Column<ImportErrorRow>[] = [
    {
      id: "row",
      header: "Row",
      accessor: "row",
      sortable: true,
      width: "80px",
      align: "center",
    },
    {
      id: "key",
      header: "Key",
      accessor: "key",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    { id: "message", header: "Error", accessor: "message", sortable: false },
  ];

  // ---------------------------------------------------------------------------
  // Import tab — lifecycle
  // ---------------------------------------------------------------------------

  $effect(() => {
    if (activeTab === "import" && availableFormats.length === 0) {
      client.batch
        .availableImportFormats()
        .then((formats) => {
          availableFormats = formats;
        })
        .catch((err: unknown) => {
          showToast("error", err instanceof Error ? err.message : String(err));
        });
    }
  });

  // ---------------------------------------------------------------------------
  // Import tab — handlers
  // ---------------------------------------------------------------------------

  async function handleImport(): Promise<void> {
    if (importFilePath.trim() === "") {
      showToast("warning", "Enter a file path to import.");
      return;
    }
    if (importKeyField.trim() === "") {
      showToast("warning", "Key Field is required.");
      return;
    }
    importLoading = true;
    importResult = null;
    try {
      const opts = {
        format: importFormat,
        key_field: importKeyField,
        value_mode: importValueMode,
        ...(importValueMode === "field"
          ? { value_field: importValueField }
          : {}),
        skip_header: importSkipHeader,
        transactional: importTransactional,
        ...(importTarget === "collection"
          ? {
              database: importDatabase,
              collection: importCollection,
            }
          : {}),
      };
      importResult = await client.batch.importFile(importFilePath.trim(), opts);
      if (importResult.failed === 0) {
        showToast(
          "success",
          `Imported ${String(importResult.succeeded)} of ${String(importResult.total)} records.`,
        );
      } else {
        showToast(
          "warning",
          `Imported ${String(importResult.succeeded)} of ${String(importResult.total)} records (${String(importResult.failed)} failed).`,
        );
      }
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      importLoading = false;
    }
  }

  function resetImport(): void {
    importFilePath = "";
    importFormat = "auto";
    importKeyField = "";
    importValueMode = "row";
    importValueField = "";
    importTarget = "kv";
    importDatabase = "";
    importCollection = "";
    importSkipHeader = true;
    importTransactional = false;
    importResult = null;
  }
</script>

<div class="flex flex-col gap-4">
  <PageHeader
    title="Batch"
    description="Execute multiple KV operations atomically"
  />

  <!-- Value codecs supported by the server -->
  <div
    class="flex flex-wrap items-center gap-2 text-sm"
    data-testid="batch-codecs"
  >
    <span class="font-medium flex items-center gap-1">
      Value codecs
      <HelpTip
        text="Codecs the server can encode and decode batch values with (batch.availableCodecs)."
      />
    </span>
    {#each codecs as codec (codec)}
      <span class="badge badge-outline" data-testid="batch-codec">{codec}</span>
    {/each}
    {#if codecsFallback}
      <span
        class="text-base-content/50 text-xs"
        data-testid="batch-codecs-fallback"
        >(server list unavailable; default shown)</span
      >
    {/if}
  </div>

  <!-- Tab bar -->
  <div role="tablist" class="tabs tabs-bordered">
    <button
      role="tab"
      class="tab"
      class:tab-active={activeTab === "operations"}
      onclick={() => {
        activeTab = "operations";
      }}
    >
      Operations
    </button>
    <button
      role="tab"
      class="tab"
      class:tab-active={activeTab === "import"}
      onclick={() => {
        activeTab = "import";
      }}
    >
      <span class="flex items-center gap-1">
        <FileUp class="h-3.5 w-3.5" />
        Import
      </span>
    </button>
  </div>

  <!-- ======================================================================
       Operations tab
       ====================================================================== -->
  {#if activeTab === "operations"}
    <div class="flex flex-col gap-6">
      <!-- Operation builder -->
      <div class="card bg-base-200 shadow-sm">
        <div class="card-body p-5">
          <div class="flex items-center justify-between">
            <h3 class="card-title text-base font-semibold">
              Batch Operations ({ops.length})
            </h3>
            <button class="btn btn-outline btn-sm" onclick={addOp}
              >Add Operation</button
            >
          </div>
          {#if ops.length === 0}
            <p class="text-base-content/50 text-sm">
              No operations defined. Click "Add Operation" to begin.
            </p>
          {:else}
            <div class="flex flex-col gap-2 mt-2">
              {#each ops as op, index (op.id)}
                <div
                  class="bg-base-300 flex flex-wrap items-end gap-2 rounded p-3"
                >
                  <span
                    class="text-base-content/50 self-center text-xs font-mono w-6"
                  >
                    {index + 1}.
                  </span>
                  <label class="form-control w-36">
                    <div class="label">
                      <span class="label-text text-xs">Type</span>
                    </div>
                    <select
                      class="select select-bordered select-sm w-full"
                      bind:value={op.type}
                    >
                      <option value={OP_PUT}>put</option>
                      <option value={OP_GET}>get</option>
                      <option value={OP_DELETE}>delete</option>
                      <option value={OP_RANGE}>delete_range</option>
                    </select>
                  </label>
                  <label class="form-control w-full max-w-48">
                    <div class="label">
                      <span class="label-text text-xs">Key</span>
                    </div>
                    <input
                      type="text"
                      class="input input-bordered input-sm w-full"
                      placeholder="Key"
                      bind:value={op.key}
                    />
                  </label>
                  {#if op.type === OP_PUT}
                    <label class="form-control w-full max-w-48">
                      <div class="label">
                        <span class="label-text text-xs">Value</span>
                      </div>
                      <input
                        type="text"
                        class="input input-bordered input-sm w-full"
                        placeholder="Value"
                        bind:value={op.value}
                      />
                    </label>
                  {/if}
                  {#if op.type === OP_RANGE}
                    <label class="form-control w-full max-w-48">
                      <div class="label">
                        <span class="label-text text-xs">Range End</span>
                      </div>
                      <input
                        type="text"
                        class="input input-bordered input-sm w-full"
                        placeholder="End key (exclusive)"
                        bind:value={op.rangeEnd}
                      />
                    </label>
                  {/if}
                  <div class="flex gap-1">
                    <button
                      class="btn btn-ghost btn-xs"
                      onclick={() => moveUp(index)}
                      disabled={index === 0}
                      aria-label="Move up"
                    >
                      <ChevronUp class="h-3 w-3" />
                    </button>
                    <button
                      class="btn btn-ghost btn-xs"
                      onclick={() => moveDown(index)}
                      disabled={index === ops.length - 1}
                      aria-label="Move down"
                    >
                      <ChevronDown class="h-3 w-3" />
                    </button>
                    <button
                      class="btn btn-error btn-xs"
                      onclick={() => removeOp(op.id)}
                      aria-label="Remove operation"
                    >
                      Remove
                    </button>
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </div>

      <!-- Execute / Reset -->
      <div class="flex gap-2">
        <button
          class="btn btn-primary btn-sm"
          onclick={handleExecute}
          disabled={opsLoading}
        >
          {#if opsLoading}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
          Execute Batch
        </button>
        <button class="btn btn-outline btn-sm" onclick={resetForm}>Reset</button
        >
      </div>

      <!-- Results -->
      {#if response !== null}
        <CrudTable
          title="Batch Results"
          data={resultTableData}
          columns={resultColumns}
          loading={false}
          showCreate={false}
          showDelete={false}
          pageSize={10}
          emptyMessage="Batch executed successfully with no individual results."
        />
      {/if}
    </div>

    <!-- ======================================================================
       Import tab
       ====================================================================== -->
  {:else}
    <div class="flex flex-col gap-6">
      <!-- Import form -->
      <div class="card bg-base-200 shadow-sm">
        <div class="card-body p-5">
          <h3 class="card-title text-base font-semibold">Import from File</h3>

          <!-- File path -->
          <div class="form-control mt-2">
            <label class="label" for="import-file-path">
              <span class="label-text text-xs font-medium">File Path</span>
            </label>
            <input
              id="import-file-path"
              type="text"
              class="input input-bordered input-sm w-full"
              placeholder="/path/to/data.csv"
              bind:value={importFilePath}
            />
          </div>

          <!-- Format -->
          <div class="form-control mt-3">
            <label class="label" for="import-format">
              <span
                class="label-text text-xs font-medium flex items-center gap-1"
              >
                Format
                <HelpTip
                  text="Auto-detect determines the format from the file extension. Override manually if detection fails."
                />
              </span>
            </label>
            <select
              id="import-format"
              class="select select-bordered select-sm w-full max-w-xs"
              bind:value={importFormat}
            >
              {#if availableFormats.length === 0}
                <option value="auto">auto</option>
              {:else}
                {#each availableFormats as fmt (fmt)}
                  <option value={fmt}>{fmt}</option>
                {/each}
              {/if}
            </select>
          </div>

          <!-- Key Field -->
          <div class="form-control mt-3">
            <label class="label" for="import-key-field">
              <span
                class="label-text text-xs font-medium flex items-center gap-1"
              >
                Key Field
                <HelpTip
                  text="The field name to use as the key for each record. For CSV, this is the column name. For JSON, this is the object property name."
                />
              </span>
            </label>
            <input
              id="import-key-field"
              type="text"
              class="input input-bordered input-sm w-full max-w-xs"
              placeholder="e.g. id"
              bind:value={importKeyField}
            />
          </div>

          <!-- Value Mode -->
          <div class="form-control mt-3">
            <span
              class="label-text text-xs font-medium flex items-center gap-1 mb-1"
            >
              Value Mode
              <HelpTip
                text="Entire Row stores the full record as the value. Specific Field stores only one field's value."
              />
            </span>
            <div class="flex items-center gap-6">
              <label class="label cursor-pointer gap-2">
                <input
                  type="radio"
                  class="radio radio-sm"
                  name="value-mode"
                  value="row"
                  bind:group={importValueMode}
                />
                <span class="label-text text-sm">Entire Row</span>
              </label>
              <label class="label cursor-pointer gap-2">
                <input
                  type="radio"
                  class="radio radio-sm"
                  name="value-mode"
                  value="field"
                  bind:group={importValueMode}
                />
                <span class="label-text text-sm">Specific Field</span>
              </label>
            </div>
          </div>

          <!-- Value Field (conditional) -->
          {#if importValueMode === "field"}
            <div class="form-control mt-2">
              <label class="label" for="import-value-field">
                <span
                  class="label-text text-xs font-medium flex items-center gap-1"
                >
                  Value Field
                  <HelpTip
                    text="The field name whose value will be stored. Only used when Value Mode is Specific Field."
                  />
                </span>
              </label>
              <input
                id="import-value-field"
                type="text"
                class="input input-bordered input-sm w-full max-w-xs"
                placeholder="e.g. name"
                bind:value={importValueField}
              />
            </div>
          {/if}

          <!-- Target -->
          <div class="form-control mt-3">
            <span class="label-text text-xs font-medium mb-1">Target</span>
            <div class="flex items-center gap-6">
              <label class="label cursor-pointer gap-2">
                <input
                  type="radio"
                  class="radio radio-sm"
                  name="import-target"
                  value="kv"
                  bind:group={importTarget}
                />
                <span class="label-text text-sm">Raw KV</span>
              </label>
              <label class="label cursor-pointer gap-2">
                <input
                  type="radio"
                  class="radio radio-sm"
                  name="import-target"
                  value="collection"
                  bind:group={importTarget}
                />
                <span class="label-text text-sm">Collection</span>
              </label>
            </div>
          </div>

          <!-- Collection target fields -->
          {#if importTarget === "collection"}
            <div class="flex flex-wrap gap-4 mt-2">
              <div class="form-control">
                <label class="label" for="import-database">
                  <span class="label-text text-xs">Database</span>
                </label>
                <input
                  id="import-database"
                  type="text"
                  class="input input-bordered input-sm w-48"
                  placeholder="Database name"
                  bind:value={importDatabase}
                />
              </div>
              <div class="form-control">
                <label class="label" for="import-collection">
                  <span class="label-text text-xs">Collection</span>
                </label>
                <input
                  id="import-collection"
                  type="text"
                  class="input input-bordered input-sm w-48"
                  placeholder="Collection name"
                  bind:value={importCollection}
                />
              </div>
            </div>
          {/if}

          <!-- Skip Header -->
          <div class="form-control mt-3">
            <label class="label cursor-pointer justify-start gap-3">
              <input
                type="checkbox"
                class="checkbox checkbox-sm"
                bind:checked={importSkipHeader}
              />
              <span class="label-text text-sm flex items-center gap-1">
                Skip Header
                <HelpTip
                  text="Skip the first row in CSV files (header row). Does not apply to JSON/YAML."
                />
              </span>
            </label>
          </div>

          <!-- Transactional -->
          <div class="form-control mt-1">
            <label class="label cursor-pointer justify-start gap-3">
              <input
                type="checkbox"
                class="checkbox checkbox-sm"
                bind:checked={importTransactional}
              />
              <span class="label-text text-sm flex items-center gap-1">
                Transactional
                <HelpTip
                  text="When enabled, the entire import fails if any single record fails. When disabled, successful records are imported and failures are reported."
                />
              </span>
            </label>
          </div>
        </div>
      </div>

      <!-- Import / Reset buttons -->
      <div class="flex gap-2">
        <button
          class="btn btn-primary btn-sm"
          onclick={handleImport}
          disabled={importDisabled}
        >
          {#if importLoading}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
          Import
        </button>
        <button class="btn btn-outline btn-sm" onclick={resetImport}
          >Reset</button
        >
      </div>

      <!-- Results panel -->
      {#if importResult !== null}
        <div class="card bg-base-200 shadow-sm">
          <div class="card-body p-5">
            <h3 class="card-title text-base font-semibold">Import Results</h3>
            <p class="text-sm mt-1">
              Imported
              <span class="font-semibold">{importResult.succeeded}</span>
              of
              <span class="font-semibold">{importResult.total}</span>
              records
              {#if importResult.failed > 0}
                (<span class="text-error font-semibold"
                  >{importResult.failed} failed</span
                >)
              {/if}.
            </p>

            {#if importErrorRows.length > 0}
              <div class="mt-4">
                <CrudTable
                  title="Import Errors"
                  data={importErrorRows}
                  columns={importErrorColumns}
                  loading={false}
                  showCreate={false}
                  showDelete={false}
                  pageSize={10}
                  emptyMessage="No errors."
                />
              </div>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  {/if}
</div>
