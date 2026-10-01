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
		collision, ok := r.(*RESTRouteCollisionError)
		if !ok || collision.Method != http.MethodPost || collision.Pattern != "/api/v1/auth/login/piv" {
			t.Fatalf("panic value: %#v, want *RESTRouteCollisionError naming the route", r)
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

// TestRESTMiddleware_ModulePublicRoute_SkipsAuthentication proves a
// module-declared public route (HandlePublic → RESTRoutePublic) serves
// anonymously even when every authenticator would reject: first-contact
// ceremonies (WebAuthn registration/login) must not demand a peer
// certificate just because client_auth is enabled.
func TestRESTMiddleware_ModulePublicRoute_SkipsAuthentication(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	rr := NewRESTRouteRegistry()
	rr.RegisterPublic(http.MethodPost, "/api/v1/webauthn/login/begin")

	served := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(rr, next)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webauthn/login/begin", nil))
	if !served || w.Code != http.StatusOK {
		t.Fatalf("module public route must serve anonymously: served=%v code=%d", served, w.Code)
	}
	if authn.calls.Load() != 0 {
		t.Fatalf("authenticator consulted %d times for a public route", authn.calls.Load())
	}
}

// fallbackRoutesMultiVerb builds a registry where ONE module owns two verbs of
// a path (POST and GET) and a third verb (PUT) is registered nowhere. This is
// the go-qrdb#211 shape: the caller's credentials are ones the module can
// authenticate, and the only thing wrong with the request is its verb.
func fallbackRoutesMultiVerb(fb RESTAuthFallback) *RESTRouteRegistry {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodPost, "/api/v1/keys", permission.Permission("keys:create"))
	rr.SetFallback(http.MethodPost, "/api/v1/keys", fb)
	return rr
}

// TestRESTMiddleware_Fallback_WrongVerbAuthenticable_Is405 is the go-qrdb#211
// fix: a caller the owning module CAN authenticate, sending a verb that path
// does not serve, is told about the verb rather than about its credentials.
//
// The old behaviour answered 401 because FallbackFor was method-keyed: PUT
// matched no route, so the lookup found no fallback and the request was
// refused with a perfectly valid module token in hand. A 401 is a worse
// signpost than the 403 go-qrdb#185 replaced -- 403 at least says "you got
// in", while 401 sends the operator off to re-issue a token or suspect clock
// skew, an arbitrarily long detour from a wrong verb.
func TestRESTMiddleware_Fallback_WrongVerbAuthenticable_Is405(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	fb := &mockFallback{identity: &auth.Identity{Subject: "alice", TenantID: "t1"}}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler must not be reached: the middleware still fails closed")
	})
	handler := m.RESTMiddleware(fallbackRoutesMultiVerb(fb), next)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/keys", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405: a module-authenticable caller with a wrong "+
			"verb must be told about the verb, not about its credentials (body=%s)",
			w.Code, w.Body.String())
	}
	if allow := w.Header().Get("Allow"); allow != http.MethodPost {
		t.Errorf("Allow = %q, want %q", allow, http.MethodPost)
	}
	if fb.calls != 1 {
		t.Errorf("fallback calls = %d, want 1: the path's owner is consulted exactly "+
			"once for a verb it did not register", fb.calls)
	}
}

// TestRESTMiddleware_Fallback_WrongVerbUnauthenticated_Is401 is the disclosure
// guarantee for the widened lookup, and the assertion that must not regress: a
// caller the module CANNOT authenticate still gets 401 with no Allow header, so
// widening the fallback lookup did not move the 405 above authentication.
func TestRESTMiddleware_Fallback_WrongVerbUnauthenticated_Is401(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	fb := &mockFallback{err: errors.New("token expired")}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler must not be reached")
	})
	handler := m.RESTMiddleware(fallbackRoutesMultiVerb(fb), next)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/keys", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: the 405 stays downstream of authentication", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != "" {
		t.Errorf("Allow = %q on a 401: a caller that did not authenticate must not "+
			"learn which verbs a path serves", allow)
	}
}

