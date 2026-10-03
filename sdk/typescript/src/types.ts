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

// ---------------------------------------------------------------------------
// Health and status responses
// ---------------------------------------------------------------------------

/** Health check response from a QRDB node. */
export interface HealthResponse {
    /** Current health status string (e.g. "ok"). */
    readonly status: string;
    /** Whether this node is the Raft leader. */
    readonly isLeader: boolean;
}

/** Detailed host and shard status information. */
export interface StatusResponse {
    /** Unique identifier for this node. */
    readonly nodeId: string;
    /** Raft transport address of this node. */
    readonly raftAddress: string;
    /** Whether this node is the Raft leader. */
    readonly isLeader: boolean;
    /** Replica ID of the current leader. */
    readonly leaderId: number;
    /** Number of shards loaded on this node. */
    readonly shardCount: number;
    /** Whether the node is in drain mode. */
    readonly isDraining: boolean;
    /** Per-shard status information. */
    readonly shards: unknown;
}

/** Peer membership for all loaded shards. */
export interface PeersResponse {
    /** Shard membership details. */
    readonly shards: readonly ShardPeers[];
}

/** Membership of a single shard. */
export interface ShardPeers {
    /** Shard identifier. */
    readonly shardId: number;
    /** Replica ID of the shard's leader. */
    readonly leaderId: number;
    /** Peers participating in this shard. */
    readonly peers: readonly PeerInfo[];
}

/** A single peer in a shard. */
export interface PeerInfo {
    /** Replica identifier. */
    readonly replicaId: number;
    /** Raft transport address. */
    readonly address: string;
    /** Whether this peer is the shard leader. */
    readonly isLeader: boolean;
}

// ---------------------------------------------------------------------------
// Topology types
// ---------------------------------------------------------------------------

/** Per-shard placement detail returned by GET /admin/topology. */
export interface ShardInfo {
    /** Shard identifier. */
    readonly shardId: number;
    /** Replica identifier for this node's replica of the shard. */
    readonly replicaId: number;
    /** Whether this node is currently the shard leader. */
    readonly isLeader: boolean;
    /** Replica ID of the current shard leader. Zero when unknown. */
    readonly leaderId: number;
    /** Mapping of replica ID → Raft transport address for all known members.
     * Keys are stringified uint64 replica IDs (JSON always uses string keys).
     * Absent/null on the wire when a shard has no known members yet — treat
     * as an empty map rather than assuming it is always present. */
    readonly nodes?: Readonly<Record<string, string>>;
}

/** Cluster topology as seen from a single node. Returned by GET /admin/topology. */
export interface TopologyResponse {
    /** Node identifier of the responding node. */
    readonly nodeId: string;
    /** Raft transport address of the responding node. */
    readonly raftAddress: string;
    /** Total number of Raft groups (shards) this node participates in. */
    readonly shardCount: number;
    /** Per-shard placement details for each Raft group. */
    readonly shards: readonly ShardInfo[];
}

/** Result of a drain operation. */
export interface DrainResponse {
    /** Status string indicating drain result. */
    readonly status: string;
    /** Error message, if the drain failed. */
    readonly error?: string;
}

/** Compile-time feature set of the QRDB binary. */
export interface CapabilitiesResponse {
    /** Whether basic key-value operations are available. */
    readonly kv: boolean;
    /** Whether MVCC revision tracking is available. */
    readonly mvcc: boolean;
    /** Whether compare-and-swap transactions are available. */
    readonly transaction: boolean;
    /** Whether atomic batch operations are available. */
    readonly batch: boolean;
    /** Whether watch event streaming is available. */
    readonly watch: boolean;
    /** Whether distributed lease management is available. */
    readonly lease: boolean;
    /** Whether secondary indexes are available. */
    readonly index: boolean;
    /** Whether time-series data operations are available. */
    readonly timeSeries: boolean;
    /** Whether vector database operations are available. */
    readonly vector: boolean;
}

/** Simple success response. */
export interface OKResponse {
    /** Status string (e.g. "ok"). */
    readonly status: string;
}

/** Simple error response. */
export interface ErrorResponse {
    /** Error message. */
    readonly error: string;
}

// ---------------------------------------------------------------------------
// KV request/response types
// ---------------------------------------------------------------------------

/** Key listing response. */
export interface KVListResponse {
    /** Sorted list of matching keys. */
    readonly keys: readonly string[];
}

/**
 * Prefix scan response returning key-value pairs. Values are
 * base64-encoded strings representing the raw byte content.
 */
export interface KVScanResponse {
    /** Map of key to base64-encoded value. */
    readonly pairs: Readonly<Record<string, string>>;
}

// ---------------------------------------------------------------------------
// Membership and snapshot request types
// ---------------------------------------------------------------------------

/** Parameters for adding a shard member. */
export interface AddMemberRequest {
    /** Target shard identifier. */
    readonly shardId: number;
    /** Replica identifier to add. */
    readonly replicaId: number;
    /** Raft transport address of the new member. */
    readonly address: string;
}

/** Parameters for removing a shard member. */
export interface RemoveMemberRequest {
    /** Target shard identifier. */
    readonly shardId: number;
    /** Replica identifier to remove. */
    readonly replicaId: number;
}

/** Parameters for requesting a shard snapshot. */
export interface SnapshotRequest {
    /** Target shard identifier. */
    readonly shardId: number;
}

// ---------------------------------------------------------------------------
// MVCC types
// ---------------------------------------------------------------------------

/**
 * A versioned key-value pair with MVCC revision metadata.
 *
 * On the wire (REST JSON and Wails IPC), Go marshals `key`/`value` as
 * base64 because the underlying transport struct carries them as `[]byte`.
 * The SDK decodes both to UTF-8 plaintext before returning `MVCCKeyValue`
 * to callers — the fields below are always plaintext, matching `kv.get`
 * and `kv.scan`. Never base64-decode them yourself.
 */
export interface MVCCKeyValue {
    /** Plaintext key (decoded from the wire's base64 representation). */
    readonly key: string;
    /** Plaintext value (decoded from the wire's base64 representation). */
    readonly value: string;
    /** Revision at which this key was first created. */
    readonly createRevision: number;
    /** Revision at which this key was last modified. */
    readonly modRevision: number;
    /** Monotonic version counter for this key. */
    readonly version: number;
    /** Lease attached to this key, or 0 if none. */
    readonly leaseId: number;
}

/**
 * Sort direction for range results.
 *
 * - `0` = None (natural key order)
 * - `1` = Ascending
 * - `2` = Descending
 */
export type SortOrder = 0 | 1 | 2;

/**
 * Field to sort range results by.
 *
 * - `0` = Key
 * - `1` = Version
 * - `2` = CreateRevision
 * - `3` = ModRevision
 * - `4` = Value
 */
export type SortTarget = 0 | 1 | 2 | 3 | 4;

/** Configuration for a range query. */
export interface RangeOptions {
    /** Maximum number of key-value pairs to return. */
    readonly limit?: number;
    /** Return results at this historical revision. */
    readonly revision?: number;
    /** When true, return only the count without key-value data. */
    readonly countOnly?: boolean;
    /** When true, return keys without values. */
    readonly keysOnly?: boolean;
    /** Sort direction. */
    readonly sortOrder?: SortOrder;
    /** Field to sort by. */
    readonly sortTarget?: SortTarget;
}

/** Response from a range query. */
export interface RangeResponse {
    /** Matching key-value pairs. */
    readonly kvs: readonly MVCCKeyValue[];
    /** Total count of matching keys. */
    readonly count: number;
}

/** Response from a PutWithLease operation. */
export interface PutWithLeaseResponse {
    /** The stored key-value pair with revision metadata. */
    readonly kv: MVCCKeyValue;
}

/** Response from a CurrentRevision query. */
export interface CurrentRevisionResponse {
    /** Latest committed MVCC revision. */
    readonly revision: number;
}

// ---------------------------------------------------------------------------
// Selective point-in-time restore
// ---------------------------------------------------------------------------

/**
 * Identifies the single change a restore plan makes to one key.
 *
 * - `'revert'` — the key exists both now and at the target revision; it is
 *   overwritten with the value it held at the target revision.
 * - `'recreate'` — the key existed at the target revision and does not
 *   exist now; it is re-created.
 * - `'delete'` — the key exists now and did not exist at the target
 *   revision; it is removed.
 */
export type RestoreAction = 'revert' | 'recreate' | 'delete';

/**
 * Describes a selective point-in-time restore: returns a bounded set of
 * keys to the value they held at an earlier MVCC revision by proposing new
 * writes (a logical undo, never a Raft rewind).
 *
 * Exactly one of `prefix` and `keys` must be set — an unscoped request
 * (neither set) is refused, and setting both is refused as ambiguous.
 * `prefix` and each element of `keys` are plain UTF-8 strings; the SDK
 * base64-encodes them before sending, matching the wire's binary-safe
 * `[]byte` field convention (the server accepts arbitrary byte-valued
 * keys, not just valid UTF-8).
 *
 * This operation requires the target daemon to run `raft.sm_type: mvcc`;
 * the default (`pebble`) has no MVCC revision history and the call fails
 * with an actionable "not implemented" error naming the fix.
 */
export interface RestoreRevisionRequest {
    /**
     * The target revision to restore the scoped keys to. Must be positive,
     * at or below the store's current committed revision, and above the
     * compaction floor.
     */
    readonly revision: number;
    /** Restore every key beginning with this prefix. Mutually exclusive with `keys`. */
    readonly prefix?: string;
    /** Restore this explicit list of keys. Mutually exclusive with `prefix`. */
    readonly keys?: readonly string[];
    /** Compute and return the plan without writing anything. */
    readonly dryRun?: boolean;
    /**
     * Apply the plan unconditionally, overwriting any key that has been
     * written since the plan was computed. The default (`false`) is
     * compare-and-swap: a key that moved on since the plan was computed is
     * left alone and reported in {@link RestoreRevisionResult.conflicts}.
     */
    readonly force?: boolean;
}

/**
 * One entry in a restore plan: the change that would be (or was) applied
 * to a single key.
 *
 * The restored value is deliberately not carried here — only its length
 * (`valueBytes`) — so a plan is not a bulk data export of the keyspace at
 * a historical revision.
 */
export interface RestoreKeyChange {
    /** Plaintext key (decoded from the wire's base64 representation). */
    readonly key: string;
    /** The change applied to `key`. */
    readonly action: RestoreAction;
    /** The key's current ModRevision, or 0 when the key does not currently exist (a recreate). */
    readonly fromRevision: number;
    /** The ModRevision of the version being restored, or 0 when the key did not exist at the target revision (a delete). */
    readonly toRevision: number;
    /** The length of the value the change writes, or 0 for a delete. */
    readonly valueBytes: number;
}

/** Reports the outcome of a selective restore. */
export interface RestoreRevisionResult {
    /** The target revision the plan was computed against. */
    readonly revision: number;
    /**
     * The plan, ordered by key. On a dry run this is the plan that would
     * be applied; on an apply it is the plan that was computed —
     * including any entry that turned out to conflict, which is reported
     * in {@link conflicts} rather than removed from here.
     */
    readonly changes: readonly RestoreKeyChange[];
    /**
     * The plan entries that were NOT applied because the key changed
     * between the diff and the write. Only populated when the request did
     * not set `force`.
     */
    readonly conflicts?: readonly RestoreKeyChange[];
    /** The number of changes committed. Always 0 for a dry run. */
    readonly applied: number;
    /**
     * The number of in-scope keys whose current state already matched the
     * target revision, including keys absent in both.
     */
    readonly skipped: number;
    /** `conflicts.length`: the number of keys left untouched because they changed since the plan was computed. */
    readonly conflicted: number;
    /** Echoes the request's `dryRun` flag. */
    readonly dryRun: boolean;
}

// ---------------------------------------------------------------------------
// Transaction types
// ---------------------------------------------------------------------------

/**
 * Selects which field of a key-value is compared in a transaction
 * precondition.
 *
 * - `0` = Version
 * - `1` = CreateRevision
 * - `2` = ModRevision
 * - `3` = Value
 */
export type CompareTarget = 0 | 1 | 2 | 3;

/**
 * Comparison operator for a transaction precondition.
 *
 * - `0` = Equal
 * - `1` = Greater
 * - `2` = Less
 * - `3` = NotEqual
 */
