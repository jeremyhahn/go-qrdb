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

package embedded

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// liveEmbeddedTenants returns the REAL seal-disabled tenant service with the
// named tenants created through it.
func liveEmbeddedTenants(t *testing.T, ids ...string) tenant.TenantServicer {
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

// TestReconcileBuiltInRolesAllTenants_ReportsEveryTenant: each live tenant's
// report reaches the caller with its changes, and a failing tenant is listed
// without stopping the pass.
func TestReconcileBuiltInRolesAllTenants_ReportsEveryTenant(t *testing.T) {
	mock := &mockRoleServicer{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			if tenantID == "broken" {
				return nil, &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")}
			}
			return reconcileFixture(tenantID), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock), WithTenantService(liveEmbeddedTenants(t, "acme", "broken")))

	resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	var acme *transport.ReconcileRolesResponse
	for i := range resp.Tenants {
		if resp.Tenants[i].TenantID == "acme" {
			acme = &resp.Tenants[i]
		}
	}
	if acme == nil || len(acme.Roles) != 2 || acme.Roles[0].Removed[0] != "sdlc:register" || !acme.Roles[1].Created {
		t.Fatalf("tenants = %+v; want acme with the fixture's changes", resp.Tenants)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].TenantID != "broken" || resp.Failed[0].Error == "" {
		t.Fatalf("failed = %+v; want broken with its error", resp.Failed)
	}
}

// TestReconcileBuiltInRolesAllTenants_RefusesTenantCaller: an embedding node
// acting for one tenant cannot reach every tenant.
func TestReconcileBuiltInRolesAllTenants_RefusesTenantCaller(t *testing.T) {
	tr := newAuthTransport(t, WithRoleService(&mockRoleServicer{}), WithTenantService(liveEmbeddedTenants(t, "acme")))

	_, err := tr.ReconcileBuiltInRolesAllTenants(tenant.WithTenantID(context.Background(), "acme"))
	var refused *tenant.PlatformOperatorRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v; want PlatformOperatorRequiredError", err)
	}
}

func TestReconcileBuiltInRolesAllTenants_Unconfigured(t *testing.T) {
	tr := newAuthTransport(t, WithTenantService(liveEmbeddedTenants(t)))
	var notImpl *transport.NotImplementedError
	if _, err := tr.ReconcileBuiltInRolesAllTenants(context.Background()); !errors.As(err, &notImpl) {
		t.Fatalf("no role service: err = %v; want NotImplementedError", err)
	}

	tr = newAuthTransport(t, WithRoleService(&mockRoleServicer{}))
	var nilSvc *NilServiceError
	if _, err := tr.ReconcileBuiltInRolesAllTenants(context.Background()); !errors.As(err, &nilSvc) {
		t.Fatalf("no tenant service: err = %v; want NilServiceError", err)
	}
}
