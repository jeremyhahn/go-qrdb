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
	"testing"
)

// newSystemBarrier creates a system-level barrier for registry tests.
func newSystemBarrier(t *testing.T) *Barrier {
	t.Helper()
	store := NewMemoryBackend()
	b, err := NewBarrier(testLogger(), store, BarrierConfig{
		RootKeyPath: "system/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier (system): %v", err)
	}
	return b
}

// newTestRegistry creates a BarrierRegistry with a system barrier.
func newTestRegistry(t *testing.T) *BarrierRegistry {
	t.Helper()
	system := newSystemBarrier(t)
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg
}

// ---------- NewBarrierRegistry tests ----------

func TestBarrierRegistry_NewBarrierRegistry_Success(t *testing.T) {
	system := newSystemBarrier(t)
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if reg.SystemBarrier() != system {
		t.Error("SystemBarrier() returned wrong reference")
	}
}

func TestBarrierRegistry_NewBarrierRegistry_NilSystem(t *testing.T) {
	_, err := NewBarrierRegistry(testLogger(), nil)
	if err != ErrNilSystemBarrier {
		t.Errorf("expected ErrNilSystemBarrier, got %v", err)
	}
}

// ---------- RegisterTenant tests ----------

func TestBarrierRegistry_RegisterTenant_Success(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()

	tb, err := reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if tb.TenantID() != "tenant-1" {
		t.Errorf("expected tenant-1, got %s", tb.TenantID())
	}
}

func TestBarrierRegistry_RegisterTenant_Duplicate(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()

	_, err := reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("first RegisterTenant: %v", err)
	}

	_, err = reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != ErrTenantAlreadyExists {
		t.Errorf("expected ErrTenantAlreadyExists, got %v", err)
	}
}

