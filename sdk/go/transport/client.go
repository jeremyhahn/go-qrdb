// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"context"
	"io"
	"math"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// DefaultClientMaxRecvMessageBytes is the response size the gRPC and Unix
// transports' clients accept, overriding grpc-go's own CLIENT-side default
// (4 MiB) -- a limit unrelated to, and unaffected by, the server's pre-auth
// MaxRecvMsgSize (go-qrdb#576 bounds what an unauthenticated peer can force
// the SERVER to allocate on receive; it says nothing about a response the
// server itself chooses to send, such as a `kv range`/`scan` whose matches
// exceed 4 MiB). It matches grpc-go's own SERVER-side MaxSendMsgSize default
// (math.MaxInt32), so a client built on either transport never truncates a
// response its server was willing to send, consistent with REST and QUIC
// having no response-size cap of their own.
const DefaultClientMaxRecvMessageBytes = math.MaxInt32

// ---------------------------------------------------------------------------
// Basic KV operations
// ---------------------------------------------------------------------------

// KVClient provides data-plane key-value operations.
type KVClient interface {
	// Put stores a key-value pair through Raft consensus. Optional
	// WriteOption controls the durability guarantee (default: Sync).
	Put(ctx context.Context, key string, value []byte, opts ...WriteOption) error

	// Get retrieves a value by key. Optional ReadOption controls the
	// consistency level (default: Linearizable).
	// Returns KeyNotFoundError when the key does not exist.
	Get(ctx context.Context, key string, opts ...ReadOption) ([]byte, error)

	// Delete removes a key through Raft consensus. Optional WriteOption
	// controls the durability guarantee (default: Sync).
	Delete(ctx context.Context, key string, opts ...WriteOption) error

	// List returns sorted keys matching the given prefix. Optional
	// ReadOption controls the consistency level (default: Linearizable).
	// An empty prefix returns all keys.
	List(ctx context.Context, prefix string, opts ...ReadOption) ([]string, error)

	// Scan returns all key-value pairs matching the given prefix.
	// Optional ReadOption controls the consistency level
	// (default: Linearizable).
	Scan(ctx context.Context, prefix string, opts ...ReadOption) (map[string][]byte, error)

	// Exists checks whether a key exists without retrieving the value.
	// Optional ReadOption controls the consistency level
	// (default: Linearizable).
	// Returns false with nil error when the key does not exist.
	Exists(ctx context.Context, key string, opts ...ReadOption) (bool, error)
}

// GuardedKVClient is a KVClient that also carries the single-key guarded write
// verbs — the conditional writes that arbitrate a cross-process race in one
// round trip rather than a read-then-write a second writer can slip between.
//
// It is a CAPABILITY interface, deliberately separate from KVClient, because a
// transport can only offer these once its wire contract carries them: the
// embedded transport delegates straight to the service and implements this
// today, while a remote transport implements it only when its protocol gains
// the verbs. A consumer detects the capability with a type assertion —
// `gv, ok := client.(GuardedKVClient)` — and MUST NOT assume a plain KVClient
// has it; that is what keeps a wrapper from promoting a guarded surface a
// transport cannot actually honour (go-qrdb#119).
type GuardedKVClient interface {
	KVClient

	// PutIfAbsent atomically stores value under key only when key is absent,
	// returning whether it stored. It is the acquire half of a claim: the
	// condition is evaluated in the state machine, so replicas cannot diverge
	// and two proposers cannot both win.
	//
	// It takes no WriteOption: a conditional write must be linearizable to mean
	// anything, so it is always synchronous — a relaxed-durability guarded write
	// would be a guarantee it cannot keep.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)

	// CompareAndDelete atomically removes key only when its stored value still
	// equals expected, returning whether it deleted. It is the release half of
	// PutIfAbsent: an unguarded delete could un-decide what the guarded put
	// decided. Synchronous for the same reason as PutIfAbsent.
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)

	// CompareAndSwap atomically replaces key's value with next only when its
	// stored value still equals expected, returning whether it replaced. An
	// absent key is left absent — a swap is not a create, which is what keeps
	// the three verbs distinct: PutIfAbsent creates, CompareAndSwap edits,
	// CompareAndDelete removes.
	//
	// It completes the guarded set at the transport boundary. service.KVServicer
	// has carried all three since go-qrdb#56, but only two reached this
	// interface in go-qrdb#119, so a consumer holding the capability could
	// arbitrate a create and a release and had to fall back to read-then-write
	// for the edit — reintroducing exactly the window the capability exists to
	// close. Synchronous for the same reason as the other two.
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)
}

// ---------------------------------------------------------------------------
// Admin operations
// ---------------------------------------------------------------------------

// AdminClient provides control-plane cluster management operations.
type AdminClient interface {
	// Health returns the health status of the QRDB node.
	Health(ctx context.Context) (*HealthResponse, error)

	// Status returns detailed status information about the node
	// and all loaded shards.
	Status(ctx context.Context) (*StatusResponse, error)

	// Peers returns the peer membership for all loaded shards.
	Peers(ctx context.Context) (*PeersResponse, error)

	// AddMember adds a new replica to the specified shard. identity is the
	// replica's Raft member identity (spki:<sha256-hex>, as printed by
	// `qrdbd node-identity --cert`), or "" to use the identity the cluster
	// has already committed for the replica or its host. A node no
	// committed membership names needs it; an identity that contradicts a
	// committed one is refused.
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error

	// RemoveMember removes a replica from the specified shard.
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error

	// PromoteMember promotes a committed observer of shardID to a voter,
	// keeping the address and identity the shard already committed for it;
	// nothing restarts. See pkg/service.AdminServicer.PromoteMember.
	PromoteMember(ctx context.Context, shardID, replicaID uint64) error

	// RequestSnapshot triggers an asynchronous snapshot for the
	// specified shard.
	RequestSnapshot(ctx context.Context, shardID uint64) error

	// Drain transitions the host into drain mode where new proposals
	// are rejected while in-flight operations complete.
	Drain(ctx context.Context) error

	// Capabilities returns the compile-time feature set of the QRDB binary.
	Capabilities(ctx context.Context) (*CapabilitiesResponse, error)

	// StartShard initializes and starts a Raft group for the given shard.
	// Members maps replicaID to address for initial cluster members.
	// Set Join to true when this node is joining an existing cluster.
	StartShard(ctx context.Context, req *StartShardRequest) error

	// StopShard stops the Raft group for the given shard.
	StopShard(ctx context.Context, shardID uint64) error

	// GetShardState returns the Raft state for a shard including
	// the configuration change index, term, and current leader.
	GetShardState(ctx context.Context, shardID uint64) (*ShardStateResponse, error)

	// GetTopology returns the full cluster topology as seen from
	// this node, including all loaded shards and their membership.
	GetTopology(ctx context.Context) (*TopologyResponse, error)

	// ReplicaStates returns, for each replica this node holds, its applied
	// index and state-machine hash: of shardID, or of every shard when
	// shardID is 0. The node answers for itself and never forwards, so
	// comparing replicas means asking every node. Requires
	// admin:replica_state.
	ReplicaStates(ctx context.Context, shardID uint64) (*ReplicaStatesResponse, error)

	// NodeCompaction returns the platform's node-keyspace compaction
	// policy -- the scheduled compaction of the keys outside every database
	// namespace -- or nil when none is set. It requires
	// admin:node_compaction, which only the system tenant can hold.
	NodeCompaction(ctx context.Context) (*DatabaseCompactionConfig, error)

	// SetNodeCompaction sets the platform's node-keyspace compaction
	// policy; nil turns it off. It returns the policy now in force, nil when
	// off. It requires admin:node_compaction (system tenant only).
	SetNodeCompaction(ctx context.Context, policy *DatabaseCompactionConfig) (*DatabaseCompactionConfig, error)

	// RetireJoinIdentity permanently refuses any future admission for a
	// node identity -- a fresh join-token mint, or a renewal of one whose
	// admission has lapsed -- after an admin decides it must never
	// (re-)join: a member removed from the cluster, or an identity whose
	// issued certificate must be treated as revoked. reason is an
	// optional, free-text operator note carried for audit only. See
	// pkg/service.JoinIdentityRetirer. Unlike MintJoinToken, its response
	// carries no secret, so it is a method on AdminClient directly rather
	// than a distinct capability interface callers type-assert for.
	RetireJoinIdentity(ctx context.Context, nodeID, reason string) error
}

