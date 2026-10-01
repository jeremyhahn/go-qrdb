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

package ceremony

import (
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// The SO PIN lockout. ceremony-sign-csr is reachable without authentication
// on the boot-time sealed-window listener, and the SO PIN is its only
// authorization, so every call is an online guess at the PIN.
//
// Only failed checks count. The first soPINFreeFailures failures in a row
// cost nothing, so an operator can mistype; every failure from then on locks
// the check for a backoff that doubles each time, from soPINBaseBackoff up to
// soPINMaxBackoff. A correct PIN resets the count. A legitimate officer is
// therefore never refused for volume: requests that never reach the check,
// and successful signings, cost nothing.
//
// Once the backoff reaches its cap an attacker gets about 96 guesses a day,
// so the SO PIN needs at least 40 bits of entropy to hold for decades -- for
// example 8 random characters from a 62-character alphabet (about 47 bits)
// or a 12-digit random number (about 40 bits). docs/ceremony states the
// requirement.
const (
	soPINFreeFailures = 5
	soPINBaseBackoff  = time.Second
	soPINMaxBackoff   = 15 * time.Minute
)

// soPINAttempts serializes SO PIN checks and counts their failures. Checks
// run one at a time and a failure is counted before the next check starts,
// so guesses fired concurrently are no cheaper than guesses fired in turn.
// The zero value is ready to use.
type soPINAttempts struct {
	mu          sync.Mutex
	failures    int
	lockedUntil time.Time
	// now is the clock; nil means time.Now. Tests replace it.
	now func() time.Time
}

// check compares presented with configured in constant time. It returns
// nil on a match, ErrSOPINMismatch on a mismatch, and *SOPINLockedError
// without comparing anything while the check is locked out.
func (a *soPINAttempts) check(presented, configured string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	if now.Before(a.lockedUntil) {
		return &SOPINLockedError{RetryAfter: a.lockedUntil.Sub(now)}
	}
	if soPINMatches(presented, configured) {
		a.failures = 0
		a.lockedUntil = time.Time{}
		return nil
	}
	a.failures++
	if a.failures >= soPINFreeFailures {
		a.lockedUntil = now.Add(soPINBackoff(a.failures - soPINFreeFailures))
	}
	return ErrSOPINMismatch
}

// soPINBackoff is the lockout after the n-th failure past the free ones:
// soPINBaseBackoff doubled n times, capped at soPINMaxBackoff.
func soPINBackoff(n int) time.Duration {
	backoff := soPINBaseBackoff
	for i := 0; i < n && backoff < soPINMaxBackoff; i++ {
		backoff *= 2
	}
	return min(backoff, soPINMaxBackoff)
}

// SOPINLockedError is returned by SignCSRInit while the SO PIN check is
// locked out after repeated failures. The presented PIN was not compared. It
// unwraps to pin.ErrPINLocked, the PIN-lockout condition every transport
// answers as 429 / RESOURCE_EXHAUSTED.
type SOPINLockedError struct {
	// RetryAfter is how long until the next check is admitted.
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *SOPINLockedError) Error() string {
	return "ceremony: SO PIN locked after repeated failures; retry after " + e.RetryAfter.String()
}

// Unwrap returns pin.ErrPINLocked.
func (e *SOPINLockedError) Unwrap() error { return pin.ErrPINLocked }
