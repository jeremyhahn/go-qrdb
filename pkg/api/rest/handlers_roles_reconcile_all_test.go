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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// liveRESTTenants is a tenant service holding the given live tenants.
func liveRESTTenants(t *testing.T, ids ...string) tenant.TenantServicer {
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

// reconcileAllRequest is POST /roles/reconcile/all-tenants as callerTenant.
func reconcileAllRequest(callerTenant string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/roles/reconcile/all-tenants", http.NoBody)
	return req.WithContext(tenant.WithTenantID(req.Context(), callerTenant))
}

// TestHandleReconcileRolesAllTenants_ReportsEveryTenant decodes the JSON body:
// every live tenant is converged with its changes in snake_case, and the
// failing tenant is listed without failing the request.
func TestHandleReconcileRolesAllTenants_ReportsEveryTenant(t *testing.T) {
	roleSvc := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			if tenantID == "broken" {
				return nil, &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")}
			}
			return &role.ReconcileReport{TenantID: tenantID, Roles: []role.RoleReconcileResult{{
				RoleID: "r-admin", Name: role.RoleNameAdmin,
				Added:   []permission.Permission{"certs:issue"},
				Removed: []permission.Permission{"certs:generate"},
			}}}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithRoleService(roleSvc), WithTenantService(liveRESTTenants(t, "acme", "broken")))

	rec := httptest.NewRecorder()
	srv.handleReconcileRolesAllTenants(rec, reconcileAllRequest(tenantid.System))
	assertStatus(t, rec, http.StatusOK)

	var body struct {
		Tenants []struct {
			TenantID string `json:"tenant_id"`
			Roles    []struct {
				Removed []string `json:"removed"`
			} `json:"roles"`
		} `json:"tenants"`
		Failed []struct {
			TenantID string `json:"tenant_id"`
			Error    string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var acme bool
	for _, tr := range body.Tenants {
		if tr.TenantID == "acme" && len(tr.Roles) == 1 && tr.Roles[0].Removed[0] == "certs:generate" {
			acme = true
		}
	}
	if !acme {
		t.Fatalf("tenants = %+v; want acme with the admin change", body.Tenants)
	}
	if len(body.Failed) != 1 || body.Failed[0].TenantID != "broken" || body.Failed[0].Error == "" {
		t.Fatalf("failed = %+v; want broken with its error", body.Failed)
	}
}

// TestHandleReconcileRolesAllTenants_RefusesTenantCaller: a tenant's caller
// is refused with 403.
func TestHandleReconcileRolesAllTenants_RefusesTenantCaller(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithRoleService(&mockRoleService{}), WithTenantService(liveRESTTenants(t, "acme")))

	rec := httptest.NewRecorder()
	srv.handleReconcileRolesAllTenants(rec, reconcileAllRequest("acme"))
	assertStatus(t, rec, http.StatusForbidden)
}

// TestHandleReconcileRolesAllTenants_Unconfigured routes through dispatch and
// answers 501 when either service the verb needs is not wired.
func TestHandleReconcileRolesAllTenants_Unconfigured(t *testing.T) {
	for name, srv := range map[string]*Server{
		"no role service":   NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTenantService(liveRESTTenants(t))),
		"no tenant service": newAuthServer(nil, &mockRoleService{}, nil),
	} {
		rec := httptest.NewRecorder()
		srv.dispatch(rec, reconcileAllRequest(tenantid.System))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: status = %d; want 501", name, rec.Code)
		}
	}
}

func TestAllTenantsReconcileReportToResponse_CarriesEveryList(t *testing.T) {
	if out := allTenantsReconcileReportToResponse(nil); out.Tenants != nil || out.Failed != nil {
		t.Fatalf("nil report = %+v; want empty", out)
	}
	out := allTenantsReconcileReportToResponse(&role.AllTenantsReconcileReport{
		Tenants: []role.ReconcileReport{{TenantID: "acme"}},
		Skipped: []role.SkippedTenant{{TenantID: "vault", Reason: role.SkipReasonSealed, Detail: "sealed"}},
		Failed:  []role.FailedTenant{{TenantID: "broken", Error: "boom"}},
	})
	if out.Tenants[0].TenantID != "acme" || out.Skipped[0].Reason != role.SkipReasonSealed ||
		out.Skipped[0].Detail != "sealed" || out.Failed[0].Error != "boom" {
		t.Fatalf("response = %+v; want every list carried", out)
	}
}
