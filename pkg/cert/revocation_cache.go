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

package cert

import (
	"context"
	"crypto/x509"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultRevocationCacheTTL is the default lifetime of a cached NOT-REVOKED
// answer on the authentication path.
//
// READ THIS AS A SECURITY WINDOW, NOT A PERFORMANCE KNOB. It is the maximum
// time a certificate that has just been revoked continues to authenticate on a
// node that had already seen it. Raising it trades a slightly cheaper auth path
// for a longer window; lowering it to zero disables caching and consults
// replicated state on every request.
const DefaultRevocationCacheTTL = 30 * time.Second

// defaultRevocationCacheMaxEntries bounds the number of serials the cache
// holds before it resets. The bound is not reachable by an unauthenticated
// caller — an entry requires a certificate that already verified against this
// node's CA — so it is a ceiling on memory, not a defence. Exceeding it drops
// the whole cache, which costs re-reads and can never produce a wrong answer.
const defaultRevocationCacheMaxEntries = 8192

// RevocationStateUnavailableError reports that revocation state could not be
// consulted at all on this node.
//
// It is returned only for the sealed barrier: the revocation markers live
// behind the same barrier as everything else, so before the first unseal there
// is no state to read — not "nothing is revoked", but "no answer exists here".
//
// The authentication layer treats it as a distinct case from a failed lookup,
// and identifies it by the RevocationStateUnavailable marker method rather
// than by importing this package. The rationale is at
// RevocationCache.IsCertRevoked.
type RevocationStateUnavailableError struct {
	// Err is the underlying provider error, kept so the cause survives.
	Err error
}

// Error returns a human-readable description.
func (e *RevocationStateUnavailableError) Error() string {
	msg := "qrdb/cert: revocation state unavailable: barrier is sealed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying provider error.
func (e *RevocationStateUnavailableError) Unwrap() error { return e.Err }

// RevocationStateUnavailable marks this error as "no answer exists here"
// rather than "the lookup failed". It is the marker pkg/auth tests for, which
// is what lets the two packages agree on the distinction without importing
// each other.
func (e *RevocationStateUnavailableError) RevocationStateUnavailable() bool { return true }

// revocationCacheEntry is one cached answer.
type revocationCacheEntry struct {
	revoked bool

	// expires is when a NOT-REVOKED answer goes stale. It is the zero time
	// for a REVOKED answer, which never expires — see IsCertRevoked.
	expires time.Time
}

// RevocationCache answers "has this peer certificate been revoked?" for the
// authentication layer, backed by a CertProvider and a bounded-TTL cache.
//
// It exists because enforcement belongs on the AUTH path, not in a TLS
// VerifyConnection callback. A TLS callback fires once, at handshake: on QUIC a
// revoked peer holding an established connection would stay authorised
// indefinitely. The auth layer sees every request, which is also why the cost
// of the lookup matters and why this type exists at all.
//
// It satisfies the one-method interface pkg/auth declares
// (auth.RevocationChecker) structurally, so pkg/auth does not import pkg/cert.
//
// The zero value is not usable; construct with NewRevocationCache.
type RevocationCache struct {
	provider CertProvider
	ttl      time.Duration

	// entries maps serial -> revocationCacheEntry. sync.Map rather than a
	// guarded map: the auth path is read-dominated and every request on every
	// transport goes through it.
	entries sync.Map

	// count tracks entries for the reset bound. It over-counts on a racing
	// double insert of the same serial, which only makes the reset marginally
	// eager and never wrong.
	count atomic.Int64
}

// NewRevocationCache returns a RevocationCache over provider.
//
// ttl is the lifetime of a cached NOT-REVOKED answer — the window during which
// a freshly revoked certificate still authenticates on a node that has already
// seen it. A ttl of zero or less disables caching of that answer entirely: every
// request then consults replicated state, which is the strictest posture and
// the one to pick when the window is unacceptable.
//
// A REVOKED answer is cached without expiry regardless of ttl. That is sound
// because revocation here is a one-way door: no operation in go-qrdb removes a
// marker for a certificate still inside its validity window (see
// revocation.go), so a cached "revoked" cannot become wrong.
//
// A nil provider yields a nil cache, so a deployment with no certificate
// provider wired simply has no revocation checker rather than a checker that
// silently answers "not revoked".
func NewRevocationCache(provider CertProvider, ttl time.Duration) *RevocationCache {
	if provider == nil {
		return nil
	}
	return &RevocationCache{provider: provider, ttl: ttl}
}

// TTL returns the configured not-revoked cache lifetime.
func (c *RevocationCache) TTL() time.Duration {
	if c == nil {
		return 0
	}
	return c.ttl
}

// IsCertRevoked reports whether peer has been revoked.
//
// The serial is derived with FormatSerial, the same function that keys the
// certificate record and the revocation marker, so the enforcement lookup
// cannot drift from the issuance format.
//
// Error contract, which the authentication layer depends on:
//
//   - *RevocationStateUnavailableError when the barrier is sealed. There is no
//     revocation state to read before the first unseal, and refusing every
//     mTLS request in that window would make the barrier UNSEALABLE — the
//     unseal endpoints are themselves reached over mTLS. The caller admits the
//     request; what it can reach is separately confined to the sealed-window
//     allowlist (pkg/api/sealedwindow), which is not the certificate's data
//     plane. This is a stated fail-open, bounded to a window in which there is
//     nothing to fail open TO.
//   - Any other error is a lookup that could have answered and did not (a KV
//     read failure, a consensus timeout). The caller fails CLOSED on it.
func (c *RevocationCache) IsCertRevoked(ctx context.Context, peer *x509.Certificate) (bool, error) {
	if c == nil || peer == nil {
		return false, nil
	}

	serial := FormatSerial(peer.SerialNumber)
	if serial == "" {
		// A certificate with no serial cannot be matched against a marker.
		// It also cannot have been issued here. Deny rather than admit an
		// identity no revocation decision can ever apply to.
		return true, nil
	}

	if revoked, ok := c.lookup(serial); ok {
		return revoked, nil
	}

	revoked, err := c.provider.IsRevoked(ctx, serial)
	if err != nil {
		var sealed *BarrierSealedError
		if errors.As(err, &sealed) {
			return false, &RevocationStateUnavailableError{Err: err}
		}
		return false, err
	}

	c.store(serial, revoked)
	return revoked, nil
}

// lookup returns a cached answer and whether one was usable.
func (c *RevocationCache) lookup(serial string) (bool, bool) {
	v, ok := c.entries.Load(serial)
	if !ok {
		return false, false
	}
	entry, ok := v.(revocationCacheEntry)
	if !ok {
		return false, false
	}
	if entry.revoked {
		// One-way door: a revoked serial is never un-revoked while its
		// certificate is still valid, so this answer cannot go stale.
		return true, true
	}
	if time.Now().After(entry.expires) {
		// LoadAndDelete rather than Delete: two goroutines evicting the same
		// stale entry must not each decrement, or the bound drifts upward and
		// the reset stops firing when it should.
		if _, loaded := c.entries.LoadAndDelete(serial); loaded {
			c.count.Add(-1)
		}
		return false, false
	}
	return false, true
}

// store caches an answer, resetting the cache if it has grown past its bound.
func (c *RevocationCache) store(serial string, revoked bool) {
	if !revoked && c.ttl <= 0 {
		// Caching of NOT-REVOKED answers is disabled: every request consults
		// replicated state.
		return
	}

	entry := revocationCacheEntry{revoked: revoked}
	if !revoked {
		entry.expires = time.Now().Add(c.ttl)
	}

	if c.count.Load() >= defaultRevocationCacheMaxEntries {
		c.entries.Clear()
		c.count.Store(0)
	}
	if _, loaded := c.entries.Swap(serial, entry); !loaded {
		c.count.Add(1)
	}
}
