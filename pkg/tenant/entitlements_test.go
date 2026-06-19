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

// TestTenantService_ImplementsEntitlementChecker verifies the interface
// satisfaction at runtime (compile-time check lives in service.go).
func TestTenantService_ImplementsEntitlementChecker(t *testing.T) {
	var _ quota.EntitlementChecker = (*TenantService)(nil)
}

// TestUpdateEntitlements_RoundTrip verifies that UpdateEntitlements persists
// the entitlements and that a subsequent GetTenant reflects the change.
func TestUpdateEntitlements_RoundTrip(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	ents := &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:    true,
	}
	updated, err := svc.UpdateEntitlements(ctx, "acme", ents)
	if err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}
	if updated.Entitlements == nil || !updated.Entitlements.AllowVolumeStorage {
		t.Fatalf("returned tenant entitlements not applied: %+v", updated.Entitlements)
	}

	// Persistence: re-read from the store.
	got, err := svc.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Entitlements == nil {
		t.Fatal("entitlements not persisted")
	}
	if !got.Entitlements.AllowVolumeStorage {
		t.Error("AllowVolumeStorage not persisted")
	}
	if !got.Entitlements.AllowsIsolation(quota.IsolationModeVolume) {
		t.Error("volume isolation mode not persisted")
	}
}

// TestUpdateEntitlements_Errors covers the validation paths.
func TestUpdateEntitlements_Errors(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.UpdateEntitlements(ctx, "", &quota.Entitlements{}); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("empty id: got %v, want ErrEmptyTenantID", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "acme", nil); !errors.Is(err, ErrNilEntitlements) {
		t.Errorf("nil entitlements: got %v, want ErrNilEntitlements", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "ghost", &quota.Entitlements{}); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("missing tenant: got %v, want ErrTenantNotFound", err)
	}
}

// TestCheckIsolation_SystemTenantImplicitlyAllowed verifies the system tenant
// is entitled to every mode without any stored entitlements.
func TestCheckIsolation_SystemTenantImplicitlyAllowed(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	for _, mode := range []string{quota.IsolationModePrefix, quota.IsolationModeRaft, quota.IsolationModeVolume} {
		if err := svc.CheckIsolation(ctx, SystemTenantID, mode); err != nil {
			t.Errorf("system tenant CheckIsolation(%q): %v", mode, err)
		}
	}
	if err := svc.CheckFeature(ctx, SystemTenantID, quota.FeatureDedicatedRaft); err != nil {
		t.Errorf("system tenant CheckFeature: %v", err)
	}
}

// TestCheckIsolation_NilEntitlementsUsesDefaults verifies that a tenant with no
// stored entitlements falls back to the conservative built-in defaults
// (prefix-only) when no operator default is configured.
func TestCheckIsolation_NilEntitlementsUsesDefaults(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Default allows prefix only.
	if err := svc.CheckIsolation(ctx, "acme", quota.IsolationModePrefix); err != nil {
		t.Errorf("prefix should be allowed by default: %v", err)
	}
	// Raft is denied by default.
	var entErr *quota.EntitlementError
	err := svc.CheckIsolation(ctx, "acme", quota.IsolationModeRaft)
	if !errors.As(err, &entErr) {
		t.Fatalf("raft should be denied: got %v, want *EntitlementError", err)
	}
	if entErr.Mode != quota.IsolationModeRaft || entErr.TenantID != "acme" {
		t.Errorf("EntitlementError fields = %+v", entErr)
	}
}

// TestCheckIsolation_ConfiguredDefaults verifies WithDefaultEntitlements is
// honored for nil-entitlement tenants.
func TestCheckIsolation_ConfiguredDefaults(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	defaults := &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
		AllowDedicatedRaft:    true,
	}
	svc, err := NewTenantService(store, reg, testLogger(), WithDefaultEntitlements(defaults))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svc.CheckIsolation(ctx, "acme", quota.IsolationModeRaft); err != nil {
		t.Errorf("raft should be allowed by configured default: %v", err)
	}
	if err := svc.CheckFeature(ctx, "acme", quota.FeatureDedicatedRaft); err != nil {
		t.Errorf("dedicated_raft should be allowed by configured default: %v", err)
	}
}

// TestCheckFeature_FlagsAndDenials covers explicit per-tenant feature flags.
func TestCheckFeature_FlagsAndDenials(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix},
		AllowDedicatedRaft:    true,
		AllowVolumeStorage:    false,
	}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}

	if err := svc.CheckFeature(ctx, "acme", quota.FeatureDedicatedRaft); err != nil {
		t.Errorf("dedicated_raft should be allowed: %v", err)
	}
	var entErr *quota.EntitlementError
	if err := svc.CheckFeature(ctx, "acme", quota.FeatureVolumeStorage); !errors.As(err, &entErr) {
		t.Errorf("volume_storage should be denied: got %v", err)
	} else if entErr.Feature != quota.FeatureVolumeStorage {
		t.Errorf("EntitlementError.Feature = %q", entErr.Feature)
	}
	// Unknown feature fails closed.
	if err := svc.CheckFeature(ctx, "acme", "no_such_feature"); !errors.As(err, &entErr) {
		t.Errorf("unknown feature should be denied: got %v", err)
	}
}
