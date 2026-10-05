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
	"net"
	"net/netip"
	"slices"
	"strings"
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
//   - one counter PER PRINCIPAL (CallerPrincipal): the authenticated
//     identity, the verified client certificate, or -- for a caller that is
//     neither, such as the sealed-window caller or an
//     awaiting-admin-credential reissue -- the caller's own remote address
//     (go-qrdb#560 review C), so that one source's wrong guesses never lock
//     out a different, unrelated source making the identical kind of
//     anonymous check;
//   - one anonymous counter shared only by checks made under a context that
//     carries no identity, certificate OR remote address at all -- every
//     production transport records one, so this is reached only when a
//     caller (chiefly a test) builds its own bare context;
//   - one aggregate cap over every principal with a recorded identity
//     together (anything other than the bare anonymous counter above).
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
//
// The store does not hold principal ids: each entry is a domain-separated
// SHA-256 digest of the key and the principal (knownGoodDigest). The digest
// is unkeyed, so it is not a secret: principal ids are guessable ("id:" plus
// a tenant and a user name), and whoever can read the record can confirm a
// guessed principal offline. It keeps the ids out of the record, not out of
// reach of someone who already suspects one.
//
// The lockout never calls a store while holding a lock, and bounds each call
// with SOPINKnownGoodTimeout, so a slow store -- a degraded Raft shard --
// delays at most the check that asked, never another key's. A save loads the
// stored set and writes the union with this lockout's own (see
// saveKnownGood), so nodes sharing one store do not drop each other's
// entries.
type SOPINKnownGoodStore interface {
	// LoadKnownGood returns key's persisted known-good digests, oldest
	// first; none is not an error.
	LoadKnownGood(ctx context.Context, key string) ([]string, error)
	// SaveKnownGood replaces key's persisted known-good digests.
	SaveKnownGood(ctx context.Context, key string, digests []string) error
}

const (
	// SOPINKnownGoodTimeout bounds every SOPINKnownGoodStore call.
	SOPINKnownGoodTimeout = 5 * time.Second
	// SOPINKnownGoodRetry is how long after a failed load the next load is
	// tried; until then the key's principals are held to the cap.
	SOPINKnownGoodRetry = 30 * time.Second
)

// knownGoodDigest is the stored form of principal's known-good status for
// key: an unkeyed SHA-256 digest under a domain separator, so the record does
// not carry the principal id (see SOPINKnownGoodStore for what it does not
// protect).
func knownGoodDigest(key, principal string) string {
	sum := sha256.Sum256([]byte("qrdb/so-pin-known-good/v1\x00" + key + "\x00" + principal))
	return hex.EncodeToString(sum[:])
}

// SOPINLockoutOption configures a SOPINLockout.
type SOPINLockoutOption func(*SOPINLockout)

// WithKnownGoodStore persists the known-good sets in store.
func WithKnownGoodStore(store SOPINKnownGoodStore) SOPINLockoutOption {
	return func(l *SOPINLockout) { l.knownGood = store }
}

// overflowPrincipal is the counter principals beyond SOPINMaxTrackedPrincipals
// share. It cannot collide with a real principal, which always carries an
// "id:", "holder:" or "cert:" prefix.
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
// against. An authenticated identity the sealed window did not admit
// anonymously is "id:<tenant>/<subject>", or "holder:<tenant>/<subject>"
// when it authenticated with a holder certificate (IsHolderIdentity) -- a
// holder named "alice" is not the user alice, and shares neither her
// counter nor her known-good status; otherwise a TLS connection whose
// certificate the TLS stack verified is "cert:<SHA-256 of the leaf
// certificate>". Each distinct principal has its own lockout counter, so
// one principal's wrong PINs never lock another out.
//
// A caller that is NEITHER authenticated NOR presenting a TLS-verified
// certificate -- the sealed-window caller, or an awaiting-admin-credential
// reissue, both of which run before any identity binds -- falls back to
// "addr:<remote>" when WithRemoteAddr recorded one (go-qrdb#560 review C):
// without a per-source identity here, every such caller on a listener
// shared this ONE "" bucket, so one source's wrong guesses locked out every
// other source's legitimate attempts. Only a ctx with no remote address at
// all (a context no transport ever decorated, as in most unit tests) falls
// all the way back to "".
func CallerPrincipal(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id := IdentityFromContext(ctx); id != nil && id.Subject != "" && !sealedwindow.Anonymous(ctx) {
		if IsHolderIdentity(id) {
			return "holder:" + tenantid.Normalize(id.TenantID) + "/" + id.Subject
		}
		return "id:" + tenantid.Normalize(id.TenantID) + "/" + id.Subject
	}
	// Deliberately VerifiedChains only, never PeerCertificates: an
	// unverified leaf is not a principal this counts against, because the
	// SAME counter identity must not be mintable at will by an attacker
	// presenting a fresh, never-checked certificate on every guess. A
	// connection pkg/clienttrust.Pool.Wire touched -- which empties
	// VerifiedChains at the TLS layer, see ResolveVerifiedChains' own doc
	// comment -- reaches this branch only for a caller Authenticate never
	// resolved an identity for (the sealed-window path above handles the
	// ordinary authenticated case): such a caller counts as anonymous
	// here, same as one presenting no certificate at all.
	if st := TLSStateFromContext(ctx); st != nil && len(st.VerifiedChains) > 0 && len(st.VerifiedChains[0]) > 0 {
		sum := sha256.Sum256(st.VerifiedChains[0][0].Raw)
		return "cert:" + hex.EncodeToString(sum[:])
	}
	if addr, ok := RemoteAddrFromContext(ctx); ok {
		return "addr:" + remoteHostOnly(addr)
	}
	return ""
}

