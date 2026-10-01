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
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

// GetUserByEmail records through the same fields as its username sibling, so a
// test can assert which handle a caller resolved by. It normalizes the way the
// real store does — a double that skipped that would accept spellings the store
// rejects.
func (m *mockUserServicer) GetUserByEmail(_ context.Context, _, email string) (*user.User, error) {
	m.calls.Add(1)
	m.lastUser = user.NormalizeEmail(email)
	if m.err != nil {
		return nil, m.err
	}
	return m.user, nil
}

func (m *mockUserServicer) CreateUser(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) GetUser(_ context.Context, _ uint64) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) UpdateUser(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) DeleteUser(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockUserServicer) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return nil, nil
}

func (m *mockUserServicer) EnableUser(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockUserServicer) DisableUser(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockUserServicer) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) RemoveCertBinding(_ context.Context, _ uint64, _ string) (*user.User, error) {
	return nil, nil
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
			ID:        1,
			Username:  "charlie",
			TenantID:  "t1",
			RoleIDs:   []string{"admin", "operator"},
			Lifecycle: user.LifecycleActive,
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

// TestAuthenticate_UserEnrichment_RoleBindingsAndAttributes asserts the
// go-qrdb#192 item 4 enrichment: the user's tenant-scoped RoleBindings land
// on the identity for rbac.effectiveRoles to consume, and the user's ABAC
// Attributes land on identity.Attributes — a SEPARATE field from Metadata,
// never merged into it (see auth.Identity.Attributes and
// abac.Decorator.Authorize for why: Attributes must never be able to shadow
// an authentication-derived Metadata key).
func TestAuthenticate_UserEnrichment_RoleBindingsAndAttributes(t *testing.T) {
	id := &auth.Identity{Subject: "charlie", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			ID:           1,
			Username:     "charlie",
			TenantID:     "t1",
			Lifecycle:    user.LifecycleActive,
			RoleBindings: []user.RoleBinding{{RoleID: "role-a", TenantID: "acme"}},
			Attributes:   map[string]string{"department": "eng"},
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(identity.RoleBindings) != 1 || identity.RoleBindings[0] != (auth.RoleBinding{RoleID: "role-a", TenantID: "acme"}) {
		t.Fatalf("identity.RoleBindings = %+v, want [{role-a acme}]", identity.RoleBindings)
	}
	if identity.Attributes["department"] != "eng" {
		t.Fatalf("identity.Attributes[department] = %q, want eng", identity.Attributes["department"])
	}
	if identity.Metadata != nil {
		t.Fatalf("identity.Metadata = %v, want nil: Attributes must never write into Metadata", identity.Metadata)
	}
}

// TestAuthenticate_UserEnrichment_NoRoleBindingsOrAttributesLeavesThemAbsent
// asserts a bare user (neither field set) does not manufacture an empty,
// non-nil RoleBindings slice or a non-nil Attributes map.
func TestAuthenticate_UserEnrichment_NoRoleBindingsOrAttributesLeavesThemAbsent(t *testing.T) {
	id := &auth.Identity{Subject: "charlie", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{ID: 1, Username: "charlie", TenantID: "t1", Lifecycle: user.LifecycleActive},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identity.RoleBindings != nil {
		t.Errorf("identity.RoleBindings = %v, want nil", identity.RoleBindings)
	}
	if identity.Attributes != nil {
		t.Errorf("identity.Attributes = %v, want nil", identity.Attributes)
	}
	if identity.Metadata != nil {
		t.Errorf("identity.Metadata = %v, want nil", identity.Metadata)
	}
}

// TestAuthenticate_UserEnrichment_StoreIsTheOnlySourceOfBindings asserts
// store-backed withdrawal fails closed: RoleBindings and Attributes an
// identity carried in from its authenticator never survive finalizeIdentity
// when a user store is configured — whether the record now holds none, does
// not exist, or cannot be read because its tenant is sealed.
func TestAuthenticate_UserEnrichment_StoreIsTheOnlySourceOfBindings(t *testing.T) {
	tests := []struct {
		name  string
		users *mockUserServicer
	}{
		{
			name:  "record holds none",
			users: &mockUserServicer{user: &user.User{ID: 1, Username: "charlie", TenantID: "t1", Lifecycle: user.LifecycleActive}},
		},
		{
			name:  "record not found",
			users: &mockUserServicer{err: &user.UserNotFoundError{ID: 1}},
		},
		{
			name:  "tenant sealed",
			users: &mockUserServicer{err: tenant.ErrTenantSealed},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := &auth.Identity{
				Subject:      "charlie",
				TenantID:     "t1",
				RoleBindings: []auth.RoleBinding{{RoleID: "stale-role", TenantID: "acme"}},
				Attributes:   map[string]string{"department": "stale"},
			}
			authn := &mockAuthenticator{name: "mtls", identity: id}
			m := New(&mockAuthorizer{}, WithAuthenticator(authn), WithUserService(tt.users))

			_, identity, err := m.authenticate(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if identity.RoleBindings != nil {
				t.Errorf("identity.RoleBindings = %+v, want nil", identity.RoleBindings)
			}
			if identity.Attributes != nil {
				t.Errorf("identity.Attributes = %+v, want nil", identity.Attributes)
			}
		})
	}
}

// TestAuthenticate_UserEnrichment_AttributesDoNotAliasTheRecord asserts the
// identity's Attributes is a copy, so a later mutation of the record's map
// cannot change an identity already issued.
func TestAuthenticate_UserEnrichment_AttributesDoNotAliasTheRecord(t *testing.T) {
	rec := &user.User{
		ID: 1, Username: "charlie", TenantID: "t1", Lifecycle: user.LifecycleActive,
		Attributes: map[string]string{"department": "eng"},
	}
	authn := &mockAuthenticator{name: "mtls", identity: &auth.Identity{Subject: "charlie", TenantID: "t1"}}
	m := New(&mockAuthorizer{}, WithAuthenticator(authn), WithUserService(&mockUserServicer{user: rec}))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rec.Attributes["department"] = "changed"
	if got := identity.Attributes["department"]; got != "eng" {
		t.Fatalf("identity.Attributes[department] = %q after mutating the record, want eng", got)
	}
}

func TestAuthenticate_UserDisabled(t *testing.T) {
	id := &auth.Identity{Subject: "disabled-user", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username:  "disabled-user",
			TenantID:  "t1",
			Lifecycle: user.LifecycleDisabled,
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
		err: &user.UserNotFoundError{Lookup: "unknown"},
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

// ---------------------------------------------------------------------------
// tenant.FromContext binding tests
// ---------------------------------------------------------------------------

// TestAuthenticate_BindsTenantIDFromIdentity verifies that after authenticate
// succeeds, tenant.FromContext returns the identity's TenantID. This is the
// canonical proof that the auth middleware is the sole source of tenant binding
// for all downstream services on all transports.
func TestAuthenticate_BindsTenantIDFromIdentity(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "acme-corp"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	ctx, _, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := tenant.FromContext(ctx)
	if got != "acme-corp" {
		t.Errorf("tenant.FromContext: got %q, want %q", got, "acme-corp")
	}
}

// TestAuthenticate_EmptyTenantIDFallsBackToSystem verifies that when an
// identity carries an empty TenantID, the context is bound to SystemTenantID
// so tenant.FromContext never returns an empty string.
func TestAuthenticate_EmptyTenantIDFallsBackToSystem(t *testing.T) {
	id := &auth.Identity{Subject: "daemon", TenantID: ""}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	ctx, _, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := tenant.FromContext(ctx)
	if got != tenant.SystemTenantID {
		t.Errorf("tenant.FromContext: got %q, want SystemTenantID %q", got, tenant.SystemTenantID)
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
// Identity-gated sentinel tests
//
// permission.IdentityGated short-circuits the RBAC check: an authenticated
// identity passes without consulting the authorizer (the binding
// authorization — officer registry membership, certificate-thumbprint match,
// nonce-signature or factor verification — is enforced at the handler
// layer), while an unauthenticated caller is still rejected with
// auth.ErrUnauthenticated.
// ---------------------------------------------------------------------------

func TestAuthorize_IdentityGated_SkipsRBAC(t *testing.T) {
	// An authorizer that would DENY anything it is asked about, proving the
	// sentinel never reaches the RBAC policy decision point.
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}
	al := &mockAuditLogger{}
	m := New(authz, WithAuditLogger(al))

	identity := &auth.Identity{Subject: "so-1", TenantID: "t1"}
	if err := m.authorize(context.Background(), identity, permission.IdentityGated); err != nil {
		t.Fatalf("identity-gated authorize with authenticated identity: %v", err)
	}

	if authz.calls.Load() != 0 {
		t.Errorf("authorizer was consulted %d times; identity-gated must short-circuit RBAC", authz.calls.Load())
	}
	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
	entry := al.entries[0]
	if entry.Result != audit.ResultSuccess {
		t.Errorf("expected audit result 'success', got %q", entry.Result)
	}
	if entry.Subject != "so-1" {
		t.Errorf("expected audit subject 'so-1', got %q", entry.Subject)
	}
	if entry.Resource != permission.IdentityGated.String() {
		t.Errorf("expected audit resource %q, got %q", permission.IdentityGated, entry.Resource)
	}
}

func TestAuthorize_IdentityGated_NilIdentity(t *testing.T) {
	authz := &mockAuthorizer{}
	al := &mockAuditLogger{}
	m := New(authz, WithAuditLogger(al))

	err := m.authorize(context.Background(), nil, permission.IdentityGated)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expected auth.ErrUnauthenticated, got %v", err)
	}
	if authz.calls.Load() != 0 {
		t.Errorf("authorizer was consulted %d times for an unauthenticated caller", authz.calls.Load())
	}
}

func TestAuthorizeTenant_IdentityGated_Authenticated(t *testing.T) {
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}
	m := New(authz)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "so-1", TenantID: "t1"})
	if err := m.AuthorizeTenant(ctx, "t2", permission.IdentityGated); err != nil {
		t.Fatalf("identity-gated AuthorizeTenant with authenticated identity: %v", err)
	}
	if authz.calls.Load() != 0 {
		t.Errorf("authorizer was consulted %d times; identity-gated must short-circuit RBAC", authz.calls.Load())
	}
}

func TestAuthorizeTenant_IdentityGated_Unauthenticated(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	err := m.AuthorizeTenant(context.Background(), "t1", permission.IdentityGated)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expected auth.ErrUnauthenticated, got %v", err)
	}
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

	// DEFAULT-DENY: an unmapped application method is rejected before the
	// handler runs.
	resp, err := interceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected unmapped method to be denied, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", status.Code(err))
	}
	if handlerCalled {
		t.Error("handler must NOT be called for an unmapped (denied) method")
	}
	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}
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
			Username:  "disabled-user",
			TenantID:  "t1",
			Lifecycle: user.LifecycleDisabled,
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

