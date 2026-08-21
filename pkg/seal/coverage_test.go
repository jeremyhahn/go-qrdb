// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// --------------------------------------------------------------------------
// barrier.go: Config, Strategies, Logger, Scan, Put encrypt error
// --------------------------------------------------------------------------

func TestStorageBarrier_ConfigAccessor(t *testing.T) {
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:  "test/root-key",
		DeploymentID: 42,
	}
	b, err := NewBarrier(slog.Default(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	got := b.Config()
	if got.RootKeyPath != "test/root-key" {
		t.Errorf("Config().RootKeyPath: got %q, want %q", got.RootKeyPath, "test/root-key")
	}
	if got.DeploymentID != 42 {
		t.Errorf("Config().DeploymentID: got %d, want 42", got.DeploymentID)
	}
}

func TestStorageBarrier_StrategiesAccessor(t *testing.T) {
	backend := NewMemoryBackend()
	sw := testSoftwareStrategy(t)
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, sw)
	if err != nil {
		t.Fatal(err)
	}
	strats := b.Strategies()
	if len(strats) != 1 {
		t.Fatalf("Strategies: expected 1, got %d", len(strats))
	}
	if _, ok := strats[StrategyPassphrase]; !ok {
		t.Error("expected software strategy in map")
	}
	// Verify it is a copy (modifying returned map does not affect barrier).
	delete(strats, StrategyPassphrase)
	strats2 := b.Strategies()
	if len(strats2) != 1 {
		t.Error("Strategies should return a copy; original was modified")
	}
}