export type CompareResult = 0 | 1 | 2 | 3;

/** A single precondition in a transaction. */
export interface Compare {
    /** Base64-encoded key to compare. */
    readonly key: string;
    /** Which field to compare. */
    readonly target: CompareTarget;
    /** Comparison operator. */
    readonly result: CompareResult;
    /** Base64-encoded value for value comparisons. */
    readonly value?: string;
    /** Version for version comparisons. */
    readonly version?: number;
    /** Creation revision for create-revision comparisons. */
    readonly createRevision?: number;
    /** Modification revision for mod-revision comparisons. */
    readonly modRevision?: number;
}

/**
 * Identifies the type of operation in a transaction or batch.
 *
 * - `0` = Put
 * - `1` = Get
 * - `2` = Delete
 * - `3` = Range
 */
export type OpType = 0 | 1 | 2 | 3;

/** A single operation within a transaction or batch. */
export interface Op {
    /** Operation type. */
    readonly type: OpType;
    /** Plain-text key. The REST and Wails handlers treat this as a UTF-8 string. */
    readonly key: string;
    /** Plain-text value (for put operations). */
    readonly value?: string;
    /** Plain-text range end key (for range operations). */
    readonly rangeEnd?: string;
    /** Lease to attach (for put operations). */
    readonly leaseId?: number;
}

/** Result of a single operation within a transaction or batch. */
export interface OpResponse {
    /** Key-value pairs returned by get/range operations. */
    readonly kvs?: readonly MVCCKeyValue[];
    /** Count of matching keys (for range operations). */
    readonly count?: number;
    /** Whether this individual operation succeeded. */
    readonly succeeded: boolean;
}

/** Payload for executing a compare-and-swap transaction. */
export interface TransactionRequest {
    /** Preconditions that must all pass for success ops to execute. */
    readonly compares: readonly Compare[];
    /** Operations applied when all compares succeed. */
    readonly successOps: readonly Op[];
    /** Operations applied when any compare fails. */
    readonly failureOps: readonly Op[];
}

/** Result of a transaction execution. */
export interface TransactionResponse {
    /** Whether all preconditions were satisfied. */
    readonly succeeded: boolean;
    /** Revision at which the transaction was applied. */
    readonly revision: number;
    /** Per-operation results. */
    readonly responses: readonly OpResponse[];
}

// ---------------------------------------------------------------------------
// Batch types
// ---------------------------------------------------------------------------

/** Payload for executing an atomic batch. */
export interface BatchRequest {
    /** Operations to execute atomically. */
    readonly ops: readonly Op[];
}

/** Result of a batch execution. */
export interface BatchResponse {
    /** Per-operation results. */
    readonly responses: readonly OpResponse[];
}

// ---------------------------------------------------------------------------
// Batch import types
// ---------------------------------------------------------------------------

/** Options for a batch import operation. */
export interface ImportOptions {
    /**
     * File format: "csv", "json", "ndjson", "yaml", "archive", or "auto".
     * When "auto" or empty, the format is inferred from the filename and
     * the first 8 KB of data.
     */
    readonly format: string;
    /** Field name to extract as the key. */
    readonly key_field: string;
    /**
     * Value extraction mode: "field" uses a specific field, "row"
     * serializes the entire row as JSON.
     */
    readonly value_mode: string;
    /** Field name for the value when value_mode is "field". */
    readonly value_field?: string;
    /** Whether to skip the first row in CSV files. */
    readonly skip_header?: boolean;
    /**
     * When true, any error causes the entire import to be rolled back.
     */
    readonly transactional?: boolean;
    /** Optional target collection name. */
    readonly collection?: string;
    /** Optional target database name. */
    readonly database?: string;
}

/** A single row that failed during import. */
export interface ImportError {
    /** 1-based row number that failed. */
    readonly row: number;
    /** Extracted key for the failed row. */
    readonly key: string;
    /** Description of why the row failed. */
    readonly message: string;
}

/** Statistics from a batch import operation. */
export interface ImportResult {
    /** Total number of rows processed. */
    readonly total: number;
    /** Number of rows successfully imported. */
    readonly succeeded: number;
    /** Number of rows that failed to import. */
    readonly failed: number;
    /** Details about failed rows. */
    readonly errors?: readonly ImportError[];
}

// ---------------------------------------------------------------------------
// Watch types
// ---------------------------------------------------------------------------

/**
 * Type of watch event.
 *
 * - `0` = Put (key created or updated)
 * - `1` = Delete (key removed)
 */
export type EventType = 0 | 1;

/** A single change event for a watched key. */
export interface WatchEvent {
    /** Event type (put or delete). */
    readonly type: EventType;
    /** Current key-value state after the event. */
    readonly kv: MVCCKeyValue;
    /** Previous key-value state before the event (when requested). */
    readonly prevKv?: MVCCKeyValue;
}

/** Configuration for a watch request. */
export interface WatchOptions {
    /** Start watching from this revision (0 for current). */
    readonly startRevision?: number;
    /** Include the previous key-value in events. */
    readonly prevKV?: boolean;
    /** Enable periodic progress notifications. */
    readonly progressNotify?: boolean;
}

/** Delivers watch events from the server. */
export interface WatchResponse {
    /** Server-assigned watch identifier. */
    readonly watchId: number;
    /** Events in this batch. */
    readonly events: readonly WatchEvent[];
    /** Whether this watch has been canceled. */
    readonly canceled?: boolean;
    /** Reason for cancellation, when applicable. */
    readonly cancelReason?: string;
    /** Revision at which these events were emitted. */
    readonly revision: number;
}

// ---------------------------------------------------------------------------
// Lease types
// ---------------------------------------------------------------------------

/** Describes a distributed TTL lease. */
export interface LeaseInfo {
    /** Lease identifier. */
    readonly id: number;
    /** Current TTL in seconds. */
    readonly ttl: number;
    /** Originally granted TTL in seconds. */
    readonly grantedTtl: number;
    /** Remaining TTL in seconds. */
    readonly remainingTtl: number;
    /** Keys attached to this lease. */
    readonly keys?: readonly string[];
}

/** Response from a LeaseList query. */
export interface LeaseListResponse {
    /** All active leases. */
    readonly leases: readonly LeaseInfo[];
}

// ---------------------------------------------------------------------------
// Lock types
// ---------------------------------------------------------------------------

/** Describes an acquired distributed lock. */
export interface LockInfo {
    /** User-provided lock key. */
    readonly key: string;
    /** MVCC ModRevision at acquisition. Monotonically increasing fencing token. */
    readonly fencingToken: number;
    /** Underlying lease ID that controls the TTL. */
    readonly leaseId: number;
    /** Unique identifier for the lock holder. */
    readonly holderId: string;
    /** Whether re-acquisition by the same holder is permitted. */
    readonly reentrant: boolean;
    /** Number of times the reentrant holder has acquired the lock. Always 1 for non-reentrant locks. */
    readonly acquireCount: number;
    /** Lock TTL in seconds. */
    readonly ttl: number;
    /** Unix timestamp in seconds when the lock was first acquired. */
    readonly acquiredAt: number;
}

/** Parameters for acquiring a distributed lock. */
export interface LockAcquireRequest {
    /** Resource key to lock. */
    readonly key: string;
    /** Unique holder identifier. A UUID is generated when omitted. */
    readonly holder_id?: string;
    /** Lock TTL in seconds. */
    readonly ttl: number;
    /** Allow re-acquisition by the same holder. */
    readonly reentrant?: boolean;
}

/** Parameters for releasing a distributed lock. */
export interface LockReleaseRequest {
    /** Resource key to unlock. */
    readonly key: string;
    /** Fencing token returned at acquisition. */
    readonly fencing_token: number;
}

/** Parameters for extending a lock's TTL. */
export interface LockExtendRequest {
    /** Resource key of the held lock. */
    readonly key: string;
    /** Fencing token returned at acquisition. */
    readonly fencing_token: number;
}

/** Response from a LockList query. */
export interface LockListResponse {
    /** All active locks. */
    readonly locks: readonly LockInfo[];
}

// ---------------------------------------------------------------------------
// Index types
// ---------------------------------------------------------------------------

/** Parameters for creating a secondary index. */
export interface IndexCreateRequest {
    /** Index name. */
    readonly name: string;
    /** Name of the field to index. */
    readonly fieldName: string;
    /** Whether the index enforces unique values. */
    readonly unique?: boolean;
    /** Whether the index skips entries missing the field. */
    readonly sparse?: boolean;
    /** Whether values are encoded as fixed-width integers for numeric range scans. */
    readonly numeric?: boolean;
}

/** Parameters for querying an index by exact value. */
export interface IndexQueryRequest {
    /** Name of the index to query. */
    readonly indexName: string;
    /** Base64-encoded value to match. */
    readonly value: string;
}

/** Response from an index query containing matching entity keys. */
export interface IndexQueryResponse {
    /** Entity keys matching the query. */
    readonly keys: readonly string[];
}

/** Parameters for scanning an index within a value range. */
export interface IndexScanRequest {
    /** Name of the index to scan. */
    readonly indexName: string;
    /** Base64-encoded start of the scan range (inclusive). */
    readonly start: string;
    /** Base64-encoded end of the scan range (exclusive). */
    readonly end: string;
}

/** Maps an index value to its matching entity keys. */
export interface IndexScanEntry {
    /** Human-readable index value. */
    readonly value: string;
    /** Entity keys matching this value. */
    readonly keys: readonly string[];
}

/** Response from an index scan. */
export interface IndexScanResponse {
    /** Entries matching the scan range. */
    readonly entries: readonly IndexScanEntry[];
}

/** Response from listing all registered indexes. */
export interface IndexListResponse {
    /** Names of all registered indexes. */
    readonly names: readonly string[];
}

// ---------------------------------------------------------------------------
// Time-series types
// ---------------------------------------------------------------------------

/** A key-value label for time-series data. */
export interface TSTag {
    /** Tag key. */
    readonly key: string;
    /** Tag value. */
    readonly value: string;
}

/** A single time-series measurement. */
export interface TSDataPoint {
    /** Metric name. */
    readonly metric: string;
    /** Measurement value. */
    readonly value: number;
    /** Timestamp in nanoseconds since epoch. */
    readonly timestampNs: number;
    /** Optional labels. */
    readonly tags?: readonly TSTag[];
}

/** Parameters for querying time-series data points. */
export interface TSQueryRequest {
    /** Metric name to query. */
    readonly metric: string;
    /** Start of the time range in nanoseconds (inclusive). */
    readonly startNs: number;
    /** End of the time range in nanoseconds (exclusive). */
    readonly endNs: number;
    /** Optional tag filters. */
    readonly tags?: readonly TSTag[];
    /** Maximum number of points to return. */
    readonly limit?: number;
    /** Return points in descending order. */
    readonly descending?: boolean;
}

/** Response containing matching time-series data points. */
export interface TSQueryResponse {
    /** Matching data points. */
    readonly points: readonly TSDataPoint[];
}

/**
 * Aggregation function identifier.
 *
 * - `0` = None
 * - `1` = Avg
 * - `2` = Sum
 * - `3` = Min
 * - `4` = Max
 * - `5` = Count
 * - `6` = Percentile
 * - `7` = First
 * - `8` = Last
 */
export type AggFunc = 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8;

/** Parameters for computing an aggregation over time-series data. */
export interface TSAggregateRequest {
    /** Metric name to aggregate. */
    readonly metric: string;
    /** Start of the time range in nanoseconds (inclusive). */
    readonly startNs: number;
    /** End of the time range in nanoseconds (exclusive). */
    readonly endNs: number;
    /** Aggregation function to apply. */
    readonly function: AggFunc;
    /** Interval width in nanoseconds for bucketed aggregation. */
    readonly intervalNs?: number;
}

/** A single aggregation result for one time interval. */
export interface TSAggResult {
    /** Interval start timestamp in nanoseconds. */
    readonly timestampNs: number;
    /** Aggregated value. */
    readonly value: number;
    /** Number of data points in this interval. */
    readonly count: number;
}

/** Response containing aggregation results. */
export interface TSAggregateResponse {
    /** Per-interval aggregation results. */
    readonly results: readonly TSAggResult[];
}

