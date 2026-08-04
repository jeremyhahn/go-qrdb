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

import type { DistanceMetric, VectorIndexType } from './types.js';

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
export function deepSnakeToCamel(value: unknown): unknown {
    if (Array.isArray(value)) {
        return value.map(deepSnakeToCamel);
    }
    if (value !== null && typeof value === 'object') {
        const out: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
            out[snakeToCamelKey(k)] = deepSnakeToCamel(v);
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
            out[camelToSnakeKey(k)] = deepCamelToSnake(v);
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
