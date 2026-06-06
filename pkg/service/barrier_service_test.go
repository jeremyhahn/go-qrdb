// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// testLogger returns a quiet logger for unit tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestSoftwareBarrier creates a StorageBarrier backed by in-memory storage
// with a SoftwareStrategy. Suitable for testing credential-based init/unseal.
func newTestSoftwareBarrier(t *testing.T) (*seal.StorageBarrier, seal.SealingStrategy) {
	t.Helper()
	backend := seal.NewMemoryBackend()
	sw, err := seal.NewSoftwareStrategy([]byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	config := seal.BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase},
	}
	barrier, err := seal.NewStorageBarrier(testLogger(), backend, config, sw)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	return barrier, sw
}

// newTestShamirBarrier creates a StorageBarrier backed by in-memory storage
// with a ShamirStrategy registered. Suitable for testing Shamir-based operations.
func newTestShamirBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	backend := seal.NewMemoryBackend()

	shamirStrat, err := seal.NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	config := seal.BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []seal.StrategyID{seal.StrategyShamir},
		Shamir: &seal.ShamirConfig{
			Threshold:   2,
			TotalShares: 3,
		},
	}
	barrier, err := seal.NewStorageBarrier(testLogger(), backend, config, shamirStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	return barrier
}

// newTestSoftwareBarrierWithShamir creates a StorageBarrier with a
// SoftwareStrategy but Shamir config set for credential-mode Shamir operations.
func newTestSoftwareBarrierWithShamir(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	backend := seal.NewMemoryBackend()
	sw, err := seal.NewSoftwareStrategy([]byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	config := seal.BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase},
		Shamir: &seal.ShamirConfig{
			Threshold:   2,
			TotalShares: 3,
		},
	}
	barrier, err := seal.NewStorageBarrier(testLogger(), backend, config, sw)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	return barrier
}

// initAndUnsealSoftwareBarrier initializes and returns an unsealed
// software-backed barrier service with its secret.
func initAndUnsealSoftwareBarrier(t *testing.T) BarrierServicer {
	t.Helper()
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()
	if err := svc.BarrierInitialize(ctx, "my-secret"); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
	return svc
}

// initShamirBarrier initializes a Shamir barrier and returns the service
// and the shares produced during initialization.
func initShamirBarrier(t *testing.T) (BarrierServicer, *ShamirInitInfo) {
	t.Helper()
	barrier := newTestShamirBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()
	info, err := svc.BarrierInitializeShamir(ctx, "", 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	return svc, info
}

// --- Lifecycle Tests ---

func TestBarrierService_BarrierInitialize(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	err := svc.BarrierInitialize(ctx, "my-secret")
	if err != nil {
		t.Fatalf("BarrierInitialize returned unexpected error: %v", err)
	}

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after initialization")
	}
	if status.Strategy != string(seal.StrategyPassphrase) {
		t.Errorf("strategy = %q, want %q", status.Strategy, seal.StrategyPassphrase)
	}
}

func TestBarrierService_BarrierInitialize_AlreadyInitialized(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if err := svc.BarrierInitialize(ctx, "my-secret"); err != nil {
		t.Fatalf("first BarrierInitialize: %v", err)
	}

	err := svc.BarrierInitialize(ctx, "my-secret")
	if !errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Errorf("second BarrierInitialize = %v, want %v", err, seal.ErrAlreadyInitialized)
	}
}

func TestBarrierService_BarrierUnseal(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if err := svc.BarrierInitialize(ctx, "my-secret"); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}

	// Seal first so we can unseal.
	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	err := svc.BarrierUnseal(ctx, "my-secret")
	if err != nil {
		t.Fatalf("BarrierUnseal returned unexpected error: %v", err)
	}

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after BarrierUnseal")
	}
}

func TestBarrierService_BarrierUnseal_NotInitialized(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	// Attempt to unseal a barrier that was never initialized.
	err := svc.BarrierUnseal(ctx, "my-secret")
	if !errors.Is(err, seal.ErrNotInitialized) {
		t.Errorf("BarrierUnseal on uninitialized = %v, want %v", err, seal.ErrNotInitialized)
	}
}

func TestBarrierService_BarrierUnseal_AlreadyUnsealed(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if err := svc.BarrierInitialize(ctx, "my-secret"); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}

	// Barrier is already unsealed after initialization.
	err := svc.BarrierUnseal(ctx, "my-secret")
	if !errors.Is(err, seal.ErrAlreadyUnsealed) {
		t.Errorf("BarrierUnseal on already unsealed = %v, want %v", err, seal.ErrAlreadyUnsealed)
	}
}

