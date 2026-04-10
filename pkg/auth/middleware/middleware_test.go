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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockAuthenticator struct {
	name     string
	identity *auth.Identity
	err      error
	calls    atomic.Int64
}

func (m *mockAuthenticator) Authenticate(_ context.Context) (*auth.Identity, error) {
	m.calls.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	return m.identity, nil
}

func (m *mockAuthenticator) Name() string {
	return m.name
}

type mockAuthorizer struct {
	err   error
	calls atomic.Int64
}

func (m *mockAuthorizer) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	m.calls.Add(1)
	return m.err
}

type mockUserServicer struct {
	user     *user.User
	err      error
	calls    atomic.Int64
	lastUser string
}

func (m *mockUserServicer) GetUserByUsername(_ context.Context, _, username string) (*user.User, error) {
	m.calls.Add(1)
	m.lastUser = username
	if m.err != nil {
		return nil, m.err
	}
	return m.user, nil
}

func (m *mockUserServicer) CreateUser(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) GetUser(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) UpdateUser(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) DeleteUser(_ context.Context, _ string) error {
	return nil
}

func (m *mockUserServicer) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return nil, nil
}

func (m *mockUserServicer) EnableUser(_ context.Context, _ string) error {
	return nil
}

func (m *mockUserServicer) DisableUser(_ context.Context, _ string) error {
	return nil
}

type mockAuditLogger struct {
	entries []*audit.Entry
	err     error
	calls   atomic.Int64
}

func (m *mockAuditLogger) Log(_ context.Context, entry *audit.Entry) error {
	m.calls.Add(1)
	m.entries = append(m.entries, entry)
	return m.err
}

func (m *mockAuditLogger) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	if m == nil {
		t.Fatal("expected non-nil AuthMiddleware")
	}
	if m.authorizer != authz {
		t.Error("expected authorizer to be set")
	}
	if len(m.authenticators) != 0 {
		t.Errorf("expected 0 authenticators, got %d", len(m.authenticators))
	}
	if m.users != nil {
		t.Error("expected nil user service")
	}
	if m.audit != nil {
		t.Error("expected nil audit logger")
	}
}

func TestNew_WithOptions(t *testing.T) {
	authz := &mockAuthorizer{}
	authn := &mockAuthenticator{name: "test"}
	users := &mockUserServicer{}
	al := &mockAuditLogger{}

	m := New(authz,
		WithAuthenticator(authn),
		WithUserService(users),
		WithAuditLogger(al),
	)

	if len(m.authenticators) != 1 {
		t.Fatalf("expected 1 authenticator, got %d", len(m.authenticators))
	}
	if m.authenticators[0].Name() != "test" {
		t.Errorf("expected authenticator name 'test', got %q", m.authenticators[0].Name())
	}
	if m.users != users {
		t.Error("expected user service to be set")
	}
	if m.audit != al {
		t.Error("expected audit logger to be set")
	}
}

func TestNew_MultipleAuthenticators(t *testing.T) {
	authz := &mockAuthorizer{}
	a1 := &mockAuthenticator{name: "first"}
	a2 := &mockAuthenticator{name: "second"}

	m := New(authz,
		WithAuthenticator(a1),
		WithAuthenticator(a2),
	)

	if len(m.authenticators) != 2 {
		t.Fatalf("expected 2 authenticators, got %d", len(m.authenticators))
	}
}

// ---------------------------------------------------------------------------
// authenticate tests
// ---------------------------------------------------------------------------

func TestAuthenticate_SingleAuthenticator_Success(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "tenant1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	ctx, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identity.Subject != "alice" {
		t.Errorf("expected subject 'alice', got %q", identity.Subject)
	}
	if identity.TenantID != "tenant1" {
		t.Errorf("expected tenant 'tenant1', got %q", identity.TenantID)
	}

	// Verify identity is stored in context.
	ctxID := auth.IdentityFromContext(ctx)
	if ctxID == nil {
		t.Fatal("expected identity in context")
	}
	if ctxID.Subject != "alice" {
		t.Errorf("expected context subject 'alice', got %q", ctxID.Subject)
	}
}

