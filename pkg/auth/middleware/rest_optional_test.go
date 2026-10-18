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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// credentialAuthenticator authenticates by what the request actually carries,
// as the real chain does: a peer certificate whose CN is "bob", or the bearer
// token "good". Any other credential is refused, and no credential is
// auth.ErrUnauthenticated.
type credentialAuthenticator struct {
	calls atomic.Int64
}

var errBadCredential = errors.New("test: credential refused")

func (c *credentialAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	c.calls.Add(1)
	if st := auth.TLSStateFromContext(ctx); st != nil && len(st.PeerCertificates) > 0 {
		if st.PeerCertificates[0].Subject.CommonName == "bob" {
			return &auth.Identity{Subject: "bob", TenantID: "t1"}, nil
		}
		return nil, auth.ErrUnauthenticated
	}
	switch auth.HTTPAuthorizationFromContext(ctx) {
	case "":
		return nil, auth.ErrUnauthenticated
	case "Bearer good":
		return &auth.Identity{Subject: "alice", TenantID: "t1"}, nil
	default:
		return nil, errBadCredential
	}
}

func (c *credentialAuthenticator) Name() string { return "credential" }

const (
	optionalPath      = "/v1/oauth/authorize"
	authenticatedPath = "/v1/oauth/consent"
	permissionPath    = "/v1/oauth/clients"
	publicPath        = "/v1/oauth/jwks"
)

// optionalRoutes registers one route of each mode on the same verb.
func optionalRoutes() *RESTRouteRegistry {
	rr := NewRESTRouteRegistry()
	rr.RegisterOptional(http.MethodGet, optionalPath)
	rr.RegisterAuthenticated(http.MethodGet, authenticatedPath)
	rr.Register(http.MethodGet, permissionPath, permission.KVGet)
	rr.RegisterPublic(http.MethodGet, publicPath)
	return rr
}

// optionalResult is what the handler saw, and what the client got back.
type optionalResult struct {
	served   bool
	identity *auth.Identity
	code     int
	body     string
}

func serveOptional(t *testing.T, path string, mutate func(*http.Request)) (optionalResult, *credentialAuthenticator) {
	t.Helper()
	authn := &credentialAuthenticator{}
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(authn))

	var res optionalResult
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res.served = true
		res.identity = auth.IdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	m.RESTMiddleware(optionalRoutes(), next).ServeHTTP(w, req)
	res.code, res.body = w.Code, w.Body.String()
	return res, authn
}

func withBearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func withPeerCert(cn string) func(*http.Request) {
	return func(r *http.Request) {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
		r.TLS.PeerCertificates[0].Subject.CommonName = cn
	}
}

func TestRESTMiddleware_OptionalRoute_NoCredentialIsAnonymous(t *testing.T) {
	res, authn := serveOptional(t, optionalPath, nil)
	if !res.served || res.code != http.StatusOK {
		t.Fatalf("anonymous caller not admitted: code=%d body=%s", res.code, res.body)
	}
	if res.identity != nil {
		t.Fatalf("anonymous caller carries identity %+v, want none", res.identity)
	}
	if n := authn.calls.Load(); n != 0 {
		t.Fatalf("authenticator consulted %d times for a request with no credential", n)
	}
}

func TestRESTMiddleware_OptionalRoute_TLSWithoutPeerCertIsAnonymous(t *testing.T) {
	res, _ := serveOptional(t, optionalPath, func(r *http.Request) { r.TLS = &tls.ConnectionState{} })
	if !res.served || res.identity != nil {
		t.Fatalf("a TLS request presenting no certificate must be anonymous: served=%v identity=%+v", res.served, res.identity)
	}
}

func TestRESTMiddleware_OptionalRoute_ValidCredentialIsAuthenticated(t *testing.T) {
	cases := map[string]struct {
		mutate  func(*http.Request)
		subject string
	}{
		"bearer": {mutate: withBearer("good"), subject: "alice"},
		"mtls":   {mutate: withPeerCert("bob"), subject: "bob"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, authn := serveOptional(t, optionalPath, tc.mutate)
			if !res.served || res.code != http.StatusOK {
				t.Fatalf("authenticated caller not admitted: code=%d body=%s", res.code, res.body)
			}
			if res.identity == nil || res.identity.Subject != tc.subject {
				t.Fatalf("handler identity = %+v, want %s", res.identity, tc.subject)
			}
			if authn.calls.Load() != 1 {
				t.Fatalf("authenticator calls = %d, want 1", authn.calls.Load())
			}
		})
	}
}