// ClusterJoinTokenMinter mints a single-use, identity-bound member-side CSR
// join token, for an admin to hand to a joining node (see
// pkg/clusterenroll.MintJoinToken). It is a distinct interface from
// AdminClient, not a method on it, mirroring service.ClusterJoinTokenMinter
// on the server side: minting a token is an admission decision available
// only on a transport that has wired it, not a capability every AdminClient
// implementation must carry. A caller type-asserts for it
// (client.Admin().(transport.ClusterJoinTokenMinter)) rather than every
// transport needing a method it has no server-side route to call; see each
// transport package's own doc comment for which have.
//
// permission.ClusterJoinToken (ScopeCross, platform-only) gates the
// server-side route on every transport that implements this.
type ClusterJoinTokenMinter interface {
	MintJoinToken(ctx context.Context, req *MintJoinTokenRequest) (*MintJoinTokenResponse, error)
}

// ObserverMemberAdder adds a replica to a shard as a non-voting observer
// rather than a voter, for an admin to promote to a voter later with
// AdminClient.PromoteMember. It is a distinct interface from AdminClient,
// not a method on it, for the identical reason ClusterJoinTokenMinter is: a
// caller type-asserts for it (client.Admin().(transport.ObserverMemberAdder))
// rather than every transport needing a method it has no server-side route
// to call.
//
// permission.AdminAddMember (ScopeCross) gates the server-side route on
// every transport that implements this -- the same reconfiguration
// permission AddMember, RemoveMember and PromoteMember already require.
type ObserverMemberAdder interface {
	// AddObserverMember adds a new replica to the specified shard as an
	// observer. identity follows AdminClient.AddMember's own rule.
	AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error
}

// ReplicaStateHasher reports the applied index and SHA-256 state digest of
// every replica the answering node runs, isolated tenants' included, so a
// caller can compare replicas by asking every node: two replicas of a shard
// at the same applied index must report the same hash. It is a distinct
// interface from AdminClient, not a method on it, for the identical reason
// ClusterJoinTokenMinter is: a caller type-asserts for it
// (client.Admin().(transport.ReplicaStateHasher)). Every transport in this
// module implements it.
//
// permission.AdminStateHash (ScopeCross, system tenant only) gates the
// server-side route on every transport.
type ReplicaStateHasher interface {
	// StateHashes returns the state digest of every local replica whose
	// shard is in shardIDs; an empty shardIDs selects every shard. A shard
	// ID the node does not replicate selects nothing. One replica's failure
	// is reported in its own entry and never fails the call.
	StateHashes(ctx context.Context, shardIDs []uint64) (*StateHashesResponse, error)
}

// JoinRequestReviewer lists, approves and rejects CSRs a joiner queued
// through the tokenless member-side CSR approval path (see
// pkg/clusterenroll's approval queue). It is a distinct interface from
// AdminClient, not methods on it, for the identical reason
// ClusterJoinTokenMinter is: a caller type-asserts for it
// (client.Admin().(transport.JoinRequestReviewer)).
//
// permission.ClusterJoinToken (ScopeCross, platform-only) gates every
// method here on every transport that implements this -- the same
// whole-cluster admission decision minting a join token already requires.
type JoinRequestReviewer interface {
	// ListPendingJoinRequests returns every request awaiting a decision.
	ListPendingJoinRequests(ctx context.Context) ([]*PendingJoinRequest, error)

	// ApproveJoinRequest binds id's queued CSR to an operator-chosen node
	// identity (spiffe://<trust domain>/node/<nodeID>). The leaf is signed
	// the next time the joiner's own retry reaches the join listener, not
	// by this call.
	ApproveJoinRequest(ctx context.Context, id, nodeID string) error

	// RejectJoinRequest records a terminal refusal of id's queued CSR.
	RejectJoinRequest(ctx context.Context, id, reason string) error
}

// ---------------------------------------------------------------------------
// MVCC operations
// ---------------------------------------------------------------------------

// MVCCClient provides MVCC-aware key-value operations with revision
// tracking, lease attachment, and range queries.
type MVCCClient interface {
	// PutWithLease stores a key-value pair with an attached lease.
	// When the lease expires, the key is automatically deleted.
	// Optional WriteOption controls the durability guarantee.
	PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...WriteOption) (*MVCCKeyValue, error)

	// GetRevision retrieves a key-value pair with full MVCC revision
	// metadata. Optional ReadOption controls the consistency level.
	// Returns KeyNotFoundError when the key does not exist.
	GetRevision(ctx context.Context, key string, opts ...ReadOption) (*MVCCKeyValue, error)

	// GetAtRevision retrieves a key-value pair at a historical revision.
	// Optional ReadOption controls the consistency level.
	// Returns RevisionCompactedError when the revision has been compacted.
	GetAtRevision(ctx context.Context, key string, revision int64, opts ...ReadOption) (*MVCCKeyValue, error)

	// Range returns key-value pairs in the half-open interval [start, end).
	// Optional ReadOption controls the consistency level.
	Range(ctx context.Context, start, end []byte, rangeOpts RangeOptions, opts ...ReadOption) (*RangeResponse, error)

	// Compact discards all MVCC revisions older than the specified revision.
	Compact(ctx context.Context, revision int64) error

	// CurrentRevision returns the latest committed MVCC revision.
	CurrentRevision(ctx context.Context) (int64, error)

	// RestoreToRevision returns a bounded set of keys to the value they
	// held at an earlier MVCC revision by proposing new writes -- a
	// logical undo, never a Raft rewind. Exactly one of req.Prefix and
	// req.Keys must be set.
	RestoreToRevision(ctx context.Context, req *RestoreRevisionRequest) (*RestoreRevisionResult, error)
}

