// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package firstboot

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestPermissionSetsEqual pins the set semantics the built-in role provenance
// check relies on: order and repetition do not change what a role grants, but
// a missing or an extra permission does.
func TestPermissionSetsEqual(t *testing.T) {
	get, put, del := permission.KVGet, permission.KVPut, permission.KVDelete
	for _, tc := range []struct {
		name string
		a, b []permission.Permission
		want bool
	}{
		{name: "identical", a: []permission.Permission{get, put}, b: []permission.Permission{get, put}, want: true},
		{name: "order does not matter", a: []permission.Permission{get, put}, b: []permission.Permission{put, get}, want: true},
		{name: "repetition does not matter", a: []permission.Permission{get, get, put}, b: []permission.Permission{put, get}, want: true},
		{name: "both empty", a: nil, b: []permission.Permission{}, want: true},
		{name: "an extra permission", a: []permission.Permission{get, put, del}, b: []permission.Permission{get, put}, want: false},
		{name: "a missing permission", a: []permission.Permission{get}, b: []permission.Permission{get, put}, want: false},
		{name: "same size, different members", a: []permission.Permission{get, del}, b: []permission.Permission{get, put}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := permissionSetsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("permissionSetsEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// usersOnlyCreator implements UserCreator but NOT UserResolver.
type usersOnlyCreator struct{}

func (usersOnlyCreator) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, nil
}

// TestUserResolver_Precedence pins the three answers userResolver gives: an
// explicit Dependencies.UserResolver wins, Users is used when it can resolve
// too, and nil — which createAdminUser treats as "cannot verify" — otherwise.
func TestUserResolver_Precedence(t *testing.T) {
	explicit := &fakeUsers{}
	users := &fakeUsers{}

	if got := userResolver(Dependencies{Users: users, UserResolver: explicit}); got != UserResolver(explicit) {
		t.Errorf("explicit resolver not preferred: got %v", got)
	}
	if got := userResolver(Dependencies{Users: users}); got != UserResolver(users) {
		t.Errorf("Users that can resolve was not used: got %v", got)
	}
	if got := userResolver(Dependencies{Users: usersOnlyCreator{}}); got != nil {
		t.Errorf("want nil for a Users that cannot resolve, got %v", got)
	}
}

// scriptedAdminUsers answers CreateUser from a script and GetUserByUsername
// with a fixed record, so createAdminUser's collision handling can be driven
// through each branch.
type scriptedAdminUsers struct {
	createErrs []error // consumed one per call; exhausted means success
	lookup     *user.User
	calls      int
}

func (s *scriptedAdminUsers) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	s.calls++
	if len(s.createErrs) > 0 {
		err := s.createErrs[0]
		s.createErrs = s.createErrs[1:]
		return nil, err
	}
	return &user.User{ID: 99, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs, Lifecycle: user.LifecycleActive}, nil
}

func (s *scriptedAdminUsers) GetUserByUsername(context.Context, string, string) (*user.User, error) {
	if s.lookup == nil {
		return nil, &user.UserNotFoundError{Lookup: "username"}
	}
	return s.lookup, nil
}

func allBuiltInRolesPresent() *fakeRoles {
	return &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO: true, role.RoleNameAdmin: true, role.RoleNameOperator: true,
		role.RoleNameUser: true, role.RoleNameAuditor: true, role.RoleNameCustodian: true,
		role.RoleNameOwner: true,
	}}
}

// TestCreateAdminUser_IDCollisionWithAnotherUserRetries pins that a generated
// id that repeated some OTHER user's — no admin behind it — is retried with a
// fresh id instead of aborting first boot.
func TestCreateAdminUser_IDCollisionWithAnotherUserRetries(t *testing.T) {
	users := &scriptedAdminUsers{createErrs: []error{&user.UserIDExistsError{ID: 7, TenantID: tenant.SystemTenantID}}}
	result, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{createAlreadyErr: true}, Users: users, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: realCertPEM(t), key: []byte("KEY")}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !result.AdminCreated || users.calls != 2 {
		t.Errorf("AdminCreated = %v after %d CreateUser calls, want true after 2 (one collision, one retry)", result.AdminCreated, users.calls)
	}
}

