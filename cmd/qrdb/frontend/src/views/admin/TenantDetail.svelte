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
    TenantInfo,
    TenantEntitlements,
    TenantIsolationType,
    TenantQuotas,
    TrustAnchor,
    UpdateTenantEntitlementsRequest,
    UpdateTenantQuotasRequest,
    UpdateTenantRequest,
  } from "@qrdb/sdk";
  import { TransportError } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { ChevronLeft, Trash2 } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";

  let {
    client,
    tenantId,
    onBack,
  }: {
    client: Client;
    tenantId: string;
    onBack: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let tenant: TenantInfo | null = $state(null);
  let loading: boolean = $state(true);
  let saving: boolean = $state(false);
  let savingInfo: boolean = $state(false);
  let savingEntitlements: boolean = $state(false);

  /**
   * Isolation modes offered by the selector. The volume and raft tiers are
   * dedicated-storage tiers; multi-node Raft replication ships (replicas placed
   * across cluster nodes, RF configurable).
   */
  const isolationTypes: readonly {
    id: TenantIsolationType;
    label: string;
    description: string;
  }[] = [
    {
      id: "prefix",
      label: "prefix",
      description: "Shared-store namespace key prefix only. No extra crypto layer.",
    },
    {
      id: "barrier",
      label: "barrier",
      description: "Namespace prefix plus a per-tenant encryption barrier for IAM data.",
    },
    {
      id: "volume",
      label: "volume",
      description: "Dedicated filesystem data root per tenant (plus a barrier). Dedicated-storage tier; requires a daemon volume pool.",
    },
    {
      id: "raft",
      label: "raft",
      description: "Dedicated Raft group per database on a dedicated data root. Dedicated-storage tier; multi-node replication ships: replicas are placed across cluster nodes (RF configurable).",
    },
  ] as const;

  /** Tenant info edit fields. */
  let editName: string = $state("");
  let editIsolationType: TenantIsolationType = $state("prefix");

  // Quota form fields — initialized to 0 (system default) until tenant loads.
  let maxDatabases: number = $state(0);
  let maxRaftGroups: number = $state(0);
  let maxReplicasPerGroup: number = $state(0);
  let maxTotalReplicas: number = $state(0);
  let maxCollections: number = $state(0);
  let maxKeys: number = $state(0);
  let maxStorageBytes: number = $state(0);
  let maxDatabaseSizeBytes: number = $state(0);
  let maxUsers: number = $state(0);
  let maxRoles: number = $state(0);
  let maxCertificates: number = $state(0);
  let maxBackups: number = $state(0);
  let maxVectorDims: number = $state(0);
  let maxTSMetrics: number = $state(0);
  let maxDedicatedWalGroups: number = $state(0);
  // Bytes an isolated tenant may consume via the shared system barrier
  // (0 = the "use system barrier" option is unavailable, -1 = unlimited).
  let systemBarrierQuota: number = $state(0);

  // Entitlement form fields.
  let allowDedicatedRaft: boolean = $state(false);
  let allowVolumeStorage: boolean = $state(false);
  let allowSystemManagedBarrier: boolean = $state(false);
  let allowDedicatedWal: boolean = $state(false);
  /** Selected allowed isolation modes (multiselect). */
  let allowedIsolationModes: TenantIsolationType[] = $state([]);
  /** Comma-separated dedicated-barrier sealing strategies the tenant may select
   * (e.g. "none, passphrase, shamir" or an xkms hardware id). Empty = software
   * defaults. */
  let allowedBarrierModes: string = $state("");

  // Trust anchors: CAs that may vouch for this tenant's users'
  // bring-your-own login certificates, and no other tenant's.
  let trustAnchors: TrustAnchor[] = $state([]);
  let trustAnchorsLoading: boolean = $state(true);
  // Why the list could not be loaded; an unloaded list is not an empty one.
  let trustAnchorsError: string = $state("");
  let newTrustAnchorsPem: string = $state("");
  let addTrustAnchorLoading: boolean = $state(false);
  let removingFingerprint: string = $state("");

  // ---------------------------------------------------------------------------
  // Data fetching
  // ---------------------------------------------------------------------------

  /** Loads the tenant and populates form fields. */
  async function loadTenant(): Promise<void> {
    try {
      loading = true;
      const response = await client.tenants.get(tenantId);
      tenant = response.tenant;
      editName = response.tenant.name;
      editIsolationType = response.tenant.isolationType ?? "prefix";
      populateQuotas(response.tenant.quotas);
      populateEntitlements(response.tenant.entitlements);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to load tenant";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  /** Submits the tenant info form (name + isolation type). */
  async function handleSaveInfo(): Promise<void> {
    try {
      savingInfo = true;
      const trimmed = editName.trim();
      const req: UpdateTenantRequest = trimmed
        ? { name: trimmed, isolationType: editIsolationType }
        : { isolationType: editIsolationType };
      const response = await client.tenants.update(tenantId, req);
      tenant = response.tenant;
      editName = response.tenant.name;
      editIsolationType = response.tenant.isolationType ?? "prefix";
      showToast("success", `Tenant "${tenantId}" updated.`);
    } catch (err: unknown) {
      if (err instanceof TransportError && err.statusCode === 409) {
        showToast("error", "Cannot change isolation type: tenant already has data.");
      } else {
        const msg = err instanceof Error ? err.message : "Failed to update tenant";
        showToast("error", msg);
      }
    } finally {
      savingInfo = false;
    }
  }

  /** Copies existing quota values into the editable form fields. */
  function populateQuotas(quotas: TenantQuotas | undefined): void {
    maxDatabases = quotas?.maxDatabases ?? 0;
    maxRaftGroups = quotas?.maxRaftGroups ?? 0;
    maxReplicasPerGroup = quotas?.maxReplicasPerGroup ?? 0;
    maxTotalReplicas = quotas?.maxTotalReplicas ?? 0;
    maxCollections = quotas?.maxCollections ?? 0;
    maxKeys = quotas?.maxKeys ?? 0;
    maxStorageBytes = quotas?.maxStorageBytes ?? 0;
    maxDatabaseSizeBytes = quotas?.maxDatabaseSizeBytes ?? 0;
    maxUsers = quotas?.maxUsers ?? 0;
    maxRoles = quotas?.maxRoles ?? 0;
    maxCertificates = quotas?.maxCertificates ?? 0;
    maxBackups = quotas?.maxBackups ?? 0;
    maxVectorDims = quotas?.maxVectorDims ?? 0;
    maxTSMetrics = quotas?.maxTSMetrics ?? 0;
    maxDedicatedWalGroups = quotas?.maxDedicatedWalGroups ?? 0;
    systemBarrierQuota = quotas?.systemBarrierQuota ?? 0;
  }

  /** Copies existing entitlement values into the editable form fields. */
  function populateEntitlements(entitlements: TenantEntitlements | undefined): void {
    allowDedicatedRaft = entitlements?.allowDedicatedRaft ?? false;
    allowVolumeStorage = entitlements?.allowVolumeStorage ?? false;
    allowSystemManagedBarrier = entitlements?.allowSystemManagedBarrier ?? false;
    allowDedicatedWal = entitlements?.allowDedicatedWal ?? false;
    allowedIsolationModes = [
      ...((entitlements?.allowedIsolationModes ?? []) as TenantIsolationType[]),
    ];
    allowedBarrierModes = (entitlements?.allowedBarrierModes ?? []).join(", ");
  }

  /** Toggles an isolation mode in the allowed-modes multiselect. */
  function toggleAllowedMode(mode: TenantIsolationType): void {
    allowedIsolationModes = allowedIsolationModes.includes(mode)
      ? allowedIsolationModes.filter((m) => m !== mode)
      : [...allowedIsolationModes, mode];
  }

  /** Loads this tenant's registered trust anchors. */
  async function loadTrustAnchors(): Promise<void> {
    try {
      trustAnchorsLoading = true;
      trustAnchorsError = "";
      const resp = await client.tenants.listTrustAnchors(tenantId);
      trustAnchors = [...resp.anchors];
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to load trust anchors";
      trustAnchorsError = msg;
      showToast("error", msg);
    } finally {
      trustAnchorsLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Submits the quota form and persists changes via the SDK. */
  async function handleSaveQuotas(): Promise<void> {
    try {
      saving = true;
      const req: UpdateTenantQuotasRequest = {
        tenantId,
        quotas: {
          maxDatabases,
          maxRaftGroups,
          maxReplicasPerGroup,
          maxTotalReplicas,
          maxCollections,
          maxKeys,
          maxStorageBytes,
          maxDatabaseSizeBytes,
          maxUsers,
          maxRoles,
          maxCertificates,
          maxBackups,
          maxVectorDims,
          maxTSMetrics,
          maxDedicatedWalGroups,
          systemBarrierQuota,
        },
      };
      const response = await client.tenants.updateQuotas(req);
      tenant = response.tenant;
      populateQuotas(response.tenant.quotas);
      showToast("success", `Quotas updated for tenant "${tenantId}".`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to update quotas";
      showToast("error", msg);
    } finally {
      saving = false;
    }
  }

  /** Submits the entitlements form and persists changes via the SDK. */
  async function handleSaveEntitlements(): Promise<void> {
    try {
      savingEntitlements = true;
      const req: UpdateTenantEntitlementsRequest = {
        tenantId,
        entitlements: {
          allowedIsolationModes: [...allowedIsolationModes],
          allowDedicatedRaft,
          allowVolumeStorage,
          allowSystemManagedBarrier,
          allowDedicatedWal,
          allowedBarrierModes: allowedBarrierModes
            .split(",")
            .map((s) => s.trim())
            .filter((s) => s.length > 0),
        },
      };
      const response = await client.tenants.updateEntitlements(req);
      tenant = response.tenant;
      populateEntitlements(response.tenant.entitlements);
      showToast("success", `Entitlements updated for tenant "${tenantId}".`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to update entitlements";
      showToast("error", msg);
    } finally {
      savingEntitlements = false;
    }
  }

  /** Registers the pasted CA certificate bundle as trust anchors. */
  async function handleAddTrustAnchors(): Promise<void> {
    if (newTrustAnchorsPem.trim() === "") {
      showToast("warning", "Paste at least one CA certificate PEM.");
      return;
    }
    try {
      addTrustAnchorLoading = true;
      const resp = await client.tenants.addTrustAnchors(tenantId, newTrustAnchorsPem.trim());
      trustAnchors = [...resp.anchors];
      trustAnchorsError = "";
      newTrustAnchorsPem = "";
      showToast("success", `Trust anchor(s) added for tenant "${tenantId}".`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to add trust anchors";
      showToast("error", msg);
    } finally {
      addTrustAnchorLoading = false;
    }
  }

  /** Withdraws a single trust anchor by its fingerprint. */
  async function handleRemoveTrustAnchor(fingerprint: string): Promise<void> {
    try {
      removingFingerprint = fingerprint;
      await client.tenants.removeTrustAnchor(tenantId, fingerprint);
      trustAnchors = trustAnchors.filter((a) => a.fingerprint !== fingerprint);
      showToast("success", "Trust anchor removed.");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to remove trust anchor";
      showToast("error", msg);
    } finally {
      removingFingerprint = "";
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void loadTenant();
    void loadTrustAnchors();
  });
</script>

<div class="flex flex-col gap-6">
  <!-- Header -->
  <div class="flex items-center gap-3">
    <button class="btn btn-ghost btn-sm" onclick={onBack}>
      <ChevronLeft class="h-4 w-4" />
      Back
    </button>
    <h2 class="text-lg font-semibold">Tenant Quotas — {tenantId}</h2>
  </div>

  {#if loading}
    <div class="flex justify-center py-12">
      <span class="loading loading-spinner loading-lg text-primary"></span>
    </div>
  {:else if tenant !== null}
    <!-- Tenant info summary -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base">Details</h3>
        <div class="grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
          <div>
            <span class="text-base-content/60 font-semibold">ID</span>
            <p class="font-mono">{tenant.id}</p>
          </div>
          <div>
            <span class="text-base-content/60 font-semibold">Name</span>
            <p>{tenant.name}</p>
          </div>
          <div>
            <span class="text-base-content/60 font-semibold">Isolation</span>
            <p>
              {#if (tenant.isolationType ?? "prefix") === "prefix"}
                <span class="badge badge-info badge-sm">prefix</span>
              {:else if (tenant.isolationType ?? "prefix") === "barrier"}
                <span class="badge badge-secondary badge-sm">barrier</span>
              {:else if tenant.isolationType === "volume"}
                <span class="badge badge-accent badge-sm">volume</span>
              {:else}
                <span class="badge badge-primary badge-sm">raft</span>
              {/if}
            </p>
          </div>
          <div>
            <span class="text-base-content/60 font-semibold">Created</span>
            <p class="font-mono">{tenant.createdAt}</p>
          </div>
          <div>
            <span class="text-base-content/60 font-semibold">Updated</span>
            <p class="font-mono">{tenant.updatedAt}</p>
          </div>
        </div>
      </div>
    </div>

    <!-- Tenant info edit -->
    <div class="card bg-base-200 shadow">
      <div class="card-body gap-4">
        <h3 class="card-title text-base">Edit Tenant</h3>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <label class="form-control w-full">
            <div class="label">
              <span class="label-text font-semibold">Name</span>
            </div>
            <input
              type="text"
              class="input input-bordered input-sm w-full"
              bind:value={editName}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text font-semibold">Isolation Type</span>
            </div>
            <select
              class="select select-bordered select-sm w-full"
              data-testid="tenant-isolation-select"
              bind:value={editIsolationType}
            >
              {#each isolationTypes as it (it.id)}
                <option value={it.id}>{it.label}</option>
              {/each}
            </select>
            <div class="label">
              <span class="label-text-alt text-base-content/60">
                {isolationTypes.find((it) => it.id === editIsolationType)?.description ?? ""}
              </span>
            </div>
            {#if editIsolationType === "volume" || editIsolationType === "raft"}
              <div class="label">
                <span class="label-text-alt text-info">
                  Dedicated-storage tier. Multi-node Raft replication ships: replicas are placed across cluster nodes (RF configurable).
                </span>
              </div>
            {/if}
            {#if editIsolationType !== (tenant.isolationType ?? "prefix")}
              <div class="label">
                <span class="label-text-alt text-warning">
                  Warning: changing isolation type on a tenant with existing data will return 409 Conflict.
                </span>
              </div>
            {/if}
          </label>
        </div>

        <div class="flex gap-2">
          <button
            class="btn btn-primary btn-sm"
            onclick={() => void handleSaveInfo()}
            disabled={savingInfo}
          >
            {#if savingInfo}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Save
          </button>
        </div>
      </div>
    </div>

    <!-- Quota form -->
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">

      <!-- Storage group -->
      <div class="card bg-base-200 shadow">
        <div class="card-body gap-4">
          <h3 class="card-title text-base">Storage</h3>
          <p class="text-base-content/60 text-xs">-1 = unlimited &nbsp;&middot;&nbsp; 0 = system default</p>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Databases</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxDatabases}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Raft Groups</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxRaftGroups}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Collections</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxCollections}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Keys</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxKeys}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Storage (bytes)</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxStorageBytes}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Database Size (bytes)</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxDatabaseSizeBytes}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Replicas / Group</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxReplicasPerGroup}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Total Replicas</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxTotalReplicas}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Dedicated WAL Groups</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              data-testid="quota-max-dedicated-wal-groups"
              bind:value={maxDedicatedWalGroups}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">System Barrier Quota (bytes)</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              data-testid="quota-system-barrier-quota"
              bind:value={systemBarrierQuota}
            />
            <div class="label">
              <span class="label-text-alt text-base-content/60">
                Bytes this isolated tenant may use via the shared system barrier.
                0 = option unavailable &middot; -1 = unlimited.
              </span>
            </div>
          </label>
        </div>
      </div>

      <!-- Access group -->
      <div class="card bg-base-200 shadow">
        <div class="card-body gap-4">
          <h3 class="card-title text-base">Access</h3>
          <p class="text-base-content/60 text-xs">-1 = unlimited &nbsp;&middot;&nbsp; 0 = system default</p>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Users</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxUsers}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Roles</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxRoles}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Certificates</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxCertificates}
            />
          </label>
        </div>
      </div>

      <!-- Data group -->
      <div class="card bg-base-200 shadow">
        <div class="card-body gap-4">
          <h3 class="card-title text-base">Data</h3>
          <p class="text-base-content/60 text-xs">-1 = unlimited &nbsp;&middot;&nbsp; 0 = system default</p>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Backups</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxBackups}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max Vector Dims</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxVectorDims}
            />
          </label>

          <label class="form-control w-full">
            <div class="label">
              <span class="label-text">Max TS Metrics</span>
            </div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              bind:value={maxTSMetrics}
            />
          </label>
        </div>
      </div>
    </div>

    <!-- Action buttons -->
    <div class="flex gap-2 pt-2">
      <button
        class="btn btn-primary btn-sm"
        onclick={() => void handleSaveQuotas()}
        disabled={saving}
      >
        {#if saving}
          <span class="loading loading-spinner loading-xs"></span>
        {/if}
        Save Quotas
      </button>
      <button class="btn btn-ghost btn-sm" onclick={onBack}>Cancel</button>
    </div>

    <!-- Entitlements -->
    <div class="card bg-base-200 shadow" data-testid="tenant-entitlements">
      <div class="card-body gap-4">
        <h3 class="card-title text-base">Entitlements</h3>
        <p class="text-base-content/60 text-xs">
          Feature entitlements gate which isolation tiers and dedicated-storage
          features this tenant's plan permits. Multi-node Raft replication ships —
          dedicated-raft databases are replicated across cluster nodes (RF configurable).
        </p>

        <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <label class="label cursor-pointer justify-start gap-3">
            <input
              type="checkbox"
              class="toggle toggle-primary toggle-sm"
              data-testid="entitlement-allow-dedicated-raft"
              bind:checked={allowDedicatedRaft}
            />
            <span class="label-text">Allow dedicated Raft</span>
          </label>

          <label class="label cursor-pointer justify-start gap-3">
            <input
              type="checkbox"
              class="toggle toggle-primary toggle-sm"
              data-testid="entitlement-allow-volume-storage"
              bind:checked={allowVolumeStorage}
            />
            <span class="label-text">Allow volume storage</span>
          </label>

          <label class="label cursor-pointer justify-start gap-3">
            <input
              type="checkbox"
              class="toggle toggle-primary toggle-sm"
              data-testid="entitlement-allow-system-managed-barrier"
              bind:checked={allowSystemManagedBarrier}
            />
            <span class="label-text">Allow system-managed barrier</span>
          </label>

          <label class="label cursor-pointer justify-start gap-3">
            <input
              type="checkbox"
              class="toggle toggle-primary toggle-sm"
              data-testid="entitlement-allow-dedicated-wal"
              bind:checked={allowDedicatedWal}
            />
            <span class="label-text">Allow dedicated WAL</span>
          </label>
        </div>

        <div class="form-control">
          <div class="label">
            <span class="label-text font-semibold">Allowed Isolation Modes</span>
          </div>
          <div class="flex flex-wrap gap-3">
            {#each isolationTypes as it (it.id)}
              <label class="label cursor-pointer justify-start gap-2">
                <input
                  type="checkbox"
                  class="checkbox checkbox-sm"
                  data-testid="allowed-mode-{it.id}"
                  checked={allowedIsolationModes.includes(it.id)}
                  onchange={() => toggleAllowedMode(it.id)}
                />
                <span class="label-text">{it.label}</span>
              </label>
            {/each}
          </div>
        </div>

        <label class="form-control w-full">
          <div class="label">
            <span class="label-text font-semibold">Allowed Barrier Modes</span>
          </div>
          <input
            type="text"
            class="input input-bordered input-sm w-full"
            data-testid="entitlement-allowed-barrier-modes"
            placeholder="none, passphrase, shamir"
            bind:value={allowedBarrierModes}
          />
          <div class="label">
            <span class="label-text-alt text-base-content/60">
              Comma-separated dedicated-barrier sealing strategies this tenant may
              select (e.g. none, passphrase, shamir, or an xkms hardware id like
              tpm2). Empty = built-in software defaults; hardware must be listed.
            </span>
          </div>
        </label>

        <div class="flex gap-2 pt-1">
          <button
            class="btn btn-primary btn-sm"
            data-testid="save-entitlements-btn"
            onclick={() => void handleSaveEntitlements()}
            disabled={savingEntitlements}
          >
            {#if savingEntitlements}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Save Entitlements
          </button>
        </div>
      </div>
    </div>

    <!-- Trust anchors -->
    <div class="card bg-base-200 shadow">
      <div class="card-body gap-4">
        <h3 class="card-title text-base">Trust Anchors</h3>
        <p class="text-base-content/70 text-sm">
          CAs that may vouch for this tenant's users' bring-your-own login
          certificates, and no other tenant's.
        </p>

        {#if trustAnchorsLoading}
          <div class="flex justify-center py-6">
            <span class="loading loading-spinner loading-md"></span>
          </div>
        {:else if trustAnchorsError !== ""}
          <p class="text-error text-sm" role="alert" data-testid="trust-anchors-error">
            Trust anchors could not be loaded: {trustAnchorsError}
          </p>
        {:else if trustAnchors.length === 0}
          <p class="text-base-content/50 text-sm">No trust anchors registered.</p>
        {:else}
          <div class="overflow-x-auto">
            <table class="table table-sm w-full" data-testid="trust-anchors-table">
              <thead>
                <tr>
                  <th>Fingerprint</th>
                  <th>Subject</th>
                  <th>Not Before</th>
                  <th>Not After</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {#each trustAnchors as anchor (anchor.fingerprint)}
                  <tr>
                    <td class="font-mono text-xs">{anchor.fingerprint}</td>
                    <td class="text-xs">{anchor.subject}</td>
                    <td class="whitespace-nowrap text-xs">{anchor.notBefore}</td>
                    <td class="whitespace-nowrap text-xs">{anchor.notAfter}</td>
                    <td>
                      <button
                        class="btn btn-ghost btn-xs text-error"
                        title="Withdraw this trust anchor"
                        disabled={removingFingerprint === anchor.fingerprint}
                        onclick={() => void handleRemoveTrustAnchor(anchor.fingerprint)}
                      >
                        {#if removingFingerprint === anchor.fingerprint}
                          <span class="loading loading-spinner loading-xs"></span>
                        {:else}
                          <Trash2 class="h-3.5 w-3.5" />
                        {/if}
                      </button>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}

        <label class="form-control w-full">
          <div class="label">
            <span class="label-text font-semibold">Add Trust Anchors (PEM)</span>
          </div>
          <textarea
            class="textarea textarea-bordered w-full font-mono text-xs"
            rows="4"
            placeholder="-----BEGIN CERTIFICATE-----"
            data-testid="add-trust-anchors-input"
            bind:value={newTrustAnchorsPem}
          ></textarea>
        </label>
        <div class="flex gap-2 pt-1">
          <button
            class="btn btn-primary btn-sm"
            data-testid="add-trust-anchors-btn"
            onclick={() => void handleAddTrustAnchors()}
            disabled={addTrustAnchorLoading || newTrustAnchorsPem.trim() === ""}
          >
            {#if addTrustAnchorLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Add Trust Anchors
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>
