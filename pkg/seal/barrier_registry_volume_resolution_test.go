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
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fakeAllocator is a minimal volume.Allocator double for BarrierRegistry's
// new resolution seam.
type fakeAllocator struct {
	assignments map[string]volume.Assignment
}

func newFakeAllocator() *fakeAllocator {
	return &fakeAllocator{assignments: make(map[string]volume.Assignment)}
}

func (a *fakeAllocator) Reserve(_ context.Context, tenantID, pool, path string, reserveBytes int64) (*volume.Assignment, error) {
	asn := volume.Assignment{TenantID: tenantID, Pool: pool, Path: path, ReservedBytes: reserveBytes}
	a.assignments[tenantID] = asn
	return &asn, nil
}

func (a *fakeAllocator) Resolve(_ context.Context, tenantID string) (*volume.Assignment, error) {
	asn, ok := a.assignments[tenantID]
	if !ok {
		return nil, &volume.AssignmentNotFoundError{TenantID: tenantID}
	}
	return &asn, nil
}

func (a *fakeAllocator) Release(_ context.Context, tenantID string) error {
	delete(a.assignments, tenantID)
	return nil
}

func (a *fakeAllocator) AdjustReservation(_ context.Context, tenantID string, newBytes int64) (*volume.Assignment, error) {
	asn, ok := a.assignments[tenantID]
	if !ok {
		return nil, &volume.AssignmentNotFoundError{TenantID: tenantID}
	}
	asn.ReservedBytes = newBytes
	a.assignments[tenantID] = asn
	return &asn, nil
}

func (a *fakeAllocator) ListAll(_ context.Context) ([]volume.Assignment, error) {
	out := make([]volume.Assignment, 0, len(a.assignments))
	for _, asn := range a.assignments {
		out = append(out, asn)
	}
	return out, nil
}

// fakeVolumeResolver is a minimal VolumeResolver double.
type fakeVolumeResolver struct {
	roots map[string]string
}

func newFakeVolumeResolver() *fakeVolumeResolver {
	return &fakeVolumeResolver{roots: make(map[string]string)}
}

func (f *fakeVolumeResolver) Root(_ context.Context, pool string) (string, error) {
	root, ok := f.roots[pool]
	if !ok {
		return "", &volume.PoolUnavailableError{Pool: pool}
	}
	return root, nil
}

func (f *fakeVolumeResolver) Pools() []string {
	names := make([]string, 0, len(f.roots))
	for name := range f.roots {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestBarrierRegistry_RequireInsideLocalPool_ResolvesThroughAllocatorAndResolver(t *testing.T) {
	allocator := newFakeAllocator()
	resolver := newFakeVolumeResolver()
	resolver.roots["fast"] = t.TempDir()
	if _, err := allocator.Reserve(context.Background(), "acme", "fast", "acme", 10); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(allocator), WithVolumeResolver(resolver))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	got, err := r.RequireInsideLocalPool(context.Background(), "acme")
	if err != nil {
		t.Fatalf("RequireInsideLocalPool: %v", err)
	}
	want := resolver.roots["fast"] + "/acme"
	if got != want {
		t.Fatalf("RequireInsideLocalPool = %q, want %q", got, want)
	}
}

func TestBarrierRegistry_RequireInsideLocalPool_NoAssignment(t *testing.T) {
	allocator := newFakeAllocator()
	resolver := newFakeVolumeResolver()
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(allocator), WithVolumeResolver(resolver))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	_, err = r.RequireInsideLocalPool(context.Background(), "ghost")
	var target *volume.AssignmentNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("RequireInsideLocalPool(unassigned) error = %v, want *volume.AssignmentNotFoundError", err)
	}
}

func TestBarrierRegistry_RequireInsideLocalPool_PoolUnavailable(t *testing.T) {
	allocator := newFakeAllocator()
	resolver := newFakeVolumeResolver() // "fast" never opened
	if _, err := allocator.Reserve(context.Background(), "acme", "fast", "acme", 10); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(allocator), WithVolumeResolver(resolver))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	_, err = r.RequireInsideLocalPool(context.Background(), "acme")
	var target *volume.PoolUnavailableError
	if !errors.As(err, &target) {
		t.Fatalf("RequireInsideLocalPool(pool unavailable) error = %v, want *volume.PoolUnavailableError", err)
	}
}

