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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// go-qrdb#569: the SO gate recognises a real Security Officer
//
// Every piece below is the production one: the kvstore-backed role store with
// the built-in roles reconciled into it, the kvstore-backed user store, the
// real mTLS authenticator, the real RBAC authorizer and the real
// SOAuthenticator. The officer is a user record whose RoleIDs carry the SO
// role's persisted IDENTIFIER, which is the only shape the identity
// enrichment ever produces (identity.Roles = u.RoleIDs). The gate used to
// compare that against the role NAME, so every real officer was refused.
// ---------------------------------------------------------------------------

// soRealFixture holds the production stores a real SO request runs against.
type soRealFixture struct {
	roles    role.RoleServicer
	users    user.UserServicer
	verifier *auth.SOAuthenticator
	soRoleID string
}

// seededSORoles returns the production kvstore-backed role store with the
// built-in roles reconciled into soGateTenant, and the identifier the store
// persisted the SO role under -- the value a real officer's identity carries.
func seededSORoles(t *testing.T) (role.RoleServicer, string) {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), soGateTenant)
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	if _, err := roles.ReconcileBuiltInRoles(ctx, soGateTenant); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	so, err := roles.GetRoleByName(ctx, soGateTenant, role.RoleNameSO)
	if err != nil {
		t.Fatalf("GetRoleByName(so): %v", err)
	}
	return roles, so.ID
}

func newSORealFixture(t *testing.T, opts ...auth.SOOption) *soRealFixture {
	t.Helper()
	ctx := context.Background()
	roles, soRoleID := seededSORoles(t)
	verifier, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64), discardLogger(), opts...)
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if err := verifier.SetSOPIN(ctx, soGateTenant, []byte(soGatePIN)); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	return &soRealFixture{
		roles:    roles,
		users:    newRealUserStore(t),
		verifier: verifier,
		soRoleID: soRoleID,
	}
}

// roleID returns the persisted identifier of the named built-in role.
func (f *soRealFixture) roleID(t *testing.T, name string) string {
	t.Helper()
	r, err := f.roles.GetRoleByName(tenant.WithTenantID(context.Background(), soGateTenant), soGateTenant, name)
	if err != nil {
		t.Fatalf("GetRoleByName(%s): %v", name, err)
	}
	return r.ID
}

// handler is the REST chain order: auth (real RBAC) outside, the SO gate
// inside, then the handler, which records whether it ran.
func (f *soRealFixture) handler(ran *bool) http.Handler {
	routes := testRESTRoutes()
	m := New(rbac.New(f.roles),
		WithAuthenticator(auth.NewMTLSAuthenticator()),
		WithUserService(f.users),
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	})
	return m.RESTMiddleware(routes, SOMiddleware(routes, f.roles, f.verifier, nil, audit.FailClosed, discardLogger(), next))
}

// serveAs presents ck as the verified client certificate on an SO-gated
// route carrying the correct PIN.
func serveAs(h http.Handler, ck certKey) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, soGateRoute, nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{ck.cert},
		VerifiedChains:   [][]*x509.Certificate{{ck.cert}},
	}
	req.Header.Set(SOPINHeader, soGatePIN)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSOMiddleware_RealSOByRoleID_Passes is the #569 regression: a seeded SO,
// authenticated by mTLS, enriched from its user record and authorized by the
// real RBAC authorizer, reaches the handler.
func TestSOMiddleware_RealSOByRoleID_Passes(t *testing.T) {
	f := newSORealFixture(t)
	officer := createUser(t, f.users, soGateTenant, "officer", []string{f.soRoleID})
	cert := mintCert(t, "officer", soGateTenant, nil, "")
	enroll(t, f.users, officer.ID, cert)

	ran := false
	rec := serveAs(f.handler(&ran), cert)
	if rec.Code != http.StatusOK || !ran {
		t.Fatalf("real SO refused: status=%d ran=%v body=%q", rec.Code, ran, rec.Body.String())
	}
}

