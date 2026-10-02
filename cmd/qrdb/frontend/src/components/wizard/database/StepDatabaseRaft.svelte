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
  import { dbWizardStore } from "$stores/database-wizard.svelte.ts";
  import type {
    RaftGroupsConfig,
    RaftClusterMode,
  } from "$stores/database-wizard.svelte.ts";
  import { connManagerStore } from "$stores/connection-manager.svelte.ts";
  import { AlertCircle, Info } from "lucide-svelte";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } =
    $props();

  // ---------------------------------------------------------------------------
  // Derived server topology from connection manager
  // ---------------------------------------------------------------------------

  /** True when the connected server is running in Raft cluster mode. */
  let isRaft = $derived(connManagerStore.activeServerMode === "raft");

  /**
   * Maximum replication factor is bounded by the number of available cluster
   * nodes (when known) or an upper bound of 9. The tenant's replica quota
   * (max_replicas_per_group / max_total_replicas) is enforced server-side.
   */
  let maxReplicationFactor = $derived(
    connManagerStore.activeClusterNodeCount > 0
      ? Math.min(connManagerStore.activeClusterNodeCount, 9)
      : 9,
  );

  // ---------------------------------------------------------------------------
  // Cluster mode (mirrors the CLI's `database create --cluster-mode` flag:
  // none, local, distributed). Only meaningful when connected to a raft-mode
  // server -- defaults to "distributed" (the prior automatic behavior) but is
  // now an explicit, user-editable choice for parity with the CLI.
  // ---------------------------------------------------------------------------

  const clusterModeOptions: readonly {
    id: RaftClusterMode;
    label: string;
    description: string;
  }[] = [
    {
      id: "none",
      label: "None — standard replication",
      description: "No dedicated Raft group for this database. Uses the server's standard multi-group Raft placement.",
    },
    {
      id: "local",
      label: "Local — single-node dedicated group",
      description: "A dedicated Raft group confined to this node. Suitable for local development or single-node dedicated storage.",
    },
    {
      id: "distributed",
      label: "Distributed — multi-node placement",
      description: "A dedicated Raft group with replicas placed across cluster nodes automatically.",
    },
  ] as const;

  // "distributed" is the default once connected to a raft-mode server
  // (the prior automatic behavior). The store's own default ("none") is
  // only meaningful for a standalone server, which never renders this
  // selector, so it is not treated as a sticky choice here -- only an
  // explicit prior "local" selection (e.g. after navigating Back) is
  // preserved.
  let clusterMode: RaftClusterMode = $state(
    dbWizardStore.raftConfig?.clusterMode === "local" ? "local" : "distributed",
  );

  // ---------------------------------------------------------------------------
  // Raft groups configuration (only for raft-mode servers)
  // ---------------------------------------------------------------------------

  let raftGroupShardCount: number = $state(
    dbWizardStore.raftConfig?.raftGroups?.shardCount ?? 1,
  );
  // Replication factor maps to CreateDatabaseRequest.replicas.
  let raftGroupReplicationFactor: number = $state(dbWizardStore.replicas || 1);

  // Number of nodes to place a "local" dedicated Raft group across.
  let localNodeCount: number = $state(
    dbWizardStore.raftConfig?.localNodeCount || 3,
  );

  // Whether to enable per-database WAL encryption under the tenant's own key.
  let dedicatedWal: boolean = $state(dbWizardStore.dedicatedWal || false);

  // ---------------------------------------------------------------------------
  // Handlers
  // ---------------------------------------------------------------------------

  function handleNext(): void {
    if (!isRaft) {
      // Standalone server: no Raft configuration needed.
      dbWizardStore.raftConfig = {
        clusterMode: "none",
        replicaId: 1,
        raftAddress: "localhost:63001",
        peers: [],
        localNodeCount: 0,
        raftGroups: null,
      };
      dbWizardStore.replicas = 1;
      dbWizardStore.dedicatedWal = false;
      onNext();
      return;
    }

    if (clusterMode === "none") {
      // Explicit opt-out of a dedicated Raft group: fall back to standard
      // server-managed replication for this database.
      dbWizardStore.raftConfig = {
        clusterMode: "none",
        replicaId: 1,
        raftAddress: "",
        peers: [],
        localNodeCount: 0,
        raftGroups: null,
      };
      dbWizardStore.replicas = 1;
      dbWizardStore.dedicatedWal = false;
      onNext();
      return;
    }

    // Raft server, "local" or "distributed": persist raft groups
    // configuration. The replication factor flows through to
    // CreateDatabaseRequest.replicas.
    const replicas = Math.max(1, Math.trunc(raftGroupReplicationFactor || 1));
    const raftGroups: RaftGroupsConfig = {
      shardCount: raftGroupShardCount,
      replicationFactor: replicas,
    };
    dbWizardStore.raftConfig = {
      clusterMode,
      replicaId: 1,
      raftAddress: "",
      peers: [],
      localNodeCount: clusterMode === "local" ? localNodeCount : 0,
      raftGroups,
    };
    dbWizardStore.replicas = replicas;
    dbWizardStore.dedicatedWal = dedicatedWal;
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Raft Configuration</h2>
    <p class="text-base-content/70 mt-2">
      Configure distributed consensus for this database.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-6">
    {#if !isRaft}
      <!-- Standalone server info -->
      <div role="alert" class="alert alert-info">
        <Info class="h-5 w-5 shrink-0" strokeWidth={2} />
        <div>
          <p class="font-semibold">Standalone Mode</p>
          <p class="text-sm mt-1">
            This database will run in standalone mode (no replication). The
            connected server is not configured for Raft clustering. To enable
            distributed consensus, reconnect to a server running in Raft mode.
          </p>
        </div>
      </div>
    {:else}
      <!-- Raft cluster: groups configuration -->
      <div role="alert" class="alert alert-success">
        <Info class="h-5 w-5 shrink-0" strokeWidth={2} />
        <div>
          <p class="font-semibold">Raft Cluster Detected</p>
          <p class="text-sm mt-1">
            The server manages node assignment automatically. Configure how
            this database's data is partitioned across the cluster.
          </p>
          {#if connManagerStore.activeClusterNodeCount > 0}
            <p class="text-sm mt-1">
              Cluster size: <strong>{connManagerStore.activeClusterNodeCount} nodes</strong>.
            </p>
          {/if}
        </div>
      </div>

      <div class="divider text-base-content/40">Cluster Mode</div>

      <!-- Cluster mode selector: mirrors the CLI's `--cluster-mode` flag
           (none, local, distributed). -->
      <div class="form-control">
        <label class="label" for="db-cluster-mode">
          <span class="label-text font-semibold">Cluster Mode</span>
        </label>
        <select
          id="db-cluster-mode"
          class="select select-bordered w-full"
          data-testid="db-cluster-mode-select"
          bind:value={clusterMode}
        >
          {#each clusterModeOptions as opt (opt.id)}
            <option value={opt.id}>{opt.label}</option>
          {/each}
        </select>
        <div class="label">
          <span class="label-text-alt text-base-content/60">
            {clusterModeOptions.find((opt) => opt.id === clusterMode)?.description ?? ""}
          </span>
        </div>
      </div>

      {#if clusterMode === "none"}
        <div role="alert" class="alert alert-info text-sm">
          <Info class="h-5 w-5 shrink-0" strokeWidth={2} />
          <span>
            This database will use the server's standard replication instead
            of a dedicated Raft group.
          </span>
        </div>
      {:else}
        <div class="divider text-base-content/40">Raft Groups</div>

        <div class="rounded-box bg-base-200/50 space-y-4 p-4">
          <p class="text-base-content/60 text-sm">
            Configure how this database's data is partitioned across the cluster.
            Each raft group is a replicated shard. The server assigns node
            membership automatically.
          </p>

          {#if clusterMode === "local"}
            <div class="form-control">
              <label class="label" for="db-local-node-count">
                <span class="label-text font-semibold">Local Node Count</span>
              </label>
              <input
                id="db-local-node-count"
                type="number"
                class="input input-bordered w-full"
                min="1"
                max="9"
                step="1"
                bind:value={localNodeCount}
              />
              <label class="label" for="db-local-node-count">
                <span class="label-text-alt text-base-content/60">
                  Number of local nodes the dedicated Raft group is confined to.
                </span>
              </label>
            </div>
          {/if}

          <div class="grid grid-cols-2 gap-4">
            <div class="form-control">
              <label class="label" for="db-replication-factor">
                <span class="label-text font-semibold">Replication Factor (Replicas)</span>
              </label>
              <input
                id="db-replication-factor"
                type="number"
                class="input input-bordered w-full"
                min="1"
                max={maxReplicationFactor}
                step="1"
                bind:value={raftGroupReplicationFactor}
              />
              <label class="label" for="db-replication-factor">
                <span class="label-text-alt text-base-content/60">
                  Voting replicas per shard ({raftGroupReplicationFactor}
                  replica{raftGroupReplicationFactor !== 1 ? "s" : ""}). Min 1, max {maxReplicationFactor}.
                  Bounded by the tenant's replica quota and the cluster size.
                </span>
              </label>
            </div>

            <div class="form-control">
              <label class="label" for="db-shard-count">
                <span class="label-text font-semibold">Number of Shards</span>
              </label>
              <input
                id="db-shard-count"
                type="number"
                class="input input-bordered w-full"
                min="1"
                max="256"
                bind:value={raftGroupShardCount}
              />
              <label class="label" for="db-shard-count">
                <span class="label-text-alt text-base-content/60">
                  Data is partitioned across {raftGroupShardCount} raft
                  group{raftGroupShardCount !== 1 ? "s" : ""}.
                </span>
              </label>
            </div>
          </div>

          <div class="text-base-content/50 text-xs">
            Total raft groups: {raftGroupShardCount} &times;
            {raftGroupReplicationFactor} replicas = {raftGroupShardCount *
              raftGroupReplicationFactor} group members across the cluster.
          </div>

          <div class="form-control">
            <label class="label cursor-pointer justify-start gap-3">
              <input
                id="db-dedicated-wal"
                type="checkbox"
                class="toggle toggle-primary toggle-sm"
                data-testid="db-dedicated-wal-toggle"
                bind:checked={dedicatedWal}
              />
              <span class="label-text font-semibold">Dedicated WAL</span>
            </label>
            <label class="label" for="db-dedicated-wal">
              <span class="label-text-alt text-base-content/60">
                Encrypts this database's Raft log under the tenant's own key.
                Requires the <code>allow_dedicated_wal</code> entitlement and
                counts against the <code>max_dedicated_wal_groups</code> quota.
              </span>
            </label>
          </div>
        </div>

        {#if clusterMode === "distributed"}
          <div role="alert" class="alert alert-success text-sm" data-testid="raft-multinode-ready">
            <AlertCircle class="h-5 w-5 shrink-0" />
            <span>
              Multi-node placement of dedicated-raft groups is production-ready.
              Replicas are distributed across cluster nodes automatically; RF&gt;1 is
              fully supported, quota-metered, and placed at create time.
            </span>
          </div>
        {/if}
      {/if}
    {/if}
  </div>

  <div class="flex w-full max-w-2xl justify-between">
    <button class="btn btn-ghost" onclick={onBack}>Back</button>
    <button class="btn btn-primary" onclick={handleNext}>Next</button>
  </div>
</div>
