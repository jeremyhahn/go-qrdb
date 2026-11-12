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
  import type { Client, TopologyResponse } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { RefreshCw } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let topology = $state<TopologyResponse | null>(null);
  let loading: boolean = $state(true);
  let hasError: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Data fetching
  // ---------------------------------------------------------------------------

  /** Fetches the cluster topology from the admin API. */
  async function fetchTopology(): Promise<void> {
    try {
      loading = true;
      topology = await client.admin.getTopology();
      hasError = false;
    } catch (err: unknown) {
      hasError = true;
      showToast(
        "error",
        err instanceof Error ? err.message : "Failed to fetch topology",
      );
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void fetchTopology();
  });
</script>

<PageHeader
  title="Topology"
  description="Per-node shard and replica placement across the Raft cluster"
/>

<div class="flex flex-col gap-6">
  <!-- Node summary card -->
  {#if topology !== null}
    <div class="card bg-base-200 shadow" data-testid="topology-node-summary">
      <div class="card-body p-5">
        <div class="flex items-center justify-between">
          <h2 class="card-title text-lg">Node</h2>
          <button
            class="btn btn-ghost btn-sm"
            onclick={() => void fetchTopology()}
            disabled={loading}
          >
            {#if loading}
              <span class="loading loading-spinner loading-xs"></span>
            {:else}
              <RefreshCw class="h-4 w-4" />
            {/if}
            Refresh
          </button>
        </div>

        <div class="grid grid-cols-2 gap-4 text-sm sm:grid-cols-3">
          <div>
            <span
              class="text-base-content/60 block text-xs font-semibold uppercase"
              >Node ID</span
            >
            <span class="font-mono" data-testid="topology-node-id"
              >{topology.nodeId}</span
            >
          </div>
          <div>
            <span
              class="text-base-content/60 block text-xs font-semibold uppercase"
              >Raft Address</span
            >
            <span class="font-mono" data-testid="topology-raft-address"
              >{topology.raftAddress}</span
            >
          </div>
          <div>
            <span
              class="text-base-content/60 block text-xs font-semibold uppercase"
              >Shard Count</span
            >
            <span class="font-mono" data-testid="topology-shard-count"
              >{topology.shardCount}</span
            >
          </div>
        </div>
      </div>
    </div>
  {/if}

  <!-- Shards table -->
  <div class="card bg-base-200 shadow">
    <div class="card-body p-5">
      <div class="flex items-center justify-between">
        <h2 class="card-title text-lg">Shard Placement</h2>
        {#if topology === null}
          <button
            class="btn btn-ghost btn-sm"
            onclick={() => void fetchTopology()}
            disabled={loading}
          >
            {#if loading}
              <span class="loading loading-spinner loading-xs"></span>
            {:else}
              <RefreshCw class="h-4 w-4" />
            {/if}
            Refresh
          </button>
        {/if}
      </div>

      {#if loading && topology === null}
        <div class="flex justify-center py-8">
          <span class="loading loading-spinner loading-md text-primary"></span>
        </div>
      {:else if hasError && topology === null}
        <div class="alert alert-error mt-2">
          <span
            >Failed to load topology. Check the toast notifications for details.</span
          >
        </div>
      {:else if topology !== null && topology.shards.length === 0}
        <p class="text-base-content/50 py-4 text-center text-sm">
          No shards found on this node.
        </p>
      {:else if topology !== null}
        <div class="overflow-x-auto" data-testid="topology-shards-table">
          <table class="table table-zebra table-sm">
            <thead>
              <tr>
                <th>Shard ID</th>
                <th>Replica ID</th>
                <th>Role</th>
                <th>Leader ID</th>
                <th>Members</th>
              </tr>
            </thead>
            <tbody>
              {#each topology.shards as shard (shard.shardId)}
                <tr data-testid="topology-shard-row">
                  <td class="font-mono">{shard.shardId}</td>
                  <td class="font-mono">{shard.replicaId}</td>
                  <td>
                    <span
                      class="badge badge-sm"
                      class:badge-primary={shard.isLeader}
                      class:badge-ghost={!shard.isLeader}
                    >
                      {shard.isLeader ? "Leader" : "Follower"}
                    </span>
                  </td>
                  <td class="font-mono">{shard.leaderId}</td>
                  <td>
                    <div class="flex flex-col gap-0.5">
                      {#each Object.entries(shard.nodes ?? {}) as [replicaId, addr] (replicaId)}
                        <span class="font-mono text-xs">
                          {replicaId}: {addr}
                        </span>
                      {/each}
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  </div>
</div>