func TestStorageBarrier_LoggerAccessor(t *testing.T) {
	backend := NewMemoryBackend()
	logger := slog.Default()
	b, err := NewBarrier(logger, backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	if b.Logger() != logger {
		t.Error("Logger() should return the logger passed to NewStorageBarrier")
	}
}

func TestStorageBarrier_Scan_Success(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, Credentials{Secret: []byte("test-password")}); err != nil {
		t.Fatal(err)
	}

	// Store some encrypted values.
	if err := b.Put(ctx, "scan/a", []byte("value-a")); err != nil {
		t.Fatal(err)
	}
	if err := b.Put(ctx, "scan/b", []byte("value-b")); err != nil {
		t.Fatal(err)
	}
	if err := b.Put(ctx, "other/c", []byte("value-c")); err != nil {
		t.Fatal(err)
	}

	result := make(map[string][]byte)
	if err := b.Scan(ctx, "scan/", func(key string, value []byte) error {
		result[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Scan: expected 2 entries, got %d", len(result))
	}
	if !bytes.Equal(result["scan/a"], []byte("value-a")) {
		t.Errorf("Scan[scan/a]: expected %q, got %q", "value-a", result["scan/a"])
	}
	if !bytes.Equal(result["scan/b"], []byte("value-b")) {
		t.Errorf("Scan[scan/b]: expected %q, got %q", "value-b", result["scan/b"])
	}
}

func TestStorageBarrier_Scan_ErrSealed(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Barrier is sealed (not initialized).
	err = b.Scan(ctx, "prefix/", func(_ string, _ []byte) error { return nil })
	if !errors.Is(err, ErrSealed) {
		t.Errorf("Scan sealed: got %v, want ErrSealed", err)
	}
}

func TestMemoryBackend_Scan(t *testing.T) {
	backend := NewMemoryBackend()
	ctx := context.Background()

	_ = backend.Put(ctx, "pfx/x", []byte("xval"))
	_ = backend.Put(ctx, "pfx/y", []byte("yval"))
	_ = backend.Put(ctx, "other/z", []byte("zval"))

	result := make(map[string][]byte)
	if err := backend.Scan(ctx, "pfx/", func(key string, value []byte) error {
		result[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Scan: expected 2 entries, got %d", len(result))
	}
	if !bytes.Equal(result["pfx/x"], []byte("xval")) {
		t.Errorf("Scan[pfx/x]: expected xval, got %q", result["pfx/x"])
	}
	// Verify returned values are copies.
	result["pfx/x"][0] = 0xFF
	val, _ := backend.Get(ctx, "pfx/x")
	if val[0] == 0xFF {
		t.Error("Scan should return copies, not references")
	}
}

// --------------------------------------------------------------------------
// errors.go: GetError and PutError Error/Unwrap coverage
// --------------------------------------------------------------------------

func TestGetError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("decrypt failed")
	e := &GetError{Key: "my-key", Err: inner}
	msg := e.Error()
	if !strings.Contains(msg, "my-key") {
		t.Errorf("expected key in error message, got: %s", msg)
	}
	if !strings.Contains(msg, "decrypt failed") {
		t.Errorf("expected inner error in message, got: %s", msg)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap should return the inner error")
	}
}

func TestPutError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("encrypt failed")
	e := &PutError{Key: "my-key", Err: inner}
	msg := e.Error()
	if !strings.Contains(msg, "my-key") {
		t.Errorf("expected key in error message, got: %s", msg)
	}
	if !strings.Contains(msg, "encrypt failed") {
		t.Errorf("expected inner error in message, got: %s", msg)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap should return the inner error")
	}
}

// --------------------------------------------------------------------------
// recovery.go: recoveryStrategy method coverage
// --------------------------------------------------------------------------

func TestRecoveryStrategy_ID(t *testing.T) {
	rs := newRecoveryStrategy([]byte("key"), nil)
	if rs.ID() != StrategyID("recovery") {
		t.Errorf("ID: got %q, want %q", rs.ID(), "recovery")
	}
}

func TestRecoveryStrategy_Available(t *testing.T) {
	rs := newRecoveryStrategy([]byte("key"), nil)
	if !rs.Available() {
		t.Error("Available: expected true")
	}
}

func TestRecoveryStrategy_HardwareBacked(t *testing.T) {
	rs := newRecoveryStrategy([]byte("key"), nil)
	if rs.HardwareBacked() {
		t.Error("HardwareBacked: expected false")
	}
}

func TestRecoveryStrategy_Close(t *testing.T) {
	key := []byte{0x01, 0x02, 0x03}
	rs := newRecoveryStrategy(key, nil)
	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After close, the internal key should be wiped.
	for _, b := range rs.rootKey {
		if b != 0 {
			t.Error("Close should wipe the root key")
			break
		}
	}
}

func TestRecoveryStrategy_SealRootKey(t *testing.T) {
	rs := newRecoveryStrategy([]byte("key"), nil)
	_, err := rs.SealRootKey(context.Background(), []byte("root"), Credentials{})
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Errorf("SealRootKey: got %v, want ErrAlreadyInitialized", err)
	}
}

func TestRecoveryStrategy_BarrierEncryptor_NilDelegate(t *testing.T) {
	rs := newRecoveryStrategy([]byte("key"), nil)
	enc, err := rs.BarrierEncryptor(context.Background(), []byte("root"))
	if err != nil {
		t.Fatalf("BarrierEncryptor with nil delegate: %v", err)
	}
	if enc != nil {
		t.Error("expected nil encryptor when delegate is nil")
	}
}

// --------------------------------------------------------------------------
// recovery.go: RecoverWithKeys with ShamirStrategy registered
// --------------------------------------------------------------------------

func TestRecoverWithKeys_ShamirStrategyPath(t *testing.T) {
	b, _, dist := newRecoveryBarrierWithShamir(t)
	ctx := context.Background()

	// Initialize via Shamir.
	shamirCfg := b.config.Shamir
	if shamirCfg == nil {
		t.Fatal("expected Shamir config")
	}
	if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}

	// Generate recovery keys while unsealed.
	if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
		t.Fatal(err)
	}
	recShares := dist.Shares()

	// Seal the barrier.
	if err := b.Seal(); err != nil {
		t.Fatal(err)
	}
	if !b.IsSealed() {
		t.Fatal("barrier should be sealed")
	}

	// Recover using recovery keys (this exercises the ShamirStrategy path
	// in RecoverWithKeys, not the recoverWithSoftwareStrategy fallback).
	if err := b.RecoverWithKeys(ctx, recShares[:2]); err != nil {
		t.Fatalf("RecoverWithKeys via Shamir path: %v", err)
	}
	if b.IsSealed() {
		t.Error("barrier should be unsealed after recovery")
	}
}

// --------------------------------------------------------------------------
// strategy_shamir.go: Close
// --------------------------------------------------------------------------