// ---------------------------------------------------------------------------
// Transaction operations
// ---------------------------------------------------------------------------

// TransactionClient provides atomic compare-and-swap transaction execution.
type TransactionClient interface {
	// ExecuteTransaction atomically evaluates compares and executes
	// the corresponding operation set. When all compares succeed,
	// success ops are applied; otherwise failure ops are applied.
	// Optional WriteOption controls the durability guarantee.
	ExecuteTransaction(ctx context.Context, req *TransactionRequest, opts ...WriteOption) (*TransactionResponse, error)
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

// BatchClient provides atomic batch operation execution.
type BatchClient interface {
	// ExecuteBatch applies the given operations atomically. All
	// operations succeed or fail together. Optional WriteOption
	// controls the durability guarantee.
	ExecuteBatch(ctx context.Context, ops []Op, opts ...WriteOption) (*BatchResponse, error)

	// ImportFile parses and imports key-value pairs from the given
	// reader. The filename parameter is used for format auto-detection
	// when opts.Format is "auto" or empty. Returns statistics about
	// the import operation.
	//
	// Internally this drives ImportBegin/ImportAppend/ImportCommit below,
	// splitting data into chunks no larger than the transport's pre-auth
	// message cap -- there is no single-message import RPC on any
	// transport, and there is no larger-message opt-in. A caller that
	// wants finer control (streaming from a live source, managing its
	// own retry of a dropped chunk) drives the four session methods
	// directly instead of calling ImportFile.
	ImportFile(ctx context.Context, filename string, data io.Reader, opts ImportOptions) (*ImportResult, error)

	// ImportBegin starts a chunked import session and returns its ID.
	// See docs/transactions/architecture.md ("Import session").
	ImportBegin(ctx context.Context, opts ImportOptions) (string, error)

	// ImportAppend stages chunk as sequence seq of sessionID. seq must
	// be exactly one more than the last appended sequence, or equal to
	// it with identical bytes (an idempotent replay of a call whose
	// response the caller may not have seen).
	ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error

	// ImportCommit applies every staged chunk of sessionID atomically
	// and releases the session on success.
	ImportCommit(ctx context.Context, sessionID string) (*ImportResult, error)

	// ImportAbort releases sessionID without applying anything.
	ImportAbort(ctx context.Context, sessionID string) error
}

// DefaultImportChunkBytes is the chunk size ImportFile splits its input
// into on a transport that carries the chunk as raw bytes (a protobuf
// `bytes` field on gRPC/Unix, a wire frame on QUIC, a multipart field on
// REST). It is comfortably under config.DefaultTransportMaxMessageBytes
// (4 MiB, the universal pre-auth cap every transport applies by default),
// leaving headroom for that framing itself, so a chunk at this size is
// never itself refused as oversized.
//
// A transport that must re-encode the chunk before it is wire-sized --
// MCP, whose JSON-RPC body carries it as base64 text -- does NOT use this
// constant: base64 alone expands n bytes to 4n/3, so this chunk size would
// already consume the entire cap with no room left for the request's own
// envelope. See DefaultMCPImportChunkBytes.
const DefaultImportChunkBytes = 3 << 20

// mcpImportEnvelopeMargin is the space DefaultMCPImportChunkBytes reserves,
// out of config.DefaultTransportMaxMessageBytes, for everything in an
// ImportAppend call that is not the base64-encoded chunk itself: the
// surrounding JSON-RPC 2.0 request (method, id, params), the tool name,
// and the session_id and seq arguments. 64 KiB is generous for all of
// that combined -- a session ID is a short opaque token and seq is a
// single integer -- so the margin is headroom, not a tight fit.
const mcpImportEnvelopeMargin = 64 << 10

// ImportChunkBytesForEncodedMessage returns the largest raw chunk size
// whose base64 encoding, plus margin bytes reserved for everything else in
// the request, fits within maxMessageBytes. It floors to a multiple of 3
// so the encoding needs no padding, making the arithmetic exact rather
// than a worst-case estimate: base64 encodes exactly 4n/3 bytes for n a
// multiple of 3. A maxMessageBytes no larger than margin yields 0.
func ImportChunkBytesForEncodedMessage(maxMessageBytes, margin int) int {
	usable := maxMessageBytes - margin
	if usable <= 0 {
		return 0
	}
	return (usable / 4) * 3
}

// DefaultMCPImportChunkBytes is the chunk size ImportFile splits its input
// into on the MCP transport, whose JSON-RPC body has no binary framing and
// so carries each chunk as base64 text (see mcp.Transport.ImportAppend):
// derived from config.DefaultTransportMaxMessageBytes so that the encoded
// chunk, plus mcpImportEnvelopeMargin for the rest of the call, never
// itself exceeds the universal pre-auth body cap every transport applies
// by default (go-qrdb#576: the previous, transport-agnostic
// DefaultImportChunkBytes encoded to exactly the cap's size, leaving zero
// room for the envelope around it).
var DefaultMCPImportChunkBytes = ImportChunkBytesForEncodedMessage(config.DefaultTransportMaxMessageBytes, mcpImportEnvelopeMargin)

// ImportFileChunked is the shared implementation of BatchClient.ImportFile:
// it drives client's own ImportBegin/ImportAppend/ImportCommit/ImportAbort
// to carry data as a chunked session, so every transport's ImportFile is a
// one-line call to this function rather than five separate copies of the
// same chunking loop. chunkSize <= 0 resolves to DefaultImportChunkBytes.
//
// A failure appending any chunk aborts the session (best-effort -- the
// abort's own error is not returned, since the append error is the one
// that matters to the caller) rather than leaving it to expire on its
// lease TTL.
func ImportFileChunked(ctx context.Context, client BatchClient, filename string, data io.Reader, opts ImportOptions, chunkSize int) (*ImportResult, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultImportChunkBytes
	}
	opts.Filename = filename

	sessionID, err := client.ImportBegin(ctx, opts)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, chunkSize)
	var seq uint64
	for {
		n, readErr := io.ReadFull(data, buf)
		if n > 0 {
			if appendErr := client.ImportAppend(ctx, sessionID, seq, buf[:n]); appendErr != nil {
				_ = client.ImportAbort(ctx, sessionID)
				return nil, appendErr
			}
			seq++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			_ = client.ImportAbort(ctx, sessionID)
			return nil, &RequestError{Method: "ImportFile", Err: readErr}
		}
	}

	return client.ImportCommit(ctx, sessionID)
}

// ---------------------------------------------------------------------------
// Watch operations
// ---------------------------------------------------------------------------