// TestUnaryInterceptor_IdentityGated_AuthenticatedPasses verifies that an
// identity-gated claim route admits an authenticated caller who has NO RBAC
// roles or grants: the deny-all authorizer is never consulted and the handler
// runs. This is the Security-Officer posture — officers are ceremony-registry
// principals, not RBAC users.
func TestUnaryInterceptor_IdentityGated_AuthenticatedPasses(t *testing.T) {
	id := &auth.Identity{Subject: "so-1", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{err: auth.ErrUnauthorized} // would deny anything asked

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierShareClaimBegin"}
	handlerCalled := false
	handler := func(_ context.Context, _ any) (any, error) {
		handlerCalled = true
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("identity-gated method denied for authenticated caller: %v", err)
	}
	if !handlerCalled || resp != "ok" {
		t.Errorf("handler not invoked (called=%v resp=%v)", handlerCalled, resp)
	}
	if authz.calls.Load() != 0 {
		t.Errorf("RBAC authorizer consulted %d times; identity-gated must short-circuit", authz.calls.Load())
	}
}

// TestUnaryInterceptor_IdentityGated_UnauthenticatedDenied verifies INV-4:
// the identity-gated sentinel still requires an authenticated identity, so a
// caller with no client certificate is rejected with Unauthenticated.
func TestUnaryInterceptor_IdentityGated_UnauthenticatedDenied(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
	interceptor := m.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierShareClaimBegin"}
	handler := func(_ context.Context, _ any) (any, error) {
		t.Error("handler should not be called")
		return nil, nil
	}

	_, err := interceptor(context.Background(), nil, info, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v (err=%v)", status.Code(err), err)
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
	err := interceptor(nil, ss, info, func(_ any, _ grpc.ServerStream) error {
		handlerCalled = true
		return nil
	})

	// DEFAULT-DENY: an unmapped stream method is rejected before the handler.
	if err == nil {
		t.Fatal("expected unmapped stream method to be denied, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", status.Code(err))
	}
	if handlerCalled {
		t.Error("handler must NOT be called for an unmapped (denied) stream method")
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

	handler := m.RESTMiddleware(testRESTRoutes(), next)

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

	handler := m.RESTMiddleware(testRESTRoutes(), next)

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

// TestRESTMiddleware_IdentityGated_AuthenticatedPasses verifies the REST
// officer share-claim routes admit an authenticated caller with no RBAC
// grants: the deny-all authorizer is never consulted, mirroring the gRPC
// identity-gated test. The handler layer enforces the binding authorization.
func TestRESTMiddleware_IdentityGated_AuthenticatedPasses(t *testing.T) {
	id := &auth.Identity{Subject: "so-1", TenantID: ""}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{err: auth.ErrUnauthorized} // would deny anything asked

	m := New(authz, WithAuthenticator(authn))

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	routes := []string{
		"/v1/ceremony/claim-share/begin",
		"/v1/ceremony/claim-share/complete",
		"/v1/barrier/officers/so-1/claim/begin",
		"/v1/barrier/officers/so-1/claim/complete",
		"/v1/barrier/recovery/officers/so-1/claim/begin",
		"/v1/barrier/recovery/officers/so-1/claim/complete",
	}
	for _, route := range routes {
		handlerCalled = false
		req := httptest.NewRequest(http.MethodPost, route, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || !handlerCalled {
			t.Errorf("POST %s: expected 200 + handler call, got %d (called=%v)", route, rec.Code, handlerCalled)
		}
	}
	if authz.calls.Load() != 0 {
		t.Errorf("RBAC authorizer consulted %d times; identity-gated must short-circuit", authz.calls.Load())
	}
}

// TestRESTMiddleware_IdentityGated_UnauthenticatedDenied verifies INV-4 on
// REST: an identity-gated claim route still rejects a caller with no client
// certificate with 401.
func TestRESTMiddleware_IdentityGated_UnauthenticatedDenied(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers/so-1/claim/begin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
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

	handler := m.RESTMiddleware(testRESTRoutes(), next)

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

	handler := m.RESTMiddleware(testRESTRoutes(), next)

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
			Username:  "disabled-user",
			TenantID:  "t1",
			Lifecycle: user.LifecycleDisabled,
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	})

	handler := m.RESTMiddleware(testRESTRoutes(), next)

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

	handler := m.RESTMiddleware(testRESTRoutes(), next)
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

	handler := m.RESTMiddleware(testRESTRoutes(), next)
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

// TestRESTMiddleware_ClusterPathsPublic verifies that /v1/cluster/ paths bypass
// user authentication entirely. The cluster peer key-transport plane is
// authorized inside the handler by node identity (SPIFFE SAN) and placement
// table, not by user RBAC. Authenticators must NOT be called for these paths.
func TestRESTMiddleware_ClusterPathsPublic(t *testing.T) {
	// Only the two EXACT cluster key-transport routes are pre-auth-exempt
	// (they self-authorize via node cert + placement). A deep/unknown path
	// under /v1/cluster/ is NOT exempt and must require authentication, so a
	// future handler mounted there is never silently unauthenticated.
	cases := []struct {
		path       string
		wantExempt bool
	}{
		{"/v1/cluster/keydelivery", true},
		{"/v1/cluster/nodekey", true},
		{"/v1/cluster/anything/deep", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
			m := New(&mockAuthorizer{}, WithAuthenticator(authn))
			handlerCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})
			handler := m.RESTMiddleware(testRESTRoutes(), next)
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tc.wantExempt {
				if rec.Code != http.StatusOK || !handlerCalled {
					t.Errorf("path %s: expected exempt (200 + handler), got %d (called=%v)", tc.path, rec.Code, handlerCalled)
				}
				if authn.calls.Load() != 0 {
					t.Errorf("path %s: authenticator called %d times; exact exempt route must skip authn", tc.path, authn.calls.Load())
				}
			} else {
				if handlerCalled {
					t.Errorf("path %s: expected NOT exempt (auth required), but handler was called", tc.path)
				}
				if authn.calls.Load() == 0 {
					t.Errorf("path %s: expected authenticator to run (path not exempt), but it did not", tc.path)
				}
			}
		})
	}
}

// TestRESTMiddleware_NonClusterV1PathStillRequiresAuth verifies that non-cluster
// /v1/ paths continue to require authentication even after the cluster exempt
// was added. Ensures the prefix check does not accidentally exempt everything
// under /v1/.
func TestRESTMiddleware_NonClusterV1PathStillRequiresAuth(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}
	m := New(authz, WithAuthenticator(authn))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be called for unauthenticated /v1/ path")
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("expected non-200 for unauthenticated /v1/barrier/status")
	}
	if authn.calls.Load() == 0 {
		t.Error("expected authenticator to be called for /v1/barrier/status")
	}
}

