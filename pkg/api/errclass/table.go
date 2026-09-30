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

package errclass

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/dolthub/vitess/go/mysql"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// dominantRules win however deeply their condition is wrapped.
var dominantRules = []rule{
	// A transport's own refusal declares its class beside its type.
	classedRule,

	// Authentication and authorization refusals, through the one classifier
	// that owns them. First, because *audit.GateError wraps the audit sink's
	// own failure, which this package does not control: any row below could
	// match that inner value by coincidence and report an unrecorded decision
	// as a credential or store problem.
	authRule,

	// The node could not confirm a tenant is empty before an isolation
	// change, so the tenant service refused it rather than risk orphaning the
	// tenant's data. The precondition is unverified whatever the probe met on
	// the way -- a missing store, a lost leader, a read fault -- so the
	// refusal wins over the cause it wraps.
	as(&tenant.TenantDataProbeError{TenantID: "acme", Err: tenant.ErrTenantNotFound}, Unavailable),

	// The identity a membership add needs could not be settled. Its refusal
	// kind decides the class, not whatever the membership read met on the way
	// (a lost leader, a missing record), so it wins over the cause it wraps.
	memberIdentityRule,

	// A relational table written under a key layout that predates table
	// generations and index incarnations (v0.20.0 through v0.21.0) is refused
	// until it is dropped and re-created (go-qrdb#547): a precondition only the
	// operator can restore, not a fault. Dominant because it reaches a
	// transport inside SQLExecutionError and ReadError, which would otherwise
	// claim it.
	tableLayoutUnsupportedRule,

	// A statement that outlived the table resolution it planned against -- the
	// table truncated, or its definition changed, underneath it (go-qrdb#547).
	// Re-issuing it re-resolves the table, so it is a lost race, not a fault.
	// Dominant for the same reason as the layout refusal.
	tableResolutionStaleRule,

	// A row-lock request that lost: a lock-wait timeout, NOWAIT on a held
	// row, a deadlock victim, a lost lock -- typed, or in the SQL engine's
	// numbered rendering. Each is a lost race the client retries, never a
	// fault. Dominant for the same reason as the two rules above: the
	// refusal reaches a transport inside SQLExecutionError.
	rowLockRule,

	// SKIP LOCKED, refused by name.
	rowLockUnsupportedRule,

	// A closed store is the node going away underneath the request (go-qrdb#371).
	// It arrives wrapped in ProposeError, ReadError, BatchError, TimeSeriesError
	// — every one of which would otherwise claim it as a server fault.
	is(storage.ErrStorageClosed, Unavailable),

	// A barrier-isolated tenant's IAM store resolved to a SEALED barrier; the
	// resolver fails closed rather than falling back to the shared store.
	is(tenant.ErrTenantSealed, Unavailable),
	// The same seal met one step later: the barrier sealed AFTER Resolve
	// returned a store and BEFORE that store's own Get/Put ran, which reports
	// the barrier's sentinel unwrapped (go-qrdb#464 round 6).
	is(seal.ErrSealed, Unavailable),

	// A system-scope encryption migration is rebuilding the data plane; the
	// service is back once it finishes. It can arrive wrapped in whatever
	// data-plane call met it, so it wins through wrappers as a closed store
	// does.
	as(&service.DataPlaneRecyclingError{}, Unavailable),

	// The node is closing: an Unseal or a migration refused rather than queued
	// behind Close. Retryable against another node, like a closed store, and
	// for the same reason it wins through whatever wrapper carried it.
	is(service.ErrNodeClosed, Unavailable),

	// A normal tenant caller named a tenant other than its own.
	as(&tenant.CrossTenantDeniedError{}, PermissionDenied),

	// A login-index rollback FAILED. It unwraps to the conflict that caused
	// it, so it must be matched before every row that could be its cause:
	// reporting the cause would drop the fact that secondary state may now be
	// inconsistent.
	as(&user.IndexCompensationError{}, DataLoss),

	// A user was read but its enrolled auth factors could not be listed, so
	// the response cannot say whether it has a second factor. Retryable,
	// whatever store failure the listing met: that cause is not the answer
	// the caller asked for.
	as(&user.FactorLookupError{}, Unavailable),

	// A collection document write whose outcome is unknown -- the write
	// failed and may have committed, its post-write fence could not read the
	// metadata back, or a fenced-out write could not be retracted. The
	// document may be stored, so this is a server fault the caller must not
	// read as the lifecycle refusal a failed retraction wraps.
	collectionOutcomeUnknownRule,
}

// dataPlaneRules are the key-value, lease, index, watch and transaction
// conditions.
var dataPlaneRules = []rule{
	// The node was not built with this verb on its state machine
	// (go-qrdb#141): a healthy node declining, not a fault.
	as(&service.CapabilityUnsupportedError{}, Unimplemented),
	// The rest of the refusals a state machine encodes instead of failing the
	// apply, one type per protocol.AppErrorKind (go-qrdb#557 finished the
	// #141 split: an absent subsystem is the capability refusal above, and
	// these are what remains). Before, they shared one catch-all type that no
	// single class described — REST answered 501 for all of them, true only of
	// the absent subsystem, and gRPC and QUIC answered Internal, true of none.
	as(&service.CommandRejectedError{}, InvalidArgument),
	as(&service.CommandConflictError{}, AlreadyExists),
	as(&service.SubsystemClosedError{}, Unavailable),
	as(&service.CommandCorruptError{}, DataLoss),
	as(&service.KeyNotFoundError{}, NotFound),
	as(&service.LeaseNotFoundError{}, NotFound),
	// Expired and absent are deliberately indistinguishable on the wire
	// (go-qrdb#387); the SDK tells them apart at the call site.
	as(&service.LeaseExpiredError{}, NotFound),
	as(&service.LeaseExistsError{}, AlreadyExists),
	as(&service.NotLeaderError{}, Unavailable),
	// A request the protocol cannot encode was never proposed (go-qrdb#52).
	as(&service.EncodeRequestError{}, InvalidArgument),
	// A guard that could not be built or applied to a command -- a compare
	// that cannot be encoded, a target no state machine can evaluate, a
	// command already guarded -- is raised before anything is proposed, the
	// same shape as EncodeRequestError above (go-qrdb#482).
	as(&service.WriteGuardError{}, InvalidArgument),
	// A write's guard did not hold when the state machine applied it: a lost
	// concurrency race, the same shape TransactionConflictError answers below.
	// A refused drop fence decodes as *database.DroppingError instead (see
	// databaseRules); this is every other guard (go-qrdb#482).
	as(&service.WriteGuardRefusedError{}, Aborted),
	as(&service.DrainError{}, Unavailable),
	as(&service.RevisionCompactedError{}, OutOfRange),
	as(&service.RevisionNotFoundError{}, NotFound),
	as(&service.TransactionConflictError{}, Aborted),
	as(&service.UnsupportedTransactionOpError{}, InvalidArgument),
	as(&service.WatchCanceledError{}, Canceled),
	as(&service.IndexNotFoundError{}, NotFound),
	as(&service.IndexExistsError{}, AlreadyExists),
	as(&service.ConsistencyError{}, PreconditionFailed),
	is(watch.ErrCompacted, OutOfRange),
	// A replay requested of a store that keeps no change history.
	is(watch.ErrStartRevisionUnsupported, Unimplemented),
}

