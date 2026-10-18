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
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// These tests pin go-qrdb#194 on the REAL REST server: the collision guard
// lives here, where the live router is, rather than in each host that mounts
// a module. Every case constructs the server the production way
// (NewServer + WithServiceModules), so what they assert is what a host gets.

// httpModule is a ServiceModule whose RegisterHTTP is the test's.
type httpModule struct {
	apimodule.BaseModule
	name     string
	register func(r *apimodule.HTTPRegistrar)
}

func (m *httpModule) Name() string { return m.name }

func (m *httpModule) RegisterHTTP(r *apimodule.HTTPRegistrar) {
	if m.register != nil {
		m.register(r)
	}
}

func noopHandler(http.ResponseWriter, *http.Request) {}

// constructPanic builds a server over mods and returns what construction
// panicked with, or nil.
func constructPanic(t *testing.T, mods ...apimodule.ServiceModule) (recovered any) {
	t.Helper()
	defer func() { recovered = recover() }()
	NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithServiceModules(mods))
	return nil
}

func TestModuleRouteGuard_ModuleShadowingABuiltinFailsConstruction(t *testing.T) {
	for name, tc := range map[string]struct {
		method, pattern string
		public          bool
	}{
		"authorized route over a built-in":    {http.MethodGet, "/status", false},
		"public route over a public built-in": {http.MethodGet, "/health", true},
		"renamed parameter over a built-in":   {http.MethodGet, "/users/{userID}", false},
	} {
		t.Run(name, func(t *testing.T) {
			mod := &httpModule{name: "shadow", register: func(r *apimodule.HTTPRegistrar) {
				if tc.public {
					r.HandlePublic(tc.method, tc.pattern, noopHandler)
					return
				}
				r.Handle(tc.method, tc.pattern, permission.KVGet, noopHandler)
			}}
			got := constructPanic(t, mod)
			collision, ok := got.(*RouteCollisionError)
			if !ok {
				t.Fatalf("construction panicked with %#v, want *RouteCollisionError", got)
			}
			if collision.Claimant != builtinRouteOwner || collision.Holder != "module shadow" {
				t.Fatalf("collision = %+v, want the built-in refused against module shadow", collision)
			}
			if !strings.Contains(collision.Error(), "module shadow") || !strings.Contains(collision.Error(), tc.method) {
				t.Fatalf("message %q does not name the parties", collision.Error())
			}
		})
	}
}

func TestModuleRouteGuard_TwoModulesClaimingOneRouteFailConstruction(t *testing.T) {
	first := &httpModule{name: "first", register: func(r *apimodule.HTTPRegistrar) {
		r.Handle(http.MethodGet, "/v1/devices/{id}", permission.KVGet, noopHandler)
	}}
	second := &httpModule{name: "second", register: func(r *apimodule.HTTPRegistrar) {
		// The same route to chi: only the parameter's name differs.
		r.Handle(http.MethodGet, "/v1/devices/{deviceID}", permission.KVPut, noopHandler)
	}}
	collision, ok := constructPanic(t, first, second).(*RouteCollisionError)
	if !ok || collision.Claimant != "module second" || collision.Holder != "module first" {
		t.Fatalf("got %+v, want module second refused against module first", collision)
	}
}

