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

// Round-trip tests against a real Unix gRPC server with stubbed user, role,
// and certificate services. These verify the SDK transport correctly marshals
// requests, dispatches them over the UDS gRPC connection, and converts the
// proto responses back into SDK types.

package unix

import (
	"context"
	"errors"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockUserSvc — minimal user.UserServicer stub for round-trip testing
// ---------------------------------------------------------------------------

type mockUserSvc struct {
	createFn        func(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
	getFn           func(ctx context.Context, id uint64) (*user.User, error)
	getByUserNameFn func(ctx context.Context, tenant, username string) (*user.User, error)
	getByEmailFn    func(ctx context.Context, tenant, email string) (*user.User, error)
	updateFn        func(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
	deleteFn        func(ctx context.Context, id uint64) error
	listFn          func(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
	enableFn        func(ctx context.Context, id uint64) error
	disableFn       func(ctx context.Context, id uint64) error
	suspendFn       func(ctx context.Context, id uint64) error
	unsuspendFn     func(ctx context.Context, id uint64) error
}

func (m *mockUserSvc) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &user.User{ID: 1, Username: req.Username, TenantID: req.TenantID, Lifecycle: user.LifecycleActive}, nil
}

func (m *mockUserSvc) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &user.User{ID: id, Username: "stub", Lifecycle: user.LifecycleActive}, nil
}

func (m *mockUserSvc) GetUserByUsername(ctx context.Context, tenant, username string) (*user.User, error) {
	if m.getByUserNameFn != nil {
		return m.getByUserNameFn(ctx, tenant, username)
	}
	return &user.User{ID: 1, Username: username, TenantID: tenant, Lifecycle: user.LifecycleActive}, nil
}

func (m *mockUserSvc) GetUserByEmail(ctx context.Context, tenant, email string) (*user.User, error) {
	normalized := user.NormalizeEmail(email)
	if m.getByEmailFn != nil {
		return m.getByEmailFn(ctx, tenant, normalized)
	}
	return &user.User{ID: 1, Username: normalized, Email: normalized, TenantID: tenant, Lifecycle: user.LifecycleActive}, nil
}

func (m *mockUserSvc) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, req)
	}
	return &user.User{ID: req.ID, Username: req.Username, RoleIDs: req.RoleIDs, Lifecycle: user.LifecycleActive}, nil
}

func (m *mockUserSvc) DeleteUser(ctx context.Context, id uint64) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockUserSvc) ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &user.ListUsersResponse{Users: []*user.User{{ID: 1, Username: "alice"}}, Total: 1, HasMore: false}, nil
}

func (m *mockUserSvc) EnableUser(ctx context.Context, id uint64) error {
	if m.enableFn != nil {
		return m.enableFn(ctx, id)
	}
	return nil
}

func (m *mockUserSvc) DisableUser(ctx context.Context, id uint64) error {
	if m.disableFn != nil {
		return m.disableFn(ctx, id)
	}
	return nil
}

func (m *mockUserSvc) SuspendUser(ctx context.Context, id uint64) error {
	if m.suspendFn != nil {
		return m.suspendFn(ctx, id)
	}
	return nil
}

func (m *mockUserSvc) UnsuspendUser(ctx context.Context, id uint64) error {
	if m.unsuspendFn != nil {
		return m.unsuspendFn(ctx, id)
	}
	return nil
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

var _ user.UserServicer = (*mockUserSvc)(nil)

// ---------------------------------------------------------------------------
// mockRoleSvc — minimal role.RoleServicer stub
// ---------------------------------------------------------------------------

type mockRoleSvc struct {
	createFn    func(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error)
	getFn       func(ctx context.Context, id string) (*role.Role, error)
	getByNameFn func(ctx context.Context, tenant, name string) (*role.Role, error)
	updateFn    func(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error)
	deleteFn    func(ctx context.Context, id string) error
	listFn      func(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error)
	resolveFn   func(ctx context.Context, roleIDs []string) ([]permission.Permission, error)
	reconcileFn func(ctx context.Context, tenantID string) (*role.ReconcileReport, error)
}

func (m *mockRoleSvc) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &role.Role{ID: "r1", Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions}, nil
}