// vectorRules are the vector-service conditions (go-qrdb#331).
var vectorRules = []rule{
	as(&service.VectorNotFoundError{}, NotFound),
	as(&service.NamespaceNotFoundError{}, NotFound),
	as(&service.NamespaceExistsError{}, AlreadyExists),
	as(&service.DimensionMismatchError{}, InvalidArgument),
	as(&service.InvalidVectorError{}, InvalidArgument),
	as(&service.InvalidNamespaceNameError{}, InvalidArgument),
	as(&service.VectorJobNotFoundError{}, NotFound),
	as(&service.VectorServiceClosedError{}, Unavailable),
	// Strict tenant isolation refuses a request bound to no tenant.
	as(&service.TenantRequiredError{}, PermissionDenied),
	as(&service.RAGTenantRequiredError{}, PermissionDenied),
}

// collectionRules are the document-collection conditions.
var collectionRules = []rule{
	as(&service.CollectionAlreadyExistsError{}, AlreadyExists),
	as(&service.CollectionNotFoundError{}, NotFound),
	as(&service.CollectionEmptyNameError{}, InvalidArgument),
	// A new collection name containing '#', which separates a name from its
	// incarnation in every per-incarnation key.
	as(&service.CollectionInvalidNameError{}, InvalidArgument),
	// A document field named "id", or a lone object-valued "fields": the
	// stored layout would read either back as something else.
	as(&service.CollectionReservedFieldError{}, InvalidArgument),
	as(&service.CollectionEmptyDatabaseError{}, InvalidArgument),
	as(&service.CollectionInvalidIDStrategyError{}, InvalidArgument),
	as(&service.CollectionMissingFieldHashError{}, InvalidArgument),
	as(&service.CollectionDocumentNotFoundError{}, NotFound),
	// The mirror of the not-found above: an insert refused because the
	// document it would have written over is already stored.
	as(&service.CollectionDocumentExistsError{}, AlreadyExists),
	as(&service.CollectionEmptyDocumentError{}, InvalidArgument),
	as(&service.CollectionInvalidDocumentIDError{}, InvalidArgument),
	// Only an active collection serves document verbs; a creating or dropping
	// one refuses them until its transition settles.
	as(&service.CollectionNotActiveError{}, FailedPrecondition),
	// A lifecycle transition or a guarded document write lost a race:
	// re-read and retry.
	as(&service.CollectionStateConflictError{}, Aborted),
	as(&service.CollectionDocumentConflictError{}, Aborted),
	as(&service.CollectionInvalidIndexError{}, InvalidArgument),
	// An index held by another owner: a create cannot take the name, and the
	// explicit API's drop may not remove a collection's index. Either way the
	// precondition -- the caller owns the name -- does not hold.
	as(&index.OwnershipError{}, FailedPrecondition),
}

// importRules are the bulk-import guards.
var importRules = []rule{
	as(&service.ImportInvalidFormatError{}, InvalidArgument),
	as(&service.ImportInvalidValueModeError{}, InvalidArgument),
	as(&service.ImportMissingKeyFieldError{}, InvalidArgument),
	as(&service.ImportMissingValueFieldError{}, InvalidArgument),
	as(&service.ImportParseError{}, InvalidArgument),
	as(&service.ImportArchiveError{}, InvalidArgument),
	as(&service.ImportTransactionAbortedError{}, Aborted),
	as(&service.ImportInputTooLargeError{}, TooLarge),
	as(&service.ImportArchiveExpandedTooLargeError{}, TooLarge),
	as(&service.ImportTooManyEntriesError{}, InvalidArgument),
	as(&service.ImportTooManyRowsError{}, InvalidArgument),
	as(&service.ImportProposalTooLargeError{}, TooLarge),
}

// restoreRules are the MVCC compaction and selective point-in-time restore
// conditions.
var restoreRules = []rule{
	// The shard keeps no MVCC revision history (raft.sm_type is not "mvcc").
	as(&service.MVCCBackendRequiredError{}, Unimplemented),
	// A prefix-scoped restore needs the durable revision index.
	as(&service.RestoreChangeIndexUnavailableError{}, Unimplemented),
	// An authority the caller does not have, not a malformed request.
	as(&service.RestoreInternalKeyError{}, PermissionDenied),
	// Compaction is a database's own: the raw shard has no keyspace to
	// compact, and a feature the group has not enabled is not usable here
	// yet. Neither is the caller's request to fix.
	as(&service.CompactScopeRequiredError{}, Unprocessable),
	as(&service.CompactPrefixInvalidError{}, InvalidArgument),
	as(&service.CompactRevisionInvalidError{}, InvalidArgument),
	as(&service.CompactRevisionFutureError{}, InvalidArgument),
	as(&service.RestoreNilRequestError{}, InvalidArgument),
	as(&service.RestoreRevisionInvalidError{}, InvalidArgument),
	as(&service.RestoreRevisionFutureError{}, InvalidArgument),
	as(&service.RestoreScopeEmptyError{}, InvalidArgument),
	as(&service.RestoreScopeAmbiguousError{}, InvalidArgument),
	as(&service.RestoreEmptyKeyError{}, InvalidArgument),
	as(&service.RestoreScopeTooLargeError{}, InvalidArgument),
	as(&service.RestoreUnboundedPrefixError{}, InvalidArgument),
}