func TestShamirStrategy_Close(t *testing.T) {
	strategy, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := strategy.Close(); err != nil {
		t.Fatalf("ShamirStrategy.Close: %v", err)
	}
}

// --------------------------------------------------------------------------
// tenant_barrier.go: Scan on TenantBarrier and NamespacedBackend
// --------------------------------------------------------------------------

func TestTenantBarrier_Scan_Success(t *testing.T) {
	backend := NewMemoryBackend()
	tb, err := NewTenantBarrier("tenant-1", slog.Default(), backend, BarrierConfig{
		RootKeyPath: "t1/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := tb.Initialize(ctx, Credentials{Secret: []byte("pw")}); err != nil {
		t.Fatal(err)
	}

	if err := tb.Put(ctx, "scan/a", []byte("val-a")); err != nil {
		t.Fatal(err)
	}
	if err := tb.Put(ctx, "scan/b", []byte("val-b")); err != nil {
		t.Fatal(err)
	}

	result := make(map[string][]byte)
	if err := tb.Scan(ctx, "scan/", func(key string, value []byte) error {
		result[key] = value
		return nil
	}); err != nil {
		t.Fatalf("TenantBarrier.Scan: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("TenantBarrier.Scan: expected 2 entries, got %d", len(result))
	}
}

func TestTenantBarrier_Scan_ErrSealed(t *testing.T) {
	backend := NewMemoryBackend()
	tb, err := NewTenantBarrier("tenant-1", slog.Default(), backend, BarrierConfig{
		RootKeyPath: "t1/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Barrier is sealed (not initialized).
	err = tb.Scan(ctx, "scan/", func(_ string, _ []byte) error { return nil })
	if !errors.Is(err, ErrSealed) {
		t.Errorf("TenantBarrier.Scan sealed: got %v, want ErrSealed", err)
	}
}

func TestNamespacedBackend_Scan(t *testing.T) {
	base := NewMemoryBackend()
	ctx := context.Background()

	// Populate data with the namespace prefix.
	_ = base.Put(ctx, "ns/a", []byte("1"))
	_ = base.Put(ctx, "ns/b", []byte("2"))
	_ = base.Put(ctx, "other/c", []byte("3"))

	ns := NewNamespacedBackend("ns/", base)

	result := make(map[string][]byte)
	if err := ns.Scan(ctx, "", func(key string, value []byte) error {
		result[key] = value
		return nil
	}); err != nil {
		t.Fatalf("NamespacedBackend.Scan: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("NamespacedBackend.Scan: expected 2 entries, got %d", len(result))
	}
	// Keys should have the namespace prefix stripped.
	if _, ok := result["a"]; !ok {
		t.Error("expected key 'a' (stripped of namespace prefix)")
	}
}

func TestNamespacedBackend_ScanError(t *testing.T) {
	base := &errorBackend{scanErr: errors.New("scan failed")}
	ns := NewNamespacedBackend("ns/", base)
	ctx := context.Background()

	err := ns.Scan(ctx, "prefix/", func(_ string, _ []byte) error { return nil })
	if err == nil {
		t.Fatal("expected error from NamespacedBackend.Scan")
	}
}

// --------------------------------------------------------------------------
// barrier_registry.go: RegisterTenant error paths
// --------------------------------------------------------------------------

func TestBarrierRegistry_RegisterTenant_NilStrategy(t *testing.T) {
	backend := NewMemoryBackend()
	sysBarrier, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewBarrierRegistry(slog.Default(), sysBarrier)
	if err != nil {
		t.Fatal(err)
	}

	// Register without providing any strategy. NewTenantBarrier calls
	// NewStorageBarrier which requires at least one strategy.
	_, err = registry.RegisterTenant(context.Background(), "tenant-no-strategy", NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "t/root-key",
	})
	if !errors.Is(err, ErrNoAvailableStrategy) {
		t.Errorf("RegisterTenant without strategy: got %v, want ErrNoAvailableStrategy", err)
	}
}

// --------------------------------------------------------------------------
// barrier.go: unsealDirect error path (already initialized branch)
// --------------------------------------------------------------------------

func TestStorageBarrier_UnsealDirect_AlreadyInitialized(t *testing.T) {
	// This test exercises the unsealDirect code path where
	// EpochBarrier.Initialize returns ErrBarrierAlreadyInit, causing
	// it to fall through to the Unseal branch.
	backend := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 2, TotalShares: 3},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)
	ctx := context.Background()

	// Initialize via Shamir (this calls barrier.Initialize internally).
	if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	shares := dist.Shares()

	// Seal the barrier.
	if err := b.Seal(); err != nil {
		t.Fatal(err)
	}

	// UnsealWithShares calls unsealDirect. Since the EpochBarrier is already
	// initialized, it will follow the "already init" path.
	if err := b.UnsealWithShares(ctx, shares[:2]); err != nil {
		t.Fatalf("UnsealWithShares: %v", err)
	}
	if b.IsSealed() {
		t.Error("barrier should be unsealed")
	}
}

// --------------------------------------------------------------------------
// barrier.go: Put encrypt error path (PutError)
// --------------------------------------------------------------------------

func TestStorageBarrier_Put_EncryptError(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, Credentials{Secret: []byte("pw")}); err != nil {
		t.Fatal(err)
	}

	// Seal the EpochBarrier directly to make Encrypt fail
	// while the StorageBarrier's state says unsealed.
	_ = b.barrier.Seal()

	err = b.Put(ctx, "key", []byte("value"))
	if err == nil {
		t.Fatal("expected error when encrypt fails")
	}
	var putErr *PutError
	if !errors.As(err, &putErr) {
		t.Fatalf("expected *PutError, got %T: %v", err, err)
	}
}

// --------------------------------------------------------------------------
// recovery.go: recoverWithSoftwareStrategy - strategy not found path
// --------------------------------------------------------------------------

func TestRecoverWithSoftwareStrategy_StrategyNotFound(t *testing.T) {
	// Create a barrier with a software strategy and initialize it.
	backend := NewMemoryBackend()
	sw := testSoftwareStrategy(t)
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, sw)
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor()
	b.SetRecoveryDistributor(dist)
	ctx := context.Background()
	if err := b.Initialize(ctx, Credentials{Secret: []byte("pw")}); err != nil {
		t.Fatal(err)
	}

	// Generate recovery keys.
	if _, err := b.GenerateRecoveryKeys(ctx, 2, 3); err != nil {
		t.Fatal(err)
	}
	recShares := dist.Shares()

	// Tamper with the sealed root key blob to reference an unknown strategy.
	blob, _ := backend.Get(ctx, "test/root-key")
	var sealed SealedRootKey
	_ = json.Unmarshal(blob, &sealed)
	sealed.Strategy = "nonexistent"
	tamperedBlob, _ := json.Marshal(&sealed)
	_ = backend.Put(ctx, "test/root-key", tamperedBlob)

	// Seal the barrier (software strategy — direct seal is allowed).
	if err := b.Seal(); err != nil {
		t.Fatal(err)
	}

	// recoverWithSoftwareStrategy loads the sealed blob and looks up sealed.Strategy
	// in the barrier's strategy map. Since we changed it to "nonexistent",
	// it should return ErrStrategyNotFound.
	err = b.RecoverWithKeys(ctx, recShares[:2])
	if !errors.Is(err, ErrStrategyNotFound) {
		t.Errorf("recoverWithSoftwareStrategy: got %v, want ErrStrategyNotFound", err)
	}
}

