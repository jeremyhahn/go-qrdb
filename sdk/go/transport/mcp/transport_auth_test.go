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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockUserSvc is an in-memory user.UserServicer for transport round-trip
// tests. Methods record the last call arguments for assertions.
// ---------------------------------------------------------------------------

type mockUserSvc struct {
	mu sync.Mutex

	createErr    error
	getErr       error
	getByErr     error
	updateErr    error
	deleteErr    error
	listErr      error
	enableErr    error
	disableErr   error
	suspendErr   error
	unsuspendErr error

	lastCreate    *user.CreateUserRequest
	lastGetID     uint64
	lastGetByTen  string
	lastGetByUser string
	lastUpdate    *user.UpdateUserRequest
	lastDelete    uint64
	lastList      *user.ListUsersRequest
	lastEnable    uint64
	lastDisable   uint64
	lastSuspend   uint64
	lastUnsuspend uint64

	getResp  *user.User
	listResp *user.ListUsersResponse
}

var _ user.UserServicer = (*mockUserSvc)(nil)

func newMockUserSvc() *mockUserSvc {
	now := time.Now().UTC()
	return &mockUserSvc{
		getResp: &user.User{
			ID:        1,
			Username:  "alice",
			TenantID:  "t1",
			RoleIDs:   []string{"r1"},
			Lifecycle: user.LifecycleActive,
			CreatedAt: now,
			UpdatedAt: now,
		},
		listResp: &user.ListUsersResponse{
			Users: []*user.User{
				{ID: 1, Username: "alice", TenantID: "t1", Lifecycle: user.LifecycleActive, CreatedAt: now, UpdatedAt: now},
				{ID: 2, Username: "bob", TenantID: "t1", Lifecycle: user.LifecycleActive, CreatedAt: now, UpdatedAt: now},
			},
			Total:   2,
			HasMore: false,
		},
	}
}

func (m *mockUserSvc) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCreate = req
	if m.createErr != nil {
		return nil, m.createErr
	}
	now := time.Now().UTC()
	return &user.User{
		ID:        9100,
		Username:  req.Username,
		TenantID:  req.TenantID,
		RoleIDs:   req.RoleIDs,
		Lifecycle: user.LifecycleActive,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (m *mockUserSvc) GetUser(_ context.Context, id uint64) (*user.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGetID = id
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResp, nil
}

func (m *mockUserSvc) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGetByTen = tenantID
	m.lastGetByUser = username
	if m.getByErr != nil {
		return nil, m.getByErr
	}
	return m.getResp, nil
}

// GetUserByEmail records the same way GetUserByUsername does so a test can
// assert which handle a caller actually resolved by.
func (m *mockUserSvc) GetUserByEmail(_ context.Context, tenantID, email string) (*user.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGetByTen = tenantID
	m.lastGetByUser = user.NormalizeEmail(email)
	if m.getByErr != nil {
		return nil, m.getByErr
	}
	return m.getResp, nil
}

func (m *mockUserSvc) UpdateUser(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUpdate = req
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.getResp, nil
}

func (m *mockUserSvc) DeleteUser(_ context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastDelete = id
	return m.deleteErr
}

func (m *mockUserSvc) ListUsers(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastList = req
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func (m *mockUserSvc) EnableUser(_ context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastEnable = id
	return m.enableErr
}

func (m *mockUserSvc) DisableUser(_ context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastDisable = id
	return m.disableErr
}

func (m *mockUserSvc) SuspendUser(_ context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSuspend = id
	return m.suspendErr
}

func (m *mockUserSvc) UnsuspendUser(_ context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUnsuspend = id
	return m.unsuspendErr
}

func (m *mockUserSvc) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserSvc) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

