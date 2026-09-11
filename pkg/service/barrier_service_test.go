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
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"testing"

	quicraftShamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// testLogger returns a quiet logger for unit tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ---------------------------------------------------------------------------
// testCaptureDistributor — local Shamir distributor for service-layer tests.
//
// Implements seal.CeremonyDistributor by splitting with the real Shamir library
// and recording the share strings so tests can later call UnsealWithShare /
// RecoverWithKeys. This mirrors the ceremony package's production split path
// without requiring a full ceremony setup.
// ---------------------------------------------------------------------------

type testCaptureDistributor struct {
	mu     sync.Mutex
	shares []string
}

func newTestCaptureDistributor() *testCaptureDistributor {
	return &testCaptureDistributor{}
}

// SealAndDistribute splits secret into total shares with the given threshold,
// records them for retrieval, and returns the count.
func (c *testCaptureDistributor) SealAndDistribute(_ context.Context, secret []byte, threshold, total int) (int, error) {
	raw, err := quicraftShamir.Split(secret, threshold, total)
	if err != nil {
		return 0, fmt.Errorf("testCaptureDistributor: split failed: %w", err)
	}
	c.mu.Lock()
	c.shares = make([]string, len(raw))
	for i, s := range raw {
		c.shares[i] = s.Value
	}
	c.mu.Unlock()
	return len(raw), nil
}

// RekeyAndDistribute destroys the prior round and produces fresh shares.
func (c *testCaptureDistributor) RekeyAndDistribute(ctx context.Context, newSecret []byte, newThreshold, newTotal int) (int, error) {
	c.mu.Lock()
	c.shares = nil
	c.mu.Unlock()
	return c.SealAndDistribute(ctx, newSecret, newThreshold, newTotal)
}

// Shares returns a copy of the share strings captured during the last split.
func (c *testCaptureDistributor) Shares() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shares == nil {
		return nil
	}
	out := make([]string, len(c.shares))
	copy(out, c.shares)
	return out
}

// ---------------------------------------------------------------------------
// Barrier construction helpers
// ---------------------------------------------------------------------------

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
	if err := svc.BarrierInitialize(ctx, []byte("my-secret")); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
	return svc
}

