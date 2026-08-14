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
  import type { Client, PeersResponse, ShardPeers, PeerInfo } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { RefreshCw, ChevronDown, UserMinus, PlusCircle, Camera, Minus } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  type DiscoveryMethod = "static" | "multicast" | "dns";

  // ---------------------------------------------------------------------------
  // Peer state
  // ---------------------------------------------------------------------------

  let peers = $state<PeersResponse | null>(null);
  let loading: boolean = $state(true);
  let hasError: boolean = $state(false);

  // Add member form state
  let addShardId: string = $state("");
  let addReplicaId: string = $state("");
  let addRaftAddress: string = $state("");
  let addLoading: boolean = $state(false);

  // Remove member form state
  let removeShardId: string = $state("");
  let removeReplicaId: string = $state("");
  let removeLoading: boolean = $state(false);

  // Snapshot state
  let snapshotShardId: string = $state("");
  let snapshotLoading: boolean = $state(false);

  // Drain state
  let drainLoading: boolean = $state(false);
  let showDrainConfirm: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Discovery configuration state
  // ---------------------------------------------------------------------------

  let discoveryOpen: boolean = $state(false);
  let discoveryMethod: DiscoveryMethod = $state("static");
  let discoveryMulticastSecret: string = $state("");
  let discoveryDnsDomain: string = $state("");
  let discoverySaveLoading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Flattened peer rows for table display. */
  interface PeerRow {
    readonly shardId: number;
    readonly replicaId: number;
    readonly address: string;
    readonly isLeader: boolean;
  }

  let peerRows = $derived<readonly PeerRow[]>(
    peers !== null
      ? peers.shards.flatMap((shard: ShardPeers) =>
          shard.peers.map((peer: PeerInfo) => ({
            shardId: shard.shardId,
            replicaId: peer.replicaId,
            address: peer.address,
            isLeader: peer.isLeader,
          })),
        )
      : [],
  );

  // ---------------------------------------------------------------------------
  // Peer actions
  // ---------------------------------------------------------------------------

  /** Fetches peer membership data from the admin API. */
  async function fetchPeers(): Promise<void> {
    try {
      loading = true;
      peers = await client.admin.peers();
      hasError = false;
    } catch (err: unknown) {
      hasError = true;
      showToast("error", err instanceof Error ? err.message : "Failed to fetch peers");
    } finally {
      loading = false;
    }
  }

  /** Adds a new member to the specified shard. */
  async function handleAddMember(): Promise<void> {
    const shard = parseInt(addShardId, 10);
    const replica = parseInt(addReplicaId, 10);
    if (isNaN(shard) || isNaN(replica) || addRaftAddress.trim() === "") {
      showToast("warning", "All fields are required for adding a member");
      return;
    }

    try {
      addLoading = true;
      await client.admin.addMember(shard, replica, addRaftAddress.trim());
      showToast("success", `Added member ${replica} to shard ${shard}`);
      addShardId = "";
      addReplicaId = "";
      addRaftAddress = "";
      await fetchPeers();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to add member";
      showToast("error", msg);
    } finally {
      addLoading = false;
    }
  }

  /** Removes a member from the specified shard. */
  async function handleRemoveMember(): Promise<void> {
    const shard = parseInt(removeShardId, 10);
    const replica = parseInt(removeReplicaId, 10);
    if (isNaN(shard) || isNaN(replica)) {
      showToast("warning", "Shard ID and Replica ID are required");
      return;
    }

    try {
      removeLoading = true;
      await client.admin.removeMember(shard, replica);
      showToast("success", `Removed member ${replica} from shard ${shard}`);
      removeShardId = "";
      removeReplicaId = "";
      await fetchPeers();
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to remove member";
      showToast("error", msg);
    } finally {
      removeLoading = false;
    }
  }

  /** Requests a snapshot for the specified shard. */
  async function handleSnapshot(): Promise<void> {
    const shard = parseInt(snapshotShardId, 10);
    if (isNaN(shard)) {
      showToast("warning", "A valid Shard ID is required");
      return;
    }

    try {
      snapshotLoading = true;
      await client.admin.requestSnapshot(shard);
      showToast("success", `Snapshot requested for shard ${shard}`);
      snapshotShardId = "";
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to request snapshot";
      showToast("error", msg);
    } finally {
      snapshotLoading = false;
    }
  }

  /** Drains the node after user confirmation. */
  async function handleDrain(): Promise<void> {
    try {
      drainLoading = true;
      await client.admin.drain();
      showToast("success", "Node drain initiated");
      showDrainConfirm = false;
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to drain node";
      showToast("error", msg);
    } finally {
      drainLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Discovery actions
  // ---------------------------------------------------------------------------

  /**
   * Saves the discovery configuration. The backend API for persisting
   * discovery config may not be available yet; this shows a success toast
   * as a placeholder for the real API call.
   */
  async function handleSaveDiscovery(): Promise<void> {
    try {
      discoverySaveLoading = true;

      // Validate method-specific fields before saving.
      if (discoveryMethod === "multicast" && discoveryMulticastSecret.trim() === "") {
        showToast("warning", "Shared secret is required for multicast discovery");
        return;
      }
      if (discoveryMethod === "dns" && discoveryDnsDomain.trim() === "") {
        showToast("warning", "Domain is required for DNS discovery");
        return;
      }

      // The runtime API for changing discovery method does not yet exist on
      // the server. Setting discovery requires restarting the daemon with the
      // new --discovery flag. Surface this honestly instead of pretending the
      // setting was persisted.
      showToast(
        "warning",
        "Discovery is set at daemon startup via --discovery. " +
          "Restart qrdbd with the new flag to apply.",
      );
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to save discovery config";
      showToast("error", msg);
    } finally {
      discoverySaveLoading = false;
    }
  }

  onMount(() => {
    void fetchPeers();
  });
</script>

<PageHeader title="Cluster" description="Manage Raft peers, membership, and discovery configuration" />

<div class="flex flex-col gap-6">
  <!-- Discovery Configuration (collapsible) -->
  <div class="card bg-base-200 shadow">
    <div class="card-body p-4">
      <button
        class="flex w-full items-center justify-between text-left"
        aria-expanded={discoveryOpen}
        onclick={() => { discoveryOpen = !discoveryOpen; }}
      >
        <h2 class="card-title text-base">Discovery Configuration</h2>
        <ChevronDown
          class="h-4 w-4 text-base-content/50 transition-transform duration-200"
          style={discoveryOpen ? "transform: rotate(180deg)" : ""}
        />
      </button>
      {#if !discoveryOpen}
        <p class="text-base-content/60 mt-1 text-xs">
          Configure how cluster nodes discover each other. Click to expand.
        </p>
      {/if}
    </div>

    {#if discoveryOpen}
      <div class="border-t border-base-content/10 px-5 pb-5 pt-4">
        <!-- Method selector -->
        <label class="form-control w-full max-w-xs">
          <div class="label">
            <span class="label-text font-semibold">Discovery Method</span>
          </div>
          <select
            class="select select-bordered select-sm w-full"
            bind:value={discoveryMethod}
          >
            <option value="static">Static — manually specified peers</option>
            <option value="multicast">Multicast — UDP multicast with shared secret</option>
            <option value="dns">DNS — SRV record-based discovery</option>
          </select>
        </label>

        <!-- Method-specific configuration -->
        <div class="mt-4">
          {#if discoveryMethod === "static"}
            <div class="alert alert-info">
              <span class="text-sm">
                Static discovery uses the peer list below. Add or remove members
                using the management actions section.
              </span>
            </div>

          {:else if discoveryMethod === "multicast"}
            <label class="form-control w-full max-w-sm">
              <div class="label">
                <span class="label-text">Shared Secret</span>
                <span class="label-text-alt text-base-content/50">
                  Used to authenticate multicast announcements
                </span>
              </div>
              <input
                type="password"
                class="input input-bordered input-sm w-full"
                placeholder="Multicast shared secret"
                bind:value={discoveryMulticastSecret}
              />
            </label>

          {:else if discoveryMethod === "dns"}
            <label class="form-control w-full max-w-sm">
              <div class="label">
                <span class="label-text">DNS Domain</span>
                <span class="label-text-alt text-base-content/50">
                  SRV records will be queried under this domain
                </span>
              </div>
              <input
                type="text"
                class="input input-bordered input-sm w-full"
                placeholder="e.g. cluster.example.com"
                bind:value={discoveryDnsDomain}
              />
            </label>
          {/if}
        </div>

        <!-- Save button -->
        <div class="mt-5">
          <button
            class="btn btn-primary btn-sm"
            onclick={() => void handleSaveDiscovery()}
            disabled={discoverySaveLoading}
          >
            {#if discoverySaveLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Save
          </button>
        </div>
      </div>
    {/if}
  </div>

  <!-- Peers table -->
  <div class="card bg-base-200 shadow">
    <div class="card-body p-5">
      <div class="flex items-center justify-between">
        <h2 class="card-title text-lg">Cluster Peers</h2>
        <button
          class="btn btn-ghost btn-sm"
          onclick={() => void fetchPeers()}
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

      {#if loading && peers === null}
        <div class="flex justify-center py-8">
          <span class="loading loading-spinner loading-md text-primary"></span>
        </div>
      {:else if hasError && peers === null}
        <div class="alert alert-error mt-2">
          <span>Failed to load cluster peers. Check the toast notifications for details.</span>
        </div>
      {:else if peerRows.length === 0}
        <p class="text-base-content/50 py-4 text-center text-sm">
          No peers found.
        </p>
      {:else}
        <div class="overflow-x-auto">
          <table class="table table-zebra table-sm">
            <thead>
              <tr>
                <th>Shard ID</th>
                <th>Replica ID</th>
                <th>Address</th>
                <th>Role</th>
              </tr>
            </thead>
            <tbody>
              {#each peerRows as row (row.shardId + "-" + row.replicaId)}
                <tr>
                  <td class="font-mono">{row.shardId}</td>
                  <td class="font-mono">{row.replicaId}</td>
                  <td class="font-mono text-sm">{row.address}</td>
                  <td>
                    <span
                      class="badge badge-sm"
                      class:badge-primary={row.isLeader}
                      class:badge-ghost={!row.isLeader}
                    >
                      {row.isLeader ? "Leader" : "Follower"}
                    </span>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  </div>

  <!-- Management actions row -->
  <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
    <!-- Add member -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Add Member</h2>
        <div class="form-control gap-5">
          <input
            type="number"
            class="input input-bordered input-sm"
            placeholder="Shard ID"
            bind:value={addShardId}
          />
          <input
            type="number"
            class="input input-bordered input-sm"
            placeholder="Replica ID"
            bind:value={addReplicaId}
          />
          <input
            type="text"
            class="input input-bordered input-sm"
            placeholder="Raft Address (e.g. 10.0.0.2:63001)"
            bind:value={addRaftAddress}
          />
          <button
            class="btn btn-primary btn-sm mt-2"
            onclick={() => void handleAddMember()}
            disabled={addLoading}
          >
            {#if addLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Add Member
          </button>
        </div>
      </div>
    </div>

    <!-- Remove member -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Remove Member</h2>
        <div class="form-control gap-5">
          <input
            type="number"
            class="input input-bordered input-sm"
            placeholder="Shard ID"
            bind:value={removeShardId}
          />
          <input
            type="number"
            class="input input-bordered input-sm"
            placeholder="Replica ID"
            bind:value={removeReplicaId}
          />
          <button
            class="btn btn-error btn-sm mt-2"
            title="Remove this replica from the shard. The node will stop participating in consensus for this shard."
            onclick={() => void handleRemoveMember()}
            disabled={removeLoading}
          >
            {#if removeLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {:else}
              <Minus class="h-4 w-4" />
            {/if}
            Remove Member
          </button>
        </div>
      </div>
    </div>

    <!-- Snapshot -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Request Snapshot</h2>
        <div class="form-control gap-5">
          <input
            type="number"
            class="input input-bordered input-sm"
            placeholder="Shard ID"
            bind:value={snapshotShardId}
          />
          <button
            class="btn btn-secondary btn-sm mt-2"
            onclick={() => void handleSnapshot()}
            disabled={snapshotLoading}
          >
            {#if snapshotLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Request Snapshot
          </button>
        </div>
      </div>
    </div>

    <!-- Drain -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Drain Node</h2>
        <p class="text-base-content/70 text-sm">
          Transitions this node into drain mode. New proposals will be rejected
          while in-flight operations complete.
        </p>

        {#if showDrainConfirm}
          <div class="alert alert-warning mt-2">
            <span>Are you sure? This will stop the node from accepting new proposals.</span>
          </div>
          <div class="mt-2 flex gap-2">
            <button
              class="btn btn-error btn-sm"
              onclick={() => void handleDrain()}
              disabled={drainLoading}
            >
              {#if drainLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Confirm Drain
            </button>
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => { showDrainConfirm = false; }}
            >
              Cancel
            </button>
          </div>
        {:else}
          <button
            class="btn btn-warning btn-sm mt-2"
            title="Drain this node. New proposals will be rejected while existing operations complete. The node will require reconfiguration to re-join."
            onclick={() => { showDrainConfirm = true; }}
          >
            <UserMinus class="h-4 w-4" />
            Drain
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>