func TestBarrierRegistry_RequireInsideLocalPool_NotConfigured(t *testing.T) {
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := r.RequireInsideLocalPool(context.Background(), "acme"); !errors.Is(err, ErrPoolNotConfigured) {
		t.Fatalf("RequireInsideLocalPool(unconfigured) error = %v, want ErrPoolNotConfigured", err)
	}
}

// TestBarrierRegistry_PlacementDataRoot_ResolvesWithoutAnAssignment proves a
// placement the catalog no longer holds still resolves to this node's
// directory, under the same containment checks RequireInsideLocalPool makes.
func TestBarrierRegistry_PlacementDataRoot_ResolvesWithoutAnAssignment(t *testing.T) {
	resolver := newFakeVolumeResolver()
	resolver.roots["fast"] = t.TempDir()
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(newFakeAllocator()), WithVolumeResolver(resolver))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	ctx := context.Background()

	got, err := r.PlacementDataRoot(ctx, "acme", "fast", "acme")
	if err != nil {
		t.Fatalf("PlacementDataRoot: %v", err)
	}
	if want := resolver.roots["fast"] + "/acme"; got != want {
		t.Fatalf("PlacementDataRoot = %q, want %q", got, want)
	}
	if _, err := r.PlacementDataRoot(ctx, "acme", "fast", "../escape"); !errors.Is(err, ErrTenantPathEscapesPool) {
		t.Fatalf("PlacementDataRoot(traversal) error = %v, want ErrTenantPathEscapesPool", err)
	}
	if _, err := r.PlacementDataRoot(ctx, "acme", "missing", "acme"); err == nil {
		t.Fatal("PlacementDataRoot(unknown pool) succeeded")
	}
}

// TestBarrierRegistry_PlacementDataRoot_Refusals covers the empty id and a
// node with no volume pools.
func TestBarrierRegistry_PlacementDataRoot_Refusals(t *testing.T) {
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := r.PlacementDataRoot(ctx, "", "fast", "acme"); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("PlacementDataRoot(empty id) error = %v, want ErrEmptyTenantID", err)
	}
	if _, err := r.PlacementDataRoot(ctx, "acme", "fast", "acme"); !errors.Is(err, ErrPoolNotConfigured) {
		t.Fatalf("PlacementDataRoot(unconfigured) error = %v, want ErrPoolNotConfigured", err)
	}
}

// TestBarrierRegistry_RequireInsideLocalPool_RefusesTraversal is the
// read-time defense invariant 2 asks for: even though volume.Reserve
// validates a Path at write time (ValidateAssignmentPath, tested in
// pkg/volume), RequireInsideLocalPool re-checks containment on every use so
// a corrupt or pre-catalog record can never resolve outside the pool root.
// fakeAllocator.Reserve, unlike the real Catalog, performs no validation of
// its own, which is what lets this test inject a traversal Path directly.
func TestBarrierRegistry_RequireInsideLocalPool_RefusesTraversal(t *testing.T) {
	cases := []string{"../../etc", "..", ".", "", "acme/prod", "/etc/passwd", "../pool1/victim"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			allocator := newFakeAllocator()
			resolver := newFakeVolumeResolver()
			resolver.roots["fast"] = t.TempDir()
			if _, err := allocator.Reserve(context.Background(), "acme", "fast", path, 0); err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
				WithVolumeAllocator(allocator), WithVolumeResolver(resolver))
			if err != nil {
				t.Fatalf("NewBarrierRegistry: %v", err)
			}
			if _, err := r.RequireInsideLocalPool(context.Background(), "acme"); !errors.Is(err, ErrTenantPathEscapesPool) {
				t.Fatalf("RequireInsideLocalPool(path=%q) = %v, want ErrTenantPathEscapesPool", path, err)
			}
		})
	}
}

func TestWithVolumeAllocator_NilIsANoOp(t *testing.T) {
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t), WithVolumeAllocator(nil))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := r.RequireInsideLocalPool(context.Background(), "acme"); !errors.Is(err, ErrPoolNotConfigured) {
		t.Fatalf("expected ErrPoolNotConfigured after WithVolumeAllocator(nil), got %v", err)
	}
}

func TestWithVolumeResolver_NilIsANoOp(t *testing.T) {
	allocator := newFakeAllocator()
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(allocator), WithVolumeResolver(nil))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := r.RequireInsideLocalPool(context.Background(), "acme"); !errors.Is(err, ErrPoolNotConfigured) {
		t.Fatalf("expected ErrPoolNotConfigured after WithVolumeResolver(nil), got %v", err)
	}
}
