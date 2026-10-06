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
	"encoding/json"
	"testing"
	"time"
)

// putTestNodeReport writes report directly to kv at its NodeReportKey,
// standing in for what Reporter does in production (a lease-attached
// MVCC write). Catalog itself has no writer for these records; it only
// reads them.
func putTestNodeReport(t *testing.T, kv *fakeKV, report NodeReport) {
	t.Helper()
	if report.ReportedAt.IsZero() {
		report.ReportedAt = time.Now()
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal NodeReport: %v", err)
	}
	if err := kv.Put(context.Background(), NodeReportKey(report.NodeID), data); err != nil {
		t.Fatalf("Put NodeReport: %v", err)
	}
}

func TestCatalog_GetPool_LockedAndUnavailableCounted(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "vault", Provider: "dir"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	reports := []NodeReport{
		{NodeID: "node-1", Pools: map[string]PoolOnNode{"vault": {Mapped: true, Health: Health{State: StateReady}}}},
		{NodeID: "node-2", Pools: map[string]PoolOnNode{"vault": {Mapped: true, Health: Health{State: StateLocked}}}},
		{NodeID: "node-3", Pools: map[string]PoolOnNode{"vault": {Mapped: true, Health: Health{State: StateUnavailable}}}},
	}
	for _, r := range reports {
		putTestNodeReport(t, kv, r)
	}
	detail, err := cat.GetPool(ctx, "vault")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.NodeCounts != (NodeHealthCounts{Ready: 1, Locked: 1, Unavailable: 1}) {
		t.Fatalf("NodeCounts = %+v, want {Ready:1 Locked:1 Unavailable:1}", detail.NodeCounts)
	}
}

func TestCatalog_GetPool_StaleReportShowsNoReport(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	cat.leaseTTL = 10 * time.Millisecond
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-1", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Health: Health{State: StateReady}},
	}})
	time.Sleep(20 * time.Millisecond)

	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.NodeCounts.Ready != 0 {
		t.Fatalf("a stale report must not count as Ready, got NodeCounts=%+v", detail.NodeCounts)
	}
	if len(detail.NodeReports) != 1 || detail.NodeReports[0].State != NodeStateNoReport {
		t.Fatalf("NodeReports = %+v, want one no_report row", detail.NodeReports)
	}
}

func TestCatalog_GetPool_UnmappedNodeReportedSeparately(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-1", Pools: map[string]PoolOnNode{
		"fast": {Mapped: false},
	}})
	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if len(detail.NodeReports) != 1 || detail.NodeReports[0].State != NodeStateUnmapped {
		t.Fatalf("NodeReports = %+v, want one unmapped row", detail.NodeReports)
	}
	if detail.NodeCounts != (NodeHealthCounts{}) {
		t.Fatalf("an unmapped node must not be counted ready/locked/unavailable, got %+v", detail.NodeCounts)
	}
}

func TestCatalog_ListPools_AggregatesTenantCountAndHealth(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 100); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-1", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Health: Health{State: StateReady}},
	}})

	views, err := cat.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("ListPools returned %d views, want 1", len(views))
	}
	if views[0].TenantCount != 1 {
		t.Fatalf("ListPools[0].TenantCount = %d, want 1", views[0].TenantCount)
	}
	if views[0].NodeCounts.Ready != 1 {
		t.Fatalf("ListPools[0].NodeCounts.Ready = %d, want 1", views[0].NodeCounts.Ready)
	}
}

// TestCatalog_ListPools_And_GetPool_PopulateUsedBytesFromHealthReports
// proves security review finding 6's UsedBytes bullet: both ListPools and
// GetPool report the busiest StateReady node's measured usage
// (SizeBytes - FreeBytes), not the always-0 placeholder the earlier
// implementation carried; a node reporting StateLocked never contributes.
func TestCatalog_ListPools_And_GetPool_PopulateUsedBytesFromHealthReports(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 10_000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-quiet", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Usage: Usage{SizeBytes: 1000, FreeBytes: 900}, Health: Health{State: StateReady}}, // used 100
	}})
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-busy", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Usage: Usage{SizeBytes: 1000, FreeBytes: 200}, Health: Health{State: StateReady}}, // used 800
	}})
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-locked-but-busiest-reported-size", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Usage: Usage{SizeBytes: 1000, FreeBytes: 0}, Health: Health{State: StateLocked}}, // used 1000, but locked: must not count
	}})

	views, err := cat.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if views[0].UsedBytes != 800 {
		t.Fatalf("ListPools[0].UsedBytes = %d, want 800 (the busiest READY node, not the locked one)", views[0].UsedBytes)
	}

	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.UsedBytes != 800 {
		t.Fatalf("GetPool.UsedBytes = %d, want 800", detail.UsedBytes)
	}
}
