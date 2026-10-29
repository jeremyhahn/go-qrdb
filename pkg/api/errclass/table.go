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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/certenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/rag"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
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

	// A database create ran out of the service's own create deadline. Every
	// step it had completed was rolled back, so it is retryable whatever the
	// step the deadline cut short reported -- a proposal timeout, or a fence
	// write's failure in its own type -- and the refusal wins over that cause.
	as(&service.DatabaseCreateTimeoutError{Name: "app", Err: &database.KeyspaceFenceError{Name: "app", TenantID: "acme", Err: context.DeadlineExceeded}}, Unavailable),

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
	// go-quicraft's own sentinel for the same condition: a node barrier
	// re-sealed at runtime refuses every store read and write with it, and
	// every service wrapper would otherwise report it as a server fault.
	is(seal.ErrBarrierSealed, Unavailable),

	// A tenant's location is a volume pool and slot resolved on each node.
	// Dominant, because the tenant and database services wrap it in their
	// own barrier errors: a removal outside this node's pool is refused as
	// a fault.
	as(&seal.DataRootOutsideLocalRootsError{}, Internal),
	// A dedicated-raft database asked of a node its placement does not name:
	// retryable against one of the replicas the refusal lists.
	as(&placement.NotAReplicaError{}, Unavailable),
	// A shard that requires a dedicated tenant WAL was not started because the
	// WAL could not be opened: it never falls back to the shared WAL, and the
	// request is retryable once the volume or the tenant barrier is back.
	as(&database.TenantWALUnavailableError{}, Unavailable),
	// The node holding a single-node database did not serve a request
	// forwarded to it: retryable once the owner is back.
	as(&database.BindingOwnerUnavailableError{}, Unavailable),
	// A placement record names a path for a replica: refused, never opened;
	// the database must be dropped and re-created.
	as(&placement.ReplicaDataRootError{}, FailedPrecondition),

	// Cluster sealing. Dominant, because the tenant and database services
	// wrap them in their own barrier errors: a seal or clear record whose
	// outcome is unknown, and a node that cannot confirm the seal records,
	// are retryable; a stream a seal ended is the sealed refusal; a lost race
	// is a conflict the caller re-reads; acting before the cluster activated
	// cluster-seal is a precondition.
	as(&clusterseal.RecordNotConfirmedError{}, Unavailable),
	as(&clusterseal.SealStateUnconfirmedError{}, Unavailable),
	as(&clusterseal.SealedError{}, Unavailable),
	as(&clusterseal.SealEpochConflictError{}, Aborted),
	is(clusterseal.ErrInactive, FailedPrecondition),
	as(&clusterseal.CorruptRecordError{}, Internal),
	as(&clusterseal.InvalidBarrierError{}, InvalidArgument),

	// Barrier init statements. A statement the barrier owner did not sign is
	// refused outright; a claim of a barrier another statement already
	// claimed is AlreadyExists; an epoch that does not follow the recorded
	// one, or lost a race, is a conflict the caller re-reads; a cluster that
	// has not enabled guarded writes cannot record a claim yet. A barrier
	// whose epoch-1 anchor is already pinned to another first admin exists.
	as(&barrierinit.InitStatementError{}, PermissionDenied),
	as(&barrierinit.BarrierAlreadyInitializedError{}, AlreadyExists),
	as(&barrierinit.AnchorConflictError{}, AlreadyExists),
	as(&barrierinit.EpochConflictError{}, Aborted),
	as(&barrierinit.CorruptRecordError{}, Internal),
	as(&barrierinit.GuardedWritesUnavailableError{}, Unavailable),

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

	// A caller outside the system tenant attempted a platform-operator-only
	// tenant provisioning operation (create, set quotas, set entitlements,
	// set isolation tier) directly against the tenant service -- the check
	// the embedded transport makes itself, since it runs no AuthMiddleware.
	as(&tenant.PlatformOperatorRequiredError{}, PermissionDenied),

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
	// The cluster enable-encryption coordinator's refusal on a node that does
	// not lead the shard its intent is written on: an election in progress,
	// the same transient condition as the typed refusal above.
	is(clustermig.ErrNotLeader, Unavailable),
	// A request Raft timed out: a proposal or read that met an election, or a
	// leader still committing the first entry of its term, and found no leader
	// to answer it in time. Retryable once a leader serves, on every
	// transport. Ahead of the ProposeError, ReadError and store wrappers'
	// Internal rows (genericRules), each of which would otherwise report the
	// election as a server fault. A timed-out write may still have committed,
	// so a client retries only a write it has made idempotent.
	is(quicraft.ErrTimeout, Unavailable),
	// A leader stepping down under a proposal or member change already in its
	// log: the same election met mid-request. The service layer keeps it out
	// of NotLeaderError, since the entry may still commit, so it arrives in
	// its ProposeError or MembershipError wrapper and is claimed here, ahead
	// of their Internal rows, with the same idempotent-retry caveat.
	is(quicraft.ErrNotLeader, Unavailable),
	// A shard paused while the node seals: the seal pauses every shard the
	// node hosts before it wipes the keys (seal-pause-resume), and a paused
	// replica refuses proposals, reads and sessions with ErrShardPaused, which
	// go-quicraft marks Temporary. The node serves again once it is unsealed
	// and the shard resumes, so the refusal is retryable on every transport.
	// Ahead of the ProposeError, ReadError and store wrappers' Internal rows
	// (genericRules) for the same reason as ErrTimeout above.
	is(quicraft.ErrShardPaused, Unavailable),
	// go-quicraft v0.17.0's membership rejections, ahead of MembershipError's
	// own blanket Internal row below (genericRules): an add that would bind a
	// Raft address or node identity another ACTIVE member already holds is a
	// naming conflict the caller can resolve (pick another address, or the
	// identity that member already committed), never a server fault.
	is(quicraft.ErrDuplicateMemberAddress, AlreadyExists),
	is(quicraft.ErrDuplicateMemberIdentity, AlreadyExists),
	// StartShard's refusal when this host's own TLS certificate's SAN list
	// also covers a peer's address: the certificate must be reissued
	// narrower before the shard can start. A configuration precondition the
	// operator corrects, not a transient fault.
	is(quicraft.ErrSharedPeerCertificate, FailedPrecondition),
	// Ceremony-initialize's admin-credential step refuses up front on a
	// follower, naming the current leader (go-qrdb#548); the same retry-
	// elsewhere class as NotLeaderError above.
	as(&service.AdminCredentialNotLeaderError{}, Unavailable),
	// The replicated audit chain's cluster feature gate has not activated
	// audit-append on every replica of the tenant's shard yet: retry once it
	// has, the same instruction NotLeaderError gives for a different cause.
	as(&service.AuditFeatureNotActiveError{}, Unavailable),
	// A dedicated-isolation (volume/raft) tenant's audit trail is not yet
	// routable: a healthy node declining a capability it does not implement
	// for this tenant class, the same shape as CapabilityUnsupportedError.
	as(&service.DedicatedTenantAuditNotSupportedError{}, Unimplemented),
	// The tenant's replicated-audit entitlement is off, the same class
	// quota.EntitlementError already uses below for every other entitlement
	// refusal.
	as(&service.ReplicatedAuditNotEntitledError{}, PermissionDenied),
	// The system barrier is brought up by "qrdb init", not "barrier init"
	// (go-qrdb#548): the request names an operation this node refuses to
	// perform at all, not a transient or server-side fault.
	as(&service.SystemBarrierInitRefusedError{}, FailedPrecondition),
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
	// A scan bound on a numeric index that is not a number, or is NaN: the
	// caller's request, refused before anything is read. It reaches a
	// transport inside ReadError, which would otherwise claim it as a fault.
	as(&index.InvalidNumericBoundError{}, InvalidArgument),
	as(&service.ConsistencyError{}, PreconditionFailed),
	is(watch.ErrCompacted, OutOfRange),
	// A replay requested of a store that keeps no change history.
	is(watch.ErrStartRevisionUnsupported, Unimplemented),
	// A raw-KV point verb (Get/Put/Delete/CAS) or a transaction compare
	// named the cluster CA's key material on an un-namespaced servicer
	// (the embedded SDK transport -- see service.hardConfinedPrefixes' own
	// doc comment). The same class as RestoreInternalKeyError: an
	// authority the caller does not have, not a malformed request.
	as(&service.HardConfinedKeyError{}, PermissionDenied),
	// Import on a BatchServicer that is not a *BatchService: no row-key
	// guard could be installed, so Import refuses rather than let an
	// unguarded file import reach the cluster CA's key material. Same
	// class as HardConfinedKeyError -- a fail-closed refusal, not a
	// malformed request.
	as(&service.HardConfinedImportGuardUnavailableError{}, PermissionDenied),
	// An ordinary key-value write or exact-key read named a key in the
	// reserved vector namespace (pkg/vector.RefuseForgedKeys /
	// vector.IsForgedKey, reachable today through Put, Delete, CompareAndSwap,
	// CompareAndDelete, Batch, Transaction and Guarded on both engines). This
	// is the caller doing something it was never authorized to do to that
	// keyspace, not a missing key and not a malformed request, so it is
	// PermissionDenied rather than the NotFound or InvalidArgument a generic
	// classifier would have defaulted it to (go-qrdb vector-replicated
	// review, B3-residual).
	as(&vector.ForgedKeyError{}, PermissionDenied),
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

