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
	"log/slog"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestTenantUpdate_MixedStatusAndIsolationPolicyRefusedWithoutTargetUpdate
// drives PUT /v1/tenants/{id} through the full chain, with strict isolation
// active, as a system principal holding tenants:update (its own tenant) and
// tenants:set_status but not tenants:update:cross. A status field in the
// body must not lift the target tenants:update check an isolation-policy
// change takes (Codex P1, #1423).
func TestTenantUpdate_MixedStatusAndIsolationPolicyRefusedWithoutTargetUpdate(t *testing.T) {
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acme", "globex"} {
		if _, err := tenants.CreateTenant(t.Context(), id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	tenants.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })

	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	r, err := roles.CreateRole(t.Context(), &role.CreateRoleRequest{
		TenantID:    tenantid.System,
		Name:        "status-operator",
		Permissions: []permission.Permission{permission.TenantsUpdate, permission.TenantsSetStatus},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	caller := authtest.NewFixedAuthenticator(&auth.Identity{Subject: "operator", TenantID: tenantid.System, Roles: []string{r.ID}})
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(caller))
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithTenantService(tenants),
		WithAuthMiddleware(mw),
	)
	f := &isolationFixture{srv: srv, caller: caller}

	if rec := f.do(http.MethodPut, "/v1/tenants/acme", `{"status":"active","isolation_policy":"strict"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("status+isolation_policy on acme = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	got, err := tenants.GetTenant(t.Context(), "acme")
	if err != nil {
		t.Fatalf("GetTenant(acme): %v", err)
	}
	if got.IsolationPolicy == tenant.TenantIsolationPolicyStrict {
		t.Fatalf("a refused update changed acme's isolation policy to %q", got.IsolationPolicy)
	}

	// An isolation-policy-only change on acme by a principal holding
	// tenants:update:cross still succeeds.
	cross, err := roles.CreateRole(t.Context(), &role.CreateRoleRequest{
		TenantID:    tenantid.System,
		Name:        "policy-operator",
		Permissions: []permission.Permission{permission.TenantsUpdate, permission.TenantsUpdateCross},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	f.as(&auth.Identity{Subject: "policy-operator", TenantID: tenantid.System, Roles: []string{cross.ID}})
	if rec := f.do(http.MethodPut, "/v1/tenants/acme", `{"isolation_policy":"strict"}`); rec.Code != http.StatusOK {
		t.Fatalf("isolation_policy on acme with tenants:update:cross = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	got, err = tenants.GetTenant(t.Context(), "acme")
	if err != nil {
		t.Fatalf("GetTenant(acme): %v", err)
	}
	if got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("acme isolation policy = %q, want strict", got.IsolationPolicy)
	}
}
