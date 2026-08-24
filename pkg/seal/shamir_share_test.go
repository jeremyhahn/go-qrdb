// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// mkShare builds a ProtectedShare carrying a standard-base64 value for tests
// that exercise the accumulator's bookkeeping — dedup, progress, TTL — where
// the shares need not actually reconstruct. NewProtectedShare validates the
// base64 shape and header but does not parse the Shamir payload (that happens
// at combine), so an arbitrary distinct seed is enough to make a distinct
// share. The accumulator TAKES OWNERSHIP of every share handed to it, so a
// share passed to AddProtectedShare must not be destroyed by the test.
func mkShare(t *testing.T, index, threshold, total int, seed string) *shamir.ProtectedShare {
	t.Helper()
	val := base64.StdEncoding.EncodeToString([]byte(seed))
	s, err := shamir.NewProtectedShare(index, threshold, total, []byte(val))
	if err != nil {
		t.Fatalf("NewProtectedShare(%q): %v", seed, err)
	}
	return s
}

// ---------- ShareAccumulator (shamirShareAccumulator) tests ----------

func TestShareAccumulator_AddShare_UniqueShares(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	prog, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 2, 3, "share-a"))
	if err != nil {
		t.Fatalf("AddProtectedShare(share-a): %v", err)
	}
	if prog.Submitted != 1 {
		t.Errorf("expected Submitted=1, got %d", prog.Submitted)
	}
	if prog.Complete {
		t.Error("expected Complete=false after 1 share")
	}

	prog, err = acc.AddProtectedShare(ctx, mkShare(t, 2, 2, 3, "share-b"))
	if err != nil {
		t.Fatalf("AddProtectedShare(share-b): %v", err)
	}
	if prog.Submitted != 2 {
		t.Errorf("expected Submitted=2, got %d", prog.Submitted)
	}
	if !prog.Complete {
		t.Error("expected Complete=true after 2 shares (threshold=2)")
	}
}

func TestShareAccumulator_AddShare_DuplicateRejected(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	if _, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 2, 3, "dup-val")); err != nil {
		t.Fatalf("first AddProtectedShare: %v", err)
	}

	// A second share with the SAME value digests to the same key and is a
	// duplicate. The accumulator takes ownership and destroys it either way.
	_, err := acc.AddProtectedShare(ctx, mkShare(t, 2, 2, 3, "dup-val"))
	if err != ErrShamirDuplicateShare {
		t.Errorf("expected ErrShamirDuplicateShare, got %v", err)
	}
}

func TestShareAccumulator_AddShare_ExpiredTTL(t *testing.T) {
	// Create with zero TTL so it expires immediately.
	acc := NewShamirShareAccumulator(2, 3, 0)
	time.Sleep(1 * time.Millisecond)

	ctx := context.Background()
	_, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 2, 3, "late-share"))
	if err != ErrShamirQuorumExpired {
		t.Errorf("expected ErrShamirQuorumExpired, got %v", err)
	}
}

func TestShareAccumulator_Progress(t *testing.T) {
	acc := NewShamirShareAccumulator(3, 5, 5*time.Minute)
	ctx := context.Background()

	// First share: Required=3, Submitted=1, Complete=false.
	prog, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 3, 5, "share-1"))
	if err != nil {
		t.Fatalf("AddProtectedShare(1): %v", err)
	}
	if prog.Required != 3 {
		t.Errorf("expected Required=3, got %d", prog.Required)
	}
	if prog.Submitted != 1 {
		t.Errorf("expected Submitted=1, got %d", prog.Submitted)
	}
	if prog.Complete {
		t.Error("expected Complete=false")
	}

	// Add remaining shares to meet threshold.
	for i := 2; i <= 3; i++ {
		prog, err = acc.AddProtectedShare(ctx, mkShare(t, i, 3, 5, string(rune('a'+i))))
		if err != nil {
			t.Fatalf("AddProtectedShare(%d): %v", i, err)
		}
	}

	if prog.Submitted != 3 {
		t.Errorf("expected Submitted=3, got %d", prog.Submitted)
	}
	if !prog.Complete {
		t.Error("expected Complete=true")
	}
}

func TestShareAccumulator_Reconstruct_BelowThreshold(t *testing.T) {
	acc := NewShamirShareAccumulator(3, 5, 5*time.Minute)
	ctx := context.Background()

	// Only add 1 share, threshold is 3.
	if _, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 3, 5, "share-1")); err != nil {
		t.Fatalf("AddProtectedShare: %v", err)
	}

	_, err := acc.ReconstructProtected(ctx)
	if err != ErrShamirQuorumIncomplete {
		t.Errorf("expected ErrShamirQuorumIncomplete, got %v", err)
	}
}

