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
    DistanceMetric,
    VectorIndexType,
    ListNamespacesResponse,
    ListVectorsResponse,
    SearchSimilarResponse,
    SearchByMetadataResponse,
    VectorData,
    BatchUpsertResponse,
    BatchDeleteVectorsResponse,
    HybridSearchResponse,
  } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import type { VectorSearchEvent } from "../../components/core/SearchBar/types.ts";
  import { showToast } from "../../stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import EmptyState from "../../components/EmptyState.svelte";
  import DataTable from "../../components/core/DataTable/DataTable.svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import TabBar from "../../components/core/TabBar.svelte";
  import StatCard from "../../components/core/StatCard.svelte";
  import SlidePanel from "../../components/core/SlidePanel.svelte";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import { VectorSearch } from "../../components/core/SearchBar";
  import {
    Hexagon, Plus, Search, RefreshCw, Trash2, Settings2,
    Wrench, BarChart3, Box, Ruler, Hash, Layers,
  } from "lucide-svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Feature availability
  // ---------------------------------------------------------------------------

  let available = $derived(client.vector !== null);

  // ---------------------------------------------------------------------------
  // Accent color
  // ---------------------------------------------------------------------------

  const accent = "var(--color-vectors)";

  // ---------------------------------------------------------------------------
  // Tab management
  // ---------------------------------------------------------------------------

  type Tab = "browse" | "search" | "maintenance";

  let activeTab: Tab = $state("browse");

  const tabs = [
    { id: "browse", label: "Browse", icon: Layers },
    { id: "search", label: "Search", icon: Search },
    { id: "maintenance", label: "Maintenance", icon: Wrench },
  ];

  // ---------------------------------------------------------------------------
  // Shared state
  // ---------------------------------------------------------------------------

  let loading: boolean = $state(false);
  let selectedNamespace: string = $state("");
  let availableNamespaces: string[] = $state([]);
  let nsList: unknown[] = $state([]);

  // ---------------------------------------------------------------------------
  // Namespace create modal state
  // ---------------------------------------------------------------------------

  let showCreateNsModal: boolean = $state(false);
  let nsCreateName: string = $state("");
  let nsCreateDimensions: string = $state("128");
  /** DistanceMetric: 0=Cosine, 1=Euclidean, 2=DotProduct */
  let nsCreateMetric: DistanceMetric = $state(0 as DistanceMetric);

  // ---------------------------------------------------------------------------
  // Namespace info state
  // ---------------------------------------------------------------------------

  let nsInfoResult: unknown = $state(null);

  // ---------------------------------------------------------------------------
  // Vectors state
  // ---------------------------------------------------------------------------

  let vecId: string = $state("");
  let vecValues: string = $state("");
  let vecMetadata: string = $state("{}");
  let vecListResults: unknown[] = $state([]);
  let vectorsLoading: boolean = $state(false);
  let showUpsertPanel: boolean = $state(false);
  let showVectorDetailPanel: boolean = $state(false);
  let selectedVector: unknown = $state(null);
  /** Authoritative copy of the selected vector, fetched with getVector. */
  let detailVector: VectorData | null = $state(null);
  let detailMetadata: string = $state("{}");
  let detailLoading: boolean = $state(false);
  let detailError: string = $state("");
  let showBatchUpsertPanel: boolean = $state(false);
  let batchUpsertInput: string = $state('[\n  {"id": "v1", "vector": [0.1, 0.2, 0.3], "metadata": {}}\n]');
  let batchUpsertResult: BatchUpsertResponse | null = $state(null);

  // ---------------------------------------------------------------------------
  // Search state
  // ---------------------------------------------------------------------------

  let searchResults: unknown[] = $state([]);
  let hybridVector: string = $state("");
  let hybridFilters: string = $state("{}");
  let hybridK: string = $state("10");
  let hybridVectorWeight: string = $state("0.7");
  let hybridMetadataWeight: string = $state("0.3");

  // ---------------------------------------------------------------------------
  // Maintenance state
  // ---------------------------------------------------------------------------

  let rebuildJobId: string = $state("");
  let rebuildProgress: unknown = $state(null);
  let statsResult: unknown = $state(null);
  let compactJobId: string = $state("");
  let compactProgress: unknown = $state(null);

  // ---------------------------------------------------------------------------
  // Delete confirmation state
  // ---------------------------------------------------------------------------

  let showDeleteNsConfirm: boolean = $state(false);
  let showDeleteVecConfirm: boolean = $state(false);
  let deleteVecId: string = $state("");

  // ---------------------------------------------------------------------------
  // DataTable Columns
  // ---------------------------------------------------------------------------

  interface NamespaceRow {
    index: number;
    namespace: string;
    raw: unknown;
  }

  const namespaceColumns: Column<NamespaceRow>[] = [
    {
      id: "index",
      header: "#",
      accessor: "index",
      sortable: true,
      width: "60px",
      align: "center",
    },
    {
      id: "namespace",
      header: "Namespace",
      accessor: "namespace",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
  ];

  interface VectorRow {
    index: number;
    id: string;
    dimensions: number;
    metadata: string;
    raw: unknown;
  }

  const vectorColumns: Column<VectorRow>[] = [
    {
      id: "index",
      header: "#",
      accessor: "index",
      sortable: true,
      width: "60px",
      align: "center",
    },
    {
      id: "id",
      header: "ID",
      accessor: "id",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "dimensions",
      header: "Dimensions",
      accessor: "dimensions",
      sortable: true,
      align: "right",
    },
    {
      id: "metadata",
      header: "Metadata",
      accessor: "metadata",
      sortable: false,
    },
  ];

  interface SearchResultRow {
    index: number;
    id: string;
    score: number;
    metadata: string;
    raw: unknown;
  }

  const searchResultColumns: Column<SearchResultRow>[] = [
    {
      id: "index",
      header: "#",
      accessor: "index",
      sortable: true,
      width: "60px",
      align: "center",
    },
    {
      id: "id",
      header: "ID",
      accessor: "id",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "score",
      header: "Score",
      accessor: "score",
      sortable: true,
      align: "right",
    },
    {
      id: "metadata",
      header: "Metadata",
      accessor: "metadata",
      sortable: false,
    },
  ];

  // ---------------------------------------------------------------------------
  // Derived data for DataTable
  // ---------------------------------------------------------------------------

  const nsTableData = $derived(
    nsList.map((ns, index) => ({
      index: index + 1,
      namespace: typeof ns === "string" ? ns : (ns as Record<string, unknown>).namespace as string ?? JSON.stringify(ns),
      raw: ns,
    }))
  );

  const vectorTableData = $derived(
    vecListResults.map((vec, index) => {
      const v = vec as Record<string, unknown>;
      const vector = v.vector as number[] | undefined;
      return {
        index: index + 1,
        id: (v.id as string) ?? "",
        dimensions: vector?.length ?? 0,
        metadata: JSON.stringify(v.metadata ?? {}),
        raw: vec,
      };
    })
  );

  const searchTableData = $derived(
    searchResults.map((result, index) => {
      const r = result as Record<string, unknown>;
      return {
        index: index + 1,
        id: (r.id as string) ?? "",
        score: (r.score as number) ?? (r.distance as number) ?? 0,
        metadata: JSON.stringify(r.metadata ?? {}),
        raw: result,
      };
    })
  );

  // ---------------------------------------------------------------------------
  // Derived: stats parsed into structured fields
  // ---------------------------------------------------------------------------

  const parsedStats = $derived.by(() => {
    if (statsResult === null) return null;
    const s = statsResult as Record<string, unknown>;
    return {
      totalVectors: (s.totalVectors as number) ?? (s.total_vectors as number) ?? 0,
      dimensions: (s.dimensions as number) ?? (s.dimension as number) ?? 0,
      indexType: (s.indexType as string) ?? (s.index_type as string) ?? "unknown",
      distanceMetric: (s.distanceMetric as string) ?? (s.distance_metric as string) ?? "unknown",
      lastRebuilt: (s.lastRebuilt as string) ?? (s.last_rebuilt as string) ?? "N/A",
    };
  });

  const parsedNsInfo = $derived.by(() => {
    if (nsInfoResult === null) return null;
    const n = nsInfoResult as Record<string, unknown>;
    return {
      dimension: (n.dimension as number) ?? (n.dimensions as number) ?? 0,
      distanceMetric: (n.distanceMetric as string) ?? (n.distance_metric as string) ?? "unknown",
      indexType: (n.indexType as string) ?? (n.index_type as string) ?? "unknown",
      vectorCount: (n.vectorCount as number) ?? (n.vector_count as number) ?? 0,
    };
  });

  // ---------------------------------------------------------------------------
  // Namespace actions
  // ---------------------------------------------------------------------------

  async function handleNsCreate(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (nsCreateName.trim() === "") {
      showToast("warning", "Namespace name is required.");
      return;
    }
    loading = true;
    try {
      const dims = parseInt(nsCreateDimensions, 10);
      if (Number.isNaN(dims) || dims <= 0) {
        showToast("warning", "Dimensions must be a positive integer.");
        return;
      }
      await client.vector.createNamespace({
        namespace: nsCreateName,
        dimension: dims,
        distanceMetric: nsCreateMetric,
        indexType: 0 as VectorIndexType,
      });
      showToast("success", `Namespace "${nsCreateName}" created.`);
      nsCreateName = "";
      nsCreateDimensions = "128";
      nsCreateMetric = 0 as DistanceMetric;
      showCreateNsModal = false;
      await handleNsList();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleNsDelete(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "No namespace selected.");
      return;
    }
    loading = true;
    try {
      await client.vector.deleteNamespace({ namespace: selectedNamespace });
      showToast("success", `Namespace "${selectedNamespace}" deleted.`);
      selectedNamespace = "";
      nsInfoResult = null;
      vecListResults = [];
      await handleNsList();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
      showDeleteNsConfirm = false;
    }
  }

  async function handleNsList(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    loading = true;
    try {
      const resp: ListNamespacesResponse = await client.vector.listNamespaces({});
      nsList = [...resp.namespaces];
      availableNamespaces = resp.namespaces.map((ns) =>
        typeof ns === "string" ? ns : (ns as Record<string, unknown>).namespace as string
      );
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleNsInfo(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") return;
    loading = true;
    try {
      nsInfoResult = await client.vector.getNamespaceInfo({ namespace: selectedNamespace });
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Vector actions
  // ---------------------------------------------------------------------------

  function parseVectorValues(raw: string): number[] {
    return raw
      .split(",")
      .map((v) => parseFloat(v.trim()))
      .filter((v) => !Number.isNaN(v));
  }

  function parseJson(raw: string): Record<string, unknown> {
    try {
      const parsed = JSON.parse(raw);
      if (typeof parsed === "object" && parsed !== null) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // fall through
    }
    return {};
  }

  async function handleVecUpsert(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "" || vecId.trim() === "" || vecValues.trim() === "") {
      showToast("warning", "Namespace, vector ID, and values are required.");
      return;
    }
    loading = true;
    try {
      const values = parseVectorValues(vecValues);
      if (values.length === 0) {
        showToast("warning", "Values must be a comma-separated list of numbers.");
        return;
      }
      const metadata = parseJson(vecMetadata);
      await client.vector.upsertVector({
        namespace: selectedNamespace,
        id: vecId,
        vector: values,
        metadata,
      });
      showToast("success", `Vector "${vecId}" upserted in namespace "${selectedNamespace}".`);
      vecId = "";
      vecValues = "";
      vecMetadata = "{}";
      showUpsertPanel = false;
      await handleVecList();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleVecDelete(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "" || deleteVecId.trim() === "") {
      showToast("warning", "Namespace and vector ID are required.");
      return;
    }
    loading = true;
    try {
      await client.vector.deleteVector({ namespace: selectedNamespace, id: deleteVecId });
      showToast("success", `Vector "${deleteVecId}" deleted from namespace "${selectedNamespace}".`);
      deleteVecId = "";
      showDeleteVecConfirm = false;
      showVectorDetailPanel = false;
      selectedVector = null;
      await handleVecList();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleVecList(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") return;
    vectorsLoading = true;
    try {
      const resp: ListVectorsResponse = await client.vector.listVectors({ namespace: selectedNamespace });
      vecListResults = [...resp.vectors];
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      vectorsLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Bulk delete handler for CrudTable
  // ---------------------------------------------------------------------------

  async function handleBulkDeleteVectors(indices: Set<number>): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace === "") return;
    const ids: string[] = [];
    for (const idx of indices) {
      const row = vectorTableData[idx];
      if (row) ids.push(row.id);
    }
    if (ids.length === 0) return;
    vectorsLoading = true;
    try {
      const resp: BatchDeleteVectorsResponse = await client.vector.batchDeleteVectors({
        namespace: selectedNamespace,
        ids,
      });
      if (resp.failed > 0) {
        const detail = (resp.errors ?? []).map((e) => `${e.id}: ${e.error}`).join("; ");
        showToast("warning", `Deleted ${resp.deleted}, failed ${resp.failed}${detail ? ` (${detail})` : ""}`);
      } else {
        showToast("success", `Deleted ${resp.deleted} vector${resp.deleted !== 1 ? "s" : ""}`);
      }
      await handleVecList();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      vectorsLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Batch upsert
  // ---------------------------------------------------------------------------

  function parseBatchVectors(raw: string): VectorData[] | null {
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      showToast("warning", "Batch must be a JSON array of {id, vector, metadata?} objects.");
      return null;
    }
    if (!Array.isArray(parsed) || parsed.length === 0) {
      showToast("warning", "Batch must be a non-empty JSON array of {id, vector, metadata?} objects.");
      return null;
    }
    const out: VectorData[] = [];
    for (const [i, item] of parsed.entries()) {
      const o = item as Record<string, unknown> | null;
      const vec = o?.vector;
      if (
        o === null || typeof o !== "object" || typeof o.id !== "string" || o.id === "" ||
        !Array.isArray(vec) || vec.length === 0 || !vec.every((n) => typeof n === "number")
      ) {
        showToast("warning", `Batch item ${i + 1} needs a string id and a numeric vector array.`);
        return null;
      }
      const md = o.metadata;
      if (md !== undefined && (typeof md !== "object" || md === null || Array.isArray(md))) {
        showToast("warning", `Batch item ${i + 1} metadata must be a JSON object.`);
        return null;
      }
      out.push({
        id: o.id,
        vector: vec as number[],
        metadata: md as Record<string, unknown> | undefined,
        version: 0,
        createdAt: 0,
        updatedAt: 0,
      });
    }
    return out;
  }

  async function handleBatchUpsert(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "Select a namespace first.");
      return;
    }
    const vectors = parseBatchVectors(batchUpsertInput);
    if (vectors === null) return;
    loading = true;
    batchUpsertResult = null;
    try {
      const resp: BatchUpsertResponse = await client.vector.batchUpsert({
        namespace: selectedNamespace,
        vectors,
      });
      batchUpsertResult = resp;
      if (resp.failed > 0) {
        showToast("warning", `Batch upsert: ${resp.succeeded} succeeded, ${resp.failed} failed.`);
      } else {
        showToast("success", `Batch upsert: ${resp.succeeded} succeeded.`);
      }
      await handleVecList();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function openBatchUpsertPanel(): void {
    batchUpsertResult = null;
    showBatchUpsertPanel = true;
  }

  // ---------------------------------------------------------------------------
  // Search actions
  // ---------------------------------------------------------------------------

  async function handleSearch(event: VectorSearchEvent): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    const { method, options } = event;

    loading = true;
    try {
      switch (method) {
        case "vector": {
          if (!options.vector || options.vector.length === 0) {
            showToast("warning", "Vector values are required for similarity search.");
            return;
          }
          const resp: SearchSimilarResponse = await client.vector.searchSimilar({
            namespace: options.namespace,
            vector: options.vector,
            k: options.topK ?? 10,
          });
          searchResults = [...resp.results];
          break;
        }
        case "id": {
          if (!options.vectorId) {
            showToast("warning", "Vector ID is required for ID-based search.");
            return;
          }
          const resp: SearchSimilarResponse = await client.vector.searchByID({
            namespace: options.namespace,
            id: options.vectorId,
            k: options.topK ?? 10,
          });
          searchResults = [...resp.results];
          break;
        }
        case "metadata": {
          if (!options.metadataFilter) {
            showToast("warning", "Metadata filter is required.");
            return;
          }
          const resp: SearchByMetadataResponse = await client.vector.searchByMetadata({
            namespace: options.namespace,
            filters: options.metadataFilter,
            limit: options.topK ?? 10,
          });
          searchResults = [...resp.results];
          break;
        }
      }

      if (searchResults.length === 0) {
        showToast("info", "No matching vectors found.");
      }
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function clearSearchResults(): void {
    searchResults = [];
  }

  async function handleHybridSearch(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "Select a namespace first.");
      return;
    }
    const vector = parseVectorValues(hybridVector);
    if (vector.length === 0) {
      showToast("warning", "Vector values are required for hybrid search.");
      return;
    }
    const metadataFilters = parseJsonObjectStrict(hybridFilters, "Metadata filter");
    if (metadataFilters === null) return;
    const k = Number(hybridK);
    const vectorWeight = Number(hybridVectorWeight);
    const metadataWeight = Number(hybridMetadataWeight);
    if (!Number.isInteger(k) || k <= 0) {
      showToast("warning", "K must be a positive integer.");
      return;
    }
    if (
      !Number.isFinite(vectorWeight) || vectorWeight < 0 || vectorWeight > 1 ||
      !Number.isFinite(metadataWeight) || metadataWeight < 0 || metadataWeight > 1
    ) {
      showToast("warning", "Weights must be numbers between 0 and 1.");
      return;
    }
    loading = true;
    try {
      const resp: HybridSearchResponse = await client.vector.hybridSearch({
        namespace: selectedNamespace,
        vector,
        metadataFilters,
        k,
        vectorWeight,
        metadataWeight,
      });
      searchResults = [...resp.results];
      if (searchResults.length === 0) {
        showToast("info", "No matching vectors found.");
      }
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Maintenance actions
  // ---------------------------------------------------------------------------

  async function handleRebuildIndex(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "Select a namespace first.");
      return;
    }
    loading = true;
    try {
      const result = await client.vector.rebuildIndex({ namespace: selectedNamespace });
      rebuildJobId = result.jobId;
      showToast("success", `Index rebuild started for namespace "${selectedNamespace}" (job: ${result.jobId}).`);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleGetRebuildProgress(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "Select a namespace first.");
      return;
    }
    loading = true;
    try {
      rebuildProgress = await client.vector.getRebuildProgress({
        namespace: selectedNamespace,
        jobId: rebuildJobId,
      });
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleGetStats(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "Select a namespace first.");
      return;
    }
    loading = true;
    try {
      statsResult = await client.vector.getIndexStats({ namespace: selectedNamespace });
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleCompact(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "Select a namespace first.");
      return;
    }
    loading = true;
    try {
      const result = await client.vector.compactNamespace({ namespace: selectedNamespace });
      compactJobId = result.jobId;
      showToast("success", `Compaction started for namespace "${selectedNamespace}" (job: ${result.jobId}).`);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function handleGetCompactionProgress(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    if (selectedNamespace.trim() === "") {
      showToast("warning", "Select a namespace first.");
      return;
    }
    loading = true;
    try {
      compactProgress = await client.vector.getCompactionProgress({
        namespace: selectedNamespace,
        jobId: compactJobId,
      });
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Row click handlers
  // ---------------------------------------------------------------------------

  function handleNamespaceRowClick(event: RowClickEvent<NamespaceRow>): void {
    selectedNamespace = event.row.namespace;
  }

  function handleVectorRowClick(event: RowClickEvent<VectorRow>): void {
    selectedVector = event.row.raw;
    detailVector = null;
    detailMetadata = JSON.stringify(
      ((event.row.raw as Record<string, unknown>).metadata as Record<string, unknown> | undefined) ?? {},
      null,
      2,
    );
    showVectorDetailPanel = true;
    void loadVectorDetail(event.row.id);
  }

  function closeVectorDetail(): void {
    showVectorDetailPanel = false;
    selectedVector = null;
    detailVector = null;
    detailError = "";
  }

  // ---------------------------------------------------------------------------
  // Vector detail: getVector + updateMetadata
  // ---------------------------------------------------------------------------

  async function loadVectorDetail(id: string): Promise<void> {
    if (client.vector === null || selectedNamespace.trim() === "" || id === "") return;
    detailLoading = true;
    detailError = "";
    try {
      const v: VectorData = await client.vector.getVector({ namespace: selectedNamespace, id });
      detailVector = v;
      detailMetadata = JSON.stringify(v.metadata ?? {}, null, 2);
    } catch (err: unknown) {
      detailVector = null;
      detailError = err instanceof Error ? err.message : String(err);
    } finally {
      detailLoading = false;
    }
  }

  function parseJsonObjectStrict(raw: string, what: string): Record<string, unknown> | null {
    try {
      const parsed: unknown = JSON.parse(raw);
      if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // fall through
    }
    showToast("warning", `${what} must be a JSON object.`);
    return null;
  }

  async function handleSaveMetadata(): Promise<void> {
    if (client.vector === null) {
      showToast("error", "Vector operations are not available on this connection.");
      return;
    }
    const id = detailVector?.id ?? ((selectedVector as Record<string, unknown> | null)?.id as string | undefined) ?? "";
    if (selectedNamespace.trim() === "" || id === "") {
      showToast("warning", "Namespace and vector ID are required.");
      return;
    }
    const metadata = parseJsonObjectStrict(detailMetadata, "Metadata");
    if (metadata === null) return;
    detailLoading = true;
    try {
      await client.vector.updateMetadata({
        namespace: selectedNamespace,
        id,
        metadata,
      });
      showToast("success", `Metadata of vector "${id}" updated.`);
      await loadVectorDetail(id);
      await handleVecList();
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      detailLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Upsert panel helpers
  // ---------------------------------------------------------------------------

  function openUpsertPanel(): void {
    showUpsertPanel = true;
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  $effect(() => {
    if (available) {
      handleNsList();
    }
  });

  $effect(() => {
    if (selectedNamespace.trim() !== "") {
      handleNsInfo();
      handleVecList();
    } else {
      nsInfoResult = null;
      vecListResults = [];
    }
  });
</script>

{#if !available}
  <div class="flex h-full items-center justify-center">
    <div class="card bg-base-200 shadow-sm w-96">
      <div class="card-body items-center text-center">
        <Hexagon class="text-base-content/30 h-12 w-12" strokeWidth={1.5} />
        <h2 class="card-title">Feature Not Available</h2>
        <p class="text-base-content/50 text-sm">
          Vector database support is not enabled on the connected QRDB server.
        </p>
      </div>
    </div>
  </div>
{:else}
  <PageHeader
    title="Vector Store"
    description="Manage namespaces, vectors, and perform similarity searches"
    accentColor={accent}
  >
    {#snippet actions()}
      <button class="btn btn-ghost btn-sm gap-2" onclick={handleNsList} disabled={loading}>
        {#if loading}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <RefreshCw class="h-4 w-4" />
        {/if}
        Refresh
      </button>
    {/snippet}
  </PageHeader>

  <!-- Shared Namespace Selector -->
  <div class="mb-4 flex items-end gap-2">
    <label class="form-control flex-1 max-w-xs">
      <div class="label"><span class="label-text font-medium">Namespace</span></div>
      <div class="flex items-center gap-2">
        <select
          class="select select-bordered select-sm flex-1"
          bind:value={selectedNamespace}
          disabled={loading}
        >
          <option value="">Select a namespace...</option>
          {#each availableNamespaces as ns}
            <option value={ns}>{ns}</option>
          {/each}
        </select>
        <button
          class="btn btn-primary btn-sm btn-square"
          onclick={() => (showCreateNsModal = true)}
          aria-label="Create namespace"
        >
          <Plus class="h-4 w-4" />
        </button>
        {#if selectedNamespace}
          <button
            class="btn btn-error btn-sm btn-square btn-outline"
            onclick={() => (showDeleteNsConfirm = true)}
            aria-label="Delete namespace"
          >
            <Trash2 class="h-4 w-4" />
          </button>
        {/if}
      </div>
    </label>
  </div>

  <!-- Tab Navigation -->
  <div class="mb-4">
    <TabBar
      {tabs}
      {activeTab}
      accentColor={accent}
      onchange={(id) => (activeTab = id as Tab)}
    />
  </div>

  <!-- =================================================================== -->
  <!-- Browse Tab                                                           -->
  <!-- =================================================================== -->
  {#if activeTab === "browse"}
    {#if !selectedNamespace}
      <!-- No namespace selected: show namespace list -->
      <div class="surface-panel engine-vectors flex flex-col rounded-xl">
        <div class="border-base-content/10 flex items-center justify-between border-b px-4 py-3">
          <h3 class="text-sm font-semibold">Namespaces</h3>
          <span class="badge badge-sm badge-ghost">{nsList.length}</span>
        </div>
        <div class="flex-1 overflow-hidden">
          {#if nsList.length === 0}
            <EmptyState
              icon={Hexagon}
              title="No namespaces found"
              description="Create a namespace to get started."
              actionLabel="Create Namespace"
              onaction={() => (showCreateNsModal = true)}
            />
          {:else}
            <DataTable
              data={nsTableData}
              columns={namespaceColumns}
              {loading}
              emptyMessage="No namespaces found"
              compact={true}
              striped={true}
              pageSize={25}
              onrowclick={handleNamespaceRowClick}
            />
          {/if}
        </div>
      </div>
    {:else}
      <!-- Namespace selected: show info + vector list -->
      {#if parsedNsInfo}
        <div class="mb-4 grid grid-cols-2 gap-6 sm:grid-cols-4">
          <StatCard label="Vectors" value={parsedNsInfo.vectorCount} icon={Hash} accentColor={accent} />
          <StatCard label="Dimensions" value={parsedNsInfo.dimension} icon={Ruler} accentColor={accent} />
          <StatCard label="Distance Metric" value={parsedNsInfo.distanceMetric} icon={BarChart3} accentColor={accent} />
          <StatCard label="Index Type" value={parsedNsInfo.indexType} icon={Box} accentColor={accent} />
        </div>
      {/if}

      <div class="mb-2 flex justify-end">
        <button class="btn btn-outline btn-sm gap-1" onclick={openBatchUpsertPanel} disabled={loading}>
          <Plus class="h-3.5 w-3.5" />
          Batch upsert
        </button>
      </div>

      <CrudTable
        title="Vectors"
        data={vectorTableData}
        columns={vectorColumns}
        loading={vectorsLoading}
        showCreate={true}
        createLabel="Upsert Vector"
        oncreateclick={openUpsertPanel}
        showDelete={true}
        deleteLabel="Delete"
        ondeleteselected={handleBulkDeleteVectors}
        onrowclick={handleVectorRowClick}
        pageSize={10}
        emptyMessage="No vectors found. Upsert a vector to get started."
      />
    {/if}
  {/if}

  <!-- =================================================================== -->
  <!-- Search Tab                                                           -->
  <!-- =================================================================== -->
  {#if activeTab === "search"}
    <div class="flex flex-col gap-6">
      <div class="surface-panel engine-vectors rounded-xl p-5">
        <VectorSearch
          {loading}
          {availableNamespaces}
          onSearch={handleSearch}
          onClear={clearSearchResults}
        />
      </div>

      <div class="card bg-base-200 engine-vectors shadow-sm" data-testid="hybrid-search-card">
        <div class="card-body p-5">
          <h3 class="card-title text-base font-semibold gap-2">
            <Search class="h-4 w-4" />
            Hybrid
          </h3>
          <p class="text-base-content/50 text-xs">
            Combine vector similarity with a metadata match in namespace
            <span class="font-semibold">{selectedNamespace || "(none selected)"}</span>.
          </p>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="form-control w-full">
              <div class="label"><span class="label-text">Vector (comma-separated floats)</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full font-mono"
                aria-label="Hybrid vector"
                placeholder="0.1, 0.5, -0.3"
                bind:value={hybridVector}
              />
            </label>
            <label class="form-control w-full">
              <div class="label"><span class="label-text">Metadata filter (JSON)</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full font-mono"
                aria-label="Hybrid metadata filter"
                placeholder={'{"label": "example"}'}
                bind:value={hybridFilters}
              />
            </label>
            <label class="form-control w-full">
              <div class="label"><span class="label-text">K</span></div>
              <input type="number" min="1" class="input input-bordered input-sm w-full" aria-label="Hybrid k" bind:value={hybridK} />
            </label>
            <div class="grid grid-cols-2 gap-3">
              <label class="form-control w-full">
                <div class="label"><span class="label-text">Vector weight</span></div>
                <input type="number" min="0" max="1" step="0.1" class="input input-bordered input-sm w-full" aria-label="Hybrid vector weight" bind:value={hybridVectorWeight} />
              </label>
              <label class="form-control w-full">
                <div class="label"><span class="label-text">Metadata weight</span></div>
                <input type="number" min="0" max="1" step="0.1" class="input input-bordered input-sm w-full" aria-label="Hybrid metadata weight" bind:value={hybridMetadataWeight} />
              </label>
            </div>
          </div>
          <div class="mt-2 flex justify-end">
            <button class="btn btn-primary btn-sm" onclick={handleHybridSearch} disabled={loading}>
              Hybrid search
            </button>
          </div>
        </div>
      </div>

      <CrudTable
        title="Search Results"
        data={searchTableData}
        columns={searchResultColumns}
        {loading}
        showCreate={false}
        showDelete={false}
        pageSize={25}
        emptyMessage="No search results. Use the search form above to find similar vectors."
      />
    </div>
  {/if}

  <!-- =================================================================== -->
  <!-- Maintenance Tab                                                      -->
  <!-- =================================================================== -->
  {#if activeTab === "maintenance"}
    <div class="flex flex-col gap-6">
      {#if !selectedNamespace}
        <EmptyState
          icon={Settings2}
          title="No namespace selected"
          description="Select a namespace from the dropdown above to perform maintenance operations."
        />
      {:else}
        <!-- Operations -->
        <div class="grid grid-cols-1 gap-6 sm:grid-cols-2">
          <!-- Rebuild Index -->
          <div class="card bg-base-200 engine-vectors shadow-sm">
            <div class="card-body p-5">
              <h3 class="card-title text-base font-semibold gap-2">
                <RefreshCw class="h-4 w-4" />
                Rebuild Index
              </h3>
              <div class="flex gap-2">
                <button class="btn btn-warning btn-sm" onclick={handleRebuildIndex} disabled={loading}>
                  {#if loading}
                    <span class="loading loading-spinner loading-xs"></span>
                  {/if}
                  Rebuild
                </button>
                {#if rebuildJobId}
                  <button class="btn btn-outline btn-sm" onclick={handleGetRebuildProgress} disabled={loading}>
                    Get Progress
                  </button>
                {/if}
              </div>
              {#if rebuildProgress !== null}
                <details class="mt-2">
                  <summary class="text-base-content/60 cursor-pointer text-xs font-medium">
                    Rebuild Progress (raw)
                  </summary>
                  <div class="bg-base-300 mt-1 rounded p-3">
                    <pre class="font-mono text-xs whitespace-pre-wrap overflow-x-auto">{JSON.stringify(rebuildProgress, null, 2)}</pre>
                  </div>
                </details>
              {/if}
            </div>
          </div>

          <!-- Compact -->
          <div class="card bg-base-200 engine-vectors shadow-sm">
            <div class="card-body p-5">
              <h3 class="card-title text-base font-semibold gap-2">
                <Wrench class="h-4 w-4" />
                Compaction
              </h3>
              <div class="flex gap-2">
                <button class="btn btn-warning btn-sm" onclick={handleCompact} disabled={loading}>
                  {#if loading}
                    <span class="loading loading-spinner loading-xs"></span>
                  {/if}
                  Compact
                </button>
                {#if compactJobId}
                  <button class="btn btn-outline btn-sm" onclick={handleGetCompactionProgress} disabled={loading}>
                    Get Progress
                  </button>
                {/if}
              </div>
              {#if compactProgress !== null}
                <details class="mt-2">
                  <summary class="text-base-content/60 cursor-pointer text-xs font-medium">
                    Compaction Progress (raw)
                  </summary>
                  <div class="bg-base-300 mt-1 rounded p-3">
                    <pre class="font-mono text-xs whitespace-pre-wrap overflow-x-auto">{JSON.stringify(compactProgress, null, 2)}</pre>
                  </div>
                </details>
              {/if}
            </div>
          </div>
        </div>

        <!-- Statistics -->
        <div class="card bg-base-200 engine-vectors shadow-sm">
          <div class="card-body p-5">
            <div class="flex items-center justify-between">
              <h3 class="card-title text-base font-semibold gap-2">
                <BarChart3 class="h-4 w-4" />
                Statistics
              </h3>
              <button class="btn btn-secondary btn-sm gap-2" onclick={handleGetStats} disabled={loading}>
                {#if loading}
                  <span class="loading loading-spinner loading-xs"></span>
                {:else}
                  <RefreshCw class="h-3.5 w-3.5" />
                {/if}
                Load Stats
              </button>
            </div>

            {#if parsedStats}
              <div class="mt-3 grid grid-cols-2 gap-6 sm:grid-cols-3 lg:grid-cols-5">
                <StatCard label="Total Vectors" value={parsedStats.totalVectors} icon={Hash} accentColor={accent} />
                <StatCard label="Dimensions" value={parsedStats.dimensions} icon={Ruler} accentColor={accent} />
                <StatCard label="Index Type" value={parsedStats.indexType} icon={Box} accentColor={accent} />
                <StatCard label="Distance Metric" value={parsedStats.distanceMetric} icon={BarChart3} accentColor={accent} />
                <StatCard label="Last Rebuilt" value={parsedStats.lastRebuilt} icon={RefreshCw} accentColor={accent} />
              </div>

              <details class="mt-3">
                <summary class="text-base-content/60 cursor-pointer text-xs font-medium">
                  Raw JSON
                </summary>
                <div class="bg-base-300 mt-1 rounded p-3">
                  <pre class="font-mono text-xs whitespace-pre-wrap overflow-x-auto">{JSON.stringify(statsResult, null, 2)}</pre>
                </div>
              </details>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  {/if}

  <!-- =================================================================== -->
  <!-- Upsert Vector SlidePanel                                             -->
  <!-- =================================================================== -->
  <SlidePanel
    open={showUpsertPanel}
    title="New Vector"
    onclose={() => (showUpsertPanel = false)}
  >
    {#snippet children()}
      <div class="flex flex-col gap-5">
        <div class="surface-panel rounded-lg p-4">
          <div class="text-base-content/50 flex items-center gap-2 text-xs font-medium">
            <Layers class="h-4 w-4" />
            Namespace: <span class="text-base-content font-semibold">{selectedNamespace}</span>
          </div>
        </div>

        <label class="form-control w-full">
          <div class="label"><span class="label-text">Vector ID</span></div>
          <input
            type="text"
            class="input input-bordered input-sm w-full"
            placeholder="Unique ID"
            bind:value={vecId}
          />
        </label>

        <label class="form-control w-full">
          <div class="label"><span class="label-text">Values (comma-separated floats)</span></div>
          <textarea
            class="textarea textarea-bordered h-24 w-full font-mono text-sm"
            placeholder="0.1, 0.5, -0.3, 0.8, ..."
            bind:value={vecValues}
          ></textarea>
        </label>

        <label class="form-control w-full">
          <div class="label"><span class="label-text">Metadata (JSON)</span></div>
          <textarea
            class="textarea textarea-bordered h-20 w-full font-mono text-sm"
            placeholder={'{"label": "example"}'}
            bind:value={vecMetadata}
          ></textarea>
        </label>
      </div>
    {/snippet}
    {#snippet footer()}
      <div class="flex gap-2 justify-end">
        <button class="btn btn-ghost btn-sm" onclick={() => (showUpsertPanel = false)}>Cancel</button>
        <button class="btn btn-primary btn-sm" onclick={handleVecUpsert} disabled={loading}>
          {#if loading}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
          Upsert
        </button>
      </div>
    {/snippet}
  </SlidePanel>

  <!-- =================================================================== -->
  <!-- Batch Upsert SlidePanel                                              -->
  <!-- =================================================================== -->
  <SlidePanel
    open={showBatchUpsertPanel}
    title="Batch Upsert"
    onclose={() => (showBatchUpsertPanel = false)}
  >
    {#snippet children()}
      <div class="flex flex-col gap-5">
        <div class="surface-panel rounded-lg p-4">
          <div class="text-base-content/50 flex items-center gap-2 text-xs font-medium">
            <Layers class="h-4 w-4" />
            Namespace: <span class="text-base-content font-semibold">{selectedNamespace}</span>
          </div>
        </div>

        <label class="form-control w-full">
          <div class="label"><span class="label-text">Vectors (JSON array of {"{id, vector, metadata?}"})</span></div>
          <textarea
            class="textarea textarea-bordered h-48 w-full font-mono text-sm"
            aria-label="Batch vectors JSON"
            data-testid="batch-upsert-input"
            bind:value={batchUpsertInput}
          ></textarea>
        </label>

        {#if batchUpsertResult}
          <div class="surface-panel rounded-lg p-4 text-sm" data-testid="batch-upsert-result">
            <div>Succeeded: <span class="font-semibold" data-testid="batch-upsert-succeeded">{batchUpsertResult.succeeded}</span></div>
            <div>Failed: <span class="font-semibold" data-testid="batch-upsert-failed">{batchUpsertResult.failed}</span></div>
            {#if batchUpsertResult.errors && batchUpsertResult.errors.length > 0}
              <ul class="text-error mt-2 list-disc pl-5" data-testid="batch-upsert-errors">
                {#each batchUpsertResult.errors as e}
                  <li>{e.id}: {e.error}</li>
                {/each}
              </ul>
            {/if}
          </div>
        {/if}
      </div>
    {/snippet}
    {#snippet footer()}
      <div class="flex gap-2 justify-end">
        <button class="btn btn-ghost btn-sm" onclick={() => (showBatchUpsertPanel = false)}>Close</button>
        <button class="btn btn-primary btn-sm" onclick={handleBatchUpsert} disabled={loading}>
          {#if loading}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
          Upsert batch
        </button>
      </div>
    {/snippet}
  </SlidePanel>

  <!-- =================================================================== -->
  <!-- Vector Detail SlidePanel                                             -->
  <!-- =================================================================== -->
  <SlidePanel
    open={showVectorDetailPanel}
    title="Vector Details"
    onclose={closeVectorDetail}
  >
    {#snippet children()}
      {#if selectedVector !== null}
        {@const v = (detailVector ?? selectedVector) as Record<string, unknown>}
        {@const vec = v.vector as number[] | undefined}
        <div class="flex flex-col gap-5" data-testid="vector-detail">
          {#if detailLoading}
            <span class="loading loading-spinner loading-sm" data-testid="vector-detail-loading"></span>
          {/if}
          {#if detailError}
            <div class="alert alert-error text-sm" role="alert" data-testid="vector-detail-error">
              Could not load vector: {detailError}
            </div>
          {/if}
          <div class="grid grid-cols-2 gap-6">
            <StatCard label="ID" value={(v.id as string) ?? "N/A"} icon={Hash} accentColor={accent} />
            <StatCard label="Dimensions" value={vec?.length ?? 0} icon={Ruler} accentColor={accent} />
          </div>

          {#if detailVector}
            <dl class="grid grid-cols-3 gap-2 text-sm" data-testid="vector-detail-meta">
              <div>
                <dt class="text-base-content/50 text-xs">Version</dt>
                <dd class="font-mono" data-testid="vector-detail-version">{detailVector.version}</dd>
              </div>
              <div>
                <dt class="text-base-content/50 text-xs">Created</dt>
                <dd class="font-mono" data-testid="vector-detail-created">{detailVector.createdAt}</dd>
              </div>
              <div>
                <dt class="text-base-content/50 text-xs">Updated</dt>
                <dd class="font-mono" data-testid="vector-detail-updated">{detailVector.updatedAt}</dd>
              </div>
            </dl>
          {/if}

          <div class="surface-panel rounded-lg p-4">
            <h4 class="text-base-content/50 mb-2 text-sm font-medium uppercase tracking-wider">Vector Values</h4>
            <div class="bg-base-300 max-h-48 overflow-auto rounded p-2">
              <pre class="font-mono text-xs whitespace-pre-wrap">{vec ? vec.join(", ") : "N/A"}</pre>
            </div>
          </div>

          <div class="surface-panel rounded-lg p-4">
            <h4 class="text-base-content/50 mb-2 text-sm font-medium uppercase tracking-wider">Metadata</h4>
            <pre class="bg-base-300 rounded p-2 font-mono text-xs whitespace-pre-wrap" data-testid="vector-detail-metadata-json">{JSON.stringify(v.metadata ?? {}, null, 2)}</pre>
            <label class="form-control mt-3 w-full">
              <div class="label"><span class="label-text">Edit metadata (JSON)</span></div>
              <textarea
                class="textarea textarea-bordered h-24 w-full font-mono text-sm"
                aria-label="Edit metadata"
                data-testid="vector-detail-metadata-input"
                bind:value={detailMetadata}
              ></textarea>
            </label>
            <button
              class="btn btn-primary btn-sm mt-2"
              onclick={handleSaveMetadata}
              disabled={detailLoading}
            >
              Save metadata
            </button>
          </div>
        </div>
      {/if}
    {/snippet}
    {#snippet footer()}
      <div class="flex gap-2 justify-between">
        <button
          class="btn btn-error btn-sm btn-outline gap-1"
          onclick={() => {
            const v = selectedVector as Record<string, unknown>;
            deleteVecId = (v.id as string) ?? "";
            showDeleteVecConfirm = true;
          }}
        >
          <Trash2 class="h-3.5 w-3.5" />
          Delete
        </button>
        <button class="btn btn-ghost btn-sm" onclick={closeVectorDetail}>
          Close
        </button>
      </div>
    {/snippet}
  </SlidePanel>

  <!-- =================================================================== -->
  <!-- Create Namespace Modal                                               -->
  <!-- =================================================================== -->
  {#if showCreateNsModal}
    <dialog class="modal modal-open">
      <div class="modal-box border border-base-content/6">
        <h3 class="text-base font-semibold flex items-center gap-2">
          <Plus class="h-5 w-5" />
          Create Namespace
        </h3>
        <div class="mt-4 flex flex-col gap-5">
          <label class="form-control w-full">
            <div class="label"><span class="label-text">Name</span></div>
            <input
              type="text"
              class="input input-bordered input-sm w-full"
              placeholder="Namespace name"
              bind:value={nsCreateName}
            />
          </label>
          <label class="form-control w-full">
            <div class="label"><span class="label-text">Dimensions</span></div>
            <input
              type="number"
              class="input input-bordered input-sm w-full"
              placeholder="128"
              min="1"
              bind:value={nsCreateDimensions}
            />
          </label>
          <label class="form-control w-full">
            <div class="label"><span class="label-text">Distance Metric</span></div>
            <select class="select select-bordered select-sm w-full" bind:value={nsCreateMetric}>
              <option value={0}>Cosine</option>
              <option value={1}>Euclidean</option>
              <option value={2}>Dot Product</option>
            </select>
          </label>
        </div>
        <div class="modal-action">
          <button class="btn btn-ghost" onclick={() => (showCreateNsModal = false)}>Cancel</button>
          <button class="btn btn-primary" onclick={handleNsCreate} disabled={loading}>
            {#if loading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Create
          </button>
        </div>
      </div>
      <form method="dialog" class="modal-backdrop">
        <button onclick={() => (showCreateNsModal = false)}>close</button>
      </form>
    </dialog>
  {/if}

  <!-- =================================================================== -->
  <!-- Delete Namespace Confirm                                             -->
  <!-- =================================================================== -->
  <ConfirmModal
    open={showDeleteNsConfirm}
    title="Delete Namespace"
    message={`Are you sure you want to delete namespace "${selectedNamespace}"? This will permanently remove all vectors in this namespace.`}
    confirmLabel="Delete"
    variant="error"
    onconfirm={handleNsDelete}
    oncancel={() => (showDeleteNsConfirm = false)}
  />

  <!-- =================================================================== -->
  <!-- Delete Vector Confirm                                                -->
  <!-- =================================================================== -->
  <ConfirmModal
    open={showDeleteVecConfirm}
    title="Delete Vector"
    message={`Are you sure you want to delete vector "${deleteVecId}" from namespace "${selectedNamespace}"?`}
    confirmLabel="Delete"
    variant="error"
    onconfirm={handleVecDelete}
    oncancel={() => (showDeleteVecConfirm = false)}
  />
{/if}