/** Parameters for a rich query with aggregation, downsampling, and grouping. */
export interface TSRichQueryRequest {
    /** Metric name to query. */
    readonly metric: string;
    /** Start of the time range in nanoseconds (inclusive). */
    readonly startNs: number;
    /** End of the time range in nanoseconds (exclusive). */
    readonly endNs: number;
    /** Optional tag filters. */
    readonly tags?: readonly TSTag[];
    /** Aggregation function to apply. */
    readonly aggregation?: AggFunc;
    /** Interval width in nanoseconds for downsampling. */
    readonly intervalNs?: number;
    /** Tag keys to group results by. */
    readonly groupBy?: readonly string[];
    /** Maximum number of results to return. */
    readonly limit?: number;
    /** Percentile value (0-100) when using percentile aggregation. */
    readonly percentile?: number;
}

/** Query results grouped by tag values. */
export interface TSGroupedResult {
    /** Tag key-value pairs defining this group. */
    readonly tags: readonly TSTag[];
    /** Data points in this group. */
    readonly points?: readonly TSDataPoint[];
    /** Aggregated value for this group, when aggregation is applied. */
    readonly aggregatedValue?: number;
}

/** Response from a rich query. */
export interface TSRichQueryResponse {
    /** Metric name. */
    readonly metric: string;
    /** Tags matching the query. */
    readonly tags?: readonly TSTag[];
    /** Matching data points. */
    readonly points: readonly TSDataPoint[];
    /** Overall aggregated value, when aggregation is applied. */
    readonly aggregatedValue?: number;
    /** Results grouped by tag values. */
    readonly groupedResults?: readonly TSGroupedResult[];
}

/** Parameters for deleting time-series data. */
export interface TSDeleteRequest {
    /** Metric name to delete. */
    readonly metric: string;
    /** Optional tag filters to narrow deletion scope. */
    readonly tags?: readonly TSTag[];
    /** Start of the time range in nanoseconds (inclusive, 0 for all). */
    readonly startNs?: number;
    /** End of the time range in nanoseconds (exclusive, 0 for all). */
    readonly endNs?: number;
}

/** Response from a time-series delete operation (empty body). */
export type TSDeleteResponse = Record<string, never>;

/** Data retention and downsampling policy for a metric. */
export interface TSRetentionPolicy {
    /** Retention duration in nanoseconds. */
    readonly durationNs: number;
    /** Downsampling resolution in nanoseconds. */
    readonly resolutionNs?: number;
    /** Aggregation function for downsampled data. */
    readonly aggregation?: AggFunc;
}

/** Parameters for retrieving a metric's retention policy. */
export interface TSGetRetentionRequest {
    /** Metric name. */
    readonly metric: string;
}

/** Response containing the retention policy. */
export interface TSGetRetentionResponse {
    /** The retention policy, or null if none is set. */
    readonly policy: TSRetentionPolicy | null;
}

/** Parameters for setting a metric's retention policy. */
export interface TSSetRetentionRequest {
    /** Metric name. */
    readonly metric: string;
    /** Retention policy to apply. */
    readonly policy: TSRetentionPolicy;
}

/** Parameters for listing metric names. */
export interface TSListMetricsRequest {
    /** Prefix filter for metric names. */
    readonly prefix?: string;
    /** Maximum number of metric names to return. */
    readonly limit?: number;
}

/** Response containing metric names. */
export interface TSListMetricsResponse {
    /** Matching metric names. */
    readonly metrics: readonly string[];
}

// ---------------------------------------------------------------------------
// Vector types
// ---------------------------------------------------------------------------

/**
 * Distance metric for measuring vector similarity.
 *
 * - `0` = Cosine
 * - `1` = Euclidean
 * - `2` = DotProduct
 */
export type DistanceMetric = 0 | 1 | 2;

/**
 * Vector index algorithm.
 *
 * - `0` = HNSW (hierarchical navigable small world)
 * - `1` = Flat (brute-force exact search)
 * - `2` = IVFFlat (inverted file index with flat quantization)
 */
export type VectorIndexType = 0 | 1 | 2;