// remoteHostOnly returns addr's host, with any port stripped, for
// CallerPrincipal's "addr:" bucket. An ephemeral client port changes on
// every new connection -- or, behind a pooling reverse proxy, on every
// request -- so keying on host:port would hand a rotating-port caller a
// FRESH counter each time: not "one source, one counter," the per-source
// isolation the lockout exists to provide, but no counter at all (go-qrdb#560
// re-review item 2). An IPv6 address is normalized through netip.ParseAddr
// (which, unlike net.ParseIP, understands a zone id) so the same host is
// one principal regardless of how it was written -- brackets, a zone,
// leading zeros in a group. addr with no parseable port, or a host that is
// not an IP at all (a hostname, as some reverse proxies log), is returned
// as net.SplitHostPort left it: stripping only ever removes a port, never
// invents a host.
func remoteHostOnly(addr string) string {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String()
	}
	return host
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

// soPINKeyState is one key's counters. Its fields are guarded by mu, which a
// check holds across its verify, so the checks of one key run one at a time
// while other keys' checks proceed.
type soPINKeyState struct {
	mu sync.Mutex
	// refs counts the calls holding this state (acquire to release); it is
	// guarded by the lockout's map lock, and a state is removed from the map
	// only when no call holds it, so one key never has two live states.
	refs int

	anonymous  soPINCounter
	principals map[string]*soPINCounter
	aggregate  soPINAggregate
	// knownGood is the key's known-good digests (knownGoodDigest), oldest
	// first, and knownGoodLoaded whether the store has been read.
	knownGood       []string
	knownGoodLoaded bool
	// loading marks a load in flight, and nextLoad when a failed one may be
	// tried again; neither is ever waited on.
	loading  bool
	nextLoad time.Time
	// saveMu serializes this key's saves. It is never held with mu across a
	// store call.
	saveMu sync.Mutex
}

// empty reports whether the state holds nothing worth keeping.
func (k *soPINKeyState) empty() bool {
	return k.anonymous == (soPINCounter{}) && len(k.principals) == 0 && k.aggregate.failures == 0 && len(k.knownGood) == 0
}

// isKnownGood reports whether digest is in k's known-good set.
func (k *soPINKeyState) isKnownGood(digest string) bool {
	return slices.Contains(k.knownGood, digest)
}

// addKnownGood records digest as known-good, keeping the most recent
// SOPINMaxKnownGood, and reports whether the set changed.
func (k *soPINKeyState) addKnownGood(digest string) bool {
	for i, p := range k.knownGood {
		if p == digest {
			if i == len(k.knownGood)-1 {
				return false
			}
			k.knownGood = append(append(k.knownGood[:i:i], k.knownGood[i+1:]...), digest)
			return true
		}
	}
	k.knownGood = append(k.knownGood, digest)
	if len(k.knownGood) > SOPINMaxKnownGood {
		k.knownGood = append([]string(nil), k.knownGood[len(k.knownGood)-SOPINMaxKnownGood:]...)
	}
	return true
}

