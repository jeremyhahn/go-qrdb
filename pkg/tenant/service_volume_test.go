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
)

// volumeEntitlements permits both volume isolation and dedicated volume storage.
func volumeEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:    true,
	}
}

// newVolumeService builds a TenantService whose default entitlements permit
// volume isolation, backed by a pooled barrier registry.
func newVolumeService(t *testing.T) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()))
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
	if info.DataRoot == "" || !strings.Contains(info.DataRoot, "vtenant") {
		t.Errorf("DataRoot = %q, want a pool path containing the tenant ID", info.DataRoot)
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
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	// Grant the system-managed-barrier entitlement on top of volume.
	svc2, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(),
		WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
			AllowVolumeStorage:        true,
			AllowSystemManagedBarrier: true,
		}))
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
	_, err := svc.CreateTenant(context.Background(), SystemTenantID, "System", IsolationVolume)
	if err == nil {
		t.Fatal("expected error creating system tenant with volume isolation, got nil")
	}
}
