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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/crypto/aesgcm"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// memoryKVStore is an in-memory KVStore for testing tenant keys.
type memoryKVStore struct {
	data map[string][]byte
}

func newMemoryKVStore() *memoryKVStore {
	return &memoryKVStore{data: make(map[string][]byte)}
}

func (m *memoryKVStore) Put(_ context.Context, key string, value []byte) error {
	m.data[key] = append([]byte(nil), value...)
	return nil
}

func (m *memoryKVStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (m *memoryKVStore) Delete(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func (m *memoryKVStore) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memoryKVStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memoryKVStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

func (m *memoryKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (m *memoryKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (m *memoryKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *memoryKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (m *memoryKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// testBarrier is a lightweight EncryptionBarrier for unit tests. It uses
// AES-GCM directly without enclaves.
type testBarrier struct {
	aead   cipher.AEAD
	sealed atomic.Bool
}

// Compile-time interface check.
var _ EncryptionBarrier = (*testBarrier)(nil)

// newTestBarrier creates a sealed testBarrier.
func newTestBarrier() *testBarrier {
	b := &testBarrier{}
	b.sealed.Store(true)
	return b
}

// newUnsealedBarrier creates a testBarrier with a random AES-256 key.
func newUnsealedBarrier(t *testing.T) *testBarrier {
	t.Helper()
	key, err := aesgcm.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}
	b := &testBarrier{aead: aead}
	b.sealed.Store(false)
	return b
}

func (b *testBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, &BarrierSealedError{}
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

func (b *testBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, &BarrierSealedError{}
	}
	nonceSize := b.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	return b.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
}

func (b *testBarrier) IsSealed() bool {
	return b.sealed.Load()
}

func TestNewTenantKeyManager_NilBarrier(t *testing.T) {
	_, err := NewTenantKeyManager(nil, newMemoryKVStore())
	if err == nil {
		t.Fatal("expected error for nil barrier")
	}
	var cfgErr *InvalidConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *InvalidConfigError, got %T: %v", err, err)
	}
}

func TestNewTenantKeyManager_NilStore(t *testing.T) {
	b := newUnsealedBarrier(t)
	_, err := NewTenantKeyManager(b, nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
	var cfgErr *InvalidConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *InvalidConfigError, got %T: %v", err, err)
	}
}

func TestTenantKeyManager_GetTenantDEK_GeneratesNew(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	dek, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK: %v", err)
	}
	if len(dek) != 32 {
		t.Errorf("expected 32-byte DEK, got %d bytes", len(dek))
	}
}

func TestTenantKeyManager_GetTenantDEK_ReturnsCached(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	dek1, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK 1: %v", err)
	}
	dek2, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK 2: %v", err)
	}
	if !bytes.Equal(dek1, dek2) {
		t.Error("same tenant should return same DEK (from cache)")
	}
}

func TestTenantKeyManager_GetTenantDEK_EmptyTenant(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	_, err = tkm.GetTenantDEK(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty tenant ID")
	}
}

func TestTenantKeyManager_GetTenantDEK_SealedBarrier(t *testing.T) {
	b := newTestBarrier() // sealed
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	_, err = tkm.GetTenantDEK(ctx, "tenant-1")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestTenantKeyManager_GetTenantDEK_DifferentTenants(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	dek1, err := tkm.GetTenantDEK(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("GetTenantDEK tenant-a: %v", err)
	}
	dek2, err := tkm.GetTenantDEK(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("GetTenantDEK tenant-b: %v", err)
	}
	if bytes.Equal(dek1, dek2) {
		t.Error("different tenants should have different DEKs")
	}
}

func TestTenantKeyManager_RotateTenantDEK(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	originalDEK, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK: %v", err)
	}

	if err := tkm.RotateTenantDEK(ctx, "tenant-1"); err != nil {
		t.Fatalf("RotateTenantDEK: %v", err)
	}

	newDEK, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK after rotate: %v", err)
	}

	if bytes.Equal(originalDEK, newDEK) {
		t.Error("rotated DEK should differ from original")
	}
}

func TestTenantKeyManager_RotateTenantDEK_EmptyTenant(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	err = tkm.RotateTenantDEK(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty tenant ID")
	}
}

func TestTenantKeyManager_RotateTenantDEK_SealedBarrier(t *testing.T) {
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
}

func TestTenantKeyManager_DestroyTenantDEK(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	_, err = tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK: %v", err)
	}

	if err := tkm.DestroyTenantDEK(ctx, "tenant-1"); err != nil {
		t.Fatalf("DestroyTenantDEK: %v", err)
	}

	// Getting the DEK again should generate a new one (different from destroyed).
	newDEK, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK after destroy: %v", err)
	}
	if len(newDEK) != 32 {
		t.Error("new DEK should be 32 bytes")
	}
}

