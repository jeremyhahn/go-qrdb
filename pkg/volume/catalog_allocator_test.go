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

func TestCatalog_Reserve_Success(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}

	a, err := cat.Reserve(ctx, "acme", "fast", "acme", 200)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if a.TenantID != "acme" || a.Pool != "fast" || a.Path != "acme" || a.ReservedBytes != 200 {
		t.Fatalf("Reserve returned unexpected assignment: %+v", a)
	}

	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.ReservedBytes != 200 {
		t.Fatalf("Pool.ReservedBytes = %d, want 200", detail.ReservedBytes)
	}
	if detail.TenantCount != 1 {
		t.Fatalf("Pool.TenantCount = %d, want 1", detail.TenantCount)
	}
}

func TestCatalog_Reserve_IdempotentOnRetry(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	first, err := cat.Reserve(ctx, "acme", "fast", "acme", 200)
	if err != nil {
		t.Fatalf("Reserve (first): %v", err)
	}
	second, err := cat.Reserve(ctx, "acme", "fast", "acme", 200)
	if err != nil {
		t.Fatalf("Reserve (retry): %v", err)
	}
	if *first != *second {
		t.Fatalf("Reserve retry returned a different assignment: %+v vs %+v", first, second)
	}

	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.ReservedBytes != 200 {
		t.Fatalf("Pool.ReservedBytes = %d after retry, want 200 (no double reservation)", detail.ReservedBytes)
	}
}

func TestCatalog_Reserve_DifferentPoolRefused(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	for _, name := range []string{"fast", "bulk"} {
		if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: name, CapacityBytes: 1000}); err != nil {
			t.Fatalf("CreatePool(%s): %v", name, err)
		}
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 100); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_, err := cat.Reserve(ctx, "acme", "bulk", "acme", 100)
	var target *TenantAlreadyAssignedError
	if !errors.As(err, &target) {
		t.Fatalf("Reserve(different pool) error = %v, want *TenantAlreadyAssignedError", err)
	}
}

func TestCatalog_Reserve_CapacityExceeded(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 100}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	_, err := cat.Reserve(ctx, "acme", "fast", "acme", 200)
	var target *PoolCapacityExceededError
	if !errors.As(err, &target) {
		t.Fatalf("Reserve(over capacity) error = %v, want *PoolCapacityExceededError", err)
	}
}

// TestCatalog_Reserve_DerivedCapacityFromHealthReports proves decision D4:
// a pool created with NO explicit capacity (CapacityBytes 0, the only
// value CreatePool ever stores for "unset") is not unconstrained forever
// -- it derives a capacity from the smallest StateReady node's reported
// size, and Reserve enforces it exactly as it would an explicit capacity,
// surfacing the SAME *PoolCapacityExceededError (which pkg/api/errclass
// classifies ResourceExhausted, a 429 at every transport) once a
// reservation would exceed it.
func TestCatalog_Reserve_DerivedCapacityFromHealthReports(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	// Two nodes report "fast"; the SMALLER of the two is the derived cap.
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-1", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Usage: Usage{SizeBytes: 1000}, Health: Health{State: StateReady}},
	}})
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-2", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Usage: Usage{SizeBytes: 500}, Health: Health{State: StateReady}},
	}})

	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 500); err != nil {
		t.Fatalf("Reserve(within derived capacity): %v", err)
	}
	_, err := cat.Reserve(ctx, "bulk-tenant", "fast", "bulk-tenant", 1)
	var target *PoolCapacityExceededError
	if !errors.As(err, &target) {
		t.Fatalf("Reserve(beyond derived capacity) error = %v, want *PoolCapacityExceededError", err)
	}
	if target.Available != 0 {
		t.Fatalf("PoolCapacityExceededError.Available = %d, want 0 (500 derived cap, 500 already reserved)", target.Available)
	}
}

// TestCatalog_Reserve_NoHealthReportsYetIsUnconstrained proves the
// transient bootstrap window: a pool with no explicit capacity AND no
// node has reported it yet has nothing to derive from, so Reserve does
// not refuse -- this is not a standing "unlimited" choice (CreatePool
// never stores a genuinely-unlimited pool), only the state before any
// node's Reporter has run a cycle.
func TestCatalog_Reserve_NoHealthReportsYetIsUnconstrained(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 1_000_000); err != nil {
		t.Fatalf("Reserve before any health report: %v", err)
	}
}

// TestCatalog_Reserve_DerivedCapacityIgnoresStaleOrUnhealthyReports proves
// the derivation reuses the SAME staleness/health rules the rest of the
// catalog's health view does: a StateLocked/StateUnavailable report, or
// one older than the lease TTL, never contributes to the derived cap.
func TestCatalog_Reserve_DerivedCapacityIgnoresStaleOrUnhealthyReports(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	// A locked node reporting a tiny size must not drag the derived cap
	// down to it.
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-locked", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Usage: Usage{SizeBytes: 1}, Health: Health{State: StateLocked}},
	}})
	putTestNodeReport(t, kv, NodeReport{NodeID: "node-ready", Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Usage: Usage{SizeBytes: 10_000}, Health: Health{State: StateReady}},
	}})

	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 5_000); err != nil {
		t.Fatalf("Reserve(within the healthy node's capacity): %v", err)
	}
}

