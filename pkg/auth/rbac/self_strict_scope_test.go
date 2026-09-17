// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package rbac

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// ScopeSelfStrict — every tenant is its own boundary, the system tenant too
//
// The permission-package tests pin the SCOPE. These pin the AUTHORIZATION
// OUTCOME, which is the property that actually matters: escrow was ScopeSelf,
// whose system-tenant lift admitted a platform admin against every tenant, so
// an identity in the system tenant could recover ANY tenant's escrowed key
// material. That is the case below that would have passed before and must
// deny now.
// ---------------------------------------------------------------------------

// A system-tenant identity is admitted against its OWN tenant. This is the
// half ScopeTenantOnly gets wrong: it would deny the system tenant outright,
// and the system tenant legitimately escrows its own keys.
func TestAuthorize_SelfStrict_SystemIdentity_AdmittedForOwnTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {permission.EscrowRecover},
	})
	authz := New(svc)
	sys := &auth.Identity{
		Subject:  "platform-admin",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{"admin"},
	}
	if err := authz.Authorize(ctxFor(tenant.SystemTenantID), sys, "escrow", "recover"); err != nil {
		t.Fatalf("system identity must reach its OWN escrowed material: %v", err)
	}
}

// ...and DENIED against another tenant. Under ScopeSelf this passed.
func TestAuthorize_SelfStrict_SystemIdentity_DeniedForAnotherTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {permission.EscrowRecover},
	})
	authz := New(svc)
	sys := &auth.Identity{
		Subject:  "platform-admin",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{"admin"},
	}
	err := authz.Authorize(ctxFor("acme"), sys, "escrow", "recover")
	if err == nil {
		t.Fatal("system identity must NOT recover another tenant's escrowed key material")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
	}
}

// A tenant-local identity behaves as it always did: own tenant admits.
func TestAuthorize_SelfStrict_TenantIdentity_AdmittedForOwnTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {permission.EscrowCreate},
	})
	authz := New(svc)
	id := &auth.Identity{Subject: "acme-admin", TenantID: "acme", Roles: []string{"admin"}}
	if err := authz.Authorize(ctxFor("acme"), id, "escrow", "create"); err != nil {
		t.Fatalf("tenant identity must reach its own tenant: %v", err)
	}
}

// ...and another tenant denies.
func TestAuthorize_SelfStrict_TenantIdentity_DeniedForAnotherTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {permission.EscrowCreate},
	})
	authz := New(svc)
	id := &auth.Identity{Subject: "acme-admin", TenantID: "acme", Roles: []string{"admin"}}
	err := authz.Authorize(ctxFor("other"), id, "escrow", "create")
	if err == nil {
		t.Fatal("tenant identity must not cross into another tenant")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
	}
}

// An identity with an EMPTY TenantID is system-scoped by omission
// (tenant.IsSystemTenant treats "" as system), so it must be confined the same
// way. This is the system identity a caller reaches without meaning to.
func TestAuthorize_SelfStrict_EmptyTenantIdentity_DeniedForAnotherTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {permission.EscrowRecover},
	})
	authz := New(svc)
	id := &auth.Identity{Subject: "no-tenant", TenantID: "", Roles: []string{"admin"}}
	err := authz.Authorize(ctxFor("acme"), id, "escrow", "recover")
	if err == nil {
		t.Fatal("an identity with no tenant must not reach another tenant's escrowed material")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
	}
}
