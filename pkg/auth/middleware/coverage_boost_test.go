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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// MCPToolRegistry lookup semantics. The exhaustive tool -> permission
// expectations live in pkg/api/mcp's tool_mapping_guard_test.go, which drives
// the real registry built by a fully wired server.
// ---------------------------------------------------------------------------

// testMCPToolRegistry builds a minimal registration-site registry for the
// AuthorizeMCP unit tests.
func testMCPToolRegistry() *MCPToolRegistry {
	r := NewMCPToolRegistry()
	r.Register("qrdb_put", permission.KVPut)
	r.Register("qrdb_get", permission.KVGet)
	r.Register("qrdb_delete", permission.KVDelete)
	r.Register("qrdb_list", permission.KVList)
	r.Register("qrdb_status", permission.AdminStatus)
	return r
}

func TestMCPToolRegistry_LookupFound(t *testing.T) {
	reg := testMCPToolRegistry()
	tests := []struct {
		tool string
		want permission.Permission
	}{
		{"qrdb_put", permission.KVPut},
		{"qrdb_get", permission.KVGet},
		{"qrdb_delete", permission.KVDelete},
		{"qrdb_list", permission.KVList},
		{"qrdb_status", permission.AdminStatus},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			p, ok := reg.Lookup(tc.tool)
			if !ok {
				t.Fatalf("Lookup(%q): not found", tc.tool)
			}
			if p != tc.want {
				t.Errorf("Lookup(%q) = %v, want %v", tc.tool, p, tc.want)
			}
		})
	}
}

func TestMCPToolRegistry_LookupNotFound(t *testing.T) {
	reg := testMCPToolRegistry()
	if _, ok := reg.Lookup("qrdb_nonexistent_tool_xyz"); ok {
		t.Error("expected not-found for unknown tool")
	}
	if reg.Covered("qrdb_nonexistent_tool_xyz") {
		t.Error("expected Covered to be false for unknown tool")
	}
}

// ---------------------------------------------------------------------------
// AuthorizeMCP
// ---------------------------------------------------------------------------

func TestAuthorizeMCP_NoIdentity_Denied(t *testing.T) {
	m := New(&mockAuthorizer{})
	ctx := context.Background()
	err := m.AuthorizeMCP(ctx, testMCPToolRegistry(), "qrdb_kv_put", nil)
	if err == nil {
		t.Fatal("expected error when no identity in context")
	}
	var authErr *auth.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *auth.AuthError, got %T: %v", err, err)
	}
	if authErr.Code != "unauthenticated" {
		t.Errorf("Code: got %q, want unauthenticated", authErr.Code)
	}
}

func TestAuthorizeMCP_UnmappedTool_Denied(t *testing.T) {
	m := New(&mockAuthorizer{})
	identity := &auth.Identity{
		Subject:  "u1",
		TenantID: "t1",
		Roles:    []string{"admin"},
	}
	ctx := auth.WithIdentity(context.Background(), identity)
	err := m.AuthorizeMCP(ctx, testMCPToolRegistry(), "qrdb_tool_that_does_not_exist", nil)
	if err == nil {
		t.Fatal("expected error for unmapped tool (default-deny)")
	}
	var authErr *auth.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *auth.AuthError, got %T: %v", err, err)
	}
	if authErr.Code != "unauthorized" {
		t.Errorf("Code: got %q, want unauthorized", authErr.Code)
	}
}

func TestAuthorizeMCP_AuthorizedCaller_Allowed(t *testing.T) {
	m := New(&mockAuthorizer{}) // nil err = allow
	identity := &auth.Identity{
		Subject:  "u1",
		TenantID: "t1",
		Roles:    []string{"admin"},
	}
	ctx := auth.WithIdentity(context.Background(), identity)
	if err := m.AuthorizeMCP(ctx, testMCPToolRegistry(), "qrdb_put", nil); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestAuthorizeMCP_AuthorizerDenied(t *testing.T) {
	denyErr := &auth.AuthError{Code: "unauthorized", Message: "permission denied"}
	m := New(&mockAuthorizer{err: denyErr})
	identity := &auth.Identity{
		Subject:  "u1",
		TenantID: "t1",
		Roles:    []string{"reader"},
	}
	ctx := auth.WithIdentity(context.Background(), identity)
	err := m.AuthorizeMCP(ctx, testMCPToolRegistry(), "qrdb_put", nil)
	if err == nil {
		t.Fatal("expected deny from authorizer")
	}
}

// ---------------------------------------------------------------------------
// AuthorizeTenant
// ---------------------------------------------------------------------------

func TestAuthorizeTenant_NoIdentity_Unauthenticated(t *testing.T) {
	m := New(&mockAuthorizer{})
	ctx := context.Background()
	err := m.AuthorizeTenant(ctx, "tenant-1", permission.KVPut)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got: %v", err)
	}
}