// tenantRoleSvc answers GetRole with a role owned by tenantID, so a
// role-assignment write naming it passes validation.
func tenantRoleSvc(tenantID string) *mockRoleSvc {
	return &mockRoleSvc{getFn: func(_ context.Context, id string) (*role.Role, error) {
		return &role.Role{ID: id, TenantID: tenantID}, nil
	}}
}

func (m *mockRoleSvc) GetRole(ctx context.Context, id string) (*role.Role, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &role.Role{ID: id, Name: "stub-role"}, nil
}

func (m *mockRoleSvc) GetRoleByName(ctx context.Context, tenant, name string) (*role.Role, error) {
	if m.getByNameFn != nil {
		return m.getByNameFn(ctx, tenant, name)
	}
	return &role.Role{ID: "r-by-name", Name: name, TenantID: tenant}, nil
}

func (m *mockRoleSvc) UpdateRole(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, req)
	}
	return &role.Role{ID: req.ID, Name: req.Name, Permissions: req.Permissions}, nil
}

func (m *mockRoleSvc) DeleteRole(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockRoleSvc) ListRoles(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &role.ListRolesResponse{Roles: []*role.Role{{ID: "r1", Name: "admin"}}, Total: 1}, nil
}

func (m *mockRoleSvc) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	if m.resolveFn != nil {
		return m.resolveFn(ctx, roleIDs)
	}
	return nil, nil
}

var _ role.RoleServicer = (*mockRoleSvc)(nil)

// ---------------------------------------------------------------------------
// mockCertProvider — minimal cert.CertProvider stub
// ---------------------------------------------------------------------------

type mockCertProvider struct {
	revokeFn  func(ctx context.Context, serial string) (*cert.RevokeResult, error)
	isRevoked func(ctx context.Context, serial string) (bool, error)
	listFn    func(ctx context.Context, tenant string) ([]*cert.CertRecord, error)
	tenantFn  func(ctx context.Context, serial string) (string, bool, error)
}

func (m *mockCertProvider) IssueClientCert(ctx context.Context, req *cert.IssueRequest) (*cert.IssuedCert, error) {
	return &cert.IssuedCert{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\n"),
		Serial:  "abc123",
	}, nil
}

func (m *mockCertProvider) RevokeCert(ctx context.Context, serial string) (*cert.RevokeResult, error) {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, serial)
	}
	return &cert.RevokeResult{Serial: serial}, nil
}

func (m *mockCertProvider) IsRevoked(ctx context.Context, serial string) (bool, error) {
	if m.isRevoked != nil {
		return m.isRevoked(ctx, serial)
	}
	return false, nil
}

func (m *mockCertProvider) ListCerts(ctx context.Context, tenant string) ([]*cert.CertRecord, error) {
	if m.listFn != nil {
		return m.listFn(ctx, tenant)
	}
	return []*cert.CertRecord{{Serial: "abc123", Username: "alice", TenantID: tenant}}, nil
}

func (m *mockCertProvider) CACertPEM() []byte { return []byte("ca-cert") }

var _ cert.CertProvider = (*mockCertProvider)(nil)

// ---------------------------------------------------------------------------
// mockCertEnrollmentSvc — minimal service.CertEnrollmentServicer stub
// ---------------------------------------------------------------------------

type mockCertEnrollmentSvc struct {
	signFn   func(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error)
	enrollFn func(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error)
	addFn    func(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	listFn   func(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	removeFn func(ctx context.Context, tenantID, fingerprint string) error
	delegFn  func(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error)
}

func (m *mockCertEnrollmentSvc) SignCertificate(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	if m.signFn != nil {
		return m.signFn(ctx, req)
	}
	return &service.EnrolledCertificate{Username: req.Username, TenantID: req.TenantID, CertPEM: []byte("-----BEGIN CERTIFICATE-----\n"), Serial: "abc123", Thumbprint: "thumb-abc123"}, nil
}

func (m *mockCertEnrollmentSvc) EnrollCertificate(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, req)
	}
	return &service.EnrolledCertificate{Username: req.Username, TenantID: req.TenantID, CertPEM: req.CertPEM, Thumbprint: "thumb-byoc"}, nil
}

func (m *mockCertEnrollmentSvc) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	if m.addFn != nil {
		return m.addFn(ctx, tenantID, bundlePEM)
	}
	return []trustanchor.Anchor{{Fingerprint: "fp1", Subject: "CN=ca"}}, nil
}