// databaseRules are the per-(tenant, database) resolution and database
// lifecycle conditions.
var databaseRules = []rule{
	// FIRST in its block: it wraps the resolver's own error (often a missing
	// tenant), and what the caller asked about is the drop, which cannot know
	// whether it reclaimed anything — retry once the store resolves.
	as(&service.ReclaimStoreUnavailableError{}, Unavailable),
	as(&service.TenantNotFoundError{}, NotFound),
	as(&service.DatabaseNotFoundError{}, NotFound),
	// A data-plane operation met a sealed barrier; retry after unseal.
	as(&service.BarrierSealedError{}, Unavailable),
	// The isolation backend a tenant requires is not wired: fail closed
	// rather than serve the tenant from the shared store.
	as(&service.IsolationBackendUnavailableError{}, Unavailable),
	as(&database.AlreadyExistsError{}, AlreadyExists),
	as(&database.NotFoundError{}, NotFound),
	as(&database.InvalidNameError{}, InvalidArgument),
	as(&database.ReservedNameError{}, InvalidArgument),
	// etcd enabled on a database whose engine is not kv.
	as(&database.EtcdEngineUnsupportedError{}, InvalidArgument),
	as(&database.InvalidEngineError{}, InvalidArgument),
	as(&database.InvalidBackendError{}, InvalidArgument),
	// A per-database barrier requested where it would not wrap the store the
	// database is served from. The caller cannot change a database's backend
	// or data_dir, so this is the configuration refusing the feature, as
	// tenant.ErrBarrierIsolationUnavailable is -- not a precondition the
	// caller can restore.
	as(&database.SharedStoreBarrierUnsupportedError{}, Unprocessable),
	as(&database.BarrierOffDataPathError{}, Unprocessable),
	as(&database.InvalidConcurrencyError{}, InvalidArgument),
	// A database configuration that fails validation is the caller's error,
	// not the server's: each of these used to reach REST as a 500.
	as(&database.InvalidModeError{Mode: "x"}, InvalidArgument),
	as(&database.ConnectionRequiredError{}, InvalidArgument),
	as(&database.InvalidRaftClusterModeError{Mode: "x"}, InvalidArgument),
	as(&database.InvalidDiscoveryMethodError{Method: "x"}, InvalidArgument),
	as(&database.DiscoveryConfigRequiredError{Method: "multicast"}, InvalidArgument),
	as(&database.DiscoverySharedSecretRequiredError{Method: "multicast"}, InvalidArgument),
	// A Raft update that re-points a discovery block without resending its
	// secret: the stored secret stays with its destination.
	as(&database.DiscoverySecretRebindError{Method: "token", Field: "endpoint"}, InvalidArgument),
	as(&database.DiscoveryDomainRequiredError{}, InvalidArgument),
	as(&database.DiscoveryEndpointRequiredError{}, InvalidArgument),
	as(&database.DiscoveryTokenRequiredError{}, InvalidArgument),
	as(&database.DiscoveryClusterSizeError{Size: 0}, InvalidArgument),
	as(&database.EtcdListenAddrRequiredError{}, InvalidArgument),
	as(&database.InvalidStorageCodecError{Codec: "x"}, InvalidArgument),
	// A per-database barrier mode this node cannot build (Shamir, until
	// per-database share holders ship): the request's choice, not a fault.
	as(&database.UnsupportedBarrierStrategyError{Mode: "shamir"}, InvalidArgument),
	as(&compaction.InvalidPolicyError{}, InvalidArgument),
	// A compaction policy on a store that keeps no MVCC history, or on a
	// node composed without the node keyspace's compaction table: the
	// configuration refusing the feature, which the caller cannot change.
	as(&service.CompactionPolicyUnavailableError{}, Unprocessable),
	as(&service.NodeCompactionUnavailableError{}, Unprocessable),
	// A per-database barrier operation on a node with no barrier manager.
	as(&service.BarrierManagerNotConfiguredError{}, Unimplemented),
	// A database mid-drop is not gone (NotFoundError, above) and not a naming
	// conflict (AlreadyExistsError) -- it is a resource whose current state
	// conflicts with the request. The caller can retry once the drop finishes
	// (the name is free) or once a failed drop restores the database
	// (go-qrdb#482).
	as(&database.DroppingError{}, Aborted),
	// A second DROP for the same database while one is already running on
	// this node (Manager.Drop's own in-flight guard): the identical shape as
	// DroppingError above -- a well-formed, retryable request whose target is
	// momentarily busy, not a client error or a server fault (go-qrdb#482).
	as(&database.DropInProgressError{}, Aborted),
	// The default database can never be dropped, whatever state it is in: the
	// request itself is invalid, not in conflict with anything.
	as(&database.DefaultDatabaseDropError{}, InvalidArgument),
	// Shard IDs are issued by the cluster; a request that names one is
	// refused, never validated.
	as(&database.CallerShardIDError{Name: "db", ShardID: 1}, InvalidArgument),
	as(&database.ProvisionRaftError{Name: "db"}, InvalidArgument),
	// A raft-backed database's Raft configuration carries its shard ID; an
	// update may change it but not remove it.
	as(&database.RaftConfigRequiredError{Name: "db"}, InvalidArgument),
	// An admin start in the user range the cluster issues to databases.
	as(&service.UserRangeShardError{ShardID: 1000}, InvalidArgument),
	// A raft create on a node with no shard ID issuer: a composition fault.
	as(&database.ShardIssuerRequiredError{}, Unimplemented),
	// FIRST of the claim rows: a corrupt high-water mark is a server fault,
	// and it reaches the caller wrapped in ShardIDClaimError, which would
	// otherwise answer it as retryable.
	as(&shard.HighWaterCorruptError{Value: "x"}, Internal),
	// No shard ID remains to issue. Nothing a retry can change: the space is
	// spent for the life of the cluster.
	as(&shard.ShardIDExhaustedError{}, ResourceExhausted),
	// An operation that needs a cluster feature the cluster has not yet
	// activated -- a raft create before cluster-wide shard IDs, a compaction
	// or compaction-state command before prefix-compaction or
	// node-compaction. Nothing was done, and a retry succeeds once every node
	// runs a binary that implements the feature: transient, so Unavailable.
	as(&featuregate.NotActiveError{Feature: featuregate.ClusterShardIDs}, Unavailable),
	// A claim that lost the race on every attempt, or whose store did not
	// answer: nothing was created, and a retry can succeed.
	as(&shard.ClaimContentionError{Attempts: 1}, Unavailable),
	as(&database.ShardIDClaimError{Err: context.DeadlineExceeded}, Unavailable),
}

// placementRules are the quota, placement and raft-topology conditions.
var placementRules = []rule{
	as(&quota.ExceededError{}, ResourceExhausted),
	as(&quota.EntitlementError{}, PermissionDenied),
	// A tenant-wide replica reservation lost a race: the quota family.
	as(&tenant.QuotaExceededError{}, ResourceExhausted),
	// Within quota, but the cluster has no free node: retryable once capacity
	// changes, which a quota refusal is not.
	as(&placement.InsufficientNodesError{}, Unavailable),
	as(&service.ReplicaFloorError{}, InvalidArgument),
	as(&service.NotDedicatedRaftDatabaseError{}, InvalidArgument),
	// The dedicated shard's leader was not stable within the wait bound.
	as(&service.ShardLeaderTimeoutError{}, Unavailable),
	// A leadership transfer ahead of a replica removal did not finish within
	// its bound: the removal is retryable, nothing was changed.
	as(&service.LeaderTransferTimeoutError{}, Unavailable),
	as(&service.InvalidRaftTopologyError{}, InvalidArgument),
	// Distributed topology requested and placement is not wired: fail closed
	// rather than collapse to one local replica.
	as(&service.RaftDistributedUnavailableError{}, Unavailable),
	as(&tenant.InvalidTenantIDCharError{}, InvalidArgument),
	// A replica refused on a node that does not advertise a feature the group
	// uses: the request is well formed and conflicts with the node's release,
	// which the operator resolves by upgrading it (go-qrdb#482).
	as(&featuregate.UnsupportedReplicaError{}, FailedPrecondition),
}

