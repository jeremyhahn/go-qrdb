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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The SO PIN lockout: the one implementation of the attempt counting every SO
// PIN check on the node goes through -- the node SO PIN behind the PIN
// backend (pkg/pin's lockout decorator) and each tenant's SO PIN behind
// SOAuthenticator.
//
// Each key (the node, or a tenant) counts:
//
//   - one anonymous counter, for checks made by a caller that is neither
//     authenticated nor presenting a TLS-verified certificate -- the
//     sealed-window caller;
//   - one counter PER AUTHENTICATED PRINCIPAL (CallerPrincipal): the
//     authenticated identity, or else the verified client certificate;
//   - one aggregate cap over every authenticated principal together.
//
// Only a wrong PIN counts. The first SOPINFreeFailures failures in a row on a
// counter cost nothing; every failure from then on locks that counter for a
// backoff that doubles from SOPINBaseBackoff up to SOPINMaxBackoff. A correct
// PIN resets the counter it was checked against.
//
// The counters are separate so that no caller can lock another out: wrong PINs
// guessed anonymously engage only the anonymous counter, and wrong PINs
// guessed by a tenant user, a share holder or any other holder of a
// certificate the client CA issued engage only that principal's own counter
// -- never the Security Officer's.
//
// The aggregate cap bounds how fast MANY principals together can guess: at
// most SOPINAggregateFailures wrong PINs from authenticated principals in any
// SOPINAggregateWindow, after which every authenticated check of the key --
// the Security Officer's included -- is refused until the window ends. One
// principal's own backoff holds it far below the cap (about 15 failures in a
// window), so reaching it takes several principals guessing together: that is
// the residual, by design, because the alternative is unbounded distributed
// guessing.
//
// A principal that has presented the correct SO PIN for a key joins that key's
// known-good set (at most SOPINMaxKnownGood, the most recent kept), which a
// SOPINKnownGoodStore persists across restarts. A known-good principal is
// exempt from the aggregate cap -- other principals' guesses never refuse the
// Security Officer -- but not from its own counter's backoff, and its
// failures do not count toward the cap. Everyone else stays bounded by it.
//
// The number of principals tracked per key is capped at
// SOPINMaxTrackedPrincipals. When the cap is reached, counters whose lockout
// has expired and whose last failure is older than SOPINStaleAfter are
// evicted first, so attackers' aged-out entries cannot push new legitimate
// principals onto the one overflow counter that principals beyond the cap
// share.
//
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
	// SOPINAggregateFailures is the most wrong SO PINs all authenticated
	// principals of one key together may present in one aggregate window.
	SOPINAggregateFailures = 64
	// SOPINAggregateWindow is the aggregate cap's window.
	SOPINAggregateWindow = 15 * time.Minute
	// SOPINMaxTrackedPrincipals caps the per-principal counters kept for one
	// key; further principals share one overflow counter.
	SOPINMaxTrackedPrincipals = 4096
	// SOPINMaxKnownGood caps a key's known-good set; the most recent
	// principals to present the correct SO PIN are kept.
	SOPINMaxKnownGood = 8
	// SOPINStaleAfter is how long after its last failure an unlocked
	// per-principal counter may be evicted to make room for another
	// principal: the longer of the aggregate window and the backoff cap, so
	// an eviction can never shorten a lockout or reopen a window.
	SOPINStaleAfter = max(SOPINAggregateWindow, SOPINMaxBackoff)
)

// SOPINKnownGoodStore persists, per key, the principals that have presented
// the correct SO PIN, so their exemption from the aggregate cap survives a
// restart. A lockout without one keeps the set in memory only.
type SOPINKnownGoodStore interface {
	// LoadKnownGood returns key's persisted known-good principals, oldest
	// first; none is not an error.
	LoadKnownGood(key string) ([]string, error)
	// SaveKnownGood replaces key's persisted known-good principals.
	SaveKnownGood(key string, principals []string) error
}

// SOPINLockoutOption configures a SOPINLockout.
type SOPINLockoutOption func(*SOPINLockout)

// WithKnownGoodStore persists the known-good sets in store.
func WithKnownGoodStore(store SOPINKnownGoodStore) SOPINLockoutOption {
	return func(l *SOPINLockout) { l.knownGood = store }
}

// overflowPrincipal is the counter principals beyond SOPINMaxTrackedPrincipals
// share. It cannot collide with a real principal, which always carries an
// "id:" or "cert:" prefix.
const overflowPrincipal = "overflow"

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