func TestBarrierService_BarrierSeal(t *testing.T) {
	svc := initAndUnsealSoftwareBarrier(t)
	ctx := context.Background()

	err := svc.BarrierSeal(ctx)
	if err != nil {
		t.Fatalf("BarrierSeal returned unexpected error: %v", err)
	}

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !status.Sealed {
		t.Error("expected barrier to be sealed after BarrierSeal")
	}
}

func TestBarrierService_BarrierSeal_Idempotent(t *testing.T) {
	svc := initAndUnsealSoftwareBarrier(t)
	ctx := context.Background()

	// Seal the barrier.
	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("first BarrierSeal: %v", err)
	}

	// Seal again should be idempotent (no error).
	err := svc.BarrierSeal(ctx)
	if err != nil {
		t.Errorf("second BarrierSeal = %v, want nil (idempotent)", err)
	}
}

func TestBarrierService_BarrierStatus(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	// Before initialization, barrier should be sealed.
	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !status.Sealed {
		t.Error("expected barrier to be sealed before initialization")
	}
	if status.Strategy != "" {
		t.Errorf("strategy before init = %q, want empty", status.Strategy)
	}
}

func TestBarrierService_BarrierStatus_AfterInitialize(t *testing.T) {
	svc := initAndUnsealSoftwareBarrier(t)
	ctx := context.Background()

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after init")
	}
	if status.Strategy != string(seal.StrategyPassphrase) {
		t.Errorf("strategy = %q, want %q", status.Strategy, seal.StrategyPassphrase)
	}
	if status.HardwareBacked {
		t.Error("expected HardwareBacked = false for software strategy")
	}
}

// --- Shamir Tests ---

func TestBarrierService_BarrierInitializeShamir(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	info, err := svc.BarrierInitializeShamir(ctx, "", 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir returned unexpected error: %v", err)
	}
	if info.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", info.Threshold)
	}
	if info.TotalShares != 3 {
		t.Errorf("TotalShares = %d, want 3", info.TotalShares)
	}
	if len(info.Shares) != 3 {
		t.Errorf("len(Shares) = %d, want 3", len(info.Shares))
	}
	for i, share := range info.Shares {
		if share == "" {
			t.Errorf("share[%d] is empty", i)
		}
	}
}

func TestBarrierService_BarrierInitializeShamir_AlreadyInitialized(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	_, err := svc.BarrierInitializeShamir(ctx, "", 2, 3)
	if err != nil {
		t.Fatalf("first BarrierInitializeShamir: %v", err)
	}

	_, err = svc.BarrierInitializeShamir(ctx, "", 2, 3)
	if !errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Errorf("second BarrierInitializeShamir = %v, want %v", err, seal.ErrAlreadyInitialized)
	}
}

func TestBarrierService_BarrierInitializeShamir_CredentialMode(t *testing.T) {
	barrier := newTestSoftwareBarrierWithShamir(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	info, err := svc.BarrierInitializeShamir(ctx, "my-secret", 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir (credential mode): %v", err)
	}
	if info.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", info.Threshold)
	}
	if len(info.Shares) != 3 {
		t.Errorf("len(Shares) = %d, want 3", len(info.Shares))
	}
}

func TestBarrierService_BarrierUnsealWithShare(t *testing.T) {
	svc, info := initShamirBarrier(t)
	ctx := context.Background()

	// Seal the barrier so we can unseal with shares.
	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	// Submit first share - should not complete yet.
	progress, err := svc.BarrierUnsealWithShare(ctx, info.Shares[0])
	if err != nil {
		t.Fatalf("first BarrierUnsealWithShare: %v", err)
	}
	if progress.Complete {
		t.Error("expected Complete = false after first share")
	}
	if progress.Required != 2 {
		t.Errorf("Required = %d, want 2", progress.Required)
	}
	if progress.Submitted != 1 {
		t.Errorf("Submitted = %d, want 1", progress.Submitted)
	}

	// Submit second share - should reach quorum and unseal.
	progress, err = svc.BarrierUnsealWithShare(ctx, info.Shares[1])
	if err != nil {
		t.Fatalf("second BarrierUnsealWithShare: %v", err)
	}
	if !progress.Complete {
		t.Error("expected Complete = true after second share")
	}
	if progress.Submitted != 2 {
		t.Errorf("Submitted = %d, want 2", progress.Submitted)
	}

	// Verify the barrier is now unsealed.
	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after quorum met")
	}
}