// WatchClient provides event streaming for key changes.
type WatchClient interface {
	// Watch creates a watcher that delivers events for the specified
	// key. Returns a channel of WatchResponse that is closed when the
	// watch ends. The caller must cancel the context to stop watching.
	Watch(ctx context.Context, key string, opts WatchOptions) (<-chan WatchResponse, error)

	// WatchPrefix creates a watcher that delivers events for all keys
	// matching the given prefix.
	WatchPrefix(ctx context.Context, prefix string, opts WatchOptions) (<-chan WatchResponse, error)
}

// ---------------------------------------------------------------------------
// Lease operations
// ---------------------------------------------------------------------------

// LeaseClient provides distributed lease management.
type LeaseClient interface {
	// LeaseGrant creates a new lease with the specified TTL in seconds.
	LeaseGrant(ctx context.Context, ttl int64) (*LeaseInfo, error)

	// LeaseRevoke immediately expires a lease and deletes attached keys.
	LeaseRevoke(ctx context.Context, leaseID int64) error

	// LeaseKeepAlive resets the TTL on an existing lease.
	LeaseKeepAlive(ctx context.Context, leaseID int64) (*LeaseInfo, error)

	// LeaseTimeToLive returns the current state of a lease.
	LeaseTimeToLive(ctx context.Context, leaseID int64) (*LeaseInfo, error)

	// LeaseList returns all active leases.
	LeaseList(ctx context.Context) ([]*LeaseInfo, error)
}

// ---------------------------------------------------------------------------
// Distributed lock operations
// ---------------------------------------------------------------------------

// LockClient provides distributed lock management with fencing tokens.
type LockClient interface {
	// LockAcquire acquires a distributed lock, blocking until the lock is
	// available or the context is cancelled.
	LockAcquire(ctx context.Context, req *LockAcquireRequest) (*LockInfo, error)

	// LockTryLock attempts to acquire the lock without blocking.
	// Returns an error immediately when the lock is held by another owner.
	LockTryLock(ctx context.Context, req *LockAcquireRequest) (*LockInfo, error)

	// LockRelease releases the lock identified by key and fencing token.
	// Returns an error when the fencing token does not match the current lock.
	LockRelease(ctx context.Context, req *LockReleaseRequest) error

	// LockExtend refreshes the TTL of an existing lock via lease keepalive.
	// Returns an error when the fencing token does not match the current lock.
	LockExtend(ctx context.Context, req *LockExtendRequest) (*LockInfo, error)

	// LockStatus returns the current lock state for key, or nil when not locked.
	LockStatus(ctx context.Context, key string) (*LockInfo, error)

	// LockList returns all active locks whose keys start with the given prefix.
	// An empty prefix returns all active locks.
	LockList(ctx context.Context, prefix string) (*LockListResponse, error)
}

// ---------------------------------------------------------------------------
// Index operations
// ---------------------------------------------------------------------------

// IndexClient provides secondary index management operations.
type IndexClient interface {
	// IndexCreate creates a new secondary index with the given name,
	// field, and configuration.
	IndexCreate(ctx context.Context, name, fieldName string, config IndexConfig) error

	// IndexDrop removes a secondary index by name.
	IndexDrop(ctx context.Context, name string) error

	// IndexQuery returns entity keys matching an exact value in an index.
	IndexQuery(ctx context.Context, indexName, value string) (*IndexQueryResponse, error)

	// IndexScan returns entries in an index within a value range [start, end).
	// A nil start or end is an OPEN bound on that side, distinct from a non-nil
	// empty bound which is an explicit "" value (go-qrdb#36). A one-sided range
	// such as Team >= "beta" passes a non-nil start and a nil end.
	IndexScan(ctx context.Context, indexName string, start, end *string) (*IndexScanResponse, error)

	// IndexList returns the names of all registered indexes.
	IndexList(ctx context.Context) (*IndexListResponse, error)
}

// ---------------------------------------------------------------------------
// Time-series operations
// ---------------------------------------------------------------------------

// TimeSeriesClient provides time-series data ingestion and querying.
type TimeSeriesClient interface {
	// TSWrite stores a single time-series data point.
	TSWrite(ctx context.Context, point *TSDataPoint) error

	// TSWriteBatch stores multiple time-series data points atomically.
	TSWriteBatch(ctx context.Context, points []TSDataPoint) error

	// TSQuery retrieves data points for a metric within a time range.
	TSQuery(ctx context.Context, req *TSQueryRequest) (*TSQueryResponse, error)

	// TSAggregate computes an aggregation over data points in a time range.
	TSAggregate(ctx context.Context, req *TSAggregateRequest) (*TSAggregateResponse, error)

	// TSWritePoint stores a single validated data point.
	TSWritePoint(ctx context.Context, point *TSDataPoint) error

	// TSRichQuery performs a rich query with aggregation, downsampling, and grouping.
	TSRichQuery(ctx context.Context, req *TSRichQueryRequest) (*TSRichQueryResponse, error)

	// TSDeleteSeries deletes time-series data matching the criteria.
	TSDeleteSeries(ctx context.Context, req *TSDeleteRequest) (*TSDeleteResponse, error)

	// TSGetRetention retrieves the retention policy for a metric.
	TSGetRetention(ctx context.Context, req *TSGetRetentionRequest) (*TSGetRetentionResponse, error)

	// TSSetRetention sets the retention policy for a metric.
	TSSetRetention(ctx context.Context, req *TSSetRetentionRequest) error

	// TSListMetrics lists metric names matching a prefix.
	TSListMetrics(ctx context.Context, req *TSListMetricsRequest) (*TSListMetricsResponse, error)
}

// ---------------------------------------------------------------------------
// Vector operations
// ---------------------------------------------------------------------------

