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

import {
    decodeBase64Utf8,
    decodeMVCCKV,
    decodeOpResponses,
    decodeRestoreResult,
    decodeWatchEventKV,
    deepSnakeToCamel,
    deepCamelToSnake,
    distanceMetricToRestWire,
    encodeBase64Utf8,
    holderEnrolmentWire,
    normalizeDatabaseBarrierStatus,
    normalizeOfficerStatus,
    indexTypeToRestWire,
    normalizeVectorEnumFields,
    normalizeVectorIDField,
    vectorIDToWire,
} from './transform.js';
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
    CreateDatabaseRequest,
    CreateNamespaceRequest,
    DaemonStatus,
    DatabaseBarrierConfig,
    DatabaseBarrierStatus,
    DatabaseConnectionConfig,
    DatabaseRaftConfig,
    DatabaseProtocolConfig,
    DatabaseSQLConfig,
    UpdateDatabaseSQLRequest,
    DatabaseCompactionConfig,
    UpdateDatabaseCompactionRequest,
    NodeCompactionBody,
    CreateNamespaceResponse,
    CreateRoleRequest,
    CreateUserRequest,
    CollectionInfo,
    CreateCollectionRequest,
    CurrentRevisionResponse,
    DatabaseInfo,
    DocumentResponse,
    InsertDocumentRequest,
    InsertDocumentResponse,
    ListCollectionsResponse,
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
    KVListResponse,
    KVScanResponse,
    LeaseInfo,
    LeaseListResponse,
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
    ClusterSeal,
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
    TenantInfo,
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
    DatabaseOfficerRegisterRequest,
    DatabaseOfficerRevokeRequest,
    DatabaseRekeyStartRequest,
    DatabaseShamirInitializeRequest,
    DatabaseShamirInitializeResponse,
    TenantRekeyStartRequest,
    CredentialVersionInfo,
    CredentialRotateResult,
} from './types.js';
import {
    DesktopOnlyError,
    KeyNotFoundError,
    NotConnectedError,
    TransportError,
} from './errors.js';
import type { AuditFilter, AuditPage } from './audit.js';

// ---------------------------------------------------------------------------
// SDK version constant
// ---------------------------------------------------------------------------

/** Current SDK version string. */
const SDK_VERSION = '0.1.0';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Issues a fetch request and parses the JSON response body.
 * All object keys in the response are transformed from snake_case to camelCase
 * so that SDK consumers see the camelCase field names declared in types.ts.
 * Throws {@link TransportError} on non-OK status codes.
 */
async function requestJSON<T>(baseUrl: string, path: string, init?: RequestInit): Promise<T> {
    const url = baseUrl + path;
    const method = init?.method ?? 'GET';
    let resp: Response;
    try {
        resp = await fetch(url, init);
    } catch (err: unknown) {
        throw new TransportError(
            `${method} ${path}`,
            undefined,
            err instanceof Error ? err : new Error(String(err)),
        );
    }
    if (!resp.ok) {
        const body = await resp.text();
        throw new TransportError(
            `${method} ${path}`,
            resp.status,
            new Error(body),
        );
    }
    const raw = await resp.json();
    return deepSnakeToCamel(raw) as T;
}

/**
 * Issues a fetch request and returns nothing. Used for endpoints that
 * return 204 No Content or 200 with an ignored body.
 * Throws {@link TransportError} on non-OK status.
 */
async function requestVoid(baseUrl: string, path: string, init?: RequestInit): Promise<void> {
    const url = baseUrl + path;
    const method = init?.method ?? 'GET';
    let resp: Response;
    try {
        resp = await fetch(url, init);
    } catch (err: unknown) {
        throw new TransportError(
            `${method} ${path}`,
            undefined,
            err instanceof Error ? err : new Error(String(err)),
        );
    }
    if (!resp.ok) {
        const body = await resp.text();
        throw new TransportError(
            `${method} ${path}`,
            resp.status,
            new Error(body),
        );
    }
}

/**
 * Issues a fetch request and returns the response body as a raw text string.
 * Throws {@link TransportError} on non-OK status codes.
 */
async function requestText(baseUrl: string, path: string, init?: RequestInit): Promise<string> {
    const url = baseUrl + path;
    const method = init?.method ?? 'GET';
    let resp: Response;
    try {
        resp = await fetch(url, init);
    } catch (err: unknown) {
        throw new TransportError(
            `${method} ${path}`,
            undefined,
            err instanceof Error ? err : new Error(String(err)),
        );
    }
    if (!resp.ok) {
        const body = await resp.text();
        throw new TransportError(
            `${method} ${path}`,
            resp.status,
            new Error(body),
        );
    }
    return resp.text();
}

/**
 * HTTP header that selects the target database within the authenticated
 * tenant's shard. Mirrors the server-side `rest.HeaderDatabase` constant.
 */
const HEADER_DATABASE = 'X-QRDB-Database';

/**
 * Sentinel name for the server's implicit database. Scoping to it is identical
 * to being unscoped: the server uses the well-known `system` bootstrap database
 * whenever no header is sent.
 */
const DATABASE_DEFAULT_SCOPE = 'system';

/**
 * Path prefixes for data-plane endpoints (KV, MVCC, transaction, batch, watch,
 * lease, lock, secondary index, time-series, and vector). Only requests to
 * these paths carry the {@link HEADER_DATABASE} scope header. Management,
 * admin, and system endpoints (databases, tenants, users, roles, certs,
 * barrier, ceremony, seal, cluster, snapshot, audit, permissions, SQL) are
 * never database-scoped via the header.
 */
const DATA_PLANE_PREFIXES: readonly string[] = [
    '/kv',
    '/tx',
    '/batch',
    '/watch',
    '/lease',
    '/lock',
    '/index',
    '/ts',
    '/vector',
];

/**
 * Reports whether a request path targets a data-plane endpoint and therefore
 * should carry the database scope header. A prefix matches only on a path-
 * segment boundary (exact, `/` separator, or `?` query start) so that, e.g.,
 * `/kv` and `/kv/scan` match while an unrelated path beginning with the same
 * letters would not.
 */
function isDataPlanePath(path: string): boolean {
    return DATA_PLANE_PREFIXES.some(
        (prefix) =>
            path === prefix ||
            path.startsWith(`${prefix}/`) ||
            path.startsWith(`${prefix}?`),
    );
}

/** Builds a query string from a record of optional key-value pairs. */
function buildQuery(params: Record<string, string | number | boolean | undefined>): string {
    const parts: string[] = [];
    for (const [k, v] of Object.entries(params)) {
        if (v !== undefined && v !== '') {
            parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
        }
    }
    return parts.length > 0 ? `?${parts.join('&')}` : '';
}

/**
 * Encodes an object as a JSON request body with the appropriate content type
 * header. All object keys are recursively converted from camelCase to
 * snake_case before serialization so that the Go server's `json:"snake_case"`
 * struct tags are matched correctly.
 *
 * Keys that are already snake_case (e.g. hand-written membership payloads like
 * `{ shard_id, replica_id, address }`) round-trip safely because
 * `camelToSnakeKey` only transforms uppercase characters and leaves lowercase
 * underscore sequences unchanged.
 */
function jsonBody(data: unknown): RequestInit {
    return {
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(deepCamelToSnake(data)),
    };
}

/**
 * Builds a JSON request body from an object already in wire form, without
 * re-casing any key (see holderEnrolmentWire).
 */
function wireBody(data: Record<string, unknown>): RequestInit {
    return {
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
    };
}

// ---------------------------------------------------------------------------
// RestClient
// ---------------------------------------------------------------------------

/**
 * REST transport implementation of the QRDB {@link Client} interface.
 * Communicates with the QRDB REST API using the browser-native `fetch()` API.
 */
export class RestClient implements Client {
    private baseUrl: string;
    private connected = false;
    private serverVersion = '';

    /**
     * The active database this client targets. When non-empty, every
     * data-plane request carries an `X-QRDB-Database` header so the server
     * namespaces the operation under `{tenant}/{database}/`. An empty value
     * (the default) leaves the header off, so the server falls back to the
     * `default` database.
     */
    private readonly activeDatabase: string;

    /**
     * @param baseUrl - The base URL of the QRDB REST API (e.g. `https://localhost:8443`).
     *   Trailing slashes are stripped automatically.
     * @param activeDatabase - Optional database scope. When empty the client is
     *   unscoped and data-plane requests target the server's `default` database.
     */
    constructor(baseUrl: string, activeDatabase = '') {
        this.baseUrl = baseUrl.replace(/\/$/, '');
        this.activeDatabase = activeDatabase;
    }

    /** @inheritdoc */
    databaseScoped(name: string): Client {
        // `system` and the empty string both map to the unscoped client: the
        // server uses the `system` bootstrap database when no header is
        // present, so there is no header to send for either.
        const target = name === DATABASE_DEFAULT_SCOPE ? '' : name;
        if (target === this.activeDatabase) {
            return this;
        }
        // A lightweight clone that shares the live connection snapshot but
        // carries its own immutable database scope. No global mutable state is
        // involved, so scopes never leak across clients.
        const scoped = new RestClient(this.baseUrl, target);
        scoped.connected = this.connected;
        scoped.serverVersion = this.serverVersion;
        return scoped;
    }

    // -----------------------------------------------------------------------
    // Connection
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get connection(): ConnectionClient {
        return {
            connect: async (address: string): Promise<void> => {
                this.baseUrl = address.replace(/\/$/, '');
                try {
                    const health = await this.fetchJSON<HealthResponse>('/health');
                    if (health.status) {
                        this.connected = true;
                    }
                } catch (err: unknown) {
                    // A sealed server returns 503 on /health but is still
                    // reachable: its sealed-window listener serves the barrier
                    // endpoints (status/initialize/unseal). Treat this as
                    // connected-but-sealed so the seal init/unseal flow can
                    // proceed instead of surfacing a fatal connection error.
                    if (err instanceof TransportError && err.statusCode === 503) {
                        this.connected = true;
                    } else {
                        throw err;
                    }
                }
                // Attempt to retrieve server version from status endpoint.
                try {
                    const status = await this.fetchJSON<StatusResponse>('/status');
                    this.serverVersion = status.nodeId ?? '';
                } catch {
                    // Non-fatal: version remains empty.
                }
            },
            disconnect: async (): Promise<void> => {
                this.connected = false;
            },
            isConnected: (): boolean => {
                return this.connected;
            },
            getVersion: (): Record<string, string> => {
                return {
                    sdk: SDK_VERSION,
                    server: this.serverVersion,
                };
            },
        };
    }

