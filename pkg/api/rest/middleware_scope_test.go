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

package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// scopeMiddleware tests
// ---------------------------------------------------------------------------

// captureScope is a test handler that captures the tenant and database values
// from the request context after scopeMiddleware has run.
func captureScope(t *testing.T, got *string, db *string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = tenantFromContext(r.Context())
		*db = databaseFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

// withIdentity injects an auth.Identity (and its tenant) into the request context,
// simulating what the auth middleware does on every authenticated request.
func withIdentity(r *http.Request, identity *auth.Identity) *http.Request {
	ctx := auth.WithIdentity(r.Context(), identity)
	ctx = tenant.WithTenantID(ctx, identity.TenantID)
	return r.WithContext(ctx)
}

func TestScopeMiddleware_DatabaseHeaderPresent(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req = withIdentity(req, &auth.Identity{Subject: "alice", TenantID: "acme"})
	req.Header.Set(HeaderDatabase, "analytics")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "acme" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "acme")
	}
	if gotDB != "analytics" {
		t.Errorf("database: got %q, want %q", gotDB, "analytics")
	}
}

func TestScopeMiddleware_NoHeaders_DefaultsApplied(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	// No identity → tenant.FromContext returns SystemTenantID; no DB header → defaultDatabaseScope.
	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "system" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "system")
	}
	if gotDB != "default" {
		t.Errorf("database: got %q, want %q", gotDB, "default")
	}
}

func TestScopeMiddleware_EmptyDatabaseHeader_DefaultApplied(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req = withIdentity(req, &auth.Identity{Subject: "bob", TenantID: "bob-corp"})
	req.Header.Set(HeaderDatabase, "")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "bob-corp" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "bob-corp")
	}
	if gotDB != "default" {
		t.Errorf("database: got %q, want %q", gotDB, "default")
	}
}

func TestScopeMiddleware_IdentityTenantIsAuthoritative(t *testing.T) {
	// Verifies that the tenant comes from the authenticated identity, not from
	// any header that a client might attempt to send. The server ignores any
	// X-QRDB-Tenant header.
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req = withIdentity(req, &auth.Identity{Subject: "charlie", TenantID: "tenant-a"})
	// Attempt to spoof a different tenant via the (now-removed) header — it must be ignored.
	req.Header.Set("X-QRDB-Tenant", "tenant-b")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "tenant-a" {
		t.Errorf("tenant: got %q, want %q (header must not override identity)", gotTenant, "tenant-a")
	}
}

func TestScopeMiddleware_OnlyDatabaseHeader(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req = withIdentity(req, &auth.Identity{Subject: "dave", TenantID: "system"})
	req.Header.Set(HeaderDatabase, "mydb")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "system" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "system")
	}
	if gotDB != "mydb" {
		t.Errorf("database: got %q, want %q", gotDB, "mydb")
	}
}

func TestScopeMiddleware_NextHandlerReceivesUpdatedContext(t *testing.T) {
	// Verify that downstream handlers see the enriched context and that
	// the database key is set by scopeMiddleware.
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		v := r.Context().Value(dbNameKey)
		if v == nil {
			t.Error("dbNameKey not in request context after scopeMiddleware")
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req = withIdentity(req, &auth.Identity{Subject: "eve", TenantID: "t1"})
	req.Header.Set(HeaderDatabase, "d1")

	scopeMiddleware(inner).ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Error("inner handler was not called")
	}
}

// ---------------------------------------------------------------------------
// Context helper tests
// ---------------------------------------------------------------------------

func TestDatabaseFromContext_MissingKey_ReturnsDefault(t *testing.T) {
	ctx := httptest.NewRequest(http.MethodGet, "/", nil).Context()
	got := databaseFromContext(ctx)
	if got != "default" {
		t.Errorf("got %q, want %q", got, "default")
	}
}

func TestDatabaseFromContext_SetKey_ReturnsValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderDatabase, "prod")
	var captured string
	scopeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = databaseFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)
	if captured != "prod" {
		t.Errorf("got %q, want %q", captured, "prod")
	}
}

func TestTenantFromContext_MissingIdentity_ReturnsDefault(t *testing.T) {
	// When no identity is in context, tenantFromContext delegates to
	// tenant.FromContext which returns SystemTenantID.
	ctx := httptest.NewRequest(http.MethodGet, "/", nil).Context()
	got := tenantFromContext(ctx)
	if got != "system" {
		t.Errorf("got %q, want %q", got, "system")
	}
}

func TestTenantFromContext_IdentityBound_ReturnsTenantID(t *testing.T) {
	// Simulate auth middleware binding the identity + tenant.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withIdentity(req, &auth.Identity{Subject: "frank", TenantID: "enterprise"})
	got := tenantFromContext(req.Context())
	if got != "enterprise" {
		t.Errorf("got %q, want %q", got, "enterprise")
	}
}