// vlogRules classifies the verifiable-log refusals (pkg/vlog and the
// LogService). The table read and write faults stay unclassified: they are
// storage faults, not answers to the request.
var vlogRules = []rule{
	as(&vlog.InvalidTenantIDError{}, InvalidArgument),
	as(&vlog.InvalidLogNameError{}, InvalidArgument),
	as(&vlog.InvalidDescriptorError{}, InvalidArgument),
	as(&vlog.AppendBatchError{}, InvalidArgument),
	as(&vlog.UnsupportedKeyTypeError{}, InvalidArgument),
	as(&vlog.EmptyCheckpointError{}, InvalidArgument),
	as(&vlog.LogExistsError{}, AlreadyExists),
	as(&vlog.CheckpointExistsError{}, AlreadyExists),
	as(&vlog.LogNotFoundError{}, NotFound),
	as(&vlog.KeyNotFoundError{}, NotFound),
	as(&vlog.SizeOutOfRangeError{}, OutOfRange),
	as(&vlog.IndexOutOfRangeError{}, OutOfRange),
	// A log whose descriptor forbids expiry, a checkpoint over another root
	// and an undecodable key record are states of the log, not of the request.
	as(&vlog.ExpireNotAllowedError{}, FailedPrecondition),
	as(&vlog.CheckpointRootMismatchError{}, FailedPrecondition),
	as(&vlog.InvalidSigningKeyError{}, FailedPrecondition),
	// Another rotation applied first: the caller retries against the new key.
	as(&vlog.KeyVersionConflictError{}, Aborted),
	as(&vlog.HeadSignatureError{}, Internal),
	as(&vlog.CorruptTableError{}, DataLoss),
	as(&vlog.NotCompiledError{}, Unimplemented),
	as(&service.LogFeatureNotActiveError{}, Unavailable),
	// A barrier tenant or database keeps its data under its own barrier; the
	// log registry sits under the system barrier, so neither is served.
	as(&service.BarrierTenantLogNotSupportedError{}, Unimplemented),
	as(&service.BarrierDatabaseLogNotSupportedError{}, Unimplemented),
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
	as(&service.ImportArchiveExpandedTooLargeError{}, TooLarge),
	as(&service.ImportTooManyEntriesError{}, InvalidArgument),
	as(&service.ImportTooManyRowsError{}, InvalidArgument),
	as(&service.ImportProposalTooLargeError{}, TooLarge),

	// Chunked import session conditions (batch_import_session.go).
	as(&service.ImportSessionNotFoundError{}, NotFound),
	as(&service.ImportSessionOwnerError{}, PermissionDenied),
	as(&service.ImportSequenceError{}, InvalidArgument),
	as(&service.ImportQuotaExceededError{}, ResourceExhausted),
	// Should never happen through the public API (see its own doc
	// comment); reported as Internal rather than given a class that
	// would suggest the caller can do something about it.
	as(&service.ImportSessionCorruptError{}, Internal),
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
	as(&database.AmbiguousNameError{}, FailedPrecondition),
	as(&database.InvalidNameError{}, InvalidArgument),
	as(&database.ReservedNameError{}, InvalidArgument),
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
	// A per-database barrier mode this node cannot build: the request's
	// choice, not a fault.
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
	// A local copy of a database the catalog moved on from (dropped, or
	// created again, through another node) that this node could not yet
	// release: the copy is refused, never served, and a retry succeeds once
	// the release completes (go-qrdb#893).
	as(&database.StaleDatabaseError{}, Unavailable),
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
	as(&feature.NotActiveError{Feature: featuregate.ClusterShardIDs}, Unavailable),
	// A claim that lost the race on every attempt, or whose store did not
	// answer: nothing was created, and a retry can succeed.
	as(&shard.ClaimContentionError{Attempts: 1}, Unavailable),
	as(&database.ShardIDClaimError{Err: context.DeadlineExceeded}, Unavailable),
	// Turning an existing database's barrier on or off needs a data
	// migration that is not performed.
	as(&database.BarrierMigrationNotSupportedError{}, FailedPrecondition),
	// A Shamir database barrier: given a secret it has no use for; created
	// on a node with no holder custody configured; or a holder operation on
	// a database whose barrier is not a Shamir barrier.
	as(&database.ShamirSecretNotAllowedError{}, InvalidArgument),
	as(&database.InvalidShamirSchemeError{}, InvalidArgument),
	as(&database.ShamirCustodyNotConfiguredError{}, FailedPrecondition),
	as(&database.NotShamirBarrierError{}, FailedPrecondition),
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
	// A leadership transfer (asked for, or ahead of a replica removal) did
	// not finish within its bound: retryable, nothing was changed.
	as(&service.LeaderTransferTimeoutError{}, Unavailable),
	// TransferLeadership named a shard this node does not hold, or a replica
	// the shard never had.
	as(&service.UnknownShardError{}, NotFound),
	as(&service.UnknownReplicaError{}, NotFound),
	// The named replica is a member but cannot lead: an observer, a witness,
	// or removed. The caller picks a voter.
	as(&service.LeaderTransferTargetError{}, FailedPrecondition),
	as(&service.LeaderTransferUnsupportedError{}, Unimplemented),
	as(&service.InvalidRaftTopologyError{}, InvalidArgument),
	// Distributed topology requested and placement is not wired: fail closed
	// rather than collapse to one local replica.
	as(&service.RaftDistributedUnavailableError{}, Unavailable),
	// A rebalance whose placement records could not be read was refused
	// whole, nothing changed: retryable, as a membership read failure is.
	as(&service.RebalancePlacementReadError{}, Unavailable),
	as(&tenant.InvalidTenantIDCharError{}, InvalidArgument),
	// A replica refused on a node that does not advertise a feature the group
	// uses: the request is well formed and conflicts with the node's release,
	// which the operator resolves by upgrading it (go-qrdb#482).
	as(&featuregate.UnsupportedReplicaError{}, FailedPrecondition),
	// A promote names a replica that is not a committed observer: absent,
	// already a voter, or a witness.
	as(&service.MemberNotObserverError{}, FailedPrecondition),
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
	// A document over max_document_size is refused before it is chunked.
	as(&rag.DocumentTooLargeError{}, TooLarge),
	// Chunking fails only on its input: empty text or a chunk setting the
	// chunker refuses.
	as(&rag.ChunkingError{}, InvalidArgument),
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
	as(&service.BackupEncryptionRequiredError{}, InvalidArgument),
	// The caller did not opt in (RestoreOptions.AllowPlaintext /
	// allow_plaintext) to restore a backup whose stored state is
	// plaintext -- the explicit-override discipline CreateBackup's own
	// Encryption field already holds, mirrored for restore.
	as(&service.BackupPlaintextRestoreNotAllowedError{}, InvalidArgument),
	as(&service.BackupInvalidCompressionError{}, InvalidArgument),
	as(&service.BackupInvalidEncryptionError{}, InvalidArgument),
	// Covers both directions: an unregistered keywrap.Provider (create or
	// restore) and an Unwrap/shard-GCM authentication failure (restore) --
	// a caller-supplied credential or reference that does not work, not a
	// server-side failure.
	as(&service.BackupDecryptionFailedError{}, InvalidArgument),
	// RestoreBackup's top-level, pre-import verification failure for a
	// specific target shard (wraps *BackupShardIntegrityError or
	// *BackupShardNotFoundError) -- see docs/key-wrapping/api.md.
	as(&service.BackupVerificationFailedError{}, InvalidArgument),
	// The backup's stored metadata and its artifact disagree about
	// encryption -- a downgrade attempt (go-qrdb security review,
	// backup-encrypt-default #B2) -- or the authenticated manifest's own
	// backup ID/destination/provider does not match the metadata it was
	// read alongside.
	as(&service.BackupMetadataMismatchError{}, InvalidArgument),
	// The authenticated manifest itself (the sealed header, not a shard)
	// failed to authenticate or decode -- a tampered manifest, or the
	// wrong KEK. Returned directly, never wrapped in
	// *BackupVerificationFailedError: the manifest is checked once, up
	// front, before PASS 1's per-shard verification loop even starts.
	as(&service.BackupManifestInvalidError{}, InvalidArgument),
	// The envelope or the (authenticated) manifest names a format
	// version/algorithm this build does not implement -- a format this
	// build cannot read, never a tamper this build could safely decode.
	as(&service.BackupEnvelopeVersionError{}, InvalidArgument),
	as(&service.BackupManifestAlgorithmError{}, InvalidArgument),
	// The artifact is not in a restorable state; retrying the same restore
	// will not change that.
	as(&service.BackupChecksumMismatchError{}, FailedPrecondition),
	as(&service.OnlineRestoreSystemShardError{}, FailedPrecondition),
	as(&service.OnlineRestoreReplicatedShardError{}, FailedPrecondition),
	as(&service.BackupUnsupportedError{}, Unimplemented),
	as(&service.OnlineRestoreUnsupportedError{}, Unimplemented),
	// The built-in "software" provider was resolved with no key directory
	// ever configured (WithKeyDir / lifecycle.Infra.KeyDir /
	// --backup-key-dir) -- a server-side capability gap, not a
	// caller-supplied value the request could fix by retrying.
	as(&service.BackupKeyDirNotConfiguredError{}, Unimplemented),
}

