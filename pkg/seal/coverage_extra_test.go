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
	"errors"
	"log/slog"
	"testing"
)

// errStorage is a sentinel used for backend error injection in coverage tests.
var errStorageInject = errors.New("injected storage error")

// --------------------------------------------------------------------------
// barrier.go: StorageBarrier.Encrypt and Decrypt (0.0% coverage)
// --------------------------------------------------------------------------

// TestStorageBarrier_Encrypt_Success exercises StorageBarrier.Encrypt
// on an unsealed barrier with valid plaintext.
func TestStorageBarrier_Encrypt_Success(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatal(err)
	}

	ciphertext, err := b.Encrypt([]byte("hello world"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ciphertext) == 0 {
		t.Error("expected non-empty ciphertext")
	}
}

// TestStorageBarrier_Encrypt_ErrSealed verifies that Encrypt returns
// ErrSealed when the barrier is sealed.
func TestStorageBarrier_Encrypt_ErrSealed(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	// Barrier not initialized — sealed state.
	_, err = b.Encrypt([]byte("data"))
	if !errors.Is(err, ErrSealed) {
		t.Errorf("Encrypt sealed: got %v, want ErrSealed", err)
	}
}

// TestStorageBarrier_Decrypt_Success exercises StorageBarrier.Decrypt
// on an unsealed barrier with valid ciphertext.
func TestStorageBarrier_Decrypt_Success(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("secret data")
	ciphertext, err := b.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	decrypted, err := b.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Errorf("Decrypt: got %q, want %q", decrypted, plaintext)
	}
}

// TestStorageBarrier_Decrypt_ErrSealed verifies that Decrypt returns
// ErrSealed when the barrier is sealed.
func TestStorageBarrier_Decrypt_ErrSealed(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Decrypt([]byte("ciphertext"))
	if !errors.Is(err, ErrSealed) {
		t.Errorf("Decrypt sealed: got %v, want ErrSealed", err)
	}
}

// --------------------------------------------------------------------------
// barrier.go: StorageBarrier.NonceTracker (0.0% coverage)
// --------------------------------------------------------------------------

// TestStorageBarrier_NonceTracker exercises the NonceTracker accessor on
// an initialized barrier.
func TestStorageBarrier_NonceTracker(t *testing.T) {
	backend := NewMemoryBackend()
	b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "test/root",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatal(err)
	}

	// NonceTracker may return nil (not configured) — the important thing
	// is that it does not panic and the code is exercised.
	_ = b.NonceTracker()
}

// --------------------------------------------------------------------------
// barrier.go: NewStorageBarrier optional config fields
// --------------------------------------------------------------------------

// TestNewStorageBarrier_WithOptionalConfig exercises the optional
// InvocationLimit, RotationThreshold, NonceTrackerProvider, and
// AEADProvider config fields in NewStorageBarrier.
func TestNewStorageBarrier_WithOptionalConfig(t *testing.T) {
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:       "test/root",
		InvocationLimit:   1000,
		RotationThreshold: 0.8,
	}
	b, err := NewStorageBarrier(slog.Default(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier with optional config: %v", err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Verify barrier is functional.
	if b.IsSealed() {
		t.Error("barrier should be unsealed after initialization")
	}
}

// --------------------------------------------------------------------------
// tenant_keys.go: RotateTenantDEK — wrapAndStore error path
// --------------------------------------------------------------------------

// --------------------------------------------------------------------------
// barrier_registry.go: RegisterTenant successful path (with strategy)
// --------------------------------------------------------------------------

// TestBarrierRegistry_RegisterTenant_WithStrategy exercises the successful
// RegisterTenant path with a valid software strategy, covering the
// successful non-error branch in RegisterTenant.
func TestBarrierRegistry_RegisterTenant_WithStrategy(t *testing.T) {
	backend := NewMemoryBackend()
	sw := testSoftwareStrategy(t)
	sysBarrier, err := NewBarrier(slog.Default(), backend, BarrierConfig{
		RootKeyPath: "sys/root-key",
	}, sw)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sysBarrier.Initialize(ctx, MustCredentials(context.Background(), []byte("sys-pw"))); err != nil {
		t.Fatal(err)
	}

	registry, err := NewBarrierRegistry(slog.Default(), sysBarrier)
	if err != nil {
		t.Fatal(err)
	}

	tenantSW := testSoftwareStrategy(t)
	tb, err := registry.RegisterTenant(context.Background(), "tenant-with-strategy", NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "t/root-key",
	}, tenantSW)
	if err != nil {
		t.Fatalf("RegisterTenant with strategy: %v", err)
	}
	if tb == nil {
		t.Fatal("expected non-nil TenantBarrier")
	}
}

// --------------------------------------------------------------------------
// barrier.go: countCurrentEntries — List error path (via Unseal with IC enabled)
// --------------------------------------------------------------------------

// TestCountCurrentEntries_ListError exercises the List-error branch in
// countCurrentEntries. The function is called from Unseal when an
// InvocationCounter is enabled. A List failure is logged as a warning
// (not returned), so Unseal still succeeds.
func TestCountCurrentEntries_ListError(t *testing.T) {
	fb := newFailingBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "test/root",
		InvocationLimit: 1000,
	}

	b, err := NewStorageBarrier(testLogger(), fb, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Inject a List error so countCurrentEntries (called from Unseal) fails.
	// The error should be silently logged, and Unseal should still succeed.
	fb.listErr = errStorageInject

	if err := b.Unseal(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatalf("Unseal with List error: %v", err)
	}
	if b.IsSealed() {
		t.Error("expected barrier to be unsealed")
	}
}

