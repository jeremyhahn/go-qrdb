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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// errMockRole is a sentinel error used by mock role service failures.
var errMockRole = errors.New("mock role error")

// newRoleServer creates a Server with the given role (and optional user)
// service mocks.
func newRoleServer(rs role.RoleServicer, us user.UserServicer) *Server {
	opts := []ServerOption{WithRoleService(rs)}
	if us != nil {
		opts = append(opts, WithUserService(us))
	}
	return NewServer(nil, nil, "localhost:0", opts...)
}

// ---------------------------------------------------------------------------
// handleCreateRole
// ---------------------------------------------------------------------------

func TestHandleCreateRole_Success(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	mock := &mockRoleService{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			if req.Name != "admin" {
				t.Fatalf("expected name 'admin', got %q", req.Name)
			}
			if req.TenantID != "tenant-1" {
				t.Fatalf("expected tenant 'tenant-1', got %q", req.TenantID)
			}
			if len(req.Permissions) != 2 {
				t.Fatalf("expected 2 permissions, got %d", len(req.Permissions))
			}
			return &role.Role{
				ID:          "role-001",
				Name:        req.Name,
				TenantID:    req.TenantID,
				Permissions: req.Permissions,
				CreatedAt:   now,
				UpdatedAt:   now,
			}, nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(createRoleWireRequest{
		Name:        "admin",
		TenantID:    "tenant-1",
		Permissions: []string{"kv:put", "kv:get"},
	})

	resp, err := srv.handleCreateRole(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out roleWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.ID != "role-001" {
		t.Fatalf("expected ID 'role-001', got %q", out.ID)
	}
	if out.Name != "admin" {
		t.Fatalf("expected name 'admin', got %q", out.Name)
	}
	if len(out.Permissions) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(out.Permissions))
	}
}

