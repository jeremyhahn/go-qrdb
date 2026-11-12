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
    CeremonyOfficerRequest,
    CompactionProgress,
    DatabaseBarrierStatus,
    DistanceMetric,
    MVCCKeyValue,
    OfficerRegisterRequest,
    OfficerStatus,
    OpResponse,
    RebuildProgress,
    RestoreKeyChange,
    RestoreRevisionResult,
    VectorIndexType,
    VectorSearchResult,
    WatchEvent,
} from './types.js';

// ---------------------------------------------------------------------------
// Wire-format key transformers
// ---------------------------------------------------------------------------

/**
 * Converts a snake_case key to camelCase.
 * Examples: "is_leader" → "isLeader", "node_id" → "nodeId".
 * Keys without underscores are returned unchanged, preserving existing
 * camelCase fields (e.g. those from Go types with explicit camelCase json tags).
 */
export function snakeToCamelKey(key: string): string {
    return key.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase());
}

/**
 * Recursively transforms all object keys from snake_case to camelCase.
 * Arrays are traversed element-by-element; primitives and null are returned
 * as-is without copying.
 *
 * Applied to every JSON response received from the QRDB REST API and Wails
 * IPC so that SDK consumers always see the camelCase field names declared in
 * types.ts, regardless of the server's snake_case JSON serialization.
 */
/**
 * Keys whose values are user-supplied maps (vector metadata, metadata
 * filters). Their own keys are the user's data and cross the wire as
 * typed: neither deep conversion renames them.
 */
const USER_MAP_KEYS: ReadonlySet<string> = new Set([
    'metadata',
    'filter',
    'filters',
    'metadataFilter',
    'metadata_filter',
]);

export function deepSnakeToCamel(value: unknown): unknown {
    if (Array.isArray(value)) {
        return value.map(deepSnakeToCamel);
    }
    if (value !== null && typeof value === 'object') {
        const out: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
            out[snakeToCamelKey(k)] = USER_MAP_KEYS.has(k) ? v : deepSnakeToCamel(v);
        }
        return out;
    }
    return value;
}

/**
 * Converts a camelCase key to snake_case.
 * Examples: "isLeader" → "is_leader", "nodeId" → "node_id".
 */
export function camelToSnakeKey(key: string): string {
    return key.replace(/[A-Z]/g, (ch) => '_' + ch.toLowerCase());
}

/**
 * Recursively transforms all object keys from camelCase to snake_case.
 * Used to serialize SDK request objects for the Go REST API and Wails IPC,
 * both of which use snake_case JSON tags on Go structs.
 */
export function deepCamelToSnake(value: unknown): unknown {
    if (Array.isArray(value)) {
        return value.map(deepCamelToSnake);
    }
    if (value !== null && typeof value === 'object') {
        const out: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
            out[camelToSnakeKey(k)] = USER_MAP_KEYS.has(k) ? v : deepCamelToSnake(v);
        }
        return out;
    }
    return value;
}

// ---------------------------------------------------------------------------
// Vector enum wire codecs
// ---------------------------------------------------------------------------
//
// The SDK's public vector API uses the numeric proto enums (DistanceMetric
// and VectorIndexType, see pkg/api/grpc/proto/qrdbv1/qrdb.proto), but both
// wire protocols the TypeScript SDK speaks carry these values as strings:
//
//   - The Go REST API (pkg/api/rest/handlers_vector.go) accepts the service
//     canonical tokens defined by the gRPC conversion maps in
//     pkg/api/grpc/service_vector.go: "COSINE" / "L2" / "DOT_PRODUCT" and
//     "HNSW" / "FLAT" / "IVF".
//   - The Wails IPC boundary unmarshals into the Go SDK transport structs
//     (sdk/go/transport/vector_types.go), whose canonical tokens are
//     "cosine" / "euclidean" / "dot_product" and "hnsw" / "flat" /
//     "ivf_flat".
//
// Each transport therefore adapts the numeric enum to its own wire contract
// on requests, and parses wire strings back to the numeric enum on
// responses. Response parsing is case-insensitive and alias-tolerant
// because the server stores the string verbatim (pkg/service/vector.go
// parses it case-insensitively) so responses can carry any accepted casing.

/** REST wire tokens for DistanceMetric, indexed by numeric enum value. */
const REST_DISTANCE_METRIC_TOKENS: Readonly<Record<DistanceMetric, string>> = {
    0: 'COSINE',
    1: 'L2',
    2: 'DOT_PRODUCT',
};