func TestRESTMiddleware_JSONContentType(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}
	m := New(authz, WithAuthenticator(authn))

	handler := m.RESTMiddleware(testRESTRoutes(), http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// RESTRouteRegistry lookup tests (replaces the old normalizePath tests: the
// /kv/{key} -> KV-permission collapse is now chi trie matching with static
// sub-routes winning over the wildcard)
// ---------------------------------------------------------------------------

func TestRESTRouteRegistry_KVLookups(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/kv", permission.KVGet)
	rr.Register(http.MethodPut, "/kv", permission.KVPut)
	rr.Register(http.MethodGet, "/kv/*", permission.KVGet)
	rr.Register(http.MethodPut, "/kv/*", permission.KVPut)
	rr.Register(http.MethodGet, "/kv/scan", permission.KVList)
	rr.Register(http.MethodGet, "/kv/range", permission.KVRange)

	tests := []struct {
		method string
		path   string
		want   permission.Permission
	}{
		{http.MethodGet, "/kv", permission.KVGet},
		{http.MethodGet, "/kv/", permission.KVGet},
		{http.MethodGet, "/kv/mykey", permission.KVGet},
		{http.MethodPut, "/kv/mykey", permission.KVPut},
		{http.MethodGet, "/kv/deep/key/path", permission.KVGet},
		// Static sub-routes declare their own permission and must never be
		// collapsed into the /kv/{key} wildcard.
		{http.MethodGet, "/kv/scan", permission.KVList},
		{http.MethodGet, "/kv/range", permission.KVRange},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got, kind, ok := rr.Lookup(tt.method, tt.path)
			if !ok || kind != RESTRoutePermission || got != tt.want {
				t.Errorf("Lookup(%q, %q) = %q (ok=%v kind=%v), want %q",
					tt.method, tt.path, got, ok, kind, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestUserDisabledError(t *testing.T) {
	// The condition is broader than "disabled" — a pending, suspended, locked
	// or deleting account fails the same gate — so the message names the state
	// when one is known. An operator needs it: a lockout is cleared by the
	// control that set it, a suspension by a human.
	e := &UserDisabledError{Username: "bob", TenantID: "t1"}
	want := "auth/middleware: user cannot authenticate: bob in tenant t1"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}

	withState := &UserDisabledError{Username: "bob", TenantID: "t1", Lifecycle: "locked"}
	wantState := "auth/middleware: user cannot authenticate: bob in tenant t1 (lifecycle locked)"
	if withState.Error() != wantState {
		t.Errorf("got %q, want %q", withState.Error(), wantState)
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

// The authentication gate asks the lifecycle state, the only account-state
// field. A record whose lifecycle says suspended, locked, pending, disabled or
// deleting is refused — and so is one that carries NO state at all: an absent
// lifecycle fails closed.
func TestAuthenticate_LifecycleGatesLogin(t *testing.T) {
	refused := []user.Lifecycle{
		"",
		user.LifecyclePending,
		user.LifecycleSuspended,
		user.LifecycleLocked,
		user.LifecycleDisabled,
		user.LifecycleDeleting,
	}
	for _, l := range refused {
		t.Run(string(l), func(t *testing.T) {
			id := &auth.Identity{Subject: "dana", TenantID: "t1"}
			users := &mockUserServicer{
				user: &user.User{
					ID:        2,
					Username:  "dana",
					TenantID:  "t1",
					Lifecycle: l,
				},
			}
			m := New(&mockAuthorizer{},
				WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}),
				WithUserService(users))

			_, _, err := m.authenticate(context.Background())
			var disabled *UserDisabledError
			if !errors.As(err, &disabled) {
				t.Fatalf("expected *UserDisabledError, got %T: %v", err, err)
			}
			if disabled.Lifecycle != string(l) {
				t.Errorf("Lifecycle: got %q, want %q", disabled.Lifecycle, l)
			}
		})
	}

	t.Run("active is admitted", func(t *testing.T) {
		id := &auth.Identity{Subject: "dana", TenantID: "t1"}
		users := &mockUserServicer{
			user: &user.User{
				ID: 2, Username: "dana", TenantID: "t1",
				Lifecycle: user.LifecycleActive,
			},
		}
		m := New(&mockAuthorizer{},
			WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}),
			WithUserService(users))
		if _, _, err := m.authenticate(context.Background()); err != nil {
			t.Fatalf("an active account was refused: %v", err)
		}
	})
}

// A permitted-method policy that is stored but never checked fails OPEN: a user
// restricted to webauthn could still walk in through the mTLS authenticator,
// because the credential is valid and nothing compared it against the policy.
func TestAuthenticate_AuthMethodPolicyIsEnforced(t *testing.T) {
	cases := []struct {
		name        string
		authName    string
		authMethods []string
		wantRefused bool
	}{
		{name: "empty policy permits anything", authName: "mtls", authMethods: nil, wantRefused: false},
		{name: "method in policy", authName: "mtls", authMethods: []string{"mtls", "piv"}, wantRefused: false},
		{name: "method not in policy", authName: "mtls", authMethods: []string{"webauthn"}, wantRefused: true},
		{
			// The policy is stored normalized, so the authenticator name must
			// be normalized the same way. Normalizing one side is how a policy
			// silently stops matching what it was written to allow.
			name: "case is folded on both sides", authName: "MTLS",
			authMethods: []string{"mtls"}, wantRefused: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := &auth.Identity{Subject: "erin", TenantID: "t1"}
			users := &mockUserServicer{
				user: &user.User{
					ID: 3, Username: "erin", TenantID: "t1",
					Lifecycle:   user.LifecycleActive,
					AuthMethods: tc.authMethods,
					RoleIDs:     []string{"viewer"},
				},
			}
			m := New(&mockAuthorizer{},
				WithAuthenticator(&mockAuthenticator{name: tc.authName, identity: id}),
				WithUserService(users))

			_, _, err := m.authenticate(context.Background())
			var refused *AuthMethodNotPermittedError
			switch {
			case tc.wantRefused:
				if !errors.As(err, &refused) {
					t.Fatalf("expected *AuthMethodNotPermittedError, got %T: %v", err, err)
				}
				if refused.Method != "mtls" {
					t.Errorf("Method: got %q, want %q", refused.Method, "mtls")
				}
				if len(refused.Permitted) != len(tc.authMethods) {
					t.Errorf("Permitted: got %v", refused.Permitted)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
			}
		})
	}
}

// The policy can only be enforced where the method is KNOWN. A forwarded
// on-behalf assertion and a capability module's session token carry no method
// qrdb can observe, so qrdb does not pretend to check them — refusing on a
// policy it could not evaluate would deny a request on no evidence.
func TestCheckAuthMethod_UnobservableIsNotEnforcedButBlankFailsClosed(t *testing.T) {
	u := &user.User{Username: "erin", TenantID: "t1", AuthMethods: []string{"webauthn"}}

	// UNOBSERVABLE: on-behalf and the capability-module fallback. Refusing on a
	// policy qrdb could not evaluate would deny a request on no evidence.
	if err := checkAuthMethod(u, unknownMethod()); err != nil {
		t.Fatalf("an unobservable method must not be refused: %v", err)
	}
	if err := checkAuthMethod(nil, knownMethod("mtls")); err != nil {
		t.Fatalf("nil user: %v", err)
	}
	if err := checkAuthMethod(&user.User{}, knownMethod("mtls")); err != nil {
		t.Fatalf("empty policy: %v", err)
	}

	// OBSERVED but blank or unnormalizable. This is the case that must NOT be
	// mistaken for unobservable: it matches no policy entry, and a
	// misconfigured authenticator must not become a way past the policy.
	for _, name := range []string{"", "   ", "\t"} {
		if err := checkAuthMethod(u, knownMethod(name)); err == nil {
			t.Errorf("an observed method named %q was admitted against a non-empty policy", name)
		}
	}
}

// The bypass is keyed on a FLAG, not on an empty string. An authenticator that
// names itself "" is observed, not unobservable — conflating the two let it
// walk straight past a non-empty policy through the normal qrdb-owned chain
// (Codex, PR #198).
func TestAuthenticate_BlankAuthenticatorNameCannotBypassThePolicy(t *testing.T) {
	id := &auth.Identity{Subject: "erin", TenantID: "t1"}
	users := &mockUserServicer{
		user: &user.User{
			ID: 3, Username: "erin", TenantID: "t1",
			Lifecycle:   user.LifecycleActive,
			AuthMethods: []string{"mtls"},
		},
	}
	m := New(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "", identity: id}),
		WithUserService(users))

	_, _, err := m.authenticate(context.Background())
	var refused *AuthMethodNotPermittedError
	if !errors.As(err, &refused) {
		t.Fatalf("expected *AuthMethodNotPermittedError, got %T: %v", err, err)
	}

	// And with NO policy the same authenticator is still fine: the fail-closed
	// rule must not become a lockout for accounts that state no restriction.
	users.user.AuthMethods = nil
	if _, _, uerr := m.authenticate(context.Background()); uerr != nil {
		t.Fatalf("a blank-named authenticator was refused despite an empty policy: %v", uerr)
	}
}

