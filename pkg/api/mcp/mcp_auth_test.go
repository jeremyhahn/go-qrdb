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
	"strconv"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Mock: UserServicer
// ---------------------------------------------------------------------------

type mockUserService struct {
	createUserFn              func(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
	getUserFn                 func(ctx context.Context, id uint64) (*user.User, error)
	getUserByUsernameFn       func(ctx context.Context, tenantID, username string) (*user.User, error)
	getUserByEmailFn          func(ctx context.Context, tenantID, email string) (*user.User, error)
	getUserByCertThumbprintFn func(ctx context.Context, thumbprint string) (*user.User, error)
	updateUserFn              func(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
	deleteUserFn              func(ctx context.Context, id uint64) error
	listUsersFn               func(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
	enableUserFn              func(ctx context.Context, id uint64) error
	disableUserFn             func(ctx context.Context, id uint64) error
	addCertBindingFn          func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
	removeCertBindingFn       func(ctx context.Context, userID uint64, thumbprint string) (*user.User, error)
}

func (m *mockUserService) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if m.createUserFn != nil {
		return m.createUserFn(ctx, req)
	}
	return &user.User{
		ID:        1001,
		Username:  req.Username,
		TenantID:  req.TenantID,
		RoleIDs:   req.RoleIDs,
		Lifecycle: user.LifecycleActive,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockUserService) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	if m.getUserFn != nil {
		return m.getUserFn(ctx, id)
	}
	return &user.User{
		ID:        id,
		Username:  "alice",
		TenantID:  "system",
		RoleIDs:   []string{"r-001"},
		Lifecycle: user.LifecycleActive,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockUserService) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if m.getUserByUsernameFn != nil {
		return m.getUserByUsernameFn(ctx, tenantID, username)
	}
	return &user.User{
		ID:        1001,
		Username:  username,
		TenantID:  tenantID,
		Lifecycle: user.LifecycleActive,
	}, nil
}

func (m *mockUserService) GetUserByEmail(ctx context.Context, tenantID, email string) (*user.User, error) {
	if m.getUserByEmailFn != nil {
		return m.getUserByEmailFn(ctx, tenantID, email)
	}
	normalized := user.NormalizeEmail(email)
	return &user.User{
		ID:        1001,
		Username:  normalized,
		Email:     normalized,
		TenantID:  tenantID,
		Lifecycle: user.LifecycleActive,
	}, nil
}

func (m *mockUserService) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if m.updateUserFn != nil {
		return m.updateUserFn(ctx, req)
	}
	u := &user.User{
		ID:        req.ID,
		Username:  req.Username,
		TenantID:  "system",
		RoleIDs:   req.RoleIDs,
		Lifecycle: user.LifecycleActive,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	if u.Username == "" {
		u.Username = "alice"
	}
	return u, nil
}

func (m *mockUserService) DeleteUser(ctx context.Context, id uint64) error {
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
			{ID: 1001, Username: "alice", TenantID: "system", Lifecycle: user.LifecycleActive},
			{ID: 1002, Username: "bob", TenantID: "system", Lifecycle: user.LifecycleActive},
		},
		Total:   2,
		HasMore: false,
	}, nil
}

func (m *mockUserService) EnableUser(ctx context.Context, id uint64) error {
	if m.enableUserFn != nil {
		return m.enableUserFn(ctx, id)
	}
	return nil
}

func (m *mockUserService) DisableUser(ctx context.Context, id uint64) error {
	if m.disableUserFn != nil {
		return m.disableUserFn(ctx, id)
	}
	return nil
}

func (m *mockUserService) UserByCertThumbprint(ctx context.Context, thumbprint string) (*user.User, error) {
	if m.getUserByCertThumbprintFn != nil {
		return m.getUserByCertThumbprintFn(ctx, thumbprint)
	}
	return nil, nil
}

func (m *mockUserService) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
	if m.addCertBindingFn != nil {
		return m.addCertBindingFn(ctx, userID, thumbprint, notAfterUnix)
	}
	return nil, nil
}

func (m *mockUserService) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*user.User, error) {
	if m.removeCertBindingFn != nil {
		return m.removeCertBindingFn(ctx, userID, thumbprint)
	}
	return nil, nil
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
	reconcileFn          func(ctx context.Context, tenantID string) (*role.ReconcileReport, error)
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
		TenantID:    "system",
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
		TenantID:    "system",
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
			{ID: "r-001", Name: "admin", TenantID: "system"},
			{ID: "r-002", Name: "viewer", TenantID: "system"},
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
	issueClientCertFn func(ctx context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error)
	revokeCertFn      func(ctx context.Context, serial string) (*cert.RevokeResult, error)
	isRevokedFn       func(ctx context.Context, serial string) (bool, error)
	listCertsFn       func(ctx context.Context, tenantID string) ([]*cert.CertRecord, error)
	tenantForSerialFn func(ctx context.Context, serial string) (string, bool, error)
	caCertPEMFn       func() []byte
}

