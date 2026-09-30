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
    AdminClient,
    AuditQueryClient,
    BackupClient,
    BatchClient,
    CertClient,
    Client,
    CollectionClient,
    ConnectionClient,
    ConnectionManagerClient,
    CredentialClient,
    DaemonClient,
    DatabaseClient,
    EmbeddedServerClient,
    IndexClient,
    KVClient,
    LeaseClient,
    LockClient,
    MVCCClient,
    OfficerClient,
    RebalanceClient,
    RoleClient,
    SealClient,
    ServerConfigClient,
    SQLClient,
    TenantClient,
    TimeSeriesClient,
    TransactionClient,
    UserClient,
    VectorClient,
    WatchClient,
    WatchHandle,
} from './interfaces.js';
import type {
    BatchDeleteVectorsRequest,
    BatchDeleteVectorsResponse,
    BatchResponse,
    BatchUpsertRequest,
    BatchUpsertResponse,
    BootstrapResult,
    ImportOptions,
    ImportResult,
    CapabilitiesResponse,
    CertBundle,
    CompactNamespaceRequest,
    CompactNamespaceResponse,
    CompactionProgress,
    CollectionInfo,
    CreateCollectionRequest,
    CreateDatabaseRequest,
    CreateNamespaceRequest,
    DaemonStatus,
    DocumentResponse,
    DatabaseBarrierConfig,
    DatabaseConnectionConfig,
    DatabaseRaftConfig,
    DatabaseProtocolConfig,
    DatabaseSQLConfig,
    DatabaseCompactionConfig,
    UpdateDatabaseCompactionRequest,
    UpdateDatabaseProtocolRequest,
    UpdateDatabaseSQLRequest,
    CreateNamespaceResponse,
    CreateRoleRequest,
    CreateUserRequest,
    DatabaseInfo,
    InsertDocumentRequest,
    InsertDocumentResponse,
    ListDocumentsRequest,
    ListDocumentsResponse,
    SQLQueryResult,
    UpdateDocumentRequest,
    DeleteNamespaceRequest,
    DeleteVectorRequest,
    EmbeddedConfig,
    EnableEncryptionRequest,
    EnableEncryptionResponse,
    ExportCertRequest,
    ExportCertResponse,
    GenerateCertRequest,
    GetCompactionProgressRequest,
    GetIndexStatsRequest,
    GetNamespaceInfoRequest,
    GetRebuildProgressRequest,
    GetVectorRequest,
    HealthResponse,
    HybridSearchRequest,
    HybridSearchResponse,
    IndexCreateRequest,
    IndexListResponse,
    IndexQueryResponse,
    IndexScanResponse,
    IndexStats,
    LeaseInfo,
    LockInfo,
    LockAcquireRequest,
    LockReleaseRequest,
    LockExtendRequest,
    LockListResponse,
    ListCertsResponse,
    ListNamespacesRequest,
    ListNamespacesResponse,
    ListPermissionsResponse,
    ListRolesRequest,
    ListRolesResponse,
    ListUsersRequest,
    ListUsersResponse,
    ListVectorsRequest,
    ListVectorsResponse,
    MVCCKeyValue,
    NamespaceInfo,
    Op,
    PeersResponse,
    TopologyResponse,
    RangeOptions,
    RangeResponse,
    RebuildIndexRequest,
    RebuildIndexResponse,
    RebuildProgress,
    RestoreRevisionRequest,
    RestoreRevisionResult,
    Role,
    SealStatus,
    SearchByIDRequest,
    SearchByMetadataRequest,
    SearchByMetadataResponse,
    SearchSimilarRequest,
    SearchSimilarResponse,
    StatusResponse,
    TSAggregateRequest,
    TSAggregateResponse,
    TSDataPoint,
    TSDeleteRequest,
    TSDeleteResponse,
    TSGetRetentionRequest,
    TSGetRetentionResponse,
    TSListMetricsRequest,
    TSListMetricsResponse,
    TSQueryRequest,
    TSQueryResponse,
    TSRichQueryRequest,
    TSRichQueryResponse,
    TSSetRetentionRequest,
    TransactionRequest,
    TransactionResponse,
    UnsealProgress,
    UpdateMetadataRequest,
    UpdateRoleRequest,
    UpdateUserRequest,
    UpsertVectorRequest,
    UpsertVectorResponse,
    User,
    VectorData,
    WatchEvent,
    WatchOptions,
    WatchResponse,
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
    ConfigInitializedResponse,
    BackupRequest,
    BackupInfo,
    RestoreRequest,
    RestoreResult,
    VerifyResult,
    ListBackupsResponse,
    ShardStatsResponse,
    RebalancePlanRequest,
    RebalancePlan,
    RebalanceResult,
    ImbalanceReport,
    ConnectionProfile,
    ConnectionState,
    OfficerRegisterRequest,
    OfficerRegisterResponse,
    OfficerStatus,
    OfficerRevokeRequest,
    QuorumRound,
    RekeyStartRequest,
    ShamirInitializeRequest,
    TenantOfficerRegisterRequest,
    TenantOfficerRevokeRequest,
    TenantRekeyStartRequest,
    CredentialVersionInfo,
    CredentialRotateResult,
} from './types.js';
import type { AuditFilter, AuditPage } from './audit.js';

import {
    decodeMVCCKV,
    decodeOpResponses,
    decodeRestoreResult,
    deepSnakeToCamel,
    deepCamelToSnake,
    distanceMetricToIpcWire,
    encodeBase64Utf8,
    holderEnrolmentWire,
    normalizeOfficerStatus,
    indexTypeToIpcWire,
    normalizeVectorEnumFields,
    normalizeVectorIDField,
    vectorIDToWire,
} from './transform.js';

// ---------------------------------------------------------------------------
// SDK version constant
// ---------------------------------------------------------------------------

/** Current SDK version string. */
const SDK_VERSION = '0.1.0';

// ---------------------------------------------------------------------------
// camelCase → snake_case conversion for Wails IPC boundary
// ---------------------------------------------------------------------------

/**
 * Recursively converts all object keys from camelCase to snake_case before
 * passing arguments to Go Wails bindings. Go transport structs use snake_case
 * JSON tags so the Wails IPC serializer must receive snake_case keys.
 *
 * Delegates to the shared {@link deepCamelToSnake} transformer.
 */
function toSnake<T>(obj: T): T {
    return deepCamelToSnake(obj) as T;
}

