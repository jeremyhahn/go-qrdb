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
	"encoding/json"
	"errors"
	"testing"
)

// newRekeyBarrier creates a barrier with ShamirStrategy + SoftwareStrategy
// and returns the barrier plus its underlying MemoryBackend.
func newRekeyBarrier(t *testing.T) (*Barrier, *MemoryBackend) {
	t.Helper()
	backend := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(3, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBarrier(testLogger(), backend, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 3, TotalShares: 5},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, testSoftwareStrategy(t), shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	return b, backend
}

func TestRekeySuccess(t *testing.T) {
	t.Run("NewSharesCanUnseal", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		initResult, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}
		if len(initResult.Shares) != 5 {
			t.Fatalf("init shares: got %d, want 5", len(initResult.Shares))
		}

		// Rekey with same parameters.
		rekeyResult, err := b.Rekey(ctx, 3, 5)
		if err != nil {
			t.Fatalf("Rekey: unexpected error: %v", err)
		}
		if rekeyResult == nil {
			t.Fatal("Rekey: result is nil")
		}
		if rekeyResult.Threshold != 3 {
			t.Errorf("Threshold: got %d, want 3", rekeyResult.Threshold)
		}
		if rekeyResult.TotalShares != 5 {
			t.Errorf("TotalShares: got %d, want 5", rekeyResult.TotalShares)
		}
		if len(rekeyResult.Shares) != 5 {
			t.Errorf("Shares count: got %d, want 5", len(rekeyResult.Shares))
		}

		// Verify new shares are different from old shares.
		oldSet := make(map[string]struct{})
		for _, s := range initResult.Shares {
			oldSet[s] = struct{}{}
		}
		for _, s := range rekeyResult.Shares {
			if _, found := oldSet[s]; found {
				t.Error("new share is identical to an old share; expected different shares")
			}
		}

		// Seal, then unseal with new shares.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		err = b.UnsealWithShares(ctx, rekeyResult.Shares[:3])
		if err != nil {
			t.Fatalf("UnsealWithShares after rekey: %v", err)
		}
		if b.IsSealed() {
			t.Error("barrier should be unsealed after using new shares")
		}
	})

	t.Run("ChangeThreshold", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		// Rekey from 3-of-5 to 2-of-3.
		rekeyResult, err := b.Rekey(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}
		if rekeyResult.Threshold != 2 {
			t.Errorf("Threshold: got %d, want 2", rekeyResult.Threshold)
		}
		if rekeyResult.TotalShares != 3 {
			t.Errorf("TotalShares: got %d, want 3", rekeyResult.TotalShares)
		}
		if len(rekeyResult.Shares) != 3 {
			t.Errorf("Shares count: got %d, want 3", len(rekeyResult.Shares))
		}

		// Verify config was updated.
		if b.config.Shamir.Threshold != 2 {
			t.Errorf("config Threshold: got %d, want 2", b.config.Shamir.Threshold)
		}
		if b.config.Shamir.TotalShares != 3 {
			t.Errorf("config TotalShares: got %d, want 3", b.config.Shamir.TotalShares)
		}

		// Verify the new threshold works for unseal.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		err = b.UnsealWithShares(ctx, rekeyResult.Shares[:2])
		if err != nil {
			t.Fatalf("UnsealWithShares with new 2-of-3 shares: %v", err)
		}
	})

	t.Run("SealedRootKeyBlobUpdated", func(t *testing.T) {
		b, backend := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		_, err = b.Rekey(ctx, 2, 4)
		if err != nil {
			t.Fatal(err)
		}

		// Verify sealed root key blob has updated Shamir parameters.
		blob, err := backend.Get(ctx, "test/root-key")
		if err != nil {
			t.Fatal(err)
		}
		var sealed SealedRootKey
		if err := json.Unmarshal(blob, &sealed); err != nil {
			t.Fatal(err)
		}
		if sealed.ShamirThreshold != 2 {
			t.Errorf("sealed ShamirThreshold: got %d, want 2", sealed.ShamirThreshold)
		}
		if sealed.ShamirTotal != 4 {
			t.Errorf("sealed ShamirTotal: got %d, want 4", sealed.ShamirTotal)
		}
	})
}

