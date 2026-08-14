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

package service

import (
	"strconv"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// SMNotConfiguredError is returned when a proposal succeeds at the Raft level
// but the state machine encodes an application error into result.Data instead
// of returning a fatal Update error (which would halt lastApplied and stall
// every read and write on the group). The encoded outcome is a deterministic,
// non-fatal consequence of valid client input — a target subsystem that is not
// configured on this SM (lease, timeseries), an invalid lease TTL, or a
// unique-index violation. This surfaces that outcome to the API layer as a
// typed error.
type SMNotConfiguredError struct {
	// Message is the error string the SM encoded into result.Data.
	Message string
}

// Error returns a human-readable description.
func (e *SMNotConfiguredError) Error() string {
	return "service: " + e.Message
}

// checkResultError inspects result.Data for a state-machine-encoded application
// error (see protocol.EncodeAppError). The discriminated prefix is required so
// ordinary result payloads — notably the one-byte transaction success flag —
// are never misread as errors. Returns a non-nil SMNotConfiguredError only when
// the payload carries the application-error prefix.
func checkResultError(result sm.Result) error {
	if msg, ok := protocol.DecodeAppError(result.Data); ok {
		return &SMNotConfiguredError{Message: msg}
	}
	return nil
}

// KeyNotFoundError is returned when a KV lookup finds no matching key.
type KeyNotFoundError struct {
	Key string
}

// Error returns a human-readable description of the missing key.
func (e *KeyNotFoundError) Error() string {
	return "service: key not found: " + e.Key
}

// ProposeError is returned when a Raft proposal fails.
type ProposeError struct {
	Err error
}

// Error returns a human-readable description of the proposal failure.
func (e *ProposeError) Error() string {
	return "service: propose failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ProposeError) Unwrap() error {
	return e.Err
}

// ReadError is returned when a linearizable read fails.
type ReadError struct {
	Err error
}

// Error returns a human-readable description of the read failure.
func (e *ReadError) Error() string {
	return "service: read failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReadError) Unwrap() error {
	return e.Err
}

// NotLeaderError is returned when an operation requires the leader but
// the local node is not the current leader. It carries the current
// leader's identity so callers can redirect.
type NotLeaderError struct {
	ShardID       uint64
	LeaderID      uint64
	LeaderAddress string
}

// Error returns a human-readable description of the not-leader condition.
func (e *NotLeaderError) Error() string {
	if e.LeaderID == 0 {
		return "service: not leader for shard " + strconv.FormatUint(e.ShardID, 10) + ", leader unknown"
	}
	return "service: not leader for shard " + strconv.FormatUint(e.ShardID, 10) +
		", leader is replica " + strconv.FormatUint(e.LeaderID, 10) +
		" at " + e.LeaderAddress
}

// MembershipError is returned when a membership change operation fails.
type MembershipError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the membership failure.
func (e *MembershipError) Error() string {
	return "service: membership " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *MembershipError) Unwrap() error {
	return e.Err
}

// SnapshotRequestError is returned when a snapshot request fails.
type SnapshotRequestError struct {
	Err error
}

// Error returns a human-readable description of the snapshot request failure.
func (e *SnapshotRequestError) Error() string {
	return "service: snapshot request failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *SnapshotRequestError) Unwrap() error {
	return e.Err
}

// DrainError is returned when a drain operation fails.
type DrainError struct {
	Err error
}

// Error returns a human-readable description of the drain failure.
func (e *DrainError) Error() string {
	return "service: drain failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *DrainError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Shard management errors
// ---------------------------------------------------------------------------

// ShardStartError is returned when starting a Raft shard fails.
type ShardStartError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description of the shard start failure.
func (e *ShardStartError) Error() string {
	return "service: start shard " + strconv.FormatUint(e.ShardID, 10) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ShardStartError) Unwrap() error {
	return e.Err
}

// ShardStopError is returned when stopping a Raft shard fails.
type ShardStopError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description of the shard stop failure.
func (e *ShardStopError) Error() string {
	return "service: stop shard " + strconv.FormatUint(e.ShardID, 10) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ShardStopError) Unwrap() error {
	return e.Err
}

// ShardStateError is returned when querying a shard's Raft state fails.
type ShardStateError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description of the shard state query failure.
func (e *ShardStateError) Error() string {
	return "service: get shard state " + strconv.FormatUint(e.ShardID, 10) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ShardStateError) Unwrap() error {
	return e.Err
}

// TopologyError is returned when querying the cluster topology fails.
type TopologyError struct {
	Err error
}

// Error returns a human-readable description of the topology query failure.
func (e *TopologyError) Error() string {
	return "service: get topology failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *TopologyError) Unwrap() error {
	return e.Err
}

// NoStateMachineFactoryError is returned when StartShard is called but no
// state machine factory has been configured on the AdminService.
type NoStateMachineFactoryError struct{}

// Error returns a human-readable description.
func (e *NoStateMachineFactoryError) Error() string {
	return "service: no state machine factory configured"
}

// ---------------------------------------------------------------------------
// MVCC errors
// ---------------------------------------------------------------------------

// RevisionCompactedError is returned when a read targets a revision that
// has been compacted and is no longer available.
type RevisionCompactedError struct {
	Requested int64
	Compacted int64
}

// Error returns a human-readable description of the compacted revision.
func (e *RevisionCompactedError) Error() string {
	return "service: revision " + strconv.FormatInt(e.Requested, 10) +
		" has been compacted (earliest available: " +
		strconv.FormatInt(e.Compacted, 10) + ")"
}

// RevisionNotFoundError is returned when the requested revision does
// not exist.
type RevisionNotFoundError struct {
	Revision int64
}

// Error returns a human-readable description of the missing revision.
func (e *RevisionNotFoundError) Error() string {
	return "service: revision not found: " + strconv.FormatInt(e.Revision, 10)
}

// ---------------------------------------------------------------------------
// Transaction errors
// ---------------------------------------------------------------------------

// TransactionConflictError is returned when a transaction's compare
// preconditions fail, indicating a concurrent modification.
type TransactionConflictError struct {
	Key string
}

// Error returns a human-readable description of the conflict.
func (e *TransactionConflictError) Error() string {
	return "service: transaction conflict on key: " + e.Key
}

// ---------------------------------------------------------------------------
// Watch errors
// ---------------------------------------------------------------------------

// WatchCanceledError is returned when a watch is canceled due to
// server-side conditions such as compaction past the watcher's revision.
type WatchCanceledError struct {
	Reason string
}

// Error returns a human-readable description of the cancellation.
func (e *WatchCanceledError) Error() string {
	return "service: watch canceled: " + e.Reason
}

// ---------------------------------------------------------------------------
// Lease errors
// ---------------------------------------------------------------------------

// LeaseNotFoundError is returned when an operation references a lease
// that does not exist.
type LeaseNotFoundError struct {
	LeaseID int64
}

// Error returns a human-readable description of the missing lease.
func (e *LeaseNotFoundError) Error() string {
	return "service: lease not found: " + strconv.FormatInt(e.LeaseID, 10)
}

// LeaseExpiredError is returned when an operation targets a lease that
// has already expired.
type LeaseExpiredError struct {
	LeaseID int64
}

// Error returns a human-readable description of the expired lease.
func (e *LeaseExpiredError) Error() string {
	return "service: lease expired: " + strconv.FormatInt(e.LeaseID, 10)
}

// LeaseExistsError is returned when attempting to create a lease with
// an ID that is already in use.
type LeaseExistsError struct {
	LeaseID int64
}

// Error returns a human-readable description of the duplicate lease.
func (e *LeaseExistsError) Error() string {
	return "service: lease already exists: " + strconv.FormatInt(e.LeaseID, 10)
}

// ---------------------------------------------------------------------------
// Batch errors
// ---------------------------------------------------------------------------

// BatchError is returned when a batch operation fails.
type BatchError struct {
	Index int
	Err   error
}

// Error returns a human-readable description of the batch failure.
func (e *BatchError) Error() string {
	return "service: batch operation " + strconv.Itoa(e.Index) +
		" failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BatchError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Index errors
// ---------------------------------------------------------------------------

// IndexNotFoundError is returned when an operation references an index
// that does not exist.
type IndexNotFoundError struct {
	Name string
}

// Error returns a human-readable description of the missing index.
func (e *IndexNotFoundError) Error() string {
	return "service: index not found: " + e.Name
}

// IndexExistsError is returned when attempting to create an index with
// a name that is already in use.
type IndexExistsError struct {
	Name string
}

// Error returns a human-readable description of the duplicate index.
func (e *IndexExistsError) Error() string {
	return "service: index already exists: " + e.Name
}

// ---------------------------------------------------------------------------
// Time-series errors
// ---------------------------------------------------------------------------

// TimeSeriesError is returned when a time-series operation fails.
type TimeSeriesError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the time-series failure.
func (e *TimeSeriesError) Error() string {
	return "service: timeseries " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *TimeSeriesError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Consistency errors
// ---------------------------------------------------------------------------

// ConsistencyError is returned when a read cannot satisfy the requested
// consistency level.
type ConsistencyError struct {
	Level  string
	Reason string
}

// Error returns a human-readable description of the consistency failure.
func (e *ConsistencyError) Error() string {
	return "service: consistency " + e.Level + ": " + e.Reason
}

// ---------------------------------------------------------------------------
// Entity errors
// ---------------------------------------------------------------------------

// EntityEmptyDataError is returned when a PutEntity request contains
// nil or zero-length data.
type EntityEmptyDataError struct {
	EntityType EntityType
	ID         string
}

// Error returns a human-readable description of the empty data error.
func (e *EntityEmptyDataError) Error() string {
	return "service: entity data must not be empty: " + string(e.EntityType) + "/" + e.ID
}

// EntityEmptyIDError is returned when a request contains an empty entity ID.
type EntityEmptyIDError struct {
	EntityType EntityType
}

// Error returns a human-readable description of the empty ID error.
func (e *EntityEmptyIDError) Error() string {
	return "service: entity ID must not be empty for type: " + string(e.EntityType)
}

// EntityEmptyTypeError is returned when a request contains an empty entity type.
type EntityEmptyTypeError struct{}

// Error returns a human-readable description of the empty type error.
func (e *EntityEmptyTypeError) Error() string {
	return "service: entity type must not be empty"
}

// InvalidPageTokenError is returned when a page token cannot be parsed.
type InvalidPageTokenError struct {
	Token string
}

// Error returns a human-readable description of the invalid page token.
func (e *InvalidPageTokenError) Error() string {
	return "service: invalid page token: " + e.Token
}

// ---------------------------------------------------------------------------
// Entity batch/transaction operation errors
// ---------------------------------------------------------------------------

// InvalidEntityOpTypeError is returned when a batch or transaction entity
// operation has an unrecognized type string.
type InvalidEntityOpTypeError struct {
	OpType string
}

// Error returns a human-readable description of the invalid op type.
func (e *InvalidEntityOpTypeError) Error() string {
	return "service: invalid entity operation type: " + e.OpType
}

// BatchEntityEmptyOpsError is returned when a BatchEntityRequest contains
// no operations. This is distinct from the raw batch which allows empty ops.
type BatchEntityEmptyOpsError struct{}

// Error returns a human-readable description.
func (e *BatchEntityEmptyOpsError) Error() string {
	return "service: batch entity request must contain at least one operation"
}

// UnsupportedTransactionOpError is returned when a transaction success or
// failure branch contains an operation type other than Put or Delete.
// A transaction's selected branch is executed atomically as a single Raft
// write; the wire protocol carries only a succeeded flag back from the
// state machine, with no per-operation read-result channel, so branch
// operations are write-only by design. A read (Get, Range) — or any other
// unrecognized OpType — in a branch is a client error and is rejected
// before the transaction is encoded or proposed to Raft, rather than being
// silently coerced into a write. Callers needing a read as part of a CAS
// decision should perform it before calling Execute/ExecuteWithMode (its
// result can feed the transaction's Compare preconditions); reads mixed
// with writes in a single atomic call are supported by BatchServicer.
type UnsupportedTransactionOpError struct {
	OpType OpType
}

// Error returns a human-readable description of the unsupported op type.
func (e *UnsupportedTransactionOpError) Error() string {
	return "service: unsupported transaction operation type " + strconv.Itoa(int(e.OpType)) +
		" (transaction branches support only Put and Delete)"
}

// ---------------------------------------------------------------------------
// Import errors
// ---------------------------------------------------------------------------

// ImportInvalidFormatError is returned when the import format parameter
// is not one of the supported values: "csv", "json", or "archive".
type ImportInvalidFormatError struct {
	Format string
}

// Error returns a human-readable description.
func (e *ImportInvalidFormatError) Error() string {
	return "service: invalid import format: " + e.Format + " (expected csv, json, yaml, ndjson, archive, or auto)"
}

// ImportMissingKeyFieldError is returned when the keyField is empty.
type ImportMissingKeyFieldError struct{}

// Error returns a human-readable description.
func (e *ImportMissingKeyFieldError) Error() string {
	return "service: import keyField is required"
}

// ImportInvalidValueModeError is returned when the valueMode parameter
// is not one of the supported values: "field" or "row".
type ImportInvalidValueModeError struct {
	Mode string
}

// Error returns a human-readable description.
func (e *ImportInvalidValueModeError) Error() string {
	return "service: invalid import valueMode: " + e.Mode + " (expected field or row)"
}

// ImportMissingValueFieldError is returned when valueMode is "field" but
// the valueField parameter is empty.
type ImportMissingValueFieldError struct{}

// Error returns a human-readable description.
func (e *ImportMissingValueFieldError) Error() string {
	return "service: import valueField is required when valueMode is 'field'"
}

// ImportRowError represents a failure during import of a specific row.
type ImportRowError struct {
	Row     int
	Key     string
	Message string
}

// Error returns a human-readable description.
func (e *ImportRowError) Error() string {
	return "service: import row " + strconv.Itoa(e.Row) + " failed: " + e.Message
}

// ImportParseError is returned when the input file cannot be parsed.
type ImportParseError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ImportParseError) Error() string {
	return "service: import parse failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ImportParseError) Unwrap() error {
	return e.Err
}

// ImportTransactionAbortedError is returned when a transactional import
// fails and all operations are rolled back.
type ImportTransactionAbortedError struct {
	Row     int
	Key     string
	Message string
}

// Error returns a human-readable description.
func (e *ImportTransactionAbortedError) Error() string {
	return "service: import aborted at row " + strconv.Itoa(e.Row) +
		" (key=" + e.Key + "): " + e.Message
}

// ImportArchiveError is returned when archive extraction fails.
type ImportArchiveError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ImportArchiveError) Error() string {
	return "service: import archive extraction failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ImportArchiveError) Unwrap() error {
	return e.Err
}

// ImportInputTooLargeError is returned when the raw bytes read from the
// Import file argument exceed maxImportInputBytes. It is the service's own
// defense-in-depth bound, enforced independently of whatever request-size
// cap (if any) the calling transport applies.
type ImportInputTooLargeError struct {
	Max int
}

// Error returns a human-readable description.
func (e *ImportInputTooLargeError) Error() string {
	return "service: import input exceeds maximum size of " + strconv.Itoa(e.Max) + " bytes"
}

// ImportArchiveExpandedTooLargeError is returned when the cumulative
// decompressed bytes produced while extracting a zip/tar/tar.gz archive
// (summed across every member) exceed maxImportExpandedBytes. This guards
// against archive ("zip bomb") inputs that are small on the wire but
// expand to an unbounded amount of memory when decompressed.
type ImportArchiveExpandedTooLargeError struct {
	Max int
}

// Error returns a human-readable description.
func (e *ImportArchiveExpandedTooLargeError) Error() string {
	return "service: import archive decompressed content exceeds maximum expanded size of " + strconv.Itoa(e.Max) + " bytes"
}

// ImportTooManyEntriesError is returned when a zip/tar/tar.gz archive
// contains more entries than maxImportArchiveEntries. This guards against
// archives with a huge number of tiny or empty entries that exhaust CPU
// and memory processing per-entry bookkeeping even while individually
// staying under the expanded-bytes budget.
type ImportTooManyEntriesError struct {
	Max int
}

// Error returns a human-readable description.
func (e *ImportTooManyEntriesError) Error() string {
	return "service: import archive contains more than the maximum of " + strconv.Itoa(e.Max) + " entries"
}

// ImportTooManyRowsError is returned when a parsed import contains more
// records than maxImportRows, regardless of source format. Callers with
// larger datasets should split them across multiple Import calls.
type ImportTooManyRowsError struct {
	Max int
}

// Error returns a human-readable description.
func (e *ImportTooManyRowsError) Error() string {
	return "service: import contains more than the maximum of " + strconv.Itoa(e.Max) + " rows"
}

// ImportProposalTooLargeError is returned when a transactional import's
// total encoded batch command would exceed maxImportProposalBytes, the
// ceiling go-quicraft enforces on any single Raft log entry. Unlike
// non-transactional imports (which are split into multiple sequential
// proposals), a transactional import cannot be chunked without breaking
// its all-or-nothing guarantee, so it is rejected outright instead.
type ImportProposalTooLargeError struct {
	Max int
}

// Error returns a human-readable description.
func (e *ImportProposalTooLargeError) Error() string {
	return "service: transactional import's encoded batch command exceeds the maximum proposal size of " +
		strconv.Itoa(e.Max) + " bytes; split the import into smaller, non-transactional batches"
}

// ---------------------------------------------------------------------------
// Barrier errors
// ---------------------------------------------------------------------------

// BarrierNotConfiguredError is returned when a barrier operation is
// attempted but no barrier has been configured on the service.
type BarrierNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *BarrierNotConfiguredError) Error() string {
	return "service: barrier service not configured"
}

// ErrCeremonyNotConfigured is returned when a Security Officer operation is
// attempted but no CeremonyService has been wired into the BarrierService.
// Use NewBarrierServiceWithCeremony to enable officer registration and share
// claim operations.
var ErrCeremonyNotConfigured = &ceremonyNotConfiguredError{}

// ceremonyNotConfiguredError is the concrete type behind ErrCeremonyNotConfigured.
type ceremonyNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *ceremonyNotConfiguredError) Error() string {
	return "service: ceremony service not configured; use NewBarrierServiceWithCeremony"
}

