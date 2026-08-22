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

//go:build !integration

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock user service
// ---------------------------------------------------------------------------

type mockUserService struct {
	createFn  func(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
	getFn     func(ctx context.Context, id uint64) (*user.User, error)
	getByName func(ctx context.Context, tenantID, username string) (*user.User, error)
	updateFn  func(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
	deleteFn  func(ctx context.Context, id uint64) error
	listFn    func(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
	enableFn  func(ctx context.Context, id uint64) error
	disableFn func(ctx context.Context, id uint64) error
}

func (m *mockUserService) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &user.User{ID: 1001, Username: req.Username, TenantID: req.TenantID, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockUserService) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &user.User{ID: id, Username: "testuser", TenantID: "system", Enabled: true}, nil
}

func (m *mockUserService) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if m.getByName != nil {
		return m.getByName(ctx, tenantID, username)
	}
	return &user.User{ID: 1001, Username: username, TenantID: tenantID, Enabled: true}, nil
}

func (m *mockUserService) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, req)
	}
	return &user.User{ID: req.ID, Username: "updated", TenantID: "system", Enabled: true, UpdatedAt: time.Now()}, nil
}

func (m *mockUserService) DeleteUser(ctx context.Context, id uint64) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockUserService) ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &user.ListUsersResponse{
		Users: []*user.User{
			{ID: 1001, Username: "alice", TenantID: "system", Enabled: true},
		},
		Total:   1,
		HasMore: false,
	}, nil
}

func (m *mockUserService) EnableUser(ctx context.Context, id uint64) error {
	if m.enableFn != nil {
		return m.enableFn(ctx, id)
	}
	return nil
}

func (m *mockUserService) DisableUser(ctx context.Context, id uint64) error {
	if m.disableFn != nil {
		return m.disableFn(ctx, id)
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
// Mock role service
// ---------------------------------------------------------------------------

type mockRoleService struct {
	createFn     func(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error)
	getFn        func(ctx context.Context, id string) (*role.Role, error)
	getByNameFn  func(ctx context.Context, tenantID, name string) (*role.Role, error)
	updateFn     func(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error)
	deleteFn     func(ctx context.Context, id string) error
	listFn       func(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error)
	resolvePerms func(ctx context.Context, roleIDs []string) ([]permission.Permission, error)
}

func (m *mockRoleService) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &role.Role{ID: "role-001", Name: req.Name, TenantID: req.TenantID, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockRoleService) GetRole(ctx context.Context, id string) (*role.Role, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &role.Role{ID: id, Name: "testrole", TenantID: "system"}, nil
}

func (m *mockRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	if m.getByNameFn != nil {
		return m.getByNameFn(ctx, tenantID, name)
	}
	return &role.Role{ID: "role-001", Name: name, TenantID: tenantID}, nil
}

func (m *mockRoleService) UpdateRole(ctx context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, req)
	}
	return &role.Role{ID: req.ID, Name: "updated", TenantID: "system", UpdatedAt: time.Now()}, nil
}

func (m *mockRoleService) DeleteRole(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockRoleService) ListRoles(ctx context.Context, req *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &role.ListRolesResponse{
		Roles: []*role.Role{
			{ID: "role-001", Name: "admin", TenantID: "system"},
		},
		Total:   1,
		HasMore: false,
	}, nil
}

func (m *mockRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	if m.resolvePerms != nil {
		return m.resolvePerms(ctx, roleIDs)
	}
	return []permission.Permission{}, nil
}

// ---------------------------------------------------------------------------
// Mock cert provider
// ---------------------------------------------------------------------------

type mockCertProvider struct {
	generateFn func(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error)
	exportFn   func(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	revokeFn   func(ctx context.Context, serial string) error
	isRevoked  func(ctx context.Context, serial string) (bool, error)
	listFn     func(ctx context.Context, tenantID string) ([]*cert.CertRecord, error)
	caCertFn   func() []byte
}

func (m *mockCertProvider) GenerateClientCert(ctx context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, req)
	}
	return &cert.CertBundle{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"),
		KeyPEM:  []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----\n"),
		Serial:  "AABBCCDD",
	}, nil
}

func (m *mockCertProvider) ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	if m.exportFn != nil {
		return m.exportFn(ctx, certPEM, keyPEM, passphrase)
	}
	return []byte("mock-pkcs12-data"), nil
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

func (m *mockCertProvider) ListCerts(ctx context.Context, tenantID string) ([]*cert.CertRecord, error) {
	if m.listFn != nil {
		return m.listFn(ctx, tenantID)
	}
	return []*cert.CertRecord{
		{Serial: "AABBCCDD", Username: "testuser", TenantID: tenantID, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour)},
	}, nil
}

func (m *mockCertProvider) CACertPEM() []byte {
	if m.caCertFn != nil {
		return m.caCertFn()
	}
	return []byte("-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n")
}

// ---------------------------------------------------------------------------
// Auth connection binding helper
// ---------------------------------------------------------------------------

