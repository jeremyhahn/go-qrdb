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
  import type { Client, RestoreRevisionRequest, RestoreRevisionResult } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { showToast } from "../../stores/toast.svelte.ts";
  import { withRetry } from "../../lib/retry.ts";
  import { rangeBoundsForPrefix } from "../../lib/kvRange.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import SlidePanel from "../../components/core/SlidePanel.svelte";
  import FormModal from "../../components/FormModal.svelte";
  import {
    Trash2,
    RefreshCw,
    Pencil,
    Search,
    History,
    Clock,
    AlertTriangle,
    RotateCcw,
  } from "lucide-svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  interface KVEntry {
    key: string;
    value: string;
  }

  type RestoreScopeType = "prefix" | "keys";
  type RestorePhase = "form" | "preview" | "result";

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let entries: KVEntry[] = $state([]);
  let loading: boolean = $state(false);
  let prefixFilter: string = $state("");

  // Create/Edit SlidePanel
  let showCreatePanel: boolean = $state(false);
  let editMode: boolean = $state(false);
  let modalKey: string = $state("");
  let modalValue: string = $state("");
  let modalLoading: boolean = $state(false);

  // Single-entry delete confirmation
  let deleteTarget: string | null = $state(null);

  // Detail panel
  let selectedEntry: KVEntry | null = $state(null);

  // As-of (historical) browse. asOfRevision is the revision currently being
  // viewed; null means "live". Set independently of the input text so a
  // typo mid-edit doesn't change what's displayed until "View" is clicked.
  let asOfRevisionInput: string = $state("");
  let asOfRevision: number | null = $state(null);

  // Restore workflow: a single modal that walks form -> preview -> result,
  // per the "always preview before applying" requirement.
  let showRestoreModal: boolean = $state(false);
  let restorePhase: RestorePhase = $state("form");
  let restoreRevisionInput: string = $state("");
  let restoreScopeType: RestoreScopeType = $state("prefix");
  let restoreScopePrefix: string = $state("");
  let restoreScopeKeysText: string = $state("");
  let restoreForce: boolean = $state(false);
  let restoreBusy: boolean = $state(false);
  let restorePlan: RestoreRevisionResult | null = $state(null);
  let restoreOutcome: RestoreRevisionResult | null = $state(null);
  let restoreRequestBase: RestoreRevisionRequest | null = $state(null);

  // ---------------------------------------------------------------------------
  // Column Definitions
  // ---------------------------------------------------------------------------

  const browseColumns: Column<KVEntry>[] = [
    {
      id: "key",
      header: "Key",
      accessor: "key",
      sortable: true,
      filterable: true,
      filterType: "text",
      width: "45%",
    },
    {
      id: "value",
      header: "Value",
      accessor: "value",
      sortable: false,
      filterable: true,
      filterType: "text",
      width: "55%",
    },
  ];

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const isHistorical = $derived(asOfRevision !== null);

  const restoreModalTitle = $derived(
    restorePhase === "form"
      ? "Restore to Revision"
      : restorePhase === "preview"
        ? "Confirm Restore Plan"
        : "Restore Result",
  );

  const restoreModalSubmitLabel = $derived.by((): string => {
    if (restorePhase === "form") return "Preview";
    if (restorePhase === "preview") return "Confirm & Apply";
    const outcome: RestoreRevisionResult | null = restoreOutcome;
    return outcome !== null && outcome.conflicted > 0 ? "Retry with Force" : "Close";
  });

  // ---------------------------------------------------------------------------
  // Live / historical browse
  // ---------------------------------------------------------------------------

  async function loadEntries(): Promise<void> {
    loading = true;
    try {
      if (asOfRevision !== null) {
        const { start, end } = rangeBoundsForPrefix(prefixFilter);
        const resp = await withRetry(() =>
          client.mvcc.range(start, end, { revision: asOfRevision as number }),
        );
        entries = resp.kvs.map((kv) => ({ key: kv.key, value: kv.value }));
      } else {
        // Use scan() so we get keys+values in one round trip instead of N+1.
        const map = await withRetry(() => client.kv.scan(prefixFilter));
        const next: KVEntry[] = [];
        for (const key of Object.keys(map).sort()) {
          next.push({ key, value: map[key] ?? "" });
        }
        entries = next;
      }
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      entries = [];
    } finally {
      loading = false;
    }
  }

  async function viewAsOf(): Promise<void> {
    const revision = Number(asOfRevisionInput);
    if (!Number.isFinite(revision) || revision <= 0) {
      showToast("warning", "Enter a positive revision number.");
      return;
    }
    asOfRevision = revision;
    if (selectedEntry !== null) selectedEntry = null;
    await loadEntries();
  }

  async function clearAsOf(): Promise<void> {
    asOfRevision = null;
    asOfRevisionInput = "";
    if (selectedEntry !== null) selectedEntry = null;
    await loadEntries();
  }

  function openCreatePanel(): void {
    editMode = false;
    modalKey = "";
    modalValue = "";
    showCreatePanel = true;
  }

  function openEditPanel(entry: KVEntry): void {
    editMode = true;
    modalKey = entry.key;
    modalValue = entry.value;
    showCreatePanel = true;
  }

  async function handleSaveEntry(): Promise<void> {
    if (modalKey.trim() === "") {
      showToast("warning", "Key is required.");
      return;
    }
    if (modalValue.trim() === "") {
      showToast("warning", "Value is required.");
      return;
    }

    modalLoading = true;
    try {
      await client.kv.put(modalKey, modalValue);
      showToast("success", editMode ? `Updated "${modalKey}"` : `Created "${modalKey}"`);
      showCreatePanel = false;
      await loadEntries();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      modalLoading = false;
    }
  }

  function requestDeleteEntry(key: string): void {
    deleteTarget = key;
  }

  async function confirmDeleteEntry(): Promise<void> {
    if (deleteTarget === null) return;
    const key = deleteTarget;
    deleteTarget = null;
    loading = true;
    try {
      await client.kv.delete(key);
      showToast("success", `Deleted "${key}"`);
      if (selectedEntry?.key === key) {
        selectedEntry = null;
      }
      await loadEntries();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleBulkDelete(indices: Set<number>): Promise<void> {
    loading = true;
    const keysToDelete: string[] = [];
    for (const idx of indices) {
      if (entries[idx]) keysToDelete.push(entries[idx].key);
    }
    let successCount = 0;
    let failCount = 0;
    for (const key of keysToDelete) {
      try {
        await client.kv.delete(key);
        successCount++;
      } catch {
        failCount++;
      }
    }
    if (failCount === 0) {
      showToast("success", `Deleted ${successCount} entries`);
    } else {
      showToast("warning", `Deleted ${successCount}, failed ${failCount}`);
    }
    selectedEntry = null;
    await loadEntries();
    loading = false;
  }

  function handleRowClick(event: RowClickEvent<KVEntry>): void {
    selectedEntry = event.row;
  }

  // ---------------------------------------------------------------------------
  // Restore workflow
  // ---------------------------------------------------------------------------

  /** Splits a keys textarea on newlines and commas, trimming and dropping blanks. */
  function parseKeysText(text: string): string[] {
    return text
      .split(/[\n,]/)
      .map((k) => k.trim())
      .filter((k) => k.length > 0);
  }

  /** Opens the restore workflow, prefilled from the current browse context. */
  function openRestoreModal(prefillKeys?: string[]): void {
    restorePhase = "form";
    restoreRevisionInput = asOfRevision !== null ? String(asOfRevision) : "";
    if (prefillKeys && prefillKeys.length > 0) {
      restoreScopeType = "keys";
      restoreScopeKeysText = prefillKeys.join("\n");
      restoreScopePrefix = "";
    } else {
      restoreScopeType = "prefix";
      restoreScopePrefix = prefixFilter;
      restoreScopeKeysText = "";
    }
    restoreForce = false;
    restoreBusy = false;
    restorePlan = null;
    restoreOutcome = null;
    restoreRequestBase = null;
    showRestoreModal = true;
  }

  function closeRestoreModal(): void {
    showRestoreModal = false;
  }

  /** Restore this one historical key: opens the workflow scoped to it. */
  function restoreSelectedEntry(): void {
    if (selectedEntry === null) return;
    openRestoreModal([selectedEntry.key]);
  }

  function buildRestoreScope(): { prefix?: string; keys?: string[] } | null {
    if (restoreScopeType === "prefix") {
      const p = restoreScopePrefix.trim();
      if (p === "") return null;
      return { prefix: p };
    }
    const keys = parseKeysText(restoreScopeKeysText);
    if (keys.length === 0) return null;
    return { keys };
  }

  async function handleRestorePreview(): Promise<void> {
    const revision = Number(restoreRevisionInput);
    if (!Number.isFinite(revision) || revision <= 0) {
      showToast("warning", "Enter a positive target revision.");
      return;
    }
    const scope = buildRestoreScope();
    if (scope === null) {
      showToast(
        "warning",
        restoreScopeType === "prefix" ? "Enter a key prefix." : "Enter at least one key.",
      );
      return;
    }

    const req: RestoreRevisionRequest = {
      revision,
      ...scope,
      dryRun: true,
      force: restoreForce,
    };

    restoreBusy = true;
    try {
      const plan = await client.mvcc.restoreToRevision(req);
      restorePlan = plan;
      restoreRequestBase = { ...req, dryRun: false };
      restorePhase = "preview";
    } catch (err: unknown) {
      // Surfaces the server's actionable message verbatim -- notably the
      // "requires raft.sm_type: mvcc" refusal a non-MVCC daemon (the
      // default) returns for every restore call.
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      restoreBusy = false;
    }
  }

  async function applyRestore(force: boolean): Promise<void> {
    if (restoreRequestBase === null) return;
    restoreBusy = true;
    try {
      const outcome = await client.mvcc.restoreToRevision({ ...restoreRequestBase, force });
      restoreOutcome = outcome;
      restoreForce = force;
      restorePhase = "result";
      if (outcome.conflicted > 0) {
        showToast(
          "warning",
          `Restored ${outcome.applied}, skipped ${outcome.skipped}; ${outcome.conflicted} key(s) left unrestored due to conflicts.`,
        );
      } else {
        showToast("success", `Restore applied: ${outcome.applied} key(s) updated.`);
      }
      await loadEntries();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      restoreBusy = false;
    }
  }

  function handleRestoreModalSubmit(): void {
    if (restorePhase === "form") {
      void handleRestorePreview();
    } else if (restorePhase === "preview") {
      void applyRestore(restoreForce);
    } else if (restoreOutcome && restoreOutcome.conflicted > 0) {
      void applyRestore(true);
    } else {
      closeRestoreModal();
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  // Re-load entries whenever the client reference changes (e.g. navigating
  // between databases) and on initial mount. Using $effect instead of onMount
  // ensures data is fetched even when the component stays mounted but receives
  // a new client prop after a database switch.
  $effect(() => {
    void client;
    loadEntries();
  });
</script>

<PageHeader
  title="KV Store"
  description="Browse, create, and manage key-value pairs"
  accentColor="var(--color-kv)"
>
  {#snippet actions()}
    <div class="flex items-center gap-2">
      <button class="btn btn-outline btn-sm gap-1" onclick={() => openRestoreModal()}>
        <History class="h-4 w-4" />
        Restore...
      </button>
      <button class="btn btn-ghost btn-sm" onclick={loadEntries} disabled={loading}>
        {#if loading}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <RefreshCw class="h-4 w-4" />
        {/if}
        Refresh
      </button>
    </div>
  {/snippet}
</PageHeader>

<!-- Search/Filter Bar -->
<div class="surface-panel mb-4 rounded-xl p-4">
  <div class="flex flex-wrap items-center gap-3">
    <div class="min-w-60 flex-1">
      <div class="join w-full max-w-md">
        <input
          type="text"
          class="input join-item input-bordered input-sm w-full font-mono"
          placeholder="Filter by key prefix..."
          bind:value={prefixFilter}
          onkeydown={(e) => e.key === "Enter" && loadEntries()}
        />
        <button class="btn btn-sm join-item" onclick={loadEntries} disabled={loading} aria-label="Search">
          <Search class="h-4 w-4" />
        </button>
      </div>
    </div>

    <div class="flex items-center gap-2">
      <label class="text-base-content/60 text-xs" for="kv-as-of-revision">As of revision</label>
      <input
        id="kv-as-of-revision"
        type="number"
        min="1"
        class="input input-bordered input-sm w-28 font-mono"
        placeholder="live"
        bind:value={asOfRevisionInput}
        onkeydown={(e) => e.key === "Enter" && viewAsOf()}
      />
      {#if !isHistorical}
        <button class="btn btn-outline btn-sm" onclick={viewAsOf} disabled={loading}>View</button>
      {:else}
        <button class="btn btn-ghost btn-sm gap-1" onclick={clearAsOf} disabled={loading}>
          <RotateCcw class="h-3.5 w-3.5" />
          Back to Live
        </button>
      {/if}
    </div>

    <div class="text-base-content/60 text-sm">
      {entries.length} entries
    </div>
  </div>

  {#if isHistorical}
    <div role="alert" class="alert alert-warning mt-3 py-2 text-sm">
      <Clock class="h-4 w-4 shrink-0" />
      <span>
        Viewing historical state as of revision <strong>{asOfRevision}</strong> (read-only). Create,
        edit, and delete are disabled here; use <strong>Restore...</strong> to write this state forward
        as new revisions.
      </span>
    </div>
  {/if}
</div>

<!-- Main Content -->
<div class="surface-panel rounded-xl overflow-hidden">
  <CrudTable
    title={isHistorical ? `Key-Value Entries (as of revision ${asOfRevision})` : "Key-Value Entries"}
    data={entries}
    columns={browseColumns}
    {loading}
    showCreate={!isHistorical}
    showDelete={!isHistorical}
    createLabel="Create"
    deleteLabel="Delete"
    oncreateclick={openCreatePanel}
    ondeleteselected={handleBulkDelete}
    onrowclick={handleRowClick}
    rowKey={(row) => row.key}
    pageSize={10}
  />
</div>

<!-- Detail SlidePanel -->
<SlidePanel
  open={selectedEntry !== null}
  title={isHistorical ? "Historical Details" : "Details"}
  onclose={() => (selectedEntry = null)}
>
  {#snippet children()}
    {#if selectedEntry}
      <div class="space-y-5">
        {#if isHistorical}
          <div role="alert" class="alert alert-warning py-2 text-xs">
            <Clock class="h-4 w-4 shrink-0" />
            <span>Value shown as of revision {asOfRevision}.</span>
          </div>
        {/if}
        <div>
          <span class="text-sm font-medium text-base-content/70 block mb-1">Key</span>
          <div class="font-mono text-sm bg-base-200 rounded px-3 py-2 break-all">{selectedEntry.key}</div>
        </div>
        <div>
          <span class="text-sm font-medium text-base-content/70 block mb-1">Value</span>
          <div class="font-mono text-sm bg-base-200 rounded px-3 py-2 break-all max-h-64 overflow-auto whitespace-pre-wrap">{selectedEntry.value}</div>
        </div>
      </div>
    {/if}
  {/snippet}
  {#snippet footer()}
    {#if selectedEntry}
      <div class="flex gap-2">
        {#if isHistorical}
          <button
            class="btn btn-sm btn-primary flex-1"
            onclick={restoreSelectedEntry}
          >
            <History class="h-4 w-4" />
            Restore This Key
          </button>
        {:else}
          <button
            class="btn btn-sm btn-outline flex-1"
            onclick={() => openEditPanel(selectedEntry!)}
          >
            <Pencil class="h-4 w-4" />
            Edit
          </button>
          <button
            class="btn btn-sm btn-error flex-1"
            onclick={() => requestDeleteEntry(selectedEntry!.key)}
          >
            <Trash2 class="h-4 w-4" />
            Delete
          </button>
        {/if}
      </div>
    {/if}
  {/snippet}
</SlidePanel>

<!-- Create/Edit FormModal -->
<FormModal
  open={showCreatePanel}
  title={editMode ? "Edit Entry" : "Create Entry"}
  submitLabel={editMode ? "Save" : "Create"}
  loading={modalLoading}
  onsubmit={handleSaveEntry}
  oncancel={() => (showCreatePanel = false)}
>
  {#snippet body()}
    <div class="space-y-5">
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Key</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono"
          placeholder="Enter key"
          bind:value={modalKey}
          disabled={editMode}
        />
        {#if editMode}
          <div class="label"><span class="label-text-alt text-base-content/50">Key cannot be changed</span></div>
        {/if}
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Value</span></div>
        <textarea
          class="textarea textarea-bordered w-full font-mono h-32"
          placeholder="Enter value (supports JSON)"
          bind:value={modalValue}
        ></textarea>
      </label>
    </div>
  {/snippet}
</FormModal>

<!-- Single-entry delete confirmation modal -->
<ConfirmModal
  open={deleteTarget !== null}
  title="Delete Entry"
  message={`Are you sure you want to delete "${deleteTarget ?? ""}"? This action cannot be undone.`}
  confirmLabel="Delete"
  variant="error"
  onconfirm={confirmDeleteEntry}
  oncancel={() => (deleteTarget = null)}
/>

<!-- Restore workflow modal: form -> preview -> result -->
<FormModal
  open={showRestoreModal}
  title={restoreModalTitle}
  submitLabel={restoreModalSubmitLabel}
  loading={restoreBusy}
  onsubmit={handleRestoreModalSubmit}
  oncancel={closeRestoreModal}
>
  {#snippet body()}
    {#if restorePhase === "form"}
      <div class="space-y-5">
        <p class="text-base-content/60 text-xs">
          Restore returns a bounded set of keys to the value they held at an earlier MVCC
          revision by proposing new writes -- a logical undo, never a Raft rewind. This
          requires the target daemon to run <code class="font-mono">raft.sm_type: mvcc</code>.
        </p>
        <label class="form-control w-full">
          <div class="label"><span class="label-text">Target revision</span></div>
          <input
            type="number"
            min="1"
            class="input input-bordered w-full font-mono"
            placeholder="Revision to restore to"
            bind:value={restoreRevisionInput}
          />
        </label>
        <div class="form-control w-full">
          <div class="label"><span class="label-text">Scope</span></div>
          <div class="join">
            <button
              type="button"
              class="btn btn-sm join-item {restoreScopeType === 'prefix' ? 'btn-active' : ''}"
              onclick={() => (restoreScopeType = "prefix")}
            >
              By Prefix
            </button>
            <button
              type="button"
              class="btn btn-sm join-item {restoreScopeType === 'keys' ? 'btn-active' : ''}"
              onclick={() => (restoreScopeType = "keys")}
            >
              By Keys
            </button>
          </div>
        </div>
        {#if restoreScopeType === "prefix"}
          <label class="form-control w-full">
            <div class="label"><span class="label-text-alt">Every key beginning with this prefix</span></div>
            <input
              type="text"
              class="input input-bordered w-full font-mono"
              placeholder="e.g. users/"
              bind:value={restoreScopePrefix}
            />
          </label>
        {:else}
          <label class="form-control w-full">
            <div class="label"><span class="label-text-alt">One key per line (or comma-separated)</span></div>
            <textarea
              class="textarea textarea-bordered w-full font-mono h-24"
              placeholder={"users/1\nusers/2"}
              bind:value={restoreScopeKeysText}
            ></textarea>
          </label>
        {/if}
        <label class="label cursor-pointer justify-start gap-3">
          <input type="checkbox" class="checkbox checkbox-sm" bind:checked={restoreForce} />
          <span class="label-text">
            Force -- overwrite keys modified since the plan was computed
          </span>
        </label>
      </div>
    {:else if restorePhase === "preview" && restorePlan}
      <div class="space-y-4">
        <div role="alert" class="alert alert-info py-2 text-sm">
          <AlertTriangle class="h-4 w-4 shrink-0" />
          <span>
            This is a preview. Nothing has been written yet -- confirm to apply
            {restorePlan.changes.length} change(s) as new revisions.
          </span>
        </div>
        <div class="max-h-72 overflow-auto rounded border border-base-content/10">
          <table class="table table-xs table-pin-rows">
            <thead>
              <tr>
                <th>Action</th>
                <th>Key</th>
                <th>From Rev</th>
                <th>To Rev</th>
              </tr>
            </thead>
            <tbody>
              {#each restorePlan.changes as change (change.key)}
                <tr>
                  <td><span class="badge badge-sm">{change.action}</span></td>
                  <td class="font-mono break-all">{change.key}</td>
                  <td>{change.fromRevision || "-"}</td>
                  <td>{change.toRevision || "-"}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <p class="text-base-content/60 text-xs">
          Revision {restorePlan.revision} &middot; {restorePlan.changes.length} planned change(s) &middot;
          {restorePlan.skipped} already match and will be skipped
          {restoreForce ? " · Force is ON" : ""}
        </p>
      </div>
    {:else if restorePhase === "result" && restoreOutcome}
      <div class="space-y-4">
        {#if restoreOutcome.conflicted > 0}
          <div role="alert" class="alert alert-warning py-2 text-sm">
            <AlertTriangle class="h-4 w-4 shrink-0" />
            <span>
              {restoreOutcome.conflicted} key(s) changed since the plan was computed and were left
              untouched. Click "Retry with Force" to overwrite them, or close and re-run the
              restore once you've confirmed the newer value should be discarded.
            </span>
          </div>
        {:else}
          <div role="alert" class="alert alert-success py-2 text-sm">
            <span>Restore applied with no conflicts.</span>
          </div>
        {/if}
        <div class="stats stats-horizontal shadow-none border border-base-content/10 w-full">
          <div class="stat py-2">
            <div class="stat-title text-xs">Applied</div>
            <div class="stat-value text-lg">{restoreOutcome.applied}</div>
          </div>
          <div class="stat py-2">
            <div class="stat-title text-xs">Skipped</div>
            <div class="stat-value text-lg">{restoreOutcome.skipped}</div>
          </div>
          <div class="stat py-2">
            <div class="stat-title text-xs">Conflicted</div>
            <div class="stat-value text-lg">{restoreOutcome.conflicted}</div>
          </div>
        </div>
        {#if restoreOutcome.conflicts && restoreOutcome.conflicts.length > 0}
          <div class="max-h-56 overflow-auto rounded border border-base-content/10">
            <table class="table table-xs table-pin-rows">
              <thead>
                <tr>
                  <th>Action</th>
                  <th>Key</th>
                  <th>From Rev</th>
                  <th>To Rev</th>
                </tr>
              </thead>
              <tbody>
                {#each restoreOutcome.conflicts as change (change.key)}
                  <tr>
                    <td><span class="badge badge-sm badge-warning">{change.action}</span></td>
                    <td class="font-mono break-all">{change.key}</td>
                    <td>{change.fromRevision || "-"}</td>
                    <td>{change.toRevision || "-"}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    {/if}
  {/snippet}
</FormModal>