// clusterJoinTokenRules are the member-side CSR join-token minting
// conditions (service.AdminService.MintJoinToken, which delegates to
// pkg/clusterenroll.MintJoinToken).
var clusterJoinTokenRules = []rule{
	// Not configured for member-side CSR join at all (no store/trust
	// domain bound): the node was never built or set up for this.
	as(&service.ClusterJoinTokenNotConfiguredError{}, Unimplemented),
	as(&service.MemberPromotionUnsupportedError{}, Unimplemented),
	as(&service.ReplicaStateHashesUnsupportedError{}, Unimplemented),
	as(&service.ReplicaStateUnsupportedError{}, Unimplemented),
	as(&service.ReplicaStateReadUnsupportedError{}, Unimplemented),
	as(&service.StateMachineHashUnsupportedError{}, Unimplemented),
	as(&service.ReplicaNotHostedError{}, NotFound),
	as(&service.JoinTokenTTLOverflowError{}, InvalidArgument),
	as(&clusterenroll.InvalidJoinIdentityError{}, InvalidArgument),
	as(&clusterenroll.JoinTokenTTLError{}, InvalidArgument),
	// The node identity is already in the cluster's join-identity ledger --
	// the cluster CA has already issued (or recorded) a certificate for it.
	as(&clusterenroll.ExistingMemberError{}, AlreadyExists),
	// The node identity has been explicitly retired (RetireJoinIdentity):
	// permanently refused, unlike ExistingMemberError's "try again once it
	// lapses" -- the resource existed and has been permanently retired.
	as(&clusterenroll.RetiredJoinIdentityError{}, Gone),
	// A vanishingly unlikely digest collision with an already-minted
	// token: the caller's request itself was fine, just lost the race.
	as(&clusterenroll.JoinTokenCollisionError{}, Aborted),
	as(&clusterenroll.JoinTokenGenerateError{}, Internal),
	as(&clusterenroll.JoinTokenStoreError{}, Internal),

	// Not configured for member-side CSR join reviewing (no store bound):
	// same "server-side capability gap" as ClusterJoinTokenNotConfiguredError.
	as(&service.ClusterJoinRequestReviewUnsupportedError{}, Unimplemented),
	// A decision (approve/reject) named an id with no live, undecided
	// queue entry -- absent, expired, or already decided.
	as(&clusterenroll.PendingJoinRequestNotFoundError{}, NotFound),
	// The approval queue is at MaxPendingJoinRequests: a caller-visible
	// capacity limit, not a server fault.
	as(&clusterenroll.PendingJoinQueueFullError{}, ResourceExhausted),
	as(&clusterenroll.PendingJoinStoreError{}, Internal),
}