func TestHandleCreateRole_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, errMockRole
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(createRoleWireRequest{Name: "r"})

	_, err := srv.handleCreateRole(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleCreateRole_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(createRoleWireRequest{Name: "x"})

	_, err := srv.handleCreateRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleCreateRole_InvalidJSON(t *testing.T) {
	mock := &mockRoleService{}
	srv := newRoleServer(mock, nil)

	_, err := srv.handleCreateRole(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleGetRole
// ---------------------------------------------------------------------------

func TestHandleGetRole_Success(t *testing.T) {
	rl := testRole("role-002", "viewer", "tenant-1")
	mock := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			if id != "role-002" {
				t.Fatalf("expected ID 'role-002', got %q", id)
			}
			return rl, nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(getRoleWireRequest{ID: "role-002"})

	resp, err := srv.handleGetRole(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out roleWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.ID != "role-002" {
		t.Fatalf("expected ID 'role-002', got %q", out.ID)
	}
	if out.Name != "viewer" {
		t.Fatalf("expected name 'viewer', got %q", out.Name)
	}
}

func TestHandleGetRole_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return nil, errMockRole
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(getRoleWireRequest{ID: "missing"})

	_, err := srv.handleGetRole(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleGetRole_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(getRoleWireRequest{ID: "x"})

	_, err := srv.handleGetRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleUpdateRole
// ---------------------------------------------------------------------------

func TestHandleUpdateRole_Success(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	mock := &mockRoleService{
		updateRoleFn: func(_ context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
			if req.ID != "role-003" {
				t.Fatalf("expected ID 'role-003', got %q", req.ID)
			}
			return &role.Role{
				ID:          req.ID,
				Name:        req.Name,
				TenantID:    "tenant-1",
				Permissions: req.Permissions,
				CreatedAt:   now,
				UpdatedAt:   now,
			}, nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(updateRoleWireRequest{
		ID:          "role-003",
		Name:        "editor",
		Permissions: []string{"kv:put", "kv:get", "kv:delete"},
	})

	resp, err := srv.handleUpdateRole(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out roleWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Name != "editor" {
		t.Fatalf("expected name 'editor', got %q", out.Name)
	}
	if len(out.Permissions) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(out.Permissions))
	}
}

func TestHandleUpdateRole_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		updateRoleFn: func(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
			return nil, errMockRole
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(updateRoleWireRequest{ID: "r"})

	_, err := srv.handleUpdateRole(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleUpdateRole_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(updateRoleWireRequest{ID: "x"})

	_, err := srv.handleUpdateRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleDeleteRole
// ---------------------------------------------------------------------------

func TestHandleDeleteRole_Success(t *testing.T) {
	mock := &mockRoleService{
		deleteRoleFn: func(_ context.Context, id string) error {
			if id != "role-004" {
				t.Fatalf("expected ID 'role-004', got %q", id)
			}
			return nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(deleteRoleWireRequest{ID: "role-004"})

	resp, err := srv.handleDeleteRole(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out statusWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", out.Status)
	}
}

func TestHandleDeleteRole_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		deleteRoleFn: func(_ context.Context, _ string) error {
			return errMockRole
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(deleteRoleWireRequest{ID: "role-004"})

	_, err := srv.handleDeleteRole(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleDeleteRole_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(deleteRoleWireRequest{ID: "x"})

	_, err := srv.handleDeleteRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleListRoles
// ---------------------------------------------------------------------------

func TestHandleListRoles_Success(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	mock := &mockRoleService{
		listRolesFn: func(_ context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			if req.TenantID != "tenant-1" {
				t.Fatalf("expected tenant 'tenant-1', got %q", req.TenantID)
			}
			return &role.ListRolesResponse{
				Roles: []*role.Role{
					{ID: "r1", Name: "admin", TenantID: "tenant-1", Permissions: []permission.Permission{"kv:put"}, CreatedAt: now, UpdatedAt: now},
					{ID: "r2", Name: "viewer", TenantID: "tenant-1", Permissions: []permission.Permission{"kv:get"}, CreatedAt: now, UpdatedAt: now},
				},
				Total:   2,
				HasMore: false,
			}, nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(listRolesWireRequest{
		TenantID: "tenant-1",
		Page:     1,
		PageSize: 20,
	})

	resp, err := srv.handleListRoles(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out listRolesWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(out.Roles))
	}
	if out.Total != 2 {
		t.Fatalf("expected total=2, got %d", out.Total)
	}
	if out.HasMore {
		t.Fatal("expected hasMore=false")
	}
}

func TestHandleListRoles_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		listRolesFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			return nil, errMockRole
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(listRolesWireRequest{TenantID: "t"})

	_, err := srv.handleListRoles(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleListRoles_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(listRolesWireRequest{TenantID: "t"})

	_, err := srv.handleListRoles(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleAssignRole
// ---------------------------------------------------------------------------

func TestHandleAssignRole_Success(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	existingUser := &user.User{
		ID:        1010,
		Username:  "alice",
		TenantID:  "tenant-1",
		RoleIDs:   []string{"role-a"},
		Lifecycle: user.LifecycleActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			if id != "role-b" {
				t.Fatalf("expected role ID 'role-b', got %q", id)
			}
			return testRole("role-b", "editor", "tenant-1"), nil
		},
	}

	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != 1010 {
				t.Fatalf("expected user ID 1010, got %d", id)
			}
			return existingUser, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			if len(req.RoleIDs) != 2 {
				t.Fatalf("expected 2 roles after assign, got %d", len(req.RoleIDs))
			}
			return &user.User{
				ID:        1010,
				Username:  "alice",
				TenantID:  "tenant-1",
				RoleIDs:   req.RoleIDs,
				Lifecycle: user.LifecycleActive,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{
		UserID: 1010,
		RoleID: "role-b",
	})

	resp, err := srv.handleAssignRole(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.RoleIDs) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(out.RoleIDs))
	}
}

func TestHandleAssignRole_AlreadyAssigned(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	existingUser := &user.User{
		ID:        1011,
		Username:  "bob",
		TenantID:  "tenant-1",
		RoleIDs:   []string{"role-a"},
		Lifecycle: user.LifecycleActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole("role-a", "admin", "tenant-1"), nil
		},
	}

	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return existingUser, nil
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{
		UserID: 1011,
		RoleID: "role-a",
	})

	resp, err := srv.handleAssignRole(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	// Role already assigned, should return existing user unchanged.
	if len(out.RoleIDs) != 1 {
		t.Fatalf("expected 1 role (unchanged), got %d", len(out.RoleIDs))
	}
}

func TestHandleAssignRole_RoleNotFound(t *testing.T) {
	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return nil, errMockRole
		},
	}

	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return &user.User{ID: id, TenantID: "t1"}, nil
		},
	}
	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 1, RoleID: "bad"})

	_, err := srv.handleAssignRole(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleAssignRole_NilRoleService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserService(&mockUserService{}))
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 1, RoleID: "r"})

	_, err := srv.handleAssignRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleAssignRole_NilUserService(t *testing.T) {
	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole("r", "admin", "t"), nil
		},
	}
	srv := newRoleServer(mockRole, nil)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 1, RoleID: "r"})

	_, err := srv.handleAssignRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleUnassignRole
// ---------------------------------------------------------------------------

func TestHandleUnassignRole_Success(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	existingUser := &user.User{
		ID:        1012,
		Username:  "carol",
		TenantID:  "tenant-1",
		RoleIDs:   []string{"role-a", "role-b"},
		Lifecycle: user.LifecycleActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockRole := &mockRoleService{}
	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != 1012 {
				t.Fatalf("expected user ID 1012, got %d", id)
			}
			return existingUser, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			if len(req.RoleIDs) != 1 {
				t.Fatalf("expected 1 role after unassign, got %d", len(req.RoleIDs))
			}
			if req.RoleIDs[0] != "role-a" {
				t.Fatalf("expected remaining role 'role-a', got %q", req.RoleIDs[0])
			}
			return &user.User{
				ID:        1012,
				Username:  "carol",
				TenantID:  "tenant-1",
				RoleIDs:   req.RoleIDs,
				Lifecycle: user.LifecycleActive,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{
		UserID: 1012,
		RoleID: "role-b",
	})

	resp, err := srv.handleUnassignRole(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.RoleIDs) != 1 {
		t.Fatalf("expected 1 role, got %d", len(out.RoleIDs))
	}
}

func TestHandleUnassignRole_UserNotFound(t *testing.T) {
	mockRole := &mockRoleService{}
	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, errMockUser
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 9999, RoleID: "r"})

	_, err := srv.handleUnassignRole(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleUnassignRole_NilRoleService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserService(&mockUserService{}))
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 1, RoleID: "r"})

	_, err := srv.handleUnassignRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleUnassignRole_NilUserService(t *testing.T) {
	mockRole := &mockRoleService{}
	srv := newRoleServer(mockRole, nil)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 1, RoleID: "r"})

	_, err := srv.handleUnassignRole(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleListPermissions
// ---------------------------------------------------------------------------

func TestHandleListPermissions_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")

	resp, err := srv.handleListPermissions(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out []*permissionCategoryWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected at least one permission category")
	}

	// Verify that at least the KV Service category exists with expected
	// permissions from the global registry.
	found := false
	for _, cat := range out {
		if cat.Name == "KV Service" {
			found = true
			if len(cat.Permissions) == 0 {
				t.Fatal("expected KV Service to have permissions")
			}
			if cat.Group != "Data Services" {
				t.Fatalf("expected group 'Data Services', got %q", cat.Group)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected to find 'KV Service' category")
	}
}

func TestHandleListPermissions_EmptyPayload(t *testing.T) {
	// handleListPermissions ignores payload; ensure it does not fail
	// with empty or nil payloads.
	srv := NewServer(nil, nil, "localhost:0")

	resp, err := srv.handleListPermissions(context.Background(), []byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out []*permissionCategoryWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected at least one permission category")
	}
}

// ---------------------------------------------------------------------------
// roleToWireResponse
// ---------------------------------------------------------------------------

func TestRoleToWireResponse_NilInput(t *testing.T) {
	result := roleToWireResponse(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestRoleToWireResponse_AllFields(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	rl := &role.Role{
		ID:          "role-1",
		Name:        "admin",
		TenantID:    "tenant-1",
		Permissions: []permission.Permission{"kv:put", "kv:get", "admin:drain"},
		System:      true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	result := roleToWireResponse(rl)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.ID != "role-1" {
		t.Fatalf("expected ID 'role-1', got %q", result.ID)
	}
	if result.Name != "admin" {
		t.Fatalf("expected name 'admin', got %q", result.Name)
	}
	if len(result.Permissions) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(result.Permissions))
	}
	if !result.System {
		t.Fatal("expected system to be true")
	}
	expectedTime := now.Format(time.RFC3339)
	if result.CreatedAt != expectedTime {
		t.Fatalf("expected createdAt %q, got %q", expectedTime, result.CreatedAt)
	}
}

func TestRoleToWireResponse_EmptyPermissions(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := &role.Role{
		ID:          "role-2",
		Name:        "viewer",
		TenantID:    "tenant-1",
		Permissions: nil,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	result := roleToWireResponse(rl)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Permissions) != 0 {
		t.Fatalf("expected 0 permissions, got %d", len(result.Permissions))
	}
}

// ---------------------------------------------------------------------------
// stringsToPermissions
// ---------------------------------------------------------------------------

func TestStringsToPermissions_NilInput(t *testing.T) {
	result := stringsToPermissions(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestStringsToPermissions_EmptySlice(t *testing.T) {
	result := stringsToPermissions([]string{})
	if len(result) != 0 {
		t.Fatalf("expected 0 permissions, got %d", len(result))
	}
}

func TestStringsToPermissions_WithValues(t *testing.T) {
	input := []string{"kv:put", "kv:get", "admin:status"}
	result := stringsToPermissions(input)
	if len(result) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(result))
	}
	if result[0] != permission.Permission("kv:put") {
		t.Fatalf("expected 'kv:put', got %q", result[0])
	}
}

// ---------------------------------------------------------------------------
// InvalidJSON tests for remaining role handlers
// ---------------------------------------------------------------------------

func TestHandleGetRole_InvalidJSON(t *testing.T) {
	srv := newRoleServer(&mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) { return nil, nil },
	}, nil)
	_, err := srv.handleGetRole(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleUpdateRole_InvalidJSON(t *testing.T) {
	srv := newRoleServer(&mockRoleService{
		updateRoleFn: func(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) { return nil, nil },
	}, nil)
	_, err := srv.handleUpdateRole(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleDeleteRole_InvalidJSON(t *testing.T) {
	srv := newRoleServer(&mockRoleService{
		deleteRoleFn: func(_ context.Context, _ string) error { return nil },
	}, nil)
	_, err := srv.handleDeleteRole(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleListRoles_InvalidJSON(t *testing.T) {
	srv := newRoleServer(&mockRoleService{
		listRolesFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			return nil, nil
		},
	}, nil)
	_, err := srv.handleListRoles(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleAssignRole_InvalidJSON(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0",
		WithRoleService(&mockRoleService{
			getRoleFn: func(_ context.Context, _ string) (*role.Role, error) { return nil, nil },
		}),
		WithUserService(&mockUserService{
			getUserFn: func(_ context.Context, _ uint64) (*user.User, error) { return nil, nil },
			updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
				return nil, nil
			},
		}),
	)
	_, err := srv.handleAssignRole(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleAssignRole_UserNotFound(t *testing.T) {
	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole("role-x", "admin", "t"), nil
		},
	}
	userErr := &testError{msg: "user not found"}
	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, userErr
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 9999, RoleID: "role-x"})

	_, err := srv.handleAssignRole(context.Background(), payload)
	if !errors.Is(err, userErr) {
		t.Fatalf("expected the user lookup error, got %T: %v", err, err)
	}
}

func TestHandleAssignRole_UpdateUserError(t *testing.T) {
	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole("role-z", "viewer", "t"), nil
		},
	}
	updateErr := &testError{msg: "update user failed"}
	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return testUser(99, "charlie", "t"), nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, updateErr
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 99, RoleID: "role-z"})

	_, err := srv.handleAssignRole(context.Background(), payload)
	if !errors.Is(err, updateErr) {
		t.Fatalf("expected the UpdateUser error, got %T: %v", err, err)
	}
}

