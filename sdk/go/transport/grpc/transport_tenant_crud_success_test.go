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
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func startTenantCRUDServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	tenantSvc := &fakeTenantServicer{}
	grpcpkg.RegisterService(srv, nil, nil,
		grpcpkg.WithRegisterTenantService(tenantSvc),
		grpcpkg.WithRegisterTenantOnboarding(onboardingFromTenant(tenantSvc)),
	)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectTenantCRUDTransport(t *testing.T, addr string) (*Transport, func()) {
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

func TestGRPCTenant_CRUDMethods_Success(t *testing.T) {
	addr, stop := startTenantCRUDServer(t)
	defer stop()
	tr, cleanup := connectTenantCRUDTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("CreateTenant", func(t *testing.T) {
		resp, err := tr.CreateTenant(ctx, &transport.CreateTenantRequest{ID: "t1", Name: "test"})
		if err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}
		if resp.Tenant.ID != "t1" {
			t.Fatalf("ID = %q", resp.Tenant.ID)
		}
	})
	t.Run("GetTenant", func(t *testing.T) {
		resp, err := tr.GetTenant(ctx, "t1")
		if err != nil {
			t.Fatalf("GetTenant: %v", err)
		}
		if resp.Tenant.ID != "t1" {
			t.Fatalf("ID = %q", resp.Tenant.ID)
		}
	})
	t.Run("ListTenants", func(t *testing.T) {
		if _, err := tr.ListTenants(ctx); err != nil {
			t.Fatalf("ListTenants: %v", err)
		}
	})
	t.Run("DeleteTenant", func(t *testing.T) {
		if err := tr.DeleteTenant(ctx, "t1"); err != nil {
			t.Fatalf("DeleteTenant: %v", err)
		}
	})
	t.Run("UpdateTenant", func(t *testing.T) {
		if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "t1"}); err != nil {
			t.Fatalf("UpdateTenant: %v", err)
		}
	})
	t.Run("UpdateTenantQuotas", func(t *testing.T) {
		if _, err := tr.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{TenantID: "t1"}); err != nil {
			t.Fatalf("UpdateTenantQuotas: %v", err)
		}
	})
}

// TestGRPCTenant_UpdateTenantEntitlements_NilRequest refuses a nil request
// with a typed error rather than dereferencing it.
func TestGRPCTenant_UpdateTenantEntitlements_NilRequest(t *testing.T) {
	addr, stop := startTenantCRUDServer(t)
	defer stop()
	tr, cleanup := connectTenantCRUDTransport(t, addr)
	defer cleanup()

	_, err := tr.UpdateTenantEntitlements(context.Background(), nil)
	var nre *transport.NilRequestError
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != "UpdateTenantEntitlements" || !errors.As(err, &nre) {
		t.Fatalf("UpdateTenantEntitlements(nil) = %T %v, want RequestError wrapping NilRequestError", err, err)
	}
}
