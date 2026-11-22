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
<!--
  Volume pool catalog (docs/volume-pools/api.md): list every pool, create
  one, and open a pool's detail panel to inspect its per-node reports and
  assigned tenants, change its description, capacity or overcommit, or
  delete it. Every operation goes through client.volumePools.
-->
<script lang="ts">
  import type { Client } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";

  let { client }: { client: Client } = $props();

  // The SDK's package index does not re-export the volume pool types, so
  // they are derived from the VolumePoolClient method signatures.
  type Pools = Client["volumePools"];
  type VolumePool = Awaited<ReturnType<Pools["list"]>>["pools"][number];
  type VolumePoolDetail = Awaited<ReturnType<Pools["get"]>>;
  type VolumePoolTenant = Awaited<ReturnType<Pools["listTenants"]>>["tenants"][number];
  type CreateVolumePoolRequest = Parameters<Pools["create"]>[0];
  type UpdateVolumePoolRequest = Parameters<Pools["update"]>[1];

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let pools: VolumePool[] = $state([]);
  let loading: boolean = $state(true);

  /** Create form. */
  let showCreate: boolean = $state(false);
  let createName: string = $state("");
  let createProvider: string = $state("dir");
  let createDescription: string = $state("");
  // Number inputs bind a number once edited and "" when cleared.
  let createCapacity: number | string = $state("");
  let createOvercommit: number | string = $state("");
  let creating: boolean = $state(false);

  /** Detail panel. */
  let detail: VolumePoolDetail | null = $state(null);
  let detailLoading: boolean = $state(false);
  let tenants: VolumePoolTenant[] = $state([]);
  let tenantsNextToken: string = $state("");
  let editDescription: string = $state("");
  let editCapacity: number | string = $state("");
  let editOvercommit: number | string = $state("");

  /** Returns a number input's bound value as trimmed text ("" when empty). */
  function fieldText(v: number | string | null | undefined): string {
    return v === null || v === undefined ? "" : String(v).trim();
  }
  let saving: boolean = $state(false);
  let confirmDelete: boolean = $state(false);

  /** Page size for the assigned-tenant listing. */
  const TENANT_PAGE_SIZE = 50;

  // ---------------------------------------------------------------------------
  // Formatting
  // ---------------------------------------------------------------------------

  const UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"] as const;

  /** Formats a byte count in binary units; zero capacity means derived. */
  function formatBytes(n: number): string {
    let v = n;
    let i = 0;
    while (v >= 1024 && i < UNITS.length - 1) {
      v /= 1024;
      i++;
    }
    return `${i === 0 ? v : v.toFixed(1)} ${UNITS[i]}`;
  }

  function formatCapacity(n: number): string {
    return n === 0 ? "derived" : formatBytes(n);
  }

  function errMessage(err: unknown, fallback: string): string {
    return err instanceof Error ? err.message : fallback;
  }

  // ---------------------------------------------------------------------------
  // Column Definitions
  // ---------------------------------------------------------------------------

  const columns: Column<VolumePool>[] = [
    { id: "name", header: "Name", accessor: "name", sortable: true, filterable: true, filterType: "text" },
    { id: "provider", header: "Provider", accessor: "provider", sortable: true },
    { id: "capacity", header: "Capacity", accessor: (row: VolumePool) => formatCapacity(row.capacityBytes), sortable: true },
    { id: "reserved", header: "Reserved", accessor: (row: VolumePool) => formatBytes(row.reservedBytes), sortable: true },
    { id: "used", header: "Used", accessor: (row: VolumePool) => formatBytes(row.usedBytes), sortable: true },
    { id: "tenants", header: "Tenants", accessor: (row: VolumePool) => String(row.tenantCount), sortable: true, align: "center" },
    { id: "overcommit", header: "Overcommit", accessor: (row: VolumePool) => `${row.overcommit}x`, sortable: true, align: "center" },
    {
      id: "nodes",
      header: "Nodes (ready/total)",
      accessor: (row: VolumePool) => {
        const n = row.nodes;
        return `${n.ready}/${n.ready + n.locked + n.unavailable + n.unmapped}`;
      },
      align: "center",
    },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the pool catalog. */
  async function fetchPools(): Promise<void> {
    try {
      loading = true;
      const resp = await client.volumePools.list();
      pools = [...(resp.pools ?? [])];
    } catch (err: unknown) {
      showToast("error", errMessage(err, "Failed to list volume pools"));
    } finally {
      loading = false;
    }
  }

  function openCreate(): void {
    createName = "";
    createProvider = "dir";
    createDescription = "";
    createCapacity = "";
    createOvercommit = "";
    showCreate = true;
  }

  /** Submits the create form. */
  async function submitCreate(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const name = createName.trim();
    if (!name) {
      showToast("error", "Pool name is required");
      return;
    }
    const req: CreateVolumePoolRequest = {
      name,
      ...(createProvider.trim() ? { provider: createProvider.trim() } : {}),
      ...(createDescription.trim() ? { description: createDescription.trim() } : {}),
      ...(fieldText(createCapacity) ? { capacityBytes: Number(createCapacity) } : {}),
      ...(fieldText(createOvercommit) ? { overcommit: Number(createOvercommit) } : {}),
    };
    try {
      creating = true;
      await client.volumePools.create(req);
      showToast("success", `Volume pool "${name}" created`);
      showCreate = false;
      await fetchPools();
    } catch (err: unknown) {
      showToast("error", errMessage(err, "Failed to create volume pool"));
    } finally {
      creating = false;
    }
  }

  /** Loads one page of the pool's assigned tenants. */
  async function loadTenants(pool: string, pageToken: string): Promise<void> {
    const page = await client.volumePools.listTenants(pool, {
      pageSize: TENANT_PAGE_SIZE,
      ...(pageToken ? { pageToken } : {}),
    });
    tenants = pageToken ? [...tenants, ...page.tenants] : [...page.tenants];
    tenantsNextToken = page.nextPageToken ?? "";
  }

  /** Opens the detail panel: get plus the first page of tenants. */
  async function openDetail(name: string): Promise<void> {
    try {
      detailLoading = true;
      const d = await client.volumePools.get(name);
      detail = d;
      editDescription = d.description ?? "";
      editCapacity = String(d.capacityBytes);
      editOvercommit = String(d.overcommit);
      await loadTenants(name, "");
    } catch (err: unknown) {
      showToast("error", errMessage(err, "Failed to load volume pool"));
    } finally {
      detailLoading = false;
    }
  }

  async function loadMoreTenants(): Promise<void> {
    if (!detail || !tenantsNextToken) return;
    try {
      await loadTenants(detail.name, tenantsNextToken);
    } catch (err: unknown) {
      showToast("error", errMessage(err, "Failed to list pool tenants"));
    }
  }

  function closeDetail(): void {
    detail = null;
    tenants = [];
    tenantsNextToken = "";
    void fetchPools();
  }

  /** Submits the edit form; only changed fields are sent. */
  async function submitUpdate(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!detail) return;
    const req: {
      description?: string;
      capacityBytes?: number;
      overcommit?: number;
    } = {};
    if (editDescription !== (detail.description ?? "")) req.description = editDescription;
    if (fieldText(editCapacity) !== "" && Number(editCapacity) !== detail.capacityBytes) {
      req.capacityBytes = Number(editCapacity);
    }
    if (fieldText(editOvercommit) !== "" && Number(editOvercommit) !== detail.overcommit) {
      req.overcommit = Number(editOvercommit);
    }
    if (Object.keys(req).length === 0) {
      showToast("info", "No changes to save");
      return;
    }
    try {
      saving = true;
      await client.volumePools.update(detail.name, req as UpdateVolumePoolRequest);
      showToast("success", `Volume pool "${detail.name}" updated`);
      await openDetail(detail.name);
    } catch (err: unknown) {
      showToast("error", errMessage(err, "Failed to update volume pool"));
    } finally {
      saving = false;
    }
  }

  /** Deletes the open pool after confirmation. */
  async function doDelete(): Promise<void> {
    confirmDelete = false;
    if (!detail) return;
    const name = detail.name;
    try {
      await client.volumePools.delete(name);
      showToast("success", `Volume pool "${name}" deleted`);
      closeDetail();
    } catch (err: unknown) {
      showToast("error", errMessage(err, "Failed to delete volume pool"));
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchPools();
  });
