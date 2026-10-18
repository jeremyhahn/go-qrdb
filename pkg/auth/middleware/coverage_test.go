//go:build !integration

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
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// injectTLSState tests
// ---------------------------------------------------------------------------

func TestInjectTLSState_NoPeer(t *testing.T) {
	// Context with no gRPC peer info; should return context unchanged.
	ctx := context.Background()
	result := injectTLSState(ctx)

	// The TLS state should not be set.
	state := auth.TLSStateFromContext(result)
	if state != nil {
		t.Error("expected nil TLS state when no peer in context")
	}
}

func TestInjectTLSState_PeerWithTLSInfo(t *testing.T) {
	// Create a context with gRPC peer info containing TLS credentials.
	cert := &x509.Certificate{
		Subject: pkix.Name{CommonName: "test-client"},
	}
	tlsState := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	tlsInfo := credentials.TLSInfo{State: tlsState}
	p := &peer.Peer{
		Addr:     &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999},
		AuthInfo: tlsInfo,
	}
	ctx := peer.NewContext(context.Background(), p)

	result := injectTLSState(ctx)

	// The TLS state should now be available in context.
	state := auth.TLSStateFromContext(result)
	if state == nil {
		t.Fatal("expected TLS state in context after injection")
	}
	if len(state.PeerCertificates) != 1 {
		t.Fatalf("expected 1 peer certificate, got %d", len(state.PeerCertificates))
	}
	if state.PeerCertificates[0].Subject.CommonName != "test-client" {
		t.Errorf("expected CN 'test-client', got %q", state.PeerCertificates[0].Subject.CommonName)
	}
}

func TestInjectTLSState_PeerWithNonTLSAuthInfo(t *testing.T) {
	// Create a context with gRPC peer info but non-TLS auth info.
	p := &peer.Peer{
		Addr:     &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999},
		AuthInfo: nil, // No TLS auth info.
	}
	ctx := peer.NewContext(context.Background(), p)

	result := injectTLSState(ctx)

	// Should return context without TLS state.
	state := auth.TLSStateFromContext(result)
	if state != nil {
		t.Error("expected nil TLS state when peer has no TLS auth info")
	}
}

// ---------------------------------------------------------------------------
// WithUserServiceFallback tests
// ---------------------------------------------------------------------------

func TestWithUserServiceFallback_SetsField(t *testing.T) {
	authz := &mockAuthorizer{}
	fallback := &mockUserServicer{
		user: &user.User{
			Username:  "alice",
			TenantID:  "t1",
			RoleIDs:   []string{"admin"},
			Lifecycle: user.LifecycleActive,
		},
	}

	m := newTestMiddleware(authz, WithUserServiceFallback(fallback))

	if m.usersFallback != fallback {
		t.Error("expected usersFallback to be set by WithUserServiceFallback")
	}
}

