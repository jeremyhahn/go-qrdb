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
	"log/slog"
	"testing"
	"time"
)

func TestCatalog_WithCatalogLogger(t *testing.T) {
	custom := slog.Default()
	kv := newFakeKV()
	tx := newFakeTx(kv)
	cat, err := NewCatalog(kv, tx, newFakeRegistry("dir"), WithCatalogLogger(custom))
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	if cat.logger != custom {
		t.Fatal("WithCatalogLogger did not take effect")
	}

	// A nil logger is ignored rather than overwriting the default.
	cat2, err := NewCatalog(kv, tx, newFakeRegistry("dir"), WithCatalogLogger(nil))
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	if cat2.logger == nil {
		t.Fatal("WithCatalogLogger(nil) left the logger nil")
	}
}

func TestCatalog_WithLeaseTTL(t *testing.T) {
	kv := newFakeKV()
	tx := newFakeTx(kv)
	cat, err := NewCatalog(kv, tx, newFakeRegistry("dir"), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	if cat.leaseTTL != time.Minute {
		t.Fatalf("leaseTTL = %v, want 1m", cat.leaseTTL)
	}

	// A non-positive TTL is ignored rather than disabling staleness.
	cat2, err := NewCatalog(kv, tx, newFakeRegistry("dir"), WithLeaseTTL(-1))
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	if cat2.leaseTTL != DefaultLeaseTTL {
		t.Fatalf("leaseTTL after WithLeaseTTL(-1) = %v, want default %v", cat2.leaseTTL, DefaultLeaseTTL)
	}
}

func TestCatalog_GetPool_PropagatesKVFailure(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	wantErr := errors.New("backend unavailable")
	kv.failGet = wantErr
	_, err := cat.GetPool(context.Background(), "fast")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetPool error = %v, want %v", err, wantErr)
	}
}

func TestCatalog_ScanPools_SkipsCorruptRecord(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "good"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if err := kv.Put(ctx, PoolKey("bad"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	views, err := cat.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(views) != 1 || views[0].Name != "good" {
		t.Fatalf("ListPools = %+v, want only the good pool", views)
	}
}

func TestCatalog_ScanNodeReports_SkipsCorruptRecord(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if err := kv.Put(ctx, NodeReportKey("node-x"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if len(detail.NodeReports) != 0 {
		t.Fatalf("GetPool.NodeReports = %+v, want the corrupt report skipped", detail.NodeReports)
	}
}

func TestCatalog_TenantCount_SkipsCorruptAssignment(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if err := kv.Put(ctx, AssignmentKey("ghost"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.TenantCount != 0 {
		t.Fatalf("TenantCount = %d, want 0 (corrupt assignment skipped)", detail.TenantCount)
	}
}

func TestCatalog_ListAssignments_SkipsCorruptRecord(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	seedAssignments(t, cat, "fast", 1)
	if err := kv.Put(context.Background(), AssignmentKey("ghost"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	page, err := cat.ListAssignments(context.Background(), "fast", "", 10)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(page.Assignments) != 1 {
		t.Fatalf("ListAssignments = %+v, want the corrupt record skipped", page.Assignments)
	}
}

func TestCatalog_Resolve_CorruptRecord(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	if err := kv.Put(context.Background(), AssignmentKey("acme"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err := cat.Resolve(context.Background(), "acme")
	var target *CorruptRecordError
	if !errors.As(err, &target) {
		t.Fatalf("Resolve(corrupt) error = %v, want *CorruptRecordError", err)
	}
}

func TestCatalog_Release_CorruptAssignmentRecord(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	if err := kv.Put(context.Background(), AssignmentKey("acme"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err := cat.Release(context.Background(), "acme")
	var target *CorruptRecordError
	if !errors.As(err, &target) {
		t.Fatalf("Release(corrupt assignment) error = %v, want *CorruptRecordError", err)
	}
}

func TestCatalog_Release_PoolReadFailurePropagates(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 10); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := cat.DeletePool(ctx, "fast"); err == nil {
		t.Fatal("DeletePool should refuse while assigned (test setup check)")
	}
	// Force the pool record itself to vanish without releasing the
	// assignment, simulating corruption, then confirm Release surfaces the
	// pool lookup failure rather than silently dropping the reservation.
	raw, err := cat.kv.Get(ctx, PoolKey("fast"))
	if err != nil || raw == nil {
		t.Fatalf("expected the fast pool record to exist, got raw=%v err=%v", raw, err)
	}
	if _, err := cat.kv.CompareAndDelete(ctx, PoolKey("fast"), raw); err != nil {
		t.Fatalf("CompareAndDelete: %v", err)
	}

	err = cat.Release(ctx, "acme")
	var target *PoolNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("Release(pool missing) error = %v, want *PoolNotFoundError", err)
	}
}

func TestCatalog_UpdatePool_InvalidRequests(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	negative := int64(-1)
	weak := 0.5

	if _, err := cat.UpdatePool(ctx, "fast", UpdatePoolRequest{CapacityBytes: &negative}); err == nil {
		t.Fatal("UpdatePool(negative capacity) should fail")
	}
	if _, err := cat.UpdatePool(ctx, "fast", UpdatePoolRequest{Overcommit: &weak}); err == nil {
		t.Fatal("UpdatePool(overcommit < 1.0) should fail")
	}
}

func TestCatalog_ListPools_PropagatesScanFailure(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	kv.failScan = errors.New("backend unavailable")
	if _, err := cat.ListPools(context.Background()); err == nil {
		t.Fatal("ListPools should propagate a Scan failure")
	}
}

func TestCatalog_GetPool_PropagatesNodeReportScanFailure(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	kv.failScan = errors.New("backend unavailable")
	if _, err := cat.GetPool(ctx, "fast"); err == nil {
		t.Fatal("GetPool should propagate a node-report Scan failure")
	}
}

func TestCatalog_DeletePool_PropagatesKVFailure(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	wantErr := errors.New("backend unavailable")
	kv.failGet = wantErr
	err := cat.DeletePool(context.Background(), "fast")
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeletePool error = %v, want %v", err, wantErr)
	}
}

func TestCatalog_Resolve_PropagatesKVFailure(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	wantErr := errors.New("backend unavailable")
	kv.failGet = wantErr
	_, err := cat.Resolve(context.Background(), "acme")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Resolve error = %v, want %v", err, wantErr)
	}
}

func TestIsAssignmentNotFound(t *testing.T) {
	var target *AssignmentNotFoundError
	if isAssignmentNotFound(errors.New("some other error"), &target) {
		t.Fatal("isAssignmentNotFound matched an unrelated error")
	}
	if target != nil {
		t.Fatal("isAssignmentNotFound set target on a non-match")
	}

	want := &AssignmentNotFoundError{TenantID: "acme"}
	if !isAssignmentNotFound(want, &target) {
		t.Fatal("isAssignmentNotFound did not match *AssignmentNotFoundError")
	}
	if target != want {
		t.Fatal("isAssignmentNotFound did not assign target")
	}
}
