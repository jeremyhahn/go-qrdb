// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// White-box companion to tenant_kvstore_test.go: exercises the concrete
// *tenantBarrierKVStore type, which is unexported and unreachable from the
// black-box seal_test package.

package seal

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// --------------------------------------------------------------------------
// tenant_kvstore.go: Delete, Scan, List, RegisterEntityIndexes,
// QueryIndex, ScanIndex, QueryCompoundIndex, ScanCompoundIndex — all 0%
// --------------------------------------------------------------------------

// newInitializedKVStore returns the concrete *tenantBarrierKVStore backed by
// a fresh, initialized TenantBarrier for use in tenant_kvstore coverage tests.
func newInitializedKVStore(t *testing.T) (*tenantBarrierKVStore, *TenantBarrier) {
	t.Helper()
	backend := NewMemoryBackend()
	tb, err := NewTenantBarrier("tkv", testLogger(), backend, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}
	ctx := context.Background()
	if err := tb.Initialize(ctx, Credentials{Secret: []byte("pw")}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	kv, err := NewTenantBarrierKVStore(tb)
	if err != nil {
		t.Fatalf("NewTenantBarrierKVStore: %v", err)
	}
	return kv.(*tenantBarrierKVStore), tb
}

func TestTenantBarrierKVStore_Delete(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	ctx := context.Background()

	if err := store.Put(ctx, "del-key", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := store.Delete(ctx, "del-key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Key must be gone now (Get returns nil,nil per kvstore contract).
	got, err := store.Get(ctx, "del-key")
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after Delete, got %q", got)
	}
}

func TestTenantBarrierKVStore_Scan(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	ctx := context.Background()

	_ = store.Put(ctx, "pfx/a", []byte("va"))
	_ = store.Put(ctx, "pfx/b", []byte("vb"))
	_ = store.Put(ctx, "other/c", []byte("vc"))

	result := make(map[string][]byte)
	if err := store.Scan(ctx, "pfx/", func(key string, value []byte) error {
		result[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Scan: expected 2 entries, got %d", len(result))
	}
}

func TestTenantBarrierKVStore_List(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	ctx := context.Background()

	_ = store.Put(ctx, "list/a", []byte("v1"))
	_ = store.Put(ctx, "list/b", []byte("v2"))
	_ = store.Put(ctx, "other/c", []byte("v3"))

	keys, err := store.List(ctx, "list/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("List: expected 2 keys, got %d: %v", len(keys), keys)
	}
}

func TestTenantBarrierKVStore_RegisterEntityIndexes_NoOp(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	if err := store.RegisterEntityIndexes("entity", []kvstore.EntityIndex{}); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
}

func TestTenantBarrierKVStore_QueryIndex_NoOp(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	result, err := store.QueryIndex(context.Background(), "idx", []byte("key"))
	if err != nil {
		t.Fatalf("QueryIndex: %v", err)
	}
	if result != nil {
		t.Errorf("QueryIndex no-op: expected nil result, got %v", result)
	}
}

func TestTenantBarrierKVStore_ScanIndex_NoOp(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	result, err := store.ScanIndex(context.Background(), "idx", []byte("lo"), []byte("hi"))
	if err != nil {
		t.Fatalf("ScanIndex: %v", err)
	}
	if result != nil {
		t.Errorf("ScanIndex no-op: expected nil result, got %v", result)
	}
}

func TestTenantBarrierKVStore_QueryCompoundIndex_NoOp(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	result, err := store.QueryCompoundIndex(context.Background(), "idx", [][]byte{[]byte("k")})
	if err != nil {
		t.Fatalf("QueryCompoundIndex: %v", err)
	}
	if result != nil {
		t.Errorf("QueryCompoundIndex no-op: expected nil result, got %v", result)
	}
}

func TestTenantBarrierKVStore_ScanCompoundIndex_NoOp(t *testing.T) {
	store, _ := newInitializedKVStore(t)
	result, err := store.ScanCompoundIndex(context.Background(), "idx", [][]byte{[]byte("k")}, []byte("lo"), []byte("hi"))
	if err != nil {
		t.Fatalf("ScanCompoundIndex: %v", err)
	}
	if result != nil {
		t.Errorf("ScanCompoundIndex no-op: expected nil result, got %v", result)
	}
}
