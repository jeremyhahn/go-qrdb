// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package user

import (
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// UserNotFoundError is returned when a user does not exist. For a lookup by
// ID, ID carries the numeric identifier. For a lookup by a non-numeric key
// (username, email or cert thumbprint), Lookup carries a human-readable
// descriptor of the key that failed to resolve and ID is zero.
//
// It IS the SDK's not-found type -- an alias of transport.UserNotFoundError --
// so a consumer that holds a UserServicer through sdk/go classifies a
// not-found with the one exported type whether the call ran in-process or
// crossed any wire (go-qrdb#343). Before this, the service returned its own
// type and the SDK exported the transport's, and a servicer consumer that
// honoured the boundary had no exported name to match. The dependency
// direction is the established one: root packages import sdk/go/transport,
// which imports nothing from pkg/.
type UserNotFoundError = transport.UserNotFoundError

// UsernameExistsError is returned when a username is already taken
// within the tenant.
type UsernameExistsError struct {
	Username string
	TenantID string
}

// Error returns a human-readable description of the conflict.
func (e *UsernameExistsError) Error() string {
	return "user: username already exists: " + e.Username + " in tenant " + e.TenantID
}

// EmailExistsError is returned when an email login key is already taken within
// the tenant. It is distinct from UsernameExistsError because the two handles
// are independently unique: a caller that collides on one needs to know which.
type EmailExistsError struct {
	Email    string
	TenantID string
}

// Error returns a human-readable description of the conflict.
func (e *EmailExistsError) Error() string {
	return "user: email already exists: " + e.Email + " in tenant " + e.TenantID
}

// UserDisabledError is returned when operating on a disabled user.
type UserDisabledError struct {
	ID uint64
}

// Error returns a human-readable description of the disabled condition.
func (e *UserDisabledError) Error() string {
	return "user: disabled: " + strconv.FormatUint(e.ID, 10)
}

// SystemUserImmutableError is returned when trying to delete a system user.
type SystemUserImmutableError struct {
	ID uint64
}

// Error returns a human-readable description of the immutability constraint.
func (e *SystemUserImmutableError) Error() string {
	return "user: system user cannot be deleted: " + strconv.FormatUint(e.ID, 10)
}

// StoreError is returned for store-level operation failures.
type StoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description of the store failure.
func (e *StoreError) Error() string {
	msg := "user: store " + e.Op + " failed"
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StoreError) Unwrap() error { return e.Err }

// ValidationError is returned when request validation fails.
type ValidationError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "user: validation failed: " + e.Field + ": " + e.Reason
}

// CertBindingConflictError is returned when a caller attempts to bind a
// certificate thumbprint that is already bound to a different user. A
// thumbprint resolves to exactly one identity, so silently overwriting the
// existing mapping would let a caller hijack another user's mTLS identity.
type CertBindingConflictError struct {
	Thumbprint   string
	BoundUserID  uint64
	TargetUserID uint64
}

// Error returns a human-readable description of the binding conflict. The
// thumbprint is included to aid operators; it is a public-key hash, not a
// secret.
func (e *CertBindingConflictError) Error() string {
	return "user: cert thumbprint " + e.Thumbprint + " is already bound to user " + strconv.FormatUint(e.BoundUserID, 10)
}

// IndexCompensationError reports that an operation failed AND its rollback of
// the secondary indexes did not fully succeed.
//
// The two facts are separate and the second is the dangerous one: a failed
// update leaves the caller's intent unapplied, which is recoverable, while a
// failed rollback can leave the canonical login key pointing somewhere the
// primary record does not agree with — an account that may be unreachable, or
// an address reserved against a record that never adopted it.
//
// It wraps the original failure (Unwrap returns Cause) so callers matching on
// the underlying error keep working, while a caller that cares about store
// consistency can detect this type and escalate rather than treating it as an
// ordinary failed write.
type IndexCompensationError struct {
	Op       string
	UserID   uint64
	Cause    error
	Failures []error
}

// Error returns a human-readable description naming both facts.
func (e *IndexCompensationError) Error() string {
	return "user: " + e.Op + " failed for id " + strconv.FormatUint(e.UserID, 10) +
		" and index rollback was incomplete (" + strconv.Itoa(len(e.Failures)) +
		" cleanup failure(s)): " + e.Cause.Error()
}

// Unwrap returns the original failure, so errors.Is/As against the underlying
// cause still match.
func (e *IndexCompensationError) Unwrap() error { return e.Cause }

// ErrStoreNotAtomic is returned when a user store is asked to claim a unique
// handle (username or email) but its KVStore cannot do so atomically.
//
// It FAILS CLOSED rather than degrading to a Get-then-Put. Two callers can both
// observe a key absent in the window between the read and the write, so a
// non-atomic "check" is a uniqueness guarantee that silently is not one — and
// for a login key the loser is left as an enabled user whose handle resolves to
// somebody else's record. Refusing is the honest outcome; every production
// KVStore behind a user store implements kvstore.AtomicKVStore.
var ErrStoreNotAtomic = errors.New("user: store cannot claim unique handles atomically")

