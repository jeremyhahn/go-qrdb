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
)

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
