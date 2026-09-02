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
}

func (m *mockUserSvc) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &user.User{ID: 1, Username: req.Username, TenantID: req.TenantID, Enabled: true}, nil
}

func (m *mockUserSvc) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &user.User{ID: id, Username: "stub", Enabled: true}, nil
}

func (m *mockUserSvc) GetUserByUsername(ctx context.Context, tenant, username string) (*user.User, error) {
	if m.getByUserNameFn != nil {
		return m.getByUserNameFn(ctx, tenant, username)
	}
	return &user.User{ID: 1, Username: username, TenantID: tenant, Enabled: true}, nil
}

func (m *mockUserSvc) GetUserByEmail(ctx context.Context, tenant, email string) (*user.User, error) {
	normalized := user.NormalizeEmail(email)
	if m.getByEmailFn != nil {
		return m.getByEmailFn(ctx, tenant, normalized)
	}
	return &user.User{ID: 1, Username: normalized, Email: normalized, TenantID: tenant, Enabled: true}, nil
}

func (m *mockUserSvc) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, req)
	}
	return &user.User{ID: req.ID, Username: req.Username, RoleIDs: req.RoleIDs, Enabled: true}, nil
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

func (m *mockUserSvc) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserSvc) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
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
}

func (m *mockRoleSvc) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &role.Role{ID: "r1", Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions}, nil
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
	generateFn func(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error)
	exportFn   func(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	revokeFn   func(ctx context.Context, serial string) error
	isRevoked  func(ctx context.Context, serial string) (bool, error)
	listFn     func(ctx context.Context, tenant string) ([]*cert.CertRecord, error)
}

func (m *mockCertProvider) GenerateClientCert(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, req)
	}
	return &cert.CertBundle{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\n"),
		KeyPEM:  []byte("-----BEGIN PRIVATE KEY-----\n"),
		Serial:  "abc123",
	}, nil
}

func (m *mockCertProvider) ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	if m.exportFn != nil {
		return m.exportFn(ctx, certPEM, keyPEM, passphrase)
	}
	return []byte("pkcs12-stub"), nil
}

func (m *mockCertProvider) RevokeCert(ctx context.Context, serial string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, serial)
	}
	return nil
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
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	got, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
		TenantID: "t1",
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
			return &user.User{ID: id, Username: "snowflake", Enabled: true}, nil
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
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
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

func TestUnixRT_GenerateCert(t *testing.T) {
	provider := &mockCertProvider{}
	tr, cleanup := startAuthServer(t, unixserver.WithCertProvider(provider))
	defer cleanup()

	got, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{
		Username: "alice",
		TenantID: "t1",
		TTL:      "1h",
	})
	if err != nil {
		t.Fatalf("GenerateCert: %v", err)
	}
	if got == nil || got.Serial != "abc123" {
		t.Errorf("got %+v, want Serial=abc123", got)
	}
}

func TestUnixRT_ExportCert(t *testing.T) {
	provider := &mockCertProvider{}
	tr, cleanup := startAuthServer(t, unixserver.WithCertProvider(provider))
	defer cleanup()

	got, err := tr.ExportCert(context.Background(), &transport.ExportCertRequest{
		CertPEM:    "cert-data",
		KeyPEM:     "key-data",
		Passphrase: "secret",
	})
	if err != nil {
		t.Fatalf("ExportCert: %v", err)
	}
	if got == nil || len(got.PKCS12) == 0 {
		t.Errorf("expected pkcs12 bytes, got %+v", got)
	}
}

func TestUnixRT_RevokeCert(t *testing.T) {
	provider := &mockCertProvider{}
	tr, cleanup := startAuthServer(t, unixserver.WithCertProvider(provider))
	defer cleanup()

	if err := tr.RevokeCert(context.Background(), "serial-1"); err != nil {
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

	_, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{Username: "alice"})
	if err == nil {
		t.Error("expected error when cert provider is not configured")
	}
	if err := tr.RevokeCert(context.Background(), "s1"); err == nil {
		t.Error("expected error when cert provider is not configured for revoke")
	}
}

// ---------------------------------------------------------------------------
// Error propagation
// ---------------------------------------------------------------------------

func TestUnixRT_GetUser_NotFound(t *testing.T) {
	usvc := &mockUserSvc{
		getFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, errors.New("not found")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	_, err := tr.GetUser(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error from server")
	}
}

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
				TenantID: req.TenantID, Enabled: true,
			}, nil
		},
		getByEmailFn: func(_ context.Context, _, email string) (*user.User, error) {
			gotEmailLookup = email
			return &user.User{ID: 7, Email: email, Enabled: true}, nil
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
