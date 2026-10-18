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
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// go-qrdb#194: the registry's notion of "the same route" must be chi's, or the
// collision guard misses collisions chi resolves silently.

func TestCanonicalRESTPattern(t *testing.T) {
	cases := map[string]string{
		"/kv":                         "/kv",
		"/a/{id}":                     "/a/{}",
		"/a/{deviceID}":               "/a/{}",
		"/a/{id:[0-9]+}":              "/a/{:[0-9]+}",
		"/a/{id:[0-9]{3}}/b":          "/a/{:[0-9]{3}}/b",
		"/t/{tenant}/u/{user}/x":      "/t/{}/u/{}/x",
		"/files/*":                    "/files/*",
		"/v1/{id}.json":               "/v1/{}.json",
		"/broken/{id":                 "/broken/{id",
		"/mixed/{a}/{b:[a-z]+}/{c}/*": "/mixed/{}/{:[a-z]+}/{}/*",
	}
	for in, want := range cases {
		if got := canonicalRESTPattern(in); got != want {
			t.Errorf("canonicalRESTPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChiTreatsRenamedParamsAsOneRoute is the ground truth the key is built
// on, measured rather than assumed: chi replaces the first handler with the
// second when only the parameter name differs, and keeps two routes when the
// regexp constraint differs. If chi ever changes either behaviour this test
// says so before the guard silently goes wrong.
func TestChiTreatsRenamedParamsAsOneRoute(t *testing.T) {
	mux := chi.NewRouter()
	mux.Get("/a/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Get("/a/{deviceID}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/xyz", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("chi served %d; the renamed-parameter route did not replace the first", rec.Code)
	}
	if RESTRouteKey(http.MethodGet, "/a/{id}") != RESTRouteKey(http.MethodGet, "/a/{deviceID}") {
		t.Fatal("RESTRouteKey distinguishes two patterns chi treats as one route")
	}

	mux = chi.NewRouter()
	mux.Get("/b/{id:[0-9]+}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Get("/b/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	for path, want := range map[string]int{"/b/42": http.StatusOK, "/b/abc": http.StatusTeapot} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Fatalf("chi served %d for %s, want %d: a constrained parameter is a distinct route", rec.Code, path, want)
		}
	}
	if RESTRouteKey(http.MethodGet, "/b/{id:[0-9]+}") == RESTRouteKey(http.MethodGet, "/b/{id}") {
		t.Fatal("RESTRouteKey merges two patterns chi routes separately")
	}
}

func TestRESTRouteRegistry_RenamedParameterIsADuplicate(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/a/{id}", permission.KVGet)
	defer func() {
		collision, ok := recover().(*RESTRouteCollisionError)
		if !ok {
			t.Fatal("renamed-parameter re-registration did not panic with *RESTRouteCollisionError")
		}
		if collision.Pattern != "/a/{deviceID}" || collision.Existing != "/a/{id}" {
			t.Fatalf("collision = %+v, want both spellings", collision)
		}
		if msg := collision.Error(); !strings.Contains(msg, "/a/{deviceID}") || !strings.Contains(msg, "/a/{id}") {
			t.Fatalf("message %q does not name both spellings", msg)
		}
	}()
	rr.Register(http.MethodGet, "/a/{deviceID}", permission.KVPut)
}

func TestRESTRouteRegistry_ConstrainedParameterIsDistinct(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/b/{id:[0-9]+}", permission.KVGet)
	rr.Register(http.MethodGet, "/b/{id}", permission.KVPut)
	if perm, _, ok := rr.Lookup(http.MethodGet, "/b/42"); !ok || perm != permission.KVGet {
		t.Fatalf("Lookup(/b/42) = (%q, %v), want the constrained route's permission", perm, ok)
	}
	if perm, _, ok := rr.Lookup(http.MethodGet, "/b/abc"); !ok || perm != permission.KVPut {
		t.Fatalf("Lookup(/b/abc) = (%q, %v), want the unconstrained route's permission", perm, ok)
	}
}

// TestRESTRouteRegistry_CanonicalKeyedLookups: Covered and SetFallback accept
// any spelling of a registered route, because they describe the route and not
// the string.
func TestRESTRouteRegistry_CanonicalKeyedLookups(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/c/{id}", permission.KVGet)
	if !rr.Covered(http.MethodGet, "/c/{other}") {
		t.Fatal("Covered does not recognise a renamed spelling of a registered route")
	}
	fb := &mockFallback{}
	rr.SetFallback(http.MethodGet, "/c/{other}", fb)
	if got := rr.FallbackFor(http.MethodGet, "/c/xyz"); got != fb {
		t.Fatalf("FallbackFor = %v, want the fallback attached under a renamed spelling", got)
	}
}

// TestRESTRouteRegistry_ModulePrefixes: a registered module subtree covers
// every path beneath it and nothing that merely shares a string prefix.
func TestRESTRouteRegistry_ModulePrefixes(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.RegisterModulePrefix("/v1/devices/")
	rr.RegisterModulePrefix("/v1/devices/") // duplicates are harmless
	rr.RegisterModulePrefix("")             // ignored
	cases := map[string]bool{
		"/v1/devices/":      true,
		"/v1/devices/a":     true,
		"/v1/devices/a/b/c": true,
		"/v1/devices":       false,
		"/v1/devicesX/a":    false,
		"/":                 false,
		"/ui/index.html":    false,
	}
	for path, want := range cases {
		if got := rr.UnderModulePrefix(path); got != want {
			t.Errorf("UnderModulePrefix(%q) = %v, want %v", path, got, want)
		}
	}
	if len(rr.modulePrefixes) != 1 {
		t.Fatalf("modulePrefixes = %v, want one entry", rr.modulePrefixes)
	}
	var nilRR *RESTRouteRegistry
	if nilRR.UnderModulePrefix("/v1/devices/a") {
		t.Fatal("a nil registry claims a prefix")
	}
}

// TestRESTMiddleware_UnmatchedPathUnderModulePrefixRefused: the fallthrough to
// the router's fallback is closed for a module-owned subtree, and still open
// everywhere else (the web UI).
func TestRESTMiddleware_UnmatchedPathUnderModulePrefixRefused(t *testing.T) {
	routes := NewRESTRouteRegistry()
	routes.Register(http.MethodGet, "/v1/devices/{id}", permission.KVGet)
	routes.RegisterModulePrefix("/v1/devices/")
	svc := newRealUserStore(t)
	u := createUser(t, svc, "acme", "bob", []string{"operator"})
	ck := mintCert(t, "bob", "acme", nil, "")
	enroll(t, svc, u.ID, ck)
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(auth.NewMTLSAuthenticator()), WithUserService(svc))
	reached := 0
	h := m.RESTMiddleware(routes, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusTeapot)
	}))
	serve := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{ck.cert}, VerifiedChains: [][]*x509.Certificate{{ck.cert}}}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := serve("/v1/devices/a/b"); code != http.StatusNotFound || reached != 0 {
		t.Fatalf("unmatched module path: status %d, handler reached %d time(s); want 404 and never", code, reached)
	}
	if code := serve("/ui/index.html"); code != http.StatusTeapot || reached != 1 {
		t.Fatalf("path outside every module prefix: status %d, reached %d; want the fallback", code, reached)
	}
}