func TestRekeyErrors(t *testing.T) {
	t.Run("ErrSealed", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		// Barrier is sealed (not initialized).
		_, err := b.Rekey(ctx, 3, 5)
		if !errors.Is(err, ErrSealed) {
			t.Errorf("got %v, want ErrSealed", err)
		}
	})

	t.Run("ErrShamirNotConfigured", func(t *testing.T) {
		// Create barrier without Shamir strategy.
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.Rekey(ctx, 3, 5)
		if !errors.Is(err, ErrShamirNotConfigured) {
			t.Errorf("got %v, want ErrShamirNotConfigured", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_ThresholdTooLow", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		_, err = b.Rekey(ctx, 1, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_ThresholdExceedsTotal", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		_, err = b.Rekey(ctx, 6, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_ZeroValues", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		_, err = b.Rekey(ctx, 0, 0)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrCorruptRootKey_CorruptBlob", func(t *testing.T) {
		b, backend := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		// Corrupt the root key blob so Rekey fails during JSON unmarshal
		// of the sealed root key (after the new strategy has been created).
		if err := backend.Put(ctx, "test/root-key", []byte("corrupt-blob")); err != nil {
			t.Fatal(err)
		}

		_, err = b.Rekey(ctx, 2, 3)
		if !errors.Is(err, ErrCorruptRootKey) {
			t.Errorf("got %v, want ErrCorruptRootKey", err)
		}
	})
}

func TestRekeyPreservesData(t *testing.T) {
	t.Run("DataAccessibleAfterRekey", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		// Store data.
		if err := b.Put(ctx, "secret-data", []byte("important")); err != nil {
			t.Fatal(err)
		}

		// Rekey.
		rekeyResult, err := b.Rekey(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		// Data should still be accessible.
		val, err := b.Get(ctx, "secret-data")
		if err != nil {
			t.Fatalf("Get after rekey: %v", err)
		}
		if string(val) != "important" {
			t.Errorf("Get after rekey: got %q, want %q", val, "important")
		}

		// Seal and unseal with new shares.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		err = b.UnsealWithShares(ctx, rekeyResult.Shares[:2])
		if err != nil {
			t.Fatal(err)
		}

		// Data should still be accessible after unseal with new shares.
		val, err = b.Get(ctx, "secret-data")
		if err != nil {
			t.Fatalf("Get after unseal with new shares: %v", err)
		}
		if string(val) != "important" {
			t.Errorf("Get after unseal with new shares: got %q, want %q", val, "important")
		}
	})
}

func TestRekeyStrategyReplaced(t *testing.T) {
	t.Run("BarrierUsesNewStrategy", func(t *testing.T) {
		b, _ := newRekeyBarrier(t)
		ctx := context.Background()

		_, err := b.InitializeShamir(ctx, Credentials{})
		if err != nil {
			t.Fatal(err)
		}

		// Verify initial ShamirStrategy threshold.
		initial := b.ShamirStrategy()
		if initial == nil {
			t.Fatal("expected ShamirStrategy to be registered")
		}
		if initial.Threshold() != 3 {
			t.Errorf("initial threshold: got %d, want 3", initial.Threshold())
		}

		// Rekey to 2-of-4.
		_, err = b.Rekey(ctx, 2, 4)
		if err != nil {
			t.Fatal(err)
		}

		// Verify strategy was replaced.
		updated := b.ShamirStrategy()
		if updated == nil {
			t.Fatal("expected ShamirStrategy after rekey")
		}
		if updated.Threshold() != 2 {
			t.Errorf("updated threshold: got %d, want 2", updated.Threshold())
		}
		if updated.TotalShares() != 4 {
			t.Errorf("updated total: got %d, want 4", updated.TotalShares())
		}
	})
}