// BarrierManagerNotConfiguredError is returned when a barrier operation is
// attempted but no DatabaseBarrierManager has been configured on the
// database service.
type BarrierManagerNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *BarrierManagerNotConfiguredError) Error() string {
	return "service: database barrier manager not configured"
}

// InvalidRaftTopologyError is returned when a raft-backed database's
// requested Raft cluster-mode topology is internally incoherent (e.g.
// Backend == BackendRaft paired with ClusterMode == RaftNone). See
// DatabaseService.validateRaftTopology.
type InvalidRaftTopologyError struct {
	Database string
	Mode     string
	Reason   string
}

// Error returns a human-readable description.
func (e *InvalidRaftTopologyError) Error() string {
	return "service: database " + e.Database + ": invalid raft topology " + e.Mode + ": " + e.Reason
}

// RaftDistributedUnavailableError is returned when a raft-backed database
// requests the "distributed" Raft cluster mode but this node has no
// placement subsystem configured (WithPlacementStore + WithPlacementScheduler)
// to compute and materialize multi-node replica placement. The request fails
// closed rather than silently collapsing to a single local replica; retry
// against a node in a clustered deployment with the placement subsystem
// wired, or request cluster mode "local" instead.
type RaftDistributedUnavailableError struct {
	Database string
}