func TestBarrierRegistry_RegisterTenant_EmptyID(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()

	_, err := reg.RegisterTenant(context.Background(), "", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != ErrEmptyTenantID {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

// ---------- RegisterTenantDefault tests ----------

func TestBarrierRegistry_RegisterTenantDefault_Success(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("tenant-pw"))

	tb, err := reg.RegisterTenantDefault("default-tenant")
	if err != nil {
		t.Fatalf("RegisterTenantDefault: %v", err)
	}
	if tb.TenantID() != "default-tenant" {
		t.Errorf("expected default-tenant, got %s", tb.TenantID())
	}

	// Verify the tenant is accessible via GetTenant.
	got, err := reg.GetTenant("default-tenant")
	if err != nil {
		t.Fatalf("GetTenant after RegisterTenantDefault: %v", err)
	}
	if got != tb {
		t.Error("GetTenant returned a different TenantBarrier instance")
	}

	// Verify the tenant can initialize and unseal.
	if err := tb.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if tb.IsSealed() {
		t.Error("expected unsealed after Initialize")
	}

	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !tb.IsSealed() {
		t.Error("expected sealed after Seal")
	}

	if err := tb.Unseal(ctx, creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if tb.IsSealed() {
		t.Error("expected unsealed after Unseal")
	}
}

func TestBarrierRegistry_RegisterTenantDefault_EmptyID(t *testing.T) {
	reg := newTestRegistry(t)

	_, err := reg.RegisterTenantDefault("")
	if err != ErrEmptyTenantID {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestBarrierRegistry_RegisterTenantDefault_Duplicate(t *testing.T) {
	reg := newTestRegistry(t)

	_, err := reg.RegisterTenantDefault("dup-tenant")
	if err != nil {
		t.Fatalf("first RegisterTenantDefault: %v", err)
	}

	_, err = reg.RegisterTenantDefault("dup-tenant")
	if err != ErrTenantAlreadyExists {
		t.Errorf("expected ErrTenantAlreadyExists, got %v", err)
	}
}

// ---------- Tenant (alias) tests ----------

func TestBarrierRegistry_Tenant_Success(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()

	registered, err := reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	// Tenant() should return the same result as GetTenant().
	fromTenant, err := reg.Tenant("tenant-1")
	if err != nil {
		t.Fatalf("Tenant: %v", err)
	}
	fromGet, err := reg.GetTenant("tenant-1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}

	if fromTenant != registered {
		t.Error("Tenant() returned a different instance than RegisterTenant")
	}
	if fromTenant != fromGet {
		t.Error("Tenant() and GetTenant() returned different instances")
	}
}

func TestBarrierRegistry_Tenant_NotFound(t *testing.T) {
	reg := newTestRegistry(t)

	_, err := reg.Tenant("nonexistent")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestBarrierRegistry_Tenant_EmptyID(t *testing.T) {
	reg := newTestRegistry(t)

	_, err := reg.Tenant("")
	if err != ErrEmptyTenantID {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

// ---------- GetTenant tests ----------

func TestBarrierRegistry_GetTenant_Success(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()

	_, err := reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	tb, err := reg.GetTenant("tenant-1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if tb.TenantID() != "tenant-1" {
		t.Errorf("expected tenant-1, got %s", tb.TenantID())
	}
}

func TestBarrierRegistry_GetTenant_NotFound(t *testing.T) {
	reg := newTestRegistry(t)

	_, err := reg.GetTenant("nonexistent")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestBarrierRegistry_GetTenant_EmptyID(t *testing.T) {
	reg := newTestRegistry(t)

	_, err := reg.GetTenant("")
	if err != ErrEmptyTenantID {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

// ---------- RemoveTenant tests ----------

func TestBarrierRegistry_RemoveTenant_Success(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()

	_, err := reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	if err := reg.RemoveTenant("tenant-1"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}

	_, err = reg.GetTenant("tenant-1")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound after remove, got %v", err)
	}
}

func TestBarrierRegistry_RemoveTenant_NotFound(t *testing.T) {
	reg := newTestRegistry(t)

	err := reg.RemoveTenant("ghost")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------- UnregisterTenant (alias) tests ----------

func TestBarrierRegistry_UnregisterTenant_Success(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()

	_, err := reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	// UnregisterTenant() should behave identically to RemoveTenant().
	if err := reg.UnregisterTenant("tenant-1"); err != nil {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	_, err = reg.GetTenant("tenant-1")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound after unregister, got %v", err)
	}
}

func TestBarrierRegistry_UnregisterTenant_NotFound(t *testing.T) {
	reg := newTestRegistry(t)

	err := reg.UnregisterTenant("ghost")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------- InitializeTenant / UnsealTenant / SealTenant / TenantStatus ----------

func TestBarrierRegistry_TenantLifecycle(t *testing.T) {
	reg := newTestRegistry(t)
	base := NewMemoryBackend()
	ctx := context.Background()
	creds := MustCredentials([]byte("tenant-pw"))

	_, err := reg.RegisterTenant(context.Background(), "tenant-1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	// Check initial status.
	status, err := reg.TenantStatus("tenant-1")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if !status.Sealed {
		t.Error("expected sealed initially")
	}

	// Initialize.
	if err := reg.InitializeTenant(ctx, "tenant-1", creds); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}

	status, err = reg.TenantStatus("tenant-1")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed after Initialize")
	}

	// Seal.
	if err := reg.SealTenant("tenant-1"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	status, err = reg.TenantStatus("tenant-1")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if !status.Sealed {
		t.Error("expected sealed after SealTenant")
	}

	// Unseal.
	if err := reg.UnsealTenant(ctx, "tenant-1", creds); err != nil {
		t.Fatalf("UnsealTenant: %v", err)
	}
	status, err = reg.TenantStatus("tenant-1")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed after UnsealTenant")
	}
}

func TestBarrierRegistry_InitializeTenant_NotFound(t *testing.T) {
	reg := newTestRegistry(t)
	err := reg.InitializeTenant(context.Background(), "ghost", MustCredentials([]byte("pw")))
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestBarrierRegistry_UnsealTenant_NotFound(t *testing.T) {
	reg := newTestRegistry(t)
	err := reg.UnsealTenant(context.Background(), "ghost", MustCredentials([]byte("pw")))
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestBarrierRegistry_SealTenant_NotFound(t *testing.T) {
	reg := newTestRegistry(t)
	err := reg.SealTenant("ghost")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestBarrierRegistry_TenantStatus_NotFound(t *testing.T) {
	reg := newTestRegistry(t)
	_, err := reg.TenantStatus("ghost")
	if err != ErrTenantNotFound {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------- ListTenants tests ----------

func TestBarrierRegistry_ListTenants_Empty(t *testing.T) {
	reg := newTestRegistry(t)

	ids := reg.ListTenants()
	if len(ids) != 0 {
		t.Errorf("expected empty list, got %v", ids)
	}
}

func TestBarrierRegistry_ListTenants_MultipleTenants(t *testing.T) {
	reg := newTestRegistry(t)

	for _, id := range []string{"alpha", "beta", "gamma"} {
		base := NewMemoryBackend()
		_, err := reg.RegisterTenant(context.Background(), id, base, BarrierConfig{}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatalf("RegisterTenant(%s): %v", id, err)
		}
	}

	ids := reg.ListTenants()
	if len(ids) != 3 {
		t.Fatalf("expected 3 tenants, got %d", len(ids))
	}

	// Verify all IDs are present (order is non-deterministic with sync.Map).
	found := make(map[string]bool)
	for _, id := range ids {
		found[id] = true
	}
	for _, expected := range []string{"alpha", "beta", "gamma"} {
		if !found[expected] {
			t.Errorf("missing tenant %s in list", expected)
		}
	}
}

// ---------- SealAll tests ----------

func TestBarrierRegistry_SealAll(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("pw"))

	// Initialize system barrier.
	if err := reg.SystemBarrier().Initialize(ctx, creds); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	// Register and initialize tenants.
	for _, id := range []string{"t1", "t2"} {
		base := NewMemoryBackend()
		_, err := reg.RegisterTenant(context.Background(), id, base, BarrierConfig{}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatalf("RegisterTenant(%s): %v", id, err)
		}
		if err := reg.InitializeTenant(ctx, id, creds); err != nil {
			t.Fatalf("InitializeTenant(%s): %v", id, err)
		}
	}

	// Seal everything.
	if err := reg.SealAll(); err != nil {
		t.Fatalf("SealAll: %v", err)
	}

	// System barrier should be sealed.
	if !reg.SystemBarrier().IsSealed() {
		t.Error("system barrier should be sealed")
	}

	// All tenants should be sealed.
	for _, id := range []string{"t1", "t2"} {
		tb, err := reg.GetTenant(id)
		if err != nil {
			t.Fatalf("GetTenant(%s): %v", id, err)
		}
		if !tb.IsSealed() {
			t.Errorf("tenant %s should be sealed", id)
		}
	}
}

// ---------- Close tests ----------

func TestBarrierRegistry_Close(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	creds := MustCredentials([]byte("pw"))

	// Initialize system barrier.
	if err := reg.SystemBarrier().Initialize(ctx, creds); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	// Register and initialize a tenant.
	base := NewMemoryBackend()
	_, err := reg.RegisterTenant(context.Background(), "t1", base, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "t1", creds); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}

	// Close everything.
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// System barrier should be sealed.
	if !reg.SystemBarrier().IsSealed() {
		t.Error("system barrier should be sealed after Close")
	}

	// Tenant should be sealed.
	tb, err := reg.GetTenant("t1")
	if err != nil {
		t.Fatalf("GetTenant(t1): %v", err)
	}
	if !tb.IsSealed() {
		t.Error("tenant t1 should be sealed after Close")
	}
}

func TestBarrierRegistry_Close_EmptyRegistry(t *testing.T) {
	reg := newTestRegistry(t)

	// Close with no tenants should succeed.
	if err := reg.Close(); err != nil {
		t.Fatalf("Close (empty): %v", err)
	}
}

// ---------- SystemBarrier accessor ----------

func TestBarrierRegistry_SystemBarrier_Accessor(t *testing.T) {
	system := newSystemBarrier(t)
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	if reg.SystemBarrier() != system {
		t.Error("SystemBarrier() should return the same barrier passed to constructor")
	}
}

// --------------------------------------------------------------------------
// barrier_registry.go: InitializeTenantShamir — 75%
// (GetTenant error path)
// --------------------------------------------------------------------------

func TestBarrierRegistry_InitializeTenantShamir_TenantNotFound(t *testing.T) {
	reg := newTestRegistry(t)
	_, err := reg.InitializeTenantShamir(context.Background(), "nonexistent", "")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("InitializeTenantShamir(unknown): got %v, want ErrTenantNotFound", err)
	}
}

// --------------------------------------------------------------------------
// barrier_registry.go: SealAll — 75%
// (exercises the happy-path with registered tenants)
// --------------------------------------------------------------------------

func TestBarrierRegistry_SealAll_Success(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Register two tenants.
	for _, id := range []string{"tenant-seal-1", "tenant-seal-2"} {
		_, err := reg.RegisterTenant(ctx, id, NewMemoryBackend(), BarrierConfig{
			RootKeyPath: "t/root-key",
		}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatalf("RegisterTenant(%s): %v", id, err)
		}
	}

	if err := reg.SealAll(); err != nil {
		t.Fatalf("SealAll: %v", err)
	}

	// All registered tenant barriers and the system barrier should now be sealed.
	for _, id := range []string{"tenant-seal-1", "tenant-seal-2"} {
		tb, err := reg.GetTenant(id)
		if err != nil {
			t.Fatalf("GetTenant(%s): %v", id, err)
		}
		if !tb.IsSealed() {
			t.Errorf("tenant %s: expected sealed after SealAll", id)
		}
	}
}

// --------------------------------------------------------------------------
// barrier_registry.go: Close — 75%
// (exercises close with registered tenants)
// --------------------------------------------------------------------------

func TestBarrierRegistry_Close_Success(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	_, err := reg.RegisterTenant(ctx, "close-tenant", NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "t/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