// TestCreateAdminUser_IDCollisionRetriesAreBounded: a store that reports the
// collision for every id cannot loop first boot forever.
func TestCreateAdminUser_IDCollisionRetriesAreBounded(t *testing.T) {
	collide := &user.UserIDExistsError{ID: 7, TenantID: tenant.SystemTenantID}
	users := &scriptedAdminUsers{createErrs: []error{collide, collide, collide, collide, collide}}
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{createAlreadyErr: true}, Users: users, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}, Logger: discardLogger(),
	})
	var ace *AdminUserCreationError
	if !errors.As(err, &ace) || !errors.As(err, &collide) {
		t.Fatalf("Initialize err = %v, want AdminUserCreationError wrapping the id collision", err)
	}
	if users.calls != adminCreateAttempts {
		t.Errorf("CreateUser calls = %d, want %d", users.calls, adminCreateAttempts)
	}
}

// TestCreateAdminUser_UsernameSquatterIsRefusedWithoutRetry: a username held
// by a record that is not the admin is a squatter. A fresh id cannot fix it.
func TestCreateAdminUser_UsernameSquatterIsRefusedWithoutRetry(t *testing.T) {
	users := &scriptedAdminUsers{
		createErrs: []error{&user.UsernameExistsError{Username: adminUsername, TenantID: tenant.SystemTenantID}},
		lookup:     &user.User{ID: 5, Username: adminUsername, TenantID: tenant.SystemTenantID, RoleIDs: []string{"some-ordinary-role"}, Lifecycle: user.LifecycleActive},
	}
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{createAlreadyErr: true}, Users: users, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}, Logger: discardLogger(),
	})
	var ace *AdminUserCreationError
	if !errors.As(err, &ace) {
		t.Fatalf("Initialize err = %v, want AdminUserCreationError", err)
	}
	if users.calls != 1 {
		t.Errorf("CreateUser calls = %d, want 1: a squatted username is not retried", users.calls)
	}
}

// TestAdminUserAlreadyExists pins what "the admin" means when a collision is
// verified: every property first boot gives the account, with the lifecycle
// allowed to have moved on to any defined, non-terminal state.
func TestAdminUserAlreadyExists(t *testing.T) {
	const tenantID, adminRoleID = "system", "role-admin"
	admin := func(mut func(*user.User)) *user.User {
		u := &user.User{ID: 1, Username: adminUsername, TenantID: tenantID, RoleIDs: []string{adminRoleID, "role-owner"}, Lifecycle: user.LifecycleActive}
		if mut != nil {
			mut(u)
		}
		return u
	}
	for _, tc := range []struct {
		name string
		u    *user.User
		want bool
	}{
		{name: "the admin", u: admin(nil), want: true},
		{name: "suspended since by an operator", u: admin(func(u *user.User) { u.Lifecycle = user.LifecycleSuspended }), want: true},
		{name: "disabled since by an operator", u: admin(func(u *user.User) { u.Lifecycle = user.LifecycleDisabled }), want: true},
		{name: "missing the admin role", u: admin(func(u *user.User) { u.RoleIDs = []string{"role-owner"} }), want: false},
		{name: "the admin as the System user", u: admin(func(u *user.User) { u.System = true }), want: true},
		{name: "being deleted", u: admin(func(u *user.User) { u.Lifecycle = user.LifecycleDeleting }), want: false},
		{name: "unknown lifecycle", u: admin(func(u *user.User) { u.Lifecycle = "hibernating" }), want: false},
		{name: "no lifecycle", u: admin(func(u *user.User) { u.Lifecycle = "" }), want: false},
		{name: "another tenant's record", u: admin(func(u *user.User) { u.TenantID = "acme" }), want: false},
		{name: "another username", u: admin(func(u *user.User) { u.Username = "root" }), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := adminUserAlreadyExists(context.Background(), &scriptedAdminUsers{lookup: tc.u}, tenantID, adminRoleID)
			if got != tc.want {
				t.Errorf("adminUserAlreadyExists = %v, want %v", got, tc.want)
			}
		})
	}
	if adminUserAlreadyExists(context.Background(), nil, tenantID, adminRoleID) {
		t.Error("a nil resolver cannot verify anything")
	}
	if adminUserAlreadyExists(context.Background(), &scriptedAdminUsers{}, tenantID, adminRoleID) {
		t.Error("a failed lookup cannot verify anything")
	}
}
