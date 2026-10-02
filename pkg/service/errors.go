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
	"errors"
	"strconv"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// The state machine refused a committed command by encoding the refusal into
// result.Data rather than failing the apply (which would halt lastApplied and
// stall every read and write on the group). protocol.AppErrorKind says what
// kind of refusal it is, and each kind has its own typed error below, so a
// caller — and every transport — can tell an absent subsystem from a bad TTL
// from a taken key without reading message text (go-qrdb#141, go-qrdb#557).
//
// Every one of them reports ErrNotStored: the entry committed and changed
// nothing.

// refusalErrors maps each refusal kind to its typed error. A map rather than a
// switch: the kinds are values, the dispatch is a lookup, and a kind added to
// protocol without a row here is caught by TestCheckResultError_EveryKind.
var refusalErrors = map[protocol.AppErrorKind]func(msg string) error{
	protocol.AppErrorCapability:  func(msg string) error { return &CapabilityUnsupportedError{Message: msg} },
	protocol.AppErrorInvalid:     func(msg string) error { return &CommandRejectedError{Message: msg} },
	protocol.AppErrorConflict:    func(msg string) error { return &CommandConflictError{Message: msg} },
	protocol.AppErrorUnavailable: func(msg string) error { return &SubsystemClosedError{Message: msg} },
	protocol.AppErrorFault:       func(msg string) error { return &ApplyFaultError{Message: msg} },
	protocol.AppErrorCorrupt:     func(msg string) error { return &CommandCorruptError{Message: msg} },
}

// checkResultError inspects result.Data for a state-machine-encoded application
// error (see protocol.EncodeAppError) and returns the typed error for its
// kind. The discriminated prefix is required so ordinary result payloads —
// notably the one-byte transaction success flag — are never misread as errors.
//
// A refused write guard (OpGuarded) is decoded FIRST, ahead of the generic
// kind dispatch below: its payload carries the key of the compare that
// failed, which guardRefusalError needs to tell a refused drop fence (a
// *database.DroppingError, the database is being dropped) apart from any
// other caller-supplied guard (*WriteGuardRefusedError) -- reading it as a
// generic AppErrorGuardRefused would lose which precondition failed.
func checkResultError(result sm.Result) error {
	if key, refused := protocol.DecodeGuardRefusal(result.Data); refused {
		return guardRefusalError(key)
	}
	// A write naming a lease the state machine does not hold is answered with
	// the same typed not-found error the pre-proposal lease check returns, so
	// a caller sees one error whichever side of the proposal caught it.
	if id, nf := protocol.DecodeLeaseNotFoundError(result.Data); nf {
		return &LeaseNotFoundError{LeaseID: id}
	}
	msg, kind, ok := protocol.DecodeAppErrorKind(result.Data)
	if !ok {
		return nil
	}
	return refusalErrors[kind](msg)
}

// CommandRejectedError reports a command the state machine understood and
// refused on its merits: an invalid lease TTL, a malformed time-series point.
// The request is the caller's to fix.
type CommandRejectedError struct {
	// Message is the refusal the state machine encoded into result.Data.
	Message string
}

// Error returns a human-readable description.
func (e *CommandRejectedError) Error() string { return "service: " + e.Message }

// Is reports the ErrNotStored marker: the command changed nothing.
func (e *CommandRejectedError) Is(target error) bool { return target == ErrNotStored }

// CommandConflictError reports a command that would have violated a
// uniqueness constraint: a unique-index value or a lease ID already taken.
type CommandConflictError struct {
	// Message is the refusal the state machine encoded into result.Data.
	Message string
}

// Error returns a human-readable description.
func (e *CommandConflictError) Error() string { return "service: " + e.Message }

// Is reports the ErrNotStored marker: the command changed nothing.
func (e *CommandConflictError) Is(target error) bool { return target == ErrNotStored }

// SubsystemClosedError reports a command that reached a subsystem already
// closed on this replica (a closed lessor or time-series store). Retry once
// the node is serving again.
type SubsystemClosedError struct {
	// Message is the refusal the state machine encoded into result.Data.
	Message string
}

// Error returns a human-readable description.
func (e *SubsystemClosedError) Error() string { return "service: " + e.Message }

// Is reports the ErrNotStored marker: the command changed nothing.
func (e *SubsystemClosedError) Is(target error) bool { return target == ErrNotStored }

// ApplyFaultError reports a subsystem that failed while applying a command,
// for a reason the request did not cause.
type ApplyFaultError struct {
	// Message is the refusal the state machine encoded into result.Data.
	Message string
}

// Error returns a human-readable description.
func (e *ApplyFaultError) Error() string { return "service: " + e.Message }