// SOPINLockout counts failed SO PIN checks per key, in the counters described
// above. Checks of one KEY run one at a time and a failure is counted before
// that key's next check starts, so guesses fired concurrently are no cheaper
// than guesses fired in turn; checks of different keys -- the node and each
// tenant -- run concurrently, so one tenant's slow verification (a KDF) never
// holds up another's. The zero value is not usable; use NewSOPINLockout.
type SOPINLockout struct {
	// mu guards keys and each state's refs; it is held only for map lookups.
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

// acquire returns key's state, creating it, and holds it until release: a
// held state stays in the map. It takes only the map lock.
func (l *SOPINLockout) acquire(key string) *soPINKeyState {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.keys[key]
	if !ok {
		state = &soPINKeyState{}
		l.keys[key] = state
	}
	state.refs++
	return state
}

// release gives up a state acquire returned, removing it from the map when no
// call holds it and it records nothing worth keeping. The caller does not
// hold state.mu; with no holder left, nothing else can be touching it.
func (l *SOPINLockout) release(key string, state *soPINKeyState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state.refs--
	if state.refs == 0 && state.empty() && !state.loading && l.keys[key] == state {
		delete(l.keys, key)
	}
}

// loadKnownGood reads key's persisted known-good set, once, holding no lock
// across the read: the read is bounded by SOPINKnownGoodTimeout and a slow
// store therefore delays only this check. A check that finds a load already
// in flight, or a failed load too recent to retry (SOPINKnownGoodRetry), does
// not wait: its principal is held to the cap. Entries the store returns are
// merged with any recorded meanwhile.
func (l *SOPINLockout) loadKnownGood(ctx context.Context, key string, state *soPINKeyState) {
	if l.knownGood == nil {
		return
	}
	state.mu.Lock()
	if state.knownGoodLoaded || state.loading || l.now().Before(state.nextLoad) {
		state.mu.Unlock()
		return
	}
	state.loading = true
	state.mu.Unlock()

	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SOPINKnownGoodTimeout)
	digests, err := l.knownGood.LoadKnownGood(lctx, key)
	cancel()

	state.mu.Lock()
	defer state.mu.Unlock()
	state.loading = false
	if err != nil {
		state.nextLoad = l.now().Add(SOPINKnownGoodRetry)
		return
	}
	state.mergeKnownGood(digests)
	state.knownGoodLoaded = true
}

// mergeKnownGood folds stored, an older set read from the store, under k's
// own entries: the result keeps k's entries as the most recent, and the cap
// drops the oldest. The caller holds k.mu.
//
// Under cap pressure -- more than SOPINMaxKnownGood distinct known-good
// principals among the nodes sharing one record -- the stored entries are
// the ones dropped first, because the record carries no times and this
// lockout's own entries are the only ones it knows to be recent. A principal
// known-good only through another node can therefore fall out of the set; it
// is then held to the aggregate cap, never refused outright, until its next
// successful check, which records it again.
func (k *soPINKeyState) mergeKnownGood(stored []string) {
	own := k.knownGood
	k.knownGood = nil
	for _, d := range stored {
		k.addKnownGood(d)
	}
	for _, d := range own {
		k.addKnownGood(d)
	}
}

// saveKnownGood persists key's known-good set, holding no lock across a store
// call. It MERGES rather than overwrites: it reads the stored set, folds it
// under this lockout's own (adopting the entries other nodes sharing the
// store have written since this one loaded), and writes the union, so one
// node's save does not drop another's digests. Saves of one key run one at a
// time. A save whose read fails writes nothing -- overwriting a set it could
// not read might drop other nodes' entries -- and like a failed write keeps
// the exemption in memory: a successful SO PIN check is never turned into a
// refusal by its bookkeeping. Two nodes saving the same key at the same
// moment can still each miss the other's newest entry; it is written again
// on that principal's next successful check.
func (l *SOPINLockout) saveKnownGood(ctx context.Context, key string, state *soPINKeyState) {
	if l.knownGood == nil {
		return
	}
	state.saveMu.Lock()
	defer state.saveMu.Unlock()

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SOPINKnownGoodTimeout)
	defer cancel()
	stored, err := l.knownGood.LoadKnownGood(sctx, key)
	if err != nil {
		return
	}
	state.mu.Lock()
	state.mergeKnownGood(stored)
	digests := slices.Clone(state.knownGood)
	state.mu.Unlock()
	_ = l.knownGood.SaveKnownGood(sctx, key, digests)
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
// answer is not a guess. ctx bounds the known-good store calls the check
// makes (see SOPINKnownGoodStore), which never run under the lockout's lock.
func (l *SOPINLockout) Check(ctx context.Context, key, principal string, verify func() error, mismatch func(error) bool) error {
	state := l.acquire(key)
	defer l.release(key, state)
	// An "addr:" principal never consults or joins the known-good set --
	// see check's aggregateEligible -- so loading it here would only ever
	// be a wasted store round-trip for one.
	if principal != "" && !isAddrPrincipal(principal) {
		l.loadKnownGood(ctx, key, state)
	}
	changed, err := l.check(state, key, principal, verify, mismatch)
	if changed {
		l.saveKnownGood(ctx, key, state)
	}
	return err
}