// lockRules are the distributed-lock conditions.
var lockRules = []rule{
	as(&lock.LockHeldError{}, Aborted),
	as(&lock.NotLockHolderError{}, PermissionDenied),
	as(&lock.LockNotFoundError{}, NotFound),
}

// sqlRules are the SQL-over-API conditions: validation and engine failures
// are the query's, not the server's.
var sqlRules = []rule{
	as(&service.SQLEmptyQueryError{}, InvalidArgument),
	as(&service.SQLEmptyDatabaseError{}, InvalidArgument),
	as(&service.SQLExecutionError{}, InvalidArgument),
}

// catalogRules are the relational table, view and sequence catalog
// conditions the SQL surfaces and the table service share.
var catalogRules = []rule{
	as(&service.TableEmptyDatabaseError{}, InvalidArgument),
	as(&service.TableInvalidNameError{}, InvalidArgument),
	as(&service.TableNoColumnsError{}, InvalidArgument),
	as(&service.TableDuplicateColumnError{}, InvalidArgument),
	as(&service.TableMultipleAutoIncrementError{}, InvalidArgument),
	// A clause naming a column the same definition does not declare: the
	// definition is malformed, not a lookup that missed.
	as(&service.TablePrimaryKeyColumnNotFoundError{}, InvalidArgument),
	as(&service.TableUnsupportedColumnTypeError{}, InvalidArgument),
	as(&service.TableIndexNoColumnsError{}, InvalidArgument),
	as(&service.TableIndexColumnNotFoundError{}, InvalidArgument),
	as(&service.TableForeignKeyNoColumnsError{}, InvalidArgument),
	as(&service.TableForeignKeyColumnNotFoundError{}, InvalidArgument),
	as(&service.TableNoChangesError{}, InvalidArgument),
	as(&service.TableUnsupportedAlterActionError{}, InvalidArgument),
	as(&service.TableAlreadyExistsError{}, AlreadyExists),
	as(&service.TableIndexAlreadyExistsError{}, AlreadyExists),
	as(&service.TableForeignKeyAlreadyExistsError{}, AlreadyExists),
	as(&service.TableCheckAlreadyExistsError{}, AlreadyExists),
	as(&service.TableNotFoundError{}, NotFound),
	// An ALTER naming an existing column that is not there: a lookup that
	// missed.
	as(&service.TableColumnNotFoundError{}, NotFound),
	as(&service.TableIndexNotFoundError{}, NotFound),
	as(&service.TableForeignKeyNotFoundError{}, NotFound),
	as(&service.TableCheckNotFoundError{}, NotFound),
	// The caller observed an older revision and must re-read: a lost
	// optimistic-concurrency check.
	as(&service.TableStaleRevisionError{}, Aborted),
	// Lost the catalog compare-and-swap; it wraps TransactionConflictError,
	// which this row reports with the table context attached.
	as(&service.TableDDLConflictError{}, Aborted),
	// The ALTER is journaled and needs its row rewrite before the table can
	// be served; the caller that owns the rewrite resumes it.
	as(&service.TableRewriteRequiredError{}, FailedPrecondition),
	// The table's rows are mid-rewrite, in a mixed encoding; retry once the
	// rewrite completes.
	as(&service.TableRewriteInProgressError{}, Unavailable),
	as(&service.TableNoRewriteInProgressError{}, FailedPrecondition),
	// A journal this service wrote always carries its target: its absence is
	// catalog corruption, not a race.
	as(&service.TableRewriteTargetMissingError{}, DataLoss),
	as(&service.ViewAlreadyExistsError{}, AlreadyExists),
	as(&service.ViewNotFoundError{}, NotFound),
	as(&service.ViewDefinitionEmptyError{}, InvalidArgument),
	as(&service.SequenceEmptyDatabaseError{}, InvalidArgument),
	as(&service.SequenceEmptyNameError{}, InvalidArgument),
	as(&service.SequenceInvalidCountError{}, InvalidArgument),
	as(&service.SequenceConflictError{}, Aborted),
}

// entityRules are the typed-entity and paging request guards.
var entityRules = []rule{
	as(&service.EntityEmptyDataError{}, InvalidArgument),
	as(&service.EntityEmptyIDError{}, InvalidArgument),
	as(&service.EntityEmptyTypeError{}, InvalidArgument),
	as(&service.InvalidEntityOpTypeError{}, InvalidArgument),
	as(&service.BatchEntityEmptyOpsError{}, InvalidArgument),
	as(&service.InvalidPageTokenError{}, InvalidArgument),
	as(&service.CollectionInvalidCodecError{}, InvalidArgument),
	// A state machine built without the subsystem a read needs: the node's
	// capability, not the request.
	as(&service.SubsystemNotConfiguredError{}, Unimplemented),
}

// privilegeRules are the SQL privilege-catalog conditions.
var privilegeRules = []rule{
	as(&service.PrivilegeEmptyDatabaseError{}, InvalidArgument),
	as(&service.PrivilegeInvalidGranteeError{}, InvalidArgument),
	as(&service.PrivilegeNoGrantsError{}, InvalidArgument),
	as(&service.PrivilegeColumnWithoutTableError{}, InvalidArgument),
	// A stored record naming a privilege this build cannot represent must not
	// be read as a different set of privileges: persisted state it cannot
	// trust.
	as(&service.PrivilegeUnknownNameError{}, DataLoss),
	as(&service.PrivilegeConflictError{}, Aborted),
}

// ragRules are the retrieval-augmented-generation conditions.
var ragRules = []rule{
	as(&service.RAGDocumentNotFoundError{}, NotFound),
	as(&service.RAGServiceClosedError{}, Unavailable),
}

// embeddingRules are the embedding-provider conditions.
var embeddingRules = []rule{
	// FIRST in its block: a registration failure wraps its cause (often a
	// not-found), and the refusal to register is what the caller asked about.
	as(&service.EmbeddingProviderRegistrationError{}, AlreadyExists),
	as(&service.EmbeddingProviderNotFoundError{}, NotFound),
	as(&service.EmbeddingBatchTooLargeError{}, InvalidArgument),
	as(&service.EmbeddingDimensionMismatchError{}, InvalidArgument),
	as(&service.EmbeddingServiceClosedError{}, Unavailable),
	as(&service.EmbeddingProviderAlreadyRegisteredError{}, AlreadyExists),
}

// backupRules are the backup and online-restore conditions.
var backupRules = []rule{
	as(&service.BackupNotFoundError{}, NotFound),
	// A shard snapshot export found no exportable snapshot within its poll
	// bound behind a concurrent request: retryable, nothing was exported.
	as(&service.ShardSnapshotTimeoutError{}, Unavailable),
	as(&service.BackupEmptyDestinationError{}, InvalidArgument),
	as(&service.BackupEmptyIDError{}, InvalidArgument),
	as(&service.BackupEncryptionKeyRequiredError{}, InvalidArgument),
	as(&service.BackupDecryptionKeyRequiredError{}, InvalidArgument),
	as(&service.BackupInvalidCompressionError{}, InvalidArgument),
	as(&service.BackupInvalidEncryptionError{}, InvalidArgument),
	// The artifact is not in a restorable state; retrying the same restore
	// will not change that.
	as(&service.BackupChecksumMismatchError{}, FailedPrecondition),
	as(&service.OnlineRestoreSystemShardError{}, FailedPrecondition),
	as(&service.OnlineRestoreReplicatedShardError{}, FailedPrecondition),
	as(&service.BackupUnsupportedError{}, Unimplemented),
	as(&service.OnlineRestoreUnsupportedError{}, Unimplemented),
}

