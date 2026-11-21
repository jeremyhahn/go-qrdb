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
  import type { Client, BackupInfo, VerifyResult } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { RefreshCw, ShieldCheck } from "lucide-svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import FormModal from "../../components/FormModal.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import PageHeader from "../../components/PageHeader.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let backups: BackupInfo[] = $state([]);
  let loading: boolean = $state(true);
  let selectedBackup: BackupInfo | null = $state(null);
  let lastVerifyResult: VerifyResult | null = $state(null);
  let detailLoading: boolean = $state(false);
  let detailError: string | null = $state(null);

  // Create-backup form
  let showCreateModal: boolean = $state(false);
  let createLoading: boolean = $state(false);
  let newDestination: string = $state("");
  let newCompression = $state<"none" | "gzip" | "zstd">("zstd");
  // "aes256" is the one canonical identifier for AES-256-GCM across the
  // entire stack (Go service layer, wire protocol, CLI, this SDK) -- it is
  // the default to match the server's own default, matching the server
  // refusing a request that names neither a key nor an explicit "none".
  let newEncryption = $state<"aes256" | "none">("aes256");
  // keyRef names the key-wrap provider's reference -- for the built-in
  // "software" provider, the NAME of a key file inside the server's own
  // configured key directory (never a path the client supplies, and
  // never raw key bytes: there is no inline-key field on this surface any
  // more).
  let newKeyRef: string = $state("");

  // Restore form
  let showRestoreModal: boolean = $state(false);
  let restoreLoading: boolean = $state(false);
  let restoreValidateFirst: boolean = $state(true);
  let restoreDecryptionKeyRef: string = $state("");

  // Delete confirmation
  let showDeleteConfirm: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Columns
  // ---------------------------------------------------------------------------

  const columns: Column<BackupInfo>[] = [
    { id: "id", header: "ID", accessor: "id", sortable: true },
    { id: "timestamp", header: "Created", accessor: "timestamp", sortable: true },
    { id: "shardCount", header: "Shards", accessor: "shardCount", sortable: true },
    { id: "size", header: "Size", accessor: (row: BackupInfo) => formatBytes(row.size), sortable: true },
    { id: "status", header: "Status", accessor: "status", sortable: true, filterable: true, filterType: "text" },
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

  function resetCreateForm(): void {
    newDestination = "";
    newCompression = "zstd";
    newEncryption = "aes256";
    newKeyRef = "";
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function loadBackups(): Promise<void> {
    loading = true;
    try {
      const resp = await client.backups.list();
      backups = [...resp.backups];
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleCreate(): Promise<void> {
    const destination = newDestination.trim();
    if (destination === "") {
      showToast("warning", "Destination is required.");
      return;
    }
    createLoading = true;
    try {
      const info = await client.backups.create({
        destination,
        ...(newCompression !== "none" ? { compression: newCompression } : {}),
        // Always sent explicitly, even "none": the server defaults to
        // aes256 when this field is omitted, so omitting it here on a
        // deliberate plaintext choice would ask for encryption, not
        // plaintext. The server independently logs and audits an explicit
        // "none".
        encryption: newEncryption,
        ...(newKeyRef.trim() !== "" ? { keyRef: newKeyRef.trim() } : {}),
      });
      showToast("success", `Backup "${info.id}" created (${info.status}).`);
      if (newEncryption === "none") {
        showToast(
          "warning",
          `Backup "${info.id}" was created in plaintext. If it covers the system shard, it carries the cluster CA and every tenant's login CA key material in the clear.`,
        );
      }
      showCreateModal = false;
      resetCreateForm();
      await loadBackups();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      createLoading = false;
    }
  }

  async function handleVerify(backup: BackupInfo): Promise<void> {
    try {
      const result = await client.backups.verify(backup.id);
      lastVerifyResult = result;
      showToast(
        result.valid ? "success" : "error",
        result.valid
          ? `Backup "${backup.id}" verified OK.`
          : `Backup "${backup.id}" failed verification.`,
      );
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  async function handleRestore(): Promise<void> {
    if (selectedBackup === null) return;
    restoreLoading = true;
    try {
      const result = await client.backups.restore({
        backupId: selectedBackup.id,
        validateFirst: restoreValidateFirst,
        ...(restoreDecryptionKeyRef.trim() !== "" ? { decryptionKeyRef: restoreDecryptionKeyRef.trim() } : {}),
      });
      const errorCount = (result.errors ?? []).length;
      showToast(
        errorCount === 0 ? "success" : "warning",
        `Restore completed: ${result.shardsRestored} shard(s) restored in ${result.duration}${errorCount > 0 ? `, ${errorCount} error(s)` : ""}.`,
      );
      showRestoreModal = false;
      restoreDecryptionKeyRef = "";
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      restoreLoading = false;
    }
  }

  async function handleDelete(): Promise<void> {
    if (selectedBackup === null) return;
    try {
      await client.backups.delete(selectedBackup.id);
      showToast("success", `Backup "${selectedBackup.id}" deleted.`);
      showDeleteConfirm = false;
      selectedBackup = null;
      lastVerifyResult = null;
      await loadBackups();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  // loadDetail fetches the authoritative BackupInfo for one backup through
  // backups.get, so the detail panel shows what the server holds now (not
  // the possibly stale list row). detailToken drops a response that a
  // later row click has superseded.
  let detailToken = 0;
  async function loadDetail(id: string): Promise<void> {
    const token = ++detailToken;
    detailLoading = true;
    detailError = null;
    try {
      const info = await client.backups.get(id);
      if (token !== detailToken) return;
      selectedBackup = info;
    } catch (err: unknown) {
      if (token !== detailToken) return;
      detailError = err instanceof Error ? err.message : String(err);
    } finally {
      if (token === detailToken) detailLoading = false;
    }
  }

  function handleRowClick(event: RowClickEvent<BackupInfo>): void {
    selectedBackup = event.row;
    lastVerifyResult = null;
    void loadDetail(event.row.id);
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void loadBackups();
  });
</script>

<div class="flex flex-col gap-6">
  <PageHeader title="Backups" description="Create, restore, and verify cluster backups">
    {#snippet actions()}
      <button class="btn btn-outline btn-sm" onclick={() => void loadBackups()} disabled={loading}>
        {#if loading}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <RefreshCw class="h-4 w-4" />
        {/if}
        Refresh
      </button>
      <button class="btn btn-primary btn-sm" onclick={() => { resetCreateForm(); showCreateModal = true; }}>
        Create Backup
      </button>
    {/snippet}
  </PageHeader>

  <div class="flex gap-6">
    <div class="flex-1">
      <CrudTable
        title="Backups"
        data={backups}
        {columns}
        {loading}
        showCreate={false}
        showDelete={false}
        onrowclick={handleRowClick}
        emptyMessage="No backups found."
        pageSize={10}
        rowKey="id"
      />
    </div>

    <div class="card bg-base-200 shadow-sm w-96">
      <div class="card-body p-5">
        <h3 class="card-title text-base font-semibold">Backup Detail</h3>
        {#if selectedBackup !== null}
          <div class="flex flex-col gap-3">
            {#if detailLoading}
              <div class="flex items-center gap-2 text-xs" data-testid="backup-detail-loading">
                <span class="loading loading-spinner loading-xs"></span>
                Loading backup detail...
              </div>
            {/if}
            {#if detailError !== null}
              <div class="alert alert-error py-2 text-xs" data-testid="backup-detail-error">
                <span>Could not load backup detail: {detailError}</span>
              </div>
            {/if}
            <div class="bg-base-300 rounded p-3" data-testid="backup-detail">
              <table class="table table-sm">
                <tbody>
                  <tr><th class="w-24">ID</th><td class="font-mono text-xs break-all">{selectedBackup.id}</td></tr>
                  <tr><th>Created</th><td class="text-xs">{selectedBackup.timestamp}</td></tr>
                  <tr><th>Shards</th><td>{selectedBackup.shardCount}</td></tr>
                  <tr><th>Size</th><td>{formatBytes(selectedBackup.size)}</td></tr>
                  <tr><th>Status</th><td>{selectedBackup.status}</td></tr>
                  <tr><th>Checksum</th><td class="font-mono text-xs break-all">{selectedBackup.checksum}</td></tr>
                  <tr><th>Destination</th><td class="font-mono text-xs break-all">{selectedBackup.destination}</td></tr>
                  <tr><th>Compression</th><td>{selectedBackup.compression}</td></tr>
                  <tr><th>Encryption</th><td>{selectedBackup.encryption}</td></tr>
                  <tr><th>Key Provider</th><td class="font-mono text-xs break-all">{selectedBackup.keyProviderId ?? ""}</td></tr>
                  <tr><th>Duration</th><td>{selectedBackup.duration}</td></tr>
                  <tr><th>Error</th><td class="text-xs break-all">{selectedBackup.errorMessage}</td></tr>
                  <tr>
                    <th>Metadata</th>
                    <td class="font-mono text-xs break-all">
                      {#each Object.entries(selectedBackup.metadata ?? {}) as [k, v] (k)}
                        <div>{k}={v}</div>
                      {/each}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>

            {#if lastVerifyResult !== null}
              <div class="alert {lastVerifyResult.valid ? 'alert-success' : 'alert-error'} py-2 text-xs">
                <ShieldCheck class="h-4 w-4" />
                <span>
                  {lastVerifyResult.valid ? "Checksum verified." : "Checksum mismatch."}
                  {#if (lastVerifyResult.errors ?? []).length > 0}
                    {(lastVerifyResult.errors ?? []).join("; ")}
                  {/if}
                </span>
              </div>
            {/if}

            <div class="flex flex-wrap gap-1">
              <button class="btn btn-outline btn-xs flex-1" onclick={() => void handleVerify(selectedBackup!)}>
                Verify
              </button>
              <button class="btn btn-outline btn-xs flex-1" onclick={() => (showRestoreModal = true)}>
                Restore
              </button>
              <button class="btn btn-error btn-xs flex-1" onclick={() => (showDeleteConfirm = true)}>
                Delete
              </button>
            </div>
          </div>
        {:else}
          <p class="text-base-content/50 text-sm">Select a backup to view details.</p>
        {/if}
      </div>
    </div>
  </div>
</div>

<!-- Create backup modal -->
<FormModal
  open={showCreateModal}
  title="Create Backup"
  submitLabel="Create"
  loading={createLoading}
  onsubmit={() => void handleCreate()}
  oncancel={() => (showCreateModal = false)}
>
  {#snippet body()}
    <div class="flex flex-col gap-4">
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Destination</span></div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="/var/backups/qrdb/backup-001 or s3://bucket/key"
          bind:value={newDestination}
        />
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Compression</span></div>
        <select class="select select-bordered w-full" bind:value={newCompression}>
          <option value="none">None</option>
          <option value="gzip">gzip</option>
          <option value="zstd">zstd</option>
        </select>
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Encryption</span></div>
        <select class="select select-bordered w-full" bind:value={newEncryption}>
          <option value="aes256">AES-256-GCM (default)</option>
          <option value="none">None (plaintext)</option>
        </select>
      </label>
      {#if newEncryption !== "none"}
        <label class="form-control w-full">
          <div class="label"><span class="label-text">Key File Name</span></div>
          <input
            type="text"
            class="input input-bordered w-full font-mono text-xs"
            placeholder="backup.key"
            bind:value={newKeyRef}
          />
          <div class="label">
            <span class="label-text-alt">
              The name of a key file inside the server's own configured key directory, resolved by
              the key-wrap provider -- not a path, and not a secret value typed here.
            </span>
          </div>
        </label>
      {:else}
        <div class="alert alert-warning py-2 text-xs">
          <span>
            This backup will be written in plaintext. If it covers the system shard (the default
            when no shard filter is used), it carries the cluster CA and every tenant's login CA
            key material in the clear.
          </span>
        </div>
      {/if}
    </div>
  {/snippet}
</FormModal>

<!-- Restore modal -->
<FormModal
  open={showRestoreModal}
  title="Restore Backup"
  submitLabel="Restore"
  loading={restoreLoading}
  onsubmit={() => void handleRestore()}
  oncancel={() => (showRestoreModal = false)}
>
  {#snippet body()}
    <div class="flex flex-col gap-4">
      {#if selectedBackup !== null}
        <p class="text-sm">
          Restoring backup <span class="font-mono">{selectedBackup.id}</span> will overwrite existing
          shard data. This action cannot be undone.
        </p>
      {/if}
      <label class="label cursor-pointer justify-start gap-2">
        <input type="checkbox" class="checkbox checkbox-sm" bind:checked={restoreValidateFirst} />
        <span class="label-text">Validate backup integrity before restoring</span>
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Key File Name Override (optional)</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono text-xs"
          placeholder="Leave blank to use the backup's own stored key reference"
          bind:value={restoreDecryptionKeyRef}
        />
      </label>
    </div>
  {/snippet}
</FormModal>

<!-- Delete confirmation -->
<ConfirmModal
  open={showDeleteConfirm}
  title="Delete Backup"
  message={selectedBackup !== null ? `Delete backup "${selectedBackup.id}"? This action cannot be undone.` : ""}
  confirmLabel="Delete"
  variant="error"
  onconfirm={() => void handleDelete()}
  oncancel={() => (showDeleteConfirm = false)}
/>
