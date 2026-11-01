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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// liveGRPCTenants is a tenant service holding the given live tenants.
func liveGRPCTenants(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := svc.CreateTenant(context.Background(), id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

func systemCaller() context.Context {
	return tenant.WithTenantID(context.Background(), tenantid.System)
}

// TestGRPC_ReconcileBuiltInRolesAllTenants_ReportsEveryTenant: every live
// tenant crosses the proto boundary with its own changes.
func TestGRPC_ReconcileBuiltInRolesAllTenants_ReportsEveryTenant(t *testing.T) {
	roleSvc := &mockRoleService{reconcileResult: &role.ReconcileReport{
		TenantID: "acme",
		Roles: []role.RoleReconcileResult{{
			RoleID: "r-admin", Name: role.RoleNameAdmin,
			Added:   []permission.Permission{"certs:issue"},
			Removed: []permission.Permission{"certs:generate"},
		}},
	}}
	s := &qrdbServiceServer{roleSvc: roleSvc, tenantSvc: liveGRPCTenants(t, "acme", "beta")}

	resp, err := s.ReconcileBuiltInRolesAllTenants(systemCaller(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	if len(resp.GetTenants()) < 2 || len(resp.GetFailed()) != 0 {
		t.Fatalf("report = %v; want every tenant converged, none failed", resp)
	}
	got := resp.GetTenants()[0].GetRoles()
	if len(got) != 1 || got[0].GetRemoved()[0] != "certs:generate" || got[0].GetAdded()[0] != "certs:issue" {
		t.Fatalf("roles = %v; want the admin change", got)
	}
}

// TestGRPC_ReconcileBuiltInRolesAllTenants_FailedTenantIsReported: one
// tenant's failure is a partial pass in the response, not an RPC error.
func TestGRPC_ReconcileBuiltInRolesAllTenants_FailedTenantIsReported(t *testing.T) {
	roleSvc := &mockRoleService{reconcileErr: &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")}}
	s := &qrdbServiceServer{roleSvc: roleSvc, tenantSvc: liveGRPCTenants(t, "acme")}

	resp, err := s.ReconcileBuiltInRolesAllTenants(systemCaller(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	if len(resp.GetFailed()) == 0 || resp.GetFailed()[0].GetError() == "" {
		t.Fatalf("failed = %v; want the failing tenants with their errors", resp.GetFailed())
	}
}

// TestGRPC_ReconcileBuiltInRolesAllTenants_RefusesTenantCaller: a caller
// outside the system tenant is refused with PermissionDenied.
func TestGRPC_ReconcileBuiltInRolesAllTenants_RefusesTenantCaller(t *testing.T) {
	s := &qrdbServiceServer{roleSvc: &mockRoleService{}, tenantSvc: liveGRPCTenants(t, "acme")}

	ctx := tenant.WithTenantID(context.Background(), "acme")
	if _, err := s.ReconcileBuiltInRolesAllTenants(ctx, &emptypb.Empty{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %s; want PermissionDenied", status.Code(err))
	}
}

// TestGRPC_ReconcileBuiltInRolesAllTenants_Unconfigured reports Unimplemented
// when either service the verb needs is not wired.
func TestGRPC_ReconcileBuiltInRolesAllTenants_Unconfigured(t *testing.T) {
	for name, s := range map[string]*qrdbServiceServer{
		"no role service":   {tenantSvc: liveGRPCTenants(t)},
		"no tenant service": {roleSvc: &mockRoleService{}},
	} {
		if _, err := s.ReconcileBuiltInRolesAllTenants(systemCaller(), &emptypb.Empty{}); status.Code(err) != codes.Unimplemented {
			t.Errorf("%s: code = %s; want Unimplemented", name, status.Code(err))
		}
	}
}

func TestAllTenantsReconcileReportToProto_CarriesEveryList(t *testing.T) {
	if out := AllTenantsReconcileReportToProto(nil); out == nil || len(out.GetTenants()) != 0 {
		t.Fatalf("nil report = %v; want an empty response", out)
	}
	out := AllTenantsReconcileReportToProto(&role.AllTenantsReconcileReport{
		Tenants: []role.ReconcileReport{{TenantID: "acme"}},
		Skipped: []role.SkippedTenant{{TenantID: "vault", Reason: role.SkipReasonSealed, Detail: "sealed"}},
		Failed:  []role.FailedTenant{{TenantID: "broken", Error: "boom"}},
	})
	if out.GetTenants()[0].GetTenantId() != "acme" ||
		out.GetSkipped()[0].GetReason() != role.SkipReasonSealed || out.GetSkipped()[0].GetDetail() != "sealed" ||
		out.GetFailed()[0].GetTenantId() != "broken" || out.GetFailed()[0].GetError() != "boom" {
		t.Fatalf("proto = %v; want every list carried", out)
	}
}
