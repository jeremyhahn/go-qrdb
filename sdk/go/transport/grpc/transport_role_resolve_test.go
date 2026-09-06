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
	"errors"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// fakeRoleServicerWithPerms extends fakeRoleServicer so that
// ResolvePermissions returns a non-empty permission list, enabling
// the success-path assertion.
// ---------------------------------------------------------------------------

type fakeRoleServicerWithPerms struct {
	fakeRoleServicer
	permissions  []permission.Permission
	reconcileErr error
}

func (f *fakeRoleServicerWithPerms) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	return &role.Role{
		ID:          "r-by-name",
		Name:        name,
		TenantID:    tenantID,
		Permissions: []permission.Permission{"kv:read"},
		CreatedAt:   time.Unix(1700000000, 0),
		UpdatedAt:   time.Unix(1700000000, 0),
	}, nil
}

func (f *fakeRoleServicerWithPerms) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return f.permissions, nil
}

// startRoleResolveServer starts a gRPC test server with the role servicer that
// returns permissions for ResolvePermissions.
func startRoleResolveServer(t *testing.T) (string, func()) {
	t.Helper()
	svc := &fakeRoleServicerWithPerms{
		permissions: []permission.Permission{"kv:read", "kv:write"},
	}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil,
		grpcpkg.WithRegisterRoleService(svc),
	)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectRoleResolveTransport(t *testing.T, addr string) (*Transport, func()) {
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

// ---------------------------------------------------------------------------
// GetRoleByName
// ---------------------------------------------------------------------------

func TestGRPCGetRoleByName_Success(t *testing.T) {
	addr, stop := startRoleResolveServer(t)
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	rl, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "admin",
	})
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if rl.Name != "admin" {
		t.Errorf("Name = %q, want %q", rl.Name, "admin")
	}
	if rl.TenantID != "t1" {
		t.Errorf("TenantID = %q, want %q", rl.TenantID, "t1")
	}
}

func TestGRPCGetRoleByName_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "admin",
	})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ResolvePermissions
// ---------------------------------------------------------------------------

func TestGRPCResolvePermissions_Success(t *testing.T) {
	addr, stop := startRoleResolveServer(t)
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	resp, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1", "r2"},
	})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(resp.Permissions) != 2 {
		t.Fatalf("expected 2 permissions, got %d: %v", len(resp.Permissions), resp.Permissions)
	}
}

func TestGRPCResolvePermissions_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ReconcileBuiltInRoles returns the reconciliation fixture the round-trip test
// asserts on: one repaired role (added AND removed, the go-qrdb#354 shape) and
// one seeded role.
func (f *fakeRoleServicerWithPerms) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	if f.reconcileErr != nil {
		return nil, f.reconcileErr
	}
	return reconcileFixture(tenantID), nil
}

// reconcileFixture is the report every gRPC reconcile assertion is written
// against.
func reconcileFixture(tenantID string) *role.ReconcileReport {
	return &role.ReconcileReport{
		TenantID: tenantID,
		Roles: []role.RoleReconcileResult{
			{
				RoleID:  "r-admin",
				Name:    role.RoleNameAdmin,
				Added:   []permission.Permission{"sdlc:bind"},
				Removed: []permission.Permission{"sdlc:register"},
			},
			{
				RoleID:  "r-operator",
				Name:    role.RoleNameOperator,
				Created: true,
				Added:   []permission.Permission{"orchestrator:task_block"},
			},
		},
	}
}