func TestAuthorizeTenant_AuthorizerDenied(t *testing.T) {
	denyErr := &auth.AuthError{Code: "unauthorized", Message: "not in tenant"}
	m := New(&mockAuthorizer{err: denyErr})
	identity := &auth.Identity{
		Subject:  "u1",
		TenantID: "other-tenant",
		Roles:    []string{"reader"},
	}
	ctx := auth.WithIdentity(context.Background(), identity)
	err := m.AuthorizeTenant(ctx, "tenant-1", permission.KVPut)
	if err == nil {
		t.Fatal("expected denial from authorizer")
	}
}

func TestAuthorizeTenant_AuthorizerAllowed(t *testing.T) {
	m := New(&mockAuthorizer{}) // nil err = allow
	identity := &auth.Identity{
		Subject:  "u1",
		TenantID: "tenant-1",
		Roles:    []string{"admin"},
	}
	ctx := auth.WithIdentity(context.Background(), identity)
	if err := m.AuthorizeTenant(ctx, "tenant-1", permission.KVPut); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
}

// The tenant injected into context must equal the targetTenantID argument.
func TestAuthorizeTenant_InjectsTenantInContext(t *testing.T) {
	var capturedTenantID string
	m := New(&captureAuthorizer{
		captureFn: func(ctx context.Context) {
			capturedTenantID = tenant.FromContext(ctx)
		},
	})
	identity := &auth.Identity{Subject: "u1", TenantID: "ctx-tenant"}
	ctx := auth.WithIdentity(context.Background(), identity)
	_ = m.AuthorizeTenant(ctx, "target-tenant", permission.KVPut)
	if capturedTenantID != "target-tenant" {
		t.Errorf("injected tenant = %q, want target-tenant", capturedTenantID)
	}
}

// ---------------------------------------------------------------------------
// RESTRouteRegistry — registration + coverage marker
// ---------------------------------------------------------------------------

func TestRESTRouteRegistry_RegisterAndCovered(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register("GET", "/v1/widgets/{id}", permission.AdminStatus)
	if !rr.Covered("GET", "/v1/widgets/{id}") {
		t.Error("registered route should be covered")
	}
	if rr.Covered("POST", "/v1/widgets/{id}") {
		t.Error("unregistered method should NOT be covered")
	}
}

func TestRESTRouteRegistry_PublicAndAuthenticatedKinds(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.RegisterPublic("GET", "/health")
	rr.RegisterAuthenticated("GET", "/metrics")

	if _, kind, ok := rr.Lookup("GET", "/health"); !ok || kind != RESTRoutePublic {
		t.Errorf("expected public kind for /health, ok=%v kind=%v", ok, kind)
	}
	if _, kind, ok := rr.Lookup("GET", "/metrics"); !ok || kind != RESTRouteAuthenticated {
		t.Errorf("expected authenticated kind for /metrics, ok=%v kind=%v", ok, kind)
	}
}

// ---------------------------------------------------------------------------
// isPublicGRPCMethod
// ---------------------------------------------------------------------------

func TestIsPublicGRPCMethod_Health(t *testing.T) {
	if !isPublicGRPCMethod("/grpc.health.v1.Health/Check") {
		t.Error("health method should be public")
	}
}

func TestIsPublicGRPCMethod_Reflection(t *testing.T) {
	if isPublicGRPCMethod("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo") {
		t.Error("reflection exposes the whole schema and must not be public by default")
	}
}

func TestIsPublicGRPCMethod_Application(t *testing.T) {
	if isPublicGRPCMethod("/qrdb.v1.QRDBService/Put") {
		t.Error("application method should NOT be public")
	}
}

// ---------------------------------------------------------------------------
// RESTRouteRegistry — path probes (the default-deny surface test)
// ---------------------------------------------------------------------------

func TestRESTRouteRegistry_PathHasRoutes(t *testing.T) {
	rr := testRESTRoutes()
	for _, p := range []string{"/kv", "/kv/mykey", "/status", "/metrics"} {
		if !rr.PathHasRoutes(p) {
			t.Errorf("expected %q to be on the registered API surface", p)
		}
	}
	for _, p := range []string{"/", "/favicon.ico", "/app.js", "/static/logo.png"} {
		if rr.PathHasRoutes(p) {
			t.Errorf("expected %q to NOT be on the registered API surface", p)
		}
	}
}

func TestRESTRouteRegistry_Lookup_NoMatch(t *testing.T) {
	rr := testRESTRoutes()
	if _, _, ok := rr.Lookup("DELETE", "/nonexistent/route/xyz/abc"); ok {
		t.Error("expected no match for completely unknown route")
	}
}

func TestRESTRouteRegistry_Lookup_EmptySegment(t *testing.T) {
	// A path with an empty parameter segment must not panic and must return a
	// well-formed result.
	rr := testRESTRoutes()
	_, _, ok := rr.Lookup("POST", "/v1/barrier/officers//claim/begin")
	_ = ok
}

// ---------------------------------------------------------------------------
// RESTMiddleware – route not on API surface passes through
// ---------------------------------------------------------------------------

func TestRESTMiddleware_NonAPIPath_PassThrough(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest("GET", "/static/app.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("next handler should have been called for non-API path")
	}
}

func TestRESTMiddleware_PublicPath_SkipsAuthn(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("next handler should be called for /health")
	}
	if authn.calls.Load() != 0 {
		t.Error("authn should NOT be called for /health")
	}
}