// CheckAuthMethod is the exported form of the rule, run by a layer that
// performs its own login ceremony. It must admit and refuse exactly what the
// middleware does for an OBSERVED method, and it names the refused method in
// the same normalized spelling the policy is stored in.
func TestCheckAuthMethod_AppliesThePolicy(t *testing.T) {
	u := &user.User{Username: "erin", TenantID: "t1", AuthMethods: []string{auth.MethodPIV, auth.MethodWebAuthn}}

	cases := []struct {
		name       string
		u          *user.User
		method     string
		wantRefuse bool
		wantMethod string
	}{
		{name: "nil user states no restriction", u: nil, method: auth.MethodMTLS},
		{name: "empty policy states no restriction", u: &user.User{Username: "erin"}, method: auth.MethodMTLS},
		{name: "permitted method", u: u, method: auth.MethodPIV},
		{name: "permitted after normalization", u: u, method: "  WebAuthn "},
		{name: "method outside the policy", u: u, method: auth.MethodMTLS, wantRefuse: true, wantMethod: auth.MethodMTLS},
		{name: "refusal names the normalized method", u: u, method: "MTLS", wantRefuse: true, wantMethod: auth.MethodMTLS},
		{name: "blank method fails closed", u: u, method: "  ", wantRefuse: true, wantMethod: "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckAuthMethod(tc.u, tc.method)
			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("CheckAuthMethod(%q) refused: %v", tc.method, err)
				}
				return
			}
			var refused *AuthMethodNotPermittedError
			if !errors.As(err, &refused) {
				t.Fatalf("expected *AuthMethodNotPermittedError, got %T: %v", err, err)
			}
			if refused.Method != tc.wantMethod {
				t.Errorf("Method = %q, want %q", refused.Method, tc.wantMethod)
			}
			if refused.Username != "erin" || refused.TenantID != "t1" {
				t.Errorf("refusal names %s/%s, want erin/t1", refused.TenantID, refused.Username)
			}
			if len(refused.Permitted) != 2 {
				t.Errorf("Permitted = %v, want the stored policy", refused.Permitted)
			}
		})
	}
}

