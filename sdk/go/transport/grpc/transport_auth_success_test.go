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

package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeUserServicer
// ===========================================================================

type fakeUserServicer struct{}

var _ user.UserServicer = (*fakeUserServicer)(nil)

func (f *fakeUserServicer) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	return &user.User{ID: 1, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs, Enabled: true, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeUserServicer) GetUser(_ context.Context, id uint64) (*user.User, error) {
	return &user.User{ID: id, Username: "alice", Enabled: true, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeUserServicer) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	return &user.User{ID: 1, Username: username, TenantID: tenantID, Enabled: true, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeUserServicer) UpdateUser(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	return &user.User{ID: req.ID, Username: req.Username, RoleIDs: req.RoleIDs, Enabled: true, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeUserServicer) DeleteUser(_ context.Context, _ uint64) error { return nil }

func (f *fakeUserServicer) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return &user.ListUsersResponse{
		Users: []*user.User{{ID: 1, Username: "alice", Enabled: true, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}},
		Total: 1,
	}, nil
}

func (f *fakeUserServicer) EnableUser(_ context.Context, _ uint64) error  { return nil }
func (f *fakeUserServicer) DisableUser(_ context.Context, _ uint64) error { return nil }
func (f *fakeUserServicer) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (f *fakeUserServicer) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

// ===========================================================================
// fakeRoleServicer
// ===========================================================================

type fakeRoleServicer struct{}

var _ role.RoleServicer = (*fakeRoleServicer)(nil)

func (f *fakeRoleServicer) CreateRole(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	return &role.Role{ID: "r1", Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeRoleServicer) GetRole(_ context.Context, id string) (*role.Role, error) {
	return &role.Role{ID: id, Name: "admin", CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeRoleServicer) GetRoleByName(_ context.Context, _, name string) (*role.Role, error) {
	return &role.Role{ID: "r1", Name: name, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeRoleServicer) UpdateRole(_ context.Context, req *role.UpdateRoleRequest) (*role.Role, error) {
	return &role.Role{ID: req.ID, Name: req.Name, Permissions: req.Permissions, CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeRoleServicer) DeleteRole(_ context.Context, _ string) error { return nil }

func (f *fakeRoleServicer) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return &role.ListRolesResponse{
		Roles: []*role.Role{{ID: "r1", Name: "admin", CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}},
		Total: 1,
	}, nil
}

func (f *fakeRoleServicer) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return nil, nil
}

// ===========================================================================
// fakeCertProvider
// ===========================================================================

type fakeCertProvider struct{}

var _ cert.CertProvider = (*fakeCertProvider)(nil)

func (f *fakeCertProvider) GenerateClientCert(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
	return &cert.CertBundle{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY"), Serial: "001"}, nil
}

func (f *fakeCertProvider) ExportPKCS12(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
	return []byte("PKCS12"), nil
}

func (f *fakeCertProvider) RevokeCert(_ context.Context, _ string) error        { return nil }
func (f *fakeCertProvider) IsRevoked(_ context.Context, _ string) (bool, error) { return false, nil }
func (f *fakeCertProvider) ListCerts(_ context.Context, _ string) ([]*cert.CertRecord, error) {
	return []*cert.CertRecord{
		{Serial: "001", Username: "alice", IssuedAt: time.Unix(1700000000, 0), ExpiresAt: time.Unix(1800000000, 0)},
	}, nil
}

func (f *fakeCertProvider) CACertPEM() []byte { return []byte("CA") }

// ===========================================================================
// Server setup
// ===========================================================================

func startUserRoleCertServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil,
		grpcpkg.WithRegisterUserService(&fakeUserServicer{}),
		grpcpkg.WithRegisterRoleService(&fakeRoleServicer{}),
		grpcpkg.WithRegisterCertProvider(&fakeCertProvider{}),
	)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectUserTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() { _ = conn.Close() }
}

// ===========================================================================
// User tests
// ===========================================================================

func TestGRPCUser_AllMethods_Success(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("CreateUser", func(t *testing.T) {
		u, err := tr.CreateUser(ctx, &transport.CreateUserRequest{Username: "alice", TenantID: "t1", RoleIDs: []string{"r1"}})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		if u.Username != "alice" {
			t.Fatalf("Username = %q", u.Username)
		}
	})
	t.Run("GetUser", func(t *testing.T) {
		u, err := tr.GetUser(ctx, 1)
		if err != nil {
			t.Fatalf("GetUser: %v", err)
		}
		if u.ID != 1 {
			t.Fatalf("ID = %d", u.ID)
		}
	})
	t.Run("GetUserByUsername", func(t *testing.T) {
		u, err := tr.GetUserByUsername(ctx, "t1", "alice")
		if err != nil {
			t.Fatalf("GetUserByUsername: %v", err)
		}
		if u.Username != "alice" {
			t.Fatalf("Username = %q", u.Username)
		}
	})
	t.Run("UpdateUser", func(t *testing.T) {
		u, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1, Username: "alice2"})
		if err != nil {
			t.Fatalf("UpdateUser: %v", err)
		}
		if u.ID != 1 {
			t.Fatalf("ID = %d", u.ID)
		}
	})
	t.Run("DeleteUser", func(t *testing.T) {
		if err := tr.DeleteUser(ctx, 1); err != nil {
			t.Fatalf("DeleteUser: %v", err)
		}
	})
	t.Run("ListUsers", func(t *testing.T) {
		resp, err := tr.ListUsers(ctx, &transport.ListUsersRequest{TenantID: "t1", Page: 1, PageSize: 10})
		if err != nil {
			t.Fatalf("ListUsers: %v", err)
		}
		if resp.Total != 1 {
			t.Fatalf("Total = %d", resp.Total)
		}
	})
	t.Run("EnableUser", func(t *testing.T) {
		if err := tr.EnableUser(ctx, 1); err != nil {
			t.Fatalf("EnableUser: %v", err)
		}
	})
	t.Run("DisableUser", func(t *testing.T) {
		if err := tr.DisableUser(ctx, 1); err != nil {
			t.Fatalf("DisableUser: %v", err)
		}
	})
}

