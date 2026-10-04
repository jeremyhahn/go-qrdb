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
	"testing"
)

// newTestSealStore creates an initialized, unsealed barrier and returns
// a SealStore backed by it with the given prefix. If prefix is empty,
// the default "sealstore/" is used.
func newTestSealStore(t *testing.T, prefix string) *SealStore {
	t.Helper()
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("test-password"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ss, err := NewSealStore(b, prefix, testLogger())
	if err != nil {
		t.Fatalf("NewSealStore: %v", err)
	}
	return ss
}

// newTestSealStoreWithBarrier returns both the SealStore and the
// underlying barrier so tests can seal/unseal it.
func newTestSealStoreWithBarrier(t *testing.T) (*SealStore, *StorageBarrier) {
	t.Helper()
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("test-password"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ss, err := NewSealStore(b, "", testLogger())
	if err != nil {
		t.Fatalf("NewSealStore: %v", err)
	}
	return ss, b
}

// ---------- Constructor tests ----------

func TestSealStore_New_Success(t *testing.T) {
	ss := newTestSealStore(t, "")
	if ss == nil {
		t.Fatal("expected non-nil SealStore")
	}
}

func TestSealStore_New_NilBarrier(t *testing.T) {
	_, err := NewSealStore(nil, "", testLogger())
	if err != ErrNilSystemBarrier {
		t.Errorf("expected ErrNilSystemBarrier, got %v", err)
	}
}

func TestSealStore_New_DefaultPrefix(t *testing.T) {
	ss := newTestSealStore(t, "")
	if ss.prefix != "sealstore/" {
		t.Errorf("expected default prefix 'sealstore/', got %q", ss.prefix)
	}
}

func TestSealStore_New_CustomPrefix(t *testing.T) {
	ss := newTestSealStore(t, "mysecrets")
	if ss.prefix != "mysecrets/" {
		t.Errorf("expected prefix 'mysecrets/', got %q", ss.prefix)
	}
}

func TestSealStore_New_PrefixWithTrailingSlash(t *testing.T) {
	ss := newTestSealStore(t, "already/")
	if ss.prefix != "already/" {
		t.Errorf("expected prefix 'already/', got %q", ss.prefix)
	}
}

// ---------- Put/Get round-trip tests ----------

func TestSealStore_PutGet_Success(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	secret := []byte("super-secret-value")
	if err := ss.Put(ctx, "db-password", secret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := ss.Get(ctx, "db-password")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Errorf("Get returned %q, want %q", got, secret)
	}
}

func TestSealStore_PutGet_EmptyName(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	err := ss.Put(ctx, "", []byte("data"))
	if err != ErrInvalidSecretName {
		t.Errorf("Put with empty name: expected ErrInvalidSecretName, got %v", err)
	}

	_, err = ss.Get(ctx, "")
	if err != ErrInvalidSecretName {
		t.Errorf("Get with empty name: expected ErrInvalidSecretName, got %v", err)
	}
}

func TestSealStore_PutGet_Overwrite(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	if err := ss.Put(ctx, "key", []byte("v1")); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := ss.Put(ctx, "key", []byte("v2")); err != nil {
		t.Fatalf("Put v2: %v", err)
	}

	got, err := ss.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("v2")) {
		t.Errorf("expected v2, got %q", got)
	}
}

// ---------- Get not found ----------

func TestSealStore_Get_NotFound(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	_, err := ss.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent secret")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreSecretNotFound) {
		t.Errorf("expected ErrSealStoreSecretNotFound, got %v", storeErr.Err)
	}
}

// ---------- Delete tests ----------