// VectorClient provides vector database operations including namespace
// management, vector CRUD, similarity search, and index maintenance.
type VectorClient interface {
	// CreateNamespace creates a new vector namespace.
	CreateNamespace(ctx context.Context, req *CreateNamespaceRequest) (*CreateNamespaceResponse, error)

	// DeleteNamespace removes a namespace and all its vectors.
	DeleteNamespace(ctx context.Context, req *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error)

	// ListNamespaces returns all vector namespaces.
	ListNamespaces(ctx context.Context, req *ListNamespacesRequest) (*ListNamespacesResponse, error)

	// GetNamespaceInfo returns metadata about a namespace.
	GetNamespaceInfo(ctx context.Context, req *GetNamespaceInfoRequest) (*GetNamespaceInfoResponse, error)

	// UpsertVector inserts or updates a vector in a namespace.
	UpsertVector(ctx context.Context, req *UpsertVectorRequest) (*UpsertVectorResponse, error)

	// GetVector retrieves a single vector by ID.
	GetVector(ctx context.Context, req *GetVectorRequest) (*GetVectorResponse, error)

	// DeleteVector removes a single vector by ID.
	DeleteVector(ctx context.Context, req *DeleteVectorRequest) (*DeleteVectorResponse, error)

	// BatchUpsert inserts or updates multiple vectors.
	BatchUpsert(ctx context.Context, req *BatchUpsertRequest) (*BatchUpsertResponse, error)

	// BatchDeleteVectors removes multiple vectors by ID.
	BatchDeleteVectors(ctx context.Context, req *BatchDeleteVectorsRequest) (*BatchDeleteVectorsResponse, error)

	// ListVectors returns a paginated list of vectors.
	ListVectors(ctx context.Context, req *ListVectorsRequest) (*ListVectorsResponse, error)

	// UpdateMetadata updates the metadata for an existing vector.
	UpdateMetadata(ctx context.Context, req *UpdateVectorMetadataRequest) (*UpdateVectorMetadataResponse, error)

	// SearchSimilar performs a k-nearest-neighbor search.
	SearchSimilar(ctx context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error)

	// SearchByMetadata returns vectors matching metadata filters.
	SearchByMetadata(ctx context.Context, req *SearchByMetadataRequest) (*SearchByMetadataResponse, error)

	// HybridSearch combines vector similarity and metadata filtering.
	HybridSearch(ctx context.Context, req *HybridSearchRequest) (*HybridSearchResponse, error)

	// SearchByID searches using an existing vector's ID as the query.
	SearchByID(ctx context.Context, req *SearchByIDRequest) (*SearchByIDResponse, error)

	// RebuildIndex triggers an asynchronous index rebuild.
	RebuildIndex(ctx context.Context, req *RebuildIndexRequest) (*RebuildIndexResponse, error)

	// GetRebuildProgress returns the current progress of an index rebuild.
	GetRebuildProgress(ctx context.Context, req *GetRebuildProgressRequest) (*GetRebuildProgressResponse, error)

	// GetIndexStats returns statistics about a namespace's vector index.
	GetIndexStats(ctx context.Context, req *GetIndexStatsRequest) (*GetIndexStatsResponse, error)

	// CompactNamespace triggers compaction of a namespace's storage.
	CompactNamespace(ctx context.Context, req *CompactNamespaceRequest) (*CompactNamespaceResponse, error)

	// GetCompactionProgress returns the current progress of a compaction.
	GetCompactionProgress(ctx context.Context, req *GetCompactionProgressRequest) (*GetCompactionProgressResponse, error)
}

// ---------------------------------------------------------------------------
// User management operations
// ---------------------------------------------------------------------------

// UserClient provides user management operations.
type UserClient interface {
	// CreateUser creates a new passwordless user. The username must be
	// unique within the tenant.
	CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error)

	// GetUser retrieves a user by its globally unique ID.
	GetUser(ctx context.Context, id uint64) (*User, error)

	// GetUserByUsername retrieves a user by tenant-scoped username. The
	// tenantID may be empty to use the default tenant. Returns ErrNotFound
	// when no user with the given username exists in the tenant.
	GetUserByUsername(ctx context.Context, tenantID, username string) (*User, error)

	// GetUserByEmail retrieves a user by tenant-scoped email — the canonical
	// login key. The tenantID may be empty to use the default tenant. Returns
	// ErrNotFound when no user with the given email exists in the tenant.
	//
	// The tenant is part of the key, not a filter: an email is unique within a
	// tenant, not globally, so the same address may name different people in
	// two tenants.
	GetUserByEmail(ctx context.Context, tenantID, email string) (*User, error)

	// GetUserByCertThumbprint resolves a user by an mTLS certificate's SPKI
	// thumbprint via the server-side secondary index. This is the mTLS-auth
	// hot path: callers compute ComputeSPKIThumbprint(peerCert) and resolve
	// the bound user synchronously. Returns ErrNotFound when no user is
	// bound to the thumbprint.
	GetUserByCertThumbprint(ctx context.Context, thumbprint string) (*User, error)

	// AddCertBinding binds an mTLS certificate's SPKI thumbprint to the user
	// server-side so GetUserByCertThumbprint resolves it. A notAfterUnix > 0
	// sets the binding's expiry; otherwise the binding never expires. This is
	// the enrollment seam used to bind an admin's certificate to a user.
	AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*User, error)

	// RemoveCertBinding withdraws an mTLS certificate's SPKI thumbprint from
	// the user server-side, so GetUserByCertThumbprint no longer resolves it
	// and a certificate issued for that key no longer authenticates as the
	// account. It is the counterpart of AddCertBinding and the only way to
	// unenroll a key.
	//
	// It is NOT certificate revocation. Revocation refuses a certificate;
	// a binding is over the SubjectPublicKeyInfo and outlives every
	// certificate issued for that key, so a re-issue for the same key
	// authenticates again until the binding is withdrawn.
	//
	// Withdrawing the LAST binding WIDENS the account: an empty binding set
	// states no restriction, so it returns to admitting any certificate whose
	// CN and OU name it. Enrol the replacement first.
	RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*User, error)

	// UpdateUser applies partial updates to an existing user.
	UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error)

	// DeleteUser removes a non-system user by ID.
	DeleteUser(ctx context.Context, id uint64) error

	// ListUsers returns a paginated list of users for the given tenant.
	ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error)

	// EnableUser activates a user account.
	EnableUser(ctx context.Context, id uint64) error

	// DisableUser deactivates a user account.
	DisableUser(ctx context.Context, id uint64) error
}

// ---------------------------------------------------------------------------
// Role management operations
// ---------------------------------------------------------------------------

// RoleClient provides role management operations.
type RoleClient interface {
	// CreateRole creates a new role with a generated UUID. The name must
	// be unique within the tenant.
	CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error)

	// GetRole retrieves a role by its globally unique ID.
	GetRole(ctx context.Context, id string) (*Role, error)

	// GetRoleByName retrieves a role by its tenant-scoped name.
	GetRoleByName(ctx context.Context, req *GetRoleByNameRequest) (*Role, error)

	// UpdateRole applies partial updates to an existing role.
	UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error)

	// DeleteRole removes a non-system role by ID.
	DeleteRole(ctx context.Context, id string) error

	// ListRoles returns a paginated list of roles for the given tenant.
	ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error)

	// AssignRole assigns a role to a user.
	AssignRole(ctx context.Context, userID uint64, roleID string) error

	// UnassignRole removes a role assignment from a user.
	UnassignRole(ctx context.Context, userID uint64, roleID string) error

	// ListPermissions returns all available permission categories for
	// UI rendering.
	ListPermissions(ctx context.Context) (*ListPermissionsResponse, error)

	// ResolvePermissions returns the union of effective permissions for the
	// given role IDs (sorted, deduplicated).
	ResolvePermissions(ctx context.Context, req *ResolvePermissionsRequest) (*ResolvePermissionsResponse, error)

	// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto
	// the server build's definitions: permissions the build now grants are
	// added, permissions it no longer grants are removed, and custom roles are
	// left untouched. It is the ONLY way to rewrite a built-in role's grants —
	// UpdateRole refuses a system role by design — and it is idempotent: a
	// converged tenant reports no changed roles and nothing is written.
	//
	// A consumer that contributes its own permissions to the server's registry
	// calls this AFTER those permissions are registered, or it will converge
	// every tenant onto a definition missing its own verbs.
	ReconcileBuiltInRoles(ctx context.Context, req *ReconcileRolesRequest) (*ReconcileRolesResponse, error)
}