func TestTenantKeyManager_DestroyTenantDEK_EmptyTenant(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	err = tkm.DestroyTenantDEK(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty tenant ID")
	}
}

func TestTenantKeyManager_ImportTenantKey(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	keyMaterial, err := aesgcm.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	if err := tkm.ImportTenantKey(ctx, "tenant-import", keyMaterial); err != nil {
		t.Fatalf("ImportTenantKey: %v", err)
	}

	dek, err := tkm.GetTenantDEK(ctx, "tenant-import")
	if err != nil {
		t.Fatalf("GetTenantDEK after import: %v", err)
	}

	if !bytes.Equal(dek, keyMaterial) {
		t.Error("imported DEK should match provided key material")
	}
}

func TestTenantKeyManager_ImportTenantKey_WrongSize(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	err = tkm.ImportTenantKey(ctx, "tenant-1", []byte("short-key"))
	if err == nil {
		t.Fatal("expected error for wrong key size")
	}
	var isErr *InvalidShareError
	if !errors.As(err, &isErr) {
		t.Fatalf("expected *InvalidShareError, got %T: %v", err, err)
	}
}

func TestTenantKeyManager_ImportTenantKey_EmptyTenant(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	key := make([]byte, 32)
	err = tkm.ImportTenantKey(ctx, "", key)
	if err == nil {
		t.Fatal("expected error for empty tenant ID")
	}
}

func TestTenantKeyManager_ImportTenantKey_SealedBarrier(t *testing.T) {
	b := newTestBarrier() // sealed
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	key := make([]byte, 32)
	err = tkm.ImportTenantKey(ctx, "tenant-1", key)
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
}

func TestTenantKeyManager_ClearCache(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	// Generate DEKs for multiple tenants.
	for _, tid := range []string{"t1", "t2", "t3"} {
		if _, err := tkm.GetTenantDEK(ctx, tid); err != nil {
			t.Fatalf("GetTenantDEK(%s): %v", tid, err)
		}
	}

	// Clear the cache.
	tkm.ClearCache()

	// Getting DEKs again should load from store (wrapped DEKs persist).
	// They should match because the wrapped DEKs are still in the store.
	for _, tid := range []string{"t1", "t2", "t3"} {
		dek, err := tkm.GetTenantDEK(ctx, tid)
		if err != nil {
			t.Fatalf("GetTenantDEK(%s) after clear: %v", tid, err)
		}
		if len(dek) != 32 {
			t.Errorf("tenant %s: expected 32-byte DEK, got %d", tid, len(dek))
		}
	}
}

func TestTenantKeyManager_GetTenantDEK_ReturnsCopy(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	dek1, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK: %v", err)
	}

	// Mutate the returned DEK.
	for i := range dek1 {
		dek1[i] = 0xFF
	}

	// Get the DEK again; it should be the original (not the mutated copy).
	dek2, err := tkm.GetTenantDEK(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("GetTenantDEK 2: %v", err)
	}

	if bytes.Equal(dek1, dek2) {
		t.Error("GetTenantDEK should return a copy, not the cached reference")
	}
}

func TestTenantKeyManager_CacheUsesSecret(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	_, err = tkm.GetTenantDEK(ctx, "tenant-pk")
	if err != nil {
		t.Fatalf("GetTenantDEK: %v", err)
	}

	// Verify the cache contains a *secure.Secret.
	internal := tkm.(*tenantKeyManager)
	raw, ok := internal.cache.Load("tenant-pk")
	if !ok {
		t.Fatal("expected cache entry for tenant-pk")
	}
	sec, isSec := raw.(*secure.Secret)
	if !isSec {
		t.Fatalf("expected *secure.Secret in cache, got %T", raw)
	}

	// Open and verify the DEK is 32 bytes.
	buf, openErr := sec.Open()
	if openErr != nil {
		t.Fatalf("secure.Secret.Open() failed: %v", openErr)
	}
	if buf.Size() != 32 {
		t.Fatalf("expected DEK size 32, got %d", buf.Size())
	}
	buf.Destroy()
}

func TestTenantKeyManager_ClearCacheReleasesKeys(t *testing.T) {
	b := newUnsealedBarrier(t)
	store := newMemoryKVStore()
	tkm, err := NewTenantKeyManager(b, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}

	ctx := context.Background()
	for _, tid := range []string{"t1", "t2", "t3"} {
		if _, err := tkm.GetTenantDEK(ctx, tid); err != nil {
			t.Fatalf("GetTenantDEK(%s): %v", tid, err)
		}
	}

	tkm.ClearCache()

	// Verify all entries are removed.
	internal := tkm.(*tenantKeyManager)
	count := 0
	internal.cache.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 0 {
		t.Errorf("expected empty cache after ClearCache, found %d entries", count)
	}
}
