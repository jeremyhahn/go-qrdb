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

package apimodule_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// go-qrdb#194: the registrar records every route a module registers straight
// on the embedded router, still forwards it to the live router (subrouters and
// direct routes keep working), and refuses registration once the server has
// validated the module's surface.

func TestRegistrar_RecordsEveryDirectRegistrationAndForwardsIt(t *testing.T) {
	live := chi.NewRouter()
	var recorded []recordedRoute
	reg := apimodule.NewHTTPRegistrar(live, func(method, pattern string, _ permission.Permission, _ http.HandlerFunc) {
		recorded = append(recorded, recordedRoute{method: method, pattern: pattern})
	}, func(method, pattern string, _ http.HandlerFunc) {
		recorded = append(recorded, recordedRoute{method: method, pattern: pattern, public: true})
	}, func(method, pattern string, _ http.HandlerFunc) {
		recorded = append(recorded, recordedRoute{method: method, pattern: pattern, optional: true})
	})

	h := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }
	reg.Handle(http.MethodGet, "/recorded/through/handle", permission.KVGet, h)
	reg.HandlePublic(http.MethodGet, "/recorded/through/public", h)
	reg.HandleOptional(http.MethodGet, "/recorded/through/optional", h)
	reg.Get("/d/get", h)
	reg.Post("/d/post", h)
	reg.Put("/d/put", h)
	reg.Patch("/d/patch", h)
	reg.Delete("/d/delete", h)
	reg.Head("/d/head", h)
	reg.Options("/d/options", h)
	reg.Connect("/d/connect", h)
	reg.Trace("/d/trace", h)
	reg.Method(http.MethodGet, "/d/method", http.HandlerFunc(h))
	reg.MethodFunc(http.MethodPost, "/d/methodfunc", h)
	reg.Router.Handle("/d/handle", http.HandlerFunc(h))
	reg.HandleFunc("/d/handlefunc", h)
	reg.With(func(next http.Handler) http.Handler { return next }).Get("/d/with", h)
	reg.Group(func(g chi.Router) { g.Get("/d/group", h) })

	want := []apimodule.RouteClaim{
		{Method: http.MethodGet, Pattern: "/d/get"},
		{Method: http.MethodPost, Pattern: "/d/post"},
		{Method: http.MethodPut, Pattern: "/d/put"},
		{Method: http.MethodPatch, Pattern: "/d/patch"},
		{Method: http.MethodDelete, Pattern: "/d/delete"},
		{Method: http.MethodHead, Pattern: "/d/head"},
		{Method: http.MethodOptions, Pattern: "/d/options"},
		{Method: http.MethodConnect, Pattern: "/d/connect"},
		{Method: http.MethodTrace, Pattern: "/d/trace"},
		{Method: http.MethodGet, Pattern: "/d/method"},
		{Method: http.MethodPost, Pattern: "/d/methodfunc"},
		{Method: apimodule.AnyMethod, Pattern: "/d/handle"},
		{Method: apimodule.AnyMethod, Pattern: "/d/handlefunc"},
		{Method: http.MethodGet, Pattern: "/d/with"},
		{Method: http.MethodGet, Pattern: "/d/group"},
	}
	if got := reg.DirectRoutes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DirectRoutes =\n%v\nwant\n%v", got, want)
	}
	if len(recorded) != 3 {
		t.Fatalf("permission-recorded routes = %v, want the Handle, HandlePublic and HandleOptional ones only", recorded)
	}

	// Recording does not intercept: the live router serves every one.
	for _, path := range []string{"/d/get", "/d/with", "/d/group", "/d/handle"} {
		rec := httptest.NewRecorder()
		live.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusTeapot {
			t.Fatalf("GET %s = %d, want the forwarded handler", path, rec.Code)
		}
	}
}

// TestRegistrar_DirectRoutesIsACopy: the server reads the record once; a
// caller mutating the slice it got must not rewrite what was registered.
func TestRegistrar_DirectRoutesIsACopy(t *testing.T) {
	reg := apimodule.NewHTTPRegistrar(chi.NewRouter(), nil, nil, nil)
	reg.Get("/x", func(http.ResponseWriter, *http.Request) {})
	got := reg.DirectRoutes()
	got[0].Pattern = "/tampered"
	if reg.DirectRoutes()[0].Pattern != "/x" {
		t.Fatal("DirectRoutes returned the registrar's own slice")
	}
}