// CallerPrincipal names the principal an SO PIN check made under ctx counts
// against, and "" for an anonymous caller. An authenticated identity the
// sealed window did not admit anonymously is "id:<tenant>/<subject>";
// otherwise a TLS connection whose certificate the TLS stack verified is
// "cert:<SHA-256 of the leaf certificate>". Each distinct principal has its
// own lockout counter, so one principal's wrong PINs never lock another out.
func CallerPrincipal(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id := IdentityFromContext(ctx); id != nil && id.Subject != "" && !sealedwindow.Anonymous(ctx) {
		return "id:" + tenantid.Normalize(id.TenantID) + "/" + id.Subject
	}
	if st := TLSStateFromContext(ctx); st != nil && len(st.VerifiedChains) > 0 && len(st.VerifiedChains[0]) > 0 {
		sum := sha256.Sum256(st.VerifiedChains[0][0].Raw)
		return "cert:" + hex.EncodeToString(sum[:])
	}
	return ""
}

// soPINCounter counts one caller's failed SO PIN checks for a key.
type soPINCounter struct {
	failures    int
	lockedUntil time.Time
	lastFailure time.Time
}

// stale reports whether c may be evicted at now: unlocked, and failing last
// longer than SOPINStaleAfter ago.
func (c *soPINCounter) stale(now time.Time) bool {
	return !now.Before(c.lockedUntil) && now.Sub(c.lastFailure) >= SOPINStaleAfter
}

// locked reports how long c stays locked at now, and whether it is.
func (c *soPINCounter) locked(now time.Time) (time.Duration, bool) {
	if now.Before(c.lockedUntil) {
		return c.lockedUntil.Sub(now), true
	}
	return 0, false
}

// fail counts one wrong PIN at now.
func (c *soPINCounter) fail(now time.Time) {
	c.failures++
	c.lastFailure = now
	if c.failures >= SOPINFreeFailures {
		c.lockedUntil = now.Add(soPINBackoff(c.failures - SOPINFreeFailures))
	}
}

// soPINAggregate is the fixed-window cap over a key's authenticated failures.
type soPINAggregate struct {
	windowStart time.Time
	failures    int
}

// locked reports how long the cap holds at now, and whether it does.
func (a *soPINAggregate) locked(now time.Time) (time.Duration, bool) {
	end := a.windowStart.Add(SOPINAggregateWindow)
	if a.failures >= SOPINAggregateFailures && now.Before(end) {
		return end.Sub(now), true
	}
	return 0, false
}

// fail counts one authenticated wrong PIN at now, opening a new window when
// the last has ended.
func (a *soPINAggregate) fail(now time.Time) {
	if !now.Before(a.windowStart.Add(SOPINAggregateWindow)) {
		a.windowStart, a.failures = now, 0
	}
	a.failures++
}

// soPINKeyState is one key's counters.
type soPINKeyState struct {
	anonymous  soPINCounter
	principals map[string]*soPINCounter
	aggregate  soPINAggregate
	// knownGood is the key's known-good principals, oldest first, and
	// knownGoodLoaded whether it has been read from the store yet.
	knownGood       []string
	knownGoodLoaded bool
}

// empty reports whether the state holds nothing worth keeping.
func (k *soPINKeyState) empty() bool {
	return k.anonymous == (soPINCounter{}) && len(k.principals) == 0 && k.aggregate.failures == 0 && len(k.knownGood) == 0
}

// isKnownGood reports whether principal is in k's known-good set.
func (k *soPINKeyState) isKnownGood(principal string) bool {
	for _, p := range k.knownGood {
		if p == principal {
			return true
		}
	}
	return false
}

// addKnownGood records principal as known-good, keeping the most recent
// SOPINMaxKnownGood, and reports whether the set changed.
func (k *soPINKeyState) addKnownGood(principal string) bool {
	for i, p := range k.knownGood {
		if p == principal {
			if i == len(k.knownGood)-1 {
				return false
			}
			k.knownGood = append(append(k.knownGood[:i:i], k.knownGood[i+1:]...), principal)
			return true
		}
	}
	k.knownGood = append(k.knownGood, principal)
	if len(k.knownGood) > SOPINMaxKnownGood {
		k.knownGood = append([]string(nil), k.knownGood[len(k.knownGood)-SOPINMaxKnownGood:]...)
	}
	return true
}

// SOPINLockout counts failed SO PIN checks per key, in the counters described
// above. Checks of one lockout run one at a time and a failure is counted
// before the next check starts, so guesses fired concurrently are no cheaper
// than guesses fired in turn. The zero value is not usable; use
// NewSOPINLockout.
type SOPINLockout struct {
	mu        sync.Mutex
	keys      map[string]*soPINKeyState
	now       func() time.Time
	knownGood SOPINKnownGoodStore
}

