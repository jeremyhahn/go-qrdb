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

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestSystemRoleID_WithTenantID verifies that SystemRoleID returns
// a deterministic, non-empty string for a given tenant id and role name.
func TestSystemRoleID_WithTenantID(t *testing.T) {
	id := SystemRoleID("acme", RoleNameAdmin)
	if id == "" {
		t.Fatal("SystemRoleID returned empty string")
	}
}

// TestSystemRoleID_EmptyTenantDefaultsToSystemTenantID verifies that an
// empty tenant id falls back to the system tenant and produces the same
// result as passing SystemTenantID explicitly.
func TestSystemRoleID_EmptyTenantDefaultsToSystemTenantID(t *testing.T) {
	idEmpty := SystemRoleID("", RoleNameAdmin)
	idSystem := SystemRoleID(tenantid.System, RoleNameAdmin)

	if idEmpty == "" {
		t.Fatal("SystemRoleID(\"\", admin) returned empty string")
	}
	if idEmpty != idSystem {
		t.Errorf("SystemRoleID(\"\", admin) = %q, want %q (same as SystemTenantID)", idEmpty, idSystem)
	}
}

// TestSystemRoleID_DifferentTenants verifies that different tenant ids
// produce different role ids for the same role name.
func TestSystemRoleID_DifferentTenants(t *testing.T) {
	id1 := SystemRoleID("tenant-a", RoleNameAdmin)
	id2 := SystemRoleID("tenant-b", RoleNameAdmin)

	if id1 == id2 {
		t.Errorf("SystemRoleID must differ between tenants, both returned %q", id1)
	}
}

// TestSystemRoleID_DeterministicForAdmin verifies that calling
// SystemRoleID twice with the same tenant and the Admin role name yields
// the same UUID. (TestSystemRoleID_Deterministic in store_test.go covers
// the same property across all six canonical role names.)
func TestSystemRoleID_DeterministicForAdmin(t *testing.T) {
	id1 := SystemRoleID("acme", RoleNameAdmin)
	id2 := SystemRoleID("acme", RoleNameAdmin)

	if id1 != id2 {
		t.Errorf("SystemRoleID must be deterministic: first=%q, second=%q", id1, id2)
	}
}

// TestSystemRoleID_DifferentRoleNames verifies that different role names
// in the same tenant produce different role ids.
func TestSystemRoleID_DifferentRoleNames(t *testing.T) {
	idAdmin := SystemRoleID("acme", RoleNameAdmin)
	idAuditor := SystemRoleID("acme", RoleNameAuditor)

	if idAdmin == idAuditor {
		t.Errorf("SystemRoleID must differ between role names, both returned %q", idAdmin)
	}
}

// TestCreateRole_NameIndexPutFails verifies CreateRole returns a StoreError
// when writing the name index fails (3rd Put).
func TestCreateRole_NameIndexPutFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 1, // name claim(1), primary(2), tenant index(3) — fail the claim
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