// HandleReclaimedError reports that a compensation could not put a released
// username or email key back, because another caller has legitimately claimed
// it in the meantime.
//
// It is deliberately a failure rather than a silent overwrite. Between a handle
// being released and a rollback running, the key is genuinely free, and whoever
// took it holds a live record that answers to it. Restoring over that winner
// would leave their account unreachable by its own unique handle — trading one
// caller's failed update for another caller's outage.
type HandleReclaimedError struct {
	Key string
}

// Error returns a human-readable description of the lost race.
func (e *HandleReclaimedError) Error() string {
	return "user: handle " + e.Key + " was reclaimed before rollback could restore it"
}

// ConcurrentUpdateError reports that another update committed to the same user
// record between this call's read and its write, so this call did not apply.
//
// It is a distinct outcome from a store failure: nothing is wrong, the caller
// simply lost a race and its view of the record is stale. Retrying after a fresh
// read is the correct response, which is why it is typed rather than folded into
// StoreError.
//
// It exists because an unconditional write let two concurrent updates of one
// user both claim distinct handles and both commit — leaving the loser's handle
// as a live alias resolving to a record whose canonical value is different.
type ConcurrentUpdateError struct {
	ID uint64
}

// Error returns a human-readable description of the lost race.
func (e *ConcurrentUpdateError) Error() string {
	return "user: concurrent update to user " + strconv.FormatUint(e.ID, 10) + "; re-read and retry"
}

// CommitOutcomeUnknownError reports that a conditional commit returned an error,
// so it is not known whether the write landed.
//
// kvstore.AtomicKVStore states this explicitly: a non-nil error from
// CompareAndSwap means the outcome is undefined and the caller must not assume
// either way. For a record whose secondary indexes were already moved, that is
// the difference between a rollback and a corruption — if the commit did land,
// undoing the index moves leaves the login key resolving to the OLD address
// while the record carries the new one.
//
// So this error exists to stop the store guessing. Compensation stands down and
// the caller is told the state needs reconciling rather than being handed a
// confident "it failed" that may be false. The residue is recoverable: a handle
// may be claimed by a record that did not adopt it, which a reconcile can
// release. A reverted commit is not recoverable.
type CommitOutcomeUnknownError struct {
	ID  uint64
	Err error
}

// Error returns a human-readable description of the ambiguity.
func (e *CommitOutcomeUnknownError) Error() string {
	msg := "user: commit outcome unknown for user " + strconv.FormatUint(e.ID, 10) +
		"; re-read and reconcile before retrying"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the underlying store error.
func (e *CommitOutcomeUnknownError) Unwrap() error { return e.Err }

// InvalidLifecycleError is returned when a request names a lifecycle state
// that is not defined, or one that is defined but not permitted in the position
// it was used (creating an account directly as suspended, for example).
//
// It is separate from ValidationError because a caller can act on it: the
// Allowed list names exactly what it may send instead, which a free-text
// validation reason cannot carry.
type InvalidLifecycleError struct {
	// Value is the state the caller supplied.
	Value Lifecycle

	// Op names where it was rejected ("create" or "update").
	Op string

	// Allowed lists the states valid in this position, in a stable order.
	Allowed []Lifecycle
}

// Error returns a human-readable description naming the allowed states.
func (e *InvalidLifecycleError) Error() string {
	msg := "user: invalid lifecycle state " + strconv.Quote(string(e.Value))
	if e.Op != "" {
		msg += " for " + e.Op
	}
	if len(e.Allowed) > 0 {
		msg += "; allowed: " + joinLifecycles(e.Allowed)
	}
	return msg
}

// LifecycleTransitionError is returned when an account is asked to move between
// two states the transition table does not connect.
//
// The most important instance is any transition out of LifecycleDeleting: that
// state is terminal precisely so a deletion in progress cannot be raced back
// into an authenticating account by a concurrent enable, and reporting the
// refusal as a typed error is what lets a caller distinguish "you may not" from
// "the store failed".
type LifecycleTransitionError struct {
	// ID is the user whose transition was refused.
	ID uint64

	// From is the state the record currently holds.
	From Lifecycle

	// To is the state the caller asked for.
	To Lifecycle
}

// Error returns a human-readable description of the refused transition.
func (e *LifecycleTransitionError) Error() string {
	msg := "user: cannot transition user " + strconv.FormatUint(e.ID, 10) +
		" from " + string(e.From) + " to " + string(e.To)
	if e.From.Terminal() {
		msg += "; " + string(e.From) + " is terminal"
	}
	return msg
}

// joinLifecycles renders a state list for an error message.
func joinLifecycles(states []Lifecycle) string {
	out := ""
	for i, s := range states {
		if i > 0 {
			out += ", "
		}
		out += string(s)
	}
	return out
}
