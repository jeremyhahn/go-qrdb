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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// bearerAuthenticator accepts the bearer token "good" as alice and refuses
// every other Authorization value; no value is auth.ErrUnauthenticated.
type bearerAuthenticator struct{}

func (bearerAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	switch auth.HTTPAuthorizationFromContext(ctx) {
	case "Bearer good":
		return &auth.Identity{Subject: "alice", TenantID: "t1"}, nil
	default:
		return nil, auth.ErrUnauthenticated
	}
}

func (bearerAuthenticator) Name() string { return "bearer" }

// fallbackModule is an httpModule that also brings a session-token plane.
type fallbackModule struct {
	httpModule
}

func (fallbackModule) AuthenticateHTTP(r *http.Request) (*auth.Identity, error) {
	if r.Header.Get("Authorization") == "Bearer module-session" {
		return &auth.Identity{Subject: "carol", TenantID: "t1"}, nil
	}
	return nil, auth.ErrUnauthenticated
}

// whoAmI answers 200 with the caller's subject, or "anonymous".
func whoAmI(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if id := auth.IdentityFromContext(r.Context()); id != nil {
		_, _ = w.Write([]byte(id.Subject))
		return
	}
	_, _ = w.Write([]byte("anonymous"))
}

func optionalModuleServer(t *testing.T, mod apimodule.ServiceModule) *Server {
	t.Helper()
	mw := authmw.New(allowAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(bearerAuthenticator{}))
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithAuthMiddleware(mw), WithServiceModules([]apimodule.ServiceModule{mod}))
}

func registerAllModes(r *apimodule.HTTPRegistrar) {
	r.HandleOptional(http.MethodGet, "/v1/oauth/authorize", whoAmI)
	r.Handle(http.MethodGet, "/v1/oauth/clients", permission.KVGet, whoAmI)
	r.HandlePublic(http.MethodGet, "/v1/oauth/jwks", whoAmI)
}

func serveAuthorized(srv *Server, path, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

func TestModuleOptionalRoute_ServesEveryCase(t *testing.T) {
	srv := optionalModuleServer(t, &httpModule{name: "oauth", register: registerAllModes})

	if _, kind, ok := srv.routePerms.Lookup(http.MethodGet, "/v1/oauth/authorize"); !ok || kind != authmw.RESTRouteOptional {
		t.Fatalf("Lookup kind = %v ok=%v, want RESTRouteOptional", kind, ok)
	}

	anon := serveAuthorized(srv, "/v1/oauth/authorize", "")
	if anon.Code != http.StatusOK || anon.Body.String() != "anonymous" {
		t.Fatalf("no credential: %d %q, want 200 anonymous", anon.Code, anon.Body.String())
	}
	authed := serveAuthorized(srv, "/v1/oauth/authorize", "Bearer good")
	if authed.Code != http.StatusOK || authed.Body.String() != "alice" {
		t.Fatalf("valid credential: %d %q, want 200 alice", authed.Code, authed.Body.String())
	}

	bad := serveAuthorized(srv, "/v1/oauth/authorize", "Bearer forged")
	onHandle := serveAuthorized(srv, "/v1/oauth/clients", "Bearer forged")
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("invalid credential: %d %q, want 401", bad.Code, bad.Body.String())
	}
	if bad.Code != onHandle.Code || bad.Body.String() != onHandle.Body.String() {
		t.Fatalf("optional refusal %d %q differs from Handle refusal %d %q",
			bad.Code, bad.Body.String(), onHandle.Code, onHandle.Body.String())
	}
}

func TestModuleOptionalRoute_HandleAndHandlePublicUnchanged(t *testing.T) {
	srv := optionalModuleServer(t, &httpModule{name: "oauth", register: registerAllModes})

	if rec := serveAuthorized(srv, "/v1/oauth/clients", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("Handle route, no credential: %d, want 401", rec.Code)
	}
	if rec := serveAuthorized(srv, "/v1/oauth/clients", "Bearer good"); rec.Code != http.StatusOK || rec.Body.String() != "alice" {
		t.Fatalf("Handle route, valid credential: %d %q, want 200 alice", rec.Code, rec.Body.String())
	}
	// A public route never authenticates, so even a forged credential is
	// served, anonymous.
	if rec := serveAuthorized(srv, "/v1/oauth/jwks", "Bearer forged"); rec.Code != http.StatusOK || rec.Body.String() != "anonymous" {
		t.Fatalf("HandlePublic route: %d %q, want 200 anonymous", rec.Code, rec.Body.String())
	}
}

func TestModuleOptionalRoute_ModuleFallbackAttached(t *testing.T) {
	mod := &fallbackModule{httpModule{name: "oauth", register: registerAllModes}}
	srv := optionalModuleServer(t, mod)

	if fb := srv.routePerms.FallbackFor(http.MethodGet, "/v1/oauth/authorize"); fb == nil {
		t.Fatal("a fallback-bearing module's optional route must carry its fallback")
	}
	rec := serveAuthorized(srv, "/v1/oauth/authorize", "Bearer module-session")
	if rec.Code != http.StatusOK || rec.Body.String() != "carol" {
		t.Fatalf("module session credential: %d %q, want 200 carol", rec.Code, rec.Body.String())
	}
	if rec := serveAuthorized(srv, "/v1/oauth/authorize", ""); rec.Body.String() != "anonymous" {
		t.Fatalf("no credential with a fallback module: %q, want anonymous", rec.Body.String())
	}
}