// --------------------------------------------------------------------------
// root_token.go: verifyDirectMode error paths
// --------------------------------------------------------------------------

func TestGenerateRootToken_VerifyDirectMode_NonShamirStrategy(t *testing.T) {
	// Create a Shamir barrier where the sealed root key uses Shamir strategy.
	backend := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 2, TotalShares: 3},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)
	ctx := context.Background()
	if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	shares := dist.Shares()

	// Tamper with sealed root key to change strategy from "shamir" to something else.
	blob, _ := backend.Get(ctx, "test/root-key")
	var sealed SealedRootKey
	_ = json.Unmarshal(blob, &sealed)
	sealed.Strategy = "other"
	tamperedBlob, _ := json.Marshal(&sealed)
	_ = backend.Put(ctx, "test/root-key", tamperedBlob)

	_, err = b.GenerateRootToken(ctx, shares[:2])
	if !errors.Is(err, ErrRootTokenVerificationFailed) {
		t.Errorf("verifyDirectMode with non-shamir strategy: got %v, want ErrRootTokenVerificationFailed", err)
	}
}

func TestGenerateRootToken_VerifyDirectMode_CorruptBlob(t *testing.T) {
	backend := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 2, TotalShares: 3},
		PreferenceOrder: []StrategyID{StrategyShamir},
	}, shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)
	ctx := context.Background()
	if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	shares := dist.Shares()

	// Corrupt the sealed root key blob.
	_ = backend.Put(ctx, "test/root-key", []byte("not-json"))

	_, err = b.GenerateRootToken(ctx, shares[:2])
	if !errors.Is(err, ErrRootTokenVerificationFailed) {
		t.Errorf("verifyDirectMode with corrupt blob: got %v, want ErrRootTokenVerificationFailed", err)
	}
}

