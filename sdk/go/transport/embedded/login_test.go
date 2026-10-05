// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock SessionIssuer
// ---------------------------------------------------------------------------

type memSessionIssuer struct {
	mu       sync.Mutex
	bearers  map[string]*auth.Identity
	issueFn  func(ctx context.Context, id *auth.Identity) (string, time.Time, error)
	revokeFn func(ctx context.Context, bearer string) error
	ttl      time.Duration
}

func newMemSessionIssuer() *memSessionIssuer {
	return &memSessionIssuer{
		bearers: make(map[string]*auth.Identity),
		ttl:     time.Hour,
	}
}

func (m *memSessionIssuer) Issue(ctx context.Context, id *auth.Identity) (string, time.Time, error) {
	if m.issueFn != nil {
		return m.issueFn(ctx, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	bearer := uuid.NewString()
	m.bearers[bearer] = id
	return bearer, time.Now().Add(m.ttl).UTC(), nil
}

func (m *memSessionIssuer) Resolve(_ context.Context, bearer string) (*auth.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.bearers[bearer]
	if !ok {
		return nil, &transport.InvalidBearerError{Reason: "unknown"}
	}
	return id, nil
}

func (m *memSessionIssuer) Revoke(ctx context.Context, bearer string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, bearer)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bearers, bearer)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// memUserLookup adapts a single Principal into auth.UserLookup. Tests
// inject this so the LoginResolver can resolve a factor's UserID into a
// principal.
type memUserLookup struct {
	principals map[uint64]*auth.Principal
}

func (m *memUserLookup) LookupUser(_ context.Context, userID uint64) (*auth.Principal, error) {
	p, ok := m.principals[userID]
	if !ok {
		return nil, &auth.UserHasNoFactorsError{UserID: userID}
	}
	return p, nil
}

func newLoginTestTransport(t *testing.T, factorSvc *memUserAuthFactorService, lookup *memUserLookup, issuer SessionIssuer) *Transport {
	t.Helper()
	resolver := auth.NewLoginResolver(factorSvc, lookup)
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserAuthFactorService(factorSvc),
		WithLoginResolver(resolver),
		WithSessionIssuer(issuer),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func enrolledFactor(t *testing.T, svc *memUserAuthFactorService, userID uint64, typ auth.AuthFactorType, cred []byte) *auth.AuthFactor {
	t.Helper()
	f := &auth.AuthFactor{
		UserID:     userID,
		Type:       typ,
		Credential: cred,
	}
	if err := svc.Add(context.Background(), userID, f); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return f
}

// ---------------------------------------------------------------------------
// BeginLogin
// ---------------------------------------------------------------------------

func TestLoginClient_BeginLogin_Happy(t *testing.T) {
	tr := newLoginTestTransport(t,
		newMemUserAuthFactorService(),
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	resp, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type: transport.FactorTypeFIDO2,
		Hint: "alice",
	})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if resp.SessionID == "" {
		t.Error("expected non-empty SessionID")
	}
	if len(resp.Nonce) != loginNonceLen {
		t.Errorf("expected nonce of len %d, got %d", loginNonceLen, len(resp.Nonce))
	}
	if !resp.ExpiresAt.After(time.Now()) {
		t.Error("expected ExpiresAt in the future")
	}
}

func TestLoginClient_BeginLogin_InvalidType(t *testing.T) {
	tr := newLoginTestTransport(t,
		newMemUserAuthFactorService(),
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	_, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type: transport.FactorType("bogus"),
	})
	if err == nil {
		t.Fatal("expected FactorTypeUnsupportedError")
	}
	var typeErr *transport.FactorTypeUnsupportedError
	if !errors.As(err, &typeErr) {
		t.Errorf("expected *transport.FactorTypeUnsupportedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// FinishLogin
// ---------------------------------------------------------------------------

func TestLoginClient_FinishLogin_Happy(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("cred-1")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "acme", Roles: []string{"User"}, Active: true},
	}}
	issuer := newMemSessionIssuer()
	tr := newLoginTestTransport(t, svc, lookup, issuer)
	lc := tr.LoginClient()

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type: transport.FactorTypeFIDO2,
	})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	finish, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: cred,
	})
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if finish.Bearer == "" {
		t.Error("expected non-empty bearer")
	}
	if finish.Identity == nil || finish.Identity.Subject != "alice" {
		t.Errorf("expected identity for alice, got %+v", finish.Identity)
	}
	if finish.Identity.TenantID != "acme" {
		t.Errorf("expected tenantID acme, got %q", finish.Identity.TenantID)
	}
}

