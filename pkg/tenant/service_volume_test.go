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

package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// volumeEntitlements permits both volume isolation and dedicated volume
// storage, entitled to the fixture's "default" pool (sealtest.PooledRegistry).
func volumeEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:    true,
		AllowedVolumePools:    []string{"default"},
	}
}

// newVolumeService builds a TenantService whose default entitlements permit
// volume isolation, backed by a pooled barrier registry.
func newVolumeService(t *testing.T) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()), WithVolumeAllocator(volumeAllocatorFor(t, reg)))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// TestCreateTenant_Volume_AllocatesDataRootAndBarrier verifies that creating an
// IsolationVolume tenant (self-managed, the default) allocates a DataRoot from
// the pool, registers the per-tenant barrier, and records the local raft group.
// The barrier is left SEALED for the tenant admin to initialize and unseal.
func TestCreateTenant_Volume_AllocatesDataRootAndBarrier(t *testing.T) {
	svc, reg := newVolumeService(t)
	ctx := context.Background()

	// RegisterVolumeTenant (reached via CreateTenant) refuses with ErrSealed
	// while the system barrier is sealed; this test is about the tenant's own
	// (self-managed, left sealed) barrier, not the system barrier's state.

	info, err := svc.CreateTenant(ctx, "vtenant", "Volume Tenant", IsolationVolume)
	if err != nil {
		t.Fatalf("CreateTenant(volume): %v", err)
	}
	if info.IsolationType != IsolationVolume {
		t.Errorf("IsolationType = %q, want %q", info.IsolationType, IsolationVolume)
	}
	if info.BarrierAutoUnseal {
		t.Error("default volume tenant must be self-managed (BarrierAutoUnseal=false)")
	}
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vtenant")
	if err != nil || dataRoot == "" || !strings.Contains(dataRoot, "vtenant") {
		t.Errorf("LocalTenantDataRoot(vtenant) = %q, %v; want a pool path containing the tenant ID", dataRoot, err)
	}
	if info.Quotas == nil || info.Quotas.CurrentRaftGroups != 1 {
		t.Errorf("CurrentRaftGroups = %v, want 1 (one local raft group per volume tenant)", info.Quotas)
	}

	tb, err := reg.GetTenant("vtenant")
	if err != nil {
		t.Fatalf("barrier not registered for volume tenant: %v", err)
	}
	if !tb.IsSealed() {
		t.Error("self-managed volume tenant barrier must be sealed after create (tenant admin initializes it)")
	}
}

// TestCreateTenant_Volume_SystemManaged_AutoUnsealed verifies that an opt-in
// system-managed volume tenant is auto-initialized at create (credential-free)
// and comes up unsealed and ready, with its DEK wrapped under the system
// barrier.
func TestCreateTenant_Volume_SystemManaged_AutoUnsealed(t *testing.T) {
	svc, reg := newVolumeService(t)
	ctx := context.Background()
	// The system-managed strategy needs an unsealed system barrier (the unseal
	// authority). The fixture's system barrier is bound; init ignores creds.
	// Grant the system-managed-barrier entitlement on top of volume.
	svc2, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(),
		WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
			AllowVolumeStorage:        true,
			AllowSystemManagedBarrier: true,
			AllowedVolumePools:        []string{"default"},
		}), WithVolumeAllocator(volumeAllocatorFor(t, reg)))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	_ = svc

	info, err := svc2.CreateTenant(ctx, "vauto", "Auto Volume", IsolationVolume, WithBarrierAutoUnseal(true))
	if err != nil {
		t.Fatalf("CreateTenant(volume, auto): %v", err)
	}
	if !info.BarrierAutoUnseal {
		t.Error("BarrierAutoUnseal not persisted on the tenant record")
	}
	tb, err := reg.GetTenant("vauto")
	if err != nil {
		t.Fatalf("barrier not registered: %v", err)
	}
	if tb.IsSealed() {
		t.Error("system-managed volume tenant barrier must be unsealed after create")
	}
	if got := tb.Barrier().ActiveStrategy(); got != seal.StrategySystemBarrier {
		t.Errorf("active strategy = %q, want %q", got, seal.StrategySystemBarrier)
	}
}

// TestCreateTenant_Volume_SystemManaged_DeniedWithoutEntitlement verifies the
// entitlement gate on the system-managed mode: a tenant whose plan permits
// volume isolation but NOT the system-managed barrier is refused auto-unseal.
func TestCreateTenant_Volume_SystemManaged_DeniedWithoutEntitlement(t *testing.T) {
	svc, _ := newVolumeService(t) // volume allowed, system-managed NOT allowed
	_, err := svc.CreateTenant(context.Background(), "vnope", "No Auto", IsolationVolume, WithBarrierAutoUnseal(true))
	if err == nil {
		t.Fatal("expected entitlement error for system-managed without entitlement, got nil")
	}
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v, want *quota.EntitlementError", err)
	}
	if entErr.Feature != quota.FeatureSystemManagedBarrier {
		t.Errorf("denied feature = %q, want %q", entErr.Feature, quota.FeatureSystemManagedBarrier)
	}
}

// TestCreateTenant_Volume_DeniedWithoutEntitlement verifies the create-time
// entitlement gate: a tenant whose plan does not permit volume isolation is
// refused.
func TestCreateTenant_Volume_DeniedWithoutEntitlement(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	// Default entitlements (prefix-only): no volume permitted.
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	_, err = svc.CreateTenant(context.Background(), "denied", "Denied", IsolationVolume)
	if err == nil {
		t.Fatal("expected entitlement error creating volume tenant without entitlement, got nil")
	}
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v, want *quota.EntitlementError", err)
	}
}

