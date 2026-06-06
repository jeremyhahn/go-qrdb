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

package rest

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	getUserFn           func(ctx context.Context, id uint64) (*user.User, error)
	getUserByUsernameFn func(ctx context.Context, tenantID, username string) (*user.User, error)
	updateUserFn        func(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
	deleteUserFn        func(ctx context.Context, id uint64) error
	listUsersFn         func(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
	enableUserFn        func(ctx context.Context, id uint64) error
	disableUserFn       func(ctx context.Context, id uint64) error
}

func (m *mockUserService) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if m.createUserFn != nil {
		return m.createUserFn(ctx, req)
	}
	return nil, nil
}

func (m *mockUserService) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	if m.getUserFn != nil {
		return m.getUserFn(ctx, id)
	}
	return nil, nil
}

func (m *mockUserService) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if m.getUserByUsernameFn != nil {
		return m.getUserByUsernameFn(ctx, tenantID, username)
	}
	return nil, nil
}

func (m *mockUserService) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if m.updateUserFn != nil {
		return m.updateUserFn(ctx, req)
	}
	return nil, nil
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
	return nil, nil
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

func (m *mockUserService) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserService) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
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
}

func (m *mockRoleService) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	if m.createRoleFn != nil {
		return m.createRoleFn(ctx, req)
	}
	return nil, nil
}

func (m *mockRoleService) GetRole(ctx context.Context, id string) (*role.Role, error) {
	if m.getRoleFn != nil {
		return m.getRoleFn(ctx, id)
	}
	return nil, nil
}

func (m *mockRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	if m.getRoleByNameFn != nil {
		return m.getRoleByNameFn(ctx, tenantID, name)
	}
	return nil, nil
}

func (m *mockRoleService) UpdateRole(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	if m.updateRoleFn != nil {
		return m.updateRoleFn(ctx, req)
	}
	return nil, nil
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
	return nil, nil
}

func (m *mockRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	if m.resolvePermissionsFn != nil {
		return m.resolvePermissionsFn(ctx, roleIDs)
	}
	return nil, nil
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
	caCertPEMVal         []byte
}

func (m *mockCertProvider) GenerateClientCert(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
	if m.generateClientCertFn != nil {
		return m.generateClientCertFn(ctx, req)
	}
	return nil, nil
}

func (m *mockCertProvider) ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	if m.exportPKCS12Fn != nil {
		return m.exportPKCS12Fn(ctx, certPEM, keyPEM, passphrase)
	}
	return nil, nil
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
	return nil, nil
}

func (m *mockCertProvider) CACertPEM() []byte {
	return m.caCertPEMVal
}

// ---------------------------------------------------------------------------
// Test fixture helpers
// ---------------------------------------------------------------------------

var (
	testTime      = time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	testTimeStr   = testTime.Format(time.RFC3339)
	testUpdateStr = testTime.Add(time.Hour).Format(time.RFC3339)
)

func testUser() *user.User {
	return &user.User{
		ID:        1001,
		Username:  "alice",
		TenantID:  "tenant-1",
		RoleIDs:   []string{"role-admin"},
		Enabled:   true,
		System:    false,
		CreatedAt: testTime,
		UpdatedAt: testTime.Add(time.Hour),
	}
}

func testRole() *role.Role {
	return &role.Role{
		ID:          "role-001",
		Name:        "admin",
		TenantID:    "tenant-1",
		Permissions: []permission.Permission{permission.KVPut, permission.KVGet},
		System:      false,
		CreatedAt:   testTime,
		UpdatedAt:   testTime.Add(time.Hour),
	}
}

func newAuthServer(userSvc user.UserServicer, roleSvc role.RoleServicer, certProv cert.CertProvider) *Server {
	var opts []ServerOption
	if userSvc != nil {
		opts = append(opts, WithUserService(userSvc))
	}
	if roleSvc != nil {
		opts = append(opts, WithRoleService(roleSvc))
	}
	if certProv != nil {
		opts = append(opts, WithCertProvider(certProv))
	}
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...)
}

func assertContentType(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	got := rec.Header().Get("Content-Type")
	if got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}

// =========================================================================
// User handler tests
// =========================================================================

// ---------------------------------------------------------------------------
// POST /users (CreateUser)
// ---------------------------------------------------------------------------