// Error returns a human-readable description.
func (e *RaftDistributedUnavailableError) Error() string {
	return "service: database " + e.Database + ": distributed raft topology requires the placement subsystem " +
		"(placement store + scheduler) to be configured on this node"
}

// ReplicaFloorError is returned by ScaleForTenant when the requested target
// replication factor is less than 1. Scaling to zero voting replicas is not a
// valid operation -- use DropForTenant to remove the database entirely.
type ReplicaFloorError struct {
	Database  string
	Requested int
}

// Error returns a human-readable description.
func (e *ReplicaFloorError) Error() string {
	return "service: database " + e.Database + ": target replication factor " + strconv.Itoa(e.Requested) +
		" is below the minimum of 1; use DropForTenant to remove a database"
}

// NotDedicatedRaftDatabaseError is returned by ScaleForTenant when the named
// database has no entry in the replicated placement table: it predates the
// placement subsystem, or it was created with Raft cluster mode "local"
// (single, non-distributed replica) rather than "distributed". Only a
// dedicated-raft database created with RaftDistributed (and a placement
// subsystem wired) can have its replication factor scaled.
type NotDedicatedRaftDatabaseError struct {
	Database string
}

// Error returns a human-readable description.
func (e *NotDedicatedRaftDatabaseError) Error() string {
	return "service: database " + e.Database + ": not a distributed dedicated-raft database (no placement entry); " +
		"scaling requires raft cluster mode \"distributed\""
}

