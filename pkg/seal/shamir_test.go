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
	"encoding/base64"
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
	if err := b.Initialize(ctx, MustCredentials([]byte("test-password"))); err != nil {
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
// credential-mode Shamir tests. A captureDistributor is wired so that
// InitializeShamir succeeds; retrieve shares via the returned distributor.
func newShamirTestBarrier(t *testing.T) (*Barrier, *captureDistributor) {
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
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)
	return b, dist
}

// newShamirTestBarrierDirect creates a barrier with both ShamirStrategy and
// SoftwareStrategy for direct-mode tests where the root key itself is split.
// A captureDistributor is wired; retrieve shares via the returned distributor.
func newShamirTestBarrierDirect(t *testing.T) (*Barrier, *captureDistributor) {
	t.Helper()
	store := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3)
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
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat, swStrat)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)
	return b, dist
}

// ---------- InitializeShamir tests ----------

func TestShamirInitializeShamir_CredentialMode_Success(t *testing.T) {
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

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
	if result.ShareCount != 5 {
		t.Errorf("expected ShareCount=5, got %d", result.ShareCount)
	}
	if len(dist.Shares()) != 5 {
		t.Errorf("expected 5 captured shares, got %d", len(dist.Shares()))
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed after InitializeShamir")
	}
}

func TestShamirInitializeShamir_DirectMode_Success(t *testing.T) {
	b, dist := newShamirTestBarrierDirect(t)
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
	if result.ShareCount != 3 {
		t.Errorf("expected ShareCount=3, got %d", result.ShareCount)
	}
	if len(dist.Shares()) != 3 {
		t.Errorf("expected 3 captured shares, got %d", len(dist.Shares()))
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed after InitializeShamir (direct)")
	}
}

func TestShamirInitializeShamir_NoCeremonyDistributor(t *testing.T) {
	store := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir: &ShamirConfig{
			Threshold:   2,
			TotalShares: 3,
		},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	// No SetCeremonyDistributor call.
	_, gotErr := b.InitializeShamir(context.Background(), Credentials{})
	if !errors.Is(gotErr, ErrCeremonyDistributorNotConfigured) {
		t.Errorf("got %v, want ErrCeremonyDistributorNotConfigured", gotErr)
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

	_, err = b.InitializeShamir(context.Background(), MustCredentials([]byte("pw")))
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
			dist := newCaptureDistributor()
			b.SetCeremonyDistributor(dist)

			_, err = b.InitializeShamir(context.Background(), MustCredentials([]byte("pw")))
			if err != ErrShamirThresholdInvalid {
				t.Errorf("expected ErrShamirThresholdInvalid, got %v", err)
			}
		})
	}
}