func TestModuleRouteGuard_DirectRouterRegistrationCollidingFailsConstruction(t *testing.T) {
	cases := map[string]struct {
		mods   []apimodule.ServiceModule
		module string
		holder string
	}{
		"same module, Handle then direct Get": {
			mods: []apimodule.ServiceModule{&httpModule{name: "sneaky", register: func(r *apimodule.HTTPRegistrar) {
				r.Handle(http.MethodGet, "/v1/sneaky/thing", permission.KVGet, noopHandler)
				r.Get("/v1/sneaky/thing", noopHandler)
			}}},
			module: "sneaky", holder: "module sneaky",
		},
		"direct registration under another module's permission": {
			mods: []apimodule.ServiceModule{
				&httpModule{name: "owner", register: func(r *apimodule.HTTPRegistrar) {
					r.Handle(http.MethodPost, "/v1/owned/{id}", permission.KVPut, noopHandler)
				}},
				&httpModule{name: "intruder", register: func(r *apimodule.HTTPRegistrar) {
					r.With().Post("/v1/owned/{other}", noopHandler)
				}},
			},
			module: "intruder", holder: "module owner",
		},
		"direct registration of a built-in": {
			mods: []apimodule.ServiceModule{&httpModule{name: "grabby", register: func(r *apimodule.HTTPRegistrar) {
				r.Group(func(g chi.Router) { g.Get("/health", noopHandler) })
			}}},
			module: "grabby", holder: builtinRouteOwner,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := constructPanic(t, tc.mods...)
			direct, ok := got.(*DirectRouteCollisionError)
			if !ok {
				t.Fatalf("construction panicked with %#v, want *DirectRouteCollisionError", got)
			}
			if direct.Module != tc.module || direct.Holder != tc.holder {
				t.Fatalf("got %+v, want module %q against %q", direct, tc.module, tc.holder)
			}
			if !strings.Contains(direct.Error(), "HTTPRegistrar.Handle") {
				t.Fatalf("message %q does not say how to register", direct.Error())
			}
		})
	}
}

// TestModuleRouteGuard_UncoveredDirectRouteStillFailsDefaultDeny: a direct
// route nothing covers is not a collision, and stays the default-deny
// assertion's refusal, exactly as before.
func TestModuleRouteGuard_UncoveredDirectRouteStillFailsDefaultDeny(t *testing.T) {
	mod := &httpModule{name: "bare", register: func(r *apimodule.HTTPRegistrar) {
		r.Get("/v1/bare/unpermissioned", noopHandler)
	}}
	got := constructPanic(t, mod)
	msg, ok := got.(string)
	if !ok || !strings.Contains(msg, "DEFAULT-DENY") || !strings.Contains(msg, "/v1/bare/unpermissioned") {
		t.Fatalf("construction panicked with %#v, want the default-deny assertion naming the route", got)
	}
}

// TestModuleRouteGuard_LateRegistrationRefused: a module that keeps its
// registrar and registers after construction presents a surface nobody
// validated, so it panics instead of reaching the live router.
func TestModuleRouteGuard_LateRegistrationRefused(t *testing.T) {
	var kept *apimodule.HTTPRegistrar
	mod := &httpModule{name: "late", register: func(r *apimodule.HTTPRegistrar) {
		r.Handle(http.MethodGet, "/v1/late/ok", permission.KVGet, noopHandler)
		kept = r
	}}
	if got := constructPanic(t, mod); got != nil {
		t.Fatalf("construction panicked: %v", got)
	}
	for name, register := range map[string]func(){
		"Handle":       func() { kept.Handle(http.MethodGet, "/v1/late/more", permission.KVGet, noopHandler) },
		"HandlePublic": func() { kept.HandlePublic(http.MethodGet, "/v1/late/public", noopHandler) },
		"direct":       func() { kept.Get("/v1/late/direct", noopHandler) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if _, ok := recover().(*apimodule.RegistrationClosedError); !ok {
					t.Fatal("late registration did not panic with *apimodule.RegistrationClosedError")
				}
			}()
			register()
		})
	}
}

// TestModuleRouteGuard_NamespacedModuleServes is the control: a module whose
// routes collide with nothing constructs, and its route is served and
// authorized exactly as registered.
func TestModuleRouteGuard_NamespacedModuleServes(t *testing.T) {
	mod := &httpModule{name: "tidy", register: func(r *apimodule.HTTPRegistrar) {
		r.Handle(http.MethodGet, "/v1/tidy/{id}", permission.KVGet, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	}}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithServiceModules([]apimodule.ServiceModule{mod}))
	perm, _, ok := srv.routePerms.Lookup(http.MethodGet, "/v1/tidy/abc")
	if !ok || perm != permission.KVGet {
		t.Fatalf("Lookup = (%q, %v), want the module's permission", perm, ok)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tidy/abc", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the module handler's 418", rec.Code)
	}
}