func TestSealStore_Delete_Success(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	if err := ss.Put(ctx, "to-delete", []byte("val")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ss.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify it is gone.
	_, err := ss.Get(ctx, "to-delete")
	if err == nil {
		t.Fatal("expected error after delete")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T", err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreSecretNotFound) {
		t.Errorf("expected ErrSealStoreSecretNotFound after delete, got %v", storeErr.Err)
	}
}

func TestSealStore_Delete_NotFound(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	err := ss.Delete(ctx, "ghost")
	if err == nil {
		t.Fatal("expected error for nonexistent delete")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreSecretNotFound) {
		t.Errorf("expected ErrSealStoreSecretNotFound, got %v", storeErr.Err)
	}
}

func TestSealStore_Delete_EmptyName(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	err := ss.Delete(ctx, "")
	if err != ErrInvalidSecretName {
		t.Errorf("expected ErrInvalidSecretName, got %v", err)
	}
}

// ---------- List tests ----------

func TestSealStore_List_Success(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		if err := ss.Put(ctx, name, []byte("val-"+name)); err != nil {
			t.Fatalf("Put(%s): %v", name, err)
		}
	}

	got, err := ss.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(names) {
		t.Fatalf("expected %d names, got %d: %v", len(names), len(got), got)
	}
	// List returns sorted names.
	for i, name := range names {
		if got[i] != name {
			t.Errorf("List[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestSealStore_List_Empty(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	got, err := ss.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", got)
	}
}

// ---------- Reseal tests ----------

func TestSealStore_Reseal_Success(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	original := []byte("reseal-me-please")
	if err := ss.Put(ctx, "reseal-target", original); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := ss.Reseal(ctx, "reseal-target"); err != nil {
		t.Fatalf("Reseal: %v", err)
	}

	// Verify the data is still correct after reseal.
	got, err := ss.Get(ctx, "reseal-target")
	if err != nil {
		t.Fatalf("Get after Reseal: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("data mismatch after reseal: got %q, want %q", got, original)
	}
}

func TestSealStore_Reseal_NotFound(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	err := ss.Reseal(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent reseal")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
}

func TestSealStore_Reseal_EmptyName(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	err := ss.Reseal(ctx, "")
	if err != ErrInvalidSecretName {
		t.Errorf("expected ErrInvalidSecretName, got %v", err)
	}
}

// ---------- Status tests ----------

func TestSealStore_Status_Unsealed(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	// Put some secrets.
	for _, name := range []string{"a", "b", "c"} {
		if err := ss.Put(ctx, name, []byte("val")); err != nil {
			t.Fatalf("Put(%s): %v", name, err)
		}
	}

	status, err := ss.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Available {
		t.Error("expected Available=true")
	}
	if status.SecretCount != 3 {
		t.Errorf("expected SecretCount=3, got %d", status.SecretCount)
	}
	if len(status.SecretNames) != 3 {
		t.Errorf("expected 3 secret names, got %d", len(status.SecretNames))
	}
	if status.Strategy != StrategyPassphrase {
		t.Errorf("expected strategy %q, got %q", StrategyPassphrase, status.Strategy)
	}
}

func TestSealStore_Status_Sealed(t *testing.T) {
	ss, b := newTestSealStoreWithBarrier(t)
	ctx := context.Background()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	status, err := ss.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Available {
		t.Error("expected Available=false when sealed")
	}
	if status.SecretCount != 0 {
		t.Errorf("expected SecretCount=0 when sealed, got %d", status.SecretCount)
	}
}

// ---------- Barrier sealed tests ----------

func TestSealStore_BarrierSealed_Put(t *testing.T) {
	ss, b := newTestSealStoreWithBarrier(t)
	ctx := context.Background()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err := ss.Put(ctx, "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreBarrierSealed) {
		t.Errorf("expected ErrSealStoreBarrierSealed, got %v", storeErr.Err)
	}
}

func TestSealStore_BarrierSealed_Get(t *testing.T) {
	ss, b := newTestSealStoreWithBarrier(t)
	ctx := context.Background()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err := ss.Get(ctx, "key")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreBarrierSealed) {
		t.Errorf("expected ErrSealStoreBarrierSealed, got %v", storeErr.Err)
	}
}

func TestSealStore_BarrierSealed_Delete(t *testing.T) {
	ss, b := newTestSealStoreWithBarrier(t)
	ctx := context.Background()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err := ss.Delete(ctx, "key")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreBarrierSealed) {
		t.Errorf("expected ErrSealStoreBarrierSealed, got %v", storeErr.Err)
	}
}

func TestSealStore_BarrierSealed_List(t *testing.T) {
	ss, b := newTestSealStoreWithBarrier(t)
	ctx := context.Background()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err := ss.List(ctx)
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreBarrierSealed) {
		t.Errorf("expected ErrSealStoreBarrierSealed, got %v", storeErr.Err)
	}
}

func TestSealStore_BarrierSealed_Reseal(t *testing.T) {
	ss, b := newTestSealStoreWithBarrier(t)
	ctx := context.Background()

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err := ss.Reseal(ctx, "key")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var storeErr *SealStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected SealStoreError, got %T: %v", err, err)
	}
	if !errors.Is(storeErr.Err, ErrSealStoreBarrierSealed) {
		t.Errorf("expected ErrSealStoreBarrierSealed, got %v", storeErr.Err)
	}
}