// ---------------------------------------------------------------------------
// Certificate management operations
// ---------------------------------------------------------------------------

// CertClient provides certificate management operations.
type CertClient interface {
	// IssueCert issues a certificate from a CSR for an existing account of
	// the caller's own tenant, signed by that tenant's own CA, and binds it
	// to the account. No private key leaves the caller.
	//
	// BREAKING: this replaces GenerateCert, which had the server generate
	// the client's private key and return it.
	IssueCert(ctx context.Context, req *IssueCertRequest) (*EnrolledCert, error)

	// EnrollCert binds a bring-your-own certificate to an existing account of
	// the caller's own tenant, after verifying it chains to one of the
	// tenant's trust anchors and authenticates as that account.
	EnrollCert(ctx context.Context, req *EnrollCertRequest) (*EnrolledCert, error)

	// IssueDelegation has the caller's own tenant's CA issue a short-lived
	// delegation certificate from a CSR: the named service principal may act
	// inside the tenant with at most the delegated scope, intersected with
	// its own grants. The system tenant and a delegated caller are refused.
	IssueDelegation(ctx context.Context, req *IssueDelegationRequest) (*IssuedDelegation, error)

	// RevokeCert marks a certificate serial as revoked and reports what the
	// target node found: RevokeResult.RecordFound distinguishes a
	// certificate the target holds a record for from a deny-by-serial
	// revocation of one it does not. Both are successes -- the marker is
	// written unconditionally -- which is why the distinction is carried in
	// the result rather than the error return.
	//
	// BREAKING (go-qrdb#287): this method previously returned only error.
	RevokeCert(ctx context.Context, serial string) (*RevokeResult, error)

	// ListCerts lists all issued client certificates for a tenant.
	ListCerts(ctx context.Context, tenantID string) (*ListCertsResponse, error)
}

// ---------------------------------------------------------------------------
// Invitation self-enrolment
// ---------------------------------------------------------------------------

// InvitationClient provides invitation-based self-enrolment: the way an
// ordinary user gets a first certificate for a key nobody else ever held.
//
// It is deliberately its own interface, not folded into UserClient or
// CertClient: RedeemInvitation is the one verb an UNCONNECTED, unauthenticated
// caller may invoke, and a transport that cannot yet carry it (no wire
// support implemented) simply does not implement InvitationClient, rather
// than every UserClient/CertClient implementation being forced to grow a
// method it cannot serve.
type InvitationClient interface {
	// InviteUser creates a pending account in the caller's own tenant -- or
	// re-invites one that is still pending -- and returns a single-use
	// enrolment token, returned exactly once. The invited user redeems it,
	// unauthenticated, with RedeemInvitation.
	InviteUser(ctx context.Context, req *InviteUserRequest) (*Invitation, error)

	// RedeemInvitation is UNAUTHENTICATED: the token from InviteUser is the
	// authority. The redeemer presents the tenant, the token and a CSR for
	// their own key (proof of possession) -- or a bring-your-own certificate
	// over that key -- and gets a certificate for the invited account, which
	// is activated with the binding in the same operation. No Connect is
	// required first: an unconnected client dials for this one call.
	RedeemInvitation(ctx context.Context, req *RedeemInvitationRequest) (*EnrolledCert, error)
}

// ---------------------------------------------------------------------------
// DEK operations
// ---------------------------------------------------------------------------

// DEKClient serves the caller's per-tenant data encryption keyring: a
// versioned set of AES-256-GCM keys that never leave the server.
//
// Every method acts on the caller's own tenant. A request naming another
// tenant is refused with PermissionDenied (403), from the system tenant too.
// Rotation keeps old versions decrypting; Rewrap moves a ciphertext to the
// current version; Retire destroys an old version and refuses the current
// one. The keyring cannot know where ciphertexts are stored, so rewrap
// everything under a version before retiring it.
type DEKClient interface {
	// TenantDEKStatus returns the keyring's versions. It never returns keys.
	TenantDEKStatus(ctx context.Context, req *TenantDEKRequest) (*TenantDEKStatusResponse, error)

	// TenantDEKRotate adds a key version and makes it current.
	TenantDEKRotate(ctx context.Context, req *TenantDEKRequest) (*TenantDEKStatusResponse, error)

	// TenantDEKRetire destroys an old key version's key.
	TenantDEKRetire(ctx context.Context, req *TenantDEKRetireRequest) (*TenantDEKStatusResponse, error)

	// TenantDEKEncrypt seals plaintext under the current key version.
	TenantDEKEncrypt(ctx context.Context, req *TenantDEKEncryptRequest) (*TenantDEKEncryptResponse, error)

	// TenantDEKDecrypt opens a ciphertext sealed for the caller's tenant.
	TenantDEKDecrypt(ctx context.Context, req *TenantDEKDecryptRequest) (*TenantDEKDecryptResponse, error)

	// TenantDEKRewrap re-seals a ciphertext under the current key version
	// without returning its plaintext.
	TenantDEKRewrap(ctx context.Context, req *TenantDEKRewrapRequest) (*TenantDEKRewrapResponse, error)
}

// ---------------------------------------------------------------------------
// Barrier operations
// ---------------------------------------------------------------------------

