// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import type {
    // Admin / health
    HealthResponse,
    StatusResponse,
    PeersResponse,
    TopologyResponse,
    CapabilitiesResponse,
    MintJoinTokenResponse,

    // MVCC
    MVCCKeyValue,
    RangeOptions,
    RangeResponse,
    RestoreRevisionRequest,
    RestoreRevisionResult,

    // Transaction
    TransactionRequest,
    TransactionResponse,

    // Batch
    Op,
    BatchResponse,
    ImportOptions,
    ImportResult,

    // Watch
    WatchEvent,
    WatchOptions,
    WatchResponse,

    // Lease
    LeaseInfo,

    // Lock
    LockInfo,
    LockAcquireRequest,
    LockReleaseRequest,
    LockExtendRequest,
    LockListResponse,

    // Index
    IndexCreateRequest,
    IndexListResponse,
    IndexQueryResponse,
    IndexScanResponse,

    // Time-series
    TSDataPoint,
    TSQueryRequest,
    TSQueryResponse,
    TSAggregateRequest,
    TSAggregateResponse,
    TSRichQueryRequest,
    TSRichQueryResponse,
    TSDeleteRequest,
    TSDeleteResponse,
    TSGetRetentionRequest,
    TSGetRetentionResponse,
    TSSetRetentionRequest,
    TSListMetricsRequest,
    TSListMetricsResponse,

    // Vector
    CreateNamespaceRequest,
    CreateNamespaceResponse,
    DeleteNamespaceRequest,
    ListNamespacesRequest,
    ListNamespacesResponse,
    GetNamespaceInfoRequest,
    NamespaceInfo,
    UpsertVectorRequest,
    UpsertVectorResponse,
    GetVectorRequest,
    VectorData,
    DeleteVectorRequest,
    BatchUpsertRequest,
    BatchUpsertResponse,
    BatchDeleteVectorsRequest,
    BatchDeleteVectorsResponse,
    ListVectorsRequest,
    ListVectorsResponse,
    UpdateMetadataRequest,
    SearchSimilarRequest,
    SearchSimilarResponse,
    SearchByMetadataRequest,
    SearchByMetadataResponse,
    HybridSearchRequest,
    HybridSearchResponse,
    SearchByIDRequest,
    RebuildIndexRequest,
    RebuildIndexResponse,
    GetRebuildProgressRequest,
    RebuildProgress,
    GetIndexStatsRequest,
    IndexStats,
    CompactNamespaceRequest,
    CompactNamespaceResponse,
    GetCompactionProgressRequest,
    CompactionProgress,

    // User
    User,
    CreateUserRequest,
    UpdateUserRequest,
    ListUsersRequest,
    ListUsersResponse,

    // Role
    Role,
    CreateRoleRequest,
    UpdateRoleRequest,
    ListRolesRequest,
    ListRolesResponse,
    ListPermissionsResponse,

    // Cert
    CertBundle,
    GenerateCertRequest,
    ExportCertRequest,
    ExportCertResponse,
    ListCertsResponse,

    // Embedded server
    EmbeddedConfig,

    // Database
    DatabaseInfo,
    CreateDatabaseRequest,
    DatabaseProtocolConfig,
    DatabaseSQLConfig,
    DatabaseCompactionConfig,
    DatabaseRaftConfig,
    DatabaseBarrierConfig,
    DatabaseBarrierStatus,
    DatabaseConnectionConfig,
    DaemonStatus,
    SQLQueryResult,

    // Collection / Document
    CollectionInfo,
    CreateCollectionRequest,
    InsertDocumentRequest,
    InsertDocumentResponse,
    DocumentResponse,
    UpdateDocumentRequest,
    ListDocumentsRequest,
    ListDocumentsResponse,

    // Seal
    SealStatus,
    ClusterSeal,
    BootstrapResult,
    UnsealProgress,
    EnableEncryptionRequest,
    EnableEncryptionResponse,

    // Share-holder administration
    OfficerRegisterRequest,
    OfficerRegisterResponse,
    OfficerStatus,
    OfficerRevokeRequest,
    QuorumRound,
    RekeyStartRequest,
    ShamirInitializeRequest,
    TenantOfficerRegisterRequest,
    TenantOfficerRevokeRequest,
    DatabaseOfficerRegisterRequest,
    DatabaseOfficerRevokeRequest,
    DatabaseOfficerUnenrolRequest,
    DatabaseRekeyStartRequest,
    DatabaseShamirInitializeRequest,
    DatabaseShamirInitializeResponse,
    TenantRekeyStartRequest,

    // Tenant
    CreateTenantRequest,
    CreateTenantResponse,
    GetTenantResponse,
    ListTenantsResponse,
    UpdateTenantRequest,
    UpdateTenantResponse,
    TenantBarrierInitRequest,
    TenantBarrierInitResponse,
    TenantBarrierUnsealRequest,
    TenantBarrierStatusResponse,
    UpdateTenantQuotasRequest,
    UpdateTenantQuotasResponse,
    UpdateTenantEntitlementsRequest,
    UpdateTenantEntitlementsResponse,

    // Server config
    ConfigInitializedResponse,

    // Connection manager
    ConnectionProfile,
    ConnectionState,

    // Backup
    BackupRequest,
    BackupInfo,
    RestoreRequest,
    RestoreResult,
    VerifyResult,
    ListBackupsResponse,

    // Rebalance
    ShardStatsResponse,
    RebalancePlanRequest,
    RebalancePlan,
    RebalanceResult,
    ImbalanceReport,

    // Credentials
    CredentialVersionInfo,
    CredentialRotateResult,
} from './types.js';
import type { AuditFilter, AuditPage } from './audit.js';

// ---------------------------------------------------------------------------
// Connection lifecycle
// ---------------------------------------------------------------------------

/**
 * ConnectionClient manages the transport connection lifecycle. Concrete
 * implementations wrap Wails IPC, REST fetch, gRPC channels, or other
 * transport mechanisms.
 */
export interface ConnectionClient {
    /**
     * Establishes a connection to the QRDB server at the given address.
     * The address format is transport-specific (e.g. "https://host:port"
     * for REST, "host:port" for gRPC).
     *
     * @param address - Server address in transport-specific format.
     */
    connect(address: string): Promise<void>;

    /** Gracefully shuts down the transport connection. */
    disconnect(): Promise<void>;

    /** Returns true when the transport connection is active and healthy. */
    isConnected(): boolean;

    /**
     * Returns version metadata about the SDK and connected server.
     * Keys typically include "sdk" and "server".
     */
    getVersion(): Record<string, string>;
}

// ---------------------------------------------------------------------------
// Basic KV operations
// ---------------------------------------------------------------------------

/**
 * KVClient provides data-plane key-value operations. Values are
 * represented as strings; binary data should be base64-encoded by the
 * caller.
 */
export interface KVClient {
    /**
     * Stores a key-value pair through Raft consensus.
     *
     * @param key - The key to store.
     * @param value - The value to associate with the key.
     */
    put(key: string, value: string): Promise<void>;

    /**
     * Retrieves a value by key with a linearizable read.
     *
     * @param key - The key to retrieve.
     * @returns The stored value.
     * @throws {@link import('./errors.js').KeyNotFoundError} when the key does not exist.
     */
    get(key: string): Promise<string>;

    /**
     * Removes a key through Raft consensus.
     *
     * @param key - The key to delete.
     */
    delete(key: string): Promise<void>;

    /**
     * Returns sorted keys matching the given prefix. An empty prefix
     * returns all keys.
     *
     * @param prefix - Key prefix to filter by.
     * @returns Sorted array of matching keys.
     */
    list(prefix: string): Promise<string[]>;

    /**
     * Returns true when the key exists in the store.
     *
     * @param key - The key to check.
     */
    exists(key: string): Promise<boolean>;

    /**
     * Returns all key-value pairs matching the given prefix.
     *
     * @param prefix - Key prefix to filter by.
     * @returns Map of key to value.
     */
    scan(prefix: string): Promise<Record<string, string>>;
}

// ---------------------------------------------------------------------------
// Admin operations
// ---------------------------------------------------------------------------

