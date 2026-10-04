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
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
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
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("test-password"))); err != nil {
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
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	if rs.ID() != StrategyID("recovery") {
		t.Errorf("ID: got %q, want %q", rs.ID(), "recovery")
	}
}

func TestRecoveryStrategy_Available(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	if !rs.Available() {
		t.Error("Available: expected true")
	}
}

func TestRecoveryStrategy_HardwareBacked(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	if rs.HardwareBacked() {
		t.Error("HardwareBacked: expected false")
	}
}

func TestRecoveryStrategy_Close(t *testing.T) {
	key := []byte{0x01, 0x02, 0x03}
	rs, err := newRecoveryStrategyForTest(key, nil)
	if err != nil {
		t.Fatalf("newRecoveryStrategy: %v", err)
	}
	// Before Close the key is readable through the protected enclave.
	buf, openErr := rs.rootKey.Open(t.Context())
	if openErr != nil {
		t.Fatalf("root key should be openable before Close: %v", openErr)
	}
	buf.Destroy()

	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the protected enclave is destroyed: opening it must fail
	// rather than yield readable key material.
	if _, openErr := rs.rootKey.Open(t.Context()); openErr == nil {
		t.Error("Close should destroy the protected root key; Open still succeeded")
	}
}

func TestRecoveryStrategy_SealRootKey(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	_, err := rs.SealRootKey(context.Background(), []byte("root"), Credentials{})
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Errorf("SealRootKey: got %v, want ErrAlreadyInitialized", err)
	}
}

func TestRecoveryStrategy_BarrierEncryptor_NilDelegate(t *testing.T) {
	rs, rsErr := newRecoveryStrategyForTest([]byte("key"), nil)
	if rsErr != nil {
		t.Fatalf("newRecoveryStrategy: %v", rsErr)
	}
	enc, err := rs.BarrierEncryptor(context.Background(), []byte("root"))
	if err != nil {
		t.Fatalf("BarrierEncryptor with nil delegate: %v", err)
	}
	if enc != nil {
		t.Error("expected nil encryptor when delegate is nil")
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
	if err := tb.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
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
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
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
	b.SetCeremonyDistributor(newCaptureDistributor("h1", "h2", "h3"))

	_, err = b.InitializeShamir(ctx, "")
	if err == nil {
		t.Fatal("expected error when Exists fails")
	}
}

// --------------------------------------------------------------------------
// tenant_keys.go: wrapAndStore Put error, RotateTenantDEK wrap error
// --------------------------------------------------------------------------

// --------------------------------------------------------------------------
// helpers: errorBackend for injecting storage errors
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

// The temporary recovery strategy's protected copy of the root key is
// destroyed before a quorum unseal returns.
//
// Barrier.Unseal does not retain the strategy — it calls UnsealRootKey and
// drops it — so without the deferred Close the copy would outlive the round
// that produced it, at the one point where the root key is most exposed
// (found in review of go-qrdb#46). The strategy is built and discarded inside
// unsealFromRootKey, so it is observed through newRecoveryStrategyFn.
func TestQuorumUnseal_DestroysTheTemporaryStrategysRootKeyCopy(t *testing.T) {
	for _, tc := range []struct {
		domain QuorumDomain
		op     QuorumOp
	}{{DomainUnseal, OpUnseal}, {DomainRecovery, OpRecover}} {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newHolderFixture(t, 2, 3)
			f.dist.setHolders(DomainRecovery, "r1", "r2", "r3")
			if _, err := f.b.GenerateRecoveryKeys(context.Background(), 2, ""); err != nil {
				t.Fatal(err)
			}
			f.sealWith("h1", "h2")

			var captured *recoveryStrategy
			original := newRecoveryStrategyFn
			newRecoveryStrategyFn = func(ctx context.Context, rootKey *secure.Buffer, delegate SealingStrategy) (*recoveryStrategy, error) {
				rs, err := original(ctx, rootKey, delegate)
				if err != nil {
					return nil, err
				}
				captured = rs
				buf, openErr := captured.rootKey.Open(ctx)
				if openErr != nil {
					t.Fatalf("the strategy was handed an unreadable root key; this test would prove nothing: %v", openErr)
				}
				buf.Destroy()
				return captured, nil
			}
			t.Cleanup(func() { newRecoveryStrategyFn = original })

			holders := []string{"h1", "h2"}
			if tc.domain == DomainRecovery {
				holders = []string{"r1", "r3"}
			}
			if o := f.quorum(tc.domain, tc.op, holders...); !o.Complete {
				t.Fatalf("quorum incomplete: %+v", o)
			}
			if captured == nil {
				t.Fatal("no recovery strategy was built; the seam did not fire")
			}
			if _, openErr := captured.rootKey.Open(t.Context()); openErr == nil {
				t.Error("the temporary strategy's protected root-key copy was left readable after the unseal returned")
			}
		})
	}
}