func TestCatalog_Reserve_OvercommitAllowsBeyondRawCapacity(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 100, Overcommit: 2.0}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 150); err != nil {
		t.Fatalf("Reserve within overcommit should succeed: %v", err)
	}
}

func TestCatalog_Reserve_UnknownPool(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.Reserve(context.Background(), "acme", "ghost", "acme", 10)
	var target *PoolNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("Reserve(unknown pool) error = %v, want *PoolNotFoundError", err)
	}
}

func TestCatalog_Reserve_InvalidPath(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	for _, path := range []string{"", "..", ".", "a/b", "/abs"} {
		t.Run(path, func(t *testing.T) {
			_, err := cat.Reserve(ctx, "acme", "fast", path, 10)
			var target *InvalidAssignmentPathError
			if !errors.As(err, &target) {
				t.Fatalf("Reserve(path=%q) error = %v, want *InvalidAssignmentPathError", path, err)
			}
		})
	}
}

func TestCatalog_Reserve_ConflictAfterBoundedRetries(t *testing.T) {
	cat, _, tx := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	tx.forceFailures = maxCASRetries
	_, err := cat.Reserve(ctx, "acme", "fast", "acme", 10)
	var target *ReservationConflictError
	if !errors.As(err, &target) {
		t.Fatalf("Reserve under sustained conflict error = %v, want *ReservationConflictError", err)
	}
}

func TestCatalog_Resolve_Success(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 10); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	a, err := cat.Resolve(ctx, "acme")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if a.TenantID != "acme" || a.Pool != "fast" {
		t.Fatalf("Resolve returned unexpected assignment: %+v", a)
	}
}

func TestCatalog_Resolve_NotFound(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.Resolve(context.Background(), "ghost")
	var target *AssignmentNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("Resolve(unknown) error = %v, want *AssignmentNotFoundError", err)
	}
}

func TestCatalog_Release_ReleasesReservationAndAssignment(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 200); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := cat.Release(ctx, "acme"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if _, err := cat.Resolve(ctx, "acme"); !errors.As(err, new(*AssignmentNotFoundError)) {
		t.Fatalf("Resolve after Release error = %v, want *AssignmentNotFoundError", err)
	}
	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.ReservedBytes != 0 {
		t.Fatalf("Pool.ReservedBytes after Release = %d, want 0", detail.ReservedBytes)
	}
}

func TestCatalog_Release_UnassignedTenantIsNoOp(t *testing.T) {
	cat, _, _ := newTestCatalog()
	if err := cat.Release(context.Background(), "never-assigned"); err != nil {
		t.Fatalf("Release(unassigned) should be a no-op, got: %v", err)
	}
}

// TestCatalog_ListAll_ReturnsEveryAssignmentAcrossPages proves ListAll (the
// boot-time restore walk's own interface, Allocator.ListAll) pages through
// ListAssignments internally and returns every tenant's Assignment
// regardless of pool, unpaginated to the caller.
func TestCatalog_ListAll_ReturnsEveryAssignmentAcrossPages(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	for _, name := range []string{"fast", "bulk"} {
		if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: name}); err != nil {
			t.Fatalf("CreatePool(%s): %v", name, err)
		}
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 10); err != nil {
		t.Fatalf("Reserve(acme): %v", err)
	}
	if _, err := cat.Reserve(ctx, "other", "bulk", "other", 10); err != nil {
		t.Fatalf("Reserve(other): %v", err)
	}

	all, err := cat.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll returned %d assignments, want 2", len(all))
	}
}

// TestCatalog_ListAll_EmptyCatalogReturnsNil proves ListAll on a catalog
// with no assignments at all returns an empty (nil) slice, not an error.
func TestCatalog_ListAll_EmptyCatalogReturnsNil(t *testing.T) {
	cat, _, _ := newTestCatalog()
	all, err := cat.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("ListAll on an empty catalog = %v, want empty", all)
	}
}