func newAuthTestConnectionBinding(
	t *testing.T,
	userSvc user.UserServicer,
	roleSvc role.RoleServicer,
	certProv cert.CertProvider,
) *ConnectionBinding {
	t.Helper()
	var opts []embedded.Option
	if userSvc != nil {
		opts = append(opts, embedded.WithUserService(userSvc))
	}
	if roleSvc != nil {
		opts = append(opts, embedded.WithRoleService(roleSvc))
	}
	if certProv != nil {
		opts = append(opts, embedded.WithCertProvider(certProv))
	}
	trans, err := embedded.New(newMockKVService(), newMockAdminService(), opts...)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(trans)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	conn := NewConnectionBinding(testLogger(t))
	conn.SetClient(cl)
	return conn
}

// ---------------------------------------------------------------------------
// UserBinding - connected success path tests
// ---------------------------------------------------------------------------

func TestUserBinding_Create_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	u, err := b.Create(&transport.CreateUserRequest{
		Username: "alice",
		TenantID: "system",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if u == nil {
		t.Fatal("expected non-nil user")
	}
}

func TestUserBinding_Create_ServiceError(t *testing.T) {
	svc := &mockUserService{
		createFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, errors.New("create failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, svc, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	_, err := b.Create(&transport.CreateUserRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error from Create")
	}
}

func TestUserBinding_Get_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	u, err := b.Get("1001")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if u == nil {
		t.Fatal("expected non-nil user")
	}
}

func TestUserBinding_Get_ServiceError(t *testing.T) {
	svc := &mockUserService{
		getFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, errors.New("not found")
		},
	}
	conn := newAuthTestConnectionBinding(t, svc, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	_, err := b.Get("9999")
	if err == nil {
		t.Fatal("expected error from Get")
	}
}

func TestUserBinding_Update_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	u, err := b.Update(&transport.UpdateUserRequest{ID: 1001})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if u == nil {
		t.Fatal("expected non-nil user")
	}
}