// initShamirBarrier initializes a Shamir barrier with wired ceremony and
// recovery capture distributors. Returns the service, the ShamirInitInfo
// (which contains ShareCount, not raw shares), and the ceremony distributor
// whose Shares() method provides the actual share strings for unseal and
// root-token operations.
//
// The recovery distributor is always wired so tests that seal then call
// GenerateRecoveryKeys do not fail with ErrCeremonyDistributorNotConfigured.
func initShamirBarrier(t *testing.T) (BarrierServicer, *ShamirInitInfo, *testCaptureDistributor) {
	t.Helper()
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	recDist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	barrier.SetRecoveryDistributor(recDist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()
	info, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	return svc, info, dist
}

// ---------------------------------------------------------------------------
// INV-1: ShamirInitInfo and RecoveryKeysInfo must not carry raw share fields
// ---------------------------------------------------------------------------

// TestShamirInitInfo_NoSharesField asserts that ShamirInitInfo has no field
// named "Shares". If it does, raw share material could escape in wire responses
// violating the per-SO claim-only distribution invariant.
func TestShamirInitInfo_NoSharesField(t *testing.T) {
	rt := reflect.TypeOf(ShamirInitInfo{})
	if _, ok := rt.FieldByName("Shares"); ok {
		t.Errorf("ShamirInitInfo must not have a Shares field (INV-1: shares must only reach SOs via ClaimShare)")
	}
}

// TestRecoveryKeysInfo_NoKeysField asserts that RecoveryKeysInfo has no field
// named "Keys". If it does, raw recovery share material could escape in wire
// responses violating the per-SO claim-only distribution invariant.
func TestRecoveryKeysInfo_NoKeysField(t *testing.T) {
	rt := reflect.TypeOf(RecoveryKeysInfo{})
	if _, ok := rt.FieldByName("Keys"); ok {
		t.Errorf("RecoveryKeysInfo must not have a Keys field (INV-1: recovery shares must only reach SOs via ClaimShare)")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle Tests
// ---------------------------------------------------------------------------

func TestBarrierService_BarrierInitialize(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	err := svc.BarrierInitialize(ctx, []byte("my-secret"))
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

	if err := svc.BarrierInitialize(ctx, []byte("my-secret")); err != nil {
		t.Fatalf("first BarrierInitialize: %v", err)
	}

	err := svc.BarrierInitialize(ctx, []byte("my-secret"))
	if !errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Errorf("second BarrierInitialize = %v, want %v", err, seal.ErrAlreadyInitialized)
	}
}

func TestBarrierService_BarrierUnseal(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if err := svc.BarrierInitialize(ctx, []byte("my-secret")); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}

	// Seal first so we can unseal.
	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	err := svc.BarrierUnseal(ctx, []byte("my-secret"))
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
	err := svc.BarrierUnseal(ctx, []byte("my-secret"))
	if !errors.Is(err, seal.ErrNotInitialized) {
		t.Errorf("BarrierUnseal on uninitialized = %v, want %v", err, seal.ErrNotInitialized)
	}
}

func TestBarrierService_BarrierUnseal_AlreadyUnsealed(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if err := svc.BarrierInitialize(ctx, []byte("my-secret")); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}

	// Barrier is already unsealed after initialization.
	err := svc.BarrierUnseal(ctx, []byte("my-secret"))
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

// TestBarrierSeal_ShamirRequiresQuorum verifies INV-8: single-admin BarrierSeal
// is blocked for Shamir-protected barriers; the caller must use
// BarrierShamirSealVote to collect a quorum instead.
func TestBarrierSeal_ShamirRequiresQuorum(t *testing.T) {
	svc, _, _ := initShamirBarrier(t)
	ctx := context.Background()

	err := svc.BarrierSeal(ctx)
	if !errors.Is(err, seal.ErrShamirBarrierRequiresQuorum) {
		t.Errorf("BarrierSeal on Shamir barrier = %v, want %v", err, seal.ErrShamirBarrierRequiresQuorum)
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

// ---------------------------------------------------------------------------
// Shamir Tests
// ---------------------------------------------------------------------------

func TestBarrierService_BarrierInitializeShamir(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	info, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir returned unexpected error: %v", err)
	}
	if info.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", info.Threshold)
	}
	if info.TotalShares != 3 {
		t.Errorf("TotalShares = %d, want 3", info.TotalShares)
	}
	if info.ShareCount != 3 {
		t.Errorf("ShareCount = %d, want 3", info.ShareCount)
	}
	// Verify the distributor captured the shares.
	shares := dist.Shares()
	if len(shares) != 3 {
		t.Errorf("distributor captured %d shares, want 3", len(shares))
	}
	for i, s := range shares {
		if s == "" {
			t.Errorf("captured share[%d] is empty", i)
		}
	}
}

func TestBarrierService_BarrierInitializeShamir_AlreadyInitialized(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	_, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3)
	if err != nil {
		t.Fatalf("first BarrierInitializeShamir: %v", err)
	}

	_, err = svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3)
	if !errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Errorf("second BarrierInitializeShamir = %v, want %v", err, seal.ErrAlreadyInitialized)
	}
}

func TestBarrierService_BarrierInitializeShamir_CredentialMode(t *testing.T) {
	barrier := newTestSoftwareBarrierWithShamir(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	info, err := svc.BarrierInitializeShamir(ctx, []byte("my-secret"), 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir (credential mode): %v", err)
	}
	if info.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", info.Threshold)
	}
	if info.ShareCount != 3 {
		t.Errorf("ShareCount = %d, want 3", info.ShareCount)
	}
}