// BarrierClient manages the cryptographic barrier that protects data at rest.
type BarrierClient interface {
	// BarrierInitialize initializes the barrier with a single master key.
	BarrierInitialize(ctx context.Context, req *BarrierInitializeRequest) error

	// BarrierUnseal unseals the barrier using the master key.
	BarrierUnseal(ctx context.Context, req *BarrierUnsealRequest) error

	// BarrierSeal seals the barrier, preventing further data access until
	// it is unsealed again.
	BarrierSeal(ctx context.Context) error

	// BarrierStatus returns the current state of the cryptographic barrier.
	BarrierStatus(ctx context.Context) (*BarrierStatusResponse, error)

	// BarrierClusterUnseal clears a cluster-wide seal of the node barrier and
	// returns its cluster seal state. It unseals nothing: the node it reaches
	// must already have unsealed itself.
	BarrierClusterUnseal(ctx context.Context) (*ClusterSeal, error)

	// BarrierInitializeShamir initializes the barrier under Shamir custody
	// and distributes one sealed share envelope to each holder enrolled in
	// the unseal domain. Share material is never returned; each holder
	// claims its own envelope via ShareClaimBegin/ShareClaimComplete.
	BarrierInitializeShamir(ctx context.Context, req *BarrierInitializeShamirRequest) (*BarrierInitializeShamirResponse, error)

	// BarrierEnableEncryption migrates a previously-unencrypted barrier to a
	// keyed strategy. The migration runs asynchronously on the server (a
	// system-scope migration recycles the data-plane server in process); the
	// response acknowledges the start and clients poll BarrierStatus for the
	// strategy change. Supported on every transport -- REST, gRPC, Unix,
	// QUIC, MCP and embedded -- and refuses with a typed error when the node
	// has no EncryptionMigrator configured.
	BarrierEnableEncryption(ctx context.Context, req *BarrierEnableEncryptionRequest) (*BarrierEnableEncryptionResponse, error)

	// BarrierGenerateRecoveryKeys generates the independent recovery key and
	// distributes it to the holders enrolled in the recovery domain.
	BarrierGenerateRecoveryKeys(ctx context.Context, req *BarrierGenerateRecoveryKeysRequest) (*BarrierRecoveryKeysResponse, error)

	// BarrierDeleteRecoveryKeys deletes all stored recovery keys.
	BarrierDeleteRecoveryKeys(ctx context.Context) error

	// BarrierHasRecoveryKeys checks whether recovery keys have been
	// generated.
	BarrierHasRecoveryKeys(ctx context.Context) (*BarrierHasRecoveryKeysResponse, error)

	// OfficerRegister enrols a share holder (Security Officer or key
	// custodian) in the unseal domain. The CA-issued certificate in the
	// response must reach the holder out of band.
	OfficerRegister(ctx context.Context, req *OfficerRegisterRequest) (*OfficerRegisterResponse, error)

	// OfficerList lists the unseal domain's holders and their claim state.
	OfficerList(ctx context.Context) ([]*OfficerStatus, error)

	// ShareClaimBegin issues a claim nonce to the named unseal-domain holder,
	// who must present its certificate on the connection.
	ShareClaimBegin(ctx context.Context, req *ShareClaimBeginRequest) (*ShareClaimBeginResponse, error)

	// ShareClaimComplete verifies the holder's nonce signature and delivers
	// its sealed share envelope exactly once.
	ShareClaimComplete(ctx context.Context, req *ShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)

	// RecoveryOfficerRegister enrols a recovery-domain holder.
	RecoveryOfficerRegister(ctx context.Context, req *OfficerRegisterRequest) (*OfficerRegisterResponse, error)

	// RecoveryShareClaimBegin issues a claim nonce to a recovery holder.
	RecoveryShareClaimBegin(ctx context.Context, req *ShareClaimBeginRequest) (*ShareClaimBeginResponse, error)

	// RecoveryShareClaimComplete delivers a recovery holder's envelope once.
	RecoveryShareClaimComplete(ctx context.Context, req *ShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)

	// BarrierQuorumBegin issues the factor challenge a holder answers to
	// submit a share. It is reachable while the barrier is sealed.
	BarrierQuorumBegin(ctx context.Context, req *QuorumBeginRequest) (*QuorumChallenge, error)

	// BarrierQuorumSubmit submits one authenticated holder's share toward
	// the open round for (domain, op): unseal and recover open the barrier,
	// seal seals it, root-token derives the root token, rekey completes an
	// open rekey. Each holder counts once per round.
	BarrierQuorumSubmit(ctx context.Context, req *QuorumSubmitRequest) (*QuorumOutcome, error)

	// BarrierRekeyStart opens a rekey round.
	BarrierRekeyStart(ctx context.Context, req *RekeyStartRequest) (*QuorumRound, error)

	// BarrierOfficerRevoke revokes a holder through a quorum-gated rekey
	// that drops it atomically.
	BarrierOfficerRevoke(ctx context.Context, req *OfficerRevokeRequest) (*QuorumRound, error)
}

// ---------------------------------------------------------------------------
// Ceremony operations
// ---------------------------------------------------------------------------

// CeremonyClient manages the initialization ceremony lifecycle including
// M-of-N officer enrollment, certificate claims, and share distribution.
type CeremonyClient interface {
	// CeremonyInitialize starts the initialization ceremony with the
	// provided configuration.
	CeremonyInitialize(ctx context.Context, req *CeremonyInitializeRequest) (*CeremonyInitializeResponse, error)

	// CeremonyStatus returns the current ceremony state.
	CeremonyStatus(ctx context.Context) (*CeremonyStatusResponse, error)

	// CeremonySignCSR submits a CSR for signing during or after the
	// ceremony.
	CeremonySignCSR(ctx context.Context, req *SignCSRInitRequest) (*SignCSRInitResponse, error)
}

// ---------------------------------------------------------------------------
// Credential operations
// ---------------------------------------------------------------------------

// CredentialClient manages named credentials using the configured
// sealing strategy. The interface surfaces both non-versioned
// (Submit/Strategy/Delete) and versioned (List/Rotate/RetrieveVersion/
// ListVersions/GCExpired) operations. All transports MUST provide
// real-wire implementations of every method — NotImplementedError
// returns are forbidden per platform CLAUDE.md "no stubs" policy.
type CredentialClient interface {
	// CredentialSubmit submits a named credential value.
	CredentialSubmit(ctx context.Context, req *CredentialSubmitRequest) error

	// CredentialStrategy returns the active credential management strategy.
	CredentialStrategy(ctx context.Context) (*CredentialStrategyResponse, error)

	// CredentialDelete removes a named credential and all of its
	// non-current historical versions.
	CredentialDelete(ctx context.Context, name string) error

	// CredentialList returns the names of every stored credential.
	// Values are never returned by enumeration.
	CredentialList(ctx context.Context, req *CredentialListRequest) (*CredentialListResponse, error)

	// CredentialRotate atomically replaces the current value of a
	// credential, optionally keeping the previous version retrievable
	// for a grace period.
	CredentialRotate(ctx context.Context, req *CredentialRotateRequest) (*CredentialRotateResponse, error)

	// CredentialRetrieveVersionBuffer returns the stored credential value
	// for a specific version in protected memory. Use version == 0 for the
	// current version. The caller owns the returned buffer and MUST Destroy
	// it on every path.
	CredentialRetrieveVersionBuffer(ctx context.Context, req *CredentialRetrieveVersionRequest) (*secure.Buffer, error)

	// CredentialListVersions returns the version history (current +
	// non-expired grace) for a credential. Values are never returned.
	CredentialListVersions(ctx context.Context, req *CredentialListVersionsRequest) (*CredentialListVersionsResponse, error)

	// CredentialGCExpired reaps historical versions whose grace
	// period has elapsed. Returns the count reaped.
	CredentialGCExpired(ctx context.Context, req *CredentialGCRequest) (*CredentialGCResponse, error)

	// CredentialStore persists a credential value under the given name.
	CredentialStore(ctx context.Context, name string, value []byte) error

	// CredentialRetrieveBuffer returns the credential value for the given
	// name in protected memory. For the manual strategy this blocks until
	// the credential is submitted or the context is cancelled. The caller
	// owns the returned buffer and MUST Destroy it on every path.
	CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error)
}

// ---------------------------------------------------------------------------
// Seal store operations
// ---------------------------------------------------------------------------