// identityRules are the user, role and certificate conditions.
var identityRules = []rule{
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
	// A RoleBinding naming a tenant other than its holder's own: a bad field
	// value in the write.
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
	// Assignments of the role kept landing while its holders were stripped:
	// nothing is wrong, and retrying the delete finishes it.
	as(&user.RoleRevocationIncompleteError{}, Aborted),
	// The node's user service cannot strip a role from its holders as composed.
	as(&user.RoleRevokerUnsupportedError{}, Unimplemented),
	// A role-assignment write named a role that does not exist, or one that
	// may not be granted in the tenant it targets: a bad field in the write,
	// whose subject is a user, not the role. ReferenceError wraps the lookup's
	// RoleNotFoundError, so it must precede that row.
	as(&role.ReferenceError{}, InvalidArgument),
	as(&role.NotAssignableError{}, InvalidArgument),
	// The role exists but its delete has begun: it can no longer be granted.
	as(&role.RoleDeletingError{}, InvalidArgument),
	// A role-assignment write named a role and the node has no role service
	// to check it against: the node cannot serve the write as composed.
	as(&role.ServiceRequiredError{}, Unimplemented),
	// A role delete on a node whose role store has no way to strip the
	// role's holders: the node cannot serve the delete as composed.
	// HolderRevocationError has no row on purpose — it wraps the failure that
	// stopped the strip, and that failure's own row is the right answer.
	as(&role.HolderRevokerRequiredError{}, Unimplemented),
	as(&role.RoleNotFoundError{}, NotFound),
	as(&role.RoleNameExistsError{}, AlreadyExists),
	as(&role.RoleIDExistsError{}, AlreadyExists),
	// Reserved whether or not anything holds it: malformed, not a collision.
	as(&role.ReservedRoleNameError{}, InvalidArgument),
	as(&role.SystemRoleImmutableError{}, PermissionDenied),
	// A grantor may grant only permissions it holds (go-qrdb#1278).
	as(&role.GrantExceedsHolderError{}, PermissionDenied),
	as(&role.ValidationError{}, InvalidArgument),
	as(&role.InvalidPermissionError{}, InvalidArgument),
	as(&cert.CertNotFoundError{}, NotFound),
	as(&cert.CertRevokedError{}, FailedPrecondition),
	as(&cert.CAKeyNotAvailableError{}, Unavailable),
	as(&cert.BarrierSealedError{}, Unavailable),
	as(&cert.ValidationError{}, InvalidArgument),
}

// volumeRules are pkg/volume's catalog and provider conditions: the volume
// pool management surface itself (docs/volume-pools/api.md's status-mapping
// table), and the per-cause rules tenantRules' VolumePoolReserveError
// catch-all falls back to when none of these match first. They are placed
// BEFORE tenantRules in the table (see rules below) so a
// *tenant.VolumePoolReserveError wrapping, say, *volume.PoolNotFoundError
// classifies as NotFound rather than the wrapper's own FailedPrecondition
// catch-all: errors.As walks the whole chain, and Classify is first-match-
// wins, so the more specific cause must be checked first.
var volumeRules = []rule{
	as(&volume.PoolNotFoundError{}, NotFound),
	as(&volume.PoolAlreadyExistsError{}, AlreadyExists),
	// Delete refused while tenants remain assigned, or Reserve refused
	// because the provider named at create is not registered on the node
	// handling the request (docs: "Create names a provider this node
	// lacks").
	as(&volume.PoolInUseError{}, FailedPrecondition),
	as(&volume.ProviderNotRegisteredError{}, FailedPrecondition),
	as(&volume.PoolCapacityExceededError{}, ResourceExhausted),
	as(&volume.InvalidPoolRequestError{}, InvalidArgument),
	as(&volume.InvalidAssignmentPathError{}, InvalidArgument),
	as(&volume.InvalidPageRequestError{}, InvalidArgument),
	as(&volume.AssignmentNotFoundError{}, NotFound),
	// A tenant's Assignment already names a different pool than the one
	// just requested: a state conflict, not a straightforward duplicate.
	as(&volume.TenantAlreadyAssignedError{}, FailedPrecondition),
	as(&volume.ReservationConflictError{}, Aborted),
	as(&volume.MoveNotImplementedError{}, Unimplemented),
	// This node cannot open or resolve the pool: a missing mount, an absent
	// device, or a Lockable volume that failed to unlock (decision D6).
	as(&volume.PoolUnavailableError{}, Unavailable),
	as(&volume.OpenError{}, Unavailable),
	as(&volume.ClosedVolumeError{}, Unavailable),
	as(&volume.UsageError{}, Unavailable),
	as(&volume.InvalidSpecError{}, InvalidArgument),
	as(&volume.InvalidVolumeIdentityError{}, InvalidArgument),
	// Composition-time/internal defects: never caused by a caller's request.
	as(&volume.VolumeAlreadyOpenError{}, Internal),
	as(&volume.InvalidCatalogDependencyError{}, Internal),
	as(&volume.InvalidProviderError{}, Internal),
	as(&volume.ProviderAlreadyRegisteredError{}, Internal),
	as(&volume.EncodeError{}, Internal),
	as(&volume.CorruptRecordError{}, Internal),
	as(&volume.InvalidMasterKeyError{}, Internal),
	as(&volume.VolumeKeyDerivationError{}, Internal),
}