func TestBarrierService_BarrierUnsealWithShare(t *testing.T) {
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal the barrier so we can unseal with shares.
	// Shamir barriers require quorum to seal, so we use SubmitSealVote path.
	// For testing, we use the underlying barrier directly via the service
	// BarrierShamirSealVote which requires a valid share. Since we're just
	// testing the unseal flow, seal via BarrierShamirSealVote with quorum.
	// Submit 2 shares (threshold = 2) to seal.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	// Submit first unseal share - should not complete yet.
	progress, err := svc.BarrierUnsealWithShare(ctx, shares[0])
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
	progress, err = svc.BarrierUnsealWithShare(ctx, shares[1])
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
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Barrier is already unsealed after init. Submitting a share should error.
	_, err := svc.BarrierUnsealWithShare(ctx, shares[0])
	if !errors.Is(err, seal.ErrAlreadyUnsealed) {
		t.Errorf("BarrierUnsealWithShare on unsealed = %v, want %v", err, seal.ErrAlreadyUnsealed)
	}
}

func TestBarrierService_BarrierUnsealWithShares(t *testing.T) {
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal via quorum votes.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	// Provide all shares at once.
	err := svc.BarrierUnsealWithShares(ctx, shares)
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
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal via quorum votes.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	// Provide only 1 share when threshold is 2.
	err := svc.BarrierUnsealWithShares(ctx, shares[:1])
	if !errors.Is(err, seal.ErrShamirQuorumIncomplete) {
		t.Errorf("BarrierUnsealWithShares with 1 share = %v, want %v", err, seal.ErrShamirQuorumIncomplete)
	}
}

// ---------------------------------------------------------------------------
// Rekey Tests
// ---------------------------------------------------------------------------

func TestBarrierService_BarrierRekey(t *testing.T) {
	svc, _, _ := initShamirBarrier(t)
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
	if result.ShareCount != 5 {
		t.Errorf("ShareCount = %d, want 5", result.ShareCount)
	}
}

func TestBarrierService_BarrierRekey_Sealed(t *testing.T) {
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal via quorum votes.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	_, err := svc.BarrierRekey(ctx, 3, 5)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierRekey on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

func TestBarrierService_BarrierRekey_InvalidThreshold(t *testing.T) {
	svc, _, _ := initShamirBarrier(t)
	ctx := context.Background()

	_, err := svc.BarrierRekey(ctx, 1, 5)
	if !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Errorf("BarrierRekey with threshold=1 = %v, want %v", err, seal.ErrShamirThresholdInvalid)
	}
}

// ---------------------------------------------------------------------------
// Recovery Tests
// ---------------------------------------------------------------------------

func TestBarrierService_BarrierGenerateRecoveryKeys(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	recDist := newTestCaptureDistributor()
	barrier.SetRecoveryDistributor(recDist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	// Initialize first so recovery keys can be generated.
	if _, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3); err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}

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
	if result.ShareCount != 3 {
		t.Errorf("ShareCount = %d, want 3", result.ShareCount)
	}
	// Verify the recovery distributor captured the shares.
	recoveryShares := recDist.Shares()
	if len(recoveryShares) != 3 {
		t.Errorf("recovery distributor captured %d shares, want 3", len(recoveryShares))
	}
}

func TestBarrierService_BarrierGenerateRecoveryKeys_InvalidThreshold(t *testing.T) {
	svc, _, _ := initShamirBarrier(t)
	ctx := context.Background()

	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 1, 3)
	if !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Errorf("BarrierGenerateRecoveryKeys with threshold=1 = %v, want %v", err, seal.ErrShamirThresholdInvalid)
	}
}