func TestRegistrar_FreezeRefusesEveryLaterRegistration(t *testing.T) {
	reg := apimodule.NewHTTPRegistrar(chi.NewRouter(),
		func(string, string, permission.Permission, http.HandlerFunc) {},
		func(string, string, http.HandlerFunc) {},
		func(string, string, http.HandlerFunc) {})
	stashed := reg.With()
	reg.Freeze()

	h := func(http.ResponseWriter, *http.Request) {}
	for name, register := range map[string]func(){
		"Handle":         func() { reg.Handle(http.MethodGet, "/late", permission.KVGet, h) },
		"HandlePublic":   func() { reg.HandlePublic(http.MethodGet, "/late", h) },
		"HandleOptional": func() { reg.HandleOptional(http.MethodGet, "/late", h) },
		"direct Get":     func() { reg.Get("/late", h) },
		"router Handle":  func() { reg.Router.Handle("/late", http.HandlerFunc(h)) },
		"stashed inline": func() { stashed.Post("/late", h) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				closed, ok := recover().(*apimodule.RegistrationClosedError)
				if !ok {
					t.Fatal("registration after Freeze did not panic with *RegistrationClosedError")
				}
				if closed.Pattern != "/late" || !strings.Contains(closed.Error(), "RegisterHTTP") {
					t.Fatalf("error %+v / %q does not name the route and the rule", closed, closed.Error())
				}
			}()
			register()
		})
	}
}

func TestRegistrar_NilRouterStaysNil(t *testing.T) {
	if reg := apimodule.NewHTTPRegistrar(nil, nil, nil, nil); reg.Router != nil {
		t.Fatal("a nil live router must not be wrapped into a non-nil one")
	}
}

// TestRegistrar_RefusesRouterMethodsThatBypassRegistration (go-qrdb#194):
// Mount, Route, NotFound, MethodNotAllowed and Use each serve requests no
// route registration describes, so the registrar's router refuses them --
// on the live router and on any inline router it hands out.
func TestRegistrar_RefusesRouterMethodsThatBypassRegistration(t *testing.T) {
	h := func(http.ResponseWriter, *http.Request) {}
	mw := func(next http.Handler) http.Handler { return next }
	calls := map[string]func(r chi.Router){
		"Mount":            func(r chi.Router) { r.Mount("/sub", http.HandlerFunc(h)) },
		"Route":            func(r chi.Router) { r.Route("/sub", func(chi.Router) {}) },
		"NotFound":         func(r chi.Router) { r.NotFound(h) },
		"MethodNotAllowed": func(r chi.Router) { r.MethodNotAllowed(h) },
		"Use":              func(r chi.Router) { r.Use(mw) },
	}
	for method, call := range calls {
		for where, router := range map[string]func(reg *apimodule.HTTPRegistrar) chi.Router{
			"live router":   func(reg *apimodule.HTTPRegistrar) chi.Router { return reg.Router },
			"inline router": func(reg *apimodule.HTTPRegistrar) chi.Router { return reg.With(mw) },
			"group router": func(reg *apimodule.HTTPRegistrar) chi.Router {
				var g chi.Router
				reg.Group(func(r chi.Router) { g = r })
				return g
			},
		} {
			t.Run(method+"/"+where, func(t *testing.T) {
				live := chi.NewRouter()
				reg := apimodule.NewHTTPRegistrar(live, nil, nil, nil)
				r := router(reg)
				defer func() {
					refused, ok := recover().(*apimodule.UnsupportedRouterMethodError)
					if !ok || refused.Method != method {
						t.Fatalf("recovered %#v, want *UnsupportedRouterMethodError{%s}", refused, method)
					}
					for _, want := range []string{method, "Handle", "/prefix/*"} {
						if !strings.Contains(refused.Error(), want) {
							t.Fatalf("message %q does not name %q", refused.Error(), want)
						}
					}
					if len(live.Routes()) != 0 || len(live.Middlewares()) != 0 {
						t.Fatal("a refused call still reached the live router")
					}
				}()
				call(r)
			})
		}
	}
}
