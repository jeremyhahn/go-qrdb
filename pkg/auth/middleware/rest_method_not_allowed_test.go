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

// This file carries NO build constraint, and every helper it uses is defined
// in a file that carries none either (middleware_test.go,
// rest_routes_helper_test.go). See go-qrdb#178: a test file whose helpers sit
// behind a constraint its own is not a subset of breaks the package under
// every tag set that drops them.

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// A known path with an unregistered verb is a METHOD problem, not an
// AUTHORIZATION problem, and the response must say so (go-qrdb#185). The old
// behaviour answered 403 "route not authorized (no permission mapping)",
// which asserts an authorization decision about a route that was never
// registered and points the caller at their credentials instead of their verb.
func TestRESTMiddleware_WrongVerbOnKnownPath_Is405WithAllow(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	m := New(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be reached: the middleware still fails closed")
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	// /kv is registered for GET, PUT and DELETE. PATCH is not.
	req := httptest.NewRequest(http.MethodPatch, "/kv", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (a wrong verb on a known path is not an "+
			"authorization failure)", rec.Code)
	}

	// RFC 9110 §15.5.6 makes Allow mandatory on a 405.
	allow := rec.Header().Get("Allow")
	if allow == "" {
		t.Fatal("405 response carries no Allow header; RFC 9110 §15.5.6 requires one")
	}
	for _, want := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if !strings.Contains(allow, want) {
			t.Errorf("Allow = %q, missing registered method %s", allow, want)
		}
	}
	if strings.Contains(allow, http.MethodPatch) {
		t.Errorf("Allow = %q lists PATCH, which is not registered for /kv", allow)
	}

	var body restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	// The message must name the verb, not the caller's authorization. Asserting
	// the absence is the half that would have saved the go-quicbaas#270
	// investigation.
	if !strings.Contains(body.Error, http.MethodPatch) {
		t.Errorf("error message %q does not name the offending verb", body.Error)
	}
	if strings.Contains(strings.ToLower(body.Error), "not authorized") ||
		strings.Contains(strings.ToLower(body.Error), "permission") {
		t.Errorf("error message %q still blames authorization for a method problem", body.Error)
	}
}

// The 405 must never be reachable without authentication: the Allow header
// enumerates a path's registered verbs, and an anonymous caller must learn
// nothing from it. This is the property that makes disclosing the verb set
// safe at all, so it is asserted rather than assumed.
func TestRESTMiddleware_WrongVerbUnauthenticated_Is401AndDisclosesNothing(t *testing.T) {
	authn := &mockAuthenticator{
		name: "mtls",
		err:  &auth.AuthError{Code: "unauthenticated", Message: "no client certificate"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be reached")
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest(http.MethodPatch, "/kv", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: authentication is evaluated before the "+
			"method check, so an anonymous caller never reaches the 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "" {
		t.Errorf("Allow = %q on a 401: an unauthenticated caller must not learn "+
			"which verbs a path serves", allow)
	}
}

// The authenticated-but-unauthorized case is untouched: still 403, still
// worded as an authorization decision, because that is what it is.
func TestRESTMiddleware_AuthenticatedButDenied_Is403(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authz := &mockAuthorizer{err: &auth.AuthError{Code: "unauthorized", Message: "permission denied"}}
	m := New(authz, WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be reached")
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	// GET /kv IS registered, so this reaches the authorizer rather than the
	// method check.
	req := httptest.NewRequest(http.MethodGet, "/kv", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a genuine authorization denial", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "" {
		t.Errorf("Allow = %q on a 403: this is not a method problem", allow)
	}
}

// A path outside the registered API surface still falls through to the
// router's own fallback (the embedded web UI / static assets). The 405 branch
// must not have widened to swallow it.
func TestRESTMiddleware_UnregisteredPath_StillFallsThrough(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	m := New(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}))

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest(http.MethodPatch, "/static/logo.png", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("a path outside the registered API surface must reach the router fallback")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 from the fallback handler", rec.Code)
	}
}

func TestRESTRouteRegistry_MethodsForPath(t *testing.T) {
	rr := testRESTRoutes()

	got := rr.MethodsForPath("/kv")
	want := map[string]bool{http.MethodGet: true, http.MethodPut: true, http.MethodDelete: true}
	for _, m := range got {
		if !want[m] {
			t.Errorf("MethodsForPath(/kv) reported unregistered method %s", m)
		}
		delete(want, m)
	}
	if len(want) != 0 {
		t.Errorf("MethodsForPath(/kv) = %v, missing %v", got, want)
	}

	// Ordering is restMethodProbeOrder, so an Allow header is stable across
	// runs rather than map-iteration order.
	if second := rr.MethodsForPath("/kv"); strings.Join(second, ",") != strings.Join(got, ",") {
		t.Errorf("MethodsForPath is not order-stable: %v then %v", got, second)
	}

	// A single-verb route reports exactly that verb.
	if got := rr.MethodsForPath("/v1/barrier/rekey"); len(got) != 1 || got[0] != http.MethodPost {
		t.Errorf("MethodsForPath(/v1/barrier/rekey) = %v, want [POST]", got)
	}

	// Off the API surface: nil, which is what makes the middleware's
	// len(...) > 0 check the same predicate PathHasRoutes answered.
	for _, p := range []string{"/", "/favicon.ico", "/static/logo.png"} {
		if got := rr.MethodsForPath(p); len(got) != 0 {
			t.Errorf("MethodsForPath(%q) = %v, want none", p, got)
		}
		if rr.PathHasRoutes(p) {
			t.Errorf("PathHasRoutes(%q) disagrees with MethodsForPath", p)
		}
	}

	// A nil registry answers nil rather than panicking, matching PathHasRoutes.
	var nilRR *RESTRouteRegistry
	if got := nilRR.MethodsForPath("/kv"); got != nil {
		t.Errorf("nil registry MethodsForPath = %v, want nil", got)
	}
}