func TestShamirInitializeShamir_DirectMode_AlreadyInitialized(t *testing.T) {
	b, _ := newShamirTestBarrierDirect(t)
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
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	b, err := NewBarrier(testLogger(), fb, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir: &ShamirConfig{
			Threshold:   2,
			TotalShares: 3,
		},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)

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
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for i := 0; i < result.Threshold-1; i++ {
		prog, err := b.UnsealWithShare(ctx, shares[i])
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

	prog, err := b.UnsealWithShare(ctx, shares[result.Threshold-1])
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
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	if _, err := b.InitializeShamir(ctx, creds); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err := b.UnsealWithShare(ctx, shares[0])
	if err != nil {
		t.Fatalf("first UnsealWithShare: %v", err)
	}

	_, err = b.UnsealWithShare(ctx, shares[0])
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
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	if _, err := b.InitializeShamir(ctx, creds); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	_, err := b.UnsealWithShare(ctx, shares[0])
	if err != ErrAlreadyUnsealed {
		t.Errorf("expected ErrAlreadyUnsealed, got %v", err)
	}
}

func TestShamirUnsealWithShare_DirectMode(t *testing.T) {
	b, dist := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err := b.UnsealWithShare(ctx, shares[0])
	if err != nil {
		t.Fatalf("UnsealWithShare(0): %v", err)
	}

	prog, err := b.UnsealWithShare(ctx, shares[1])
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
	b, _ := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	if _, err := b.InitializeShamir(ctx, creds); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Submit valid-base64 shares that pass NewProtectedShare's format check and
	// dedup but are not real Shamir shares, so Combine fails once the threshold
	// (3) is met. Malformed, non-base64 input is now rejected earlier — at
	// submission — because NewProtectedShare bounds and validates the value
	// before it reaches locked memory.
	garbage := []string{
		base64.StdEncoding.EncodeToString([]byte("garbage-share-payload-0")),
		base64.StdEncoding.EncodeToString([]byte("garbage-share-payload-1")),
		base64.StdEncoding.EncodeToString([]byte("garbage-share-payload-2")),
	}
	for i := 0; i < 2; i++ {
		_, err := b.UnsealWithShare(ctx, garbage[i])
		if err != nil {
			t.Fatalf("UnsealWithShare(%d): %v", i, err)
		}
	}

	// The third share meets the threshold and triggers Combine, which fails.
	_, err := b.UnsealWithShare(ctx, garbage[2])
	if err == nil {
		t.Fatal("expected error from Combine with garbage shares")
	}
	if !errors.Is(err, ErrShamirCombineFailed) {
		t.Errorf("expected ErrShamirCombineFailed, got %v", err)
	}
}

// TestShamirUnsealWithShare_MalformedShareRejected proves a non-base64 share is
// rejected at SUBMISSION by NewProtectedShare's validation — before it reaches
// the accumulator or locked memory — rather than being accepted and failing
// later at Combine.
func TestShamirUnsealWithShare_MalformedShareRejected(t *testing.T) {
	b, _ := newShamirTestBarrier(t)
	ctx := context.Background()
	if _, err := b.InitializeShamir(ctx, MustCredentials([]byte("test-password"))); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := b.UnsealWithShare(ctx, "not valid base64!!"); err == nil {
		t.Fatal("expected a malformed share to be rejected at submission")
	} else if !errors.Is(err, ErrShamirCombineFailed) {
		t.Errorf("expected ErrShamirCombineFailed, got %v", err)
	}
}

// ---------- UnsealWithShares tests ----------

func TestShamirUnsealWithShares_BatchUnseal(t *testing.T) {
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err = b.UnsealWithShares(ctx, shares[:result.Threshold])
	if err != nil {
		t.Fatalf("UnsealWithShares: %v", err)
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed")
	}
}

func TestShamirUnsealWithShares_QuorumIncomplete(t *testing.T) {
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err = b.UnsealWithShares(ctx, shares[:result.Threshold-1])
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
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	err = b.UnsealWithShares(ctx, shares[:result.Threshold])
	if err != ErrAlreadyUnsealed {
		t.Errorf("expected ErrAlreadyUnsealed, got %v", err)
	}
}

func TestShamirUnsealWithShares_DirectMode(t *testing.T) {
	b, dist := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	result, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err = b.UnsealWithShares(ctx, shares[:result.Threshold])
	if err != nil {
		t.Fatalf("UnsealWithShares (direct): %v", err)
	}

	if b.IsSealed() {
		t.Error("expected barrier to be unsealed (direct mode)")
	}
}

func TestShamirUnsealWithShares_CombineFails(t *testing.T) {
	b, _ := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	if _, err := b.InitializeShamir(ctx, creds); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Provide enough garbage shares to meet the threshold.
	err := b.UnsealWithShares(ctx, []string{"bad-1", "bad-2", "bad-3"})
	if err == nil {
		t.Fatal("expected error from Combine with garbage shares")
	}
	if !errors.Is(err, ErrShamirCombineFailed) {
		t.Errorf("expected ErrShamirCombineFailed, got %v", err)
	}
}

// ---------- unsealWithKey tests ----------
//
// unsealWithKey is the single implementation UnsealWithShare and
// UnsealWithShares both call after they finish their own in-process Shamir
// reconstruction (see combineShareValues in test_distributor_test.go, which
// reconstructs the same secret those two methods reconstruct internally, so
// these tests exercise the resolution step in isolation from the
// accumulator).

// TestShamirUnsealWithKeyInternal_CredentialMode_Success verifies that, with no
// ShamirStrategy registered, unsealWithKey treats key as a credential-mode
// passphrase and unseals via the underlying SoftwareStrategy.
func TestShamirUnsealWithKeyInternal_CredentialMode_Success(t *testing.T) {
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	secret := combineShareValues(t, shares[:result.Threshold], result.Threshold, result.TotalShares)

	if err := b.unsealWithKey(ctx, secret); err != nil {
		t.Fatalf("unsealWithKey: %v", err)
	}
	if b.IsSealed() {
		t.Error("expected barrier to be unsealed")
	}
}

// TestShamirUnsealWithKeyInternal_DirectMode_Success verifies that, with a
// ShamirStrategy registered, unsealWithKey resolves key as the unseal key (or
// legacy root key) via unsealDirect rather than treating it as a passphrase.
func TestShamirUnsealWithKeyInternal_DirectMode_Success(t *testing.T) {
	b, dist := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	result, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	secret := combineShareValues(t, shares[:result.Threshold], result.Threshold, result.TotalShares)

	if err := b.unsealWithKey(ctx, secret); err != nil {
		t.Fatalf("unsealWithKey (direct): %v", err)
	}
	if b.IsSealed() {
		t.Error("expected barrier to be unsealed (direct mode)")
	}
}

// TestShamirUnsealWithKeyInternal_AlreadyUnsealed verifies unsealWithKey refuses a
// second unseal rather than silently re-running the strategy dispatch.
func TestShamirUnsealWithKeyInternal_AlreadyUnsealed(t *testing.T) {
	b, dist := newShamirTestBarrier(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))

	result, err := b.InitializeShamir(ctx, creds)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()

	// InitializeShamir leaves the barrier unsealed in credential mode.
	secret := combineShareValues(t, shares[:result.Threshold], result.Threshold, result.TotalShares)

	err = b.unsealWithKey(ctx, secret)
	if err != ErrAlreadyUnsealed {
		t.Errorf("expected ErrAlreadyUnsealed, got %v", err)
	}
}