func (m *mockUserSvc) RemoveCertBinding(_ context.Context, _ uint64, _ string) (*user.User, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// mockRoleSvc is an in-memory role.RoleServicer for transport round-trip
// tests.
// ---------------------------------------------------------------------------

type mockRoleSvc struct {
	mu sync.Mutex

	createErr     error
	getErr        error
	getByErr      error
	updateErr     error
	deleteErr     error
	listErr       error
	resolveErr    error
	reconcileErr  error
	reconcileResp *role.ReconcileReport

	lastCreate     *role.CreateRoleRequest
	lastGetID      string
	lastUpdate     *role.UpdateRoleRequest
	lastDelete     string
	lastList       *role.ListRolesRequest
	lastResolveIDs []string

	getResp  *role.Role
	listResp *role.ListRolesResponse
}

var _ role.RoleServicer = (*mockRoleSvc)(nil)

func newMockRoleSvc() *mockRoleSvc {
	now := time.Now().UTC()
	return &mockRoleSvc{
		getResp: &role.Role{
			ID:          "r1",
			Name:        "admin",
			TenantID:    "t1",
			Permissions: []permission.Permission{"kv:read", "kv:write"},
			System:      false,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		listResp: &role.ListRolesResponse{
			Roles: []*role.Role{
				{ID: "r1", Name: "admin", TenantID: "t1", Permissions: []permission.Permission{"kv:read"}, CreatedAt: now, UpdatedAt: now},
				{ID: "r2", Name: "viewer", TenantID: "t1", Permissions: []permission.Permission{"kv:read"}, CreatedAt: now, UpdatedAt: now},
			},
			Total:   2,
			HasMore: false,
		},
	}
}

func (m *mockRoleSvc) CreateRole(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCreate = req
	if m.createErr != nil {
		return nil, m.createErr
	}
	now := time.Now().UTC()
	return &role.Role{
		ID:          "r-new",
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: req.Permissions,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (m *mockRoleSvc) GetRole(_ context.Context, id string) (*role.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGetID = id
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResp, nil
}

func (m *mockRoleSvc) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	if m.getByErr != nil {
		return nil, m.getByErr
	}
	return m.getResp, nil
}

func (m *mockRoleSvc) UpdateRole(_ context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUpdate = req
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.getResp, nil
}

func (m *mockRoleSvc) DeleteRole(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastDelete = id
	return m.deleteErr
}

func (m *mockRoleSvc) ListRoles(_ context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastList = req
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func (m *mockRoleSvc) ResolvePermissions(_ context.Context, ids []string) ([]permission.Permission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastResolveIDs = append([]string(nil), ids...)
	if m.resolveErr != nil {
		return nil, m.resolveErr
	}
	return []permission.Permission{"kv:read"}, nil
}

// ---------------------------------------------------------------------------
// mockCertProvider is an in-memory cert.CertProvider for transport tests.
// ---------------------------------------------------------------------------

type mockCertProvider struct {
	mu sync.Mutex

	revokeErr error
	listErr   error

	lastRevoke string
	lastListT  string

	listResp []*cert.CertRecord
}

var _ cert.CertProvider = (*mockCertProvider)(nil)

func newMockCertProvider() *mockCertProvider {
	now := time.Now().UTC()
	return &mockCertProvider{
		listResp: []*cert.CertRecord{
			{Serial: "AA01", Username: "alice", TenantID: "t1", IssuedAt: now, ExpiresAt: now.Add(time.Hour), Revoked: false},
		},
	}
}

func (m *mockCertProvider) IssueClientCert(_ context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error) {
	return &cert.IssuedCert{CertPEM: []byte("-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----\n"), Serial: "0123ABCD"}, nil
}

func (m *mockCertProvider) RevokeCert(_ context.Context, serial string) (*cert.RevokeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRevoke = serial
	if m.revokeErr != nil {
		return nil, m.revokeErr
	}
	return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeRecordUpdated}, nil
}

func (m *mockCertProvider) IsRevoked(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *mockCertProvider) ListCerts(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastListT = tenantID
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func (m *mockCertProvider) CACertPEM() []byte {
	return []byte("ca-pem")
}

// ---------------------------------------------------------------------------
// mockCertEnrollmentServicer is an in-memory service.CertEnrollmentServicer
// for transport tests of IssueCert/EnrollCert/trust-anchor verbs.
// ---------------------------------------------------------------------------

type mockCertEnrollmentServicer struct {
	mu sync.Mutex

	signErr   error
	enrollErr error

	lastSign   *service.SignCertificateRequest
	lastEnroll *service.EnrollCertificateRequest

	signResp   *service.EnrolledCertificate
	enrollResp *service.EnrolledCertificate

	anchorsErr error
	anchors    []trustanchor.Anchor

	lastAddTenant, lastAddBundle   string
	lastListTenant                 string
	lastRemoveTenant, lastRemoveFP string
	removeErr                      error

	lastDelegation *service.IssueDelegationRequest
	delegationResp *service.IssuedDelegation
	delegationErr  error
}

var _ service.CertEnrollmentServicer = (*mockCertEnrollmentServicer)(nil)

func newMockCertEnrollmentServicer() *mockCertEnrollmentServicer {
	return &mockCertEnrollmentServicer{
		signResp: &service.EnrolledCertificate{
			Username: "alice", TenantID: "t1",
			CertPEM: []byte("-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----\n"),
			Serial:  "0123ABCD", Thumbprint: "thumb-0123ABCD",
		},
		enrollResp: &service.EnrolledCertificate{
			Username: "alice", TenantID: "t1",
			CertPEM:    []byte("-----BEGIN CERTIFICATE-----\nBYOC\n-----END CERTIFICATE-----\n"),
			Thumbprint: "thumb-byoc",
		},
		anchors: []trustanchor.Anchor{{Fingerprint: "fp1", Subject: "CN=ca"}},
	}
}

func (m *mockCertEnrollmentServicer) SignCertificate(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSign = req
	if m.signErr != nil {
		return nil, m.signErr
	}
	return m.signResp, nil
}

func (m *mockCertEnrollmentServicer) EnrollCertificate(_ context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastEnroll = req
	if m.enrollErr != nil {
		return nil, m.enrollErr
	}
	return m.enrollResp, nil
}

func (m *mockCertEnrollmentServicer) AddTrustAnchors(_ context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastAddTenant, m.lastAddBundle = tenantID, string(bundlePEM)
	if m.anchorsErr != nil {
		return nil, m.anchorsErr
	}
	return m.anchors, nil
}

func (m *mockCertEnrollmentServicer) ListTrustAnchors(_ context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastListTenant = tenantID
	if m.anchorsErr != nil {
		return nil, m.anchorsErr
	}
	return m.anchors, nil
}

func (m *mockCertEnrollmentServicer) RemoveTrustAnchor(_ context.Context, tenantID, fingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRemoveTenant, m.lastRemoveFP = tenantID, fingerprint
	return m.removeErr
}

func (m *mockCertEnrollmentServicer) IssueDelegation(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastDelegation = req
	if m.delegationErr != nil {
		return nil, m.delegationErr
	}
	return m.delegationResp, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

func connectAuthInProcess(t *testing.T, uSvc user.UserServicer, rSvc role.RoleServicer, cProv cert.CertProvider, extraOpts ...mcpapi.ServerOption) (*Transport, func()) {
	t.Helper()

	opts := []mcpapi.ServerOption{}
	if uSvc != nil {
		opts = append(opts, mcpapi.WithUserService(uSvc))
	}
	if rSvc != nil {
		opts = append(opts, mcpapi.WithRoleService(rSvc))
	}
	if cProv != nil {
		opts = append(opts, mcpapi.WithCertProvider(cProv))
	}
	opts = append(opts, extraOpts...)

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		opts...,
	)

	mcpSrv := srv.MCPServer()
	if mcpSrv == nil {
		t.Fatal("MCPServer is nil")
	}

	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}

	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}

	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// CreateUser
// ---------------------------------------------------------------------------

func TestCreateUser_Success(t *testing.T) {
	svc := newMockUserSvc()
	roles := newMockRoleSvc()
	roles.getResp.TenantID = "system"
	tr, cleanup := connectAuthInProcess(t, svc, roles, nil)
	defer cleanup()

	u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
		TenantID: "system",
		RoleIDs:  []string{"r1", "r2"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("username: got %q, want %q", u.Username, "alice")
	}
	if u.TenantID != "system" {
		t.Errorf("tenantID: got %q, want %q", u.TenantID, "system")
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastCreate == nil || svc.lastCreate.Username != "alice" {
		t.Errorf("recorded create: %+v", svc.lastCreate)
	}
	if len(svc.lastCreate.RoleIDs) != 2 {
		t.Errorf("role ids: got %d, want 2", len(svc.lastCreate.RoleIDs))
	}
}

func TestCreateUser_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.createErr = errors.New("username taken")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "x"})
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// GetUser / GetUserByUsername
// ---------------------------------------------------------------------------

func TestGetUser_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	u, err := tr.GetUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.ID != 1 || u.Username != "alice" {
		t.Errorf("user: %+v", u)
	}
}

func TestGetUser_NotFound(t *testing.T) {
	svc := newMockUserSvc()
	// The type the REAL user service returns, not prose that reads like it.
	// go-qrdb#438 made that distinction load-bearing: the server classifies by
	// TYPE now, so a double answering errors.New("user not found") produces an
	// unclassified fault here exactly as it would in production, and the
	// substring match that used to rescue it is gone.
	svc.getErr = &user.UserNotFoundError{ID: 9999}
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	_, err := tr.GetUser(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error")
	}
	var unf *transport.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("expected *transport.UserNotFoundError, got %T: %v", err, err)
	}
	if unf.ID != 9999 {
		t.Errorf("ID = %d, want 9999 — the id the CALLER asked for", unf.ID)
	}
}

