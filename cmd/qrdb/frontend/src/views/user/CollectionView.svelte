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
  import { onMount } from "svelte";
  import type {
    Client,
    DatabaseInfo,
    IndexQueryResponse,
    IndexScanResponse,
  } from "@qrdb/sdk";
  import type {
    Column,
    RowClickEvent,
  } from "../../components/core/DataTable/types.ts";
  import { showToast } from "../../stores/toast.svelte.ts";
  import { withRetry } from "../../lib/retry.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import HelpTip from "../../components/HelpTip.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import SlidePanel from "../../components/core/SlidePanel.svelte";
  import FormModal from "../../components/FormModal.svelte";
  import EmptyState from "../../components/EmptyState.svelte";
  import SchemaForm from "../../components/core/SchemaForm.svelte";
  import {
    FolderOpen,
    Plus,
    Trash2,
    Pencil,
    FileText,
    RefreshCw,
  } from "lucide-svelte";

  let { client, database }: { client: Client; database: DatabaseInfo } =
    $props();

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  interface CollectionInfo {
    name: string;
    database: string;
    id_strategy: string;
    field_hash?: string;
    schema?: Record<string, string>;
    document_count: number;
    created_at: string;
  }

  interface DocumentResponse {
    id: string;
    document: Record<string, unknown>;
  }

  /**
   * Adapter interface for collection operations. Matches the SDK's
   * CollectionClient interface so we can cast through safely.
   */
  interface CollectionClient {
    list(database: string): Promise<CollectionInfo[]>;
    create(req: {
      database: string;
      name: string;
      id_strategy: string;
      field_hash?: string;
    }): Promise<CollectionInfo>;
    drop(database: string, name: string): Promise<void>;
    listDocuments(req: {
      database: string;
      collection: string;
      page_size?: number;
      page_token?: string;
    }): Promise<{
      documents: DocumentResponse[];
      next_page_token?: string;
      total_count?: number;
    }>;
    insertDocument(req: {
      database: string;
      collection: string;
      document: Record<string, unknown>;
    }): Promise<{ id: string }>;
    getDocument(
      database: string,
      collection: string,
      id: string,
    ): Promise<DocumentResponse>;
    updateDocument(req: {
      database: string;
      collection: string;
      id: string;
      document: Record<string, unknown>;
    }): Promise<void>;
    deleteDocument(
      database: string,
      collection: string,
      id: string,
    ): Promise<void>;
    get(database: string, name: string): Promise<CollectionInfo>;
  }

  // ---------------------------------------------------------------------------
  // SDK access
  // ---------------------------------------------------------------------------

  const collectionClient = $derived(
    (client as unknown as Record<string, unknown>).collection as
      CollectionClient | undefined,
  );

  // ---------------------------------------------------------------------------
  // State: Collections (left panel)
  // ---------------------------------------------------------------------------

  let collections: CollectionInfo[] = $state([]);
  let collectionsLoading: boolean = $state(false);
  let selectedCollection: CollectionInfo | null = $state(null);

  // Create collection panel
  let showCreateCollectionPanel: boolean = $state(false);
  let createName: string = $state("");
  let createIdStrategy: string = $state("unix_nano");
  let createFieldHash: string = $state("");
  let createLoading: boolean = $state(false);

  // Drop collection confirmation
  let dropTarget: CollectionInfo | null = $state(null);

  // ---------------------------------------------------------------------------
  // State: Documents (right panel)
  // ---------------------------------------------------------------------------

  let documents: DocumentResponse[] = $state([]);
  let documentsLoading: boolean = $state(false);

  // Insert/Edit document panel
  let showDocumentPanel: boolean = $state(false);
  let documentEditMode: boolean = $state(false);
  let documentEditId: string | null = $state(null);
  let documentJson: string = $state("{}");
  let documentJsonError: string = $state("");
  let documentPanelLoading: boolean = $state(false);
  let documentFormMode: boolean = $state(true);
  let documentFormFields: Record<string, unknown> = $state({});
  let documentPanelKey: number = $state(0);

  // Document detail panel
  let selectedDocument: DocumentResponse | null = $state(null);

  // Delete document confirmation
  let deleteDocumentTarget: DocumentResponse | null = $state(null);

  // ---------------------------------------------------------------------------
  // State: Index management (scoped to selected collection)
  // ---------------------------------------------------------------------------

  type CollectionDetailTab = "documents" | "indexes";
  let collectionDetailTab: CollectionDetailTab = $state("documents");

  type IndexInnerTab = "manage" | "query" | "scan";
  let indexActiveTab: IndexInnerTab = $state("manage");

  // Full index names scoped to this collection (e.g. "mydb.users.email")
  let collectionIndexes: string[] = $state([]);
  let indexesLoading: boolean = $state(false);

  // Derived: strip the "{database}.{collection}." prefix for display
  const collectionIndexPrefix = $derived.by((): string => {
    const col = selectedCollection;
    if (col === null) return "";
    return `${database.name}.${col.name}.`;
  });

  const collectionFieldNames = $derived(
    collectionIndexes.map((name) =>
      name.startsWith(collectionIndexPrefix)
        ? name.slice(collectionIndexPrefix.length)
        : name,
    ),
  );

  // Create index modal
  let showCreateIndexModal: boolean = $state(false);
  let newFieldName: string = $state("");
  let newUnique: boolean = $state(false);
  let newSparse: boolean = $state(false);
  let newNumeric: boolean = $state(false);
  let createIndexLoading: boolean = $state(false);

  // Drop index confirmation
  let dropIndexTarget: string | null = $state(null);

  // Query index state
  let selectedQueryIndex: string = $state("");
  let queryValue: string = $state("");
  let queryLoading: boolean = $state(false);
  let queryResults: IndexQueryResponse | null = $state(null);

  // Scan index state
  let selectedScanIndex: string = $state("");
  let scanStart: string = $state("");
  let scanEnd: string = $state("");
  let scanLoading: boolean = $state(false);
  let scanResults: IndexScanResponse | null = $state(null);

  // ---------------------------------------------------------------------------
  // Derived: Document Columns (dynamic based on document fields)
  // ---------------------------------------------------------------------------

  const documentColumns = $derived.by((): Column<Record<string, unknown>>[] => {
    const baseColumns: Column<Record<string, unknown>>[] = [
      {
        id: "id",
        header: "ID",
        accessor: "id",
        sortable: true,
        width: "80px",
        align: "center",
      },
    ];

    if (documents.length === 0) {
      return baseColumns;
    }

    // Extract unique field keys from all documents to build dynamic columns
    const fieldKeys = new Set<string>();
    for (const doc of documents) {
      if (doc.document !== null && doc.document !== undefined) {
        for (const key of Object.keys(doc.document)) {
          fieldKeys.add(key);
        }
      }
    }

    const fieldColumns: Column<Record<string, unknown>>[] = Array.from(
      fieldKeys,
    )
      .sort()
      .map((key) => ({
        id: `field_${key}`,
        header: key,
        accessor: (row: Record<string, unknown>) => {
          const doc = row.document as Record<string, unknown> | undefined;
          if (doc === undefined || doc === null) return "";
          const val = doc[key];
          if (val === undefined || val === null) return "";
          if (typeof val === "object") return JSON.stringify(val);
          return String(val);
        },
        sortable: true,
        filterable: true,
        filterType: "text" as const,
      }));

    return [...baseColumns, ...fieldColumns];
  });

  // ---------------------------------------------------------------------------
  // Derived: Document table data (flatten for CrudTable compatibility)
  // ---------------------------------------------------------------------------

  const documentTableData = $derived(
    documents.map((doc) => ({
      id: doc.id,
      document: doc.document,
    })),
  );

  // ---------------------------------------------------------------------------
  // Actions: Collections
  // ---------------------------------------------------------------------------

  async function loadCollections(): Promise<void> {
    if (collectionClient === undefined) {
      showToast(
        "warning",
        "Collection API is not available in the current SDK.",
      );
      return;
    }
    collectionsLoading = true;
    try {
      collections = await withRetry(() =>
        collectionClient!.list(database.name),
      );
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      collections = [];
    } finally {
      collectionsLoading = false;
    }
  }

  function openCreateCollectionPanel(): void {
    createName = "";
    createIdStrategy = "unix_nano";
    createFieldHash = "";
    showCreateCollectionPanel = true;
  }

  async function handleCreateCollection(): Promise<void> {
    if (collectionClient === undefined) return;
    if (createName.trim() === "") {
      showToast("warning", "Collection name is required.");
      return;
    }
    if (createIdStrategy === "field_hash" && createFieldHash.trim() === "") {
      showToast(
        "warning",
        "Field hash is required when using the field_hash ID strategy.",
      );
      return;
    }

    createLoading = true;
    try {
      await collectionClient.create({
        database: database.name,
        name: createName,
        id_strategy: createIdStrategy,
        ...(createIdStrategy === "field_hash"
          ? { field_hash: createFieldHash }
          : {}),
      });
      showToast("success", `Collection "${createName}" created.`);
      showCreateCollectionPanel = false;
      await loadCollections();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      createLoading = false;
    }
  }

  function requestDropCollection(collection: CollectionInfo): void {
    dropTarget = collection;
  }

  async function confirmDropCollection(): Promise<void> {
    if (collectionClient === undefined || dropTarget === null) return;
    const name = dropTarget.name;
    dropTarget = null;
    collectionsLoading = true;
    try {
      await collectionClient.drop(database.name, name);
      showToast("success", `Collection "${name}" dropped.`);
      if (selectedCollection?.name === name) {
        selectedCollection = null;
        documents = [];
        collectionIndexes = [];
      }
      await loadCollections();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      collectionsLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Actions: Documents
  // ---------------------------------------------------------------------------

  async function loadDocuments(): Promise<void> {
    if (collectionClient === undefined || selectedCollection === null) return;
    documentsLoading = true;
    try {
      const resp = await withRetry(() =>
        collectionClient!.listDocuments({
          database: database.name,
          collection: selectedCollection!.name,
        }),
      );
      documents = resp.documents ?? [];
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      documents = [];
    } finally {
      documentsLoading = false;
    }
  }

  function openInsertDocumentPanel(): void {
    documentEditMode = false;
    documentEditId = null;
    documentJson = "{}";
    documentJsonError = "";
    documentFormFields = {};
    documentFormMode = true;
    documentPanelKey++;
    showDocumentPanel = true;
  }

  function openEditDocumentPanel(doc: DocumentResponse): void {
    documentEditMode = true;
    documentEditId = doc.id;
    documentJson = JSON.stringify(doc.document, null, 2);
    documentFormFields = { ...doc.document };
    documentJsonError = "";
    selectedDocument = null;
    documentFormMode = true;
    documentPanelKey++;
    showDocumentPanel = true;
  }

  function validateJson(value: string): boolean {
    try {
      const parsed = JSON.parse(value);
      if (
        typeof parsed !== "object" ||
        parsed === null ||
        Array.isArray(parsed)
      ) {
        documentJsonError = "Document must be a JSON object.";
        return false;
      }
      documentJsonError = "";
      return true;
    } catch {
      documentJsonError = "Invalid JSON syntax.";
      return false;
    }
  }

  async function handleSaveDocument(): Promise<void> {
    if (collectionClient === undefined || selectedCollection === null) return;
    if (documentFormMode) {
      documentJson = JSON.stringify(documentFormFields);
    }
    if (!validateJson(documentJson)) return;

    const parsed = JSON.parse(documentJson) as Record<string, unknown>;

    documentPanelLoading = true;
    try {
      if (documentEditMode && documentEditId !== null) {
        await collectionClient.updateDocument({
          database: database.name,
          collection: selectedCollection.name,
          id: documentEditId,
          document: parsed,
        });
        showToast("success", `Document ${documentEditId} updated.`);
      } else {
        await collectionClient.insertDocument({
          database: database.name,
          collection: selectedCollection.name,
          document: parsed,
        });
        showToast("success", "Document inserted.");
      }
      showDocumentPanel = false;
      await loadDocuments();
      await refreshSelectedCollection();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      documentPanelLoading = false;
    }
  }

  // handleDocumentRowClick fetches the document through
  // collection.getDocument so the detail panel shows the stored document,
  // not the possibly stale list row; a failed fetch is an error toast and
  // the panel stays closed. documentFetchToken drops a superseded response.
  let documentFetchToken = 0;
  async function handleDocumentRowClick(
    event: RowClickEvent<Record<string, unknown>>,
  ): Promise<void> {
    if (collectionClient === undefined || selectedCollection === null) return;
    const id = event.row.id as string;
    const token = ++documentFetchToken;
    try {
      const doc = await collectionClient.getDocument(
        database.name,
        selectedCollection.name,
        id,
      );
      if (token !== documentFetchToken) return;
      selectedDocument = doc;
    } catch (err: unknown) {
      if (token !== documentFetchToken) return;
      showToast("error", err instanceof Error ? err.message : String(err));
    }
  }

  function requestDeleteDocument(doc: DocumentResponse): void {
    deleteDocumentTarget = doc;
  }

  async function confirmDeleteDocument(): Promise<void> {
    if (
      collectionClient === undefined ||
      selectedCollection === null ||
      deleteDocumentTarget === null
    )
      return;
    const id = deleteDocumentTarget.id;
    deleteDocumentTarget = null;
    documentsLoading = true;
    try {
      await collectionClient.deleteDocument(
        database.name,
        selectedCollection.name,
        id,
      );
      showToast("success", `Document ${id} deleted.`);
      if (selectedDocument?.id === id) {
        selectedDocument = null;
      }
      await loadDocuments();
      await refreshSelectedCollection();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      documentsLoading = false;
    }
  }

  async function handleBulkDeleteDocuments(
    indices: Set<number>,
  ): Promise<void> {
    if (collectionClient === undefined || selectedCollection === null) return;
    documentsLoading = true;
    const docsToDelete: DocumentResponse[] = [];
    for (const idx of indices) {
      if (documents[idx]) docsToDelete.push(documents[idx]);
    }
    let successCount = 0;
    let failCount = 0;
    for (const doc of docsToDelete) {
      try {
        await collectionClient.deleteDocument(
          database.name,
          selectedCollection.name,
          doc.id,
        );
        successCount++;
      } catch {
        failCount++;
      }
    }
    if (failCount === 0) {
      showToast("success", `Deleted ${successCount} documents.`);
    } else {
      showToast("warning", `Deleted ${successCount}, failed ${failCount}.`);
    }
    selectedDocument = null;
    await loadDocuments();
    await refreshSelectedCollection();
    documentsLoading = false;
  }

  // ---------------------------------------------------------------------------
  // Helpers: Extract document ID from internal key
  // ---------------------------------------------------------------------------

  // Internal keys have the format "__col:{database}:{collection}/{docId}".
  // This helper strips the prefix to show just the document ID.
  function extractDocId(internalKey: string): string {
    const slashIdx = internalKey.lastIndexOf("/");
    return slashIdx >= 0 ? internalKey.slice(slashIdx + 1) : internalKey;
  }

  // ---------------------------------------------------------------------------
  // Helpers: Refresh selected collection metadata
  // ---------------------------------------------------------------------------

  async function refreshSelectedCollection(): Promise<void> {
    if (collectionClient === undefined || selectedCollection === null) return;
    try {
      const info = await collectionClient.get(
        database.name,
        selectedCollection.name,
      );
      if (info !== undefined) {
        selectedCollection = info;
        const idx = collections.findIndex((c) => c.name === info.name);
        if (idx !== -1) {
          collections[idx] = info;
          collections = [...collections];
        }
      }
    } catch {
      // Best effort — sidebar will catch up on next full refresh.
    }
  }

  // ---------------------------------------------------------------------------
  // Actions: Indexes (scoped to selected collection)
  // ---------------------------------------------------------------------------

  async function refreshCollectionIndexes(): Promise<void> {
    if (selectedCollection === null) return;
    indexesLoading = true;
    try {
      const result = await client.index.list();
      const prefix = `${database.name}.${selectedCollection.name}.`;
      collectionIndexes = (result.names ?? []).filter((n) =>
        n.startsWith(prefix),
      );
      // Reset query/scan selections when indexes change
      if (!collectionIndexes.includes(selectedQueryIndex))
        selectedQueryIndex = "";
      if (!collectionIndexes.includes(selectedScanIndex))
        selectedScanIndex = "";
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
      collectionIndexes = [];
    } finally {
      indexesLoading = false;
    }
  }

  function openCreateIndexModal(): void {
    newFieldName = "";
    newUnique = false;
    newSparse = false;
    newNumeric = false;
    showCreateIndexModal = true;
  }

  async function createCollectionIndex(): Promise<void> {
    if (selectedCollection === null) return;
    if (newFieldName.trim() === "") {
      showToast("warning", "Field name is required.");
      return;
    }
    const indexName = `${database.name}.${selectedCollection.name}.${newFieldName.trim()}`;
    createIndexLoading = true;
    try {
      await client.index.create({
        name: indexName,
        fieldName: newFieldName.trim(),
        unique: newUnique,
        sparse: newSparse,
        numeric: newNumeric,
      });
      showToast("success", `Index "${indexName}" created.`);
      showCreateIndexModal = false;
      newFieldName = "";
      newUnique = false;
      newSparse = false;
      newNumeric = false;
      await refreshCollectionIndexes();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      createIndexLoading = false;
    }
  }

  function requestDropIndex(name: string): void {
    dropIndexTarget = name;
  }

  async function dropCollectionIndex(): Promise<void> {
    if (dropIndexTarget === null) return;
    const name = dropIndexTarget;
    dropIndexTarget = null;
    indexesLoading = true;
    try {
      await client.index.drop(name);
      showToast("success", `Index "${name}" dropped.`);
      await refreshCollectionIndexes();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      indexesLoading = false;
    }
  }

  async function queryCollectionIndex(): Promise<void> {
    if (selectedQueryIndex === "" || queryValue.trim() === "") {
      showToast("warning", "Select an index and provide a value to query.");
      return;
    }
    queryLoading = true;
    queryResults = null;
    try {
      queryResults = await client.index.query(
        selectedQueryIndex,
        queryValue.trim(),
      );
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      queryLoading = false;
    }
  }

  async function scanCollectionIndex(): Promise<void> {
    if (
      selectedScanIndex === "" ||
      scanStart.trim() === "" ||
      scanEnd.trim() === ""
    ) {
      showToast("warning", "Select an index and provide start and end values.");
      return;
    }
    scanLoading = true;
    scanResults = null;
    try {
      scanResults = await client.index.scan(
        selectedScanIndex,
        scanStart.trim(),
        scanEnd.trim(),
      );
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      scanLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Help tip text (stored as constants to avoid Svelte template brace escaping)
  // ---------------------------------------------------------------------------

  const helpFieldName =
    'The JSON field name in your documents to index. For example, if your documents have {"email": "user@example.com"}, enter email.';
  const helpIndexName =
    "Index names are automatically generated as database.collection.field to ensure uniqueness across your system.";

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    loadCollections();
  });

  // Reload collections when the database changes
  $effect(() => {
    void database.name;
    selectedCollection = null;
    documents = [];
    collectionIndexes = [];
    loadCollections();
  });

  // Reset detail tab and load indexes when a collection is selected
  $effect(() => {
    if (selectedCollection !== null) {
      collectionDetailTab = "documents";
      refreshCollectionIndexes();
    } else {
      collectionIndexes = [];
      queryResults = null;
      scanResults = null;
    }
  });
</script>

<PageHeader
  title="Collections"
  description="Browse and manage document collections"
  accentColor="var(--color-collections)"
>
  {#snippet actions()}
    <div class="flex items-center gap-2">
      <button
        class="btn btn-ghost btn-sm"
        onclick={loadCollections}
        disabled={collectionsLoading}
      >
        {#if collectionsLoading}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <RefreshCw class="h-4 w-4" />
        {/if}
        Refresh
      </button>
    </div>
  {/snippet}
</PageHeader>

<!-- Two-panel layout -->
<div class="flex gap-6" style="min-height: 500px;">
  <!-- Left Panel: Collection List -->
  <div
    class="surface-panel flex w-80 shrink-0 flex-col rounded-xl border border-base-content/6"
  >
    <div
      class="flex items-center justify-between border-b border-base-content/6 px-4 py-3"
    >
      <div class="flex items-center gap-2 text-sm font-semibold">
        <FolderOpen class="h-4 w-4 text-base-content/60" />
        Collections
        <span class="badge badge-ghost badge-sm">{collections.length}</span>
      </div>
      <button
        class="btn btn-primary btn-xs"
        onclick={openCreateCollectionPanel}
        disabled={collectionClient === undefined}
      >
        <Plus class="h-3 w-3" />
        Create
      </button>
    </div>

    <div class="flex-1 overflow-y-auto">
      {#if collectionsLoading}
        <div class="flex items-center justify-center py-8">
          <span class="loading loading-spinner loading-md"></span>
        </div>
      {:else if collections.length === 0}
        <div class="px-4 py-8 text-center text-sm text-base-content/50">
          No collections found.
        </div>
      {:else}
        <ul class="menu menu-sm w-full p-2">
          {#each collections as collection (collection.name)}
            <li>
              <button
                class="flex w-full items-center justify-between rounded-lg px-3 py-2"
                class:active={selectedCollection?.name === collection.name}
                onclick={() => {
                  selectedCollection = collection;
                  loadDocuments();
                }}
              >
                <div class="flex flex-col items-start gap-0.5">
                  <span class="font-medium">{collection.name}</span>
                  <span class="text-xs text-base-content/50">
                    {collection.document_count} docs
                    <span class="mx-1">-</span>
                    {collection.id_strategy}
                  </span>
                </div>
                <span
                  role="button"
                  tabindex="0"
                  class="btn btn-ghost btn-xs text-error opacity-0 group-hover:opacity-100 hover:opacity-100"
                  title="Drop collection {collection.name} — permanently deletes all documents"
                  onclick={(e: MouseEvent) => {
                    e.stopPropagation();
                    requestDropCollection(collection);
                  }}
                  onkeydown={(e: KeyboardEvent) => {
                    if (e.key === "Enter") {
                      e.stopPropagation();
                      requestDropCollection(collection);
                    }
                  }}
                  aria-label="Drop collection {collection.name}"
                >
                  <Trash2 class="h-3 w-3" />
                </span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>

  <!-- Right Panel: Collection Detail with inner tabs -->
  <div class="flex flex-1 flex-col gap-4">
    {#if selectedCollection === null}
      <div
        class="surface-panel flex flex-1 items-center justify-center rounded-xl border border-base-content/6"
      >
        <EmptyState
          icon={FileText}
          title="Select a Collection"
          description="Choose a collection from the list to browse its documents."
        />
      </div>
    {:else}
      <!-- Collection header -->
      <div
        class="surface-panel flex items-center justify-between rounded-xl border border-base-content/6 px-4 py-3"
      >
        <div class="flex items-center gap-3">
          <h3 class="text-sm font-semibold">{selectedCollection.name}</h3>
          <span class="badge badge-ghost badge-sm"
            >{documents.length} documents</span
          >
          <span class="badge badge-outline badge-sm"
            >{selectedCollection.id_strategy}</span
          >
        </div>
      </div>

      <!-- Inner tab bar: Documents / Indexes -->
      <div role="tablist" class="tabs tabs-bordered">
        <button
          role="tab"
          class="tab"
          class:tab-active={collectionDetailTab === "documents"}
          onclick={() => {
            collectionDetailTab = "documents";
          }}
        >
          Documents
        </button>
        <button
          role="tab"
          class="tab"
          class:tab-active={collectionDetailTab === "indexes"}
          onclick={() => {
            collectionDetailTab = "indexes";
          }}
        >
          <span class="flex items-center gap-1">
            Indexes
            <HelpTip
              text="Secondary indexes speed up queries on specific document fields. Create an index on a field you frequently search by."
            />
            {#if collectionIndexes.length > 0}
              <span class="badge badge-info badge-sm ml-0.5"
                >{collectionIndexes.length}</span
              >
            {/if}
          </span>
        </button>
      </div>

      <!-- ------------------------------------------------------------------ -->
      <!-- Documents tab                                                       -->
      <!-- ------------------------------------------------------------------ -->
      {#if collectionDetailTab === "documents"}
        <!-- Document toolbar -->
        <div
          class="surface-panel flex items-center justify-end rounded-xl border border-base-content/6 px-4 py-2"
        >
          <div class="flex items-center gap-2">
            <button
              class="btn btn-ghost btn-xs"
              onclick={loadDocuments}
              disabled={documentsLoading}
            >
              {#if documentsLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {:else}
                <RefreshCw class="h-3 w-3" />
              {/if}
            </button>
            <button
              class="btn btn-primary btn-xs"
              onclick={openInsertDocumentPanel}
            >
              <Plus class="h-3 w-3" />
              Insert
            </button>
          </div>
        </div>

        <!-- Document table -->
        <div
          class="surface-panel overflow-hidden rounded-xl border border-base-content/6"
        >
          <CrudTable
            title="Documents"
            data={documentTableData}
            columns={documentColumns}
            loading={documentsLoading}
            showCreate={false}
            showDelete={true}
            deleteLabel="Delete"
            ondeleteselected={handleBulkDeleteDocuments}
            onrowclick={handleDocumentRowClick}
            rowKey={(row) => String(row.id)}
            pageSize={25}
          />
        </div>
      {/if}

      <!-- ------------------------------------------------------------------ -->
      <!-- Indexes tab                                                         -->
      <!-- ------------------------------------------------------------------ -->
      {#if collectionDetailTab === "indexes"}
        <div
          class="surface-panel flex flex-col gap-0 rounded-xl border border-base-content/6"
        >
          <!-- Manage / Query / Scan inner tabs -->
          <div class="border-b border-base-content/6 px-4 pt-3">
            <div role="tablist" class="tabs tabs-bordered">
              <button
                role="tab"
                class="tab"
                class:tab-active={indexActiveTab === "manage"}
                onclick={() => {
                  indexActiveTab = "manage";
                }}
              >
                Manage
              </button>
              <button
                role="tab"
                class="tab"
                class:tab-active={indexActiveTab === "query"}
                onclick={() => {
                  indexActiveTab = "query";
                }}
              >
                Query
              </button>
              <button
                role="tab"
                class="tab"
                class:tab-active={indexActiveTab === "scan"}
                onclick={() => {
                  indexActiveTab = "scan";
                }}
              >
                Scan
              </button>
            </div>
          </div>

          <div class="p-4">
            <!-- -------------------------------------------------------------- -->
            <!-- Manage sub-tab                                                  -->
            <!-- -------------------------------------------------------------- -->
            {#if indexActiveTab === "manage"}
              <div class="flex flex-col gap-4">
                <div class="flex items-center justify-between">
                  <h4 class="text-sm font-semibold">Collection Indexes</h4>
                  <div class="flex items-center gap-2">
                    <button
                      class="btn btn-ghost btn-xs"
                      onclick={refreshCollectionIndexes}
                      disabled={indexesLoading}
                      aria-label="Refresh indexes"
                    >
                      {#if indexesLoading}
                        <span class="loading loading-spinner loading-xs"></span>
                      {:else}
                        <RefreshCw class="h-3 w-3" />
                      {/if}
                      Refresh
                    </button>
                    <button
                      class="btn btn-primary btn-xs"
                      onclick={openCreateIndexModal}
                    >
                      <Plus class="h-3 w-3" />
                      Create Index
                    </button>
                  </div>
                </div>

                {#if indexesLoading}
                  <div class="flex items-center justify-center py-6">
                    <span class="loading loading-spinner loading-sm"></span>
                  </div>
                {:else if collectionIndexes.length === 0}
                  <div
                    class="rounded-lg border border-base-content/6 bg-base-200 px-4 py-6 text-center text-sm text-base-content/50"
                  >
                    No indexes defined. Create an index on a frequently queried
                    field to speed up lookups.
                  </div>
                {:else}
                  <div
                    class="overflow-x-auto rounded-lg border border-base-content/6"
                  >
                    <table class="table table-sm w-full">
                      <thead>
                        <tr>
                          <th>Field Name</th>
                          <th>Index Name</th>
                          <th class="w-20 text-right">Actions</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each collectionIndexes as name, i (name)}
                          <tr class="hover">
                            <td class="font-mono text-sm font-medium"
                              >{collectionFieldNames[i]}</td
                            >
                            <td class="font-mono text-xs text-base-content/60"
                              >{name}</td
                            >
                            <td class="text-right">
                              <button
                                class="btn btn-error btn-xs"
                                title="Drop index {name} — removes the index from this collection"
                                onclick={() => requestDropIndex(name)}
                              >
                                Drop
                              </button>
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                {/if}
              </div>
            {/if}

            <!-- -------------------------------------------------------------- -->
            <!-- Query sub-tab                                                   -->
            <!-- -------------------------------------------------------------- -->
            {#if indexActiveTab === "query"}
              <div class="flex flex-col gap-4">
                <div
                  class="flex flex-wrap items-end gap-3 rounded-lg bg-base-200 px-4 py-3"
                >
                  <label class="form-control w-full max-w-xs">
                    <div class="label">
                      <span class="label-text text-xs flex items-center gap-1">
                        Index
                        <HelpTip
                          text="Select which index to query. Each index corresponds to a field in your documents."
                        />
                      </span>
                    </div>
                    <select
                      class="select select-bordered select-sm w-full"
                      bind:value={selectedQueryIndex}
                    >
                      <option value="">-- select index --</option>
                      {#each collectionIndexes as name, i (name)}
                        <option value={name}>{collectionFieldNames[i]}</option>
                      {/each}
                    </select>
                  </label>
                  <label class="form-control w-full max-w-xs">
                    <div class="label">
                      <span class="label-text text-xs flex items-center gap-1">
                        Value
                        <HelpTip
                          text="The exact field value to search for. For example, if you indexed the 'email' field, enter an email address like 'user@example.com'."
                        />
                      </span>
                    </div>
                    <input
                      type="text"
                      class="input input-bordered input-sm w-full"
                      placeholder="Value to match"
                      bind:value={queryValue}
                    />
                  </label>
                  <button
                    class="btn btn-primary btn-sm"
                    onclick={queryCollectionIndex}
                    disabled={queryLoading}
                  >
                    {#if queryLoading}
                      <span class="loading loading-spinner loading-xs"></span>
                    {/if}
                    Query
                  </button>
                </div>

                <div class="flex flex-col gap-2">
                  <h4 class="text-sm font-semibold">Results</h4>
                  {#if queryResults === null}
                    <p class="text-sm text-base-content/50">No results</p>
                  {:else if queryResults.keys.length === 0}
                    <p class="text-sm text-base-content/50">
                      No matching entries found.
                    </p>
                  {:else}
                    <div
                      class="overflow-x-auto rounded-lg border border-base-content/6"
                    >
                      <table class="table table-sm w-full">
                        <thead>
                          <tr>
                            <th>#</th>
                            <th>Document ID</th>
                          </tr>
                        </thead>
                        <tbody>
                          {#each queryResults.keys as key, i (key)}
                            <tr class="hover">
                              <td class="text-xs text-base-content/50"
                                >{i + 1}</td
                              >
                              <td class="font-mono text-sm"
                                >{extractDocId(key)}</td
                              >
                            </tr>
                          {/each}
                        </tbody>
                      </table>
                    </div>
                  {/if}
                </div>
              </div>
            {/if}

            <!-- -------------------------------------------------------------- -->
            <!-- Scan sub-tab                                                    -->
            <!-- -------------------------------------------------------------- -->
            {#if indexActiveTab === "scan"}
              <div class="flex flex-col gap-4">
                <div
                  class="flex flex-wrap items-end gap-3 rounded-lg bg-base-200 px-4 py-3"
                >
                  <div
                    class="w-full mb-1 text-xs text-base-content/50 flex items-center gap-1"
                  >
                    Scan Index Range
                    <HelpTip
                      text="Scans a range of indexed values in lexicographic (alphabetical) order. Returns all documents whose indexed field value falls within the range [start, end)."
                      position="right"
                    />
                  </div>
                  <label class="form-control w-full max-w-xs">
                    <div class="label">
                      <span class="label-text text-xs flex items-center gap-1">
                        Index
                        <HelpTip
                          text="Select which index to query. Each index corresponds to a field in your documents."
                        />
                      </span>
                    </div>
                    <select
                      class="select select-bordered select-sm w-full"
                      bind:value={selectedScanIndex}
                    >
                      <option value="">-- select index --</option>
                      {#each collectionIndexes as name, i (name)}
                        <option value={name}>{collectionFieldNames[i]}</option>
                      {/each}
                    </select>
                  </label>
                  <label class="form-control w-full max-w-xs">
                    <div class="label">
                      <span class="label-text text-xs flex items-center gap-1">
                        Start value
                        <HelpTip
                          text="The beginning of the range (inclusive). For example, 'a' to find all values starting from 'a'."
                        />
                      </span>
                    </div>
                    <input
                      type="text"
                      class="input input-bordered input-sm w-full"
                      placeholder="Start value"
                      bind:value={scanStart}
                    />
                  </label>
                  <label class="form-control w-full max-w-xs">
                    <div class="label">
                      <span class="label-text text-xs flex items-center gap-1">
                        End value
                        <HelpTip
                          text="The end of the range (exclusive). For example, 'd' with start 'a' returns values 'a', 'b', 'c' but not 'd'."
                        />
                      </span>
                    </div>
                    <input
                      type="text"
                      class="input input-bordered input-sm w-full"
                      placeholder="End value"
                      bind:value={scanEnd}
                    />
                  </label>
                  <button
                    class="btn btn-primary btn-sm"
                    onclick={scanCollectionIndex}
                    disabled={scanLoading}
                  >
                    {#if scanLoading}
                      <span class="loading loading-spinner loading-xs"></span>
                    {/if}
                    Scan
                  </button>
                </div>

                {#if scanResults !== null}
                  <div
                    class="overflow-x-auto rounded-lg border border-base-content/6"
                  >
                    <table class="table table-sm w-full">
                      <thead>
                        <tr>
                          <th>Value</th>
                          <th>Document IDs</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#if scanResults.entries.length === 0}
                          <tr>
                            <td
                              colspan="2"
                              class="py-6 text-center text-sm text-base-content/50"
                            >
                              No entries in range
                            </td>
                          </tr>
                        {:else}
                          {#each scanResults.entries as entry (entry.value)}
                            <tr class="hover">
                              <td class="font-mono text-sm">{entry.value}</td>
                              <td class="font-mono text-sm"
                                >{entry.keys.map(extractDocId).join(", ")}</td
                              >
                            </tr>
                          {/each}
                        {/if}
                      </tbody>
                    </table>
                  </div>
                {:else}
                  <p class="text-sm text-base-content/50">
                    No entries in range
                  </p>
                {/if}
              </div>
            {/if}
          </div>
        </div>
      {/if}
    {/if}
  </div>
</div>

<!-- Document Detail SlidePanel -->
<SlidePanel
  open={selectedDocument !== null}
  title="Document Detail"
  onclose={() => (selectedDocument = null)}
>
  {#if selectedDocument}
    <div class="space-y-5">
      <div>
        <span class="text-sm font-medium text-base-content/70 block mb-1"
          >Document ID</span
        >
        <div class="font-mono text-sm bg-base-200 rounded px-3 py-2">
          {selectedDocument.id}
        </div>
      </div>
      <div>
        <span class="text-sm font-medium text-base-content/70 block mb-1"
          >Document Body</span
        >
        <pre
          class="font-mono text-sm bg-base-200 rounded px-3 py-2 max-h-96 overflow-auto whitespace-pre-wrap"
          data-testid="document-detail-body">{JSON.stringify(
            selectedDocument.document,
            null,
            2,
          )}</pre>
      </div>
      {#if selectedCollection}
        <div>
          <span class="text-sm font-medium text-base-content/70 block mb-1"
            >Collection</span
          >
          <div class="text-sm bg-base-200 rounded px-3 py-2">
            {selectedCollection.name}
          </div>
        </div>
      {/if}
    </div>
  {/if}
  {#snippet footer()}
    {#if selectedDocument}
      <div class="flex gap-2">
        <button
          class="btn btn-sm btn-ghost flex-1"
          onclick={() => openEditDocumentPanel(selectedDocument!)}
        >
          <Pencil class="h-4 w-4" />
          Edit
        </button>
        <button
          class="btn btn-sm btn-error flex-1"
          onclick={() => requestDeleteDocument(selectedDocument!)}
        >
          <Trash2 class="h-4 w-4" />
          Delete
        </button>
      </div>
    {/if}
  {/snippet}
</SlidePanel>

<!-- Create Collection FormModal -->
<FormModal
  open={showCreateCollectionPanel}
  title="Create Collection"
  submitLabel="Create"
  loading={createLoading}
  onsubmit={handleCreateCollection}
  oncancel={() => (showCreateCollectionPanel = false)}
>
  {#snippet body()}
    <div class="space-y-5">
      <label class="form-control w-full">
        <div class="label"><span class="label-text">Name</span></div>
        <input
          type="text"
          class="input input-bordered w-full font-mono"
          placeholder="Enter collection name"
          bind:value={createName}
        />
      </label>

      <label class="form-control w-full">
        <div class="label"><span class="label-text">ID Strategy</span></div>
        <select
          class="select select-bordered w-full"
          bind:value={createIdStrategy}
        >
          <option value="unix_nano">unix_nano</option>
          <option value="increment">increment</option>
          <option value="field_hash">field_hash</option>
        </select>
        <div class="label">
          <span class="label-text-alt text-base-content/50">
            {#if createIdStrategy === "unix_nano"}
              IDs are generated from Unix nanosecond timestamps.
            {:else if createIdStrategy === "increment"}
              IDs are auto-incrementing integers.
            {:else if createIdStrategy === "field_hash"}
              IDs are derived from a hash of the specified field.
            {/if}
          </span>
        </div>
      </label>

      {#if createIdStrategy === "field_hash"}
        <label class="form-control w-full">
          <div class="label"><span class="label-text">Field Hash</span></div>
          <input
            type="text"
            class="input input-bordered w-full font-mono"
            placeholder="e.g. email, username"
            bind:value={createFieldHash}
          />
          <div class="label">
            <span class="label-text-alt text-base-content/50">
              The document field used to generate the ID hash.
            </span>
          </div>
        </label>
      {/if}
    </div>
  {/snippet}
</FormModal>

<!-- Insert/Edit Document FormModal -->
<FormModal
  open={showDocumentPanel}
  title={documentEditMode ? "Edit Document" : "Insert Document"}
  submitLabel={documentEditMode ? "Save" : "Insert"}
  loading={documentPanelLoading}
  onsubmit={handleSaveDocument}
  oncancel={() => (showDocumentPanel = false)}
>
  {#snippet body()}
    <div class="space-y-5">
      {#if documentEditMode && documentEditId !== null}
        <label class="form-control w-full">
          <div class="label"><span class="label-text">Document ID</span></div>
          <input
            type="text"
            class="input input-bordered w-full font-mono"
            value={String(documentEditId)}
            disabled
          />
          <div class="label">
            <span class="label-text-alt text-base-content/50"
              >ID cannot be changed</span
            >
          </div>
        </label>
      {/if}

      <div class="join mb-4">
        <button
          class="btn btn-xs join-item {documentFormMode
            ? 'btn-primary'
            : 'btn-ghost'}"
          onclick={() => {
            if (!documentFormMode) {
              try {
                documentFormFields = JSON.parse(documentJson);
              } catch {
                // Keep current form fields if JSON is invalid
              }
              documentFormMode = true;
            }
          }}
        >
          Form
        </button>
        <button
          class="btn btn-xs join-item {!documentFormMode
            ? 'btn-primary'
            : 'btn-ghost'}"
          onclick={() => {
            if (documentFormMode) {
              documentJson = JSON.stringify(documentFormFields, null, 2);
              documentFormMode = false;
            }
          }}
        >
          JSON
        </button>
      </div>

      <div class:hidden={!documentFormMode}>
        {#key documentPanelKey}
          <SchemaForm
            schema={selectedCollection?.schema ?? {}}
            value={documentFormFields}
            onchange={(fields) => {
              documentFormFields = fields;
              documentJson = JSON.stringify(fields, null, 2);
            }}
            disabled={documentPanelLoading}
          />
        {/key}
      </div>
      <div class:hidden={documentFormMode}>
        <label class="form-control w-full">
          <div class="label">
            <span class="label-text">Document Body (JSON)</span>
          </div>
          <textarea
            class="textarea textarea-bordered w-full font-mono h-64"
            placeholder={'{"key": "value"}'}
            bind:value={documentJson}
            oninput={() => {
              if (validateJson(documentJson)) {
                documentFormFields = JSON.parse(documentJson);
              }
            }}></textarea>
          {#if documentJsonError !== ""}
            <div class="label">
              <span class="label-text-alt text-error">{documentJsonError}</span>
            </div>
          {/if}
        </label>
      </div>
    </div>
  {/snippet}
</FormModal>

<!-- Create Index FormModal -->
<FormModal
  open={showCreateIndexModal}
  title="Create Index"
  submitLabel="Create"
  loading={createIndexLoading}
  onsubmit={() => void createCollectionIndex()}
  oncancel={() => (showCreateIndexModal = false)}
>
  {#snippet body()}
    <div class="space-y-4">
      {#if selectedCollection}
        <div
          class="rounded-lg bg-base-200 px-3 py-2 text-xs text-base-content/60 flex items-center gap-1.5"
        >
          Index name: <span class="font-mono font-medium"
            >{database.name}.{selectedCollection.name}.<em>fieldName</em
            >{newNumeric ? " (numeric)" : ""}</span
          >
          <HelpTip text={helpIndexName} position="right" />
        </div>
      {/if}
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text flex items-center gap-1">
            Field name
            <HelpTip text={helpFieldName} />
          </span>
        </div>
        <input
          type="text"
          class="input input-bordered w-full"
          placeholder="Field name"
          bind:value={newFieldName}
        />
      </label>
      <label class="form-control w-full">
        <div class="label">
          <span class="label-text">Value Type</span>
          <HelpTip
            text="String indexes use lexicographic ordering (alphabetical). Numeric indexes use integer ordering so range scans like [1, 222) correctly exclude 1234."
          />
        </div>
        <select class="select select-bordered w-full" bind:value={newNumeric}>
          <option value={false}>String (lexicographic)</option>
          <option value={true}>Numeric (integer ordering)</option>
        </select>
      </label>
      <div class="flex gap-6">
        <label class="label cursor-pointer gap-2">
          <span class="label-text flex items-center gap-1">
            Unique
            <HelpTip
              text="Enforces that no two documents can have the same value for this field. Use for fields like email addresses or usernames."
            />
          </span>
          <input
            type="checkbox"
            class="checkbox checkbox-sm"
            bind:checked={newUnique}
          />
        </label>
        <label class="label cursor-pointer gap-2">
          <span class="label-text flex items-center gap-1">
            Sparse
            <HelpTip
              text="Only indexes documents that contain this field. Documents missing the field are skipped. Useful when a field is optional and you only want to query documents that have it."
            />
          </span>
          <input
            type="checkbox"
            class="checkbox checkbox-sm"
            bind:checked={newSparse}
          />
        </label>
      </div>
    </div>
  {/snippet}
</FormModal>

<!-- Drop Collection Confirmation -->
<ConfirmModal
  open={dropTarget !== null}
  title="Drop Collection"
  message={`Are you sure you want to drop "${dropTarget?.name ?? ""}"? All documents will be permanently deleted. This action cannot be undone.`}
  confirmLabel="Drop"
  variant="error"
  onconfirm={confirmDropCollection}
  oncancel={() => (dropTarget = null)}
/>

<!-- Delete Document Confirmation -->
<ConfirmModal
  open={deleteDocumentTarget !== null}
  title="Delete Document"
  message={`Are you sure you want to delete document ${deleteDocumentTarget?.id ?? ""}? This action cannot be undone.`}
  confirmLabel="Delete"
  variant="error"
  onconfirm={confirmDeleteDocument}
  oncancel={() => (deleteDocumentTarget = null)}
/>

<!-- Drop Index Confirmation -->
<ConfirmModal
  open={dropIndexTarget !== null}
  title="Drop Index"
  message={`Are you sure you want to drop index "${dropIndexTarget ?? ""}"? This action cannot be undone.`}
  confirmLabel="Drop"
  variant="error"
  onconfirm={dropCollectionIndex}
  oncancel={() => (dropIndexTarget = null)}
/>