func TestLoginClient_FinishLogin_UnknownSession(t *testing.T) {
	tr := newLoginTestTransport(t,
		newMemUserAuthFactorService(),
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	_, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: "nope",
		Type:      transport.FactorTypeFIDO2,
		LookupKey: []byte("c"),
	})
	if err == nil {
		t.Fatal("expected LoginSessionNotFoundError")
	}
	var nf *transport.LoginSessionNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *transport.LoginSessionNotFoundError, got %T", err)
	}
}

func TestLoginClient_FinishLogin_TypeMismatch(t *testing.T) {
	tr := newLoginTestTransport(t,
		newMemUserAuthFactorService(),
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type: transport.FactorTypeFIDO2,
	})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	_, err = lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypePIV,
		LookupKey: []byte("c"),
	})
	if err == nil {
		t.Fatal("expected mismatch denial")
	}
}

func TestLoginClient_FinishLogin_Expired(t *testing.T) {
	svc := newMemUserAuthFactorService()
	tr := newLoginTestTransport(t,
		svc,
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	// Pin nowFunc so we can advance time deterministically.
	base := time.Now().UTC()
	tr.nowFunc = func() time.Time { return base }

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type: transport.FactorTypeFIDO2,
	})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	// Jump past the TTL.
	tr.nowFunc = func() time.Time { return base.Add(loginSessionTTL + time.Second) }

	_, err = lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: []byte("c"),
	})
	if err == nil {
		t.Fatal("expected LoginSessionExpiredError")
	}
	var expired *transport.LoginSessionExpiredError
	if !errors.As(err, &expired) {
		t.Errorf("expected *transport.LoginSessionExpiredError, got %T", err)
	}
}

func TestLoginClient_FinishLogin_NoFactorMatch(t *testing.T) {
	svc := newMemUserAuthFactorService()
	tr := newLoginTestTransport(t,
		svc,
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	begin, _ := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type: transport.FactorTypeFIDO2,
	})
	_, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: []byte("does-not-exist"),
	})
	if err == nil {
		t.Fatal("expected resolver miss to surface")
	}
}

func TestLoginClient_FinishLogin_MissingResolver(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithSessionIssuer(newMemSessionIssuer()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lc := tr.LoginClient()

	_, err = lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: "any",
		Type:      transport.FactorTypeFIDO2,
		LookupKey: []byte("c"),
	})
	if err == nil {
		t.Fatal("expected MissingLoginResolverError")
	}
	var miss *MissingLoginResolverError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingLoginResolverError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// ResolveIdentity / Logout
// ---------------------------------------------------------------------------

func TestLoginClient_ResolveIdentity_RoundTrip(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("cred-1")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "acme", Roles: []string{"User"}, Active: true},
	}}
	issuer := newMemSessionIssuer()
	tr := newLoginTestTransport(t, svc, lookup, issuer)
	lc := tr.LoginClient()

	begin, _ := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type: transport.FactorTypeFIDO2,
	})
	finish, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: cred,
	})
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}

	id, err := lc.ResolveIdentity(context.Background(), finish.Bearer)
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if id.Subject != "alice" {
		t.Errorf("expected alice, got %q", id.Subject)
	}
}

func TestLoginClient_ResolveIdentity_EmptyBearer(t *testing.T) {
	tr := newLoginTestTransport(t,
		newMemUserAuthFactorService(),
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	_, err := lc.ResolveIdentity(context.Background(), "")
	if err == nil {
		t.Fatal("expected InvalidBearerError")
	}
	var ibe *transport.InvalidBearerError
	if !errors.As(err, &ibe) {
		t.Errorf("expected *transport.InvalidBearerError, got %T", err)
	}
}

func TestLoginClient_Logout_RemovesBearer(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("c")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "acme", Active: true},
	}}
	issuer := newMemSessionIssuer()
	tr := newLoginTestTransport(t, svc, lookup, issuer)
	lc := tr.LoginClient()

	begin, _ := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2})
	finish, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: cred,
	})
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}

	if err := lc.Logout(context.Background(), finish.Bearer); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	_, err = lc.ResolveIdentity(context.Background(), finish.Bearer)
	if err == nil {
		t.Fatal("expected ResolveIdentity to fail after Logout")
	}
}