func TestHandleUnassignRole_InvalidJSON(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0",
		WithRoleService(&mockRoleService{
			getRoleFn: func(_ context.Context, _ string) (*role.Role, error) { return nil, nil },
		}),
		WithUserService(&mockUserService{
			getUserFn: func(_ context.Context, _ uint64) (*user.User, error) { return nil, nil },
			updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
				return nil, nil
			},
		}),
	)
	_, err := srv.handleUnassignRole(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleGetRoleByName
// ---------------------------------------------------------------------------

func TestHandleGetRoleByName_Success(t *testing.T) {
	rl := testRole("role-byname", "auditor", "tenant-1")
	mock := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			if tenantID != "tenant-1" {
				t.Fatalf("expected tenantID 'tenant-1', got %q", tenantID)
			}
			if name != "auditor" {
				t.Fatalf("expected name 'auditor', got %q", name)
			}
			return rl, nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(getRoleByNameWireRequest{
		TenantID: "tenant-1",
		Name:     "auditor",
	})

	resp, err := srv.handleGetRoleByName(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out roleWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.ID != "role-byname" {
		t.Fatalf("expected ID 'role-byname', got %q", out.ID)
	}
	if out.Name != "auditor" {
		t.Fatalf("expected name 'auditor', got %q", out.Name)
	}
	if out.TenantID != "tenant-1" {
		t.Fatalf("expected tenantID 'tenant-1', got %q", out.TenantID)
	}
}

func TestHandleGetRoleByName_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, _, _ string) (*role.Role, error) {
			return nil, errMockRole
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(getRoleByNameWireRequest{
		TenantID: "tenant-1",
		Name:     "missing",
	})

	_, err := srv.handleGetRoleByName(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleGetRoleByName_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(getRoleByNameWireRequest{TenantID: "t", Name: "x"})

	_, err := srv.handleGetRoleByName(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleGetRoleByName_InvalidJSON(t *testing.T) {
	mock := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, _, _ string) (*role.Role, error) { return nil, nil },
	}
	srv := newRoleServer(mock, nil)

	_, err := srv.handleGetRoleByName(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleResolvePermissions
// ---------------------------------------------------------------------------

func TestHandleResolvePermissions_Success(t *testing.T) {
	mock := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
			if len(roleIDs) != 2 {
				t.Fatalf("expected 2 roleIDs, got %d", len(roleIDs))
			}
			return []permission.Permission{"kv:put", "kv:get", "admin:health"}, nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(resolvePermissionsWireRequest{
		RoleIDs: []string{"role-a", "role-b"},
	})

	resp, err := srv.handleResolvePermissions(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out resolvePermissionsWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Permissions) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(out.Permissions))
	}
	found := false
	for _, p := range out.Permissions {
		if p == "kv:put" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'kv:put' in permissions, got %v", out.Permissions)
	}
}

func TestHandleResolvePermissions_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, errMockRole
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(resolvePermissionsWireRequest{RoleIDs: []string{"r-x"}})

	_, err := srv.handleResolvePermissions(context.Background(), payload)
	if !errors.Is(err, errMockRole) {
		t.Fatalf("expected errMockRole, got %v", err)
	}
}

func TestHandleResolvePermissions_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(resolvePermissionsWireRequest{RoleIDs: []string{"r-1"}})

	_, err := srv.handleResolvePermissions(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleResolvePermissions_EmptyRoleIDs(t *testing.T) {
	mock := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
			if len(roleIDs) != 0 {
				t.Fatalf("expected empty roleIDs, got %d", len(roleIDs))
			}
			return []permission.Permission{}, nil
		},
	}

	srv := newRoleServer(mock, nil)
	payload, _ := json.Marshal(resolvePermissionsWireRequest{RoleIDs: []string{}})

	resp, err := srv.handleResolvePermissions(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out resolvePermissionsWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Permissions) != 0 {
		t.Fatalf("expected 0 permissions, got %d", len(out.Permissions))
	}
}

func TestHandleResolvePermissions_InvalidJSON(t *testing.T) {
	mock := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, nil
		},
	}
	srv := newRoleServer(mock, nil)

	_, err := srv.handleResolvePermissions(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}