func TestAuthenticate_ChainFallthrough(t *testing.T) {
	failing := &mockAuthenticator{
		name: "jwt",
		err:  auth.ErrUnauthenticated,
	}
	succeeding := &mockAuthenticator{
		name:     "mtls",
		identity: &auth.Identity{Subject: "bob", TenantID: "t2"},
	}
	authz := &mockAuthorizer{}

	m := New(authz,
		WithAuthenticator(failing),
		WithAuthenticator(succeeding),
	)

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identity.Subject != "bob" {
		t.Errorf("expected subject 'bob', got %q", identity.Subject)
	}
}

func TestAuthenticate_AllFail_ReturnsUnauthenticated(t *testing.T) {
	f1 := &mockAuthenticator{name: "a1", err: auth.ErrNoPeerCertificate}
	f2 := &mockAuthenticator{name: "a2", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(f1), WithAuthenticator(f2))

	_, _, err := m.authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}

	// Last error is auth.ErrUnauthenticated, which is an *AuthError,
	// so it should be returned directly.
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticate_AllFail_NonAuthError(t *testing.T) {
	genericErr := errors.New("network timeout")
	f1 := &mockAuthenticator{name: "a1", err: genericErr}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(f1))

	_, _, err := m.authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}

	var chainErr *AuthChainError
	if !errors.As(err, &chainErr) {
		t.Errorf("expected AuthChainError, got %T: %v", err, err)
	}
	if !errors.Is(chainErr, genericErr) {
		t.Errorf("expected wrapped generic error, got %v", chainErr.Unwrap())
	}
}

func TestAuthenticate_NoAuthenticators(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	_, _, err := m.authenticate(context.Background())
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticate_UserEnrichment(t *testing.T) {
	id := &auth.Identity{Subject: "charlie", TenantID: "t1", Roles: []string{"base"}}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			ID:       "u-1",
			Username: "charlie",
			TenantID: "t1",
			RoleIDs:  []string{"admin", "operator"},
			Enabled:  true,
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Roles should be enriched from user store.
	if len(identity.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(identity.Roles))
	}
	if identity.Roles[0] != "admin" || identity.Roles[1] != "operator" {
		t.Errorf("expected roles [admin operator], got %v", identity.Roles)
	}

	if users.calls.Load() != 1 {
		t.Errorf("expected 1 user lookup, got %d", users.calls.Load())
	}
}

func TestAuthenticate_UserDisabled(t *testing.T) {
	id := &auth.Identity{Subject: "disabled-user", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username: "disabled-user",
			TenantID: "t1",
			Enabled:  false,
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, _, err := m.authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error for disabled user")
	}

	var ude *UserDisabledError
	if !errors.As(err, &ude) {
		t.Fatalf("expected UserDisabledError, got %T: %v", err, err)
	}
	if ude.Username != "disabled-user" {
		t.Errorf("expected username 'disabled-user', got %q", ude.Username)
	}
	if ude.TenantID != "t1" {
		t.Errorf("expected tenant 't1', got %q", ude.TenantID)
	}
}

func TestAuthenticate_UserNotFound(t *testing.T) {
	id := &auth.Identity{Subject: "unknown", TenantID: "t1", Roles: []string{"original"}}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		err: &user.UserNotFoundError{ID: "unknown"},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Roles should remain from base identity.
	if len(identity.Roles) != 1 || identity.Roles[0] != "original" {
		t.Errorf("expected original roles preserved, got %v", identity.Roles)
	}
}

func TestAuthenticate_NoUserService(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1", Roles: []string{"dev"}}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(identity.Roles) != 1 || identity.Roles[0] != "dev" {
		t.Errorf("expected roles [dev], got %v", identity.Roles)
	}
}