func TestLoginClient_Logout_EmptyBearer(t *testing.T) {
	tr := newLoginTestTransport(t,
		newMemUserAuthFactorService(),
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	lc := tr.LoginClient()

	err := lc.Logout(context.Background(), "")
	if err == nil {
		t.Fatal("expected InvalidBearerError on empty token")
	}
}

// ---------------------------------------------------------------------------
// Default identity hydrator
// ---------------------------------------------------------------------------

func TestDefaultIdentityHydrator_Hydrate_PassesThrough(t *testing.T) {
	h := &defaultIdentityHydrator{}
	id, err := h.Hydrate(context.Background(),
		&auth.Principal{ID: 1, Username: "alice", TenantID: "acme", Roles: []string{"User"}, Active: true},
		&auth.AuthFactor{ID: "f-1", Type: auth.AuthFactorFIDO2},
	)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if id.Subject != "alice" || id.TenantID != "acme" {
		t.Errorf("unexpected identity: %+v", id)
	}
	if id.Metadata["factor_id"] != "f-1" {
		t.Error("expected factor_id metadata")
	}
}

func TestDefaultIdentityHydrator_Hydrate_NilPrincipal(t *testing.T) {
	h := &defaultIdentityHydrator{}
	_, err := h.Hydrate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected nil-principal error")
	}
}

// confirm WithIdentityHydrator round-trips a custom hydrator.
type stubHydrator struct {
	called bool
}

func (s *stubHydrator) Hydrate(_ context.Context, _ *auth.Principal, _ *auth.AuthFactor) (*auth.Identity, error) {
	s.called = true
	return &auth.Identity{Subject: "stubbed", TenantID: "any"}, nil
}

func TestLoginClient_FinishLogin_WithCustomHydrator(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("c")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "acme", Active: true},
	}}
	hyd := &stubHydrator{}
	resolver := auth.NewLoginResolver(svc, lookup)
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserAuthFactorService(svc),
		WithLoginResolver(resolver),
		WithSessionIssuer(newMemSessionIssuer()),
		WithIdentityHydrator(hyd),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lc := tr.LoginClient()

	begin, _ := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2})
	finish, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: cred,
	})
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if !hyd.called {
		t.Error("custom hydrator was not invoked")
	}
	if finish.Identity.Subject != "stubbed" {
		t.Errorf("expected hydrated subject 'stubbed', got %q", finish.Identity.Subject)
	}
}

// ---------------------------------------------------------------------------
// FinishLogin proof verification (security gap fix): the ceremony MUST verify
// the proof-of-possession via the registered verifier before issuing a bearer.
// ---------------------------------------------------------------------------

// loginTransportWithVerifier mirrors newLoginTestTransport and also registers
// a FactorVerifier so the FinishLogin proof-verification path is exercised.
func loginTransportWithVerifier(t *testing.T, factorSvc *memUserAuthFactorService, lookup *memUserLookup, issuer SessionIssuer, v FactorVerifier) *Transport {
	t.Helper()
	resolver := auth.NewLoginResolver(factorSvc, lookup)
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserAuthFactorService(factorSvc),
		WithLoginResolver(resolver),
		WithSessionIssuer(issuer),
		WithFactorVerifier(v),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestLoginClient_FinishLogin_VerifiesProofBeforeIssuing(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("cred-verify")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "acme", Roles: []string{"User"}, Active: true},
	}}
	var gotNonce, gotSig string
	verifier := &mockVerifier{typ: transport.FactorTypeFIDO2, verifyF: func(_ context.Context, _ *auth.AuthFactor, ch transport.VerifyChallenge) error {
		gotNonce, gotSig = string(ch.Nonce), string(ch.Signature)
		return nil
	}}
	tr := loginTransportWithVerifier(t, svc, lookup, newMemSessionIssuer(), verifier)
	lc := tr.LoginClient()

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2, TenantID: "acme"})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	finish, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID, Type: transport.FactorTypeFIDO2, LookupKey: cred, Signature: []byte("sig"),
	})
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if finish.Bearer == "" {
		t.Error("expected non-empty bearer")
	}
	if gotNonce != string(begin.Nonce) {
		t.Errorf("verifier received nonce %q, want the BeginLogin challenge %q", gotNonce, string(begin.Nonce))
	}
	if gotSig != "sig" {
		t.Errorf("verifier received signature %q, want %q", gotSig, "sig")
	}
}