func TestRESTMiddleware_APIPath_NoPermissionMapping_DefaultDeny(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	// /status is a registered route (GET) but DELETE carries no registered
	// permission: default-deny must reject rather than pass through.
	req := httptest.NewRequest("DELETE", "/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// THE PROPERTY THIS TEST IS FOR is that the handler is not reached. It was
	// not asserted -- only the status code was -- so the test would have passed
	// with the request served and a 403 written by the handler itself. Asserting
	// it directly is what makes the status-code change below safe to make.
	if reached {
		t.Error("default-deny breached: the handler ran for an unregistered verb")
	}

	// 405, not 403 (go-qrdb#185). This assertion USED to be
	// `403 || 401`. That was a correct reading of the old behaviour and a
	// wrong statement of the invariant: a wrong verb on a known path is a
	// METHOD problem, and answering 403 asserts an authorization decision
	// about a route that was never registered. Failing closed did not change;
	// the claim the response makes did.
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for an unregistered verb on a known path, got %d", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow == "" {
		t.Error("405 carries no Allow header; RFC 9110 §15.5.6 requires one")
	}
}

func TestRESTMiddleware_AuthzPublicPath_Allowed(t *testing.T) {
	// /metrics is on the API surface but in the authz-public set; authenticated
	// callers should reach the handler without a specific permission.
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("next handler should be called for /metrics (authz-public)")
	}
}

// TestRESTMiddleware_AuthzPublicPath_RefusesHolderIdentity is the regression
// test for go-qrdb#560 review: a holder certificate (an officer's or
// custodian's, signed by the ceremony CA) carries no roles by design and is
// admitted only for its own narrow set of IdentityGated verbs -- never for
// an authenticated-only route like /metrics, which checks no permission at
// all. Contrast TestRESTMiddleware_AuthzPublicPath_Allowed immediately
// above: the SAME route, the SAME "authenticated is enough" contract, but
// refused here because THIS identity is a holder's.
func TestRESTMiddleware_AuthzPublicPath_RefusesHolderIdentity(t *testing.T) {
	authn := &mockAuthenticator{
		name: "test-authn",
		identity: &auth.Identity{
			Subject: "officer-1", TenantID: "t1",
			Metadata: map[string]string{auth.HolderProfileMetadataKey: "so"},
		},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if called {
		t.Error("a holder identity must never reach /metrics: it has no business outside its own ceremony verbs")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// ---------------------------------------------------------------------------
// UnaryInterceptor – public gRPC method pass-through
// ---------------------------------------------------------------------------

func TestUnaryInterceptor_PublicMethod_PassThrough(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}
	resp, err := interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should be called for public method")
	}
	if resp != "ok" {
		t.Errorf("resp = %v, want ok", resp)
	}
}

func TestUnaryInterceptor_UnmappedNonPublicMethod_DefaultDeny(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/fake.v1.UnknownService/DoSomething"}
	_, err := interceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected error for unmapped non-public method")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", st.Code())
	}
}

// ---------------------------------------------------------------------------
// StreamInterceptor – public gRPC method pass-through
// ---------------------------------------------------------------------------

func TestStreamInterceptor_PublicMethod_PassThrough(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))
	interceptor := m.StreamInterceptor()

	called := false
	handler := func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/grpc.health.v1.Health/Watch"}
	err := interceptor(nil, &boostServerStream{ctx: context.Background()}, info, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should be called for public stream method")
	}
}

func TestStreamInterceptor_UnmappedNonPublicMethod_DefaultDeny(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "test-authn",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))
	interceptor := m.StreamInterceptor()

	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/fake.v1.Unknown/Stream"}
	err := interceptor(nil, &boostServerStream{ctx: context.Background()}, info, handler)
	if err == nil {
		t.Fatal("expected denial for unmapped non-public stream")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", st.Code())
	}
}

// ---------------------------------------------------------------------------
// RESTRouteRegistry — templated routes resolve via the chi matcher
// ---------------------------------------------------------------------------

func TestRESTRouteRegistry_TemplatedRoutesResolve(t *testing.T) {
	rr := testRESTRoutes()
	perm, kind, ok := rr.Lookup("POST", "/v1/barrier/officers/so-1/claim/begin")
	if !ok || kind != RESTRoutePermission || perm != permission.IdentityGated {
		t.Errorf("templated officer claim route did not resolve: ok=%v kind=%v perm=%q", ok, kind, perm)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// captureAuthorizer calls its captureFn on every Authorize call so tests can
// inspect the context that was passed.
type captureAuthorizer struct {
	captureFn func(ctx context.Context)
	err       error
}

func (c *captureAuthorizer) Authorize(ctx context.Context, _ *auth.Identity, _, _ string) error {
	if c.captureFn != nil {
		c.captureFn(ctx)
	}
	return c.err
}

// boostServerStream is a minimal grpc.ServerStream for interceptor tests.
// Named distinctly to avoid collision with the one in middleware_test.go.
type boostServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *boostServerStream) Context() context.Context { return s.ctx }
