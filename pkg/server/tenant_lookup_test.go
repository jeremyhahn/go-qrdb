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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// TestBuildTenantInfra_VolumeRegistryWithoutLookup_ReturnsConfigError pins
// go-qrdb#383's contract: the IsolationVolume registry outlives a single tenant
// service, so building it requires an explicit use-time lookup. Omitting one is
// refused before any side effect rather than silently capturing TenantSvc.
func TestBuildTenantInfra_VolumeRegistryWithoutLookup_ReturnsConfigError(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)

	ti, err := BuildTenantInfra(TenantInfraConfig{
		Ctx:                  context.Background(),
		BarrierReg:           reg,
		TenantSvc:            svc,
		VolumePoolConfigured: true,
		Logger:               reattachTestLogger(),
		// TenantLookup intentionally omitted -- the contract violation under test.
	})

	var cfgErr *TenantInfraConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *TenantInfraConfigError, got %T: %v", err, err)
	}
	if cfgErr.Field != "TenantLookup" {
		t.Errorf("TenantInfraConfigError.Field = %q, want %q", cfgErr.Field, "TenantLookup")
	}
	if ti != nil {
		t.Errorf("BuildTenantInfra returned a TenantInfra alongside a config error: %+v", ti)
	}
}

// TestNewTenantHostRegistryFromLookup_NilLookupIsUnavailable verifies a nil
// lookup yields a registry that refuses with ErrTenantHostUnavailable rather
// than one that dereferences nil on first use.
func TestNewTenantHostRegistryFromLookup_NilLookupIsUnavailable(t *testing.T) {
	reg := NewTenantHostRegistryFromLookup(&seal.BarrierRegistry{}, nil, reattachTestLogger())
	_, err := reg.Services(context.Background(), "acme")
	if !errors.Is(err, ErrTenantHostUnavailable) {
		t.Fatalf("Services with nil lookup = %v, want ErrTenantHostUnavailable", err)
	}
}

// TestTenantHostRegistry_AttachPropagatesUnavailable verifies Attach returns
// the Services resolution error, typed, rather than swallowing it.
func TestTenantHostRegistry_AttachPropagatesUnavailable(t *testing.T) {
	reg := NewTenantHostRegistry(nil, nil, nil)
	if err := reg.Attach(context.Background(), "acme"); !errors.Is(err, ErrTenantHostUnavailable) {
		t.Fatalf("Attach on an unwired registry = %v, want ErrTenantHostUnavailable", err)
	}
}

// TestTenantHostRegistry_CloseAbsentTenantIsNoOp verifies Close of a tenant
// with no live Host returns nil.
func TestTenantHostRegistry_CloseAbsentTenantIsNoOp(t *testing.T) {
	reg := NewTenantHostRegistry(nil, nil, nil)
	if err := reg.Close("never-attached"); err != nil {
		t.Fatalf("Close of an absent tenant = %v, want nil", err)
	}
}

// TestLookupDataRootSource_ResolvesThroughTheRegistryAtUseTime is the
// unit-level half of go-qrdb#383, re-expressed for the catalog-backed
// resolution path: lookupDataRootSource.dataRootOf delegates to
// BarrierRegistry.LocalTenantDataRoot on EVERY call rather than caching a
// resolved path, so a tenant reserved and registered AFTER the
// TenantHostRegistry was built still resolves correctly on first use.
func TestLookupDataRootSource_ResolvesThroughTheRegistryAtUseTime(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	src := &lookupDataRootSource{registry: reg}

	// Not yet reserved/registered: resolution fails.
	if _, err := src.dataRootOf(context.Background(), "acme"); err == nil {
		t.Fatal("dataRootOf must fail before the tenant has a catalog Assignment")
	}

	dataRoot, _ := registerVolumeTenant(t, reg, "acme")

	got, err := src.dataRootOf(context.Background(), "acme")
	if err != nil || got != dataRoot {
		t.Fatalf("dataRootOf after registration = %q, %v; want %q", got, err, dataRoot)
	}
}