/** AdminClient provides control-plane cluster management operations. */
export interface AdminClient {
    /** Returns the health status of the QRDB node. */
    health(): Promise<HealthResponse>;

    /**
     * Returns detailed status information about the node and all
     * loaded shards.
     */
    status(): Promise<StatusResponse>;

    /** Returns the peer membership for all loaded shards. */
    peers(): Promise<PeersResponse>;

    /**
     * Returns the full cluster topology as seen from this node: per-shard
     * placement, leader election state, and Raft transport addresses for all
     * known replicas. Maps to GET /admin/topology.
     */
    getTopology(): Promise<TopologyResponse>;

    /**
     * Returns the platform's node-keyspace compaction policy -- the scheduled
     * compaction of the keys outside every database namespace -- or
     * `undefined` when none is set. Requires `admin:node_compaction`, which
     * only the system tenant can hold. Maps to GET /admin/node-compaction.
     */
    nodeCompaction(): Promise<DatabaseCompactionConfig | undefined>;

    /**
     * Sets the platform's node-keyspace compaction policy; omitting it turns
     * the policy off. Resolves to the policy now in force, `undefined` when
     * off. Requires `admin:node_compaction` (system tenant only). Maps to PUT
     * /admin/node-compaction.
     */
    setNodeCompaction(compaction?: DatabaseCompactionConfig): Promise<DatabaseCompactionConfig | undefined>;

    /**
     * Mints a single-use, identity-bound member-side CSR join token for
     * `nodeId`. Requires `cluster:join_token` (`ScopeCross`, platform-only
     * -- no built-in role carries it by default, so a tenant caller is
     * always refused). The token is returned exactly once; losing it means
     * minting a fresh one. Maps to `POST /v1/cluster/join-tokens`.
     *
     * @param nodeId - The joining node's SPIFFE node identity to bind the
     * token to.
     * @param ttlSeconds - Token lifetime in seconds. Omitted or `<= 0` uses
     * the server's default TTL.
     */
    mintJoinToken(nodeId: string, ttlSeconds?: number): Promise<MintJoinTokenResponse>;

    /**
     * Adds a new replica to the specified shard.
     *
     * @param shardId - Target shard identifier.
     * @param replicaId - Replica identifier to add.
     * @param raftAddr - Raft transport address of the new member.
     */
    addMember(shardId: number, replicaId: number, raftAddr: string): Promise<void>;

    /**
     * Removes a replica from the specified shard.
     *
     * @param shardId - Target shard identifier.
     * @param replicaId - Replica identifier to remove.
     */
    removeMember(shardId: number, replicaId: number): Promise<void>;

    /**
     * Triggers an asynchronous snapshot for the specified shard.
     *
     * @param shardId - Target shard identifier.
     */
    requestSnapshot(shardId: number): Promise<void>;

    /**
     * Transitions the host into drain mode where new proposals are
     * rejected while in-flight operations complete.
     */
    drain(): Promise<void>;

    /** Returns the compile-time feature set of the QRDB binary. */
    capabilities(): Promise<CapabilitiesResponse>;
}

// ---------------------------------------------------------------------------
// MVCC operations
// ---------------------------------------------------------------------------

/**
 * MVCCClient provides MVCC-aware key-value operations with revision
 * tracking, lease attachment, and range queries.
 */
export interface MVCCClient {
    /**
     * Stores a key-value pair with an attached lease. When the lease
     * expires, the key is automatically deleted.
     *
     * @param key - The key to store.
     * @param value - Base64-encoded value.
     * @param leaseId - Lease to attach.
     * @returns The stored key-value pair with revision metadata.
     */
    putWithLease(key: string, value: string, leaseId: number): Promise<MVCCKeyValue>;

    /**
     * Retrieves a key-value pair with full MVCC revision metadata.
     *
     * @param key - The key to retrieve.
     * @returns The versioned key-value pair.
     * @throws {@link import('./errors.js').KeyNotFoundError} when the key does not exist.
     */
    getRevision(key: string): Promise<MVCCKeyValue>;

    /**
     * Retrieves a key-value pair at a historical revision.
     *
     * @param key - The key to retrieve.
     * @param revision - The historical revision to read from.
     * @returns The versioned key-value pair at the requested revision.
     * @throws {@link import('./errors.js').RevisionCompactedError} when the revision has been compacted.
     */
    getAtRevision(key: string, revision: number): Promise<MVCCKeyValue>;

    /**
     * Returns key-value pairs in the half-open interval [start, end).
     *
     * @param start - Base64-encoded start key (inclusive).
     * @param end - Base64-encoded end key (exclusive).
     * @param opts - Range query configuration.
     * @returns Matching key-value pairs and count.
     */
    range(start: string, end: string, opts: RangeOptions): Promise<RangeResponse>;

    /**
     * Discards all MVCC revisions older than the specified revision.
     *
     * @param revision - Compact up to this revision.
     */
    compact(revision: number): Promise<void>;

    /**
     * Returns the latest committed MVCC revision.
     *
     * @returns The current revision number.
     */
    currentRevision(): Promise<number>;

    /**
     * Returns a bounded set of keys to the value they held at an earlier
     * MVCC revision by proposing new writes — a logical undo, never a Raft
     * rewind. Exactly one of `req.prefix` and `req.keys` must be set.
     *
     * Requires the target daemon to run `raft.sm_type: mvcc`; the default
     * (`pebble`) has no MVCC revision history and this call fails with an
     * actionable error naming the fix rather than a generic failure.
     *
     * @param req - The restore request (target revision, scope, and options).
     * @returns The computed (and, unless `req.dryRun`, applied) restore plan.
     */
    restoreToRevision(req: RestoreRevisionRequest): Promise<RestoreRevisionResult>;
}

// ---------------------------------------------------------------------------
// Transaction operations
// ---------------------------------------------------------------------------

/**
 * TransactionClient provides atomic compare-and-swap transaction
 * execution.
 */
export interface TransactionClient {
    /**
     * Atomically evaluates compares and executes the corresponding
     * operation set. When all compares succeed, success ops are applied;
     * otherwise failure ops are applied.
     *
     * @param req - The transaction request with compares and ops.
     * @returns The transaction result including whether it succeeded.
     */
    execute(req: TransactionRequest): Promise<TransactionResponse>;
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

/** BatchClient provides atomic batch operation execution. */
export interface BatchClient {
    /**
     * Applies the given operations atomically. All operations succeed or
     * fail together.
     *
     * @param ops - Operations to execute.
     * @returns Per-operation results.
     */
    execute(ops: readonly Op[]): Promise<BatchResponse>;

    /**
     * Opens the file at the given path and imports its records into the
     * KV store. The opts.format field controls format detection; "auto"
     * infers the format from the filename and file content.
     *
     * @param filePath - Absolute path to the file to import.
     * @param opts - Import configuration (format, key/value fields, etc.).
     * @returns Statistics about the import operation.
     */
    importFile(filePath: string, opts: ImportOptions): Promise<ImportResult>;

    /**
     * Returns the names of all compiled-in codec types.
     *
     * @returns Sorted list of codec names.
     */
    availableCodecs(): Promise<readonly string[]>;

    /**
     * Returns the supported import file format names.
     *
     * @returns List of format names (e.g. "csv", "json", "auto").
     */
    availableImportFormats(): Promise<readonly string[]>;
}

// ---------------------------------------------------------------------------
// Watch operations
// ---------------------------------------------------------------------------

/**
 * WatchHandle represents an active watch subscription. It provides
 * event callbacks and cancellation support.
 */
export interface WatchHandle {
    /** Server-assigned watch identifier. */
    readonly id: number;

    /**
     * Registers a callback for incoming watch events. Returns an
     * unsubscribe function that removes the callback.
     *
     * @param callback - Function invoked with each batch of events.
     * @returns A function that removes this callback when called.
     */
    onEvent(callback: (events: readonly WatchEvent[]) => void): () => void;

    /**
     * Registers a callback invoked when the watch is canceled (either
     * by the client or server). Returns an unsubscribe function.
     *
     * @param callback - Function invoked with the watch response on cancellation.
     * @returns A function that removes this callback when called.
     */
    onCancel(callback: (response: WatchResponse) => void): () => void;

