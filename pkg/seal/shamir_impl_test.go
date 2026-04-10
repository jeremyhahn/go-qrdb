// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"testing"
	"time"
)

// ---------- ShareAccumulator (shamirShareAccumulator) tests ----------

func TestShareAccumulator_AddShare_UniqueShares(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	prog, err := acc.AddShare(ctx, "share-a")
	if err != nil {
		t.Fatalf("AddShare(share-a): %v", err)
	}
	if prog.Submitted != 1 {
		t.Errorf("expected Submitted=1, got %d", prog.Submitted)
	}
	if prog.Complete {
		t.Error("expected Complete=false after 1 share")
	}

	prog, err = acc.AddShare(ctx, "share-b")
	if err != nil {
		t.Fatalf("AddShare(share-b): %v", err)
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

	_, err := acc.AddShare(ctx, "dup-val")
	if err != nil {
		t.Fatalf("first AddShare: %v", err)
	}

	_, err = acc.AddShare(ctx, "dup-val")
	if err != ErrShamirDuplicateShare {
		t.Errorf("expected ErrShamirDuplicateShare, got %v", err)
	}
}

func TestShareAccumulator_AddShare_ExpiredTTL(t *testing.T) {
	// Create with zero TTL so it expires immediately.
	acc := NewShamirShareAccumulator(2, 3, 0)
	time.Sleep(1 * time.Millisecond)

	ctx := context.Background()
	_, err := acc.AddShare(ctx, "late-share")
	if err != ErrShamirQuorumExpired {
		t.Errorf("expected ErrShamirQuorumExpired, got %v", err)
	}
}

func TestShareAccumulator_Progress(t *testing.T) {
	acc := NewShamirShareAccumulator(3, 5, 5*time.Minute)
	ctx := context.Background()

	// First share: Required=3, Submitted=1, Complete=false.
	prog, err := acc.AddShare(ctx, "share-1")
	if err != nil {
		t.Fatalf("AddShare(1): %v", err)
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
		prog, err = acc.AddShare(ctx, string(rune('a'+i)))
		if err != nil {
			t.Fatalf("AddShare(%d): %v", i, err)
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
	_, err := acc.AddShare(ctx, "share-1")
	if err != nil {
		t.Fatalf("AddShare: %v", err)
	}

	_, err = acc.Reconstruct(ctx)
	if err != ErrShamirQuorumIncomplete {
		t.Errorf("expected ErrShamirQuorumIncomplete, got %v", err)
	}
}

func TestShareAccumulator_Reconstruct_Success(t *testing.T) {
	// Use the splitter to generate real shares, then accumulate and reconstruct.
	secret := []byte("test-secret-data")
	splitter := NewShamirShareSplitter()
	shares, err := splitter.Split(secret, 2, 3)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}

	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	for _, share := range shares[:2] {
		_, err := acc.AddShare(ctx, share)
		if err != nil {
			t.Fatalf("AddShare: %v", err)
		}
	}

	reconstructed, err := acc.Reconstruct(ctx)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}

	if string(reconstructed) != string(secret) {
		t.Errorf("expected %q, got %q", secret, reconstructed)
	}
}

func TestShareAccumulator_Reset(t *testing.T) {
	acc := NewShamirShareAccumulator(2, 3, 5*time.Minute)
	ctx := context.Background()

	_, err := acc.AddShare(ctx, "share-1")
	if err != nil {
		t.Fatalf("AddShare: %v", err)
	}

	acc.Reset()

	// After reset, the same share should be accepted again (no duplicate).
	prog, err := acc.AddShare(ctx, "share-1")
	if err != nil {
		t.Fatalf("AddShare after Reset: %v", err)
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
	_, err := acc.AddShare(ctx, "share-1")
	if err != nil {
		t.Fatalf("AddShare: %v", err)
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
	_, err = acc.AddShare(ctx, "share-2")
	if err != nil {
		t.Fatalf("AddShare: %v", err)
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

	_, err := acc.AddShare(ctx, "share-1")
	if err != nil {
		t.Fatalf("AddShare: %v", err)
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

	shares, err := splitter.Split(secret, 2, 3)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}

	if len(shares) != 3 {
		t.Fatalf("expected 3 shares, got %d", len(shares))
	}

	// Verify each share is non-empty.
	for i, share := range shares {
		if share == "" {
			t.Errorf("share %d is empty", i)
		}
	}
}

func TestShareSplitter_Split_InvalidThreshold(t *testing.T) {
	splitter := NewShamirShareSplitter()

	// threshold < 2 is invalid for Shamir.
	_, err := splitter.Split([]byte("secret"), 1, 3)
	if err == nil {
		t.Fatal("expected error for threshold < 2")
	}
}

func TestShareSplitter_Split_ThresholdExceedsTotal(t *testing.T) {
	splitter := NewShamirShareSplitter()

	_, err := splitter.Split([]byte("secret"), 5, 3)
	if err == nil {
		t.Fatal("expected error for threshold > total")
	}
}

func TestShareSplitter_Split_EmptySecret(t *testing.T) {
	splitter := NewShamirShareSplitter()

	_, err := splitter.Split([]byte{}, 2, 3)
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestShareSplitter_RoundTrip(t *testing.T) {
	// Split a secret, accumulate threshold shares, and reconstruct.
	secret := []byte("round-trip-test-secret")
	splitter := NewShamirShareSplitter()

	shares, err := splitter.Split(secret, 3, 5)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}

	acc := NewShamirShareAccumulator(3, 5, 5*time.Minute)
	ctx := context.Background()

	// Use only the first 3 shares (threshold).
	for i := 0; i < 3; i++ {
		_, err := acc.AddShare(ctx, shares[i])
		if err != nil {
			t.Fatalf("AddShare(%d): %v", i, err)
		}
	}

	reconstructed, err := acc.Reconstruct(ctx)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}

	if string(reconstructed) != string(secret) {
		t.Errorf("expected %q, got %q", secret, reconstructed)
	}
}

func TestShareSplitter_RoundTrip_DifferentShareSubset(t *testing.T) {
	// Verify that any subset of threshold shares can reconstruct.
	secret := []byte("any-subset-works")
	splitter := NewShamirShareSplitter()

	shares, err := splitter.Split(secret, 2, 5)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}

	// Use shares at index 2 and 4 (not the first ones).
	acc := NewShamirShareAccumulator(2, 5, 5*time.Minute)
	ctx := context.Background()

	_, err = acc.AddShare(ctx, shares[2])
	if err != nil {
		t.Fatalf("AddShare(2): %v", err)
	}
	_, err = acc.AddShare(ctx, shares[4])
	if err != nil {
		t.Fatalf("AddShare(4): %v", err)
	}

	reconstructed, err := acc.Reconstruct(ctx)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}

	if string(reconstructed) != string(secret) {
		t.Errorf("expected %q, got %q", secret, reconstructed)
	}
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