// TestLookupDataRootSource_PropagatesRegistryError verifies that a
// resolution failure (here: an unregistered tenant) is returned to the
// caller rather than read as an empty DataRoot.
func TestLookupDataRootSource_PropagatesRegistryError(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	src := &lookupDataRootSource{registry: reg}

	_, err := src.dataRootOf(context.Background(), "ghost")
	var notFound *volume.AssignmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("dataRootOf = %v, want *volume.AssignmentNotFoundError", err)
	}
}

// TestTenantHostRegistry_Services_SkippedPoolTenantGetsPoolUnavailable
// proves the owner's 2026-10-06 ruling's second half: a tenant assigned to
// a pool this node never opened (the degraded-boot skip,
// --allow-degraded-volume-boot) was never registered in the barrier
// registry at all (reattachVolumeTenants/reattachRaftTenants skip it, as
// reattach_refused_test.go's TestBoot_ALegacyTenantDoesNotBrickTheNode
// shows for the barrierReg side). Before this fix, Services() stopped at
// GetTenant's generic "not found" and never got as far as asking WHY. It
// must now resolve through the volume allocator directly and surface
// *volume.PoolUnavailableError -- classified Unavailable by
// pkg/api/errclass on every transport -- not a not-found or an
// uninformative internal error.
func TestTenantHostRegistry_Services_SkippedPoolTenantGetsPoolUnavailable(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	ctx := context.Background()

	allocVal, ok := testVolumeAllocators.Load(reg)
	if !ok {
		t.Fatal("reg was not built by newVolumePooledRegistry")
	}
	allocator := allocVal.(volume.Allocator)
	catalogSvc, ok := allocator.(volume.Service)
	if !ok {
		t.Fatal("the test allocator must also be a volume.Service to CreatePool")
	}
	if _, err := catalogSvc.CreatePool(ctx, volume.CreatePoolRequest{Name: "broken", Provider: "dir"}); err != nil {
		t.Fatalf("CreatePool(broken): %v", err)
	}
	// Reserve directly through the allocator -- NOT RegisterVolumeTenant --
	// so the tenant's Assignment exists in the catalog (as it would on the
	// node that created it, or any node that already restored it) but this
	// registry's barrier map never learns about "ghost": exactly the state
	// a degraded boot leaves a tenant on a pool it skipped in.
	if _, err := allocator.Reserve(ctx, "ghost", "broken", "ghost", 0); err != nil {
		t.Fatalf("Reserve(ghost, broken): %v", err)
	}
	if _, err := reg.GetTenant("ghost"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("GetTenant(ghost) = %v, want seal.ErrTenantNotFound (never registered, matching a degraded-boot skip)", err)
	}

	hostReg := NewTenantHostRegistry(reg, &lookupDataRootSource{registry: reg}, reattachTestLogger())
	_, err := hostReg.Services(ctx, "ghost")
	if err == nil {
		t.Fatal("Services(ghost) succeeded; want *volume.PoolUnavailableError for a tenant on a pool this node never opened")
	}
	var poolErr *volume.PoolUnavailableError
	if !errors.As(err, &poolErr) {
		t.Fatalf("Services(ghost) = %T: %v, want *volume.PoolUnavailableError", err, err)
	}
	if poolErr.Pool != "broken" {
		t.Errorf("PoolUnavailableError.Pool = %q, want %q", poolErr.Pool, "broken")
	}
}

// TestTenantHostRegistry_Services_TrulyUnknownTenantGetsAssignmentNotFound
// is the control case: a tenant with NO catalog assignment at all (never
// created anywhere, not merely skipped on this node) still resolves to a
// typed, specific error -- *volume.AssignmentNotFoundError, classified
// NotFound -- rather than the generic ErrTenantNotFound the in-memory
// lookup alone would give. The fix in Services() (consult the allocator
// when GetTenant reports not-found) improves this case too: it used to
// surface an untyped seal.ErrTenantNotFound wrapped in TenantHostError,
// which pkg/api/errclass has no specific rule for.
func TestTenantHostRegistry_Services_TrulyUnknownTenantGetsAssignmentNotFound(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	hostReg := NewTenantHostRegistry(reg, &lookupDataRootSource{registry: reg}, reattachTestLogger())

	_, err := hostReg.Services(context.Background(), "never-existed")
	var notFound *volume.AssignmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("Services(never-existed) = %T: %v, want *volume.AssignmentNotFoundError", err, err)
	}
}