func TestBarrierService_BarrierGenerateRecoveryKeys_Sealed(t *testing.T) {
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal via quorum votes.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierGenerateRecoveryKeys on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

func TestBarrierService_BarrierRecoverWithKeys(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	recDist := newTestCaptureDistributor()
	barrier.SetRecoveryDistributor(recDist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	// Initialize first.
	if _, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3); err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}

	// Generate recovery keys while unsealed.
	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	recoveryShares := recDist.Shares()

	// Seal via quorum votes using unseal shares.
	unsealShares := dist.Shares()
	if _, err := svc.BarrierShamirSealVote(ctx, unsealShares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, unsealShares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	// Recover using the recovery keys.
	err = svc.BarrierRecoverWithKeys(ctx, recoveryShares)
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
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	recDist := newTestCaptureDistributor()
	barrier.SetRecoveryDistributor(recDist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if _, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3); err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}

	_, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, 3)
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	recoveryShares := recDist.Shares()

	unsealShares := dist.Shares()
	if _, err := svc.BarrierShamirSealVote(ctx, unsealShares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, unsealShares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	// Provide only 1 key when threshold is 2.
	err = svc.BarrierRecoverWithKeys(ctx, recoveryShares[:1])
	if !errors.Is(err, seal.ErrShamirQuorumIncomplete) {
		t.Errorf("BarrierRecoverWithKeys with 1 key = %v, want %v", err, seal.ErrShamirQuorumIncomplete)
	}
}

func TestBarrierService_BarrierRecoverWithKeys_NoRecoveryMeta(t *testing.T) {
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal via quorum votes.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	// Attempt recovery without ever generating recovery keys.
	err := svc.BarrierRecoverWithKeys(ctx, []string{"fake-share-1", "fake-share-2"})
	if !errors.Is(err, seal.ErrRecoveryKeysNotFound) {
		t.Errorf("BarrierRecoverWithKeys without meta = %v, want %v", err, seal.ErrRecoveryKeysNotFound)
	}
}

func TestBarrierService_BarrierDeleteRecoveryKeys(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	recDist := newTestCaptureDistributor()
	barrier.SetRecoveryDistributor(recDist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if _, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3); err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}

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
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal via quorum votes.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	err := svc.BarrierDeleteRecoveryKeys(ctx)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierDeleteRecoveryKeys on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