// TestShamirUnsealWithKeyInternal_WrongKeyFails verifies that an incorrect key is
// rejected by the underlying strategy rather than silently unsealing.
//
// This MUST use NewDeferredSoftwareStrategy, not the bound
// NewSoftwareStrategy newShamirTestBarrier's fixture uses: a bound strategy
// derives its key from the passphrase given at CONSTRUCTION and ignores the
// credentials handed to Unseal, so it returns success for ANY key including
// the wrong one — see the doc warning on NewSoftwareStrategy and
// TestSoftwareStrategyBindingModes_CredentialSemantics, which pins exactly
// this distinction after go-qrdb#162.
func TestShamirUnsealWithKeyInternal_WrongKeyFails(t *testing.T) {
	store := NewMemoryBackend()
	strategy := NewDeferredSoftwareStrategy()
	defer func() { _ = strategy.Close() }()
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
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)

	ctx := context.Background()
	creds := MustCredentials([]byte("test-password"))
	if _, err := b.InitializeShamir(ctx, creds); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if err := b.unsealWithKey(ctx, []byte("definitely-the-wrong-password")); err == nil {
		t.Fatal("expected error unsealing with the wrong key")
	}
	if !b.IsSealed() {
		t.Error("expected barrier to remain sealed after a failed unseal")
	}
}

// ---------- InitializeShamir request-params-win tests ----------

// TestShamirInitializeShamir_DirectMode_RequestParamsOverride verifies that
// when SetShamirConfig is called with threshold/total different from the
// boot-time ShamirStrategy, initializeShamirDirect uses the config values
// and persists them to the sealed envelope.
func TestShamirInitializeShamir_DirectMode_RequestParamsOverride(t *testing.T) {
	tests := []struct {
		name             string
		bootThreshold    int
		bootTotal        int
		requestThreshold int
		requestTotal     int
	}{
		{"3-of-5 overrides 2-of-3", 2, 3, 3, 5},
		{"2-of-4 overrides 3-of-6", 3, 6, 2, 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryBackend()
			shamirStrat, err := NewShamirStrategy(tc.bootThreshold, tc.bootTotal)
			if err != nil {
				t.Fatalf("NewShamirStrategy: %v", err)
			}
			b, err := NewBarrier(testLogger(), store, BarrierConfig{
				RootKeyPath: "test/root-key",
				Shamir: &ShamirConfig{
					Threshold:   tc.bootThreshold,
					TotalShares: tc.bootTotal,
				},
				PreferenceOrder: []StrategyID{StrategyShamir},
			}, shamirStrat, testSoftwareStrategy(t))
			if err != nil {
				t.Fatalf("NewBarrier: %v", err)
			}
			dist := newCaptureDistributor()
			b.SetCeremonyDistributor(dist)

			// Simulate "request wins" — service layer calls SetShamirConfig.
			b.SetShamirConfig(&ShamirConfig{
				Threshold:   tc.requestThreshold,
				TotalShares: tc.requestTotal,
			})

			result, err := b.InitializeShamir(context.Background(), Credentials{})
			if err != nil {
				t.Fatalf("InitializeShamir: %v", err)
			}

			if result.Threshold != tc.requestThreshold {
				t.Errorf("result.Threshold = %d, want %d", result.Threshold, tc.requestThreshold)
			}
			if result.TotalShares != tc.requestTotal {
				t.Errorf("result.TotalShares = %d, want %d", result.TotalShares, tc.requestTotal)
			}
			if result.ShareCount != tc.requestTotal {
				t.Errorf("result.ShareCount = %d, want %d", result.ShareCount, tc.requestTotal)
			}
			if len(dist.Shares()) != tc.requestTotal {
				t.Errorf("len(dist.Shares()) = %d, want %d", len(dist.Shares()), tc.requestTotal)
			}

			// In-memory strategy must reflect the new params.
			shamirStrategy := b.ShamirStrategy()
			if shamirStrategy == nil {
				t.Fatal("ShamirStrategy() returned nil after override")
			}
			if shamirStrategy.Threshold() != tc.requestThreshold {
				t.Errorf("ShamirStrategy.Threshold() = %d, want %d", shamirStrategy.Threshold(), tc.requestThreshold)
			}
			if shamirStrategy.TotalShares() != tc.requestTotal {
				t.Errorf("ShamirStrategy.TotalShares() = %d, want %d", shamirStrategy.TotalShares(), tc.requestTotal)
			}
		})
	}
}

