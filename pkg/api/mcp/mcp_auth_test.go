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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Mock: UserServicer
// ---------------------------------------------------------------------------

type mockUserService struct {
	createUserFn        func(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
	getUserFn           func(ctx context.Context, id string) (*user.User, error)
	getUserByUsernameFn func(ctx context.Context, tenantID, username string) (*user.User, error)
	updateUserFn        func(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
	deleteUserFn        func(ctx context.Context, id string) error
	listUsersFn         func(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
	enableUserFn        func(ctx context.Context, id string) error
	disableUserFn       func(ctx context.Context, id string) error
}

func (m *mockUserService) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if m.createUserFn != nil {
		return m.createUserFn(ctx, req)
	}
	return &user.User{
		ID:        "u-001",
		Username:  req.Username,
		TenantID:  req.TenantID,
		RoleIDs:   req.RoleIDs,
		Enabled:   true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockUserService) GetUser(ctx context.Context, id string) (*user.User, error) {
	if m.getUserFn != nil {
		return m.getUserFn(ctx, id)
	}
	return &user.User{
		ID:        id,
		Username:  "alice",
		TenantID:  "default",
		RoleIDs:   []string{"r-001"},
		Enabled:   true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockUserService) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if m.getUserByUsernameFn != nil {
		return m.getUserByUsernameFn(ctx, tenantID, username)
	}
	return &user.User{
		ID:       "u-001",
		Username: username,
		TenantID: tenantID,
		Enabled:  true,
	}, nil
}

func (m *mockUserService) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if m.updateUserFn != nil {
		return m.updateUserFn(ctx, req)
	}
	u := &user.User{
		ID:        req.ID,
		Username:  req.Username,
		TenantID:  "default",
		RoleIDs:   req.RoleIDs,
		Enabled:   true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	if u.Username == "" {
		u.Username = "alice"
	}
	return u, nil
}

func (m *mockUserService) DeleteUser(ctx context.Context, id string) error {
	if m.deleteUserFn != nil {
		return m.deleteUserFn(ctx, id)
	}
	return nil
}

func (m *mockUserService) ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	if m.listUsersFn != nil {
		return m.listUsersFn(ctx, req)
	}
	return &user.ListUsersResponse{
		Users: []*user.User{
			{ID: "u-001", Username: "alice", TenantID: "default", Enabled: true},
			{ID: "u-002", Username: "bob", TenantID: "default", Enabled: true},
		},
		Total:   2,
		HasMore: false,
	}, nil
}

func (m *mockUserService) EnableUser(ctx context.Context, id string) error {
	if m.enableUserFn != nil {
		return m.enableUserFn(ctx, id)
	}
	return nil
}

func (m *mockUserService) DisableUser(ctx context.Context, id string) error {
	if m.disableUserFn != nil {
		return m.disableUserFn(ctx, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mock: RoleServicer
// ---------------------------------------------------------------------------

type mockRoleService struct {
	createRoleFn         func(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error)
	getRoleFn            func(ctx context.Context, id string) (*role.Role, error)
	getRoleByNameFn      func(ctx context.Context, tenantID, name string) (*role.Role, error)
	updateRoleFn         func(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error)
	deleteRoleFn         func(ctx context.Context, id string) error
	listRolesFn          func(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error)
	resolvePermissionsFn func(ctx context.Context, roleIDs []string) ([]permission.Permission, error)
}

func (m *mockRoleService) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	if m.createRoleFn != nil {
		return m.createRoleFn(ctx, req)
	}
	return &role.Role{
		ID:          "r-001",
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: req.Permissions,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockRoleService) GetRole(ctx context.Context, id string) (*role.Role, error) {
	if m.getRoleFn != nil {
		return m.getRoleFn(ctx, id)
	}
	return &role.Role{
		ID:          id,
		Name:        "admin",
		TenantID:    "default",
		Permissions: []permission.Permission{"kv:put", "kv:get"},
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	if m.getRoleByNameFn != nil {
		return m.getRoleByNameFn(ctx, tenantID, name)
	}
	return &role.Role{
		ID:       "r-001",
		Name:     name,
		TenantID: tenantID,
	}, nil
}

func (m *mockRoleService) UpdateRole(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	if m.updateRoleFn != nil {
		return m.updateRoleFn(ctx, req)
	}
	r := &role.Role{
		ID:          req.ID,
		Name:        req.Name,
		TenantID:    "default",
		Permissions: req.Permissions,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	if r.Name == "" {
		r.Name = "admin"
	}
	return r, nil
}

func (m *mockRoleService) DeleteRole(ctx context.Context, id string) error {
	if m.deleteRoleFn != nil {
		return m.deleteRoleFn(ctx, id)
	}
	return nil
}

func (m *mockRoleService) ListRoles(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	if m.listRolesFn != nil {
		return m.listRolesFn(ctx, req)
	}
	return &role.ListRolesResponse{
		Roles: []*role.Role{
			{ID: "r-001", Name: "admin", TenantID: "default"},
			{ID: "r-002", Name: "viewer", TenantID: "default"},
		},
		Total:   2,
		HasMore: false,
	}, nil
}

func (m *mockRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	if m.resolvePermissionsFn != nil {
		return m.resolvePermissionsFn(ctx, roleIDs)
	}
	return []permission.Permission{"kv:put", "kv:get"}, nil
}

// ---------------------------------------------------------------------------
// Mock: CertProvider
// ---------------------------------------------------------------------------

type mockCertProvider struct {
	generateClientCertFn func(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error)
	exportPKCS12Fn       func(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	revokeCertFn         func(ctx context.Context, serial string) error
	isRevokedFn          func(ctx context.Context, serial string) (bool, error)
	listCertsFn          func(ctx context.Context, tenantID string) ([]*cert.CertRecord, error)
	caCertPEMFn          func() []byte
}

func (m *mockCertProvider) GenerateClientCert(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
	if m.generateClientCertFn != nil {
		return m.generateClientCertFn(ctx, req)
	}
	return &cert.CertBundle{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----"),
		KeyPEM:  []byte("-----BEGIN EC PRIVATE KEY-----\nfake\n-----END EC PRIVATE KEY-----"),
		Serial:  "AABBCCDD",
	}, nil
}

func (m *mockCertProvider) ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	if m.exportPKCS12Fn != nil {
		return m.exportPKCS12Fn(ctx, certPEM, keyPEM, passphrase)
	}
	return []byte("fake-p12-data"), nil
}

func (m *mockCertProvider) RevokeCert(ctx context.Context, serial string) error {
	if m.revokeCertFn != nil {
		return m.revokeCertFn(ctx, serial)
	}
	return nil
}

func (m *mockCertProvider) IsRevoked(ctx context.Context, serial string) (bool, error) {
	if m.isRevokedFn != nil {
		return m.isRevokedFn(ctx, serial)
	}
	return false, nil
}

func (m *mockCertProvider) ListCerts(ctx context.Context, tenantID string) ([]*cert.CertRecord, error) {
	if m.listCertsFn != nil {
		return m.listCertsFn(ctx, tenantID)
	}
	return []*cert.CertRecord{
		{
			Serial:    "AABBCCDD",
			Username:  "alice",
			TenantID:  tenantID,
			IssuedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			Revoked:   false,
		},
	}, nil
}

func (m *mockCertProvider) CACertPEM() []byte {
	if m.caCertPEMFn != nil {
		return m.caCertPEMFn()
	}
	return []byte("-----BEGIN CERTIFICATE-----\nca-cert\n-----END CERTIFICATE-----")
}

// ---------------------------------------------------------------------------
// Sentinel errors for testing
// ---------------------------------------------------------------------------

type errUserNotFound struct{ ID string }

func (e *errUserNotFound) Error() string { return "user not found: " + e.ID }

type errDuplicateRole struct{ Name string }

func (e *errDuplicateRole) Error() string { return "role already exists: " + e.Name }

type errCertGenerate struct{ Msg string }

func (e *errCertGenerate) Error() string { return "cert generation failed: " + e.Msg }

// ---------------------------------------------------------------------------
// User tool tests
// ---------------------------------------------------------------------------

func TestUserCreateTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
		"role_ids": "r-001,r-002",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var u user.User
	if jsonErr := json.Unmarshal([]byte(text), &u); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if u.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", u.Username)
	}
	if u.ID != "u-001" {
		t.Errorf("expected ID 'u-001', got %q", u.ID)
	}
	if !u.Enabled {
		t.Error("expected user to be enabled")
	}
}

func TestUserCreateTool_WithTenantID(t *testing.T) {
	var captured *user.CreateUserRequest
	svc := &mockUserService{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			captured = req
			return &user.User{
				ID:       "u-002",
				Username: req.Username,
				TenantID: req.TenantID,
				RoleIDs:  req.RoleIDs,
				Enabled:  true,
			}, nil
		},
	}
	handler := handleUserCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username":  "bob",
		"tenant_id": "tenant-42",
		"role_ids":  "r-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatal("unexpected error result")
	}
	if captured == nil {
		t.Fatal("expected CreateUser to be called")
	}
	if captured.TenantID != "tenant-42" {
		t.Errorf("expected tenant_id 'tenant-42', got %q", captured.TenantID)
	}
	if len(captured.RoleIDs) != 1 || captured.RoleIDs[0] != "r-001" {
		t.Errorf("expected role_ids [r-001], got %v", captured.RoleIDs)
	}
}

func TestUserCreateTool_MissingUsername(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestUserCreateTool_ServiceError(t *testing.T) {
	svc := &mockUserService{
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, errors.New("internal error")
		},
	}
	handler := handleUserCreate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_user_create" {
		t.Errorf("expected tool 'qrdb_user_create', got %q", toolErr.Tool)
	}
}

func TestUserGetTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserGet(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var u user.User
	if jsonErr := json.Unmarshal([]byte(text), &u); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if u.ID != "u-001" {
		t.Errorf("expected ID 'u-001', got %q", u.ID)
	}
	if u.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", u.Username)
	}
}

func TestUserGetTool_NotFound(t *testing.T) {
	svc := &mockUserService{
		getUserFn: func(_ context.Context, id string) (*user.User, error) {
			return nil, &errUserNotFound{ID: id}
		},
	}
	handler := handleUserGet(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-999",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestUserGetTool_MissingID(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserGet(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestUserUpdateTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserUpdate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":       "u-001",
		"username": "alice-updated",
		"role_ids": "r-002, r-003",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var u user.User
	if jsonErr := json.Unmarshal([]byte(text), &u); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if u.ID != "u-001" {
		t.Errorf("expected ID 'u-001', got %q", u.ID)
	}
	if u.Username != "alice-updated" {
		t.Errorf("expected username 'alice-updated', got %q", u.Username)
	}
}

func TestUserUpdateTool_MissingID(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserUpdate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username": "new-name",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestUserUpdateTool_ServiceError(t *testing.T) {
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, errors.New("update failed")
		},
	}
	handler := handleUserUpdate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":       "u-001",
		"username": "new-name",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestUserUpdateTool_NoRoleIDsPreservesNil(t *testing.T) {
	var captured *user.UpdateUserRequest
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			captured = req
			return &user.User{ID: req.ID, Username: req.Username}, nil
		},
	}
	handler := handleUserUpdate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":       "u-001",
		"username": "bob",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected UpdateUser to be called")
	}
	if captured.RoleIDs != nil {
		t.Errorf("expected nil role_ids when not provided, got %v", captured.RoleIDs)
	}
}

func TestUserDeleteTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserDelete(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestUserDeleteTool_MissingID(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserDelete(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestUserDeleteTool_ServiceError(t *testing.T) {
	svc := &mockUserService{
		deleteUserFn: func(_ context.Context, _ string) error {
			return errors.New("system user cannot be deleted")
		},
	}
	handler := handleUserDelete(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-system",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestUserListTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserList(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp user.ListUsersResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Total != 2 {
		t.Errorf("expected total=2, got %d", resp.Total)
	}
	if len(resp.Users) != 2 {
		t.Errorf("expected 2 users, got %d", len(resp.Users))
	}
}

func TestUserListTool_WithPagination(t *testing.T) {
	var captured *user.ListUsersRequest
	svc := &mockUserService{
		listUsersFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			captured = req
			return &user.ListUsersResponse{
				Users:   []*user.User{{ID: "u-003", Username: "charlie"}},
				Total:   5,
				HasMore: true,
			}, nil
		},
	}
	handler := handleUserList(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id": "tenant-42",
		"page":      float64(2),
		"page_size": float64(1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp user.ListUsersResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !resp.HasMore {
		t.Error("expected HasMore=true")
	}
	if resp.Total != 5 {
		t.Errorf("expected total=5, got %d", resp.Total)
	}
	if captured == nil {
		t.Fatal("expected ListUsers to be called")
	}
	if captured.TenantID != "tenant-42" {
		t.Errorf("expected tenant_id 'tenant-42', got %q", captured.TenantID)
	}
	if captured.Page != 2 {
		t.Errorf("expected page=2, got %d", captured.Page)
	}
	if captured.PageSize != 1 {
		t.Errorf("expected page_size=1, got %d", captured.PageSize)
	}
}

func TestUserListTool_ServiceError(t *testing.T) {
	svc := &mockUserService{
		listUsersFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	handler := handleUserList(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestUserEnableTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserEnable(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestUserEnableTool_MissingID(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserEnable(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestUserEnableTool_ServiceError(t *testing.T) {
	svc := &mockUserService{
		enableUserFn: func(_ context.Context, _ string) error {
			return errors.New("enable failed")
		},
	}
	handler := handleUserEnable(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-001",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestUserDisableTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserDisable(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestUserDisableTool_MissingID(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserDisable(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestUserDisableTool_ServiceError(t *testing.T) {
	svc := &mockUserService{
		disableUserFn: func(_ context.Context, _ string) error {
			return errors.New("disable failed")
		},
	}
	handler := handleUserDisable(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "u-001",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// User tools registration test
// ---------------------------------------------------------------------------

func TestUserTools_Registration(t *testing.T) {
	svc := &mockUserService{}
	tools := userTools(svc)

	expectedNames := []string{
		"qrdb_user_create",
		"qrdb_user_get",
		"qrdb_user_update",
		"qrdb_user_delete",
		"qrdb_user_list",
		"qrdb_user_enable",
		"qrdb_user_disable",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d", len(expectedNames), len(tools))
	}

	nameSet := make(map[string]bool, len(tools))
	for _, tool := range tools {
		nameSet[tool.Tool.Name] = true
	}

	for _, name := range expectedNames {
		if !nameSet[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Role tool tests
// ---------------------------------------------------------------------------

func TestRoleCreateTool_Success(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name":        "editor",
		"permissions": "kv:put,kv:get",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var r role.Role
	if jsonErr := json.Unmarshal([]byte(text), &r); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if r.Name != "editor" {
		t.Errorf("expected name 'editor', got %q", r.Name)
	}
	if r.ID != "r-001" {
		t.Errorf("expected ID 'r-001', got %q", r.ID)
	}
	if len(r.Permissions) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(r.Permissions))
	}
}

func TestRoleCreateTool_WithTenantID(t *testing.T) {
	var captured *role.CreateRoleRequest
	svc := &mockRoleService{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			captured = req
			return &role.Role{
				ID:          "r-002",
				Name:        req.Name,
				TenantID:    req.TenantID,
				Permissions: req.Permissions,
			}, nil
		},
	}
	handler := handleRoleCreate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"name":        "viewer",
		"tenant_id":   "tenant-42",
		"permissions": "kv:get",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected CreateRole to be called")
	}
	if captured.TenantID != "tenant-42" {
		t.Errorf("expected tenant_id 'tenant-42', got %q", captured.TenantID)
	}
}

func TestRoleCreateTool_MissingName(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"permissions": "kv:put",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestRoleCreateTool_MissingPermissions(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name": "editor",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing permissions")
	}
}

func TestRoleCreateTool_InvalidPermissionFormat(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name":        "editor",
		"permissions": "invalid-no-colon",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid permission format")
	}
}

func TestRoleCreateTool_DuplicateName(t *testing.T) {
	svc := &mockRoleService{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &errDuplicateRole{Name: "admin"}
		},
	}
	handler := handleRoleCreate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"name":        "admin",
		"permissions": "kv:put",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleGetTool_Success(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleGet(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "r-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var r role.Role
	if jsonErr := json.Unmarshal([]byte(text), &r); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if r.ID != "r-001" {
		t.Errorf("expected ID 'r-001', got %q", r.ID)
	}
	if r.Name != "admin" {
		t.Errorf("expected name 'admin', got %q", r.Name)
	}
}

func TestRoleGetTool_NotFound(t *testing.T) {
	svc := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return nil, errors.New("role not found")
		},
	}
	handler := handleRoleGet(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "r-999",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleGetTool_MissingID(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleGet(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestRoleUpdateTool_Success(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleUpdate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":          "r-001",
		"name":        "super-admin",
		"permissions": "kv:put,kv:get,kv:delete",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var r role.Role
	if jsonErr := json.Unmarshal([]byte(text), &r); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if r.ID != "r-001" {
		t.Errorf("expected ID 'r-001', got %q", r.ID)
	}
	if r.Name != "super-admin" {
		t.Errorf("expected name 'super-admin', got %q", r.Name)
	}
}

func TestRoleUpdateTool_MissingID(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleUpdate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name": "new-name",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestRoleUpdateTool_InvalidPermissionFormat(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleUpdate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":          "r-001",
		"permissions": "nocolon",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid permission format")
	}
}

func TestRoleUpdateTool_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		updateRoleFn: func(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
			return nil, errors.New("update failed")
		},
	}
	handler := handleRoleUpdate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":   "r-001",
		"name": "new-name",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleUpdateTool_NoPermissionsPreservesNil(t *testing.T) {
	var captured *role.UpdateRoleRequest
	svc := &mockRoleService{
		updateRoleFn: func(_ context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
			captured = req
			return &role.Role{ID: req.ID, Name: req.Name}, nil
		},
	}
	handler := handleRoleUpdate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":   "r-001",
		"name": "updated",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected UpdateRole to be called")
	}
	if captured.Permissions != nil {
		t.Errorf("expected nil permissions when not provided, got %v", captured.Permissions)
	}
}

func TestRoleDeleteTool_Success(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleDelete(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "r-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestRoleDeleteTool_MissingID(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleDelete(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestRoleDeleteTool_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		deleteRoleFn: func(_ context.Context, _ string) error {
			return errors.New("system role cannot be deleted")
		},
	}
	handler := handleRoleDelete(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "r-system",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleListTool_Success(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleList(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp role.ListRolesResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Total != 2 {
		t.Errorf("expected total=2, got %d", resp.Total)
	}
	if len(resp.Roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(resp.Roles))
	}
}

func TestRoleListTool_WithPagination(t *testing.T) {
	var captured *role.ListRolesRequest
	svc := &mockRoleService{
		listRolesFn: func(_ context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			captured = req
			return &role.ListRolesResponse{
				Roles:   []*role.Role{{ID: "r-003", Name: "custom"}},
				Total:   10,
				HasMore: true,
			}, nil
		},
	}
	handler := handleRoleList(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id": "tenant-42",
		"page":      float64(3),
		"page_size": float64(5),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp role.ListRolesResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !resp.HasMore {
		t.Error("expected HasMore=true")
	}
	if captured == nil {
		t.Fatal("expected ListRoles to be called")
	}
	if captured.TenantID != "tenant-42" {
		t.Errorf("expected tenant_id 'tenant-42', got %q", captured.TenantID)
	}
	if captured.Page != 3 {
		t.Errorf("expected page=3, got %d", captured.Page)
	}
	if captured.PageSize != 5 {
		t.Errorf("expected page_size=5, got %d", captured.PageSize)
	}
}

func TestRoleListTool_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		listRolesFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	handler := handleRoleList(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleAssignTool_Success(t *testing.T) {
	userSvc := &mockUserService{}
	handler := handleRoleAssign(userSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-001",
		"role_id": "r-002",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestRoleAssignTool_AlreadyAssigned(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ string) (*user.User, error) {
			return &user.User{
				ID:      "u-001",
				RoleIDs: []string{"r-001", "r-002"},
			}, nil
		},
	}
	handler := handleRoleAssign(userSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-001",
		"role_id": "r-002",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok' for already-assigned role, got %q", text)
	}
}

func TestRoleAssignTool_MissingUserID(t *testing.T) {
	userSvc := &mockUserService{}
	handler := handleRoleAssign(userSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"role_id": "r-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing user_id")
	}
}

func TestRoleAssignTool_MissingRoleID(t *testing.T) {
	userSvc := &mockUserService{}
	handler := handleRoleAssign(userSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing role_id")
	}
}

func TestRoleAssignTool_GetUserError(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ string) (*user.User, error) {
			return nil, &errUserNotFound{ID: "u-999"}
		},
	}
	handler := handleRoleAssign(userSvc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-999",
		"role_id": "r-001",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleAssignTool_UpdateUserError(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ string) (*user.User, error) {
			return &user.User{ID: "u-001", RoleIDs: []string{}}, nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, errors.New("update failed")
		},
	}
	handler := handleRoleAssign(userSvc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-001",
		"role_id": "r-001",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleUnassignTool_Success(t *testing.T) {
	var captured *user.UpdateUserRequest
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ string) (*user.User, error) {
			return &user.User{
				ID:      "u-001",
				RoleIDs: []string{"r-001", "r-002", "r-003"},
			}, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			captured = req
			return &user.User{ID: req.ID, RoleIDs: req.RoleIDs}, nil
		},
	}
	handler := handleRoleUnassign(userSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-001",
		"role_id": "r-002",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
	if captured == nil {
		t.Fatal("expected UpdateUser to be called")
	}
	if len(captured.RoleIDs) != 2 {
		t.Fatalf("expected 2 remaining role IDs, got %d", len(captured.RoleIDs))
	}
	for _, rid := range captured.RoleIDs {
		if rid == "r-002" {
			t.Error("expected r-002 to be removed from role IDs")
		}
	}
}

func TestRoleUnassignTool_MissingUserID(t *testing.T) {
	userSvc := &mockUserService{}
	handler := handleRoleUnassign(userSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"role_id": "r-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing user_id")
	}
}

func TestRoleUnassignTool_MissingRoleID(t *testing.T) {
	userSvc := &mockUserService{}
	handler := handleRoleUnassign(userSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing role_id")
	}
}

func TestRoleUnassignTool_GetUserError(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ string) (*user.User, error) {
			return nil, &errUserNotFound{ID: "u-999"}
		},
	}
	handler := handleRoleUnassign(userSvc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-999",
		"role_id": "r-001",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleUnassignTool_UpdateUserError(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ string) (*user.User, error) {
			return &user.User{ID: "u-001", RoleIDs: []string{"r-001"}}, nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, errors.New("update failed")
		},
	}
	handler := handleRoleUnassign(userSvc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "u-001",
		"role_id": "r-001",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestPermissionsListTool_Success(t *testing.T) {
	handler := handlePermissionsList()

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	var categories []permission.Category
	if jsonErr := json.Unmarshal([]byte(text), &categories); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(categories) == 0 {
		t.Error("expected at least one permission category")
	}
}

func TestPermissionsListTool_ReturnsValidJSON(t *testing.T) {
	handler := handlePermissionsList()

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !json.Valid([]byte(text)) {
		t.Error("expected valid JSON in response")
	}
}

// ---------------------------------------------------------------------------
// Role tools registration test
// ---------------------------------------------------------------------------

func TestRoleTools_Registration(t *testing.T) {
	roleSvc := &mockRoleService{}
	userSvc := &mockUserService{}
	tools := roleTools(roleSvc, userSvc)

	expectedNames := []string{
		"qrdb_role_create",
		"qrdb_role_get",
		"qrdb_role_update",
		"qrdb_role_delete",
		"qrdb_role_list",
		"qrdb_role_assign",
		"qrdb_role_unassign",
		"qrdb_permissions_list",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d", len(expectedNames), len(tools))
	}

	nameSet := make(map[string]bool, len(tools))
	for _, tool := range tools {
		nameSet[tool.Tool.Name] = true
	}

	for _, name := range expectedNames {
		if !nameSet[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Cert tool tests
// ---------------------------------------------------------------------------

func TestCertGenerateTool_Success(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertGenerate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var bundle cert.CertBundle
	if jsonErr := json.Unmarshal([]byte(text), &bundle); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if bundle.Serial != "AABBCCDD" {
		t.Errorf("expected serial 'AABBCCDD', got %q", bundle.Serial)
	}
}

func TestCertGenerateTool_WithTenantAndTTL(t *testing.T) {
	var captured *cert.GenerateRequest
	svc := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			captured = req
			return &cert.CertBundle{
				CertPEM: []byte("cert"),
				KeyPEM:  []byte("key"),
				Serial:  "112233",
			}, nil
		},
	}
	handler := handleCertGenerate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"username":  "bob",
		"tenant_id": "tenant-42",
		"ttl":       "8760h",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected GenerateClientCert to be called")
	}
	if captured.Username != "bob" {
		t.Errorf("expected username 'bob', got %q", captured.Username)
	}
	if captured.TenantID != "tenant-42" {
		t.Errorf("expected tenant_id 'tenant-42', got %q", captured.TenantID)
	}
	expected := 8760 * time.Hour
	if captured.TTL != expected {
		t.Errorf("expected TTL %v, got %v", expected, captured.TTL)
	}
}

func TestCertGenerateTool_MissingUsername(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertGenerate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestCertGenerateTool_InvalidTTL(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertGenerate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
		"ttl":      "not-a-duration",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid ttl")
	}
}

func TestCertGenerateTool_ServiceError(t *testing.T) {
	svc := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, &errCertGenerate{Msg: "CA key sealed"}
		},
	}
	handler := handleCertGenerate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_cert_generate" {
		t.Errorf("expected tool 'qrdb_cert_generate', got %q", toolErr.Tool)
	}
}

func TestCertExportTool_Success(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertExport(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"cert_pem":   "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----",
		"key_pem":    "-----BEGIN EC PRIVATE KEY-----\nfake\n-----END EC PRIVATE KEY-----",
		"passphrase": "s3cret",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp certExportResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.P12Base64 == "" {
		t.Error("expected non-empty p12_base64")
	}
}

func TestCertExportTool_MissingCertPEM(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertExport(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key_pem":    "key-data",
		"passphrase": "s3cret",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing cert_pem")
	}
}

func TestCertExportTool_MissingKeyPEM(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertExport(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"cert_pem":   "cert-data",
		"passphrase": "s3cret",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing key_pem")
	}
}

func TestCertExportTool_MissingPassphrase(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertExport(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"cert_pem": "cert-data",
		"key_pem":  "key-data",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing passphrase")
	}
}

func TestCertExportTool_ServiceError(t *testing.T) {
	svc := &mockCertProvider{
		exportPKCS12Fn: func(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
			return nil, errors.New("pkcs12 encoding failed")
		},
	}
	handler := handleCertExport(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"cert_pem":   "cert-data",
		"key_pem":    "key-data",
		"passphrase": "s3cret",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestCertRevokeTool_Success(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertRevoke(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"serial": "AABBCCDD",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestCertRevokeTool_MissingSerial(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertRevoke(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing serial")
	}
}

func TestCertRevokeTool_ServiceError(t *testing.T) {
	svc := &mockCertProvider{
		revokeCertFn: func(_ context.Context, _ string) error {
			return errors.New("certificate not found")
		},
	}
	handler := handleCertRevoke(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"serial": "DEADBEEF",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestCertListTool_Success(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertList(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var records []*cert.CertRecord
	if jsonErr := json.Unmarshal([]byte(text), &records); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
	if records[0].Serial != "AABBCCDD" {
		t.Errorf("expected serial 'AABBCCDD', got %q", records[0].Serial)
	}
}

func TestCertListTool_WithTenantID(t *testing.T) {
	var capturedTenantID string
	svc := &mockCertProvider{
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			capturedTenantID = tenantID
			return []*cert.CertRecord{}, nil
		},
	}
	handler := handleCertList(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id": "custom-tenant",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTenantID != "custom-tenant" {
		t.Errorf("expected tenant_id 'custom-tenant', got %q", capturedTenantID)
	}
}

func TestCertListTool_DefaultTenantID(t *testing.T) {
	var capturedTenantID string
	svc := &mockCertProvider{
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			capturedTenantID = tenantID
			return []*cert.CertRecord{}, nil
		},
	}
	handler := handleCertList(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTenantID != "default" {
		t.Errorf("expected default tenant_id 'default', got %q", capturedTenantID)
	}
}

func TestCertListTool_ServiceError(t *testing.T) {
	svc := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	handler := handleCertList(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Cert tools registration test
// ---------------------------------------------------------------------------

func TestCertTools_Registration(t *testing.T) {
	svc := &mockCertProvider{}
	tools := certTools(svc)

	expectedNames := []string{
		"qrdb_cert_generate",
		"qrdb_cert_export",
		"qrdb_cert_revoke",
		"qrdb_cert_list",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d", len(expectedNames), len(tools))
	}

	nameSet := make(map[string]bool, len(tools))
	for _, tool := range tools {
		nameSet[tool.Tool.Name] = true
	}

	for _, name := range expectedNames {
		if !nameSet[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// parseCSV tests
// ---------------------------------------------------------------------------

func TestParseCSV_Empty(t *testing.T) {
	result := parseCSV("")
	if result != nil {
		t.Errorf("expected nil for empty string, got %v", result)
	}
}

func TestParseCSV_Single(t *testing.T) {
	result := parseCSV("value")
	if len(result) != 1 || result[0] != "value" {
		t.Errorf("expected [value], got %v", result)
	}
}

func TestParseCSV_Multiple(t *testing.T) {
	result := parseCSV("a,b,c")
	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
	if result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("expected [a b c], got %v", result)
	}
}

func TestParseCSV_Trimming(t *testing.T) {
	result := parseCSV("  a , b , c  ")
	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
	if result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("expected [a b c], got %v", result)
	}
}

func TestParseCSV_EmptyValues(t *testing.T) {
	result := parseCSV(",,,")
	if result != nil {
		t.Errorf("expected nil for all-empty values, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// parsePermissions tests
// ---------------------------------------------------------------------------

func TestParsePermissions_Valid(t *testing.T) {
	perms, err := parsePermissions("kv:put,kv:get")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(perms) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(perms))
	}
	if perms[0] != "kv:put" || perms[1] != "kv:get" {
		t.Errorf("expected [kv:put kv:get], got %v", perms)
	}
}

func TestParsePermissions_Invalid(t *testing.T) {
	_, err := parsePermissions("invalid")
	if err == nil {
		t.Fatal("expected error for invalid permission")
	}
	var invalidErr *permission.InvalidPermissionError
	if !errors.As(err, &invalidErr) {
		t.Errorf("expected *InvalidPermissionError, got %T", err)
	}
}

func TestParsePermissions_Empty(t *testing.T) {
	perms, err := parsePermissions("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if perms != nil {
		t.Errorf("expected nil for empty string, got %v", perms)
	}
}

func TestParsePermissions_MixedValid(t *testing.T) {
	perms, err := parsePermissions("admin:health, kv:put , kv:delete")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(perms) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(perms))
	}
}

func TestParsePermissions_EmptyResource(t *testing.T) {
	_, err := parsePermissions(":action")
	if err == nil {
		t.Fatal("expected error for empty resource")
	}
}

func TestParsePermissions_EmptyAction(t *testing.T) {
	_, err := parsePermissions("resource:")
	if err == nil {
		t.Fatal("expected error for empty action")
	}
}