// onboardingRules are tenant onboarding, certificate enrolment and trust
// anchors. The wrappers bootstrap uses for a refused credential, a failed SO
// enrolment or an undone provisioning (AdminCredentialRejectedError,
// SOCredentialRejectedError, TenantSOEnrollmentError, ProvisioningUndoneError)
// carry no row of their own: they classify as the cause they wrap.
var onboardingRules = []rule{
	as(&cert.InvalidCSRError{}, InvalidArgument),
	as(&certenroll.InvalidCertificateError{}, InvalidArgument),
	// A certificate that would log in as someone else is the request's fault,
	// however well it chains.
	as(&certenroll.IdentityMismatchError{}, InvalidArgument),
	as(&trustanchor.EmptyBundleError{}, InvalidArgument),
	as(&trustanchor.InvalidAnchorError{}, InvalidArgument),
	as(&trustanchor.InvalidTenantError{}, InvalidArgument),
	as(&trustanchor.CertificateNotTrustedError{}, InvalidArgument),
	as(&trustanchor.AnchorNotFoundError{}, NotFound),
	as(&trustanchor.TooManyAnchorsError{}, ResourceExhausted),
	// Enrolling a bring-your-own certificate before registering any anchor:
	// a precondition the caller clears by adding one.
	as(&trustanchor.NoAnchorsError{}, FailedPrecondition),
	as(&trustanchor.ExternallyManagedError{}, FailedPrecondition),
	as(&tenantadmin.InvalidTenantError{}, InvalidArgument),
	as(&service.CertEnrollmentFieldError{}, InvalidArgument),
	as(&service.CertEnrollmentNotConfiguredError{}, Unimplemented),
	// Key custody stays with the ceremony: certs:issue, certs:enroll and
	// (through the auth middleware) users:add_cert_binding all refuse a
	// Security Officer or (for a non-Owner caller) Owner target the same
	// way, and every transport renders it as a permission refusal, not an
	// internal error.
	as(&service.CertTargetRefusedError{}, PermissionDenied),
	// A delegation the tenant may not issue is the request's fault; a caller
	// that may not delegate at all -- the system tenant, or a delegated
	// caller renewing itself -- is refused permission.
	as(&service.DelegationRequestError{}, InvalidArgument),
	as(&service.DelegationDeniedError{}, PermissionDenied),
	as(&service.TenantOnboardingFieldError{}, InvalidArgument),
	as(&service.TenantOnboardingNotConfiguredError{}, Unimplemented),
	as(&bootstrap.MissingAdminCredentialError{}, InvalidArgument),
	as(&bootstrap.ConflictingAdminCredentialError{}, InvalidArgument),
	as(&bootstrap.InvalidTrustAnchorsError{}, InvalidArgument),
	// A tenant SO named in part, with two credentials, or as the admin by
	// name or by key is the request's fault.
	as(&bootstrap.IncompleteTenantSOError{}, InvalidArgument),
	as(&bootstrap.ConflictingSOCredentialError{}, InvalidArgument),
	as(&bootstrap.SOMatchesAdminError{}, InvalidArgument),
	as(&bootstrap.SOPartlyMatchesAdminError{}, InvalidArgument),
	as(&bootstrap.MissingProvisioningSeamError{}, Unimplemented),
	// Invitation self-enrolment. A token that cannot be redeemed is a bad
	// credential presented by an unauthenticated caller, whatever the reason.
	as(&service.InvitationInvalidError{}, Unauthenticated),
	as(&service.InvitationFieldError{}, InvalidArgument),
	as(&service.InvitationKeyInUseError{}, AlreadyExists),
	as(&service.InvitationAccountActiveError{}, FailedPrecondition),
	as(&user.AccountNotPendingError{}, FailedPrecondition),
}

// tenantRules are the tenant-lifecycle conditions.
var tenantRules = []rule{
	as(&tenant.TenantScopeError{}, PermissionDenied),
	// A page size or page token the caller supplied that cannot be honoured.
	as(&tenant.InvalidPageRequestError{}, InvalidArgument),
	// A description or metadata map outside its bounds on UpdateTenant.
	as(&tenant.TenantFieldError{}, InvalidArgument),
	// A status that is not a status is the caller's error; a known status
	// the tenant cannot move to from where it is depends on its state.
	as(&tenant.InvalidStatusError{}, InvalidArgument),
	as(&tenant.StatusTransitionError{}, FailedPrecondition),
	// An isolation policy only tightens: a loosening, or a setter that lost
	// the compare-and-set to one that moved the policy first, leaves the
	// store unchanged, and the caller re-reads before deciding anything.
	as(&tenant.IsolationPolicyChangeError{}, FailedPrecondition),
	// UpdateTenant lost its compare-and-swap on every attempt: other writers
	// kept committing the record, and the caller may retry the update.
	as(&tenant.ConcurrentUpdateError{}, Aborted),
	as(&tenant.DataRootIsolationTypeError{}, InvalidArgument),
	// Reserving the tenant's named volume pool failed (pool not found,
	// capacity exceeded, entitlement denied). volumeRules above, placed
	// earlier in the table, classifies the specific cause when
	// errors.As finds one; this is the catch-all for anything it does not
	// (VolumePoolReserveError.Unwrap exposes the cause either way).
	as(&tenant.VolumePoolReserveError{}, FailedPrecondition),
	is(tenant.ErrEmptyTenantID, InvalidArgument),
	is(tenant.ErrEmptyTenantName, InvalidArgument),
	is(tenant.ErrEmptyPassphrase, InvalidArgument),
	is(tenant.ErrEmptyShare, InvalidArgument),
	is(tenant.ErrSystemTenantMustBePrefix, InvalidArgument),
	is(tenant.ErrNilQuotas, InvalidArgument),
	is(tenant.ErrNilEntitlements, InvalidArgument),
	as(&tenant.InvalidStrategyError{}, InvalidArgument),
	as(&tenant.InitStatementRequiredError{}, InvalidArgument),
	// A signed init's statement reference that was never begun, was spent or
	// lapsed admits nothing: the caller begins again.
	as(&tenant.SignedInitError{}, PermissionDenied),
	as(&tenant.SignedInitFieldRequiredError{}, InvalidArgument),
	as(&tenant.TooManySignedInitsError{}, ResourceExhausted),
	as(&tenant.SignedInitUnsupportedError{}, FailedPrecondition),
	// A raft-isolated tenant's barrier with no recorded init statement has
	// nothing to reattach from: no retry or other credential changes that.
	as(&tenant.InitStatementMissingError{}, FailedPrecondition),
	// A platform statement names a barrier its owner keeps custody of: the
	// platform holds no key for it, whatever the caller retries with.
	is(tenant.ErrNotPlatformCustody, FailedPrecondition),
	is(tenant.ErrBarrierIsolationUnavailable, Unprocessable),
	is(tenant.ErrBarrierNotEnabled, Unprocessable),
	is(tenant.ErrTenantAlreadyExists, AlreadyExists),
	// A deleted tenant's id is retired permanently: taken, in exactly the
	// sense an existing tenant's id is.
	as(&tenant.TenantDeletedError{}, AlreadyExists),
	// An undone id is not retired: it is released once every serving node
	// has acknowledged its cleanup, so the create is retried after the
	// delay the error names.
	as(&tenant.TenantUndoPendingError{}, Unavailable),
	// Satisfiable by dropping the databases the error names.
	as(&tenant.TenantHasDatabasesError{}, FailedPrecondition),
	is(tenant.ErrTenantHasData, FailedPrecondition),
	// No data probe is wired to confirm the tenant is empty: the isolation
	// change fails closed.
	is(tenant.ErrTenantDataUnverifiable, Unavailable),
	is(tenant.ErrTenantNotFound, NotFound),
	as(&tenant.InvalidTenantIDError{}, InvalidArgument),
	// A tenant ID no SPIFFE ID could carry as its trust domain.
	as(&tenant.TenantTrustDomainError{}, InvalidArgument),
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
	// A record written before key wrapping existed is refused outright,
	// never migrated -- the same DataLoss class as a genuinely corrupt
	// record, since either way the stored record is not usable as is.
	is(dek.ErrLegacyKeyringFormat, DataLoss),
	// No key directory was ever configured for the built-in "software"
	// key-wrap provider -- a SERVER-side capability gap, not a
	// caller-supplied value the request could fix, mirroring
	// *service.BackupKeyDirNotConfiguredError's identical classification.
	is(dek.ErrKeyDirNotConfigured, Unimplemented),
	// An unregistered provider ID, a bad key reference and a KEK
	// authentication failure are one answer on purpose -- the same
	// discipline ErrAuthenticationFailed holds one layer up, and
	// *service.BackupDecryptionFailedError holds for backup.
	is(dek.ErrKeyWrapFailed, InvalidArgument),
}