// identityRules are the user, role and certificate conditions.
var identityRules = []rule{
	// A targeted request whose body names the tenant it acts on could not be
	// read: refused rather than authorized against the caller's own tenant.
	as(&authmw.TargetBodyInvalidError{}, InvalidArgument),
	as(&authmw.TargetBodyTooLargeError{}, TooLarge),
	as(&user.UserNotFoundError{}, NotFound),
	// A withdrawal naming a binding the account does not hold.
	as(&user.CertBindingNotFoundError{}, NotFound),
	as(&user.UsernameExistsError{}, AlreadyExists),
	// A store-generated user id collided with an existing identity claim or
	// primary record (go-qrdb#555).
	as(&user.UserIDExistsError{}, AlreadyExists),
	as(&user.EmailExistsError{}, AlreadyExists),
	// A second System user: at most one exists, the bootstrap admin
	// (go-qrdb#106).
	as(&user.SystemUserAlreadyExistsError{}, AlreadyExists),
	// The System flag asked for outside the system tenant: a bad field value.
	as(&user.SystemTenantRequiredError{}, InvalidArgument),
	// A RoleBinding naming another tenant on a non-system user, which could
	// never grant: a bad field value in the write.
	as(&user.RoleBindingTenantError{}, InvalidArgument),
	// A thumbprint already bound to ANOTHER user: rebinding it silently would
	// hand the caller that user's mTLS identity (go-qrdb#536).
	as(&user.CertBindingConflictError{}, AlreadyExists),
	// A change that would lock the bootstrap admin out under
	// auth.mtls_binding_policy "required": deleting, renaming, disabling or
	// suspending it, dropping mtls from its methods, or leaving it with no live
	// binding. The request is well formed; the account's state forbids it.
	as(&user.BootstrapAdminLockoutError{}, FailedPrecondition),
	// An enrolment whose expiry is already past: a bad field value.
	as(&user.CertBindingExpiredError{}, InvalidArgument),
	// A lost optimistic-concurrency check: re-read and retry.
	as(&user.ConcurrentUpdateError{}, Aborted),
	// The commit may have landed: reconcile, do not assume failure.
	as(&user.CommitOutcomeUnknownError{}, Unavailable),
	as(&user.UserDisabledError{}, FailedPrecondition),
	// No caller may alter a system account: the documented answer is 403.
	as(&user.SystemUserImmutableError{}, PermissionDenied),
	as(&user.InvalidLifecycleError{}, InvalidArgument),
	// The state is valid; the record is not somewhere this move starts from.
	as(&user.LifecycleTransitionError{}, FailedPrecondition),
	as(&user.ValidationError{}, InvalidArgument),
	// A role-assignment write named a role that does not exist, or one that
	// may not be granted in the tenant it targets: a bad field in the write,
	// whose subject is a user, not the role. ReferenceError wraps the lookup's
	// RoleNotFoundError, so it must precede that row.
	as(&role.ReferenceError{}, InvalidArgument),
	as(&role.NotAssignableError{}, InvalidArgument),
	// A role-assignment write named a role and the node has no role service
	// to check it against: the node cannot serve the write as composed.
	as(&role.ServiceRequiredError{}, Unimplemented),
	as(&role.RoleNotFoundError{}, NotFound),
	as(&role.RoleNameExistsError{}, AlreadyExists),
	as(&role.RoleIDExistsError{}, AlreadyExists),
	// Reserved whether or not anything holds it: malformed, not a collision.
	as(&role.ReservedRoleNameError{}, InvalidArgument),
	as(&role.SystemRoleImmutableError{}, PermissionDenied),
	as(&role.ValidationError{}, InvalidArgument),
	as(&role.InvalidPermissionError{}, InvalidArgument),
	as(&cert.CertNotFoundError{}, NotFound),
	as(&cert.CertRevokedError{}, FailedPrecondition),
	as(&cert.CAKeyNotAvailableError{}, Unavailable),
	as(&cert.BarrierSealedError{}, Unavailable),
	as(&cert.ValidationError{}, InvalidArgument),
}

// tenantRules are the tenant-lifecycle conditions.
var tenantRules = []rule{
	as(&tenant.TenantScopeError{}, PermissionDenied),
	// The requested DataRoot collides with a reserved path or another
	// tenant's -- a retired one's included, whose message names the reconcile
	// verb that frees it. A precondition the caller clears by choosing another
	// path or reconciling.
	as(&tenant.DataRootOverlapError{}, FailedPrecondition),
	// A page size or page token the caller supplied that cannot be honoured.
	as(&tenant.InvalidPageRequestError{}, InvalidArgument),
	is(tenant.ErrEmptyDataRoot, InvalidArgument),
	as(&tenant.InvalidDataRootError{}, InvalidArgument),
	as(&tenant.DataRootIsolationTypeError{}, InvalidArgument),
	as(&tenant.DataRootNotEmptyError{}, AlreadyExists),
	is(tenant.ErrEmptyTenantID, InvalidArgument),
	is(tenant.ErrEmptyTenantName, InvalidArgument),
	is(tenant.ErrEmptyPassphrase, InvalidArgument),
	is(tenant.ErrEmptyShare, InvalidArgument),
	is(tenant.ErrSystemTenantMustBePrefix, InvalidArgument),
	is(tenant.ErrNilQuotas, InvalidArgument),
	is(tenant.ErrNilEntitlements, InvalidArgument),
	as(&tenant.InvalidStrategyError{}, InvalidArgument),
	is(tenant.ErrBarrierIsolationUnavailable, Unprocessable),
	is(tenant.ErrBarrierNotEnabled, Unprocessable),
	is(tenant.ErrTenantAlreadyExists, AlreadyExists),
	// A deleted tenant's id is retired permanently: taken, in exactly the
	// sense an existing tenant's id is.
	as(&tenant.TenantDeletedError{}, AlreadyExists),
	// Satisfiable by dropping the databases the error names.
	as(&tenant.TenantHasDatabasesError{}, FailedPrecondition),
	is(tenant.ErrTenantHasData, FailedPrecondition),
	// No data probe is wired to confirm the tenant is empty: the isolation
	// change fails closed.
	is(tenant.ErrTenantDataUnverifiable, Unavailable),
	is(tenant.ErrTenantNotFound, NotFound),
	as(&tenant.InvalidTenantIDError{}, InvalidArgument),
	// A reserved id is not the caller's to name, on create or on delete.
	as(&tenant.ReservedTenantIDError{}, InvalidArgument),
}

