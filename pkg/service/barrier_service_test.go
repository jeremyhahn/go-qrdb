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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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
// initAndUnsealSoftwareBarrier returns a BarrierServicer over an already
// initialized, unsealed software barrier. BarrierInitialize itself is
// refused now (go-qrdb#548: the system barrier is brought up by `qrdb
// init`, not this service), so this initializes the underlying
// seal.StorageBarrier directly -- the same thing ceremony.Initialize does
// server-side -- rather than through the service method under test.
func initAndUnsealSoftwareBarrier(t *testing.T) BarrierServicer {
	t.Helper()
	barrier, _ := newTestSoftwareBarrier(t)
	ctx := context.Background()
	if err := barrier.Initialize(ctx, seal.MustCredentials([]byte("my-secret"))); err != nil {
		t.Fatalf("barrier.Initialize: %v", err)
	}
	return NewBarrierService(barrier)
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

// TestBarrierService_BarrierInitialize_Refused covers the go-qrdb#548
// refusal: the system barrier is brought up by `qrdb init`, not this
// service method, so it always refuses with *SystemBarrierInitRefusedError
// and never touches the barrier.
// TestBarrierService_BarrierInitialize_RefusedWithCeremony pins the
// go-qrdb#548 refusal to the shape that actually has a ceremony to merge
// into: a BarrierService built via NewBarrierServiceWithCeremony, exactly
// as qrdbd's own system barrier always is.
func TestBarrierService_BarrierInitialize_RefusedWithCeremony(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	if err != nil {
		t.Fatalf("ceremony.NewCeremonyService: %v", err)
	}
	svc := NewBarrierServiceWithCeremony(barrier, core)
	ctx := context.Background()

	initErr := svc.BarrierInitialize(ctx, []byte("my-secret"))
	var refused *SystemBarrierInitRefusedError
	if !errors.As(initErr, &refused) {
		t.Fatalf("BarrierInitialize = %v, want *SystemBarrierInitRefusedError", initErr)
	}
	if !barrier.IsSealed() {
		t.Error("a refused BarrierInitialize must not have touched the barrier")
	}
}

// TestBarrierService_BarrierInitialize_DelegatesWithoutCeremony covers the
// other half: a BarrierService built via the plain NewBarrierService (no
// ceremony -- the shape the embedded SDK's library-level barrier uses, with
// no multi-tenant RBAC or first-boot admin binding to merge qrdb init's
// bring-up into) still initializes the barrier directly, exactly as before
// go-qrdb#548.
func TestBarrierService_BarrierInitialize_DelegatesWithoutCeremony(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	if err := svc.BarrierInitialize(ctx, []byte("my-secret")); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
	if barrier.IsSealed() {
		t.Error("BarrierInitialize without a ceremony must unseal the barrier")
	}
}

// TestBarrierService_BarrierInitializeShamir_RefusedWithCeremony is
// BarrierInitialize's refusal (go-qrdb#548), extended to the Shamir path
// (go-qrdb#560 review item 8): BarrierInitializeShamir had no ceremony
// check at all, so an operator could call `barrier init-shamir` directly
// against qrdbd's own system barrier -- always ceremony-backed -- bringing
// it up OUTSIDE the sealed window `qrdb init` runs in, with no admin
// credential ever issued. It must refuse the same way BarrierInitialize
// does, naming the same replacement.
func TestBarrierService_BarrierInitializeShamir_RefusedWithCeremony(t *testing.T) {
	barrier := shamirTestBarrier(t, seal.NewMemoryBackend(), 2, 3)
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	if err != nil {
		t.Fatalf("ceremony.NewCeremonyService: %v", err)
	}
	svc := NewBarrierServiceWithCeremony(barrier, core)
	ctx := context.Background()

	_, initErr := svc.BarrierInitializeShamir(ctx, 2, 3, seal.FactorPolicyDesignated)
	var refused *SystemBarrierInitRefusedError
	if !errors.As(initErr, &refused) {
		t.Fatalf("BarrierInitializeShamir = %v, want *SystemBarrierInitRefusedError", initErr)
	}
	if !barrier.IsSealed() {
		t.Error("a refused BarrierInitializeShamir must not have touched the barrier")
	}
}

// TestBarrierService_BarrierInitializeShamir_DelegatesWithoutCeremony is
// BarrierInitialize's other half (go-qrdb#548) extended to the Shamir
// path: a BarrierService with no ceremony to merge into is NEVER refused
// by the ceremony check this fix adds -- it reaches the underlying
// barrier unchanged, exactly as before this fix, and fails (if it does)
// only for a reason unrelated to it, such as Shamir custody's own
// distributor wiring, which is this path's existing, separate
// responsibility and out of scope here.
func TestBarrierService_BarrierInitializeShamir_DelegatesWithoutCeremony(t *testing.T) {
	barrier := shamirTestBarrier(t, seal.NewMemoryBackend(), 2, 3)
	svc := NewBarrierService(barrier)
	ctx := context.Background()

	_, err := svc.BarrierInitializeShamir(ctx, 2, 3, seal.FactorPolicyDesignated)
	var refused *SystemBarrierInitRefusedError
	if errors.As(err, &refused) {
		t.Fatalf("BarrierInitializeShamir without a ceremony = %v, must never be the ceremony refusal", err)
	}
}

func TestBarrierService_BarrierUnseal(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	if err := barrier.Initialize(context.Background(), seal.MustCredentials([]byte("my-secret"))); err != nil {
		t.Fatalf("barrier.Initialize: %v", err)
	}
	svc := NewBarrierService(barrier)
	ctx := context.Background()

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
	ctx := context.Background()
	if err := barrier.Initialize(ctx, seal.MustCredentials([]byte("my-secret"))); err != nil {
		t.Fatalf("barrier.Initialize: %v", err)
	}
	svc := NewBarrierService(barrier)

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