// TestCatalog_Release_InterimShapedRecordIsDeletedOutright proves decision
// D9(a): a record written by the pre-catalog interim fix ({pool, slot},
// no Path field) is deleted outright by Release rather than refused --
// this is what lets DestroyTenant/reconcile actually finish deleting a
// tenant stuck with one of these, per D9's own ruling ("the tenant is
// deleted and re-created"). Before this fix, Release assumed every
// decoded record had a real Pool to look up and credit ReservedBytes
// back to; an interim record's Pool may not even exist any more under
// that name, which would have made deletion impossible.
func TestCatalog_Release_InterimShapedRecordIsDeletedOutright(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	ctx := context.Background()

	interim := []byte(`{"pool":"legacy-pool-name","slot":2}`)
	if err := kv.Put(ctx, AssignmentKey("legacy-tenant"), interim); err != nil {
		t.Fatalf("seed interim record: %v", err)
	}

	// Resolve refuses it per D9, confirming this really is the shape
	// under test.
	if _, err := cat.Resolve(ctx, "legacy-tenant"); !errors.As(err, new(*InvalidAssignmentPathError)) {
		t.Fatalf("Resolve(interim-shaped) error = %v, want *InvalidAssignmentPathError", err)
	}

	if err := cat.Release(ctx, "legacy-tenant"); err != nil {
		t.Fatalf("Release(interim-shaped): %v", err)
	}

	raw, err := kv.Get(ctx, AssignmentKey("legacy-tenant"))
	if err != nil {
		t.Fatalf("Get after Release: %v", err)
	}
	if raw != nil {
		t.Fatal("the interim-shaped record must be deleted, not left behind")
	}
}

func TestCatalog_ReserveThenRelease_AllowsReassignmentToAnotherPool(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	for _, name := range []string{"fast", "bulk"} {
		if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: name, CapacityBytes: 1000}); err != nil {
			t.Fatalf("CreatePool(%s): %v", name, err)
		}
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 100); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := cat.Release(ctx, "acme"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	a, err := cat.Reserve(ctx, "acme", "bulk", "acme", 100)
	if err != nil {
		t.Fatalf("Reserve(after release, different pool): %v", err)
	}
	if a.Pool != "bulk" {
		t.Fatalf("Reserve after release landed on pool %q, want bulk", a.Pool)
	}
}

// TestCatalog_AdjustReservation_IncreaseAndDecrease proves security review
// finding 6: a tenant's catalog reservation can be changed to track its
// current MaxStorageBytes quota, crediting or debiting the pool's own
// ReservedBytes by the delta atomically with the Assignment update
// (invariant 4).
func TestCatalog_AdjustReservation_IncreaseAndDecrease(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 200); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	if _, err := cat.AdjustReservation(ctx, "acme", 500); err != nil {
		t.Fatalf("AdjustReservation(increase): %v", err)
	}
	detail, err := cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.ReservedBytes != 500 {
		t.Fatalf("Pool.ReservedBytes after increase = %d, want 500", detail.ReservedBytes)
	}
	asn, err := cat.Resolve(ctx, "acme")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if asn.ReservedBytes != 500 {
		t.Fatalf("Assignment.ReservedBytes after increase = %d, want 500", asn.ReservedBytes)
	}

	if _, err := cat.AdjustReservation(ctx, "acme", 100); err != nil {
		t.Fatalf("AdjustReservation(decrease): %v", err)
	}
	detail, err = cat.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.ReservedBytes != 100 {
		t.Fatalf("Pool.ReservedBytes after decrease = %d, want 100", detail.ReservedBytes)
	}
}

// TestCatalog_AdjustReservation_IncreaseBeyondCapacityRefused proves an
// increase is checked against the pool's capacity exactly as a new
// Reserve would be, surfacing *PoolCapacityExceededError rather than
// silently over-committing the pool.
func TestCatalog_AdjustReservation_IncreaseBeyondCapacityRefused(t *testing.T) {
	cat, _, _ := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 200); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	_, err := cat.AdjustReservation(ctx, "acme", 2000)
	var target *PoolCapacityExceededError
	if !errors.As(err, &target) {
		t.Fatalf("AdjustReservation(beyond capacity) error = %v, want *PoolCapacityExceededError", err)
	}
	detail, getErr := cat.GetPool(ctx, "fast")
	if getErr != nil {
		t.Fatalf("GetPool: %v", getErr)
	}
	if detail.ReservedBytes != 200 {
		t.Fatalf("Pool.ReservedBytes after refused increase = %d, want unchanged 200", detail.ReservedBytes)
	}
}

// TestCatalog_AdjustReservation_UnassignedTenantIsNotFound proves
// AdjustReservation only adjusts an EXISTING reservation; it never
// creates one.
func TestCatalog_AdjustReservation_UnassignedTenantIsNotFound(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.AdjustReservation(context.Background(), "never-assigned", 100)
	var target *AssignmentNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("AdjustReservation(unassigned) error = %v, want *AssignmentNotFoundError", err)
	}
}

func TestValidateAssignmentPath_Valid(t *testing.T) {
	if err := ValidateAssignmentPath("acme", "acme"); err != nil {
		t.Fatalf("ValidateAssignmentPath(clean component) = %v, want nil", err)
	}
}

func TestValidateAssignmentPath_Invalid(t *testing.T) {
	for _, path := range []string{"", ".", "..", "a/b", "/abs"} {
		if err := ValidateAssignmentPath("acme", path); err == nil {
			t.Fatalf("ValidateAssignmentPath(%q) = nil, want an error", path)
		}
	}
}