// credentialRules are the named-credential conditions.
var credentialRules = []rule{
	is(credentials.ErrCredentialNotFound, NotFound),
	// Expired and never-existed are one documented answer: the version the
	// caller named is not there.
	is(credentials.ErrVersionNotFound, NotFound),
	is(credentials.ErrVersionExpired, NotFound),
	is(credentials.ErrCredentialAlreadySubmitted, AlreadyExists),
	is(credentials.ErrEnumerationNotSupported, Unimplemented),
	is(credentials.ErrEmptyCredentialName, InvalidArgument),
	is(credentials.ErrEmptyCredentialValue, InvalidArgument),
	is(credentials.ErrInvalidGracePeriod, InvalidArgument),
}

// dekRules are the tenant DEK keyring's conditions. Every keyring error
// matches exactly one pkg/dek sentinel class, so one row per class covers the
// package. A keyring made unreachable by a sealed tenant is already answered
// by the dominant sealed-tenant row; the Unavailable row here covers the
// keyring's other unreachable causes.
var dekRules = []rule{
	is(dek.ErrInvalidInput, InvalidArgument),
	// Another tenant's keyring is refused even for a system-tenant caller.
	is(dek.ErrCrossTenant, PermissionDenied),
	is(dek.ErrMalformedCiphertext, InvalidArgument),
	// Tampering, a different AAD and another tenant's ciphertext are one
	// answer on purpose: distinguishing them would be an oracle.
	is(dek.ErrAuthenticationFailed, InvalidArgument),
	is(dek.ErrUnknownVersion, NotFound),
	is(dek.ErrVersionRetired, FailedPrecondition),
	is(dek.ErrRetireCurrentVersion, FailedPrecondition),
	is(dek.ErrKeyringUnavailable, Unavailable),
	is(dek.ErrCorruptKeyring, DataLoss),
	is(dek.ErrConcurrentUpdate, Aborted),
	is(dek.ErrCrypto, Internal),
}

// pinRules are the PIN backend's conditions.
var pinRules = []rule{
	is(pin.ErrPINNotSet, PreconditionFailed),
	is(pin.ErrPINLocked, ResourceExhausted),
	is(pin.ErrPINInvalid, Unauthenticated),
	is(pin.ErrSOPINRequired, Unauthenticated),
	is(pin.ErrPINTooShort, InvalidArgument),
	is(pin.ErrPINAlreadySet, AlreadyExists),
	is(pin.ErrInvalidCurrentPIN, Unauthenticated),
	// A corrupted PIN state file: restore from backup, do not retry.
	is(pin.ErrStateCorrupted, DataLoss),
	is(pin.ErrStrategyNotSet, PreconditionFailed),
	// No PIN backend is wired, so every PIN-gated operation fails closed.
	is(pin.ErrBackendNotConfigured, PreconditionFailed),
}

// authFactorRules are the hardware authentication-factor seam's conditions.
var authFactorRules = []rule{
	as(&authfactors.EnrollmentNotFoundError{}, NotFound),
	as(&authfactors.FactorNotFoundError{}, NotFound),
	as(&authfactors.EnrollmentInvalidError{}, InvalidArgument),
	// An enrolment naming a tenant other than the caller's context
	// (go-qrdb#464): the caller's input, not a server fault.
	as(&service.AuthFactorTenantMismatchError{}, InvalidArgument),
	// A verify naming a factor other than the enrollment's own (go-qrdb#577):
	// the caller's input, refused rather than corrected.
	as(&authfactors.FactorMismatchError{}, InvalidArgument),
	as(&authfactors.EnrollmentRevokedError{}, Gone),
	as(&authfactors.ChallengeExpiredError{}, Gone),
	as(&authfactors.VerificationFailedError{}, Unauthenticated),
	as(&authfactors.HardwareUnavailableError{}, Unavailable),
	is(authfactors.ErrHardwareUnavailable, Unavailable),
	is(authfactors.ErrFactorDisabled, Unimplemented),
}

// userAuthFactorRules are the per-user enrolled-factor conditions.
var userAuthFactorRules = []rule{
	is(auth.ErrFactorNotFound, NotFound),
	is(auth.ErrUserHasNoFactors, NotFound),
	is(auth.ErrFactorRevoked, Gone),
	// The documented answer: removing the last factor is refused until the
	// caller forces it or enrols another.
	is(auth.ErrLastFactor, FailedPrecondition),
	is(auth.ErrFactorAlreadyExists, AlreadyExists),
	is(auth.ErrFactorTypeUnsupported, InvalidArgument),
	is(auth.ErrInvalidFactor, InvalidArgument),
	// No caller may write the server-owned metadata namespace over any
	// transport: a malformed request, not a decision about this principal.
	is(auth.ErrReservedMetadata, InvalidArgument),
	// The legacy-factor migration left records it could not place: the run
	// is not finished, and re-running it is the caller's move.
	as(&service.FactorMigrationRefusedError{}, FailedPrecondition),
}

// holderRules are the share-holder, quorum and envelope conditions. They sit
// ahead of the PIN, auth-factor and ceremony families: a holder's failed
// authentication wraps its factor's own error, and a rekey that would break
// the quorum also matches seal.ErrShamirThresholdInvalid.
var holderRules = []rule{
	as(&seal.HolderAuthenticationError{Holder: "h", Err: errHolderSample}, PermissionDenied),
	as(&seal.HolderNotInRosterError{}, PermissionDenied),
	as(&seal.HolderRevokedError{}, PermissionDenied),
	as(&seal.ShareIndexMismatchError{}, PermissionDenied),
	as(&seal.FactorNotAdmittedError{}, PermissionDenied),
	as(&seal.HolderAlreadySubmittedError{}, AlreadyExists),
	as(&seal.RevocationBreaksQuorumError{}, FailedPrecondition),
	as(&seal.HolderCountMismatchError{}, FailedPrecondition),
	as(&seal.QuorumOpInvalidError{}, InvalidArgument),
	as(&seal.InvalidRosterError{}, InvalidArgument),
	is(seal.ErrHolderRosterMissing, FailedPrecondition),
	is(seal.ErrHolderVerifierNotConfigured, FailedPrecondition),
	is(seal.ErrQuorumRoundNotOpen, FailedPrecondition),
	is(seal.ErrQuorumRoundInProgress, AlreadyExists),
	is(seal.ErrRecoveryKeysExist, AlreadyExists),
	is(seal.ErrShamirQuorumExpired, FailedPrecondition),
	is(seal.ErrShamirInitializeRequiresHolders, FailedPrecondition),
	is(ceremony.ErrSelfEnrolment, PermissionDenied),
	is(ceremony.ErrChallengeVerificationFailed, PermissionDenied),
	is(ceremony.ErrChallengeExpired, PermissionDenied),
	is(ceremony.ErrChallengeNotFound, FailedPrecondition),
	is(ceremony.ErrQuorumChallengeNotFound, PermissionDenied),
	is(ceremony.ErrQuorumChallengeMismatch, PermissionDenied),
	is(ceremony.ErrDuplicateHolderKey, AlreadyExists),
	is(ceremony.ErrDuplicateUsername, AlreadyExists),
	is(ceremony.ErrNoOfficers, InvalidArgument),
	is(ceremony.ErrInvalidThreshold, InvalidArgument),
	is(ceremony.ErrAlreadyInitialized, FailedPrecondition),
	is(ceremony.ErrTooManyPendingChallenges, ResourceExhausted),
	is(ceremony.ErrTrustDomainNotConfigured, FailedPrecondition),
	is(ceremony.ErrCANotInitialized, FailedPrecondition),
	is(ceremony.ErrInvalidHolderRole, InvalidArgument),
	is(ceremony.ErrMissingUsername, InvalidArgument),
	is(ceremony.ErrMissingCSRPEM, InvalidArgument),
	is(ceremony.ErrShareEnvelopeMalformed, InvalidArgument),
	as(&ceremony.InvalidHolderNameError{}, InvalidArgument),
	as(&ceremony.EncryptionKeyRequiredError{Err: errHolderSample}, InvalidArgument),
	as(&ceremony.DuplicateFactorError{}, InvalidArgument),
	as(&ceremony.FactorNotEnrolledError{}, InvalidArgument),
	as(&ceremony.HolderFactorEnrollmentError{Err: errHolderSample}, InvalidArgument),
	as(&ceremony.CSRParseError{Err: errHolderSample}, InvalidArgument),
	as(&keydelivery.UnsupportedKEMError{}, InvalidArgument),
	as(&keydelivery.UnsupportedRecipientKeyError{}, InvalidArgument),
	as(&keydelivery.SuiteNotApprovedError{}, InvalidArgument),
	is(service.ErrNilRequest, InvalidArgument),
}