// ---------- Namespace isolation tests ----------

func TestSealStore_NamespaceIsolation(t *testing.T) {
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(context.Background(), []byte("test-password"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ss1, err := NewSealStore(b, "ns1/", testLogger())
	if err != nil {
		t.Fatalf("NewSealStore ns1: %v", err)
	}
	ss2, err := NewSealStore(b, "ns2/", testLogger())
	if err != nil {
		t.Fatalf("NewSealStore ns2: %v", err)
	}

	// Store different values under the same name in different namespaces.
	if err := ss1.Put(ctx, "shared-name", []byte("ns1-value")); err != nil {
		t.Fatalf("ss1.Put: %v", err)
	}
	if err := ss2.Put(ctx, "shared-name", []byte("ns2-value")); err != nil {
		t.Fatalf("ss2.Put: %v", err)
	}

	// Each namespace sees only its own value.
	got1, err := ss1.Get(ctx, "shared-name")
	if err != nil {
		t.Fatalf("ss1.Get: %v", err)
	}
	if !bytes.Equal(got1, []byte("ns1-value")) {
		t.Errorf("ss1 got %q, want %q", got1, "ns1-value")
	}

	got2, err := ss2.Get(ctx, "shared-name")
	if err != nil {
		t.Fatalf("ss2.Get: %v", err)
	}
	if !bytes.Equal(got2, []byte("ns2-value")) {
		t.Errorf("ss2 got %q, want %q", got2, "ns2-value")
	}

	// List from ss1 does not include ss2 secrets.
	list1, err := ss1.List(ctx)
	if err != nil {
		t.Fatalf("ss1.List: %v", err)
	}
	if len(list1) != 1 || list1[0] != "shared-name" {
		t.Errorf("ss1.List = %v, want [shared-name]", list1)
	}

	list2, err := ss2.List(ctx)
	if err != nil {
		t.Fatalf("ss2.List: %v", err)
	}
	if len(list2) != 1 || list2[0] != "shared-name" {
		t.Errorf("ss2.List = %v, want [shared-name]", list2)
	}

	// Delete from ss1 does not affect ss2.
	if err := ss1.Delete(ctx, "shared-name"); err != nil {
		t.Fatalf("ss1.Delete: %v", err)
	}
	_, err = ss1.Get(ctx, "shared-name")
	if err == nil {
		t.Fatal("ss1.Get after delete should fail")
	}

	got2Again, err := ss2.Get(ctx, "shared-name")
	if err != nil {
		t.Fatalf("ss2.Get after ss1 delete: %v", err)
	}
	if !bytes.Equal(got2Again, []byte("ns2-value")) {
		t.Errorf("ss2 value changed after ss1 delete: got %q", got2Again)
	}
}

// ---------- Close tests ----------

func TestSealStore_Close(t *testing.T) {
	ss := newTestSealStore(t, "")
	if err := ss.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !ss.closed.Load() {
		t.Error("expected closed=true after Close()")
	}
}

// ---------- SealStoreError tests ----------

func TestSealStoreError_WithKey(t *testing.T) {
	err := &SealStoreError{Op: "get", Key: "mykey", Err: ErrSealStoreSecretNotFound}
	expected := "sealstore: get mykey: qrdb/seal: sealstore secret not found"
	if err.Error() != expected {
		t.Errorf("error string = %q, want %q", err.Error(), expected)
	}
}

func TestSealStoreError_WithoutKey(t *testing.T) {
	err := &SealStoreError{Op: "list", Err: ErrSealStoreBarrierSealed}
	expected := "sealstore: list: qrdb/seal: sealstore barrier is sealed"
	if err.Error() != expected {
		t.Errorf("error string = %q, want %q", err.Error(), expected)
	}
}

func TestSealStoreError_Unwrap(t *testing.T) {
	inner := ErrSealStoreSecretNotFound
	err := &SealStoreError{Op: "get", Key: "k", Err: inner}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return the inner error")
	}
}

// ---------- Binary data round-trip ----------

func TestSealStore_PutGet_BinaryData(t *testing.T) {
	ss := newTestSealStore(t, "")
	ctx := context.Background()

	// Store binary data with null bytes and high bytes.
	secret := make([]byte, 256)
	for i := range secret {
		secret[i] = byte(i)
	}

	if err := ss.Put(ctx, "binary-secret", secret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := ss.Get(ctx, "binary-secret")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Error("binary data round-trip mismatch")
	}
}
