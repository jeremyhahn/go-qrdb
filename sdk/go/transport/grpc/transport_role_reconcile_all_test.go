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

package grpc

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// startReconcileAllServer starts a gRPC test server with the role servicer and
// a tenant service holding tenant t1. A server with no identity interceptor
// serves the system tenant, the platform operator the verb requires.
func startReconcileAllServer(t *testing.T, reconcileErr error) (string, func()) {
	t.Helper()
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenants.CreateTenant(context.Background(), "t1", "Tenant t1", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	svc := &fakeRoleServicerWithPerms{permissions: []permission.Permission{"kv:read"}, reconcileErr: reconcileErr}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterRoleService(svc), grpcpkg.WithRegisterTenantService(tenants))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

// TestGRPCReconcileBuiltInRolesAllTenants_RoundTrip proves each tenant's
// report survives the proto wire intact.
func TestGRPCReconcileBuiltInRolesAllTenants_RoundTrip(t *testing.T) {
	addr, stop := startReconcileAllServer(t, nil)
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	for i := range resp.Tenants {
		if resp.Tenants[i].TenantID == "t1" {
			assertReconcileFixture(t, &resp.Tenants[i])
			return
		}
	}
	t.Fatalf("tenants = %+v; want t1", resp.Tenants)
}

// TestGRPCReconcileBuiltInRolesAllTenants_FailedTenants: a failing tenant is
// a partial pass carried in Failed, not an RPC error.
func TestGRPCReconcileBuiltInRolesAllTenants_FailedTenants(t *testing.T) {
	addr, stop := startReconcileAllServer(t, errors.New("backend unavailable"))
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	if len(resp.Failed) == 0 || resp.Failed[0].Error == "" {
		t.Fatalf("failed = %+v; want the failing tenants with their errors", resp.Failed)
	}
}

// TestGRPCReconcileBuiltInRolesAllTenants_Unconfigured maps the server's
// Unimplemented answer to an error.
func TestGRPCReconcileBuiltInRolesAllTenants_Unconfigured(t *testing.T) {
	addr, stop := startRoleResolveServer(t)
	defer stop()
	tr, cleanup := connectRoleResolveTransport(t, addr)
	defer cleanup()

	if _, err := tr.ReconcileBuiltInRolesAllTenants(context.Background()); err == nil {
		t.Fatal("expected an error from a server without a tenant service")
	}
}

func TestGRPCReconcileBuiltInRolesAllTenants_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