// policyRules classify the ABAC policy service's errors (pkg/service,
// policy_errors.go).
var policyRules = []rule{
	// Before PolicyInvalidError: a corrupt record wraps the invalid-name
	// error that found it, and is still persisted state, not a bad request.
	as(&service.PolicyCorruptError{}, DataLoss),
	as(&service.PolicyInvalidError{}, InvalidArgument),
	as(&service.PolicyNotFoundError{}, NotFound),
	as(&service.PolicyExistsError{}, AlreadyExists),
	// A lost guarded write: the store is unchanged and a re-read and retry
	// can succeed.
	as(&service.PolicyConflictError{}, Aborted),
	// Another tenant's policies are refused even for a system-tenant caller.
	as(&service.PolicyCrossTenantError{}, PermissionDenied),
	// A server-side capability gap, not a value the request could fix.
	as(&service.PolicyStoreUnsupportedError{}, Unimplemented),
}

// auditVerifyRules are the conditions that stop an audit chain verification
// from being performed at all. A tamper verdict is never an error -- it is a
// field of the report -- so none of these says anything about the trail's
// integrity. A cancelled or expired verification unwraps to its context
// error, which contextRules classify.
var auditVerifyRules = []rule{
	as(&audit.VerificationUnsupportedError{}, Unimplemented),
	as(&audit.ConcurrentRotationError{}, Unavailable),
	as(&audit.FileLockedError{Path: "audit.ndjson"}, Unavailable),
	as(&audit.VerificationBusyError{}, Unavailable),
}