// ===========================================================================
// Role tests
// ===========================================================================

func TestGRPCRole_AllMethods_Success(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("CreateRole", func(t *testing.T) {
		r, err := tr.CreateRole(ctx, &transport.CreateRoleRequest{Name: "admin", TenantID: "t1", Permissions: []string{"kv:read"}})
		if err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		if r.Name != "admin" {
			t.Fatalf("Name = %q", r.Name)
		}
	})
	t.Run("GetRole", func(t *testing.T) {
		r, err := tr.GetRole(ctx, "r1")
		if err != nil {
			t.Fatalf("GetRole: %v", err)
		}
		if r.ID != "r1" {
			t.Fatalf("ID = %q", r.ID)
		}
	})
	t.Run("UpdateRole", func(t *testing.T) {
		r, err := tr.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "r1", Name: "admin2"})
		if err != nil {
			t.Fatalf("UpdateRole: %v", err)
		}
		if r.ID != "r1" {
			t.Fatalf("ID = %q", r.ID)
		}
	})
	t.Run("DeleteRole", func(t *testing.T) {
		if err := tr.DeleteRole(ctx, "r1"); err != nil {
			t.Fatalf("DeleteRole: %v", err)
		}
	})
	t.Run("ListRoles", func(t *testing.T) {
		resp, err := tr.ListRoles(ctx, &transport.ListRolesRequest{TenantID: "t1"})
		if err != nil {
			t.Fatalf("ListRoles: %v", err)
		}
		if resp.Total != 1 {
			t.Fatalf("Total = %d", resp.Total)
		}
	})
}

// ===========================================================================
// Cert tests
// ===========================================================================

func TestGRPCCert_AllMethods_Success(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("GenerateCert", func(t *testing.T) {
		b, err := tr.GenerateCert(ctx, &transport.GenerateCertRequest{Username: "alice", TenantID: "t1"})
		if err != nil {
			t.Fatalf("GenerateCert: %v", err)
		}
		if b.Serial != "001" {
			t.Fatalf("Serial = %q", b.Serial)
		}
	})
	t.Run("ExportCert", func(t *testing.T) {
		resp, err := tr.ExportCert(ctx, &transport.ExportCertRequest{CertPEM: "CERT", KeyPEM: "KEY", Passphrase: "x"})
		if err != nil {
			t.Fatalf("ExportCert: %v", err)
		}
		if resp.PKCS12 == "" {
			t.Fatal("expected PKCS12 to be set")
		}
	})
	t.Run("RevokeCert", func(t *testing.T) {
		if err := tr.RevokeCert(ctx, "001"); err != nil {
			t.Fatalf("RevokeCert: %v", err)
		}
	})
	t.Run("ListCerts", func(t *testing.T) {
		resp, err := tr.ListCerts(ctx, "t1")
		if err != nil {
			t.Fatalf("ListCerts: %v", err)
		}
		if len(resp.Certs) != 1 {
			t.Fatalf("got %d certs", len(resp.Certs))
		}
	})
}

func TestGRPCRoleAssignment_AllMethods_Success(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("AssignRole", func(t *testing.T) {
		if err := tr.AssignRole(ctx, 1, "r1"); err != nil {
			t.Fatalf("AssignRole: %v", err)
		}
	})
	t.Run("UnassignRole", func(t *testing.T) {
		if err := tr.UnassignRole(ctx, 1, "r1"); err != nil {
			t.Fatalf("UnassignRole: %v", err)
		}
	})
	t.Run("ListPermissions", func(t *testing.T) {
		if _, err := tr.ListPermissions(ctx); err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
	})
}