func TestWithUserServiceFallback_PrimaryFails_FallbackSucceeds(t *testing.T) {
	// the user so the middleware can enrich the identity. The fallback holds
	// the system tenant only, so the identity is a system-tenant one.
	id := &auth.Identity{Subject: "alice", TenantID: "system", Roles: []string{"base"}}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	primary := &mockUserServicer{err: errors.New("raft not ready")}
	fallback := &mockUserServicer{
		user: &user.User{
			Username:  "alice",
			TenantID:  "system",
			RoleIDs:   []string{"admin", "operator"},
			Lifecycle: user.LifecycleActive,
		},
	}

	m := newTestMiddleware(authz,
		WithAuthenticator(authn),
		WithUserService(primary),
		WithUserServiceFallback(fallback),
		WithMTLSBindingPolicy(auth.MTLSBindingEnrolled),
	)

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Roles should be enriched from the fallback user store.
	if len(identity.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(identity.Roles))
	}
	if identity.Roles[0] != "admin" || identity.Roles[1] != "operator" {
		t.Errorf("expected roles [admin operator], got %v", identity.Roles)
	}

	// Both stores should have been called.
	if primary.calls.Load() != 1 {
		t.Errorf("expected 1 primary lookup, got %d", primary.calls.Load())
	}
	if fallback.calls.Load() != 1 {
		t.Errorf("expected 1 fallback lookup, got %d", fallback.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// lookupUser tests
// ---------------------------------------------------------------------------

func TestLookupUser_PrimarySucceeds(t *testing.T) {
	primary := &mockUserServicer{
		user: &user.User{
			Username:  "alice",
			TenantID:  "t1",
			Lifecycle: user.LifecycleActive,
		},
	}

	m := &AuthMiddleware{users: primary}

	u, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", u.Username)
	}
	if primary.calls.Load() != 1 {
		t.Errorf("expected 1 call, got %d", primary.calls.Load())
	}
}

func TestLookupUser_PrimaryNotFound_NoFallback(t *testing.T) {
	// UserNotFoundError is authoritative; should not fall back.
	primary := &mockUserServicer{
		err: &user.UserNotFoundError{Lookup: "alice"},
	}

	m := &AuthMiddleware{users: primary}

	_, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if err == nil {
		t.Fatal("expected error")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("expected UserNotFoundError, got %T: %v", err, err)
	}
}

func TestLookupUser_PrimaryTransient_FallbackSucceeds(t *testing.T) {
	primary := &mockUserServicer{err: errors.New("raft timeout")}
	fallback := &mockUserServicer{
		user: &user.User{
			Username:  "bob",
			TenantID:  "system",
			Lifecycle: user.LifecycleActive,
		},
	}

	m := &AuthMiddleware{users: primary, usersFallback: fallback}

	u, err := m.lookupUser(context.Background(), "system", userKey{username: "bob"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Username != "bob" {
		t.Errorf("expected username 'bob', got %q", u.Username)
	}
	if primary.calls.Load() != 1 {
		t.Errorf("expected 1 primary call, got %d", primary.calls.Load())
	}
	if fallback.calls.Load() != 1 {
		t.Errorf("expected 1 fallback call, got %d", fallback.calls.Load())
	}
}

func TestLookupUser_PrimaryTransient_FallbackNotFound(t *testing.T) {
	// Primary fails with transient error, fallback returns UserNotFoundError.
	// The fallback's UserNotFoundError should be used: it holds the system
	// tenant, so for a system-tenant lookup its "not found" is authoritative.
	primary := &mockUserServicer{err: errors.New("raft timeout")}
	fallback := &mockUserServicer{
		err: &user.UserNotFoundError{Lookup: "ghost"},
	}

	m := &AuthMiddleware{users: primary, usersFallback: fallback}

	_, err := m.lookupUser(context.Background(), "system", userKey{username: "ghost"})
	if err == nil {
		t.Fatal("expected error")
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("expected UserNotFoundError from fallback, got %T: %v", err, err)
	}
}

func TestLookupUser_PrimaryTransient_FallbackTransient(t *testing.T) {
	// Both primary and fallback fail with transient errors, for the SYSTEM
	// tenant the fallback actually holds -- so the fallback is consulted
	// (calls == 1) and, failing too, the primary error should be returned.
	primaryErr := errors.New("raft not ready")
	primary := &mockUserServicer{err: primaryErr}
	fallback := &mockUserServicer{err: errors.New("fallback also broken")}

	m := &AuthMiddleware{users: primary, usersFallback: fallback}

	_, err := m.lookupUser(context.Background(), tenantid.System, userKey{username: "alice"})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != primaryErr.Error() {
		t.Errorf("expected primary error %q, got %q", primaryErr.Error(), err.Error())
	}
	if got := fallback.calls.Load(); got != 1 {
		t.Errorf("fallback.calls = %d, want 1: the system tenant is the one the fallback holds", got)
	}
}

// TestLookupUser_PrimaryTransient_FallbackNeverConsultedForAnotherTenant is
// the regression the fix above guards: for any tenant other than the system
// tenant, the fallback is confined to an identity namespace that cannot hold
// it, so lookupUser must not consult it at all -- see lookupUser's own doc
// comment and WithUserServiceFallback.
func TestLookupUser_PrimaryTransient_FallbackNeverConsultedForAnotherTenant(t *testing.T) {
	primaryErr := errors.New("raft not ready")
	primary := &mockUserServicer{err: primaryErr}
	fallback := &mockUserServicer{err: errors.New("fallback also broken")}

	m := &AuthMiddleware{users: primary, usersFallback: fallback}

	_, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != primaryErr.Error() {
		t.Errorf("expected primary error %q, got %q", primaryErr.Error(), err.Error())
	}
	if got := fallback.calls.Load(); got != 0 {
		t.Errorf("fallback.calls = %d, want 0: the fallback is confined to the system tenant and must "+
			"not be consulted for tenant t1", got)
	}
}

func TestLookupUser_PrimaryTransient_NoFallback(t *testing.T) {
	// Primary fails with transient error, no fallback configured.
	primaryErr := errors.New("raft not ready")
	primary := &mockUserServicer{err: primaryErr}

	m := &AuthMiddleware{users: primary}

	_, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != primaryErr.Error() {
		t.Errorf("expected primary error %q, got %q", primaryErr.Error(), err.Error())
	}
}

// ---------------------------------------------------------------------------
// AuthenticateQUIC tests
// ---------------------------------------------------------------------------

func TestAuthenticateQUIC_Success(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}

	m := newTestMiddleware(authz, WithAuthenticator(authn))

	ctx, err := m.AuthenticateQUIC(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify identity is stored in context.
	ctxID := auth.IdentityFromContext(ctx)
	if ctxID == nil {
		t.Fatal("expected identity in context")
	}
	if ctxID.Subject != "alice" {
		t.Errorf("expected subject 'alice', got %q", ctxID.Subject)
	}
	if ctxID.TenantID != "t1" {
		t.Errorf("expected tenant 't1', got %q", ctxID.TenantID)
	}
}

func TestAuthenticateQUIC_Unauthenticated(t *testing.T) {
	authn := &mockAuthenticator{name: "test-authn", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}

	m := newTestMiddleware(authz, WithAuthenticator(authn))

	_, err := m.AuthenticateQUIC(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticateQUIC_NoAuthenticators(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz)

	_, err := m.AuthenticateQUIC(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticateQUIC_UserEnrichment(t *testing.T) {
	id := &auth.Identity{Subject: "charlie", TenantID: "t1", Roles: []string{"base"}}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username:  "charlie",
			TenantID:  "t1",
			RoleIDs:   []string{"admin"},
			Lifecycle: user.LifecycleActive,
		},
	}

	m := newTestMiddleware(authz, WithAuthenticator(authn), WithUserService(users))

	ctx, err := m.AuthenticateQUIC(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctxID := auth.IdentityFromContext(ctx)
	if ctxID == nil {
		t.Fatal("expected identity in context")
	}
	if len(ctxID.Roles) != 1 || ctxID.Roles[0] != "admin" {
		t.Errorf("expected roles [admin], got %v", ctxID.Roles)
	}
}

// ---------------------------------------------------------------------------
// AuthorizeQUIC tests
// ---------------------------------------------------------------------------

// testQUICMethodRegistry builds a minimal registration-site registry for the
// AuthorizeQUIC unit tests (the full production registry is built by the QUIC
// server's buildHandlers and guarded in pkg/api/quic).
func testQUICMethodRegistry() *QUICMethodRegistry {
	r := NewQUICMethodRegistry()
	r.Register("kv.Put", permission.KVPut)
	return r
}

func TestAuthorizeQUIC_NoMapping(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz)

	// DEFAULT-DENY: a method without a registered permission is rejected, not
	// allowed through. The authorizer is never consulted for an unregistered
	// method (the denial is decided before the permission check).
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "t1"})
	err := m.AuthorizeQUIC(ctx, testQUICMethodRegistry(), "unknown.Method")
	if err == nil {
		t.Fatal("expected unregistered QUIC method to be denied, got nil")
	}
	if authz.calls.Load() != 0 {
		t.Errorf("expected 0 authorize calls, got %d", authz.calls.Load())
	}
}

func TestAuthorizeQUIC_NilRegistryDenied(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz)

	// A nil registry covers nothing: every method is denied (fail-closed).
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "t1"})
	err := m.AuthorizeQUIC(ctx, nil, "kv.Put")
	if err == nil {
		t.Fatal("expected nil registry to deny, got nil")
	}
	if authz.calls.Load() != 0 {
		t.Errorf("expected 0 authorize calls, got %d", authz.calls.Load())
	}
}

