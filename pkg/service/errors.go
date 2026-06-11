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

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
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