// Is reports the ErrNotStored marker: the command changed nothing.
func (e *ApplyFaultError) Is(target error) bool { return target == ErrNotStored }

// CommandCorruptError reports a committed command the state machine could not
// decode: the log holds bytes no writer of this version produced. A retry of
// the same request is not the remedy.
type CommandCorruptError struct {
	// Message is the refusal the state machine encoded into result.Data.
	Message string
}

// Error returns a human-readable description.
func (e *CommandCorruptError) Error() string { return "service: " + e.Message }

// Is reports the ErrNotStored marker: the command changed nothing.
func (e *CommandCorruptError) Is(target error) bool { return target == ErrNotStored }

// WriteGuardError reports that a WriteGuard could not be built or applied to
// a command: a compare that cannot be encoded, a target a guard cannot
// evaluate on every state machine, or a command that is empty or already
// guarded. It is raised before anything is proposed.
type WriteGuardError struct {
	// Err is the underlying encoding or validation failure.
	Err error
}

// Error returns a human-readable description.
func (e *WriteGuardError) Error() string {
	return "service: write guard: " + e.Err.Error()
}

// Unwrap returns the underlying failure.
func (e *WriteGuardError) Unwrap() error {
	return e.Err
}

// Is reports the ErrNotStored marker: the command was never proposed.
func (e *WriteGuardError) Is(target error) bool {
	return target == ErrNotStored
}

// WriteGuardRefusedError reports that a write's guard did not hold when the
// state machine applied it, so the write changed nothing. A refused drop
// fence is reported as *database.DroppingError instead; this is every other
// guard.
type WriteGuardRefusedError struct {
	// Key is the key of the compare that did not hold.
	Key string
}

// Error returns a human-readable description.
func (e *WriteGuardRefusedError) Error() string {
	return "service: write guard refused on key " + strconv.Quote(e.Key)
}

// Is reports the ErrNotStored marker: the entry committed and applied
// nothing.
func (e *WriteGuardRefusedError) Is(target error) bool {
	return target == ErrNotStored
}

// CapabilityUnsupportedError reports that the state machine serving this
// request was not built to perform the operation: an opcode it has no handler
// for, or a subsystem it was constructed without.
//
// It is deliberately distinct from CommandRejectedError and
// CommandConflictError, which report outcomes refused on their merits — an
// invalid lease TTL, a unique-index violation. Those are client errors about
// the REQUEST; this one is about the NODE, and a caller wanting to fall back to
// a different verb needs to tell them apart without reading message text
// (go-qrdb#141). The entry committed and changed nothing.
type CapabilityUnsupportedError struct {
	// Message is the error string the state machine encoded into result.Data.
	Message string
}

// Error returns a human-readable description.
func (e *CapabilityUnsupportedError) Error() string {
	return "service: " + e.Message
}

// Is reports the ErrNotStored marker: the command reached consensus and stored
// nothing.
func (e *CapabilityUnsupportedError) Is(target error) bool {
	return target == ErrNotStored
}

// KeyNotFoundError is returned when a KV lookup finds no matching key.
type KeyNotFoundError struct {
	Key string
}

// Error returns a human-readable description of the missing key.
func (e *KeyNotFoundError) Error() string {
	return "service: key not found: " + e.Key
}

// EncodeRequestError is returned when a request cannot be encoded into a
// command, so no proposal was ever made.
//
// It exists because the alternative was a lie with consequences. Encoding used
// to be infallible; now that the encoders refuse a field longer than its length
// field can describe (go-qrdb#52), every one of those call sites had a new
// failure to report and reported it as ProposeError — "the Raft proposal
// failed" — for a proposal that had not been attempted. The transports map
// ProposeError to gRPC Internal and HTTP 500, so a caller sending an oversized
// key was told the cluster was broken: an alert fires, and a client that retries
// on 5xx retries an input that cannot ever succeed.
//
// This is a CLIENT error. The transports map it to InvalidArgument and 400.
type EncodeRequestError struct {
	Err error
}

// Error returns a human-readable description of the encoding failure.
func (e *EncodeRequestError) Error() string {
	return "service: cannot encode request: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *EncodeRequestError) Unwrap() error {
	return e.Err
}

// Is reports the ErrNotStored marker: a request that could not be encoded was
// never proposed, so nothing was stored.
func (e *EncodeRequestError) Is(target error) bool {
	return target == ErrNotStored
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

// Is reports the ErrOutcomeUnknown marker.
//
// This is the one write failure that genuinely may have landed: SyncPropose
// can commit the entry and then fail to deliver its response, so the value is
// durable and the caller was told nothing. Every other error out of a
// proposal is raised before the entry can commit and marks ErrNotStored
// instead.
func (e *ProposeError) Is(target error) bool {
	return target == ErrOutcomeUnknown
}

// translateProposeError converts a QuicRaft proposal error into the service
// layer's typed vocabulary: a not-leader refusal keeps its routing detail as
// NotLeaderError, and every other failure is a ProposeError.
//
// It is the one translation every proposing service uses. Seven services each
// used to carry an identical method of their own, and the vector service,
// which carried none, reported every proposal failure through an untyped code
// no transport mapped.
func translateProposeError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ProposeError{Err: err}
}

