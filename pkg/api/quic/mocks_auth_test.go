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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	addCertBindingFn    func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
	removeCertBindingFn func(ctx context.Context, userID uint64, thumbprint string) (*user.User, error)
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
	reconcileFn          func(ctx context.Context, tenantID string) (*role.ReconcileReport, error)
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
	issueClientCertFn func(ctx context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error)
	exportPKCS12Fn    func(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	revokeCertFn      func(ctx context.Context, serial string) (*cert.RevokeResult, error)
	isRevokedFn       func(ctx context.Context, serial string) (bool, error)
	listCertsFn       func(ctx context.Context, tenantID string) ([]*cert.CertRecord, error)
	tenantForSerialFn func(ctx context.Context, serial string) (string, bool, error)
	caCertPEMFn       func() []byte
}

func (m *mockCertProvider) IssueClientCert(ctx context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error) {
	return m.issueClientCertFn(ctx, req)
}

func (m *mockCertProvider) ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	return m.exportPKCS12Fn(ctx, certPEM, keyPEM, passphrase)
}

func (m *mockCertProvider) RevokeCert(ctx context.Context, serial string) (*cert.RevokeResult, error) {
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
		Lifecycle: user.LifecycleActive,
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
// build constraint: it is used from both the tagged (handlers_tenant_test.go)
// and untagged (handlers_coverage_extra2_test.go) tenant test files, so its
// own constraint must be the superset -- none at all.
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