/** REST wire tokens for VectorIndexType, indexed by numeric enum value. */
const REST_INDEX_TYPE_TOKENS: Readonly<Record<VectorIndexType, string>> = {
    0: 'HNSW',
    1: 'FLAT',
    2: 'IVF',
};

/** Wails IPC (Go SDK transport) tokens for DistanceMetric. */
const IPC_DISTANCE_METRIC_TOKENS: Readonly<Record<DistanceMetric, string>> = {
    0: 'cosine',
    1: 'euclidean',
    2: 'dot_product',
};

/** Wails IPC (Go SDK transport) tokens for VectorIndexType. */
const IPC_INDEX_TYPE_TOKENS: Readonly<Record<VectorIndexType, string>> = {
    0: 'hnsw',
    1: 'flat',
    2: 'ivf_flat',
};

/** Converts a numeric DistanceMetric to the Go REST API wire string. */
export function distanceMetricToRestWire(metric: DistanceMetric): string {
    return REST_DISTANCE_METRIC_TOKENS[metric] ?? REST_DISTANCE_METRIC_TOKENS[1];
}

/** Converts a numeric VectorIndexType to the Go REST API wire string. */
export function indexTypeToRestWire(indexType: VectorIndexType): string {
    return REST_INDEX_TYPE_TOKENS[indexType] ?? REST_INDEX_TYPE_TOKENS[0];
}

/** Converts a numeric DistanceMetric to the Wails IPC wire string. */
export function distanceMetricToIpcWire(metric: DistanceMetric): string {
    return IPC_DISTANCE_METRIC_TOKENS[metric] ?? IPC_DISTANCE_METRIC_TOKENS[1];
}

/** Converts a numeric VectorIndexType to the Wails IPC wire string. */
export function indexTypeToIpcWire(indexType: VectorIndexType): string {
    return IPC_INDEX_TYPE_TOKENS[indexType] ?? IPC_INDEX_TYPE_TOKENS[0];
}

/**
 * Parses a wire distance-metric value (any accepted casing or alias) back to
 * the numeric DistanceMetric enum. Unknown values fall back to Euclidean/L2,
 * mirroring the server's default (pkg/service/vector.go).
 */
export function wireToDistanceMetric(value: unknown): DistanceMetric {
    if (typeof value === 'number') {
        return value === 0 || value === 2 ? (value as DistanceMetric) : 1;
    }
    if (typeof value === 'string') {
        switch (value.toUpperCase()) {
            case 'COSINE':
                return 0;
            case 'L2':
            case 'EUCLIDEAN':
                return 1;
            case 'DOT_PRODUCT':
                return 2;
        }
    }
    return 1;
}

/**
 * Parses a wire index-type value (any accepted casing or alias) back to the
 * numeric VectorIndexType enum. Unknown values fall back to HNSW, mirroring
 * the server's default (pkg/service/vector.go).
 */
export function wireToIndexType(value: unknown): VectorIndexType {
    if (typeof value === 'number') {
        return value === 1 || value === 2 ? (value as VectorIndexType) : 0;
    }
    if (typeof value === 'string') {
        switch (value.toUpperCase()) {
            case 'HNSW':
                return 0;
            case 'FLAT':
                return 1;
            case 'IVF':
            case 'IVF_FLAT':
                return 2;
        }
    }
    return 0;
}

// ---------------------------------------------------------------------------
// Vector ID wire codecs
// ---------------------------------------------------------------------------
//
// The SDK's public vector API uses string IDs (mirroring the proto's string
// IDs, which the gRPC service parses with strconv.ParseUint, see
// pkg/api/grpc/service_vector.go parseVectorID). Both the Go REST API
// (pkg/api/rest/handlers_vector.go) and the Go SDK transport structs behind
// the Wails IPC boundary (sdk/go/transport/vector_types.go) carry vector IDs
// as uint64 JSON numbers, so the TypeScript transports convert string IDs to
// numbers on requests and back to strings on responses.

/**
 * Converts an SDK string vector ID to the numeric wire representation.
 * Non-decimal IDs are passed through unchanged so the server rejects them
 * authoritatively (they can never round-trip through the uint64 ID space,
 * matching the gRPC service's parse failure for non-numeric proto IDs).
 */
export function vectorIDToWire(id: string): number | string {
    if (!/^\d+$/.test(id)) {
        return id;
    }
    return Number(id);
}

/**
 * Converts vector index parameters to the REST wire's index_config, a
 * map[string]string on the Go side (vectorCreateNamespaceRequest): a
 * numeric or boolean parameter is sent as its decimal string, which the
 * server parses, instead of being refused as a type mismatch.
 */
