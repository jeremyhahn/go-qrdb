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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock: UserServicer
// ---------------------------------------------------------------------------

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
	suspendUserFn       func(ctx context.Context, id uint64) error
	unsuspendUserFn     func(ctx context.Context, id uint64) error
	addCertBindingFn    func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
	removeCertBindingFn func(ctx context.Context, userID uint64, thumbprint string) (*user.User, error)
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

func (m *mockUserService) GetUserByEmail(ctx context.Context, tenantID, email string) (*user.User, error) {
	if m.getUserByEmailFn != nil {
		return m.getUserByEmailFn(ctx, tenantID, email)
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

func (m *mockUserService) SuspendUser(ctx context.Context, id uint64) error {
	if m.suspendUserFn != nil {
		return m.suspendUserFn(ctx, id)
	}
	return nil
}

func (m *mockUserService) UnsuspendUser(ctx context.Context, id uint64) error {
	if m.unsuspendUserFn != nil {
		return m.unsuspendUserFn(ctx, id)
	}
	return nil
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
	// The real store never answers (nil, nil): an absent name is
	// RoleNotFoundError, which the TenantOwner decision reads as "not an owner".
	return nil, &role.RoleNotFoundError{ID: name}
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
	issueClientCertFn func(ctx context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error)
	revokeCertFn      func(ctx context.Context, serial string) (*cert.RevokeResult, error)
	isRevokedFn       func(ctx context.Context, serial string) (bool, error)
	listCertsFn       func(ctx context.Context, tenantID string) ([]*cert.CertRecord, error)
	tenantForSerialFn func(ctx context.Context, serial string) (string, bool, error)
	caCertPEMVal      []byte
}

func (m *mockCertProvider) IssueClientCert(ctx context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error) {
	if m.issueClientCertFn != nil {
		return m.issueClientCertFn(ctx, req)
	}
	return nil, nil
}

func (m *mockCertProvider) RevokeCert(ctx context.Context, serial string) (*cert.RevokeResult, error) {
	if m.revokeCertFn != nil {
		return m.revokeCertFn(ctx, serial)
	}
	return &cert.RevokeResult{Serial: serial}, nil
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
		Lifecycle: user.LifecycleActive,
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

// tenantRoleService answers GetRole with a role owned by tenantID, so a
// role-assignment write naming it passes validation.
func tenantRoleService(tenantID string) *mockRoleService {
	return &mockRoleService{getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
		return &role.Role{ID: id, TenantID: tenantID}, nil
	}}
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

// ---------------------------------------------------------------------------
// fakeCertEnroll: service.CertEnrollmentServicer double for REST handler
// tests (handleIssueCert, handleEnrollCert, the trust-anchor handlers).
// ---------------------------------------------------------------------------

type fakeCertEnroll struct {
	signFn   func(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error)
	enrollFn func(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error)
	addFn    func(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	listFn   func(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	removeFn func(ctx context.Context, tenantID, fingerprint string) error
	delegFn  func(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error)

	policyPutFn    func(ctx context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error)
	policyListFn   func(ctx context.Context, tenantID string) ([]delegation.Policy, error)
	policyDeleteFn func(ctx context.Context, tenantID, principal string) error
}

var _ service.CertEnrollmentServicer = (*fakeCertEnroll)(nil)

func (f *fakeCertEnroll) IssueDelegation(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	return f.delegFn(ctx, req)
}

func (f *fakeCertEnroll) SignCertificate(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.signFn(ctx, req)
}

func (f *fakeCertEnroll) EnrollCertificate(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.enrollFn(ctx, req)
}

func (f *fakeCertEnroll) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	return f.addFn(ctx, tenantID, bundlePEM)
}

func (f *fakeCertEnroll) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	return f.listFn(ctx, tenantID)
}

func (f *fakeCertEnroll) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	return f.removeFn(ctx, tenantID, fingerprint)
}

// newEnrollAuthServer builds a Server with ce wired as the sole
// CertEnrollmentServicer, matching newAuthServer's minimal posture.
func newEnrollAuthServer(ce service.CertEnrollmentServicer) *Server {
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithCertEnrollment(ce))
}

// fakeTenantOnboarding adapts tenant.TenantServicer.CreateTenant to
// service.TenantOnboardingServicer for handler tests that exercise
// handleCreateTenant's request/response mapping rather than the onboarding
// business rules (first-admin credential issuance), which pkg/service and
// pkg/bootstrap test directly against the real TenantOnboardingService.
type fakeTenantOnboarding struct {
	tenants tenant.TenantServicer
	// so is answered as the tenant's SO; gotReq is the last request.
	so     *service.TenantAdminCredential
	gotReq *service.OnboardTenantRequest
}

var _ service.TenantOnboardingServicer = (*fakeTenantOnboarding)(nil)

func (f *fakeTenantOnboarding) OnboardTenant(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	f.gotReq = req
	info, err := f.tenants.CreateTenant(ctx, req.ID, req.Name, req.IsolationType)
	if err != nil {
		return nil, err
	}
	return &service.OnboardTenantResult{Tenant: info, SO: f.so}, nil
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
	srv := newAuthServer(svc, tenantRoleService("tenant-1"), nil)

	body := `{"username":"alice","tenant_id":"tenant-1","role_ids":["role-admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	// Bound to the request's own tenant: the system-tenant lift was removed
	// (owner ruling, 2026-10-01), so an unbound (system-folding) caller
	// naming a different tenant would now be refused before reaching the
	// mock.
	req = req.WithContext(tenant.WithTenantID(req.Context(), "tenant-1"))
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
	if resp.Lifecycle != string(user.LifecycleActive) {
		t.Fatalf("lifecycle = %q, want %q", resp.Lifecycle, user.LifecycleActive)
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

	body := `{"username":"alice","tenant_id":"tenant-1"}`
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
	body := `{"username":"alice","tenant_id":"tenant-1"}`
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

	body := `{"username":"","tenant_id":"tenant-1"}`
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

	body := `{"username":"alice","tenant_id":"tenant-1"}`
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
			return &user.User{ID: id, Username: "snowflake", Lifecycle: user.LifecycleActive}, nil
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
		getUserFn: func(context.Context, uint64) (*user.User, error) { return testUser(), nil },
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
	srv := newAuthServer(svc, tenantRoleService("tenant-1"), nil)

	body := `{"username":"bob","role_ids":["role-admin"]}`
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
	resp := mustUserToResponse(t, nil, u)
	if resp.RoleIDs == nil {
		t.Fatal("roleIDs should be empty slice, not nil")
	}
	if len(resp.RoleIDs) != 0 {
		t.Fatalf("roleIDs length = %d, want 0", len(resp.RoleIDs))
	}
}

func TestUserToResponse_Nil(t *testing.T) {
	resp := mustUserToResponse(t, nil, nil)
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

	body := `{"name":"editor","tenant_id":"tenant-1","permissions":["kv:put","kv:get"]}`
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

	body := `{"name":"admin","tenant_id":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleCreateRole_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	body := `{"name":"admin","tenant_id":"tenant-1"}`
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

	body := `{"name":"","tenant_id":"tenant-1"}`
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

	body := `{"name":"bad","tenant_id":"tenant-1","permissions":["invalid:perm"]}`
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

	body := `{"user_id":"1001","role_id":"role-new"}`
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
	body := `{"user_id":"1001","role_id":"role-admin"}`
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

	body := `{"user_id":"1001","role_id":"role-001"}`
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

	body := `{"user_id":"1001","role_id":"role-001"}`
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

	body := `{"user_id":"1001","role_id":"nonexistent"}`
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

	body := `{"user_id":"9999","role_id":"role-001"}`
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
	body := `{"user_id":"1001","role_id":"role-new"}`
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

	body := `{"user_id":"1001","role_id":"role-admin"}`
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

	body := `{"user_id":"1001","role_id":"role-001"}`
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

	body := `{"user_id":"1001","role_id":"role-001"}`
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

	body := `{"user_id":"9999","role_id":"role-001"}`
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

	body := `{"user_id":"1001","role_id":"role-admin"}`
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
// POST /certs/issue (IssueCert)
// ---------------------------------------------------------------------------

func TestHandleIssueCert_Success(t *testing.T) {
	notAfter := testTime
	ce := &fakeCertEnroll{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.Username != "alice" {
				t.Fatalf("unexpected request: %+v", req)
			}
			if req.TTL != 24*time.Hour {
				t.Fatalf("ttl = %v, want 24h", req.TTL)
			}
			return &service.EnrolledCertificate{
				UserID:     7,
				Username:   "alice",
				TenantID:   req.TenantID,
				CertPEM:    []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----"),
				Serial:     "AABBCCDD",
				Thumbprint: "thumb-1",
				NotAfter:   notAfter,
			}, nil
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"alice","csrPEM":"csr-pem","ttl":"24h"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	assertContentType(t, rec, "application/json")

	var resp enrolledCertResponse
	decodeJSON(t, rec, &resp)
	if resp.Serial != "AABBCCDD" {
		t.Fatalf("serial = %q, want %q", resp.Serial, "AABBCCDD")
	}
	if resp.UserID != 7 {
		t.Fatalf("userID = %d, want 7", resp.UserID)
	}
	if resp.Thumbprint != "thumb-1" {
		t.Fatalf("thumbprint = %q, want %q", resp.Thumbprint, "thumb-1")
	}
	if !strings.Contains(resp.CertPEM, "BEGIN CERTIFICATE") {
		t.Fatal("expected PEM certificate in response")
	}
}

func TestHandleIssueCert_NoTTL(t *testing.T) {
	ce := &fakeCertEnroll{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.TTL != 0 {
				t.Fatalf("ttl = %v, want 0 (default)", req.TTL)
			}
			return &service.EnrolledCertificate{CertPEM: []byte("cert"), Serial: "001"}, nil
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"alice","csrPEM":"csr-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
}

func TestHandleIssueCert_InvalidTTL(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})

	body := `{"username":"alice","csrPEM":"csr-pem","ttl":"not-a-duration"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIssueCert_ServiceError(t *testing.T) {
	ce := &fakeCertEnroll{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &cert.CertGenerateError{Err: errors.New("CA key not loaded")}
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"alice","csrPEM":"csr-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleIssueCert_BarrierSealed(t *testing.T) {
	ce := &fakeCertEnroll{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &cert.BarrierSealedError{}
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"alice","csrPEM":"csr-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

func TestHandleIssueCert_CAKeyNotAvailable(t *testing.T) {
	ce := &fakeCertEnroll{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &cert.CAKeyNotAvailableError{}
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"alice","csrPEM":"csr-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

func TestHandleIssueCert_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	body := `{"username":"alice","csrPEM":"csr-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleIssueCert_InvalidJSON(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})

	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIssueCert_ValidationError(t *testing.T) {
	ce := &fakeCertEnroll{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &service.CertEnrollmentFieldError{Field: "csr_pem"}
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"alice","csrPEM":""}`
	req := httptest.NewRequest(http.MethodPost, "/certs/issue", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// POST /certs/enroll (EnrollCert)
// ---------------------------------------------------------------------------

func TestHandleEnrollCert_Success(t *testing.T) {
	ce := &fakeCertEnroll{
		enrollFn: func(_ context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.Username != "dave" {
				t.Fatalf("unexpected request: %+v", req)
			}
			return &service.EnrolledCertificate{
				Username:   "dave",
				TenantID:   req.TenantID,
				CertPEM:    []byte("-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----"),
				Thumbprint: "thumb-2",
			}, nil
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"dave","certPEM":"leaf-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/enroll", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	var resp enrolledCertResponse
	decodeJSON(t, rec, &resp)
	if resp.Thumbprint != "thumb-2" {
		t.Fatalf("thumbprint = %q, want %q", resp.Thumbprint, "thumb-2")
	}
	if resp.Serial != "" {
		t.Fatalf("serial = %q, want empty for a BYOC enrolment", resp.Serial)
	}
}

func TestHandleEnrollCert_ServiceError(t *testing.T) {
	ce := &fakeCertEnroll{
		enrollFn: func(context.Context, *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &trustanchor.NoAnchorsError{}
		},
	}
	srv := newEnrollAuthServer(ce)

	body := `{"username":"dave","certPEM":"leaf-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/enroll", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleEnrollCert_NilService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	body := `{"username":"dave","certPEM":"leaf-pem"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/enroll", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleEnrollCert_InvalidJSON(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})

	req := httptest.NewRequest(http.MethodPost, "/certs/enroll", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// POST /certs/revoke (RevokeCert)
// ---------------------------------------------------------------------------

func TestHandleRevokeCert_Success(t *testing.T) {
	prov := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			if serial != "AABBCCDD" {
				t.Fatalf("serial = %q, want %q", serial, "AABBCCDD")
			}
			return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeRecordUpdated}, nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"serial":"AABBCCDD"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp revokeCertResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want %q", resp.Status, "ok")
	}
	if !resp.RecordFound {
		t.Fatal("recordFound should be true when the provider found a local record")
	}
}

func TestHandleRevokeCert_DenyBySerial(t *testing.T) {
	prov := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeDenyBySerial}, nil
		},
	}
	srv := newAuthServer(nil, nil, prov)

	body := `{"serial":"peer-issued"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp revokeCertResponse
	decodeJSON(t, rec, &resp)
	if resp.RecordFound {
		t.Fatal("recordFound should be false for a deny-by-serial revocation")
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
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			return nil, &cert.CertNotFoundError{Serial: serial}
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
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			return nil, &cert.CertRevokedError{Serial: serial}
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
		revokeCertFn: func(_ context.Context, _ string) (*cert.RevokeResult, error) {
			return nil, &cert.StoreError{Op: "revoke", Err: errors.New("disk full")}
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
			// A store-generated user id collided with an existing identity
			// claim or primary record (go-qrdb#555's atomic create path) — a
			// client-correctable conflict exactly as a taken username is, not
			// a server fault.
			name:     "UserIDExistsError",
			err:      &user.UserIDExistsError{ID: 42, TenantID: "t1"},
			wantCode: http.StatusConflict,
		},
		{
			// A taken email is a client-correctable conflict, not a server
			// fault. Before this case it fell through to the StoreError arm
			// and surfaced as 500 (Codex review, PR #191).
			name:     "EmailExistsError",
			err:      &user.EmailExistsError{Email: "alice@example.com", TenantID: "t1"},
			wantCode: http.StatusConflict,
		},
		{
			// A thumbprint already bound to another user: the store's refusal
			// of an mTLS identity takeover, a client-visible conflict and not
			// a server fault. Fell through to 500 before go-qrdb#536.
			name:     "CertBindingConflictError",
			err:      &user.CertBindingConflictError{Thumbprint: "AAAA", BoundUserID: 7, TargetUserID: 9},
			wantCode: http.StatusConflict,
		},
		{
			// A lost race the caller can simply retry.
			name:     "ConcurrentUpdateError",
			err:      &user.ConcurrentUpdateError{ID: 7},
			wantCode: http.StatusConflict,
		},
		{
			// An undefined state, or one not permitted where it was used. The
			// error names the allowed set, so the caller can fix the request.
			name:     "InvalidLifecycleError",
			err:      &user.InvalidLifecycleError{Value: "quarantined", Op: "create"},
			wantCode: http.StatusBadRequest,
		},
		{
			// The state is valid and the caller is not wrong about it — the
			// RECORD is not somewhere this move can start from. A precondition
			// on the resource, not a malformed request. Without its own arm the
			// commonest instance, an EnableUser racing a delete, surfaced as 500:
			// a server fault for a refusal the server made deliberately.
			name:     "LifecycleTransitionError",
			err:      &user.LifecycleTransitionError{ID: 7, From: user.LifecycleDeleting, To: user.LifecycleActive},
			wantCode: http.StatusConflict,
		},
		{
			// The SAME cause, wrapped in a compensation failure, must NOT be a
			// retryable conflict. IndexCompensationError unwraps to its cause,
			// so an errors.As for ConcurrentUpdateError matches it — the mapper
			// has to check the wrapper first or it downgrades "rollback failed,
			// a login index may be inconsistent" to "re-read and retry"
			// (Codex review, PR #191).
			name: "IndexCompensationError wrapping ConcurrentUpdateError",
			err: &user.IndexCompensationError{
				Op: "update", UserID: 7,
				Cause:    &user.ConcurrentUpdateError{ID: 7},
				Failures: []error{errors.New("restore failed")},
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			// The create path's cause, which the update path's ordering did not
			// cover. CreateUser claims the username index before the email one,
			// so a taken email fails the create with EmailExistsError while the
			// username claim is already held; if releasing it then fails, the
			// compensation error's cause is EmailExistsError — and the duplicate
			// arms sat ABOVE the wrapper. The caller got a 409 "email taken",
			// retried with another email, and hit "username taken" forever on a
			// username reserved for a user record that was never written.
			name: "IndexCompensationError wrapping EmailExistsError",
			err: &user.IndexCompensationError{
				Op: "create", UserID: 7,
				Cause:    &user.EmailExistsError{Email: "a@example.com"},
				Failures: []error{errors.New("release failed")},
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			// Same ordering hazard for the username sibling: a create can fail
			// on a taken username while holding no claim, but an update that
			// claims the new username and fails to release the old one wraps
			// this cause too.
			name: "IndexCompensationError wrapping UsernameExistsError",
			err: &user.IndexCompensationError{
				Op: "create", UserID: 7,
				Cause:    &user.UsernameExistsError{Username: "a"},
				Failures: []error{errors.New("release failed")},
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			// The atomic-create sibling (go-qrdb#555): a generated id
			// collision as the wrapper's cause must outrank the plain 409
			// the cause alone would produce, the same way the username and
			// email causes above do.
			name: "IndexCompensationError wrapping UserIDExistsError",
			err: &user.IndexCompensationError{
				Op: "create", UserID: 7,
				Cause:    &user.UserIDExistsError{ID: 7},
				Failures: []error{errors.New("release failed")},
			},
			wantCode: http.StatusInternalServerError,
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
			got := mapHTTPError(tt.err)
			if got != tt.wantCode {
				t.Fatalf("mapHTTPError(%T) = %d, want %d", tt.err, got, tt.wantCode)
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
			// 400, deliberately NOT the 409 its sibling above gets: a reserved
			// name is refused whether or not any role holds it, so it is a
			// malformed request rather than a collision. Mapping both to 409
			// would tell the caller to pick another name when the real remedy
			// is to stop trying to mint a built-in (go-qrdb#17).
			name:     "ReservedRoleNameError",
			err:      &role.ReservedRoleNameError{Name: "so", TenantID: "t1", Detail: "wrong id"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "SystemRoleImmutableError",
			err:      &role.SystemRoleImmutableError{ID: "sys-admin"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "SystemRoleImmutableError/re-permissioned",
			err:      &role.SystemRoleImmutableError{ID: "sys-admin", Op: "re-permissioned"},
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
			got := mapHTTPError(tt.err)
			if got != tt.wantCode {
				t.Fatalf("mapHTTPError(%T) = %d, want %d", tt.err, got, tt.wantCode)
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
			got := mapHTTPError(tt.err)
			if got != tt.wantCode {
				t.Fatalf("mapHTTPError(%T) = %d, want %d", tt.err, got, tt.wantCode)
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

	for _, path := range []string{"/certs/revoke", "/certs/list"} {
		if _, ok := srv.routes[path]; !ok {
			t.Fatalf("%s route should be registered when CertProvider is provided", path)
		}
	}
	// Issuance and enrolment are a SEPARATE service (CertEnrollmentServicer,
	// WithCertEnrollment): CertProvider alone must not register them.
	for _, path := range []string{"/certs/issue", "/certs/enroll"} {
		if _, ok := srv.routes[path]; ok {
			t.Fatalf("%s route should NOT be registered without WithCertEnrollment", path)
		}
	}
}

func TestCertRoutes_NotRegisteredWithoutProvider(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	for _, path := range []string{"/certs/issue", "/certs/enroll", "/certs/revoke", "/certs/list"} {
		if _, ok := srv.routes[path]; ok {
			t.Fatalf("%s route should NOT be registered without CertProvider", path)
		}
	}
}

func TestCertRoutes_RegisteredWhenEnrollmentProvided(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})

	for _, path := range []string{"/certs/issue", "/certs/enroll"} {
		if _, ok := srv.routes[path]; !ok {
			t.Fatalf("%s route should be registered when CertEnrollmentServicer is provided", path)
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

func TestDispatch_CertsIssue_WrongMethod(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})

	req := httptest.NewRequest(http.MethodGet, "/certs/issue", nil)
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

// =========================================================================
// Wire-format regression tests
// =========================================================================

// snowflakeID is a real-magnitude Snowflake user ID that exceeds
// JavaScript's Number.MAX_SAFE_INTEGER (2^53-1). If it crossed the wire
// as a bare JSON number, JSON.parse in browser/Node clients would round
// it (…256 -> …260), corrupting every follow-up request that echoes the
// ID. The wire contract therefore encodes uint64 IDs as JSON strings.
const snowflakeID uint64 = 58370771029856256

// TestUserResponse_WireFormat_SnakeCaseStringID locks the user wire
// contract: snake_case keys and a string-encoded uint64 ID.
func TestUserResponse_WireFormat_SnakeCaseStringID(t *testing.T) {
	u := testUser()
	u.ID = snowflakeID

	svc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != snowflakeID {
				t.Fatalf("id = %d, want %d", id, snowflakeID)
			}
			return u, nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/58370771029856256", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}

	if got := string(wire["id"]); got != `"58370771029856256"` {
		t.Fatalf("id on the wire = %s, want string \"58370771029856256\"", got)
	}
	for _, key := range []string{"username", "tenant_id", "role_ids", "lifecycle", "system", "created_at", "updated_at"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("wire key %q missing; got keys %v", key, wireKeys(wire))
		}
	}
	for _, stale := range []string{"tenantID", "roleIDs", "createdAt", "updatedAt"} {
		if _, ok := wire[stale]; ok {
			t.Fatalf("stale camelCase wire key %q present", stale)
		}
	}
	if _, ok := wire["enabled"]; ok {
		t.Fatal("removed wire key \"enabled\" present; lifecycle is the only account state")
	}
	if _, ok := wire["group_ids"]; ok {
		t.Fatal("removed wire key \"group_ids\" present")
	}
}

// TestListUsersResponse_WireFormat_HasMore locks the paginated list
// envelope keys: users, total, has_more.
func TestListUsersResponse_WireFormat_HasMore(t *testing.T) {
	svc := &mockUserService{
		listUsersFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return &user.ListUsersResponse{Users: []*user.User{testUser()}, Total: 11, HasMore: true}, nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if _, ok := wire["has_more"]; !ok {
		t.Fatalf("wire key has_more missing; got keys %v", wireKeys(wire))
	}
	if _, ok := wire["hasMore"]; ok {
		t.Fatal("stale camelCase wire key hasMore present")
	}
}

// TestRoleResponse_WireFormat_SnakeCase locks the role wire contract.
func TestRoleResponse_WireFormat_SnakeCase(t *testing.T) {
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/roles/role-001", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, key := range []string{"id", "name", "tenant_id", "permissions", "system", "created_at", "updated_at"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("wire key %q missing; got keys %v", key, wireKeys(wire))
		}
	}
	for _, stale := range []string{"tenantID", "createdAt", "updatedAt"} {
		if _, ok := wire[stale]; ok {
			t.Fatalf("stale camelCase wire key %q present", stale)
		}
	}
}

// TestHandleAssignRole_StringUserID locks the assign-role request
// contract: user_id arrives as a JSON string and decodes losslessly
// to the full uint64 Snowflake.
func TestHandleAssignRole_StringUserID(t *testing.T) {
	u := testUser()
	u.ID = snowflakeID
	u.RoleIDs = nil

	var gotUpdate *user.UpdateUserRequest
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != snowflakeID {
				t.Fatalf("GetUser id = %d, want %d", id, snowflakeID)
			}
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			gotUpdate = req
			return u, nil
		},
	}
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole(), nil
		},
	}
	srv := newAuthServer(userSvc, roleSvc, nil)

	body := `{"user_id":"58370771029856256","role_id":"role-001"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if gotUpdate == nil {
		t.Fatal("UpdateUser was not called")
	}
	if gotUpdate.ID != snowflakeID {
		t.Fatalf("UpdateUser id = %d, want %d (precision lost on the wire)", gotUpdate.ID, snowflakeID)
	}
}

// wireKeys returns the sorted key set of a decoded wire object for
// diagnostics in failure messages.
func wireKeys(wire map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(wire))
	for k := range wire {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// POST /users/lookup — the canonical email login-key lookup (#190).
//
// This route, its body contract and its dispatch arm were all added by this PR
// and nothing exercised them: a route mismatch or a wire-field typo would have
// compiled cleanly and shipped. Codex made the point that this is the same
// class of transport-specific omission the MCP client already had twice.
// ---------------------------------------------------------------------------

func TestHandleLookupUser_Success(t *testing.T) {
	var gotTenant, gotEmail string
	svc := &mockUserService{
		getUserByEmailFn: func(_ context.Context, tenantID, email string) (*user.User, error) {
			gotTenant, gotEmail = tenantID, email
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"email":"alice@example.com","tenant_id":"tenant-1"}`
	req := httptest.NewRequest(http.MethodPost, "/users/lookup", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	// The body fields must actually reach the service.
	if gotEmail != "alice@example.com" {
		t.Fatalf("email did not reach the service: %q", gotEmail)
	}
	if gotTenant != "tenant-1" {
		t.Fatalf("tenant_id did not reach the service: %q", gotTenant)
	}

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 1001 {
		t.Fatalf("id = %d, want 1001", resp.ID)
	}
}

func TestHandleLookupUser_MissingEmail(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/users/lookup", strings.NewReader(`{"tenant_id":"t"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleLookupUser_NotFound(t *testing.T) {
	svc := &mockUserService{
		getUserByEmailFn: func(_ context.Context, _, email string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: email}
		},
	}
	srv := newAuthServer(svc, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/users/lookup", strings.NewReader(`{"email":"nobody@example.com"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// The email must NOT be reachable as a path parameter: it is the canonical
// login key, and a path lands in access logs on every successful lookup.
func TestHandleGetUserByEmail_PathFormIsGone(t *testing.T) {
	svc := &mockUserService{
		getUserByEmailFn: func(_ context.Context, _, _ string) (*user.User, error) {
			t.Fatal("the path form must not reach the service")
			return nil, nil
		},
	}
	srv := newAuthServer(svc, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/users/by-email/alice@example.com", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("GET /users/by-email/{email} still resolves — the login key is back in the URL")
	}
}

// Lifecycle is the only account state a REST client reads: it travels
// verbatim, and the removed `enabled` projection does not reappear beside it.
func TestUserToResponse_CarriesLifecycle(t *testing.T) {
	u := testUser()
	u.Lifecycle = user.LifecycleSuspended

	resp := mustUserToResponse(t, nil, u)
	if resp.Lifecycle != string(user.LifecycleSuspended) {
		t.Fatalf("lifecycle = %q, want %q", resp.Lifecycle, user.LifecycleSuspended)
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["enabled"]; ok {
		t.Fatalf("response carries the removed \"enabled\" key: %s", data)
	}
}

// Every identity field must reach a REST client. A field the domain stores and
// the wire drops is a field a remote consumer cannot see, which is how a
// consumer ends up keeping its own copy.
func TestUserToResponse_CarriesEveryIdentityField(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	u := testUser()
	u.Lifecycle = user.LifecycleActive
	u.AuthMethods = []string{"webauthn", "piv"}
	u.LastAuth = &at

	resp := mustUserToResponse(t, nil, u)
	if len(resp.AuthMethods) != 2 || resp.AuthMethods[1] != "piv" {
		t.Errorf("auth_methods: got %v", resp.AuthMethods)
	}
	if resp.LastAuth != at.Format(time.RFC3339) {
		t.Errorf("last_auth: got %q, want %q", resp.LastAuth, at.Format(time.RFC3339))
	}
}

// The binding set crosses the wire in full, expired ones included, with the
// thumbprint byte-identical to the enrolled one (go-qrdb#535).
func TestUserToResponse_CarriesCertBindingsIncludingExpired(t *testing.T) {
	expired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	u := testUser()
	u.CertBindings = []user.CertBinding{
		{Thumbprint: "AAAA", Kind: user.CertBindingSPKIThumbprint},
		{Thumbprint: "BBBB", Kind: user.CertBindingSPKIThumbprint, NotAfter: expired},
	}
	resp := mustUserToResponse(t, nil, u)
	if len(resp.CertBindings) != 2 {
		t.Fatalf("cert_bindings: got %+v, want both bindings", resp.CertBindings)
	}
	if resp.CertBindings[0] != (transport.CertBinding{Thumbprint: "AAAA", Kind: "spki_thumbprint"}) {
		t.Errorf("cert_bindings[0] = %+v", resp.CertBindings[0])
	}
	if resp.CertBindings[1] != (transport.CertBinding{Thumbprint: "BBBB", Kind: "spki_thumbprint", NotAfter: "2020-01-01T00:00:00Z"}) {
		t.Errorf("cert_bindings[1] = %+v; the expired binding must be listed with its expiry", resp.CertBindings[1])
	}
	u.CertBindings = nil
	if got := mustUserToResponse(t, nil, u).CertBindings; got != nil {
		t.Errorf("no bindings must render as absent, got %+v", got)
	}
}

// "Never authenticated" must not render as 1970. An empty string lets
// `omitempty` drop the key entirely, which is the honest encoding of absence.
func TestFormatLastAuth_NeverAuthenticatedIsAbsentNotEpoch(t *testing.T) {
	if got := formatLastAuth(nil); got != "" {
		t.Fatalf("got %q, want the empty string", got)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if got := formatLastAuth(&at); got != at.Format(time.RFC3339) {
		t.Fatalf("got %q, want %q", got, at.Format(time.RFC3339))
	}
}

// The three-way distinction the domain request depends on must survive the
// wire: an ABSENT key leaves the set alone, an EMPTY array clears it. JSON
// carries that for free — absent and `null` both decode to nil, `[]` decodes to
// a non-nil empty slice — and getting it wrong makes a clear silently do
// nothing.
func TestHandleUpdateUser_AuthMethodsAbsentVersusEmptyVersusSet(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantNil     bool
		wantCleared bool
		wantSet     []string
	}{
		{name: "absent leaves alone", body: `{"username":"bob"}`, wantNil: true},
		{name: "null leaves alone", body: `{"auth_methods":null}`, wantNil: true},
		{name: "empty array clears", body: `{"auth_methods":[]}`, wantCleared: true},
		{name: "set replaces", body: `{"auth_methods":["webauthn","piv"]}`, wantSet: []string{"webauthn", "piv"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *user.UpdateUserRequest
			svc := &mockUserService{
				updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
					got = req
					return testUser(), nil
				},
			}
			srv := newAuthServer(svc, nil, nil)
			r := httptest.NewRequest(http.MethodPut, "/users/1001", strings.NewReader(tc.body))
			srv.dispatch(httptest.NewRecorder(), r)

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
				if len(got.AuthMethods) != len(tc.wantSet) {
					t.Fatalf("AuthMethods = %#v, want %v", got.AuthMethods, tc.wantSet)
				}
			}
		})
	}
}

// A malformed last_auth is the caller's mistake and must be reported as one,
// not swallowed into a successful update that quietly changed nothing.
func TestHandleUpdateUser_MalformedLastAuthIsBadRequest(t *testing.T) {
	called := false
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			called = true
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)
	r := httptest.NewRequest(http.MethodPut, "/users/1001", strings.NewReader(`{"last_auth":"yesterday"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, r)

	assertStatus(t, rec, http.StatusBadRequest)
	if called {
		t.Fatal("the update reached the service despite a malformed timestamp")
	}
}

// The identity fields must reach the service on a create, not be dropped
// between the body and the domain request.
func TestHandleCreateUser_CarriesIdentityFields(t *testing.T) {
	var got *user.CreateUserRequest
	svc := &mockUserService{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			got = req
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	body := `{"username":"bob","email":"bob@example.com",` +
		`"auth_methods":["webauthn"],"lifecycle":"pending"}`
	r := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	srv.dispatch(httptest.NewRecorder(), r)

	if got == nil {
		t.Fatal("the service was never called")
	}
	if len(got.AuthMethods) != 1 || got.AuthMethods[0] != "webauthn" {
		t.Errorf("AuthMethods = %v", got.AuthMethods)
	}
	if got.Lifecycle != user.LifecyclePending {
		t.Errorf("Lifecycle = %q, want %q", got.Lifecycle, user.LifecyclePending)
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