/** Parameters for creating a vector namespace. */
export interface CreateNamespaceRequest {
    /** Namespace name. */
    readonly namespace: string;
    /** Vector dimensionality. */
    readonly dimension: number;
    /** Distance metric for similarity measurement. */
    readonly distanceMetric: DistanceMetric;
    /** Index algorithm. */
    readonly indexType: VectorIndexType;
    /** Algorithm-specific index configuration parameters. */
    readonly indexParams?: Readonly<Record<string, unknown>>;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Result of a namespace creation. */
export interface CreateNamespaceResponse {
    /** Whether the namespace was newly created. */
    readonly created: boolean;
    /** Server timestamp of creation. */
    readonly timestamp: number;
}

/** Parameters for deleting a vector namespace. */
export interface DeleteNamespaceRequest {
    /** Namespace to delete. */
    readonly namespace: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Parameters for listing vector namespaces. */
export interface ListNamespacesRequest {
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Response containing namespace names. */
export interface ListNamespacesResponse {
    /** All namespace names. */
    readonly namespaces: readonly string[];
}

/** Parameters for retrieving namespace metadata. */
export interface GetNamespaceInfoRequest {
    /** Namespace name. */
    readonly namespace: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Metadata about a vector namespace. */
export interface NamespaceInfo {
    /** Namespace name. */
    readonly namespace: string;
    /** Vector dimensionality. */
    readonly dimension: number;
    /** Distance metric. */
    readonly distanceMetric: DistanceMetric;
    /** Index algorithm. */
    readonly indexType: VectorIndexType;
    /** Total number of vectors in the namespace. */
    readonly vectorCount: number;
    /** Creation timestamp. */
    readonly createdAt: number;
    /** Last update timestamp. */
    readonly updatedAt: number;
}

/** A single vector with its metadata. */
export interface VectorData {
    /** Unique vector identifier. */
    readonly id: string;
    /** Vector components. */
    readonly vector: readonly number[];
    /** Arbitrary metadata key-value pairs. */
    readonly metadata?: Readonly<Record<string, unknown>>;
    /** Monotonic version counter. */
    readonly version: number;
    /** Creation timestamp. */
    readonly createdAt: number;
    /** Last update timestamp. */
    readonly updatedAt: number;
}

/** Parameters for inserting or updating a vector. */
export interface UpsertVectorRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Unique vector identifier. */
    readonly id: string;
    /** Vector components. */
    readonly vector: readonly number[];
    /** Arbitrary metadata key-value pairs. */
    readonly metadata?: Readonly<Record<string, unknown>>;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Result of a vector upsert. */
export interface UpsertVectorResponse {
    /** New version number of the vector. */
    readonly version: number;
    /** Server timestamp of the upsert. */
    readonly timestamp: number;
    /** Whether the vector was newly created (vs updated). */
    readonly created: boolean;
}

/** Parameters for retrieving a single vector. */
export interface GetVectorRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Vector identifier. */
    readonly id: string;
    /** Consistency level (0 = default). */
    readonly consistency?: number;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Parameters for deleting a single vector. */
export interface DeleteVectorRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Vector identifier. */
    readonly id: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Parameters for bulk vector upserts. */
export interface BatchUpsertRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Vectors to upsert. */
    readonly vectors: readonly VectorData[];
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Result of a batch upsert operation. */
export interface BatchUpsertResponse {
    /** Number of vectors successfully upserted. */
    readonly succeeded: number;
    /** Number of vectors that failed. */
    readonly failed: number;
    /** Per-vector errors for failed upserts. */
    readonly errors?: readonly BatchVectorError[];
}

/** Describes a failure for a single vector in a batch operation. */
export interface BatchVectorError {
    /** Vector identifier that failed. */
    readonly id: string;
    /** Error message. */
    readonly error: string;
}

/** Parameters for bulk vector deletion. */
export interface BatchDeleteVectorsRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Vector identifiers to delete. */
    readonly ids: readonly string[];
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Result of a batch vector deletion. */
export interface BatchDeleteVectorsResponse {
    /** Number of vectors successfully deleted. */
    readonly deleted: number;
    /** Number of vectors that failed to delete. */
    readonly failed: number;
    /** Per-vector errors for failed deletions. */
    readonly errors?: readonly BatchVectorError[];
}

/** Parameters for paginated vector listing. */
export interface ListVectorsRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Maximum number of vectors to return. */
    readonly limit?: number;
    /** Number of vectors to skip. */
    readonly offset?: number;
    /** Cursor for cursor-based pagination. */
    readonly cursor?: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Paginated list of vectors. */
export interface ListVectorsResponse {
    /** Vectors in this page. */
    readonly vectors: readonly VectorData[];
    /** Cursor for the next page, empty when no more results. */
    readonly nextCursor?: string;
    /** Total number of vectors in the namespace. */
    readonly total: number;
}

/** Parameters for updating vector metadata. */
export interface UpdateMetadataRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Vector identifier. */
    readonly id: string;
    /** New metadata key-value pairs (replaces existing metadata). */
    readonly metadata: Readonly<Record<string, unknown>>;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** A single result from a similarity search. */
export interface VectorSearchResult {
    /** Vector identifier. */
    readonly id: string;
    /** Similarity score. */
    readonly score: number;
    /** Vector components (when requested). */
    readonly vector?: readonly number[];
    /** Metadata key-value pairs (when present). */
    readonly metadata?: Readonly<Record<string, unknown>>;
}

/** Parameters for a k-nearest-neighbor similarity search. */
export interface SearchSimilarRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Query vector. */
    readonly vector: readonly number[];
    /** Number of nearest neighbors to return. */
    readonly k: number;
    /** HNSW search expansion factor. */
    readonly efSearch?: number;
    /** Minimum similarity score threshold. */
    readonly minScore?: number;
    /** Consistency level (0 = default). */
    readonly consistency?: number;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Results of a similarity search. */
export interface SearchSimilarResponse {
    /** Ordered search results. */
    readonly results: readonly VectorSearchResult[];
    /** Search execution time in nanoseconds. */
    readonly took: number;
}

/** Parameters for metadata-only vector search. */
export interface SearchByMetadataRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Metadata filter criteria. */
    readonly filters: Readonly<Record<string, unknown>>;
    /** Maximum number of results. */
    readonly limit?: number;
    /** Number of results to skip. */
    readonly offset?: number;
    /** Consistency level (0 = default). */
    readonly consistency?: number;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Results of a metadata search. */
export interface SearchByMetadataResponse {
    /** Matching vectors. */
    readonly results: readonly VectorData[];
    /** Total number of matching vectors. */
    readonly total: number;
}

/** Parameters for combined vector similarity and metadata filtering. */
export interface HybridSearchRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Query vector for similarity component. */
    readonly vector: readonly number[];
    /** Metadata filter criteria. */
    readonly metadataFilters: Readonly<Record<string, unknown>>;
    /** Number of results to return. */
    readonly k: number;
    /** Weight applied to vector similarity score (0-1). */
    readonly vectorWeight: number;
    /** Weight applied to metadata match score (0-1). */
    readonly metadataWeight: number;
    /** Consistency level (0 = default). */
    readonly consistency?: number;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Results of a hybrid search. */
export interface HybridSearchResponse {
    /** Ordered search results. */
    readonly results: readonly VectorSearchResult[];
    /** Search execution time in nanoseconds. */
    readonly took: number;
}

/** Parameters for searching by an existing vector's ID. */
export interface SearchByIDRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** ID of the vector to use as the search query. */
    readonly id: string;
    /** Number of nearest neighbors to return. */
    readonly k: number;
    /** Consistency level (0 = default). */
    readonly consistency?: number;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Parameters for triggering an index rebuild. */
export interface RebuildIndexRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Result of an index rebuild request. */
export interface RebuildIndexResponse {
    /** Job identifier for tracking progress. */
    readonly jobId: string;
    /** Whether the rebuild was started. */
    readonly started: boolean;
    /** Server timestamp of the rebuild request. */
    readonly timestamp: number;
}

/** Parameters for querying rebuild progress. */
export interface GetRebuildProgressRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Job identifier. */
    readonly jobId: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Current state of an index rebuild job. */
export interface RebuildProgress {
    /** Job identifier. */
    readonly jobId: string;
    /** Job status (e.g. "running", "completed", "failed"). */
    readonly status: string;
    /** Completion progress (0.0 to 1.0). */
    readonly progress: number;
    /** Timestamp when the rebuild started. */
    readonly startedAt: number;
    /** Timestamp when the rebuild finished (0 if still running). */
    readonly finishedAt?: number;
    /** Error message if the rebuild failed. */
    readonly error?: string;
}

/** Parameters for querying index statistics. */
export interface GetIndexStatsRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Statistics about a namespace's vector index. */
export interface IndexStats {
    /** Total number of indexed vectors. */
    readonly vectorCount: number;
    /** Vector dimensionality. */
    readonly dimension: number;
    /** Distance metric. */
    readonly distanceMetric: DistanceMetric;
    /** Index algorithm. */
    readonly indexType: VectorIndexType;
    /** Estimated memory usage in bytes. */
    readonly memoryUsage: number;
    /** Estimated disk usage in bytes. */
    readonly diskUsage: number;
}

/** Parameters for triggering namespace compaction. */
export interface CompactNamespaceRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Result of a compaction request. */
export interface CompactNamespaceResponse {
    /** Job identifier for tracking progress. */
    readonly jobId: string;
    /** Whether the compaction was started. */
    readonly started: boolean;
}

/** Parameters for querying compaction progress. */
export interface GetCompactionProgressRequest {
    /** Target namespace. */
    readonly namespace: string;
    /** Job identifier. */
    readonly jobId: string;
    /** Target shard identifier. */
    readonly shardId?: number;
}

/** Current state of a compaction job. */
export interface CompactionProgress {
    /** Job identifier. */
    readonly jobId: string;
    /** Job status (e.g. "running", "completed", "failed"). */
    readonly status: string;
    /** Completion progress (0.0 to 1.0). */
    readonly progress: number;
    /** Timestamp when the compaction started. */
    readonly startedAt: number;
    /** Timestamp when the compaction finished (0 if still running). */
    readonly finishedAt?: number;
    /** Error message if the compaction failed. */
    readonly error?: string;
}

// ---------------------------------------------------------------------------
// User management types
// ---------------------------------------------------------------------------

/**
 * A passwordless user entity. Authentication is exclusively via mTLS
 * X.509 certificates.
 */
export interface User {
    /** Globally unique user identifier. */
    readonly id: string;
    /** Unique username within the tenant. */
    readonly username: string;
    /** Tenant that owns this user. */
    readonly tenantId: string;
    /** Role IDs assigned to this user. */
    readonly roleIds: readonly string[];
    /**
     * Canonical account state: pending, active, suspended, locked, disabled
     * or deleting. Only active can authenticate.
     */
    readonly lifecycle: string;
    /** Whether this is a built-in system user. */
    readonly system: boolean;
    /** ISO 8601 creation timestamp. */
    readonly createdAt: string;
    /** ISO 8601 last update timestamp. */
    readonly updatedAt: string;
}

/** Parameters for creating a new user. */
export interface CreateUserRequest {
    /** Unique username within the tenant. */
    readonly username: string;
    /** Target tenant ID (defaults to the caller's tenant). */
    readonly tenantId?: string;
    /** Role IDs to assign at creation time. */
    readonly roleIds?: readonly string[];
}

/** Parameters for updating an existing user. */
export interface UpdateUserRequest {
    /** User ID to update. */
    readonly id: string;
    /** New username. */
    readonly username?: string;
    /** New set of role IDs. */
    readonly roleIds?: readonly string[];
}

/** Parameters for binding an mTLS certificate thumbprint to a user. */
export interface AddCertBindingRequest {
    /** User ID to bind the certificate thumbprint to. */
    readonly id: string;
    /** Base64url SHA-256 SPKI thumbprint of the certificate. */
    readonly thumbprint: string;
    /** Optional unix-seconds expiry; 0 or omitted means the binding never expires. */
    readonly notAfterUnix?: number;
}

/** Parameters for listing users. */
export interface ListUsersRequest {
    /** Filter by tenant ID. */
    readonly tenantId?: string;
    /** Page number (1-indexed). */
    readonly page?: number;
    /** Results per page. */
    readonly pageSize?: number;
}

/** Paginated user list response. */
export interface ListUsersResponse {
    /** Users on this page. */
    readonly users: readonly User[];
    /** Total number of users matching the filter. */
    readonly total: number;
    /** Whether more pages are available. */
    readonly hasMore: boolean;
}

// ---------------------------------------------------------------------------
// Role management types
// ---------------------------------------------------------------------------

/**
 * An RBAC role entity. Roles aggregate fine-grained permissions and can
 * be assigned to users.
 */
export interface Role {
    /** Globally unique role identifier. */
    readonly id: string;
    /** Unique role name within the tenant. */
    readonly name: string;
    /** Tenant that owns this role. */
    readonly tenantId: string;
    /** Permission strings assigned to this role. */
    readonly permissions: readonly string[];
    /** Whether this is a built-in system role. */
    readonly system: boolean;
    /** ISO 8601 creation timestamp. */
    readonly createdAt: string;
    /** ISO 8601 last update timestamp. */
    readonly updatedAt: string;
}

/** Parameters for creating a new role. */
export interface CreateRoleRequest {
    /** Unique role name within the tenant. */
    readonly name: string;
    /** Target tenant ID (defaults to the caller's tenant). */
    readonly tenantId?: string;
    /** Permission strings to assign. */
    readonly permissions: readonly string[];
}

/** Parameters for updating an existing role. */
export interface UpdateRoleRequest {
    /** Role ID to update. */
    readonly id: string;
    /** New role name. */
    readonly name?: string;
    /** New set of permission strings. */
    readonly permissions?: readonly string[];
}

/** Parameters for listing roles. */
export interface ListRolesRequest {
    /** Filter by tenant ID. */
    readonly tenantId?: string;
    /** Page number (1-indexed). */
    readonly page?: number;
    /** Results per page. */
    readonly pageSize?: number;
}

/** Paginated role list response. */
export interface ListRolesResponse {
    /** Roles on this page. */
    readonly roles: readonly Role[];
    /** Total number of roles matching the filter. */
    readonly total: number;
    /** Whether more pages are available. */
    readonly hasMore: boolean;
}

/** Parameters for assigning a role to a user. */
export interface AssignRoleRequest {
    /** User ID. */
    readonly userId: string;
    /** Role ID. */
    readonly roleId: string;
}

/** Parameters for unassigning a role from a user. */
export interface UnassignRoleRequest {
    /** User ID. */
    readonly userId: string;
    /** Role ID. */
    readonly roleId: string;
}

/**
 * Groups related permissions under a display name and logical group for
 * UI rendering.
 */
export interface PermissionCategory {
    /** Display name for this permission category. */
    readonly name: string;
    /** Logical group (e.g. "kv", "admin", "vector"). */
    readonly group: string;
    /** Permission strings in this category. */
    readonly permissions: readonly string[];
}

/** Response containing all permission categories. */
export interface ListPermissionsResponse {
    /** Permission categories for UI rendering. */
    readonly categories: readonly PermissionCategory[];
}

// ---------------------------------------------------------------------------
// Certificate management types
// ---------------------------------------------------------------------------

/** Parameters for generating a client certificate. */
export interface GenerateCertRequest {
    /** Username to embed in the certificate subject. */
    readonly username: string;
    /** Target tenant ID. */
    readonly tenantId?: string;
    /** Certificate validity duration (e.g. "8760h"). */
    readonly ttl?: string;
}

/**
 * PEM-encoded certificate and private key returned after generating a
 * client certificate.
 */
export interface CertBundle {
    /** PEM-encoded X.509 certificate. */
    readonly certPEM: string;
    /** PEM-encoded private key. */
    readonly keyPEM: string;
    /** Certificate serial number. */
    readonly serial: string;
}

/** Parameters for exporting a certificate as PKCS#12. */
export interface ExportCertRequest {
    /** PEM-encoded certificate. */
    readonly certPEM: string;
    /** PEM-encoded private key. */
    readonly keyPEM: string;
    /** Passphrase for the PKCS#12 archive. */
    readonly passphrase: string;
}

/** Response containing the exported PKCS#12 archive. */
export interface ExportCertResponse {
    /** Base64-encoded PKCS#12 archive. */
    readonly pkcs12: string;
}

/** Metadata for an issued certificate. */
export interface CertRecord {
    /** Certificate serial number. */
    readonly serial: string;
    /** Username embedded in the certificate. */
    readonly username: string;
    /** Tenant the certificate belongs to. */
    readonly tenantId: string;
    /** ISO 8601 issuance timestamp. */
    readonly issuedAt: string;
    /** ISO 8601 expiration timestamp. */
    readonly expiresAt: string;
    /** Whether this certificate has been revoked. */
    readonly revoked: boolean;
}

/** Response containing issued certificate records. */
export interface ListCertsResponse {
    /** Issued certificate records. */
    readonly certs: readonly CertRecord[];
}

// ---------------------------------------------------------------------------
// Seal types
// ---------------------------------------------------------------------------

/** Current seal state of the server. */
export interface SealStatus {
    /** Whether the system is currently sealed. */
    readonly sealed: boolean;
    /** Whether the seal has been initialized (bootstrap completed). */
    readonly initialized: boolean;
    /**
     * Active sealing strategy: "none", "passphrase", or "shamir". Empty
     * string when the barrier has not yet been initialized. Consumers use
     * this to choose the correct unseal flow (passphrase entry vs Shamir
     * share accumulation).
     */
    readonly strategy: string;
    /** Number of Shamir shares needed to unseal. */
    readonly sharesRequired: number;
    /** Number of Shamir shares provided so far. */
    readonly sharesProvided: number;
    /** Total number of Shamir shares generated. */
    readonly sharesTotal: number;
    /**
     * The node barrier's cluster seal state (docs/barrier-seal): whether
     * cluster sealing is active, the replicated record, and this node's own
     * state. Absent from a server that does not report it.
     */
    readonly clusterSeal?: ClusterSeal;
}

/**
 * A barrier's cluster seal state. Before the cluster activates cluster
 * sealing, `active` is false and every seal is node-local.
 */
export interface ClusterSeal {
    /** Whether the cluster has activated cluster sealing. */
    readonly active: boolean;
    /** Whether the barrier's replicated record says sealed. */
    readonly sealed: boolean;
    /** The record's epoch; 0 when no record exists. */
    readonly epoch: number;
    /** Who wrote the record. */
    readonly principal?: string;
    /** The tenant of whoever wrote the record. */
    readonly principalTenant?: string;
    /** The node, by Raft address, the record was written through. */
    readonly node?: string;
    /** When the record was written (RFC 3339, UTC). */
    readonly at?: string;
    /**
     * This node's own state: "serving", "sealed", "held" (unsealed here but
     * the record is sealed), "cut_off" (cannot read the records) or
     * "unconfirmed" (has not read them since it started).
     */
    readonly local?: string;
}

/**
 * Result of a barrier initialization. Shares are never included: a Shamir
 * initialization seals each share to its enrolled holder, who claims it from
 * the CLI or the desktop app. The root token is empty for Shamir, where a
 * root token needs a holder quorum.
 */
export interface BootstrapResult {
    /** Administrative root token. */
    readonly rootToken: string;
    /** Minimum number of shares required to unseal (M in M-of-N). */
    readonly threshold: number;
    /** Total number of shares distributed to enrolled officers. */
    readonly shareCount: number;
}

/**
 * Parameters for enabling at-rest encryption on a previously-unencrypted
 * ("none") barrier. The migration re-encrypts all at-rest data under a newly
 * minted keyed strategy.
 *
 * Scope is selected by `tenantId`: omit it (or pass an empty string) to migrate
 * the SYSTEM barrier (admin scope); set it to migrate that tenant's OWN
 * dedicated barrier. A tenant whose data lives on the shared system barrier
 * cannot change its encryption and the server rejects the request.
 */
export interface EnableEncryptionRequest {
    /**
     * Target tenant's dedicated barrier. Omit or leave empty for the system
     * barrier (admin scope).
     */
    readonly tenantId?: string;
    /** Target keyed strategy to migrate to. */
    readonly strategy: 'passphrase' | 'shamir';
    /**
     * Re-encryption mechanism: "swap" (default) mints a keyed sibling and
     * atomically swaps it in; "in-place" rewrites records under the new key.
     */
    readonly mechanism?: 'swap' | 'in-place';
    /**
     * Availability mode: "quiesced" (default) briefly pauses the data plane;
     * "online" migrates while serving traffic.
     */
    readonly availability?: 'quiesced' | 'online';
    /** Unseal passphrase for the keyed barrier (passphrase strategy). */
    readonly passphrase?: string;
    /** Minimum number of shares required to unseal (M in M-of-N, shamir strategy). */
    readonly shamirThreshold?: number;
    /** Total number of shares to generate (N in M-of-N, shamir strategy). */
    readonly shamirTotal?: number;
}

/**
 * Acknowledgement that an enable-encryption migration has started. The work
 * runs asynchronously; callers observe completion by polling the barrier status
 * until the reported strategy changes from "none" to the keyed strategy.
 */
export interface EnableEncryptionResponse {
    /** Migration lifecycle status (e.g. "migration_started"). */
    readonly status: string;
    /** Scope the migration targets: "system" or "tenant". */
    readonly scope: string;
    /** Human-readable note describing how to observe completion. */
    readonly note?: string;
}

// ---------------------------------------------------------------------------
// Share-holder administration types
// ---------------------------------------------------------------------------

/**
 * A share holder's role. Security Officers and key custodians are the same
 * kind of holder — enrolled the same way, submitting to the same quorum — and
 * differ only in governance rights.
 */
export type HolderRole = 'so' | 'custodian';

/**
 * A barrier key domain. The system barrier has an unseal and a recovery
 * domain, each with its own holders; a tenant barrier has the unseal domain
 * only.
 */
export type QuorumDomain = 'unseal' | 'recovery';

/**
 * Which enrolled factor a holder may answer a quorum challenge with: only its
 * designated factor, or any factor it enrolled.
 */
export type FactorPolicy = 'designated' | 'any-enrolled';

/**
 * A factor to enrol for a holder beyond the built-in `mtls` factor. `params`
 * belong to the factor and are sent verbatim — their keys are never re-cased.
 */
export interface HolderFactorEnrolment {
    /** Registered factor name (for example "fido2"). */
    readonly name: string;
    /** Factor-specific enrolment parameters, sent as given. */
    readonly params?: Readonly<Record<string, unknown>>;
}

/**
 * Request to enrol a share holder. An Admin or a Security Officer enrols a
 * holder from the holder's CSR; the ceremony CA takes only the CSR's public
 * key and assigns the certificate's identity. No share exists at enrolment.
 */
export interface OfficerRegisterRequest {
    /** Unique holder username. */
    readonly username: string;
    /** Holder role; the server defaults it to "so". */
    readonly role?: HolderRole;
    /** PEM-encoded certificate signing request from the holder. */
    readonly csrPem: string;
    /**
     * HPKE suite of a separate envelope key (for example "dhkem-x25519" or
     * "mlkem768-x25519"). Without it the envelope is sealed to the
     * certificate key (P-256 or P-384).
     */
    readonly encryptionKem?: string;
    /** Base64 envelope public key for `encryptionKem`. */
    readonly encryptionKey?: string;
    /** Factors to enrol beyond the built-in `mtls`. */
    readonly factors?: readonly HolderFactorEnrolment[];
    /** The designated factor; the server defaults it to "mtls". */
    readonly designated?: string;
}

/**
 * Result of enrolling a share holder: the CA-issued certificate (handed to
 * the holder out of band) and what was enrolled. Never a share.
 */
export interface OfficerRegisterResponse {
    /** PEM-encoded holder certificate issued by the ceremony CA. */
    readonly certPem: string;
    /** The holder's role. */
    readonly role: string;
    /** The designated factor. */
    readonly designated?: string;
    /** The envelope key's HPKE suite. */
    readonly encryptionKem?: string;
    /** Every factor enrolled for the holder, `mtls` included. */
    readonly factors?: readonly string[];
}

/** Non-secret view of an enrolled share holder. Never carries a share. */
export interface OfficerStatus {
    /** Holder username. */
    readonly username: string;
    /** Holder role ("so" or "custodian"). */
    readonly role: string;
    /** SHA-256 thumbprint of the holder's certificate. */
    readonly certThumbprint?: string;
    /** The holder's share index in the current share set (0 before one). */
    readonly shareIndex: number;
    /** The designated factor. */
    readonly designated?: string;
    /** Every enrolled factor. */
    readonly factors?: readonly string[];
    /** The envelope key's HPKE suite. */
    readonly encryptionKem?: string;
    /** RFC 3339 enrolment time. */
    readonly enrolledAt: string;
    /** RFC 3339 time the current envelope was claimed, if it was. */
    readonly claimedAt?: string;
    /** True while an envelope is held for the holder, unclaimed. */
    readonly sharePending: boolean;
}

/**
 * Request to open a rekey round. Opening needs RBAC and the SO PIN;
 * completing the rekey needs a fresh quorum of current holders, who submit
 * from the CLI or the desktop app.
 */
export interface RekeyStartRequest {
    /** Key domain; defaults to "unseal". */
    readonly domain?: QuorumDomain;
    /** Threshold of the new share set. */
    readonly threshold: number;
    /** Factor policy of the new share set. */
    readonly policy?: FactorPolicy;
    /** Security Officer PIN. */
    readonly soPin?: string;
}

/**
 * Request to revoke a holder: a rekey round that leaves the holder out of the
 * new share set.
 */
export interface OfficerRevokeRequest {
    /** Key domain; defaults to "unseal". */
    readonly domain?: QuorumDomain;
    /** The holder to remove. */
    readonly username: string;
    /** Threshold of the new share set; omitted or 0 keeps the current one. */
    readonly threshold?: number;
    /** Security Officer PIN. */
    readonly soPin?: string;
}

/** An open quorum round, as a rekey or a revocation answers it. */
export interface QuorumRound {
    /** Key domain of the round. */
    readonly domain: string;
    /** Operation the round completes ("rekey" for rekey and revocation). */
    readonly op: string;
    /** Submissions required to complete the round. */
    readonly required: number;
    /** Submissions counted so far. */
    readonly submitted: number;
    /** RFC 3339 time the round expires. */
    readonly expiresAt: string;
    /** Threshold of the new share set, for a rekey. */
    readonly newThreshold?: number;
    /** Factor policy of the new share set, for a rekey. */
    readonly policy?: string;
    /** Holders the rekey removes. */
    readonly remove?: readonly string[];
}

/** Request to enrol a share holder for a tenant's barrier. */
export interface TenantOfficerRegisterRequest extends OfficerRegisterRequest {
    /** Target tenant identifier. */
    readonly tenantId: string;
}

/** Request to open a rekey round on a tenant's barrier. */
export interface TenantRekeyStartRequest {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** Threshold of the new share set. */
    readonly threshold: number;
    /** Factor policy of the new share set. */
    readonly policy?: FactorPolicy;
}

/** Request to revoke a holder of a tenant's barrier. */
export interface TenantOfficerRevokeRequest {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** The holder to remove. */
    readonly username: string;
    /** Threshold of the new share set; omitted or 0 keeps the current one. */
    readonly threshold?: number;
}

/** Request to enrol a holder of a database's barrier. */
export interface DatabaseOfficerRegisterRequest extends OfficerRegisterRequest {
    /** The database, resolved in the caller's tenant. */
    readonly database: string;
}

/**
 * Request to initialize a Shamir database barrier's custody. The M-of-N is the
 * one the database was created with (`sharesRequired`/`sharesTotal`); exactly
 * `sharesTotal` holders must be enrolled.
 */
export interface DatabaseShamirInitializeRequest {
    /** The database, resolved in the caller's tenant. */
    readonly database: string;
    /** Factor policy of the share set; the server defaults it to "designated". */
    readonly policy?: FactorPolicy;
}

/** The share-free result of a Shamir database barrier's initialization. */
export interface DatabaseShamirInitializeResponse {
    /** Shares required to unseal (M). */
    readonly threshold: number;
    /** Share count (N). */
    readonly totalShares: number;
    /** Envelopes the holders now claim. */
    readonly shareCount: number;
}

/** Request to open a rekey round on a database's barrier. */
export interface DatabaseRekeyStartRequest {
    /** The database, resolved in the caller's tenant. */
    readonly database: string;
    /** Threshold of the new share set. */
    readonly threshold: number;
    /** Factor policy of the new share set. */
    readonly policy?: FactorPolicy;
}

/** Request to remove a holder of a database's barrier before its custody is
 * initialized, when it holds no share. */
export interface DatabaseOfficerUnenrolRequest {
    /** The database, resolved in the caller's tenant. */
    readonly database: string;
    /** The holder to remove. */
    readonly username: string;
}

/** Request to revoke a holder of a database's barrier. */
export interface DatabaseOfficerRevokeRequest {
    /** The database, resolved in the caller's tenant. */
    readonly database: string;
    /** The holder to remove. */
    readonly username: string;
    /** Threshold of the new share set; omitted or 0 keeps the current one. */
    readonly threshold?: number;
}

/**
 * Request to initialize the system barrier under Shamir custody. The unseal
 * key is split to the enrolled unseal-domain holders; there is no secret.
 */
export interface ShamirInitializeRequest {
    /** Shares required to unseal (M). */
    readonly threshold: number;
    /** Share count (N); must equal the number of enrolled unseal holders. */
    readonly totalShares: number;
    /** Factor policy of the share set; the server defaults it to "designated". */
    readonly policy?: FactorPolicy;
    /** Provisions the SO PIN first, when a PIN backend is configured. */
    readonly soPin?: string;
    /** Provisions the User PIN first, when a PIN backend is configured. */
    readonly userPin?: string;
}

/** Progress of an unseal operation after providing a share. */
export interface UnsealProgress {
    /** Whether the system is still sealed after this share. */
    readonly sealed: boolean;
    /** Number of Shamir shares provided so far. */
    readonly sharesProvided: number;
    /** Total number of Shamir shares needed to unseal. */
    readonly sharesRequired: number;
}

// ---------------------------------------------------------------------------
// Embedded server types
// ---------------------------------------------------------------------------

/** Configuration for the embedded server. */
export interface EmbeddedConfig {
    /** Storage backend identifier (e.g. "pebble"). */
    readonly storageBackend: string;
    /** Directory for data storage. */
    readonly dataDir: string;
    /** Whether Raft consensus is enabled. */
    readonly raftEnabled: boolean;
    /** Raft cluster configuration, when Raft is enabled. */
    readonly raftConfig?: EmbeddedRaftConfig;
    /** Seal configuration. */
    readonly sealConfig?: EmbeddedSealConfig;
    /** Compliance audit trail. Off unless enabled. */
    readonly audit?: EmbeddedAuditConfig;
}

/**
 * The embedded server's compliance audit trail: the same hash-chained file,
 * ring and operational log qrdbd keeps. Off by default. When enabled it is
 * opened before the seal barrier, and a trail that cannot be opened stops the
 * server from starting.
 */
export interface EmbeddedAuditConfig {
    /** Turns the trail on. */
    readonly enabled: boolean;
    /**
     * The trail file, relative to the data directory; omitted is qrdbd's
     * default, "audit-events.ndjson". It may not leave the data directory.
     */
    readonly path?: string;
}

/** Raft cluster configuration for the embedded server. */
export interface EmbeddedRaftConfig {
    /** Whether this is a single-node cluster. */
    readonly singleNode: boolean;
    /** Total number of nodes in the cluster. */
    readonly nodeCount: number;
    /** Shard identifier for this node. */
    readonly shardId: number;
    /** Replica identifier for this node. */
    readonly replicaId: number;
    /** Raft transport address for this node. */
    readonly raftAddress: string;
    /** Addresses of peer nodes. */
    readonly peers: readonly string[];
    /** Discovery mechanism (e.g. "static", "multicast", "dns"). */
    readonly discovery: string;
    /** Shared secret for multicast discovery. */
    readonly multicastSecret?: string;
    /** Domain name for DNS-based discovery. */
    readonly dnsDomain?: string;
}

/** Seal mode for the encryption barrier. */
export type SealMode = 'none' | 'passphrase' | 'shamir';

/** Seal configuration for the embedded server. */
export interface EmbeddedSealConfig {
    /** Seal mode. */
    readonly mode: SealMode;
    /** Minimum shares needed to unseal (M in M-of-N). */
    readonly sharesRequired?: number;
    /** Total shares to generate (N in M-of-N). */
    readonly sharesTotal?: number;
}

// ---------------------------------------------------------------------------
// Database management types
// ---------------------------------------------------------------------------

/** SQL wire protocol configuration for a database: which wire protocols
 * serve it. The Go SDK's `ProtocolConfig`. */
export interface DatabaseProtocolConfig {
    /** Whether MySQL wire protocol is enabled. */
    readonly mysql: boolean;
    /** Whether PostgreSQL wire protocol is enabled. */
    readonly postgresql: boolean;
}

/** The etcd wire protocol as a database reports it. The Go SDK's
 * `EtcdProtocolConfig`. */
export interface DatabaseEtcdProtocolInfo {
    /** Whether the etcd wire protocol serves this database. */
    readonly enabled: boolean;
    /** Address the etcd listener binds. */
    readonly listenAddr: string;
    /** Client URLs the listener advertises. */
    readonly advertiseClientUrls?: readonly string[];
}

/** Wire protocols as a database reports them. */
export interface DatabaseProtocolInfo extends DatabaseProtocolConfig {
    /** etcd wire protocol settings, when configured. */
    readonly etcd?: DatabaseEtcdProtocolInfo;
}

/** A database's SQL concurrency mode: whether its SQL statements serialise
 * on row locks (`pessimistic`) or settle conflicts at commit (`optimistic`,
 * the default). */
export type SQLConcurrency = 'optimistic' | 'pessimistic';

/** SQL settings for a database. The Go SDK's `DatabaseSQLConfig`. */
export interface DatabaseSQLConfig {
    /** The database's default SQL concurrency mode. Absent means
     * `optimistic`. */
    readonly concurrency?: SQLConcurrency;
}

/** How a database's scheduled compaction picks its revision: never
 * (`off`, the default), keeping the store's last N revisions (`revision`),
 * or keeping the history newer than a duration (`period`). */
export type CompactionMode = 'off' | 'revision' | 'period';

/** A database's scheduled-compaction policy. The Go SDK's
 * `DatabaseCompactionConfig`. A database without one is never compacted
 * implicitly. */
export interface DatabaseCompactionConfig {
    /** The policy's mode. Absent means `off`. */
    readonly mode?: CompactionMode;
    /** How many of the store's most recent revisions a `revision` policy
     * keeps (at least 1). Revision mode only. */
    readonly retainRevisions?: number;
    /** How much history, by age, a `period` policy keeps: a Go duration
     * string of at least `1s`, e.g. `"24h"`. Period mode only. */
    readonly retainPeriod?: string;
}

/** Request to set a database's scheduled-compaction policy. An absent
 * `compaction` turns it off. */
export interface UpdateDatabaseCompactionRequest {
    /** The policy to apply. */
    readonly compaction?: DatabaseCompactionConfig;
}

/** The platform's node-keyspace compaction policy, as `GET` and `PUT
 * /admin/node-compaction` carry it: the scheduled compaction of the keys
 * outside every database namespace. An absent `compaction` is no policy --
 * in a request, it turns the policy off. */
export interface NodeCompactionBody {
    /** The policy. */
    readonly compaction?: DatabaseCompactionConfig;
}

/**
 * Raft consensus configuration as a request sets it on a database. There is no
 * shard ID: the cluster issues it when the database is created, it never
 * changes, and it is reported as {@link DatabaseInfo.shardId}. A response
 * reports the configuration as a {@link DatabaseRaftInfo}.
 *
 * A Raft update replaces the whole configuration: a discovery block left out
 * is removed, secret and all. A block sent without its secret keeps the stored
 * secret while the block's destination is unchanged, so settings read back --
 * responses never carry secrets -- can be written back without them. The Go
 * SDK's `transport.DatabaseRaftConfig`.
 */
export interface DatabaseRaftConfig {
    /** Cluster mode (e.g. "single", "multi"). */
    readonly clusterMode: string;
    /** Replica identifier for this database. */
    readonly replicaId?: number;
    /** Raft transport address. */
    readonly raftAddress?: string;
    /** Addresses of peer nodes. */
    readonly peers?: readonly string[];
    /** Number of local nodes in the cluster. */
    readonly localNodeCount?: number;
    /** Peer discovery method (static, multicast, dns, token). */
    readonly discovery?: string;
    /** Static discovery settings. */
    readonly static?: StaticDiscoveryConfig;
    /** Multicast discovery settings. */
    readonly multicast?: MulticastDiscoveryConfig;
    /** DNS discovery settings. */
    readonly dns?: DNSDiscoveryConfig;
    /** Token discovery settings. */
    readonly token?: TokenDiscoveryConfig;
}

/** Static peer discovery as a request sets it. */
export interface StaticDiscoveryConfig {
    /** Peer addresses. */
    readonly peers?: readonly string[];
    /** HMAC shared secret. Write-only: a response reports only that it is set. */
    readonly sharedSecret?: string;
}

/** UDP multicast peer discovery as a request sets it. */
export interface MulticastDiscoveryConfig {
    /** HMAC shared secret. Write-only: a response reports only that it is set. */
    readonly sharedSecret?: string;
    /** Multicast group address. */
    readonly multicastGroup?: string;
    /** Network interface the announcements use. */
    readonly interface?: string;
    /** Peers to find before forming the cluster. */
    readonly minPeers?: number;
    /** Interval between announcements, as a Go duration. */
    readonly announceInterval?: string;
    /** How long discovery waits for peers, as a Go duration. */
    readonly discoverTimeout?: string;
}

/** DNS SRV peer discovery as a request sets it. It holds no secret. */
export interface DNSDiscoveryConfig {
    /** SRV service name. */
    readonly service?: string;
    /** SRV protocol. */
    readonly proto?: string;
    /** Domain the SRV records live under. */
    readonly domain: string;
    /** Lookup timeout, as a Go duration. */
    readonly lookupTimeout?: string;
}

/** Token-based peer discovery as a request sets it. */
export interface TokenDiscoveryConfig {
    /** Discovery service endpoint. */
    readonly endpoint: string;
    /** Bootstrap token. Write-only: a response reports only that it is set. */
    readonly token?: string;
    /** Expected cluster size. */
    readonly clusterSize: number;
    /** Discovery timeout, as a Go duration. */
    readonly timeout?: string;
}

/** Static peer discovery as a database reports it. */
export interface StaticDiscoveryInfo {
    /** Configured peer addresses. */
    readonly peers?: readonly string[];
    /** Whether an HMAC shared secret is configured. The secret itself is
     * write-only: no response carries it. */
    readonly sharedSecretSet?: boolean;
}

/** UDP multicast peer discovery as a database reports it. */
export interface MulticastDiscoveryInfo {
    /** Whether the HMAC shared secret is configured. The secret itself is
     * write-only: no response carries it. */
    readonly sharedSecretSet?: boolean;
    /** Multicast group address. */
    readonly multicastGroup?: string;
    /** Network interface the announcements use. */
    readonly interface?: string;
    /** Peers to find before forming the cluster. */
    readonly minPeers?: number;
    /** Interval between announcements. */
    readonly announceInterval?: string;
    /** How long discovery waits for peers. */
    readonly discoverTimeout?: string;
}

/** DNS SRV peer discovery as a database reports it. */
export interface DNSDiscoveryInfo {
    /** SRV service name. */
    readonly service?: string;
    /** SRV protocol. */
    readonly proto?: string;
    /** Domain the SRV records live under. */
    readonly domain: string;
    /** Lookup timeout. */
    readonly lookupTimeout?: string;
}

/** Token-based peer discovery as a database reports it. */
export interface TokenDiscoveryInfo {
    /** Discovery service endpoint. */
    readonly endpoint: string;
    /** Whether the bootstrap token is configured. The token itself is
     * write-only: no response carries it. */
    readonly tokenSet?: boolean;
    /** Expected cluster size. */
    readonly clusterSize: number;
    /** Discovery timeout. */
    readonly timeout?: string;
}

/** Raft configuration as a database reports it. Discovery secrets are
 * write-only: each is reported only as set. The Go SDK's `DatabaseRaftInfo`. */
export interface DatabaseRaftInfo {
    /** Cluster mode (e.g. "single", "multi"). */
    readonly clusterMode: string;
    /** Replica identifier for this database. */
    readonly replicaId?: number;
    /** Raft transport address. */
    readonly raftAddress?: string;
    /** Addresses of peer nodes. */
    readonly peers?: readonly string[];
    /** Number of local nodes in the cluster. */
    readonly localNodeCount?: number;
    /** Peer discovery method (static, multicast, dns, token). */
    readonly discovery?: string;
    /** Static discovery settings. */
    readonly static?: StaticDiscoveryInfo;
    /** Multicast discovery settings. */
    readonly multicast?: MulticastDiscoveryInfo;
    /** DNS discovery settings. */
    readonly dns?: DNSDiscoveryInfo;
    /** Token discovery settings. */
    readonly token?: TokenDiscoveryInfo;
}

/** Encryption barrier configuration for a database. */
export interface DatabaseBarrierConfig {
    /** Whether the encryption barrier is enabled. */
    readonly enabled: boolean;
    /** Barrier mode (e.g. "none", "passphrase", "shamir"). */
    readonly mode?: string;
    /** Minimum number of shares needed to unseal (M in M-of-N). */
    readonly sharesRequired?: number;
    /** Total number of shares to generate (N in M-of-N). */
    readonly sharesTotal?: number;
}

/** A database barrier's state: whether it is sealed, the strategy protecting
 * its root key, and when it was first initialized. A Shamir database's custody
 * is initialized exactly when `initializedAt` is present. The Go SDK's
 * `DatabaseBarrierStatus`. */
export interface DatabaseBarrierStatus {
    /** Whether the barrier is sealed. */
    readonly sealed: boolean;
    /** The strategy protecting the root key ("passphrase", "shamir", ...). */
    readonly strategy: string;
    /** Whether the strategy uses dedicated hardware. */
    readonly hardwareBacked: boolean;
    /** RFC 3339 time the barrier was first initialized; absent until then. */
    readonly initializedAt?: string;
}

/** Encryption barrier settings a create request sets. The secret
 * initialises the barrier; it is write-only and no response returns it. The
 * Go SDK's `DatabaseBarrierConfig`. */
export interface DatabaseBarrierRequest extends DatabaseBarrierConfig {
    /** The barrier passphrase. Sent once, at create; never stored by the
     * server and never returned. */
    readonly secret?: string;
}

/** Remote connection configuration for a database. */
export interface DatabaseConnectionConfig {
    /** Transport protocol (e.g. "rest", "grpc", "unix"). */
    readonly transport: string;
    /** Server address. */
    readonly address: string;
    /** Path to the CA certificate file. */
    readonly caFile?: string;
    /** Path to the client certificate file. */
    readonly certFile?: string;
    /** Path to the client private key file. */
    readonly keyFile?: string;
}

/** Request to update Raft configuration for a database. */
export interface UpdateDatabaseRaftRequest {
    /** Raft consensus configuration. */
    readonly raft: DatabaseRaftConfig;
}

/** Request to update barrier configuration for a database. */
export interface UpdateDatabaseBarrierRequest {
    /** Encryption barrier configuration. */
    readonly barrier: DatabaseBarrierConfig;
}

/** Request to update connection configuration for a database. */
export interface UpdateDatabaseConnectionRequest {
    /** Remote connection configuration. */
    readonly connection: DatabaseConnectionConfig;
}

/** Status of the qrdbd daemon process. */
export interface DaemonStatus {
    /** Whether the daemon is currently running. */
    readonly running: boolean;
    /** Process ID of the daemon, if running. */
    readonly pid?: number;
    /** Path to the daemon log file. */
    readonly logFile?: string;
}

/** Information about a database. */
export interface DatabaseInfo {
    /** Tenant that owns the database. */
    readonly tenantId?: string;
    /** Database name. */
    readonly name: string;
    /** Engine type (kv, timeseries, vector). */
    readonly engine: string;
    /** Storage backend (memory, file, pebble, raft). */
    readonly backend: string;
    /** Current status (open, closed, error). */
    readonly status: string;
    /** Wire protocol configuration (mysql/postgresql/etc). Wire field name
     * is `protocol` to match the Go transport type; this is the canonical
     * field. */
    readonly protocol?: DatabaseProtocolInfo;
    /** SQL settings (concurrency mode). Absent means the defaults. */
    readonly sql?: DatabaseSQLConfig;
    /** Scheduled-compaction policy. Absent means off: the database is never
     * compacted implicitly -- unless `compactionUnavailable` is set. */
    readonly compaction?: DatabaseCompactionConfig;
    /** True when the server could not read the database's policy from its
     * store: `compaction` is then unknown, not off. */
    readonly compactionUnavailable?: boolean;
    /** Data directory path, if applicable. */
    readonly dataDir?: string;
    /** Raft shard ID the cluster issued at create; absent for non-raft
     * databases. It never changes. */
    readonly shardId?: number;
    /** Database mode (e.g. "embedded", "remote"). */
    readonly mode?: string;
    /** The codec the database's values are stored in; absent means the
     * node's default. */
    readonly storageCodec?: string;
    /** Replication factor of a dedicated-raft database. Zero for non-raft. */
    readonly replicas?: number;
    /** Whether this dedicated-raft database's Raft log is encrypted under the
     * tenant's own key. Only meaningful for the raft backend. */
    readonly dedicatedWal?: boolean;
    /** Raft consensus configuration. */
    readonly raft?: DatabaseRaftInfo;
    /** Encryption barrier configuration. */
    readonly barrier?: DatabaseBarrierConfig;
    /** Remote connection configuration. */
    readonly connection?: DatabaseConnectionConfig;
}

/** Request to create a new database. */
export interface CreateDatabaseRequest {
    /** Database name. */
    readonly name: string;
    /** Engine type (kv, timeseries, vector). */
    readonly engine: string;
    /** Storage backend (memory, file, pebble, raft). */
    readonly backend: string;
    /** Wire protocol configuration (mysql/postgresql). The wire field name
     * is `protocol` to match the Go transport type. */
    readonly protocol?: DatabaseProtocolConfig;
    /** SQL settings (concurrency mode). Absent means the defaults. */
    readonly sql?: DatabaseSQLConfig;
    /** Custom data directory for file/pebble backends. */
    readonly dataDir?: string;
    /** Database mode (e.g. "embedded", "remote"). */
    readonly mode?: string;
    /** Replication factor for a dedicated-raft database. Meaningful only for the
     * raft backend; unset defaults to 1 replica. */
    readonly replicas?: number;
    /** Enables per-database dedicated WAL encryption under the tenant's own key.
     * Gated on the tenant's `allow_dedicated_wal` entitlement and the
     * `max_dedicated_wal_groups` quota. Only honoured for the raft backend;
     * a non-raft request silently clears the flag server-side. */
    readonly dedicatedWal?: boolean;
    /** Raft consensus configuration. */
    readonly raft?: DatabaseRaftConfig;
    /** Encryption barrier configuration, with the passphrase that
     * initialises it. */
    readonly barrier?: DatabaseBarrierRequest;
    /** Remote connection configuration. */
    readonly connection?: DatabaseConnectionConfig;
}

/** Request to update SQL wire protocol configuration for a database. */
export interface UpdateDatabaseProtocolRequest {
    /** Whether MySQL wire protocol is enabled. */
    readonly mysql: boolean;
    /** Whether PostgreSQL wire protocol is enabled. */
    readonly postgresql: boolean;
}

/** Request to update a database's SQL settings. An absent `sql` restores
 * the defaults. */
export interface UpdateDatabaseSQLRequest {
    /** The SQL settings to apply. */
    readonly sql?: DatabaseSQLConfig;
}

/** Response containing a list of databases. */
export interface ListDatabasesResponse {
    /** List of database information objects. */
    readonly databases: readonly DatabaseInfo[];
}

// ---------------------------------------------------------------------------
// SQL query types
// ---------------------------------------------------------------------------

/** Result of a SQL query execution. */
export interface SQLQueryResult {
    /** Column names from the result set. */
    readonly columns: readonly string[];
    /** Rows as key-value maps (column name to value). */
    readonly rows: readonly Record<string, unknown>[];
    /** Number of rows returned. */
    readonly rowCount: number;
    /** Human-readable execution duration. */
    readonly duration: string;
}

// ---------------------------------------------------------------------------
// Collection management types
// ---------------------------------------------------------------------------

/** Information about a collection within a database. */
export interface CollectionInfo {
    readonly name: string;
    readonly database: string;
    readonly id_strategy: string;
    readonly field_hash?: string;
    readonly schema?: Readonly<Record<string, string>>;
    readonly document_count: number;
    readonly storage_codec?: string;
    readonly created_at: string;
}

/** Request to create a new collection. */
export interface CreateCollectionRequest {
    readonly database: string;
    readonly name: string;
    readonly id_strategy?: string;
    readonly field_hash?: string;
    readonly storage_codec?: string;
}

/** Response containing a list of collections. */
export interface ListCollectionsResponse {
    readonly collections: readonly CollectionInfo[];
}

// ---------------------------------------------------------------------------
// Document types
// ---------------------------------------------------------------------------

/** A document returned from a collection. */
export interface DocumentResponse {
    readonly id: string;
    readonly document: Readonly<Record<string, unknown>>;
}

/** Request to insert a document into a collection. */
export interface InsertDocumentRequest {
    readonly database: string;
    readonly collection: string;
    readonly document: Record<string, unknown>;
}

/** Response from inserting a document. */
export interface InsertDocumentResponse {
    readonly id: string;
}

/** Request to update an existing document. */
export interface UpdateDocumentRequest {
    readonly database: string;
    readonly collection: string;
    readonly id: string;
    readonly document: Record<string, unknown>;
}

/** Request to list documents with pagination. */
export interface ListDocumentsRequest {
    readonly database: string;
    readonly collection: string;
    readonly page_size?: number;
    readonly page_token?: string;
}

/** Response containing a page of documents. */
export interface ListDocumentsResponse {
    readonly documents: readonly DocumentResponse[];
    readonly next_page_token?: string;
    readonly total_count: number;
}

// ---------------------------------------------------------------------------
// Tenant management types
// ---------------------------------------------------------------------------

/** Per-tenant resource quotas. -1 = unlimited, 0 = system default. */
export interface TenantQuotas {
    /** Maximum number of databases this tenant can create. */
    readonly maxDatabases: number;
    /** Maximum number of raft groups (shards) across all databases. */
    readonly maxRaftGroups: number;
    /**
     * Maximum replication factor (voting replicas) any single dedicated-raft
     * database may request at create time. Evaluated per group.
     */
    readonly maxReplicasPerGroup: number;
    /**
     * Maximum aggregate number of replicas the tenant may hold across all of
     * its dedicated-raft groups (the capacity/billing lever).
     */
    readonly maxTotalReplicas: number;
    /**
     * Maximum number of dedicated-raft groups for which this tenant may enable
     * the per-database WAL encryption feature. -1 = unlimited, 0 = system
     * default. Wire name: `max_dedicated_wal_groups`.
     */
    readonly maxDedicatedWalGroups: number;
    /** Maximum number of collections across all databases. */
    readonly maxCollections: number;
    /** Maximum total KV entries. */
    readonly maxKeys: number;
    /** Maximum total storage in bytes. */
    readonly maxStorageBytes: number;
    /** Maximum size in bytes of any single database. */
    readonly maxDatabaseSizeBytes: number;
    /** Maximum users in this tenant. */
    readonly maxUsers: number;
    /** Maximum roles in this tenant. */
    readonly maxRoles: number;
    /** Maximum issued certificates. */
    readonly maxCertificates: number;
    /** Maximum stored backups. */
    readonly maxBackups: number;
    /** Maximum vector dimensions. */
    readonly maxVectorDims: number;
    /** Maximum time-series metrics. */
    readonly maxTSMetrics: number;
    /**
     * Bytes an isolated tenant may consume by opting to use the shared system
     * barrier: 0 makes that option unavailable, -1 unlimited, >0 a cap accounted
     * against the system tenant. Wire name: `system_barrier_quota`.
     */
    readonly systemBarrierQuota?: number;
}

/**
 * Feature entitlements describe which isolation tiers and dedicated-storage
 * features a tenant's plan permits.
 */
export interface TenantEntitlements {
    /** Permitted isolation modes, e.g. "prefix", "barrier", "volume", "raft". */
    readonly allowedIsolationModes?: string[];
    /** Whether the tenant may provision dedicated Raft-backed databases. */
    readonly allowDedicatedRaft?: boolean;
    /** Whether the tenant may provision databases on a dedicated volume. */
    readonly allowVolumeStorage?: boolean;
    /**
     * Whether the tenant may opt into the system-managed auto-unseal
     * key-management mode for its dedicated barrier. When false (the secure
     * default) the tenant must self-manage its barrier keys.
     */
    readonly allowSystemManagedBarrier?: boolean;
    /**
     * Whether the tenant may opt a dedicated-raft database into per-database
     * WAL encryption (the `dedicatedWal` create-database flag). Requires the
     * `max_dedicated_wal_groups` quota. Wire name: `allow_dedicated_wal`.
     */
    readonly allowDedicatedWal?: boolean;
    /**
     * Dedicated-barrier sealing strategies the isolated tenant may select
     * (none/passphrase/shamir or an xkms hardware strategy id). Empty permits
     * the built-in software defaults and fail-closes hardware modes. Wire name:
     * `allowed_barrier_modes`.
     */
    readonly allowedBarrierModes?: string[];
}

/**
 * Physical isolation strategy for a tenant. Mirrors the Go
 * `tenant.IsolationType` constants.
 *
 * - `"prefix"`  — shared-store namespace key prefix only (no extra crypto layer).
 * - `"barrier"` — namespace key prefix plus a per-tenant encryption barrier.
 * - `"volume"`  — dedicated filesystem data root per tenant (plus a barrier),
 *   a dedicated-storage tier. Requires the daemon's volume pool.
 * - `"raft"`    — dedicated Raft group per database on a dedicated data root,
 *   a dedicated-storage tier. Multi-node replication of those groups is in
 *   development; databases run single-replica today.
 */
export type TenantIsolationType = 'prefix' | 'barrier' | 'volume' | 'raft';

/** Information about a tenant. */
export interface TenantInfo {
    /** Unique tenant identifier. */
    readonly id: string;
    /** Human-readable tenant name. */
    readonly name: string;
    /**
     * Physical isolation strategy. Pre-isolation-type records report
     * `"prefix"` (barrier isolation is strictly opt-in).
     */
    readonly isolationType?: TenantIsolationType;
    /** Resource quotas for this tenant, if configured. */
    readonly quotas?: TenantQuotas;
    /** Feature entitlements for this tenant, if configured. */
    readonly entitlements?: TenantEntitlements;
    /**
     * Dedicated filesystem root for an IsolationVolume/IsolationRaft tenant.
     * Empty for prefix/barrier tenants.
     */
    readonly dataRoot?: string;
    /** ISO 8601 creation timestamp. */
    readonly createdAt: string;
    /** ISO 8601 last-updated timestamp. */
    readonly updatedAt: string;
}

/** Request to create a new tenant. */
export interface CreateTenantRequest {
    /** Unique identifier for the new tenant. */
    readonly id: string;
    /** Human-readable name for the new tenant. */
    readonly name: string;
    /**
     * Physical isolation strategy. Defaults to `"prefix"` when omitted
     * (barrier isolation is strictly opt-in and requires the seal
     * subsystem to be enabled on the server).
     */
    readonly isolationType?: TenantIsolationType;
    /**
     * Opt a dedicated-isolation tenant (barrier/volume) into the system-managed
     * auto-unseal key-management mode (entitlement-gated). Defaults to `false`
     * (the secure self-managed mode where the tenant admin holds the key).
     */
    readonly barrierAutoUnseal?: boolean;
    /**
     * Pins an IsolationVolume/IsolationRaft tenant's dedicated filesystem data
     * root to this operator-supplied absolute path instead of pool
     * allocation. Empty (the default) allocates from the configured volume
     * pool. Rejected by the server for prefix/barrier isolation.
     */
    readonly dataRoot?: string;
}

/** Request to apply a partial update to an existing tenant. */
export interface UpdateTenantRequest {
    /** New human-readable name (leave undefined to keep current value). */
    readonly name?: string;
    /**
     * New isolation strategy. Returns 409 Conflict if the tenant already has
     * data stored under the current strategy.
     */
    readonly isolationType?: TenantIsolationType;
}

/** Response from updating a tenant. */
export interface UpdateTenantResponse {
    /** The updated tenant. */
    readonly tenant: TenantInfo;
}

/** Response from creating a tenant. */
export interface CreateTenantResponse {
    /** The created tenant. */
    readonly tenant: TenantInfo;
}

/** Response from fetching a single tenant. */
export interface GetTenantResponse {
    /** The requested tenant. */
    readonly tenant: TenantInfo;
}

/** Response containing all tenants. */
export interface ListTenantsResponse {
    /** All registered tenants. */
    readonly tenants: TenantInfo[];
}

/** Request to initialize the encryption barrier for a tenant. */
export interface TenantBarrierInitRequest {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** Optional passphrase used to derive the barrier root key. */
    readonly passphrase?: string;
    /** Minimum number of shares required to unseal. */
    readonly threshold: number;
    /** Total number of key shares to generate. */
    readonly shares: number;
    /** Optional seal strategy identifier (e.g. "passphrase", "shamir", or a hardware id). */
    readonly strategy?: string;
}

/**
 * Request to unseal a tenant barrier held by a credential strategy (the
 * passphrase). A Shamir tenant barrier is unsealed by a quorum of its holders
 * from the CLI or the desktop app, not by this request.
 */
export interface TenantBarrierUnsealRequest {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** The unseal passphrase; base64 on the wire. */
    readonly key: string;
}

/**
 * Response from initializing a tenant's encryption barrier. For all
 * strategies, no share bytes are returned here — shares are distributed to
 * Security Officers through the officer claim flow. For "none" and
 * "passphrase" strategies the server returns 204 No Content and the SDK
 * synthesizes a response containing only the tenant id and strategy.
 */
export interface TenantBarrierInitResponse {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** Active sealing strategy: "none", "passphrase", or "shamir". */
    readonly strategy: string;
    /** Number of shares required to unseal (Shamir only). */
    readonly threshold?: number;
    /** Total number of shares distributed to enrolled officers (Shamir only). */
    readonly totalShares?: number;
    /**
     * Number of share enclaves sealed and ready for officer claim (Shamir only).
     * Always equals the number of enrolled officers at init time.
     */
    readonly shareCount?: number;
}

/** Current seal state of a tenant's encryption barrier. */
export interface TenantBarrierStatusResponse {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** Whether the tenant barrier is currently sealed. */
    readonly sealed: boolean;
    /** Whether the tenant barrier has been initialized. */
    readonly initialized: boolean;
    /**
     * Active sealing strategy: "none", "passphrase", or "shamir". Empty string
     * when the barrier has not yet been initialized.
     */
    readonly strategy: string;
    /** Whether the active seal strategy is backed by a hardware device. */
    readonly hardwareBacked: boolean;
    /** Shamir shares required to unseal. Omitted or 0 when not Shamir. */
    readonly shamirThreshold?: number;
    /** Total Shamir shares generated. Omitted or 0 when not Shamir. */
    readonly shamirTotal?: number;
}

/** Request to update tenant quotas. */
export interface UpdateTenantQuotasRequest {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** The new quota configuration. */
    readonly quotas: TenantQuotas;
}

/** Response from updating tenant quotas. */
export interface UpdateTenantQuotasResponse {
    /** The updated tenant. */
    readonly tenant: TenantInfo;
}

/** Request to update a tenant's feature entitlements. */
export interface UpdateTenantEntitlementsRequest {
    /** Target tenant identifier. */
    readonly tenantId: string;
    /** The new entitlement configuration. */
    readonly entitlements: TenantEntitlements;
}

/** Response from updating tenant entitlements. */
export interface UpdateTenantEntitlementsResponse {
    /** The updated tenant. */
    readonly tenant: TenantInfo;
}

// ---------------------------------------------------------------------------
// Server config types
// ---------------------------------------------------------------------------

/** Response from checking server initialization status. */
export interface ConfigInitializedResponse {
    /** Whether the server has completed first-run setup. */
    readonly initialized: boolean;
}

// ---------------------------------------------------------------------------
// Backup and restore types
// ---------------------------------------------------------------------------

/** Parameters for creating a backup. */
export interface BackupRequest {
    /** Destination path or URI for the backup archive. */
    readonly destination: string;
    /** Compression algorithm to apply (e.g. "gzip", "zstd", "none"). */
    readonly compression?: string;
    /** Encryption algorithm to apply (e.g. "aes256gcm", "none"). */
    readonly encryption?: string;
    /** Encryption key material (base64-encoded). */
    readonly encryptionKey?: string;
    /** Arbitrary metadata to embed in the backup manifest. */
    readonly metadata?: Record<string, string>;
    /** Specific shard IDs to include; omit to back up all shards. */
    readonly shardIds?: number[];
}

/** Metadata about a completed or in-progress backup. */
export interface BackupInfo {
    /** Unique backup identifier. */
    readonly id: string;
    /** ISO 8601 backup creation timestamp. */
    readonly timestamp: string;
    /** Total compressed size in bytes. */
    readonly size: number;
    /** Number of shards included in the backup. */
    readonly shardCount: number;
    /** Backup status (e.g. "completed", "failed", "in_progress"). */
    readonly status: string;
    /** Hex-encoded checksum of the backup archive. */
    readonly checksum: string;
    /** Destination where the backup was written. */
    readonly destination: string;
    /** Compression algorithm used. */
    readonly compression: string;
    /** Encryption algorithm used. */
    readonly encryption: string;
    /** Metadata embedded in the backup manifest. */
    readonly metadata: Record<string, string>;
    /** Human-readable backup duration. */
    readonly duration: string;
    /** Non-empty when the backup failed. */
    readonly errorMessage: string;
}

/** Request to restore from a backup. */
export interface RestoreRequest {
    /** Identifier of the backup to restore. */
    readonly backupId: string;
    /** Specific shard IDs to restore; omit to restore all shards. */
    readonly targetShards?: number[];
    /** When true, verify the backup integrity before restoring. */
    readonly validateFirst?: boolean;
    /** Decryption key material (base64-encoded) when the backup is encrypted. */
    readonly decryptionKey?: string;
}

/** Result of a restore operation. */
export interface RestoreResult {
    /** Identifier of the backup that was restored. */
    readonly backupId: string;
    /** Number of shards successfully restored. */
    readonly shardsRestored: number;
    /** Human-readable restore duration. */
    readonly duration: string;
    /** Per-shard errors, if any. Omitted on the wire when there are none. */
    readonly errors?: ShardError[];
}

/** A per-shard error within a restore result. */
export interface ShardError {
    /** Shard that encountered the error. */
    readonly shardId: number;
    /** Error message. */
    readonly message: string;
}

/** Result of a backup verification. */
export interface VerifyResult {
    /** Whether the backup passed all verification checks. */
    readonly valid: boolean;
    /** Whether the stored and computed checksums match. */
    readonly checksumMatch: boolean;
    /** Expected (stored) checksum. */
    readonly expectedChecksum: string;
    /** Actual (computed) checksum. */
    readonly actualChecksum: string;
    /** Verification error messages, if any. Omitted on the wire when there are none. */
    readonly errors?: string[];
}

/** Response containing all available backups. */
export interface ListBackupsResponse {
    /** All backup metadata records. */
    readonly backups: BackupInfo[];
}

// ---------------------------------------------------------------------------
// Rebalance types
// ---------------------------------------------------------------------------

/** Load statistics for a single shard. */
export interface ShardStatsResponse {
    /** Shard identifier. */
    readonly shardId: number;
    /** Number of keys stored in this shard. */
    readonly keyCount: number;
    /** Total bytes stored in this shard. */
    readonly byteCount: number;
    /** Replica ID of the current shard leader. */
    readonly leaderId: number;
    /** Number of replicas in this shard. */
    readonly replicaCount: number;
}

/** Parameters for generating a rebalance plan. */
export interface RebalancePlanRequest {
    /** Maximum number of shard moves to include. */
    readonly maxMoves?: number;
    /** When true, plan is computed but not executed. */
    readonly dryRun?: boolean;
    /** Target imbalance score threshold (0.0–1.0). */
    readonly targetBalance?: number;
}

/** A single shard move within a rebalance plan. */
export interface RebalanceMove {
    /** Shard to move. */
    readonly shardId: number;
    /** Source node address. */
    readonly fromNode: string;
    /** Destination node address. */
    readonly toNode: string;
    /** Estimated data transfer size in bytes. */
    readonly estimatedBytes: number;
}

/** A generated rebalance execution plan. */
export interface RebalancePlan {
    /** Ordered list of shard moves to execute. */
    readonly moves: RebalanceMove[];
    /** Human-readable estimated execution duration. */
    readonly estimatedDuration: string;
    /** Current imbalance score before executing this plan (0.0–1.0). */
    readonly currentImbalance: number;
}

/** Result of executing a rebalance plan. */
export interface RebalanceResult {
    /** Number of moves that completed successfully. */
    readonly movesCompleted: number;
    /** Number of moves that failed. */
    readonly movesFailed: number;
    /** Human-readable execution duration. */
    readonly duration: string;
    /** Imbalance score after execution (0.0–1.0). */
    readonly finalImbalance: number;
}

/** Load metrics for a single cluster node. */
export interface NodeLoad {
    /** Unique node identifier. */
    readonly nodeId: string;
    /** Raft transport address of the node. */
    readonly address: string;
    /** Number of shards assigned to this node. */
    readonly shardCount: number;
    /** Total key count across all shards on this node. */
    readonly totalKeys: number;
    /** Total byte count across all shards on this node. */
    readonly totalBytes: number;
    /** Number of shards for which this node is the leader. */
    readonly leaderCount: number;
}

/** Cluster-wide load imbalance report. */
export interface ImbalanceReport {
    /** Per-node load metrics. */
    readonly nodes: NodeLoad[];
    /** Aggregate imbalance score (0.0 = perfectly balanced, 1.0 = maximally skewed). */
    readonly imbalanceScore: number;
    /** Node addresses identified as load hotspots. */
    readonly hotspots: string[];
}

// ---------------------------------------------------------------------------
// Credential types
// ---------------------------------------------------------------------------

/**
 * Version metadata entry for a named credential. Value bytes are
 * intentionally absent — use CredentialClient.retrieveVersion to fetch the
 * value for a specific version.
 */
export interface CredentialVersionInfo {
    /** Version number (monotonically increasing). */
    readonly version: number;
    /** Whether this is the current (active) version. */
    readonly isCurrent: boolean;
    /** Unix timestamp (seconds) this version was created. */
    readonly createdAtUnix?: number;
    /** Unix timestamp (seconds) after which this version is no longer retrievable. */
    readonly graceUntilUnix?: number;
}

/** Result of a credential rotation. */
export interface CredentialRotateResult {
    /** Credential name that was rotated. */
    readonly name: string;
    /** Version number prior to rotation. */
    readonly previousVersion: number;
    /** Version number after rotation. */
    readonly newVersion: number;
    /** Unix timestamp (seconds) after which the previous version expires. */
    readonly graceUntilUnix?: number;
}

// ---------------------------------------------------------------------------
// Connection manager types
// ---------------------------------------------------------------------------

/** ConnectionProfile stores persistent configuration for a saved connection. */
export interface ConnectionProfile {
    /** Unique identifier. */
    readonly id: string;
    /** User-given display name. */
    readonly name: string;
    /** Connection type: "embedded" or "external". */
    readonly type: 'embedded' | 'external';
    /** Embedded server configuration (embedded type only). */
    readonly embedded?: EmbeddedConfig;
    /** External server configuration (external type only). */
    readonly external?: ExternalConnectionConfig;
    /** ISO 8601 creation timestamp. */
    readonly createdAt: string;
    /** ISO 8601 last-used timestamp. */
    readonly lastUsed?: string;
}

/** ExternalConnectionConfig stores connection parameters for a remote server. */
export interface ExternalConnectionConfig {
    /** Transport protocol: "rest", "grpc", "quic", "unix", "mcp". */
    readonly transport: string;
    /** Server address. */
    readonly address: string;
    /** Path to the CA certificate file. */
    readonly caFile?: string;
    /** Path to the client certificate file. */
    readonly certFile?: string;
    /** Path to the client private key file. */
    readonly keyFile?: string;
}

/** ConnectionState represents the runtime state of a connection. */
export interface ConnectionState {
    /** Profile ID this state belongs to. */
    readonly profileId: string;
    /** Current status. */
    readonly status: 'disconnected' | 'connecting' | 'needs-init' | 'sealed' | 'connected' | 'error';
    /** Error message when status is "error". */
    readonly error?: string;
    /** Whether this is an embedded connection. */
    readonly isEmbedded: boolean;
}