func (m *mockCertProvider) IssueClientCert(ctx context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error) {
	if m.issueClientCertFn != nil {
		return m.issueClientCertFn(ctx, req)
	}
	return &cert.IssuedCert{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----"),
		Serial:  "AABBCCDD",
	}, nil
}

func (m *mockCertProvider) RevokeCert(ctx context.Context, serial string) (*cert.RevokeResult, error) {
	if m.revokeCertFn != nil {
		return m.revokeCertFn(ctx, serial)
	}
	return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeRecordUpdated}, nil
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

type userNotFoundError struct{ ID string }

func (e *userNotFoundError) Error() string { return "user not found: " + e.ID }

type duplicateRoleError struct{ Name string }

func (e *duplicateRoleError) Error() string { return "role already exists: " + e.Name }

type certGenerateError struct{ Msg string }

func (e *certGenerateError) Error() string { return "cert generation failed: " + e.Msg }

// ---------------------------------------------------------------------------
// User tool tests
// ---------------------------------------------------------------------------

func TestUserCreateTool_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserCreate(svc, nil, nil, &mockRoleService{})

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
	if u.ID != 1001 {
		t.Errorf("expected ID 1001, got %d", u.ID)
	}
	if u.Lifecycle != user.LifecycleActive {
		t.Errorf("expected lifecycle active, got %q", u.Lifecycle)
	}
	// Lifecycle is the only account state: the removed "enabled" projection
	// must not reappear in the tool output.
	var raw map[string]json.RawMessage
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		t.Fatalf("unmarshal raw: %v", jsonErr)
	}
	if _, ok := raw["enabled"]; ok {
		t.Errorf("tool output carries the removed \"enabled\" key: %s", text)
	}
}

func TestUserCreateTool_WithTenantID(t *testing.T) {
	var captured *user.CreateUserRequest
	svc := &mockUserService{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			captured = req
			return &user.User{
				ID:        1002,
				Username:  req.Username,
				TenantID:  req.TenantID,
				RoleIDs:   req.RoleIDs,
				Lifecycle: user.LifecycleActive,
			}, nil
		},
	}
	handler := handleUserCreate(svc, nil, nil, &mockRoleService{getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
		return &role.Role{ID: id, TenantID: "tenant-42"}, nil
	}})

	// Bound to the request's own tenant: the system-tenant lift was removed
	// (owner ruling, 2026-10-01), so an unbound (system-folding) caller
	// naming a different tenant would now be refused before reaching the
	// mock.
	ctx := tenant.WithTenantID(context.Background(), "tenant-42")
	result, err := handler(ctx, makeReq(map[string]any{
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
	handler := handleUserCreate(svc, nil, nil, nil)

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
	handler := handleUserCreate(svc, nil, nil, nil)

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
	handler := handleUserGet(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "1001",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var u user.User
	if jsonErr := json.Unmarshal([]byte(text), &u); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if u.ID != 1001 {
		t.Errorf("expected ID 1001, got %d", u.ID)
	}
	if u.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", u.Username)
	}
}

// TestUserGetTool_Uint64IDRoundTrip proves a large uint64 user id supplied as
// the MCP string "id" arg is parsed to uint64, passed to the service, and
// echoed back unchanged in the marshaled user JSON.
func TestUserGetTool_Uint64IDRoundTrip(t *testing.T) {
	const wantID uint64 = 1<<63 + 7777
	var got uint64
	svc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			got = id
			return &user.User{ID: id, Username: "snowflake", Lifecycle: user.LifecycleActive}, nil
		},
	}
	handler := handleUserGet(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": "9223372036854783585",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantID {
		t.Fatalf("arg id lost: got %d, want %d", got, wantID)
	}
	var u user.User
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &u); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if u.ID != wantID {
		t.Fatalf("response id lost: got %d, want %d", u.ID, wantID)
	}
}

func TestUserGetTool_NotFound(t *testing.T) {
	svc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return nil, &userNotFoundError{ID: strconv.FormatUint(id, 10)}
		},
	}
	handler := handleUserGet(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "9999",
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
	handler := handleUserGet(svc, nil)

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
	handler := handleUserUpdate(svc, nil, nil, &mockRoleService{})

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":       "1001",
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
	if u.ID != 1001 {
		t.Errorf("expected ID 1001, got %d", u.ID)
	}
	if u.Username != "alice-updated" {
		t.Errorf("expected username 'alice-updated', got %q", u.Username)
	}
}