// TestShamirInitializeShamir_DirectMode_FallbackToBootConfig verifies that
// when config threshold/total match the boot ShamirStrategy, the sealed
// envelope is not unnecessarily rewritten but the result is still correct.
func TestShamirInitializeShamir_DirectMode_FallbackToBootConfig(t *testing.T) {
	b, dist := newShamirTestBarrierDirect(t)
	// Config already matches the boot strategy (2-of-3). No override needed.
	result, err := b.InitializeShamir(context.Background(), Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	if result.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", result.Threshold)
	}
	if result.TotalShares != 3 {
		t.Errorf("TotalShares = %d, want 3", result.TotalShares)
	}
	if result.ShareCount != 3 {
		t.Errorf("ShareCount = %d, want 3", result.ShareCount)
	}
	if len(dist.Shares()) != 3 {
		t.Errorf("len(dist.Shares()) = %d, want 3", len(dist.Shares()))
	}
}

// ---------- Status Shamir fields tests ----------

// TestBarrierStatus_ShamirFields_ShamirStrategy verifies that Status() exposes
// ShamirThreshold/ShamirTotal from the registered ShamirStrategy.
func TestBarrierStatus_ShamirFields_ShamirStrategy(t *testing.T) {
	b, _ := newShamirTestBarrierDirect(t)
	ctx := context.Background()

	_, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	status := b.Status()
	if status.ShamirThreshold != 2 {
		t.Errorf("ShamirThreshold = %d, want 2", status.ShamirThreshold)
	}
	if status.ShamirTotal != 3 {
		t.Errorf("ShamirTotal = %d, want 3", status.ShamirTotal)
	}
}

// TestBarrierStatus_ShamirFields_AfterOverride verifies that after a request
// overrides threshold/total, Status() reflects the new values.
func TestBarrierStatus_ShamirFields_AfterOverride(t *testing.T) {
	store := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir: &ShamirConfig{
			Threshold:   2,
			TotalShares: 3,
		},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)

	// Override to 3-of-5 via SetShamirConfig.
	b.SetShamirConfig(&ShamirConfig{Threshold: 3, TotalShares: 5})

	_, err = b.InitializeShamir(context.Background(), Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	status := b.Status()
	if status.ShamirThreshold != 3 {
		t.Errorf("ShamirThreshold = %d, want 3 after override", status.ShamirThreshold)
	}
	if status.ShamirTotal != 5 {
		t.Errorf("ShamirTotal = %d, want 5 after override", status.ShamirTotal)
	}
}