func TestHandleCreateUser_Success(t *testing.T) {
	svc := &mockUserService{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			if req.Username != "alice" || req.TenantID != "tenant-1" {
				t.Fatalf("unexpected request: %+v", req)
			}
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"username":"alice","tenantID":"tenant-1","roleIDs":["role-admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	assertContentType(t, rec, "application/json")

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 1001 {
		t.Fatalf("id = %d, want %d", resp.ID, uint64(1001))
	}
	if resp.Username != "alice" {
		t.Fatalf("username = %q, want %q", resp.Username, "alice")
	}
	if resp.TenantID != "tenant-1" {
		t.Fatalf("tenantID = %q, want %q", resp.TenantID, "tenant-1")
	}
	if !resp.Enabled {
		t.Fatal("enabled should be true")
	}
}

func TestHandleCreateUser_InvalidJSON(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateUser_ServiceError_UsernameExists(t *testing.T) {
	svc := &mockUserService{
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, &user.UsernameExistsError{Username: "alice", TenantID: "tenant-1"}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleCreateUser_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	// Without UserService, the /users route is not registered.
	// POST /users does not match the "/users/" prefix either (len 6 < 7).
	// Dispatch returns 404.
	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCreateUser_ValidationError(t *testing.T) {
	svc := &mockUserService{
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, &user.ValidationError{Field: "username", Reason: "must not be empty"}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"username":"","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateUser_StoreError(t *testing.T) {
	svc := &mockUserService{
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, &user.StoreError{Op: "put", Err: errors.New("disk full")}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// GET /users/list (ListUsers)
// ---------------------------------------------------------------------------

func TestHandleListUsers_Success(t *testing.T) {
	svc := &mockUserService{
		listUsersFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			if req.TenantID != "tenant-1" {
				t.Fatalf("tenantID = %q, want %q", req.TenantID, "tenant-1")
			}
			return &user.ListUsersResponse{
				Users:   []*user.User{testUser()},
				Total:   1,
				HasMore: false,
			}, nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp listUsersResponse
	decodeJSON(t, rec, &resp)
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if len(resp.Users) != 1 {
		t.Fatalf("users length = %d, want 1", len(resp.Users))
	}
	if resp.Users[0].Username != "alice" {
		t.Fatalf("username = %q, want %q", resp.Users[0].Username, "alice")
	}
}

func TestHandleListUsers_WithPagination(t *testing.T) {
	svc := &mockUserService{
		listUsersFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			if req.Page != 2 || req.PageSize != 10 {
				t.Fatalf("pagination: page=%d, pageSize=%d; want page=2, pageSize=10", req.Page, req.PageSize)
			}
			return &user.ListUsersResponse{
				Users:   []*user.User{},
				Total:   15,
				HasMore: false,
			}, nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/list?tenantID=tenant-1&page=2&pageSize=10", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listUsersResponse
	decodeJSON(t, rec, &resp)
	if resp.Total != 15 {
		t.Fatalf("total = %d, want 15", resp.Total)
	}
}

func TestHandleListUsers_InvalidPage(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/list?page=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleListUsers_InvalidPageSize(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/list?pageSize=xyz", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleListUsers_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	// Without UserService, /users/list is not registered as exact route.
	// However, "/users/list" has len > 7 and starts with "/users/", so
	// dispatch sends it to handleUser which returns 501 for nil userSvc.
	req := httptest.NewRequest(http.MethodGet, "/users/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListUsers_ServiceError(t *testing.T) {
	svc := &mockUserService{
		listUsersFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return nil, &user.StoreError{Op: "list", Err: errors.New("io error")}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// GET /users/{id} (GetUser)
// ---------------------------------------------------------------------------

func TestHandleGetUser_Success(t *testing.T) {
	svc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != 1001 {
				t.Fatalf("id = %d, want %d", id, uint64(1001))
			}
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/1001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 1001 {
		t.Fatalf("id = %d, want %d", resp.ID, uint64(1001))
	}
	if resp.CreatedAt != testTimeStr {
		t.Fatalf("createdAt = %q, want %q", resp.CreatedAt, testTimeStr)
	}
	if resp.UpdatedAt != testUpdateStr {
		t.Fatalf("updatedAt = %q, want %q", resp.UpdatedAt, testUpdateStr)
	}
}

func TestHandleGetUser_NotFound(t *testing.T) {
	svc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: id}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/9999", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetUser_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/1001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// dispatch prefix match "/users/" requires len > 7, which "usr-001" satisfies.
	// handleUser checks nil service and returns 501.
	assertStatus(t, rec, http.StatusNotImplemented)
}

// TestHandleGetUser_Uint64IDRoundTrip proves a large uint64 user id survives
// the REST wire: it is parsed from the URL path into the uint64 service
// argument and echoed back in the JSON UserResponse.id field.
func TestHandleGetUser_Uint64IDRoundTrip(t *testing.T) {
	const wantID uint64 = 1<<63 + 987654321
	var got uint64
	svc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			got = id
			return &user.User{ID: id, Username: "snowflake", Enabled: true}, nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/9223372037842430129", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if got != wantID {
		t.Fatalf("path id lost: got %d, want %d", got, wantID)
	}
	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != wantID {
		t.Fatalf("response id lost: got %d, want %d", resp.ID, wantID)
	}
}

// ---------------------------------------------------------------------------
// PUT /users/{id} (UpdateUser)
// ---------------------------------------------------------------------------

func TestHandleUpdateUser_Success(t *testing.T) {
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			if req.ID != 1001 {
				t.Fatalf("id = %d, want %d", req.ID, uint64(1001))
			}
			if req.Username != "bob" {
				t.Fatalf("username = %q, want %q", req.Username, "bob")
			}
			u := testUser()
			u.Username = "bob"
			return u, nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"username":"bob","roleIDs":["role-admin"]}`
	req := httptest.NewRequest(http.MethodPut, "/users/1001", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.Username != "bob" {
		t.Fatalf("username = %q, want %q", resp.Username, "bob")
	}
}

func TestHandleUpdateUser_InvalidJSON(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPut, "/users/1001", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUpdateUser_ServiceError(t *testing.T) {
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: 1001}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"username":"bob"}`
	req := httptest.NewRequest(http.MethodPut, "/users/1001", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// DELETE /users/{id} (DeleteUser)
// ---------------------------------------------------------------------------

func TestHandleDeleteUser_Success(t *testing.T) {
	svc := &mockUserService{
		deleteUserFn: func(_ context.Context, id uint64) error {
			if id != 1001 {
				t.Fatalf("id = %d, want %d", id, uint64(1001))
			}
			return nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/1001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleDeleteUser_SystemUser(t *testing.T) {
	svc := &mockUserService{
		deleteUserFn: func(_ context.Context, id uint64) error {
			return &user.SystemUserImmutableError{ID: id}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/9001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

func TestHandleDeleteUser_NotFound(t *testing.T) {
	svc := &mockUserService{
		deleteUserFn: func(_ context.Context, id uint64) error {
			return &user.UserNotFoundError{ID: id}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/9999", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// POST /users/{id}/enable (EnableUser)
// ---------------------------------------------------------------------------

func TestHandleEnableUser_Success(t *testing.T) {
	svc := &mockUserService{
		enableUserFn: func(_ context.Context, id uint64) error {
			if id != 1001 {
				t.Fatalf("id = %d, want %d", id, uint64(1001))
			}
			return nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/1001/enable", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp okResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want %q", resp.Status, "ok")
	}
}

func TestHandleEnableUser_ServiceError(t *testing.T) {
	svc := &mockUserService{
		enableUserFn: func(_ context.Context, id uint64) error {
			return &user.UserNotFoundError{ID: id}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/1001/enable", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// POST /users/{id}/disable (DisableUser)
// ---------------------------------------------------------------------------

func TestHandleDisableUser_Success(t *testing.T) {
	svc := &mockUserService{
		disableUserFn: func(_ context.Context, id uint64) error {
			if id != 1001 {
				t.Fatalf("id = %d, want %d", id, uint64(1001))
			}
			return nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/1001/disable", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp okResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want %q", resp.Status, "ok")
	}
}

func TestHandleDisableUser_ServiceError(t *testing.T) {
	svc := &mockUserService{
		disableUserFn: func(_ context.Context, id uint64) error {
			return &user.SystemUserImmutableError{ID: id}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/9001/disable", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

// ---------------------------------------------------------------------------
// Method not allowed for /users/{id}
// ---------------------------------------------------------------------------

func TestHandleUser_MethodNotAllowed_PATCH(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPatch, "/users/1001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleUser_PostWithoutSuffix_MethodNotAllowed(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	// POST /users/usr-001 (no /enable or /disable suffix)
	req := httptest.NewRequest(http.MethodPost, "/users/1001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleUser_MissingID(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	// GET /users/ with empty ID -- dispatch requires len > 7, "/users/" is exactly 7,
	// so this matches the exact route first. Since we have WithUserService, /users
	// route is POST. GET will be method not allowed.
	req := httptest.NewRequest(http.MethodGet, "/users/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// "/users/" has length 7 which is not > 7, so it won't match the prefix route.
	// It also won't match the exact "/users" route (that requires POST).
	// This falls through to 404.
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// User conversion edge cases
// ---------------------------------------------------------------------------

func TestUserToResponse_NilRoleIDs(t *testing.T) {
	u := testUser()
	u.RoleIDs = nil
	resp := userToResponse(u)
	if resp.RoleIDs == nil {
		t.Fatal("roleIDs should be empty slice, not nil")
	}
	if len(resp.RoleIDs) != 0 {
		t.Fatalf("roleIDs length = %d, want 0", len(resp.RoleIDs))
	}
}

func TestUserToResponse_Nil(t *testing.T) {
	resp := userToResponse(nil)
	if resp != nil {
		t.Fatal("expected nil response for nil user")
	}
}

// =========================================================================
// Role handler tests
// =========================================================================

// ---------------------------------------------------------------------------
// POST /roles (CreateRole)
// ---------------------------------------------------------------------------

func TestHandleCreateRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			if req.Name != "editor" || req.TenantID != "tenant-1" {
				t.Fatalf("unexpected request: %+v", req)
			}
			if len(req.Permissions) != 2 {
				t.Fatalf("permissions length = %d, want 2", len(req.Permissions))
			}
			return testRole(), nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"name":"editor","tenantID":"tenant-1","permissions":["kv:put","kv:get"]}`
	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	assertContentType(t, rec, "application/json")

	var resp roleResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != "role-001" {
		t.Fatalf("id = %q, want %q", resp.ID, "role-001")
	}
	if resp.Name != "admin" {
		t.Fatalf("name = %q, want %q", resp.Name, "admin")
	}
	if len(resp.Permissions) != 2 {
		t.Fatalf("permissions length = %d, want 2", len(resp.Permissions))
	}
}

func TestHandleCreateRole_InvalidJSON(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateRole_DuplicateName(t *testing.T) {
	roleSvc := &mockRoleService{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &role.RoleNameExistsError{Name: "admin", TenantID: "tenant-1"}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"name":"admin","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleCreateRole_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	body := `{"name":"admin","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCreateRole_ValidationError(t *testing.T) {
	roleSvc := &mockRoleService{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &role.ValidationError{Field: "name", Reason: "must not be empty"}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"name":"","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateRole_InvalidPermission(t *testing.T) {
	roleSvc := &mockRoleService{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &role.InvalidPermissionError{Permission: "invalid:perm"}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"name":"bad","tenantID":"tenant-1","permissions":["invalid:perm"]}`
	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// GET /roles/list (ListRoles)
// ---------------------------------------------------------------------------

func TestHandleListRoles_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		listRolesFn: func(_ context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			if req.TenantID != "tenant-1" {
				t.Fatalf("tenantID = %q, want %q", req.TenantID, "tenant-1")
			}
			return &role.ListRolesResponse{
				Roles:   []*role.Role{testRole()},
				Total:   1,
				HasMore: false,
			}, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listRolesResponse
	decodeJSON(t, rec, &resp)
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if len(resp.Roles) != 1 {
		t.Fatalf("roles length = %d, want 1", len(resp.Roles))
	}
}

func TestHandleListRoles_WithPagination(t *testing.T) {
	roleSvc := &mockRoleService{
		listRolesFn: func(_ context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			if req.Page != 3 || req.PageSize != 5 {
				t.Fatalf("pagination: page=%d, pageSize=%d; want page=3, pageSize=5", req.Page, req.PageSize)
			}
			return &role.ListRolesResponse{
				Roles:   []*role.Role{},
				Total:   20,
				HasMore: true,
			}, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/list?tenantID=t1&page=3&pageSize=5", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listRolesResponse
	decodeJSON(t, rec, &resp)
	if !resp.HasMore {
		t.Fatal("hasMore should be true")
	}
}

func TestHandleListRoles_InvalidPage(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/list?page=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleListRoles_InvalidPageSize(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/list?pageSize=xyz", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleListRoles_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	// Without RoleService, /roles/list is not registered as exact route.
	// However, "/roles/list" has len > 7 and starts with "/roles/", so
	// dispatch sends it to handleRole which returns 501 for nil roleSvc.
	req := httptest.NewRequest(http.MethodGet, "/roles/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListRoles_ServiceError(t *testing.T) {
	roleSvc := &mockRoleService{
		listRolesFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			return nil, &role.StoreError{Op: "list", Err: errors.New("io error")}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// GET /roles/{id} (GetRole)
// ---------------------------------------------------------------------------

func TestHandleGetRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			if id != "role-001" {
				t.Fatalf("id = %q, want %q", id, "role-001")
			}
			return testRole(), nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/role-001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp roleResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != "role-001" {
		t.Fatalf("id = %q, want %q", resp.ID, "role-001")
	}
	if resp.CreatedAt != testTimeStr {
		t.Fatalf("createdAt = %q, want %q", resp.CreatedAt, testTimeStr)
	}
}

func TestHandleGetRole_NotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: id}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetRole_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/role-001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// dispatch prefix match "/roles/" requires len > 7, "/roles/role-001" matches.
	// handleRole checks nil service and returns 501.
	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// PUT /roles/{id} (UpdateRole)
// ---------------------------------------------------------------------------

func TestHandleUpdateRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		updateRoleFn: func(_ context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
			if req.ID != "role-001" {
				t.Fatalf("id = %q, want %q", req.ID, "role-001")
			}
			if req.Name != "role-x" {
				t.Fatalf("name = %q, want %q", req.Name, "role-x")
			}
			r := testRole()
			r.Name = "role-x"
			return r, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"name":"role-x","permissions":["kv:put","kv:get"]}`
	req := httptest.NewRequest(http.MethodPut, "/roles/role-001", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp roleResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "role-x" {
		t.Fatalf("name = %q, want %q", resp.Name, "role-x")
	}
}

func TestHandleUpdateRole_InvalidJSON(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodPut, "/roles/role-001", strings.NewReader("invalid"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUpdateRole_ServiceError(t *testing.T) {
	roleSvc := &mockRoleService{
		updateRoleFn: func(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: "role-001"}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"name":"role-x"}`
	req := httptest.NewRequest(http.MethodPut, "/roles/role-001", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// DELETE /roles/{id} (DeleteRole)
// ---------------------------------------------------------------------------

func TestHandleDeleteRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		deleteRoleFn: func(_ context.Context, id string) error {
			if id != "role-001" {
				t.Fatalf("id = %q, want %q", id, "role-001")
			}
			return nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodDelete, "/roles/role-001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleDeleteRole_SystemRole(t *testing.T) {
	roleSvc := &mockRoleService{
		deleteRoleFn: func(_ context.Context, id string) error {
			return &role.SystemRoleImmutableError{ID: id}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodDelete, "/roles/sys-admin", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

func TestHandleDeleteRole_NotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		deleteRoleFn: func(_ context.Context, id string) error {
			return &role.RoleNotFoundError{ID: id}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodDelete, "/roles/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Method not allowed for /roles/{id}
// ---------------------------------------------------------------------------

func TestHandleRole_MethodNotAllowed(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodPatch, "/roles/role-001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
	allow := rec.Header().Get("Allow")
	if allow != "GET, PUT, DELETE" {
		t.Fatalf("Allow = %q, want %q", allow, "GET, PUT, DELETE")
	}
}

func TestHandleRole_MissingID(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	// "/roles/" has length 7 which is not > 7, so dispatch won't prefix-match.
	req := httptest.NewRequest(http.MethodGet, "/roles/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// POST /roles/assign (AssignRole)
// ---------------------------------------------------------------------------

func TestHandleAssignRole_Success(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != 1001 {
				t.Fatalf("id = %d, want %d", id, uint64(1001))
			}
			u := testUser()
			u.RoleIDs = []string{"role-old"}
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			if len(req.RoleIDs) != 2 {
				t.Fatalf("roleIDs length = %d, want 2", len(req.RoleIDs))
			}
			u := testUser()
			u.RoleIDs = req.RoleIDs
			return u, nil
		},
	}
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			if id != "role-new" {
				t.Fatalf("role id = %q, want %q", id, "role-new")
			}
			return testRole(), nil
		},
	}
	srv := newAuthServer(userSvc, roleSvc, nil)

	body := `{"userID":1001,"roleID":"role-new"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if len(resp.RoleIDs) != 2 {
		t.Fatalf("roleIDs length = %d, want 2", len(resp.RoleIDs))
	}
}

func TestHandleAssignRole_AlreadyAssigned(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			u := testUser()
			u.RoleIDs = []string{"role-admin"}
			return u, nil
		},
	}
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	srv := newAuthServer(userSvc, roleSvc, nil)

	// Assign the role that is already assigned -- should be idempotent.
	body := `{"userID":1001,"roleID":"role-admin"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if len(resp.RoleIDs) != 1 {
		t.Fatalf("roleIDs length = %d, want 1 (idempotent)", len(resp.RoleIDs))
	}
}

func TestHandleAssignRole_NilRoleService(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)

	body := `{"userID":1001,"roleID":"role-001"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Without RoleService, /roles/assign is not registered as exact route.
	// However, "/roles/assign" has len > 7 and starts with "/roles/", so
	// dispatch sends it to handleRole which returns 501 for nil roleSvc.
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleAssignRole_NilUserService(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"userID":1001,"roleID":"role-001"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleAssignRole_InvalidJSON(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, &mockRoleService{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleAssignRole_RoleNotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: id}
		},
	}
	srv := newAuthServer(&mockUserService{}, roleSvc, nil)

	body := `{"userID":1001,"roleID":"nonexistent"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleAssignRole_UserNotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: id}
		},
	}
	srv := newAuthServer(userSvc, roleSvc, nil)

	body := `{"userID":9999,"roleID":"role-001"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleAssignRole_UpdateError(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return testUser(), nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, &user.StoreError{Op: "update", Err: errors.New("disk full")}
		},
	}
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	srv := newAuthServer(userSvc, roleSvc, nil)

	// Assign a role that is NOT already in the user's RoleIDs to trigger the update path.
	body := `{"userID":1001,"roleID":"role-new"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// POST /roles/unassign (UnassignRole)
// ---------------------------------------------------------------------------

func TestHandleUnassignRole_Success(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			u := testUser()
			u.RoleIDs = []string{"role-admin", "role-editor"}
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			if len(req.RoleIDs) != 1 {
				t.Fatalf("roleIDs length = %d, want 1", len(req.RoleIDs))
			}
			if req.RoleIDs[0] != "role-editor" {
				t.Fatalf("remaining role = %q, want %q", req.RoleIDs[0], "role-editor")
			}
			u := testUser()
			u.RoleIDs = req.RoleIDs
			return u, nil
		},
	}
	roleSvc := &mockRoleService{}
	srv := newAuthServer(userSvc, roleSvc, nil)

	body := `{"userID":1001,"roleID":"role-admin"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/unassign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if len(resp.RoleIDs) != 1 {
		t.Fatalf("roleIDs length = %d, want 1", len(resp.RoleIDs))
	}
}

func TestHandleUnassignRole_NilRoleService(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)

	body := `{"userID":1001,"roleID":"role-001"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/unassign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Without RoleService, /roles/unassign is not registered as exact route.
	// However, "/roles/unassign" has len > 7 and starts with "/roles/", so
	// dispatch sends it to handleRole which returns 501 for nil roleSvc.
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUnassignRole_NilUserService(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"userID":1001,"roleID":"role-001"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/unassign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUnassignRole_InvalidJSON(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, &mockRoleService{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/unassign", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUnassignRole_UserNotFound(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: id}
		},
	}
	roleSvc := &mockRoleService{}
	srv := newAuthServer(userSvc, roleSvc, nil)

	body := `{"userID":9999,"roleID":"role-001"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/unassign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleUnassignRole_UpdateError(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return testUser(), nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, &user.StoreError{Op: "update", Err: errors.New("io error")}
		},
	}
	roleSvc := &mockRoleService{}
	srv := newAuthServer(userSvc, roleSvc, nil)

	body := `{"userID":1001,"roleID":"role-admin"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/unassign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// GET /permissions (ListPermissions)
// ---------------------------------------------------------------------------

func TestHandleListPermissions_Success(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/permissions", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp listPermissionsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Categories) == 0 {
		t.Fatal("expected non-empty categories")
	}

	// Verify a known category exists.
	found := false
	for _, cat := range resp.Categories {
		if cat.Name == "KV Service" {
			found = true
			if cat.Group != "Data Services" {
				t.Fatalf("KV Service group = %q, want %q", cat.Group, "Data Services")
			}
			if len(cat.Permissions) == 0 {
				t.Fatal("KV Service should have permissions")
			}
			break
		}
	}
	if !found {
		t.Fatal("expected KV Service category in permissions list")
	}
}

func TestHandleListPermissions_NilRoleService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/permissions", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// When roleSvc is nil, the /permissions route is not registered.
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Role conversion edge cases
// ---------------------------------------------------------------------------

func TestRoleToResponse_Nil(t *testing.T) {
	resp := roleToResponse(nil)
	if resp != nil {
		t.Fatal("expected nil response for nil role")
	}
}

func TestRoleToResponse_EmptyPermissions(t *testing.T) {
	r := testRole()
	r.Permissions = nil
	resp := roleToResponse(r)
	if resp.Permissions == nil {
		t.Fatal("permissions should be non-nil empty slice")
	}
	if len(resp.Permissions) != 0 {
		t.Fatalf("permissions length = %d, want 0", len(resp.Permissions))
	}
}

func TestStringsToPermissions_Nil(t *testing.T) {
	result := stringsToPermissions(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestStringsToPermissions_Values(t *testing.T) {
	result := stringsToPermissions([]string{"kv:put", "kv:get"})
	if len(result) != 2 {
		t.Fatalf("length = %d, want 2", len(result))
	}
	if result[0] != permission.KVPut {
		t.Fatalf("result[0] = %q, want %q", result[0], permission.KVPut)
	}
	if result[1] != permission.KVGet {
		t.Fatalf("result[1] = %q, want %q", result[1], permission.KVGet)
	}
}

// ---------------------------------------------------------------------------
// GET /roles/by-name (GetRoleByName)
// ---------------------------------------------------------------------------

func TestHandleGetRoleByName_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			if tenantID != "tenant-1" {
				t.Fatalf("tenantID = %q, want %q", tenantID, "tenant-1")
			}
			if name != "auditor" {
				t.Fatalf("name = %q, want %q", name, "auditor")
			}
			r := testRole()
			r.Name = "auditor"
			return r, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/by-name?tenant_id=tenant-1&name=auditor", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp roleResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != "role-001" {
		t.Fatalf("id = %q, want %q", resp.ID, "role-001")
	}
	if resp.Name != "auditor" {
		t.Fatalf("name = %q, want %q", resp.Name, "auditor")
	}
}

func TestHandleGetRoleByName_MissingName(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/by-name?tenant_id=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleGetRoleByName_NotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleByNameFn: func(_ context.Context, _, name string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: name}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/by-name?tenant_id=tenant-1&name=nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// POST /roles/resolve-permissions (ResolvePermissions)
// ---------------------------------------------------------------------------

func TestHandleResolvePermissions_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
			if len(roleIDs) != 2 {
				t.Fatalf("roleIDs length = %d, want 2", len(roleIDs))
			}
			return []permission.Permission{permission.KVPut, permission.KVGet}, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"role_ids":["role-001","role-002"]}`
	req := httptest.NewRequest(http.MethodPost, "/roles/resolve-permissions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp resolvePermissionsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Permissions) != 2 {
		t.Fatalf("permissions length = %d, want 2", len(resp.Permissions))
	}
}

func TestHandleResolvePermissions_ServiceError(t *testing.T) {
	roleSvc := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, &role.StoreError{Op: "resolve", Err: errors.New("storage failure")}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"role_ids":["role-001"]}`
	req := httptest.NewRequest(http.MethodPost, "/roles/resolve-permissions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleResolvePermissions_InvalidJSON(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/resolve-permissions", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleResolvePermissions_EmptyRoleIDs(t *testing.T) {
	roleSvc := &mockRoleService{
		resolvePermissionsFn: func(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
			return []permission.Permission{}, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	body := `{"role_ids":[]}`
	req := httptest.NewRequest(http.MethodPost, "/roles/resolve-permissions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp resolvePermissionsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Permissions) != 0 {
		t.Fatalf("permissions length = %d, want 0", len(resp.Permissions))
	}
}

// =========================================================================
// Certificate handler tests
// =========================================================================

// ---------------------------------------------------------------------------
// POST /certs/generate (GenerateCert)
// ---------------------------------------------------------------------------

func TestHandleGenerateCert_Success(t *testing.T) {
	prov := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			if req.Username != "alice" || req.TenantID != "tenant-1" {
				t.Fatalf("unexpected request: %+v", req)
			}
			if req.TTL != 24*time.Hour {
				t.Fatalf("ttl = %v, want 24h", req.TTL)
			}
			return &cert.CertBundle{
				CertPEM: []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----"),
				KeyPEM:  []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----"),
				Serial:  "AABBCCDD",
			}, nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"username":"alice","tenantID":"tenant-1","ttl":"24h"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	assertContentType(t, rec, "application/json")

	var resp certBundleResponse
	decodeJSON(t, rec, &resp)
	if resp.Serial != "AABBCCDD" {
		t.Fatalf("serial = %q, want %q", resp.Serial, "AABBCCDD")
	}
	if !strings.Contains(resp.CertPEM, "BEGIN CERTIFICATE") {
		t.Fatal("expected PEM certificate in response")
	}
}

func TestHandleGenerateCert_NoTTL(t *testing.T) {
	prov := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			if req.TTL != 0 {
				t.Fatalf("ttl = %v, want 0 (default)", req.TTL)
			}
			return &cert.CertBundle{
				CertPEM: []byte("cert"),
				KeyPEM:  []byte("key"),
				Serial:  "001",
			}, nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
}

func TestHandleGenerateCert_InvalidTTL(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	body := `{"username":"alice","tenantID":"tenant-1","ttl":"not-a-duration"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleGenerateCert_ServiceError(t *testing.T) {
	prov := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, &cert.CertGenerateError{Err: errors.New("CA key not loaded")}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleGenerateCert_BarrierSealed(t *testing.T) {
	prov := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, &cert.BarrierSealedError{}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

func TestHandleGenerateCert_CAKeyNotAvailable(t *testing.T) {
	prov := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, &cert.CAKeyNotAvailableError{}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

func TestHandleGenerateCert_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	body := `{"username":"alice","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGenerateCert_InvalidJSON(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleGenerateCert_ValidationError(t *testing.T) {
	prov := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, &cert.ValidationError{Field: "username", Reason: "must not be empty"}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"username":"","tenantID":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// POST /certs/export (ExportCert)
// ---------------------------------------------------------------------------

func TestHandleExportCert_Success(t *testing.T) {
	p12Data := []byte("fake-pkcs12-archive-bytes")
	prov := &mockCertProvider{
		exportPKCS12Fn: func(_ context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
			if string(certPEM) != "cert-pem" {
				t.Fatalf("certPEM = %q, want %q", certPEM, "cert-pem")
			}
			if string(keyPEM) != "key-pem" {
				t.Fatalf("keyPEM = %q, want %q", keyPEM, "key-pem")
			}
			if passphrase != "secret" {
				t.Fatalf("passphrase = %q, want %q", passphrase, "secret")
			}
			return p12Data, nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"certPEM":"cert-pem","keyPEM":"key-pem","passphrase":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/export", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp exportCertResponse
	decodeJSON(t, rec, &resp)
	expected := base64.StdEncoding.EncodeToString(p12Data)
	if resp.PKCS12 != expected {
		t.Fatalf("pkcs12 = %q, want %q", resp.PKCS12, expected)
	}
}

func TestHandleExportCert_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	body := `{"certPEM":"c","keyPEM":"k","passphrase":"p"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/export", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleExportCert_InvalidJSON(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodPost, "/certs/export", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleExportCert_ServiceError(t *testing.T) {
	prov := &mockCertProvider{
		exportPKCS12Fn: func(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
			return nil, &cert.CertExportError{Err: errors.New("export failed")}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"certPEM":"c","keyPEM":"k","passphrase":"p"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/export", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// POST /certs/revoke (RevokeCert)
// ---------------------------------------------------------------------------

func TestHandleRevokeCert_Success(t *testing.T) {
	prov := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) error {
			if serial != "AABBCCDD" {
				t.Fatalf("serial = %q, want %q", serial, "AABBCCDD")
			}
			return nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"serial":"AABBCCDD"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp okResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want %q", resp.Status, "ok")
	}
}

func TestHandleRevokeCert_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	body := `{"serial":"AABBCCDD"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleRevokeCert_InvalidJSON(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRevokeCert_NotFound(t *testing.T) {
	prov := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) error {
			return &cert.CertNotFoundError{Serial: serial}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"serial":"nonexistent"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleRevokeCert_AlreadyRevoked(t *testing.T) {
	prov := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) error {
			return &cert.CertRevokedError{Serial: serial}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"serial":"AABBCCDD"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleRevokeCert_StoreError(t *testing.T) {
	prov := &mockCertProvider{
		revokeCertFn: func(_ context.Context, _ string) error {
			return &cert.StoreError{Op: "revoke", Err: errors.New("disk full")}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"serial":"AABBCCDD"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// GET /certs/list (ListCerts)
// ---------------------------------------------------------------------------

func TestHandleListCerts_Success(t *testing.T) {
	issuedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	prov := &mockCertProvider{
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			if tenantID != "tenant-1" {
				t.Fatalf("tenantID = %q, want %q", tenantID, "tenant-1")
			}
			return []*cert.CertRecord{
				{
					Serial:    "AA",
					Username:  "alice",
					TenantID:  "tenant-1",
					IssuedAt:  issuedAt,
					ExpiresAt: expiresAt,
					Revoked:   false,
				},
				{
					Serial:    "BB",
					Username:  "bob",
					TenantID:  "tenant-1",
					IssuedAt:  issuedAt,
					ExpiresAt: expiresAt,
					Revoked:   true,
				},
			}, nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodGet, "/certs/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp listCertsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Certs) != 2 {
		t.Fatalf("records length = %d, want 2", len(resp.Certs))
	}
	if resp.Certs[0].Serial != "AA" {
		t.Fatalf("serial[0] = %q, want %q", resp.Certs[0].Serial, "AA")
	}
	if resp.Certs[1].Revoked != true {
		t.Fatal("records[1].Revoked should be true")
	}
	if resp.Certs[0].IssuedAt != issuedAt.Format(time.RFC3339) {
		t.Fatalf("issuedAt = %q, want %q", resp.Certs[0].IssuedAt, issuedAt.Format(time.RFC3339))
	}
}

func TestHandleListCerts_MissingTenantID(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodGet, "/certs/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleListCerts_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/certs/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleListCerts_ServiceError(t *testing.T) {
	prov := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return nil, &cert.StoreError{Op: "list", Err: errors.New("io error")}
		},
	}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodGet, "/certs/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleListCerts_EmptyResult(t *testing.T) {
	prov := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return []*cert.CertRecord{}, nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodGet, "/certs/list?tenantID=tenant-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listCertsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Certs) != 0 {
		t.Fatalf("expected empty list, got %d records", len(resp.Certs))
	}
}

// ---------------------------------------------------------------------------
// Cert conversion edge cases
// ---------------------------------------------------------------------------

func TestCertRecordToResponse_Nil(t *testing.T) {
	resp := certRecordToResponse(nil)
	if resp != nil {
		t.Fatal("expected nil response for nil record")
	}
}

// =========================================================================
// Error mapping tests
// =========================================================================

func TestMapUserError_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "ValidationError",
			err:      &user.ValidationError{Field: "username", Reason: "empty"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "UserNotFoundError",
			err:      &user.UserNotFoundError{ID: 123},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "UsernameExistsError",
			err:      &user.UsernameExistsError{Username: "alice", TenantID: "t1"},
			wantCode: http.StatusConflict,
		},
		{
			name:     "SystemUserImmutableError",
			err:      &user.SystemUserImmutableError{ID: 9001},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "StoreError",
			err:      &user.StoreError{Op: "put", Err: errors.New("fail")},
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "UnknownError",
			err:      errors.New("unknown error"),
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapUserError(tt.err)
			if got != tt.wantCode {
				t.Fatalf("mapUserError(%T) = %d, want %d", tt.err, got, tt.wantCode)
			}
		})
	}
}

func TestMapRoleError_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "ValidationError",
			err:      &role.ValidationError{Field: "name", Reason: "empty"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "RoleNotFoundError",
			err:      &role.RoleNotFoundError{ID: "123"},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "RoleNameExistsError",
			err:      &role.RoleNameExistsError{Name: "admin", TenantID: "t1"},
			wantCode: http.StatusConflict,
		},
		{
			name:     "SystemRoleImmutableError",
			err:      &role.SystemRoleImmutableError{ID: "sys-admin"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "InvalidPermissionError",
			err:      &role.InvalidPermissionError{Permission: "bad:perm"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "StoreError",
			err:      &role.StoreError{Op: "put", Err: errors.New("fail")},
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "UnknownError",
			err:      errors.New("unknown error"),
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapRoleError(tt.err)
			if got != tt.wantCode {
				t.Fatalf("mapRoleError(%T) = %d, want %d", tt.err, got, tt.wantCode)
			}
		})
	}
}

func TestMapCertError_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "ValidationError",
			err:      &cert.ValidationError{Field: "username", Reason: "empty"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "CertNotFoundError",
			err:      &cert.CertNotFoundError{Serial: "AA"},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "CertRevokedError",
			err:      &cert.CertRevokedError{Serial: "AA"},
			wantCode: http.StatusConflict,
		},
		{
			name:     "BarrierSealedError",
			err:      &cert.BarrierSealedError{},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "CAKeyNotAvailableError",
			err:      &cert.CAKeyNotAvailableError{},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "CertGenerateError",
			err:      &cert.CertGenerateError{Err: errors.New("fail")},
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "CertExportError",
			err:      &cert.CertExportError{Err: errors.New("fail")},
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "StoreError",
			err:      &cert.StoreError{Op: "put", Err: errors.New("fail")},
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "UnknownError",
			err:      errors.New("unknown error"),
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapCertError(tt.err)
			if got != tt.wantCode {
				t.Fatalf("mapCertError(%T) = %d, want %d", tt.err, got, tt.wantCode)
			}
		})
	}
}

// =========================================================================
// ServerOption tests for auth services
// =========================================================================

func TestWithUserService_Option(t *testing.T) {
	svc := &mockUserService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithUserService(svc))
	if srv.userSvc == nil {
		t.Fatal("userSvc should not be nil after WithUserService")
	}
}

func TestWithRoleService_Option(t *testing.T) {
	svc := &mockRoleService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithRoleService(svc))
	if srv.roleSvc == nil {
		t.Fatal("roleSvc should not be nil after WithRoleService")
	}
}

func TestWithCertProvider_Option(t *testing.T) {
	prov := &mockCertProvider{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithCertProvider(prov))
	if srv.certProv == nil {
		t.Fatal("certProv should not be nil after WithCertProvider")
	}
}

// =========================================================================
// Route registration tests
// =========================================================================

func TestUserRoutes_RegisteredWhenServiceProvided(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	// /users should be registered as POST.
	if _, ok := srv.routes["/users"]; !ok {
		t.Fatal("/users route should be registered when UserService is provided")
	}
	if _, ok := srv.routes["/users/list"]; !ok {
		t.Fatal("/users/list route should be registered when UserService is provided")
	}
}

func TestUserRoutes_NotRegisteredWithoutService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	if _, ok := srv.routes["/users"]; ok {
		t.Fatal("/users route should NOT be registered without UserService")
	}
	if _, ok := srv.routes["/users/list"]; ok {
		t.Fatal("/users/list route should NOT be registered without UserService")
	}
}

func TestRoleRoutes_RegisteredWhenServiceProvided(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	for _, path := range []string{"/roles", "/roles/list", "/roles/assign", "/roles/unassign", "/permissions"} {
		if _, ok := srv.routes[path]; !ok {
			t.Fatalf("%s route should be registered when RoleService is provided", path)
		}
	}
}

func TestRoleRoutes_NotRegisteredWithoutService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	for _, path := range []string{"/roles", "/roles/list", "/roles/assign", "/roles/unassign", "/permissions"} {
		if _, ok := srv.routes[path]; ok {
			t.Fatalf("%s route should NOT be registered without RoleService", path)
		}
	}
}

func TestCertRoutes_RegisteredWhenProviderProvided(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	for _, path := range []string{"/certs/generate", "/certs/export", "/certs/revoke", "/certs/list"} {
		if _, ok := srv.routes[path]; !ok {
			t.Fatalf("%s route should be registered when CertProvider is provided", path)
		}
	}
}

func TestCertRoutes_NotRegisteredWithoutProvider(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	for _, path := range []string{"/certs/generate", "/certs/export", "/certs/revoke", "/certs/list"} {
		if _, ok := srv.routes[path]; ok {
			t.Fatalf("%s route should NOT be registered without CertProvider", path)
		}
	}
}

// =========================================================================
// Dispatch method enforcement tests
// =========================================================================

func TestDispatch_UserCreate_WrongMethod(t *testing.T) {
	svc := &mockUserService{}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestDispatch_RoleCreate_WrongMethod(t *testing.T) {
	roleSvc := &mockRoleService{}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestDispatch_CertsGenerate_WrongMethod(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodGet, "/certs/generate", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestDispatch_CertsList_WrongMethod(t *testing.T) {
	prov := &mockCertProvider{}
	srv := newAuthServer(nil, nil, prov)

	req := httptest.NewRequest(http.MethodPost, "/certs/list?tenantID=t1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}