func (m *mockCertEnrollmentSvc) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	if m.listFn != nil {
		return m.listFn(ctx, tenantID)
	}
	return []trustanchor.Anchor{{Fingerprint: "fp1", Subject: "CN=ca"}}, nil
}

func (m *mockCertEnrollmentSvc) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	if m.removeFn != nil {
		return m.removeFn(ctx, tenantID, fingerprint)
	}
	return nil
}

func (m *mockCertEnrollmentSvc) IssueDelegation(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	if m.delegFn != nil {
		return m.delegFn(ctx, req)
	}
	return &service.IssuedDelegation{TenantID: req.TenantID, Principal: req.Principal, Scope: req.Scope, CertPEM: []byte("-----BEGIN CERTIFICATE-----\n"), Serial: "d1"}, nil
}

var _ service.CertEnrollmentServicer = (*mockCertEnrollmentSvc)(nil)

// ---------------------------------------------------------------------------
// Server bootstrap
// ---------------------------------------------------------------------------

func startAuthServer(t *testing.T, opts ...unixserver.ServerOption) (*Transport, func()) {
	t.Helper()
	socketPath := tempSocketPath(t)
	opts = append(opts, unixserver.WithTLS(testUnixServerTLS))
	srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath, opts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		srv.Stop()
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		srv.Stop()
		t.Fatalf("Connect: %v", err)
	}
	cleanup := func() {
		_ = tr.Close()
		srv.Stop()
	}
	return tr, cleanup
}

// ---------------------------------------------------------------------------
// User round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_CreateUser(t *testing.T) {
	usvc := &mockUserSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc), unixserver.WithRoleService(tenantRoleSvc("system")))
	defer cleanup()

	got, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
		TenantID: "system",
		RoleIDs:  []string{"admin"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got == nil || got.Username != "alice" {
		t.Errorf("got %+v, want Username=alice", got)
	}
}

func TestUnixRT_GetUser(t *testing.T) {
	usvc := &mockUserSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	got, err := tr.GetUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got == nil || got.ID != 1 {
		t.Errorf("got %+v, want ID=u1", got)
	}
}

