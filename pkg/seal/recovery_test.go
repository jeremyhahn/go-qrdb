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

// newRecoveryBarrier creates a software-strategy barrier with a
// captureDistributor wired as both the ceremony and recovery distributor.
func newRecoveryBarrier(t *testing.T) (*Barrier, *MemoryBackend, *captureDistributor) {
	t.Helper()
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor()
	b.SetRecoveryDistributor(dist)
	return b, backend, dist
}

// newRecoveryBarrierWithShamir creates a Shamir-strategy barrier with
// captureDistributors for both ceremony and recovery paths.
func newRecoveryBarrierWithShamir(t *testing.T) (*Barrier, *MemoryBackend, *captureDistributor) {
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
	dist := newCaptureDistributor()
	b.SetRecoveryDistributor(dist)
	b.SetCeremonyDistributor(dist)
	return b, backend, dist
}

func initRecoveryBarrier(t *testing.T, b *Barrier) {
	t.Helper()
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials([]byte("test-password"))); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryGenerateRecoveryKeys(t *testing.T) {
	t.Run("Success_3of5", func(t *testing.T) {
		b, backend, dist := newRecoveryBarrier(t)
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
		if result.ShareCount != 5 {
			t.Errorf("ShareCount: got %d, want 5", result.ShareCount)
		}
		shares := dist.Shares()
		if len(shares) != 5 {
			t.Errorf("captured shares: got %d, want 5", len(shares))
		}
		for i, share := range shares {
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
		b, _, dist := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		result, err := b.GenerateRecoveryKeys(ctx, 2, 3)
		if err != nil {
			t.Fatalf("GenerateRecoveryKeys: unexpected error: %v", err)
		}
		if result.Threshold != 2 || result.TotalShares != 3 {
			t.Errorf("got threshold=%d total=%d, want 2/3", result.Threshold, result.TotalShares)
		}
		if result.ShareCount != 3 {
			t.Errorf("ShareCount: got %d, want 3", result.ShareCount)
		}
		if len(dist.Shares()) != 3 {
			t.Errorf("captured shares: got %d, want 3", len(dist.Shares()))
		}
	})

	t.Run("ErrShamirThresholdInvalid_ThresholdLessThan2", func(t *testing.T) {
		b, _, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 1, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_ThresholdGreaterThanTotal", func(t *testing.T) {
		b, _, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 6, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_ZeroThreshold", func(t *testing.T) {
		b, _, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, 0, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrShamirThresholdInvalid_NegativeThreshold", func(t *testing.T) {
		b, _, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRecoveryKeys(ctx, -1, 5)
		if !errors.Is(err, ErrShamirThresholdInvalid) {
			t.Errorf("got %v, want ErrShamirThresholdInvalid", err)
		}
	})

	t.Run("ErrCeremonyDistributorNotConfigured", func(t *testing.T) {
		// Barrier with no recovery distributor wired.
		backend := NewMemoryBackend()
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath: "test/root-key",
		}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatal(err)
		}
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		_, err = b.GenerateRecoveryKeys(ctx, 3, 5)
		if !errors.Is(err, ErrCeremonyDistributorNotConfigured) {
			t.Errorf("got %v, want ErrCeremonyDistributorNotConfigured", err)
		}
	})

	t.Run("ErrSealed_BarrierNotInitialized", func(t *testing.T) {
		b, _, _ := newRecoveryBarrier(t)
		ctx := context.Background()

		// Barrier is sealed (not initialized), so GetMasterKey will fail.
		_, err := b.GenerateRecoveryKeys(ctx, 3, 5)
		if !errors.Is(err, ErrSealed) {
			t.Errorf("got %v, want ErrSealed", err)
		}
	})

	t.Run("MultipleGenerateOverwritesMetadata", func(t *testing.T) {
		b, backend, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		// First generation: 2 of 3.
		if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
			t.Fatal(err)
		}

		// Second generation: 3 of 5 overwrites.
		if _, err := b.GenerateRecoveryKeys(ctx, 3, 5); err != nil {
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
		b, _, dist := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		if _, err := b.GenerateRecoveryKeys(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		seen := make(map[string]struct{}, len(shares))
		for i, share := range shares {
			if _, exists := seen[share]; exists {
				t.Errorf("Share[%d] is a duplicate", i)
			}
			seen[share] = struct{}{}
		}
	})
}

