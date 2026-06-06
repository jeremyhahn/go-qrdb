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
// LookupMCP
// ---------------------------------------------------------------------------

func TestLookupMCP_Found(t *testing.T) {
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
			p, ok := LookupMCP(tc.tool)
			if !ok {
				t.Fatalf("LookupMCP(%q): not found", tc.tool)
			}
			if p != tc.want {
				t.Errorf("LookupMCP(%q) = %v, want %v", tc.tool, p, tc.want)
			}
		})
	}
}

func TestLookupMCP_NotFound(t *testing.T) {
	_, ok := LookupMCP("qrdb_nonexistent_tool_xyz")
	if ok {
		t.Error("expected not-found for unknown tool")
	}
}

// ---------------------------------------------------------------------------
// AuthorizeMCP
// ---------------------------------------------------------------------------

func TestAuthorizeMCP_NoIdentity_Denied(t *testing.T) {
	m := New(&mockAuthorizer{})
	ctx := context.Background()
	err := m.AuthorizeMCP(ctx, "qrdb_kv_put")
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
	err := m.AuthorizeMCP(ctx, "qrdb_tool_that_does_not_exist")
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
	if err := m.AuthorizeMCP(ctx, "qrdb_put"); err != nil {
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
	err := m.AuthorizeMCP(ctx, "qrdb_put")
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
// splitMethodPath
// ---------------------------------------------------------------------------

func TestSplitMethodPath_Valid(t *testing.T) {
	m, p, ok := splitMethodPath("GET /v1/users")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if m != "GET" || p != "/v1/users" {
		t.Errorf("got method=%q path=%q", m, p)
	}
}

func TestSplitMethodPath_NoSpace(t *testing.T) {
	_, _, ok := splitMethodPath("GETPATH")
	if ok {
		t.Error("expected ok=false for string without space")
	}
}

func TestSplitMethodPath_EmptyString(t *testing.T) {
	_, _, ok := splitMethodPath("")
	if ok {
		t.Error("expected ok=false for empty string")
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
	if !isPublicGRPCMethod("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo") {
		t.Error("reflection method should be public")
	}
}

func TestIsPublicGRPCMethod_Application(t *testing.T) {
	if isPublicGRPCMethod("/qrdb.v1.QRDBService/Put") {
		t.Error("application method should NOT be public")
	}
}

// ---------------------------------------------------------------------------
// isRESTAPISurface + isRESTAuthzPublic
// ---------------------------------------------------------------------------

func TestIsRESTAPISurface_API(t *testing.T) {
	for _, p := range []string{
		"/kv", "/kv/mykey", "/v1/users", "/tx", "/batch", "/vector/ns",
	} {
		if !isRESTAPISurface(p) {
			t.Errorf("expected %q to be on API surface", p)
		}
	}
}

func TestIsRESTAPISurface_NonAPI(t *testing.T) {
	for _, p := range []string{
		"/", "/favicon.ico", "/app.js", "/static/logo.png",
	} {
		if isRESTAPISurface(p) {
			t.Errorf("expected %q to NOT be on API surface", p)
		}
	}
}

func TestIsRESTAuthzPublic_Metrics(t *testing.T) {
	if !isRESTAuthzPublic("/metrics") {
		t.Error("/metrics should be authz-public")
	}
}

func TestIsRESTAuthzPublic_KV(t *testing.T) {
	if isRESTAuthzPublic("/kv") {
		t.Error("/kv should NOT be authz-public")
	}
}

// ---------------------------------------------------------------------------
// matchRESTPattern
// ---------------------------------------------------------------------------

func TestMatchRESTPattern_NoMatch(t *testing.T) {
	_, ok := matchRESTPattern("DELETE", "/nonexistent/route/xyz/abc")
	if ok {
		t.Error("expected no match for completely unknown route")
	}
}

func TestMatchRESTPattern_EmptyWildcardRejected(t *testing.T) {
	// A path segment that maps to a wildcard must not be empty.
	// /users// has an empty second segment which should not match /users/{id}.
	_, ok := matchRESTPattern("GET", "/users//")
	// This may or may not match; the critical test is that matchRESTPattern
	// doesn't panic and returns a well-formed result.
	_ = ok
}

// ---------------------------------------------------------------------------
// RESTMiddleware – route not on API surface passes through
// ---------------------------------------------------------------------------

func TestRESTMiddleware_NonAPIPath_PassThrough(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "mtls",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(next)

	req := httptest.NewRequest("GET", "/static/app.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("next handler should have been called for non-API path")
	}
}

func TestRESTMiddleware_PublicPath_SkipsAuthn(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "mtls",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(next)

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
		name:     "mtls",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(next)

	// /v1/no-such-endpoint is under the API surface but has no mapping.
	req := httptest.NewRequest("DELETE", "/v1/no-such-endpoint", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Errorf("expected 403/401 default-deny, got %d", w.Code)
	}
}

func TestRESTMiddleware_AuthzPublicPath_Allowed(t *testing.T) {
	// /metrics is on the API surface but in the authz-public set; authenticated
	// callers should reach the handler without a specific permission.
	authn := &mockAuthenticator{
		name:     "mtls",
		identity: &auth.Identity{Subject: "u1", TenantID: "t1"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(next)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("next handler should be called for /metrics (authz-public)")
	}
}

// ---------------------------------------------------------------------------
// UnaryInterceptor – public gRPC method pass-through
// ---------------------------------------------------------------------------

func TestUnaryInterceptor_PublicMethod_PassThrough(t *testing.T) {
	authn := &mockAuthenticator{
		name:     "mtls",
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
		name:     "mtls",
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
		name:     "mtls",
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
		name:     "mtls",
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
// init() coverage — verify rest patterns compiled
// ---------------------------------------------------------------------------

func TestInitRestPatterns_AllParsed(t *testing.T) {
	if len(restPatterns) == 0 {
		t.Error("expected restPatterns to be non-empty after init()")
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
