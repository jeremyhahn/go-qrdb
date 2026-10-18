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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// bearerCarrierAuthenticator authenticates a request whose Authorization
// carrier equals token — the shape of an embedder-supplied OAuth 2.1 bearer
// validator (qrdb ships none itself; this stub only exercises the REST
// transport's header plumbing).
type bearerCarrierAuthenticator struct{ token string }

func (b *bearerCarrierAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	if auth.HTTPAuthorizationFromContext(ctx) != b.token {
		return nil, auth.ErrUnauthenticated
	}
	return &auth.Identity{Subject: "bearer-alice", TenantID: "t1"}, nil
}

func (b *bearerCarrierAuthenticator) Name() string { return "bearer-carrier-stub" }

// bearerCarrierRoutes registers a single permission-gated route.
func bearerCarrierRoutes() *RESTRouteRegistry {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/status", permission.AdminStatus)
	return rr
}

// TestRESTMiddleware_AuthorizationHeaderReachesAuthenticators verifies the
// REST middleware carries the raw Authorization header into the request
// context, where an embedder-registered credential authenticator resolves
// it and authenticates the caller.
func TestRESTMiddleware_AuthorizationHeaderReachesAuthenticators(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz, WithAuthenticator(&bearerCarrierAuthenticator{token: "Bearer tok-1"}))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if id := auth.IdentityFromContext(r.Context()); id == nil || id.Subject != "bearer-alice" {
			t.Errorf("handler identity = %+v, want bearer-alice", id)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(bearerCarrierRoutes(), next)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer tok-1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatalf("handler not reached: status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestRESTMiddleware_WrongBearerDenied verifies fail-closed behavior: a
// non-matching Authorization value authenticates nothing and the request is
// refused before the handler.
func TestRESTMiddleware_WrongBearerDenied(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz, WithAuthenticator(&bearerCarrierAuthenticator{token: "Bearer tok-1"}))

	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler must not be reached")
	})
	handler := m.RESTMiddleware(bearerCarrierRoutes(), next)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer WRONG")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