// TestRESTRouteRegistry_FallbackFor_WidensOnlyWhenMethodUnregistered pins the
// exact-match-first half of the rule. A registered route whose owner declared
// NO fallback must keep answering nil: widening there would let one module's
// auth plane establish the identity a different owner's handler is then
// authorized and dispatched for -- an authorization consequence, unlike the
// widened case, which can only ever end in 405 or 401.
func TestRESTRouteRegistry_FallbackFor_WidensOnlyWhenMethodUnregistered(t *testing.T) {
	fb := &mockFallback{identity: &auth.Identity{Subject: "alice"}}
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodPost, "/api/v1/keys", permission.Permission("keys:create"))
	rr.SetFallback(http.MethodPost, "/api/v1/keys", fb)
	// A second module (or the built-in surface) owns GET on the same path and
	// brought no fallback of its own.
	rr.Register(http.MethodGet, "/api/v1/keys", permission.Permission("keys:read"))

	if got := rr.FallbackFor(http.MethodPost, "/api/v1/keys"); got != RESTAuthFallback(fb) {
		t.Errorf("FallbackFor(POST) = %v, want the registered fallback", got)
	}
	if got := rr.FallbackFor(http.MethodGet, "/api/v1/keys"); got != nil {
		t.Errorf("FallbackFor(GET) = %v, want nil: GET is registered and its owner "+
			"declared no fallback", got)
	}
	if got := rr.FallbackFor(http.MethodPut, "/api/v1/keys"); got != RESTAuthFallback(fb) {
		t.Errorf("FallbackFor(PUT) = %v, want the path's fallback: PUT matches no "+
			"route, so the lookup widens to the path's owner", got)
	}
	// A path with no fallback on any method is unchanged.
	rr.Register(http.MethodGet, "/status", permission.AdminStatus)
	if got := rr.FallbackFor(http.MethodDelete, "/status"); got != nil {
		t.Errorf("FallbackFor(DELETE, /status) = %v, want nil", got)
	}
	// Off the registered surface entirely: still nil, not a panic.
	if got := rr.FallbackFor(http.MethodGet, "/static/logo.png"); got != nil {
		t.Errorf("FallbackFor on an unregistered path = %v, want nil", got)
	}
	var nilRR *RESTRouteRegistry
	if got := nilRR.FallbackFor(http.MethodPut, "/api/v1/keys"); got != nil {
		t.Errorf("nil registry FallbackFor = %v, want nil", got)
	}
}

// TestRESTRouteRegistry_FallbackFor_AmbiguousPathResolvesToNone pins the
// deliberate choice for the contested case. entries is keyed
// method + " " + pattern and so is the collision guard, so two modules may
// split one path's verbs between them and bring one auth plane each. Which of
// them owns a verb NEITHER registered is unanswerable, so the registry answers
// none and the caller keeps its 401 -- the conservative half of a change whose
// whole benefit is a clearer status code.
func TestRESTRouteRegistry_FallbackFor_AmbiguousPathResolvesToNone(t *testing.T) {
	fbA := &mockFallback{identity: &auth.Identity{Subject: "from-module-a"}}
	fbB := &mockFallback{identity: &auth.Identity{Subject: "from-module-b"}}

	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/api/v1/keys", permission.Permission("keys:read"))
	rr.SetFallback(http.MethodGet, "/api/v1/keys", fbA)
	rr.Register(http.MethodPost, "/api/v1/keys", permission.Permission("keys:create"))
	rr.SetFallback(http.MethodPost, "/api/v1/keys", fbB)

	// Each registered verb still resolves to its own owner...
	if got := rr.FallbackFor(http.MethodGet, "/api/v1/keys"); got != RESTAuthFallback(fbA) {
		t.Errorf("FallbackFor(GET) = %v, want module A's fallback", got)
	}
	if got := rr.FallbackFor(http.MethodPost, "/api/v1/keys"); got != RESTAuthFallback(fbB) {
		t.Errorf("FallbackFor(POST) = %v, want module B's fallback", got)
	}
	// ...but an unregistered verb belongs to neither.
	if got := rr.FallbackFor(http.MethodPut, "/api/v1/keys"); got != nil {
		t.Errorf("FallbackFor(PUT) = %v, want nil: two owners, no answer", got)
	}
	if fbA.calls != 0 || fbB.calls != 0 {
		t.Errorf("resolution consulted a fallback (%d/%d calls); it must only select one",
			fbA.calls, fbB.calls)
	}
}

// nonComparableFallback has a non-comparable dynamic type (a map field). Two
// interface values holding it would panic under ==, which is why sameFallback
// exists; the registry must answer, not crash.
type nonComparableFallback struct {
	tokens map[string]string
}

func (nonComparableFallback) AuthenticateHTTP(*http.Request) (*auth.Identity, error) {
	return nil, errors.New("not authenticated")
}

func TestRESTRouteRegistry_FallbackFor_NonComparableFallbacksDoNotPanic(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/api/v1/keys", permission.Permission("keys:read"))
	rr.SetFallback(http.MethodGet, "/api/v1/keys", nonComparableFallback{tokens: map[string]string{}})
	rr.Register(http.MethodPost, "/api/v1/keys", permission.Permission("keys:create"))
	rr.SetFallback(http.MethodPost, "/api/v1/keys", nonComparableFallback{tokens: map[string]string{}})

	// Unprovable equality is reported as ambiguity: nil, not a panic.
	if got := rr.FallbackFor(http.MethodPut, "/api/v1/keys"); got != nil {
		t.Errorf("FallbackFor(PUT) = %v, want nil", got)
	}
}