func TestLoginClient_FinishLogin_RejectsBadProof(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("cred-bad")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "a", TenantID: "t", Active: true},
	}}
	verifier := &mockVerifier{typ: transport.FactorTypeFIDO2, verifyF: func(_ context.Context, _ *auth.AuthFactor, _ transport.VerifyChallenge) error {
		return errors.New("bad signature")
	}}
	tr := loginTransportWithVerifier(t, svc, lookup, newMemSessionIssuer(), verifier)
	lc := tr.LoginClient()

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID, Type: transport.FactorTypeFIDO2, LookupKey: cred, Signature: []byte("nope"),
	})
	if err == nil {
		t.Fatal("expected FinishLogin to reject an invalid proof")
	}
	var ve *transport.FactorVerificationError
	if !errors.As(err, &ve) {
		t.Errorf("expected *transport.FactorVerificationError, got %T", err)
	}
}

func TestLoginClient_FinishLogin_FailsClosedWhenProofButNoVerifier(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("cred-nf")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "a", TenantID: "t", Active: true},
	}}
	tr := newLoginTestTransport(t, svc, lookup, newMemSessionIssuer()) // NO verifier registered
	lc := tr.LoginClient()

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID, Type: transport.FactorTypeFIDO2, LookupKey: cred, Signature: []byte("proof"),
	})
	if err == nil {
		t.Fatal("expected fail-closed: proof supplied but no verifier registered")
	}
	var ue *transport.FactorTypeUnsupportedError
	if !errors.As(err, &ue) {
		t.Errorf("expected *transport.FactorTypeUnsupportedError (fail closed), got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Tenant-scoped resolution (go-qrdb#303)
//
// FinishLogin must resolve the presented (type, lookup key) against the
// login SESSION's tenant — BeginLoginRequest.TenantID, captured on entry
// before the caller's own ctx (which is unauthenticated for this endpoint
// and carries no tenant) is ever consulted. tenantScopedFactorStore below is
// a FactorStore double that indexes by (tenant, type, key) instead of the
// production store's flat system-wide keyspace, so these tests observe
// exactly what tenant FinishLogin resolved on rather than what a
// non-tenant-aware double would silently paper over.
// ---------------------------------------------------------------------------

// tenantScopedFactorStore is an auth.FactorStore double keyed by
// (tenant, type, credential), unlike memUserAuthFactorService which ignores
// ctx entirely. It reads the tenant FinishLogin bound to the resolve
// context via sdk.TenantFromContext, normalizing an absent binding to the
// system tenant exactly as pkg/tenant.FromContext does, so a test can prove
// which tenant a resolve call actually ran against.
type tenantScopedFactorStore struct {
	// factors is keyed by tenantFactorKey(tenant, type, credential).
	factors map[string]*auth.AuthFactor
}

func newTenantScopedFactorStore() *tenantScopedFactorStore {
	return &tenantScopedFactorStore{factors: make(map[string]*auth.AuthFactor)}
}

// enroll records factor as belonging to tenantID, mirroring what a
// tenant-scoped enrolment write would persist.
func (s *tenantScopedFactorStore) enroll(tenantID string, factor *auth.AuthFactor) {
	s.factors[tenantFactorKey(tenantID, factor.Type, factor.Credential)] = factor
}

func tenantFactorKey(tenantID string, t auth.AuthFactorType, cred []byte) string {
	return tenantID + "/" + string(t) + "/" + string(cred)
}

// LookupByCredentialKey implements auth.FactorStore, resolving against the
// tenant bound to ctx rather than a fixed keyspace.
func (s *tenantScopedFactorStore) LookupByCredentialKey(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
	tenantID, ok := sdk.TenantFromContext(ctx)
	if !ok {
		tenantID = sdk.SystemTenantID
	}
	f, ok := s.factors[tenantFactorKey(tenantID, t, key)]
	if !ok {
		return nil, &auth.FactorNotFoundError{}
	}
	return f, nil
}

// MarkUsed implements auth.FactorStore. The async LastUsedAt update is not
// under test here, so it is a no-op.
func (s *tenantScopedFactorStore) MarkUsed(context.Context, string, time.Time) error {
	return nil
}

// TestLoginClient_FinishLogin_ScopesResolutionToSessionTenant is the
// positive half of go-qrdb#303's verification: a factor enrolled for tenant
// A resolves a ceremony BEGUN as tenant A, even though the caller's own ctx
// (context.Background()) carries no tenant of its own. If FinishLogin still
// resolved on the caller's context, this would behave identically to the
// cross-tenant case below rather than succeeding — proving the session's
// tenant, not the ambient default, is what makes the match.
func TestLoginClient_FinishLogin_ScopesResolutionToSessionTenant(t *testing.T) {
	store := newTenantScopedFactorStore()
	cred := []byte("cred-tenant-a")
	factor := &auth.AuthFactor{ID: "f-a", UserID: 1, Type: auth.AuthFactorFIDO2, Credential: cred}
	store.enroll("tenant-a", factor)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "tenant-a", Roles: []string{"User"}, Active: true},
	}}
	resolver := auth.NewLoginResolver(store, lookup)
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithLoginResolver(resolver),
		WithSessionIssuer(newMemSessionIssuer()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lc := tr.LoginClient()

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type:     transport.FactorTypeFIDO2,
		TenantID: "tenant-a",
	})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	finish, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: cred,
	})
	if err != nil {
		t.Fatalf("FinishLogin: expected the session's own tenant to resolve, got: %v", err)
	}
	if finish.Identity == nil || finish.Identity.Subject != "alice" {
		t.Errorf("expected identity for alice, got %+v", finish.Identity)
	}
}