func TestUserBinding_Update_ServiceError(t *testing.T) {
	svc := &mockUserService{
		updateFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, errors.New("update failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, svc, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	_, err := b.Update(&transport.UpdateUserRequest{ID: 1001})
	if err == nil {
		t.Fatal("expected error from Update")
	}
}

func TestUserBinding_Delete_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	if err := b.Delete("1001"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestUserBinding_Delete_ServiceError(t *testing.T) {
	svc := &mockUserService{
		deleteFn: func(_ context.Context, _ uint64) error {
			return errors.New("delete failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, svc, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	if err := b.Delete("1001"); err == nil {
		t.Fatal("expected error from Delete")
	}
}

func TestUserBinding_List_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	resp, err := b.List(&transport.ListUsersRequest{TenantID: "system"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestUserBinding_List_ServiceError(t *testing.T) {
	svc := &mockUserService{
		listFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, svc, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	_, err := b.List(&transport.ListUsersRequest{TenantID: "system"})
	if err == nil {
		t.Fatal("expected error from List")
	}
}

func TestUserBinding_Enable_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	if err := b.Enable("1001"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestUserBinding_Enable_ServiceError(t *testing.T) {
	svc := &mockUserService{
		enableFn: func(_ context.Context, _ uint64) error {
			return errors.New("enable failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, svc, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	if err := b.Enable("1001"); err == nil {
		t.Fatal("expected error from Enable")
	}
}

func TestUserBinding_Disable_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	if err := b.Disable("1001"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestUserBinding_Disable_ServiceError(t *testing.T) {
	svc := &mockUserService{
		disableFn: func(_ context.Context, _ uint64) error {
			return errors.New("disable failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, svc, nil, nil)
	b := NewUserBinding(testLogger(t), conn)

	if err := b.Disable("1001"); err == nil {
		t.Fatal("expected error from Disable")
	}
}

// ---------------------------------------------------------------------------
// RoleBinding - connected success path tests
// ---------------------------------------------------------------------------

func TestRoleBinding_Create_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	r, err := b.Create(&transport.CreateRoleRequest{Name: "editor", TenantID: "system"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil role")
	}
}

func TestRoleBinding_Create_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		createFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, errors.New("create failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, svc, nil)
	b := NewRoleBinding(testLogger(t), conn)

	_, err := b.Create(&transport.CreateRoleRequest{Name: "editor"})
	if err == nil {
		t.Fatal("expected error from Create")
	}
}

func TestRoleBinding_Get_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	r, err := b.Get("role-001")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil role")
	}
}

func TestRoleBinding_Get_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		getFn: func(_ context.Context, _ string) (*role.Role, error) {
			return nil, errors.New("not found")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, svc, nil)
	b := NewRoleBinding(testLogger(t), conn)

	_, err := b.Get("role-missing")
	if err == nil {
		t.Fatal("expected error from Get")
	}
}

func TestRoleBinding_Update_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	r, err := b.Update(&transport.UpdateRoleRequest{ID: "role-001"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil role")
	}
}

func TestRoleBinding_Update_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		updateFn: func(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
			return nil, errors.New("update failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, svc, nil)
	b := NewRoleBinding(testLogger(t), conn)

	_, err := b.Update(&transport.UpdateRoleRequest{ID: "role-001"})
	if err == nil {
		t.Fatal("expected error from Update")
	}
}

func TestRoleBinding_Delete_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	if err := b.Delete("role-001"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRoleBinding_Delete_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("delete failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, svc, nil)
	b := NewRoleBinding(testLogger(t), conn)

	if err := b.Delete("role-001"); err == nil {
		t.Fatal("expected error from Delete")
	}
}

func TestRoleBinding_List_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	resp, err := b.List(&transport.ListRolesRequest{TenantID: "system"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestRoleBinding_List_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		listFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, svc, nil)
	b := NewRoleBinding(testLogger(t), conn)

	_, err := b.List(&transport.ListRolesRequest{TenantID: "system"})
	if err == nil {
		t.Fatal("expected error from List")
	}
}

func TestRoleBinding_Assign_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	if err := b.Assign("1001", "role-001"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRoleBinding_Assign_ServiceError(t *testing.T) {
	roleSvc := &mockRoleService{
		getFn: func(_ context.Context, _ string) (*role.Role, error) {
			return nil, errors.New("role not found")
		},
	}
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, roleSvc, nil)
	b := NewRoleBinding(testLogger(t), conn)

	if err := b.Assign("1001", "role-missing"); err == nil {
		t.Fatal("expected error from Assign")
	}
}

func TestRoleBinding_Unassign_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, &mockUserService{}, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	if err := b.Unassign("1001", "role-001"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRoleBinding_Unassign_ServiceError(t *testing.T) {
	userSvc := &mockUserService{
		getFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, errors.New("user not found")
		},
	}
	conn := newAuthTestConnectionBinding(t, userSvc, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	if err := b.Unassign("user-missing", "role-001"); err == nil {
		t.Fatal("expected error from Unassign")
	}
}

func TestRoleBinding_ListPermissions_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, &mockRoleService{}, nil)
	b := NewRoleBinding(testLogger(t), conn)

	resp, err := b.ListPermissions()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// CertBinding - connected success path tests
// ---------------------------------------------------------------------------

func TestCertBinding_Generate_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, nil, &mockCertProvider{})
	b := NewCertBinding(testLogger(t), conn)

	bundle, err := b.Generate(&transport.GenerateCertRequest{
		Username: "alice",
		TenantID: "system",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}
}

func TestCertBinding_Generate_ServiceError(t *testing.T) {
	prov := &mockCertProvider{
		generateFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, errors.New("ca key not loaded")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, nil, prov)
	b := NewCertBinding(testLogger(t), conn)

	_, err := b.Generate(&transport.GenerateCertRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error from Generate")
	}
}

func TestCertBinding_Export_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, nil, &mockCertProvider{})
	b := NewCertBinding(testLogger(t), conn)

	resp, err := b.Export(&transport.ExportCertRequest{
		CertPEM:    "cert-pem",
		KeyPEM:     "key-pem",
		Passphrase: "secret",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestCertBinding_Export_ServiceError(t *testing.T) {
	prov := &mockCertProvider{
		exportFn: func(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
			return nil, errors.New("export failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, nil, prov)
	b := NewCertBinding(testLogger(t), conn)

	_, err := b.Export(&transport.ExportCertRequest{CertPEM: "x", KeyPEM: "y"})
	if err == nil {
		t.Fatal("expected error from Export")
	}
}

func TestCertBinding_Revoke_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, nil, &mockCertProvider{})
	b := NewCertBinding(testLogger(t), conn)

	if err := b.Revoke("AABBCCDD"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCertBinding_Revoke_ServiceError(t *testing.T) {
	prov := &mockCertProvider{
		revokeFn: func(_ context.Context, _ string) error {
			return errors.New("revoke failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, nil, prov)
	b := NewCertBinding(testLogger(t), conn)

	if err := b.Revoke("AABBCCDD"); err == nil {
		t.Fatal("expected error from Revoke")
	}
}

func TestCertBinding_List_Success(t *testing.T) {
	conn := newAuthTestConnectionBinding(t, nil, nil, &mockCertProvider{})
	b := NewCertBinding(testLogger(t), conn)

	resp, err := b.List("system")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestCertBinding_List_ServiceError(t *testing.T) {
	prov := &mockCertProvider{
		listFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return nil, errors.New("list failed")
		},
	}
	conn := newAuthTestConnectionBinding(t, nil, nil, prov)
	b := NewCertBinding(testLogger(t), conn)

	_, err := b.List("system")
	if err == nil {
		t.Fatal("expected error from List")
	}
}

// suppress unused import warnings
var (
	_ service.KVServicer = (*mockKVService)(nil)
)

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}