// SealStoreClient manages the local machine sealed credential store.
type SealStoreClient interface {
	// SealStorePut stores a secret in the seal store.
	SealStorePut(ctx context.Context, req *SealStorePutRequest) error

	// SealStoreGet retrieves a secret from the seal store.
	SealStoreGet(ctx context.Context, req *SealStoreGetRequest) (*SealStoreGetResponse, error)

	// SealStoreDelete deletes a secret from the seal store.
	SealStoreDelete(ctx context.Context, req *SealStoreDeleteRequest) error

	// SealStoreList lists all stored secret names.
	SealStoreList(ctx context.Context) (*SealStoreListResponse, error)

	// SealStoreReseal reseals a secret with the current sealing key.
	SealStoreReseal(ctx context.Context, req *SealStoreResealRequest) error

	// SealStoreStatus returns the seal store status.
	SealStoreStatus(ctx context.Context) (*SealStoreStatusResponse, error)
}

// ---------------------------------------------------------------------------
// Tenant operations
// ---------------------------------------------------------------------------

// TenantClient manages multi-tenant operations and per-tenant barriers.
type TenantClient interface {
	// CreateTenant creates a new tenant.
	CreateTenant(ctx context.Context, req *CreateTenantRequest) (*CreateTenantResponse, error)

	// GetTenant retrieves a tenant by ID.
	GetTenant(ctx context.Context, tenantID string) (*GetTenantResponse, error)

	// ListTenants lists all tenants.
	ListTenants(ctx context.Context) (*ListTenantsResponse, error)

	// ListDeletedTenants returns one page of retired tenant ids with their
	// deletion records, in ascending id order.
	ListDeletedTenants(ctx context.Context, req *ListDeletedTenantsRequest) (*ListDeletedTenantsResponse, error)

	// ReconcileDeletedTenants removes, from the node serving the request, the
	// on-disk residue of every retired tenant id. It never removes a directory
	// a live tenant claims. It is node-local: in a cluster, run it against
	// each node.
	ReconcileDeletedTenants(ctx context.Context) (*ReconcileDeletedTenantsResponse, error)

	// DeleteTenant deletes a tenant.
	DeleteTenant(ctx context.Context, tenantID string) error

	// UpdateTenant applies a partial update to a tenant.
	UpdateTenant(ctx context.Context, req *UpdateTenantRequest) (*UpdateTenantResponse, error)

	// UpdateTenantQuotas sets the resource quotas for a tenant.
	UpdateTenantQuotas(ctx context.Context, req *UpdateTenantQuotasRequest) (*UpdateTenantQuotasResponse, error)

	// UpdateTenantEntitlements sets the feature entitlements for a tenant.
	UpdateTenantEntitlements(ctx context.Context, req *UpdateTenantEntitlementsRequest) (*UpdateTenantEntitlementsResponse, error)

	// AddTrustAnchors registers CA certificates as trust anchors of
	// req.TenantID, which must be the caller's own tenant: certificates
	// chaining to one may then be enrolled on, and log in as, that tenant's
	// accounts -- no other tenant's.
	AddTrustAnchors(ctx context.Context, req *AddTrustAnchorsRequest) (*TrustAnchorList, error)

	// ListTrustAnchors lists tenantID's trust anchors (the caller's own).
	ListTrustAnchors(ctx context.Context, tenantID string) (*TrustAnchorList, error)

	// RemoveTrustAnchor withdraws one of tenantID's trust anchors (the
	// caller's own) by fingerprint; it stops vouching for logins at once.
	RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error

	// TenantBarrierInit initializes a per-tenant barrier. A network transport
	// runs init/begin and init/complete on one connection, proving
	// possession of req.Signer's key (see TenantBarrierInitRequest.Signer).
	// Share material is never returned; for the Shamir strategy the response
	// counts the shares sealed to officers.
	TenantBarrierInit(ctx context.Context, req *TenantBarrierInitRequest) (*TenantBarrierInitResponse, error)

	// TenantBarrierUnseal unseals a per-tenant barrier using a single key.
	TenantBarrierUnseal(ctx context.Context, req *TenantBarrierUnsealRequest) error

	// TenantBarrierSeal seals a per-tenant barrier, returning it to the
	// sealed state and refusing further reads/writes until unsealed again.
	TenantBarrierSeal(ctx context.Context, req *TenantBarrierSealRequest) error

	// TenantBarrierStatus returns the current sealed/initialized state and
	// active strategy metadata for a per-tenant barrier.
	TenantBarrierStatus(ctx context.Context, req *TenantBarrierStatusRequest) (*TenantBarrierStatusResponse, error)

	// TenantBarrierOfficerRegister enrols a share holder for a tenant
	// barrier.
	TenantBarrierOfficerRegister(ctx context.Context, req *TenantOfficerRegisterRequest) (*OfficerRegisterResponse, error)

	// TenantBarrierOfficerList lists a tenant barrier's holders.
	TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*OfficerStatus, error)

	// TenantBarrierShareClaimBegin issues a claim nonce to a tenant holder.
	TenantBarrierShareClaimBegin(ctx context.Context, req *TenantShareClaimBeginRequest) (*ShareClaimBeginResponse, error)

	// TenantBarrierShareClaimComplete delivers a tenant holder's sealed
	// share envelope exactly once.
	TenantBarrierShareClaimComplete(ctx context.Context, req *TenantShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)

	// TenantBarrierQuorumBegin issues a tenant holder's quorum challenge.
	TenantBarrierQuorumBegin(ctx context.Context, req *TenantQuorumBeginRequest) (*QuorumChallenge, error)

	// TenantBarrierQuorumSubmit submits one authenticated tenant holder's
	// share toward the open round for op (unseal, seal or rekey).
	TenantBarrierQuorumSubmit(ctx context.Context, req *TenantQuorumSubmitRequest) (*QuorumOutcome, error)

	// TenantBarrierRekeyStart opens a rekey round on a tenant barrier.
	TenantBarrierRekeyStart(ctx context.Context, req *TenantRekeyStartRequest) (*QuorumRound, error)

	// TenantBarrierOfficerRevoke revokes a tenant holder through a
	// quorum-gated rekey.
	TenantBarrierOfficerRevoke(ctx context.Context, req *TenantOfficerRevokeRequest) (*QuorumRound, error)
}

// ---------------------------------------------------------------------------
// Composite client interface
// ---------------------------------------------------------------------------

// Client combines transport lifecycle with all typed service interfaces.
// All concrete transports implement this composite interface.
type Client interface {
	Transport
	KVClient
	AdminClient
	MVCCClient
	TransactionClient
	BatchClient
	WatchClient
	LeaseClient
	LockClient
	IndexClient
	TimeSeriesClient
	VectorClient
	UserClient
	RoleClient
	CertClient
	DEKClient
	BarrierClient
	DatabaseClient
	CeremonyClient
	CollectionClient
	CredentialClient
	BackupClient
	RebalanceClient
	SealStoreClient
	TenantClient
	VolumePoolClient
}
