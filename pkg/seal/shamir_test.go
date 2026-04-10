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
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// testLogger returns a discard logger shared across all test files.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// initializeBarrier initializes a barrier with a standard test password.
// Shared across multiple test files.
func initializeBarrier(t *testing.T, b *Barrier) {
	t.Helper()
	ctx := context.Background()
	if err := b.Initialize(ctx, Credentials{Secret: "test-password"}); err != nil {
		t.Fatal(err)
	}
}

// testSoftwareStrategy creates a SoftwareStrategy with a test passphrase.
// Shared across multiple test files.
func testSoftwareStrategy(t *testing.T) *SoftwareStrategy {
	t.Helper()
	s, err := NewSoftwareStrategy([]byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return s
}

// newShamirTestBarrier creates a barrier with a SoftwareStrategy for
// credential-mode Shamir tests.
func newShamirTestBarrier(t *testing.T) *Barrier {
	t.Helper()
	store := NewMemoryBackend()
	strategy := testSoftwareStrategy(t)
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir: &ShamirConfig{
			Threshold:   3,
			TotalShares: 5,
		},
	}, strategy)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	return b
}

// newShamirTestBarrierDirect creates a barrier with both ShamirStrategy and
// SoftwareStrategy for direct-mode tests where the root key itself is split.
func newShamirTestBarrierDirect(t *testing.T) *Barrier {
	t.Helper()
	store := NewMemoryBackend()
	shamirStore := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(shamirStore, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	swStrat := testSoftwareStrategy(t)
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir: &ShamirConfig{
			Threshold:   2,
			TotalShares: 3,
		},
	}, shamirStrat, swStrat)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	return b
}

// ---------- InitializeShamir tests ----------

func TestShamirInitializeShamir_CredentialMode_Success(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if result.Threshold != 3 {
		t.Errorf("expected Threshold=3, got %d", result.Threshold)
	}
	if result.TotalShares != 5 {
		t.Errorf("expected TotalShares=5, got %d", result.TotalShares)
	}
	if len(result.Shares) != 5 {
		t.Errorf("expected 5 shares, got %d", len(result.Shares))
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed after InitializeShamir")
	}
}

func TestShamirInitializeShamir_DirectMode_Success(t *testing.T) {
	b := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	result, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir (direct): %v", err)
	}

	if result.Threshold != 2 {
		t.Errorf("expected Threshold=2, got %d", result.Threshold)
	}
	if result.TotalShares != 3 {
		t.Errorf("expected TotalShares=3, got %d", result.TotalShares)
	}
	if len(result.Shares) != 3 {
		t.Errorf("expected 3 shares, got %d", len(result.Shares))
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed after InitializeShamir (direct)")
	}
}

func TestShamirInitializeShamir_NotConfigured(t *testing.T) {
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	_, err = b.InitializeShamir(context.Background(), Credentials{Secret: "pw"})
	if err != ErrShamirNotConfigured {
		t.Errorf("expected ErrShamirNotConfigured, got %v", err)
	}
}

func TestShamirInitializeShamir_ThresholdInvalid(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		total     int
	}{
		{"threshold_too_low", 1, 3},
		{"threshold_exceeds_total", 5, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryBackend()
			b, err := NewBarrier(testLogger(), store, BarrierConfig{
				RootKeyPath: "test/root-key",
				Shamir: &ShamirConfig{
					Threshold:   tc.threshold,
					TotalShares: tc.total,
				},
			}, testSoftwareStrategy(t))
			if err != nil {
				t.Fatalf("NewBarrier: %v", err)
			}

			_, err = b.InitializeShamir(context.Background(), Credentials{Secret: "pw"})
			if err != ErrShamirThresholdInvalid {
				t.Errorf("expected ErrShamirThresholdInvalid, got %v", err)
			}
		})
	}
}