func TestUserUpdateTool_MissingID(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserUpdate(svc, nil, nil, nil)

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
	handler := handleUserUpdate(svc, nil, nil, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":       "1001",
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
	handler := handleUserUpdate(svc, nil, nil, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":       "1001",
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
		"id": "1001",
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
		deleteUserFn: func(_ context.Context, _ uint64) error {
			return errors.New("system user cannot be deleted")
		},
	}
	handler := handleUserDelete(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "9001",
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
	handler := handleUserList(svc, nil)

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
				Users:   []*user.User{{ID: 1003, Username: "charlie"}},
				Total:   5,
				HasMore: true,
			}, nil
		},
	}
	handler := handleUserList(svc, nil)

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
	handler := handleUserList(svc, nil)

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
		"id": "1001",
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
		enableUserFn: func(_ context.Context, _ uint64) error {
			return errors.New("enable failed")
		},
	}
	handler := handleUserEnable(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "1001",
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
		"id": "1001",
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
		disableUserFn: func(_ context.Context, _ uint64) error {
			return errors.New("disable failed")
		},
	}
	handler := handleUserDisable(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id": "1001",
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
	tools := userTools(svc, nil, nil, nil)

	expectedNames := []string{
		"qrdb_user_create",
		"qrdb_user_get",
		"qrdb_user_get_by_username",
		"qrdb_user_get_by_email",
		"qrdb_user_get_by_cert_thumbprint",
		"qrdb_user_add_cert_binding",
		"qrdb_user_remove_cert_binding",
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
			return nil, &duplicateRoleError{Name: "admin"}
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
	handler := handleRoleAssign(&mockRoleService{}, userSvc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
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

func TestRoleAssignTool_RoleNotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: id}
		},
	}
	userSvc := &mockUserService{}
	handler := handleRoleAssign(roleSvc, userSvc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
		"role_id": "missing",
	}))
	if err == nil {
		t.Fatal("expected error when assigning a nonexistent role")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestRoleAssignTool_AlreadyAssigned(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return &user.User{
				ID:      1001,
				RoleIDs: []string{"r-001", "r-002"},
			}, nil
		},
	}
	handler := handleRoleAssign(&mockRoleService{}, userSvc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
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
	handler := handleRoleAssign(&mockRoleService{}, userSvc, nil)

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
	handler := handleRoleAssign(&mockRoleService{}, userSvc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
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
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, &userNotFoundError{ID: "u-999"}
		},
	}
	handler := handleRoleAssign(&mockRoleService{}, userSvc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "9999",
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
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return &user.User{ID: 1001, RoleIDs: []string{}}, nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, errors.New("update failed")
		},
	}
	handler := handleRoleAssign(&mockRoleService{}, userSvc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
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
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return &user.User{
				ID:      1001,
				RoleIDs: []string{"r-001", "r-002", "r-003"},
			}, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			captured = req
			return &user.User{ID: req.ID, RoleIDs: req.RoleIDs}, nil
		},
	}
	handler := handleRoleUnassign(userSvc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
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
	handler := handleRoleUnassign(userSvc, nil)

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
	handler := handleRoleUnassign(userSvc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
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
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, &userNotFoundError{ID: "u-999"}
		},
	}
	handler := handleRoleUnassign(userSvc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "9999",
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
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return &user.User{ID: 1001, RoleIDs: []string{"r-001"}}, nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, errors.New("update failed")
		},
	}
	handler := handleRoleUnassign(userSvc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"user_id": "1001",
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
	tools := roleTools(roleSvc, userSvc, nil)

	expectedNames := []string{
		"qrdb_role_create",
		"qrdb_role_get",
		"qrdb_role_get_by_name",
		"qrdb_role_update",
		"qrdb_role_delete",
		"qrdb_role_list",
		"qrdb_role_assign",
		"qrdb_role_unassign",
		"qrdb_permissions_list",
		"qrdb_permissions_resolve",
		"qrdb_role_reconcile_builtin",
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
// handleRoleGetByName tool tests
// ---------------------------------------------------------------------------

func TestRoleGetByNameTool_Success(t *testing.T) {
	svc := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			if tenantID != "tenant-1" {
				t.Fatalf("expected tenantID 'tenant-1', got %q", tenantID)
			}
			if name != "auditor" {
				t.Fatalf("expected name 'auditor', got %q", name)
			}
			return &role.Role{
				ID:          "r-audit",
				Name:        "auditor",
				TenantID:    "tenant-1",
				Permissions: []permission.Permission{"kv:get"},
			}, nil
		},
	}
	handler := handleRoleGetByName(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name":      "auditor",
		"tenant_id": "tenant-1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var r role.Role
	if jsonErr := json.Unmarshal([]byte(text), &r); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if r.ID != "r-audit" {
		t.Errorf("expected ID 'r-audit', got %q", r.ID)
	}
	if r.Name != "auditor" {
		t.Errorf("expected name 'auditor', got %q", r.Name)
	}
	if r.TenantID != "tenant-1" {
		t.Errorf("expected tenantID 'tenant-1', got %q", r.TenantID)
	}
}

func TestRoleGetByNameTool_MissingName(t *testing.T) {
	svc := &mockRoleService{}
	handler := handleRoleGetByName(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id": "tenant-1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestRoleGetByNameTool_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, _, _ string) (*role.Role, error) {
			return nil, errors.New("role not found")
		},
	}
	handler := handleRoleGetByName(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"name":      "nonexistent",
		"tenant_id": "tenant-1",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_role_get_by_name" {
		t.Errorf("expected tool 'qrdb_role_get_by_name', got %q", toolErr.Tool)
	}
}

func TestRoleGetByNameTool_NoTenantID(t *testing.T) {
	var capturedTenantID string
	svc := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, tenantID, _ string) (*role.Role, error) {
			capturedTenantID = tenantID
			return &role.Role{ID: "r-001", Name: "admin", TenantID: tenantID}, nil
		},
	}
	handler := handleRoleGetByName(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name": "admin",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected success result with empty tenant_id")
	}
	if capturedTenantID != "" {
		t.Errorf("expected empty tenantID when not provided, got %q", capturedTenantID)
	}
}