// The middleware and the exported function are one rule. For every observed
// method, the middleware's decision must be CheckAuthMethod's decision — a
// ceremony layer calling the exported form cannot be more or less permissive
// than the transports.
func TestCheckAuthMethod_IsTheMiddlewareRule(t *testing.T) {
	u := &user.User{Username: "erin", TenantID: "t1", AuthMethods: []string{auth.MethodWebAuthn}}
	for _, name := range []string{auth.MethodMTLS, auth.MethodPIV, auth.MethodWebAuthn, "WEBAUTHN", "", " "} {
		exported := CheckAuthMethod(u, name)
		internal := checkAuthMethod(u, knownMethod(name))
		if (exported == nil) != (internal == nil) {
			t.Errorf("method %q: exported=%v, middleware=%v", name, exported, internal)
		}
	}
}

// A policy stored before the vocabulary was enforced may hold a token qrdb no
// longer accepts on write. The gate must not lock that account out: the entry
// still matches the method it names (a composing host's ceremony reporting
// it), a known entry beside it still admits, and anything else is refused as
// before.
func TestCheckAuthMethod_LegacyStoredPolicyIsNotALockout(t *testing.T) {
	u := &user.User{Username: "carol", TenantID: "t1", AuthMethods: []string{"legacy-otp", auth.MethodMTLS}}
	for _, admitted := range []string{"legacy-otp", "Legacy-OTP", auth.MethodMTLS} {
		if err := CheckAuthMethod(u, admitted); err != nil {
			t.Errorf("CheckAuthMethod(%q) against a legacy policy: %v", admitted, err)
		}
	}
	var refused *AuthMethodNotPermittedError
	if err := CheckAuthMethod(u, auth.MethodPIV); !errors.As(err, &refused) {
		t.Errorf("piv against a policy without it: err = %v, want a refusal", err)
	}
}

