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
	"log/slog"
	"testing"
)

func newRecoveryBarrier(t *testing.T) (*Barrier, *MemoryBackend) {
	t.Helper()
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	return b, backend
}

func newRecoveryBarrierWithShamir(t *testing.T) (*Barrier, *MemoryBackend) {
	t.Helper()
	backend := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(3, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 3, TotalShares: 5},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, testSoftwareStrategy(t), shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	return b, backend
}

func initRecoveryBarrier(t *testing.T, b *Barrier) {
	t.Helper()
	ctx := context.Background()
	if err := b.Initialize(ctx, Credentials{Secret: "test-password"}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryGenerateRecoveryKeys(t *testing.T) {
	t.Run("Success_3of5", func(t *testing.T) {
		b, backend := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 3, 5)
		if err != nil {
			t.Fatalf("GenerateRecoveryKeys: unexpected error: %v", err)
		}
		if result == nil {
			t.Fatal("GenerateRecoveryKeys: result is nil")
		}
		if result.Threshold != 3 {
			t.Errorf("Threshold: got %d, want 3", result.Threshold)
		}
		if result.TotalShares != 5 {
			t.Errorf("TotalShares: got %d, want 5", result.TotalShares)
		}
		if len(result.Shares) != 5 {
			t.Errorf("Shares count: got %d, want 5", len(result.Shares))
		}
		for i, share := range result.Shares {
			if share == "" {
				t.Errorf("Share[%d]: empty string", i)
			}
		}

		// Verify metadata is persisted in storage.
		metaBytes, err := backend.Get(ctx, recoveryMetaKey)
		if err != nil {
			t.Fatalf("Get recovery-meta: unexpected error: %v", err)
		}
		var meta recoveryMeta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			t.Fatalf("Unmarshal recovery-meta: %v", err)
		}
		if meta.Threshold != 3 {
			t.Errorf("meta.Threshold: got %d, want 3", meta.Threshold)
		}
		if meta.Total != 5 {
			t.Errorf("meta.Total: got %d, want 5", meta.Total)
		}
	})

	t.Run("Success_2of3", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatalf("GenerateRecoveryKeys: unexpected error: %v", err)
		}
		if result.Threshold != 2 || result.TotalShares != 3 {
			t.Errorf("got threshold=%d total=%d, want 2/3", result.Threshold, result.TotalShares)
		}
		if len(result.Shares) != 3 {
			t.Errorf("Shares count: got %d, want 3", len(result.Shares))
		}
	})

	t.Run("ErrShamirThresholdInvalid_ThresholdLessThan2", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 1, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_ThresholdGreaterThanTotal", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 6, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_ZeroThreshold", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 0, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_NegativeThreshold", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, -1, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrSealed_BarrierNotInitialized", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		ctx := context.Background()

		// Barrier is sealed (not initialized), so GetMasterKey will fail.
		_, err := b.GenerateRecoveryKeys(ctx, 3, 5)
		if !errors.Is(err, ErrSealed) {
			t.Errorf("got %v, want ErrSealed", err)
		}
	})

	t.Run("MultipleGenerateOverwritesMetadata", func(t *testing.T) {
		b, backend := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		// First generation: 2 of 3.
		_, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		// Second generation: 3 of 5 overwrites.
		_, err = b.GenerateRecoveryKeys(ctx, 3, 5)
		if err != nil {
			t.Fatal(err)
		}

		metaBytes, err := backend.Get(ctx, recoveryMetaKey)
		if err != nil {
			t.Fatal(err)
		}
		var meta recoveryMeta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Threshold != 3 || meta.Total != 5 {
			t.Errorf("metadata not overwritten: got threshold=%d total=%d, want 3/5",
				meta.Threshold, meta.Total)
		}
	})

	t.Run("SharesAreUnique", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 3, 5)
		if err != nil {
			t.Fatal(err)
		}

		seen := make(map[string]struct{}, len(result.Shares))
		for i, share := range result.Shares {
			if _, exists := seen[share]; exists {
				t.Errorf("Share[%d] is a duplicate", i)
			}
			seen[share] = struct{}{}
		}
	})
}