// TestSOMiddleware_RealNonSORole_Forbidden: a real user holding a real,
// non-SO role is refused at the role step even with the right PIN.
func TestSOMiddleware_RealNonSORole_Forbidden(t *testing.T) {
	f := newSORealFixture(t)
	// Admin is refused before the gate or at it; either way the handler
	// must not run and the answer is 403.
	admin := createUser(t, f.users, soGateTenant, "boss", []string{f.roleID(t, role.RoleNameAdmin)})
	cert := mintCert(t, "boss", soGateTenant, nil, "")
	enroll(t, f.users, admin.ID, cert)

	ran := false
	rec := serveAs(f.handler(&ran), cert)
	if rec.Code != http.StatusForbidden || ran {
		t.Fatalf("non-SO: status=%d ran=%v body=%q, want 403 and handler not run", rec.Code, ran, rec.Body.String())
	}
}

// TestSOMiddleware_NameBearingIdentity_Forbidden is the other half of #569:
// an identity carrying the role NAME "so" — as a token claim for a subject
// with no user record would — is not a Security Officer. It is driven into
// the gate directly, since that is the layer the claim would reach.
func TestSOMiddleware_NameBearingIdentity_Forbidden(t *testing.T) {
	f := newSORealFixture(t)
	ran := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	h := SOMiddleware(testRESTRoutes(), f.roles, f.verifier, nil, audit.FailClosed, discardLogger(), next)

	req := httptest.NewRequest(http.MethodPost, soGateRoute, nil)
	req.TLS = &tls.ConnectionState{}
	req.Header.Set(SOPINHeader, soGatePIN)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{
		Subject:  "claimant",
		TenantID: soGateTenant,
		Roles:    []string{role.RoleNameSO},
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || ran {
		t.Fatalf("name-bearing identity: status=%d ran=%v, want 403 and handler not run", rec.Code, ran)
	}
}

// failingRoleResolver is a role store that cannot answer, the way a node whose
// role shard is unreachable cannot.
type failingRoleResolver struct{}

func (failingRoleResolver) GetRoleByName(context.Context, string, string) (*role.Role, error) {
	return nil, &role.StoreError{Op: "get", Err: context.DeadlineExceeded}
}

// serveSOGate drives one SO-gated request with the correct PIN through a gate
// built over roles, as an identity carrying soRoleID.
func serveSOGate(t *testing.T, roles role.NameResolver, soRoleID string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	f := newSORealFixture(t)
	ran := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	h := SOMiddleware(testRESTRoutes(), roles, f.verifier, nil, audit.FailClosed, discardLogger(), next)
	req := httptest.NewRequest(http.MethodPost, soGateRoute, nil)
	req.TLS = &tls.ConnectionState{}
	req.Header.Set(SOPINHeader, soGatePIN)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{
		Subject: "officer", TenantID: soGateTenant, Roles: []string{soRoleID},
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, ran
}

// TestSOMiddleware_NoRoleResolver_Refuses503: a gate wired without a role
// store cannot tell who the officer is, and refuses rather than guessing.
func TestSOMiddleware_NoRoleResolver_Refuses503(t *testing.T) {
	_, soRoleID := seededSORoles(t)
	rec, ran := serveSOGate(t, nil, soRoleID)
	if rec.Code != http.StatusServiceUnavailable || ran {
		t.Fatalf("status=%d ran=%v, want 503 and handler not run", rec.Code, ran)
	}
}

// TestSOMiddleware_RoleStoreError_Refuses503: an unanswerable role store is a
// node-side fault, never an admission.
func TestSOMiddleware_RoleStoreError_Refuses503(t *testing.T) {
	_, soRoleID := seededSORoles(t)
	rec, ran := serveSOGate(t, failingRoleResolver{}, soRoleID)
	if rec.Code != http.StatusServiceUnavailable || ran {
		t.Fatalf("status=%d ran=%v, want 503 and handler not run", rec.Code, ran)
	}
}
