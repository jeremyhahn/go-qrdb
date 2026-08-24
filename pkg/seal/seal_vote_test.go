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

package seal

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

// newSealVoteBarrier returns an initialized direct-mode Shamir barrier
// (threshold=2, total=3) suitable for SubmitSealVote tests.
func newSealVoteBarrier(t *testing.T) (*Barrier, []string) {
	t.Helper()
	b, dist := newShamirTestBarrierDirect(t)
	ctx := context.Background()
	if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()
	if len(shares) != 3 {
		t.Fatalf("expected 3 shares, got %d", len(shares))
	}
	return b, shares
}

// TestSubmitSealVote_NotConfigured verifies ErrShamirNotConfigured when the
// barrier has no Shamir config.
func TestSubmitSealVote_NotConfigured(t *testing.T) {
	store := NewMemoryBackend()
	sw := testSoftwareStrategy(t)
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, sw)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	ctx := context.Background()
	_, gotErr := b.SubmitSealVote(ctx, "someshare")
	if !errors.Is(gotErr, ErrShamirNotConfigured) {
		t.Errorf("got %v, want ErrShamirNotConfigured", gotErr)
	}
}

// TestSubmitSealVote_AlreadySealed verifies ErrSealed when barrier is sealed.
func TestSubmitSealVote_AlreadySealed(t *testing.T) {
	b, _ := newShamirTestBarrierDirect(t)
	// Barrier starts sealed (never initialized in this test).
	ctx := context.Background()
	_, gotErr := b.SubmitSealVote(ctx, "someshare")
	if !errors.Is(gotErr, ErrSealed) {
		t.Errorf("got %v, want ErrSealed", gotErr)
	}
}

// TestSubmitSealVote_HappyPath verifies that M-of-N votes seal the barrier.
func TestSubmitSealVote_HappyPath(t *testing.T) {
	b, shares := newSealVoteBarrier(t)
	ctx := context.Background()

	// Barrier should be unsealed after init.
	if b.IsSealed() {
		t.Fatal("expected barrier unsealed after InitializeShamir")
	}

	// Submit first share — threshold not yet met.
	prog1, err := b.SubmitSealVote(ctx, shares[0])
	if err != nil {
		t.Fatalf("SubmitSealVote[0]: %v", err)
	}
	if prog1.Complete {
		t.Error("expected prog1.Complete==false after first share")
	}
	if prog1.Submitted != 1 {
		t.Errorf("expected Submitted=1, got %d", prog1.Submitted)
	}
	if prog1.Required != 2 {
		t.Errorf("expected Required=2, got %d", prog1.Required)
	}
	if b.IsSealed() {
		t.Error("barrier should not be sealed after first share")
	}

	// Submit second share — threshold met, barrier seals.
	prog2, err := b.SubmitSealVote(ctx, shares[1])
	if err != nil {
		t.Fatalf("SubmitSealVote[1]: %v", err)
	}
	if !prog2.Complete {
		t.Error("expected prog2.Complete==true after second share")
	}
	if !b.IsSealed() {
		t.Error("barrier should be sealed after quorum met")
	}
}

// TestSubmitSealVote_DuplicateShare verifies ErrShamirDuplicateShare.
func TestSubmitSealVote_DuplicateShare(t *testing.T) {
	b, shares := newSealVoteBarrier(t)
	ctx := context.Background()

	if _, err := b.SubmitSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first SubmitSealVote: %v", err)
	}
	_, gotErr := b.SubmitSealVote(ctx, shares[0])
	if !errors.Is(gotErr, ErrShamirDuplicateShare) {
		t.Errorf("got %v, want ErrShamirDuplicateShare", gotErr)
	}
}

// TestSubmitSealVote_GarbageSharesVerificationFailed verifies that garbage
// shares reaching the threshold return ErrSealVoteVerificationFailed and
// reset the accumulator (new votes can be submitted after).
func TestSubmitSealVote_GarbageSharesVerificationFailed(t *testing.T) {
	b, _ := newSealVoteBarrier(t)
	ctx := context.Background()

	// Submit two distinct valid-base64 shares to meet threshold=2. They pass
	// NewProtectedShare's format check and dedup but are not real Shamir
	// shares, so reconstruction fails verification once the threshold is met.
	// (Malformed, non-base64 input is now rejected earlier, at submission.)
	_, err1 := b.SubmitSealVote(ctx, base64.StdEncoding.EncodeToString([]byte("garbage-share-payload-a")))
	if err1 != nil {
		// First share should be accepted (verification cannot happen until
		// the threshold is met).
		t.Fatalf("first garbage share unexpected error: %v", err1)
	}
	_, gotErr := b.SubmitSealVote(ctx, base64.StdEncoding.EncodeToString([]byte("garbage-share-payload-b")))
	if !errors.Is(gotErr, ErrSealVoteVerificationFailed) {
		t.Errorf("got %v, want ErrSealVoteVerificationFailed", gotErr)
	}

	// Barrier must not be sealed.
	if b.IsSealed() {
		t.Error("barrier must not be sealed after failed verification")
	}
}

// TestSubmitSealVote_MalformedShareRejected proves a non-base64 share is
// rejected at SUBMISSION by NewProtectedShare's validation, before it reaches
// the accumulator or locked memory.
func TestSubmitSealVote_MalformedShareRejected(t *testing.T) {
	b, _ := newSealVoteBarrier(t)
	ctx := context.Background()

	if _, err := b.SubmitSealVote(ctx, "not valid base64!!"); err == nil {
		t.Fatal("expected a malformed share to be rejected at submission")
	} else if !errors.Is(err, ErrShamirCombineFailed) {
		t.Errorf("expected ErrShamirCombineFailed, got %v", err)
	}

	// Barrier must not be sealed.
	if b.IsSealed() {
		t.Error("barrier must not be sealed after failed verification")
	}
}

// TestSubmitSealVote_ProgressCounts verifies the Submitted/Required counters.
func TestSubmitSealVote_ProgressCounts(t *testing.T) {
	b, shares := newSealVoteBarrier(t)
	ctx := context.Background()

	prog, err := b.SubmitSealVote(ctx, shares[2])
	if err != nil {
		t.Fatalf("SubmitSealVote: %v", err)
	}
	if prog.Submitted != 1 || prog.Required != 2 {
		t.Errorf("expected Submitted=1, Required=2; got %d, %d", prog.Submitted, prog.Required)
	}
}

// TestDistributorSettersGetters verifies the distributor accessor roundtrip.
func TestDistributorSettersGetters(t *testing.T) {
	// Create a barrier without a pre-wired distributor to test the nil
	// state, then verify setters and getters work correctly.
	store := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 2, TotalShares: 3},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	if b.CeremonyDistributor() != nil {
		t.Error("expected nil distributor before set")
	}
	if b.RecoveryDistributor() != nil {
		t.Error("expected nil recovery distributor before set")
	}

	stub := &stubDistributor{}
	b.SetCeremonyDistributor(stub)
	b.SetRecoveryDistributor(stub)

	if b.CeremonyDistributor() != stub {
		t.Error("CeremonyDistributor() should return the stub")
	}
	if b.RecoveryDistributor() != stub {
		t.Error("RecoveryDistributor() should return the stub")
	}
}

// stubDistributor satisfies CeremonyDistributor for testing.
type stubDistributor struct{}

func (s *stubDistributor) SealAndDistribute(_ context.Context, _ []byte, _, _ int) (int, error) {
	return 0, nil
}
func (s *stubDistributor) RekeyAndDistribute(_ context.Context, _ []byte, _, _ int) (int, error) {
	return 0, nil
}