func TestAuthorizeQUIC_NoIdentity(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz)

	// A registered method with no identity in context should return unauthenticated.
	err := m.AuthorizeQUIC(context.Background(), testQUICMethodRegistry(), "kv.Put")
	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *auth.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T: %v", err, err)
	}
	if authErr.Code != "unauthenticated" {
		t.Errorf("expected code 'unauthenticated', got %q", authErr.Code)
	}
}

func TestAuthorizeQUIC_Authorized(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz)

	// Set identity in context.
	id := &auth.Identity{Subject: "alice", TenantID: "t1", Roles: []string{"admin"}}
	ctx := auth.WithIdentity(context.Background(), id)

	err := m.AuthorizeQUIC(ctx, testQUICMethodRegistry(), "kv.Put")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authz.calls.Load() != 1 {
		t.Errorf("expected 1 authorize call, got %d", authz.calls.Load())
	}
}

func TestAuthorizeQUIC_Unauthorized(t *testing.T) {
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}
	m := newTestMiddleware(authz)

	id := &auth.Identity{Subject: "bob", TenantID: "t1"}
	ctx := auth.WithIdentity(context.Background(), id)

	err := m.AuthorizeQUIC(ctx, testQUICMethodRegistry(), "kv.Put")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// writeJSONError - encoder error branch
