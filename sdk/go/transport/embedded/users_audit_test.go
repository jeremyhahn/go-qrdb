// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The Phase 3 prompt requires UserClient and RoleClient to expose enough
// surface for Phase 2 self-service registration:
//
//   - CreateUser  (with TenantID and initial RoleIDs)
//   - GrantRole   (= RoleClient.AssignRole)
//   - ListByTenant (= UserClient.ListUsers with TenantID filter)
//
// All three already exist on the existing interfaces (Phase 0 + earlier).
// These tests pin the contract so future refactors don't drift.

func TestUserAudit_CreateUser_AcceptsTenantAndRoles(t *testing.T) {
	captured := &user.CreateUserRequest{WithoutEmailLoginKey: true}
	mock := &mockUserServicer{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			*captured = *req
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock), WithRoleService(tenantRoleServicer("acme")))

	_, err := tr.CreateUser(tenant.WithTenantID(context.Background(), "acme"), &transport.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "bob",
		TenantID: "acme",
		RoleIDs:  []string{"role-user", "role-admin"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if captured.TenantID != "acme" {
		t.Errorf("expected TenantID 'acme', got %q", captured.TenantID)
	}
	if len(captured.RoleIDs) != 2 {
		t.Errorf("expected 2 RoleIDs, got %d", len(captured.RoleIDs))
	}
}

func TestUserAudit_ListUsers_FiltersByTenant(t *testing.T) {
	captured := &user.ListUsersRequest{}
	mock := &mockUserServicer{
		listUsersFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			*captured = *req
			return &user.ListUsersResponse{Users: []*user.User{testUser()}, Total: 1}, nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	_, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{
		TenantID: "acme",
		Page:     2,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if captured.TenantID != "acme" {
		t.Errorf("expected TenantID 'acme', got %q", captured.TenantID)
	}
	if captured.Page != 2 || captured.PageSize != 10 {
		t.Errorf("expected Page=2 PageSize=10, got Page=%d PageSize=%d", captured.Page, captured.PageSize)
	}
}

func TestRoleAudit_AssignRole_AddsRoleIdempotent(t *testing.T) {
	stored := *testUser() // copy
	getFn := func(_ context.Context, _ uint64) (*user.User, error) {
		u := stored
		u.RoleIDs = append([]string(nil), stored.RoleIDs...)
		return &u, nil
	}
	updateFn := func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
		stored.RoleIDs = req.RoleIDs
		return &stored, nil
	}
	roleSvc := &mockRoleServicer{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			r := *testRole()
			r.ID = id
			return &r, nil
		},
	}
	userSvc := &mockUserServicer{
		getUserFn:    getFn,
		updateUserFn: updateFn,
	}
	tr := newAuthTransport(t, WithUserService(userSvc), WithRoleService(roleSvc))

	if err := tr.AssignRole(context.Background(), 1001, "role-extra"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if len(stored.RoleIDs) != 2 {
		t.Fatalf("expected 2 roles after Assign, got %d", len(stored.RoleIDs))
	}

	// Idempotent — second call leaves the count at 2.
	if err := tr.AssignRole(context.Background(), 1001, "role-extra"); err != nil {
		t.Fatalf("AssignRole idempotent: %v", err)
	}
	if len(stored.RoleIDs) != 2 {
		t.Errorf("expected idempotent assign, role count drifted to %d", len(stored.RoleIDs))
	}
}

func TestRoleAudit_AssignRole_RoleLookupFails(t *testing.T) {
	roleSvc := &mockRoleServicer{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return nil, errors.New("role lookup failed")
		},
	}
	userSvc := &mockUserServicer{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) { return testUser(), nil },
	}
	tr := newAuthTransport(t, WithUserService(userSvc), WithRoleService(roleSvc))

	err := tr.AssignRole(context.Background(), 1001, "missing")
	if err == nil {
		t.Fatal("expected role lookup error to surface")
	}
}