    /** Cancels the watch and releases server-side resources. */
    cancel(): Promise<void>;
}

/** WatchClient provides event streaming for key changes. */
export interface WatchClient {
    /**
     * Creates a watcher that delivers events for the specified key.
     *
     * @param key - The key to watch.
     * @param opts - Watch configuration.
     * @returns A handle for receiving events and canceling the watch.
     */
    watch(key: string, opts: WatchOptions): Promise<WatchHandle>;

    /**
     * Creates a watcher that delivers events for all keys matching the
     * given prefix.
     *
     * @param prefix - Key prefix to watch.
     * @param opts - Watch configuration.
     * @returns A handle for receiving events and canceling the watch.
     */
    watchPrefix(prefix: string, opts: WatchOptions): Promise<WatchHandle>;
}

// ---------------------------------------------------------------------------
// Lease operations
// ---------------------------------------------------------------------------

/** LeaseClient provides distributed lease management. */
export interface LeaseClient {
    /**
     * Creates a new lease with the specified TTL in seconds.
     *
     * @param ttl - Time-to-live in seconds.
     * @returns The created lease information.
     */
    grant(ttl: number): Promise<LeaseInfo>;

    /**
     * Immediately expires a lease and deletes attached keys.
     *
     * @param leaseId - Lease to revoke.
     */
    revoke(leaseId: number): Promise<void>;

    /**
     * Resets the TTL on an existing lease.
     *
     * @param leaseId - Lease to keep alive.
     * @returns The refreshed lease information.
     */
    keepAlive(leaseId: number): Promise<LeaseInfo>;

    /**
     * Returns the current state of a lease.
     *
     * @param leaseId - Lease to query.
     * @returns The lease information including remaining TTL.
     */
    timeToLive(leaseId: number): Promise<LeaseInfo>;

    /**
     * Returns all active leases.
     *
     * @returns Array of active lease information.
     */
    list(): Promise<readonly LeaseInfo[]>;
}

// ---------------------------------------------------------------------------
// Distributed lock operations
// ---------------------------------------------------------------------------

/** LockClient provides distributed lock management with fencing tokens. */
export interface LockClient {
    /**
     * Acquires a distributed lock, blocking until the lock is available
     * or the context is cancelled.
     *
     * @param req - Lock acquisition parameters.
     * @returns The acquired lock including its fencing token.
     */
    acquire(req: LockAcquireRequest): Promise<LockInfo>;

    /**
     * Attempts to acquire the lock without blocking.
     * Rejects immediately when the lock is held by another owner.
     *
     * @param req - Lock acquisition parameters.
     * @returns The acquired lock including its fencing token.
     */
    tryLock(req: LockAcquireRequest): Promise<LockInfo>;

    /**
     * Releases a held lock. The fencing token must match the current
     * lock state to prevent stale unlocks.
     *
     * @param req - Lock release parameters including fencing token.
     */
    release(req: LockReleaseRequest): Promise<void>;

    /**
     * Refreshes the TTL of an existing lock via lease keepalive.
     * The fencing token must match the current lock state.
     *
     * @param req - Lock extend parameters including fencing token.
     * @returns The updated lock with refreshed TTL.
     */
    extend(req: LockExtendRequest): Promise<LockInfo>;

    /**
     * Returns the current lock state for a key, or null when not locked.
     *
     * @param key - Resource key to inspect.
     * @returns The current lock state, or null if the key is not locked.
     */
    status(key: string): Promise<LockInfo | null>;

    /**
     * Returns all active locks whose keys start with the given prefix.
     * An empty prefix returns all active locks.
     *
     * @param prefix - Optional key prefix filter.
     * @returns All matching active locks.
     */
    list(prefix?: string): Promise<LockListResponse>;
}

// ---------------------------------------------------------------------------
// Index operations
// ---------------------------------------------------------------------------

/** IndexClient provides secondary index management and querying. */
export interface IndexClient {
    /**
     * Creates a new secondary index.
     *
     * @param req - Index creation parameters.
     */
    create(req: IndexCreateRequest): Promise<void>;

    /**
     * Removes a secondary index by name.
     *
     * @param name - Index name to drop.
     */
    drop(name: string): Promise<void>;

    /**
     * Returns entry IDs matching an exact value in an index.
     *
     * @param indexName - Name of the index to query.
     * @param value - Base64-encoded value to match.
     * @returns Matching entry IDs.
     */
    query(indexName: string, value: string): Promise<IndexQueryResponse>;

    /**
     * Returns entries in an index within a value range.
     *
     * @param indexName - Name of the index to scan.
     * @param start - Base64-encoded start of the scan range (inclusive).
     * @param end - Base64-encoded end of the scan range (exclusive).
     * @returns Entries matching the scan range.
     */
    scan(indexName: string, start: string, end: string): Promise<IndexScanResponse>;

    /**
     * Returns the names of all registered indexes.
     *
     * @returns List of index names.
     */
    list(): Promise<IndexListResponse>;
}

// ---------------------------------------------------------------------------
// Time-series operations
// ---------------------------------------------------------------------------

/**
 * TimeSeriesClient provides time-series data ingestion and querying.
 * This client is nullable on the composite Client interface because
 * time-series support is a compile-time feature that may be disabled.
 */
export interface TimeSeriesClient {
    /**
     * Stores a single time-series data point.
     *
     * @param point - The data point to write.
     */
    write(point: TSDataPoint): Promise<void>;

    /**
     * Stores multiple time-series data points atomically.
     *
     * @param points - Data points to write.
     */
    writeBatch(points: readonly TSDataPoint[]): Promise<void>;

    /**
     * Retrieves data points for a metric within a time range.
     *
     * @param req - Query parameters.
     * @returns Matching data points.
     */
    query(req: TSQueryRequest): Promise<TSQueryResponse>;

    /**
     * Computes an aggregation over data points in a time range.
     *
     * @param req - Aggregation parameters.
     * @returns Aggregation results.
     */
    aggregate(req: TSAggregateRequest): Promise<TSAggregateResponse>;

    /**
     * Stores a single validated data point (alias for write).
     *
     * @param point - The data point to write.
     */
    writePoint(point: TSDataPoint): Promise<void>;

    /**
     * Performs a rich query with aggregation, downsampling, and grouping.
     *
     * @param req - Rich query parameters.
     * @returns Rich query results with optional grouping.
     */
    richQuery(req: TSRichQueryRequest): Promise<TSRichQueryResponse>;

    /**
     * Deletes time-series data matching the criteria.
     *
     * @param req - Deletion parameters.
     * @returns Deletion result.
     */
    deleteSeries(req: TSDeleteRequest): Promise<TSDeleteResponse>;

    /**
     * Retrieves the retention policy for a metric.
     *
     * @param req - Request specifying the metric.
     * @returns The retention policy.
     */
    getRetention(req: TSGetRetentionRequest): Promise<TSGetRetentionResponse>;

    /**
     * Sets the retention policy for a metric.
     *
     * @param req - Request specifying the metric and policy.
     */
    setRetention(req: TSSetRetentionRequest): Promise<void>;

    /**
     * Lists metric names matching a prefix.
     *
     * @param req - Request specifying the prefix and limit.
     * @returns Matching metric names.
     */
    listMetrics(req: TSListMetricsRequest): Promise<TSListMetricsResponse>;
}

// ---------------------------------------------------------------------------
// Vector operations
// ---------------------------------------------------------------------------

/**
 * VectorClient provides vector database operations including namespace
 * management, vector CRUD, similarity search, and index maintenance.
 * This client is nullable on the composite Client interface because
 * vector support is a compile-time feature that may be disabled.
 */
export interface VectorClient {
    /**
     * Creates a new vector namespace.
     *
     * @param req - Namespace creation parameters.
     * @returns Creation result.
     */
    createNamespace(req: CreateNamespaceRequest): Promise<CreateNamespaceResponse>;

    /**
     * Removes a namespace and all its vectors.
     *
     * @param req - Namespace deletion parameters.
     */
    deleteNamespace(req: DeleteNamespaceRequest): Promise<void>;

    /**
     * Returns all vector namespace names.
     *
     * @param req - Request parameters (optional shard filter).
     * @returns List of namespace names.
     */
    listNamespaces(req: ListNamespacesRequest): Promise<ListNamespacesResponse>;

