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

package embedded

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

// mockUserServicer implements user.UserServicer with function-field injection.
type mockUserServicer struct {
	createUserFn      func(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
	getUserFn         func(ctx context.Context, id uint64) (*user.User, error)
	getUserByUserFn   func(ctx context.Context, tenantID, username string) (*user.User, error)
	getUserByEmailFn  func(ctx context.Context, tenantID, email string) (*user.User, error)
	updateUserFn      func(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
	deleteUserFn      func(ctx context.Context, id uint64) error
	listUsersFn       func(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
	enableUserFn      func(ctx context.Context, id uint64) error
	disableUserFn     func(ctx context.Context, id uint64) error
	userByCertThumbFn func(ctx context.Context, thumbprint string) (*user.User, error)
}

func (m *mockUserServicer) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	return m.createUserFn(ctx, req)
}

func (m *mockUserServicer) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	return m.getUserFn(ctx, id)
}

func (m *mockUserServicer) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	return m.getUserByUserFn(ctx, tenantID, username)
}

func (m *mockUserServicer) GetUserByEmail(ctx context.Context, tenantID, email string) (*user.User, error) {
	return m.getUserByEmailFn(ctx, tenantID, user.NormalizeEmail(email))
}

func (m *mockUserServicer) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	return m.updateUserFn(ctx, req)
}

func (m *mockUserServicer) DeleteUser(ctx context.Context, id uint64) error {
	return m.deleteUserFn(ctx, id)
}

func (m *mockUserServicer) ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return m.listUsersFn(ctx, req)
}

func (m *mockUserServicer) EnableUser(ctx context.Context, id uint64) error {
	return m.enableUserFn(ctx, id)
}

func (m *mockUserServicer) DisableUser(ctx context.Context, id uint64) error {
	return m.disableUserFn(ctx, id)
}

func (m *mockUserServicer) UserByCertThumbprint(ctx context.Context, thumbprint string) (*user.User, error) {
	if m.userByCertThumbFn != nil {
		return m.userByCertThumbFn(ctx, thumbprint)
	}
	return nil, nil
}

func (m *mockUserServicer) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

// mockRoleServicer implements role.RoleServicer with function-field injection.
type mockRoleServicer struct {
	createRoleFn    func(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error)
	getRoleFn       func(ctx context.Context, id string) (*role.Role, error)
	getRoleByNameFn func(ctx context.Context, tenantID, name string) (*role.Role, error)
	updateRoleFn    func(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error)
	deleteRoleFn    func(ctx context.Context, id string) error
	listRolesFn     func(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error)
	resolvePermsFn  func(ctx context.Context, roleIDs []string) ([]permission.Permission, error)
}

func (m *mockRoleServicer) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	return m.createRoleFn(ctx, req)
}

func (m *mockRoleServicer) GetRole(ctx context.Context, id string) (*role.Role, error) {
	return m.getRoleFn(ctx, id)
}

func (m *mockRoleServicer) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	return m.getRoleByNameFn(ctx, tenantID, name)
}

func (m *mockRoleServicer) UpdateRole(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	return m.updateRoleFn(ctx, req)
}

func (m *mockRoleServicer) DeleteRole(ctx context.Context, id string) error {
	return m.deleteRoleFn(ctx, id)
}

func (m *mockRoleServicer) ListRoles(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return m.listRolesFn(ctx, req)
}

func (m *mockRoleServicer) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	return m.resolvePermsFn(ctx, roleIDs)
}

// mockCertProvider implements cert.CertProvider with function-field injection.
type mockCertProvider struct {
	generateClientCertFn func(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error)
	exportPKCS12Fn       func(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	revokeCertFn         func(ctx context.Context, serial string) error
	isRevokedFn          func(ctx context.Context, serial string) (bool, error)
	listCertsFn          func(ctx context.Context, tenantID string) ([]*cert.CertRecord, error)
	caCertPEMFn          func() []byte
}

func (m *mockCertProvider) GenerateClientCert(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
	return m.generateClientCertFn(ctx, req)
}

func (m *mockCertProvider) ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	return m.exportPKCS12Fn(ctx, certPEM, keyPEM, passphrase)
}

func (m *mockCertProvider) RevokeCert(ctx context.Context, serial string) error {
	return m.revokeCertFn(ctx, serial)
}

func (m *mockCertProvider) IsRevoked(ctx context.Context, serial string) (bool, error) {
	return m.isRevokedFn(ctx, serial)
}

func (m *mockCertProvider) ListCerts(ctx context.Context, tenantID string) ([]*cert.CertRecord, error) {
	return m.listCertsFn(ctx, tenantID)
}

func (m *mockCertProvider) CACertPEM() []byte {
	return m.caCertPEMFn()
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testTime returns a deterministic time for test assertions.
func testTime() time.Time {
	return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
}

// testUser returns a standard user for test assertions.
func testUser() *user.User {
	return &user.User{
		ID:        1001,
		Username:  "alice",
		TenantID:  "tenant-a",
		RoleIDs:   []string{"role-admin"},
		Enabled:   true,
		System:    false,
		CreatedAt: testTime(),
		UpdatedAt: testTime(),
	}
}

// testRole returns a standard role for test assertions.
func testRole() *role.Role {
	return &role.Role{
		ID:          "role-001",
		Name:        "admin",
		TenantID:    "tenant-a",
		Permissions: []permission.Permission{"kv:put", "kv:get"},
		System:      false,
		CreatedAt:   testTime(),
		UpdatedAt:   testTime(),
	}
}

// newAuthTransport creates a Transport with the given auth services injected.
func newAuthTransport(t *testing.T, opts ...Option) *Transport {
	t.Helper()
	tr, err := New(newMockKVService(), &mockAdminService{}, opts...)
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	return tr
}

// assertNotImplementedError verifies that err is a *transport.NotImplementedError
// with the expected method name.
func assertNotImplementedError(t *testing.T, err error, method string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected NotImplementedError, got nil")
	}
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Fatalf("expected *transport.NotImplementedError, got %T: %v", err, err)
	}
	if nie.Method != method {
		t.Errorf("expected method %q, got %q", method, nie.Method)
	}
}