// pinRules are the PIN backend's conditions.
var pinRules = []rule{
	is(pin.ErrPINNotSet, PreconditionFailed),
	is(pin.ErrPINLocked, ResourceExhausted),
	is(auth.ErrSOPINLocked, ResourceExhausted),
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
	// A conditional add refused because its condition no longer holds is the
	// same answer as a duplicate: the state the caller wanted to create
	// already exists in another form. Contention on the factor-set record is
	// the optimistic-concurrency answer -- nothing changed, retry.
	is(auth.ErrFactorConditionFailed, AlreadyExists),
	is(service.ErrFactorSetContended, Aborted),
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
	// A rollback that failed outranks the distribution failure it wraps: the
	// barrier may be left initialized without shares, which is a server fault.
	as(&seal.ShamirInitRollbackError{Distribution: errHolderSample, Rollback: errHolderSample}, Internal),
	is(seal.ErrRestartRequired, FailedPrecondition),
	as(&seal.HolderAuthenticationError{Holder: "h", Err: errHolderSample}, PermissionDenied),
	as(&seal.HolderNotInRosterError{}, PermissionDenied),
	as(&seal.HolderRevokedError{}, PermissionDenied),
	as(&seal.ShareIndexMismatchError{}, PermissionDenied),
	as(&seal.FactorNotAdmittedError{}, PermissionDenied),
	as(&seal.HolderAlreadySubmittedError{}, AlreadyExists),
	// A submission or rekey prepared against a roster replaced meanwhile;
	// retrying reads the current one.
	as(&seal.StaleRosterError{}, Aborted),
	as(&seal.RevocationBreaksQuorumError{}, FailedPrecondition),
	as(&seal.HolderCountMismatchError{}, FailedPrecondition),
	// A Shamir init's second request echoed a split other than the one this
	// node prepared: the caller signs the split the first request returned.
	as(&seal.PreparedShamirMismatchError{}, FailedPrecondition),
	// Every holder the uninitialized barrier is split to is enrolled.
	as(&ceremony.HolderLimitReachedError{}, FailedPrecondition),
	// An initialized barrier's holder is removed by revocation.
	is(ceremony.ErrUnenrolInitialized, FailedPrecondition),
	// An unenrolment racing the barrier's initialization: retry after it.
	is(ceremony.ErrInitializationInProgress, Aborted),
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
	// Registration on a sealed, initialized barrier: unseal first.
	is(ceremony.ErrRegistrationWhileSealed, FailedPrecondition),
	// A holder cached while sealed names a persisted holder with another
	// identity; the persisted record stands and an operator must resolve it.
	as(&ceremony.HolderRecordConflictError{}, FailedPrecondition),
	as(&ceremony.StagedDistributionMissingError{}, Aborted),
	is(ceremony.ErrNoOfficers, InvalidArgument),
	is(ceremony.ErrInvalidThreshold, InvalidArgument),
	is(ceremony.ErrAlreadyInitialized, FailedPrecondition),
	is(ceremony.ErrTooManyPendingChallenges, ResourceExhausted),
	as(&ceremony.QuorumRateLimitedError{}, ResourceExhausted),
	is(ceremony.ErrCANotInitialized, FailedPrecondition),
	is(ceremony.ErrInvalidHolderRole, InvalidArgument),
	is(ceremony.ErrMissingUsername, InvalidArgument),
	is(ceremony.ErrMissingCSRPEM, InvalidArgument),
	is(ceremony.ErrShareEnvelopeMalformed, InvalidArgument),
	as(&ceremony.InvalidHolderNameError{}, InvalidArgument),
	as(&ceremony.EncryptionKeyRequiredError{Err: errHolderSample}, InvalidArgument),
	as(&ceremony.UnsupportedHolderKeyError{}, InvalidArgument),
	as(&ceremony.DuplicateFactorError{}, InvalidArgument),
	as(&ceremony.FactorNotEnrolledError{}, InvalidArgument),
	as(&ceremony.HolderFactorEnrollmentError{Err: errHolderSample}, InvalidArgument),
	as(&ceremony.CSRParseError{Err: errHolderSample}, InvalidArgument),
	as(&keydelivery.UnsupportedKEMError{}, InvalidArgument),
	as(&keydelivery.UnsupportedRecipientKeyError{}, InvalidArgument),
	as(&keydelivery.SuiteNotApprovedError{}, InvalidArgument),
	is(service.ErrNilRequest, InvalidArgument),
	// A share claim over a connection with no verified client certificate.
	as(&service.ClientCertRequiredError{}, Unauthenticated),
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
	// The root key is sealed with another strategy than the one the unseal
	// was resolved for -- the barrier's init statement names it.
	is(seal.ErrStrategyMismatch, FailedPrecondition),
	// A passphrase change whose current passphrase does not open the
	// persisted root key: the caller does not hold the barrier's passphrase.
	is(seal.ErrCredentialMismatch, PermissionDenied),
	// A passphrase change with an empty current or next passphrase.
	is(seal.ErrInvalidPassphrase, InvalidArgument),
	// Another writer replaced the root-key blob between the change's read
	// and its guarded swap; nothing was written. Re-read and retry.
	is(seal.ErrPassphraseChangeConflict, Aborted),
	// Another rewrite of the root-key blob (a second passphrase change, a
	// share redistribution, a migration's adoption) holds it; nothing was
	// written. Retry once that rewrite has finished.
	is(seal.ErrRootKeyWriteInProgress, Aborted),
	// The barrier is not passphrase-sealed, or its storage cannot perform the
	// guarded swap the change requires: no retry of the same request helps.
	is(seal.ErrStrategyNotFound, FailedPrecondition),
	is(seal.ErrPassphraseStrategyNotSoftware, FailedPrecondition),
	is(seal.ErrGuardedSwapUnsupported, FailedPrecondition),
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
	// sign-csr issues holder certificates for so/custodian only; any other
	// requested role is a malformed request, not a server-side fault.
	is(ceremony.ErrInvalidRole, InvalidArgument),
	// CSR-sign and generate need a CA that can sign, which a BYO-TLS node
	// (no CeremonyCA) has none of; the request names a path this node
	// cannot ever satisfy, not a transient or server-side fault.
	as(&ceremony.AdminCredentialRequiresCAError{}, FailedPrecondition),
	// The composer never configured a server certificate for a BYO-TLS
	// node's qrdb init to pin; an operator/configuration gap, not
	// something retrying the same request fixes.
	as(&ceremony.NoServerCertificateError{}, FailedPrecondition),
	// More than one of AdminCSRPEM, AdminCertPEM, AdminGenerate set: a
	// malformed request, never a server-side fault -- 400, not 500
	// (go-qrdb#548).
	as(&ceremony.ConflictingAdminCredentialInputsError{}, InvalidArgument),
	// None of AdminCSRPEM, AdminCertPEM, AdminGenerate set: there is no
	// credential-less default admin-credential path, so this is a malformed
	// request too, not a server-side fault (go-qrdb#548 review).
	as(&ceremony.NoAdminCredentialSelectedError{}, InvalidArgument),
	// The presented admin credential (CSR, certificate, or trust pool)
	// could not be turned into a bound-ready certificate: unparseable
	// input, an expired window, or a chain that does not validate. The
	// caller's request is what is wrong, not this node.
	as(&ceremony.AdminCredentialRejectedError{Cause: errors.New("sample")}, InvalidArgument),
	// The ceremony CA itself refused to sign (or the in-process generate
	// path failed before reaching it): a server-side fault, not something
	// the caller's request can fix by retrying unchanged.
	as(&ceremony.AdminCredentialIssuanceError{Cause: errors.New("sample")}, Internal),
	// The system barrier failed to initialize or unseal during `qrdb init`:
	// a server-side fault in the one place that barrier is ever brought up.
	as(&ceremony.SystemBarrierInitError{Cause: errors.New("sample")}, Internal),
	// The bootstrap admin already holds a live certificate binding: retrying
	// the admin-credential step would mint a second, unbound credential for
	// no reason -- the caller's own prior success, not a fault.
	as(&service.AdminAlreadyBoundError{}, FailedPrecondition),
	// Create-if-absent lost the pending-admin-cert race to another node's
	// ceremony (go-qrdb#548): this node's credential will not be bound: the
	// caller must discard it, not retry the identical request.
	as(&firstboot.AdminCredentialAlreadyPendingError{}, FailedPrecondition),
	// Neither a live admin binding nor a pending-admin-cert record appeared
	// within the bound: retriable by re-running `qrdb init`, not a
	// server-side fault.
	as(&firstboot.NoPendingAdminCredentialError{}, FailedPrecondition),
	// The pending-admin-cert record's own backing store failed a read,
	// write or consume-in-place call: a server-side fault, exactly like
	// user.StoreError/role.StoreError/cert.StoreError/tenant.StoreError
	// below.
	as(&firstboot.PendingAdminCertStoreError{Cause: errors.New("sample")}, Internal),
	// This composition never wired a CertBinder into the admin-credential
	// retry path: an operator/configuration gap, not something retrying
	// fixes.
	as(&service.AdminRebindNoBinderError{}, FailedPrecondition),
	// The admin lookup needed to bind a reissued certificate failed: a
	// server-side fault, since the request itself asked for nothing that
	// could cause this.
	as(&service.AdminRebindLookupError{Cause: errors.New("sample")}, Internal),
	// A reissue succeeded but the bootstrap admin does not exist yet (first
	// boot has not created it on this cluster): retriable once it has,
	// not a server-side fault.
	as(&service.AdminRebindAdminNotFoundError{}, FailedPrecondition),
	// Binding the reissued certificate failed after it was successfully
	// minted: a server-side fault in the one place reissue is ever bound.
	as(&service.AdminRebindBindError{Cause: errors.New("sample")}, Internal),
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
//
// quicraft reports a proposal or read the caller cancelled as its own
// sentinel, so that is the same condition and gets the same answer. Its
// timeout sentinel is not here: dataPlaneRules answers it as Unavailable,
// because it most often means an election, not the caller's deadline.
var contextRules = []rule{
	is(context.Canceled, Canceled),
	is(context.DeadlineExceeded, DeadlineExceeded),
	is(quicraft.ErrCanceled, Canceled),
}

// raftTransientRules are the consensus layer's refusals that clear on their
// own -- rate limiting, a dropped or aborted proposal, a shard still coming
// up, a replica that is not the leader -- exactly the set quicraft.Temporary
// names. dataPlaneRules claims the timeout Temporary also covers, as the same
// Unavailable. They sit before the generic wrappers they arrive in, so the caller is
// told to retry instead of being handed a server fault.
var raftTransientRules = []rule{{
	name: "quicraft.Temporary",
	classify: func(err error) (Class, bool) {
		return Unavailable, quicraft.Temporary(err)
	},
	samples: []error{
		quicraft.ErrBusy,
		quicraft.ErrSystemBusy,
		quicraft.ErrAborted,
		quicraft.ErrDropped,
		quicraft.ErrShardNotStarted,
		quicraft.ErrShardNotReady,
		quicraft.ErrNotLeader,
		quicraft.ErrReadIndexNotReady,
		quicraft.ErrSnapshotInProgress,
		quicraft.ErrTransferInProgress,
	},
}}

// genericRules are the wrappers a failure is REPORTED THROUGH. Each is a
// server fault, and each comes last so that whatever more specific condition
// it wraps is reported instead. Listing them rather than leaving them to
// Unclassified states the decision: these are faults this table has
// identified, not errors it has never seen.
var genericRules = []rule{
	// A forward to a replica failed after the request may have reached it;
	// what the replica would have answered is unknown, so the node it came
	// through is unavailable to the caller for it.
	as(&replicaforward.SendError{}, Unavailable),
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
	// The invitation store's backing DAO/KV failed a lookup, a redemption
	// check or the consuming write (go-qrdb invite-notfound-ambiguity):
	// reported as itself rather than folded into the opaque
	// *service.InvitationInvalidError, so an outage classifies as a server
	// fault instead of a forged credential.
	as(&invite.StoreError{}, Internal),
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

// numberedSampleError is a minimal, dependency-free stand-in for the
// numbered SQL errors the wire protocols produce at runtime (vitess's
// *mysql.SQLError, reached only under the qrdb_mysql / qrdb_pgsql build
// tags). rowLockRule and rowLockUnsupportedRule classify by the
// `interface{ Number() int }` shape alone, so a parity sample only needs to
// satisfy that shape -- it never needs the real vitess type, and importing
// it here would link vitess into every build, including one without SQL.
type numberedSampleError struct {
	number int
	msg    string
}

func (e *numberedSampleError) Error() string { return e.msg }

func (e *numberedSampleError) Number() int { return e.number }

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
		&numberedSampleError{number: rowlock.SQLCodeLockWaitTimeout, msg: "Lock wait timeout exceeded"},
		&numberedSampleError{number: rowlock.SQLCodeLockNowait, msg: "NOWAIT"},
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
		&numberedSampleError{number: sqlCodeNotSupportedYet, msg: "SKIP LOCKED is not supported"},
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
	&authmw.TenantOwnerUnresolvedError{},
	&authmw.BarrierSealedError{Err: seal.ErrBarrierSealed},
	&auth.RevocationCheckError{},
	&auth.TenantLookupError{},
	&auth.TrustStateUnavailableError{},
	&authmw.UserDisabledError{},
	&authmw.TenantNotActiveError{},
	&authmw.AuthMethodNotPermittedError{},
	&authmw.CertBindingNotPermittedError{},
	&authmw.FirstInitCertificateMismatchError{},
	&authmw.FirstInitNotOnboardedError{},
	&tenant.FirstAdminProofError{Reason: initproof.ReasonBadSignature},
	&initproof.TooManyChallengesError{Limit: initproof.DefaultMaxOutstanding},
	&authmw.AuthChainError{},
	&authmw.CertSerialUnresolvableError{},
	&authmw.RateLimitedError{},
	// The attested-caller gate: a peer whose measured state is not acceptable
	// is refused, and a verifier that could not decide is a retryable refusal.
	&authmw.PeerAttestationRefusedError{Reason: authmw.PeerAttestationStateRejected},
	&authmw.PeerAttestationRefusedError{Reason: authmw.PeerAttestationVerifierError},
	// The RBAC role-resolution failure: the primary role store failed or
	// timed out and the StaleRead fallback -- confined to the system tenant
	// -- either is not configured or was not consulted for the caller's
	// tenant. Every transport must answer 503/Unavailable, not the 401/403
	// an unclassified failure would have fallen back to.
	&rbac.ResolveError{Err: context.DeadlineExceeded},
	// Every OTHER cause resolveErrorIsTransient allow-lists, so cross-transport
	// parity covers all of them, not just the one above. Each of these shares
	// the SAME real answer (503/Unavailable) as the sample above, so adding
	// them does not create the "one Want, two real answers" conflict a
	// DEFINITIVE cause would (see TestClassify_ResolveErrorDefinitiveCausesAreNotUnavailable
	// in classify_test.go for why those live there instead).
	&rbac.ResolveError{Err: context.Canceled},
	// The two role.StoreError samples and the tenant.StoreError sample below
	// are NOT exercising transientMarker/ReadTransient: a composite literal
	// built outside pkg/role/pkg/tenant cannot set either type's unexported
	// transient field (only role.newStoreError/tenant.newStoreError can), so
	// each one's ReadTransient() reads its zero value, false. They classify
	// Unavailable here because resolveErrorIsTransient's EARLIER
	// context.DeadlineExceeded check unwraps straight through the StoreError
	// wrapper via errors.Is -- these samples prove that unwrap, i.e. that a
	// real role.newStoreError("get", "primary lookup",
	// context.DeadlineExceeded) (what the bounded primary resolve actually
	// produces when resolveSyncTimeout fires) still classifies correctly
	// once wrapped, not that the marker mechanism itself works. The marker
	// is proven end-to-end, through the real producers, by
	// pkg/auth/middleware's own TestResolveErrorIsTransient_ProducerDeclaresTransience
	// and the in-package newStoreError/ReadTransient unit tests in
	// pkg/role/errors_test.go and pkg/tenant/errors_test.go -- a composite
	// literal cannot reach that field, so no sample built here ever could.
	&rbac.ResolveError{Err: &role.StoreError{Op: "get", Detail: "primary lookup", Err: context.DeadlineExceeded}},
	&rbac.ResolveError{Err: &role.StoreError{Op: "get_by_name", Detail: "index lookup", Err: context.DeadlineExceeded}},
	&rbac.ResolveError{Err: tenant.ErrTenantSealed},
	&rbac.ResolveError{Err: seal.ErrSealed},
	&rbac.ResolveError{Err: seal.ErrBarrierSealed},
	&rbac.ResolveError{Err: &tenant.StoreError{Op: "get", Err: context.DeadlineExceeded}},
}