    /**
     * Returns metadata about a namespace.
     *
     * @param req - Request specifying the namespace.
     * @returns Namespace metadata.
     */
    getNamespaceInfo(req: GetNamespaceInfoRequest): Promise<NamespaceInfo>;

    /**
     * Inserts or updates a vector in a namespace.
     *
     * @param req - Upsert parameters.
     * @returns Upsert result including version and whether it was created.
     */
    upsertVector(req: UpsertVectorRequest): Promise<UpsertVectorResponse>;

    /**
     * Retrieves a single vector by ID.
     *
     * @param req - Request specifying namespace and vector ID.
     * @returns The vector data.
     */
    getVector(req: GetVectorRequest): Promise<VectorData>;

    /**
     * Removes a single vector by ID.
     *
     * @param req - Deletion parameters.
     */
    deleteVector(req: DeleteVectorRequest): Promise<void>;

    /**
     * Inserts or updates multiple vectors.
     *
     * @param req - Batch upsert parameters.
     * @returns Batch result with success/failure counts.
     */
    batchUpsert(req: BatchUpsertRequest): Promise<BatchUpsertResponse>;

    /**
     * Removes multiple vectors by ID.
     *
     * @param req - Batch deletion parameters.
     * @returns Batch result with success/failure counts.
     */
    batchDeleteVectors(req: BatchDeleteVectorsRequest): Promise<BatchDeleteVectorsResponse>;

    /**
     * Returns a paginated list of vectors.
     *
     * @param req - Pagination parameters.
     * @returns Paginated vector list.
     */
    listVectors(req: ListVectorsRequest): Promise<ListVectorsResponse>;

    /**
     * Updates the metadata for an existing vector.
     *
     * @param req - Metadata update parameters.
     */
    updateMetadata(req: UpdateMetadataRequest): Promise<void>;

    /**
     * Performs a k-nearest-neighbor search.
     *
     * @param req - Search parameters.
     * @returns Ordered search results.
     */
    searchSimilar(req: SearchSimilarRequest): Promise<SearchSimilarResponse>;

    /**
     * Returns vectors matching metadata filters.
     *
     * @param req - Metadata search parameters.
     * @returns Matching vectors.
     */
    searchByMetadata(req: SearchByMetadataRequest): Promise<SearchByMetadataResponse>;

    /**
     * Combines vector similarity and metadata filtering.
     *
     * @param req - Hybrid search parameters.
     * @returns Ordered search results.
     */
    hybridSearch(req: HybridSearchRequest): Promise<HybridSearchResponse>;

    /**
     * Searches using an existing vector's ID as the query.
     *
     * @param req - Search-by-ID parameters.
     * @returns Ordered search results (same shape as searchSimilar).
     */
    searchByID(req: SearchByIDRequest): Promise<SearchSimilarResponse>;

    /**
     * Triggers an asynchronous index rebuild.
     *
     * @param req - Rebuild request parameters.
     * @returns Rebuild job information.
     */
    rebuildIndex(req: RebuildIndexRequest): Promise<RebuildIndexResponse>;

    /**
     * Returns the current progress of an index rebuild.
     *
     * @param req - Progress query parameters.
     * @returns Current rebuild progress.
     */
    getRebuildProgress(req: GetRebuildProgressRequest): Promise<RebuildProgress>;

    /**
     * Returns statistics about a namespace's vector index.
     *
     * @param req - Stats query parameters.
     * @returns Index statistics.
     */
    getIndexStats(req: GetIndexStatsRequest): Promise<IndexStats>;

    /**
     * Triggers compaction of a namespace's storage.
     *
     * @param req - Compaction request parameters.
     * @returns Compaction job information.
     */
    compactNamespace(req: CompactNamespaceRequest): Promise<CompactNamespaceResponse>;

    /**
     * Returns the current progress of a compaction.
     *
     * @param req - Progress query parameters.
     * @returns Current compaction progress.
     */
    getCompactionProgress(req: GetCompactionProgressRequest): Promise<CompactionProgress>;
}

// ---------------------------------------------------------------------------
// User management operations
// ---------------------------------------------------------------------------

/**
 * UserClient provides user management operations. Users authenticate
 * exclusively via mTLS X.509 certificates (passwordless).
 */
export interface UserClient {
    /**
     * Creates a new passwordless user. The username must be unique
     * within the tenant.
     *
     * @param req - User creation parameters.
     * @returns The created user.
     */
    create(req: CreateUserRequest): Promise<User>;

    /**
     * Retrieves a user by its globally unique ID.
     *
     * @param id - User identifier.
     * @returns The user entity.
     */
    get(id: string): Promise<User>;

    /**
     * Applies partial updates to an existing user.
     *
     * @param req - Update parameters.
     * @returns The updated user.
     */
    update(req: UpdateUserRequest): Promise<User>;

    /**
     * Removes a non-system user by ID.
     *
     * @param id - User identifier.
     */
    delete(id: string): Promise<void>;

    /**
     * Returns a paginated list of users for the given tenant.
     *
     * @param req - List parameters including pagination.
     * @returns Paginated user list.
     */
    list(req: ListUsersRequest): Promise<ListUsersResponse>;

    /**
     * Activates a user account.
     *
     * @param id - User identifier.
     */
    enable(id: string): Promise<void>;

    /**
     * Deactivates a user account.
     *
     * @param id - User identifier.
     */
    disable(id: string): Promise<void>;

    /**
     * Resolves a user by an mTLS certificate's SPKI thumbprint via the
     * secondary index (the mTLS-auth hot path).
     *
     * @param thumbprint - Base64url SHA-256 SPKI thumbprint of the certificate.
     * @returns The user entity bound to the thumbprint.
     */
    getUserByCertThumbprint(thumbprint: string): Promise<User>;

    /**
     * Binds an mTLS certificate's SPKI thumbprint to a user so the secondary
     * index resolves it (the enrollment seam).
     *
     * @param userId - User identifier to bind the thumbprint to.
     * @param thumbprint - Base64url SHA-256 SPKI thumbprint of the certificate.
     * @param notAfterUnix - Optional unix-seconds expiry; 0 or omitted never expires.
     * @returns The updated user.
     */
    addCertBinding(userId: string, thumbprint: string, notAfterUnix?: number): Promise<User>;

    /**
     * Withdraws an mTLS certificate's SPKI thumbprint from a user so it no
     * longer resolves and a certificate issued for that key no longer
     * authenticates as the account. The counterpart of addCertBinding, and the
     * only way to unenroll a key.
     *
     * This is NOT certificate revocation. Revocation refuses a certificate; a
     * binding is over the SubjectPublicKeyInfo and outlives every certificate
     * issued for that key, so a re-issue for the same key authenticates again
     * until the binding is withdrawn.
     *
     * Withdrawing the LAST binding WIDENS the account: an empty binding set
     * states no restriction, so it returns to admitting any certificate whose
     * CN and OU name it. Enrol the replacement first.
     *
     * @param userId - User identifier to withdraw the thumbprint from.
     * @param thumbprint - Base64url SHA-256 SPKI thumbprint to withdraw.
     * @returns The updated user.
     */
    removeCertBinding(userId: string, thumbprint: string): Promise<User>;
}

// ---------------------------------------------------------------------------
// Role management operations
// ---------------------------------------------------------------------------

/**
 * RoleClient provides role management operations. Roles aggregate
 * fine-grained permissions and can be assigned to users.
 */
export interface RoleClient {
    /**
     * Creates a new role with a generated UUID. The name must be unique
     * within the tenant.
     *
     * @param req - Role creation parameters.
     * @returns The created role.
     */
    create(req: CreateRoleRequest): Promise<Role>;

    /**
     * Retrieves a role by its globally unique ID.
     *
     * @param id - Role identifier.
     * @returns The role entity.
     */
    get(id: string): Promise<Role>;

    /**
     * Applies partial updates to an existing role.
     *
     * @param req - Update parameters.
     * @returns The updated role.
     */
    update(req: UpdateRoleRequest): Promise<Role>;

    /**
     * Removes a non-system role by ID.
     *
     * @param id - Role identifier.
     */
    delete(id: string): Promise<void>;