// SubsystemNotConfiguredError is returned when a request reaches a state
// machine that was built without the subsystem the request needs. It is the
// service-layer spelling of statemachine.ServiceNotConfiguredError, translated
// at this boundary for the same reason NotLeaderError is: a caller above the
// service layer should not have to reach into the state machine's error types
// to tell a missing capability apart from a genuine failure.
//
// The distinction matters because some subsystems are optional accelerators.
// A node built on the "memory" state machine has no index subsystem at all, so
// asking it for the indexes on a table is not an error condition -- the honest
// answer is that there are none, and the query planner should scan. Reported
// as an opaque read failure instead, it failed every SQL query carrying a
// WHERE clause on such a node.
type SubsystemNotConfiguredError struct {
	Subsystem string
}

// Error returns a human-readable description of the missing subsystem.
func (e *SubsystemNotConfiguredError) Error() string {
	return "service: " + e.Subsystem + " subsystem not configured on this state machine"
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

// Is reports the ErrNotStored marker: a node that is not the leader cannot
// commit the entry, so the proposal was refused rather than applied.
func (e *NotLeaderError) Is(target error) bool {
	return target == ErrNotStored
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

// UserRangeShardError is returned when an admin start names a shard ID in the
// user range. The cluster issues those IDs to databases (shard.Claimer); a
// group started on one by hand could hold an ID the claimer gives a database.
type UserRangeShardError struct {
	ShardID uint64
}

// Error returns a human-readable description of the refusal.
func (e *UserRangeShardError) Error() string {
	return "service: shard " + strconv.FormatUint(e.ShardID, 10) +
		" is in the user range, which the cluster issues to databases; start a shard below " +
		strconv.FormatUint(shard.UserMin, 10)
}

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

// Is reports the ErrNotStored marker: the request was rejected on validation,
// before anything was proposed.
func (e *EntityEmptyDataError) Is(target error) bool {
	return target == ErrNotStored
}

// EntityEmptyIDError is returned when a request contains an empty entity ID.
type EntityEmptyIDError struct {
	EntityType EntityType
}

// Error returns a human-readable description of the empty ID error.
func (e *EntityEmptyIDError) Error() string {
	return "service: entity ID must not be empty for type: " + string(e.EntityType)
}

// Is reports the ErrNotStored marker: the request was rejected on validation,
// before anything was proposed.
func (e *EntityEmptyIDError) Is(target error) bool {
	return target == ErrNotStored
}

// EntityEmptyTypeError is returned when a request contains an empty entity type.
type EntityEmptyTypeError struct{}

// Error returns a human-readable description of the empty type error.
func (e *EntityEmptyTypeError) Error() string {
	return "service: entity type must not be empty"
}

// Is reports the ErrNotStored marker: the request was rejected on validation,
// before anything was proposed.
func (e *EntityEmptyTypeError) Is(target error) bool {
	return target == ErrNotStored
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

// ErrNodeClosed is returned by an operation that needs a node which Close has
// begun or finished tearing down — an Unseal, or an encryption migration
// refused rather than queued behind Close. The node is going away underneath
// the request, not refusing it, so every transport answers it as retryable:
// 503 on REST, Unavailable on gRPC, unix, QUIC and MCP. Match it with
// errors.Is.
var ErrNodeClosed error = &nodeClosedError{}

// nodeClosedError is the concrete type behind ErrNodeClosed.
type nodeClosedError struct{}

// Error returns a human-readable description.
func (e *nodeClosedError) Error() string { return "service: node closed" }

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

// InvalidKeyError is returned, before anything is proposed, for a transaction
// compare or a transaction or batch operation that names an empty key. The
// state machines refuse the same request at apply; this answers it without a
// round through consensus. Callers see it wrapped in EncodeRequestError, which
// every transport reports as an invalid argument.
type InvalidKeyError struct {
	// Field is what named the key: "compare" or "op".
	Field string
	// Index is the compare's position, when Field is "compare".
	Index int
}

// Error returns a human-readable description of the refused key.
func (e *InvalidKeyError) Error() string {
	if e.Field == "compare" {
		return "service: compare " + strconv.Itoa(e.Index) + " names an empty key"
	}
	return "service: " + e.Field + " names an empty key"
}
