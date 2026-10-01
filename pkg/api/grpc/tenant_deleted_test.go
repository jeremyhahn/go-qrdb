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
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// retiredGRPCTenants returns the REAL seal-disabled tenant service with the
// named tenants created and deleted through it.
func retiredGRPCTenants(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

// failingDeletedSvc is the real service with the two retired-id verbs failing.
type failingDeletedSvc struct {
	tenant.TenantServicer
	report *tenant.ReconcileReport
	err    error
}

func (f failingDeletedSvc) ListDeletedTenants(context.Context, string, int) (*tenant.DeletedTenantsPage, error) {
	return nil, f.err
}

func (f failingDeletedSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	return f.report, f.err
}

func TestGRPC_TenantListDeleted_PagesThroughRetiredIds(t *testing.T) {
	s := &qrdbServiceServer{tenantSvc: retiredGRPCTenants(t, "bravo", "alpha", "charlie")}
	ctx := context.Background()

	first, err := s.TenantListDeleted(ctx, &pb.TenantListDeletedRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("TenantListDeleted: %v", err)
	}
	if len(first.GetTenants()) != 2 || first.GetTenants()[0].GetId() != "alpha" || first.GetNextPageToken() == "" {
		t.Fatalf("first page = %v", first)
	}
	if _, perr := time.Parse(time.RFC3339, first.GetTenants()[0].GetDeletedAt()); perr != nil {
		t.Fatalf("deleted_at %q is not RFC 3339: %v", first.GetTenants()[0].GetDeletedAt(), perr)
	}
	second, err := s.TenantListDeleted(ctx, &pb.TenantListDeletedRequest{PageSize: 2, PageToken: first.GetNextPageToken()})
	if err != nil {
		t.Fatalf("TenantListDeleted (page 2): %v", err)
	}
	if len(second.GetTenants()) != 1 || second.GetTenants()[0].GetId() != "charlie" || second.GetNextPageToken() != "" {
		t.Fatalf("second page = %v", second)
	}
}

func TestGRPC_TenantListDeleted_BadPageSizeIsInvalidArgument(t *testing.T) {
	s := &qrdbServiceServer{tenantSvc: retiredGRPCTenants(t)}
	_, err := s.TenantListDeleted(context.Background(), &pb.TenantListDeletedRequest{PageSize: -1})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s; want InvalidArgument (%v)", status.Code(err), err)
	}
}

func TestGRPC_TenantDeletedVerbs_NilServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}
	if _, err := s.TenantListDeleted(context.Background(), &pb.TenantListDeletedRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("TenantListDeleted code = %s; want Unimplemented", status.Code(err))
	}
	if _, err := s.TenantReconcileDeleted(context.Background(), &emptypb.Empty{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("TenantReconcileDeleted code = %s; want Unimplemented", status.Code(err))
	}
}

func TestGRPC_TenantReconcileDeleted_ReportsAndMapsErrors(t *testing.T) {
	s := &qrdbServiceServer{tenantSvc: retiredGRPCTenants(t, "alpha", "bravo")}
	resp, err := s.TenantReconcileDeleted(context.Background(), &emptypb.Empty{})
	if err != nil || resp.GetInspected() != 2 {
		t.Fatalf("TenantReconcileDeleted = %v, %v; want 2 inspected", resp, err)
	}

	boom := &tenant.ResidueReconcileError{TenantID: "x", Op: "remove", Err: errors.New("busy")}
	s = &qrdbServiceServer{tenantSvc: failingDeletedSvc{err: boom}}
	if _, err := s.TenantReconcileDeleted(context.Background(), &emptypb.Empty{}); status.Code(err) != codes.Internal {
		t.Fatalf("code = %s; want Internal", status.Code(err))
	}
	if _, err := s.TenantListDeleted(context.Background(), &pb.TenantListDeletedRequest{}); status.Code(err) != codes.Internal {
		t.Fatalf("code = %s; want Internal", status.Code(err))
	}
}

func TestReconcileReportToProto_CarriesEveryPath(t *testing.T) {
	got := ReconcileReportToProto(&tenant.ReconcileReport{
		Inspected: 3,
		Reclaimed: []tenant.ReclaimedPath{{TenantID: "a", Path: "/p/a"}},
		Skipped:   []tenant.SkippedPath{{TenantID: "b", Path: "/p/b", Reason: tenant.SkipOverlapsLiveTenant}},
	})
	if got.GetInspected() != 3 || len(got.GetReclaimed()) != 1 || got.GetReclaimed()[0].GetPath() != "/p/a" ||
		len(got.GetSkipped()) != 1 || got.GetSkipped()[0].GetReason() != tenant.SkipOverlapsLiveTenant {
		t.Fatalf("ReconcileReportToProto = %v", got)
	}
	if ReconcileReportToProto(nil).GetInspected() != 0 || len(DeletedTenantsPageToProto(nil).GetTenants()) != 0 {
		t.Fatal("nil input must convert to an empty message")
	}
	page := DeletedTenantsPageToProto(&tenant.DeletedTenantsPage{Tombstones: []tenant.Tombstone{{ID: "undated"}}})
	if page.GetTenants()[0].GetDeletedAt() != "" || page.GetTenants()[0].GetCreatedAt() != "" {
		t.Fatalf("a zero time must be sent as absent, got %v", page.GetTenants()[0])
	}
}
