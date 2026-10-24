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
	"log/slog"
	"maps"
	"net"
	"strings"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// connectDescriptiveTenants connects to a gRPC server over the REAL
// seal-disabled tenant service holding one tenant, "acme".
func connectDescriptiveTenants(t *testing.T) *Transport {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterTenantService(svc))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	tr, cleanup := connectTenantCRUDTransport(t, lis.Addr().String())
	t.Cleanup(cleanup)
	return tr
}

func TestGRPCTenant_UpdateTenant_DescriptionAndMetadataRoundTrip(t *testing.T) {
	tr := connectDescriptiveTenants(t)
	ctx := context.Background()
	desc := "Acme production"
	want := map[string]string{"env": "prod", "team": "storage"}

	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Description: &desc, Metadata: want}); err != nil {
		t.Fatal(err)
	}
	got, err := tr.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tenant.Description != desc || !maps.Equal(got.Tenant.Metadata, want) {
		t.Fatalf("get = %q, %v; want %q, %v", got.Tenant.Description, got.Tenant.Metadata, desc, want)
	}

	// Nil fields stay absent on the wire, so the stored values stay.
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Name: "Acme Inc"}); err != nil {
		t.Fatal(err)
	}
	got, err = tr.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tenant.Description != desc || !maps.Equal(got.Tenant.Metadata, want) {
		t.Fatalf("rename changed descriptive fields: %q, %v", got.Tenant.Description, got.Tenant.Metadata)
	}
}

func TestGRPCTenant_UpdateTenant_EmptyMetadataClears(t *testing.T) {
	tr := connectDescriptiveTenants(t)
	ctx := context.Background()
	desc := "to clear"
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{
		TenantID: "acme", Description: &desc, Metadata: map[string]string{"env": "prod"},
	}); err != nil {
		t.Fatal(err)
	}

	empty := ""
	resp, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Description: &empty, Metadata: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Tenant.Description != "" || len(resp.Tenant.Metadata) != 0 {
		t.Fatalf("cleared tenant = %q, %v; want both empty", resp.Tenant.Description, resp.Tenant.Metadata)
	}
}

func TestGRPCTenant_UpdateTenant_OverLimitIsInvalidArgument(t *testing.T) {
	tr := connectDescriptiveTenants(t)
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{
		TenantID: "acme",
		Metadata: map[string]string{strings.Repeat("k", tenant.MaxMetadataKeyBytes+1): "v"},
	})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.InvalidArgument) {
		t.Fatalf("over-limit metadata key = %v; want a RequestError carrying InvalidArgument", err)
	}
}