// Close destroys the protected copy the deferred cleanup relies on. Asserted
// directly because the recovery paths depend on it and nothing else in this
// package does.
func TestRecoveryStrategy_CloseWipesItsRootKeyCopy(t *testing.T) {
	key := []byte("root-key-material")
	want := append([]byte(nil), key...)
	recStrat, err := newRecoveryStrategyForTest(key, nil)
	if err != nil {
		t.Fatalf("newRecoveryStrategyForTest: %v", err)
	}

	// The strategy holds a COPY in protected memory: clearing the caller's
	// slice must not change what the strategy protects, or the test would pass
	// against a constructor that aliased the caller's key.
	for i := range key {
		key[i] = 0
	}
	buf, openErr := recStrat.rootKey.Open(t.Context())
	if openErr != nil {
		t.Fatalf("root key should be readable before Close: %v", openErr)
	}
	aliased := !bytes.Equal(buf.Bytes(), want)
	buf.Destroy()
	if aliased {
		t.Fatal("the strategy aliased the caller's key instead of copying it")
	}

	if err := recStrat.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the protected enclave is destroyed: opening it must fail.
	if _, openErr := recStrat.rootKey.Open(t.Context()); openErr == nil {
		t.Error("Close left the strategy's protected root-key copy readable")
	}
}

func allZeroBytes(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// TestNamespacedBackend_GuardedVerbsStayInTheNamespace: compare-and-delete
// and compare-and-swap act on the namespaced key only, answer false on a
// stale expectation, and a base with no guarded swap is refused rather than
// degraded to a read followed by a write.
func TestNamespacedBackend_GuardedVerbsStayInTheNamespace(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	if err := base.Put(ctx, "other/k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	ns := NewNamespacedBackend("ns/", base)
	if err := ns.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if swapped, err := ns.CompareAndSwap(ctx, "k", []byte("stale"), []byte("x")); swapped || err != nil {
		t.Fatalf("stale swap = %v, %v", swapped, err)
	}
	if swapped, err := ns.CompareAndSwap(ctx, "k", []byte("v"), []byte("w")); !swapped || err != nil {
		t.Fatalf("swap = %v, %v", swapped, err)
	}
	if got, _ := base.Get(ctx, "ns/k"); string(got) != "w" {
		t.Fatalf("namespaced value after swap = %q", got)
	}
	if deleted, err := ns.CompareAndDelete(ctx, "k", []byte("w")); !deleted || err != nil {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if got, _ := base.Get(ctx, "other/k"); string(got) != "v" {
		t.Fatalf("a key outside the namespace changed: %q", got)
	}

	plain := NewNamespacedBackend("ns/", unfencedMemory{NewMemoryBackend()})
	if swapped, err := plain.CompareAndSwap(ctx, "k", []byte("v"), []byte("w")); swapped || !errors.Is(err, ErrGuardedSwapUnsupported) {
		t.Fatalf("swap over a base without guarded swap = %v, %v", swapped, err)
	}
}