func TestAuthenticate_UserStoreError(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1", Roles: []string{"dev"}}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		err: errors.New("store unavailable"),
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	// Transient store errors return StoreUnavailableError (503/UNAVAILABLE)
	// instead of proceeding without enrichment (which would cause false 403).
	_, _, err := m.authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var sue *StoreUnavailableError
	if !errors.As(err, &sue) {
		t.Fatalf("expected StoreUnavailableError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Authorize with audit tests
// ---------------------------------------------------------------------------

func TestAuthorize_AuditSuccess(t *testing.T) {
	authz := &mockAuthorizer{}
	al := &mockAuditLogger{}
	authn := &mockAuthenticator{
		name:     "mtls",
		identity: &auth.Identity{Subject: "alice", TenantID: "t1"},
	}

	m := New(authz, WithAuthenticator(authn), WithAuditLogger(al))

	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}
	err := m.authorize(context.Background(), identity, "kv:put")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
	entry := al.entries[0]
	if entry.Subject != "alice" {
		t.Errorf("expected audit subject 'alice', got %q", entry.Subject)
	}
	if entry.Action != "auth.authorize" {
		t.Errorf("expected audit action 'auth.authorize', got %q", entry.Action)
	}
	if entry.Resource != "kv:put" {
		t.Errorf("expected audit resource 'kv:put', got %q", entry.Resource)
	}
	if entry.Result != audit.ResultSuccess {
		t.Errorf("expected audit result 'success', got %q", entry.Result)
	}
}

func TestAuthorize_AuditDenied(t *testing.T) {
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}
	al := &mockAuditLogger{}

	m := New(authz, WithAuditLogger(al))

	identity := &auth.Identity{Subject: "bob", TenantID: "t2"}
	err := m.authorize(context.Background(), identity, "admin:drain")
	if err == nil {
		t.Fatal("expected error")
	}

	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
	if al.entries[0].Result != audit.ResultDenied {
		t.Errorf("expected audit result 'denied', got %q", al.entries[0].Result)
	}
}

func TestAuthorize_NoAudit(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	identity := &auth.Identity{Subject: "alice"}
	err := m.authorize(context.Background(), identity, "kv:get")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No panic from nil audit logger.
}

// ---------------------------------------------------------------------------
// gRPC UnaryInterceptor tests
// ---------------------------------------------------------------------------

func TestUnaryInterceptor_Authorized(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Put"}
	handler := func(ctx context.Context, req any) (any, error) {
		// Verify identity is in context.
		ctxID := auth.IdentityFromContext(ctx)
		if ctxID == nil {
			t.Error("expected identity in handler context")
		}
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("expected 'ok', got %v", resp)
	}
	if authz.calls.Load() != 1 {
		t.Errorf("expected 1 authorize call, got %d", authz.calls.Load())
	}
}

func TestUnaryInterceptor_Unauthenticated(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Put"}
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
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated code, got %v", st.Code())
	}
}

func TestUnaryInterceptor_Unauthorized(t *testing.T) {
	id := &auth.Identity{Subject: "bob", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Put"}
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
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied code, got %v", st.Code())
	}
}

func TestUnaryInterceptor_NoMapping(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/UnknownMethod"}
	handlerCalled := false
	handler := func(_ context.Context, _ any) (any, error) {
		handlerCalled = true
		return "passed", nil
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Error("expected handler to be called")
	}
	if resp != "passed" {
		t.Errorf("expected 'passed', got %v", resp)
	}
	// Authorizer should NOT have been called.
	if authz.calls.Load() != 0 {
		t.Errorf("expected 0 authorize calls, got %d", authz.calls.Load())
	}
}

func TestUnaryInterceptor_UserDisabled(t *testing.T) {
	id := &auth.Identity{Subject: "disabled-user", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username: "disabled-user",
			TenantID: "t1",
			Enabled:  false,
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))
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
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied code, got %v", st.Code())
	}
}

// ---------------------------------------------------------------------------
// gRPC StreamInterceptor tests
// ---------------------------------------------------------------------------

type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