    /**
     * Returns a paginated list of roles for the given tenant.
     *
     * @param req - List parameters including pagination.
     * @returns Paginated role list.
     */
    list(req: ListRolesRequest): Promise<ListRolesResponse>;

    /**
     * Assigns a role to a user.
     *
     * @param userId - User identifier.
     * @param roleId - Role identifier.
     */
    assign(userId: string, roleId: string): Promise<void>;

    /**
     * Removes a role assignment from a user.
     *
     * @param userId - User identifier.
     * @param roleId - Role identifier.
     */
    unassign(userId: string, roleId: string): Promise<void>;

    /**
     * Returns all available permission categories for UI rendering.
     *
     * @returns Permission categories grouped by logical area.
     */
    listPermissions(): Promise<ListPermissionsResponse>;
}

// ---------------------------------------------------------------------------
// Certificate management operations
// ---------------------------------------------------------------------------

/**
 * CertClient provides certificate management operations for mTLS
 * client certificates.
 */
export interface CertClient {
    /**
     * Creates a client certificate signed by the internal CA.
     *
     * @param req - Certificate generation parameters.
     * @returns The PEM-encoded certificate bundle.
     */
    generate(req: GenerateCertRequest): Promise<CertBundle>;

    /**
     * Exports a certificate and key as a PKCS#12 archive for browser
     * import.
     *
     * @param req - Export parameters including passphrase.
     * @returns Base64-encoded PKCS#12 archive.
     */
    export(req: ExportCertRequest): Promise<ExportCertResponse>;

    /**
     * Marks a certificate serial as revoked.
     *
     * @param serial - Certificate serial number to revoke.
     */
    revoke(serial: string): Promise<void>;

    /**
     * Lists all issued client certificates for a tenant.
     *
     * @param tenantId - Tenant identifier.
     * @returns Certificate records.
     */
    list(tenantId: string): Promise<ListCertsResponse>;
}

// ---------------------------------------------------------------------------
// Database management operations
// ---------------------------------------------------------------------------

/**
 * DatabaseClient provides database management operations for the
 * multi-database architecture.
 */
export interface DatabaseClient {
    /**
     * Creates a new database with the specified engine and backend.
     *
     * @param req - Database creation parameters.
     * @returns The created database information.
     */
    create(req: CreateDatabaseRequest): Promise<DatabaseInfo>;

    /**
     * Retrieves database information by name.
     *
     * @param name - Database name.
     * @returns The database information.
     */
    get(name: string): Promise<DatabaseInfo>;

    /**
     * Returns information about all databases.
     *
     * @returns Array of database information objects.
     */
    list(): Promise<DatabaseInfo[]>;

    /**
     * Removes a database by name.
     *
     * @param name - Database name to drop.
     */
    drop(name: string): Promise<void>;

    /**
     * Updates which SQL wire protocols (MySQL, PostgreSQL) serve a database.
     *
     * @param name - Database name.
     * @param protocol - Wire protocol configuration.
     * @returns The updated database information.
     */
    updateProtocol(name: string, protocol: DatabaseProtocolConfig): Promise<DatabaseInfo>;

    /**
     * Updates a database's SQL settings: its default concurrency mode.
     * Omitting `sql` clears the settings, restoring the defaults.
     *
     * @param name - Database name.
     * @param sql - SQL settings.
     * @returns The updated database information.
     */
    updateSQL(name: string, sql?: DatabaseSQLConfig): Promise<DatabaseInfo>;

    /**
     * Sets a database's scheduled-compaction policy. Omitting `compaction`
     * turns it off. Requires `mvcc:compact` on the database, which only its
     * owning tenant can hold; another tenant's database answers as missing.
     *
     * @param name - Database name.
     * @param compaction - Compaction policy.
     * @returns The updated database information.
     */
    updateCompaction(name: string, compaction?: DatabaseCompactionConfig): Promise<DatabaseInfo>;

    /**
     * Updates the Raft consensus configuration for a database.
     *
     * @param name - Database name.
     * @param raft - Raft configuration.
     * @returns The updated database information.
     */
    updateRaft(name: string, raft: DatabaseRaftConfig): Promise<DatabaseInfo>;

    /**
     * Updates the encryption barrier configuration for a database.
     *
     * @param name - Database name.
     * @param barrier - Barrier configuration.
     * @returns The updated database information.
     */
    updateBarrier(name: string, barrier: DatabaseBarrierConfig): Promise<DatabaseInfo>;

    /**
     * Reports a database barrier's state: sealed or not, its strategy and
     * when it was first initialized. A Shamir database's custody is
     * initialized exactly when `initializedAt` is present.
     *
     * @param name - Database name.
     * @returns The barrier's status.
     */
    barrierStatus(name: string): Promise<DatabaseBarrierStatus>;

    /**
     * Updates the remote connection configuration for a database.
     *
     * @param name - Database name.
     * @param connection - Connection configuration.
     * @returns The updated database information.
     */
    updateConnection(name: string, connection: DatabaseConnectionConfig): Promise<DatabaseInfo>;

    /**
     * Changes the replication factor of an existing dedicated-raft database,
     * adding or removing voting replicas without recreating the database.
     *
     * @param name - Database name.
     * @param targetReplicas - Desired replication factor (must be >= 1).
     * @returns The updated database information.
     */
    scale(name: string, targetReplicas: number): Promise<DatabaseInfo>;
}

// ---------------------------------------------------------------------------
// SQL query operations
// ---------------------------------------------------------------------------

/**
 * SQLClient provides SQL query execution against databases with
 * SQL wire protocols enabled.
 */
export interface SQLClient {
    /**
     * Executes a SQL query against the specified database.
     *
     * @param query - SQL query string.
     * @param database - Target database name.
     * @returns Query result with columns, rows, and timing.
     */
    query(query: string, database: string): Promise<SQLQueryResult>;
}

// ---------------------------------------------------------------------------
// Seal operations
// ---------------------------------------------------------------------------

/**
 * SealClient manages the seal/unseal lifecycle of the QRDB server.
 * The seal system protects data-at-rest encryption keys using Shamir
 * secret sharing.
 */
export interface SealClient {
    /**
     * Initializes the encryption barrier with a passphrase (or empty
     * string for no-barrier mode). Must be called once during first-run
     * setup to mark the server as configured.
     *
     * @param secret - Passphrase for the barrier, or empty string for none.
     */
    initialize(secret: string): Promise<void>;

    /**
     * Initializes the barrier under Shamir custody. Every unseal-domain
     * holder must be enrolled first (OfficerClient.registerOfficer): the
     * unseal key is split to them and each share is sealed to its holder.
     * There is no secret, and no share is returned.
     *
     * @param req - Threshold, share count (the enrolled holder count),
     *   factor policy and optional PINs.
     * @returns The share-free distribution result.
     */
    initializeShamir(req: ShamirInitializeRequest): Promise<BootstrapResult>;

    /**
     * Unseals a barrier held by a credential strategy with its passphrase.
     * A Shamir barrier is unsealed by a quorum of its holders from the CLI or
     * the desktop app; the server refuses this request for it.
     *
     * @param secret - The unseal passphrase.
     * @returns The barrier's state after the request.
     */
    unseal(secret: string): Promise<UnsealProgress>;

    /**
     * Transitions the server back to sealed state, zeroing the master
     * key. A Shamir barrier is sealed by a holder quorum instead; the
     * server refuses this request for it.
     */
    seal(): Promise<void>;

    /**
     * Returns the current seal state of the server.
     *
     * @returns Seal status including share counts.
     */
    status(): Promise<SealStatus>;

    /**
     * Clears the node barrier's cluster seal record once this node has
     * unsealed with its own credential, so every node held by the seal
     * serves again. Refused while cluster sealing is not active, and while
     * this node's own barrier is still sealed.
     *
     * @returns The node barrier's cluster seal state after the clear.
     */
    clusterUnseal(): Promise<ClusterSeal>;