</script>

{#if detail}
  <div class="flex flex-col gap-6" data-testid="volume-pool-detail">
    <div class="flex items-center gap-3">
      <button class="btn btn-ghost btn-sm" data-testid="volume-pool-back" onclick={closeDetail}>
        ← Back
      </button>
      <h2 class="text-xl font-semibold" data-testid="volume-pool-detail-name">{detail.name}</h2>
      <span class="badge badge-outline">{detail.provider}</span>
      <div class="flex-1"></div>
      <button
        class="btn btn-error btn-sm"
        data-testid="volume-pool-delete"
        onclick={() => (confirmDelete = true)}
      >
        Delete Pool
      </button>
    </div>

    <div class="stats shadow" data-testid="volume-pool-stats">
      <div class="stat">
        <div class="stat-title">Capacity</div>
        <div class="stat-value text-lg" data-testid="volume-pool-capacity">{formatCapacity(detail.capacityBytes)}</div>
      </div>
      <div class="stat">
        <div class="stat-title">Reserved</div>
        <div class="stat-value text-lg">{formatBytes(detail.reservedBytes)}</div>
      </div>
      <div class="stat">
        <div class="stat-title">Used</div>
        <div class="stat-value text-lg">{formatBytes(detail.usedBytes)}</div>
      </div>
      <div class="stat">
        <div class="stat-title">Overcommit</div>
        <div class="stat-value text-lg" data-testid="volume-pool-overcommit">{detail.overcommit}x</div>
      </div>
      <div class="stat">
        <div class="stat-title">Tenants</div>
        <div class="stat-value text-lg">{detail.tenantCount}</div>
      </div>
    </div>
    {#if detail.description}
      <p class="text-sm opacity-80" data-testid="volume-pool-description">{detail.description}</p>
    {/if}

    <div class="card bg-base-200">
      <div class="card-body">
        <h3 class="card-title text-base">Node reports</h3>
        <table class="table table-sm" data-testid="volume-pool-nodes">
          <thead>
            <tr><th>Node</th><th>State</th><th>Size</th><th>Free</th><th>Detail</th><th>Reported</th></tr>
          </thead>
          <tbody>
            {#each detail.nodeReports ?? [] as r (r.nodeId)}
              <tr>
                <td>{r.nodeId}</td>
                <td>{r.state}</td>
                <td>{r.sizeBytes !== undefined ? formatBytes(r.sizeBytes) : "—"}</td>
                <td>{r.freeBytes !== undefined ? formatBytes(r.freeBytes) : "—"}</td>
                <td>{r.detail ?? ""}</td>
                <td>{r.reportedAt ?? ""}</td>
              </tr>
            {:else}
              <tr><td colspan="6" class="text-center opacity-60">No node has reported this pool.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>

    <div class="card bg-base-200">
      <div class="card-body">
        <h3 class="card-title text-base">Assigned tenants</h3>
        <table class="table table-sm" data-testid="volume-pool-tenants">
          <thead>
            <tr><th>Tenant</th><th>Reserved</th></tr>
          </thead>
          <tbody>
            {#each tenants as t (t.tenantId)}
              <tr>
                <td>{t.tenantId}</td>
                <td>{formatBytes(t.reservedBytes)}</td>
              </tr>
            {:else}
              <tr><td colspan="2" class="text-center opacity-60">No tenants are assigned to this pool.</td></tr>
            {/each}
          </tbody>
        </table>
        {#if tenantsNextToken}
          <button class="btn btn-sm btn-ghost self-start" data-testid="volume-pool-tenants-more" onclick={loadMoreTenants}>
            Load more
          </button>
        {/if}
      </div>
    </div>

    <form class="card bg-base-200" data-testid="volume-pool-edit-form" onsubmit={submitUpdate}>
      <div class="card-body gap-3">
        <h3 class="card-title text-base">Edit pool</h3>
        <p class="text-xs opacity-70">The provider is immutable. Lowering capacity below the current reservation is refused.</p>
        <label class="form-control" for="volume-pool-edit-description">
          <div class="label"><span class="label-text font-semibold">Description</span></div>
          <input id="volume-pool-edit-description" class="input input-bordered input-sm" bind:value={editDescription} />
        </label>
        <label class="form-control" for="volume-pool-edit-capacity">
          <div class="label"><span class="label-text font-semibold">Capacity (bytes, 0 = derived)</span></div>
          <input id="volume-pool-edit-capacity" type="number" min="0" class="input input-bordered input-sm" bind:value={editCapacity} />
        </label>
        <label class="form-control" for="volume-pool-edit-overcommit">
          <div class="label"><span class="label-text font-semibold">Overcommit (≥ 1.0)</span></div>
          <input id="volume-pool-edit-overcommit" type="number" min="1" step="0.1" class="input input-bordered input-sm" bind:value={editOvercommit} />
        </label>
        <div class="card-actions justify-end">
          <button type="submit" class="btn btn-primary btn-sm" data-testid="volume-pool-save" disabled={saving}>
            Save Changes
          </button>
        </div>
      </div>
    </form>

    <ConfirmModal
      open={confirmDelete}
      title="Delete volume pool"
      message={`Delete volume pool "${detail.name}" from the catalog? Nothing on disk changes. Pools with assigned tenants cannot be deleted.`}
      confirmLabel="Delete"
      variant="error"
      onconfirm={doDelete}
      oncancel={() => (confirmDelete = false)}
    />
  </div>
{:else}
  <PageHeader title="Volume Pools" description="Manage the cluster's volume pool catalog" />
  <div class="flex flex-col gap-6">
    {#if showCreate}
      <form class="card bg-base-200" data-testid="volume-pool-create-form" onsubmit={submitCreate}>
        <div class="card-body gap-3">
          <h3 class="card-title text-base">Create volume pool</h3>
          <p class="text-xs opacity-70">
            The pool is not served until each node maps it in its own configuration and restarts or reloads.
          </p>
          <label class="form-control" for="volume-pool-name">
            <div class="label"><span class="label-text font-semibold">Name</span></div>
            <input id="volume-pool-name" class="input input-bordered input-sm" bind:value={createName} required />
          </label>
          <label class="form-control" for="volume-pool-provider">
            <div class="label"><span class="label-text font-semibold">Provider</span></div>
            <input id="volume-pool-provider" class="input input-bordered input-sm" bind:value={createProvider} />
          </label>
          <label class="form-control" for="volume-pool-description">
            <div class="label"><span class="label-text font-semibold">Description</span></div>
            <input id="volume-pool-description" class="input input-bordered input-sm" bind:value={createDescription} />
          </label>
          <label class="form-control" for="volume-pool-capacity">
            <div class="label"><span class="label-text font-semibold">Capacity (bytes, empty = derived)</span></div>
            <input id="volume-pool-capacity" type="number" min="0" class="input input-bordered input-sm" bind:value={createCapacity} />
          </label>
          <label class="form-control" for="volume-pool-overcommit">
            <div class="label"><span class="label-text font-semibold">Overcommit (≥ 1.0, empty = 1.0)</span></div>
            <input id="volume-pool-overcommit" type="number" min="1" step="0.1" class="input input-bordered input-sm" bind:value={createOvercommit} />
          </label>
          <div class="card-actions justify-end">
            <button type="button" class="btn btn-ghost btn-sm" onclick={() => (showCreate = false)}>Cancel</button>
            <button type="submit" class="btn btn-primary btn-sm" data-testid="volume-pool-create-submit" disabled={creating}>
              Create Pool
            </button>
          </div>
        </div>
      </form>
    {/if}

    <CrudTable
      title="Volume Pools"
      data={pools}
      {columns}
      loading={loading || detailLoading}
      pageSize={10}
      showCreate={true}
      showDelete={false}
      createLabel="Create Pool"
      oncreateclick={openCreate}
      onrowclick={(event: RowClickEvent<VolumePool>) => void openDetail(event.row.name)}
      rowKey="name"
      emptyMessage="No volume pools in the catalog."
    />
  </div>
{/if}
