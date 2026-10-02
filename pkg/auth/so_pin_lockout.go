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

package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// The SO PIN lockout: the one implementation of the attempt counting every SO
// PIN check on the node goes through -- the node SO PIN behind the PIN
// backend (pkg/pin's lockout decorator) and each tenant's SO PIN behind
// SOAuthenticator.
//
// Each key (the node, or a tenant) has TWO counters:
//
//   - the anonymous counter, for checks made by a caller that is neither
//     authenticated nor presenting a TLS-verified certificate -- the
//     sealed-window caller;
//   - the authenticated counter, for checks made by an authenticated
//     identity or over a TLS-verified certificate (AuthenticatedCaller).
//
// They are separate so anonymous traffic can never lock an authenticated
// Security Officer out: wrong PINs guessed anonymously engage only the
// anonymous counter. Only a wrong PIN counts. The first SOPINFreeFailures
// failures in a row on a counter cost nothing; every failure from then on
// locks that counter for a backoff that doubles from SOPINBaseBackoff up to
// SOPINMaxBackoff. A correct PIN resets the counter it was checked against.
// Recovery is to wait the backoff out (RetryAfter) and present the right PIN;
// an officer locked out anonymously recovers at once by authenticating.
//
// Once the backoff reaches its cap an attacker gets about 96 guesses a day on
// a counter, so an SO PIN needs at least 40 bits of entropy to hold for
// decades.
const (
	// SOPINFreeFailures is the number of consecutive wrong SO PINs admitted
	// on one counter before the lockout engages.
	SOPINFreeFailures = 5
	// SOPINBaseBackoff is the lockout after the first failure past the free
	// ones.
	SOPINBaseBackoff = time.Second
	// SOPINMaxBackoff caps the doubling backoff.
	SOPINMaxBackoff = 15 * time.Minute
)

// ErrSOPINLocked is the condition SOPINLockedError unwraps to.
var ErrSOPINLocked = errors.New("auth: SO PIN locked after repeated failures")

// SOPINLockedError is returned by a check refused while its counter is
// locked out. The presented PIN was not compared.
type SOPINLockedError struct {
	// RetryAfter is how long until the next check is admitted.
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *SOPINLockedError) Error() string {
	return "auth: SO PIN locked after repeated failures; retry after " + e.RetryAfter.String()
}

// Unwrap returns ErrSOPINLocked.
func (e *SOPINLockedError) Unwrap() error { return ErrSOPINLocked }

// AuthenticatedCaller reports whether ctx carries an authenticated identity
// the sealed window did not admit anonymously, or a TLS connection whose
// certificate the TLS stack verified. Its answer picks the SO PIN lockout
// counter a check counts against.
func AuthenticatedCaller(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if id := IdentityFromContext(ctx); id != nil && id.Subject != "" && !sealedwindow.Anonymous(ctx) {
		return true
	}
	st := TLSStateFromContext(ctx)
	return st != nil && len(st.VerifiedChains) > 0
}

// soPINCounter counts one class of caller's failed SO PIN checks for a key.
type soPINCounter struct {
	failures    int
	lockedUntil time.Time
}

// soPINCounters are one key's two counters.
type soPINCounters struct {
	anonymous     soPINCounter
	authenticated soPINCounter
}

// SOPINLockout counts failed SO PIN checks per key, in the two counters
// described above. Checks of one lockout run one at a time and a failure is
// counted before the next check starts, so guesses fired concurrently are no
// cheaper than guesses fired in turn. The zero value is not usable; use
// NewSOPINLockout.
type SOPINLockout struct {
	mu   sync.Mutex
	keys map[string]*soPINCounters
	now  func() time.Time
}

// NewSOPINLockout returns an empty lockout. clock is the time source; nil
// means time.Now.
func NewSOPINLockout(clock func() time.Time) *SOPINLockout {
	if clock == nil {
		clock = time.Now
	}
	return &SOPINLockout{keys: make(map[string]*soPINCounters), now: clock}
}

// Check runs verify -- the SO PIN comparison -- for key unless the counter of
// the caller's class (authenticated or anonymous) is locked out, in which
// case it returns *SOPINLockedError without running verify. It returns
// verify's error unchanged otherwise. Only an error mismatch reports as a
// wrong PIN counts as a failure: a store that cannot answer is not a guess.
func (l *SOPINLockout) Check(key string, authenticated bool, verify func() error, mismatch func(error) bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	counters, ok := l.keys[key]
	if !ok {
		counters = &soPINCounters{}
		l.keys[key] = counters
	}
	c := &counters.anonymous
	if authenticated {
		c = &counters.authenticated
	}
	now := l.now()
	if now.Before(c.lockedUntil) {
		return &SOPINLockedError{RetryAfter: c.lockedUntil.Sub(now)}
	}
	err := verify()
	if err == nil {
		*c = soPINCounter{}
		if counters.anonymous == (soPINCounter{}) && counters.authenticated == (soPINCounter{}) {
			delete(l.keys, key)
		}
		return nil
	}
	if !mismatch(err) {
		return err
	}
	c.failures++
	if c.failures >= SOPINFreeFailures {
		c.lockedUntil = now.Add(soPINBackoff(c.failures - SOPINFreeFailures))
	}
	return err
}

// Status reports key's counter for the caller's class: its consecutive
// failures and, while locked, the time the lockout ends.
func (l *SOPINLockout) Status(key string, authenticated bool) (failures int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	counters, ok := l.keys[key]
	if !ok {
		return 0, time.Time{}
	}
	c := counters.anonymous
	if authenticated {
		c = counters.authenticated
	}
	if !l.now().Before(c.lockedUntil) {
		return c.failures, time.Time{}
	}
	return c.failures, c.lockedUntil
}

// Now returns the lockout's clock.
func (l *SOPINLockout) Now() time.Time { return l.now() }

// soPINBackoff is the lockout after the n-th failure past the free ones:
// SOPINBaseBackoff doubled n times, capped at SOPINMaxBackoff.
func soPINBackoff(n int) time.Duration {
	backoff := SOPINBaseBackoff
	for i := 0; i < n && backoff < SOPINMaxBackoff; i++ {
		backoff *= 2
	}
	return min(backoff, SOPINMaxBackoff)
}
