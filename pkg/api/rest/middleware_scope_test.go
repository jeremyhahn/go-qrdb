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
)

// ---------------------------------------------------------------------------
// scopeMiddleware tests
// ---------------------------------------------------------------------------

// captureScope is a test handler that captures the tenant and database values
// from the request context after scopeMiddleware has run.
func captureScope(t *testing.T, tenant, db *string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*tenant = tenantFromContext(r.Context())
		*db = databaseFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestScopeMiddleware_BothHeadersPresent(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req.Header.Set(HeaderTenant, "acme")
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

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "default" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "default")
	}
	if gotDB != "default" {
		t.Errorf("database: got %q, want %q", gotDB, "default")
	}
}

func TestScopeMiddleware_EmptyHeaders_DefaultsApplied(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req.Header.Set(HeaderTenant, "")
	req.Header.Set(HeaderDatabase, "")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "default" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "default")
	}
	if gotDB != "default" {
		t.Errorf("database: got %q, want %q", gotDB, "default")
	}
}

func TestScopeMiddleware_OnlyTenantHeader(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req.Header.Set(HeaderTenant, "tenant-a")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "tenant-a" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "tenant-a")
	}
	if gotDB != "default" {
		t.Errorf("database: got %q, want %q", gotDB, "default")
	}
}

func TestScopeMiddleware_OnlyDatabaseHeader(t *testing.T) {
	var gotTenant, gotDB string
	handler := scopeMiddleware(captureScope(t, &gotTenant, &gotDB))

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req.Header.Set(HeaderDatabase, "mydb")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "default" {
		t.Errorf("tenant: got %q, want %q", gotTenant, "default")
	}
	if gotDB != "mydb" {
		t.Errorf("database: got %q, want %q", gotDB, "mydb")
	}
}

func TestScopeMiddleware_NextHandlerReceivesUpdatedContext(t *testing.T) {
	// Verify that downstream handlers see the enriched context and not the
	// original request context.
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		v := r.Context().Value(tenantIDKey)
		if v == nil {
			t.Error("tenantIDKey not in request context after scopeMiddleware")
		}
		v2 := r.Context().Value(dbNameKey)
		if v2 == nil {
			t.Error("dbNameKey not in request context after scopeMiddleware")
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
	req.Header.Set(HeaderTenant, "t1")
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

func TestTenantFromContext_MissingKey_ReturnsDefault(t *testing.T) {
	ctx := httptest.NewRequest(http.MethodGet, "/", nil).Context()
	got := tenantFromContext(ctx)
	if got != "default" {
		t.Errorf("got %q, want %q", got, "default")
	}
}

func TestTenantFromContext_SetKey_ReturnsValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderTenant, "enterprise")
	var captured string
	scopeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = tenantFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)
	if captured != "enterprise" {
		t.Errorf("got %q, want %q", captured, "enterprise")
	}
}
