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
)

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

	// AddMember adds a new replica to the specified shard.
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error

	// RemoveMember removes a replica from the specified shard.
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error

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
	ImportFile(ctx context.Context, filename string, data io.Reader, opts ImportOptions) (*ImportResult, error)
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
	// GenerateCert creates a client certificate signed by the internal CA.
	GenerateCert(ctx context.Context, req *GenerateCertRequest) (*CertBundle, error)

	// ExportCert exports a certificate and key as a PKCS#12 archive for
	// browser import.
	ExportCert(ctx context.Context, req *ExportCertRequest) (*ExportCertResponse, error)

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
// DEK operations
// ---------------------------------------------------------------------------

// DEKClient manages data encryption keys for the cluster and tenants.
type DEKClient interface {
	// DEKInitialize initializes the cluster DEK with the given algorithm and key size.
	DEKInitialize(ctx context.Context, req *DEKInitializeRequest) error

	// DEKGetStatus returns the current cluster DEK status.
	DEKGetStatus(ctx context.Context) (*DEKStatusResponse, error)

	// DEKRotate rotates the cluster DEK to a new version.
	DEKRotate(ctx context.Context) error

	// DEKGetCurrent returns the current DEK metadata.
	DEKGetCurrent(ctx context.Context) (*DEKCurrentResponse, error)

	// DEKSyncToNode synchronizes the DEK to a specific node.
	DEKSyncToNode(ctx context.Context, req *DEKSyncRequest) error

	// TenantDEKGetOrCreate returns the DEK for a tenant, creating if needed.
	TenantDEKGetOrCreate(ctx context.Context, req *TenantDEKRequest) (*TenantDEKResponse, error)

	// TenantDEKRotate rotates the DEK for a specific tenant.
	TenantDEKRotate(ctx context.Context, req *TenantDEKRotateRequest) error

	// TenantDEKDelete deletes a tenant's DEK.
	TenantDEKDelete(ctx context.Context, req *TenantDEKDeleteRequest) error

	// TenantDEKList returns all tenant IDs that have DEKs.
	TenantDEKList(ctx context.Context) (*TenantDEKListResponse, error)

	// TenantDEKGetStatus returns the status of a tenant's DEK.
	TenantDEKGetStatus(ctx context.Context, req *TenantDEKRequest) (*TenantDEKStatusResponse, error)
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

	// BarrierInitializeShamir initializes the barrier using Shamir secret
	// sharing. Share material is never returned; Security Officers claim
	// their individual shares via the OfficerRegister/ShareClaimBegin/
	// ShareClaimComplete flow.
	BarrierInitializeShamir(ctx context.Context, req *BarrierInitializeShamirRequest) (*BarrierInitializeShamirResponse, error)

	// BarrierUnsealWithShare submits a single Shamir share toward the
	// unseal threshold.
	BarrierUnsealWithShare(ctx context.Context, req *BarrierUnsealShareRequest) (*BarrierUnsealShareResponse, error)

	// BarrierUnsealWithShares submits multiple Shamir shares to unseal
	// the barrier in a single request.
	BarrierUnsealWithShares(ctx context.Context, req *BarrierUnsealSharesRequest) error

	// BarrierRekey generates new Shamir shares with the specified
	// threshold and total count, replacing the existing shares.
	BarrierRekey(ctx context.Context, req *BarrierRekeyRequest) (*BarrierRekeyResponse, error)

	// BarrierEnableEncryption migrates a previously-unencrypted barrier to a
	// keyed strategy. The migration runs asynchronously on the server (a
	// system-scope migration recycles the data-plane server in process); the
	// response acknowledges the start and clients poll BarrierStatus for the
	// strategy change. Supported on REST and gRPC; other transports return
	// NotImplementedError.
	BarrierEnableEncryption(ctx context.Context, req *BarrierEnableEncryptionRequest) (*BarrierEnableEncryptionResponse, error)

	// BarrierGenerateRecoveryKeys generates recovery keys for disaster
	// recovery scenarios.
	BarrierGenerateRecoveryKeys(ctx context.Context, req *BarrierGenerateRecoveryKeysRequest) (*BarrierRecoveryKeysResponse, error)

	// BarrierRecoverWithKeys unseals the barrier using recovery keys.
	BarrierRecoverWithKeys(ctx context.Context, req *BarrierRecoverWithKeysRequest) error

	// BarrierDeleteRecoveryKeys deletes all stored recovery keys.
	BarrierDeleteRecoveryKeys(ctx context.Context) error

	// BarrierHasRecoveryKeys checks whether recovery keys have been
	// generated.
	BarrierHasRecoveryKeys(ctx context.Context) (*BarrierHasRecoveryKeysResponse, error)

	// BarrierGenerateRootToken generates a new root token using the
	// provided Shamir shares.
	BarrierGenerateRootToken(ctx context.Context, req *BarrierGenerateRootTokenRequest) (*BarrierRootTokenResponse, error)

	// OfficerRegister registers a Security Officer for the system barrier.
	// The signed certificate in the response must be delivered to the officer
	// out of band; it is not stored server-side after this response.
	OfficerRegister(ctx context.Context, req *OfficerRegisterRequest) (*OfficerRegisterResponse, error)

	// OfficerList lists all registered Security Officers for the system barrier.
	OfficerList(ctx context.Context) ([]*OfficerStatus, error)

	// OfficerClaimStatus returns per-officer claim status (no share data).
	OfficerClaimStatus(ctx context.Context) ([]*OfficerStatus, error)

	// ShareClaimBegin issues a challenge nonce to the named Security Officer.
	// The officer must sign the nonce with their private key and call
	// ShareClaimComplete within the nonce TTL.
	ShareClaimBegin(ctx context.Context, req *ShareClaimBeginRequest) (*ShareClaimBeginResponse, error)

	// ShareClaimComplete verifies the officer's nonce signature and returns
	// the share bytes exactly once. The caller must wipe ShareBytes after use.
	ShareClaimComplete(ctx context.Context, req *ShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)

	// ShamirSealVote submits a Shamir share as a seal vote for a Shamir-
	// protected system barrier. When the threshold is met the barrier is
	// sealed automatically.
	ShamirSealVote(ctx context.Context, req *ShamirSealVoteRequest) (*ShamirSealVoteResponse, error)

	// RecoveryOfficerRegister registers a Security Officer for recovery key
	// distribution.
	RecoveryOfficerRegister(ctx context.Context, req *OfficerRegisterRequest) (*OfficerRegisterResponse, error)

	// RecoveryShareClaimBegin issues a challenge nonce for a recovery officer.
	RecoveryShareClaimBegin(ctx context.Context, req *ShareClaimBeginRequest) (*ShareClaimBeginResponse, error)

	// RecoveryShareClaimComplete verifies the recovery officer's nonce
	// signature and returns the recovery share bytes exactly once.
	RecoveryShareClaimComplete(ctx context.Context, req *ShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)
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

	// CeremonyClaimCertBegin starts a certificate claim challenge for
	// the named officer.
	CeremonyClaimCertBegin(ctx context.Context, req *ClaimCertBeginRequest) (*ClaimCertBeginResponse, error)

	// CeremonyClaimCertComplete completes a certificate claim by
	// submitting the signed nonce.
	CeremonyClaimCertComplete(ctx context.Context, req *ClaimCertCompleteRequest) (*ClaimCertCompleteResponse, error)

	// CeremonyBeginClaimShare starts the Shamir share claim handshake for an
	// officer who has already claimed their certificate. The caller must
	// present an mTLS client certificate whose thumbprint matches the
	// officer's enrolment record; the server derives it from the peer chain.
	CeremonyBeginClaimShare(ctx context.Context, req *BeginClaimShareRequest) (*BeginClaimShareResponse, error)

	// CeremonyCompleteClaimShare completes the Shamir share claim handshake
	// by submitting the signed challenge nonce. The share is released exactly
	// once.
	CeremonyCompleteClaimShare(ctx context.Context, req *CompleteClaimShareRequest) (*CompleteClaimShareResponse, error)

	// CeremonySignCSR submits a CSR for signing during or after the
	// ceremony.
	CeremonySignCSR(ctx context.Context, req *SignCSRInitRequest) (*SignCSRInitResponse, error)

	// CeremonyClaimShareBegin issues a factor challenge for an enrolled
	// custodian to claim their Shamir share.
	CeremonyClaimShareBegin(ctx context.Context, req *CeremonyClaimShareBeginRequest) (*CeremonyClaimShareBeginResponse, error)

	// CeremonyClaimShareComplete verifies the custodian's factor response and
	// returns the custodian metadata confirming share delivery.
	CeremonyClaimShareComplete(ctx context.Context, req *CeremonyClaimShareCompleteRequest) (*CeremonyCustodianInfo, error)

	// CeremonySubmitUnsealShare submits a custodian's plaintext Shamir share
	// (with factor proof) toward the unseal quorum.
	CeremonySubmitUnsealShare(ctx context.Context, req *CeremonySubmitUnsealShareRequest) (*CeremonySubmitUnsealShareResponse, error)

	// CeremonyEnrollExternalOfficer enrols an officer whose key material
	// lives outside go-qrdb. Either CertPEM (validated against the
	// configured CA pools) or PubKeyPEM (admin-asserted trust) MUST be
	// supplied, but not both.
	CeremonyEnrollExternalOfficer(ctx context.Context, req *EnrollExternalOfficerRequest) (*EnrollExternalOfficerResponse, error)
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

	// CredentialRetrieveVersion returns the stored credential value
	// for a specific version. Use version == 0 for the current version.
	CredentialRetrieveVersion(ctx context.Context, req *CredentialRetrieveVersionRequest) (*CredentialRetrieveVersionResponse, error)

	// CredentialListVersions returns the version history (current +
	// non-expired grace) for a credential. Values are never returned.
	CredentialListVersions(ctx context.Context, req *CredentialListVersionsRequest) (*CredentialListVersionsResponse, error)

	// CredentialGCExpired reaps historical versions whose grace
	// period has elapsed. Returns the count reaped.
	CredentialGCExpired(ctx context.Context, req *CredentialGCRequest) (*CredentialGCResponse, error)

	// CredentialStore persists a credential value under the given name.
	CredentialStore(ctx context.Context, name string, value []byte) error

	// CredentialRetrieve returns the credential value for the given name.
	// For the manual strategy this blocks until the credential is submitted
	// or the context is cancelled.
	CredentialRetrieve(ctx context.Context, name string) ([]byte, error)
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
// Custodian group operations
// ---------------------------------------------------------------------------

// CustodianGroupClient manages custodian groups for Shamir secret sharing.
type CustodianGroupClient interface {
	// CreateCustodianGroup creates a new custodian group.
	CreateCustodianGroup(ctx context.Context, req *CreateCustodianGroupRequest) (*CreateCustodianGroupResponse, error)

	// GetCustodianGroup retrieves a custodian group by ID.
	GetCustodianGroup(ctx context.Context, groupID string) (*GetCustodianGroupResponse, error)

	// ListCustodianGroups lists all custodian groups.
	ListCustodianGroups(ctx context.Context) (*ListCustodianGroupsResponse, error)

	// DeleteCustodianGroup deletes a custodian group.
	DeleteCustodianGroup(ctx context.Context, groupID string) error

	// AddCustodianMember adds a member to a custodian group.
	AddCustodianMember(ctx context.Context, req *AddCustodianMemberRequest) (*AddCustodianMemberResponse, error)

	// RemoveCustodianMember removes a member from a custodian group.
	RemoveCustodianMember(ctx context.Context, req *RemoveCustodianMemberRequest) error

	// DistributeShares distributes Shamir shares to custodian group members.
	DistributeShares(ctx context.Context, req *DistributeSharesRequest) (*DistributeSharesResponse, error)
}

// ---------------------------------------------------------------------------
// Share operations
// ---------------------------------------------------------------------------

// ShareClient manages Shamir share submission and status tracking.
type ShareClient interface {
	// SubmitShare submits a received Shamir share back to the server.
	SubmitShare(ctx context.Context, req *SubmitShareRequest) (*SubmitShareResponse, error)

	// ListShares lists shares available for the authenticated user.
	ListShares(ctx context.Context) (*ListSharesResponse, error)

	// GetShare retrieves a single submitted share identified by its
	// server URL, group ID, and share index.
	GetShare(ctx context.Context, serverURL, groupID string, shareIndex int) (*ShareInfo, error)

	// DeleteShare removes a single submitted share identified by its
	// server URL, group ID, and share index.
	DeleteShare(ctx context.Context, serverURL, groupID string, shareIndex int) error

	// GetShareCollectionStatus returns the collection status for a group.
	GetShareCollectionStatus(ctx context.Context, groupID string) (*ShareCollectionStatus, error)
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

	// DeleteTenant deletes a tenant.
	DeleteTenant(ctx context.Context, tenantID string) error

	// UpdateTenant applies a partial update to a tenant.
	UpdateTenant(ctx context.Context, req *UpdateTenantRequest) (*UpdateTenantResponse, error)

	// UpdateTenantQuotas sets the resource quotas for a tenant.
	UpdateTenantQuotas(ctx context.Context, req *UpdateTenantQuotasRequest) (*UpdateTenantQuotasResponse, error)

	// UpdateTenantEntitlements sets the feature entitlements for a tenant.
	UpdateTenantEntitlements(ctx context.Context, req *UpdateTenantEntitlementsRequest) (*UpdateTenantEntitlementsResponse, error)

	// TenantBarrierInit initializes a per-tenant barrier. For the Shamir
	// strategy the response carries the generated M-of-N shares; for
	// none/passphrase the share fields are empty.
	TenantBarrierInit(ctx context.Context, req *TenantBarrierInitRequest) (*TenantBarrierInitResponse, error)

	// TenantBarrierUnseal unseals a per-tenant barrier using a single key.
	TenantBarrierUnseal(ctx context.Context, req *TenantBarrierUnsealRequest) error

	// TenantBarrierUnsealShare submits a single Shamir share toward a
	// per-tenant barrier's quorum threshold, automatically unsealing once
	// enough distinct shares have been submitted.
	TenantBarrierUnsealShare(ctx context.Context, req *TenantBarrierUnsealShareRequest) (*TenantBarrierUnsealProgress, error)

	// TenantBarrierSeal seals a per-tenant barrier, returning it to the
	// sealed state and refusing further reads/writes until unsealed again.
	TenantBarrierSeal(ctx context.Context, req *TenantBarrierSealRequest) error

	// TenantBarrierStatus returns the current sealed/initialized state and
	// active strategy metadata for a per-tenant barrier.
	TenantBarrierStatus(ctx context.Context, req *TenantBarrierStatusRequest) (*TenantBarrierStatusResponse, error)

	// TenantBarrierOfficerRegister registers a Security Officer for a
	// specific tenant's barrier.
	TenantBarrierOfficerRegister(ctx context.Context, req *TenantOfficerRegisterRequest) (*OfficerRegisterResponse, error)

	// TenantBarrierOfficerList lists all registered Security Officers for
	// a specific tenant's barrier.
	TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*OfficerStatus, error)

	// TenantBarrierOfficerClaimStatus returns per-officer claim status for
	// a specific tenant's barrier (no share data).
	TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*OfficerStatus, error)

	// TenantBarrierShareClaimBegin issues a challenge nonce for a tenant
	// barrier Security Officer.
	TenantBarrierShareClaimBegin(ctx context.Context, req *TenantShareClaimBeginRequest) (*ShareClaimBeginResponse, error)

	// TenantBarrierShareClaimComplete verifies the tenant officer's nonce
	// signature and returns the share bytes exactly once. The caller must
	// wipe ShareBytes after use.
	TenantBarrierShareClaimComplete(ctx context.Context, req *TenantShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)

	// TenantBarrierShamirSealVote submits a Shamir share as a seal vote for
	// a Shamir-protected tenant barrier.
	TenantBarrierShamirSealVote(ctx context.Context, req *TenantShamirSealVoteRequest) (*ShamirSealVoteResponse, error)
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
	CustodianGroupClient
	ShareClient
	TenantClient
}