/**
 * Recursively converts all object keys from snake_case to camelCase on the
 * response returned by a Wails binding call. Go's encoding/json respects the
 * struct's json tags (e.g. `json:"is_leader"`) so responses arrive as
 * snake_case and must be transformed before SDK consumers see them.
 *
 * Delegates to the shared {@link deepSnakeToCamel} transformer.
 */
function fromSnake<T>(obj: unknown): T {
    return deepSnakeToCamel(obj) as T;
}

/**
 * Wraps a Wails binding namespace so that every method call automatically
 * applies {@link fromSnake} to the resolved value. Methods returning void
 * (undefined) are passed through without transformation.
 *
 * This avoids having to call `fromSnake` at every call site while keeping the
 * transformation logic in a single place.
 */
function wrapBindings(
    b: Record<string, (...args: unknown[]) => Promise<unknown>>,
): Record<string, (...args: unknown[]) => Promise<unknown>> {
    return new Proxy(b, {
        get(target, prop: string) {
            const fn = target[prop];
            if (typeof fn !== 'function') return fn;
            return (...args: unknown[]) =>
                fn.apply(target, args).then((result: unknown) =>
                    result === undefined || result === null
                        ? result
                        : fromSnake(result),
                );
        },
    });
}

// ---------------------------------------------------------------------------
// Wails binding accessor
// ---------------------------------------------------------------------------

/**
 * The Wails build toolchain generates TypeScript bindings under `window.go`.
 * Each Go binding struct becomes available as `window.go.main.<StructName>`.
 * Since these bindings are generated at build time, we access them via a
 * typed helper that returns the binding namespace from the global window.
 */
interface WailsBindings {
    main: {
        KVBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        AdminBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        MVCCBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        TransactionBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        BatchBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        WatchBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        LeaseBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        IndexBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        TimeSeriesBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        VectorBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        UserBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        RoleBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        CertBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        CollectionBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        DatabaseBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        SQLBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        SealBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        EmbeddedServerBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        DaemonBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        ConfigBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        TenantBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        BackupBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        OfficerBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        RebalanceBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        LockBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        CredentialBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        AuditBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
        ConnectionManagerBinding: Record<string, (...args: unknown[]) => Promise<unknown>>;
    };
}

/** Returns the Wails-generated binding namespace from the global window object. */
function getBindings(): WailsBindings {
    return (window as unknown as { go: WailsBindings }).go;
}

/** Returns true when the Wails runtime IPC bindings are available on window. */
function hasBindings(): boolean {
    return typeof (window as unknown as Record<string, unknown>).go !== 'undefined';
}

/**
 * Minimal type declaration for the Wails v2 runtime event bus
 * available at `window.runtime` inside a Wails application.
 */
interface WailsRuntime {
    EventsOnMultiple(
        eventName: string,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        callback: (...data: any[]) => void,
        maxCallbacks: number,
    ): () => void;
    EventsOff(eventName: string, ...additionalEventNames: string[]): void;
}

// ---------------------------------------------------------------------------
// WailsClient
// ---------------------------------------------------------------------------

/**
 * Wails transport implementation of the QRDB {@link Client} interface.
 * Wraps the auto-generated Wails TypeScript bindings that communicate with
 * the Go backend via IPC.
 *
 * This client is only usable inside a Wails desktop application where the
 * `window.go.main.*Binding` namespaces are available.
 */
export class WailsClient implements Client {

    /** Tracks whether the embedded server is currently running. */
    private embeddedRunning = false;

    /**
     * @inheritdoc
     *
     * Desktop/embedded multi-database scoping is out of scope for this stage:
     * the Wails bindings route per-database through their own mechanism rather
     * than an HTTP header, so this is a no-op passthrough that returns the same
     * client. It exists only to satisfy the {@link Client} interface on both
     * transports.
     */
    databaseScoped(_name: string): Client {
        return this;
    }

    // -----------------------------------------------------------------------
    // Connection
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get connection(): ConnectionClient {
        return {
            connect: async (_address: string): Promise<void> => {
                // Wails IPC is always connected when the app is running.
                // The address parameter is ignored because IPC does not use
                // network addressing.
            },
            disconnect: async (): Promise<void> => {
                // No-op: IPC lifecycle is managed by the Wails runtime.
            },
            isConnected: (): boolean => {
                return hasBindings();
            },
            getVersion: (): Record<string, string> => {
                return {
                    sdk: SDK_VERSION,
                    server: 'embedded',
                };
            },
        };
    }

