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
)

// raftEntitlements permits raft isolation and the dedicated-raft feature.
func raftEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
		AllowDedicatedRaft:    true,
	}
}

// newRaftService builds a TenantService whose default entitlements permit raft
// isolation, backed by a pooled barrier registry.
func newRaftService(t *testing.T) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(raftEntitlements()))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// TestCreateTenant_Raft_AllocatesDataRootAndBarrier verifies that creating an
// IsolationRaft tenant (self-managed, the default) allocates a DataRoot from the
// pool and registers the per-tenant barrier (left SEALED for the tenant admin).
// Unlike a volume tenant, NO raft group is pre-counted: each dedicated-raft
// database increments the counter individually at database-create time.
func TestCreateTenant_Raft_AllocatesDataRootAndBarrier(t *testing.T) {
	svc, reg := newRaftService(t)
	ctx := context.Background()

	info, err := svc.CreateTenant(ctx, "rtenant", "Raft Tenant", IsolationRaft)
	if err != nil {
		t.Fatalf("CreateTenant(raft): %v", err)
	}
	if info.IsolationType != IsolationRaft {
		t.Errorf("IsolationType = %q, want %q", info.IsolationType, IsolationRaft)
	}
	if info.BarrierAutoUnseal {
		t.Error("default raft tenant must be self-managed (BarrierAutoUnseal=false)")
	}
	if info.DataRoot == "" || !strings.Contains(info.DataRoot, "rtenant") {
		t.Errorf("DataRoot = %q, want a pool path containing the tenant ID", info.DataRoot)
	}
	// No pre-counted raft group: per-database accounting handles MaxRaftGroups.
	if info.Quotas != nil && info.Quotas.CurrentRaftGroups != 0 {
		t.Errorf("CurrentRaftGroups = %d, want 0 (per-database accounting)", info.Quotas.CurrentRaftGroups)
	}

	tb, err := reg.GetTenant("rtenant")
	if err != nil {
		t.Fatalf("barrier not registered for raft tenant: %v", err)
	}
	if !tb.IsSealed() {
		t.Error("self-managed raft tenant barrier must be sealed after create")
	}
}

// TestCreateTenant_Raft_DeniedWithoutEntitlement verifies that a tenant whose
// plan does not permit raft isolation is rejected with an EntitlementError.
func TestCreateTenant_Raft_DeniedWithoutEntitlement(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	// Conservative defaults: prefix-only, no dedicated raft.
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	_, err = svc.CreateTenant(context.Background(), "denied", "Denied", IsolationRaft)
	if err == nil {
		t.Fatal("CreateTenant(raft) without entitlement must fail")
	}
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v, want *quota.EntitlementError", err)
	}
	if entErr.Mode != quota.IsolationModeRaft {
		t.Errorf("EntitlementError.Mode = %q, want %q", entErr.Mode, quota.IsolationModeRaft)
	}
}

// TestCreateTenant_Raft_SystemManaged_AutoUnsealed verifies the opt-in
// system-managed mode auto-initializes the raft tenant's barrier at create so it
// comes up unsealed and re-attaches credential-free at boot.
func TestCreateTenant_Raft_SystemManaged_AutoUnsealed(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	// The system-managed strategy needs an unsealed system barrier (the unseal
	// authority for the wrapped tenant DEK).
	if err := reg.SystemBarrier().Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:        true,
			AllowSystemManagedBarrier: true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	info, err := svc.CreateTenant(context.Background(), "rauto", "Auto Raft", IsolationRaft, WithBarrierAutoUnseal(true))
	if err != nil {
		t.Fatalf("CreateTenant(raft, auto-unseal): %v", err)
	}
	if !info.BarrierAutoUnseal {
		t.Error("BarrierAutoUnseal must be true for a system-managed raft tenant")
	}
	tb, err := reg.GetTenant("rauto")
	if err != nil {
		t.Fatalf("barrier not registered: %v", err)
	}
	if tb.IsSealed() {
		t.Error("system-managed raft tenant barrier must be unsealed and ready after create")
	}
}

// TestCreateTenant_Raft_SystemManaged_DeniedWithoutEntitlement verifies that
// opting into system-managed auto-unseal without the entitlement is rejected.
func TestCreateTenant_Raft_SystemManaged_DeniedWithoutEntitlement(t *testing.T) {
	svc, _ := newRaftService(t) // permits raft, but NOT system-managed barrier
	_, err := svc.CreateTenant(context.Background(), "rnope", "No Auto", IsolationRaft, WithBarrierAutoUnseal(true))
	if err == nil {
		t.Fatal("system-managed raft tenant without entitlement must fail")
	}
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v, want *quota.EntitlementError", err)
	}
}

// TestCreateTenant_Raft_SystemTenantRejected verifies the structurally
// prefix-only system tenant cannot use raft isolation.
func TestCreateTenant_Raft_SystemTenantRejected(t *testing.T) {
	svc, _ := newRaftService(t)
	_, err := svc.CreateTenant(context.Background(), tenantid.System, "System", IsolationRaft)
	if err == nil {
		t.Fatal("expected error creating system tenant with raft isolation, got nil")
	}
}