func TestBarrierService_BarrierUnsealWithShare_AlreadyUnsealed(t *testing.T) {
	svc, info := initShamirBarrier(t)
	ctx := context.Background()

	// Barrier is already unsealed after init. Submitting a share should error.
	_, err := svc.BarrierUnsealWithShare(ctx, info.Shares[0])
	if !errors.Is(err, seal.ErrAlreadyUnsealed) {
		t.Errorf("BarrierUnsealWithShare on unsealed = %v, want %v", err, seal.ErrAlreadyUnsealed)
	}
}

func TestBarrierService_BarrierUnsealWithShares(t *testing.T) {
	svc, info := initShamirBarrier(t)
	ctx := context.Background()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	// Provide all shares at once.
	err := svc.BarrierUnsealWithShares(ctx, info.Shares)
	if err != nil {
		t.Fatalf("BarrierUnsealWithShares returned unexpected error: %v", err)
	}

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after BarrierUnsealWithShares")
	}
}

func TestBarrierService_BarrierUnsealWithShares_InsufficientShares(t *testing.T) {
	svc, info := initShamirBarrier(t)
	ctx := context.Background()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	// Provide only 1 share when threshold is 2.
	err := svc.BarrierUnsealWithShares(ctx, info.Shares[:1])
	if !errors.Is(err, seal.ErrShamirQuorumIncomplete) {
		t.Errorf("BarrierUnsealWithShares with 1 share = %v, want %v", err, seal.ErrShamirQuorumIncomplete)
	}
}

// --- Rekey Tests ---

func TestBarrierService_BarrierRekey(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	result, err := svc.BarrierRekey(ctx, 3, 5)
	if err != nil {
		t.Fatalf("BarrierRekey returned unexpected error: %v", err)
	}
	if result.Threshold != 3 {
		t.Errorf("Threshold = %d, want 3", result.Threshold)
	}
	if result.TotalShares != 5 {
		t.Errorf("TotalShares = %d, want 5", result.TotalShares)
	}
	if len(result.Shares) != 5 {
		t.Errorf("len(Shares) = %d, want 5", len(result.Shares))
	}
	for i, share := range result.Shares {
		if share == "" {
			t.Errorf("rekeyed share[%d] is empty", i)
		}
	}
}

func TestBarrierService_BarrierRekey_Sealed(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	_, err := svc.BarrierRekey(ctx, 3, 5)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierRekey on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

func TestBarrierService_BarrierRekey_InvalidThreshold(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	_, err := svc.BarrierRekey(ctx, 1, 5)
	if !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Errorf("BarrierRekey with threshold=1 = %v, want %v", err, seal.ErrShamirThresholdInvalid)
	}
}

// --- Recovery Tests ---

func TestBarrierService_BarrierGenerateRecoveryKeys(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	result, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys returned unexpected error: %v", err)
	}
	if result.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", result.Threshold)
	}
	if result.Total != 3 {
		t.Errorf("Total = %d, want 3", result.Total)
	}
	if len(result.Keys) != 3 {
		t.Errorf("len(Keys) = %d, want 3", len(result.Keys))
	}
	for i, key := range result.Keys {
		if key == "" {
			t.Errorf("recovery key[%d] is empty", i)
		}
	}
}

func TestBarrierService_BarrierGenerateRecoveryKeys_InvalidThreshold(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 1, 3)
	if !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Errorf("BarrierGenerateRecoveryKeys with threshold=1 = %v, want %v", err, seal.ErrShamirThresholdInvalid)
	}
}

func TestBarrierService_BarrierGenerateRecoveryKeys_Sealed(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierGenerateRecoveryKeys on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

func TestBarrierService_BarrierRecoverWithKeys(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	// Generate recovery keys while unsealed.
	recovery, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}

	// Seal the barrier.
	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	// Recover using the recovery keys.
	err = svc.BarrierRecoverWithKeys(ctx, recovery.Keys)
	if err != nil {
		t.Fatalf("BarrierRecoverWithKeys returned unexpected error: %v", err)
	}

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after recovery")
	}
}

func TestBarrierService_BarrierRecoverWithKeys_InsufficientKeys(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	recovery, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	// Provide only 1 key when threshold is 2.
	err = svc.BarrierRecoverWithKeys(ctx, recovery.Keys[:1])
	if !errors.Is(err, seal.ErrShamirQuorumIncomplete) {
		t.Errorf("BarrierRecoverWithKeys with 1 key = %v, want %v", err, seal.ErrShamirQuorumIncomplete)
	}
}

