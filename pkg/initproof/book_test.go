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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

var (
	connA = bytes.Repeat([]byte{0xa}, ExporterLength)
	connB = bytes.Repeat([]byte{0xb}, ExporterLength)
)

func requireReason(t *testing.T, err error, want Reason) {
	t.Helper()
	var ce *ChallengeError
	if !errors.As(err, &ce) || ce.Reason != want {
		t.Fatalf("err = %v; want *ChallengeError %q", err, want)
	}
}

func TestBook_IssueThenConsumeOnTheSameConnection(t *testing.T) {
	clk := clocktest.New()
	b := NewBook(clk, 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Nonce) != NonceSize || !c.ExpiresAt.Equal(clk.Now().Add(ChallengeTTL)) {
		t.Fatalf("challenge %d bytes, expires %v", len(c.Nonce), c.ExpiresAt)
	}
	if b.Outstanding() != 1 {
		t.Fatalf("outstanding = %d", b.Outstanding())
	}
	if err := b.Consume("acme", c.Nonce, connA); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if b.Outstanding() != 0 {
		t.Fatalf("outstanding after consume = %d", b.Outstanding())
	}
}

func TestBook_ReplayIsRefused(t *testing.T) {
	b := NewBook(clocktest.New(), 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Consume("acme", c.Nonce, connA); err != nil {
		t.Fatal(err)
	}
	requireReason(t, b.Consume("acme", c.Nonce, connA), ReasonUnknownNonce)
}

func TestBook_ExporterMismatchIsRefusedAndSpendsTheNonce(t *testing.T) {
	b := NewBook(clocktest.New(), 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	requireReason(t, b.Consume("acme", c.Nonce, connB), ReasonConnectionMismatch)
	// The rightful connection cannot redeem it afterwards either: a nonce
	// is spent by its first attempt.
	requireReason(t, b.Consume("acme", c.Nonce, connA), ReasonUnknownNonce)
}

func TestBook_EmptyExporterOnCompleteIsAMismatch(t *testing.T) {
	b := NewBook(clocktest.New(), 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	requireReason(t, b.Consume("acme", c.Nonce, nil), ReasonConnectionMismatch)
}

func TestBook_ExpiredChallengeIsRefused(t *testing.T) {
	clk := clocktest.New()
	b := NewBook(clk, 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(ChallengeTTL + time.Second)
	requireReason(t, b.Consume("acme", c.Nonce, connA), ReasonExpired)
}

func TestBook_ChallengeAtExactlyTTLIsStillValid(t *testing.T) {
	clk := clocktest.New()
	b := NewBook(clk, 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(ChallengeTTL)
	if err := b.Consume("acme", c.Nonce, connA); err != nil {
		t.Fatalf("Consume at the boundary: %v", err)
	}
}

func TestBook_NonceForAnotherTenantIsUnknown(t *testing.T) {
	b := NewBook(clocktest.New(), 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	requireReason(t, b.Consume("globex", c.Nonce, connA), ReasonUnknownNonce)
}

func TestBook_AbsentAndNeverIssuedNonces(t *testing.T) {
	b := NewBook(nil, 0)
	requireReason(t, b.Consume("acme", nil, connA), ReasonAbsent)
	requireReason(t, b.Consume("acme", []byte("never issued"), connA), ReasonUnknownNonce)
}

func TestBook_IssueRefusesAConnectionWithoutExporter(t *testing.T) {
	b := NewBook(clocktest.New(), 0)
	var ne *NoExporterError
	if _, err := b.Issue("acme", nil); !errors.As(err, &ne) {
		t.Fatalf("Issue without exporter: %v", err)
	}
	if b.Outstanding() != 0 {
		t.Fatal("a refused issue held a slot")
	}
}

func TestBook_LimitIsEnforcedAndExpiredSlotsAreReclaimed(t *testing.T) {
	clk := clocktest.New()
	b := NewBook(clk, 2)
	for i := 0; i < 2; i++ {
		if _, err := b.Issue("acme", connA); err != nil {
			t.Fatal(err)
		}
	}
	var tm *TooManyChallengesError
	if _, err := b.Issue("acme", connA); !errors.As(err, &tm) || tm.Limit != 2 {
		t.Fatalf("third issue: %v", err)
	}
	clk.Advance(ChallengeTTL + time.Second)
	if _, err := b.Issue("acme", connA); err != nil {
		t.Fatalf("issue after expiry: %v", err)
	}
	if b.Outstanding() != 1 {
		t.Fatalf("outstanding after sweep = %d", b.Outstanding())
	}
}

func TestBook_ConcurrentCompletionsRedeemANonceOnce(t *testing.T) {
	b := NewBook(clocktest.New(), 0)
	c, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Consume("acme", c.Nonce, connA) == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d completions redeemed one nonce", wins.Load())
	}
}

func TestBook_NoncesAreUnique(t *testing.T) {
	b := NewBook(clocktest.New(), 0)
	c1, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := b.Issue("acme", connA)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(c1.Nonce, c2.Nonce) {
		t.Fatal("two issues returned the same nonce")
	}
}