export function indexParamsToWire(
    params: Readonly<Record<string, unknown>> | undefined,
): Record<string, string> | undefined {
    if (params === undefined) {
        return undefined;
    }
    const out: Record<string, string> = {};
    for (const [key, value] of Object.entries(params)) {
        if (value === undefined || value === null) {
            continue;
        }
        out[key] = typeof value === 'string' ? value : String(value);
    }
    return out;
}

/** The REST wire's index-rebuild start answer, after snake-to-camel. */
export interface WireRebuildIndexResponse {
    readonly jobId: string;
    readonly timestamp: number;
    readonly success: boolean;
}

/** The REST wire's index-rebuild progress (vectorRebuildProgressResponse). */
export interface WireRebuildProgress {
    readonly jobId: string;
    readonly status: string;
    readonly percentComplete?: number;
    readonly startedAt?: number;
    readonly completedAt?: number;
    readonly error?: string;
}

/** The REST wire's compaction start answer (vectorCompactResponse). */
export interface WireCompactResponse {
    readonly operationId: string;
    readonly success: boolean;
}

/** The REST wire's compaction progress (vectorCompactionProgressResponse). */
export interface WireCompactionProgress {
    readonly operationId: string;
    readonly percentComplete?: number;
    readonly inProgress?: boolean;
    readonly startedAt?: number;
    readonly completedAt?: number;
    readonly error?: string;
}

/**
 * Maps the wire's rebuild progress to the SDK shape: percent_complete
 * (0-100) becomes progress (0.0-1.0) and completed_at becomes finishedAt.
 */
export function rebuildProgressFromWire(w: WireRebuildProgress): RebuildProgress {
    return {
        jobId: w.jobId,
        status: w.status,
        progress: (w.percentComplete ?? 0) / 100,
        startedAt: w.startedAt ?? 0,
        finishedAt: w.completedAt || undefined,
        error: w.error || undefined,
    };
}

/**
 * Maps the wire's compaction progress to the SDK shape: operation_id is
 * the job id, in_progress and error give the status, percent_complete
 * (0-100) becomes progress (0.0-1.0) and completed_at becomes finishedAt.
 */
export function compactionProgressFromWire(w: WireCompactionProgress): CompactionProgress {
    const status = w.inProgress ? 'running' : w.error ? 'failed' : 'completed';
    return {
        jobId: w.operationId,
        status,
        progress: (w.percentComplete ?? 0) / 100,
        startedAt: w.startedAt ?? 0,
        finishedAt: w.completedAt || undefined,
        error: w.error || undefined,
    };
}

/** A REST vector search answer (similar, hybrid, by-id), after snake-to-camel. */
export interface WireSearchResponse {
    readonly results?: readonly VectorSearchResult[] | null;
    readonly queryDuration?: number;
}

/**
 * Maps a vector search answer to the SDK shape: result IDs become
 * strings and query_duration becomes took.
 */
export function searchResponseFromWire(w: WireSearchResponse): {
    results: VectorSearchResult[];
    took: number;
} {
    return {
        results: (w.results ?? []).map(normalizeVectorIDField),
        took: w.queryDuration ?? 0,
    };
}

/** Converts a wire vector ID (number or string) to the SDK string form. */
export function vectorIDFromWire(value: unknown): string {
    return typeof value === 'string' ? value : String(value ?? '');
}

/**
 * Returns a shallow copy of `obj` with its `id` field (when present)
 * converted from the numeric wire representation to the SDK string form.
 */
export function normalizeVectorIDField<T>(obj: T): T {
    if (obj === null || typeof obj !== 'object' || Array.isArray(obj)) {
        return obj;
    }
    const rec = obj as Record<string, unknown>;
    if (rec.id === undefined) {
        return obj;
    }
    return { ...rec, id: vectorIDFromWire(rec.id) } as T;
}

/**
 * Returns a shallow copy of `obj` with its `distanceMetric` / `indexType`
 * fields (when present) converted from wire strings to the SDK's numeric
 * enums. Objects without these fields are returned unchanged.
 */
export function normalizeVectorEnumFields<T>(obj: T): T {
    if (obj === null || typeof obj !== 'object' || Array.isArray(obj)) {
        return obj;
    }
    const rec = obj as Record<string, unknown>;
    if (rec.distanceMetric === undefined && rec.indexType === undefined) {
        return obj;
    }
    const out: Record<string, unknown> = { ...rec };
    if (out.distanceMetric !== undefined) {
        out.distanceMetric = wireToDistanceMetric(out.distanceMetric);
    }
    if (out.indexType !== undefined) {
        out.indexType = wireToIndexType(out.indexType);
    }
    return out as T;
}

