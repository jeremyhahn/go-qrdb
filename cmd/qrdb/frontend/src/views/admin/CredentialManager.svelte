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
  import type { Client, CredentialVersionInfo } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { RefreshCw, Eye, EyeOff, RotateCw, Trash2, Sparkles } from "lucide-svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import FormModal from "../../components/FormModal.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import PageHeader from "../../components/PageHeader.svelte";
  import StatusBadge from "../../components/StatusBadge.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Row shape
  // ---------------------------------------------------------------------------

  interface CredentialRow {
    readonly name: string;
  }

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let names: string[] = $state([]);
  let rows: CredentialRow[] = $derived(names.map((name) => ({ name })));
  let loading: boolean = $state(true);
  let strategy: string = $state("");

  let selectedName: string = $state("");
  let versions: CredentialVersionInfo[] = $state([]);
  let versionsLoading: boolean = $state(false);
  let revealedValue: string | null = $state(null);
  let revealLoading: boolean = $state(false);

  // Create modal (store or submit)
  let showCreateModal: boolean = $state(false);
  let createLoading: boolean = $state(false);
  let createMethod = $state<"store" | "submit">("store");
  let createName: string = $state("");
  let createValue: string = $state("");

  // Rotate modal
  let showRotateModal: boolean = $state(false);
  let rotateLoading: boolean = $state(false);
  let rotateNewValue: string = $state("");
  let rotateGracePeriod: string = $state("0");

  // Delete confirmation
  let showDeleteConfirm: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Columns
  // ---------------------------------------------------------------------------

  const columns: Column<CredentialRow>[] = [
    { id: "name", header: "Name", accessor: "name", sortable: true, filterable: true, filterType: "text" },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function loadCredentials(): Promise<void> {
    loading = true;
    try {
      names = await client.credentials.list();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function loadStrategy(): Promise<void> {
    try {
      strategy = await client.credentials.strategy();
    } catch {
      strategy = "";
    }
  }

  async function loadVersions(name: string): Promise<void> {
    versionsLoading = true;
    try {
      versions = await client.credentials.listVersions(name);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      versions = [];
    } finally {
      versionsLoading = false;
    }
  }

  function handleRowClick(event: RowClickEvent<CredentialRow>): void {
    selectedName = event.row.name;
    revealedValue = null;
    void loadVersions(selectedName);
  }

  async function handleReveal(): Promise<void> {
    if (selectedName === "") return;
    revealLoading = true;
    try {
      revealedValue = await client.credentials.retrieve(selectedName);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      revealLoading = false;
    }
  }

  function resetCreateForm(): void {
    createMethod = "store";
    createName = "";
    createValue = "";
  }

  async function handleCreateSubmit(): Promise<void> {
    const name = createName.trim();
    if (name === "") {
      showToast("warning", "Name is required.");
      return;
    }
    createLoading = true;
    try {
      if (createMethod === "store") {
        await client.credentials.store(name, createValue);
      } else {
        await client.credentials.submit(name, createValue);
      }
      showToast("success", `Credential "${name}" ${createMethod === "store" ? "stored" : "submitted"}.`);
      showCreateModal = false;
      resetCreateForm();
      await loadCredentials();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      createLoading = false;
    }
  }

  async function handleRotate(): Promise<void> {
    if (selectedName === "") return;
    const grace = parseInt(rotateGracePeriod, 10);
    rotateLoading = true;
    try {
      const result = await client.credentials.rotate(
        selectedName,
        rotateNewValue,
        Number.isNaN(grace) ? 0 : grace,
      );
      showToast(
        "success",
        `Credential "${selectedName}" rotated: v${result.previousVersion} -> v${result.newVersion}.`,
      );
      showRotateModal = false;
      rotateNewValue = "";
      rotateGracePeriod = "0";
      revealedValue = null;
      await loadVersions(selectedName);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      rotateLoading = false;
    }
  }

  async function handleGCExpired(): Promise<void> {
    if (selectedName === "") return;
    try {
      const reaped = await client.credentials.gcExpired(selectedName);
      showToast("success", `Reaped ${reaped} expired version(s) for "${selectedName}".`);
      await loadVersions(selectedName);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  async function handleDelete(): Promise<void> {
    if (selectedName === "") return;
    try {
      await client.credentials.delete(selectedName);
      showToast("success", `Credential "${selectedName}" deleted.`);
      showDeleteConfirm = false;
      selectedName = "";
      versions = [];
      revealedValue = null;
      await loadCredentials();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  function formatUnix(seconds: number | undefined): string {
    if (seconds === undefined || seconds === 0) return "";
    return new Date(seconds * 1000).toISOString();
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void loadCredentials();
    void loadStrategy();
  });
</script>

<div class="flex flex-col gap-6">
  <PageHeader title="Credentials" description="Named-credential submission, storage, and versioned rotation">
    {#snippet actions()}
      {#if strategy !== ""}
        <StatusBadge label={`Strategy: ${strategy}`} variant="info" />
      {/if}
      <button class="btn btn-outline btn-sm" onclick={() => void loadCredentials()} disabled={loading}>
        {#if loading}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <RefreshCw class="h-4 w-4" />
        {/if}
        Refresh
      </button>
      <button class="btn btn-primary btn-sm" onclick={() => { resetCreateForm(); showCreateModal = true; }}>
        New Credential
      </button>
    {/snippet}
  </PageHeader>

  <div class="flex gap-6">
    <div class="flex-1">
      <CrudTable
        title="Credentials"
        data={rows}
        {columns}
        {loading}
        showCreate={false}
        showDelete={false}
        onrowclick={handleRowClick}
        emptyMessage="No credentials found."
        pageSize={15}
        rowKey="name"
      />
    </div>

    <div class="card bg-base-200 shadow-sm w-96">
      <div class="card-body p-5">
        <h3 class="card-title text-base font-semibold">Credential Detail</h3>
        {#if selectedName !== ""}
          <div class="flex flex-col gap-3">
            <div class="bg-base-300 rounded p-3 flex items-center justify-between gap-2">
              <span class="font-mono text-sm break-all">{selectedName}</span>
            </div>

            <div>
              <div class="flex items-center justify-between mb-1">
                <span class="text-sm font-medium text-base-content/70">Value</span>
                <button
                  class="btn btn-ghost btn-xs"
                  onclick={() => void handleReveal()}
                  disabled={revealLoading}
                >
                  {#if revealLoading}
                    <span class="loading loading-spinner loading-xs"></span>
                  {:else if revealedValue !== null}
                    <EyeOff class="h-3.5 w-3.5" />
                  {:else}
                    <Eye class="h-3.5 w-3.5" />
                  {/if}
                  {revealedValue !== null ? "Hide" : "Reveal"}
                </button>
              </div>
              {#if revealedValue !== null}
                <div class="font-mono text-xs bg-base-300 rounded px-3 py-2 break-all">{revealedValue}</div>
              {:else}
                <div class="font-mono text-xs bg-base-300 rounded px-3 py-2 text-base-content/40">•••••••••••</div>
              {/if}
            </div>

            <div>
              <span class="text-sm font-medium text-base-content/70">Version History</span>
              {#if versionsLoading}
                <div class="flex justify-center py-3">
                  <span class="loading loading-spinner loading-sm"></span>
                </div>
              {:else if versions.length === 0}
                <p class="text-xs text-base-content/50 mt-1">No version history available.</p>
              {:else}
                <table class="table table-xs mt-1">
                  <thead>
                    <tr><th>Version</th><th>Current</th><th>Created</th><th>Grace Until</th></tr>
                  </thead>
                  <tbody>
                    {#each versions as v (v.version)}
                      <tr>
                        <td>{v.version}</td>
                        <td>{v.isCurrent ? "yes" : "no"}</td>
                        <td class="text-xs">{formatUnix(v.createdAtUnix)}</td>
                        <td class="text-xs">{formatUnix(v.graceUntilUnix)}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </div>

            <div class="flex flex-wrap gap-1">
              <button class="btn btn-outline btn-xs flex-1" onclick={() => (showRotateModal = true)}>
                <RotateCw class="h-3.5 w-3.5" />
                Rotate
              </button>
              <button class="btn btn-outline btn-xs flex-1" onclick={() => void handleGCExpired()}>
                <Sparkles class="h-3.5 w-3.5" />
                GC Expired
              </button>
              <button class="btn btn-error btn-xs flex-1" onclick={() => (showDeleteConfirm = true)}>
                <Trash2 class="h-3.5 w-3.5" />
                Delete
              </button>
            </div>
          </div>
        {:else}
          <p class="text-base-content/50 text-sm">Select a credential to view details.</p>
        {/if}
      </div>
    </div>
  </div>
</div>

<!-- Create credential modal -->
<FormModal
  open={showCreateModal}
  title="New Credential"
  submitLabel={createMethod === "store" ? "Store" : "Submit"}
  loading={createLoading}
  onsubmit={() => void handleCreateSubmit()}
  oncancel={() => (showCreateModal = false)}
>
  {#snippet body()}
    <div class="flex flex-col gap-4">
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Method</span></div>
        <select class="select select-bordered w-full" bind:value={createMethod}>
          <option value="store">Store — persist under the given name</option>
          <option value="submit">Submit — notify blocked retrievers (manual strategy)</option>
        </select>
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Name</span></div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="e.g. db-password"
          bind:value={createName}
        />
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Value</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono text-xs"
          placeholder="credential value (base64 or plain text)"
          bind:value={createValue}
        />
      </label>
    </div>
  {/snippet}
</FormModal>

<!-- Rotate modal -->
<FormModal
  open={showRotateModal}
  title="Rotate Credential"
  submitLabel="Rotate"
  loading={rotateLoading}
  onsubmit={() => void handleRotate()}
  oncancel={() => (showRotateModal = false)}
>
  {#snippet body()}
    <div class="flex flex-col gap-4">
      <p class="text-sm">
        Rotating <span class="font-mono">{selectedName}</span> atomically replaces its current value.
      </p>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">New Value</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono text-xs"
          placeholder="new credential value"
          bind:value={rotateNewValue}
        />
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Grace Period (seconds)</span></div>
        <input
          type="number"
          class="input input-bordered w-full"
          placeholder="0 = wipe previous value immediately"
          min="0"
          bind:value={rotateGracePeriod}
        />
      </label>
    </div>
  {/snippet}
</FormModal>

<!-- Delete confirmation -->
<ConfirmModal
  open={showDeleteConfirm}
  title="Delete Credential"
  message={selectedName !== "" ? `Delete credential "${selectedName}" and all of its historical versions? This action cannot be undone.` : ""}
  confirmLabel="Delete"
  variant="error"
  onconfirm={() => void handleDelete()}
  oncancel={() => (showDeleteConfirm = false)}
/>