    /**
     * Enables at-rest encryption on a barrier that was initialized without it
     * (strategy "none"), re-encrypting all stored data under a newly minted
     * keyed strategy.
     *
     * Scope is selected by `req.tenantId`:
     * - Omitted/empty → the SYSTEM barrier (admin scope). Requires admin
     *   privileges.
     * - Present → that tenant's OWN dedicated barrier. A tenant whose data
     *   lives on the shared system barrier cannot change its encryption; the
     *   server rejects such a request with a 403 (shared-barrier immutable).
     *
     * The source barrier MUST currently be unencrypted: if it already uses a
     * keyed strategy, or a migration is already in progress, the server rejects
     * the call with a 409 conflict. Invalid options yield a 400; an unavailable
     * migration provider yields a 501.
     *
     * The migration runs ASYNCHRONOUSLY. A successful call returns
     * "migration_started"; callers must then POLL {@link status} until the
     * reported `strategy` changes from "none" to the keyed strategy to confirm
     * completion.
     *
     * @param req - Migration parameters including scope, target strategy, and credentials.
     * @returns Acknowledgement that the migration has started.
     * @throws {@link import('./errors.js').TransportError} with statusCode 403 (shared barrier),
     *   409 (source not "none" or migration in progress), 400 (invalid options), or
     *   501 (migration provider unavailable).
     */
    enableEncryption(req: EnableEncryptionRequest): Promise<EnableEncryptionResponse>;
}

// ---------------------------------------------------------------------------
// Share-holder administration
// ---------------------------------------------------------------------------

/**
 * OfficerClient administers share holders — Security Officers and key
 * custodians — for the system barrier's unseal and recovery domains and for
 * tenant barriers: enrolment, listing, rekey and revocation.
 *
 * It never handles share material. Claiming an envelope and submitting a
 * share to a quorum need the holder's private keys, and happen in the CLI or
 * the desktop app, where those keys stay in Go.
 */
export interface OfficerClient {
    /**
     * Enrols an unseal-domain holder of the system barrier from its CSR. The
     * ceremony CA issues the holder certificate; no share exists yet.
     *
     * @param req - The enrolment.
     * @returns The issued certificate and what was enrolled.
     */
    registerOfficer(req: OfficerRegisterRequest): Promise<OfficerRegisterResponse>;

    /**
     * Enrols a recovery-domain holder of the system barrier.
     *
     * @param req - The enrolment.
     * @returns The issued certificate and what was enrolled.
     */
    registerRecoveryOfficer(req: OfficerRegisterRequest): Promise<OfficerRegisterResponse>;

    /**
     * Lists the system barrier's unseal-domain holders.
     *
     * @returns Holder status records; never a share.
     */
    listOfficers(): Promise<OfficerStatus[]>;

    /**
     * Opens a rekey round on a system-barrier domain. A quorum of current
     * holders completes it.
     *
     * @param req - Domain, new threshold and policy, and the SO PIN.
     * @returns The open round.
     */
    startRekey(req: RekeyStartRequest): Promise<QuorumRound>;

    /**
     * Revokes a system-barrier holder: opens a rekey round that removes it.
     *
     * @param req - Domain, holder, optional new threshold, and the SO PIN.
     * @returns The open round.
     */
    revokeOfficer(req: OfficerRevokeRequest): Promise<QuorumRound>;

    /**
     * Enrols a holder of a tenant's barrier.
     *
     * @param req - The tenant and the enrolment.
     * @returns The issued certificate and what was enrolled.
     */
    registerTenantOfficer(req: TenantOfficerRegisterRequest): Promise<OfficerRegisterResponse>;

    /**
     * Lists the holders of a tenant's barrier.
     *
     * @param tenantId - Target tenant identifier.
     * @returns Holder status records; never a share.
     */
    listTenantOfficers(tenantId: string): Promise<OfficerStatus[]>;

    /**
     * Opens a rekey round on a tenant's barrier.
     *
     * @param req - The tenant, new threshold and policy.
     * @returns The open round.
     */
    startTenantRekey(req: TenantRekeyStartRequest): Promise<QuorumRound>;

    /**
     * Revokes a holder of a tenant's barrier.
     *
     * @param req - The tenant, the holder and an optional new threshold.
     * @returns The open round.
     */
    revokeTenantOfficer(req: TenantOfficerRevokeRequest): Promise<QuorumRound>;

    /**
     * Enrols a holder of a database's Shamir barrier.
     *
     * @param req - The database and the enrolment.
     * @returns The issued certificate and what was enrolled.
     */
    registerDatabaseOfficer(req: DatabaseOfficerRegisterRequest): Promise<OfficerRegisterResponse>;

    /**
     * Lists the holders of a database's barrier.
     *
     * @param database - The database, resolved in the caller's tenant.
     * @returns Holder status records; never a share.
     */
    listDatabaseOfficers(database: string): Promise<OfficerStatus[]>;

    /**
     * Splits a Shamir database's root key to its enrolled holders, at the
     * M-of-N the database was created with. Each holder then claims its
     * envelope from the CLI or the desktop app.
     *
     * @param req - The database and the factor policy.
     * @returns The share-free split.
     */
    initializeDatabaseShamir(req: DatabaseShamirInitializeRequest): Promise<DatabaseShamirInitializeResponse>;

    /**
     * Opens a rekey round on a database's barrier.
     *
     * @param req - The database, new threshold and policy.
     * @returns The open round.
     */
    startDatabaseRekey(req: DatabaseRekeyStartRequest): Promise<QuorumRound>;

    /**
     * Revokes a holder of a database's barrier.
     *
     * @param req - The database, the holder and an optional new threshold.
     * @returns The open round.
     */
    revokeDatabaseOfficer(req: DatabaseOfficerRevokeRequest): Promise<QuorumRound>;

    /**
     * Removes a holder of a database's barrier before its custody is
     * initialized, when it holds no share; its place is free for another
     * holder. Once initialized a holder is removed by
     * {@link revokeDatabaseOfficer}.
     *
     * @param req - The database and the holder.
     */
    unenrolDatabaseOfficer(req: DatabaseOfficerUnenrolRequest): Promise<void>;
}

// ---------------------------------------------------------------------------
// Embedded server operations
// ---------------------------------------------------------------------------

/**
 * EmbeddedServerClient manages the lifecycle of an in-process qrdbd
 * server instance. Only available in desktop (Wails) mode.
 */
export interface EmbeddedServerClient {
    /**
     * Starts an embedded QRDB server with the given configuration.
     *
     * @param config - Embedded server configuration.
     */
    startEmbedded(config: EmbeddedConfig): Promise<void>;

    /** Stops the embedded QRDB server and releases all resources. */
    stopEmbedded(): Promise<void>;

    /** Returns true when the embedded server is running. */
    isEmbedded(): boolean;

    /**
     * Returns true when a saved embedded configuration exists on disk.
     * Used on startup to determine whether to auto-start the server.
     */
    hasSavedConfig(): Promise<boolean>;

    /**
     * Restores the embedded server from a previously saved configuration.
     * Throws when no saved config exists.
     */
    restoreEmbedded(): Promise<void>;

    /** Removes the saved embedded configuration from disk. */
    clearSavedConfig(): Promise<void>;
}

// ---------------------------------------------------------------------------
// Daemon operations
// ---------------------------------------------------------------------------

/**
 * DaemonClient manages the lifecycle of the qrdbd daemon process.
 * Only available in desktop (Wails) mode.
 */
export interface DaemonClient {
    /**
     * Starts the qrdbd daemon with the given configuration file.
     *
     * @param configPath - Path to the daemon configuration file.
     */
    startDaemon(configPath: string): Promise<void>;

    /** Stops the running qrdbd daemon. */
    stopDaemon(): Promise<void>;

    /**
     * Restarts the qrdbd daemon with the given configuration file.
     *
     * @param configPath - Path to the daemon configuration file.
     */
    restartDaemon(configPath: string): Promise<void>;

    /**
     * Returns the current status of the qrdbd daemon.
     *
     * @returns Daemon status including PID and log file path.
     */
    daemonStatus(): Promise<DaemonStatus>;

