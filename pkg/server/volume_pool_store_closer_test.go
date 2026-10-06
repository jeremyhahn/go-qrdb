// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fakeAllocatorForStoreCloser is a minimal volume.Allocator double:
// ListAll answers from a fixed slice (or a fixed error); every other
// method panics (unused by CloseTenantStoresForPool).
type fakeAllocatorForStoreCloser struct {
	assignments []volume.Assignment
	listErr     error
}

func (f *fakeAllocatorForStoreCloser) ListAll(context.Context) ([]volume.Assignment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.assignments, nil
}
func (f *fakeAllocatorForStoreCloser) Reserve(context.Context, string, string, string, int64) (*volume.Assignment, error) {
	panic("unused")
}
func (f *fakeAllocatorForStoreCloser) Resolve(context.Context, string) (*volume.Assignment, error) {
	panic("unused")
}
func (f *fakeAllocatorForStoreCloser) Release(context.Context, string) error { panic("unused") }
func (f *fakeAllocatorForStoreCloser) AdjustReservation(context.Context, string, int64) (*volume.Assignment, error) {
	panic("unused")
}

// TestCloseTenantStoresForPool_ClosesOnlyAssignedTenants proves the pool
// filter: of two tenants with a live per-tenant Host, only the one the
// allocator reports assigned to the target pool is closed; the other's
// Host is left running.
func TestCloseTenantStoresForPool_ClosesOnlyAssignedTenants(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "t1")
	registerVolumeTenant(t, reg, "t2")
	ctx := context.Background()

	source := &lookupDataRootSource{registry: reg}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	if err := hostReg.Attach(ctx, "t1"); err != nil {
		t.Fatalf("Attach(t1): %v", err)
	}
	if err := hostReg.Attach(ctx, "t2"); err != nil {
		t.Fatalf("Attach(t2): %v", err)
	}
	if !hostReg.Started("t1") || !hostReg.Started("t2") {
		t.Fatalf("both tenants must have a live Host before the test proceeds")
	}

	// Both tenants are actually registered under the fixture's single real
	// pool ("default"), but CloseTenantStoresForPool decides purely from
	// the allocator's reported Assignment.Pool -- so a double reporting t1
	// on the target pool and t2 on a different one exercises the filter
	// without needing a second real catalog pool.
	allocator := &fakeAllocatorForStoreCloser{assignments: []volume.Assignment{
		{TenantID: "t1", Pool: "fast"},
		{TenantID: "t2", Pool: "bulk"},
	}}

	if err := CloseTenantStoresForPool(ctx, allocator, hostReg, "fast"); err != nil {
		t.Fatalf("CloseTenantStoresForPool: %v", err)
	}
	if hostReg.Started("t1") {
		t.Error("t1 is assigned to the target pool and must be closed")
	}
	if !hostReg.Started("t2") {
		t.Error("t2 is assigned to a different pool and must be left running")
	}
}

// TestCloseTenantStoresForPool_NoLiveHostIsANoOp proves a tenant the
// allocator reports on the target pool, but with no live Host on this
// node (an IsolationRaft tenant, or one never attached), is a no-op --
// matching TenantHostRegistry.Close's own contract.
func TestCloseTenantStoresForPool_NoLiveHostIsANoOp(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	source := &lookupDataRootSource{registry: reg}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	allocator := &fakeAllocatorForStoreCloser{assignments: []volume.Assignment{
		{TenantID: "never-attached", Pool: "fast"},
	}}

	if err := CloseTenantStoresForPool(context.Background(), allocator, hostReg, "fast"); err != nil {
		t.Fatalf("CloseTenantStoresForPool: %v, want nil", err)
	}
}

// TestCloseTenantStoresForPool_NilHostsIsNoOp proves a nil registry (the
// IsolationVolume registry was never built, or the holder has not been
// Store'd yet) is a no-op rather than a panic or error.
func TestCloseTenantStoresForPool_NilHostsIsNoOp(t *testing.T) {
	allocator := &fakeAllocatorForStoreCloser{assignments: []volume.Assignment{{TenantID: "t1", Pool: "fast"}}}
	if err := CloseTenantStoresForPool(context.Background(), allocator, nil, "fast"); err != nil {
		t.Fatalf("CloseTenantStoresForPool: %v, want nil", err)
	}
}

// TestCloseTenantStoresForPool_NilAllocatorIsNoOp proves a nil allocator
// (no volume pool catalog on this node) is a no-op for the same reason.
func TestCloseTenantStoresForPool_NilAllocatorIsNoOp(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	source := &lookupDataRootSource{registry: reg}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	if err := CloseTenantStoresForPool(context.Background(), nil, hostReg, "fast"); err != nil {
		t.Fatalf("CloseTenantStoresForPool: %v, want nil", err)
	}
}

// TestCloseTenantStoresForPool_AllocatorErrorPropagates proves an
// allocator.ListAll failure is returned rather than swallowed.
func TestCloseTenantStoresForPool_AllocatorErrorPropagates(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	source := &lookupDataRootSource{registry: reg}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	wantErr := errors.New("boom")
	allocator := &fakeAllocatorForStoreCloser{listErr: wantErr}

	if err := CloseTenantStoresForPool(context.Background(), allocator, hostReg, "fast"); !errors.Is(err, wantErr) {
		t.Fatalf("CloseTenantStoresForPool: %v, want %v", err, wantErr)
	}
}

// TestTenantHostRegistryHolder_StoreLoadRoundTrip proves the holder
// publishes exactly what was last Stored.
func TestTenantHostRegistryHolder_StoreLoadRoundTrip(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	source := &lookupDataRootSource{registry: reg}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	var holder TenantHostRegistryHolder
	if got := holder.Load(); got != nil {
		t.Fatalf("zero-value Load() = %v, want nil", got)
	}
	holder.Store(hostReg)
	if got := holder.Load(); got != hostReg {
		t.Fatalf("Load() = %v, want %v", got, hostReg)
	}
}

// TestTenantHostRegistryHolder_NilReceiverIsSafe proves a nil
// *TenantHostRegistryHolder (the composer never built one, e.g. a test
// double) is safe to call Store/Load on -- matching the nil-receiver
// safety every other holder-like type in this codebase follows.
func TestTenantHostRegistryHolder_NilReceiverIsSafe(t *testing.T) {
	var holder *TenantHostRegistryHolder
	holder.Store(nil) // must not panic
	if got := holder.Load(); got != nil {
		t.Fatalf("nil-receiver Load() = %v, want nil", got)
	}
}