func TestBarrierService_BarrierHasRecoveryKeys(t *testing.T) {
	barrier := newTestShamirBarrier(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	recDist := newTestCaptureDistributor()
	barrier.SetRecoveryDistributor(recDist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if _, err := svc.BarrierInitializeShamir(ctx, []byte(""), 2, 3); err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}

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
	svc, _, _ := initShamirBarrier(t)
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
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Seal via quorum votes.
	if _, err := svc.BarrierShamirSealVote(ctx, shares[0]); err != nil {
		t.Fatalf("first BarrierShamirSealVote: %v", err)
	}
	if _, err := svc.BarrierShamirSealVote(ctx, shares[1]); err != nil {
		t.Fatalf("second BarrierShamirSealVote: %v", err)
	}

	_, err := svc.BarrierHasRecoveryKeys(ctx)
	if !errors.Is(err, seal.ErrSealed) {
		t.Errorf("BarrierHasRecoveryKeys on sealed = %v, want %v", err, seal.ErrSealed)
	}
}

// ---------------------------------------------------------------------------
// Root Token Tests
// ---------------------------------------------------------------------------

func TestBarrierService_BarrierGenerateRootToken(t *testing.T) {
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Generate a root token using the shares.
	token, err := svc.BarrierGenerateRootToken(ctx, shares)
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
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	// Provide only 1 share when threshold is 2.
	_, err := svc.BarrierGenerateRootToken(ctx, shares[:1])
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
	svc, _, dist := initShamirBarrier(t)
	ctx := context.Background()
	shares := dist.Shares()

	token1, err := svc.BarrierGenerateRootToken(ctx, shares)
	if err != nil {
		t.Fatalf("first BarrierGenerateRootToken: %v", err)
	}

	token2, err := svc.BarrierGenerateRootToken(ctx, shares)
	if err != nil {
		t.Fatalf("second BarrierGenerateRootToken: %v", err)
	}

	// Each call should produce a unique token due to random nonce.
	if token1.Token == token2.Token {
		t.Error("expected unique tokens from successive calls, got identical values")
	}
}

// ---------------------------------------------------------------------------
// Credential-Mode Shamir Unseal Tests
// ---------------------------------------------------------------------------

func TestBarrierService_BarrierUnsealWithShares_CredentialMode(t *testing.T) {
	barrier := newTestSoftwareBarrierWithShamir(t)
	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	_, err := svc.BarrierInitializeShamir(ctx, []byte("my-secret"), 2, 3)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	shares := dist.Shares()

	if err := svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	err = svc.BarrierUnsealWithShares(ctx, shares)
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

// ---------------------------------------------------------------------------
// Fix 1: InitializeShamir honors request threshold/total
// ---------------------------------------------------------------------------

// TestBarrierService_InitializeShamir_RequestParamsWin verifies that when
// explicit threshold/total are provided to BarrierInitializeShamir, those
// values override the boot-time daemon config (direct-mode ShamirStrategy).
func TestBarrierService_InitializeShamir_RequestParamsWin(t *testing.T) {
	tests := []struct {
		name             string
		daemonThreshold  int
		daemonTotal      int
		requestThreshold int
		requestTotal     int
		wantThreshold    int
		wantTotal        int
		wantShareCount   int
	}{
		{
			name:             "request 3-of-5 overrides daemon 2-of-3",
			daemonThreshold:  2,
			daemonTotal:      3,
			requestThreshold: 3,
			requestTotal:     5,
			wantThreshold:    3,
			wantTotal:        5,
			wantShareCount:   5,
		},
		{
			name:             "request 2-of-4 overrides daemon 3-of-5",
			daemonThreshold:  3,
			daemonTotal:      5,
			requestThreshold: 2,
			requestTotal:     4,
			wantThreshold:    2,
			wantTotal:        4,
			wantShareCount:   4,
		},
		{
			name:             "request matches daemon config",
			daemonThreshold:  2,
			daemonTotal:      3,
			requestThreshold: 2,
			requestTotal:     3,
			wantThreshold:    2,
			wantTotal:        3,
			wantShareCount:   3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := seal.NewMemoryBackend()
			shamirStrat, err := seal.NewShamirStrategy(tc.daemonThreshold, tc.daemonTotal)
			if err != nil {
				t.Fatalf("NewShamirStrategy: %v", err)
			}
			config := seal.BarrierConfig{
				RootKeyPath:     "root-key",
				PreferenceOrder: []seal.StrategyID{seal.StrategyShamir},
				Shamir: &seal.ShamirConfig{
					Threshold:   tc.daemonThreshold,
					TotalShares: tc.daemonTotal,
				},
			}
			barrier, err := seal.NewStorageBarrier(testLogger(), backend, config, shamirStrat)
			if err != nil {
				t.Fatalf("NewStorageBarrier: %v", err)
			}
			t.Cleanup(func() { _ = barrier.Close() })

			dist := newTestCaptureDistributor()
			barrier.SetCeremonyDistributor(dist)

			svc := NewBarrierService(barrier)
			ctx := context.Background()

			info, err := svc.BarrierInitializeShamir(ctx, []byte(""), tc.requestThreshold, tc.requestTotal)
			if err != nil {
				t.Fatalf("BarrierInitializeShamir: %v", err)
			}
			if info.Threshold != tc.wantThreshold {
				t.Errorf("Threshold = %d, want %d", info.Threshold, tc.wantThreshold)
			}
			if info.TotalShares != tc.wantTotal {
				t.Errorf("TotalShares = %d, want %d", info.TotalShares, tc.wantTotal)
			}
			if info.ShareCount != tc.wantShareCount {
				t.Errorf("ShareCount = %d, want %d", info.ShareCount, tc.wantShareCount)
			}
			// Distributor must have captured the expected number of shares.
			if got := len(dist.Shares()); got != tc.wantShareCount {
				t.Errorf("distributor captured %d shares, want %d", got, tc.wantShareCount)
			}
		})
	}
}

// TestBarrierService_InitializeShamir_ZeroValueFallsBackToConfig verifies
// that when the request omits threshold/total (both zero), the barrier's
// existing daemon config is preserved as the fallback.
func TestBarrierService_InitializeShamir_ZeroValueFallsBackToConfig(t *testing.T) {
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

	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)

	svc := NewBarrierService(barrier)
	ctx := context.Background()

	// Pass threshold=0, totalShares=0 — request omits the params.
	info, err := svc.BarrierInitializeShamir(ctx, []byte(""), 0, 0)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir with zero params: %v", err)
	}
	if info.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2 (daemon config fallback)", info.Threshold)
	}
	if info.TotalShares != 3 {
		t.Errorf("TotalShares = %d, want 3 (daemon config fallback)", info.TotalShares)
	}
	if info.ShareCount != 3 {
		t.Errorf("ShareCount = %d, want 3", info.ShareCount)
	}
}