// ---------------------------------------------------------------------------

// brokenWriter is a http.ResponseWriter that always fails on Write.
type brokenWriter struct {
	header     http.Header
	statusCode int
}

func newBrokenWriter() *brokenWriter {
	return &brokenWriter{header: make(http.Header)}
}

func (w *brokenWriter) Header() http.Header  { return w.header }
func (w *brokenWriter) WriteHeader(code int) { w.statusCode = code }
func (w *brokenWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestWriteJSONError_EncoderFailure(t *testing.T) {
	// When the encoder fails to write, the function should not panic.
	// It logs the error via slog, which we cannot easily capture, but
	// we verify no panic and the status code + headers are set correctly.
	w := newBrokenWriter()
	writeJSONError(w, http.StatusForbidden, "access denied")

	if w.statusCode != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.statusCode)
	}
	if ct := w.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

func TestWriteJSONError_VariousStatusCodes(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		message string
	}{
		{"unauthorized", http.StatusUnauthorized, "not authenticated"},
		{"forbidden", http.StatusForbidden, "access denied"},
		{"service_unavailable", http.StatusServiceUnavailable, "store down"},
		{"internal", http.StatusInternalServerError, "unexpected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJSONError(rec, tt.code, tt.message)

			if rec.Code != tt.code {
				t.Errorf("expected %d, got %d", tt.code, rec.Code)
			}
			var resp restErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("failed to decode: %v", err)
			}
			if resp.Error != tt.message {
				t.Errorf("expected %q, got %q", tt.message, resp.Error)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// StreamInterceptor - authorization denied path
// ---------------------------------------------------------------------------

func TestStreamInterceptor_Unauthorized(t *testing.T) {
	id := &auth.Identity{Subject: "bob", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}

	m := newTestMiddleware(authz, WithAuthenticator(authn))
	interceptor := m.StreamInterceptor()

	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}
	ss := &mockServerStream{ctx: context.Background()}

	err := interceptor(nil, ss, info, func(_ any, _ grpc.ServerStream) error {
		t.Error("handler should not be called")
		return nil
	})

	if err == nil {
		t.Fatal("expected error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", st.Code())
	}
}

func TestStreamInterceptor_UserDisabled(t *testing.T) {
	id := &auth.Identity{Subject: "disabled-user", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username:  "disabled-user",
			TenantID:  "t1",
			Lifecycle: user.LifecycleDisabled,
		},
	}

	m := newTestMiddleware(authz, WithAuthenticator(authn), WithUserService(users))
	interceptor := m.StreamInterceptor()

	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}
	ss := &mockServerStream{ctx: context.Background()}

	err := interceptor(nil, ss, info, func(_ any, _ grpc.ServerStream) error {
		t.Error("handler should not be called")
		return nil
	})

	if err == nil {
		t.Fatal("expected error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied for disabled user, got %v", st.Code())
	}
}

