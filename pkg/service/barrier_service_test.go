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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// testLogger returns a quiet logger for unit tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ---------------------------------------------------------------------------
// Barrier construction helpers
// ---------------------------------------------------------------------------

// newTestSoftwareBarrier creates a StorageBarrier backed by in-memory storage
// with a SoftwareStrategy. Suitable for testing credential-based init/unseal.
func newTestSoftwareBarrier(t *testing.T) (*seal.StorageBarrier, seal.SealingStrategy) {
	t.Helper()
	backend := seal.NewMemoryBackend()
	sw, err := seal.NewSoftwareStrategy(context.Background(), []byte("test-master-key-32bytes!abcdefgh"))
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
// Fix 2: BarrierStatus reports active Shamir threshold/total
// ---------------------------------------------------------------------------

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
