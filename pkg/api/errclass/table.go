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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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

	// A normal tenant caller named a tenant other than its own.
	as(&tenant.CrossTenantDeniedError{}, PermissionDenied),

	// A login-index rollback FAILED. It unwraps to the conflict that caused
	// it, so it must be matched before every row that could be its cause:
	// reporting the cause would drop the fact that secondary state may now be
	// inconsistent.
	as(&user.IndexCompensationError{}, DataLoss),
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
}

// collectionRules are the document-collection conditions.
var collectionRules = []rule{
	as(&service.CollectionAlreadyExistsError{}, AlreadyExists),
	as(&service.CollectionNotFoundError{}, NotFound),
	as(&service.CollectionEmptyNameError{}, InvalidArgument),
	as(&service.CollectionEmptyDatabaseError{}, InvalidArgument),
	as(&service.CollectionInvalidIDStrategyError{}, InvalidArgument),
	as(&service.CollectionMissingFieldHashError{}, InvalidArgument),
	as(&service.CollectionDocumentNotFoundError{}, NotFound),
	// The mirror of the not-found above: an insert refused because the
	// document it would have written over is already stored.
	as(&service.CollectionDocumentExistsError{}, AlreadyExists),
	as(&service.CollectionEmptyDocumentError{}, InvalidArgument),
	as(&service.CollectionInvalidDocumentIDError{}, InvalidArgument),
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
	as(&service.CompactSystemOnlyError{}, PermissionDenied),
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
	as(&database.InvalidEngineError{}, InvalidArgument),
	as(&database.InvalidBackendError{}, InvalidArgument),
	// A per-database control operation on another tenant's database.
	as(&database.TenantAccessDeniedError{}, PermissionDenied),
	// A per-database barrier operation on a node with no barrier manager.
	as(&service.BarrierManagerNotConfiguredError{}, Unimplemented),
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
	as(&user.UserNotFoundError{}, NotFound),
	// A withdrawal naming a binding the account does not hold.
	as(&user.CertBindingNotFoundError{}, NotFound),
	as(&user.UsernameExistsError{}, AlreadyExists),
	// A store-generated user id collided with an existing identity claim or
	// primary record (go-qrdb#555).
	as(&user.UserIDExistsError{}, AlreadyExists),
	as(&user.EmailExistsError{}, AlreadyExists),
	// A thumbprint already bound to ANOTHER user: rebinding it silently would
	// hand the caller that user's mTLS identity (go-qrdb#536).
	as(&user.CertBindingConflictError{}, AlreadyExists),
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
}

// authFactorRules are the hardware authentication-factor seam's conditions.
var authFactorRules = []rule{
	as(&authfactors.EnrollmentNotFoundError{}, NotFound),
	as(&authfactors.FactorNotFoundError{}, NotFound),
	as(&authfactors.EnrollmentInvalidError{}, InvalidArgument),
	// An enrolment naming a tenant other than the caller's context
	// (go-qrdb#464): the caller's input, not a server fault.
	as(&service.AuthFactorTenantMismatchError{}, InvalidArgument),
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

// ceremonyRules are the barrier seal/unseal and M-of-N ceremony conditions.
var ceremonyRules = []rule{
	is(service.ErrCeremonyNotConfigured, Unprocessable),
	is(seal.ErrShamirBarrierRequiresQuorum, Unprocessable),
	is(seal.ErrSealVoteVerificationFailed, PermissionDenied),
	is(seal.ErrUnsealKeyMismatch, PermissionDenied),
	is(seal.ErrInvalidCredentials, PermissionDenied),
	is(seal.ErrDecryptionFailed, PermissionDenied),
	is(seal.ErrAlreadyUnsealed, FailedPrecondition),
	is(seal.ErrAlreadyInitialized, FailedPrecondition),
	is(seal.ErrNotInitialized, FailedPrecondition),
	is(ceremony.ErrCertThumbprintMismatch, PermissionDenied),
	is(ceremony.ErrOfficerNotFound, NotFound),
	is(ceremony.ErrShareAlreadyClaimed, FailedPrecondition),
	is(ceremony.ErrNoPendingShare, NotFound),
	is(ceremony.ErrInsufficientOfficers, Unprocessable),
	is(ceremony.ErrInvalidAuthenticatorType, InvalidArgument),
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
}