    /**
     * Returns recent daemon log output.
     *
     * @param maxLines - Maximum number of log lines to return.
     * @returns Log output as a single string.
     */
    daemonLogs(maxLines: number): Promise<string>;
}

// ---------------------------------------------------------------------------
// Collection and document operations
// ---------------------------------------------------------------------------

/**
 * CollectionClient provides collection and document management operations
 * for organizing structured data within databases.
 */
export interface CollectionClient {
    /**
     * Creates a new collection within a database.
     *
     * @param req - Collection creation parameters.
     * @returns The created collection information.
     */
    create(req: CreateCollectionRequest): Promise<CollectionInfo>;

    /**
     * Retrieves information about a specific collection.
     *
     * @param database - Database name.
     * @param name - Collection name.
     * @returns The collection information.
     */
    get(database: string, name: string): Promise<CollectionInfo>;

    /**
     * Returns all collections within a database.
     *
     * @param database - Database name.
     * @returns Array of collection information objects.
     */
    list(database: string): Promise<CollectionInfo[]>;

    /**
     * Removes a collection from a database by name.
     *
     * @param database - Database name.
     * @param name - Collection name to drop.
     */
    drop(database: string, name: string): Promise<void>;

    /**
     * Inserts a document into a collection.
     *
     * @param req - Insert request with database, collection, and document.
     * @returns The assigned document ID.
     */
    insertDocument(req: InsertDocumentRequest): Promise<InsertDocumentResponse>;

    /**
     * Retrieves a single document by its ID.
     *
     * @param database - Database name.
     * @param collection - Collection name.
     * @param id - Document ID.
     * @returns The document response.
     */
    getDocument(database: string, collection: string, id: string): Promise<DocumentResponse>;

    /**
     * Replaces an existing document in a collection.
     *
     * @param req - Update request with database, collection, ID, and document.
     */
    updateDocument(req: UpdateDocumentRequest): Promise<void>;

    /**
     * Removes a document from a collection by its ID.
     *
     * @param database - Database name.
     * @param collection - Collection name.
     * @param id - Document ID.
     */
    deleteDocument(database: string, collection: string, id: string): Promise<void>;

    /**
     * Returns a paginated list of documents in a collection.
     *
     * @param req - List request with pagination parameters.
     * @returns A page of documents with pagination token.
     */
    listDocuments(req: ListDocumentsRequest): Promise<ListDocumentsResponse>;
}

// ---------------------------------------------------------------------------
// Tenant management operations
// ---------------------------------------------------------------------------

/**
 * TenantClient provides multi-tenant lifecycle management. Tenants are
 * isolated namespaces with independent encryption barriers and user bases.
 */
export interface TenantClient {
    /**
     * Creates a new tenant with the given identifier and name.
     *
     * @param req - Tenant creation parameters.
     * @returns The created tenant.
     */
    create(req: CreateTenantRequest): Promise<CreateTenantResponse>;

    /**
     * Retrieves a tenant by its unique identifier.
     *
     * @param tenantId - Tenant identifier.
     * @returns The tenant.
     */
    get(tenantId: string): Promise<GetTenantResponse>;

    /**
     * Returns all registered tenants.
     *
     * @returns List of tenant information objects.
     */
    list(): Promise<ListTenantsResponse>;

    /**
     * Removes a tenant and all its associated data.
     *
     * @param tenantId - Tenant identifier to delete.
     */
    delete(tenantId: string): Promise<void>;

    /**
     * Applies a partial update to an existing tenant. Changing the isolation
     * type returns 409 Conflict if the tenant already has stored data.
     *
     * @param tenantId - Tenant identifier.
     * @param req      - Fields to update (undefined fields are left unchanged).
     * @returns The updated tenant.
     */
    update(tenantId: string, req: UpdateTenantRequest): Promise<UpdateTenantResponse>;

    /**
     * Initializes the encryption barrier for a tenant. Must be called once
     * before the tenant can store encrypted data. For the "shamir" strategy the
     * response carries the threshold and shareCount but NO share bytes — shares
     * are sealed to the tenant's share holders, enrolled beforehand with
     * OfficerClient.registerTenantOfficer. Each holder claims its envelope from
     * the CLI (`qrdb tenant barrier-officer claim`) or the desktop app, where
     * its private key stays; this SDK never handles share material.
     * For "none" and "passphrase" strategies the response carries only the
     * tenant id and strategy.
     *
     * @param req - Barrier initialization parameters.
     * @returns The initialization result; no share bytes for any strategy.
     */
    barrierInit(req: TenantBarrierInitRequest): Promise<TenantBarrierInitResponse>;

    /**
     * Unseals a tenant barrier held by a credential strategy with its
     * passphrase. A Shamir tenant barrier is unsealed by a quorum of its
     * holders from the CLI or the desktop app.
     *
     * @param req - The tenant and its unseal passphrase.
     */
    barrierUnseal(req: TenantBarrierUnsealRequest): Promise<void>;

    /**
     * Returns the current seal state of a tenant's encryption barrier,
     * including the active strategy and, for Shamir barriers, the threshold and
     * total share counts.
     *
     * @param tenantId - Tenant identifier.
     * @returns The tenant barrier status.
     */
    barrierStatus(tenantId: string): Promise<TenantBarrierStatusResponse>;

    /**
     * Updates the resource quotas for a tenant.
     *
     * @param req - Quota update parameters.
     * @returns The updated tenant.
     */
    updateQuotas(req: UpdateTenantQuotasRequest): Promise<UpdateTenantQuotasResponse>;

    /**
     * Updates the feature entitlements for a tenant (which isolation tiers and
     * dedicated-storage features the tenant's plan permits).
     *
     * @param req - Entitlement update parameters.
     * @returns The updated tenant.
     */
    updateEntitlements(req: UpdateTenantEntitlementsRequest): Promise<UpdateTenantEntitlementsResponse>;
}

// ---------------------------------------------------------------------------
// Server configuration operations
// ---------------------------------------------------------------------------

/**
 * ServerConfigClient provides server-level configuration queries.
 */
export interface ServerConfigClient {
    /**
     * Checks whether the server has completed first-run setup.
     *
     * @returns Initialization status.
     */
    initialized(): Promise<ConfigInitializedResponse>;

    /**
     * Reports whether the binary was compiled with embedded server support.
     *
     * @returns True when the qrdb_embedded build tag is present.
     */
    isEmbeddedAvailable(): Promise<boolean>;
}

// ---------------------------------------------------------------------------
// Backup and restore operations
// ---------------------------------------------------------------------------

/** BackupClient provides cluster backup and restore operations. */
export interface BackupClient {
    /**
     * Creates a new backup of the cluster data.
     *
     * @param req - Backup creation parameters.
     * @returns Metadata about the created backup.
     */
    create(req: BackupRequest): Promise<BackupInfo>;

    /**
     * Returns metadata for all available backups.
     *
     * @returns List of backup metadata records.
     */
    list(): Promise<ListBackupsResponse>;

    /**
     * Retrieves metadata for a single backup by ID.
     *
     * @param id - Backup identifier.
     * @returns Backup metadata.
     */
    get(id: string): Promise<BackupInfo>;

    /**
     * Removes a backup record and its associated data.
     *
     * @param id - Backup identifier to delete.
     */
    delete(id: string): Promise<void>;

    /**
     * Restores cluster data from an existing backup.
     *
     * @param req - Restore request parameters.
     * @returns Result including number of shards restored and any errors.
     */
    restore(req: RestoreRequest): Promise<RestoreResult>;

    /**
     * Verifies the integrity of a backup by recomputing and comparing
     * its checksum.
     *
     * @param id - Backup identifier to verify.
     * @returns Verification result including checksum comparison.
     */
    verify(id: string): Promise<VerifyResult>;
}

// ---------------------------------------------------------------------------
// Rebalance operations
// ---------------------------------------------------------------------------

/** RebalanceClient provides cluster shard rebalancing operations. */
export interface RebalanceClient {
    /**
     * Returns load statistics for the specified shard.
     *
     * @param shardId - Shard identifier.
     * @returns Key count, byte count, leader ID, and replica count.
     */
    getShardStats(shardId: number): Promise<ShardStatsResponse>;

    /**
     * Generates a rebalance plan without executing it. The plan describes
     * the optimal set of shard moves to balance load across nodes.
     *
     * @param req - Plan generation parameters.
     * @returns A plan with moves and estimated execution cost.
     */
    createPlan(req: RebalancePlanRequest): Promise<RebalancePlan>;