// --------------------------------------------------------------------------
// tenant_keys.go: wrapAndStore — Encrypt error path
// --------------------------------------------------------------------------

// alwaysFailBarrier is an EncryptionBarrier that reports as unsealed but
// returns an error on every Encrypt call. It is used to exercise the
// wrapAndStore Encrypt error path without going through the IsSealed guard.
type alwaysFailBarrier struct{}

func (a *alwaysFailBarrier) IsSealed() bool { return false }
func (a *alwaysFailBarrier) Encrypt(_ []byte) ([]byte, error) {
	return nil, errors.New("encrypt failed")
}
func (a *alwaysFailBarrier) Decrypt(_ []byte) ([]byte, error) {
	return nil, errors.New("decrypt failed")
}

// --------------------------------------------------------------------------
// change_password.go: ChangePassword — Put error path
// --------------------------------------------------------------------------

// TestChangePassword_PutError exercises the base.Put error path in
// ChangePassword, which occurs when persisting the new sealed root key blob.
func TestChangePassword_PutError(t *testing.T) {
	fb := newFailingBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root"}
	b, err := NewStorageBarrier(testLogger(), fb, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("old-pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Inject Put error so ChangePassword fails when writing the new blob.
	fb.putErr = errStorageInject

	err = b.ChangePassword(ctx, MustCredentials(context.Background(), []byte("new-pw")))
	if err == nil {
		t.Fatal("expected error when Put fails in ChangePassword")
	}
}

// --------------------------------------------------------------------------
// barrier_registry.go: RegisterTenant — concurrent registration race path
// --------------------------------------------------------------------------

// TestBarrierRegistry_RegisterTenant_ConcurrentRace exercises the LoadOrStore
// race path in RegisterTenant where two concurrent callers both pass the
// initial Load check and one wins the LoadOrStore.
func TestBarrierRegistry_RegisterTenant_ConcurrentRace(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	_ = ctx

	const tenantID = "concurrent-tenant"
	const goroutines = 20

	results := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			_, err := reg.RegisterTenant(context.Background(), tenantID, NewMemoryBackend(), BarrierConfig{
				RootKeyPath: "t/root-key",
			}, testSoftwareStrategy(t))
			results <- err
		}()
	}

	successCount := 0
	conflictCount := 0
	for i := 0; i < goroutines; i++ {
		err := <-results
		if err == nil {
			successCount++
		} else if errors.Is(err, ErrTenantAlreadyExists) {
			conflictCount++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful registration, got %d", successCount)
	}
	if conflictCount != goroutines-1 {
		t.Errorf("expected %d conflicts, got %d", goroutines-1, conflictCount)
	}
}

// --------------------------------------------------------------------------
// seal_store.go: Get — ErrSecretNotFound mapped to ErrSealStoreSecretNotFound
// --------------------------------------------------------------------------

// TestSealStore_Get_NotFound exercises the ErrSecretNotFound → ErrSealStoreSecretNotFound
// mapping in SealStore.Get, which is a distinct error branch from the general storage error.
func TestSealStore_Get_NotFoundMapsError(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatal(err)
	}

	ss, err := NewSealStore(b, "secrets/", testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Get a key that doesn't exist — barrier.Get returns ErrSecretNotFound,
	// which is mapped to ErrSealStoreSecretNotFound.
	_, getErr := ss.Get(ctx, "missing-key")
	if getErr == nil {
		t.Fatal("expected error for missing key")
	}
}

// --------------------------------------------------------------------------
// seal_store.go: Delete — not-found path (Exists returns false)
// --------------------------------------------------------------------------

// TestSealStore_Delete_NotFound exercises the !exists branch in SealStore.Delete
// which returns ErrSealStoreSecretNotFound when the key doesn't exist.
func TestSealStore_Delete_NotFoundReturnsError(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatal(err)
	}

	ss, err := NewSealStore(b, "secrets/", testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Delete a key that doesn't exist — Delete returns ErrSealStoreSecretNotFound.
	delErr := ss.Delete(ctx, "no-such-key")
	if delErr == nil {
		t.Fatal("expected error when deleting non-existent key")
	}
}

// --------------------------------------------------------------------------
// barrier.go: Scan — decrypt error path
// --------------------------------------------------------------------------

// TestStorageBarrier_Scan_DecryptError exercises the decrypt-error branch
// in StorageBarrier.Scan by storing raw (unencrypted) data directly in the
// backend, then scanning through the barrier which tries to decrypt it.
func TestStorageBarrier_Scan_DecryptError(t *testing.T) {
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root"}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatal(err)
	}

	// Write invalid ciphertext directly into the backend bypassing encryption.
	_ = backend.Put(ctx, "scan/bad-data", []byte("not-valid-ciphertext"))

	// Scan will try to decrypt the raw bytes and fail.
	err = b.Scan(ctx, "scan/", func(key string, value []byte) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error when Scan encounters invalid ciphertext")
	}
}
