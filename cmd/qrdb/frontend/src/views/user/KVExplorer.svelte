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
    MVCCKeyValue,
    RestoreRevisionRequest,
    RestoreRevisionResult,
  } from "@qrdb/sdk";
  import type {
    Column,
    RowClickEvent,
  } from "../../components/core/DataTable/types.ts";
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

  // Selected entry's MVCC metadata (mvcc.getRevision) and the point-in-time
  // "value at revision" lookup (mvcc.getAtRevision).
  let entryMeta: MVCCKeyValue | null = $state(null);
  let entryMetaError: string = $state("");
  let atRevisionInput: string = $state("");
  let atRevisionResult: MVCCKeyValue | null = $state(null);
  let atRevisionError: string = $state("");
  let atRevisionBusy: boolean = $state(false);

  // Store-wide revision (mvcc.currentRevision) and compaction (mvcc.compact).
  let currentRevision: number | null = $state(null);
  let compactRevisionInput: string = $state("");
  let compactTarget: number | null = $state(null);

  // Optional lease for a save (mvcc.putWithLease); blank saves through kv.put.
  let modalLeaseId: string = $state("");

  // "Go to key" (kv.exists then kv.get) and the keys-only listing (kv.list).
  let gotoKeyInput: string = $state("");
  let keysOnly: boolean = $state(false);

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

  // Keys-only listing has no values, so the Value column is dropped.
  const visibleColumns = $derived(
    keysOnly && asOfRevision === null
      ? browseColumns.filter((c) => c.id === "key")
      : browseColumns,
  );

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
    return outcome !== null && outcome.conflicted > 0
      ? "Retry with Force"
      : "Close";
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
      } else if (keysOnly) {
        // Keys only: kv.list returns names without values; a row's value is
        // fetched with kv.get when it is opened.
        const keys = await withRetry(() => client.kv.list(prefixFilter));
        entries = [...keys].sort().map((key) => ({ key, value: "" }));
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
    await refreshCurrentRevision();
  }

  /** Reads the store-wide revision; a daemon without MVCC leaves it unknown. */
  async function refreshCurrentRevision(): Promise<void> {
    try {
      currentRevision = await client.mvcc.currentRevision();
    } catch {
      currentRevision = null;
    }
  }

  /** Asks for confirmation before compacting history up to the entered revision. */
  function requestCompact(): void {
    const revision = Number(compactRevisionInput);
    if (!Number.isInteger(revision) || revision <= 0) {
      showToast("warning", "Enter a positive revision to compact to.");
      return;
    }
    if (currentRevision !== null && revision > currentRevision) {
      showToast(
        "warning",
        `Revision ${revision} is beyond the current revision ${currentRevision}.`,
      );
      return;
    }
    compactTarget = revision;
  }

  async function confirmCompact(): Promise<void> {
    if (compactTarget === null) return;
    const revision = compactTarget;
    compactTarget = null;
    try {
      await client.mvcc.compact(revision);
      showToast("success", `Compacted history to revision ${revision}`);
      compactRevisionInput = "";
      await refreshCurrentRevision();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  /** Opens the detail panel for entry and loads its MVCC metadata. */
  async function selectEntry(entry: KVEntry): Promise<void> {
    selectedEntry = entry;
    entryMeta = null;
    entryMetaError = "";
    atRevisionInput = "";
    atRevisionResult = null;
    atRevisionError = "";
    try {
      const meta = await client.mvcc.getRevision(entry.key);
      if (selectedEntry?.key === entry.key) entryMeta = meta;
    } catch (err: unknown) {
      if (selectedEntry?.key === entry.key) {
        entryMetaError = err instanceof Error ? err.message : String(err);
      }
    }
  }

  /** Opens key's panel, fetching its current value with kv.get. */
  async function openKey(key: string): Promise<void> {
    try {
      const value = await client.kv.get(key);
      await selectEntry({ key, value });
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  /** "Go to key": checks the key exists, then opens it. */
  async function goToKey(): Promise<void> {
    const key = gotoKeyInput.trim();
    if (key === "") {
      showToast("warning", "Enter a key.");
      return;
    }
    try {
      const exists = await client.kv.exists(key);
      if (!exists) {
        showToast("warning", `Key "${key}" does not exist.`);
        return;
      }
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      return;
    }
    await openKey(key);
  }

  async function toggleKeysOnly(): Promise<void> {
    selectedEntry = null;
    await loadEntries();
  }

  /** Looks up the selected key's value at the entered revision. */
  async function lookupAtRevision(): Promise<void> {
    if (selectedEntry === null) return;
    const revision = Number(atRevisionInput);
    atRevisionResult = null;
    atRevisionError = "";
    if (!Number.isInteger(revision) || revision <= 0) {
      atRevisionError = "Enter a positive revision.";
      return;
    }
    const key = selectedEntry.key;
    atRevisionBusy = true;
    try {
      const kv = await client.mvcc.getAtRevision(key, revision);
      if (selectedEntry?.key === key) atRevisionResult = kv;
    } catch (err: unknown) {
      if (selectedEntry?.key === key) {
        atRevisionError = err instanceof Error ? err.message : String(err);
      }
    } finally {
      atRevisionBusy = false;
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
    modalLeaseId = "";
    showCreatePanel = true;
  }

  function openEditPanel(entry: KVEntry): void {
    editMode = true;
    modalKey = entry.key;
    modalValue = entry.value;
    modalLeaseId =
      entryMeta && entryMeta.key === entry.key && entryMeta.leaseId
        ? String(entryMeta.leaseId)
        : "";
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

    const leaseText = String(modalLeaseId ?? "").trim();
    const leaseId = leaseText === "" ? 0 : Number(leaseText);
    if (!Number.isInteger(leaseId) || leaseId < 0) {
      showToast("warning", "Lease ID must be a positive integer.");
      return;
    }

    modalLoading = true;
    try {
      if (leaseId > 0) {
        await client.mvcc.putWithLease(modalKey, modalValue, leaseId);
      } else {
        await client.kv.put(modalKey, modalValue);
      }
      const verb = editMode ? "Updated" : "Created";
      showToast(
        "success",
        leaseId > 0
          ? `${verb} "${modalKey}" with lease ${leaseId}`
          : `${verb} "${modalKey}"`,
      );
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
    if (keysOnly && !isHistorical) {
      void openKey(event.row.key);
    } else {
      void selectEntry(event.row);
    }
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
        restoreScopeType === "prefix"
          ? "Enter a key prefix."
          : "Enter at least one key.",
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
      const outcome = await client.mvcc.restoreToRevision({
        ...restoreRequestBase,
        force,
      });
      restoreOutcome = outcome;
      restoreForce = force;
      restorePhase = "result";
      if (outcome.conflicted > 0) {
        showToast(
          "warning",
          `Restored ${outcome.applied}, skipped ${outcome.skipped}; ${outcome.conflicted} key(s) left unrestored due to conflicts.`,
        );
      } else {
        showToast(
          "success",
          `Restore applied: ${outcome.applied} key(s) updated.`,
        );
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
      <button
        class="btn btn-outline btn-sm gap-1"
        onclick={() => openRestoreModal()}
      >
        <History class="h-4 w-4" />
        Restore...
      </button>
      <button
        class="btn btn-ghost btn-sm"
        onclick={loadEntries}
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
        <button
          class="btn btn-sm join-item"
          onclick={loadEntries}
          disabled={loading}
          aria-label="Search"
        >
          <Search class="h-4 w-4" />
        </button>
      </div>
    </div>

    <div class="flex items-center gap-2">
      <label class="text-base-content/60 text-xs" for="kv-as-of-revision"
        >As of revision</label
      >
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
        <button
          class="btn btn-outline btn-sm"
          onclick={viewAsOf}
          disabled={loading}>View</button
        >
      {:else}
        <button
          class="btn btn-ghost btn-sm gap-1"
          onclick={clearAsOf}
          disabled={loading}
        >
          <RotateCcw class="h-3.5 w-3.5" />
          Back to Live
        </button>
      {/if}
    </div>

    <div class="text-base-content/60 text-sm">
      {entries.length} entries
    </div>
  </div>

  <div class="mt-3 flex flex-wrap items-center gap-3 text-sm">
    <span class="text-base-content/70" data-testid="kv-current-revision">
      Current revision: <strong class="font-mono"
        >{currentRevision ?? "—"}</strong
      >
    </span>

    <div class="join">
      <input
        type="number"
        min="1"
        class="input join-item input-bordered input-sm w-28 font-mono"
        placeholder="revision"
        aria-label="Compact revision"
        bind:value={compactRevisionInput}
        onkeydown={(e) => e.key === "Enter" && requestCompact()}
      />
      <button class="btn btn-outline btn-sm join-item" onclick={requestCompact}>
        Compact to revision
      </button>
    </div>

    <div class="join">
      <input
        type="text"
        class="input join-item input-bordered input-sm w-48 font-mono"
        placeholder="Go to key..."
        aria-label="Go to key"
        bind:value={gotoKeyInput}
        disabled={isHistorical}
        onkeydown={(e) => e.key === "Enter" && goToKey()}
      />
      <button
        class="btn btn-sm join-item"
        onclick={goToKey}
        disabled={isHistorical}>Go</button
      >
    </div>

    <label class="label cursor-pointer gap-2">
      <input
        type="checkbox"
        class="toggle toggle-sm"
        aria-label="Keys only"
        bind:checked={keysOnly}
        onchange={toggleKeysOnly}
        disabled={isHistorical}
      />
      <span class="label-text">Keys only</span>
    </label>
  </div>

  {#if isHistorical}
    <div role="alert" class="alert alert-warning mt-3 py-2 text-sm">
      <Clock class="h-4 w-4 shrink-0" />
      <span>
        Viewing historical state as of revision <strong>{asOfRevision}</strong>
        (read-only). Create, edit, and delete are disabled here; use
        <strong>Restore...</strong> to write this state forward as new revisions.
      </span>
    </div>
  {/if}
</div>

<!-- Main Content -->
<div class="surface-panel rounded-xl overflow-hidden">
  <CrudTable
    title={isHistorical
      ? `Key-Value Entries (as of revision ${asOfRevision})`
      : "Key-Value Entries"}
    data={entries}
    columns={visibleColumns}
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
  {#if selectedEntry}
    <div class="space-y-5">
      {#if isHistorical}
        <div role="alert" class="alert alert-warning py-2 text-xs">
          <Clock class="h-4 w-4 shrink-0" />
          <span>Value shown as of revision {asOfRevision}.</span>
        </div>
      {/if}
      <div>
        <span class="text-sm font-medium text-base-content/70 block mb-1"
          >Key</span
        >
        <div class="font-mono text-sm bg-base-200 rounded px-3 py-2 break-all">
          {selectedEntry.key}
        </div>
      </div>
      <div>
        <span class="text-sm font-medium text-base-content/70 block mb-1"
          >Value</span
        >
        <div
          class="font-mono text-sm bg-base-200 rounded px-3 py-2 break-all max-h-64 overflow-auto whitespace-pre-wrap"
        >
          {selectedEntry.value}
        </div>
      </div>
      <div data-testid="kv-entry-revision">
        <span class="text-sm font-medium text-base-content/70 block mb-1"
          >Latest revision</span
        >
        {#if entryMeta}
          <dl
            class="grid grid-cols-2 gap-x-4 gap-y-1 text-sm bg-base-200 rounded px-3 py-2"
          >
            <dt class="text-base-content/60">Create revision</dt>
            <dd class="font-mono" data-testid="kv-create-revision">
              {entryMeta.createRevision ?? "—"}
            </dd>
            <dt class="text-base-content/60">Mod revision</dt>
            <dd class="font-mono" data-testid="kv-mod-revision">
              {entryMeta.modRevision ?? "—"}
            </dd>
            <dt class="text-base-content/60">Version</dt>
            <dd class="font-mono" data-testid="kv-version">
              {entryMeta.version ?? "—"}
            </dd>
            <dt class="text-base-content/60">Lease</dt>
            <dd class="font-mono" data-testid="kv-lease">
              {entryMeta.leaseId ? entryMeta.leaseId : "none"}
            </dd>
          </dl>
        {:else if entryMetaError}
          <div class="text-error text-xs" data-testid="kv-entry-revision-error">
            {entryMetaError}
          </div>
        {:else}
          <span class="loading loading-spinner loading-xs"></span>
        {/if}
      </div>
      <div>
        <label
          class="text-sm font-medium text-base-content/70 block mb-1"
          for="kv-at-revision"
        >
          Value at revision
        </label>
        <div class="join w-full">
          <input
            id="kv-at-revision"
            type="number"
            min="1"
            class="input join-item input-bordered input-sm w-full font-mono"
            placeholder="revision"
            bind:value={atRevisionInput}
            onkeydown={(e) => e.key === "Enter" && lookupAtRevision()}
          />
          <button
            class="btn btn-sm join-item"
            onclick={lookupAtRevision}
            disabled={atRevisionBusy}
          >
            Look up
          </button>
        </div>
        {#if atRevisionResult}
          <div
            class="mt-2 font-mono text-sm bg-base-200 rounded px-3 py-2 break-all whitespace-pre-wrap"
            data-testid="kv-at-revision-value"
          >
            {atRevisionResult.value}
          </div>
        {:else if atRevisionError}
          <div
            class="mt-2 text-error text-xs"
            data-testid="kv-at-revision-error"
          >
            {atRevisionError}
          </div>
        {/if}
      </div>
    </div>
  {/if}
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
          <div class="label">
            <span class="label-text-alt text-base-content/50"
              >Key cannot be changed</span
            >
          </div>
        {/if}
      </label>
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Value</span></div>
        <textarea
          class="textarea textarea-bordered w-full font-mono h-32"
          placeholder="Enter value (supports JSON)"
          bind:value={modalValue}></textarea>
      </label>
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Lease ID (optional)</span>
        </div>
        <input
          type="text"
          inputmode="numeric"
          class="input input-bordered w-full font-mono"
          placeholder="Attach the key to a lease; blank for none"
          bind:value={modalLeaseId}
        />
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

<!-- Compaction confirmation modal -->
<ConfirmModal
  open={compactTarget !== null}
  title="Compact History"
  message={`Discard every revision older than ${compactTarget ?? ""}? Reads and restores below it will no longer be possible.`}
  confirmLabel="Compact"
  variant="warning"
  onconfirm={confirmCompact}
  oncancel={() => (compactTarget = null)}
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
          Restore returns a bounded set of keys to the value they held at an
          earlier MVCC revision by proposing new writes -- a logical undo, never
          a Raft rewind. This requires the target daemon to run <code
            class="font-mono">raft.sm_type: mvcc</code
          >.
        </p>
        <label class="form-control w-full">
          <div class="label">
            <span class="label-text">Target revision</span>
          </div>
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
              class="btn btn-sm join-item {restoreScopeType === 'prefix'
                ? 'btn-active'
                : ''}"
              onclick={() => (restoreScopeType = "prefix")}
            >
              By Prefix
            </button>
            <button
              type="button"
              class="btn btn-sm join-item {restoreScopeType === 'keys'
                ? 'btn-active'
                : ''}"
              onclick={() => (restoreScopeType = "keys")}
            >
              By Keys
            </button>
          </div>
        </div>
        {#if restoreScopeType === "prefix"}
          <label class="form-control w-full">
            <div class="label">
              <span class="label-text-alt"
                >Every key beginning with this prefix</span
              >
            </div>
            <input
              type="text"
              class="input input-bordered w-full font-mono"
              placeholder="e.g. users/"
              bind:value={restoreScopePrefix}
            />
          </label>
        {:else}
          <label class="form-control w-full">
            <div class="label">
              <span class="label-text-alt"
                >One key per line (or comma-separated)</span
              >
            </div>
            <textarea
              class="textarea textarea-bordered w-full font-mono h-24"
              placeholder={"users/1\nusers/2"}
              bind:value={restoreScopeKeysText}></textarea>
          </label>
        {/if}
        <label class="label cursor-pointer justify-start gap-3">
          <input
            type="checkbox"
            class="checkbox checkbox-sm"
            bind:checked={restoreForce}
          />
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
        <div
          class="max-h-72 overflow-auto rounded border border-base-content/10"
        >
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
          Revision {restorePlan.revision} &middot; {restorePlan.changes.length} planned
          change(s) &middot;
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
              {restoreOutcome.conflicted} key(s) changed since the plan was computed
              and were left untouched. Click "Retry with Force" to overwrite them,
              or close and re-run the restore once you've confirmed the newer value
              should be discarded.
            </span>
          </div>
        {:else}
          <div role="alert" class="alert alert-success py-2 text-sm">
            <span>Restore applied with no conflicts.</span>
          </div>
        {/if}
        <div
          class="stats stats-horizontal shadow-none border border-base-content/10 w-full"
        >
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
          <div
            class="max-h-56 overflow-auto rounded border border-base-content/10"
          >
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
                    <td
                      ><span class="badge badge-sm badge-warning"
                        >{change.action}</span
                      ></td
                    >
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