// ---------------------------------------------------------------------------
// handlePermissionsResolve tool tests
// ---------------------------------------------------------------------------

func TestPermissionsResolveTool_Success(t *testing.T) {
	svc := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
			if len(roleIDs) != 2 {
				t.Fatalf("expected 2 roleIDs, got %d", len(roleIDs))
			}
			return []permission.Permission{"kv:put", "kv:get"}, nil
		},
	}
	handler := handlePermissionsResolve(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"role_ids": "r-001,r-002",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	var out map[string][]string
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	perms, ok := out["permissions"]
	if !ok {
		t.Fatal("expected 'permissions' key in response")
	}
	if len(perms) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(perms))
	}
}

func TestPermissionsResolveTool_MissingRoleIDs(t *testing.T) {
	svc := &mockRoleService{}
	handler := handlePermissionsResolve(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing role_ids")
	}
}

func TestPermissionsResolveTool_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	handler := handlePermissionsResolve(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"role_ids": "r-001",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_permissions_resolve" {
		t.Errorf("expected tool 'qrdb_permissions_resolve', got %q", toolErr.Tool)
	}
}

func TestPermissionsResolveTool_EmptyPermissionsResult(t *testing.T) {
	svc := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return []permission.Permission{}, nil
		},
	}
	handler := handlePermissionsResolve(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"role_ids": "r-empty",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	var out map[string][]string
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	perms := out["permissions"]
	if len(perms) != 0 {
		t.Fatalf("expected 0 permissions, got %d", len(perms))
	}
}

// ---------------------------------------------------------------------------
// Enrollment tool tests (qrdb_cert_issue, qrdb_cert_enroll, qrdb_trust_anchor_*)
// ---------------------------------------------------------------------------

// fakeCertEnrollTool is a configurable, faithful service.CertEnrollmentServicer
// double for the MCP enrollment tool handlers.
type fakeCertEnrollTool struct {
	signFn   func(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error)
	enrollFn func(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error)
	addFn    func(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	listFn   func(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	removeFn func(ctx context.Context, tenantID, fingerprint string) error
}

var _ service.CertEnrollmentServicer = (*fakeCertEnrollTool)(nil)

func (f *fakeCertEnrollTool) SignCertificate(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.signFn(ctx, req)
}

func (f *fakeCertEnrollTool) EnrollCertificate(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.enrollFn(ctx, req)
}

func (f *fakeCertEnrollTool) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	return f.addFn(ctx, tenantID, bundlePEM)
}

func (f *fakeCertEnrollTool) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	return f.listFn(ctx, tenantID)
}

func (f *fakeCertEnrollTool) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	return f.removeFn(ctx, tenantID, fingerprint)
}

func TestCertIssueTool_Success(t *testing.T) {
	svc := &fakeCertEnrollTool{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return &service.EnrolledCertificate{Username: req.Username, Serial: "AABBCCDD"}, nil
		},
	}
	handler := handleCertIssue(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
		"csr_pem":  "csr-pem",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var out enrolledCertResult
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out.Serial != "AABBCCDD" {
		t.Errorf("expected serial 'AABBCCDD', got %q", out.Serial)
	}
}

func TestCertIssueTool_WithTTL(t *testing.T) {
	var captured *service.SignCertificateRequest
	svc := &fakeCertEnrollTool{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			captured = req
			return &service.EnrolledCertificate{Serial: "112233"}, nil
		},
	}
	handler := handleCertIssue(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"username": "bob",
		"csr_pem":  "csr-pem",
		"ttl":      "8760h",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected SignCertificate to be called")
	}
	if captured.Username != "bob" {
		t.Errorf("expected username 'bob', got %q", captured.Username)
	}
	expected := 8760 * time.Hour
	if captured.TTL != expected {
		t.Errorf("expected TTL %v, got %v", expected, captured.TTL)
	}
}