// TestBarrierService_InitializeShamir_CredentialMode_RequestParamsWin verifies
// the "request wins" behavior also holds in credential mode (no ShamirStrategy).
func TestBarrierService_InitializeShamir_CredentialMode_RequestParamsWin(t *testing.T) {
	backend := seal.NewMemoryBackend()
	sw, err := seal.NewSoftwareStrategy([]byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	config := seal.BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase},
		// Daemon config says 2-of-3.
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

	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)

	svc := NewBarrierService(barrier)
	ctx := context.Background()

	// Request 3-of-5 — must override daemon config.
	info, err := svc.BarrierInitializeShamir(ctx, []byte("my-secret"), 3, 5)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	if info.Threshold != 3 {
		t.Errorf("Threshold = %d, want 3", info.Threshold)
	}
	if info.TotalShares != 5 {
		t.Errorf("TotalShares = %d, want 5", info.TotalShares)
	}
	if info.ShareCount != 5 {
		t.Errorf("ShareCount = %d, want 5", info.ShareCount)
	}
}

// ---------------------------------------------------------------------------
// Fix 2: BarrierStatus reports active Shamir threshold/total
// ---------------------------------------------------------------------------

// TestBarrierService_BarrierStatus_ShamirFields verifies that after a Shamir
// initialization, BarrierStatus reports the correct ShamirThreshold and
// ShamirTotal fields.
func TestBarrierService_BarrierStatus_ShamirFields(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		total     int
	}{
		{"2-of-3", 2, 3},
		{"3-of-5", 3, 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := seal.NewMemoryBackend()
			shamirStrat, err := seal.NewShamirStrategy(tc.threshold, tc.total)
			if err != nil {
				t.Fatalf("NewShamirStrategy: %v", err)
			}
			config := seal.BarrierConfig{
				RootKeyPath:     "root-key",
				PreferenceOrder: []seal.StrategyID{seal.StrategyShamir},
				Shamir: &seal.ShamirConfig{
					Threshold:   tc.threshold,
					TotalShares: tc.total,
				},
			}
			barrier, err := seal.NewStorageBarrier(testLogger(), backend, config, shamirStrat)
			if err != nil {
				t.Fatalf("NewStorageBarrier: %v", err)
			}
			t.Cleanup(func() { _ = barrier.Close() })

			dist := newTestCaptureDistributor()
			barrier.SetCeremonyDistributor(dist)

			svc := NewBarrierService(barrier)
			ctx := context.Background()

			_, err = svc.BarrierInitializeShamir(ctx, []byte(""), tc.threshold, tc.total)
			if err != nil {
				t.Fatalf("BarrierInitializeShamir: %v", err)
			}

			status, err := svc.BarrierStatus(ctx)
			if err != nil {
				t.Fatalf("BarrierStatus: %v", err)
			}
			if status.ShamirThreshold != tc.threshold {
				t.Errorf("ShamirThreshold = %d, want %d", status.ShamirThreshold, tc.threshold)
			}
			if status.ShamirTotal != tc.total {
				t.Errorf("ShamirTotal = %d, want %d", status.ShamirTotal, tc.total)
			}
		})
	}
}

