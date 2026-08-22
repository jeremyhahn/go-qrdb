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

package sdk

import "strconv"

// The guarded-swap terminal errors.
//
// All three are distinguishable with errors.As, and none of them is reported
// when a write landed — a caller that receives any of these knows the record is
// unchanged by this call. That is the fail-closed property the helper exists
// to provide: the hand-rolled loops it replaces tended to report the ambiguous
// cases as success.

// SwapLostError reports that Decide chose to lose the record to another writer.
//
// It is not a fault. A no-retry consumer — a claim on a monotonic mark, where
// losing the guard means losing the claim — returns this on the ordinary path
// where a competitor won, and the caller treats it as an outcome rather than an
// error condition.
type SwapLostError struct {
	// Key is the record that was lost.
	Key []byte
}

func (e *SwapLostError) Error() string {
	return "qrdb: guarded swap lost the record " + strconv.Quote(string(e.Key)) + " to another writer"
}

// SwapContendedError reports that the attempt bound was reached with the guard
// refused every time.
//
// The record moved on each attempt — that is what distinguishes this from
// SwapStalledError — so this is genuine contention: more writers are competing
// for the record than the bound allows this caller to outlast. Raising
// MaxAttempts is rarely the right response; a record this contended usually
// wants partitioning.
type SwapContendedError struct {
	// Key is the record that could not be won.
	Key []byte
	// Attempts is the bound that was reached.
	Attempts int
}

func (e *SwapContendedError) Error() string {
	return "qrdb: guarded swap on " + strconv.Quote(string(e.Key)) +
		" gave up after " + strconv.Itoa(e.Attempts) + " contended attempts"
}

// SwapStalledError reports that the guard was refused while the record did not
// move.
//
// This is a FAULT, not contention, and it is reported rather than retried
// because retrying cannot change the outcome: the next attempt would build the
// identical guard against the identical record and be refused identically, so
// the loop would spin to the attempt bound and then report contention for
// something that is not contention. Reporting it immediately, with its own
// type, is the difference between a caller that can diagnose the fault and one
// that sees a misleading timeout.
//
// It means one of: the transaction plane does not evaluate the compare target
// in use (try GuardOnValue on a store that does not implement
// CompareModRevision), the store is not honouring the guard, or the failure
// branch is not returning the record the guard was evaluated against.
type SwapStalledError struct {
	// Key is the record whose guard was refused without it moving.
	Key []byte
	// Attempts is how many attempts had been made when the stall was detected.
	Attempts int
}

func (e *SwapStalledError) Error() string {
	return "qrdb: guarded swap on " + strconv.Quote(string(e.Key)) +
		" was refused without the record changing; retrying cannot succeed"
}

// SwapConfigError reports a GuardedSwapRequest that cannot be executed as
// written — a missing key, a nil Decide, an unknown decision. It is a
// programming error in the caller and is never produced by contention.
type SwapConfigError struct {
	// Reason names what is wrong with the request.
	Reason string
}

func (e *SwapConfigError) Error() string {
	return "qrdb: invalid guarded-swap request: " + e.Reason
}