// ---------------------------------------------------------------------------
// authorize - audit log failure branch
// ---------------------------------------------------------------------------

// TestAuthorize_AuditLogFailure asserts the go-qrdb#335 policy: a GRANT the
// middleware could not record is refused, not issued. This test previously
// asserted the opposite ("authorize should not fail due to audit"), which was
// accurate about the code and wrong about the property — audit.Emit had no
// error return, so the middleware's own fail-closed branch could never fire
// and the test was describing an unreachable-by-construction behaviour as a
// policy.
func TestAuthorize_AuditLogFailure(t *testing.T) {
	authz := &mockAuthorizer{}
	sinkErr := errors.New("disk full")
	al := &mockAuditLogger{err: sinkErr}

	m := newTestMiddleware(authz, WithAuditLogger(al))

	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}
	err := m.authorize(context.Background(), identity, "kv:put")
	if err == nil {
		t.Fatal("expected refusal: an unrecorded grant is the case audit exists to prevent")
	}

	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
	if !errors.Is(err, sinkErr) {
		t.Errorf("AuditError must wrap the sink's own error, got %v", err)
	}
	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
}

// TestAuthorize_AuditLogFailure_OnDenied asserts the refusal is UNIFORM: even
// when the underlying decision was already a denial, an unrecordable decision
// is answered with *audit.GateError. "We answer only what we could record" is one
// rule a reader can check; carving out the denials would make it four.
func TestAuthorize_AuditLogFailure_OnDenied(t *testing.T) {
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}
	al := &mockAuditLogger{err: errors.New("disk full")}

	m := newTestMiddleware(authz, WithAuditLogger(al))

	identity := &auth.Identity{Subject: "bob", TenantID: "t2"}
	err := m.authorize(context.Background(), identity, "admin:drain")
	if err == nil {
		t.Fatal("expected authorization error")
	}

	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
}

// TestAuthorize_AuditLogFailure_FailOpen is the same request under the
// operator's deliberate escape: the decision stands and the failure is
// counted rather than refused.
func TestAuthorize_AuditLogFailure_FailOpen(t *testing.T) {
	authz := &mockAuthorizer{}
	al := &mockAuditLogger{err: errors.New("disk full")}

	m := newTestMiddleware(authz, WithAuditLogger(al), WithAuditFailurePolicy(audit.FailOpen))

	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}
	if err := m.authorize(context.Background(), identity, "kv:put"); err != nil {
		t.Fatalf("fail-open must permit the decision, got %v", err)
	}
	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// REST middleware - StoreUnavailableError path
// ---------------------------------------------------------------------------

func TestRESTMiddleware_StoreUnavailable(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{err: errors.New("raft not ready")}

	m := newTestMiddleware(authz, WithAuthenticator(authn), WithUserService(users))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	})

	handler := m.RESTMiddleware(testRESTRoutes(), next)
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}

	var errResp restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// REST middleware - TLS injection path
// ---------------------------------------------------------------------------

