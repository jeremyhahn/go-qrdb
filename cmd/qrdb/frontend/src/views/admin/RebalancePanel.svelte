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
  import type { Client, ImbalanceReport, RebalancePlan, RebalanceResult, NodeLoad, RebalanceMove } from "@qrdb/sdk";
  import type { Column } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { RefreshCw, Play, Zap } from "lucide-svelte";
  import DataTable from "../../components/core/DataTable/DataTable.svelte";
  import StatCard from "../../components/core/StatCard.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import PageHeader from "../../components/PageHeader.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let report: ImbalanceReport | null = $state(null);
  let reportLoading: boolean = $state(true);

  let plan: RebalancePlan | null = $state(null);
  let planLoading: boolean = $state(false);
  let maxMoves: string = $state("10");
  let targetBalance: string = $state("0.1");

  let showExecuteConfirm: boolean = $state(false);
  let lastResult: RebalanceResult | null = $state(null);

  // ---------------------------------------------------------------------------
  // Columns
  // ---------------------------------------------------------------------------

  const nodeColumns: Column<NodeLoad>[] = [
    { id: "nodeId", header: "Node", accessor: "nodeId", sortable: true },
    { id: "address", header: "Address", accessor: "address" },
    { id: "shardCount", header: "Shards", accessor: "shardCount", sortable: true },
    { id: "leaderCount", header: "Leaders", accessor: "leaderCount", sortable: true },
    { id: "totalKeys", header: "Keys", accessor: "totalKeys", sortable: true },
    { id: "totalBytes", header: "Bytes", accessor: (row: NodeLoad) => formatBytes(row.totalBytes), sortable: true },
  ];

  const moveColumns: Column<RebalanceMove>[] = [
    { id: "shardId", header: "Shard", accessor: "shardId", sortable: true },
    { id: "fromNode", header: "From", accessor: "fromNode" },
    { id: "toNode", header: "To", accessor: "toNode" },
    { id: "estimatedBytes", header: "Est. Size", accessor: (row: RebalanceMove) => formatBytes(row.estimatedBytes) },
  ];

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    const units = ["KB", "MB", "GB", "TB"];
    let value = bytes / 1024;
    let unitIndex = 0;
    while (value >= 1024 && unitIndex < units.length - 1) {
      value /= 1024;
      unitIndex += 1;
    }
    return `${value.toFixed(1)} ${units[unitIndex]}`;
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function loadReport(): Promise<void> {
    reportLoading = true;
    try {
      report = await client.rebalance.getImbalanceReport();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      reportLoading = false;
    }
  }

  async function handleCreatePlan(): Promise<void> {
    planLoading = true;
    try {
      const moves = parseInt(maxMoves, 10);
      const balance = parseFloat(targetBalance);
      plan = await client.rebalance.createPlan({
        ...(Number.isNaN(moves) ? {} : { maxMoves: moves }),
        ...(Number.isNaN(balance) ? {} : { targetBalance: balance }),
        dryRun: true,
      });
      lastResult = null;
      showToast("success", `Plan generated: ${plan.moves.length} move(s), est. ${plan.estimatedDuration}.`);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      planLoading = false;
    }
  }

  async function handleExecute(): Promise<void> {
    if (plan === null) return;
    try {
      lastResult = await client.rebalance.execute(plan);
      showToast(
        lastResult.movesFailed === 0 ? "success" : "warning",
        `Rebalance complete: ${lastResult.movesCompleted} succeeded, ${lastResult.movesFailed} failed, in ${lastResult.duration}.`,
      );
      showExecuteConfirm = false;
      plan = null;
      await loadReport();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void loadReport();
  });
</script>