// TestLoginClient_FinishLogin_DeniesCrossTenantResolution is the negative
// half of go-qrdb#303's verification: a factor enrolled for tenant A must
// NOT resolve a ceremony begun with TenantID "tenant-b". Before the fix,
// resolution ran on the caller's ctx (context.Background(), no tenant
// bound → system tenant) regardless of either BeginLoginRequest.TenantID
// value, so this case and the "tenant-a" case above resolved identically;
// this test is what turns red under `git revert` of the fix (see the PR
// description for the reverted-diff run).
func TestLoginClient_FinishLogin_DeniesCrossTenantResolution(t *testing.T) {
	store := newTenantScopedFactorStore()
	cred := []byte("cred-tenant-a")
	factor := &auth.AuthFactor{ID: "f-a", UserID: 1, Type: auth.AuthFactorFIDO2, Credential: cred}
	store.enroll("tenant-a", factor)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "tenant-a", Roles: []string{"User"}, Active: true},
	}}
	resolver := auth.NewLoginResolver(store, lookup)
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithLoginResolver(resolver),
		WithSessionIssuer(newMemSessionIssuer()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lc := tr.LoginClient()

	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{
		Type:     transport.FactorTypeFIDO2,
		TenantID: "tenant-b",
	})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	_, err = lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: cred,
	})
	if err == nil {
		t.Fatal("expected a tenant-A factor to be invisible to a tenant-B ceremony")
	}
	var nf *transport.FactorNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *transport.FactorNotFoundError, got %T", err)
	}
}

// TestLoginClient_FinishLogin_RateLimitsRepeatedAttemptsOnTheResolvedPrincipal
// pins AuthMiddleware.CheckLoginRateLimit's embedded seam end to end: a
// second FinishLogin attempt against the SAME account, within the SAME
// declared ratelimit.Policy window, is refused, even though each attempt
// opens its own fresh BeginLogin session with its own nonce -- the charge
// follows the RESOLVED principal (alice), not the per-attempt session or
// lookup key.
func TestLoginClient_FinishLogin_RateLimitsRepeatedAttemptsOnTheResolvedPrincipal(t *testing.T) {
	svc := newMemUserAuthFactorService()
	cred := []byte("cred-1")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{
		1: {ID: 1, Username: "alice", TenantID: "acme", Roles: []string{"User"}, Active: true},
	}}
	resolver := auth.NewLoginResolver(svc, lookup)

	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.UserAuthFactorsLogin, ratelimit.Policy{RatePerSecond: 1.0 / 3600, Burst: 1})
	mw := authmw.New(rbac.New(&mockRoleServicer{}), authmw.WithRateLimiter(limiter), authmw.WithRateLimitPolicies(policies))

	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserAuthFactorService(svc),
		WithLoginResolver(resolver),
		WithSessionIssuer(newMemSessionIssuer()),
		WithAuthMiddleware(mw),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lc := tr.LoginClient()

	attempt := func() error {
		begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2})
		if err != nil {
			t.Fatalf("BeginLogin: %v", err)
		}
		_, err = lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
			SessionID: begin.SessionID,
			Type:      transport.FactorTypeFIDO2,
			LookupKey: cred,
		})
		return err
	}

	if err := attempt(); err != nil {
		t.Fatalf("first attempt within burst: %v", err)
	}
	err = attempt()
	var limited *authmw.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("second attempt over burst: err = %v, want *authmw.RateLimitedError", err)
	}
}