func TestShamirInitializeShamir_DirectMode_AlreadyInitialized(t *testing.T) {
	b := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	_, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("first InitializeShamir: %v", err)
	}

	_, err = b.InitializeShamir(ctx, Credentials{})
	if err != ErrAlreadyInitialized {
		t.Errorf("expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestShamirInitializeShamir_DirectMode_BaseExistsError(t *testing.T) {
	fb := newFailingBackend()
	shamirStore := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(shamirStore, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	b, err := NewBarrier(testLogger(), fb, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir: &ShamirConfig{
			Threshold:   2,
			TotalShares: 3,
		},
	}, shamirStrat, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	// Make Exists fail.
	fb.existErr = errors.New("exists failed")

	_, err = b.InitializeShamir(context.Background(), Credentials{})
	if err == nil {
		t.Fatal("expected error from base.Exists failure")
	}
	if !errors.Is(err, ErrCorruptRootKey) {
		t.Errorf("expected ErrCorruptRootKey, got %v", err)
	}
}

// ---------- UnsealWithShare tests ----------

func TestShamirUnsealWithShare_IncrementalUnseal(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for i := 0; i < result.Threshold-1; i++ {
		prog, err := b.UnsealWithShare(ctx, result.Shares[i])
		if err != nil {
			t.Fatalf("UnsealWithShare(%d): %v", i, err)
		}
		if prog.Complete {
			t.Errorf("share %d: expected Complete=false", i)
		}
		if prog.Submitted != i+1 {
			t.Errorf("share %d: expected Submitted=%d, got %d", i, i+1, prog.Submitted)
		}
	}

	prog, err := b.UnsealWithShare(ctx, result.Shares[result.Threshold-1])
	if err != nil {
		t.Fatalf("UnsealWithShare (final): %v", err)
	}
	if !prog.Complete {
		t.Error("expected Complete=true after reaching threshold")
	}
	if b.IsSealed() {
		t.Error("expected barrier to be unsealed after threshold reached")
	}
}

func TestShamirUnsealWithShare_DuplicateShare(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = b.UnsealWithShare(ctx, result.Shares[0])
	if err != nil {
		t.Fatalf("first UnsealWithShare: %v", err)
	}

	_, err = b.UnsealWithShare(ctx, result.Shares[0])
	if err != ErrShamirDuplicateShare {
		t.Errorf("expected ErrShamirDuplicateShare, got %v", err)
	}
}

func TestShamirUnsealWithShare_NotConfigured(t *testing.T) {
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	_, err = b.UnsealWithShare(context.Background(), "some-share")
	if err != ErrShamirNotConfigured {
		t.Errorf("expected ErrShamirNotConfigured, got %v", err)
	}
}

func TestShamirUnsealWithShare_AlreadyUnsealed(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	_, err = b.UnsealWithShare(ctx, result.Shares[0])
	if err != ErrAlreadyUnsealed {
		t.Errorf("expected ErrAlreadyUnsealed, got %v", err)
	}
}

func TestShamirUnsealWithShare_DirectMode(t *testing.T) {
	b := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	result, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = b.UnsealWithShare(ctx, result.Shares[0])
	if err != nil {
		t.Fatalf("UnsealWithShare(0): %v", err)
	}

	prog, err := b.UnsealWithShare(ctx, result.Shares[1])
	if err != nil {
		t.Fatalf("UnsealWithShare(1): %v", err)
	}
	if !prog.Complete {
		t.Error("expected Complete=true")
	}
	if b.IsSealed() {
		t.Error("expected barrier to be unsealed in direct mode")
	}
}

func TestShamirUnsealWithShare_CombineFails(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	_, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Submit 3 garbage shares that will pass dedup but fail Combine.
	for i := 0; i < 2; i++ {
		_, err = b.UnsealWithShare(ctx, "garbage-share-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("UnsealWithShare(%d): %v", i, err)
		}
	}

	// The third share meets the threshold and triggers Combine which should fail.
	_, err = b.UnsealWithShare(ctx, "garbage-share-c")
	if err == nil {
		t.Fatal("expected error from Combine with garbage shares")
	}
	if !errors.Is(err, ErrShamirCombineFailed) {
		t.Errorf("expected ErrShamirCombineFailed, got %v", err)
	}
}

// ---------- UnsealWithShares tests ----------

func TestShamirUnsealWithShares_BatchUnseal(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err = b.UnsealWithShares(ctx, result.Shares[:result.Threshold])
	if err != nil {
		t.Fatalf("UnsealWithShares: %v", err)
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed")
	}
}

func TestShamirUnsealWithShares_QuorumIncomplete(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err = b.UnsealWithShares(ctx, result.Shares[:result.Threshold-1])
	if err != ErrShamirQuorumIncomplete {
		t.Errorf("expected ErrShamirQuorumIncomplete, got %v", err)
	}
}

func TestShamirUnsealWithShares_NotConfigured(t *testing.T) {
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	err = b.UnsealWithShares(context.Background(), []string{"a", "b"})
	if err != ErrShamirNotConfigured {
		t.Errorf("expected ErrShamirNotConfigured, got %v", err)
	}
}

func TestShamirUnsealWithShares_AlreadyUnsealed(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	err = b.UnsealWithShares(ctx, result.Shares[:result.Threshold])
	if err != ErrAlreadyUnsealed {
		t.Errorf("expected ErrAlreadyUnsealed, got %v", err)
	}
}

func TestShamirUnsealWithShares_DirectMode(t *testing.T) {
	b := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	result, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err = b.UnsealWithShares(ctx, result.Shares[:result.Threshold])
	if err != nil {
		t.Fatalf("UnsealWithShares (direct): %v", err)
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed (direct mode)")
	}
}

func TestShamirUnsealWithShares_CombineFails(t *testing.T) {
	b := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	_, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Provide enough garbage shares to meet the threshold.
	err = b.UnsealWithShares(ctx, []string{"bad-1", "bad-2", "bad-3"})
	if err == nil {
		t.Fatal("expected error from Combine with garbage shares")
	}
	if !errors.Is(err, ErrShamirCombineFailed) {
		t.Errorf("expected ErrShamirCombineFailed, got %v", err)
	}
}

// ---------- loadOrCreateAccumulator tests ----------

func TestShamirLoadOrCreateAccumulator_CreatesNew(t *testing.T) {
	b := newShamirTestBarrier(t)

	acc := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)
	if acc == nil {
		t.Fatal("expected non-nil accumulator")
	}

	// Verify the accumulator is functional with the configured threshold.
	// AddShare should succeed and report Required=3.
	ctx := context.Background()
	prog, err := acc.AddShare(ctx, "share-1")
	if err != nil {
		t.Fatalf("AddShare: %v", err)
	}
	if prog.Required != 3 {
		t.Errorf("expected Required=3, got %d", prog.Required)
	}
}

func TestShamirLoadOrCreateAccumulator_ReusesExisting(t *testing.T) {
	b := newShamirTestBarrier(t)

	acc1 := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)
	acc2 := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)

	// Add a share via acc1, then verify acc2 sees it (same instance).
	ctx := context.Background()
	_, err := acc1.AddShare(ctx, "test-share")
	if err != nil {
		t.Fatalf("AddShare via acc1: %v", err)
	}

	// If acc2 is the same instance, adding the same share should be rejected.
	_, err = acc2.AddShare(ctx, "test-share")
	if err != ErrShamirDuplicateShare {
		t.Errorf("expected ErrShamirDuplicateShare (same instance), got %v", err)
	}
}

func TestShamirLoadOrCreateAccumulator_ReplacesExpired(t *testing.T) {
	b := newShamirTestBarrier(t)

	// Create an accumulator with zero TTL (immediately expired).
	acc1 := b.loadOrCreateAccumulator(3, 5, 0)

	// The zero-TTL accumulator should expire immediately or after
	// a brief pause. Force expiration by adding a share to confirm
	// it was created, then verify a new one replaces it.
	ctx := context.Background()
	_, _ = acc1.AddShare(ctx, "share-a")

	// Wait just enough for the zero TTL to kick in.
	time.Sleep(1 * time.Millisecond)

	acc2 := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)

	// acc2 should be a fresh accumulator. Adding "share-a" should succeed
	// (no duplicate error), proving it is a different instance.
	prog, err := acc2.AddShare(ctx, "share-a")
	if err != nil {
		t.Fatalf("AddShare on fresh accumulator: %v", err)
	}
	if prog.Submitted != 1 {
		t.Errorf("expected Submitted=1, got %d", prog.Submitted)
	}
}