// TestBarrierStatus_ShamirFields_NonShamirBarrier verifies that for a
// non-Shamir barrier, ShamirThreshold and ShamirTotal are zero.
func TestBarrierStatus_ShamirFields_NonShamirBarrier(t *testing.T) {
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	if err := b.Initialize(context.Background(), MustCredentials([]byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status := b.Status()
	if status.ShamirThreshold != 0 {
		t.Errorf("ShamirThreshold = %d, want 0 for non-shamir barrier", status.ShamirThreshold)
	}
	if status.ShamirTotal != 0 {
		t.Errorf("ShamirTotal = %d, want 0 for non-shamir barrier", status.ShamirTotal)
	}
}

// ---------- loadOrCreateAccumulator tests ----------

func TestShamirLoadOrCreateAccumulator_CreatesNew(t *testing.T) {
	b, _ := newShamirTestBarrier(t)

	acc := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)
	if acc == nil {
		t.Fatal("expected non-nil accumulator")
	}

	// Verify the accumulator is functional with the configured threshold.
	// AddProtectedShare should succeed and report Required=3.
	ctx := context.Background()
	prog, err := acc.AddProtectedShare(ctx, mkShare(t, 1, 3, 5, "share-1"))
	if err != nil {
		t.Fatalf("AddProtectedShare: %v", err)
	}
	if prog.Required != 3 {
		t.Errorf("expected Required=3, got %d", prog.Required)
	}
}

func TestShamirLoadOrCreateAccumulator_ReusesExisting(t *testing.T) {
	b, _ := newShamirTestBarrier(t)

	acc1 := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)
	acc2 := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)

	// Add a share via acc1, then verify acc2 sees it (same instance).
	ctx := context.Background()
	_, err := acc1.AddProtectedShare(ctx, mkShare(t, 1, 3, 5, "test-share"))
	if err != nil {
		t.Fatalf("AddProtectedShare via acc1: %v", err)
	}

	// If acc2 is the same instance, adding a share with the same value should
	// be rejected as a duplicate.
	_, err = acc2.AddProtectedShare(ctx, mkShare(t, 2, 3, 5, "test-share"))
	if err != ErrShamirDuplicateShare {
		t.Errorf("expected ErrShamirDuplicateShare (same instance), got %v", err)
	}
}

func TestShamirLoadOrCreateAccumulator_ReplacesExpired(t *testing.T) {
	b, _ := newShamirTestBarrier(t)

	// Create an accumulator with zero TTL (immediately expired).
	acc1 := b.loadOrCreateAccumulator(3, 5, 0)

	// The zero-TTL accumulator should expire immediately or after
	// a brief pause. Force expiration by adding a share to confirm
	// it was created, then verify a new one replaces it.
	ctx := context.Background()
	_, _ = acc1.AddProtectedShare(ctx, mkShare(t, 1, 3, 5, "share-a"))

	// Wait just enough for the zero TTL to kick in.
	time.Sleep(1 * time.Millisecond)

	acc2 := b.loadOrCreateAccumulator(3, 5, 5*time.Minute)

	// acc2 should be a fresh accumulator. Adding a share with value "share-a"
	// should succeed (no duplicate error), proving it is a different instance.
	prog, err := acc2.AddProtectedShare(ctx, mkShare(t, 1, 3, 5, "share-a"))
	if err != nil {
		t.Fatalf("AddProtectedShare on fresh accumulator: %v", err)
	}
	if prog.Submitted != 1 {
		t.Errorf("expected Submitted=1, got %d", prog.Submitted)
	}
}

// --------------------------------------------------------------------------
// shamir.go: updateSealedShamirParams — 76.2%
// (exercises the params-differ path in initializeShamirDirect)
// --------------------------------------------------------------------------

func TestUpdateSealedShamirParams_ParamsDiffer(t *testing.T) {
	// Build a Shamir barrier where the request threshold/total differ from
	// the boot-time ShamirStrategy (2-of-3 vs 3-of-5). This triggers the
	// updateSealedShamirParams branch in initializeShamirDirect.
	backend := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3) // boot-time: 2-of-3
	if err != nil {
		t.Fatal(err)
	}

	// Configure the barrier to request 3-of-5 at init time.
	b, err := NewBarrier(testLogger(), backend, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 3, TotalShares: 5}, // request: 3-of-5
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, testSoftwareStrategy(t), shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)
	ctx := context.Background()

	params, err := b.InitializeShamir(ctx, Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir with differing params: %v", err)
	}
	if params.Threshold != 3 || params.TotalShares != 5 {
		t.Errorf("expected 3-of-5 params, got %d-of-%d", params.Threshold, params.TotalShares)
	}

	// Verify the in-memory strategy was updated.
	shamirStrategy := b.ShamirStrategy()
	if shamirStrategy == nil {
		t.Fatal("expected non-nil ShamirStrategy after update")
	}
	if shamirStrategy.Threshold() != 3 || shamirStrategy.TotalShares() != 5 {
		t.Errorf("in-memory strategy: expected 3-of-5, got %d-of-%d", shamirStrategy.Threshold(), shamirStrategy.TotalShares())
	}
}
