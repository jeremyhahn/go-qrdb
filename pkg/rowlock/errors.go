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

package rowlock

import (
	"strconv"
	"time"
)

// SQLCodeLockWaitTimeout is MySQL's ER_LOCK_WAIT_TIMEOUT, the number a
// LockWaitTimeoutError reaches a MySQL client as.
const SQLCodeLockWaitTimeout = 1205

// SQLCodeLockNowait is MySQL's ER_LOCK_NOWAIT, the number a
// LockNotAvailableError reaches a MySQL client as.
const SQLCodeLockNowait = 3572

// LockWaitTimeoutError reports a lock request that waited for its whole
// lock-wait timeout without being granted. Only the request fails: locks the
// owner already held stay held, and the request's own waiting records have
// been withdrawn so it no longer blocks anyone queued behind it.
type LockWaitTimeoutError struct {
	// Key is the locked key the request was still waiting for.
	Key string

	// Mode is the mode the request asked for.
	Mode Mode

	// Wait is the timeout that elapsed.
	Wait time.Duration
}

// Error returns MySQL's own wording, with the elapsed timeout.
func (e *LockWaitTimeoutError) Error() string {
	return "rowlock: lock wait timeout exceeded after " + e.Wait.String() + "; try restarting transaction"
}

// LockNotAvailableError reports a NOWAIT request whose key was held by
// another owner. Like a timeout, only the request fails.
type LockNotAvailableError struct {
	// Key is the locked key that was not immediately available.
	Key string

	// Mode is the mode the request asked for.
	Mode Mode
}

// Error returns a human-readable description.
func (e *LockNotAvailableError) Error() string {
	return "rowlock: statement aborted because lock(s) could not be acquired immediately and NOWAIT is set"
}

// DeadlockError reports that the request closed a cycle in the wait-for
// graph, or that the lock table was too large to search for one, and was
// chosen as the victim. The owner has already been released: every lock it
// held is gone, and its transaction must be rolled back.
type DeadlockError struct {
	// Key is the key whose wait closed the cycle.
	Key string

	// Mode is the mode the request asked for.
	Mode Mode

	// Unsearched is true when no cycle was found because the lock table
	// held more than MaxDetectionRecords records, so the requester was made
	// the victim rather than wait on a graph that was not fully read.
	Unsearched bool
}

// Error returns a human-readable description.
func (e *DeadlockError) Error() string {
	if e.Unsearched {
		return "rowlock: deadlock assumed: the lock table holds more than " +
			strconv.Itoa(MaxDetectionRecords) + " records; try restarting transaction"
	}
	return "rowlock: deadlock found when trying to get lock; try restarting transaction"
}

// LockLostError reports that one of the owner's own lock records disappeared
// while the owner still needed it: its lease expired (a long pause, a lost
// node) or the database's namespace was reclaimed. The owner has been
// released and its transaction must be rolled back.
type LockLostError struct {
	// Key is the key whose record was lost, when one is known.
	Key string
}

// Error returns a human-readable description.
func (e *LockLostError) Error() string {
	return "rowlock: this transaction's lock lease was lost; try restarting transaction"
}

// ScopeMismatchError reports an Acquire against a Table of a different
// (tenant, database) scope than the one the owner is bound to. An owner's
// locks live in one store; a transaction that spans two cannot be detected
// for deadlocks as a whole, so it is refused before anything is written.
type ScopeMismatchError struct {
	// Bound is the scope the owner first locked in.
	Bound string

	// Requested is the scope of the refused request.
	Requested string
}

// Error returns a human-readable description.
func (e *ScopeMismatchError) Error() string {
	return "rowlock: owner is bound to " + e.Bound + " and cannot lock in " + e.Requested
}

// OwnerReleasedError reports an Acquire on an owner that has already been
// released -- its transaction ended, or it was a deadlock victim.
type OwnerReleasedError struct{}

// Error returns a human-readable description.
func (e *OwnerReleasedError) Error() string {
	return "rowlock: the lock owner has been released"
}

// InvalidModeError reports a Mode that is neither ModeShared nor
// ModeExclusive.
type InvalidModeError struct {
	// Mode is the rejected value.
	Mode Mode
}

// Error returns a human-readable description.
func (e *InvalidModeError) Error() string {
	return "rowlock: invalid lock mode " + strconv.Itoa(int(e.Mode))
}

// MissingDependencyError reports a Table constructed without one of the
// servicers it locks through.
type MissingDependencyError struct {
	// Name names the missing dependency.
	Name string
}

// Error returns a human-readable description.
func (e *MissingDependencyError) Error() string {
	return "rowlock: missing dependency: " + e.Name
}

// CorruptRecordError reports a record under the lock prefix whose key or
// value this version cannot parse. It is never written by this package; its
// presence means the keyspace was written by something else.
type CorruptRecordError struct {
	// Key is the offending record key.
	Key string
}

// Error returns a human-readable description.
func (e *CorruptRecordError) Error() string {
	return "rowlock: unparseable lock record " + strconv.Quote(e.Key)
}
