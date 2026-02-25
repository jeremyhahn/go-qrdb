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

import "strconv"

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