// ShardLeaderTimeoutError is returned by ScaleForTenant's scaleUp/scaleDown
// when the dedicated shard does not report a known Raft leader within the
// bounded wait applied before issuing a membership change. AddFollowerNode
// and RemoveFollowerNode are hard-rejected by go-quicraft until the shard's
// leader is known (this is common at RF=1, where the sole replica has not
// necessarily campaigned and won its own single-voter election yet) --
// waitForShardLeaderKnown polls for that election to complete instead of
// racing it, and returns this typed error if it never stabilizes in time so
// the caller fails closed rather than proceeding to a membership-change call
// that is certain to be rejected.
type ShardLeaderTimeoutError struct {
	ShardID uint64
	Timeout time.Duration
}

// Error returns a human-readable description.
func (e *ShardLeaderTimeoutError) Error() string {
	return "service: shard " + strconv.FormatUint(e.ShardID, 10) +
		": no raft leader known after " + e.Timeout.String()
}

// LeaderTransferTimeoutError is returned by
// transferLeadershipAwayFromRemoved when a requested leadership transfer
// (issued so a replica about to be removed is not still the shard's leader
// when RemoveFollowerNode is called -- see that function's doc comment for
// the race this avoids) does not complete within the bounded wait.
type LeaderTransferTimeoutError struct {
	ShardID       uint64
	FromReplicaID uint64
	Timeout       time.Duration
}