func TestRESTMiddleware_TLSInjection(t *testing.T) {
	// When r.TLS is set, the middleware should inject TLS state into the
	// context so the mTLS authenticator can use it.
	cert := &x509.Certificate{
		Subject: pkix.Name{
			CommonName:         "test-client",
			OrganizationalUnit: []string{"t1"},
		},
	}
	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}

	// Use a real mTLS authenticator to verify the full pipeline. This test is
	// about TLS-state injection into the request context, with no user store
	// wired at all, so "enrolled" is the correct, explicit choice (go-qrdb#548).
	authn := auth.NewMTLSAuthenticator()
	authz := &mockAuthorizer{}

	m := newTestMiddleware(authz, WithAuthenticator(authn), WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		ctxID := auth.IdentityFromContext(r.Context())
		if ctxID == nil {
			t.Error("expected identity in context")
			return
		}
		if ctxID.Subject != "test-client" {
			t.Errorf("expected subject 'test-client', got %q", ctxID.Subject)
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := m.RESTMiddleware(testRESTRoutes(), next)
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.TLS = tlsState
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("expected handler to be called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// gRPC UnaryInterceptor - StoreUnavailable path
// ---------------------------------------------------------------------------

func TestUnaryInterceptor_StoreUnavailable(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{err: errors.New("raft not ready")}

	m := newTestMiddleware(authz, WithAuthenticator(authn), WithUserService(users))
	interceptor := m.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Get"}
	handler := func(_ context.Context, _ any) (any, error) {
		t.Error("handler should not be called")
		return nil, nil
	}

	_, err := interceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected error")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("expected Unavailable code, got %v", st.Code())
	}
}

// ---------------------------------------------------------------------------
// gRPC StreamInterceptor - StoreUnavailable path
// ---------------------------------------------------------------------------

func TestStreamInterceptor_StoreUnavailable(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{err: errors.New("raft not ready")}

	m := newTestMiddleware(authz, WithAuthenticator(authn), WithUserService(users))
	interceptor := m.StreamInterceptor()

	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}
	ss := &mockServerStream{ctx: context.Background()}

	err := interceptor(nil, ss, info, func(_ any, _ grpc.ServerStream) error {
		t.Error("handler should not be called")
		return nil
	})

	if err == nil {
		t.Fatal("expected error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("expected Unavailable, got %v", st.Code())
	}
}

// ---------------------------------------------------------------------------
// AuthenticateQUIC with fallback
// ---------------------------------------------------------------------------

func TestAuthenticateQUIC_FallbackUsed(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "system"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	primary := &mockUserServicer{err: errors.New("raft timeout")}
	fallback := &mockUserServicer{
		user: &user.User{
			Username:  "alice",
			TenantID:  "system",
			RoleIDs:   []string{"operator"},
			Lifecycle: user.LifecycleActive,
		},
	}

	m := newTestMiddleware(authz,
		WithAuthenticator(authn),
		WithUserService(primary),
		WithUserServiceFallback(fallback),
		WithMTLSBindingPolicy(auth.MTLSBindingEnrolled),
	)

	ctx, err := m.AuthenticateQUIC(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctxID := auth.IdentityFromContext(ctx)
	if ctxID == nil {
		t.Fatal("expected identity in context")
	}
	if len(ctxID.Roles) != 1 || ctxID.Roles[0] != "operator" {
		t.Errorf("expected roles [operator], got %v", ctxID.Roles)
	}
}

func TestAuthenticateQUIC_UserDisabled(t *testing.T) {
	id := &auth.Identity{Subject: "disabled", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username:  "disabled",
			TenantID:  "t1",
			Lifecycle: user.LifecycleDisabled,
		},
	}

	m := newTestMiddleware(authz, WithAuthenticator(authn), WithUserService(users))

	_, err := m.AuthenticateQUIC(context.Background())
	if err == nil {
		t.Fatal("expected error for disabled user")
	}

	var ude *UserDisabledError
	if !errors.As(err, &ude) {
		t.Fatalf("expected UserDisabledError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// StoreUnavailableError tests
// ---------------------------------------------------------------------------

func TestStoreUnavailableError(t *testing.T) {
	inner := errors.New("raft not ready")
	e := &StoreUnavailableError{Err: inner}
	want := "auth/middleware: user store unavailable: raft not ready"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap should return inner error")
	}
}

func TestStoreUnavailableError_ErrorsIs(t *testing.T) {
	inner := errors.New("connection refused")
	e := &StoreUnavailableError{Err: inner}
	if !errors.Is(e, inner) {
		t.Error("errors.Is should match inner error")
	}
}
