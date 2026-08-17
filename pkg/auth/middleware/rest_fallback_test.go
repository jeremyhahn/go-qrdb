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

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// mockFallback implements RESTAuthFallback for the module session-token seam.
type mockFallback struct {
	identity *auth.Identity
	err      error
	calls    int
}

func (m *mockFallback) AuthenticateHTTP(*http.Request) (*auth.Identity, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.identity, nil
}

// fallbackRoutes builds a registry with one permission route owned by a
// fallback-bearing module and one without any fallback.
func fallbackRoutes(fb RESTAuthFallback) *RESTRouteRegistry {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodPost, "/api/v1/keys", permission.Permission("keys:create"))
	rr.SetFallback(http.MethodPost, "/api/v1/keys", fb)
	rr.Register(http.MethodGet, "/status", permission.AdminStatus)
	return rr
}

// TestRESTMiddleware_Fallback_AuthenticatesTokenCaller proves the seam: when
// qrdb's authenticators fail, the owning module's fallback authenticates the
// request and the ONE authorizer still renders the decision on the resulting
// identity.
func TestRESTMiddleware_Fallback_AuthenticatesTokenCaller(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}
	fb := &mockFallback{identity: &auth.Identity{Subject: "alice", TenantID: "t1"}}
	m := New(authz, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if id := auth.IdentityFromContext(r.Context()); id == nil || id.Subject != "alice" {
			t.Errorf("handler identity = %+v, want alice", id)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(fallbackRoutes(fb), next)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatalf("handler not reached: status=%d body=%s", w.Code, w.Body.String())
	}
	if fb.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fb.calls)
	}
	if authz.calls.Load() == 0 {
		t.Fatal("the one authorizer must still authorize the fallback identity")
	}
}

// TestRESTMiddleware_Fallback_FailureIsDenied proves fail-closed: a fallback
// that cannot authenticate the caller yields 401, and the handler never runs.
func TestRESTMiddleware_Fallback_FailureIsDenied(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	fb := &mockFallback{err: errors.New("token expired")}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run when the fallback rejects the caller")
	})
	handler := m.RESTMiddleware(fallbackRoutes(fb), next)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestRESTMiddleware_Fallback_NotConsultedForOtherRoutes proves scoping: a
// route without a fallback keeps the original authentication failure, and
// the module's fallback is never consulted for routes it does not own.
func TestRESTMiddleware_Fallback_NotConsultedForOtherRoutes(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	fb := &mockFallback{identity: &auth.Identity{Subject: "alice"}}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for an unauthenticated caller")
	})
	handler := m.RESTMiddleware(fallbackRoutes(fb), next)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if fb.calls != 0 {
		t.Fatalf("fallback consulted for a route it does not own (%d calls)", fb.calls)
	}
}

// TestRESTMiddleware_Fallback_SkippedWhenUpstreamAuthenticates proves
// precedence: when qrdb's own authenticators produce an identity, the module
// fallback never runs.
func TestRESTMiddleware_Fallback_SkippedWhenUpstreamAuthenticates(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", identity: &auth.Identity{Subject: "cert-user", TenantID: "t1"}}
	fb := &mockFallback{identity: &auth.Identity{Subject: "token-user"}}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := auth.IdentityFromContext(r.Context()); id == nil || id.Subject != "cert-user" {
			t.Errorf("identity = %+v, want cert-user", id)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(fallbackRoutes(fb), next)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if fb.calls != 0 {
		t.Fatalf("fallback ran despite an upstream identity (%d calls)", fb.calls)
	}
}

// TestRESTRouteRegistry_DuplicateRegistrationPanics pins the route-collision
// guard: two registrations of the same method+pattern (two modules, or a
// module and the built-in surface) fail construction loudly and by name.
func TestRESTRouteRegistry_DuplicateRegistrationPanics(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodPost, "/api/v1/auth/login/piv", permission.Permission("a:b"))
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("duplicate registration must panic")
		}
		msg, ok := r.(string)
		if !ok || msg == "" {
			t.Fatalf("panic value: %v", r)
		}
	}()
	rr.RegisterPublic(http.MethodPost, "/api/v1/auth/login/piv")
}

// TestRESTRouteRegistry_SetFallbackUnregisteredPanics pins the programmer
// error: attaching a fallback to a route that was never registered.
func TestRESTRouteRegistry_SetFallbackUnregisteredPanics(t *testing.T) {
	rr := NewRESTRouteRegistry()
	defer func() {
		if recover() == nil {
			t.Fatal("SetFallback on an unregistered route must panic")
		}
	}()
	rr.SetFallback(http.MethodGet, "/nope", &mockFallback{})
}
