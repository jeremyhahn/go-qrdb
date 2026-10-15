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
)

// ---------------------------------------------------------------------------
// User passwords:write / passwords:delete — self-scoped (owner ruling,
// 2026-10-15)
//
// The User role changes and removes its own password. These pin the
// AUTHORIZATION OUTCOME against the built-in User grant itself, not a
// hand-written permission list, with the target tenant injected separately
// from the identity's: a ScopeSelf check whose target is the caller's own
// tenant compares the tenant to itself and admits everything, so only the
// denial for another tenant shows the scope is enforced.
// ---------------------------------------------------------------------------

// userPasswordActions are the password actions the User role must hold.
var userPasswordActions = []string{"write", "delete"}

func newBuiltinUserAuthorizer() *RBACAuthorizer {
	return New(newMockRoleService(map[string][]permission.Permission{
		"user": permission.RolePermissions(permission.RoleUser),
	}))
}

func TestAuthorize_UserPasswords_AdmittedForOwnTenant(t *testing.T) {
	authz := newBuiltinUserAuthorizer()
	id := &auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"user"}}
	for _, action := range userPasswordActions {
		if err := authz.Authorize(ctxFor("acme"), id, "passwords", action); err != nil {
			t.Errorf("passwords:%s: user must change and remove its own password, got %v", action, err)
		}
	}
}

func TestAuthorize_UserPasswords_DeniedForAnotherTenant(t *testing.T) {
	authz := newBuiltinUserAuthorizer()
	id := &auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"user"}}
	for _, action := range userPasswordActions {
		err := authz.Authorize(ctxFor("other"), id, "passwords", action)
		if err == nil {
			t.Errorf("passwords:%s: user must not reach another tenant's passwords", action)
			continue
		}
		var denied *PermissionDeniedError
		if !errors.As(err, &denied) {
			t.Errorf("passwords:%s: expected *PermissionDeniedError, got %T: %v", action, err, err)
			continue
		}
		if denied.Reason != "self-scope permission: identity not in target tenant" {
			t.Errorf("passwords:%s: denied for the wrong reason %q: the grant must be held and the scope must refuse",
				action, denied.Reason)
		}
	}
}

func TestAuthorize_UserPasswords_GenerateStillDenied(t *testing.T) {
	authz := newBuiltinUserAuthorizer()
	id := &auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"user"}}
	err := authz.Authorize(ctxFor("acme"), id, "passwords", "generate")
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("passwords:generate: user must not mint credentials, got %v", err)
	}
}