// TestBarrierService_BarrierStatus_ShamirFields_AfterRequestOverride verifies
// that when the request changes threshold/total at init time, BarrierStatus
// reflects the overridden values (not the daemon config).
func TestBarrierService_BarrierStatus_ShamirFields_AfterRequestOverride(t *testing.T) {
	backend := seal.NewMemoryBackend()
	// Daemon says 2-of-3.
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

	dist := newTestCaptureDistributor()
	barrier.SetCeremonyDistributor(dist)

	svc := NewBarrierService(barrier)
	ctx := context.Background()

	// Request 3-of-5.
	_, err = svc.BarrierInitializeShamir(ctx, []byte(""), 3, 5)
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.ShamirThreshold != 3 {
		t.Errorf("ShamirThreshold = %d, want 3 (request override)", status.ShamirThreshold)
	}
	if status.ShamirTotal != 5 {
		t.Errorf("ShamirTotal = %d, want 5 (request override)", status.ShamirTotal)
	}
}

// TestBarrierService_BarrierStatus_NonShamir_ZeroFields verifies that for
// a non-Shamir barrier, ShamirThreshold and ShamirTotal are zero.
func TestBarrierService_BarrierStatus_NonShamir_ZeroFields(t *testing.T) {
	svc := initAndUnsealSoftwareBarrier(t)
	ctx := context.Background()

	status, err := svc.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.ShamirThreshold != 0 {
		t.Errorf("ShamirThreshold = %d, want 0 for non-shamir barrier", status.ShamirThreshold)
	}
	if status.ShamirTotal != 0 {
		t.Errorf("ShamirTotal = %d, want 0 for non-shamir barrier", status.ShamirTotal)
	}
}

// ---------------------------------------------------------------------------
// ErrCeremonyNotConfigured: all SO methods require wired CeremonyService
// ---------------------------------------------------------------------------

// TestBarrierService_OfficerOps_CeremonyNotConfigured verifies that every
// Security Officer and recovery method returns ErrCeremonyNotConfigured when
// no CeremonyService has been wired via NewBarrierServiceWithCeremony.
func TestBarrierService_OfficerOps_CeremonyNotConfigured(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier) // no ceremony wired
	ctx := context.Background()

	req := &BarrierOfficerRegisterRequest{
		Username: "alice",
		CSRPEM:   []byte("fake-csr"),
	}

	tests := []struct {
		name string
		fn   func() error
	}{
		{
			name: "BarrierOfficerRegister",
			fn:   func() error { _, err := svc.BarrierOfficerRegister(ctx, req); return err },
		},
		{
			name: "BarrierOfficerList",
			fn:   func() error { _, err := svc.BarrierOfficerList(ctx); return err },
		},
		{
			name: "BarrierOfficerClaimStatus",
			fn:   func() error { _, err := svc.BarrierOfficerClaimStatus(ctx); return err },
		},
		{
			name: "BarrierShareClaimBegin",
			fn: func() error {
				_, err := svc.BarrierShareClaimBegin(ctx, "alice", "thumbprint")
				return err
			},
		},
		{
			name: "BarrierShareClaimComplete",
			fn: func() error {
				_, err := svc.BarrierShareClaimComplete(ctx, "alice", "nonce", []byte("sig"), "thumbprint")
				return err
			},
		},
		{
			name: "BarrierRecoveryOfficerRegister",
			fn:   func() error { _, err := svc.BarrierRecoveryOfficerRegister(ctx, req); return err },
		},
		{
			name: "BarrierRecoveryShareClaimBegin",
			fn: func() error {
				_, err := svc.BarrierRecoveryShareClaimBegin(ctx, "alice", "thumbprint")
				return err
			},
		},
		{
			name: "BarrierRecoveryShareClaimComplete",
			fn: func() error {
				_, err := svc.BarrierRecoveryShareClaimComplete(ctx, "alice", "nonce", []byte("sig"), "thumbprint")
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if !errors.Is(err, ErrCeremonyNotConfigured) {
				t.Errorf("%s = %v, want %v", tc.name, err, ErrCeremonyNotConfigured)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewBarrierService Construction Test
// ---------------------------------------------------------------------------

func TestNewBarrierService(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	if svc == nil {
		t.Fatal("NewBarrierService returned nil")
	}
	// Verify the returned value satisfies the interface.
	_ = BarrierServicer(svc)
}
