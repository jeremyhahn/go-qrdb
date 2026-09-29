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
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// Authorization resolves the decoded r.URL.Path; chi v5 routed on
// r.URL.RawPath whenever one was set. A percent-encoded slash therefore made
// the two see DIFFERENT routes, and the request was authorized as one and
// served by another. These probes run the real NewServer with its real
// RESTMiddleware, and each asserts the SIDE EFFECT did not happen -- the
// database was not dropped, the stronger handler was not reached -- because a
// status code alone would pass against a handler that ran and then errored.

// permAuthorizer grants exactly the permissions it holds, and records every
// decision it was asked for.
type permAuthorizer struct {
	mu      sync.Mutex
	granted map[string]bool
	asked   []string
}

func (a *permAuthorizer) Authorize(_ context.Context, _ *auth.Identity, resource, action string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, resource+":"+action)
	if a.granted[resource+":"+action] {
		return nil
	}
	return auth.ErrUnauthorized
}

// probeModule registers routes through the real HTTPRegistrar.
type probeModule struct {
	apimodule.BaseModule
	register func(r *apimodule.HTTPRegistrar)
}

func (probeModule) Name() string { return "probe" }

func (m probeModule) RegisterHTTP(r *apimodule.HTTPRegistrar) { m.register(r) }

// authedServer builds the real server with the real auth middleware over a
// fixed authenticated caller and the given authorizer.
func authedServer(t *testing.T, authz auth.Authorizer, opts ...ServerOption) *Server {
	t.Helper()
	caller := &certScopeAuthenticator{}
	caller.as("acme", "mallory")
	mw := authmw.New(authz, authmw.WithAuthenticator(caller))
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		append([]ServerOption{WithAuthMiddleware(mw)}, opts...)...)
}

