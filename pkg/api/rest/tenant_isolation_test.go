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

// Tenant isolation on the tenant-administration surface, end to end.
//
// Every test drives the FULL middleware chain (srv.server.Handler) with a REAL
// rbac.RBACAuthorizer over the REAL built-in roles and a REAL in-memory tenant
// service. The only double is the authenticator, which stands in for the mTLS
// handshake and plays no part in the decisions under test.
//
// No build constraint: newDefaultKVSvc and newDefaultAdminSvc
// (rest_auth_test.go) are unconstrained, and this surface ships in every build.

package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// isolationFixture is one node holding the system tenant, acme and globex.
type isolationFixture struct {
	srv     *Server
	tenants *authtest.Tenants
	caller  *authtest.FixedAuthenticator
}

func newIsolationFixture(t *testing.T, opts ...authmw.Option) *isolationFixture {
	t.Helper()
	tenants := authtest.NewTenants(t, tenantid.System, "acme", "globex")
	caller := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "acme-admin"))
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	mw := authmw.New(rbac.New(roles), append([]authmw.Option{authmw.WithAuthenticator(caller)}, opts...)...)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithTenantService(tenants),
		WithAuthMiddleware(mw),
	)
	return &isolationFixture{srv: srv, tenants: tenants, caller: caller}
}

func (f *isolationFixture) as(id *auth.Identity) { f.caller.Set(id) }

func (f *isolationFixture) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.server.Handler.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

// TestTenantIsolation_TenantAdminCannotActOnAnotherTenant is the headline: an
// acme Admin holds tenants:* in acme, and before the fix every one of these
// reached globex (200/204).
func TestTenantIsolation_TenantAdminCannotActOnAnotherTenant(t *testing.T) {
	tests := []struct{ name, method, path, body string }{
		{"get", http.MethodGet, "/v1/tenants/globex", ""},
		{"update", http.MethodPut, "/v1/tenants/globex", `{"name":"pwned"}`},
		{"quotas", http.MethodPost, "/v1/tenants/globex/quotas", `{"max_databases":1}`},
		{"entitlements", http.MethodPatch, "/v1/tenants/globex/entitlements", `{"allow_dedicated_wal":true}`},
		{"delete", http.MethodDelete, "/v1/tenants/globex", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newIsolationFixture(t)
			rec := f.do(tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
			got, err := f.tenants.GetTenant(t.Context(), "globex")
			if err != nil {
				t.Fatalf("globex after a refused %s: %v", tc.name, err)
			}
			if got.Name != "globex" || got.Quotas != nil || got.Entitlements != nil {
				t.Fatalf("a refused %s changed globex: %+v", tc.name, got)
			}
		})
	}
}

