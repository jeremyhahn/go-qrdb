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
  import type { Client, LeaseInfo } from "@qrdb/sdk";
  import type {
    Column,
    RowClickEvent,
  } from "../../components/core/DataTable/types.ts";
  import { CrudTable } from "../../components/core/CrudTable";
  import FormModal from "../../components/FormModal.svelte";
  import PageHeader from "../../components/PageHeader.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  // Grant form
  let grantTtl: string = $state("60");
  let showGrantModal: boolean = $state(false);
  let grantLoading: boolean = $state(false);

  // Lease list
  let leases: LeaseInfo[] = $state([]);
  let selectedLease: LeaseInfo | null = $state(null);
  let ttlRemaining: number | null = $state(null);

  // Auto-refresh
  let autoRefresh: boolean = $state(false);
  let refreshTimer: ReturnType<typeof setInterval> | null = null;

  // Shared
  let loading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Column definitions
  // ---------------------------------------------------------------------------

  const leaseColumns: Column<LeaseInfo>[] = [
    { id: "id", header: "ID", accessor: "id", sortable: true },
    {
      id: "ttl",
      header: "TTL",
      accessor: (row: LeaseInfo) => `${row.ttl}s`,
      sortable: true,
    },
    {
      id: "grantedTtl",
      header: "Granted TTL",
      accessor: (row: LeaseInfo) => `${row.grantedTtl}s`,
      sortable: true,
    },
    {
      id: "remaining",
      header: "Remaining",
      accessor: (row: LeaseInfo) => `${Math.round(ttlPercentage(row))}%`,
      sortable: true,
    },
  ];

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function ttlPercentage(lease: LeaseInfo): number {
    if (lease.grantedTtl <= 0) return 0;
    return Math.max(0, Math.min(100, (lease.ttl / lease.grantedTtl) * 100));
  }

  function ttlColor(pct: number): string {
    if (pct > 60) return "progress-success";
    if (pct > 30) return "progress-warning";
    return "progress-error";
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function loadLeases(): Promise<void> {
    loading = true;
    try {
      const result = await client.lease.list();
      leases = [...result];
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleGrant(): Promise<void> {
    const ttl = parseInt(grantTtl, 10);
    if (Number.isNaN(ttl) || ttl <= 0) {
      showToast("warning", "TTL must be a positive integer (seconds).");
      return;
    }
    grantLoading = true;
    try {
      const lease = await client.lease.grant(ttl);
      showToast(
        "success",
        `Lease granted with ID ${lease.id}, TTL ${lease.ttl}s.`,
      );
      showGrantModal = false;
      grantTtl = "60";
      await loadLeases();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      grantLoading = false;
    }
  }

  async function handleRevoke(id: number): Promise<void> {
    loading = true;
    try {
      await client.lease.revoke(id);
      if (selectedLease !== null && selectedLease.id === id) {
        selectedLease = null;
        ttlRemaining = null;
      }
      showToast("success", `Lease ${id} revoked.`);
      await loadLeases();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleKeepAlive(id: number): Promise<void> {
    try {
      await client.lease.keepAlive(id);
      showToast("success", `Lease ${id} keep-alive sent.`);
      await loadLeases();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  async function handleTimeToLive(lease: LeaseInfo): Promise<void> {
    try {
      const info = await client.lease.timeToLive(lease.id);
      selectedLease = info;
      ttlRemaining = info.ttl;
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  function handleRowClick(event: RowClickEvent<LeaseInfo>): void {
    void handleTimeToLive(event.row);
  }

  // ---------------------------------------------------------------------------
  // Auto-refresh lifecycle
  // ---------------------------------------------------------------------------

  $effect(() => {
    loadLeases();

    if (autoRefresh) {
      refreshTimer = setInterval(() => {
        loadLeases();
      }, 3000);
    }

    return () => {
      if (refreshTimer !== null) {
        clearInterval(refreshTimer);
        refreshTimer = null;
      }
    };
  });
</script>

<div class="flex flex-col gap-6">
  <PageHeader
    title="Leases"
    description="Manage TTL-bound keys with lease renewals"
  >
    {#snippet actions()}
      <label class="label cursor-pointer gap-2">
        <span class="label-text text-xs">Auto-refresh</span>
        <input
          type="checkbox"
          class="toggle toggle-primary toggle-xs"
          bind:checked={autoRefresh}
        />
      </label>
      <button class="btn btn-outline btn-sm" onclick={loadLeases}
        >Refresh</button
      >
      <button
        class="btn btn-primary btn-sm"
        onclick={() => (showGrantModal = true)}>Grant Lease</button
      >
    {/snippet}
  </PageHeader>

  <div class="flex gap-6">
    <!-- Active leases CrudTable -->
    <div class="flex-1">
      <CrudTable
        title="Active Leases"
        data={leases}
        columns={leaseColumns}
        {loading}
        showCreate={false}
        showDelete={false}
        onrowclick={handleRowClick}
        pageSize={10}
        emptyMessage="No active leases."
      />
    </div>

    <!-- Selected lease detail -->
    <div class="card bg-base-200 shadow-sm w-80">
      <div class="card-body p-5">
        <h3 class="card-title text-base font-semibold">Lease Detail</h3>
        {#if selectedLease !== null}
          {@const pct = ttlPercentage(selectedLease)}
          <div class="flex flex-col gap-3">
            <div class="bg-base-300 rounded p-3">
              <table class="table table-sm">
                <tbody>
                  <tr
                    ><th class="w-28">ID</th><td class="font-mono text-xs"
                      >{selectedLease.id}</td
                    ></tr
                  >
                  <tr><th>TTL</th><td>{selectedLease.ttl}s</td></tr>
                  <tr
                    ><th>Granted TTL</th><td>{selectedLease.grantedTtl}s</td
                    ></tr
                  >
                </tbody>
              </table>
            </div>
            <div>
              <span class="text-sm font-medium text-base-content/70"
                >Time Remaining</span
              >
              <div class="flex items-center gap-2 mt-1">
                <progress
                  class="progress flex-1 {ttlColor(pct)}"
                  value={pct}
                  max="100"
                ></progress>
                <span class="font-mono text-xs w-12 text-right"
                  >{Math.round(pct)}%</span
                >
              </div>
              {#if ttlRemaining !== null}
                <p class="text-xs text-base-content/50 mt-1">
                  {ttlRemaining}s remaining of {selectedLease.grantedTtl}s
                </p>
              {/if}
            </div>
            <div class="flex gap-1">
              <button
                class="btn btn-outline btn-xs flex-1"
                onclick={() => handleKeepAlive(selectedLease!.id)}
              >
                KeepAlive
              </button>
              <button
                class="btn btn-error btn-xs flex-1"
                onclick={() => handleRevoke(selectedLease!.id)}
              >
                Revoke
              </button>
            </div>
          </div>
        {:else}
          <p class="text-base-content/50 text-sm">
            Select a lease to view details.
          </p>
        {/if}
      </div>
    </div>
  </div>
</div>

<!-- Grant Lease modal -->
<FormModal
  open={showGrantModal}
  title="Grant Lease"
  submitLabel="Grant"
  loading={grantLoading}
  onsubmit={() => void handleGrant()}
  oncancel={() => (showGrantModal = false)}
>
  {#snippet body()}
    <label class="form-control w-full">
      <div class="label"><span class="label-text">TTL (seconds)</span></div>
      <input
        type="number"
        class="input input-bordered w-full"
        placeholder="60"
        min="1"
        bind:value={grantTtl}
      />
    </label>
  {/snippet}
</FormModal>
