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

package clusterenroll

import (
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// joinAttemptRatePolicy bounds how often one source address may attempt a
// join: a sustained rate low enough that a brute-force token guesser gains
// nothing from sheer request volume, with enough burst for an operator's
// own retries (a mistyped token, a transient network blip) not to be
// mistaken for an attack.
var joinAttemptRatePolicy = ratelimit.Policy{RatePerSecond: 0.2, Burst: 5}

// joinLockoutThreshold is the number of consecutive bad-token attempts from
// one source address that triggers a lockout. It counts ACROSS transports
// (Noise_NK and SPKI/Direct share the one package-level guard below), so an
// attacker gains nothing by switching from one to the other mid-attack.
const joinLockoutThreshold = 5

// joinLockoutDuration is how long a locked-out source is refused outright,
// independent of the token-bucket rate limit above.
const joinLockoutDuration = 5 * time.Minute

// joinAttemptGuard rate-limits and locks out join attempts per source
// address. It is deliberately simpler than pkg/ratelimit's own
// permission-Policy wiring (pkg/auth/middleware): a join attempt is
// UNAUTHENTICATED by construction -- that is the problem member-side CSR
// join solves -- so there is no permission to key a Policy by, only a
// network source address.
type joinAttemptGuard struct {
	limiter *ratelimit.TokenBucketLimiter

	mu       sync.Mutex
	failures map[string]*joinFailureState
}

// joinFailureState is one source address's consecutive-failure count and,
// once it trips joinLockoutThreshold, the time its lockout expires.
type joinFailureState struct {
	count       int
	lockedUntil time.Time
}

// newJoinAttemptGuard constructs a guard with its own background rate-limit
// reaper. Callers must Close it during shutdown.
func newJoinAttemptGuard() *joinAttemptGuard {
	return &joinAttemptGuard{
		limiter:  ratelimit.NewTokenBucketLimiter(),
		failures: make(map[string]*joinFailureState),
	}
}

// allow reports whether source may attempt a join right now: neither
// locked out from repeated bad tokens, nor over its sustained request rate.
func (g *joinAttemptGuard) allow(source string) bool {
	g.mu.Lock()
	state := g.failures[source]
	lockedOut := state != nil && time.Now().Before(state.lockedUntil)
	g.mu.Unlock()
	if lockedOut {
		return false
	}

	allowed, _ := g.limiter.Allow(source, joinAttemptRatePolicy)
	return allowed
}

// recordFailure counts one bad-token attempt from source, locking it out
// for joinLockoutDuration once joinLockoutThreshold consecutive failures
// accrue.
func (g *joinAttemptGuard) recordFailure(source string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.failures[source]
	if state == nil {
		state = &joinFailureState{}
		g.failures[source] = state
	}
	state.count++
	if state.count >= joinLockoutThreshold {
		state.lockedUntil = time.Now().Add(joinLockoutDuration)
		state.count = 0
	}
}

// recordSuccess clears source's failure count after an admitted join. A
// source with no recorded failures (the overwhelming common case) never
// gets an entry in the first place, so this is a no-op for it.
func (g *joinAttemptGuard) recordSuccess(source string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, source)
}

// close releases the guard's background rate-limit reaper.
func (g *joinAttemptGuard) close() error {
	return g.limiter.Close()
}

// defaultJoinAttemptGuard is shared by every JoinServer (Noise_NK) in this
// process AND by pkg/api/rest's SPKI/Direct route (through the exported
// functions below), so an attacker gains nothing by switching transports
// mid-attack: both transports count against the SAME per-source state.
//
// A package-level singleton, rather than a value threaded through
// StartJoinServer/WithClusterJoin's constructors, is a deliberate choice:
// every production process runs at most one signing member's worth of join
// surface (one JoinServer, one REST route, both bound to the same cluster
// CA provider), so there is exactly one guard to share, and the alternative
// -- plumbing a *joinAttemptGuard through two independent packages'
// constructors -- buys no additional correctness for the cost of a wider
// signature change across both. Tests that need isolation construct their
// own *joinAttemptGuard directly rather than reaching for this one.
var defaultJoinAttemptGuard = newJoinAttemptGuard()

// AllowJoinAttempt reports whether source (a network address, typically
// host:port or just host) may attempt a member-side CSR join right now.
// Exported so pkg/api/rest's SPKI/Direct route shares this package's
// lockout/rate-limit state with the Noise_NK transport.
func AllowJoinAttempt(source string) bool {
	return defaultJoinAttemptGuard.allow(source)
}

// RecordJoinAttemptFailure counts one bad-token join attempt from source.
func RecordJoinAttemptFailure(source string) {
	defaultJoinAttemptGuard.recordFailure(source)
}

// RecordJoinAttemptSuccess clears source's recorded failures after an
// admitted join.
func RecordJoinAttemptSuccess(source string) {
	defaultJoinAttemptGuard.recordSuccess(source)
}

// ResetJoinAttemptGuardForTest replaces the package-level, process-wide
// join-attempt guard with a fresh one and returns a func that restores the
// previous one. It exists so another package's tests (pkg/api/rest's
// SPKI/Direct route tests, in particular) that dial or POST to a join
// surface from a shared source address (loopback, or httptest's fixed
// remote addr) can isolate themselves from each other's accumulated
// rate-limit/lockout state, exactly as this package's own tests do for the
// Noise_NK transport. Production code never calls this.
func ResetJoinAttemptGuardForTest() (restore func()) {
	prev := defaultJoinAttemptGuard
	defaultJoinAttemptGuard = newJoinAttemptGuard()
	return func() {
		_ = defaultJoinAttemptGuard.close()
		defaultJoinAttemptGuard = prev
	}
}
