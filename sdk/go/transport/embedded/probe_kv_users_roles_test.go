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

// This file is the probe the coordinator asked for directly: a
// tenant-scoped identity cannot read, write, or delete another tenant's
// KV, users, or roles through the embedded transport.
package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedProbe_KVRequiresAPermissionGrant is the KV half of the probe.
// KV carries no cross-tenant target of its own -- the service layer resolves
// every call against the caller's own (ctx-bound) tenant, exactly as the
// data plane does on every network transport (see
// docs/identity-rbac/architecture.md: "the whole data plane ... against the
// caller's own tenant") -- so there is no "acme reads globex's key" request
// shape to deny. What this fix closes is the OTHER half: before it, Put,
// Get and Delete ran for ANY caller, holding ANY permission or none at all.
// This pins that an identity holding none of kv:put/kv:get/kv:delete is
// refused every one of them, and one holding the built-in Operator role --
// the data-plane role that carries them (Admin, deliberately, does not:
// pkg/permission/builtin_role_grants.go keeps the administrative and data
// planes separate) -- is admitted.
func TestEmbeddedProbe_KVRequiresAPermissionGrant(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	mw := authmw.New(rbac.New(roles))
	tr := newExtTransport(t, WithAuthMiddleware(mw))

	noRole := identityCtx("acme")
	if err := tr.Put(noRole, "k", []byte("v")); err == nil {
		t.Error("Put succeeded for an identity holding no permission at all")
	}
	if _, err := tr.Get(noRole, "k"); err == nil {
		t.Error("Get succeeded for an identity holding no permission at all")
	}
	if err := tr.Delete(noRole, "k"); err == nil {
		t.Error("Delete succeeded for an identity holding no permission at all")
	}

	operator := ctxFor(&auth.Identity{
		Subject:  "op",
		TenantID: "acme",
		Roles:    []string{role.SystemRoleID("acme", role.RoleNameOperator)},
	})
	if err := tr.Put(operator, "k", []byte("v")); err != nil {
		t.Fatalf("Put must be admitted for the built-in Operator role: %v", err)
	}
	got, err := tr.Get(operator, "k")
	if err != nil {
		t.Fatalf("Get must be admitted for the built-in Operator role: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get returned %q, want %q", got, "v")
	}
	if err := tr.Delete(operator, "k"); err != nil {
		t.Fatalf("Delete must be admitted for the built-in Operator role: %v", err)
	}
}

// probeUserRoleRecords is a mockUserServicer/mockRoleServicer pair seeded
// with one user and one role in EACH of two tenants, so AuthMiddleware's
// UserTarget/RoleTarget resolution (reading the record, exactly as the
// network transports' route-target extractors do) resolves each id to its
// OWNING tenant rather than the caller's.
func probeUserRoleRecords() (*mockUserServicer, *mockRoleServicer) {
	users := map[uint64]*user.User{
		1: {ID: 1, Username: "acme-user", TenantID: "acme"},
		2: {ID: 2, Username: "globex-user", TenantID: "globex"},
	}
	roles := map[string]*role.Role{
		"acme-role":   {ID: "acme-role", TenantID: "acme"},
		"globex-role": {ID: "globex-role", TenantID: "globex"},
	}
	userSvc := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			u, ok := users[id]
			if !ok {
				return nil, &user.UserNotFoundError{ID: id}
			}
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			u, ok := users[req.ID]
			if !ok {
				return nil, &user.UserNotFoundError{ID: req.ID}
			}
			return u, nil
		},
		deleteUserFn: func(_ context.Context, id uint64) error {
			if _, ok := users[id]; !ok {
				return &user.UserNotFoundError{ID: id}
			}
			return nil
		},
	}
	roleSvc := &mockRoleServicer{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			r, ok := roles[id]
			if !ok {
				return nil, &role.RoleNotFoundError{ID: id}
			}
			return r, nil
		},
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return nil, nil
		},
	}
	return userSvc, roleSvc
}

// TestEmbeddedProbe_CannotReadWriteDeleteAnotherTenantsUser is the users
// half of the probe: acme's built-in Admin -- which holds users:read,
// users:update, and users:delete in its OWN tenant -- is refused all three
// against globex's user id 2, and admitted against acme's own id 1.
func TestEmbeddedProbe_CannotReadWriteDeleteAnotherTenantsUser(t *testing.T) {
	userSvc, roleSvc := probeUserRoleRecords()
	roles := authtest.BuiltInRoles(t, "acme", "globex")
	mw := authmw.New(rbac.New(roles), authmw.WithUserService(userSvc), authmw.WithRoleService(roleSvc))
	tr := newExtTransport(t, WithUserService(userSvc), WithRoleService(roleSvc), WithAuthMiddleware(mw))

	acmeAdmin := ctxFor(authtest.AdminIdentity("acme", "alice"))

	if _, err := tr.GetUser(acmeAdmin, 2); err == nil {
		t.Error("acme's Admin read globex's user")
	} else if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("GetUser(globex): err = %v, want an authorization denial", err)
	}
	if _, err := tr.UpdateUser(acmeAdmin, &transport.UpdateUserRequest{ID: 2, Name: "pwned"}); err == nil {
		t.Error("acme's Admin updated globex's user")
	} else if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("UpdateUser(globex): err = %v, want an authorization denial", err)
	}
	if err := tr.DeleteUser(acmeAdmin, 2); err == nil {
		t.Error("acme's Admin deleted globex's user")
	} else if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("DeleteUser(globex): err = %v, want an authorization denial", err)
	}

	// Acting on its own tenant's user must still work.
	if _, err := tr.GetUser(acmeAdmin, 1); err != nil {
		t.Fatalf("acme's Admin must be able to read its own tenant's user: %v", err)
	}
}

// TestEmbeddedProbe_CannotReadWriteDeleteAnotherTenantsRole mirrors the
// users probe for roles: GetRole, UpdateRole, and DeleteRole against
// globex's role id are refused, and admitted against acme's own.
func TestEmbeddedProbe_CannotReadWriteDeleteAnotherTenantsRole(t *testing.T) {
	_, roleSvc := probeUserRoleRecords()
	roles := authtest.BuiltInRoles(t, "acme", "globex")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleService(roleSvc))
	tr := newExtTransport(t, WithRoleService(roleSvc), WithAuthMiddleware(mw))

	acmeAdmin := ctxFor(authtest.AdminIdentity("acme", "alice"))

	if _, err := tr.GetRole(acmeAdmin, "globex-role"); err == nil {
		t.Error("acme's Admin read globex's role")
	} else if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("GetRole(globex): err = %v, want an authorization denial", err)
	}
	if _, err := tr.UpdateRole(acmeAdmin, &transport.UpdateRoleRequest{ID: "globex-role", Name: "pwned"}); err == nil {
		t.Error("acme's Admin updated globex's role")
	} else if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("UpdateRole(globex): err = %v, want an authorization denial", err)
	}
	if err := tr.DeleteRole(acmeAdmin, "globex-role"); err == nil {
		t.Error("acme's Admin deleted globex's role")
	} else if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("DeleteRole(globex): err = %v, want an authorization denial", err)
	}

	if _, err := tr.GetRole(acmeAdmin, "acme-role"); err != nil {
		t.Fatalf("acme's Admin must be able to read its own tenant's role: %v", err)
	}
}