func TestStreamInterceptor_Authorized(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.StreamInterceptor()

	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}
	ss := &mockServerStream{ctx: context.Background()}

	handlerCalled := false
	handler := func(_ any, stream grpc.ServerStream) error {
		handlerCalled = true
		ctxID := auth.IdentityFromContext(stream.Context())
		if ctxID == nil {
			t.Error("expected identity in stream context")
		}
		return nil
	}

	err := interceptor(nil, ss, info, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Error("expected handler to be called")
	}
}

func TestStreamInterceptor_Unauthenticated(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
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
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", st.Code())
	}
}

func TestStreamInterceptor_NoMapping(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.StreamInterceptor()

	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/UnknownStream"}
	ss := &mockServerStream{ctx: context.Background()}

	handlerCalled := false
	err := interceptor(nil, ss, info, func(_ any, stream grpc.ServerStream) error {
		handlerCalled = true
		// Identity should still be in context even without mapping.
		ctxID := auth.IdentityFromContext(stream.Context())
		if ctxID == nil {
			t.Error("expected identity in stream context")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Error("expected handler to be called")
	}
	if authz.calls.Load() != 0 {
		t.Errorf("expected 0 authorize calls, got %d", authz.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// REST middleware tests
// ---------------------------------------------------------------------------

func TestRESTMiddleware_Authorized(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		ctxID := auth.IdentityFromContext(r.Context())
		if ctxID == nil {
			t.Error("expected identity in request context")
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := m.RESTMiddleware(next)

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("expected handler to be called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if authz.calls.Load() != 1 {
		t.Errorf("expected 1 authorize call, got %d", authz.calls.Load())
	}
}

func TestRESTMiddleware_Unauthenticated(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	})

	handler := m.RESTMiddleware(next)

	req := httptest.NewRequest(http.MethodPut, "/kv/key1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}

	var errResp restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestRESTMiddleware_Unauthorized(t *testing.T) {
	id := &auth.Identity{Subject: "bob", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}

	m := New(authz, WithAuthenticator(authn))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	})

	handler := m.RESTMiddleware(next)

	req := httptest.NewRequest(http.MethodPut, "/kv/key1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}

	var errResp restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestRESTMiddleware_NoMapping(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := m.RESTMiddleware(next)

	// /unknown is not in the mapping table.
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("expected handler to be called for unmapped route")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if authz.calls.Load() != 0 {
		t.Errorf("expected 0 authorize calls, got %d", authz.calls.Load())
	}
}

func TestRESTMiddleware_UserDisabled(t *testing.T) {
	id := &auth.Identity{Subject: "disabled-user", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username: "disabled-user",
			TenantID: "t1",
			Enabled:  false,
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	})

	handler := m.RESTMiddleware(next)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestRESTMiddleware_HealthPublic(t *testing.T) {
	// /health must be accessible without authentication so that load
	// balancers, Kubernetes probes, and monitoring can reach it.
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}
	m := New(authz, WithAuthenticator(authn))

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := m.RESTMiddleware(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("expected handler to be called for /health")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for /health, got %d", rec.Code)
	}
	if authn.calls.Load() != 0 {
		t.Errorf("expected 0 authenticate calls for /health, got %d", authn.calls.Load())
	}
}

func TestRESTMiddleware_CABootstrapPublic(t *testing.T) {
	// /v1/ca/bootstrap must be accessible without authentication so that
	// joining nodes without TLS credentials can bootstrap their trust store.
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}
	m := New(authz, WithAuthenticator(authn))

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := m.RESTMiddleware(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("expected handler to be called for /v1/ca/bootstrap")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for /v1/ca/bootstrap, got %d", rec.Code)
	}
	if authn.calls.Load() != 0 {
		t.Errorf("expected 0 authenticate calls for /v1/ca/bootstrap, got %d", authn.calls.Load())
	}
}

func TestRESTMiddleware_JSONContentType(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}
	m := New(authz, WithAuthenticator(authn))

	handler := m.RESTMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// REST normalizePath tests
// ---------------------------------------------------------------------------

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/kv", "/kv"},
		{"/kv/", "/kv"},
		{"/kv/mykey", "/kv"},
		{"/kv/deep/key/path", "/kv"},
		{"/kv/scan", "/kv/scan"},
		{"/kv/range", "/kv/range"},
		{"/kv/lease", "/kv/lease"},
		{"/kv/revision", "/kv/revision"},
		{"/kv/compact", "/kv/compact"},
		{"/health", "/health"},
		{"/status", "/status"},
		{"/tx", "/tx"},
		{"/batch", "/batch"},
		{"/vector/namespace", "/vector/namespace"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizePath(tt.input)
			if got != tt.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestUserDisabledError(t *testing.T) {
	e := &UserDisabledError{Username: "bob", TenantID: "t1"}
	want := "auth/middleware: user disabled: bob in tenant t1"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
}

func TestAuthChainError(t *testing.T) {
	t.Run("with_error", func(t *testing.T) {
		inner := errors.New("timeout")
		e := &AuthChainError{Err: inner}
		want := "auth/middleware: all authenticators failed: timeout"
		if e.Error() != want {
			t.Errorf("got %q, want %q", e.Error(), want)
		}
		if e.Unwrap() != inner {
			t.Error("Unwrap should return inner error")
		}
	})

	t.Run("nil_error", func(t *testing.T) {
		e := &AuthChainError{}
		want := "auth/middleware: all authenticators failed"
		if e.Error() != want {
			t.Errorf("got %q, want %q", e.Error(), want)
		}
	})
}

func TestAuditError(t *testing.T) {
	inner := errors.New("disk full")
	e := &AuditError{Err: inner}
	want := "auth/middleware: audit log failed: disk full"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap should return inner error")
	}
}

// ---------------------------------------------------------------------------
// mapGRPCAuthError tests
// ---------------------------------------------------------------------------

func TestMapGRPCAuthError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{
			name:     "unauthenticated",
			err:      auth.ErrUnauthenticated,
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "unauthorized",
			err:      auth.ErrUnauthorized,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "no_peer_cert",
			err:      auth.ErrNoPeerCertificate,
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "invalid_cert",
			err:      auth.ErrInvalidCertificate,
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "user_disabled",
			err:      &UserDisabledError{Username: "bob", TenantID: "t1"},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "store_unavailable",
			err:      &StoreUnavailableError{Err: errors.New("raft not ready")},
			wantCode: codes.Unavailable,
		},
		{
			name:     "chain_error",
			err:      &AuthChainError{Err: errors.New("timeout")},
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "generic_error",
			err:      errors.New("something"),
			wantCode: codes.Unauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grpcErr := mapGRPCAuthError(tt.err)
			st, ok := status.FromError(grpcErr)
			if !ok {
				t.Fatalf("expected gRPC status error, got %T", grpcErr)
			}
			if st.Code() != tt.wantCode {
				t.Errorf("expected %v, got %v", tt.wantCode, st.Code())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// mapHTTPAuthError tests
// ---------------------------------------------------------------------------

func TestMapHTTPAuthError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "unauthenticated",
			err:      auth.ErrUnauthenticated,
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "unauthorized",
			err:      auth.ErrUnauthorized,
			wantCode: http.StatusForbidden,
		},
		{
			name:     "no_peer_cert",
			err:      auth.ErrNoPeerCertificate,
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "user_disabled",
			err:      &UserDisabledError{Username: "bob", TenantID: "t1"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "store_unavailable",
			err:      &StoreUnavailableError{Err: errors.New("raft not ready")},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "chain_error",
			err:      &AuthChainError{Err: errors.New("timeout")},
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "generic_error",
			err:      errors.New("something"),
			wantCode: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapHTTPAuthError(tt.err)
			if got != tt.wantCode {
				t.Errorf("expected %d, got %d", tt.wantCode, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// writeJSONError tests
// ---------------------------------------------------------------------------

func TestWriteJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusForbidden, "access denied")

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}

	var resp restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Error != "access denied" {
		t.Errorf("expected 'access denied', got %q", resp.Error)
	}
}