// TestModuleRouteGuard_BypassingRouterMethodsFailConstruction are the review's
// probes on the real server (go-qrdb#194). Before the fix each one constructed
// and served a request no registration authorized: a mounted sub-router's
// NotFound answered everything under /v1/nf/*, and a top-level Use answered
// /v1/hidden ahead of routing.
func TestModuleRouteGuard_BypassingRouterMethodsFailConstruction(t *testing.T) {
	open := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	probes := map[string]func(r *apimodule.HTTPRegistrar){
		"Mount": func(r *apimodule.HTTPRegistrar) {
			sub := chi.NewRouter()
			sub.NotFound(open)
			r.Mount("/v1/nf", sub)
		},
		"Route": func(r *apimodule.HTTPRegistrar) {
			r.Route("/v1/nf", func(sr chi.Router) { sr.NotFound(open) })
		},
		"NotFound":         func(r *apimodule.HTTPRegistrar) { r.NotFound(open) },
		"MethodNotAllowed": func(r *apimodule.HTTPRegistrar) { r.MethodNotAllowed(open) },
		"Use": func(r *apimodule.HTTPRegistrar) {
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.URL.Path == "/v1/hidden" {
						open(w, req)
						return
					}
					next.ServeHTTP(w, req)
				})
			})
		},
	}
	for method, probe := range probes {
		t.Run(method, func(t *testing.T) {
			got := constructPanic(t, &httpModule{name: "prober", register: probe})
			refused, ok := got.(*apimodule.UnsupportedRouterMethodError)
			if !ok || refused.Method != method {
				t.Fatalf("construction panicked with %#v, want *apimodule.UnsupportedRouterMethodError{%s}", got, method)
			}
		})
	}
}

// TestModuleRouteGuard_UnmatchedPathUnderModulePrefixRefused: the defence in
// depth behind the refusals, on the real server with its auth middleware and
// a web UI fallback. A path under a module's subtree that no registration
// matches is refused instead of reaching the fallback; a path outside every
// module subtree still reaches it.
func TestModuleRouteGuard_UnmatchedPathUnderModulePrefixRefused(t *testing.T) {
	mod := &httpModule{name: "devices", register: func(r *apimodule.HTTPRegistrar) {
		r.Handle(http.MethodGet, "/v1/devices/{id}", permission.KVGet, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}}
	webUI := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	caller := &certScopeAuthenticator{}
	caller.as("acme", "bob")
	mw := authmw.New(&allowAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(caller))
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithServiceModules([]apimodule.ServiceModule{mod}),
		WithAuthMiddleware(mw),
		WithWebUI(webUI),
	)
	serve := func(path string) int {
		rec := httptest.NewRecorder()
		srv.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if code := serve("/v1/devices/abc"); code != http.StatusOK {
		t.Fatalf("registered module route = %d, want 200", code)
	}
	if code := serve("/v1/devices/abc/extra"); code != http.StatusNotFound {
		t.Fatalf("unmatched path under the module prefix = %d, want 404 (not the fallback)", code)
	}
	if code := serve("/app/index.html"); code != http.StatusTeapot {
		t.Fatalf("path outside every module prefix = %d, want the web UI fallback", code)
	}
}

// allowAllAuthorizer grants everything; these tests are about routing.
type allowAllAuthorizer struct{}

func (allowAllAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return nil
}

func TestModuleRoutePrefix(t *testing.T) {
	cases := map[string]string{
		"/v1/devices/{id}":        "/v1/devices/",
		"/v1/nf/*":                "/v1/nf/",
		"/v1/t/{tenant}/x/{id}":   "/v1/t/",
		"/v1/static/route":        "",
		"/{anything}":             "",
		"/*":                      "",
		"/v1/devices/{id:[0-9]+}": "/v1/devices/",
	}
	for pattern, want := range cases {
		if got := moduleRoutePrefix(pattern); got != want {
			t.Errorf("moduleRoutePrefix(%q) = %q, want %q", pattern, got, want)
		}
	}
}