// errHolderSample is the cause the holder rows' wrapping samples carry, so
// their Error methods have something to render.
var errHolderSample = errors.New("sample cause")

// ceremonyRules are the barrier seal/unseal and M-of-N ceremony conditions.
var ceremonyRules = []rule{
	is(service.ErrCeremonyNotConfigured, Unprocessable),
	is(seal.ErrShamirBarrierRequiresQuorum, Unprocessable),
	// A Shamir barrier is unsealed by a holder quorum, not by one caller.
	is(seal.ErrShamirUnsealRequiresQuorum, Unprocessable),
	is(seal.ErrUnsealKeyMismatch, PermissionDenied),
	is(seal.ErrInvalidCredentials, PermissionDenied),
	is(seal.ErrDecryptionFailed, PermissionDenied),
	is(seal.ErrAlreadyUnsealed, FailedPrecondition),
	is(seal.ErrAlreadyInitialized, FailedPrecondition),
	is(seal.ErrNotInitialized, FailedPrecondition),
	is(ceremony.ErrCertThumbprintMismatch, PermissionDenied),
	// An anonymous officer registration after enrollment closed: retrying
	// will not help; register through an authenticated listener.
	is(ceremony.ErrEnrollmentClosed, PermissionDenied),
	is(ceremony.ErrOfficerNotFound, NotFound),
	is(ceremony.ErrShareAlreadyClaimed, FailedPrecondition),
	is(ceremony.ErrNoPendingShare, NotFound),
	// A held share outlived its claim window and was destroyed; only a rekey
	// deals a new one.
	is(ceremony.ErrShareExpired, Gone),
	is(ceremony.ErrInsufficientOfficers, Unprocessable),
}

// migrationRules are the enable-encryption migration conditions.
var migrationRules = []rule{
	is(seal.ErrMigrationInvalidTarget, InvalidArgument),
	is(seal.ErrMigrationInvalidMechanism, InvalidArgument),
	is(seal.ErrMigrationInvalidAvailability, InvalidArgument),
	is(seal.ErrShamirThresholdInvalid, InvalidArgument),
	is(seal.ErrMigrationSourceNotNone, FailedPrecondition),
	is(seal.ErrMigrationInProgress, FailedPrecondition),
	is(service.ErrSharedBarrierImmutable, PermissionDenied),
	is(service.ErrMigrationProviderUnavailable, Unimplemented),
}

// contextRules are the caller's own context ending. They sit after every
// family block, so a typed condition that happens to carry a context error as
// its cause is still reported as itself, and before the generic wrappers, so
// a propose or read that failed BECAUSE the caller gave up is reported as
// that rather than as a server fault.
var contextRules = []rule{
	is(context.Canceled, Canceled),
	is(context.DeadlineExceeded, DeadlineExceeded),
}

// genericRules are the wrappers a failure is REPORTED THROUGH. Each is a
// server fault, and each comes last so that whatever more specific condition
// it wraps is reported instead. Listing them rather than leaving them to
// Unclassified states the decision: these are faults this table has
// identified, not errors it has never seen.
var genericRules = []rule{
	as(&service.ProposeError{}, Internal),
	as(&service.ReadError{}, Internal),
	as(&service.MembershipError{}, Internal),
	as(&service.SnapshotRequestError{}, Internal),
	as(&service.BatchError{}, Internal),
	as(&service.TimeSeriesError{}, Internal),
	as(&service.CollectionMetadataError{}, Internal),
	as(&service.ApplyFaultError{}, Internal),
	as(&service.TableMetadataError{}, Internal),
	as(&service.PrivilegeMetadataError{}, Internal),
	as(&service.VectorOperationError{}, Internal),
	// A write in an already-validated restore plan failed part way; re-issuing
	// the identical request is safe.
	as(&service.RestoreApplyError{}, Internal),
	as(&user.StoreError{}, Internal),
	as(&role.StoreError{}, Internal),
	as(&cert.StoreError{}, Internal),
	as(&cert.CertGenerateError{}, Internal),
	as(&cert.CertExportError{}, Internal),
	as(&tenant.StoreError{}, Internal),
	as(&tenant.BarrierError{}, Internal),
	is(seal.ErrWrappedRootKeyCorrupt, Internal),
	// The credential service validates its strategy at construction; meeting
	// either at request time is a server fault, not the caller's.
	is(credentials.ErrInvalidStrategy, Internal),
	is(credentials.ErrNilBarrier, Internal),
}

// memberIdentityClasses is the class of each member-identity refusal kind.
// A malformed override is the caller's input; an override contradicting a
// committed identity is a conflict with the membership's current state; a node
// no committed membership names, with no override, is a precondition the
// operator restores by supplying its identity; an unreadable membership is
// retryable.
var memberIdentityClasses = map[service.MemberIdentityRefusal]Class{
	service.IdentityInvalid:               InvalidArgument,
	service.IdentityConflict:              FailedPrecondition,
	service.IdentityUnknown:               PreconditionFailed,
	service.IdentityMembershipUnavailable: Unavailable,
}