// ---------------------------------------------------------------------------
// MVCCKeyValue base64 → plaintext decoding
// ---------------------------------------------------------------------------
//
// `sdk/go/transport.MVCCKeyValue` carries `Key`/`Value` as `[]byte`, so both
// wire protocols the TypeScript SDK speaks serialize them as base64:
//
//   - The Go REST API marshals `[]byte` fields as base64 via
//     `encoding/json` (Go's standard behavior), e.g. GET /kv/{key}?revision=true,
//     POST /kv/range, POST /tx, and POST /batch.
//   - The Wails IPC boundary marshals Go method return values the same way.
//     `cmd/qrdb/bindings_mvcc.go`'s MVCCBinding methods (PutWithLease,
//     GetRevision, GetAtRevision, Range) and `bindings_transaction.go` /
//     `bindings_batch.go`'s Execute methods return `*transport.MVCCKeyValue`
//     / `*transport.OpResponse` unconverted, so they carry the same base64
//     encoding. (This differs from `KVBinding.Get`/`Scan`, which already
//     convert to Go strings server-side, and `WatchBinding`, which emits the
//     string-typed `wailsKeyValue` — see `bindings_watch.go` — so those two
//     paths need no decoding here.)
//
// Both transports decode through the helpers below so every `MVCCKeyValue`
// the SDK returns is plaintext, matching `kv.get`/`kv.scan`.

/**
 * Decodes a base64 string into its UTF-8 plaintext representation.
 *
 * The `atob` → `Uint8Array` → `TextDecoder` chain is used (rather than the
 * lossy `atob` shortcut on its own) so that multibyte UTF-8 sequences decode
 * correctly.
 */
export function decodeBase64Utf8(b64: string): string {
    const binary = atob(b64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
        bytes[i] = binary.charCodeAt(i);
    }
    return new TextDecoder().decode(bytes);
}

/**
 * Encodes a UTF-8 string into its base64 representation.
 *
 * The inverse of {@link decodeBase64Utf8}. Used to prepare plaintext
 * SDK-facing fields (e.g. a restore request's `prefix`/`keys`) for the
 * wire's binary-safe `[]byte` field convention (base64 in REST JSON and
 * Wails IPC), so arbitrary byte content — not just valid UTF-8 — round-trips
 * intact once it reaches the server as raw bytes.
 */
export function encodeBase64Utf8(text: string): string {
    const bytes = new TextEncoder().encode(text);
    let binary = '';
    for (let i = 0; i < bytes.length; i++) {
        binary += String.fromCharCode(bytes[i]);
    }
    return btoa(binary);
}

/**
 * Decodes the base64 `key`/`value` fields of an {@link MVCCKeyValue} to
 * UTF-8 plaintext, returning a new object.
 *
 * Empty strings decode to empty strings (no-op); the field is only skipped
 * when falsy, which cannot happen for a well-formed wire object but is
 * guarded against defensively.
 */
export function decodeMVCCKV(kv: MVCCKeyValue): MVCCKeyValue {
    return {
        ...kv,
        key: kv.key ? decodeBase64Utf8(kv.key) : kv.key,
        value: kv.value ? decodeBase64Utf8(kv.value) : kv.value,
    };
}

/**
 * Decodes the `kv` and, when present, `prevKv` fields of a {@link
 * WatchEvent} from base64 to plaintext. See {@link decodeMVCCKV}.
 */
export function decodeWatchEventKV(evt: WatchEvent): WatchEvent {
    return {
        ...evt,
        kv: evt.kv ? decodeMVCCKV(evt.kv) : evt.kv,
        prevKv: evt.prevKv ? decodeMVCCKV(evt.prevKv) : evt.prevKv,
    };
}

/**
 * Decodes the `kvs` of each {@link OpResponse} (the `Get`/`Range` results
 * within a transaction or batch) from base64 to plaintext. See
 * {@link decodeMVCCKV}.
 */
export function decodeOpResponses(responses: readonly OpResponse[]): OpResponse[] {
    return responses.map((r) => (r.kvs ? { ...r, kvs: r.kvs.map(decodeMVCCKV) } : r));
}

// ---------------------------------------------------------------------------
// RestoreRevisionResult base64 → plaintext decoding
// ---------------------------------------------------------------------------
//
// `sdk/go/transport.RestoreKeyChange` carries `Key` as `[]byte` for the same
// binary-safety reason as `MVCCKeyValue`, so both wire protocols base64
// -encode it. Decoded here so every `RestoreKeyChange` the SDK returns is
// plaintext, matching `decodeMVCCKV`.