// The control: the same Admin on its own tenant, and the platform operator on
// any tenant, are still served.
func TestTenantIsolation_OwnTenantAndSystemAdminStillServed(t *testing.T) {
	f := newIsolationFixture(t)
	if rec := f.do(http.MethodGet, "/v1/tenants/acme", ""); rec.Code != http.StatusOK {
		t.Fatalf("acme admin GET acme = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	f.as(authtest.AdminIdentity(tenantid.System, "root"))
	if rec := f.do(http.MethodGet, "/v1/tenants/globex", ""); rec.Code != http.StatusOK {
		t.Fatalf("system admin GET globex = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// TestTenantIsolation_TenantAdminCannotChangeItsOwnPlan: quotas, entitlements
// and the isolation tier are the tenant's plan, set by the platform operator
// only. Before the fix an acme Admin could raise acme's own limits.
func TestTenantIsolation_TenantAdminCannotChangeItsOwnPlan(t *testing.T) {
	tests := []struct{ name, method, path, body string }{
		{"quotas", http.MethodPost, "/v1/tenants/acme/quotas", `{"max_databases":1000}`},
		{"entitlements", http.MethodPatch, "/v1/tenants/acme/entitlements", `{"allow_dedicated_wal":true}`},
		{"isolation", http.MethodPut, "/v1/tenants/acme", `{"isolation_type":"barrier"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newIsolationFixture(t)
			rec := f.do(tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
			got, err := f.tenants.GetTenant(t.Context(), "acme")
			if err != nil {
				t.Fatalf("acme after a refused %s: %v", tc.name, err)
			}
			if got.Quotas != nil || got.Entitlements != nil || got.IsolationType != tenant.IsolationPrefix {
				t.Fatalf("a refused %s changed acme's plan: %+v", tc.name, got)
			}
		})
	}
}

// A tenant Admin still edits what is its own to edit: its display name.
func TestTenantIsolation_TenantAdminMayRenameItself(t *testing.T) {
	f := newIsolationFixture(t)
	if rec := f.do(http.MethodPut, "/v1/tenants/acme", `{"name":"Acme Corp"}`); rec.Code != http.StatusOK {
		t.Fatalf("acme admin renaming acme = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestTenantIsolation_PlatformOperatorSetsAPlan(t *testing.T) {
	f := newIsolationFixture(t)
	f.as(authtest.AdminIdentity(tenantid.System, "root"))
	if rec := f.do(http.MethodPost, "/v1/tenants/acme/quotas", `{"max_databases":1000}`); rec.Code != http.StatusOK {
		t.Fatalf("operator setting acme's quotas = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPatch, "/v1/tenants/acme/entitlements", `{"allow_dedicated_wal":true}`); rec.Code != http.StatusOK {
		t.Fatalf("operator granting acme entitlements = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	got, err := f.tenants.GetTenant(t.Context(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Quotas == nil || got.Quotas.MaxDatabases != 1000 || got.Entitlements == nil || !got.Entitlements.AllowDedicatedWAL {
		t.Fatalf("the operator's plan did not reach acme: %+v", got)
	}
}

func TestTenantIsolation_TenantAdminCannotCreateATenant(t *testing.T) {
	f := newIsolationFixture(t)
	rec := f.do(http.MethodPost, "/v1/tenants", `{"id":"initech","name":"initech"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("acme admin create = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := f.tenants.GetTenant(t.Context(), "initech"); err == nil {
		t.Fatal("a refused create left a tenant behind")
	}

	f.as(authtest.AdminIdentity(tenantid.System, "root"))
	if rec := f.do(http.MethodPost, "/v1/tenants", `{"id":"initech","name":"initech"}`); rec.Code != http.StatusCreated {
		t.Fatalf("system admin create = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
}

func listedIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp listTenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make([]string, len(resp.Tenants))
	for i, tr := range resp.Tenants {
		ids[i] = tr.ID
	}
	return ids
}

func TestTenantIsolation_ListShowsATenantAdminOnlyItsOwnTenant(t *testing.T) {
	f := newIsolationFixture(t)
	if ids := listedIDs(t, f.do(http.MethodGet, "/v1/tenants/list", "")); len(ids) != 1 || ids[0] != "acme" {
		t.Fatalf("acme admin listed %v, want [acme]", ids)
	}
	f.as(authtest.AdminIdentity(tenantid.System, "root"))
	if ids := listedIDs(t, f.do(http.MethodGet, "/v1/tenants/list", "")); len(ids) != 3 {
		t.Fatalf("system admin listed %v, want all three tenants", ids)
	}
}

// A tenant id carrying a path separator cannot name a tenant: it would nest
// one tenant's keyspace inside another's. It is the caller's error, 400. On
// REST a '/' reaches a tenant id only through a request body -- in the URL it
// is a path separator (canonicalPathMiddleware routes %2F as one), so a by-id
// route never sees it -- and the by-id routes refuse the rest of the illegal
// set, a control byte here, before any authorization or service call.
func TestTenantIsolation_IllegalTenantIDIsBadRequest(t *testing.T) {
	f := newIsolationFixture(t)
	f.as(authtest.AdminIdentity(tenantid.System, "root"))
	if rec := f.do(http.MethodPost, "/v1/tenants", `{"id":"acme/evil","name":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("create acme/evil = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/tenants/acme%01", ""},
		{http.MethodPut, "/v1/tenants/acme%01", `{"name":"x"}`},
		{http.MethodDelete, "/v1/tenants/acme%01", ""},
		{http.MethodPost, "/v1/tenants/acme%01/quotas", `{"max_databases":1}`},
		{http.MethodPatch, "/v1/tenants/acme%01/entitlements", `{"allow_dedicated_wal":true}`},
	} {
		if rec := f.do(tc.method, tc.path, tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// TestTenantIsolation_SealedWindowHonoursTheUnsealRateLimit: the sealed
// window admits a tenant's own role-less identity to unseal, but it must not
// admit it past the limiter. Before the fix the second unseal reached the
// service.
func TestTenantIsolation_SealedWindowHonoursTheUnsealRateLimit(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	f := newIsolationFixture(t,
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(permission.TenantBarrierUnseal)))
	f.as(authtest.SealedIdentity("acme", "acme-admin"))

	if rec := f.do(http.MethodPost, "/v1/tenants/acme/barrier/unseal", `{"key":"a2V5"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("first unseal = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, "/v1/tenants/acme/barrier/unseal", `{"key":"a2V5"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second unseal = %d, want 429 (%s)", rec.Code, rec.Body.String())
	}
	if n := f.tenants.Unseals(); n != 1 {
		t.Fatalf("unseals reaching the service = %d, want 1", n)
	}
}