// memberIdentityRule recognises a *service.MemberIdentityError and classes it
// by its transport-neutral refusal kind.
var memberIdentityRule = rule{
	name: "service.MemberIdentityError",
	classify: func(err error) (Class, bool) {
		var mie *service.MemberIdentityError
		if !errors.As(err, &mie) {
			return Unclassified, false
		}
		return memberIdentityClasses[mie.Refusal()], true
	},
	samples: []error{
		&service.MemberIdentityError{ReplicaID: 4, Err: &cluster.MemberIdentityFormatError{Identity: "x"}},
		&service.MemberIdentityError{ReplicaID: 4, Err: &cluster.MemberIdentityConflictError{ReplicaID: 2}},
		&service.MemberIdentityError{ReplicaID: 4, Err: &cluster.MemberIdentityUnknownError{ReplicaID: 4}},
		&service.MemberIdentityError{ReplicaID: 4, Err: &cluster.MembershipReadError{ShardID: 1, Err: service.ErrNodeClosed}},
	},
}

// collectionOutcomeUnknownRule recognises a *service.CollectionMetadataError
// whose operation leaves a document write's outcome unknown
// (CollectionMetadataError.OutcomeUnknown). Every other operation of that type
// is the generic wrapper genericRules classifies last.
var collectionOutcomeUnknownRule = rule{
	name: "service.CollectionMetadataError.OutcomeUnknown",
	classify: func(err error) (Class, bool) {
		var cme *service.CollectionMetadataError
		if !errors.As(err, &cme) || !cme.OutcomeUnknown() {
			return Unclassified, false
		}
		return Internal, true
	},
	samples: []error{
		&service.CollectionMetadataError{Op: "retract-write", Err: &service.CollectionNotActiveError{Database: "db", Collection: "c", State: "dropping"}},
		&service.CollectionMetadataError{Op: "insert-document", Err: context.DeadlineExceeded},
	},
}

// tableLayoutUnsupportedRule recognises service.IsTableLayoutUnsupported: the
// typed refusal anywhere in the chain, or the SQL engine's numbered rendering
// of it, which carries only the error number.
var tableLayoutUnsupportedRule = rule{
	name: "service.IsTableLayoutUnsupported",
	classify: func(err error) (Class, bool) {
		return PreconditionFailed, service.IsTableLayoutUnsupported(err)
	},
	samples: []error{
		&service.TableLayoutUnsupportedError{Database: "shop", Name: "users", TableID: 7},
		&service.ReadError{Err: &service.TableLayoutUnsupportedError{Database: "shop", Name: "users", TableID: 7}},
	},
}

// tableResolutionStaleRule recognises service.IsTableResolutionStale, which
// also follows the SQL engine's Cause() links that errors.As cannot.
var tableResolutionStaleRule = rule{
	name: "service.IsTableResolutionStale",
	classify: func(err error) (Class, bool) {
		return Aborted, service.IsTableResolutionStale(err)
	},
	samples: []error{
		&service.TableGenerationStaleError{Database: "shop", Name: "orders", Expected: 0, Actual: 1},
		&service.TableDefinitionStaleError{Database: "shop", Name: "orders", Reason: "index idx_sku was dropped"},
		&service.ReadError{Err: &service.TableGenerationStaleError{Database: "shop", Name: "orders", Expected: 0, Actual: 1}},
	},
}

// rowLockConflicts are the row-lock refusals rowLockRule recognises by type,
// anywhere in the chain including through Cause() links.
var rowLockConflicts = []func(error) bool{
	func(err error) bool { var e *rowlock.LockWaitTimeoutError; return service.AsCause(err, &e) },
	func(err error) bool { var e *rowlock.LockNotAvailableError; return service.AsCause(err, &e) },
	func(err error) bool { var e *rowlock.DeadlockError; return service.AsCause(err, &e) },
	func(err error) bool { var e *rowlock.LockLostError; return service.AsCause(err, &e) },
}

// rowLockNumbers are the MySQL numbers the SQL layer renders a lock-wait
// timeout and a NOWAIT refusal as.
var rowLockNumbers = map[int]bool{
	rowlock.SQLCodeLockWaitTimeout: true,
	rowlock.SQLCodeLockNowait:      true,
}

// rowLockRule classifies a lost row-lock request as Aborted.
var rowLockRule = rule{
	name: "rowlock conflict",
	classify: func(err error) (Class, bool) {
		for _, match := range rowLockConflicts {
			if match(err) {
				return Aborted, true
			}
		}
		var numbered interface{ Number() int }
		return Aborted, errors.As(err, &numbered) && rowLockNumbers[numbered.Number()]
	},
	samples: []error{
		&rowlock.LockWaitTimeoutError{Key: "k", Mode: rowlock.ModeExclusive},
		&rowlock.LockNotAvailableError{Key: "k", Mode: rowlock.ModeShared},
		&rowlock.DeadlockError{Key: "k", Mode: rowlock.ModeExclusive},
		&rowlock.LockLostError{Key: "k"},
		mysql.NewSQLError(rowlock.SQLCodeLockWaitTimeout, "HY000", "%s", "Lock wait timeout exceeded"),
		mysql.NewSQLError(rowlock.SQLCodeLockNowait, "HY000", "%s", "NOWAIT"),
	},
}

// sqlCodeNotSupportedYet is MySQL's ER_NOT_SUPPORTED_YET, which the SQL
// layer refuses SKIP LOCKED with.
const sqlCodeNotSupportedYet = 1235

// rowLockUnsupportedRule classifies ER_NOT_SUPPORTED_YET as Unimplemented.
var rowLockUnsupportedRule = rule{
	name: "ER_NOT_SUPPORTED_YET",
	classify: func(err error) (Class, bool) {
		var numbered interface{ Number() int }
		return Unimplemented, errors.As(err, &numbered) && numbered.Number() == sqlCodeNotSupportedYet
	},
	samples: []error{
		mysql.NewSQLError(sqlCodeNotSupportedYet, "42000", "%s", "SKIP LOCKED is not supported"),
	},
}

// authSamples are authRule's parity samples: one per rule in
// pkg/auth/middleware's classifier, so every class it can return reaches every
// transport under test.
var authSamples = []error{
	&audit.GateError{},
	&auth.AuthError{Code: "unauthorized"},
	&auth.AuthError{Code: "unauthenticated"},
	&authmw.StoreUnavailableError{},
	&auth.RevocationCheckError{},
	&authmw.UserDisabledError{},
	&authmw.AuthMethodNotPermittedError{},
	&authmw.CertBindingNotPermittedError{},
	&authmw.AuthChainError{},
	&authmw.CertSerialUnresolvableError{},
	&authmw.RateLimitedError{},
	// The attested-caller gate: a peer whose measured state is not acceptable
	// is refused, and a verifier that could not decide is a retryable refusal.
	&authmw.PeerAttestationRefusedError{Reason: authmw.PeerAttestationStateRejected},
	&authmw.PeerAttestationRefusedError{Reason: authmw.PeerAttestationVerifierError},
}
