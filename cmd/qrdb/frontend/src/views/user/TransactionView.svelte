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
    TransactionRequest,
    TransactionResponse,
    Compare,
    Op,
    CompareTarget as SDKCompareTarget,
    CompareResult as SDKCompareResult,
    OpType as SDKOpType,
  } from "@qrdb/sdk";
  import type { Column } from "../../components/core/DataTable/types.ts";
  import { CrudTable } from "../../components/core/CrudTable";
  import { showToast } from "../../stores/toast.svelte.ts";
  import { CheckCircle, XCircle, Plus, Trash2 } from "lucide-svelte";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // SDK numeric constants with display labels
  // ---------------------------------------------------------------------------

  /** CompareTarget: 0=Version, 1=CreateRevision, 2=ModRevision, 3=Value */
  const CT_VERSION: SDKCompareTarget = 0;
  const CT_CREATE: SDKCompareTarget = 1;
  const CT_MOD: SDKCompareTarget = 2;
  const CT_VALUE: SDKCompareTarget = 3;

  /** CompareResult: 0=Equal, 1=Greater, 2=Less, 3=NotEqual */
  const CR_EQUAL: SDKCompareResult = 0;
  const CR_GREATER: SDKCompareResult = 1;
  const CR_LESS: SDKCompareResult = 2;
  const CR_NOT_EQUAL: SDKCompareResult = 3;

  /** OpType: 0=Put, 1=Get, 2=Delete, 3=Range */
  const OP_PUT: SDKOpType = 0;
  const OP_DELETE: SDKOpType = 2;

  // ---------------------------------------------------------------------------
  // Display label maps
  // ---------------------------------------------------------------------------

  const TARGET_LABELS: Record<number, string> = {
    [CT_VERSION]: "Version",
    [CT_CREATE]: "Create Revision",
    [CT_MOD]: "Mod Revision",
    [CT_VALUE]: "Value",
  };

  const RESULT_LABELS: Record<number, string> = {
    [CR_EQUAL]: "Equals",
    [CR_NOT_EQUAL]: "Not Equals",
    [CR_GREATER]: "Greater Than",
    [CR_LESS]: "Less Than",
  };

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  interface CompareRow {
    readonly id: number;
    key: string;
    target: SDKCompareTarget;
    result: SDKCompareResult;
    targetValue: string;
  }

  interface OpRow {
    readonly id: number;
    type: SDKOpType;
    key: string;
    value: string;
  }

  interface TxnResultRow {
    index: number;
    response: string;
  }

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let nextId: number = $state(0);
  let compares: CompareRow[] = $state([]);
  let successOps: OpRow[] = $state([]);
  let failureOps: OpRow[] = $state([]);
  let response = $state<TransactionResponse | null>(null);
  let loading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** True when any numeric-target compare has a non-numeric targetValue. */
  const hasInvalidCompare: boolean = $derived(
    compares.some(
      (c) =>
        c.target !== CT_VALUE &&
        c.targetValue.trim() !== "" &&
        Number.isNaN(parseInt(c.targetValue, 10)),
    ),
  );

  /** Disables the execute button while invalid compares exist or a request is in flight. */
  const executeDisabled: boolean = $derived(loading || hasInvalidCompare);

  const resultTableData: TxnResultRow[] = $derived(
    response !== null && response.responses !== undefined
      ? response.responses.map((resp, i) => ({
          index: i + 1,
          response: JSON.stringify(resp),
        }))
      : []
  );

  // ---------------------------------------------------------------------------
  // Result table columns
  // ---------------------------------------------------------------------------

  const resultColumns: Column<TxnResultRow>[] = [
    {
      id: "index",
      header: "#",
      accessor: "index",
      sortable: true,
      width: "60px",
      align: "center",
    },
    {
      id: "response",
      header: "Response",
      accessor: "response",
      sortable: false,
    },
  ];

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function allocId(): number {
    return nextId++;
  }

  function describeCond(c: CompareRow): string {
    if (c.key === "") return "";
    const target = TARGET_LABELS[c.target] ?? "unknown";
    const result = RESULT_LABELS[c.result] ?? "unknown";
    const val = c.targetValue || "?";
    return `Key "${c.key}" ${target} ${result} ${val}`;
  }

  // ---------------------------------------------------------------------------
  // Compare management
  // ---------------------------------------------------------------------------

  function addCompare(): void {
    compares = [
      ...compares,
      { id: allocId(), key: "", target: CT_VERSION, result: CR_EQUAL, targetValue: "" },
    ];
  }

  function removeCompare(id: number): void {
    compares = compares.filter((c) => c.id !== id);
  }

  // ---------------------------------------------------------------------------
  // Op management
  // ---------------------------------------------------------------------------

  function addSuccessOp(): void {
    successOps = [...successOps, { id: allocId(), type: OP_PUT, key: "", value: "" }];
  }

  function removeSuccessOp(id: number): void {
    successOps = successOps.filter((o) => o.id !== id);
  }

  function addFailureOp(): void {
    failureOps = [...failureOps, { id: allocId(), type: OP_PUT, key: "", value: "" }];
  }

  function removeFailureOp(id: number): void {
    failureOps = failureOps.filter((o) => o.id !== id);
  }

  // ---------------------------------------------------------------------------
  // Execute
  // ---------------------------------------------------------------------------

  function buildCompares(): Compare[] {
    return compares.map((c): Compare => {
      const base = { key: c.key, target: c.target, result: c.result };
      if (c.target === CT_VALUE) {
        return { ...base, value: c.targetValue };
      }
      const numVal = parseInt(c.targetValue, 10);
      if (c.target === CT_VERSION) {
        return { ...base, version: numVal };
      }
      if (c.target === CT_CREATE) {
        return { ...base, createRevision: numVal };
      }
      // CT_MOD
      return { ...base, modRevision: numVal };
    });
  }

  function buildOps(rows: OpRow[]): Op[] {
    return rows.map((o): Op => {
      if (o.type === OP_PUT) {
        return { type: o.type, key: o.key, value: o.value };
      }
      return { type: o.type, key: o.key };
    });
  }

  async function handleExecute(): Promise<void> {
    if (compares.length === 0) {
      showToast("warning", "At least one compare is required.");
      return;
    }
    if (successOps.length === 0 && failureOps.length === 0) {
      showToast("warning", "At least one success or failure operation is required.");
      return;
    }
    loading = true;
    try {
      const req: TransactionRequest = {
        compares: buildCompares(),
        successOps: buildOps(successOps),
        failureOps: buildOps(failureOps),
      };
      response = await client.transaction.execute(req);
      showToast("success", response.succeeded ? "Transaction succeeded." : "Transaction condition failed; failure ops executed.");
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function resetForm(): void {
    compares = [];
    successOps = [];
    failureOps = [];
    response = null;
  }
</script>

<div class="flex flex-col gap-6">

  <PageHeader title="Transactions" description="Run multi-key transactions with compare-and-swap semantics">
    {#snippet actions()}
      <button class="btn btn-primary btn-sm" onclick={handleExecute} disabled={executeDisabled}>
        {#if loading}
          <span class="loading loading-spinner loading-xs"></span>
        {/if}
        Execute Transaction
      </button>
      <button class="btn btn-outline btn-sm" onclick={resetForm}>Reset</button>
    {/snippet}
  </PageHeader>

  <!-- Step 1: Conditions -->
  <div class="card bg-base-200 shadow-sm">
    <div class="card-body p-5">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <span class="badge badge-lg badge-neutral font-bold">1</span>
          <div>
            <h3 class="text-base font-semibold">Conditions</h3>
            <p class="text-base-content/50 text-xs">Define conditions that must be true for the transaction to succeed.</p>
          </div>
        </div>
        <button class="btn btn-outline btn-sm gap-1" onclick={addCompare}>
          <Plus class="h-3.5 w-3.5" />
          Add Condition
        </button>
      </div>
      {#if compares.length === 0}
        <p class="text-base-content/50 mt-2 text-sm">No conditions defined. Add at least one condition.</p>
      {:else}
        <div class="mt-3 flex flex-col gap-3">
          {#each compares as compare (compare.id)}
            <div class="bg-base-300 rounded-lg p-4">
              <div class="flex flex-wrap items-end gap-2">
                <label class="form-control w-full max-w-40">
                  <div class="label"><span class="label-text text-xs">Key</span></div>
                  <input
                    type="text"
                    class="input input-bordered input-sm w-full"
                    placeholder="Key"
                    bind:value={compare.key}
                  />
                </label>
                <label class="form-control w-36">
                  <div class="label"><span class="label-text text-xs">Property</span></div>
                  <select class="select select-bordered select-sm w-full" bind:value={compare.target}>
                    <option value={CT_VERSION}>Version</option>
                    <option value={CT_CREATE}>Create Revision</option>
                    <option value={CT_MOD}>Mod Revision</option>
                    <option value={CT_VALUE}>Value</option>
                  </select>
                </label>
                <label class="form-control w-36">
                  <div class="label"><span class="label-text text-xs">Comparison</span></div>
                  <select class="select select-bordered select-sm w-full" bind:value={compare.result}>
                    <option value={CR_EQUAL}>Equals</option>
                    <option value={CR_NOT_EQUAL}>Not Equals</option>
                    <option value={CR_GREATER}>Greater Than</option>
                    <option value={CR_LESS}>Less Than</option>
                  </select>
                </label>
                <label class="form-control w-full max-w-40">
                  <div class="label"><span class="label-text text-xs">Expected Value</span></div>
                  <input
                    type="text"
                    class="input input-bordered input-sm w-full {compare.target !== CT_VALUE && compare.targetValue.trim() !== '' && Number.isNaN(parseInt(compare.targetValue, 10)) ? 'input-error' : ''}"
                    placeholder="Expected value"
                    bind:value={compare.targetValue}
                  />
                  {#if compare.target !== CT_VALUE && compare.targetValue.trim() !== "" && Number.isNaN(parseInt(compare.targetValue, 10))}
                    <span class="text-error mt-1 text-xs">Must be a whole number.</span>
                  {/if}
                </label>
                <button
                  class="btn btn-ghost btn-sm text-error"
                  onclick={() => removeCompare(compare.id)}
                  aria-label="Remove condition"
                >
                  <Trash2 class="h-4 w-4" />
                </button>
              </div>
              {#if describeCond(compare)}
                <p class="text-base-content/40 mt-2 text-xs italic">{describeCond(compare)}</p>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <!-- Step 2: If Conditions Pass -->
  <div class="card bg-base-200 shadow-sm border-l-4 border-success">
    <div class="card-body p-5">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <span class="badge badge-lg badge-success font-bold">2</span>
          <div>
            <h3 class="text-base font-semibold">If Conditions Pass</h3>
            <p class="text-base-content/50 text-xs">Operations that execute atomically when all conditions are met.</p>
          </div>
        </div>
        <button class="btn btn-outline btn-sm gap-1" onclick={addSuccessOp}>
          <Plus class="h-3.5 w-3.5" />
          Add Op
        </button>
      </div>
      {#if successOps.length === 0}
        <p class="text-base-content/50 mt-2 text-sm">No success operations defined.</p>
      {:else}
        <div class="mt-3 flex flex-col gap-2">
          {#each successOps as op (op.id)}
            <div class="bg-base-300 flex flex-wrap items-end gap-2 rounded-lg p-3">
              <label class="form-control w-28">
                <div class="label"><span class="label-text text-xs">Type</span></div>
                <select class="select select-bordered select-sm w-full" bind:value={op.type}>
                  <option value={OP_PUT}>PUT</option>
                  <option value={OP_DELETE}>DELETE</option>
                </select>
              </label>
              <label class="form-control w-full max-w-48">
                <div class="label"><span class="label-text text-xs">Key</span></div>
                <input
                  type="text"
                  class="input input-bordered input-sm w-full"
                  placeholder="Key"
                  bind:value={op.key}
                />
              </label>
              {#if op.type === OP_PUT}
                <label class="form-control w-full max-w-48">
                  <div class="label"><span class="label-text text-xs">Value</span></div>
                  <input
                    type="text"
                    class="input input-bordered input-sm w-full"
                    placeholder="Value"
                    bind:value={op.value}
                  />
                </label>
              {/if}
              <button
                class="btn btn-ghost btn-sm text-error"
                onclick={() => removeSuccessOp(op.id)}
                aria-label="Remove success operation"
              >
                <Trash2 class="h-4 w-4" />
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <!-- Step 3: If Conditions Fail -->
  <div class="card bg-base-200 shadow-sm border-l-4 border-error">
    <div class="card-body p-5">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <span class="badge badge-lg badge-error font-bold">3</span>
          <div>
            <h3 class="text-base font-semibold">If Conditions Fail</h3>
            <p class="text-base-content/50 text-xs">Operations that execute when any condition is not met.</p>
          </div>
          <span class="badge badge-ghost badge-sm">Optional</span>
        </div>
        <button class="btn btn-outline btn-sm gap-1" onclick={addFailureOp}>
          <Plus class="h-3.5 w-3.5" />
          Add Op
        </button>
      </div>
      {#if failureOps.length === 0}
        <p class="text-base-content/50 mt-2 text-sm">No failure operations defined.</p>
      {:else}
        <div class="mt-3 flex flex-col gap-2">
          {#each failureOps as op (op.id)}
            <div class="bg-base-300 flex flex-wrap items-end gap-2 rounded-lg p-3">
              <label class="form-control w-28">
                <div class="label"><span class="label-text text-xs">Type</span></div>
                <select class="select select-bordered select-sm w-full" bind:value={op.type}>
                  <option value={OP_PUT}>PUT</option>
                  <option value={OP_DELETE}>DELETE</option>
                </select>
              </label>
              <label class="form-control w-full max-w-48">
                <div class="label"><span class="label-text text-xs">Key</span></div>
                <input
                  type="text"
                  class="input input-bordered input-sm w-full"
                  placeholder="Key"
                  bind:value={op.key}
                />
              </label>
              {#if op.type === OP_PUT}
                <label class="form-control w-full max-w-48">
                  <div class="label"><span class="label-text text-xs">Value</span></div>
                  <input
                    type="text"
                    class="input input-bordered input-sm w-full"
                    placeholder="Value"
                    bind:value={op.value}
                  />
                </label>
              {/if}
              <button
                class="btn btn-ghost btn-sm text-error"
                onclick={() => removeFailureOp(op.id)}
                aria-label="Remove failure operation"
              >
                <Trash2 class="h-4 w-4" />
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <!-- Result -->
  {#if response !== null}
    <div class="flex flex-col gap-4">
      <div class="flex items-center gap-3">
        {#if response.succeeded}
          <CheckCircle class="h-6 w-6 text-success" />
          <span class="badge badge-success text-sm">Succeeded</span>
        {:else}
          <XCircle class="h-6 w-6 text-error" />
          <span class="badge badge-error text-sm">Failed</span>
        {/if}
      </div>
      <CrudTable
        title="Transaction Results"
        data={resultTableData}
        columns={resultColumns}
        loading={false}
        showCreate={false}
        showDelete={false}
        pageSize={10}
        emptyMessage="No response payloads returned."
      />
    </div>
  {/if}
</div>