// TestRESTMiddleware_OptionalRoute_InvalidCredentialRejectedAsOnHandle pins
// the refusal byte for byte against the same request on an authenticated and
// on a permission route: an optional route never downgrades a bad credential
// to anonymous, and refuses it in the same words.
func TestRESTMiddleware_OptionalRoute_InvalidCredentialRejectedAsOnHandle(t *testing.T) {
	cases := map[string]func(*http.Request){
		"bad bearer":     withBearer("forged"),
		"untrusted cert": withPeerCert("mallory"),
		"half assertion": func(r *http.Request) { r.Header.Set(forward.HeaderAssertion, "x") },
		"dpop only":      func(r *http.Request) { r.Header.Set("DPoP", "x") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			got, _ := serveOptional(t, optionalPath, mutate)
			if got.served {
				t.Fatal("an invalid credential reached the handler")
			}
			if got.code < 400 {
				t.Fatalf("code = %d, want a refusal", got.code)
			}
			for _, path := range []string{authenticatedPath, permissionPath} {
				want, _ := serveOptional(t, path, mutate)
				if got.code != want.code || got.body != want.body {
					t.Fatalf("optional refusal %d %q differs from %s refusal %d %q",
						got.code, got.body, path, want.code, want.body)
				}
			}
		})
	}
}

// TestRESTMiddleware_OptionalRoute_LeavesOtherModesUnchanged pins that an
// anonymous request is still refused on an authenticated and a permission
// route, and that a public route still skips authentication entirely.
func TestRESTMiddleware_OptionalRoute_LeavesOtherModesUnchanged(t *testing.T) {
	for _, path := range []string{authenticatedPath, permissionPath} {
		res, _ := serveOptional(t, path, nil)
		if res.served || res.code != http.StatusUnauthorized {
			t.Fatalf("%s anonymous: served=%v code=%d, want 401", path, res.served, res.code)
		}
	}
	res, authn := serveOptional(t, publicPath, withBearer("forged"))
	if !res.served || res.identity != nil || authn.calls.Load() != 0 {
		t.Fatalf("public route: served=%v identity=%+v calls=%d, want served unauthenticated",
			res.served, res.identity, authn.calls.Load())
	}
}

func TestRESTMiddleware_OptionalRoute_ModuleFallbackAuthenticates(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	fb := &mockFallback{identity: &auth.Identity{Subject: "carol", TenantID: "t1"}}
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(authn))
	rr := NewRESTRouteRegistry()
	rr.RegisterOptional(http.MethodGet, optionalPath)
	rr.SetFallback(http.MethodGet, optionalPath, fb)

	var id *auth.Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id = auth.IdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, optionalPath, nil)
	req.Header.Set("Authorization", "Bearer module-token")
	w := httptest.NewRecorder()
	m.RESTMiddleware(rr, next).ServeHTTP(w, req)
	if w.Code != http.StatusOK || id == nil || id.Subject != "carol" {
		t.Fatalf("code=%d identity=%+v, want the module fallback's carol", w.Code, id)
	}
}

func TestRESTRouteRegistry_RegisterOptional(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.RegisterOptional(http.MethodGet, "/v1/x/{id}")
	perm, kind, ok := rr.Lookup(http.MethodGet, "/v1/x/42")
	if !ok || kind != RESTRouteOptional || perm != "" {
		t.Fatalf("Lookup = (%q, %v, %v), want an optional route with no permission", perm, kind, ok)
	}
	if !rr.Covered(http.MethodGet, "/v1/x/{id}") {
		t.Fatal("an optional route must count as covered for the default-deny assertion")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registering the same optional route twice must panic")
		}
	}()
	rr.RegisterOptional(http.MethodGet, "/v1/x/{name}")
}