// check is Check under the key's lock, which it holds across verify so that
// a failure is counted before the key's next check; changed reports that the
// known-good set grew.
//
// A success resets the counter it was checked against. An AUTHENTICATED
// success also clears the aggregate cap and records the principal as
// known-good. An ANONYMOUS success -- the sealed-window caller -- does
// neither: there is no principal to record, and an unauthenticated caller
// must not be able to lift a cap other callers' guesses engaged. So an
// officer who has only ever presented the PIN anonymously is not exempt from
// the cap when it later authenticates, until it succeeds once authenticated.
func (l *SOPINLockout) check(state *soPINKeyState, key, principal string, verify func() error, mismatch func(error) bool) (changed bool, _ error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	now := l.now()
	c := &state.anonymous
	capped := false
	// An "addr:" principal -- a caller with no identity or certificate at
	// all, isolated only by remote host (go-qrdb#560 re-review item 2) --
	// gets its OWN counter below, exactly like an authenticated principal,
	// but never participates in the aggregate cap: that cap exists so a
	// coordinated AUTHENTICATED attack cannot grind down a key's own
	// backoff by spreading guesses across many identities, and extending
	// it to anonymous sources would let any one anonymous source's
	// failures count against, and its success clear, a cap an
	// authenticated officer's failures engaged -- the opposite of "an
	// authenticated principal must never be affected by anonymous
	// failures."
	aggregateEligible := principal != "" && !isAddrPrincipal(principal)
	digest := knownGoodDigest(key, principal)
	if principal != "" {
		c = state.principalCounter(principal, true, now)
	}
	if aggregateEligible {
		capped = !state.isKnownGood(digest)
		if wait, held := state.aggregate.locked(now); capped && held {
			return false, &SOPINLockedError{RetryAfter: wait}
		}
	}
	if wait, held := c.locked(now); held {
		return false, &SOPINLockedError{RetryAfter: wait}
	}
	err := verify()
	if err == nil {
		*c = soPINCounter{}
		for name, pc := range state.principals {
			if *pc == (soPINCounter{}) {
				delete(state.principals, name)
			}
		}
		if aggregateEligible {
			state.aggregate = soPINAggregate{}
			changed = state.addKnownGood(digest)
		}
		return changed, nil
	}
	if !mismatch(err) {
		return false, err
	}
	c.fail(now)
	if capped {
		state.aggregate.fail(now)
	}
	return false, err
}

// addrPrincipalPrefix is CallerPrincipal's prefix for a caller isolated
// only by remote host (go-qrdb#560 re-review item 2): no identity, no
// certificate, just a per-source bucket among other such callers.
const addrPrincipalPrefix = "addr:"

// isAddrPrincipal reports whether principal is one of CallerPrincipal's
// "addr:" buckets, as opposed to an authenticated identity ("id:"/
// "holder:"), a verified certificate ("cert:"), or the single bare
// anonymous counter ("").
func isAddrPrincipal(principal string) bool {
	return strings.HasPrefix(principal, addrPrincipalPrefix)
}

// Status reports key's counter for principal ("" is the anonymous caller):
// its consecutive failures and, while it -- or for an authenticated
// principal the aggregate cap -- is locked, the time the lockout ends.
func (l *SOPINLockout) Status(key, principal string) (failures int, lockedUntil time.Time) {
	l.mu.Lock()
	_, ok := l.keys[key]
	l.mu.Unlock()
	if !ok {
		return 0, time.Time{}
	}
	state := l.acquire(key)
	defer l.release(key, state)
	state.mu.Lock()
	defer state.mu.Unlock()
	now := l.now()
	c := &state.anonymous
	if principal != "" && !isAddrPrincipal(principal) {
		if wait, held := state.aggregate.locked(now); held && !state.isKnownGood(knownGoodDigest(key, principal)) {
			lockedUntil = now.Add(wait)
		}
	}
	if principal != "" {
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
