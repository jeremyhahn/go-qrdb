// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package user

import (
	"context"
	"errors"
	"testing"
)

// TestEnableUser_PutFails_Coverage verifies that EnableUser propagates a store Put error.
func TestEnableUser_PutFails_Coverage(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 4, // first 3 Puts are CreateUser (primary, tenant index, username index)
		putErr:    errors.New("disk full"),
	}
	svc, err := NewStore(fkv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "alice",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// The next Put (4th overall) will fail — that is the EnableUser Put.
	if err := svc.EnableUser(ctx, u.ID); err == nil {
		t.Fatal("expected error from EnableUser when Put fails")
	} else {
		var se *StoreError
		if !errors.As(err, &se) {
			t.Fatalf("expected *StoreError, got %T: %v", err, err)
		}
		if se.Op != "enable" {
			t.Errorf("Op: got %q, want %q", se.Op, "enable")
		}
	}
}

// TestDisableUser_PutFails_Coverage verifies that DisableUser propagates a store Put error.
func TestDisableUser_PutFails_Coverage(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 4, // first 3 Puts are CreateUser (primary, tenant index, username index)
		putErr:    errors.New("disk full"),
	}
	svc, err := NewStore(fkv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "bob",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// The next Put (4th overall) will fail — that is the DisableUser Put.
	if err := svc.DisableUser(ctx, u.ID); err == nil {
		t.Fatal("expected error from DisableUser when Put fails")
	} else {
		var se *StoreError
		if !errors.As(err, &se) {
			t.Fatalf("expected *StoreError, got %T: %v", err, err)
		}
		if se.Op != "disable" {
			t.Errorf("Op: got %q, want %q", se.Op, "disable")
		}
	}
}

// TestListUsers_ScanError verifies that ListUsers propagates a scan error.
func TestListUsers_ScanError(t *testing.T) {
	fkv := &faultKVStore{
		inner:   newMemKVStore(),
		scanErr: errors.New("scan failed"),
	}
	svc, err := NewStore(fkv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	_, err = svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error from ListUsers when scan fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestCreateUser_UsernameIndexPutFails verifies that CreateUser returns a
// StoreError when writing the username index fails.
func TestCreateUser_UsernameIndexPutFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 3, // primary(1), tenant index(2), username index(3) — fail 3rd
		putErr:    errors.New("disk full"),
	}
	svc, err := NewStore(fkv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	_, err = svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "charlie",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error on username index Put")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestUpdateUser_PutPrimaryFails_Coverage verifies that UpdateUser propagates a
// Put error when writing back the updated primary record.
func TestUpdateUser_PutPrimaryFails_Coverage(t *testing.T) {
	inner := newMemKVStore()
	fkv := &faultKVStore{
		inner:     inner,
		putFailAt: 0, // no put failures during create
	}
	svc, err := NewStore(fkv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "dave",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Now make all subsequent Puts fail.
	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("disk error")

	_, err = svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:      u.ID,
		RoleIDs: []string{"admin"},
	})
	if err == nil {
		t.Fatal("expected error from UpdateUser when Put fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}