// TestCreateTenant_Volume_SystemTenantRejected verifies the system tenant may
// not use volume isolation.
func TestCreateTenant_Volume_SystemTenantRejected(t *testing.T) {
	svc, _ := newVolumeService(t)
	_, err := svc.CreateTenant(context.Background(), tenantid.System, "System", IsolationVolume)
	if err == nil {
		t.Fatal("expected error creating system tenant with volume isolation, got nil")
	}
}

// TestUpdateQuotas_Volume_AdjustsCatalogReservation proves security review
// finding 6: changing a volume-isolation tenant's MaxStorageBytes quota
// also adjusts its catalog reservation, keeping the two in step rather
// than letting the catalog's number go stale the moment an operator
// changes the quota after creation.
func TestUpdateQuotas_Volume_AdjustsCatalogReservation(t *testing.T) {
	svc, reg := newVolumeService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationVolume); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	allocator := volumeAllocatorFor(t, reg)
	before, err := allocator.Resolve(ctx, "acme")
	if err != nil {
		t.Fatalf("Resolve (before): %v", err)
	}
	t.Logf("reservation at create: %d bytes", before.ReservedBytes)

	info, err := svc.UpdateQuotas(ctx, "acme", &quota.Quotas{MaxStorageBytes: 12345})
	if err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if info.Quotas.MaxStorageBytes != 12345 {
		t.Fatalf("TenantInfo.Quotas.MaxStorageBytes = %d, want 12345", info.Quotas.MaxStorageBytes)
	}

	after, err := allocator.Resolve(ctx, "acme")
	if err != nil {
		t.Fatalf("Resolve (after): %v", err)
	}
	if after.ReservedBytes != 12345 {
		t.Fatalf("catalog Assignment.ReservedBytes after UpdateQuotas = %d, want 12345 (kept in step with the quota)", after.ReservedBytes)
	}
}

// TestUpdateQuotas_Prefix_NeverTouchesTheCatalog proves the new wiring is
// scoped to volume/raft tenants: a prefix-isolation tenant has no catalog
// Assignment at all, and UpdateQuotas must not try to adjust one.
func TestUpdateQuotas_Prefix_NeverTouchesTheCatalog(t *testing.T) {
	svc, reg := newVolumeService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme-prefix", "Acme Prefix", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "acme-prefix", &quota.Quotas{MaxStorageBytes: 999}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	allocator := volumeAllocatorFor(t, reg)
	var notFound *volume.AssignmentNotFoundError
	if _, err := allocator.Resolve(ctx, "acme-prefix"); !errors.As(err, &notFound) {
		t.Fatalf("Resolve(prefix tenant) error = %v, want *volume.AssignmentNotFoundError (no catalog Assignment at all)", err)
	}
}

// TestCreateTenant_Volume_Refusals covers each refusal CreateTenant makes
// before or while reserving volume-pool capacity.
func TestCreateTenant_Volume_Refusals(t *testing.T) {
	noPools := volumeEntitlements()
	noPools.AllowedVolumePools = nil
	ghostPool := volumeEntitlements()
	ghostPool.AllowedVolumePools = []string{"default", "ghost"}

	tests := []struct {
		name         string
		ents         *quota.Entitlements
		noAllocator  bool
		opts         []CreateTenantOption
		wantFeature  string
		wantSentinel error
		wantReserve  bool
	}{
		{name: "no allocator", ents: volumeEntitlements(), noAllocator: true, wantSentinel: seal.ErrPoolNotConfigured},
		{name: "no default pool", ents: noPools, wantFeature: "volume_pool:<none>"},
		{name: "pool not entitled", ents: volumeEntitlements(), opts: []CreateTenantOption{WithVolumePool("other")},
			wantFeature: "volume_pool:other"},
		{name: "pool not in catalog", ents: ghostPool, opts: []CreateTenantOption{WithVolumePool("ghost")},
			wantReserve: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newTestBarrierRegistry(t)
			svcOpts := []TenantServiceOption{WithDefaultEntitlements(tt.ents)}
			if !tt.noAllocator {
				svcOpts = append(svcOpts, WithVolumeAllocator(volumeAllocatorFor(t, reg)))
			}
			svc, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(), svcOpts...)
			if err != nil {
				t.Fatalf("NewTenantService: %v", err)
			}
			_, err = svc.CreateTenant(context.Background(), "vol", "Vol", IsolationVolume, tt.opts...)
			switch {
			case tt.wantSentinel != nil:
				if !errors.Is(err, tt.wantSentinel) {
					t.Fatalf("err = %v, want %v", err, tt.wantSentinel)
				}
			case tt.wantFeature != "":
				var entErr *quota.EntitlementError
				if !errors.As(err, &entErr) || entErr.Feature != tt.wantFeature {
					t.Fatalf("err = %v, want *quota.EntitlementError for %q", err, tt.wantFeature)
				}
			case tt.wantReserve:
				var resErr *VolumePoolReserveError
				if !errors.As(err, &resErr) || resErr.Pool != "ghost" {
					t.Fatalf("err = %v, want *VolumePoolReserveError for pool ghost", err)
				}
				var notFound *volume.PoolNotFoundError
				if !errors.As(err, &notFound) {
					t.Fatalf("err = %v, want it to wrap *volume.PoolNotFoundError", err)
				}
			}
			if _, gerr := svc.GetTenant(context.Background(), "vol"); !errors.Is(gerr, ErrTenantNotFound) {
				t.Fatalf("GetTenant after refused create = %v, want ErrTenantNotFound", gerr)
			}
		})
	}
}
