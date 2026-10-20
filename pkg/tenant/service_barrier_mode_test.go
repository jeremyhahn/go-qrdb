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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestCreateTenant_Barrier_SelfManaged_StaysSealed verifies that the default
// (self-managed) IsolationBarrier tenant is registered but left SEALED so the
// tenant admin initializes it; no auto-init occurs at create.
func TestCreateTenant_Barrier_SelfManaged_StaysSealed(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	info, err := svc.CreateTenant(ctx, "self-barrier", "Self Barrier", IsolationBarrier)
	if err != nil {
		t.Fatalf("CreateTenant(barrier, self): %v", err)
	}
	if info.BarrierAutoUnseal {
		t.Error("default barrier tenant must be self-managed (BarrierAutoUnseal=false)")
	}
	tb, err := reg.GetTenant("self-barrier")
	if err != nil {
		t.Fatalf("barrier not registered: %v", err)
	}
	if !tb.IsSealed() {
		t.Error("self-managed barrier tenant must be sealed after create")
	}
}

// TestCreateTenant_Barrier_SystemManaged_AutoUnsealed verifies the opt-in
// system-managed IsolationBarrier tenant is auto-initialized (credential-free)
// and comes up unsealed with its DEK wrapped under the system barrier.
func TestCreateTenant_Barrier_SystemManaged_AutoUnsealed(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	// System-managed strategy requires an unsealed system barrier.
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix},
			AllowSystemManagedBarrier: true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	info, err := svc.CreateTenant(ctx, "auto-barrier", "Auto Barrier", IsolationBarrier, WithBarrierAutoUnseal(true))
	if err != nil {
		t.Fatalf("CreateTenant(barrier, auto): %v", err)
	}
	if !info.BarrierAutoUnseal {
		t.Error("BarrierAutoUnseal not persisted")
	}
	tb, err := reg.GetTenant("auto-barrier")
	if err != nil {
		t.Fatalf("barrier not registered: %v", err)
	}
	if tb.IsSealed() {
		t.Error("system-managed barrier tenant must be unsealed after create")
	}
	if got := tb.Barrier().ActiveStrategy(); got != seal.StrategySystemBarrier {
		t.Errorf("active strategy = %q, want %q", got, seal.StrategySystemBarrier)
	}
}

// TestCreateTenant_Barrier_SystemManaged_DeniedWithoutEntitlement verifies the
// system-managed barrier mode is entitlement-gated for IsolationBarrier too.
func TestCreateTenant_Barrier_SystemManaged_DeniedWithoutEntitlement(t *testing.T) {
	svc, _ := newTestService(t) // default entitlements: system-managed NOT allowed
	_, err := svc.CreateTenant(context.Background(), "nope-barrier", "Nope", IsolationBarrier, WithBarrierAutoUnseal(true))
	if err == nil {
		t.Fatal("expected entitlement error for system-managed barrier, got nil")
	}
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v, want *quota.EntitlementError", err)
	}
	if entErr.Feature != quota.FeatureSystemManagedBarrier {
		t.Errorf("denied feature = %q, want %q", entErr.Feature, quota.FeatureSystemManagedBarrier)
	}
}
