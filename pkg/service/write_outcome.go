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

import "errors"

// A failed write does not say, by itself, whether anything was stored.
//
// SyncPropose can commit an entry and then fail to deliver its response: the
// value is durable and the caller was told only that something went wrong. Most
// errors are not like that — a validation failure, an encoding failure, a
// refusal by a node that is not the leader, a state machine that applied
// nothing — and each of those proves no value was stored.
//
// Callers that reconcile state need the difference, and getting it wrong is
// expensive in both directions: treating unknown as failed under-counts a
// stored value, treating failed as unknown leaks the caller's accounting on
// every rejected request (go-qrdb#90).
//
// These two sentinels let the layer that KNOWS say so, so callers ask a
// question instead of enumerating concrete types:
//
//	errors.Is(err, service.ErrNotStored)      // safe to un-account
//	errors.Is(err, service.ErrOutcomeUnknown) // may have committed
//
// WHY THE DEFAULT IS UNKNOWN, and why it is ErrNotStored that is marked
// rather than only ErrOutcomeUnknown. An unmarked error answers false to both,
// and a caller written the obvious way then treats it as unknown and keeps its
// accounting. That is the conservative direction, and it has to be what
// happens by DEFAULT, because the errors that cannot be marked are exactly the
// ones nobody can classify: BackendKVService forwards whatever its storage
// engine returns, and a write that failed mid-fsync may or may not be durable.
// Marking only the ambiguous case would invert this — every unmarked
// third-party error would read as "proven not stored", which is the fail-open
// behaviour go-qrdb#90 removed, arrived at silently. So a claim of
// ErrNotStored is opt-in and must be justified where it is made; ambiguity
// costs nothing to declare and nothing to omit.
// WHAT IS DELIBERATELY NOT MARKED. BarrierSealedError is the notable one: a
// sealed barrier refuses the write, and marking it would very likely be
// correct — but "very likely" is not the standard for a claim that lets a
// caller drop its accounting, and it has not been established for every
// backend a BarrierBackend can wrap. Left unmarked it costs nothing: the
// caller keeps its accounting, which is what it did before this file existed.
// That is the shape of the decision for anything added later — mark it when
// the path is known to store nothing, not when it probably does.
var (
	// ErrNotStored marks an error that PROVES the write did not reach
	// storage: it was refused, rejected or rendered impossible before
	// anything could be committed. A caller may safely act as though the
	// write never happened.
	ErrNotStored = errors.New("service: write proven not stored")

	// ErrOutcomeUnknown marks an error that leaves it UNKNOWN whether the
	// write was committed. It is the state a failed Raft proposal is in, and
	// it is also what an unmarked error means, so a caller never needs to
	// test for it — it is stated positively for the paths that can state it.
	ErrOutcomeUnknown = errors.New("service: write outcome unknown")
)

// notStoredError marks an existing error as proving nothing was stored,
// without changing what it matches otherwise.
//
// Used for sentinels owned by other packages, which cannot carry an Is method
// of their own. Unwrap keeps errors.Is(err, kvstore.ErrAtomicWriteUnsupported)
// answering true for callers that match on the original.
type notStoredError struct {
	err error
}

// markNotStored wraps err so that it also matches ErrNotStored. A nil error is
// returned unchanged.
func markNotStored(err error) error {
	if err == nil {
		return nil
	}
	return &notStoredError{err: err}
}

// Error returns the wrapped error's message unchanged: the marker is a fact
// about the error, not something to read in a log line.
func (e *notStoredError) Error() string {
	return e.err.Error()
}

// Unwrap returns the marked error for use with errors.Is and errors.As.
func (e *notStoredError) Unwrap() error {
	return e.err
}

// Is reports the ErrNotStored marker.
func (e *notStoredError) Is(target error) bool {
	return target == ErrNotStored
}