// ---------------------------------------------------------------------------
// UserClient tests
// ---------------------------------------------------------------------------

func TestCreateUser_Success(t *testing.T) {
	mock := &mockUserServicer{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			if req.Username != "alice" {
				t.Errorf("expected username 'alice', got %q", req.Username)
			}
			if req.TenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", req.TenantID)
			}
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
		TenantID: "tenant-a",
		RoleIDs:  []string{"role-admin"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID != 1001 {
		t.Errorf("expected ID 1001, got %d", u.ID)
	}
	if u.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", u.Username)
	}
	if !u.Enabled {
		t.Error("expected enabled to be true")
	}
}

func TestCreateUser_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
	})
	assertNotImplementedError(t, err, "CreateUser")
}

func TestCreateUser_ServiceError(t *testing.T) {
	svcErr := &user.UsernameExistsError{Username: "alice", TenantID: "tenant-a"}
	mock := &mockUserServicer{
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, svcErr
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
		TenantID: "tenant-a",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var uee *user.UsernameExistsError
	if !errors.As(err, &uee) {
		t.Errorf("expected *user.UsernameExistsError, got %T", err)
	}
}

// TestGetUser_Uint64IDRoundTrip proves a large uint64 user id passes through
// the in-process embedded transport unchanged on both request and response.
func TestGetUser_Uint64IDRoundTrip(t *testing.T) {
	const wantID uint64 = 1<<63 + 31415
	var got uint64
	mock := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			got = id
			return &user.User{ID: id, Username: "snowflake", Enabled: true}, nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	u, err := tr.GetUser(context.Background(), wantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantID {
		t.Fatalf("request id lost: got %d, want %d", got, wantID)
	}
	if u.ID != wantID {
		t.Fatalf("response id lost: got %d, want %d", u.ID, wantID)
	}
}

func TestGetUser_Success(t *testing.T) {
	mock := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != 1001 {
				t.Errorf("expected id 1001, got %d", id)
			}
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	u, err := tr.GetUser(context.Background(), 1001)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID != 1001 {
		t.Errorf("expected ID 1001, got %d", u.ID)
	}
	if u.TenantID != "tenant-a" {
		t.Errorf("expected tenantID 'tenant-a', got %q", u.TenantID)
	}
}

func TestGetUser_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.GetUser(context.Background(), 1001)
	assertNotImplementedError(t, err, "GetUser")
}

func TestGetUser_NotFound(t *testing.T) {
	mock := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	_, err := tr.GetUser(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Errorf("expected *user.UserNotFoundError, got %T", err)
	}
	if unf.ID != 9999 {
		t.Errorf("expected ID 9999, got %d", unf.ID)
	}
}

func TestGetUserByUsername_Success(t *testing.T) {
	mock := &mockUserServicer{
		getUserByUserFn: func(_ context.Context, tenantID, username string) (*user.User, error) {
			if tenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", tenantID)
			}
			if username != "alice" {
				t.Errorf("expected username 'alice', got %q", username)
			}
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	u, err := tr.GetUserByUsername(context.Background(), "tenant-a", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID != 1001 {
		t.Errorf("expected ID 1001, got %d", u.ID)
	}
	if u.TenantID != "tenant-a" {
		t.Errorf("expected tenantID 'tenant-a', got %q", u.TenantID)
	}
}

func TestGetUserByUsername_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.GetUserByUsername(context.Background(), "tenant-a", "alice")
	assertNotImplementedError(t, err, "GetUserByUsername")
}

func TestGetUserByUsername_NotFound(t *testing.T) {
	mock := &mockUserServicer{
		getUserByUserFn: func(_ context.Context, _, username string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: username}
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	_, err := tr.GetUserByUsername(context.Background(), "tenant-a", "missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Errorf("expected *user.UserNotFoundError, got %T", err)
	}
	if unf.Lookup != "missing" {
		t.Errorf("expected Lookup 'missing', got %q", unf.Lookup)
	}
}

func TestUpdateUser_Success(t *testing.T) {
	mock := &mockUserServicer{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			if req.ID != 1001 {
				t.Errorf("expected ID 'user-001', got %d", req.ID)
			}
			if req.Username != "alice-updated" {
				t.Errorf("expected username 'alice-updated', got %q", req.Username)
			}
			u := testUser()
			u.Username = req.Username
			return u, nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	u, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{
		ID:       1001,
		Username: "alice-updated",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Username != "alice-updated" {
		t.Errorf("expected username 'alice-updated', got %q", u.Username)
	}
}

func TestUpdateUser_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{
		ID:       1001,
		Username: "alice-updated",
	})
	assertNotImplementedError(t, err, "UpdateUser")
}

func TestUpdateUser_ServiceError(t *testing.T) {
	mock := &mockUserServicer{
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: 9999}
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	_, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{
		ID: 9999,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Errorf("expected *user.UserNotFoundError, got %T", err)
	}
}

func TestDeleteUser_Success(t *testing.T) {
	called := false
	mock := &mockUserServicer{
		deleteUserFn: func(_ context.Context, id uint64) error {
			if id != 1001 {
				t.Errorf("expected id 1001, got %d", id)
			}
			called = true
			return nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	if err := tr.DeleteUser(context.Background(), 1001); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected deleteUserFn to be called")
	}
}

func TestDeleteUser_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	err := tr.DeleteUser(context.Background(), 1001)
	assertNotImplementedError(t, err, "DeleteUser")
}

func TestDeleteUser_SystemUser(t *testing.T) {
	mock := &mockUserServicer{
		deleteUserFn: func(_ context.Context, id uint64) error {
			return &user.SystemUserImmutableError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	err := tr.DeleteUser(context.Background(), 9001)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var sue *user.SystemUserImmutableError
	if !errors.As(err, &sue) {
		t.Errorf("expected *user.SystemUserImmutableError, got %T", err)
	}
}

func TestListUsers_Success(t *testing.T) {
	mock := &mockUserServicer{
		listUsersFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			if req.TenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", req.TenantID)
			}
			if req.Page != 1 {
				t.Errorf("expected page 1, got %d", req.Page)
			}
			if req.PageSize != 10 {
				t.Errorf("expected pageSize 10, got %d", req.PageSize)
			}
			return &user.ListUsersResponse{
				Users:   []*user.User{testUser()},
				Total:   1,
				HasMore: false,
			}, nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	resp, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{
		TenantID: "tenant-a",
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(resp.Users))
	}
	if resp.Users[0].ID != 1001 {
		t.Errorf("expected user ID 'user-001', got %d", resp.Users[0].ID)
	}
	if resp.Total != 1 {
		t.Errorf("expected total 1, got %d", resp.Total)
	}
	if resp.HasMore {
		t.Error("expected hasMore to be false")
	}
}

func TestListUsers_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{
		TenantID: "tenant-a",
	})
	assertNotImplementedError(t, err, "ListUsers")
}

func TestListUsers_Pagination(t *testing.T) {
	mock := &mockUserServicer{
		listUsersFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			u1 := testUser()
			u2 := testUser()
			u2.ID = 1002
			u2.Username = "bob"
			return &user.ListUsersResponse{
				Users:   []*user.User{u1, u2},
				Total:   5,
				HasMore: true,
			}, nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	resp, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{
		TenantID: "tenant-a",
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(resp.Users))
	}
	if resp.Total != 5 {
		t.Errorf("expected total 5, got %d", resp.Total)
	}
	if !resp.HasMore {
		t.Error("expected hasMore to be true")
	}
}

func TestListUsers_ServiceError(t *testing.T) {
	mock := &mockUserServicer{
		listUsersFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return nil, &user.StoreError{Op: "list", Detail: "connection reset"}
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	_, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{
		TenantID: "tenant-a",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *user.StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected *user.StoreError, got %T", err)
	}
}

func TestEnableUser_Success(t *testing.T) {
	called := false
	mock := &mockUserServicer{
		enableUserFn: func(_ context.Context, id uint64) error {
			if id != 1001 {
				t.Errorf("expected id 1001, got %d", id)
			}
			called = true
			return nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	if err := tr.EnableUser(context.Background(), 1001); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected enableUserFn to be called")
	}
}

func TestEnableUser_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	err := tr.EnableUser(context.Background(), 1001)
	assertNotImplementedError(t, err, "EnableUser")
}

func TestEnableUser_ServiceError(t *testing.T) {
	mock := &mockUserServicer{
		enableUserFn: func(_ context.Context, id uint64) error {
			return &user.UserNotFoundError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	err := tr.EnableUser(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Errorf("expected *user.UserNotFoundError, got %T", err)
	}
}

func TestDisableUser_Success(t *testing.T) {
	called := false
	mock := &mockUserServicer{
		disableUserFn: func(_ context.Context, id uint64) error {
			if id != 1001 {
				t.Errorf("expected id 1001, got %d", id)
			}
			called = true
			return nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	if err := tr.DisableUser(context.Background(), 1001); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected disableUserFn to be called")
	}
}

func TestDisableUser_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	err := tr.DisableUser(context.Background(), 1001)
	assertNotImplementedError(t, err, "DisableUser")
}

func TestDisableUser_ServiceError(t *testing.T) {
	mock := &mockUserServicer{
		disableUserFn: func(_ context.Context, id uint64) error {
			return &user.UserNotFoundError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	err := tr.DisableUser(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Errorf("expected *user.UserNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// RoleClient tests
// ---------------------------------------------------------------------------

func TestCreateRole_Success(t *testing.T) {
	mock := &mockRoleServicer{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			if req.Name != "admin" {
				t.Errorf("expected name 'admin', got %q", req.Name)
			}
			if req.TenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", req.TenantID)
			}
			if len(req.Permissions) != 2 {
				t.Fatalf("expected 2 permissions, got %d", len(req.Permissions))
			}
			if req.Permissions[0] != "kv:put" {
				t.Errorf("expected permission 'kv:put', got %q", req.Permissions[0])
			}
			if req.Permissions[1] != "kv:get" {
				t.Errorf("expected permission 'kv:get', got %q", req.Permissions[1])
			}
			return testRole(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	rl, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		Name:        "admin",
		TenantID:    "tenant-a",
		Permissions: []string{"kv:put", "kv:get"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rl.ID != "role-001" {
		t.Errorf("expected ID 'role-001', got %q", rl.ID)
	}
	if rl.Name != "admin" {
		t.Errorf("expected name 'admin', got %q", rl.Name)
	}
	if len(rl.Permissions) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(rl.Permissions))
	}
	if rl.Permissions[0] != "kv:put" {
		t.Errorf("expected permission 'kv:put', got %q", rl.Permissions[0])
	}
}

func TestCreateRole_ForwardsPreAssignedID(t *testing.T) {
	var captured string
	mock := &mockRoleServicer{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			captured = req.ID
			r := testRole()
			r.ID = req.ID
			return r, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	const wantID = "deterministic-admin-id"
	rl, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		ID:          wantID,
		Name:        "admin",
		TenantID:    "tenant-a",
		Permissions: []string{"kv:put"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != wantID {
		t.Errorf("forwarded ID: got %q, want %q", captured, wantID)
	}
	if rl.ID != wantID {
		t.Errorf("returned role ID: got %q, want %q", rl.ID, wantID)
	}
}

func TestCreateRole_EmptyIDPreservesAutoGeneration(t *testing.T) {
	mock := &mockRoleServicer{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			if req.ID != "" {
				t.Errorf("expected empty ID forwarded for auto-generation, got %q", req.ID)
			}
			return testRole(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	if _, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		Name:        "admin",
		TenantID:    "tenant-a",
		Permissions: []string{"kv:put"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateRole_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		Name: "admin",
	})
	assertNotImplementedError(t, err, "CreateRole")
}

func TestCreateRole_ServiceError(t *testing.T) {
	mock := &mockRoleServicer{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &role.RoleNameExistsError{Name: "admin", TenantID: "tenant-a"}
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	_, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		Name:     "admin",
		TenantID: "tenant-a",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rne *role.RoleNameExistsError
	if !errors.As(err, &rne) {
		t.Errorf("expected *role.RoleNameExistsError, got %T", err)
	}
}

func TestGetRole_Success(t *testing.T) {
	mock := &mockRoleServicer{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			if id != "role-001" {
				t.Errorf("expected id 'role-001', got %q", id)
			}
			return testRole(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	rl, err := tr.GetRole(context.Background(), "role-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rl.ID != "role-001" {
		t.Errorf("expected ID 'role-001', got %q", rl.ID)
	}
	if rl.TenantID != "tenant-a" {
		t.Errorf("expected tenantID 'tenant-a', got %q", rl.TenantID)
	}
}

func TestGetRole_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.GetRole(context.Background(), "role-001")
	assertNotImplementedError(t, err, "GetRole")
}

func TestGetRole_NotFound(t *testing.T) {
	mock := &mockRoleServicer{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	_, err := tr.GetRole(context.Background(), "role-missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rnf *role.RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Errorf("expected *role.RoleNotFoundError, got %T", err)
	}
}

func TestUpdateRole_Success(t *testing.T) {
	mock := &mockRoleServicer{
		updateRoleFn: func(_ context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
			if req.ID != "role-001" {
				t.Errorf("expected ID 'role-001', got %q", req.ID)
			}
			if req.Name != "admin-v2" {
				t.Errorf("expected name 'admin-v2', got %q", req.Name)
			}
			if len(req.Permissions) != 1 {
				t.Fatalf("expected 1 permission, got %d", len(req.Permissions))
			}
			rl := testRole()
			rl.Name = req.Name
			rl.Permissions = req.Permissions
			return rl, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	rl, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{
		ID:          "role-001",
		Name:        "admin-v2",
		Permissions: []string{"kv:delete"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rl.Name != "admin-v2" {
		t.Errorf("expected name 'admin-v2', got %q", rl.Name)
	}
}

func TestUpdateRole_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{
		ID:   "role-001",
		Name: "admin-v2",
	})
	assertNotImplementedError(t, err, "UpdateRole")
}

func TestUpdateRole_ServiceError(t *testing.T) {
	mock := &mockRoleServicer{
		updateRoleFn: func(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: "role-missing"}
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	_, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{
		ID: "role-missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rnf *role.RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Errorf("expected *role.RoleNotFoundError, got %T", err)
	}
}

func TestDeleteRole_Success(t *testing.T) {
	called := false
	mock := &mockRoleServicer{
		deleteRoleFn: func(_ context.Context, id string) error {
			if id != "role-001" {
				t.Errorf("expected id 'role-001', got %q", id)
			}
			called = true
			return nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	if err := tr.DeleteRole(context.Background(), "role-001"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected deleteRoleFn to be called")
	}
}

func TestDeleteRole_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	err := tr.DeleteRole(context.Background(), "role-001")
	assertNotImplementedError(t, err, "DeleteRole")
}

func TestDeleteRole_SystemRole(t *testing.T) {
	mock := &mockRoleServicer{
		deleteRoleFn: func(_ context.Context, id string) error {
			return &role.SystemRoleImmutableError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	err := tr.DeleteRole(context.Background(), "system-role")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var sre *role.SystemRoleImmutableError
	if !errors.As(err, &sre) {
		t.Errorf("expected *role.SystemRoleImmutableError, got %T", err)
	}
}

func TestListRoles_Success(t *testing.T) {
	mock := &mockRoleServicer{
		listRolesFn: func(_ context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			if req.TenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", req.TenantID)
			}
			return &role.ListRolesResponse{
				Roles:   []*role.Role{testRole()},
				Total:   1,
				HasMore: false,
			}, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{
		TenantID: "tenant-a",
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(resp.Roles))
	}
	if resp.Roles[0].Name != "admin" {
		t.Errorf("expected role name 'admin', got %q", resp.Roles[0].Name)
	}
	if resp.Total != 1 {
		t.Errorf("expected total 1, got %d", resp.Total)
	}
}

func TestListRoles_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{
		TenantID: "tenant-a",
	})
	assertNotImplementedError(t, err, "ListRoles")
}

func TestListRoles_Pagination(t *testing.T) {
	mock := &mockRoleServicer{
		listRolesFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			r1 := testRole()
			r2 := testRole()
			r2.ID = "role-002"
			r2.Name = "editor"
			return &role.ListRolesResponse{
				Roles:   []*role.Role{r1, r2},
				Total:   10,
				HasMore: true,
			}, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{
		TenantID: "tenant-a",
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(resp.Roles))
	}
	if resp.Total != 10 {
		t.Errorf("expected total 10, got %d", resp.Total)
	}
	if !resp.HasMore {
		t.Error("expected hasMore to be true")
	}
}

func TestListRoles_ServiceError(t *testing.T) {
	mock := &mockRoleServicer{
		listRolesFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			return nil, &role.StoreError{Op: "list", Detail: "db closed"}
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	_, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{
		TenantID: "tenant-a",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *role.StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected *role.StoreError, got %T", err)
	}
}

func TestAssignRole_Success(t *testing.T) {
	updateCalled := false
	roleMock := &mockRoleServicer{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	userMock := &mockUserServicer{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			u := testUser()
			u.RoleIDs = []string{"role-existing"}
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			updateCalled = true
			if len(req.RoleIDs) != 2 {
				t.Errorf("expected 2 roleIDs, got %d", len(req.RoleIDs))
			}
			found := false
			for _, rid := range req.RoleIDs {
				if rid == "role-001" {
					found = true
				}
			}
			if !found {
				t.Error("expected roleIDs to contain 'role-001'")
			}
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(roleMock), WithUserService(userMock))

	if err := tr.AssignRole(context.Background(), 1001, "role-001"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updateCalled {
		t.Error("expected updateUserFn to be called")
	}
}

func TestAssignRole_AlreadyAssigned(t *testing.T) {
	roleMock := &mockRoleServicer{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	userMock := &mockUserServicer{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			u := testUser()
			u.RoleIDs = []string{"role-001"} // already has the role
			return u, nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			t.Fatal("updateUserFn should not be called when role already assigned")
			return nil, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(roleMock), WithUserService(userMock))

	// Should succeed silently (idempotent).
	if err := tr.AssignRole(context.Background(), 1001, "role-001"); err != nil {
		t.Fatalf("expected nil error for already-assigned role, got: %v", err)
	}
}

func TestAssignRole_NilRoleService(t *testing.T) {
	userMock := &mockUserServicer{}
	tr := newAuthTransport(t, WithUserService(userMock))
	err := tr.AssignRole(context.Background(), 1001, "role-001")
	assertNotImplementedError(t, err, "AssignRole")
}

func TestAssignRole_NilUserService(t *testing.T) {
	roleMock := &mockRoleServicer{}
	tr := newAuthTransport(t, WithRoleService(roleMock))
	err := tr.AssignRole(context.Background(), 1001, "role-001")
	assertNotImplementedError(t, err, "AssignRole")
}

func TestAssignRole_RoleNotFound(t *testing.T) {
	roleMock := &mockRoleServicer{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: id}
		},
	}
	userMock := &mockUserServicer{}
	tr := newAuthTransport(t, WithRoleService(roleMock), WithUserService(userMock))

	err := tr.AssignRole(context.Background(), 1001, "role-missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rnf *role.RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Errorf("expected *role.RoleNotFoundError, got %T", err)
	}
}

func TestAssignRole_UserNotFound(t *testing.T) {
	roleMock := &mockRoleServicer{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	userMock := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithRoleService(roleMock), WithUserService(userMock))

	err := tr.AssignRole(context.Background(), 9999, "role-001")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Errorf("expected *user.UserNotFoundError, got %T", err)
	}
}

func TestUnassignRole_Success(t *testing.T) {
	updateCalled := false
	roleMock := &mockRoleServicer{}
	userMock := &mockUserServicer{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			u := testUser()
			u.RoleIDs = []string{"role-001", "role-002"}
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			updateCalled = true
			if len(req.RoleIDs) != 1 {
				t.Errorf("expected 1 roleID after unassign, got %d", len(req.RoleIDs))
			}
			if req.RoleIDs[0] != "role-002" {
				t.Errorf("expected remaining role 'role-002', got %q", req.RoleIDs[0])
			}
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(roleMock), WithUserService(userMock))

	if err := tr.UnassignRole(context.Background(), 1001, "role-001"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updateCalled {
		t.Error("expected updateUserFn to be called")
	}
}

func TestUnassignRole_RoleNotInUserRoles(t *testing.T) {
	updateCalled := false
	roleMock := &mockRoleServicer{}
	userMock := &mockUserServicer{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			u := testUser()
			u.RoleIDs = []string{"role-002", "role-003"}
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			updateCalled = true
			// All existing roles should remain since role-001 was not present.
			if len(req.RoleIDs) != 2 {
				t.Errorf("expected 2 roleIDs, got %d", len(req.RoleIDs))
			}
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(roleMock), WithUserService(userMock))

	if err := tr.UnassignRole(context.Background(), 1001, "role-001"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updateCalled {
		t.Error("expected updateUserFn to be called")
	}
}

func TestUnassignRole_NilRoleService(t *testing.T) {
	userMock := &mockUserServicer{}
	tr := newAuthTransport(t, WithUserService(userMock))
	err := tr.UnassignRole(context.Background(), 1001, "role-001")
	assertNotImplementedError(t, err, "UnassignRole")
}

func TestUnassignRole_NilUserService(t *testing.T) {
	roleMock := &mockRoleServicer{}
	tr := newAuthTransport(t, WithRoleService(roleMock))
	err := tr.UnassignRole(context.Background(), 1001, "role-001")
	assertNotImplementedError(t, err, "UnassignRole")
}

func TestUnassignRole_UserNotFound(t *testing.T) {
	roleMock := &mockRoleServicer{}
	userMock := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return nil, &user.UserNotFoundError{ID: id}
		},
	}
	tr := newAuthTransport(t, WithRoleService(roleMock), WithUserService(userMock))

	err := tr.UnassignRole(context.Background(), 9999, "role-001")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Errorf("expected *user.UserNotFoundError, got %T", err)
	}
}

func TestListPermissions_ReturnsCategories(t *testing.T) {
	tr := newAuthTransport(t)

	resp, err := tr.ListPermissions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Categories) == 0 {
		t.Fatal("expected at least one permission category")
	}

	// Verify the structure of the first category.
	found := false
	for _, cat := range resp.Categories {
		if cat.Name == "" {
			t.Error("category name must not be empty")
		}
		if cat.Group == "" {
			t.Error("category group must not be empty")
		}
		if len(cat.Permissions) == 0 {
			t.Errorf("category %q must have at least one permission", cat.Name)
		}
		if cat.Name == "KV Service" {
			found = true
			kvPerms := cat.Permissions
			kvPutFound := false
			for _, p := range kvPerms {
				if p == "kv:put" {
					kvPutFound = true
				}
			}
			if !kvPutFound {
				t.Error("expected KV Service category to contain 'kv:put'")
			}
		}
	}
	if !found {
		t.Error("expected to find 'KV Service' category")
	}
}

func TestListPermissions_PermissionStringFormat(t *testing.T) {
	tr := newAuthTransport(t)

	resp, err := tr.ListPermissions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all permissions follow "resource:action" format.
	for _, cat := range resp.Categories {
		for _, p := range cat.Permissions {
			if len(p) == 0 {
				t.Error("permission string must not be empty")
				continue
			}
			// The permission.Permission type is just a string.
			// Verify it contains a colon separator.
			colonFound := false
			for _, ch := range p {
				if ch == ':' {
					colonFound = true
					break
				}
			}
			if !colonFound {
				t.Errorf("permission %q does not follow 'resource:action' format", p)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// CertClient tests
// ---------------------------------------------------------------------------

func TestGenerateCert_Success(t *testing.T) {
	mock := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			if req.Username != "alice" {
				t.Errorf("expected username 'alice', got %q", req.Username)
			}
			if req.TenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", req.TenantID)
			}
			if req.TTL != 24*time.Hour {
				t.Errorf("expected TTL 24h, got %v", req.TTL)
			}
			return &cert.CertBundle{
				CertPEM: []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----"),
				KeyPEM:  []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----"),
				Serial:  "abc123",
			}, nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	bundle, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{
		Username: "alice",
		TenantID: "tenant-a",
		TTL:      "24h",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bundle.Serial != "abc123" {
		t.Errorf("expected serial 'abc123', got %q", bundle.Serial)
	}
	if bundle.CertPEM == "" {
		t.Error("expected non-empty certPEM")
	}
	if bundle.KeyPEM == "" {
		t.Error("expected non-empty keyPEM")
	}
}

func TestGenerateCert_NoTTL(t *testing.T) {
	mock := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			if req.TTL != 0 {
				t.Errorf("expected zero TTL, got %v", req.TTL)
			}
			return &cert.CertBundle{
				CertPEM: []byte("cert"),
				KeyPEM:  []byte("key"),
				Serial:  "def456",
			}, nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	bundle, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{
		Username: "alice",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bundle.Serial != "def456" {
		t.Errorf("expected serial 'def456', got %q", bundle.Serial)
	}
}

func TestGenerateCert_InvalidTTL(t *testing.T) {
	mock := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			t.Fatal("generateClientCertFn should not be called with invalid TTL")
			return nil, nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	_, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{
		Username: "alice",
		TTL:      "not-a-duration",
	})
	if err == nil {
		t.Fatal("expected error for invalid TTL, got nil")
	}
}

func TestGenerateCert_NilProvider(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{
		Username: "alice",
	})
	assertNotImplementedError(t, err, "GenerateCert")
}

func TestGenerateCert_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, &cert.CertGenerateError{Err: errors.New("CA key sealed")}
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	_, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cge *cert.CertGenerateError
	if !errors.As(err, &cge) {
		t.Errorf("expected *cert.CertGenerateError, got %T", err)
	}
}

func TestExportCert_Success(t *testing.T) {
	p12Data := []byte("mock-pkcs12-data")
	mock := &mockCertProvider{
		exportPKCS12Fn: func(_ context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
			if string(certPEM) != "test-cert" {
				t.Errorf("expected certPEM 'test-cert', got %q", string(certPEM))
			}
			if string(keyPEM) != "test-key" {
				t.Errorf("expected keyPEM 'test-key', got %q", string(keyPEM))
			}
			if passphrase != "secret" {
				t.Errorf("expected passphrase 'secret', got %q", passphrase)
			}
			return p12Data, nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	resp, err := tr.ExportCert(context.Background(), &transport.ExportCertRequest{
		CertPEM:    "test-cert",
		KeyPEM:     "test-key",
		Passphrase: "secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := base64.StdEncoding.EncodeToString(p12Data)
	if resp.PKCS12 != expected {
		t.Errorf("expected base64 %q, got %q", expected, resp.PKCS12)
	}
}

func TestExportCert_NilProvider(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.ExportCert(context.Background(), &transport.ExportCertRequest{
		CertPEM: "cert",
		KeyPEM:  "key",
	})
	assertNotImplementedError(t, err, "ExportCert")
}

func TestExportCert_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		exportPKCS12Fn: func(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
			return nil, &cert.CertExportError{Err: errors.New("invalid PEM")}
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	_, err := tr.ExportCert(context.Background(), &transport.ExportCertRequest{
		CertPEM: "bad-cert",
		KeyPEM:  "bad-key",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cee *cert.CertExportError
	if !errors.As(err, &cee) {
		t.Errorf("expected *cert.CertExportError, got %T", err)
	}
}

func TestRevokeCert_Success(t *testing.T) {
	called := false
	mock := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) error {
			if serial != "abc123" {
				t.Errorf("expected serial 'abc123', got %q", serial)
			}
			called = true
			return nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	if err := tr.RevokeCert(context.Background(), "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected revokeCertFn to be called")
	}
}

func TestRevokeCert_NilProvider(t *testing.T) {
	tr := newAuthTransport(t)
	err := tr.RevokeCert(context.Background(), "abc123")
	assertNotImplementedError(t, err, "RevokeCert")
}

func TestRevokeCert_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) error {
			return &cert.CertNotFoundError{Serial: serial}
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	err := tr.RevokeCert(context.Background(), "not-found-serial")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cnf *cert.CertNotFoundError
	if !errors.As(err, &cnf) {
		t.Errorf("expected *cert.CertNotFoundError, got %T", err)
	}
	if cnf.Serial != "not-found-serial" {
		t.Errorf("expected serial 'not-found-serial', got %q", cnf.Serial)
	}
}

func TestListCerts_Success(t *testing.T) {
	issuedAt := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2027, 1, 10, 8, 0, 0, 0, time.UTC)
	mock := &mockCertProvider{
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			if tenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", tenantID)
			}
			return []*cert.CertRecord{
				{
					Serial:    "abc123",
					Username:  "alice",
					TenantID:  "tenant-a",
					IssuedAt:  issuedAt,
					ExpiresAt: expiresAt,
					Revoked:   false,
				},
				{
					Serial:    "def456",
					Username:  "bob",
					TenantID:  "tenant-a",
					IssuedAt:  issuedAt,
					ExpiresAt: expiresAt,
					Revoked:   true,
				},
			}, nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	resp, err := tr.ListCerts(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Certs) != 2 {
		t.Fatalf("expected 2 certs, got %d", len(resp.Certs))
	}

	c0 := resp.Certs[0]
	if c0.Serial != "abc123" {
		t.Errorf("expected serial 'abc123', got %q", c0.Serial)
	}
	if c0.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", c0.Username)
	}
	if c0.Revoked {
		t.Error("expected first cert not revoked")
	}
	expectedIssuedAt := issuedAt.Format(time.RFC3339)
	if c0.IssuedAt != expectedIssuedAt {
		t.Errorf("expected issuedAt %q, got %q", expectedIssuedAt, c0.IssuedAt)
	}
	expectedExpiresAt := expiresAt.Format(time.RFC3339)
	if c0.ExpiresAt != expectedExpiresAt {
		t.Errorf("expected expiresAt %q, got %q", expectedExpiresAt, c0.ExpiresAt)
	}

	c1 := resp.Certs[1]
	if !c1.Revoked {
		t.Error("expected second cert to be revoked")
	}
}

func TestListCerts_NilProvider(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.ListCerts(context.Background(), "tenant-a")
	assertNotImplementedError(t, err, "ListCerts")
}

func TestListCerts_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return nil, &cert.StoreError{Op: "scan", Detail: "connection refused"}
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	_, err := tr.ListCerts(context.Background(), "tenant-a")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *cert.StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected *cert.StoreError, got %T", err)
	}
}

func TestListCerts_Empty(t *testing.T) {
	mock := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return []*cert.CertRecord{}, nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	resp, err := tr.ListCerts(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Certs) != 0 {
		t.Errorf("expected 0 certs, got %d", len(resp.Certs))
	}
}

// ---------------------------------------------------------------------------
// Conversion helper tests
// ---------------------------------------------------------------------------

func TestUserToTransport_NilInput(t *testing.T) {
	result := userToTransport(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestUserToTransport_NilRoleIDs(t *testing.T) {
	u := testUser()
	u.RoleIDs = nil

	result := userToTransport(u)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.RoleIDs == nil {
		t.Fatal("expected roleIDs to be non-nil empty slice")
	}
	if len(result.RoleIDs) != 0 {
		t.Errorf("expected empty roleIDs slice, got %d elements", len(result.RoleIDs))
	}
}

func TestUserToTransport_FullConversion(t *testing.T) {
	u := testUser()
	result := userToTransport(u)

	if result.ID != u.ID {
		t.Errorf("expected ID %d, got %d", u.ID, result.ID)
	}
	if result.Username != u.Username {
		t.Errorf("expected Username %q, got %q", u.Username, result.Username)
	}
	if result.TenantID != u.TenantID {
		t.Errorf("expected TenantID %q, got %q", u.TenantID, result.TenantID)
	}
	if len(result.RoleIDs) != len(u.RoleIDs) {
		t.Errorf("expected %d roleIDs, got %d", len(u.RoleIDs), len(result.RoleIDs))
	}
	if result.Enabled != u.Enabled {
		t.Errorf("expected Enabled %v, got %v", u.Enabled, result.Enabled)
	}
	if result.System != u.System {
		t.Errorf("expected System %v, got %v", u.System, result.System)
	}
	expectedCreated := u.CreatedAt.Format(time.RFC3339)
	if result.CreatedAt != expectedCreated {
		t.Errorf("expected CreatedAt %q, got %q", expectedCreated, result.CreatedAt)
	}
	expectedUpdated := u.UpdatedAt.Format(time.RFC3339)
	if result.UpdatedAt != expectedUpdated {
		t.Errorf("expected UpdatedAt %q, got %q", expectedUpdated, result.UpdatedAt)
	}
}

func TestRoleToTransport_NilInput(t *testing.T) {
	result := roleToTransport(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestRoleToTransport_FullConversion(t *testing.T) {
	rl := testRole()
	result := roleToTransport(rl)

	if result.ID != rl.ID {
		t.Errorf("expected ID %q, got %q", rl.ID, result.ID)
	}
	if result.Name != rl.Name {
		t.Errorf("expected Name %q, got %q", rl.Name, result.Name)
	}
	if result.TenantID != rl.TenantID {
		t.Errorf("expected TenantID %q, got %q", rl.TenantID, result.TenantID)
	}
	if len(result.Permissions) != len(rl.Permissions) {
		t.Fatalf("expected %d permissions, got %d", len(rl.Permissions), len(result.Permissions))
	}
	for i, p := range rl.Permissions {
		if result.Permissions[i] != p.String() {
			t.Errorf("expected permission[%d] %q, got %q", i, p.String(), result.Permissions[i])
		}
	}
	if result.System != rl.System {
		t.Errorf("expected System %v, got %v", rl.System, result.System)
	}
	expectedCreated := rl.CreatedAt.Format(time.RFC3339)
	if result.CreatedAt != expectedCreated {
		t.Errorf("expected CreatedAt %q, got %q", expectedCreated, result.CreatedAt)
	}
}

func TestRoleToTransport_EmptyPermissions(t *testing.T) {
	rl := testRole()
	rl.Permissions = []permission.Permission{}
	result := roleToTransport(rl)

	if len(result.Permissions) != 0 {
		t.Errorf("expected 0 permissions, got %d", len(result.Permissions))
	}
}

func TestStringsToPermissions_NilInput(t *testing.T) {
	result := stringsToPermissions(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestStringsToPermissions_ValidConversion(t *testing.T) {
	input := []string{"kv:put", "kv:get", "admin:health"}
	result := stringsToPermissions(input)

	if len(result) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(result))
	}
	if result[0] != permission.Permission("kv:put") {
		t.Errorf("expected permission 'kv:put', got %q", result[0])
	}
	if result[1] != permission.Permission("kv:get") {
		t.Errorf("expected permission 'kv:get', got %q", result[1])
	}
	if result[2] != permission.Permission("admin:health") {
		t.Errorf("expected permission 'admin:health', got %q", result[2])
	}
}

func TestStringsToPermissions_EmptySlice(t *testing.T) {
	result := stringsToPermissions([]string{})
	if result == nil {
		t.Fatal("expected non-nil result for empty slice input")
	}
	if len(result) != 0 {
		t.Errorf("expected 0 permissions, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// WithUserService / WithRoleService / WithCertProvider option tests
// ---------------------------------------------------------------------------

func TestWithUserService_Option(t *testing.T) {
	mock := &mockUserServicer{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return testUser(), nil
		},
	}
	tr := newAuthTransport(t, WithUserService(mock))

	u, err := tr.GetUser(context.Background(), 1001)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID != 1001 {
		t.Errorf("expected ID 1001, got %d", u.ID)
	}
}

func TestWithRoleService_Option(t *testing.T) {
	mock := &mockRoleServicer{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	rl, err := tr.GetRole(context.Background(), "role-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rl.ID != "role-001" {
		t.Errorf("expected ID 'role-001', got %q", rl.ID)
	}
}

func TestWithCertProvider_Option(t *testing.T) {
	mock := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) error {
			return nil
		},
	}
	tr := newAuthTransport(t, WithCertProvider(mock))

	if err := tr.RevokeCert(context.Background(), "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