    /**
     * Executes a previously generated rebalance plan.
     *
     * @param plan - The plan to execute.
     * @returns Result including completed/failed move counts and final score.
     */
    execute(plan: RebalancePlan): Promise<RebalanceResult>;

    /**
     * Returns a cluster-wide load imbalance report with per-node metrics
     * and hotspot identification.
     *
     * @returns Imbalance report.
     */
    getImbalanceReport(): Promise<ImbalanceReport>;
}

// ---------------------------------------------------------------------------
// Credential management operations
// ---------------------------------------------------------------------------

/**
 * CredentialClient manages named-credential submission, storage, and
 * versioned rotation. Credential VALUES are handled as opaque strings on
 * the wire (base64 or plain text, per the storing caller's convention);
 * this SDK never interprets or validates their contents.
 */
export interface CredentialClient {
    /**
     * Submits a credential value for the named credential, notifying any
     * blocked retrievers for the manual strategy or delegating to storage
     * for the barrier strategy.
     *
     * @param name - Credential name.
     * @param value - Credential value (base64 or plain text).
     */
    submit(name: string, value: string): Promise<void>;

    /**
     * Persists a credential value under the given name.
     *
     * @param name - Credential name.
     * @param value - Credential value (base64 or plain text).
     */
    store(name: string, value: string): Promise<void>;

    /**
     * Returns the credential value for the given name. For the manual
     * strategy this may block server-side until the credential is
     * submitted.
     *
     * @param name - Credential name.
     * @returns The stored credential value.
     */
    retrieve(name: string): Promise<string>;

    /**
     * Returns the sorted list of every stored credential name. Values are
     * never returned.
     */
    list(): Promise<string[]>;

    /** Returns the active credential management strategy name. */
    strategy(): Promise<string>;

    /**
     * Removes a named credential and all of its non-current historical
     * versions.
     *
     * @param name - Credential name to delete.
     */
    delete(name: string): Promise<void>;

    /**
     * Atomically replaces the current value of a credential, optionally
     * keeping the previous version retrievable for a grace period.
     *
     * @param name - Credential name to rotate.
     * @param newValue - The new credential value.
     * @param gracePeriodSeconds - Seconds the previous version remains
     *   retrievable. Omit or pass 0 to wipe the previous value immediately.
     * @returns The previous and new version numbers.
     */
    rotate(name: string, newValue: string, gracePeriodSeconds?: number): Promise<CredentialRotateResult>;

    /**
     * Returns the version history (current + non-expired grace) for a
     * credential. Values are never returned.
     *
     * @param name - Credential name.
     */
    listVersions(name: string): Promise<CredentialVersionInfo[]>;

    /**
     * Returns the stored credential value for a specific version.
     *
     * @param name - Credential name.
     * @param version - Version number; 0 means the current version.
     */
    retrieveVersion(name: string, version: number): Promise<string>;

    /**
     * Reaps historical versions whose grace period has elapsed for the
     * named credential.
     *
     * @param name - Credential name.
     * @returns The number of versions reaped.
     */
    gcExpired(name: string): Promise<number>;
}

// ---------------------------------------------------------------------------
// Audit query operations
// ---------------------------------------------------------------------------

/**
 * AuditQueryClient queries compliance-grade audit events. Requires the
 * "audit:query" permission on the active connection.
 */
export interface AuditQueryClient {
    /**
     * Queries audit events matching the provided filter.
     *
     * @param f - Filter criteria (time range, actor, resource, action,
     *   outcome, tenant, pagination). Omit for the server default page.
     * @returns A paginated page of matching audit events.
     */
    query(f?: AuditFilter): Promise<AuditPage>;
}

// ---------------------------------------------------------------------------
// Connection manager operations
// ---------------------------------------------------------------------------

/**
 * ConnectionManagerClient manages multiple named database connections,
 * supporting both embedded (in-process) and external (network) profiles.
 */
export interface ConnectionManagerClient {
    /** Returns all saved connection profiles. */
    listProfiles(): Promise<ConnectionProfile[]>;

    /** Validates and saves a connection profile (insert or update). */
    saveProfile(profile: ConnectionProfile): Promise<void>;

    /** Disconnects and removes a connection profile. */
    deleteProfile(id: string): Promise<void>;

    /** Connects to a saved profile and returns its runtime state. */
    connect(profileId: string): Promise<ConnectionState>;

    /** Disconnects an active connection. */
    disconnect(profileId: string): Promise<void>;

    /** Returns the runtime state for a connection, or null. */
    getState(profileId: string): Promise<ConnectionState | null>;

    /** Sets the active connection used by all data bindings. */
    setActive(profileId: string): Promise<void>;

    /** Returns the active connection's profile ID, or empty string. */
    getActiveProfileId(): Promise<string>;

    /** Reports whether embedded server support is compiled in. */
    isEmbeddedAvailable(): Promise<boolean>;
}

// ---------------------------------------------------------------------------
// Composite client interface
// ---------------------------------------------------------------------------

/**
 * Client combines transport lifecycle with all typed service interfaces.
 * Concrete implementations provide the appropriate transport binding
 * (Wails IPC, REST fetch, gRPC, etc.).
 *
 * The `timeSeries` and `vector` properties are nullable because these
 * are compile-time features that may be disabled in the server binary.
 */
export interface Client {
    /**
     * Returns a client scoped to the named database within the authenticated
     * tenant. Every data-plane operation (KV, MVCC, transaction, batch, watch,
     * lease, lock, index, time-series, vector) on the returned client targets
     * that database; management and admin operations are unaffected and remain
     * system-scoped. Passing an empty string or the `default` database name
     * returns an unscoped client (the server uses `default` when no scope is
     * selected).
     *
     * @param name - Target database name within the tenant.
     * @returns A client whose data-plane calls operate on the named database.
     */
    databaseScoped(name: string): Client;

    /** Connection lifecycle management. */
    readonly connection: ConnectionClient;

    /** Basic key-value operations. */
    readonly kv: KVClient;

    /** Cluster administration and health. */
    readonly admin: AdminClient;

    /** MVCC-aware key-value operations with revision tracking. */
    readonly mvcc: MVCCClient;

    /** Atomic compare-and-swap transactions. */
    readonly transaction: TransactionClient;

    /** Atomic batch operations. */
    readonly batch: BatchClient;

    /** Key change event streaming. */
    readonly watch: WatchClient;

    /** Distributed lease management. */
    readonly lease: LeaseClient;

    /** Distributed lock management with fencing tokens. */
    readonly lock: LockClient;

    /** Secondary index management and querying. */
    readonly index: IndexClient;

    /** Time-series data operations (null when disabled). */
    readonly timeSeries: TimeSeriesClient | null;

    /** Vector database operations (null when disabled). */
    readonly vector: VectorClient | null;

    /** User management. */
    readonly users: UserClient;

    /** Role management. */
    readonly roles: RoleClient;

    /** Certificate management. */
    readonly certs: CertClient;

    /** Database management. */
    readonly database: DatabaseClient;

    /** Collection and document management. */
    readonly collection: CollectionClient;

    /** SQL query execution. */
    readonly sql: SQLClient;

    /** Seal/unseal lifecycle management. */
    readonly seal: SealClient;

    /** Share-holder administration: enrolment, listing, rekey and revocation. */
    readonly officers: OfficerClient;

    /** Embedded server lifecycle (desktop mode only). */
    readonly embeddedServer: EmbeddedServerClient;

    /** Daemon lifecycle management (desktop mode only). */
    readonly daemon: DaemonClient;

    /** Multi-tenant lifecycle management. */
    readonly tenants: TenantClient;

    /** Cluster backup and restore operations. */
    readonly backups: BackupClient;

    /** Shard rebalancing operations. */
    readonly rebalance: RebalanceClient;

    /** Named-credential submission, storage, and versioned rotation. */
    readonly credentials: CredentialClient;

    /** Compliance-grade audit event querying. */
    readonly audit: AuditQueryClient;

    /** Server configuration queries. */
    readonly config: ServerConfigClient;

    /** Connection manager for multi-connection support. */
    readonly connectionManager: ConnectionManagerClient;
}