    // -----------------------------------------------------------------------
    // KV
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get kv(): KVClient {
        const b = wrapBindings(getBindings().main.KVBinding);
        return {
            put: async (key: string, value: string): Promise<void> => {
                await b.Put(key, value);
            },
            get: async (key: string): Promise<string> => {
                return b.Get(key) as Promise<string>;
            },
            delete: async (key: string): Promise<void> => {
                await b.Delete(key);
            },
            list: async (prefix: string): Promise<string[]> => {
                return b.List(prefix) as Promise<string[]>;
            },
            exists: async (key: string): Promise<boolean> => {
                return b.Exists(key) as Promise<boolean>;
            },
            scan: async (prefix: string): Promise<Record<string, string>> => {
                return b.Scan(prefix) as Promise<Record<string, string>>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Admin
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get admin(): AdminClient {
        const b = wrapBindings(getBindings().main.AdminBinding);
        return {
            health: async (): Promise<HealthResponse> => {
                return b.Health() as Promise<HealthResponse>;
            },
            status: async (): Promise<StatusResponse> => {
                return b.Status() as Promise<StatusResponse>;
            },
            peers: async (): Promise<PeersResponse> => {
                return b.Peers() as Promise<PeersResponse>;
            },
            getTopology: async (): Promise<TopologyResponse> => {
                return b.GetTopology() as Promise<TopologyResponse>;
            },
            nodeCompaction: async (): Promise<DatabaseCompactionConfig | undefined> => {
                // AdminBinding.NodeCompaction returns the Go
                // *DatabaseCompactionConfig: null when no policy is set.
                const policy = (await b.NodeCompaction()) as DatabaseCompactionConfig | null;
                return policy ?? undefined;
            },
            setNodeCompaction: async (compaction?: DatabaseCompactionConfig): Promise<DatabaseCompactionConfig | undefined> => {
                // AdminBinding.SetNodeCompaction takes a
                // *DatabaseCompactionConfig: null turns the policy off.
                const policy = (await b.SetNodeCompaction(compaction ?? null)) as DatabaseCompactionConfig | null;
                return policy ?? undefined;
            },
            addMember: async (
                shardId: number,
                replicaId: number,
                raftAddr: string,
            ): Promise<void> => {
                await b.AddMember(shardId, replicaId, raftAddr);
            },
            removeMember: async (shardId: number, replicaId: number): Promise<void> => {
                await b.RemoveMember(shardId, replicaId);
            },
            requestSnapshot: async (shardId: number): Promise<void> => {
                await b.RequestSnapshot(shardId);
            },
            drain: async (): Promise<void> => {
                await b.Drain();
            },
            capabilities: async (): Promise<CapabilitiesResponse> => {
                return b.Capabilities() as Promise<CapabilitiesResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // MVCC
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get mvcc(): MVCCClient {
        const b = wrapBindings(getBindings().main.MVCCBinding);
        return {
            putWithLease: async (
                key: string,
                value: string,
                leaseId: number,
            ): Promise<MVCCKeyValue> => {
                // MVCCBinding returns *transport.MVCCKeyValue unconverted, so
                // the Wails IPC JSON marshaling base64-encodes its []byte
                // key/value fields, same as the REST wire. Decode to
                // plaintext for consistency with kv.get/kv.scan (see
                // decodeMVCCKV in transform.ts for the full explanation).
                const kv = await b.PutWithLease(key, value, leaseId) as MVCCKeyValue;
                return decodeMVCCKV(kv);
            },
            getRevision: async (key: string): Promise<MVCCKeyValue> => {
                const kv = await b.GetRevision(key) as MVCCKeyValue;
                return decodeMVCCKV(kv);
            },
            getAtRevision: async (key: string, revision: number): Promise<MVCCKeyValue> => {
                const kv = await b.GetAtRevision(key, revision) as MVCCKeyValue;
                return decodeMVCCKV(kv);
            },
            range: async (
                start: string,
                end: string,
                opts: RangeOptions,
            ): Promise<RangeResponse> => {
                const resp = await b.Range(start, end, toSnake(opts)) as RangeResponse;
                return { ...resp, kvs: (resp.kvs ?? []).map(decodeMVCCKV) };
            },
            compact: async (revision: number): Promise<void> => {
                await b.Compact(revision);
            },
            currentRevision: async (): Promise<number> => {
                return b.CurrentRevision() as Promise<number>;
            },
            restoreToRevision: async (
                req: RestoreRevisionRequest,
            ): Promise<RestoreRevisionResult> => {
                // prefix/keys are plaintext in the public API; base64-encode
                // them here to match the server's binary-safe []byte field
                // convention over Wails IPC, the same wire contract
                // MVCCKeyValue's key/value fields use. MVCCBinding.RestoreToRevision
                // is expected to take a single snake_case request object
                // (mirroring TransactionBinding.Execute) and return
                // *transport.RestoreRevisionResult unconverted.
                const wireReq: Record<string, unknown> = { revision: req.revision };
                if (req.prefix !== undefined) {
                    wireReq.prefix = encodeBase64Utf8(req.prefix);
                }
                if (req.keys !== undefined) {
                    wireReq.keys = req.keys.map(encodeBase64Utf8);
                }
                if (req.dryRun !== undefined) {
                    wireReq.dryRun = req.dryRun;
                }
                if (req.force !== undefined) {
                    wireReq.force = req.force;
                }
                const result = await b.RestoreToRevision(toSnake(wireReq)) as RestoreRevisionResult;
                // Response change/conflict "key" fields carry the same
                // base64 []byte encoding as MVCCKeyValue; decode to
                // plaintext for consistency.
                return decodeRestoreResult(result);
            },
        };
    }

    // -----------------------------------------------------------------------
    // Transaction
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get transaction(): TransactionClient {
        const b = wrapBindings(getBindings().main.TransactionBinding);
        return {
            execute: async (req: TransactionRequest): Promise<TransactionResponse> => {
                // TransactionBinding.Execute returns
                // *transport.TransactionResponse unconverted, so Get/Range
                // op results carry base64-encoded MVCCKeyValue key/value
                // over Wails IPC, same as the REST wire. Decode to
                // plaintext for consistency.
                const resp = await b.Execute(toSnake(req)) as TransactionResponse;
                return { ...resp, responses: decodeOpResponses(resp.responses) };
            },
        };
    }

    // -----------------------------------------------------------------------
    // Batch
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get batch(): BatchClient {
        const b = wrapBindings(getBindings().main.BatchBinding);
        return {
            execute: async (ops: readonly Op[]): Promise<BatchResponse> => {
                // BatchBinding.Execute returns *transport.BatchResponse
                // unconverted, so Get/Range op results carry base64-encoded
                // MVCCKeyValue key/value over Wails IPC, same as the REST
                // wire. Decode to plaintext for consistency.
                const resp = await b.Execute(toSnake(ops)) as BatchResponse;
                return { ...resp, responses: decodeOpResponses(resp.responses) };
            },
            importFile: async (filePath: string, opts: ImportOptions): Promise<ImportResult> => {
                return b.ImportFile(filePath, toSnake(opts)) as Promise<ImportResult>;
            },
            availableCodecs: async (): Promise<readonly string[]> => {
                return b.AvailableCodecs() as Promise<readonly string[]>;
            },
            availableImportFormats: async (): Promise<readonly string[]> => {
                return b.AvailableImportFormats() as Promise<readonly string[]>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Watch
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get watch(): WatchClient {
        const b = wrapBindings(getBindings().main.WatchBinding);
        return {
            watch: async (key: string, opts: WatchOptions): Promise<WatchHandle> => {
                return this.createWailsWatchHandle(b, key, opts, false);
            },
            watchPrefix: async (prefix: string, opts: WatchOptions): Promise<WatchHandle> => {
                return this.createWailsWatchHandle(b, prefix, opts, true);
            },
        };
    }

    /**
     * Creates a {@link WatchHandle} backed by the Wails IPC watch binding.
     * Events are delivered through the Wails runtime event bus and dispatched
     * to registered callbacks.
     */
    private async createWailsWatchHandle(
        b: Record<string, (...args: unknown[]) => Promise<unknown>>,
        keyOrPrefix: string,
        opts: WatchOptions,
        isPrefix: boolean,
    ): Promise<WatchHandle> {
        const eventCallbacks: Array<(events: readonly WatchEvent[]) => void> = [];
        const cancelCallbacks: Array<(response: WatchResponse) => void> = [];

        // Invoke the Go binding to start the watch. It returns a watch ID.
        const watchId = isPrefix
            ? await b.WatchPrefix(keyOrPrefix, toSnake(opts)) as number
            : await b.Watch(keyOrPrefix, toSnake(opts)) as number;

        // Subscribe to the Wails runtime event bus for this watch ID.
        // Go emits events via wailsruntime.EventsEmit(ctx, eventName, data)
        // which delivers data as direct arguments to callbacks registered
        // with window.runtime.EventsOnMultiple.
        const eventName = `qrdb:watch:${String(watchId)}`;
        const rt = (window as unknown as { runtime: WailsRuntime }).runtime;
        const unsubscribe = rt.EventsOnMultiple(eventName, (resp: WatchResponse) => {
            if (resp.canceled) {
                for (const cb of cancelCallbacks) {
                    cb(resp);
                }
            } else if (resp.events && resp.events.length > 0) {
                for (const cb of eventCallbacks) {
                    cb(resp.events);
                }
            }
        }, -1);

        return {
            get id(): number {
                return watchId;
            },
            onEvent(callback: (events: readonly WatchEvent[]) => void): () => void {
                eventCallbacks.push(callback);
                return () => {
                    const idx = eventCallbacks.indexOf(callback);
                    if (idx >= 0) {
                        eventCallbacks.splice(idx, 1);
                    }
                };
            },
            onCancel(callback: (response: WatchResponse) => void): () => void {
                cancelCallbacks.push(callback);
                return () => {
                    const idx = cancelCallbacks.indexOf(callback);
                    if (idx >= 0) {
                        cancelCallbacks.splice(idx, 1);
                    }
                };
            },
            cancel: async (): Promise<void> => {
                unsubscribe();
                await b.CancelWatch(watchId);
            },
        };
    }

    // -----------------------------------------------------------------------
    // Lease
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get lease(): LeaseClient {
        const b = wrapBindings(getBindings().main.LeaseBinding);
        return {
            grant: async (ttl: number): Promise<LeaseInfo> => {
                return b.Grant(ttl) as Promise<LeaseInfo>;
            },
            revoke: async (leaseId: number): Promise<void> => {
                await b.Revoke(leaseId);
            },
            keepAlive: async (leaseId: number): Promise<LeaseInfo> => {
                return b.KeepAlive(leaseId) as Promise<LeaseInfo>;
            },
            timeToLive: async (leaseId: number): Promise<LeaseInfo> => {
                return b.TimeToLive(leaseId) as Promise<LeaseInfo>;
            },
            list: async (): Promise<readonly LeaseInfo[]> => {
                return b.List() as Promise<readonly LeaseInfo[]>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Lock
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get lock(): LockClient {
        const b = wrapBindings(getBindings().main.LockBinding);
        return {
            acquire: async (req: LockAcquireRequest): Promise<LockInfo> => {
                return b.Acquire(toSnake(req)) as Promise<LockInfo>;
            },
            tryLock: async (req: LockAcquireRequest): Promise<LockInfo> => {
                return b.TryLock(toSnake(req)) as Promise<LockInfo>;
            },
            release: async (req: LockReleaseRequest): Promise<void> => {
                await b.Release(toSnake(req));
            },
            extend: async (req: LockExtendRequest): Promise<LockInfo> => {
                return b.Extend(toSnake(req)) as Promise<LockInfo>;
            },
            status: async (key: string): Promise<LockInfo | null> => {
                const result = await b.Status(key);
                return result as LockInfo | null;
            },
            list: async (prefix?: string): Promise<LockListResponse> => {
                return b.List(prefix ?? '') as Promise<LockListResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Index
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get index(): IndexClient {
        const b = wrapBindings(getBindings().main.IndexBinding);
        return {
            create: async (req: IndexCreateRequest): Promise<void> => {
                await b.Create(toSnake(req));
            },
            drop: async (name: string): Promise<void> => {
                await b.Drop(name);
            },
            query: async (indexName: string, value: string): Promise<IndexQueryResponse> => {
                return b.Query(indexName, value) as Promise<IndexQueryResponse>;
            },
            scan: async (
                indexName: string,
                start: string,
                end: string,
            ): Promise<IndexScanResponse> => {
                return b.Scan(indexName, start, end) as Promise<IndexScanResponse>;
            },
            list: async (): Promise<IndexListResponse> => {
                return b.List("") as Promise<IndexListResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // TimeSeries
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get timeSeries(): TimeSeriesClient | null {
        const b = wrapBindings(getBindings().main.TimeSeriesBinding);
        return {
            write: async (point: TSDataPoint): Promise<void> => {
                await b.Write(toSnake(point));
            },
            writeBatch: async (points: readonly TSDataPoint[]): Promise<void> => {
                await b.WriteBatch(toSnake(points));
            },
            query: async (req: TSQueryRequest): Promise<TSQueryResponse> => {
                return b.Query(toSnake(req)) as Promise<TSQueryResponse>;
            },
            aggregate: async (req: TSAggregateRequest): Promise<TSAggregateResponse> => {
                return b.Aggregate(toSnake(req)) as Promise<TSAggregateResponse>;
            },
            writePoint: async (point: TSDataPoint): Promise<void> => {
                await b.WritePoint(toSnake(point));
            },
            richQuery: async (req: TSRichQueryRequest): Promise<TSRichQueryResponse> => {
                return b.RichQuery(toSnake(req)) as Promise<TSRichQueryResponse>;
            },
            deleteSeries: async (req: TSDeleteRequest): Promise<TSDeleteResponse> => {
                return b.DeleteSeries(toSnake(req)) as Promise<TSDeleteResponse>;
            },
            getRetention: async (req: TSGetRetentionRequest): Promise<TSGetRetentionResponse> => {
                return b.GetRetention(toSnake(req)) as Promise<TSGetRetentionResponse>;
            },
            setRetention: async (req: TSSetRetentionRequest): Promise<void> => {
                await b.SetRetention(toSnake(req));
            },
            listMetrics: async (req: TSListMetricsRequest): Promise<TSListMetricsResponse> => {
                return b.ListMetrics(toSnake(req)) as Promise<TSListMetricsResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Vector
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get vector(): VectorClient | null {
        const b = wrapBindings(getBindings().main.VectorBinding);
        return {
            createNamespace: async (
                req: CreateNamespaceRequest,
            ): Promise<CreateNamespaceResponse> => {
                // The Go SDK transport (sdk/go/transport/vector_types.go)
                // expects distance_metric / index_type as its canonical
                // lowercase strings, not the SDK's numeric proto enums, and
                // the index parameters under index_config.
                return b.CreateNamespace(toSnake({
                    namespace: req.namespace,
                    dimension: req.dimension,
                    distanceMetric: distanceMetricToIpcWire(req.distanceMetric),
                    indexType: indexTypeToIpcWire(req.indexType),
                    indexConfig: req.indexParams,
                    shardId: req.shardId,
                })) as Promise<CreateNamespaceResponse>;
            },
            deleteNamespace: async (req: DeleteNamespaceRequest): Promise<void> => {
                await b.DeleteNamespace(toSnake(req));
            },
            listNamespaces: async (
                req: ListNamespacesRequest,
            ): Promise<ListNamespacesResponse> => {
                const resp = await b.ListNamespaces(toSnake(req)) as ListNamespacesResponse;
                // Namespace entries that arrive as info objects carry wire
                // enum strings; convert them to the SDK's numeric enums.
                return {
                    ...resp,
                    namespaces: (resp.namespaces ?? []).map(normalizeVectorEnumFields),
                };
            },
            getNamespaceInfo: async (req: GetNamespaceInfoRequest): Promise<NamespaceInfo> => {
                const info = await b.GetNamespaceInfo(toSnake(req)) as NamespaceInfo;
                return normalizeVectorEnumFields(info);
            },
            upsertVector: async (req: UpsertVectorRequest): Promise<UpsertVectorResponse> => {
                // The Go SDK transport carries vector IDs as uint64 values;
                // the SDK's string IDs are converted at the IPC boundary.
                return b.UpsertVector(toSnake({ ...req, id: vectorIDToWire(req.id) })) as Promise<UpsertVectorResponse>;
            },
            getVector: async (req: GetVectorRequest): Promise<VectorData> => {
                const vec = await b.GetVector(toSnake({ ...req, id: vectorIDToWire(req.id) })) as VectorData;
                return normalizeVectorIDField(vec);
            },
            deleteVector: async (req: DeleteVectorRequest): Promise<void> => {
                await b.DeleteVector(toSnake({ ...req, id: vectorIDToWire(req.id) }));
            },
            batchUpsert: async (req: BatchUpsertRequest): Promise<BatchUpsertResponse> => {
                return b.BatchUpsert(toSnake({
                    ...req,
                    vectors: req.vectors.map((v) => ({ ...v, id: vectorIDToWire(v.id) })),
                })) as Promise<BatchUpsertResponse>;
            },
            batchDeleteVectors: async (
                req: BatchDeleteVectorsRequest,
            ): Promise<BatchDeleteVectorsResponse> => {
                return b.BatchDeleteVectors(toSnake({
                    ...req,
                    ids: req.ids.map(vectorIDToWire),
                })) as Promise<BatchDeleteVectorsResponse>;
            },
            listVectors: async (req: ListVectorsRequest): Promise<ListVectorsResponse> => {
                const resp = await b.ListVectors(toSnake(req)) as ListVectorsResponse;
                return {
                    ...resp,
                    vectors: (resp.vectors ?? []).map(normalizeVectorIDField),
                };
            },
            updateMetadata: async (req: UpdateMetadataRequest): Promise<void> => {
                await b.UpdateMetadata(toSnake({ ...req, id: vectorIDToWire(req.id) }));
            },
            searchSimilar: async (
                req: SearchSimilarRequest,
            ): Promise<SearchSimilarResponse> => {
                const resp = await b.SearchSimilar(toSnake(req)) as SearchSimilarResponse;
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            searchByMetadata: async (
                req: SearchByMetadataRequest,
            ): Promise<SearchByMetadataResponse> => {
                const resp = await b.SearchByMetadata(toSnake(req)) as SearchByMetadataResponse;
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            hybridSearch: async (
                req: HybridSearchRequest,
            ): Promise<HybridSearchResponse> => {
                const resp = await b.HybridSearch(toSnake(req)) as HybridSearchResponse;
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            searchByID: async (req: SearchByIDRequest): Promise<SearchSimilarResponse> => {
                const resp = await b.SearchByID(toSnake({
                    ...req,
                    id: vectorIDToWire(req.id),
                })) as SearchSimilarResponse;
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            rebuildIndex: async (
                req: RebuildIndexRequest,
            ): Promise<RebuildIndexResponse> => {
                return b.RebuildIndex(toSnake(req)) as Promise<RebuildIndexResponse>;
            },
            getRebuildProgress: async (
                req: GetRebuildProgressRequest,
            ): Promise<RebuildProgress> => {
                return b.GetRebuildProgress(toSnake(req)) as Promise<RebuildProgress>;
            },
            getIndexStats: async (req: GetIndexStatsRequest): Promise<IndexStats> => {
                const stats = await b.GetIndexStats(toSnake(req)) as IndexStats;
                return normalizeVectorEnumFields(stats);
            },
            compactNamespace: async (
                req: CompactNamespaceRequest,
            ): Promise<CompactNamespaceResponse> => {
                return b.CompactNamespace(toSnake(req)) as Promise<CompactNamespaceResponse>;
            },
            getCompactionProgress: async (
                req: GetCompactionProgressRequest,
            ): Promise<CompactionProgress> => {
                return b.GetCompactionProgress(toSnake(req)) as Promise<CompactionProgress>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Users
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get users(): UserClient {
        const b = wrapBindings(getBindings().main.UserBinding);
        return {
            create: async (req: CreateUserRequest): Promise<User> => {
                return b.Create(toSnake(req)) as Promise<User>;
            },
            get: async (id: string): Promise<User> => {
                return b.Get(id) as Promise<User>;
            },
            update: async (req: UpdateUserRequest): Promise<User> => {
                return b.Update(toSnake(req)) as Promise<User>;
            },
            delete: async (id: string): Promise<void> => {
                await b.Delete(id);
            },
            list: async (req: ListUsersRequest): Promise<ListUsersResponse> => {
                return b.List(toSnake(req)) as Promise<ListUsersResponse>;
            },
            enable: async (id: string): Promise<void> => {
                await b.Enable(id);
            },
            disable: async (id: string): Promise<void> => {
                await b.Disable(id);
            },
            getUserByCertThumbprint: async (thumbprint: string): Promise<User> => {
                return b.GetByCertThumbprint(thumbprint) as Promise<User>;
            },
            addCertBinding: async (
                userId: string,
                thumbprint: string,
                notAfterUnix?: number,
            ): Promise<User> => {
                return b.AddCertBinding(userId, thumbprint, notAfterUnix ?? 0) as Promise<User>;
            },
            removeCertBinding: async (
                userId: string,
                thumbprint: string,
            ): Promise<User> => {
                return b.RemoveCertBinding(userId, thumbprint) as Promise<User>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Roles
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get roles(): RoleClient {
        const b = wrapBindings(getBindings().main.RoleBinding);
        return {
            create: async (req: CreateRoleRequest): Promise<Role> => {
                return b.Create(toSnake(req)) as Promise<Role>;
            },
            get: async (id: string): Promise<Role> => {
                return b.Get(id) as Promise<Role>;
            },
            update: async (req: UpdateRoleRequest): Promise<Role> => {
                return b.Update(toSnake(req)) as Promise<Role>;
            },
            delete: async (id: string): Promise<void> => {
                await b.Delete(id);
            },
            list: async (req: ListRolesRequest): Promise<ListRolesResponse> => {
                return b.List(toSnake(req)) as Promise<ListRolesResponse>;
            },
            assign: async (userId: string, roleId: string): Promise<void> => {
                await b.Assign(userId, roleId);
            },
            unassign: async (userId: string, roleId: string): Promise<void> => {
                await b.Unassign(userId, roleId);
            },
            listPermissions: async (): Promise<ListPermissionsResponse> => {
                return b.ListPermissions() as Promise<ListPermissionsResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Certs
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get certs(): CertClient {
        const b = wrapBindings(getBindings().main.CertBinding);
        return {
            generate: async (req: GenerateCertRequest): Promise<CertBundle> => {
                return b.Generate(toSnake(req)) as Promise<CertBundle>;
            },
            export: async (req: ExportCertRequest): Promise<ExportCertResponse> => {
                return b.Export(toSnake(req)) as Promise<ExportCertResponse>;
            },
            revoke: async (serial: string): Promise<void> => {
                await b.Revoke(serial);
            },
            list: async (tenantId: string): Promise<ListCertsResponse> => {
                return b.List(tenantId) as Promise<ListCertsResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Database
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get database(): DatabaseClient {
        const b = wrapBindings(getBindings().main.DatabaseBinding);
        return {
            create: async (req: CreateDatabaseRequest): Promise<DatabaseInfo> => {
                return b.Create(req) as Promise<DatabaseInfo>;
            },
            get: async (name: string): Promise<DatabaseInfo> => {
                return b.Get(name) as Promise<DatabaseInfo>;
            },
            list: async (): Promise<DatabaseInfo[]> => {
                return b.List() as Promise<DatabaseInfo[]>;
            },
            drop: async (name: string): Promise<void> => {
                await b.Drop(name);
            },
            updateProtocol: async (name: string, protocol: DatabaseProtocolConfig): Promise<DatabaseInfo> => {
                // DatabaseBinding.UpdateProtocol takes the Go
                // UpdateDatabaseProtocolRequest, whose JSON is {mysql, postgresql}.
                const req: UpdateDatabaseProtocolRequest = {
                    mysql: protocol.mysql ?? false,
                    postgresql: protocol.postgresql ?? false,
                };
                return b.UpdateProtocol(name, req) as Promise<DatabaseInfo>;
            },
            updateSQL: async (name: string, sql?: DatabaseSQLConfig): Promise<DatabaseInfo> => {
                // DatabaseBinding.UpdateSQL takes the Go UpdateDatabaseSQLRequest,
                // whose JSON is {sql: {...}}; an absent sql restores the defaults.
                const req: UpdateDatabaseSQLRequest = sql === undefined ? {} : { sql };
                return b.UpdateSQL(name, req) as Promise<DatabaseInfo>;
            },
            updateCompaction: async (name: string, compaction?: DatabaseCompactionConfig): Promise<DatabaseInfo> => {
                // DatabaseBinding.UpdateCompaction takes the Go
                // UpdateDatabaseCompactionRequest, whose JSON is {compaction: {...}};
                // an absent compaction turns the policy off.
                const req: UpdateDatabaseCompactionRequest = compaction === undefined ? {} : { compaction };
                return b.UpdateCompaction(name, req) as Promise<DatabaseInfo>;
            },
            updateRaft: async (name: string, raft: DatabaseRaftConfig): Promise<DatabaseInfo> => {
                return b.UpdateRaft(name, raft) as Promise<DatabaseInfo>;
            },
            updateBarrier: async (name: string, barrier: DatabaseBarrierConfig): Promise<DatabaseInfo> => {
                return b.UpdateBarrier(name, barrier) as Promise<DatabaseInfo>;
            },
            updateConnection: async (name: string, connection: DatabaseConnectionConfig): Promise<DatabaseInfo> => {
                return b.UpdateConnection(name, connection) as Promise<DatabaseInfo>;
            },
            scale: async (name: string, targetReplicas: number): Promise<DatabaseInfo> => {
                return b.Scale(name, targetReplicas) as Promise<DatabaseInfo>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Collection
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get collection(): CollectionClient {
        const b = wrapBindings(getBindings().main.CollectionBinding);
        return {
            create: async (req: CreateCollectionRequest): Promise<CollectionInfo> => {
                return b.Create(toSnake(req)) as Promise<CollectionInfo>;
            },
            get: async (database: string, name: string): Promise<CollectionInfo> => {
                return b.Get(database, name) as Promise<CollectionInfo>;
            },
            list: async (database: string): Promise<CollectionInfo[]> => {
                const resp = await b.List(database) as { collections?: CollectionInfo[] } | null;
                return resp?.collections ?? [];
            },
            drop: async (database: string, name: string): Promise<void> => {
                await b.Drop(database, name);
            },
            insertDocument: async (req: InsertDocumentRequest): Promise<InsertDocumentResponse> => {
                return b.InsertDocument(toSnake(req)) as Promise<InsertDocumentResponse>;
            },
            getDocument: async (
                database: string,
                collection: string,
                id: string,
            ): Promise<DocumentResponse> => {
                return b.GetDocument(database, collection, id) as Promise<DocumentResponse>;
            },
            updateDocument: async (req: UpdateDocumentRequest): Promise<void> => {
                await b.UpdateDocument(toSnake(req));
            },
            deleteDocument: async (
                database: string,
                collection: string,
                id: string,
            ): Promise<void> => {
                await b.DeleteDocument(database, collection, id);
            },
            listDocuments: async (req: ListDocumentsRequest): Promise<ListDocumentsResponse> => {
                return b.ListDocuments(toSnake(req)) as Promise<ListDocumentsResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // SQL
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get sql(): SQLClient {
        const b = wrapBindings(getBindings().main.SQLBinding);
        return {
            query: async (query: string, database: string): Promise<SQLQueryResult> => {
                return b.Execute(query, database) as Promise<SQLQueryResult>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Seal
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get seal(): SealClient {
        const b = wrapBindings(getBindings().main.SealBinding);
        return {
            initialize: async (secret: string): Promise<void> => {
                await b.Initialize(secret);
            },
            initializeShamir: async (req: ShamirInitializeRequest): Promise<BootstrapResult> => {
                // SealBinding.InitializeShamir(threshold, totalShares, policy):
                // the desktop provisions PINs through its own settings, so the
                // PIN fields have no binding argument.
                return b.InitializeShamir(req.threshold, req.totalShares, req.policy ?? '') as Promise<BootstrapResult>;
            },
            unseal: async (secret: string): Promise<UnsealProgress> => {
                return b.Unseal(secret) as Promise<UnsealProgress>;
            },
            seal: async (): Promise<void> => {
                await b.Seal();
            },
            status: async (): Promise<SealStatus> => {
                return b.Status() as Promise<SealStatus>;
            },
            enableEncryption: async (req: EnableEncryptionRequest): Promise<EnableEncryptionResponse> => {
                // The desktop Go SealBinding exposes a matching EnableEncryption
                // method that delegates to the SDK seal client.
                return b.EnableEncryption(toSnake(req)) as Promise<EnableEncryptionResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Embedded Server
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get embeddedServer(): EmbeddedServerClient {
        const b = wrapBindings(getBindings().main.EmbeddedServerBinding);
        return {
            startEmbedded: async (config: EmbeddedConfig): Promise<void> => {
                await b.StartEmbedded(config);
                this.embeddedRunning = true;
            },
            stopEmbedded: async (): Promise<void> => {
                await b.StopEmbedded();
                this.embeddedRunning = false;
            },
            isEmbedded: (): boolean => {
                return this.embeddedRunning;
            },
            hasSavedConfig: async (): Promise<boolean> => {
                return b.HasSavedConfig() as Promise<boolean>;
            },
            restoreEmbedded: async (): Promise<void> => {
                await b.RestoreEmbedded();
                this.embeddedRunning = true;
            },
            clearSavedConfig: async (): Promise<void> => {
                await b.ClearSavedConfig();
            },
        };
    }

    // -----------------------------------------------------------------------
    // Daemon
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get daemon(): DaemonClient {
        const b = wrapBindings(getBindings().main.DaemonBinding);
        return {
            startDaemon: async (configPath: string): Promise<void> => {
                await b.Start(configPath);
            },
            stopDaemon: async (): Promise<void> => {
                await b.Stop();
            },
            restartDaemon: async (configPath: string): Promise<void> => {
                await b.Restart(configPath);
            },
            daemonStatus: async (): Promise<DaemonStatus> => {
                return b.Status() as Promise<DaemonStatus>;
            },
            daemonLogs: async (maxLines: number): Promise<string> => {
                return b.GetLogs(maxLines) as Promise<string>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Tenants
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get tenants(): TenantClient {
        const b = wrapBindings(getBindings().main.TenantBinding);
        return {
            create: async (req: CreateTenantRequest): Promise<CreateTenantResponse> => {
                return b.Create(toSnake(req)) as Promise<CreateTenantResponse>;
            },
            get: async (tenantId: string): Promise<GetTenantResponse> => {
                return b.Get(tenantId) as Promise<GetTenantResponse>;
            },
            list: async (): Promise<ListTenantsResponse> => {
                return b.List() as Promise<ListTenantsResponse>;
            },
            delete: async (tenantId: string): Promise<void> => {
                await b.Delete(tenantId);
            },
            update: async (tenantId: string, req: UpdateTenantRequest): Promise<UpdateTenantResponse> => {
                return b.Update(tenantId, toSnake(req)) as Promise<UpdateTenantResponse>;
            },
            barrierInit: async (req: TenantBarrierInitRequest): Promise<TenantBarrierInitResponse> => {
                // The desktop TenantBinding.BarrierInit returns only an error;
                // it does not surface Shamir shares. Per-tenant Shamir share
                // capture is therefore a REST-transport-only flow. We honor the
                // interface contract by returning a minimal response synthesized
                // from the request for the non-Shamir strategies.
                const result = await b.BarrierInit(toSnake(req));
                if (result !== undefined && result !== null) {
                    return result as TenantBarrierInitResponse;
                }
                return { tenantId: req.tenantId, strategy: req.strategy ?? '' };
            },
            barrierUnseal: async (req: TenantBarrierUnsealRequest): Promise<void> => {
                // The binding decodes transport.TenantBarrierUnsealRequest,
                // whose key is []byte: base64, as on REST.
                await b.BarrierUnseal({ tenant_id: req.tenantId, key: encodeBase64Utf8(req.key) });
            },
            barrierStatus: async (tenantId: string): Promise<TenantBarrierStatusResponse> => {
                return b.BarrierStatus(tenantId) as Promise<TenantBarrierStatusResponse>;
            },
            updateQuotas: async (req: UpdateTenantQuotasRequest): Promise<UpdateTenantQuotasResponse> => {
                return b.UpdateQuotas(toSnake(req)) as Promise<UpdateTenantQuotasResponse>;
            },
            updateEntitlements: async (req: UpdateTenantEntitlementsRequest): Promise<UpdateTenantEntitlementsResponse> => {
                return b.UpdateEntitlements(toSnake(req)) as Promise<UpdateTenantEntitlementsResponse>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Server Config
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get config(): ServerConfigClient {
        const b = wrapBindings(getBindings().main.ConfigBinding);
        return {
            initialized: async (): Promise<ConfigInitializedResponse> => {
                return b.Initialized() as Promise<ConfigInitializedResponse>;
            },
            isEmbeddedAvailable: async (): Promise<boolean> => {
                return b.IsEmbeddedAvailable() as Promise<boolean>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Backups
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get backups(): BackupClient {
        const b = wrapBindings(getBindings().main.BackupBinding);
        return {
            create: async (req: BackupRequest): Promise<BackupInfo> => {
                return b.Create(toSnake(req)) as Promise<BackupInfo>;
            },
            list: async (): Promise<ListBackupsResponse> => {
                return b.List() as Promise<ListBackupsResponse>;
            },
            get: async (id: string): Promise<BackupInfo> => {
                return b.Get(id) as Promise<BackupInfo>;
            },
            delete: async (id: string): Promise<void> => {
                await b.Delete(id);
            },
            restore: async (req: RestoreRequest): Promise<RestoreResult> => {
                return b.Restore(toSnake(req)) as Promise<RestoreResult>;
            },
            verify: async (id: string): Promise<VerifyResult> => {
                return b.Verify(id) as Promise<VerifyResult>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Rebalance
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get rebalance(): RebalanceClient {
        const b = wrapBindings(getBindings().main.RebalanceBinding);
        return {
            getShardStats: async (shardId: number): Promise<ShardStatsResponse> => {
                return b.GetShardStats(shardId) as Promise<ShardStatsResponse>;
            },
            createPlan: async (req: RebalancePlanRequest): Promise<RebalancePlan> => {
                return b.CreatePlan(toSnake(req)) as Promise<RebalancePlan>;
            },
            execute: async (plan: RebalancePlan): Promise<RebalanceResult> => {
                return b.Execute(toSnake(plan)) as Promise<RebalanceResult>;
            },
            getImbalanceReport: async (): Promise<ImbalanceReport> => {
                return b.GetImbalanceReport() as Promise<ImbalanceReport>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Credentials
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get credentials(): CredentialClient {
        const b = wrapBindings(getBindings().main.CredentialBinding);
        return {
            submit: async (name: string, value: string): Promise<void> => {
                await b.Submit(name, value);
            },
            store: async (name: string, value: string): Promise<void> => {
                await b.Store(name, value);
            },
            retrieve: async (name: string): Promise<string> => {
                return b.Retrieve(name) as Promise<string>;
            },
            list: async (): Promise<string[]> => {
                return b.List() as Promise<string[]>;
            },
            strategy: async (): Promise<string> => {
                return b.Strategy() as Promise<string>;
            },
            delete: async (name: string): Promise<void> => {
                await b.Delete(name);
            },
            rotate: async (name: string, newValue: string, gracePeriodSeconds?: number): Promise<CredentialRotateResult> => {
                return b.Rotate(toSnake({
                    name,
                    newValue,
                    gracePeriodSeconds: gracePeriodSeconds ?? 0,
                })) as Promise<CredentialRotateResult>;
            },
            listVersions: async (name: string): Promise<CredentialVersionInfo[]> => {
                interface VersionsResponse { readonly versions: CredentialVersionInfo[] }
                const resp = await b.ListVersions(name) as VersionsResponse;
                return resp.versions ?? [];
            },
            retrieveVersion: async (name: string, version: number): Promise<string> => {
                return b.RetrieveVersion(name, version) as Promise<string>;
            },
            gcExpired: async (name: string): Promise<number> => {
                return b.GCExpired(name) as Promise<number>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Audit
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get audit(): AuditQueryClient {
        const b = wrapBindings(getBindings().main.AuditBinding);
        return {
            query: async (f: AuditFilter = {}): Promise<AuditPage> => {
                return b.Query(toSnake(f)) as Promise<AuditPage>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Connection Manager
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get connectionManager(): ConnectionManagerClient {
        const b = wrapBindings(getBindings().main.ConnectionManagerBinding);
        return {
            listProfiles: async (): Promise<ConnectionProfile[]> => {
                return b.ListProfiles() as Promise<ConnectionProfile[]>;
            },
            saveProfile: async (profile: ConnectionProfile): Promise<void> => {
                await b.SaveProfile(profile);
            },
            deleteProfile: async (id: string): Promise<void> => {
                await b.DeleteProfile(id);
            },
            connect: async (profileId: string): Promise<ConnectionState> => {
                return b.Connect(profileId) as Promise<ConnectionState>;
            },
            disconnect: async (profileId: string): Promise<void> => {
                await b.Disconnect(profileId);
            },
            getState: async (profileId: string): Promise<ConnectionState | null> => {
                return b.GetState(profileId) as Promise<ConnectionState | null>;
            },
            setActive: async (profileId: string): Promise<void> => {
                await b.SetActive(profileId);
            },
            getActiveProfileId: async (): Promise<string> => {
                return b.GetActiveProfileID() as Promise<string>;
            },
            isEmbeddedAvailable: async (): Promise<boolean> => {
                return b.IsEmbeddedAvailable() as Promise<boolean>;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Share-holder administration (delegates to the Go OfficerBinding)
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get officers(): OfficerClient {
        const b = wrapBindings(getBindings().main.OfficerBinding);
        // A HolderScope selects the barrier: the system barrier in a domain,
        // or a tenant's barrier.
        const system = (domain?: string): Record<string, unknown> => ({ domain: domain ?? 'unseal' });
        const tenant = (tenantId: string): Record<string, unknown> => ({ tenant_id: tenantId });
        const list = async (scope: Record<string, unknown>): Promise<OfficerStatus[]> =>
            (((await b.ListHolders(scope)) ?? []) as OfficerStatus[]).map(normalizeOfficerStatus);
        return {
            registerOfficer: async (req: OfficerRegisterRequest): Promise<OfficerRegisterResponse> => {
                return b.RegisterHolder(system('unseal'), holderEnrolmentWire(req)) as Promise<OfficerRegisterResponse>;
            },
            registerRecoveryOfficer: async (req: OfficerRegisterRequest): Promise<OfficerRegisterResponse> => {
                return b.RegisterHolder(system('recovery'), holderEnrolmentWire(req)) as Promise<OfficerRegisterResponse>;
            },
            listOfficers: async (): Promise<OfficerStatus[]> => list(system('unseal')),
            startRekey: async (req: RekeyStartRequest): Promise<QuorumRound> => {
                return b.StartRekey({ ...system(req.domain), ...toSnake({ threshold: req.threshold, policy: req.policy, soPin: req.soPin }) }) as Promise<QuorumRound>;
            },
            revokeOfficer: async (req: OfficerRevokeRequest): Promise<QuorumRound> => {
                return b.RevokeHolder({ ...system(req.domain), ...toSnake({ username: req.username, threshold: req.threshold ?? 0, soPin: req.soPin }) }) as Promise<QuorumRound>;
            },
            registerTenantOfficer: async (req: TenantOfficerRegisterRequest): Promise<OfficerRegisterResponse> => {
                return b.RegisterHolder(tenant(req.tenantId), holderEnrolmentWire(req)) as Promise<OfficerRegisterResponse>;
            },
            listTenantOfficers: async (tenantId: string): Promise<OfficerStatus[]> => list(tenant(tenantId)),
            startTenantRekey: async (req: TenantRekeyStartRequest): Promise<QuorumRound> => {
                return b.StartRekey({ ...tenant(req.tenantId), threshold: req.threshold, policy: req.policy }) as Promise<QuorumRound>;
            },
            revokeTenantOfficer: async (req: TenantOfficerRevokeRequest): Promise<QuorumRound> => {
                return b.RevokeHolder({ ...tenant(req.tenantId), username: req.username, threshold: req.threshold ?? 0 }) as Promise<QuorumRound>;
            },
        };
    }
}
