// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"errors"
	"testing"
)

func TestNewCatalog_RequiresDependencies(t *testing.T) {
	kv := newFakeKV()
	tx := newFakeTx(kv)
	registry := newFakeRegistry("dir")

	cases := []struct {
		name string
		kv   KVServicer
		tx   TransactionServicer
		reg  Registry
	}{
		{"nil kv", nil, tx, registry},
		{"nil tx", kv, nil, registry},
		{"nil registry", kv, tx, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewCatalog(c.kv, c.tx, c.reg); err == nil {
				t.Fatal("NewCatalog should refuse a nil dependency")
			}
		})
	}
}

func TestCatalog_CreatePool_Success(t *testing.T) {
	cat, _, _ := newTestCatalog()
	pool, err := cat.CreatePool(context.Background(), CreatePoolRequest{Name: "fast", Description: "NVMe tier"})
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if pool.Provider != "dir" {
		t.Fatalf("CreatePool defaulted Provider = %q, want dir", pool.Provider)
	}
	if pool.Overcommit != DefaultOvercommit {
		t.Fatalf("CreatePool defaulted Overcommit = %v, want %v", pool.Overcommit, DefaultOvercommit)
	}
	if pool.CreatedAt.IsZero() || pool.UpdatedAt.IsZero() {
		t.Fatal("CreatePool left CreatedAt/UpdatedAt zero")
	}
}

func TestCatalog_CreatePool_DuplicateRefused(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	_, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"})
	var target *PoolAlreadyExistsError
	if !errors.As(err, &target) {
		t.Fatalf("CreatePool(duplicate) error = %v, want *PoolAlreadyExistsError", err)
	}
}

func TestCatalog_CreatePool_InvalidRequests(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()

	cases := []struct {
		name string
		req  CreatePoolRequest
	}{
		{"empty name", CreatePoolRequest{Name: ""}},
		{"unregistered provider", CreatePoolRequest{Name: "vault", Provider: "luks2"}},
		{"negative capacity", CreatePoolRequest{Name: "fast", CapacityBytes: -1}},
		{"overcommit below 1.0", CreatePoolRequest{Name: "fast", Overcommit: 0.5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := cat.CreatePool(ctx, c.req); err == nil {
				t.Fatal("CreatePool should have refused the request")
			}
		})
	}
}

func TestCatalog_GetPool_NotFound(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.GetPool(context.Background(), "ghost")
	var target *PoolNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("GetPool(unknown) error = %v, want *PoolNotFoundError", err)
	}
}

func TestCatalog_GetPool_ReflectsCreatedPool(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", Description: "NVMe"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.Name != "fast" || detail.Description != "NVMe" {
		t.Fatalf("GetPool returned unexpected pool: %+v", detail.Pool)
	}
	if detail.TenantCount != 0 {
		t.Fatalf("GetPool.TenantCount = %d, want 0", detail.TenantCount)
	}
}

func TestCatalog_ListPools_Empty(t *testing.T) {
	cat, _, _ := newTestCatalog()
	views, err := cat.ListPools(context.Background())
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("ListPools on an empty catalog = %v, want empty", views)
	}
}

func TestCatalog_ListPools_SortedByName(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	for _, name := range []string{"fast", "bulk", "vault"} {
		if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: name}); err != nil {
			t.Fatalf("CreatePool(%s): %v", name, err)
		}
	}
	views, err := cat.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	want := []string{"bulk", "fast", "vault"}
	if len(views) != len(want) {
		t.Fatalf("ListPools returned %d pools, want %d", len(views), len(want))
	}
	for i, name := range want {
		if views[i].Name != name {
			t.Fatalf("ListPools[%d].Name = %q, want %q", i, views[i].Name, name)
		}
	}
}

func TestCatalog_UpdatePool_ChangesDescriptionAndCapacity(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	newDesc := "rack A"
	newCap := int64(1 << 40)
	updated, err := cat.UpdatePool(ctx, "fast", UpdatePoolRequest{Description: &newDesc, CapacityBytes: &newCap})
	if err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if updated.Description != newDesc || updated.CapacityBytes != newCap {
		t.Fatalf("UpdatePool did not apply changes: %+v", updated)
	}
}

func TestCatalog_UpdatePool_CapacityBelowReservedRefused(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 800); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	tooSmall := int64(100)
	_, err := cat.UpdatePool(ctx, "fast", UpdatePoolRequest{CapacityBytes: &tooSmall})
	var target *PoolCapacityExceededError
	if !errors.As(err, &target) {
		t.Fatalf("UpdatePool(below reserved) error = %v, want *PoolCapacityExceededError", err)
	}
}

func TestCatalog_UpdatePool_NotFound(t *testing.T) {
	cat, _, _ := newTestCatalog()
	desc := "x"
	_, err := cat.UpdatePool(context.Background(), "ghost", UpdatePoolRequest{Description: &desc})
	var target *PoolNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("UpdatePool(unknown) error = %v, want *PoolNotFoundError", err)
	}
}

func TestCatalog_DeletePool_Success(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if err := cat.DeletePool(ctx, "fast"); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	_, err := cat.GetPool(ctx, "fast")
	var target *PoolNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("GetPool after delete error = %v, want *PoolNotFoundError", err)
	}
}

func TestCatalog_DeletePool_RefusedWhileAssigned(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 10); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	err := cat.DeletePool(ctx, "fast")
	var target *PoolInUseError
	if !errors.As(err, &target) {
		t.Fatalf("DeletePool(in use) error = %v, want *PoolInUseError", err)
	}
	if len(target.Tenants) != 1 || target.Tenants[0] != "acme" {
		t.Fatalf("PoolInUseError.Tenants = %v, want [acme]", target.Tenants)
	}
}

// TestCatalog_DeletePool_RefusesWhileUnparseableAssignmentExists proves
// invariant 9: DeletePool fails closed rather than silently proceeding
// when an assignment record exists that cannot even be parsed enough to
// prove it does not name the pool being deleted. ListAssignments' own
// best-effort scan (used for display) would skip this record with only a
// logged warning and report the pool as unused -- DeletePool must not
// trust that for a destructive operation.
func TestCatalog_DeletePool_RefusesWhileUnparseableAssignmentExists(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if err := kv.Put(ctx, AssignmentKey("corrupt"), []byte("not valid json")); err != nil {
		t.Fatalf("seed corrupt record: %v", err)
	}

	err := cat.DeletePool(ctx, "fast")
	var target *PoolUnparseableAssignmentError
	if !errors.As(err, &target) {
		t.Fatalf("DeletePool(unparseable assignment present) error = %v, want *PoolUnparseableAssignmentError", err)
	}

	// The pool must still exist: the delete was refused, not partially
	// applied.
	if _, err := cat.GetPool(ctx, "fast"); err != nil {
		t.Fatalf("GetPool after refused delete: %v", err)
	}
}

func TestCatalog_MoveTenant_NotImplemented(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.MoveTenant(context.Background(), "acme", "bulk")
	var target *MoveNotImplementedError
	if !errors.As(err, &target) {
		t.Fatalf("MoveTenant error = %v, want *MoveNotImplementedError", err)
	}
}

func TestCatalog_GetMove_NotImplemented(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.GetMove(context.Background(), "acme")
	var target *MoveNotImplementedError
	if !errors.As(err, &target) {
		t.Fatalf("GetMove error = %v, want *MoveNotImplementedError", err)
	}
}