func TestShareAccumulator_Reconstruct_Success(t *testing.T) {
	// Use the splitter to generate real shares, then accumulate and reconstruct.
	secret := []byte("test-secret-data")
	splitter := NewShamirShareSplitter()
	shares, err := splitter.SplitProtected(secret, 2, 3)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	// The accumulator takes ownership of the shares it is given; destroy the
	// remainder so no locked-memory share leaks.
	defer destroyShares(shares[2:])

	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	for _, share := range shares[:2] {
		if _, err := acc.AddProtectedShare(ctx, share); err != nil {
			t.Fatalf("AddProtectedShare: %v", err)
		}
	}

	secretEnclave, err := acc.ReconstructProtected(ctx)
	if err != nil {
		t.Fatalf("ReconstructProtected: %v", err)
	}
	defer secretEnclave.Destroy()

	assertEnclaveEquals(t, secretEnclave, secret)
}

func TestShareAccumulator_Reset(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	if _, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 2, 3, "share-1")); err != nil {
		t.Fatalf("AddProtectedShare: %v", err)
	}

	acc.Reset()

	// After reset, the same share value should be accepted again (no duplicate).
	prog, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 2, 3, "share-1"))
	if err != nil {
		t.Fatalf("AddProtectedShare after Reset: %v", err)
	}
	if prog.Submitted != 1 {
		t.Errorf("expected Submitted=1 after Reset, got %d", prog.Submitted)
	}
}

func TestShareAccumulator_Expired(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	if acc.Expired() {
		t.Error("fresh accumulator should not be expired")
	}

	// Create one with zero TTL.
	expiredAcc := NewShamirShareAccumulator(2, 3, 0)
	time.Sleep(1 * time.Millisecond)
	if !expiredAcc.Expired() {
		t.Error("accumulator with zero TTL should be expired")
	}
}

func TestShareAccumulator_Reset_RefreshesTTL(t *testing.T) {
	// Create with a very short TTL.
	acc := NewShamirShareAccumulator(2, 3, 50*time.Millisecond)
	time.Sleep(60 * time.Millisecond)

	if !acc.Expired() {
		t.Error("expected accumulator to be expired after TTL")
	}

	// Reset should refresh the createdAt.
	acc.Reset()

	if acc.Expired() {
		t.Error("expected accumulator to not be expired after Reset")
	}
}

// ---------- GetProgress tests ----------

func TestShareAccumulator_GetProgress_Empty(t *testing.T) {
	acc := NewShamirShareAccumulator(3, 5, 5*time.Minute)

	prog := acc.GetProgress()
	if prog.Required != 3 {
		t.Errorf("expected Required=3, got %d", prog.Required)
	}
	if prog.Submitted != 0 {
		t.Errorf("expected Submitted=0, got %d", prog.Submitted)
	}
	if prog.Complete {
		t.Error("expected Complete=false for empty accumulator")
	}
}

func TestShareAccumulator_GetProgress_AfterShares(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	// Add one share and verify progress.
	if _, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 2, 3, "share-1")); err != nil {
		t.Fatalf("AddProtectedShare: %v", err)
	}

	prog := acc.GetProgress()
	if prog.Required != 2 {
		t.Errorf("expected Required=2, got %d", prog.Required)
	}
	if prog.Submitted != 1 {
		t.Errorf("expected Submitted=1, got %d", prog.Submitted)
	}
	if prog.Complete {
		t.Error("expected Complete=false after 1 share with threshold=2")
	}

	// Add second share to meet threshold.
	if _, err := acc.AddProtectedShare(ctx, mkShare(t, 2, 2, 3, "share-2")); err != nil {
		t.Fatalf("AddProtectedShare: %v", err)
	}

	prog = acc.GetProgress()
	if prog.Submitted != 2 {
		t.Errorf("expected Submitted=2, got %d", prog.Submitted)
	}
	if !prog.Complete {
		t.Error("expected Complete=true after meeting threshold")
	}
}

func TestShareAccumulator_GetProgress_AfterReset(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	if _, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 2, 3, "share-1")); err != nil {
		t.Fatalf("AddProtectedShare: %v", err)
	}

	acc.Reset()

	prog := acc.GetProgress()
	if prog.Submitted != 0 {
		t.Errorf("expected Submitted=0 after Reset, got %d", prog.Submitted)
	}
	if prog.Complete {
		t.Error("expected Complete=false after Reset")
	}
}

// ---------- ShareSplitter (shamirShareSplitter) tests ----------

func TestShareSplitter_Split_Success(t *testing.T) {
	splitter := NewShamirShareSplitter()
	secret := []byte("split-me")

	shares, err := splitter.SplitProtected(secret, 2, 3)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	defer destroyShares(shares)

	if len(shares) != 3 {
		t.Fatalf("expected 3 shares, got %d", len(shares))
	}

	// Verify each share carries a non-empty protected value.
	for i, share := range shares {
		if err := share.Validate(); err != nil {
			t.Errorf("share %d is invalid: %v", i, err)
		}
	}
}

func TestShareSplitter_Split_InvalidThreshold(t *testing.T) {
	splitter := NewShamirShareSplitter()

	// threshold < 2 is invalid for Shamir.
	shares, err := splitter.SplitProtected([]byte("secret"), 1, 3)
	defer destroyShares(shares)
	if err == nil {
		t.Fatal("expected error for threshold < 2")
	}
}

