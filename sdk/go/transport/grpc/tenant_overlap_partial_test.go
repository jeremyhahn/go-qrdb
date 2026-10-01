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
	"strings"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// partialReconcileSvc answers ReconcileDeletedTenants as the real service does
// when one retired id failed and another was reclaimed: the report, whose
// Failed list copies the aggregate error returned with it (pinned in
// pkg/tenant). CreateTenant answers with createErr when it is set.
type partialReconcileSvc struct {
	tenant.TenantServicer
	createErr error
}

func (p partialReconcileSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	boom := errors.New("device busy")
	return &tenant.ReconcileReport{
			Inspected: 2,
			Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
			Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: boom.Error()}},
		}, &tenant.ResidueReconcileFailuresError{Errors: []*tenant.ResidueReconcileError{
			{TenantID: "va", Op: "remove", Path: "/srv/va", Err: boom},
		}}
}

func (p partialReconcileSvc) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, opts ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if p.createErr != nil {
		return nil, p.createErr
	}
	return p.TenantServicer.CreateTenant(ctx, id, name, iso, opts...)
}

// retiredOverlap is the refusal CreateTenant returns for a DataRoot a retired
// tenant's unreclaimed record still names.
func retiredOverlap() *tenant.DataRootOverlapError {
	return &tenant.DataRootOverlapError{
		DataRoot: "/srv/disk", Conflict: "/srv/disk", Kind: "retired tenant vgone",
		Remedy: "it is retired-tenant residue on this node; run `qrdb tenant reconcile-deleted` on this node to reclaim it",
	}
}

// assertPartial checks a client saw both halves of a partly failed pass.
func assertPartial(t *testing.T, resp *transport.ReconcileDeletedTenantsResponse, err error) *transport.ReconcileDeletedTenantsError {
	t.Helper()
	if resp == nil || resp.Inspected != 2 || len(resp.Reclaimed) != 1 || resp.Reclaimed[0].TenantID != "vb" {
		t.Fatalf("resp = %+v; want vb reclaimed out of 2", resp)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].TenantID != "va" || resp.Failed[0].Op != "remove" ||
		resp.Failed[0].Path != "/srv/va" || resp.Failed[0].Error != "device busy" {
		t.Fatalf("failed = %+v; want va's removal failure", resp.Failed)
	}
	var re *transport.ReconcileDeletedTenantsError
	if !errors.As(err, &re) || len(re.Failed) != 1 || re.Failed[0].TenantID != "va" {
		t.Fatalf("err = %v; want *transport.ReconcileDeletedTenantsError naming va", err)
	}
	return re
}

// startOverlapPartialServer serves the REAL gRPC tenant service over svc.
func startOverlapPartialServer(t *testing.T, svc tenant.TenantServicer) *Transport {
	t.Helper()
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

// TestGRPCTenant_CreateTenant_DataRootOverlapIsAFailedPrecondition: over the
// wire the refusal is FailedPrecondition and keeps its remedy.
func TestGRPCTenant_CreateTenant_DataRootOverlapIsAFailedPrecondition(t *testing.T) {
	tr := startOverlapPartialServer(t, partialReconcileSvc{TenantServicer: &fakeTenantServicer{}, createErr: retiredOverlap()})
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "vnew", Name: "New"})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.FailedPrecondition) || !strings.Contains(err.Error(), "qrdb tenant reconcile-deleted") {
		t.Fatalf("CreateTenant = %v; want FailedPrecondition carrying the remedy", err)
	}
}

// TestGRPCTenant_ReconcileDeletedTenants_PartialFailureCarriesBoth: the
// response carries the report and the failures, and the client returns both.
func TestGRPCTenant_ReconcileDeletedTenants_PartialFailureCarriesBoth(t *testing.T) {
	tr := startOverlapPartialServer(t, partialReconcileSvc{TenantServicer: &fakeTenantServicer{}})
	resp, err := tr.ReconcileDeletedTenants(context.Background())
	if re := assertPartial(t, resp, err); re.Err != nil {
		t.Fatalf("a network transport has no node-side error to wrap; got %v", re.Err)
	}
}