/**
 * Decodes the base64 `key` field of a {@link RestoreKeyChange} to UTF-8
 * plaintext, returning a new object. See {@link decodeMVCCKV}.
 */
export function decodeRestoreKeyChange(change: RestoreKeyChange): RestoreKeyChange {
    return {
        ...change,
        key: change.key ? decodeBase64Utf8(change.key) : change.key,
    };
}

/**
 * Decodes the `changes` and, when present, `conflicts` entries of a
 * {@link RestoreRevisionResult} from base64 to plaintext. See
 * {@link decodeRestoreKeyChange}.
 */
export function decodeRestoreResult(result: RestoreRevisionResult): RestoreRevisionResult {
    return {
        ...result,
        changes: result.changes.map(decodeRestoreKeyChange),
        conflicts: result.conflicts ? result.conflicts.map(decodeRestoreKeyChange) : result.conflicts,
    };
}

/**
 * Renders a share-holder enrolment in the server's wire form.
 *
 * It is built field by field, not through {@link deepCamelToSnake}: a
 * factor's `params` belong to the factor and are sent exactly as given, so
 * their keys must never be re-cased. Optional fields left unset are omitted.
 * Both transports send this object: REST as the JSON body, Wails as the
 * argument the Go binding decodes.
 */
export function holderEnrolmentWire(req: OfficerRegisterRequest): Record<string, unknown> {
    const wire: Record<string, unknown> = { username: req.username };
    if (req.role) wire.role = req.role;
    wire.csr_pem = req.csrPem;
    if (req.encryptionKem) wire.encryption_kem = req.encryptionKem;
    if (req.encryptionKey) wire.encryption_key = req.encryptionKey;
    if (req.factors && req.factors.length > 0) {
        wire.factors = req.factors.map((f) => (f.params ? { name: f.name, params: f.params } : { name: f.name }));
    }
    if (req.designated) wire.designated = req.designated;
    return wire;
}

/**
 * Normalizes a database barrier status decoded from either transport: a
 * barrier never initialized arrives without the time (REST) or with the zero
 * timestamp of the server's time type, "0001-01-01T00:00:00Z" (the desktop
 * binding), and is reported without `initializedAt` either way.
 */
export function normalizeDatabaseBarrierStatus(status: DatabaseBarrierStatus): DatabaseBarrierStatus {
    if (status.initializedAt !== undefined && status.initializedAt !== '' && !status.initializedAt.startsWith('0001-01-01')) {
        return status;
    }
    const { initializedAt: _never, ...rest } = status;
    return rest;
}

/**
 * Renders one `CeremonyInitializeRequest.officers` entry in the server's
 * wire form, field by field -- the same reason {@link holderEnrolmentWire}
 * is: a factor's `params` belong to the factor and are sent exactly as
 * given, so a blanket {@link deepCamelToSnake} over the whole ceremony
 * request would incorrectly re-case them. `qrdb init` is the ONLY system
 * bootstrap, so `CeremonyOfficerRequest` now carries the same
 * encryptionKem/encryptionKey/factors/designated fields
 * `OfficerRegisterRequest` does; this mirrors `holderEnrolmentWire` rather
 * than duplicating its field-by-field logic under a different name by
 * accident.
 */
export function ceremonyOfficerWire(o: CeremonyOfficerRequest): Record<string, unknown> {
    const wire: Record<string, unknown> = { username: o.username };
    if (o.role) wire.role = o.role;
    wire.csr_pem = o.csrPem;
    if (o.encryptionKem) wire.encryption_kem = o.encryptionKem;
    if (o.encryptionKey) wire.encryption_key = o.encryptionKey;
    if (o.factors && o.factors.length > 0) {
        wire.factors = o.factors.map((f) => (f.params ? { name: f.name, params: f.params } : { name: f.name }));
    }
    if (o.designated) wire.designated = o.designated;
    return wire;
}

/**
 * Normalizes a holder status decoded from either transport: a holder whose
 * envelope was never claimed arrives with the zero timestamp of the server's
 * time type ("0001-01-01T00:00:00Z"), which is reported as no claim at all so
 * `claimedAt` is present only for a real claim.
 */
export function normalizeOfficerStatus(status: OfficerStatus): OfficerStatus {
    if (status.claimedAt === undefined || !status.claimedAt.startsWith('0001-01-01')) {
        return status;
    }
    const { claimedAt: _unclaimed, ...rest } = status;
    return rest;
}