func TestCertIssueTool_MissingUsername(t *testing.T) {
	svc := &fakeCertEnrollTool{}
	handler := handleCertIssue(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{"csr_pem": "csr-pem"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestCertIssueTool_InvalidTTL(t *testing.T) {
	svc := &fakeCertEnrollTool{}
	handler := handleCertIssue(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
		"csr_pem":  "csr-pem",
		"ttl":      "not-a-duration",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid ttl")
	}
}

func TestCertIssueTool_ServiceError(t *testing.T) {
	svc := &fakeCertEnrollTool{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &certGenerateError{Msg: "CA key sealed"}
		},
	}
	handler := handleCertIssue(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"username": "alice",
		"csr_pem":  "csr-pem",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_cert_issue" {
		t.Errorf("expected tool 'qrdb_cert_issue', got %q", toolErr.Tool)
	}
}

func TestCertEnrollTool_Success(t *testing.T) {
	svc := &fakeCertEnrollTool{
		enrollFn: func(_ context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			return &service.EnrolledCertificate{Username: req.Username, Thumbprint: "thumb-1"}, nil
		},
	}
	handler := handleCertEnroll(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"username": "dave",
		"cert_pem": "leaf-pem",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var out enrolledCertResult
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out.Thumbprint != "thumb-1" {
		t.Errorf("expected thumbprint 'thumb-1', got %q", out.Thumbprint)
	}
}

func TestCertEnrollTool_MissingUsername(t *testing.T) {
	svc := &fakeCertEnrollTool{}
	handler := handleCertEnroll(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{"cert_pem": "leaf-pem"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestCertEnrollTool_ServiceError(t *testing.T) {
	svc := &fakeCertEnrollTool{
		enrollFn: func(context.Context, *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &certGenerateError{Msg: "no trust anchors"}
		},
	}
	handler := handleCertEnroll(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"username": "dave",
		"cert_pem": "leaf-pem",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_cert_enroll" {
		t.Errorf("expected tool 'qrdb_cert_enroll', got %q", toolErr.Tool)
	}
}

func TestTrustAnchorAddTool_Success(t *testing.T) {
	var gotTenant string
	var gotBundle []byte
	svc := &fakeCertEnrollTool{
		addFn: func(_ context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
			gotTenant, gotBundle = tenantID, bundlePEM
			return []trustanchor.Anchor{{Fingerprint: "fp-1", Subject: "CN=acme-root"}}, nil
		},
	}
	handler := handleTrustAnchorAdd(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id": "acme",
		"certs_pem": "ca-pem",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "acme" || string(gotBundle) != "ca-pem" {
		t.Errorf("AddTrustAnchors(%q, %q), want (acme, ca-pem)", gotTenant, string(gotBundle))
	}
	text := resultText(t, result)
	var out struct {
		Anchors []trustAnchorResult `json:"anchors"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(out.Anchors) != 1 || out.Anchors[0].Fingerprint != "fp-1" {
		t.Errorf("expected 1 anchor with fingerprint 'fp-1', got %+v", out.Anchors)
	}
}

func TestTrustAnchorAddTool_ServiceError(t *testing.T) {
	svc := &fakeCertEnrollTool{
		addFn: func(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
			return nil, &trustanchor.EmptyBundleError{}
		},
	}
	handler := handleTrustAnchorAdd(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id": "acme",
		"certs_pem": "",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestTrustAnchorListTool_Success(t *testing.T) {
	svc := &fakeCertEnrollTool{
		listFn: func(_ context.Context, tenantID string) ([]trustanchor.Anchor, error) {
			if tenantID != "acme" {
				t.Fatalf("expected tenant 'acme', got %q", tenantID)
			}
			return []trustanchor.Anchor{{Fingerprint: "fp-1"}, {Fingerprint: "fp-2"}}, nil
		},
	}
	handler := handleTrustAnchorList(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{"tenant_id": "acme"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var out struct {
		Anchors []trustAnchorResult `json:"anchors"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(out.Anchors) != 2 {
		t.Fatalf("expected 2 anchors, got %d", len(out.Anchors))
	}
}

func TestTrustAnchorListTool_ServiceError(t *testing.T) {
	svc := &fakeCertEnrollTool{
		listFn: func(context.Context, string) ([]trustanchor.Anchor, error) {
			return nil, &certGenerateError{Msg: "store unavailable"}
		},
	}
	handler := handleTrustAnchorList(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{"tenant_id": "acme"}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestTrustAnchorRemoveTool_Success(t *testing.T) {
	var gotTenant, gotFingerprint string
	svc := &fakeCertEnrollTool{
		removeFn: func(_ context.Context, tenantID, fingerprint string) error {
			gotTenant, gotFingerprint = tenantID, fingerprint
			return nil
		},
	}
	handler := handleTrustAnchorRemove(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id":   "acme",
		"fingerprint": "fp-1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "acme" || gotFingerprint != "fp-1" {
		t.Errorf("RemoveTrustAnchor(%q, %q), want (acme, fp-1)", gotTenant, gotFingerprint)
	}
	text := resultText(t, result)
	if text != `{"status":"ok"}` {
		t.Errorf("expected ok status, got %q", text)
	}
}

func TestTrustAnchorRemoveTool_ServiceError(t *testing.T) {
	svc := &fakeCertEnrollTool{
		removeFn: func(context.Context, string, string) error {
			return &trustanchor.AnchorNotFoundError{Fingerprint: "fp-missing"}
		},
	}
	handler := handleTrustAnchorRemove(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"tenant_id":   "acme",
		"fingerprint": "fp-missing",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestEnrollmentTools_Registration(t *testing.T) {
	svc := &fakeCertEnrollTool{}
	tools := enrollmentTools(svc, nil)

	expectedNames := []string{
		"qrdb_cert_issue",
		"qrdb_cert_enroll",
		"qrdb_trust_anchor_add",
		"qrdb_trust_anchor_list",
		"qrdb_trust_anchor_remove",
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

func TestCertRevokeTool_Success(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertRevoke(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"serial": "AABBCCDD",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp certRevokeResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
	if !resp.RecordFound {
		t.Error("expected RecordFound true")
	}
}

func TestCertRevokeTool_DenyBySerial(t *testing.T) {
	svc := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeDenyBySerial}, nil
		},
	}
	handler := handleCertRevoke(svc, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"serial": "peer-issued",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp certRevokeResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.RecordFound {
		t.Error("expected RecordFound false for a deny-by-serial revocation")
	}
}

func TestCertRevokeTool_MissingSerial(t *testing.T) {
	svc := &mockCertProvider{}
	handler := handleCertRevoke(svc, nil)

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
		revokeCertFn: func(_ context.Context, _ string) (*cert.RevokeResult, error) {
			return nil, errors.New("certificate not found")
		},
	}
	handler := handleCertRevoke(svc, nil)

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
	handler := handleCertList(svc, nil)

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
	handler := handleCertList(svc, nil)

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

func TestCertListTool_SystemTenantID(t *testing.T) {
	var capturedTenantID string
	svc := &mockCertProvider{
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			capturedTenantID = tenantID
			return []*cert.CertRecord{}, nil
		},
	}
	handler := handleCertList(svc, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTenantID != "system" {
		t.Errorf("expected default tenant_id 'system', got %q", capturedTenantID)
	}
}

func TestCertListTool_ServiceError(t *testing.T) {
	svc := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	handler := handleCertList(svc, nil)

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
	tools := certTools(svc, nil)

	expectedNames := []string{
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

// The MCP tool schemas and handlers must carry the canonical login key. The
// transport exposed Email/Name on the request types while MCP forwarded neither,
// so a caller could set them, be told the create succeeded, and get a user with
// no entry in the index logins resolve through (Codex review, PR #191).
func TestUserCreateTool_CarriesEmailAndName(t *testing.T) {
	var got *user.CreateUserRequest
	svc := &mockUserService{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			got = req
			return &user.User{
				ID: 1, Username: req.Username, Email: req.Email, Name: req.Name,
				TenantID: req.TenantID, Lifecycle: user.LifecycleActive,
			}, nil
		},
	}

	if _, err := handleUserCreate(svc, nil, nil, nil)(context.Background(), makeReq(map[string]any{
		"username": "alice",
		"email":    "alice@example.com",
		"name":     "Alice",
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("service was never called")
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("email dropped on the way to the store: %q", got.Email)
	}
	if got.Name != "Alice" {
		t.Fatalf("name dropped on the way to the store: %q", got.Name)
	}
}

func TestUserUpdateTool_CarriesEmailAndName(t *testing.T) {
	var got *user.UpdateUserRequest
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			got = req
			return &user.User{ID: req.ID, Email: req.Email, Name: req.Name, Lifecycle: user.LifecycleActive}, nil
		},
	}

	if _, err := handleUserUpdate(svc, nil, nil, nil)(context.Background(), makeReq(map[string]any{
		"id":    "42",
		"email": "new@example.com",
		"name":  "New Name",
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("service was never called")
	}
	if got.Email != "new@example.com" {
		t.Fatalf("email dropped on the way to the store: %q", got.Email)
	}
	if got.Name != "New Name" {
		t.Fatalf("name dropped on the way to the store: %q", got.Name)
	}
}

// The schema is the contract a caller reads. A handler that parses a field the
// schema does not declare works only for a caller that guessed.
func TestUserTools_SchemasDeclareEmailAndName(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool mcp.Tool
	}{
		{"create", toolUserCreate()},
		{"update", toolUserUpdate()},
	} {
		props := tc.tool.InputSchema.Properties
		for _, field := range []string{"email", "name"} {
			if _, ok := props[field]; !ok {
				t.Errorf("%s tool schema does not declare %q", tc.name, field)
			}
		}
	}
}

// MCP is the transport that keeps getting missed, because it builds an argument
// MAP while every other transport uses struct literals — an edit shaped around
// struct fields passes over it silently and nothing fails to compile. These pin
// the identity fields on both verbs.
func TestUserCreateTool_CarriesIdentityFields(t *testing.T) {
	var got *user.CreateUserRequest
	svc := &mockUserService{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			got = req
			return &user.User{ID: 1001}, nil
		},
	}
	handler := handleUserCreate(svc, nil, nil, nil)

	if _, err := handler(context.Background(), makeReq(map[string]any{
		"username":     "alice",
		"email":        "alice@example.com",
		"auth_methods": `["webauthn","piv"]`,
		"lifecycle":    "pending",
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("the service was never called")
	}
	if len(got.AuthMethods) != 2 || got.AuthMethods[1] != "piv" {
		t.Errorf("AuthMethods = %v", got.AuthMethods)
	}
	if got.Lifecycle != user.LifecyclePending {
		t.Errorf("Lifecycle = %q, want %q", got.Lifecycle, user.LifecyclePending)
	}
}

// A flat tool argument cannot carry nil-vs-empty the way JSON null-vs-[] does,
// so clearing is its own flag. Without it, lifting a method restriction would be
// unexpressible over MCP: an empty string reads as "leave alone" and the clear
// would report success while doing nothing.
func TestUserUpdateTool_ClearFlagExpressesClear(t *testing.T) {
	cases := []struct {
		name        string
		args        map[string]any
		wantNil     bool
		wantCleared bool
		wantSet     []string
	}{
		{name: "absent leaves alone", args: map[string]any{"id": "1001"}, wantNil: true},
		{
			name:        "clear flag clears",
			args:        map[string]any{"id": "1001", "clear_auth_methods": true},
			wantCleared: true,
		},
		{
			name:    "values replace",
			args:    map[string]any{"id": "1001", "auth_methods": `["platform","sre"]`},
			wantSet: []string{"platform", "sre"},
		},
		{
			// Values win over the flag: a caller that sends both means to set,
			// not to clear.
			name:    "values win over the flag",
			args:    map[string]any{"id": "1001", "auth_methods": `["platform"]`, "clear_auth_methods": true},
			wantSet: []string{"platform"},
		},
		{
			// THE REASON THIS IS JSON. The list carries opaque values, so a
			// comma inside one is a legitimate character. A CSV encoding split
			// it into two — writing a value the caller never named, and
			// reporting success (Codex peer review, PR #198).
			name:    "a value containing a comma stays one value",
			args:    map[string]any{"id": "1001", "auth_methods": `["team,with,commas"]`},
			wantSet: []string{"team,with,commas"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *user.UpdateUserRequest
			svc := &mockUserService{
				updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
					got = req
					return &user.User{ID: 1001}, nil
				},
			}
			if _, err := handleUserUpdate(svc, nil, nil, nil)(context.Background(), makeReq(tc.args)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("the service was never called")
			}
			switch {
			case tc.wantNil:
				if got.AuthMethods != nil {
					t.Fatalf("AuthMethods = %#v, want nil (leave alone)", got.AuthMethods)
				}
			case tc.wantCleared:
				if got.AuthMethods == nil || len(got.AuthMethods) != 0 {
					t.Fatalf("AuthMethods = %#v, want a non-nil empty slice (clear)", got.AuthMethods)
				}
			default:
				if len(got.AuthMethods) != len(tc.wantSet) || got.AuthMethods[0] != tc.wantSet[0] {
					t.Fatalf("AuthMethods = %#v, want %v", got.AuthMethods, tc.wantSet)
				}
			}
		})
	}
}

// A malformed last_auth is the caller's mistake and must be reported as one,
// not swallowed into an update that quietly changed nothing.
func TestUserUpdateTool_MalformedLastAuthIsAnErrorResult(t *testing.T) {
	called := false
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			called = true
			return &user.User{ID: 1001}, nil
		},
	}
	result, err := handleUserUpdate(svc, nil, nil, nil)(context.Background(), makeReq(map[string]any{
		"id":        "1001",
		"last_auth": "yesterday",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected an error result for a malformed last_auth")
	}
	if called {
		t.Error("the update reached the service despite a malformed timestamp")
	}
}

// A malformed identifier list must be REPORTED, not silently read as "leave
// alone". Quietly leaving a policy list unchanged when the caller asked
// to change it is the same class of defect as changing it wrongly: a success
// that did something other than what was requested.
func TestUserUpdateTool_MalformedIdentifierListIsAnErrorResult(t *testing.T) {
	for _, bad := range []string{"not-json", `{"a":1}`, `["ok", 7]`} {
		called := false
		svc := &mockUserService{
			updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
				called = true
				return &user.User{ID: 1001}, nil
			},
		}
		result, err := handleUserUpdate(svc, nil, nil, nil)(context.Background(), makeReq(map[string]any{
			"id":           "1001",
			"auth_methods": bad,
		}))
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", bad, err)
		}
		if !result.IsError {
			t.Errorf("%q: expected an error result", bad)
		}
		if called {
			t.Errorf("%q: the update reached the service despite a malformed list", bad)
		}
	}
}

// Blank elements are not dropped. The store refuses them, and this surface must
// send what the caller wrote so that refusal actually reaches them.
func TestUserUpdateTool_BlankIdentifierElementReachesTheStore(t *testing.T) {
	var got *user.UpdateUserRequest
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			got = req
			return &user.User{ID: 1001}, nil
		},
	}
	if _, err := handleUserUpdate(svc, nil, nil, nil)(context.Background(), makeReq(map[string]any{
		"id":           "1001",
		"auth_methods": `["ok",""]`,
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("the service was never called")
	}
	if len(got.AuthMethods) != 2 || got.AuthMethods[1] != "" {
		t.Fatalf("AuthMethods = %#v; the blank element was dropped instead of forwarded for the store to refuse", got.AuthMethods)
	}
}

// mcp.ParseString routes through cast.ToString, which returns "" for anything
// it cannot convert — a native JSON array included. Reading the argument that
// way made every malformed or non-string payload indistinguishable from an
// ABSENT one, so update left the field unchanged and create dropped the list,
// both reporting success. That is the same silent-corruption class the JSON
// encoding exists to close, reached by a different route (Codex, PR #198).
func TestParseOpaqueStringList_ShapesAreNeverMistakenForAbsence(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		want    []string
		wantNil bool
		wantErr bool
	}{
		{name: "absent is nil", args: map[string]any{}, wantNil: true},
		{name: "explicit null is nil", args: map[string]any{"auth_methods": nil}, wantNil: true},
		{name: "JSON null is nil", args: map[string]any{"auth_methods": "null"}, wantNil: true},
		{name: "JSON string array", args: map[string]any{"auth_methods": `["a","b"]`}, want: []string{"a", "b"}},
		{name: "JSON empty array clears", args: map[string]any{"auth_methods": `[]`}, want: []string{}},
		{
			// A raw MCP client answering the schema with a real array is a
			// legitimate caller, not a malformed one.
			name: "native array", args: map[string]any{"auth_methods": []any{"a", "b"}},
			want: []string{"a", "b"},
		},
		{name: "native empty array clears", args: map[string]any{"auth_methods": []any{}}, want: []string{}},
		{
			// An in-process caller can hand the tool a []string directly (JSON
			// only ever produces []any). Cloning it with append([]string(nil),
			// v...) turned an empty one back into nil — "leave alone" — so a
			// revocation reported success and revoked nothing.
			name: "native []string", args: map[string]any{"auth_methods": []string{"a", "b"}},
			want: []string{"a", "b"},
		},
		{
			name: "native empty []string clears",
			args: map[string]any{"auth_methods": []string{}}, want: []string{},
		},
		{
			// The values that used to vanish.
			name: "empty string is an error", args: map[string]any{"auth_methods": ""}, wantErr: true,
		},
		{name: "number is an error", args: map[string]any{"auth_methods": 7}, wantErr: true},
		{name: "object is an error", args: map[string]any{"auth_methods": map[string]any{"a": 1}}, wantErr: true},
		{name: "non-string element is an error", args: map[string]any{"auth_methods": []any{"a", 7}}, wantErr: true},
		{name: "not JSON is an error", args: map[string]any{"auth_methods": "a,b"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOpaqueStringList(makeReq(tc.args), "auth_methods")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %#v", got)
				}
				var argErr *ToolArgumentError
				if !errors.As(err, &argErr) {
					t.Fatalf("expected *ToolArgumentError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %#v, want nil (absent)", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %v", tc.want)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %#v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %#v, want %v", got, tc.want)
				}
			}
		})
	}
}

// An empty list must CLEAR, not be read as absent — the whole point of
// distinguishing the two — and it must do so through EVERY encoding a caller
// can use, since each takes a different branch on the way in.
func TestUserUpdateTool_EmptyListClearsThroughEveryEncoding(t *testing.T) {
	for name, value := range map[string]any{
		"JSON string":     `[]`,
		"native []any":    []any{},
		"native []string": []string{},
	} {
		t.Run(name, func(t *testing.T) {
			var got *user.UpdateUserRequest
			svc := &mockUserService{
				updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
					got = req
					return &user.User{ID: 1001}, nil
				},
			}
			if _, err := handleUserUpdate(svc, nil, nil, nil)(context.Background(), makeReq(map[string]any{
				"id":           "1001",
				"auth_methods": value,
			})); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("the service was never called")
			}
			if got.AuthMethods == nil || len(got.AuthMethods) != 0 {
				t.Fatalf("AuthMethods = %#v, want a non-nil empty slice (clear)", got.AuthMethods)
			}
		})
	}
}

func (m *mockRoleService) ReconcileBuiltInRoles(ctx context.Context, tenantID string) (*role.ReconcileReport, error) {
	if m.reconcileFn != nil {
		return m.reconcileFn(ctx, tenantID)
	}
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

// TenantForSerial resolves the owning tenant for a serial. The default is
// ("", false, nil) -- the deny-by-serial case -- because a double holding no
// records genuinely owns no serials, and a double that claimed otherwise would
// let a cross-tenant revocation pass a test written to catch one.
func (m *mockCertProvider) TenantForSerial(ctx context.Context, serial string) (string, bool, error) {
	if m.tenantForSerialFn != nil {
		return m.tenantForSerialFn(ctx, serial)
	}
	return "", false, nil
}

// ---------------------------------------------------------------------------
// fakeTenantOnboarding
// ---------------------------------------------------------------------------

// fakeTenantOnboarding adapts tenant.TenantServicer.CreateTenant to
// service.TenantOnboardingServicer for handler tests that exercise
// handleTenantCreate's request/response mapping rather than the onboarding
// business rules (first-admin credential issuance), which pkg/service and
// pkg/bootstrap test directly against the real TenantOnboardingService. No
// build constraint: it is used from tagged and untagged tenant test files, so
// its own constraint must be the superset -- none at all.
type fakeTenantOnboarding struct {
	tenants tenant.TenantServicer
}

var _ service.TenantOnboardingServicer = (*fakeTenantOnboarding)(nil)

func (f *fakeTenantOnboarding) OnboardTenant(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	info, err := f.tenants.CreateTenant(ctx, req.ID, req.Name, req.IsolationType)
	if err != nil {
		return nil, err
	}
	return &service.OnboardTenantResult{Tenant: info}, nil
}
