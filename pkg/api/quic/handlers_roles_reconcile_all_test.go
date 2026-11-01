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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// liveQUICTenants is a tenant service holding the given live tenants.
func liveQUICTenants(t *testing.T, ids ...string) tenant.TenantServicer {
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

func newReconcileAllServer(t *testing.T, rs role.RoleServicer, ids ...string) *Server {
	t.Helper()
	return NewServer(nil, nil, "localhost:0", WithRoleService(rs), WithTenantService(liveQUICTenants(t, ids...)))
}

// TestHandleReconcileRolesAllTenants_ReportsEveryTenant decodes the protobuf
// report: each live tenant is converged, the failing one is listed, and the
// pass does not stop at it.
func TestHandleReconcileRolesAllTenants_ReportsEveryTenant(t *testing.T) {
	mock := &mockRoleService{
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
	srv := newReconcileAllServer(t, mock, "acme", "broken", "zeta")

	raw, err := srv.handleReconcileRolesAllTenants(tenant.WithTenantID(context.Background(), tenantid.System), nil)
	if err != nil {
		t.Fatalf("handleReconcileRolesAllTenants: %v", err)
	}
	var resp reconcileRolesAllTenantsWireResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tr := range resp.Tenants {
		seen[tr.TenantID] = true
		if len(tr.Roles) != 1 || tr.Roles[0].Removed[0] != "certs:generate" {
			t.Fatalf("tenant %s roles = %+v; want the admin change", tr.TenantID, tr.Roles)
		}
	}
	if !seen["acme"] || !seen["zeta"] {
		t.Fatalf("tenants = %+v; want acme and zeta converged", resp.Tenants)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].TenantID != "broken" {
		t.Fatalf("failed = %+v; want broken", resp.Failed)
	}
}

func TestAllTenantsReconcileReportToWire_CarriesEveryList(t *testing.T) {
	out := allTenantsReconcileReportToWire(&role.AllTenantsReconcileReport{
		Tenants: []role.ReconcileReport{{TenantID: "acme"}},
		Skipped: []role.SkippedTenant{{TenantID: "vault", Reason: role.SkipReasonSealed, Detail: "sealed"}},
		Failed:  []role.FailedTenant{{TenantID: "broken", Error: "boom"}},
	})
	if len(out.Tenants) != 1 || out.Tenants[0].TenantID != "acme" {
		t.Fatalf("tenants = %+v; want acme", out.Tenants)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Reason != role.SkipReasonSealed {
		t.Fatalf("skipped = %+v; want vault sealed", out.Skipped)
	}
	if len(out.Failed) != 1 || out.Failed[0].Error != "boom" {
		t.Fatalf("failed = %+v; want broken", out.Failed)
	}
}

func TestAllTenantsReconcileReportToWire_NilIsEmpty(t *testing.T) {
	out := allTenantsReconcileReportToWire(nil)
	if out.Tenants != nil || out.Skipped != nil || out.Failed != nil {
		t.Fatalf("nil report = %+v; want empty", out)
	}
}

// TestHandleReconcileRolesAllTenants_RefusesTenantCaller: the verb is a
// platform operator's; a tenant's caller is refused.
func TestHandleReconcileRolesAllTenants_RefusesTenantCaller(t *testing.T) {
	srv := newReconcileAllServer(t, &mockRoleService{}, "acme")

	_, err := srv.handleReconcileRolesAllTenants(tenant.WithTenantID(context.Background(), "acme"), nil)
	var refused *tenant.PlatformOperatorRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v; want PlatformOperatorRequiredError", err)
	}
}

func TestHandleReconcileRolesAllTenants_Unconfigured(t *testing.T) {
	for name, srv := range map[string]*Server{
		"no role service":   NewServer(nil, nil, "localhost:0", WithTenantService(liveQUICTenants(t))),
		"no tenant service": newRoleServer(&mockRoleService{}, nil),
	} {
		_, err := srv.handleReconcileRolesAllTenants(context.Background(), nil)
		var notConfigured *ServiceNotConfiguredError
		if !errors.As(err, &notConfigured) {
			t.Errorf("%s: err = %v; want ServiceNotConfiguredError", name, err)
		}
	}
}
