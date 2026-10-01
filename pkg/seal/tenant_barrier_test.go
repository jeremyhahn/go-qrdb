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
	"testing"
)

// ---------- NewTenantBarrier tests ----------

func TestTenantBarrier_NewTenantBarrier_Success(t *testing.T) {
	base := NewMemoryBackend()
	config := BarrierConfig{RootKeyPath: ""}
	strategy := testSoftwareStrategy(t)

	tb, err := NewTenantBarrier("tenant-1", testLogger(), base, config, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	if tb.TenantID() != "tenant-1" {
		t.Errorf("expected TenantID=tenant-1, got %s", tb.TenantID())
	}

	if tb.Barrier() == nil {
		t.Fatal("expected non-nil Barrier()")
	}

	// Should default the root key path.
	if tb.Barrier().config.RootKeyPath != "tenant/tenant-1/root-key" {
		t.Errorf("expected default root key path, got %s", tb.Barrier().config.RootKeyPath)
	}
}

func TestTenantBarrier_NewTenantBarrier_CustomRootKeyPath(t *testing.T) {
	base := NewMemoryBackend()
	config := BarrierConfig{RootKeyPath: "custom/path"}
	strategy := testSoftwareStrategy(t)

	tb, err := NewTenantBarrier("tenant-1", testLogger(), base, config, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	if tb.Barrier().config.RootKeyPath != "custom/path" {
		t.Errorf("expected custom/path, got %s", tb.Barrier().config.RootKeyPath)
	}
}

func TestTenantBarrier_NewTenantBarrier_EmptyTenantID(t *testing.T) {
	base := NewMemoryBackend()
	_, err := NewTenantBarrier("", testLogger(), base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != ErrEmptyTenantID {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantBarrier_NewTenantBarrier_NilBase(t *testing.T) {
	_, err := NewTenantBarrier("tenant-1", testLogger(), nil, BarrierConfig{}, testSoftwareStrategy(t))
	if err != ErrNilStorageBackend {
		t.Errorf("expected ErrNilStorageBackend, got %v", err)
	}
}

func TestTenantBarrier_NewTenantBarrier_NoStrategy(t *testing.T) {
	base := NewMemoryBackend()
	_, err := NewTenantBarrier("tenant-1", testLogger(), base, BarrierConfig{})
	if err != ErrNoAvailableStrategy {
		t.Errorf("expected ErrNoAvailableStrategy, got %v", err)
	}
}

// ---------- TenantBarrier lifecycle tests ----------

func TestTenantBarrier_Lifecycle_InitUnsealSeal(t *testing.T) {
	base := NewMemoryBackend()
	config := BarrierConfig{}
	strategy := testSoftwareStrategy(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("tenant-password"))

	tb, err := NewTenantBarrier("tenant-A", testLogger(), base, config, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	// Initially sealed.
	if !tb.IsSealed() {
		t.Error("expected sealed initially")
	}

	// Initialize.
	if err := tb.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if tb.IsSealed() {
		t.Error("expected unsealed after Initialize")
	}

	// Seal.
	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !tb.IsSealed() {
		t.Error("expected sealed after Seal")
	}

	// Unseal.
	if err := tb.Unseal(ctx, creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if tb.IsSealed() {
		t.Error("expected unsealed after Unseal")
	}
}

func TestTenantBarrier_Status(t *testing.T) {
	base := NewMemoryBackend()
	strategy := testSoftwareStrategy(t)

	tb, err := NewTenantBarrier("tenant-B", testLogger(), base, BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	status := tb.Status()
	if !status.Sealed {
		t.Error("expected status.Sealed=true before init")
	}

	if err := tb.Initialize(context.Background(), MustCredentials([]byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status = tb.Status()
	if status.Sealed {
		t.Error("expected status.Sealed=false after init")
	}
	if status.Strategy != StrategyPassphrase {
		t.Errorf("expected strategy=%s, got %s", StrategyPassphrase, status.Strategy)
	}
}

// ---------- TenantBarrier data operations ----------

func TestTenantBarrier_DataRoundTrip(t *testing.T) {
	base := NewMemoryBackend()
	strategy := testSoftwareStrategy(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("pw"))

	tb, err := NewTenantBarrier("tenant-C", testLogger(), base, BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	if err := tb.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Put and Get.
	if err := tb.Put(ctx, "key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	val, err := tb.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "value1" {
		t.Errorf("expected value1, got %s", string(val))
	}

	// Exists.
	exists, err := tb.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected key1 to exist")
	}

	// List.
	keys, err := tb.List(ctx, "key")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 key, got %d", len(keys))
	}

	// Delete.
	if err := tb.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, err = tb.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("Exists after delete: %v", err)
	}
	if exists {
		t.Error("expected key1 to not exist after delete")
	}
}

func TestTenantBarrier_DataOperations_WhenSealed(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	strategy := testSoftwareStrategy(t)

	tb, err := NewTenantBarrier("tenant-D", testLogger(), base, BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	// All data ops should fail when sealed.
	if _, err := tb.Get(ctx, "key"); err != ErrSealed {
		t.Errorf("Get: expected ErrSealed, got %v", err)
	}
	if err := tb.Put(ctx, "key", []byte("val")); err != ErrSealed {
		t.Errorf("Put: expected ErrSealed, got %v", err)
	}
	if err := tb.Delete(ctx, "key"); err != ErrSealed {
		t.Errorf("Delete: expected ErrSealed, got %v", err)
	}
	if _, err := tb.List(ctx, ""); err != ErrSealed {
		t.Errorf("List: expected ErrSealed, got %v", err)
	}
	if _, err := tb.Exists(ctx, "key"); err != ErrSealed {
		t.Errorf("Exists: expected ErrSealed, got %v", err)
	}
}

func TestTenantBarrier_Close(t *testing.T) {
	base := NewMemoryBackend()
	strategy := testSoftwareStrategy(t)
	ctx := context.Background()

	tb, err := NewTenantBarrier("tenant-E", testLogger(), base, BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	if err := tb.Initialize(ctx, MustCredentials([]byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if err := tb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !tb.IsSealed() {
		t.Error("expected sealed after Close")
	}
}

// ---------- Cross-tenant isolation tests ----------

func TestTenantBarrier_CrossTenantIsolation(t *testing.T) {
	base := NewMemoryBackend()
	strategy := testSoftwareStrategy(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("pw"))

	tbA, err := NewTenantBarrier("tenant-A", testLogger(), base, BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier(A): %v", err)
	}
	tbB, err := NewTenantBarrier("tenant-B", testLogger(), base, BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("NewTenantBarrier(B): %v", err)
	}

	if err := tbA.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize(A): %v", err)
	}
	if err := tbB.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize(B): %v", err)
	}

	// Tenant A stores data.
	if err := tbA.Put(ctx, "secret", []byte("A-data")); err != nil {
		t.Fatalf("Put(A): %v", err)
	}

	// Tenant B stores data with the same key.
	if err := tbB.Put(ctx, "secret", []byte("B-data")); err != nil {
		t.Fatalf("Put(B): %v", err)
	}

	// Each tenant should see only its own data.
	valA, err := tbA.Get(ctx, "secret")
	if err != nil {
		t.Fatalf("Get(A): %v", err)
	}
	if string(valA) != "A-data" {
		t.Errorf("tenant A: expected A-data, got %s", string(valA))
	}

	valB, err := tbB.Get(ctx, "secret")
	if err != nil {
		t.Fatalf("Get(B): %v", err)
	}
	if string(valB) != "B-data" {
		t.Errorf("tenant B: expected B-data, got %s", string(valB))
	}

	// Tenant A listing should not show tenant B's keys.
	keysA, err := tbA.List(ctx, "secret")
	if err != nil {
		t.Fatalf("List(A): %v", err)
	}
	if len(keysA) != 1 {
		t.Errorf("tenant A: expected 1 key, got %d", len(keysA))
	}
}

// ---------- NamespacedBackend tests ----------

func TestNamespacedBackend_PutGet(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	ns := NewNamespacedBackend("ns1/", base)

	if err := ns.Put(ctx, "key", []byte("val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Verify the key is stored with the prefix.
	rawVal, err := base.Get(ctx, "ns1/key")
	if err != nil {
		t.Fatalf("base.Get(ns1/key): %v", err)
	}
	if string(rawVal) != "val" {
		t.Errorf("expected val, got %s", string(rawVal))
	}

	// Get via namespace.
	val, err := ns.Get(ctx, "key")
	if err != nil {
		t.Fatalf("ns.Get(key): %v", err)
	}
	if string(val) != "val" {
		t.Errorf("expected val, got %s", string(val))
	}
}

func TestNamespacedBackend_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	ns := NewNamespacedBackend("ns1/", base)

	_, err := ns.Get(ctx, "nonexistent")
	if err != ErrSecretNotFound {
		t.Errorf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestNamespacedBackend_List_StripPrefix(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	ns := NewNamespacedBackend("ns1/", base)

	if err := ns.Put(ctx, "data/a", []byte("1")); err != nil {
		t.Fatalf("Put(a): %v", err)
	}
	if err := ns.Put(ctx, "data/b", []byte("2")); err != nil {
		t.Fatalf("Put(b): %v", err)
	}
	if err := ns.Put(ctx, "other/c", []byte("3")); err != nil {
		t.Fatalf("Put(c): %v", err)
	}

	keys, err := ns.List(ctx, "data/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}

	// Keys should not have the namespace prefix.
	for _, k := range keys {
		ks := string(k)
		if ks != "data/a" && ks != "data/b" {
			t.Errorf("unexpected key: %s", k)
		}
	}
}

func TestNamespacedBackend_List_BaseError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	ns := NewNamespacedBackend("ns1/", fb)

	fb.listErr = errors.New("list failed")

	_, err := ns.List(ctx, "")
	if err == nil {
		t.Fatal("expected error from base List failure")
	}
}

func TestNamespacedBackend_Delete(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	ns := NewNamespacedBackend("ns1/", base)

	if err := ns.Put(ctx, "key", []byte("val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := ns.Delete(ctx, "key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, err := ns.Exists(ctx, "key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("expected key to not exist after delete")
	}
}

func TestNamespacedBackend_Exists(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	ns := NewNamespacedBackend("ns1/", base)

	exists, err := ns.Exists(ctx, "nope")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("expected false for non-existent key")
	}

	if err := ns.Put(ctx, "yes", []byte("val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err = ns.Exists(ctx, "yes")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected true for existing key")
	}
}

func TestNamespacedBackend_Close_NoOp(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	ns := NewNamespacedBackend("ns1/", base)

	if err := ns.Close(); err != nil {
		t.Errorf("Close: expected nil error, got %v", err)
	}

	// Base should still be usable after namespace close.
	if err := base.Put(ctx, "test", []byte("data")); err != nil {
		t.Errorf("base.Put after ns.Close: %v", err)
	}
}

func TestNamespacedBackend_IsolationBetweenNamespaces(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	ns1 := NewNamespacedBackend("ns1/", base)
	ns2 := NewNamespacedBackend("ns2/", base)

	if err := ns1.Put(ctx, "key", []byte("ns1-val")); err != nil {
		t.Fatalf("ns1.Put: %v", err)
	}
	if err := ns2.Put(ctx, "key", []byte("ns2-val")); err != nil {
		t.Fatalf("ns2.Put: %v", err)
	}

	val1, err := ns1.Get(ctx, "key")
	if err != nil {
		t.Fatalf("ns1.Get: %v", err)
	}
	if string(val1) != "ns1-val" {
		t.Errorf("ns1 expected ns1-val, got %s", string(val1))
	}

	val2, err := ns2.Get(ctx, "key")
	if err != nil {
		t.Fatalf("ns2.Get: %v", err)
	}
	if string(val2) != "ns2-val" {
		t.Errorf("ns2 expected ns2-val, got %s", string(val2))
	}
}

// --------------------------------------------------------------------------
// tenant_barrier.go: TenantBarrier.PutIfAbsent — 0%
// --------------------------------------------------------------------------

func TestTenantBarrier_PutIfAbsent_Sealed(t *testing.T) {
	backend := NewMemoryBackend()
	tb, err := NewTenantBarrier("t1", testLogger(), backend, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	// Barrier not yet initialized — sealed.
	_, err = tb.PutIfAbsent(context.Background(), "k", []byte("v"))
	if !errors.Is(err, ErrSealed) {
		t.Errorf("TenantBarrier.PutIfAbsent sealed: got %v, want ErrSealed", err)
	}
}

func TestTenantBarrier_PutIfAbsent_Success(t *testing.T) {
	backend := NewMemoryBackend()
	tb, err := NewTenantBarrier("t2", testLogger(), backend, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := tb.Initialize(ctx, MustCredentials([]byte("pw"))); err != nil {
		t.Fatal(err)
	}

	stored, err := tb.PutIfAbsent(ctx, "pia-tenant", []byte("val"))
	if err != nil {
		t.Fatalf("TenantBarrier.PutIfAbsent: %v", err)
	}
	if !stored {
		t.Error("expected stored=true on first call")
	}

	// Second call must not overwrite.
	stored, err = tb.PutIfAbsent(ctx, "pia-tenant", []byte("other"))
	if err != nil {
		t.Fatalf("TenantBarrier.PutIfAbsent (dup): %v", err)
	}
	if stored {
		t.Error("expected stored=false for duplicate")
	}
}