    // -----------------------------------------------------------------------
    // KV
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get kv(): KVClient {
        return {
            put: async (key: string, value: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/kv/${encodeURIComponent(key)}`, {
                    method: 'PUT',
                    body: value,
                });
            },
            get: async (key: string): Promise<string> => {
                this.ensureConnected();
                try {
                    return await this.fetchText(`/kv/${encodeURIComponent(key)}`);
                } catch (err: unknown) {
                    if (err instanceof TransportError && err.statusCode === 404) {
                        throw new KeyNotFoundError(key);
                    }
                    throw err;
                }
            },
            delete: async (key: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/kv/${encodeURIComponent(key)}`, {
                    method: 'DELETE',
                });
            },
            list: async (prefix: string): Promise<string[]> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<KVListResponse>(`/kv${buildQuery({ prefix })}`,
                );
                return resp.keys as string[];
            },
            exists: async (key: string): Promise<boolean> => {
                this.ensureConnected();
                try {
                    await this.fetchText(`/kv/${encodeURIComponent(key)}`);
                    return true;
                } catch (err: unknown) {
                    if (err instanceof TransportError && err.statusCode === 404) {
                        return false;
                    }
                    throw err;
                }
            },
            scan: async (prefix: string): Promise<Record<string, string>> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<KVScanResponse>(`/kv/scan${buildQuery({ prefix })}`,
                );
                // Wire values are base64 (Go marshals the `map[string][]byte`
                // payload as base64). Decode each to plaintext so the result
                // matches the Wails client and the REST single-key get().
                const pairs: Record<string, string> = {};
                for (const [k, v] of Object.entries(resp.pairs)) {
                    pairs[k] = decodeBase64Utf8(v);
                }
                return pairs;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Admin
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get admin(): AdminClient {
        return {
            health: async (): Promise<HealthResponse> => {
                this.ensureConnected();
                return this.fetchJSON<HealthResponse>('/health');
            },
            status: async (): Promise<StatusResponse> => {
                this.ensureConnected();
                return this.fetchJSON<StatusResponse>('/status');
            },
            peers: async (): Promise<PeersResponse> => {
                this.ensureConnected();
                return this.fetchJSON<PeersResponse>('/peers');
            },
            getTopology: async (): Promise<TopologyResponse> => {
                this.ensureConnected();
                return this.fetchJSON<TopologyResponse>('/admin/topology');
            },
            nodeCompaction: async (): Promise<DatabaseCompactionConfig | undefined> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<NodeCompactionBody>('/admin/node-compaction');
                return resp.compaction;
            },
            setNodeCompaction: async (compaction?: DatabaseCompactionConfig): Promise<DatabaseCompactionConfig | undefined> => {
                this.ensureConnected();
                const req: NodeCompactionBody = compaction === undefined ? {} : { compaction };
                const resp = await this.fetchJSON<NodeCompactionBody>('/admin/node-compaction', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(req),
                });
                return resp.compaction;
            },
            addMember: async (
                shardId: number,
                replicaId: number,
                raftAddr: string,
            ): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/membership/add', {
                    method: 'POST',
                    ...jsonBody({ shard_id: shardId, replica_id: replicaId, address: raftAddr }),
                });
            },
            removeMember: async (shardId: number, replicaId: number): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/membership/remove', {
                    method: 'POST',
                    ...jsonBody({ shard_id: shardId, replica_id: replicaId }),
                });
            },
            requestSnapshot: async (shardId: number): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/snapshot', {
                    method: 'POST',
                    ...jsonBody({ shard_id: shardId }),
                });
            },
            drain: async (): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/drain', {
                    method: 'POST',
                });
            },
            capabilities: async (): Promise<CapabilitiesResponse> => {
                this.ensureConnected();
                return this.fetchJSON<CapabilitiesResponse>('/capabilities');
            },
        };
    }

    // -----------------------------------------------------------------------
    // MVCC
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get mvcc(): MVCCClient {
        return {
            putWithLease: async (
                key: string,
                value: string,
                leaseId: number,
            ): Promise<MVCCKeyValue> => {
                this.ensureConnected();
                const kv = await this.fetchJSON<MVCCKeyValue>(`/kv/${encodeURIComponent(key)}${buildQuery({ 'lease-id': leaseId })}`,
                    { method: 'PUT', body: value },
                );
                // Wire key/value are base64 (Go marshals MVCCKeyValue's []byte
                // fields that way); decode to plaintext for consistency with
                // kv.get/kv.scan.
                return decodeMVCCKV(kv);
            },
            getRevision: async (key: string): Promise<MVCCKeyValue> => {
                this.ensureConnected();
                const kv = await this.fetchJSON<MVCCKeyValue>(`/kv/${encodeURIComponent(key)}?revision=true`,
                );
                return decodeMVCCKV(kv);
            },
            getAtRevision: async (key: string, revision: number): Promise<MVCCKeyValue> => {
                this.ensureConnected();
                const kv = await this.fetchJSON<MVCCKeyValue>(`/kv/${encodeURIComponent(key)}${buildQuery({ 'at-revision': revision })}`,
                );
                return decodeMVCCKV(kv);
            },
            range: async (
                start: string,
                end: string,
                opts: RangeOptions,
            ): Promise<RangeResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<RangeResponse>(`/kv/range${buildQuery({
                        start,
                        end,
                        limit: opts.limit,
                        revision: opts.revision,
                        'count-only': opts.countOnly ? 'true' : undefined,
                        'keys-only': opts.keysOnly ? 'true' : undefined,
                        'sort-order': sortOrderToString(opts.sortOrder),
                        'sort-target': sortTargetToString(opts.sortTarget),
                    })}`,
                );
                return { ...resp, kvs: (resp.kvs ?? []).map(decodeMVCCKV) };
            },
            compact: async (revision: number): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/kv/compact', {
                    method: 'POST',
                    ...jsonBody({ revision }),
                });
            },
            currentRevision: async (): Promise<number> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<CurrentRevisionResponse>('/kv/revision',
                );
                return resp.revision;
            },
            restoreToRevision: async (
                req: RestoreRevisionRequest,
            ): Promise<RestoreRevisionResult> => {
                this.ensureConnected();
                // prefix/keys are plaintext in the public API; base64-encode
                // them here to match the server's binary-safe []byte field
                // convention (POST /kv/restore), the same wire contract
                // MVCCKeyValue's key/value fields use.
                const body: Record<string, unknown> = { revision: req.revision };
                if (req.prefix !== undefined) {
                    body.prefix = encodeBase64Utf8(req.prefix);
                }
                if (req.keys !== undefined) {
                    body.keys = req.keys.map(encodeBase64Utf8);
                }
                if (req.dryRun !== undefined) {
                    body.dry_run = req.dryRun;
                }
                if (req.force !== undefined) {
                    body.force = req.force;
                }
                const result = await this.fetchJSON<RestoreRevisionResult>('/kv/restore', {
                    method: 'POST',
                    ...jsonBody(body),
                });
                // The response's change/conflict "key" fields are base64 on
                // the wire (server-side []byte, see restoreKeyChangeResponse
                // in pkg/api/rest/handlers_mvcc.go); decode to plaintext for
                // consistency with kv.get/kv.scan/mvcc.getRevision.
                return decodeRestoreResult(result);
            },
        };
    }

    // -----------------------------------------------------------------------
    // Transaction
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get transaction(): TransactionClient {
        return {
            execute: async (req: TransactionRequest): Promise<TransactionResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<TransactionResponse>('/tx', {
                    method: 'POST',
                    ...jsonBody({
                        compares: req.compares.map(serializeCompare),
                        success_ops: req.successOps.map(serializeOp),
                        failure_ops: req.failureOps.map(serializeOp),
                    }),
                });
                // Get/Range ops within the transaction return MVCCKeyValue
                // results whose key/value are base64 on the wire; decode them
                // to plaintext for consistency with kv.get/kv.scan.
                return { ...resp, responses: decodeOpResponses(resp.responses) };
            },
        };
    }

    // -----------------------------------------------------------------------
    // Batch
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get batch(): BatchClient {
        return {
            execute: async (ops: readonly Op[]): Promise<BatchResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<BatchResponse>('/batch', {
                    method: 'POST',
                    ...jsonBody({ ops }),
                });
                // Get/Range ops within the batch return MVCCKeyValue results
                // whose key/value are base64 on the wire; decode them to
                // plaintext for consistency with kv.get/kv.scan.
                return { ...resp, responses: decodeOpResponses(resp.responses) };
            },
            importFile: async (_filePath: string, _opts: ImportOptions): Promise<ImportResult> => {
                throw new DesktopOnlyError('batch.importFile');
            },
            availableCodecs: async (): Promise<readonly string[]> => {
                throw new DesktopOnlyError('batch.availableCodecs');
            },
            availableImportFormats: async (): Promise<readonly string[]> => {
                throw new DesktopOnlyError('batch.availableImportFormats');
            },
        };
    }

    // -----------------------------------------------------------------------
    // Watch (Server-Sent Events)
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get watch(): WatchClient {
        return {
            watch: async (key: string, opts: WatchOptions): Promise<WatchHandle> => {
                this.ensureConnected();
                return this.createSSEWatchHandle(
                    buildQuery({
                        key,
                        'start-revision': opts.startRevision,
                        'prev-kv': opts.prevKV ? 'true' : undefined,
                        'progress-notify': opts.progressNotify ? 'true' : undefined,
                    }),
                );
            },
            watchPrefix: async (prefix: string, opts: WatchOptions): Promise<WatchHandle> => {
                this.ensureConnected();
                return this.createSSEWatchHandle(
                    buildQuery({
                        key: prefix,
                        prefix: 'true',
                        'start-revision': opts.startRevision,
                        'prev-kv': opts.prevKV ? 'true' : undefined,
                        'progress-notify': opts.progressNotify ? 'true' : undefined,
                    }),
                );
            },
        };
    }

    /**
     * Creates a {@link WatchHandle} backed by a Server-Sent Events stream.
     * The SSE stream is consumed in the background and events are dispatched
     * to registered callbacks.
     */
    private createSSEWatchHandle(query: string): WatchHandle {
        const controller = new AbortController();
        const eventCallbacks: Array<(events: readonly WatchEvent[]) => void> = [];
        const cancelCallbacks: Array<(response: WatchResponse) => void> = [];
        let watchId = 0;

        const url = `${this.baseUrl}/watch${query}`;

        // Start the SSE stream asynchronously.
        void this.consumeSSE(
            url,
            controller.signal,
            (response: WatchResponse) => {
                if (watchId === 0 && response.watchId > 0) {
                    watchId = response.watchId;
                }
                if (response.canceled) {
                    for (const cb of cancelCallbacks) {
                        cb(response);
                    }
                } else if (response.events.length > 0) {
                    for (const cb of eventCallbacks) {
                        cb(response.events);
                    }
                }
            },
        );

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
                controller.abort();
            },
        };
    }

    /**
     * Consumes a Server-Sent Events stream from the given URL and dispatches
     * parsed events to the callback. The stream is terminated when the abort
     * signal fires or the server closes the connection.
     */
    private async consumeSSE(
        url: string,
        signal: AbortSignal,
        callback: (response: WatchResponse) => void,
    ): Promise<void> {
        try {
            // Watch is a data-plane stream, so it carries the database scope
            // header when this client is scoped (the helper returns undefined
            // for unscoped clients, leaving the header off).
            const resp = await fetch(url, { signal, headers: this.scopeHeaders('/watch') });
            if (!resp.ok) {
                const body = await resp.text();
                throw new TransportError(
                    'GET /watch',
                    resp.status,
                    new Error(body),
                );
            }
            if (!resp.body) {
                return;
            }

            const reader = resp.body.getReader();
            const decoder = new TextDecoder();
            let buffer = '';

            for (;;) {
                const { done, value } = await reader.read();
                if (done) {
                    break;
                }

                buffer += decoder.decode(value, { stream: true });

                // SSE frames are delimited by double newlines.
                const frames = buffer.split('\n\n');
                // Keep the last potentially-incomplete frame in the buffer.
                buffer = frames.pop() ?? '';

                for (const frame of frames) {
                    if (!frame.trim()) {
                        continue;
                    }
                    // Extract "data: {...}" lines.
                    for (const line of frame.split('\n')) {
                        if (line.startsWith('data: ')) {
                            const json = line.slice(6);
                            try {
                                const event = deepSnakeToCamel(
                                    JSON.parse(json) as unknown,
                                ) as WatchResponse;
                                normalizeWatchEventTypes(event);
                                // Each event's kv/prevKv carries base64
                                // key/value on the wire (Go marshals
                                // MVCCKeyValue's []byte fields that way);
                                // decode to plaintext so WatchEvent consumers
                                // (e.g. the GUI's WatchView) see the same
                                // plaintext kv.get/kv.scan already return.
                                callback({
                                    ...event,
                                    events: (event.events ?? []).map(decodeWatchEventKV),
                                });
                            } catch {
                                // Skip malformed JSON frames.
                            }
                        }
                    }
                }
            }
        } catch (err: unknown) {
            // AbortError is expected when the caller cancels the watch.
            if (err instanceof DOMException && err.name === 'AbortError') {
                return;
            }
            throw err;
        }
    }

    // -----------------------------------------------------------------------
    // Lease
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get lease(): LeaseClient {
        return {
            grant: async (ttl: number): Promise<LeaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<LeaseInfo>('/lease/grant',
                    { method: 'POST', ...jsonBody({ ttl }) },
                );
            },
            revoke: async (leaseId: number): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/lease/revoke', {
                    method: 'POST',
                    ...jsonBody({ lease_id: leaseId }),
                });
            },
            keepAlive: async (leaseId: number): Promise<LeaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<LeaseInfo>('/lease/keepalive',
                    { method: 'POST', ...jsonBody({ lease_id: leaseId }) },
                );
            },
            timeToLive: async (leaseId: number): Promise<LeaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<LeaseInfo>(`/lease/ttl${buildQuery({ lease_id: leaseId })}`,
                );
            },
            list: async (): Promise<readonly LeaseInfo[]> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<LeaseListResponse>('/lease/list',
                );
                return resp.leases;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Lock
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get lock(): LockClient {
        return {
            acquire: async (req: LockAcquireRequest): Promise<LockInfo> => {
                this.ensureConnected();
                return this.fetchJSON<LockInfo>('/lock/acquire',
                    { method: 'POST', ...jsonBody(req) },
                );
            },
            tryLock: async (req: LockAcquireRequest): Promise<LockInfo> => {
                this.ensureConnected();
                return this.fetchJSON<LockInfo>('/lock/trylock',
                    { method: 'POST', ...jsonBody(req) },
                );
            },
            release: async (req: LockReleaseRequest): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/lock/release', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            extend: async (req: LockExtendRequest): Promise<LockInfo> => {
                this.ensureConnected();
                return this.fetchJSON<LockInfo>('/lock/extend',
                    { method: 'POST', ...jsonBody(req) },
                );
            },
            status: async (key: string): Promise<LockInfo | null> => {
                this.ensureConnected();
                try {
                    return await this.fetchJSON<LockInfo>(`/lock/status${buildQuery({ key })}`,
                    );
                } catch (err: unknown) {
                    if (err instanceof TransportError && err.statusCode === 404) {
                        return null;
                    }
                    throw err;
                }
            },
            list: async (prefix?: string): Promise<LockListResponse> => {
                this.ensureConnected();
                return this.fetchJSON<LockListResponse>(`/lock/list${prefix ? buildQuery({ prefix }) : ''}`,
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Index
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get index(): IndexClient {
        return {
            create: async (req: IndexCreateRequest): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/index/create', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            drop: async (name: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/index/drop', {
                    method: 'POST',
                    ...jsonBody({ name }),
                });
            },
            query: async (indexName: string, value: string): Promise<IndexQueryResponse> => {
                this.ensureConnected();
                return this.fetchJSON<IndexQueryResponse>(`/index/query${buildQuery({ index_name: indexName, value })}`,
                );
            },
            scan: async (
                indexName: string,
                start: string,
                end: string,
            ): Promise<IndexScanResponse> => {
                this.ensureConnected();
                // The REST endpoint serializes entries as a JSON object
                // (Go map[string][]string), not the IndexScanEntry[] array
                // the SDK contract declares (which matches the desktop/Wails
                // transport). Convert it here so callers can always iterate
                // scanResult.entries as an array regardless of transport.
                const raw = await this.fetchJSON<{ entries?: Record<string, string[]> }>(
                    `/index/scan${buildQuery({ index_name: indexName, start, end })}`,
                );
                const entries = raw.entries ?? {};
                return {
                    entries: Object.entries(entries).map(([value, keys]) => ({
                        value,
                        keys: keys ?? [],
                    })),
                };
            },
            list: async (): Promise<IndexListResponse> => {
                this.ensureConnected();
                return this.fetchJSON<IndexListResponse>('/index/list');
            },
        };
    }

    // -----------------------------------------------------------------------
    // TimeSeries
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get timeSeries(): TimeSeriesClient | null {
        return {
            write: async (point: TSDataPoint): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/ts/write', {
                    method: 'POST',
                    ...jsonBody({
                        metric: point.metric,
                        value: point.value,
                        timestamp: point.timestampNs,
                        tags: tagsArrayToMap(point.tags),
                    }),
                });
            },
            writeBatch: async (points: readonly TSDataPoint[]): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/ts/write-batch', {
                    method: 'POST',
                    ...jsonBody({
                        points: points.map((p) => ({
                            metric: p.metric,
                            value: p.value,
                            timestamp: p.timestampNs,
                            tags: tagsArrayToMap(p.tags),
                        })),
                    }),
                });
            },
            query: async (req: TSQueryRequest): Promise<TSQueryResponse> => {
                this.ensureConnected();
                const tagsStr = req.tags
                    ? req.tags.map((t) => `${t.key}=${t.value}`).join(',')
                    : undefined;
                interface RawTSPoint {
                    metric: string;
                    value: number;
                    timestampNs: number;
                    tags?: Record<string, string>;
                }
                const raw = await this.fetchJSON<{ points: RawTSPoint[] }>(`/ts/query${buildQuery({
                        metric: req.metric,
                        start_ns: req.startNs,
                        end_ns: req.endNs,
                        tags: tagsStr,
                        limit: req.limit,
                        descending: req.descending ? 'true' : undefined,
                    })}`,
                );
                return {
                    points: (raw.points ?? []).map((p) => ({
                        metric: p.metric,
                        timestampNs: p.timestampNs,
                        value: p.value,
                        tags: p.tags
                            ? Object.entries(p.tags).map(([key, value]) => ({ key, value }))
                            : [],
                    })),
                };
            },
            aggregate: async (req: TSAggregateRequest): Promise<TSAggregateResponse> => {
                this.ensureConnected();
                return this.fetchJSON<TSAggregateResponse>(`/ts/aggregate${buildQuery({
                        metric: req.metric,
                        start_ns: req.startNs,
                        end_ns: req.endNs,
                        function: aggFuncToString(req.function),
                        interval_ns: req.intervalNs,
                    })}`,
                );
            },
            writePoint: async (point: TSDataPoint): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/ts/write', {
                    method: 'POST',
                    ...jsonBody({
                        metric: point.metric,
                        value: point.value,
                        timestamp: point.timestampNs,
                        tags: tagsArrayToMap(point.tags),
                    }),
                });
            },
            richQuery: async (req: TSRichQueryRequest): Promise<TSRichQueryResponse> => {
                this.ensureConnected();
                return this.fetchJSON<TSRichQueryResponse>('/ts/rich-query', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            deleteSeries: async (req: TSDeleteRequest): Promise<TSDeleteResponse> => {
                this.ensureConnected();
                return this.fetchJSON<TSDeleteResponse>('/ts/delete', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            getRetention: async (req: TSGetRetentionRequest): Promise<TSGetRetentionResponse> => {
                this.ensureConnected();
                return this.fetchJSON<TSGetRetentionResponse>(`/ts/retention${buildQuery({ metric: req.metric })}`,
                );
            },
            setRetention: async (req: TSSetRetentionRequest): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/ts/retention', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            listMetrics: async (req: TSListMetricsRequest): Promise<TSListMetricsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<TSListMetricsResponse>(`/ts/metrics${buildQuery({
                        prefix: req.prefix,
                        limit: req.limit,
                    })}`,
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Vector
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get vector(): VectorClient | null {
        return {
            createNamespace: async (
                req: CreateNamespaceRequest,
            ): Promise<CreateNamespaceResponse> => {
                this.ensureConnected();
                // The Go REST handler (vectorCreateNamespaceRequest) expects
                // distance_metric / index_type as canonical service strings,
                // not the SDK's numeric proto enums, and the index parameters
                // under index_config.
                return this.fetchJSON<CreateNamespaceResponse>('/vector/namespace',
                    {
                        method: 'POST',
                        ...jsonBody({
                            namespace: req.namespace,
                            dimension: req.dimension,
                            distance_metric: distanceMetricToRestWire(req.distanceMetric),
                            index_type: indexTypeToRestWire(req.indexType),
                            index_config: req.indexParams,
                            shard_id: req.shardId,
                        }),
                    },
                );
            },
            deleteNamespace: async (req: DeleteNamespaceRequest): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/vector/namespace/delete',
                    { method: 'POST', ...jsonBody(req) },
                );
            },
            listNamespaces: async (
                req: ListNamespacesRequest,
            ): Promise<ListNamespacesResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<ListNamespacesResponse>(`/vector/namespaces${buildQuery({ shard_id: req.shardId })}`,
                );
                // Namespace entries that arrive as info objects carry wire
                // enum strings; convert them to the SDK's numeric enums.
                return {
                    ...resp,
                    namespaces: (resp.namespaces ?? []).map(normalizeVectorEnumFields),
                };
            },
            getNamespaceInfo: async (req: GetNamespaceInfoRequest): Promise<NamespaceInfo> => {
                this.ensureConnected();
                const info = await this.fetchJSON<NamespaceInfo>(`/vector/namespace/info${buildQuery({
                        namespace: req.namespace,
                        shard_id: req.shardId,
                    })}`,
                );
                return normalizeVectorEnumFields(info);
            },
            upsertVector: async (req: UpsertVectorRequest): Promise<UpsertVectorResponse> => {
                this.ensureConnected();
                // The Go REST handler carries vector IDs as uint64 JSON
                // numbers; the SDK's string IDs are converted on the wire.
                return this.fetchJSON<UpsertVectorResponse>('/vector/upsert', {
                    method: 'POST',
                    ...jsonBody({ ...req, id: vectorIDToWire(req.id) }),
                });
            },
            getVector: async (req: GetVectorRequest): Promise<VectorData> => {
                this.ensureConnected();
                const vec = await this.fetchJSON<VectorData>(`/vector/get${buildQuery({
                        namespace: req.namespace,
                        id: req.id,
                        consistency: req.consistency,
                        shard_id: req.shardId,
                    })}`,
                );
                return normalizeVectorIDField(vec);
            },
            deleteVector: async (req: DeleteVectorRequest): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/vector/delete', {
                    method: 'POST',
                    ...jsonBody({ ...req, id: vectorIDToWire(req.id) }),
                });
            },
            batchUpsert: async (req: BatchUpsertRequest): Promise<BatchUpsertResponse> => {
                this.ensureConnected();
                return this.fetchJSON<BatchUpsertResponse>('/vector/batch/upsert',
                    {
                        method: 'POST',
                        ...jsonBody({
                            ...req,
                            vectors: req.vectors.map((v) => ({ ...v, id: vectorIDToWire(v.id) })),
                        }),
                    },
                );
            },
            batchDeleteVectors: async (
                req: BatchDeleteVectorsRequest,
            ): Promise<BatchDeleteVectorsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<BatchDeleteVectorsResponse>('/vector/batch/delete',
                    {
                        method: 'POST',
                        ...jsonBody({ ...req, ids: req.ids.map(vectorIDToWire) }),
                    },
                );
            },
            listVectors: async (req: ListVectorsRequest): Promise<ListVectorsResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<ListVectorsResponse>(`/vector/list${buildQuery({
                        namespace: req.namespace,
                        limit: req.limit,
                        offset: req.offset,
                        cursor: req.cursor,
                        shard_id: req.shardId,
                    })}`,
                );
                return {
                    ...resp,
                    vectors: (resp.vectors ?? []).map(normalizeVectorIDField),
                };
            },
            updateMetadata: async (req: UpdateMetadataRequest): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/vector/metadata', {
                    method: 'POST',
                    ...jsonBody({ ...req, id: vectorIDToWire(req.id) }),
                });
            },
            searchSimilar: async (
                req: SearchSimilarRequest,
            ): Promise<SearchSimilarResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<SearchSimilarResponse>('/vector/search/similar',
                    { method: 'POST', ...jsonBody(req) },
                );
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            searchByMetadata: async (
                req: SearchByMetadataRequest,
            ): Promise<SearchByMetadataResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<SearchByMetadataResponse>('/vector/search/metadata',
                    { method: 'POST', ...jsonBody(req) },
                );
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            hybridSearch: async (
                req: HybridSearchRequest,
            ): Promise<HybridSearchResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<HybridSearchResponse>('/vector/search/hybrid',
                    { method: 'POST', ...jsonBody(req) },
                );
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            searchByID: async (req: SearchByIDRequest): Promise<SearchSimilarResponse> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<SearchSimilarResponse>('/vector/search/by-id',
                    {
                        method: 'POST',
                        ...jsonBody({ ...req, id: vectorIDToWire(req.id) }),
                    },
                );
                return { ...resp, results: (resp.results ?? []).map(normalizeVectorIDField) };
            },
            rebuildIndex: async (
                req: RebuildIndexRequest,
            ): Promise<RebuildIndexResponse> => {
                this.ensureConnected();
                return this.fetchJSON<RebuildIndexResponse>('/vector/index/rebuild',
                    { method: 'POST', ...jsonBody(req) },
                );
            },
            getRebuildProgress: async (
                req: GetRebuildProgressRequest,
            ): Promise<RebuildProgress> => {
                this.ensureConnected();
                return this.fetchJSON<RebuildProgress>(`/vector/index/rebuild/progress${buildQuery({
                        namespace: req.namespace,
                        job_id: req.jobId,
                        shard_id: req.shardId,
                    })}`,
                );
            },
            getIndexStats: async (req: GetIndexStatsRequest): Promise<IndexStats> => {
                this.ensureConnected();
                const stats = await this.fetchJSON<IndexStats>(`/vector/index/stats${buildQuery({
                        namespace: req.namespace,
                        shard_id: req.shardId,
                    })}`,
                );
                return normalizeVectorEnumFields(stats);
            },
            compactNamespace: async (
                req: CompactNamespaceRequest,
            ): Promise<CompactNamespaceResponse> => {
                this.ensureConnected();
                return this.fetchJSON<CompactNamespaceResponse>('/vector/compact',
                    { method: 'POST', ...jsonBody(req) },
                );
            },
            getCompactionProgress: async (
                req: GetCompactionProgressRequest,
            ): Promise<CompactionProgress> => {
                this.ensureConnected();
                return this.fetchJSON<CompactionProgress>(`/vector/compact/progress${buildQuery({
                        namespace: req.namespace,
                        job_id: req.jobId,
                        shard_id: req.shardId,
                    })}`,
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Users
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get users(): UserClient {
        return {
            create: async (req: CreateUserRequest): Promise<User> => {
                this.ensureConnected();
                return this.fetchJSON<User>('/users', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            get: async (id: string): Promise<User> => {
                this.ensureConnected();
                return this.fetchJSON<User>(`/users/${encodeURIComponent(id)}`,
                );
            },
            update: async (req: UpdateUserRequest): Promise<User> => {
                this.ensureConnected();
                return this.fetchJSON<User>(`/users/${encodeURIComponent(req.id)}`,
                    { method: 'PUT', ...jsonBody(req) },
                );
            },
            delete: async (id: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/users/${encodeURIComponent(id)}`,
                    { method: 'DELETE' },
                );
            },
            list: async (req: ListUsersRequest): Promise<ListUsersResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ListUsersResponse>(`/users/list${buildQuery({
                        tenantID: req.tenantId,
                        page: req.page,
                        pageSize: req.pageSize,
                    })}`,
                );
            },
            enable: async (id: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/users/${encodeURIComponent(id)}/enable`,
                    { method: 'POST' },
                );
            },
            disable: async (id: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/users/${encodeURIComponent(id)}/disable`,
                    { method: 'POST' },
                );
            },
            getUserByCertThumbprint: async (thumbprint: string): Promise<User> => {
                this.ensureConnected();
                return this.fetchJSON<User>(`/users/by-cert-thumbprint/${encodeURIComponent(thumbprint)}`,
                );
            },
            addCertBinding: async (
                userId: string,
                thumbprint: string,
                notAfterUnix?: number,
            ): Promise<User> => {
                this.ensureConnected();
                return this.fetchJSON<User>(`/users/${encodeURIComponent(userId)}/cert-bindings`,
                    { method: 'POST', ...jsonBody({ thumbprint, notAfterUnix }) },
                );
            },
            removeCertBinding: async (
                userId: string,
                thumbprint: string,
            ): Promise<User> => {
                this.ensureConnected();
                return this.fetchJSON<User>(
                    `/users/${encodeURIComponent(userId)}/cert-bindings/${encodeURIComponent(thumbprint)}`,
                    { method: 'DELETE' },
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Roles
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get roles(): RoleClient {
        return {
            create: async (req: CreateRoleRequest): Promise<Role> => {
                this.ensureConnected();
                return this.fetchJSON<Role>('/roles', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            get: async (id: string): Promise<Role> => {
                this.ensureConnected();
                return this.fetchJSON<Role>(`/roles/${encodeURIComponent(id)}`,
                );
            },
            update: async (req: UpdateRoleRequest): Promise<Role> => {
                this.ensureConnected();
                return this.fetchJSON<Role>(`/roles/${encodeURIComponent(req.id)}`,
                    { method: 'PUT', ...jsonBody(req) },
                );
            },
            delete: async (id: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/roles/${encodeURIComponent(id)}`,
                    { method: 'DELETE' },
                );
            },
            list: async (req: ListRolesRequest): Promise<ListRolesResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ListRolesResponse>(`/roles/list${buildQuery({
                        tenantID: req.tenantId,
                        page: req.page,
                        pageSize: req.pageSize,
                    })}`,
                );
            },
            assign: async (userId: string, roleId: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/roles/assign', {
                    method: 'POST',
                    ...jsonBody({ userId, roleId }),
                });
            },
            unassign: async (userId: string, roleId: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/roles/unassign', {
                    method: 'POST',
                    ...jsonBody({ userId, roleId }),
                });
            },
            listPermissions: async (): Promise<ListPermissionsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ListPermissionsResponse>('/permissions',
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Certs
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get certs(): CertClient {
        return {
            generate: async (req: GenerateCertRequest): Promise<CertBundle> => {
                this.ensureConnected();
                return this.fetchJSON<CertBundle>('/certs/generate', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            export: async (req: ExportCertRequest): Promise<ExportCertResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ExportCertResponse>('/certs/export', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            revoke: async (serial: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/certs/revoke', {
                    method: 'POST',
                    ...jsonBody({ serial }),
                });
            },
            list: async (tenantId: string): Promise<ListCertsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ListCertsResponse>(`/certs/list${buildQuery({ tenantID: tenantId })}`,
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Database
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get database(): DatabaseClient {
        return {
            create: async (req: CreateDatabaseRequest): Promise<DatabaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<DatabaseInfo>('/v1/databases', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(req),
                });
            },
            get: async (name: string): Promise<DatabaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<DatabaseInfo>(`/v1/databases/${encodeURIComponent(name)}`,
                );
            },
            list: async (): Promise<DatabaseInfo[]> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<{ databases: DatabaseInfo[] }>('/v1/databases/list',
                );
                return resp.databases;
            },
            drop: async (name: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/v1/databases/${encodeURIComponent(name)}`,
                    { method: 'DELETE' },
                );
            },
            updateProtocol: async (name: string, protocol: DatabaseProtocolConfig): Promise<DatabaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<DatabaseInfo>(`/v1/databases/${encodeURIComponent(name)}/protocol`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            mysql: protocol.mysql ?? false,
                            postgresql: protocol.postgresql ?? false,
                        }),
                    },
                );
            },
            updateSQL: async (name: string, sql?: DatabaseSQLConfig): Promise<DatabaseInfo> => {
                this.ensureConnected();
                const req: UpdateDatabaseSQLRequest = sql === undefined ? {} : { sql };
                return this.fetchJSON<DatabaseInfo>(`/v1/databases/${encodeURIComponent(name)}/sql`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(req),
                    },
                );
            },
            updateCompaction: async (name: string, compaction?: DatabaseCompactionConfig): Promise<DatabaseInfo> => {
                this.ensureConnected();
                const req: UpdateDatabaseCompactionRequest = compaction === undefined ? {} : { compaction };
                return this.fetchJSON<DatabaseInfo>(`/v1/databases/${encodeURIComponent(name)}/compaction`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(req),
                    },
                );
            },
            updateRaft: async (name: string, raft: DatabaseRaftConfig): Promise<DatabaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<DatabaseInfo>(`/databases/${encodeURIComponent(name)}/raft`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(raft),
                    },
                );
            },
            updateBarrier: async (name: string, barrier: DatabaseBarrierConfig): Promise<DatabaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<DatabaseInfo>(`/databases/${encodeURIComponent(name)}/barrier`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(barrier),
                    },
                );
            },
            barrierStatus: async (name: string): Promise<DatabaseBarrierStatus> => {
                this.ensureConnected();
                return normalizeDatabaseBarrierStatus(await this.fetchJSON<DatabaseBarrierStatus>(
                    `/v1/databases/${encodeURIComponent(name)}/barrier/status`,
                ));
            },
            updateConnection: async (name: string, connection: DatabaseConnectionConfig): Promise<DatabaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<DatabaseInfo>(`/databases/${encodeURIComponent(name)}/connection`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(connection),
                    },
                );
            },
            scale: async (name: string, targetReplicas: number): Promise<DatabaseInfo> => {
                this.ensureConnected();
                return this.fetchJSON<DatabaseInfo>(`/v1/databases/${encodeURIComponent(name)}/scale`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ target_replicas: targetReplicas }),
                    },
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Collection
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get collection(): CollectionClient {
        return {
            create: async (req: CreateCollectionRequest): Promise<CollectionInfo> => {
                this.ensureConnected();
                return this.fetchJSON<CollectionInfo>('/v1/collections', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(req),
                });
            },
            get: async (database: string, name: string): Promise<CollectionInfo> => {
                this.ensureConnected();
                return this.fetchJSON<CollectionInfo>(`/v1/collections/${encodeURIComponent(database)}/${encodeURIComponent(name)}`,
                );
            },
            list: async (database: string): Promise<CollectionInfo[]> => {
                this.ensureConnected();
                const resp = await this.fetchJSON<ListCollectionsResponse>(`/v1/collections/list${buildQuery({ database })}`,
                );
                return resp.collections as CollectionInfo[];
            },
            drop: async (database: string, name: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/v1/collections/${encodeURIComponent(database)}/${encodeURIComponent(name)}`,
                    { method: 'DELETE' },
                );
            },
            insertDocument: async (req: InsertDocumentRequest): Promise<InsertDocumentResponse> => {
                this.ensureConnected();
                return this.fetchJSON<InsertDocumentResponse>(`/v1/collections/${encodeURIComponent(req.database)}/${encodeURIComponent(req.collection)}/documents`,
                    {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ document: req.document }),
                    },
                );
            },
            getDocument: async (
                database: string,
                collection: string,
                id: string,
            ): Promise<DocumentResponse> => {
                this.ensureConnected();
                return this.fetchJSON<DocumentResponse>(`/v1/collections/${encodeURIComponent(database)}/${encodeURIComponent(collection)}/documents/${encodeURIComponent(String(id))}`,
                );
            },
            updateDocument: async (req: UpdateDocumentRequest): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/v1/collections/${encodeURIComponent(req.database)}/${encodeURIComponent(req.collection)}/documents/${encodeURIComponent(String(req.id))}`,
                    {
                        method: 'PUT',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ document: req.document }),
                    },
                );
            },
            deleteDocument: async (
                database: string,
                collection: string,
                id: string,
            ): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/v1/collections/${encodeURIComponent(database)}/${encodeURIComponent(collection)}/documents/${encodeURIComponent(String(id))}`,
                    { method: 'DELETE' },
                );
            },
            listDocuments: async (req: ListDocumentsRequest): Promise<ListDocumentsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ListDocumentsResponse>(`/v1/collections/${encodeURIComponent(req.database)}/${encodeURIComponent(req.collection)}/documents${buildQuery({
                        page_size: req.page_size,
                        page_token: req.page_token,
                    })}`,
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // SQL
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get sql(): SQLClient {
        return {
            query: async (query: string, database: string): Promise<SQLQueryResult> => {
                this.ensureConnected();
                return this.fetchJSON<SQLQueryResult>('/sql/query', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ query, database }),
                });
            },
        };
    }

    // -----------------------------------------------------------------------
    // Seal
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get seal(): SealClient {
        return {
            initialize: async (secret: string): Promise<void> => {
                this.ensureConnected();
                // Passphrase (or none, when secret is "") barrier bootstrap.
                // The wire field is `[]byte` on the server so the caller can
                // zeroize it there; a `[]byte` is base64 in REST JSON, which is
                // the same convention `[]byte` fields use everywhere else in
                // this SDK. The SDK-facing argument stays plaintext.
                await this.fetchVoid('/v1/barrier/initialize', {
                    method: 'POST',
                    ...jsonBody({ secret: encodeBase64Utf8(secret) }),
                });
            },
            initializeShamir: async (req: ShamirInitializeRequest): Promise<BootstrapResult> => {
                this.ensureConnected();
                // The unseal key is split to the enrolled holders and each
                // share sealed to its holder; the response is share-free.
                interface ShamirInitResponse {
                    readonly threshold: number;
                    readonly totalShares: number;
                    readonly shareCount: number;
                }
                const init = await this.fetchJSON<ShamirInitResponse>('/v1/barrier/shamir/initialize',
                    { method: 'POST', ...jsonBody(req) },
                );
                // A Shamir root token needs a holder quorum, so none is minted
                // here.
                return { rootToken: '', threshold: init.threshold, shareCount: init.shareCount };
            },
            unseal: async (secret: string): Promise<UnsealProgress> => {
                this.ensureConnected();
                // Credential strategies only: a Shamir barrier opens through a
                // holder quorum, and the server refuses this request for it.
                // base64 on the wire; see initialize.
                await this.fetchVoid('/v1/barrier/unseal', {
                    method: 'POST',
                    ...jsonBody({ secret: encodeBase64Utf8(secret) }),
                });
                return { sealed: false, sharesProvided: 0, sharesRequired: 0 };
            },
            seal: async (): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/v1/barrier/seal', { method: 'POST' });
            },
            status: async (): Promise<SealStatus> => {
                this.ensureConnected();
                interface BarrierStatusResponse {
                    readonly sealed: boolean;
                    readonly strategy: string;
                    readonly hardwareBacked: boolean;
                    readonly initializedAt?: string;
                    // Shamir scheme, present only for strategy === "shamir".
                    readonly shamirThreshold?: number;
                    readonly shamirTotal?: number;
                    // The node barrier's cluster seal state; camel-cased by fetchJSON.
                    readonly clusterSeal?: ClusterSeal;
                }
                try {
                    const resp = await this.fetchJSON<BarrierStatusResponse>('/v1/barrier/status',
                    );
                    return {
                        sealed: resp.sealed,
                        initialized: resp.initializedAt !== undefined && resp.initializedAt !== '',
                        strategy: resp.strategy ?? '',
                        // For Shamir the server reports the M-of-N scheme; other
                        // strategies have no shares. Quorum progress belongs to
                        // an open round, not to the status.
                        sharesRequired: resp.shamirThreshold ?? 0,
                        sharesProvided: 0,
                        sharesTotal: resp.shamirTotal ?? 0,
                        ...(resp.clusterSeal ? { clusterSeal: resp.clusterSeal } : {}),
                    };
                } catch (err: unknown) {
                    if (err instanceof TransportError && err.statusCode === 404) {
                        // Barrier service not configured on this server.
                        return {
                            sealed: false,
                            initialized: false,
                            strategy: 'none',
                            sharesRequired: 0,
                            sharesProvided: 0,
                            sharesTotal: 0,
                        };
                    }
                    throw err;
                }
            },
            clusterUnseal: async (): Promise<ClusterSeal> => {
                this.ensureConnected();
                return this.fetchJSON<ClusterSeal>('/v1/barrier/cluster-unseal', { method: 'POST' });
            },
            enableEncryption: async (req: EnableEncryptionRequest): Promise<EnableEncryptionResponse> => {
                this.ensureConnected();
                // Build the snake_case payload, omitting empty/zero optional
                // fields so the server's `omitempty` tags see exactly what the
                // caller set (e.g. an absent tenant_id selects the system scope).
                const payload: Record<string, unknown> = { strategy: req.strategy };
                if (req.tenantId) payload.tenant_id = req.tenantId;
                if (req.mechanism) payload.mechanism = req.mechanism;
                if (req.availability) payload.availability = req.availability;
                // base64 on the wire; the server field is `[]byte` so it can
                // be zeroized. See seal.initialize.
                if (req.passphrase) payload.passphrase = encodeBase64Utf8(req.passphrase);
                if (req.shamirThreshold) payload.shamir_threshold = req.shamirThreshold;
                if (req.shamirTotal) payload.shamir_total = req.shamirTotal;
                return this.fetchJSON<EnableEncryptionResponse>('/v1/barrier/enable-encryption',
                    { method: 'POST', ...jsonBody(payload) },
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Share-holder administration
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get officers(): OfficerClient {
        const tenantPath = (tenantId: string, rest: string): string =>
            `/v1/tenants/${encodeURIComponent(tenantId)}/barrier${rest}`;
        const databasePath = (database: string, rest: string): string =>
            `/v1/databases/${encodeURIComponent(database)}/barrier${rest}`;
        const list = async (path: string): Promise<OfficerStatus[]> => {
            interface ListResponse { readonly officers?: OfficerStatus[] }
            const resp = await this.fetchJSON<ListResponse>(path);
            return (resp.officers ?? []).map(normalizeOfficerStatus);
        };
        return {
            registerOfficer: async (req: OfficerRegisterRequest): Promise<OfficerRegisterResponse> => {
                this.ensureConnected();
                return this.fetchJSON<OfficerRegisterResponse>('/v1/barrier/officers',
                    { method: 'POST', ...wireBody(holderEnrolmentWire(req)) },
                );
            },
            registerRecoveryOfficer: async (req: OfficerRegisterRequest): Promise<OfficerRegisterResponse> => {
                this.ensureConnected();
                return this.fetchJSON<OfficerRegisterResponse>('/v1/barrier/recovery/officers',
                    { method: 'POST', ...wireBody(holderEnrolmentWire(req)) },
                );
            },
            listOfficers: async (): Promise<OfficerStatus[]> => {
                this.ensureConnected();
                return list('/v1/barrier/officers');
            },
            startRekey: async (req: RekeyStartRequest): Promise<QuorumRound> => {
                this.ensureConnected();
                return this.fetchJSON<QuorumRound>('/v1/barrier/rekey/start',
                    { method: 'POST', ...jsonBody({ ...req, domain: req.domain ?? 'unseal' }) },
                );
            },
            revokeOfficer: async (req: OfficerRevokeRequest): Promise<QuorumRound> => {
                this.ensureConnected();
                return this.fetchJSON<QuorumRound>('/v1/barrier/officers/revoke',
                    { method: 'POST', ...jsonBody({ ...req, domain: req.domain ?? 'unseal' }) },
                );
            },
            registerTenantOfficer: async (req: TenantOfficerRegisterRequest): Promise<OfficerRegisterResponse> => {
                this.ensureConnected();
                return this.fetchJSON<OfficerRegisterResponse>(tenantPath(req.tenantId, '/officers'),
                    { method: 'POST', ...wireBody(holderEnrolmentWire(req)) },
                );
            },
            listTenantOfficers: async (tenantId: string): Promise<OfficerStatus[]> => {
                this.ensureConnected();
                return list(tenantPath(tenantId, '/officers'));
            },
            startTenantRekey: async (req: TenantRekeyStartRequest): Promise<QuorumRound> => {
                this.ensureConnected();
                const { tenantId, ...body } = req;
                return this.fetchJSON<QuorumRound>(tenantPath(tenantId, '/rekey/start'),
                    { method: 'POST', ...jsonBody(body) },
                );
            },
            revokeTenantOfficer: async (req: TenantOfficerRevokeRequest): Promise<QuorumRound> => {
                this.ensureConnected();
                const { tenantId, ...body } = req;
                return this.fetchJSON<QuorumRound>(tenantPath(tenantId, '/officers/revoke'),
                    { method: 'POST', ...jsonBody(body) },
                );
            },
            registerDatabaseOfficer: async (req: DatabaseOfficerRegisterRequest): Promise<OfficerRegisterResponse> => {
                this.ensureConnected();
                return this.fetchJSON<OfficerRegisterResponse>(databasePath(req.database, '/officers'),
                    { method: 'POST', ...wireBody(holderEnrolmentWire(req)) },
                );
            },
            listDatabaseOfficers: async (database: string): Promise<OfficerStatus[]> => {
                this.ensureConnected();
                return list(databasePath(database, '/officers'));
            },
            initializeDatabaseShamir: async (req: DatabaseShamirInitializeRequest): Promise<DatabaseShamirInitializeResponse> => {
                this.ensureConnected();
                const { database, ...body } = req;
                return this.fetchJSON<DatabaseShamirInitializeResponse>(databasePath(database, '/shamir/initialize'),
                    { method: 'POST', ...jsonBody(body) },
                );
            },
            startDatabaseRekey: async (req: DatabaseRekeyStartRequest): Promise<QuorumRound> => {
                this.ensureConnected();
                const { database, ...body } = req;
                return this.fetchJSON<QuorumRound>(databasePath(database, '/rekey/start'),
                    { method: 'POST', ...jsonBody(body) },
                );
            },
            revokeDatabaseOfficer: async (req: DatabaseOfficerRevokeRequest): Promise<QuorumRound> => {
                this.ensureConnected();
                const { database, ...body } = req;
                return this.fetchJSON<QuorumRound>(databasePath(database, '/officers/revoke'),
                    { method: 'POST', ...jsonBody(body) },
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Embedded server (desktop-only, not available via REST)
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get embeddedServer(): EmbeddedServerClient {
        return {
            startEmbedded: async (_config: EmbeddedConfig): Promise<void> => {
                throw new DesktopOnlyError('embeddedServer.startEmbedded');
            },
            stopEmbedded: async (): Promise<void> => {
                throw new DesktopOnlyError('embeddedServer.stopEmbedded');
            },
            isEmbedded: (): boolean => {
                return false;
            },
            hasSavedConfig: async (): Promise<boolean> => {
                return false;
            },
            restoreEmbedded: async (): Promise<void> => {
                throw new DesktopOnlyError('embeddedServer.restoreEmbedded');
            },
            clearSavedConfig: async (): Promise<void> => {
                throw new DesktopOnlyError('embeddedServer.clearSavedConfig');
            },
        };
    }

    // -----------------------------------------------------------------------
    // Daemon (desktop-only, not available via REST)
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get daemon(): DaemonClient {
        return {
            startDaemon: async (_configPath: string): Promise<void> => {
                throw new DesktopOnlyError('daemon.startDaemon');
            },
            stopDaemon: async (): Promise<void> => {
                throw new DesktopOnlyError('daemon.stopDaemon');
            },
            restartDaemon: async (_configPath: string): Promise<void> => {
                throw new DesktopOnlyError('daemon.restartDaemon');
            },
            daemonStatus: async (): Promise<DaemonStatus> => {
                throw new DesktopOnlyError('daemon.daemonStatus');
            },
            daemonLogs: async (_maxLines: number): Promise<string> => {
                throw new DesktopOnlyError('daemon.daemonLogs');
            },
        };
    }

    // -----------------------------------------------------------------------
    // Tenants
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get tenants(): TenantClient {
        return {
            create: async (req: CreateTenantRequest): Promise<CreateTenantResponse> => {
                this.ensureConnected();
                // The REST wire format returns the tenant object directly;
                // wrap it to match the SDK contract (mirrors the Go REST
                // transport's CreateTenant adaptation).
                const tenant = await this.fetchJSON<TenantInfo>('/v1/tenants', {
                    method: 'POST',
                    ...jsonBody(req),
                });
                return { tenant };
            },
            get: async (tenantId: string): Promise<GetTenantResponse> => {
                this.ensureConnected();
                // Bare tenant on the wire; wrap to the SDK contract.
                const tenant = await this.fetchJSON<TenantInfo>(`/v1/tenants/${encodeURIComponent(tenantId)}`,
                );
                return { tenant };
            },
            list: async (): Promise<ListTenantsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ListTenantsResponse>('/v1/tenants/list');
            },
            delete: async (tenantId: string): Promise<void> => {
                this.ensureConnected();
                // DELETE /v1/tenants/{id} responds 204 No Content.
                await this.fetchVoid(`/v1/tenants/${encodeURIComponent(tenantId)}`,
                    { method: 'DELETE' },
                );
            },
            update: async (tenantId: string, req: UpdateTenantRequest): Promise<UpdateTenantResponse> => {
                this.ensureConnected();
                // Bare tenant on the wire; wrap to the SDK contract.
                const tenant = await this.fetchJSON<TenantInfo>(`/v1/tenants/${encodeURIComponent(tenantId)}`,
                    { method: 'PUT', ...jsonBody(req) },
                );
                return { tenant };
            },
            barrierInit: async (req: TenantBarrierInitRequest): Promise<TenantBarrierInitResponse> => {
                this.ensureConnected();
                // "none" and "passphrase" strategies respond 204 No Content;
                // "shamir" responds 200 with threshold/share_count but NO shares.
                // fetchJSON cannot parse an empty body, so we issue the request
                // directly and synthesize a response for the 204 case.
                const path = `/v1/tenants/${encodeURIComponent(req.tenantId)}/barrier/init`;
                // passphrase is base64 on the wire; the server field is
                // `[]byte` so it can be zeroized. See seal.initialize.
                const initBody: Record<string, unknown> = { ...req };
                if (req.passphrase) initBody.passphrase = encodeBase64Utf8(req.passphrase);
                let resp: Response;
                try {
                    resp = await fetch(this.baseUrl + path, { method: 'POST', ...jsonBody(initBody) });
                } catch (err: unknown) {
                    throw new TransportError(
                        `POST ${path}`,
                        undefined,
                        err instanceof Error ? err : new Error(String(err)),
                    );
                }
                if (!resp.ok) {
                    throw new TransportError(`POST ${path}`, resp.status, new Error(await resp.text()));
                }
                if (resp.status === 204) {
                    return { tenantId: req.tenantId, strategy: req.strategy ?? '' };
                }
                const raw = await resp.json();
                // The wire response has no `shares` field; deepSnakeToCamel maps
                // share_count → shareCount, total_shares → totalShares.
                return deepSnakeToCamel(raw) as TenantBarrierInitResponse;
            },
            barrierUnseal: async (req: TenantBarrierUnsealRequest): Promise<void> => {
                this.ensureConnected();
                // The key is `[]byte` on the server so it can be zeroized:
                // base64 on the wire.
                await this.fetchJSON<unknown>(`/v1/tenants/${encodeURIComponent(req.tenantId)}/barrier/unseal`,
                    { method: 'POST', ...jsonBody({ key: encodeBase64Utf8(req.key) }) },
                );
            },
            barrierStatus: async (tenantId: string): Promise<TenantBarrierStatusResponse> => {
                this.ensureConnected();
                return this.fetchJSON<TenantBarrierStatusResponse>(`/v1/tenants/${encodeURIComponent(tenantId)}/barrier/status`,
                );
            },
            updateQuotas: async (req: UpdateTenantQuotasRequest): Promise<UpdateTenantQuotasResponse> => {
                this.ensureConnected();
                // The REST handler (POST /v1/tenants/{id}/quotas) decodes a FLAT
                // quota body (top-level max_* fields), not a wrapper object, and
                // returns the bare updated tenant. Send only the quotas and wrap
                // the response to match the SDK contract.
                const tenant = await this.fetchJSON<TenantInfo>(`/v1/tenants/${encodeURIComponent(req.tenantId)}/quotas`,
                    { method: 'POST', ...jsonBody(req.quotas) },
                );
                return { tenant };
            },
            updateEntitlements: async (req: UpdateTenantEntitlementsRequest): Promise<UpdateTenantEntitlementsResponse> => {
                this.ensureConnected();
                // The REST handler (PATCH /v1/tenants/{id}/entitlements) decodes a
                // FLAT entitlements body (top-level allow_* / allowed_isolation_modes
                // fields), not a wrapper object, and returns the bare updated
                // tenant. Send only the entitlements and wrap the response to match
                // the SDK contract (mirrors updateQuotas above).
                const tenant = await this.fetchJSON<TenantInfo>(`/v1/tenants/${encodeURIComponent(req.tenantId)}/entitlements`,
                    { method: 'PATCH', ...jsonBody(req.entitlements) },
                );
                return { tenant };
            },
        };
    }

    // -----------------------------------------------------------------------
    // Server Config
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get config(): ServerConfigClient {
        return {
            initialized: async (): Promise<ConfigInitializedResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ConfigInitializedResponse>('/v1/config/initialized',
                );
            },
            isEmbeddedAvailable: async (): Promise<boolean> => false,
        };
    }

    // -----------------------------------------------------------------------
    // Backups
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get backups(): BackupClient {
        return {
            create: async (req: BackupRequest): Promise<BackupInfo> => {
                this.ensureConnected();
                return this.fetchJSON<BackupInfo>('/v1/backups', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            list: async (): Promise<ListBackupsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ListBackupsResponse>('/v1/backups');
            },
            get: async (id: string): Promise<BackupInfo> => {
                this.ensureConnected();
                return this.fetchJSON<BackupInfo>(`/v1/backups/${encodeURIComponent(id)}`,
                );
            },
            delete: async (id: string): Promise<void> => {
                this.ensureConnected();
                // DELETE /v1/backups/{id} responds 204 No Content.
                await this.fetchVoid(`/v1/backups/${encodeURIComponent(id)}`,
                    { method: 'DELETE' },
                );
            },
            restore: async (req: RestoreRequest): Promise<RestoreResult> => {
                this.ensureConnected();
                return this.fetchJSON<RestoreResult>(`/v1/backups/${encodeURIComponent(req.backupId)}/restore`,
                    { method: 'POST', ...jsonBody(req) },
                );
            },
            verify: async (id: string): Promise<VerifyResult> => {
                this.ensureConnected();
                return this.fetchJSON<VerifyResult>(`/v1/backups/${encodeURIComponent(id)}/verify`,
                    { method: 'POST' },
                );
            },
        };
    }

    // -----------------------------------------------------------------------
    // Rebalance
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get rebalance(): RebalanceClient {
        return {
            getShardStats: async (shardId: number): Promise<ShardStatsResponse> => {
                this.ensureConnected();
                return this.fetchJSON<ShardStatsResponse>(`/v1/rebalance/shards/${encodeURIComponent(String(shardId))}/stats`,
                );
            },
            createPlan: async (req: RebalancePlanRequest): Promise<RebalancePlan> => {
                this.ensureConnected();
                return this.fetchJSON<RebalancePlan>('/v1/rebalance/plan', {
                    method: 'POST',
                    ...jsonBody(req),
                });
            },
            execute: async (plan: RebalancePlan): Promise<RebalanceResult> => {
                this.ensureConnected();
                return this.fetchJSON<RebalanceResult>('/v1/rebalance/execute', {
                    method: 'POST',
                    ...jsonBody(plan),
                });
            },
            getImbalanceReport: async (): Promise<ImbalanceReport> => {
                this.ensureConnected();
                return this.fetchJSON<ImbalanceReport>('/v1/rebalance/report');
            },
        };
    }

    // -----------------------------------------------------------------------
    // Credentials
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get credentials(): CredentialClient {
        return {
            submit: async (name: string, value: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/v1/credentials/submit', {
                    method: 'POST',
                    ...jsonBody({ name, value }),
                });
            },
            store: async (name: string, value: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid('/v1/credentials/store', {
                    method: 'POST',
                    ...jsonBody({ name, value }),
                });
            },
            retrieve: async (name: string): Promise<string> => {
                this.ensureConnected();
                interface ValueResponse { readonly value: string }
                const resp = await this.fetchJSON<ValueResponse>(`/v1/credentials/${encodeURIComponent(name)}/value`,
                );
                return resp.value;
            },
            list: async (): Promise<string[]> => {
                this.ensureConnected();
                interface ListResponse { readonly names: string[] }
                const resp = await this.fetchJSON<ListResponse>('/v1/credentials');
                return resp.names ?? [];
            },
            strategy: async (): Promise<string> => {
                this.ensureConnected();
                interface StrategyResponse { readonly strategy: string }
                const resp = await this.fetchJSON<StrategyResponse>('/v1/credentials/strategy');
                return resp.strategy;
            },
            delete: async (name: string): Promise<void> => {
                this.ensureConnected();
                await this.fetchVoid(`/v1/credentials/${encodeURIComponent(name)}`,
                    { method: 'DELETE' },
                );
            },
            rotate: async (name: string, newValue: string, gracePeriodSeconds?: number): Promise<CredentialRotateResult> => {
                this.ensureConnected();
                return this.fetchJSON<CredentialRotateResult>(`/v1/credentials/${encodeURIComponent(name)}/rotate`,
                    {
                        method: 'POST',
                        ...jsonBody({
                            name,
                            newValue,
                            gracePeriodSeconds: gracePeriodSeconds ?? 0,
                        }),
                    },
                );
            },
            listVersions: async (name: string): Promise<CredentialVersionInfo[]> => {
                this.ensureConnected();
                interface VersionsResponse { readonly versions: CredentialVersionInfo[] }
                const resp = await this.fetchJSON<VersionsResponse>(`/v1/credentials/${encodeURIComponent(name)}/versions`,
                );
                return resp.versions ?? [];
            },
            retrieveVersion: async (name: string, version: number): Promise<string> => {
                this.ensureConnected();
                interface ValueResponse { readonly value: string }
                const resp = await this.fetchJSON<ValueResponse>(`/v1/credentials/${encodeURIComponent(name)}/versions/${encodeURIComponent(String(version))}`,
                );
                return resp.value;
            },
            gcExpired: async (name: string): Promise<number> => {
                this.ensureConnected();
                interface GCResponse { readonly reaped: number }
                const resp = await this.fetchJSON<GCResponse>(`/v1/credentials/${encodeURIComponent(name)}/gc`,
                    { method: 'POST' },
                );
                return resp.reaped;
            },
        };
    }

    // -----------------------------------------------------------------------
    // Audit
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get audit(): AuditQueryClient {
        return {
            query: async (f: AuditFilter = {}): Promise<AuditPage> => {
                this.ensureConnected();
                const snake = deepCamelToSnake(f) as Record<string, unknown>;
                const params = new URLSearchParams();
                for (const [k, v] of Object.entries(snake)) {
                    if (v !== undefined && v !== null && v !== '') {
                        params.set(k, String(v));
                    }
                }
                const query = params.size > 0 ? `?${params.toString()}` : '';
                return this.fetchJSON<AuditPage>(`/v1/audit/events${query}`);
            },
        };
    }

    // -----------------------------------------------------------------------
    // Connection Manager
    // -----------------------------------------------------------------------

    /** @inheritdoc */
    get connectionManager(): ConnectionManagerClient {
        return {
            listProfiles: async (): Promise<ConnectionProfile[]> => {
                throw new DesktopOnlyError('connectionManager.listProfiles');
            },
            saveProfile: async (_profile: ConnectionProfile): Promise<void> => {
                throw new DesktopOnlyError('connectionManager.saveProfile');
            },
            deleteProfile: async (_id: string): Promise<void> => {
                throw new DesktopOnlyError('connectionManager.deleteProfile');
            },
            connect: async (_profileId: string): Promise<ConnectionState> => {
                throw new DesktopOnlyError('connectionManager.connect');
            },
            disconnect: async (_profileId: string): Promise<void> => {
                throw new DesktopOnlyError('connectionManager.disconnect');
            },
            getState: async (_profileId: string): Promise<ConnectionState | null> => {
                throw new DesktopOnlyError('connectionManager.getState');
            },
            setActive: async (_profileId: string): Promise<void> => {
                throw new DesktopOnlyError('connectionManager.setActive');
            },
            getActiveProfileId: async (): Promise<string> => {
                throw new DesktopOnlyError('connectionManager.getActiveProfileId');
            },
            isEmbeddedAvailable: async (): Promise<boolean> => false,
        };
    }

    // -----------------------------------------------------------------------
    // Internal
    // -----------------------------------------------------------------------

    /**
     * Throws {@link NotConnectedError} if the client has not been connected
     * via {@link ConnectionClient.connect}.
     */
    private ensureConnected(): void {
        if (!this.connected) {
            throw new NotConnectedError();
        }
    }

    // -----------------------------------------------------------------------
    // Request helpers (database-scope aware)
    //
    // Every service getter routes its requests through these three wrappers
    // rather than the module-level request functions. The wrappers inject the
    // X-QRDB-Database header for DATA-PLANE paths only when this client is
    // database-scoped; management/admin/system paths never carry the header.
    // This keeps the database scope in exactly one place instead of at ~150
    // individual call sites.
    // -----------------------------------------------------------------------

    /**
     * Returns the database scope header for a data-plane path, or `undefined`
     * when this client is unscoped or the path is a management/admin endpoint.
     */
    private scopeHeaders(path: string): Record<string, string> | undefined {
        if (this.activeDatabase !== '' && isDataPlanePath(path)) {
            return { [HEADER_DATABASE]: this.activeDatabase };
        }
        return undefined;
    }

    /** Merges the database scope header into a request init, when applicable. */
    private scopedInit(path: string, init?: RequestInit): RequestInit | undefined {
        const dbHeaders = this.scopeHeaders(path);
        if (dbHeaders === undefined) {
            return init;
        }
        return {
            ...init,
            headers: {
                ...(init?.headers as Record<string, string> | undefined),
                ...dbHeaders,
            },
        };
    }

    /** Database-scope-aware wrapper over {@link requestJSON}. */
    private fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
        return requestJSON<T>(this.baseUrl, path, this.scopedInit(path, init));
    }

    /** Database-scope-aware wrapper over {@link requestVoid}. */
    private fetchVoid(path: string, init?: RequestInit): Promise<void> {
        return requestVoid(this.baseUrl, path, this.scopedInit(path, init));
    }

    /** Database-scope-aware wrapper over {@link requestText}. */
    private fetchText(path: string, init?: RequestInit): Promise<string> {
        return requestText(this.baseUrl, path, this.scopedInit(path, init));
    }
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

/** Maps numeric SortOrder values to their REST API query parameter strings. */
const SORT_ORDER_STRINGS: Record<number, string> = {
    0: 'none',
    1: 'ascend',
    2: 'descend',
};

/** Maps numeric SortTarget values to their REST API query parameter strings. */
const SORT_TARGET_STRINGS: Record<number, string> = {
    0: 'key',
    1: 'version',
    2: 'create_revision',
    3: 'mod_revision',
    4: 'value',
};

/** Maps numeric AggFunc values to their REST API query parameter strings. */
const AGG_FUNC_STRINGS: Record<number, string> = {
    0: 'none',
    1: 'avg',
    2: 'sum',
    3: 'min',
    4: 'max',
    5: 'count',
    6: 'percentile',
    7: 'first',
    8: 'last',
};

/** Converts a numeric SortOrder to its REST API string, or undefined. */
function sortOrderToString(order: number | undefined): string | undefined {
    if (order === undefined || order === 0) {
        return undefined;
    }
    return SORT_ORDER_STRINGS[order];
}

/** Converts a numeric SortTarget to its REST API string, or undefined. */
function sortTargetToString(target: number | undefined): string | undefined {
    if (target === undefined || target === 0) {
        return undefined;
    }
    return SORT_TARGET_STRINGS[target];
}

/** Converts a numeric AggFunc to its REST API string. */
function aggFuncToString(fn: number): string {
    return AGG_FUNC_STRINGS[fn] ?? 'avg';
}

/** Converts a TSTag array to a Record<string, string> map for the REST API. */
function tagsArrayToMap(
    tags: readonly { readonly key: string; readonly value: string }[] | undefined,
): Record<string, string> | undefined {
    if (!tags || tags.length === 0) {
        return undefined;
    }
    const result: Record<string, string> = {};
    for (const tag of tags) {
        result[tag.key] = tag.value;
    }
    return result;
}

// ---------------------------------------------------------------------------
// Transaction wire-format serializers
// ---------------------------------------------------------------------------

/**
 * Normalizes watch event types from the REST SSE stream. The server emits
 * `type: "PUT"` / `type: "DELETE"` as strings, but the SDK's EventType is
 * numeric (0 = put, 1 = delete). Mutates the response in place so downstream
 * consumers see a consistent numeric type matching the declared EventType.
 */
function normalizeWatchEventTypes(resp: WatchResponse): void {
    if (!resp.events) return;
    for (const evt of resp.events) {
        const t = evt.type as unknown;
        if (typeof t === 'string') {
            (evt as { type: number }).type = t === 'DELETE' ? 1 : 0;
        }
    }
}

/**
 * Maps numeric CompareTarget enum values to the string names the REST API
 * expects. Server: handlers_transaction.go compareTargetMap.
 */
const COMPARE_TARGET_STRINGS: Record<number, string> = {
    0: 'version',
    1: 'create_revision',
    2: 'mod_revision',
    3: 'value',
};

/**
 * Maps numeric CompareResult enum values to the string names the REST API
 * expects. Server: handlers_transaction.go compareResultMap.
 */
const COMPARE_RESULT_STRINGS: Record<number, string> = {
    0: 'equal',
    1: 'greater',
    2: 'less',
    3: 'not_equal',
};

/**
 * Maps numeric OpType enum values to the string names the REST API expects.
 * Server: handlers_transaction.go restOpTypeMap.
 */
const OP_TYPE_STRINGS: Record<number, string> = {
    0: 'put',
    1: 'get',
    2: 'delete',
    3: 'range',
};

/** Wire representation of a compare precondition sent to POST /tx. */
interface WireCompare {
    key: string;
    target: string;
    result: string;
    value?: string;
    version?: number;
    create_revision?: number;
    mod_revision?: number;
}

/** Wire representation of a transaction/batch operation sent to POST /tx or POST /batch. */
interface WireOp {
    type: string;
    key: string;
    value?: string;
    range_end?: string;
    lease_id?: number;
}

/** Converts a {@link Compare} to its REST wire format. */
function serializeCompare(c: import('./types.js').Compare): WireCompare {
    const wire: WireCompare = {
        key: c.key,
        target: COMPARE_TARGET_STRINGS[c.target] ?? 'version',
        result: COMPARE_RESULT_STRINGS[c.result] ?? 'equal',
    };
    if (c.value !== undefined) {
        wire.value = c.value;
    }
    if (c.version !== undefined) {
        wire.version = c.version;
    }
    if (c.createRevision !== undefined) {
        wire.create_revision = c.createRevision;
    }
    if (c.modRevision !== undefined) {
        wire.mod_revision = c.modRevision;
    }
    return wire;
}

/** Converts an {@link Op} to its REST wire format. */
function serializeOp(op: import('./types.js').Op): WireOp {
    const wire: WireOp = {
        type: OP_TYPE_STRINGS[op.type] ?? 'put',
        key: op.key,
    };
    if (op.value !== undefined) {
        wire.value = op.value;
    }
    if (op.rangeEnd !== undefined) {
        wire.range_end = op.rangeEnd;
    }
    if (op.leaseId !== undefined) {
        wire.lease_id = op.leaseId;
    }
    return wire;
}