func TestGetUser_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.getErr = errors.New("internal")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	_, err := tr.GetUser(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetUserByUsername_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	u, err := tr.GetUserByUsername(context.Background(), "t1", "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("username: %q", u.Username)
	}
}

func TestGetUserByUsername_NotFound(t *testing.T) {
	svc := newMockUserSvc()
	svc.getByErr = &user.UserNotFoundError{Lookup: "t1/missing"}
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	_, err := tr.GetUserByUsername(context.Background(), "t1", "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	var unf *transport.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("expected *transport.UserNotFoundError, got %T: %v", err, err)
	}
	if unf.Lookup == "" {
		t.Error("a non-id lookup left Lookup empty, so the error names no key")
	}
}

// ---------------------------------------------------------------------------
// UpdateUser
// ---------------------------------------------------------------------------

func TestUpdateUser_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, newMockRoleSvc(), nil)
	defer cleanup()

	u, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{
		ID:       1,
		Username: "alice2",
		RoleIDs:  []string{"r1"},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if u == nil {
		t.Fatal("expected non-nil user")
	}
}

func TestUpdateUser_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.updateErr = errors.New("not found")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	_, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// DeleteUser
// ---------------------------------------------------------------------------

func TestDeleteUser_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.DeleteUser(context.Background(), 1); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastDelete != 1 {
		t.Errorf("delete id: got %d, want 1", svc.lastDelete)
	}
}