func TestRecoveryRecoverWithKeys(t *testing.T) {
	t.Run("Success_ExactThreshold", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 3, 5)
		if err != nil {
			t.Fatal(err)
		}

		// Seal the barrier.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}
		if !b.IsSealed() {
			t.Fatal("barrier should be sealed")
		}

		// Recover with exactly threshold shares.
		err = b.RecoverWithKeys(ctx, result.Shares[:3])
		if err != nil {
			t.Fatalf("RecoverWithKeys: unexpected error: %v", err)
		}
		if b.IsSealed() {
			t.Error("barrier should be unsealed after recovery")
		}
	})

	t.Run("Success_AllShares", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 3, 5)
		if err != nil {
			t.Fatal(err)
		}

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Recover with all shares (more than threshold).
		err = b.RecoverWithKeys(ctx, result.Shares)
		if err != nil {
			t.Fatalf("RecoverWithKeys with all shares: unexpected error: %v", err)
		}
		if b.IsSealed() {
			t.Error("barrier should be unsealed after recovery")
		}
	})

	t.Run("BarrierTransitionsFromSealedToUnsealed", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}
		if !b.IsSealed() {
			t.Fatal("expected sealed")
		}

		err = b.RecoverWithKeys(ctx, result.Shares[:2])
		if err != nil {
			t.Fatal(err)
		}
		if b.IsSealed() {
			t.Error("expected unsealed after recovery")
		}

		// Verify the active strategy is set to "recovery".
		activeStrat := b.ActiveStrategy()
		if activeStrat != StrategyID("recovery") {
			t.Errorf("active strategy: got %q, want %q", activeStrat, "recovery")
		}
	})

	t.Run("ErrAlreadyUnsealed", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		// Barrier is already unsealed.
		err = b.RecoverWithKeys(ctx, result.Shares[:2])
		if !errors.Is(err, ErrAlreadyUnsealed) {
			t.Errorf("got %v, want ErrAlreadyUnsealed", err)
		}
	})

	t.Run("ErrRecoveryKeysNotFound", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		// Seal without generating recovery keys.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		err := b.RecoverWithKeys(ctx, []string{"share1", "share2"})
		if !errors.Is(err, ErrRecoveryKeysNotFound) {
			t.Errorf("got %v, want ErrRecoveryKeysNotFound", err)
		}
	})

	t.Run("ErrShamirQuorumIncomplete", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 3, 5)
		if err != nil {
			t.Fatal(err)
		}

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Provide only 2 shares when 3 are needed.
		err = b.RecoverWithKeys(ctx, result.Shares[:2])
		if !errors.Is(err, ErrShamirQuorumIncomplete) {
			t.Errorf("got %v, want ErrShamirQuorumIncomplete", err)
		}
	})

	t.Run("ErrShamirCombineFailed_InvalidShares", func(t *testing.T) {
		b, backend := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		// Generate real recovery keys to persist metadata.
		_, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Manually manipulate metadata to allow passing the threshold check
		// but provide garbage shares that will fail Shamir combine.
		meta := &recoveryMeta{Threshold: 2, Total: 3}
		metaBytes, _ := json.Marshal(meta)
		if err := backend.Put(ctx, recoveryMetaKey, metaBytes); err != nil {
			t.Fatal(err)
		}

		err = b.RecoverWithKeys(ctx, []string{"invalid-share-1", "invalid-share-2"})
		if !errors.Is(err, ErrShamirCombineFailed) {
			t.Errorf("got %v, want ErrShamirCombineFailed", err)
		}
	})

	t.Run("ErrRecoveryKeysNotFound_CorruptMetadata", func(t *testing.T) {
		b, backend := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		// Store corrupt metadata.
		if err := backend.Put(ctx, recoveryMetaKey, []byte("not-json")); err != nil {
			t.Fatal(err)
		}

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		err := b.RecoverWithKeys(ctx, []string{"share1", "share2", "share3"})
		if !errors.Is(err, ErrRecoveryKeysNotFound) {
			t.Errorf("got %v, want ErrRecoveryKeysNotFound", err)
		}
	})
}

