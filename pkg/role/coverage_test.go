// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package role

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestSuperadminRoleID_WithTenantID verifies that SuperadminRoleID returns
// a deterministic, non-empty string for a given tenant ID.
func TestSuperadminRoleID_WithTenantID(t *testing.T) {
	id := SuperadminRoleID("acme")
	if id == "" {
		t.Fatal("SuperadminRoleID returned empty string")
	}
}

// TestSuperadminRoleID_EmptyTenantDefaultsToDefaultTenantID verifies that an
// empty tenantID falls back to the default tenant and produces the same result
// as passing DefaultTenantID explicitly.
func TestSuperadminRoleID_EmptyTenantDefaultsToDefaultTenantID(t *testing.T) {
	idEmpty := SuperadminRoleID("")
	idDefault := SuperadminRoleID(tenant.DefaultTenantID)

	if idEmpty == "" {
		t.Fatal("SuperadminRoleID(\"\") returned empty string")
	}
	if idEmpty != idDefault {
		t.Errorf("SuperadminRoleID(\"\") = %q, want %q (same as DefaultTenantID)", idEmpty, idDefault)
	}
}

// TestSuperadminRoleID_DifferentTenants verifies that different tenant IDs
// produce different superadmin role IDs.
func TestSuperadminRoleID_DifferentTenants(t *testing.T) {
	id1 := SuperadminRoleID("tenant-a")
	id2 := SuperadminRoleID("tenant-b")

	if id1 == id2 {
		t.Errorf("SuperadminRoleID must differ between tenants, both returned %q", id1)
	}
}

// TestSuperadminRoleID_Deterministic verifies that calling SuperadminRoleID
// twice with the same tenant yields the same UUID.
func TestSuperadminRoleID_Deterministic(t *testing.T) {
	id1 := SuperadminRoleID("acme")
	id2 := SuperadminRoleID("acme")

	if id1 != id2 {
		t.Errorf("SuperadminRoleID must be deterministic: first=%q, second=%q", id1, id2)
	}
}

// TestCreateRole_NameIndexPutFails verifies CreateRole returns a StoreError
// when writing the name index fails (3rd Put).
func TestCreateRole_NameIndexPutFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 3, // primary(1), tenant index(2), name index(3) — fail 3rd
		putErr:    errors.New("disk full"),
	}
	svc, err := NewStore(fkv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	_, err = svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "viewer",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error on name index Put")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestUpdateRole_PutPrimaryFails_Coverage verifies that UpdateRole propagates a Put error
// when the underlying store fails during the final primary record write.
func TestUpdateRole_PutPrimaryFails_Coverage(t *testing.T) {
	fkv := &faultKVStore{
		inner: newMemKVStore(),
	}
	svc, err := NewStore(fkv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "editor",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// Fail on the next Put (UpdateRole writes back the primary record).
	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("disk error")

	_, err = svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID: r.ID,
		// No name change, just trigger the final Put.
	})
	if err == nil {
		t.Fatal("expected error from UpdateRole when Put fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestListRoles_ScanError_Coverage verifies that ListRoles propagates a scan error.
func TestListRoles_ScanError_Coverage(t *testing.T) {
	fkv := &faultKVStore{
		inner:   newMemKVStore(),
		scanErr: errors.New("scan failed"),
	}
	svc, err := NewStore(fkv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	_, err = svc.ListRoles(ctx, &ListRolesRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error from ListRoles when scan fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}
