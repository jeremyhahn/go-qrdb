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

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// bearerStubAuthenticator authenticates any request whose Authorization
// carrier equals token — the shape of the embedder-supplied OAuth 2.1
// validator this transport's hooks exist for. qrdb itself ships no such
// validator; the stub exists only to exercise the transport plumbing.
type bearerStubAuthenticator struct{ token string }

func (b *bearerStubAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	if auth.HTTPAuthorizationFromContext(ctx) != b.token {
		return nil, auth.ErrUnauthenticated
	}
	return &auth.Identity{Subject: "oauth-subject", TenantID: "t1"}, nil
}

func (b *bearerStubAuthenticator) Name() string { return "bearer-stub" }

// newBearerTestServer builds a Server whose middleware accepts exactly one
// bearer token, with the given challenge configured.
func newBearerTestServer(t *testing.T, challenge string) *Server {
	t.Helper()
	mw := authmw.New(&auth.NoopAuthorizer{},
		authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}))
	s := &Server{}
	WithAuthMiddleware(mw)(s)
	WithBearerChallenge(challenge)(s)
	return s
}

// TestBearerChallengeHandler_Unauthenticated401 verifies the MCP
// authorization discovery entry point: a request no authenticator accepts is
// answered 401 with the embedder-composed WWW-Authenticate value and never
// reaches the MCP handler.
func TestBearerChallengeHandler_Unauthenticated401(t *testing.T) {
	const challenge = `Bearer realm="mcp", resource_metadata="https://h/.well-known/oauth-protected-resource"`
	s := newBearerTestServer(t, challenge)

	reached := false
	h := s.bearerChallengeHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != challenge {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, challenge)
	}
	if reached {
		t.Fatal("unauthenticated request must not reach the MCP handler")
	}
}

// TestBearerChallengeHandler_AuthenticatedPassesIdentity verifies an
// accepted credential proceeds to the MCP handler with the identity already
// bound into the request context, and that authHTTPContext then reuses it
// instead of re-authenticating.
func TestBearerChallengeHandler_AuthenticatedPassesIdentity(t *testing.T) {
	s := newBearerTestServer(t, "Bearer realm=\"mcp\"")

	var seen *auth.Identity
	h := s.bearerChallengeHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = auth.IdentityFromContext(r.Context())
		// authHTTPContext must short-circuit on the pre-bound identity.
		ctx := s.authHTTPContext(r.Context(), r)
		if auth.IdentityFromContext(ctx) != seen {
			t.Error("authHTTPContext must reuse the pre-authenticated identity")
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if seen == nil || seen.Subject != "oauth-subject" {
		t.Fatalf("handler identity = %+v, want oauth-subject", seen)
	}
}

// TestAuthHTTPContext_CarriesAuthorizationHeader verifies the raw
// Authorization header reaches the authenticator chain through the request
// context even without the challenge wrapper (mTLS-only deployments that
// still register a bearer authenticator).
func TestAuthHTTPContext_CarriesAuthorizationHeader(t *testing.T) {
	s := newBearerTestServer(t, "")

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	ctx := s.authHTTPContext(context.Background(), req)
	id := auth.IdentityFromContext(ctx)
	if id == nil || id.Subject != "oauth-subject" {
		t.Fatalf("identity = %+v, want oauth-subject via bearer authenticator", id)
	}
}

// TestProtectedResourceMetadataHandler_ServesDocument verifies the RFC 9728
// document is served verbatim on GET with the JSON content type.
func TestProtectedResourceMetadataHandler_ServesDocument(t *testing.T) {
	doc := `{"resource":"https://h/mcp","authorization_servers":["https://as"]}`
	s := &Server{}
	WithProtectedResourceMetadata([]byte(doc))(s)

	rec := httptest.NewRecorder()
	s.protectedResourceMetadataHandler(rec, httptest.NewRequest(http.MethodGet, ProtectedResourceMetadataPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if body := rec.Body.String(); body != doc {
		t.Fatalf("body = %q, want the embedder document verbatim", body)
	}
}

// TestProtectedResourceMetadataHandler_MethodGuard verifies non-GET/HEAD
// methods are refused with 405 and an Allow header.
func TestProtectedResourceMetadataHandler_MethodGuard(t *testing.T) {
	s := &Server{}
	WithProtectedResourceMetadata([]byte(`{}`))(s)

	rec := httptest.NewRecorder()
	s.protectedResourceMetadataHandler(rec, httptest.NewRequest(http.MethodPost, ProtectedResourceMetadataPath, nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Fatalf("Allow = %q, want it to contain GET", allow)
	}
}

// TestWithProtectedResourceMetadata_EmptyIgnored verifies an empty document
// leaves the option unset (Start then does not register the endpoint).
func TestWithProtectedResourceMetadata_EmptyIgnored(t *testing.T) {
	s := &Server{}
	WithProtectedResourceMetadata(nil)(s)
	if s.protectedResourceMetadata != nil {
		t.Fatal("empty document must leave protectedResourceMetadata unset")
	}
}