func TestDeleteUser_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.deleteErr = errors.New("cannot delete")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.DeleteUser(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListUsers
// ---------------------------------------------------------------------------

func TestListUsers_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	resp, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{
		TenantID: "t1",
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total: got %d, want 2", resp.Total)
	}
	if len(resp.Users) != 2 {
		t.Errorf("users: got %d, want 2", len(resp.Users))
	}
}

func TestListUsers_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if _, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// EnableUser / DisableUser
// ---------------------------------------------------------------------------

func TestEnableUser_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.EnableUser(context.Background(), 1); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastEnable != 1 {
		t.Errorf("enable id: got %d, want 1", svc.lastEnable)
	}
}

func TestEnableUser_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.enableErr = errors.New("not found")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.EnableUser(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestDisableUser_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.DisableUser(context.Background(), 1); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
}

func TestDisableUser_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.disableErr = errors.New("not found")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.DisableUser(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestSuspendUnsuspendUser_Success(t *testing.T) {
	svc := newMockUserSvc()
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.SuspendUser(context.Background(), 7); err != nil {
		t.Fatalf("SuspendUser: %v", err)
	}
	if err := tr.UnsuspendUser(context.Background(), 8); err != nil {
		t.Fatalf("UnsuspendUser: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastSuspend != 7 || svc.lastUnsuspend != 8 {
		t.Fatalf("service saw suspend %d, unsuspend %d; want 7, 8", svc.lastSuspend, svc.lastUnsuspend)
	}
}

func TestSuspendUnsuspendUser_ServiceError(t *testing.T) {
	svc := newMockUserSvc()
	svc.suspendErr = errors.New("suspend failed")
	svc.unsuspendErr = errors.New("unsuspend failed")
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if err := tr.SuspendUser(context.Background(), 1); err == nil {
		t.Fatal("SuspendUser: expected error")
	}
	if err := tr.UnsuspendUser(context.Background(), 1); err == nil {
		t.Fatal("UnsuspendUser: expected error")
	}
}

// ---------------------------------------------------------------------------
// CreateRole / GetRole / UpdateRole / DeleteRole / ListRoles
// ---------------------------------------------------------------------------

func TestCreateRole_Success(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	r, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		Name:        "admin",
		TenantID:    "t1",
		Permissions: []string{"kv:read", "kv:write"},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if r.Name != "admin" {
		t.Errorf("name: %q", r.Name)
	}
}

func TestCreateRole_ServiceError(t *testing.T) {
	svc := newMockRoleSvc()
	svc.createErr = errors.New("name taken")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	if _, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{Name: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetRole_Success(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	r, err := tr.GetRole(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if r.ID != "r1" {
		t.Errorf("id: %q", r.ID)
	}
}

func TestGetRole_NotFound(t *testing.T) {
	svc := newMockRoleSvc()
	svc.getErr = &role.RoleNotFoundError{ID: "missing"}
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	_, err := tr.GetRole(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Errorf("expected *transport.KeyNotFoundError, got %T", err)
	}
}

func TestUpdateRole_Success(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	r, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{
		ID:          "r1",
		Name:        "renamed",
		Permissions: []string{"kv:read"},
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil role")
	}
}

func TestUpdateRole_ServiceError(t *testing.T) {
	svc := newMockRoleSvc()
	svc.updateErr = errors.New("not found")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	if _, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{ID: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteRole_Success(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	if err := tr.DeleteRole(context.Background(), "r1"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
}

func TestDeleteRole_ServiceError(t *testing.T) {
	svc := newMockRoleSvc()
	svc.deleteErr = errors.New("system role")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	if err := tr.DeleteRole(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestListRoles_Success(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	resp, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{
		TenantID: "t1",
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total: got %d, want 2", resp.Total)
	}
}

func TestListRoles_ServiceError(t *testing.T) {
	svc := newMockRoleSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	if _, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// AssignRole / UnassignRole
// ---------------------------------------------------------------------------

func TestAssignRole_Success(t *testing.T) {
	uSvc := newMockUserSvc()
	rSvc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, uSvc, rSvc, nil)
	defer cleanup()

	if err := tr.AssignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
}

func TestUnassignRole_Success(t *testing.T) {
	uSvc := newMockUserSvc()
	rSvc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, uSvc, rSvc, nil)
	defer cleanup()

	if err := tr.UnassignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListPermissions
// ---------------------------------------------------------------------------

func TestListPermissions_Success(t *testing.T) {
	uSvc := newMockUserSvc()
	rSvc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, uSvc, rSvc, nil)
	defer cleanup()

	resp, err := tr.ListPermissions(context.Background())
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// Categories may be empty in tests with no registry — we just check decode
}

// ---------------------------------------------------------------------------
// IssueCert / EnrollCert / RevokeCert / ListCerts
// ---------------------------------------------------------------------------

func TestIssueCert_Success(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	enrolled, err := tr.IssueCert(context.Background(), &transport.IssueCertRequest{
		Username: "alice",
		CSRPEM:   "csr-bytes",
		TTL:      "24h",
	})
	if err != nil {
		t.Fatalf("IssueCert: %v", err)
	}
	if enrolled.Serial != "0123ABCD" {
		t.Errorf("serial: %q", enrolled.Serial)
	}
	if enrolled.Thumbprint != "thumb-0123ABCD" {
		t.Errorf("thumbprint: %q", enrolled.Thumbprint)
	}
	enroll.mu.Lock()
	defer enroll.mu.Unlock()
	if enroll.lastSign == nil || enroll.lastSign.Username != "alice" || string(enroll.lastSign.CSRPEM) != "csr-bytes" || enroll.lastSign.TTL != 24*time.Hour {
		t.Errorf("server received %+v; want Username=alice CSRPEM=csr-bytes TTL=24h", enroll.lastSign)
	}
}

func TestIssueCert_ServiceError(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.signErr = errors.New("ca unavailable")
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if _, err := tr.IssueCert(context.Background(), &transport.IssueCertRequest{Username: "x", CSRPEM: "csr"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestEnrollCert_Success(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	enrolled, err := tr.EnrollCert(context.Background(), &transport.EnrollCertRequest{
		Username: "alice",
		CertPEM:  "leaf-pem",
		ChainPEM: "chain-pem",
	})
	if err != nil {
		t.Fatalf("EnrollCert: %v", err)
	}
	if enrolled.Serial != "" {
		t.Errorf("serial: %q, want empty for a bring-your-own certificate", enrolled.Serial)
	}
	if enrolled.Thumbprint != "thumb-byoc" {
		t.Errorf("thumbprint: %q", enrolled.Thumbprint)
	}
	enroll.mu.Lock()
	defer enroll.mu.Unlock()
	if enroll.lastEnroll == nil || string(enroll.lastEnroll.CertPEM) != "leaf-pem" || string(enroll.lastEnroll.ChainPEM) != "chain-pem" {
		t.Errorf("server received %+v; want CertPEM=leaf-pem ChainPEM=chain-pem", enroll.lastEnroll)
	}
}

func TestEnrollCert_ServiceError(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.enrollErr = errors.New("chain verification failed")
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if _, err := tr.EnrollCert(context.Background(), &transport.EnrollCertRequest{Username: "x", CertPEM: "pem"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// AddTrustAnchors / ListTrustAnchors / RemoveTrustAnchor
// ---------------------------------------------------------------------------

func TestAddTrustAnchors_Success(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	resp, err := tr.AddTrustAnchors(context.Background(), &transport.AddTrustAnchorsRequest{TenantID: "t1", CertsPEM: "bundle-pem"})
	if err != nil {
		t.Fatalf("AddTrustAnchors: %v", err)
	}
	if len(resp.Anchors) != 1 || resp.Anchors[0].Fingerprint != "fp1" {
		t.Fatalf("anchors: %+v", resp.Anchors)
	}
	enroll.mu.Lock()
	defer enroll.mu.Unlock()
	if enroll.lastAddTenant != "t1" || enroll.lastAddBundle != "bundle-pem" {
		t.Errorf("server received tenant=%q bundle=%q; want t1/bundle-pem", enroll.lastAddTenant, enroll.lastAddBundle)
	}
}

func TestAddTrustAnchors_ServiceError(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.anchorsErr = errors.New("invalid PEM")
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if _, err := tr.AddTrustAnchors(context.Background(), &transport.AddTrustAnchorsRequest{TenantID: "t1", CertsPEM: "bad"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestListTrustAnchors_Success(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	resp, err := tr.ListTrustAnchors(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListTrustAnchors: %v", err)
	}
	if len(resp.Anchors) != 1 || resp.Anchors[0].Fingerprint != "fp1" {
		t.Fatalf("anchors: %+v", resp.Anchors)
	}
	enroll.mu.Lock()
	defer enroll.mu.Unlock()
	if enroll.lastListTenant != "t1" {
		t.Errorf("server received tenant=%q; want t1", enroll.lastListTenant)
	}
}

func TestRemoveTrustAnchor_Success(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if err := tr.RemoveTrustAnchor(context.Background(), "t1", "fp1"); err != nil {
		t.Fatalf("RemoveTrustAnchor: %v", err)
	}
	enroll.mu.Lock()
	defer enroll.mu.Unlock()
	if enroll.lastRemoveTenant != "t1" || enroll.lastRemoveFP != "fp1" {
		t.Errorf("server received tenant=%q fp=%q; want t1/fp1", enroll.lastRemoveTenant, enroll.lastRemoveFP)
	}
}

func TestRemoveTrustAnchor_ServiceError(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.removeErr = errors.New("no such anchor")
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if err := tr.RemoveTrustAnchor(context.Background(), "t1", "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRevokeCert_Success(t *testing.T) {
	prov := newMockCertProvider()
	tr, cleanup := connectAuthInProcess(t, nil, nil, prov)
	defer cleanup()

	result, err := tr.RevokeCert(context.Background(), "AA01")
	if err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if !result.RecordFound {
		t.Error("RecordFound should be true")
	}
}

func TestRevokeCert_ServiceError(t *testing.T) {
	prov := newMockCertProvider()
	prov.revokeErr = errors.New("revoke failed")
	tr, cleanup := connectAuthInProcess(t, nil, nil, prov)
	defer cleanup()

	if _, err := tr.RevokeCert(context.Background(), "AA01"); err == nil {
		t.Fatal("expected error")
	}
}

func TestListCerts_Success(t *testing.T) {
	prov := newMockCertProvider()
	tr, cleanup := connectAuthInProcess(t, nil, nil, prov)
	defer cleanup()

	resp, err := tr.ListCerts(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	if len(resp.Certs) != 1 {
		t.Errorf("certs: got %d, want 1", len(resp.Certs))
	}
	if resp.Certs[0].Serial != "AA01" {
		t.Errorf("serial: %q", resp.Certs[0].Serial)
	}
}

func TestListCerts_ServiceError(t *testing.T) {
	prov := newMockCertProvider()
	prov.listErr = errors.New("list failed")
	tr, cleanup := connectAuthInProcess(t, nil, nil, prov)
	defer cleanup()

	if _, err := tr.ListCerts(context.Background(), "t1"); err == nil {
		t.Fatal("expected error")
	}
}

// Every field of transport.CreateUserRequest must reach the service through the
// MCP client.
//
// MCP is the transport that keeps being missed on this PR, and always for the
// same reason: it builds an argument MAP while the other transports use struct
// literals, so an edit shaped around struct fields passes over it and nothing
// fails to compile. It silently dropped Email/Name once and
// WithoutEmailLoginKey once. This test enumerates the fields rather than
// checking the one that was reported, so the next addition has somewhere to fail.
func TestTransport_CreateUser_ForwardsEveryField(t *testing.T) {
	svc := &mockUserSvc{}
	roles := newMockRoleSvc()
	roles.getResp.TenantID = "system"
	tr, cleanup := connectAuthInProcess(t, svc, roles, nil)
	defer cleanup()

	// A certificate-only identity: no email, opt-out declared.
	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		Username:             "cert-only",
		WithoutEmailLoginKey: true,
		TenantID:             "system",
		RoleIDs:              []string{"viewer"},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	svc.mu.Lock()
	got := svc.lastCreate
	svc.mu.Unlock()
	if got == nil {
		t.Fatal("service never received a create")
	}
	if !got.WithoutEmailLoginKey {
		t.Fatal("WithoutEmailLoginKey was dropped — a certificate-only identity cannot be created over MCP")
	}
	if got.Username != "cert-only" || got.TenantID != "system" {
		t.Fatalf("username/tenant dropped: %q / %q", got.Username, got.TenantID)
	}

	// And the email-backed shape carries its own fields.
	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		Username: "alice",
		Email:    "alice@example.com",
		Name:     "Alice",
		TenantID: "system",
	}); err != nil {
		t.Fatalf("CreateUser(email): %v", err)
	}
	svc.mu.Lock()
	got = svc.lastCreate
	svc.mu.Unlock()
	if got.Email != "alice@example.com" {
		t.Fatalf("Email dropped: %q", got.Email)
	}
	if got.Name != "Alice" {
		t.Fatalf("Name dropped: %q", got.Name)
	}
}

// The MCP email lookup is assembled from free-form strings on both sides — the
// tool name qrdb_user_get_by_email and the argument keys tenant_id/email — so a
// typo compiles cleanly and ships as a broken lookup. Registration and
// create/update forwarding were covered; the lookup itself was not
// (Codex, PR #191).
func TestTransport_GetUserByEmail_ReachesTheService(t *testing.T) {
	svc := &mockUserSvc{getResp: &user.User{
		ID: 42, Username: "alice", Email: "alice@example.com", TenantID: "t1", Lifecycle: user.LifecycleActive,
	}}
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	got, err := tr.GetUserByEmail(context.Background(), "t1", "alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got == nil || got.ID != 42 {
		t.Fatalf("response not decoded: %+v", got)
	}

	// The tool name and argument keys must have carried, not just returned
	// something: the mock records what the server handler received.
	svc.mu.Lock()
	tenant, email := svc.lastGetByTen, svc.lastGetByUser
	svc.mu.Unlock()
	if email != "alice@example.com" {
		t.Fatalf(`argument key "email" did not reach the service: %q`, email)
	}
	if tenant != "t1" {
		t.Fatalf(`argument key "tenant_id" did not reach the service: %q`, tenant)
	}
}

func TestTransport_GetUserByEmail_NotFound(t *testing.T) {
	svc := &mockUserSvc{getByErr: &user.UserNotFoundError{Lookup: "t1/nobody@example.com"}}
	tr, cleanup := connectAuthInProcess(t, svc, nil, nil)
	defer cleanup()

	if _, err := tr.GetUserByEmail(context.Background(), "t1", "nobody@example.com"); err == nil {
		t.Fatal("expected a not-found error")
	}
}

func (m *mockRoleSvc) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconcileErr != nil {
		return nil, m.reconcileErr
	}
	if m.reconcileResp != nil {
		return m.reconcileResp, nil
	}
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

// TenantForSerial derives the owning tenant from this double's OWN listResp,
// so it cannot answer "tenant t1 owns it" while reporting an inventory that
// says otherwise. A serial absent from listResp is the deny-by-serial case.
func (m *mockCertProvider) TenantForSerial(_ context.Context, serial string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return "", false, m.listErr
	}
	for _, rec := range m.listResp {
		if rec != nil && rec.Serial == serial {
			return rec.TenantID, true, nil
		}
	}
	return "", false, nil
}