func TestBarrierService_BarrierRecoverWithKeys_NoRecoveryMeta(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	// Attempt recovery without ever generating recovery keys.
	err := svc.BarrierRecoverWithKeys(ctx, []string{"fake-share-1", "fake-share-2"})
	if !errors.Is(err, seal.ErrRecoveryKeysNotFound) {
		t.Errorf("BarrierRecoverWithKeys without meta = %v, want %v", err, seal.ErrRecoveryKeysNotFound)
	}
}

func TestBarrierService_BarrierDeleteRecoveryKeys(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}

	err = svc.BarrierDeleteRecoveryKeys(ctx)
	if err != nil {
		t.Fatalf("BarrierDeleteRecoveryKeys returned unexpected error: %v", err)
	}

	has, err := svc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if has {
		t.Error("expected no recovery keys after delete")
	}
}

func TestBarrierService_BarrierDeleteRecoveryKeys_Sealed(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	err := svc.BarrierDeleteRecoveryKeys(ctx)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierDeleteRecoveryKeys on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

func TestBarrierService_BarrierHasRecoveryKeys(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}

	has, err := svc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if !has {
		t.Error("expected BarrierHasRecoveryKeys = true after generation")
	}
}

func TestBarrierService_BarrierHasRecoveryKeys_NoKeys(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	has, err := svc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if has {
		t.Error("expected BarrierHasRecoveryKeys = false when no recovery keys generated")
	}
}

func TestBarrierService_BarrierHasRecoveryKeys_Sealed(t *testing.T) {
	svc, _ := initShamirBarrier(t)
	ctx := context.Background()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	_, err := svc.BarrierHasRecoveryKeys(ctx)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierHasRecoveryKeys on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

// --- Root Token Tests ---

func TestBarrierService_BarrierGenerateRootToken(t *testing.T) {
	svc, info := initShamirBarrier(t)
	ctx := context.Background()

	// Generate a root token using the shares.
	token, err := svc.BarrierGenerateRootToken(ctx, info.Shares)
	if err != nil {
		t.Fatalf("BarrierGenerateRootToken returned unexpected error: %v", err)
	}
	if token.Token == "" {
		t.Error("expected non-empty token")
	}
	if token.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	// Token should be hex-encoded SHA-256 HMAC = 64 hex chars.
	if len(token.Token) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(token.Token))
	}
}

func TestBarrierService_BarrierGenerateRootToken_InsufficientShares(t *testing.T) {
	svc, info := initShamirBarrier(t)
	ctx := context.Background()

	// Provide only 1 share when threshold is 2.
	_, err := svc.BarrierGenerateRootToken(ctx, info.Shares[:1])
	if !errors.Is(err, seal.ErrShamirQuorumIncomplete) {
		t.Errorf("BarrierGenerateRootToken with 1 share = %v, want %v", err, seal.ErrShamirQuorumIncomplete)
	}
}

func TestBarrierService_BarrierGenerateRootToken_NotConfigured(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	_, err := svc.BarrierGenerateRootToken(ctx, []string{"share1", "share2"})
	if !errors.Is(err, seal.ErrShamirNotConfigured) {
		t.Errorf("BarrierGenerateRootToken = %v, want %v", err, seal.ErrShamirNotConfigured)
	}
}

func TestBarrierService_BarrierGenerateRootToken_Uniqueness(t *testing.T) {
	svc, info := initShamirBarrier(t)
	ctx := context.Background()

	token1, err := svc.BarrierGenerateRootToken(ctx, info.Shares)
	if err != nil {
		t.Fatalf("first BarrierGenerateRootToken: %v", err)
	}

	token2, err := svc.BarrierGenerateRootToken(ctx, info.Shares)
	if err != nil {
		t.Fatalf("second BarrierGenerateRootToken: %v", err)
	}

	// Each call should produce a unique token due to random nonce.
	if token1.Token == token2.Token {
		t.Error("expected unique tokens from successive calls, got identical values")
	}
}

// --- Credential-Mode Shamir Unseal Tests ---

func TestBarrierService_BarrierUnsealWithShares_CredentialMode(t *testing.T) {
	barrier := newTestSoftwareBarrierWithShamir(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	info, err := svc.BarrierInitializeShamir(ctx, "my-secret", 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	err = svc.BarrierUnsealWithShares(ctx, info.Shares)
	if err != nil {
		t.Fatalf("BarrierUnsealWithShares (credential mode) returned unexpected error: %v", err)
	}

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after credential-mode unseal with shares")
	}
}

// --- NewBarrierService Construction Test ---

func TestNewBarrierService(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	if svc == nil {
		t.Fatal("NewBarrierService returned nil")
	}
	// Verify the returned value satisfies the interface.
	_ = BarrierServicer(svc)
}
