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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// mockUserService
// ---------------------------------------------------------------------------

// mockUserService is a configurable mock for user.UserServicer.
type mockUserService struct {
	createUserFn        func(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
	getUserFn           func(ctx context.Context, id uint64) (*user.User, error)
	getUserByUsernameFn func(ctx context.Context, tenantID, username string) (*user.User, error)
	getUserByEmailFn    func(ctx context.Context, tenantID, email string) (*user.User, error)
	updateUserFn        func(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
	deleteUserFn        func(ctx context.Context, id uint64) error
	listUsersFn         func(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
	enableUserFn        func(ctx context.Context, id uint64) error
	disableUserFn       func(ctx context.Context, id uint64) error
}

func (m *mockUserService) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	return m.createUserFn(ctx, req)
}

func (m *mockUserService) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	return m.getUserFn(ctx, id)
}

func (m *mockUserService) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	return m.getUserByUsernameFn(ctx, tenantID, username)
}

func (m *mockUserService) GetUserByEmail(ctx context.Context, tenantID, email string) (*user.User, error) {
	return m.getUserByEmailFn(ctx, tenantID, email)
}

func (m *mockUserService) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	return m.updateUserFn(ctx, req)
}

func (m *mockUserService) DeleteUser(ctx context.Context, id uint64) error {
	return m.deleteUserFn(ctx, id)
}

func (m *mockUserService) ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return m.listUsersFn(ctx, req)
}

func (m *mockUserService) EnableUser(ctx context.Context, id uint64) error {
	return m.enableUserFn(ctx, id)
}

func (m *mockUserService) DisableUser(ctx context.Context, id uint64) error {
	return m.disableUserFn(ctx, id)
}

func (m *mockUserService) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserService) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// mockRoleService
// ---------------------------------------------------------------------------

// mockRoleService is a configurable mock for role.RoleServicer.
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
	return m.createRoleFn(ctx, req)
}

func (m *mockRoleService) GetRole(ctx context.Context, id string) (*role.Role, error) {
	return m.getRoleFn(ctx, id)
}

func (m *mockRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	return m.getRoleByNameFn(ctx, tenantID, name)
}

func (m *mockRoleService) UpdateRole(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	return m.updateRoleFn(ctx, req)
}

func (m *mockRoleService) DeleteRole(ctx context.Context, id string) error {
	return m.deleteRoleFn(ctx, id)
}

func (m *mockRoleService) ListRoles(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return m.listRolesFn(ctx, req)
}

func (m *mockRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	return m.resolvePermissionsFn(ctx, roleIDs)
}

// ---------------------------------------------------------------------------
// mockCertProvider
// ---------------------------------------------------------------------------

// mockCertProvider is a configurable mock for cert.CertProvider.
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
// Shared test helpers
// ---------------------------------------------------------------------------

// testUser returns a fully populated user for test assertions.
func testUser(id uint64, username, tenantID string) *user.User {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	return &user.User{
		ID:        id,
		Username:  username,
		TenantID:  tenantID,
		RoleIDs:   []string{"role-1"},
		Enabled:   true,
		System:    false,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// testRole returns a fully populated role for test assertions.
func testRole(id, name, tenantID string) *role.Role {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	return &role.Role{
		ID:          id,
		Name:        name,
		TenantID:    tenantID,
		Permissions: []permission.Permission{"kv:put", "kv:get"},
		System:      false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}
