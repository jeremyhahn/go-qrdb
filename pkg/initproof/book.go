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

package initproof

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
)

const (
	// NonceSize is the number of random bytes in an init nonce.
	NonceSize = 32

	// ChallengeTTL is how long an issued nonce may be completed.
	ChallengeTTL = 5 * time.Minute

	// DefaultMaxOutstanding bounds the unexpired challenges one node holds,
	// so begin cannot be used to grow its memory without limit.
	DefaultMaxOutstanding = 4096
)

// Challenge is what begin returns: the nonce to sign and when it lapses.
type Challenge struct {
	Nonce     []byte
	ExpiresAt time.Time
}

// entry is one outstanding challenge: the tenant it was issued for, the
// exporter of the connection it was issued on, and its expiry.
type entry struct {
	tenantID  string
	exporter  []byte
	expiresAt time.Time
}

// Book holds the init challenges this node has issued and not yet seen
// completed. A challenge is bound to the connection it was issued on, so it
// lives with that connection's node, in memory, exactly like the officer
// claim's: it is never state another node or a restart needs.
//
// Book is safe for concurrent use and takes no lock: redeeming a nonce is a
// single LoadAndDelete, so two completions racing on one nonce cannot both
// succeed.
type Book struct {
	clock   clock.Clock
	limit   int64
	entries sync.Map // string(nonce) -> *entry
	count   atomic.Int64
}

// NewBook returns an empty Book on clk (clock.Real() when nil) holding at
// most limit unexpired challenges (DefaultMaxOutstanding when limit <= 0).
func NewBook(clk clock.Clock, limit int) *Book {
	if clk == nil {
		clk = clock.Real()
	}
	if limit <= 0 {
		limit = DefaultMaxOutstanding
	}
	return &Book{clock: clk, limit: int64(limit)}
}

// Issue records a fresh nonce for tenantID on the connection whose exporter
// is given and returns it. It returns *NoExporterError when exporter is empty,
// and *TooManyChallengesError when the node already holds its limit of
// unexpired challenges.
func (b *Book) Issue(tenantID string, exporter []byte) (*Challenge, error) {
	if len(exporter) == 0 {
		return nil, &NoExporterError{}
	}
	if !b.reserve() {
		b.sweep()
		if !b.reserve() {
			return nil, &TooManyChallengesError{Limit: int(b.limit)}
		}
	}
	nonce := make([]byte, NonceSize)
	_, _ = rand.Read(nonce) // crypto/rand.Read never fails (Go 1.24+).
	e := &entry{
		tenantID:  tenantID,
		exporter:  bytes.Clone(exporter),
		expiresAt: b.clock.Now().Add(ChallengeTTL),
	}
	b.entries.Store(string(nonce), e)
	return &Challenge{Nonce: nonce, ExpiresAt: e.expiresAt}, nil
}

// Consume redeems nonce for tenantID on the connection whose exporter is
// given. The nonce is spent by the attempt whatever its outcome, so a second
// attempt with it -- a replay -- is refused as unknown. It returns
// *ChallengeError naming the reason a nonce cannot be redeemed.
func (b *Book) Consume(tenantID string, nonce, exporter []byte) error {
	if len(nonce) == 0 {
		return &ChallengeError{Reason: ReasonAbsent}
	}
	v, ok := b.entries.LoadAndDelete(string(nonce))
	if !ok {
		return &ChallengeError{Reason: ReasonUnknownNonce}
	}
	b.count.Add(-1)
	e, ok := v.(*entry)
	if !ok || e.tenantID != tenantID {
		return &ChallengeError{Reason: ReasonUnknownNonce}
	}
	if b.clock.Now().After(e.expiresAt) {
		return &ChallengeError{Reason: ReasonExpired}
	}
	if len(exporter) == 0 || subtle.ConstantTimeCompare(e.exporter, exporter) != 1 {
		return &ChallengeError{Reason: ReasonConnectionMismatch}
	}
	return nil
}

// Outstanding reports how many challenges the book holds, expired ones
// included until a sweep removes them.
func (b *Book) Outstanding() int {
	return int(b.count.Load())
}

// reserve claims one slot under the limit.
func (b *Book) reserve() bool {
	for {
		n := b.count.Load()
		if n >= b.limit {
			return false
		}
		if b.count.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// sweep drops every expired challenge, releasing its slot.
func (b *Book) sweep() {
	now := b.clock.Now()
	b.entries.Range(func(k, v any) bool {
		if e, ok := v.(*entry); !ok || now.After(e.expiresAt) {
			if _, loaded := b.entries.LoadAndDelete(k); loaded {
				b.count.Add(-1)
			}
		}
		return true
	})
}