<div class="flex flex-col gap-6">
  <PageHeader title="Rebalance" description="Shard rebalancing and cluster load distribution">
    {#snippet actions()}
      <button class="btn btn-outline btn-sm" onclick={() => void loadReport()} disabled={reportLoading}>
        {#if reportLoading}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <RefreshCw class="h-4 w-4" />
        {/if}
        Refresh Report
      </button>
    {/snippet}
  </PageHeader>

  {#if report !== null}
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <StatCard label="Imbalance Score" value={report.imbalanceScore.toFixed(3)} icon={Zap} />
      <StatCard label="Nodes" value={report.nodes.length} />
      <StatCard label="Hotspots" value={(report.hotspots ?? []).length} />
    </div>

    {#if (report.hotspots ?? []).length > 0}
      <div class="alert alert-warning text-sm">
        <span>Hotspots: {(report.hotspots ?? []).join(", ")}</span>
      </div>
    {/if}

    <div class="card bg-base-200 shadow">
      <div class="card-body p-5">
        <h3 class="card-title text-base">Node Load</h3>
        <DataTable data={report.nodes} columns={nodeColumns} rowKey="nodeId" emptyMessage="No node data." />
      </div>
    </div>
  {/if}

  <!-- Plan generation -->
  <div class="card bg-base-200 shadow">
    <div class="card-body p-5">
      <h3 class="card-title text-base">Rebalance Plan</h3>
      <div class="flex flex-wrap items-end gap-3">
        <label class="form-control w-full max-w-[160px]">
          <div class="label"><span class="label-text text-xs">Max Moves</span></div>
          <input type="number" class="input input-bordered input-sm w-full" min="1" bind:value={maxMoves} />
        </label>
        <label class="form-control w-full max-w-[160px]">
          <div class="label"><span class="label-text text-xs">Target Balance</span></div>
          <input type="number" class="input input-bordered input-sm w-full" min="0" max="1" step="0.01" bind:value={targetBalance} />
        </label>
        <button class="btn btn-primary btn-sm" onclick={() => void handleCreatePlan()} disabled={planLoading}>
          {#if planLoading}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
          Generate Plan
        </button>
      </div>

      {#if plan !== null}
        <div class="mt-4 flex flex-col gap-3">
          <div class="flex gap-6 text-sm">
            <span>Current imbalance: <strong>{plan.currentImbalance.toFixed(3)}</strong></span>
            <span>Estimated duration: <strong>{plan.estimatedDuration}</strong></span>
            <span>Moves: <strong>{plan.moves.length}</strong></span>
          </div>
          <DataTable
            data={plan.moves}
            columns={moveColumns}
            rowKey={(row) => `${row.shardId}-${row.fromNode}-${row.toNode}`}
            emptyMessage="Cluster is already balanced — no moves required."
          />
          {#if plan.moves.length > 0}
            <div>
              <button class="btn btn-warning btn-sm" onclick={() => (showExecuteConfirm = true)}>
                <Play class="h-4 w-4" />
                Execute Plan
              </button>
            </div>
          {/if}
        </div>
      {/if}
    </div>
  </div>

  {#if lastResult !== null}
    <div class="card bg-base-200 shadow">
      <div class="card-body p-5">
        <h3 class="card-title text-base">Last Execution Result</h3>
        <table class="table table-sm">
          <tbody>
            <tr><th class="w-40">Moves Completed</th><td>{lastResult.movesCompleted}</td></tr>
            <tr><th>Moves Failed</th><td>{lastResult.movesFailed}</td></tr>
            <tr><th>Duration</th><td>{lastResult.duration}</td></tr>
            <tr><th>Final Imbalance</th><td>{lastResult.finalImbalance.toFixed(3)}</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  {/if}
</div>

<!-- Execute confirmation -->
<ConfirmModal
  open={showExecuteConfirm}
  title="Execute Rebalance Plan"
  message={plan !== null ? `Execute ${plan.moves.length} shard move(s)? This will transfer live data between nodes.` : ""}
  confirmLabel="Execute"
  variant="warning"
  onconfirm={() => void handleExecute()}
  oncancel={() => (showExecuteConfirm = false)}
/>