func TestAuthMethodNotPermittedError_Message(t *testing.T) {
	e := &AuthMethodNotPermittedError{
		Username: "erin", TenantID: "t1", Method: "mtls", Permitted: []string{"webauthn", "piv"},
	}
	want := "auth/middleware: authentication method mtls is not permitted for erin in tenant t1 (permitted: webauthn, piv)"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

// The refusal is 403 / PermissionDenied, not 401 / Unauthenticated: the
// credential is valid and retrying the same method will never succeed, so
// telling the client to present credentials again is the wrong instruction.
func TestAuthMethodNotPermitted_MapsToForbidden(t *testing.T) {
	err := &AuthMethodNotPermittedError{Username: "erin", TenantID: "t1", Method: "mtls"}
	if got := mapHTTPAuthError(err); got != http.StatusForbidden {
		t.Errorf("REST: got %d, want %d", got, http.StatusForbidden)
	}
	if got := status.Code(mapGRPCAuthError(err)); got != codes.PermissionDenied {
		t.Errorf("gRPC: got %v, want %v", got, codes.PermissionDenied)
	}
}

// TestAuthenticate_NoUserStoreCarriesNoBindings asserts RoleBindings and
// Attributes an authenticator hands in never survive authentication, even
// with no user store configured: they are user-record state, and with no
// store there is no record to grant them.
func TestAuthenticate_NoUserStoreCarriesNoBindings(t *testing.T) {
	id := &auth.Identity{
		Subject:      "charlie",
		TenantID:     "t1",
		RoleBindings: []auth.RoleBinding{{RoleID: "smuggled", TenantID: "acme"}},
		Attributes:   map[string]string{"department": "smuggled"},
	}
	m := New(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identity.RoleBindings != nil || identity.Attributes != nil {
		t.Fatalf("identity kept RoleBindings=%v Attributes=%v, want both cleared", identity.RoleBindings, identity.Attributes)
	}
}