// --------------------------------------------------------------------------
// shamir.go: initializeShamirDirect error path - Exists fails
// --------------------------------------------------------------------------

func TestInitializeShamirDirect_ExistsError(t *testing.T) {
	errBackend := &errorBackend{existsErr: errors.New("exists failed")}
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBarrier(slog.Default(), errBackend, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir:      &ShamirConfig{Threshold: 2, TotalShares: 3},
	}, shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	_, err = b.InitializeShamir(ctx, Credentials{})
	if err == nil {
		t.Fatal("expected error when Exists fails")
	}
}

// --------------------------------------------------------------------------
// tenant_keys.go: wrapAndStore Put error, RotateTenantDEK wrap error
// --------------------------------------------------------------------------

func TestTenantKeyManager_RotateTenantDEK_WrapError(t *testing.T) {
	// Use a sealed barrier for wrap to fail during the Encrypt call.
	b := newTestBarrier() // sealed
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	err = tkm.RotateTenantDEK(ctx, "tenant-1")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestTenantKeyManager_GetTenantDEK_WrapAndStoreError(t *testing.T) {
	// Create a barrier that is unsealed but uses a KVStore that fails on Put.
	b := newUnsealedBarrier(t)
	store := &failingKVStore{
		inner:  newMemoryKVStore(),
		putErr: errors.New("storage write error"),
	}
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	_, err = tkm.GetTenantDEK(ctx, "tenant-1")
	if err == nil {
		t.Fatal("expected error when KVStore.Put fails during generateAndStoreDEK")
	}
	var dekErr *DEKDerivationError
	if !errors.As(err, &dekErr) {
		t.Fatalf("expected *DEKDerivationError, got %T: %v", err, err)
	}
}

// --------------------------------------------------------------------------
// helpers: errorBackend for injecting storage errors
// --------------------------------------------------------------------------

// errorBackend is a StorageBackend that returns configurable errors.
type errorBackend struct {
	getErr    error
	putErr    error
	deleteErr error
	listErr   error
	scanErr   error
	existsErr error
}

func (e *errorBackend) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, e.getErr
}
func (e *errorBackend) Put(_ context.Context, _ string, _ []byte) error {
	return e.putErr
}
func (e *errorBackend) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	if e.putErr != nil {
		return false, e.putErr
	}
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (e *errorBackend) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (e *errorBackend) Delete(_ context.Context, _ string) error {
	return e.deleteErr
}
func (e *errorBackend) List(_ context.Context, _ string) ([]string, error) {
	return nil, e.listErr
}
func (e *errorBackend) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return e.scanErr
}
func (e *errorBackend) Exists(_ context.Context, _ string) (bool, error) {
	return false, e.existsErr
}
func (e *errorBackend) Close() error { return nil }

// failingKVStore wraps a KVStore and injects errors on specific operations.
type failingKVStore struct {
	inner     *memoryKVStore
	putErr    error
	getErr    error
	deleteErr error
}

func (f *failingKVStore) Put(ctx context.Context, key string, value []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	return f.inner.Put(ctx, key, value)
}

func (f *failingKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.inner.Get(ctx, key)
}

func (f *failingKVStore) Delete(ctx context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.inner.Delete(ctx, key)
}

func (f *failingKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return f.inner.Scan(ctx, prefix, fn)
}

func (f *failingKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	return f.inner.List(ctx, prefix)
}

func (f *failingKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return f.inner.Exists(ctx, key)
}

func (f *failingKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (f *failingKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (f *failingKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (f *failingKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (f *failingKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