func TestRecoveryRecoverWithKeys(t *testing.T) {
	t.Run("Success_ExactThreshold", func(t *testing.T) {
		b, _, dist := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		if _, err := b.GenerateRecoveryKeys(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		// Seal the barrier (software strategy — direct Seal is allowed).
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}
		if !b.IsSealed() {
			t.Fatal("barrier should be sealed")
		}

		// Recover with exactly threshold shares.
		err := b.RecoverWithKeys(ctx, shares[:3])
		if err != nil {
			t.Fatalf("RecoverWithKeys: unexpected error: %v", err)
		}
		if b.IsSealed() {
			t.Error("barrier should be unsealed after recovery")
		}
	})

	t.Run("Success_AllShares", func(t *testing.T) {
		b, _, dist := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		if _, err := b.GenerateRecoveryKeys(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Recover with all shares (more than threshold).
		err := b.RecoverWithKeys(ctx, shares)
		if err != nil {
			t.Fatalf("RecoverWithKeys with all shares: unexpected error: %v", err)
		}
		if b.IsSealed() {
			t.Error("barrier should be unsealed after recovery")
		}
	})

	t.Run("BarrierTransitionsFromSealedToUnsealed", func(t *testing.T) {
		b, _, dist := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}
		if !b.IsSealed() {
			t.Fatal("expected sealed")
		}

		if err := b.RecoverWithKeys(ctx, shares[:2]); err != nil {
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
		b, _, dist := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		// Barrier is already unsealed.
		err := b.RecoverWithKeys(ctx, shares[:2])
		if !errors.Is(err, ErrAlreadyUnsealed) {
			t.Errorf("got %v, want ErrAlreadyUnsealed", err)
		}
	})

	t.Run("ErrRecoveryKeysNotFound", func(t *testing.T) {
		b, _, _ := newRecoveryBarrier(t)
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
		b, _, dist := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		if _, err := b.GenerateRecoveryKeys(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Provide only 2 shares when 3 are needed.
		err := b.RecoverWithKeys(ctx, shares[:2])
		if !errors.Is(err, ErrShamirQuorumIncomplete) {
			t.Errorf("got %v, want ErrShamirQuorumIncomplete", err)
		}
	})

	t.Run("ErrShamirCombineFailed_InvalidShares", func(t *testing.T) {
		b, backend, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		// Generate real recovery keys to persist metadata.
		if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
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

		err := b.RecoverWithKeys(ctx, []string{"invalid-share-1", "invalid-share-2"})
		if !errors.Is(err, ErrShamirCombineFailed) {
			t.Errorf("got %v, want ErrShamirCombineFailed", err)
		}
	})

	t.Run("ErrRecoveryKeysNotFound_CorruptMetadata", func(t *testing.T) {
		b, backend, _ := newRecoveryBarrier(t)
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
		b, _, dist := newRecoveryBarrier(t)
		ctx := context.Background()

		// Initialize the barrier.
		if err := b.Initialize(ctx, MustCredentials([]byte("test-password"))); err != nil {
			t.Fatal(err)
		}

		// Store some data while unsealed.
		if err := b.Put(ctx, "mykey", []byte("myvalue")); err != nil {
			t.Fatal(err)
		}

		// Generate recovery keys.
		if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		// Seal the barrier.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Verify Get fails while sealed.
		_, err := b.Get(ctx, "mykey")
		if !errors.Is(err, ErrSealed) {
			t.Errorf("Get while sealed: got %v, want ErrSealed", err)
		}

		// Recover with shares.
		if err := b.RecoverWithKeys(ctx, shares[:2]); err != nil {
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
		b, _, _ := newRecoveryBarrier(t)
		initRecoveryBarrier(t, b)
		ctx := context.Background()

		if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
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
		b, _, _ := newRecoveryBarrier(t)
		// Barrier is sealed (never initialized).
		ctx := context.Background()

		err := b.DeleteRecoveryKeys(ctx)
		if !errors.Is(err, ErrSealed) {
			t.Errorf("got %v, want ErrSealed", err)
		}
	})
}

// --------------------------------------------------------------------------
// recovery.go: HasRecoveryKeys — 66.7% (sealed branch untested)
// --------------------------------------------------------------------------

func TestHasRecoveryKeys_Sealed(t *testing.T) {
	// StorageBarrier not yet initialized is sealed.
	backend := NewMemoryBackend()
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{
		RootKeyPath: "test/root",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.HasRecoveryKeys(context.Background())
	if !errors.Is(err, ErrSealed) {
		t.Errorf("HasRecoveryKeys sealed: got %v, want ErrSealed", err)
	}
}

func TestHasRecoveryKeys_Present(t *testing.T) {
	b := newUnsealedStorageBarrier(t)
	dist := newCaptureDistributor()
	b.SetRecoveryDistributor(dist)
	ctx := context.Background()

	if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
		t.Fatalf("GenerateRecoveryKeys: %v", err)
	}

	has, err := b.HasRecoveryKeys(ctx)
	if err != nil {
		t.Fatalf("HasRecoveryKeys: %v", err)
	}
	if !has {
		t.Error("expected HasRecoveryKeys=true after GenerateRecoveryKeys")
	}
}

func TestHasRecoveryKeys_Absent(t *testing.T) {
	b := newUnsealedStorageBarrier(t)
	has, err := b.HasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("HasRecoveryKeys (absent): %v", err)
	}
	if has {
		t.Error("expected HasRecoveryKeys=false when no recovery keys generated")
	}
}

// --------------------------------------------------------------------------
// recovery.go: itoa — 60% (negative branch untested)
// --------------------------------------------------------------------------

func TestItoa_Zero(t *testing.T) {
	if got := itoa(0); got != "0" {
		t.Errorf("itoa(0) = %q, want \"0\"", got)
	}
}

func TestItoa_Positive(t *testing.T) {
	if got := itoa(42); got != "42" {
		t.Errorf("itoa(42) = %q, want \"42\"", got)
	}
}

func TestItoa_Negative(t *testing.T) {
	if got := itoa(-7); got != "-7" {
		t.Errorf("itoa(-7) = %q, want \"-7\"", got)
	}
}
