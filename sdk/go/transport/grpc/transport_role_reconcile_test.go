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

	googlegrpc "google.golang.org/grpc"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// startRoleReconcileServer starts a gRPC test server whose role servicer fails
// reconciliation, for the error-path assertion.
func startFailingRoleReconcileServer(t *testing.T) (string, func()) {
	t.Helper()
	svc := &fakeRoleServicerWithPerms{
		permissions:  []permission.Permission{"kv:read"},
		reconcileErr: errors.New("backend unavailable"),
	}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterRoleService(svc))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

// TestGRPCReconcileBuiltInRoles_RoundTrip proves the report survives the proto
// wire intact — a report that lost its Added/Removed lists would still look
// like a success to a caller and tell an operator nothing about what changed.
func TestGRPCReconcileBuiltInRoles_RoundTrip(t *testing.T) {
	addr, stop := startRoleResolveServer(t)
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	assertReconcileFixture(t, resp)
}

// TestGRPCReconcileBuiltInRoles_NilRequest covers the "my own tenant" call.
func TestGRPCReconcileBuiltInRoles_NilRequest(t *testing.T) {
	addr, stop := startRoleResolveServer(t)
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), nil)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles(nil): %v", err)
	}
	if resp.TenantID != "" {
		t.Errorf("TenantID = %q, want empty", resp.TenantID)
	}
}

// TestGRPCReconcileBuiltInRoles_ServiceError checks the failure path surfaces
// rather than reporting an empty (and therefore "already converged") report.
func TestGRPCReconcileBuiltInRoles_ServiceError(t *testing.T) {
	addr, stop := startFailingRoleReconcileServer(t)
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err == nil {
		t.Fatalf("ReconcileBuiltInRoles succeeded against a failing service: %+v", resp)
	}
}

func TestGRPCReconcileBuiltInRoles_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// TestGRPCProtoToReconcileReport_Nil covers the nil guard in the converter.
func TestGRPCProtoToReconcileReport_Nil(t *testing.T) {
	if got := protoToReconcileReport(nil); got != nil {
		t.Errorf("protoToReconcileReport(nil) = %+v, want nil", got)
	}
}

// assertReconcileFixture asserts a transport report matches the fixture every
// transport's round-trip test is written against.
func assertReconcileFixture(t *testing.T, resp *transport.ReconcileRolesResponse) {
	t.Helper()
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("Roles len = %d, want 2: %+v", len(resp.Roles), resp.Roles)
	}

	admin := resp.Roles[0]
	if admin.Name != "admin" || admin.RoleID != "r-admin" {
		t.Errorf("role[0] = %s/%s, want admin/r-admin", admin.Name, admin.RoleID)
	}
	if admin.Created {
		t.Error("admin reported as created")
	}
	if len(admin.Added) != 1 || admin.Added[0] != "sdlc:bind" {
		t.Errorf("admin added = %v, want [sdlc:bind]", admin.Added)
	}
	if len(admin.Removed) != 1 || admin.Removed[0] != "sdlc:register" {
		t.Errorf("admin removed = %v, want [sdlc:register]", admin.Removed)
	}

	operator := resp.Roles[1]
	if operator.Name != "operator" || !operator.Created {
		t.Errorf("role[1] = %s created=%v, want operator created=true", operator.Name, operator.Created)
	}
	if len(operator.Removed) != 0 {
		t.Errorf("operator removed = %v, want none", operator.Removed)
	}
}