func TestShareSplitter_Split_ThresholdExceedsTotal(t *testing.T) {
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected([]byte("secret"), 5, 3)
	defer destroyShares(shares)
	if err == nil {
		t.Fatal("expected error for threshold > total")
	}
}

func TestShareSplitter_Split_EmptySecret(t *testing.T) {
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected([]byte{}, 2, 3)
	defer destroyShares(shares)
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestShareSplitter_RoundTrip(t *testing.T) {
	// Split a secret, accumulate threshold shares, and reconstruct.
	secret := []byte("round-trip-test-secret")
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected(secret, 3, 5)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	defer destroyShares(shares[3:])

	acc := NewShamirShareAccumulator(3, 5, 5*time.Minute)
	ctx := context.Background()

	// Use only the first 3 shares (threshold).
	for i := 0; i < 3; i++ {
		if _, err := acc.AddProtectedShare(ctx, shares[i]); err != nil {
			t.Fatalf("AddProtectedShare(%d): %v", i, err)
		}
	}

	secretEnclave, err := acc.ReconstructProtected(ctx)
	if err != nil {
		t.Fatalf("ReconstructProtected: %v", err)
	}
	defer secretEnclave.Destroy()

	assertEnclaveEquals(t, secretEnclave, secret)
}

func TestShareSplitter_RoundTrip_DifferentShareSubset(t *testing.T) {
	// Verify that any subset of threshold shares can reconstruct.
	secret := []byte("any-subset-works")
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected(secret, 2, 5)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	// Shares 2 and 4 go to the accumulator; the rest must be destroyed here.
	defer destroyShares([]*shamir.ProtectedShare{shares[0], shares[1], shares[3]})

	acc := NewShamirShareAccumulator(2, 5, 5*time.Minute)
	ctx := context.Background()

	if _, err := acc.AddProtectedShare(ctx, shares[2]); err != nil {
		t.Fatalf("AddProtectedShare(2): %v", err)
	}
	if _, err := acc.AddProtectedShare(ctx, shares[4]); err != nil {
		t.Fatalf("AddProtectedShare(4): %v", err)
	}

	secretEnclave, err := acc.ReconstructProtected(ctx)
	if err != nil {
		t.Fatalf("ReconstructProtected: %v", err)
	}
	defer secretEnclave.Destroy()

	assertEnclaveEquals(t, secretEnclave, secret)
}

func TestShareSplitter_RoundTrip_BinarySecret(t *testing.T) {
	// Regression guard for the protected reconstruction path
	// (shamir.CombineProtected): a binary secret containing embedded NUL
	// bytes and every byte value must survive SplitProtected ->
	// AddProtectedShare -> ReconstructProtected unchanged. A string-based
	// path that truncates on NUL or mangles non-UTF-8 bytes would corrupt it.
	secret := make([]byte, 256)
	for i := range secret {
		secret[i] = byte(i)
	}

	splitter := NewShamirShareSplitter()
	shares, err := splitter.SplitProtected(secret, 3, 5)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	defer destroyShares(shares[3:])

	acc := NewShamirShareAccumulator(3, 5, 5*time.Minute)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := acc.AddProtectedShare(ctx, shares[i]); err != nil {
			t.Fatalf("AddProtectedShare(%d): %v", i, err)
		}
	}

	secretEnclave, err := acc.ReconstructProtected(ctx)
	if err != nil {
		t.Fatalf("ReconstructProtected: %v", err)
	}
	defer secretEnclave.Destroy()

	assertEnclaveEquals(t, secretEnclave, secret)
}

// ---------- Compile-time interface checks ----------

func TestShareAccumulator_ImplementsInterface(t *testing.T) {
	var acc interface{} = NewShamirShareAccumulator(2, 3, time.Minute)
	if _, ok := acc.(ShareAccumulator); !ok {
		t.Fatal("ShamirShareAccumulator does not implement ShareAccumulator")
	}
}

func TestShareSplitter_ImplementsInterface(t *testing.T) {
	var splitter interface{} = NewShamirShareSplitter()
	if _, ok := splitter.(ShareSplitter); !ok {
		t.Fatal("ShamirShareSplitter does not implement ShareSplitter")
	}
}

// destroyShares releases every protected share in the slice. Safe on nil
// entries and a nil slice.
func destroyShares(shares []*shamir.ProtectedShare) {
	for _, s := range shares {
		if s != nil {
			s.Destroy()
		}
	}
}

// assertEnclaveEquals asserts the reconstructed secret held in the enclave
// equals want, reading it only inside the protected borrow.
func assertEnclaveEquals(t *testing.T, enc interface {
	WithBytes(func([]byte) error) error
}, want []byte) {
	t.Helper()
	if err := enc.WithBytes(func(got []byte) error {
		if !bytes.Equal(got, want) {
			t.Errorf("reconstructed secret mismatch: got %d bytes, want %d bytes", len(got), len(want))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}
