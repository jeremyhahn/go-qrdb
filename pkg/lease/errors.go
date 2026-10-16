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

package lease

import "strconv"

// NotFoundError is returned when a lease ID does not exist.
type NotFoundError struct {
	ID int64
}

func (e *NotFoundError) Error() string {
	return "lease not found: " + strconv.FormatInt(e.ID, 10)
}

// ExistsError is returned when attempting to grant a lease with an ID
// that is already in use.
type ExistsError struct {
	ID int64
}

func (e *ExistsError) Error() string {
	return "lease already exists: " + strconv.FormatInt(e.ID, 10)
}

// InvalidTTLError is returned when a lease TTL is invalid.
type InvalidTTLError struct {
	TTL int64
}

func (e *InvalidTTLError) Error() string {
	return "invalid lease TTL: " + strconv.FormatInt(e.TTL, 10) + " (must be > 0)"
}

// LessorClosedError is returned when an operation is attempted on a
// closed lessor.
type LessorClosedError struct{}

func (e *LessorClosedError) Error() string {
	return "lessor is closed"
}

// NotPrimaryError is returned by RenewLocal when this replica is not the one
// that may renew leases in memory: it is not the Raft leader, or its lessor is
// not running the expiry loop with checkpointing. It is the instruction to
// renew through the replicated log instead, not a failure of the lease.
type NotPrimaryError struct{}

func (e *NotPrimaryError) Error() string {
	return "lease renewal: this replica is not the lease primary"
}

// StaleTTLError is returned by TimeToLive on a replica that holds the lease
// but does not lead its group. The lease primary renews leases in its own
// memory, and those renewals reach no other replica, so the remaining TTL this
// replica could report would understate the lease; only the primary's is
// current, and the query must be answered there. Lease is this replica's view:
// that the lease exists and the keys attached to it are current, both being
// replicated, while its remaining TTL is not.
type StaleTTLError struct {
	Lease *Lease
}

func (e *StaleTTLError) Error() string {
	return "lease time-to-live: lease " + strconv.FormatInt(e.Lease.ID, 10) + " is held here, but only the lease primary knows its remaining TTL"
}

// CheckpointedError is returned by RenewLocal when lease ID has a checkpoint
// recorded, or one in flight, that a renewal changes. A renewal in memory
// would leave that checkpoint claiming less time than the lease now has, so
// the renewal must go through the replicated log, whose apply clears it. Like
// NotPrimaryError it is an instruction, not a failure of the lease.
type CheckpointedError struct {
	ID int64
}

func (e *CheckpointedError) Error() string {
	return "lease renewal: lease " + strconv.FormatInt(e.ID, 10) + " is checkpointed; renew through the log"
}

// ApplyIndexUnsetError is returned by ApplyCheckpoints when the state machine
// applying the checkpoint has not reported the Raft index it is applying
// through Applying. The index is what a checkpoint is judged against, so a
// checkpoint applied without one is refused rather than judged against zero.
type ApplyIndexUnsetError struct{}

func (e *ApplyIndexUnsetError) Error() string {
	return "lease checkpoint: the applied Raft index is not set"
}

// CheckpointsDisabledError is returned by an on-demand checkpoint (see
// RequestCheckpoint) when the lessor's Checkpointer reports that checkpoints
// may not be proposed yet, because not every replica of the group can apply
// them.
type CheckpointsDisabledError struct{}

func (e *CheckpointsDisabledError) Error() string {
	return "lease checkpoint: checkpoints are not enabled for this group"
}

// TableWriteError is returned when a lease-table mutation could not be staged
// into the apply batch. It is a storage fault, not a refusal: the state
// machine must surface it as an apply failure, never as a per-command answer,
// because a fault local to one replica decided as a refusal would let that
// replica diverge from peers whose write succeeded.
type TableWriteError struct {
	Err error
}

func (e *TableWriteError) Error() string {
	return "lease table write failed: " + e.Err.Error()
}

// Unwrap returns the underlying storage error.
func (e *TableWriteError) Unwrap() error { return e.Err }

// TableReadError is returned when the lease table could not be read back
// from storage while restoring the Lessor.
type TableReadError struct {
	Err error
}

func (e *TableReadError) Error() string {
	return "lease table read failed: " + e.Err.Error()
}

// Unwrap returns the underlying storage error.
func (e *TableReadError) Unwrap() error { return e.Err }

// CorruptTableError is returned when a persisted lease-table record does not
// decode, or contradicts the rest of the table.
type CorruptTableError struct {
	// Key is the offending record's raw key.
	Key string

	// Reason says what is wrong with it.
	Reason string
}

func (e *CorruptTableError) Error() string {
	return "lease table corrupt at " + strconv.Quote(e.Key) + ": " + e.Reason
}