func TestRecoveryRoundTrip(t *testing.T) {
	t.Run("Initialize_Generate_Seal_Recover_GetPut", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		ctx := context.Background()

		// Initialize the barrier.
		if err := b.Initialize(ctx, Credentials{Secret: "test-password"}); err != nil {
			t.Fatal(err)
		}

		// Store some data while unsealed.
		if err := b.Put(ctx, "mykey", []byte("myvalue")); err != nil {
			t.Fatal(err)
		}

		// Generate recovery keys.
		result, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		// Seal the barrier.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Verify Get fails while sealed.
		_, err = b.Get(ctx, "mykey")
		if !errors.Is(err, ErrSealed) {
			t.Errorf("Get while sealed: got %v, want ErrSealed", err)
		}

		// Recover with shares.
		if err := b.RecoverWithKeys(ctx, result.Shares[:2]); err != nil {
			t.Fatal(err)
		}

		// Verify data is accessible after recovery.
		val, err := b.Get(ctx, "mykey")
		if err != nil {
			t.Fatalf("Get after recovery: %v", err)
		}
		if string(val) != "myvalue" {
			t.Errorf("Get after recovery: got %q, want %q", val, "myvalue")
		}

		// Verify Put still works after recovery.
		if err := b.Put(ctx, "another-key", []byte("another-value")); err != nil {
			t.Fatalf("Put after recovery: %v", err)
		}
		val, err = b.Get(ctx, "another-key")
		if err != nil {
			t.Fatalf("Get another-key: %v", err)
		}
		if string(val) != "another-value" {
			t.Errorf("Get another-key: got %q, want %q", val, "another-value")
		}
	})
}

func TestRecoveryDeleteRecoveryKeys(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		// Verify keys exist.
		has, err := b.HasRecoveryKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !has {
			t.Fatal("expected recovery keys to exist")
		}

		// Delete them.
		if err := b.DeleteRecoveryKeys(ctx); err != nil {
			t.Fatal(err)
		}

		// Verify they are gone.
		has, err = b.HasRecoveryKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Error("expected recovery keys to be deleted")
		}
	})

	t.Run("ErrSealed", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		ctx := context.Background()
		// Barrier is sealed by default (not initialized).

		err := b.DeleteRecoveryKeys(ctx)
		if !errors.Is(err, ErrSealed) {
			t.Errorf("got %v, want ErrSealed", err)
		}
	})

	t.Run("DeleteNonExistent", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		// Deleting non-existent recovery keys should not error
		// (MemoryBackend.Delete is a no-op for missing keys).
		err := b.DeleteRecoveryKeys(ctx)
		if err != nil {
			t.Errorf("DeleteRecoveryKeys for non-existent: got %v, want nil", err)
		}
	})
}

func TestRecoveryHasRecoveryKeys(t *testing.T) {
	t.Run("ReturnsFalseWhenNoneExist", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		has, err := b.HasRecoveryKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Error("expected false, got true")
		}
	})

	t.Run("ReturnsTrueWhenExist", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		has, err := b.HasRecoveryKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !has {
			t.Error("expected true, got false")
		}
	})

	t.Run("ErrSealed", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		ctx := context.Background()

		has, err := b.HasRecoveryKeys(ctx)
		if !errors.Is(err, ErrSealed) {
			t.Errorf("got %v, want ErrSealed", err)
		}
		if has {
			t.Error("expected false when sealed")
		}
	})

	t.Run("ReturnsFalseAfterDelete", func(t *testing.T) {
		b, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatal(err)
		}

		if err := b.DeleteRecoveryKeys(ctx); err != nil {
			t.Fatal(err)
		}

		has, err := b.HasRecoveryKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Error("expected false after deletion")
		}
	})
}

func TestRecoveryItoa(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want string
	}{
		{"Zero", 0, "0"},
		{"Positive", 42, "42"},
		{"Negative", -7, "-7"},
		{"Large", 123456789, "123456789"},
		{"One", 1, "1"},
		{"Ten", 10, "10"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := itoa(tc.in)
			if got != tc.want {
				t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRecoveryUitoa(t *testing.T) {
	tests := []struct {
		name string
		in   uint
		want string
	}{
		{"Zero", 0, "0"},
		{"One", 1, "1"},
		{"Large", 999999999, "999999999"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := uitoa(tc.in)
			if got != tc.want {
				t.Errorf("uitoa(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