// Error returns a human-readable description.
func (e *LeaderTransferTimeoutError) Error() string {
	return "service: shard " + strconv.FormatUint(e.ShardID, 10) +
		": leadership transfer away from replica " + strconv.FormatUint(e.FromReplicaID, 10) +
		" did not complete after " + e.Timeout.String()
}

// CompactSystemOnlyError is returned when a non-system tenant attempts an
// MVCC Compact. MVCC revisions are a store-global monotonic counter shared by
// every prefix-isolated tenant on the store, so compacting to revision N
// discards the historical revisions below N for EVERY tenant's keys, not just
// the caller's. Compaction is therefore an inherently store-global admin
// operation reserved for the system tenant. This is the service-layer
// (data-plane) defense; the mvcc:compact permission is additionally tagged
// ScopeCross so the RBAC authorizer denies it to tenant-local identities. It
// maps to 403 Forbidden on REST and codes.PermissionDenied on gRPC/QUIC.
type CompactSystemOnlyError struct {
	// TenantID is the non-system caller that was refused.
	TenantID string
}

// Error returns a human-readable description.
func (e *CompactSystemOnlyError) Error() string {
	return "service: tenant " + e.TenantID + " is not permitted to compact MVCC revisions; " +
		"compaction is store-global and reserved for the system tenant"
}

// ---------------------------------------------------------------------------
// Codec errors
// ---------------------------------------------------------------------------

// CodecEncodeError is returned when the codec manager fails to encode a
// value for storage.
type CodecEncodeError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *CodecEncodeError) Error() string {
	return "service: codec encode failed for " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CodecEncodeError) Unwrap() error {
	return e.Err
}

// CodecDecodeError is returned when the codec manager fails to decode or
// transcode a value from storage.
type CodecDecodeError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *CodecDecodeError) Error() string {
	return "service: codec decode failed for " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CodecDecodeError) Unwrap() error {
	return e.Err
}