// NewSOPINLockout returns an empty lockout. clock is the time source; nil
// means time.Now.
func NewSOPINLockout(clock func() time.Time, opts ...SOPINLockoutOption) *SOPINLockout {
	if clock == nil {
		clock = time.Now
	}
	l := &SOPINLockout{keys: make(map[string]*soPINKeyState), now: clock}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// loadKnownGood reads key's persisted known-good set into state once. A read
// that fails leaves the set unloaded, to be tried again on the next check;
// meanwhile the principal is held to the cap like any other.
func (l *SOPINLockout) loadKnownGood(key string, state *soPINKeyState) {
	if state.knownGoodLoaded {
		return
	}
	if l.knownGood == nil {
		state.knownGoodLoaded = true
		return
	}
	principals, err := l.knownGood.LoadKnownGood(key)
	if err != nil {
		return
	}
	state.knownGood = principals
	if len(state.knownGood) > SOPINMaxKnownGood {
		state.knownGood = state.knownGood[len(state.knownGood)-SOPINMaxKnownGood:]
	}
	state.knownGoodLoaded = true
}

// rememberKnownGood records principal as known-good for key and persists the
// set when it changed. A persist that fails keeps the exemption in memory: a
// successful SO PIN check is never turned into a refusal by its bookkeeping.
func (l *SOPINLockout) rememberKnownGood(key, principal string, state *soPINKeyState) {
	if !state.addKnownGood(principal) || l.knownGood == nil {
		return
	}
	_ = l.knownGood.SaveKnownGood(key, append([]string(nil), state.knownGood...))
}

// principalCounter returns principal's counter in k, creating it when asked
// to. At SOPINMaxTrackedPrincipals it first evicts stale counters (see
// soPINCounter.stale), and only when none is stale falls back to the shared
// overflow counter.
func (k *soPINKeyState) principalCounter(principal string, create bool, now time.Time) *soPINCounter {
	if c, ok := k.principals[principal]; ok {
		return c
	}
	if !create {
		return nil
	}
	if k.principals == nil {
		k.principals = make(map[string]*soPINCounter)
	}
	if len(k.principals) >= SOPINMaxTrackedPrincipals {
		for name, c := range k.principals {
			if c.stale(now) {
				delete(k.principals, name)
			}
		}
	}
	if len(k.principals) >= SOPINMaxTrackedPrincipals {
		principal = overflowPrincipal
		if c, ok := k.principals[principal]; ok {
			return c
		}
	}
	c := &soPINCounter{}
	k.principals[principal] = c
	return c
}

// Check runs verify -- the SO PIN comparison -- for key on behalf of
// principal (CallerPrincipal; "" is the anonymous caller) unless the
// principal's counter, or for an authenticated principal the aggregate cap,
// is locked out, in which case it returns *SOPINLockedError without running
// verify. It returns verify's error unchanged otherwise. Only an error
// mismatch reports as a wrong PIN counts as a failure: a store that cannot
// answer is not a guess.
func (l *SOPINLockout) Check(key, principal string, verify func() error, mismatch func(error) bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.keys[key]
	if !ok {
		state = &soPINKeyState{}
		l.keys[key] = state
	}
	now := l.now()
	c := &state.anonymous
	capped := false
	if principal != "" {
		l.loadKnownGood(key, state)
		capped = !state.isKnownGood(principal)
		c = state.principalCounter(principal, true, now)
		if wait, held := state.aggregate.locked(now); capped && held {
			return &SOPINLockedError{RetryAfter: wait}
		}
	}
	if wait, held := c.locked(now); held {
		return &SOPINLockedError{RetryAfter: wait}
	}
	err := verify()
	if err == nil {
		*c = soPINCounter{}
		for name, pc := range state.principals {
			if *pc == (soPINCounter{}) {
				delete(state.principals, name)
			}
		}
		if principal != "" {
			state.aggregate = soPINAggregate{}
			l.rememberKnownGood(key, principal, state)
		}
		if state.empty() {
			delete(l.keys, key)
		}
		return nil
	}
	if !mismatch(err) {
		return err
	}
	c.fail(now)
	if capped {
		state.aggregate.fail(now)
	}
	return err
}

// Status reports key's counter for principal ("" is the anonymous caller):
// its consecutive failures and, while it -- or for an authenticated
// principal the aggregate cap -- is locked, the time the lockout ends.
func (l *SOPINLockout) Status(key, principal string) (failures int, lockedUntil time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.keys[key]
	if !ok {
		return 0, time.Time{}
	}
	now := l.now()
	c := &state.anonymous
	if principal != "" {
		if wait, held := state.aggregate.locked(now); held && !state.isKnownGood(principal) {
			lockedUntil = now.Add(wait)
		}
		if c = state.principalCounter(principal, false, now); c == nil {
			return 0, lockedUntil
		}
	}
	if wait, held := c.locked(now); held && now.Add(wait).After(lockedUntil) {
		lockedUntil = now.Add(wait)
	}
	return c.failures, lockedUntil
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