func serveRaw(srv *Server, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// TestEncodedSlash_CannotDropADatabaseUnderDenyAll is the proven bypass: any
// authenticated caller could drop a database by encoding a slash into its
// name.
func TestEncodedSlash_CannotDropADatabaseUnderDenyAll(t *testing.T) {
	var dropped []string
	var mu sync.Mutex
	dbSvc := &mockDatabaseService{dropFn: func(name string) error {
		mu.Lock()
		defer mu.Unlock()
		dropped = append(dropped, name)
		return nil
	}}
	srv := authedServer(t, &permAuthorizer{}, WithDatabaseService(dbSvc))

	for _, target := range []string{"/v1/databases/prod%2Fx", "/v1/databases/prod%2fx", "/v1/databases/prod%2F"} {
		rec := serveRaw(srv, http.MethodDelete, target)
		mu.Lock()
		got := append([]string(nil), dropped...)
		mu.Unlock()
		if len(got) != 0 {
			t.Fatalf("DELETE %s under a deny-all authorizer dropped %v (status %d)", target, got, rec.Code)
		}
		if rec.Code >= 200 && rec.Code < 300 {
			t.Fatalf("DELETE %s = %d, want a refusal", target, rec.Code)
		}
	}
}

// TestEncodedSlash_CannotReachAStrongerModuleHandler: authorized as the weak
// route, the request must not be served by the strong one.
func TestEncodedSlash_CannotReachAStrongerModuleHandler(t *testing.T) {
	var strongHit, weakHit atomic.Int32
	mod := probeModule{register: func(r *apimodule.HTTPRegistrar) {
		r.Handle(http.MethodGet, "/v1/m/{id}", permission.AdminDrain, func(w http.ResponseWriter, _ *http.Request) {
			strongHit.Add(1)
			w.WriteHeader(http.StatusOK)
		})
		r.Handle(http.MethodGet, "/v1/m/x/{sub}", permission.KVGet, func(w http.ResponseWriter, _ *http.Request) {
			weakHit.Add(1)
			w.WriteHeader(http.StatusOK)
		})
	}}
	kvGet := permission.KVGet
	authz := &permAuthorizer{granted: map[string]bool{
		permission.Resource(kvGet) + ":" + permission.Action(kvGet): true,
	}}
	srv := authedServer(t, authz, WithServiceModules([]apimodule.ServiceModule{mod}))

	serveRaw(srv, http.MethodGet, "/v1/m/x%2Fy")
	if n := strongHit.Load(); n != 0 {
		t.Fatalf("a caller holding only kv:get reached the admin:drain handler (%d time(s))", n)
	}
	// Control: the route it WAS authorized for is the one that serves it.
	if n := weakHit.Load(); n != 1 {
		t.Fatalf("weak handler hits = %d, want 1: the request should be served by the route it was authorized as", n)
	}
}

// TestEncodedSlash_PublicRouteCannotOpenAProtectedHandler: a public route's
// decoded path must not carry an unauthenticated caller into a protected
// handler.
func TestEncodedSlash_PublicRouteCannotOpenAProtectedHandler(t *testing.T) {
	var protectedHit atomic.Int32
	mod := probeModule{register: func(r *apimodule.HTTPRegistrar) {
		r.HandlePublic(http.MethodGet, "/v1/x/pub/info", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		r.Handle(http.MethodGet, "/v1/x/{id}", permission.AdminDrain, func(w http.ResponseWriter, _ *http.Request) {
			protectedHit.Add(1)
			w.WriteHeader(http.StatusOK)
		})
	}}
	// No authenticator can succeed: the caller is anonymous.
	mw := authmw.New(&permAuthorizer{})
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithAuthMiddleware(mw), WithServiceModules([]apimodule.ServiceModule{mod}))

	serveRaw(srv, http.MethodGet, "/v1/x/pub%2Finfo")
	if n := protectedHit.Load(); n != 0 {
		t.Fatalf("an anonymous caller reached the protected handler through a public route (%d time(s))", n)
	}
}

// TestEncodedCharacters_StillReachTheirHandler: canonicalising must not break
// legitimate encoded keys. A KV key with an encoded slash reaches /kv/* with
// the decoded key, exactly as before; an encoded space in a single-segment
// name reaches its route with the decoded name.
func TestEncodedCharacters_StillReachTheirHandler(t *testing.T) {
	var gotKey string
	kv := &mockKVService{getFn: func(_ context.Context, key string) ([]byte, error) {
		gotKey = key
		return []byte("v"), nil
	}}
	caller := &certScopeAuthenticator{}
	caller.as("acme", "alice")
	all := &permAuthorizer{granted: map[string]bool{}}
	for _, p := range []permission.Permission{permission.KVGet, permission.UsersRead} {
		all.granted[permission.Resource(p)+":"+permission.Action(p)] = true
	}
	srv := NewServer(kv, newDefaultAdminSvc(), "127.0.0.1:0",
		WithAuthMiddleware(authmw.New(all, authmw.WithAuthenticator(caller))))

	for target, want := range map[string]string{
		"/kv/a%2Fb":      "a/b",
		"/kv/a%2Fb%2Fc":  "a/b/c",
		"/kv/sp%20ace":   "sp ace",
		"/kv/pct%25sign": "pct%sign",
	} {
		gotKey = ""
		rec := serveRaw(srv, http.MethodGet, target)
		if rec.Code != http.StatusOK || gotKey != want {
			t.Fatalf("GET %s = %d with key %q, want 200 with key %q", target, rec.Code, gotKey, want)
		}
	}
}

func TestCanonicalPathMiddleware_ClearsRawPathWithoutMutatingTheCaller(t *testing.T) {
	var seenRaw, seenPath string
	h := canonicalPathMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenRaw, seenPath = r.URL.RawPath, r.URL.Path
	}))
	req := httptest.NewRequest(http.MethodGet, "/a/b%2Fc", nil)
	if req.URL.RawPath == "" {
		t.Fatal("precondition: the request should carry a RawPath")
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seenRaw != "" || seenPath != "/a/b/c" {
		t.Fatalf("handler saw RawPath %q Path %q, want an empty RawPath and /a/b/c", seenRaw, seenPath)
	}
	if req.URL.RawPath == "" {
		t.Fatal("the caller's request was mutated")
	}
	// A request with no RawPath passes through untouched.
	plain := httptest.NewRequest(http.MethodGet, "/a/b", nil)
	h.ServeHTTP(httptest.NewRecorder(), plain)
	if seenPath != "/a/b" {
		t.Fatalf("plain request path = %q", seenPath)
	}
}