// TestUnixRT_GetUser_Uint64IDRoundTrip drives the full unix-socket transport
// (SDK client -> server -> SDK client) and proves a large uint64 user id
// survives the round trip on both the request id and the response id.
func TestUnixRT_GetUser_Uint64IDRoundTrip(t *testing.T) {
	const wantID uint64 = 1<<63 + 555
	var got uint64
	usvc := &mockUserSvc{
		getFn: func(_ context.Context, id uint64) (*user.User, error) {
			got = id
			return &user.User{ID: id, Username: "snowflake", Lifecycle: user.LifecycleActive}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	u, err := tr.GetUser(context.Background(), wantID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got != wantID {
		t.Fatalf("request id lost on wire: got %d, want %d", got, wantID)
	}
	if u == nil || u.ID != wantID {
		t.Fatalf("response id lost on wire: got %+v, want ID=%d", u, wantID)
	}
}

func TestUnixRT_GetUserByUsername(t *testing.T) {
	usvc := &mockUserSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	got, err := tr.GetUserByUsername(context.Background(), "t1", "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got == nil || got.Username != "alice" {
		t.Errorf("got %+v, want Username=alice", got)
	}
}

func TestUnixRT_UpdateUser(t *testing.T) {
	usvc := &mockUserSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc), unixserver.WithRoleService(tenantRoleSvc("")))
	defer cleanup()

	got, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{
		ID:       1,
		Username: "alice-renamed",
		RoleIDs:  []string{"admin"},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if got == nil || got.Username != "alice-renamed" {
		t.Errorf("got %+v, want Username=alice-renamed", got)
	}
}

func TestUnixRT_DeleteUser(t *testing.T) {
	usvc := &mockUserSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.DeleteUser(context.Background(), 1); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
}

func TestUnixRT_ListUsers(t *testing.T) {
	usvc := &mockUserSvc{
		listFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			if req.TenantID != "t1" {
				t.Errorf("tenant = %q, want t1", req.TenantID)
			}
			return &user.ListUsersResponse{
				Users:   []*user.User{{ID: 1, Username: "alice"}, {ID: 2, Username: "bob"}},
				Total:   2,
				HasMore: false,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	resp, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{TenantID: "t1", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(resp.Users) != 2 || resp.Total != 2 {
		t.Errorf("got %+v, want 2 users", resp)
	}
}

func TestUnixRT_EnableDisableUser(t *testing.T) {
	usvc := &mockUserSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.EnableUser(context.Background(), 1); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	if err := tr.DisableUser(context.Background(), 1); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
}

func TestUnixRT_SuspendUnsuspendUser(t *testing.T) {
	suspendCh, unsuspendCh := make(chan uint64, 1), make(chan uint64, 1)
	usvc := &mockUserSvc{
		suspendFn:   func(_ context.Context, id uint64) error { suspendCh <- id; return nil },
		unsuspendFn: func(_ context.Context, id uint64) error { unsuspendCh <- id; return nil },
	}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.SuspendUser(context.Background(), 7); err != nil {
		t.Fatalf("SuspendUser: %v", err)
	}
	if err := tr.UnsuspendUser(context.Background(), 8); err != nil {
		t.Fatalf("UnsuspendUser: %v", err)
	}
	if suspended, unsuspended := <-suspendCh, <-unsuspendCh; suspended != 7 || unsuspended != 8 {
		t.Fatalf("service saw suspend %d, unsuspend %d; want 7, 8", suspended, unsuspended)
	}
}

// mockUserSvcWithCert extends mockUserSvc with recording thumbprint and
// binding methods.
type mockUserSvcWithCert struct {
	mockUserSvc
	certThumbprintFn    func(ctx context.Context, thumbprint string) (*user.User, error)
	addCertBindingFn    func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
	removeCertBindingFn func(ctx context.Context, userID uint64, thumbprint string) (*user.User, error)
}

func (m *mockUserSvcWithCert) UserByCertThumbprint(ctx context.Context, thumbprint string) (*user.User, error) {
	if m.certThumbprintFn != nil {
		return m.certThumbprintFn(ctx, thumbprint)
	}
	return &user.User{ID: 1, Username: "cert-user"}, nil
}

func (m *mockUserSvcWithCert) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
	if m.addCertBindingFn != nil {
		return m.addCertBindingFn(ctx, userID, thumbprint, notAfterUnix)
	}
	return &user.User{ID: userID, Username: "cert-user"}, nil
}

func (m *mockUserSvcWithCert) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*user.User, error) {
	if m.removeCertBindingFn != nil {
		return m.removeCertBindingFn(ctx, userID, thumbprint)
	}
	return &user.User{ID: userID, Username: "cert-user"}, nil
}

var _ user.UserServicer = (*mockUserSvcWithCert)(nil)

func TestUnixRT_AddCertBinding(t *testing.T) {
	var gotID uint64
	var gotThumbprint string
	var gotNotAfter int64
	usvc := &mockUserSvcWithCert{
		addCertBindingFn: func(_ context.Context, userID uint64, tp string, notAfter int64) (*user.User, error) {
			gotID, gotThumbprint, gotNotAfter = userID, tp, notAfter
			return &user.User{ID: userID, Username: "cert-user"}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	got, err := tr.AddCertBinding(context.Background(), 7, "AABBCC", 1700000000)
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if got == nil || got.ID != 7 {
		t.Errorf("unexpected result: %+v", got)
	}
	if gotID != 7 || gotThumbprint != "AABBCC" || gotNotAfter != 1700000000 {
		t.Errorf("service saw (%d, %q, %d), want (7, \"AABBCC\", 1700000000)", gotID, gotThumbprint, gotNotAfter)
	}
}

// TestUnixRT_RemoveCertBinding round-trips the withdrawal verb over the unix
// socket, asserting both arguments reach the service: a thumbprint that did
// not arrive withdraws nothing and reports a not-found (go-qrdb#518).
func TestUnixRT_CreateUser_ServiceUnavailable(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()

	_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice"})
	if err == nil {
		t.Fatal("expected error when user service is not configured")
	}
}

// ---------------------------------------------------------------------------
// Role round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_CreateRole(t *testing.T) {
	rsvc := &mockRoleSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	got, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		Name:        "admin",
		TenantID:    "t1",
		Permissions: []string{"kv.read", "kv.write"},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if got == nil || got.Name != "admin" {
		t.Errorf("got %+v, want Name=admin", got)
	}
}

func TestUnixRT_GetRole(t *testing.T) {
	rsvc := &mockRoleSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	got, err := tr.GetRole(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if got == nil || got.ID != "r1" {
		t.Errorf("got %+v, want ID=r1", got)
	}
}

func TestUnixRT_UpdateRole(t *testing.T) {
	rsvc := &mockRoleSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	got, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{
		ID:          "r1",
		Name:        "operator",
		Permissions: []string{"kv.read"},
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if got == nil || got.Name != "operator" {
		t.Errorf("got %+v, want Name=operator", got)
	}
}

func TestUnixRT_DeleteRole(t *testing.T) {
	rsvc := &mockRoleSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	if err := tr.DeleteRole(context.Background(), "r1"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
}

func TestUnixRT_ListRoles(t *testing.T) {
	rsvc := &mockRoleSvc{
		listFn: func(_ context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			if req.TenantID != "t1" {
				t.Errorf("tenant = %q, want t1", req.TenantID)
			}
			return &role.ListRolesResponse{
				Roles: []*role.Role{{ID: "r1", Name: "admin"}, {ID: "r2", Name: "viewer"}},
				Total: 2,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	resp, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(resp.Roles) != 2 || resp.Total != 2 {
		t.Errorf("got %+v, want 2 roles", resp)
	}
}

func TestUnixRT_ListPermissions(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()

	resp, err := tr.ListPermissions(context.Background())
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
}

func TestUnixRT_AssignRole(t *testing.T) {
	usvc := &mockUserSvc{
		getFn: func(_ context.Context, id uint64) (*user.User, error) {
			return &user.User{ID: id, RoleIDs: []string{}}, nil
		},
	}
	rsvc := &mockRoleSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc), unixserver.WithRoleService(rsvc))
	defer cleanup()

	if err := tr.AssignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
}

func TestUnixRT_UnassignRole(t *testing.T) {
	usvc := &mockUserSvc{
		getFn: func(_ context.Context, id uint64) (*user.User, error) {
			return &user.User{ID: id, RoleIDs: []string{"r1"}}, nil
		},
	}
	rsvc := &mockRoleSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc), unixserver.WithRoleService(rsvc))
	defer cleanup()

	if err := tr.UnassignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cert round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_IssueCert(t *testing.T) {
	var gotReq *service.SignCertificateRequest
	enroll := &mockCertEnrollmentSvc{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			gotReq = req
			return &service.EnrolledCertificate{
				UserID:     7,
				Username:   req.Username,
				TenantID:   req.TenantID,
				CertPEM:    []byte("-----BEGIN CERTIFICATE-----\n"),
				Serial:     "abc123",
				Thumbprint: "thumb-abc123",
				NotAfter:   time.Unix(1800000000, 0).UTC(),
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.IssueCert(context.Background(), &transport.IssueCertRequest{
		Username: "alice",
		CSRPEM:   "csr-bytes",
		TTL:      "1h",
	})
	if err != nil {
		t.Fatalf("IssueCert: %v", err)
	}
	if got == nil || got.Serial != "abc123" {
		t.Errorf("got %+v, want Serial=abc123", got)
	}
	if got.Username != "alice" || got.Thumbprint != "thumb-abc123" {
		t.Errorf("got %+v, want Username=alice Thumbprint=thumb-abc123", got)
	}
	if !got.NotAfter.Equal(time.Unix(1800000000, 0).UTC()) {
		t.Errorf("NotAfter = %v, want %v", got.NotAfter, time.Unix(1800000000, 0).UTC())
	}
	if gotReq == nil || gotReq.Username != "alice" || string(gotReq.CSRPEM) != "csr-bytes" || gotReq.TTL != time.Hour {
		t.Errorf("server received %+v; want Username=alice CSRPEM=csr-bytes TTL=1h", gotReq)
	}
}

func TestUnixRT_EnrollCert(t *testing.T) {
	var gotReq *service.EnrollCertificateRequest
	enroll := &mockCertEnrollmentSvc{
		enrollFn: func(_ context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			gotReq = req
			return &service.EnrolledCertificate{
				Username:   req.Username,
				TenantID:   req.TenantID,
				CertPEM:    req.CertPEM,
				Thumbprint: "thumb-byoc",
				NotAfter:   time.Unix(1800000000, 0).UTC(),
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.EnrollCert(context.Background(), &transport.EnrollCertRequest{
		Username: "alice",
		CertPEM:  "leaf-pem",
		ChainPEM: "chain-pem",
	})
	if err != nil {
		t.Fatalf("EnrollCert: %v", err)
	}
	if got.Serial != "" {
		t.Errorf("Serial = %q, want empty for a bring-your-own certificate", got.Serial)
	}
	if got.Thumbprint != "thumb-byoc" {
		t.Errorf("Thumbprint = %q, want thumb-byoc", got.Thumbprint)
	}
	if gotReq == nil || string(gotReq.CertPEM) != "leaf-pem" || string(gotReq.ChainPEM) != "chain-pem" {
		t.Errorf("server received %+v; want CertPEM=leaf-pem ChainPEM=chain-pem", gotReq)
	}
}

func TestUnixRT_IssueCert_Error(t *testing.T) {
	enroll := &mockCertEnrollmentSvc{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &cert.CertGenerateError{Err: errors.New("CA key sealed")}
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	_, err := tr.IssueCert(context.Background(), &transport.IssueCertRequest{Username: "alice", CSRPEM: "csr"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Trust anchor round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_AddTrustAnchors(t *testing.T) {
	var gotTenant, gotBundle string
	enroll := &mockCertEnrollmentSvc{
		addFn: func(_ context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
			gotTenant, gotBundle = tenantID, string(bundlePEM)
			return []trustanchor.Anchor{{
				Fingerprint: "fp1", Subject: "CN=ca",
				NotBefore: time.Unix(1700000000, 0).UTC(), NotAfter: time.Unix(1800000000, 0).UTC(),
				CertPEM: []byte("ca-pem"), AddedAt: time.Unix(1750000000, 0).UTC(),
			}}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	resp, err := tr.AddTrustAnchors(context.Background(), &transport.AddTrustAnchorsRequest{
		TenantID: "t1",
		CertsPEM: "ca-bundle-pem",
	})
	if err != nil {
		t.Fatalf("AddTrustAnchors: %v", err)
	}
	if len(resp.Anchors) != 1 || resp.Anchors[0].Fingerprint != "fp1" {
		t.Fatalf("got %+v, want one anchor fp1", resp.Anchors)
	}
	if resp.Anchors[0].CertPEM != "ca-pem" || !resp.Anchors[0].NotAfter.Equal(time.Unix(1800000000, 0).UTC()) {
		t.Errorf("anchor = %+v; want CertPEM=ca-pem NotAfter=1800000000", resp.Anchors[0])
	}
	if gotBundle != "ca-bundle-pem" {
		t.Errorf("server received CertsPEM=%q, want ca-bundle-pem", gotBundle)
	}
	if gotTenant != "t1" {
		t.Errorf("server received TenantID=%q, want t1", gotTenant)
	}
}

func TestUnixRT_ListTrustAnchors(t *testing.T) {
	enroll := &mockCertEnrollmentSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	resp, err := tr.ListTrustAnchors(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListTrustAnchors: %v", err)
	}
	if len(resp.Anchors) != 1 || resp.Anchors[0].Fingerprint != "fp1" {
		t.Fatalf("got %+v, want one anchor fp1", resp.Anchors)
	}
}

func TestUnixRT_RemoveTrustAnchor(t *testing.T) {
	var gotTenant, gotFingerprint string
	enroll := &mockCertEnrollmentSvc{
		removeFn: func(_ context.Context, tenantID, fingerprint string) error {
			gotTenant, gotFingerprint = tenantID, fingerprint
			return nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	if err := tr.RemoveTrustAnchor(context.Background(), "t1", "fp1"); err != nil {
		t.Fatalf("RemoveTrustAnchor: %v", err)
	}
	if gotFingerprint != "fp1" {
		t.Errorf("server received fingerprint=%q, want fp1", gotFingerprint)
	}
	if gotTenant != "t1" {
		t.Errorf("server received TenantID=%q, want t1", gotTenant)
	}
}

func TestUnixRT_RemoveTrustAnchor_Error(t *testing.T) {
	enroll := &mockCertEnrollmentSvc{
		removeFn: func(context.Context, string, string) error {
			return errors.New("no such anchor")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	if err := tr.RemoveTrustAnchor(context.Background(), "t1", "nope"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUnixRT_RevokeCert(t *testing.T) {
	provider := &mockCertProvider{}
	tr, cleanup := startAuthServer(t, unixserver.WithCertProvider(provider))
	defer cleanup()

	if _, err := tr.RevokeCert(context.Background(), "serial-1"); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
}

func TestUnixRT_ListCerts(t *testing.T) {
	provider := &mockCertProvider{
		listFn: func(_ context.Context, tenant string) ([]*cert.CertRecord, error) {
			return []*cert.CertRecord{
				{Serial: "s1", Username: "alice", TenantID: tenant, IssuedAt: time.Now()},
				{Serial: "s2", Username: "bob", TenantID: tenant, IssuedAt: time.Now(), Revoked: true},
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertProvider(provider))
	defer cleanup()

	resp, err := tr.ListCerts(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	if len(resp.Certs) != 2 {
		t.Fatalf("got %d certs, want 2", len(resp.Certs))
	}
}

func TestUnixRT_CertOps_NotConfigured(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()

	_, err := tr.IssueCert(context.Background(), &transport.IssueCertRequest{Username: "alice"})
	if err == nil {
		t.Error("expected error when cert enrollment is not configured")
	}
	if _, err := tr.EnrollCert(context.Background(), &transport.EnrollCertRequest{Username: "alice", CertPEM: "pem"}); err == nil {
		t.Error("expected error when cert enrollment is not configured for enroll")
	}
	if _, err := tr.RevokeCert(context.Background(), "s1"); err == nil {
		t.Error("expected error when cert provider is not configured for revoke")
	}
}

// ---------------------------------------------------------------------------
// Error propagation
// ---------------------------------------------------------------------------

// The identity fields this PR threads through the unix transport must survive
// the round trip, and GetUserByEmail must reach the service.
//
// The round-trip suite exercised CreateUser without asserting the new fields
// and never called GetUserByEmail — the same transport-specific omission the
// MCP client already had twice on this PR (Codex, PR #191).
func TestUnixRT_NewIdentityFieldsRoundTrip(t *testing.T) {
	var gotCreate *user.CreateUserRequest
	var gotEmailLookup string

	usvc := &mockUserSvc{
		createFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			gotCreate = req
			return &user.User{
				ID: 1, Username: req.Username, Email: req.Email, Name: req.Name,
				TenantID: req.TenantID, Lifecycle: user.LifecycleActive,
			}, nil
		},
		getByEmailFn: func(_ context.Context, _, email string) (*user.User, error) {
			gotEmailLookup = email
			return &user.User{ID: 7, Email: email, Lifecycle: user.LifecycleActive}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()
	ctx := context.Background()

	got, err := tr.CreateUser(ctx, &transport.CreateUserRequest{
		Username: "alice", Email: "alice@example.com", Name: "Alice", TenantID: "t1",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if gotCreate.Email != "alice@example.com" {
		t.Fatalf("Email dropped on the wire: %q", gotCreate.Email)
	}
	if gotCreate.Name != "Alice" {
		t.Fatalf("Name dropped on the wire: %q", gotCreate.Name)
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("Email dropped from the response: %q", got.Email)
	}

	if _, cErr := tr.CreateUser(ctx, &transport.CreateUserRequest{
		Username: "cert-only", WithoutEmailLoginKey: true, TenantID: "t1",
	}); cErr != nil {
		t.Fatalf("CreateUser(cert-only): %v", cErr)
	}
	if !gotCreate.WithoutEmailLoginKey {
		t.Fatal("WithoutEmailLoginKey dropped — a certificate-only identity cannot be created over the unix socket")
	}

	u, lErr := tr.GetUserByEmail(ctx, "t1", "alice@example.com")
	if lErr != nil {
		t.Fatalf("GetUserByEmail: %v", lErr)
	}
	if gotEmailLookup != "alice@example.com" {
		t.Fatalf("lookup email did not reach the service: %q", gotEmailLookup)
	}
	if u.ID != 7 {
		t.Fatalf("GetUserByEmail returned id %d, want 7", u.ID)
	}
}

func (m *mockRoleSvc) ReconcileBuiltInRoles(ctx context.Context, tenantID string) (*role.ReconcileReport, error) {
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
	if m.tenantFn != nil {
		return m.tenantFn(ctx, serial)
	}
	return "", false, nil
}
