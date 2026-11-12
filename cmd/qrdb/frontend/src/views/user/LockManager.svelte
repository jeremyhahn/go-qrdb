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
  import type { Client, LockInfo } from "@qrdb/sdk";
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

  // Acquire form
  let acquireKey: string = $state("");
  let acquireTtl: string = $state("60");
  let acquireHolderId: string = $state("");
  let acquireReentrant: boolean = $state(false);
  let showAcquireModal: boolean = $state(false);
  let acquireLoading: boolean = $state(false);
  /** When true, submitting the modal calls tryLock() instead of acquire(). */
  let acquireNonBlocking: boolean = $state(false);

  // Lock list
  let locks: LockInfo[] = $state([]);
  let selectedLock: LockInfo | null = $state(null);
  let filterPrefix: string = $state("");

  // Auto-refresh
  let autoRefresh: boolean = $state(false);
  let refreshTimer: ReturnType<typeof setInterval> | null = null;

  // Shared
  let loading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Column definitions
  // ---------------------------------------------------------------------------

  const lockColumns: Column<LockInfo>[] = [
    {
      id: "key",
      header: "Key",
      accessor: "key",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    { id: "holderId", header: "Holder", accessor: "holderId", sortable: true },
    {
      id: "fencingToken",
      header: "Fencing Token",
      accessor: "fencingToken",
      sortable: true,
    },
    {
      id: "ttl",
      header: "TTL",
      accessor: (row: LockInfo) => `${row.ttl}s`,
      sortable: true,
    },
    {
      id: "reentrant",
      header: "Reentrant",
      accessor: (row: LockInfo) => (row.reentrant ? "yes" : "no"),
    },
    {
      id: "acquireCount",
      header: "Acquire Count",
      accessor: "acquireCount",
      sortable: true,
    },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function loadLocks(): Promise<void> {
    loading = true;
    try {
      const result = await client.lock.list(filterPrefix.trim() || undefined);
      locks = [...result.locks];
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function resetAcquireForm(): void {
    acquireKey = "";
    acquireTtl = "60";
    acquireHolderId = "";
    acquireReentrant = false;
  }

  async function handleAcquireSubmit(): Promise<void> {
    const key = acquireKey.trim();
    if (key === "") {
      showToast("warning", "Key is required.");
      return;
    }
    const ttl = parseInt(acquireTtl, 10);
    if (Number.isNaN(ttl) || ttl <= 0) {
      showToast("warning", "TTL must be a positive integer (seconds).");
      return;
    }
    acquireLoading = true;
    try {
      const req = {
        key,
        ttl,
        reentrant: acquireReentrant,
        ...(acquireHolderId.trim() !== ""
          ? { holder_id: acquireHolderId.trim() }
          : {}),
      };
      const info = acquireNonBlocking
        ? await client.lock.tryLock(req)
        : await client.lock.acquire(req);
      showToast(
        "success",
        `Lock "${info.key}" acquired (fencing token ${info.fencingToken}).`,
      );
      showAcquireModal = false;
      resetAcquireForm();
      await loadLocks();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      acquireLoading = false;
    }
  }

  async function handleRelease(lock: LockInfo): Promise<void> {
    loading = true;
    try {
      await client.lock.release({
        key: lock.key,
        fencing_token: lock.fencingToken,
      });
      if (selectedLock !== null && selectedLock.key === lock.key) {
        selectedLock = null;
      }
      showToast("success", `Lock "${lock.key}" released.`);
      await loadLocks();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleExtend(lock: LockInfo): Promise<void> {
    try {
      const info = await client.lock.extend({
        key: lock.key,
        fencing_token: lock.fencingToken,
      });
      showToast("success", `Lock "${lock.key}" extended.`);
      selectedLock = info;
      await loadLocks();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  async function handleStatus(lock: LockInfo): Promise<void> {
    try {
      const info = await client.lock.status(lock.key);
      selectedLock = info;
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  function handleRowClick(event: RowClickEvent<LockInfo>): void {
    void handleStatus(event.row);
  }

  // ---------------------------------------------------------------------------
  // Auto-refresh lifecycle
  // ---------------------------------------------------------------------------

  $effect(() => {
    loadLocks();

    if (autoRefresh) {
      refreshTimer = setInterval(() => {
        loadLocks();
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
    title="Locks"
    description="Distributed lock management with fencing tokens"
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
      <button class="btn btn-outline btn-sm" onclick={loadLocks}>Refresh</button
      >
      <button
        class="btn btn-primary btn-sm"
        onclick={() => {
          resetAcquireForm();
          acquireNonBlocking = false;
          showAcquireModal = true;
        }}
      >
        Acquire Lock
      </button>
      <button
        class="btn btn-outline btn-sm"
        onclick={() => {
          resetAcquireForm();
          acquireNonBlocking = true;
          showAcquireModal = true;
        }}
      >
        Try Lock
      </button>
    {/snippet}
  </PageHeader>

  <div class="flex items-end gap-3">
    <label class="form-control w-full max-w-xs">
      <div class="label">
        <span class="label-text text-xs">Filter by key prefix</span>
      </div>
      <input
        type="text"
        class="input input-bordered input-sm w-full"
        placeholder="e.g. jobs/"
        bind:value={filterPrefix}
        onkeydown={(e) => {
          if (e.key === "Enter") void loadLocks();
        }}
      />
    </label>
    <button
      class="btn btn-ghost btn-sm"
      onclick={() => {
        filterPrefix = "";
        void loadLocks();
      }}
    >
      Clear
    </button>
  </div>

  <div class="flex gap-6">
    <!-- Active locks CrudTable -->
    <div class="flex-1">
      <CrudTable
        title="Active Locks"
        data={locks}
        columns={lockColumns}
        {loading}
        showCreate={false}
        showDelete={false}
        onrowclick={handleRowClick}
        pageSize={10}
        emptyMessage="No active locks."
        rowKey="key"
      />
    </div>

    <!-- Selected lock detail -->
    <div class="card bg-base-200 shadow-sm w-80">
      <div class="card-body p-5">
        <h3 class="card-title text-base font-semibold">Lock Detail</h3>
        {#if selectedLock !== null}
          <div class="flex flex-col gap-3">
            <div class="bg-base-300 rounded p-3">
              <table class="table table-sm">
                <tbody>
                  <tr
                    ><th class="w-32">Key</th><td
                      class="font-mono text-xs break-all">{selectedLock.key}</td
                    ></tr
                  >
                  <tr
                    ><th>Holder</th><td class="font-mono text-xs break-all"
                      >{selectedLock.holderId}</td
                    ></tr
                  >
                  <tr
                    ><th>Fencing Token</th><td>{selectedLock.fencingToken}</td
                    ></tr
                  >
                  <tr><th>TTL</th><td>{selectedLock.ttl}s</td></tr>
                  <tr
                    ><th>Reentrant</th><td
                      >{selectedLock.reentrant ? "yes" : "no"}</td
                    ></tr
                  >
                  <tr
                    ><th>Acquire Count</th><td>{selectedLock.acquireCount}</td
                    ></tr
                  >
                </tbody>
              </table>
            </div>
            <div class="flex gap-1">
              <button
                class="btn btn-outline btn-xs flex-1"
                onclick={() => handleExtend(selectedLock!)}
              >
                Extend
              </button>
              <button
                class="btn btn-error btn-xs flex-1"
                onclick={() => handleRelease(selectedLock!)}
              >
                Release
              </button>
            </div>
          </div>
        {:else}
          <p class="text-base-content/50 text-sm">
            Select a lock to view details.
          </p>
        {/if}
      </div>
    </div>
  </div>
</div>

<!-- Acquire / Try Lock modal -->
<FormModal
  open={showAcquireModal}
  title={acquireNonBlocking ? "Try Lock" : "Acquire Lock"}
  submitLabel={acquireNonBlocking ? "Try Lock" : "Acquire"}
  loading={acquireLoading}
  onsubmit={() => void handleAcquireSubmit()}
  oncancel={() => (showAcquireModal = false)}
>
  {#snippet body()}
    <div class="flex flex-col gap-4">
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Key</span></div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="resource key"
          bind:value={acquireKey}
        />
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">TTL (seconds)</span></div>
        <input
          type="number"
          class="input input-bordered w-full"
          placeholder="60"
          min="1"
          bind:value={acquireTtl}
        />
      </label>
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Holder ID (optional)</span>
        </div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="auto-generated when empty"
          bind:value={acquireHolderId}
        />
      </label>
      <label class="label cursor-pointer justify-start gap-2">
        <input
          type="checkbox"
          class="checkbox checkbox-sm"
          bind:checked={acquireReentrant}
        />
        <span class="label-text"
          >Allow re-acquisition by the same holder (reentrant)</span
        >
      </label>
    </div>
  {/snippet}
</FormModal>
