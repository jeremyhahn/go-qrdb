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

// No build constraint: newTestMiddleware is the constructor every test file
// in this package uses, whatever its own constraint.

package middleware

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// noAdminRoles is a role store in which no tenant has an Admin role, so every
// identity resolves as not its tenant's owner. It is the role store
// newTestMiddleware wires for tests whose subject is not TenantOwner.
type noAdminRoles struct{}

var _ role.NameResolver = noAdminRoles{}

func (noAdminRoles) GetRoleByName(_ context.Context, _, name string) (*role.Role, error) {
	return nil, &role.RoleNotFoundError{ID: name}
}

// failingRoles is a role store whose every read fails with err.
type failingRoles struct{ err error }

var _ role.NameResolver = failingRoles{}

func (f failingRoles) GetRoleByName(context.Context, string, string) (*role.Role, error) {
	return nil, f.err
}

// newTestMiddleware is New with noAdminRoles as the role store, which New
// requires for every authenticated request. The caller's options come after
// it, so a test that wires its own with WithRoleResolver replaces it.
func newTestMiddleware(authorizer auth.Authorizer, opts ...Option) *AuthMiddleware {
	return New(authorizer, append([]Option{WithRoleResolver(noAdminRoles{})}, opts...)...)
}

// ownerFixture is a middleware over the real role store, with acme's and the
// system tenant's built-in roles seeded, authenticating "carol" of tenantID
// against a user record the test edits between requests.
type ownerFixture struct {
	m      *AuthMiddleware
	authn  *mockAuthenticator
	record *user.User
}

func newOwnerFixture(t *testing.T, tenantID string, roleIDs []string, opts ...Option) *ownerFixture {
	t.Helper()
	record := &user.User{ID: 11, Username: "carol", TenantID: tenantID, RoleIDs: roleIDs, Lifecycle: user.LifecycleActive}
	authn := &mockAuthenticator{name: "test-authn"}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	base := []Option{
		WithAuthenticator(authn),
		WithUserService(&mockUserServicer{user: record}),
		WithRoleResolver(roles),
	}
	return &ownerFixture{m: newTestMiddleware(&mockAuthorizer{}, append(base, opts...)...), authn: authn, record: record}
}

// request authenticates one request as a freshly built identity, the way an
// authenticator builds one per request, and returns its TenantOwner.
func (f *ownerFixture) request(t *testing.T) bool {
	t.Helper()
	f.authn.identity = &auth.Identity{Subject: f.record.Username, TenantID: f.record.TenantID}
	_, id, err := f.m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return id.TenantOwner
}

func TestTenantOwner_TenantAdminIsOwner(t *testing.T) {
	f := newOwnerFixture(t, "acme", []string{role.SystemRoleID("acme", role.RoleNameAdmin)})
	if !f.request(t) {
		t.Fatal("acme's Admin must authenticate with TenantOwner")
	}
}

func TestTenantOwner_NonAdminIsNotOwner(t *testing.T) {
	f := newOwnerFixture(t, "acme", []string{role.SystemRoleID("acme", role.RoleNameOperator)})
	if f.request(t) {
		t.Fatal("an Operator must authenticate without TenantOwner")
	}
}

// The system tenant's Admin role, held by an account of another tenant, makes
// it owner of nothing there.
func TestTenantOwner_ForeignAdminRoleConfersNothing(t *testing.T) {
	f := newOwnerFixture(t, "acme", []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)})
	if f.request(t) {
		t.Fatal("a system Admin role id on an acme account must not make it acme's owner")
	}
}

// The decision is read per request, uncached: withdrawing Admin shows on the
// caller's very next request and granting it back on the one after.
func TestTenantOwner_RoleChangeTakesEffectOnTheNextRequest(t *testing.T) {
	admin := role.SystemRoleID("acme", role.RoleNameAdmin)
	operator := role.SystemRoleID("acme", role.RoleNameOperator)
	f := newOwnerFixture(t, "acme", []string{operator, admin})

	if !f.request(t) {
		t.Fatal("request as Admin: want TenantOwner")
	}
	f.record.RoleIDs = []string{operator}
	if f.request(t) {
		t.Fatal("the request after Admin was withdrawn: want no TenantOwner")
	}
	f.record.RoleIDs = []string{admin}
	if !f.request(t) {
		t.Fatal("the request after Admin was granted back: want TenantOwner")
	}
}

// An authenticator's TenantOwner is never trusted: the role store is the one
// source, so a claimed ownership on a non-Admin account is cleared.
func TestTenantOwner_AuthenticatorClaimIsCleared(t *testing.T) {
	f := newOwnerFixture(t, "acme", []string{role.SystemRoleID("acme", role.RoleNameOperator)})
	f.authn.identity = &auth.Identity{Subject: "carol", TenantID: "acme", TenantOwner: true}
	_, id, err := f.m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if id.TenantOwner {
		t.Fatal("an authenticator-supplied TenantOwner must not survive the auth chain")
	}
}

// A role store that cannot answer refuses the request; it never admits the
// caller as "not owner".
func TestTenantOwner_RoleStoreErrorRefuses(t *testing.T) {
	cause := errors.New("read index not ready")
	f := newOwnerFixture(t, "acme", []string{role.SystemRoleID("acme", role.RoleNameAdmin)},
		WithRoleResolver(failingRoles{err: cause}))
	f.authn.identity = &auth.Identity{Subject: "carol", TenantID: "acme"}
	_, id, err := f.m.authenticate(context.Background())
	var unresolved *TenantOwnerUnresolvedError
	if !errors.As(err, &unresolved) || !errors.Is(err, cause) {
		t.Fatalf("want *TenantOwnerUnresolvedError wrapping the store error, got %v", err)
	}
	if id != nil {
		t.Fatalf("a refused request must carry no identity, got %+v", id)
	}
	if unresolved.TenantID != "acme" {
		t.Fatalf("TenantID = %q, want acme", unresolved.TenantID)
	}
	if got := ClassifyAuthError(err); got != AuthFailureUnavailable {
		t.Fatalf("class = %v, want AuthFailureUnavailable (retryable)", got)
	}
	if got := mapHTTPAuthError(err); got != http.StatusServiceUnavailable {
		t.Fatalf("HTTP status = %d, want 503", got)
	}
}

// A middleware built without a role store refuses every authenticated request,
// a role-less one included, rather than default TenantOwner to false.
func TestTenantOwner_NoRoleStoreRefuses(t *testing.T) {
	for name, roles := range map[string][]string{"no roles": nil, "admin": {role.SystemRoleID("acme", role.RoleNameAdmin)}} {
		t.Run(name, func(t *testing.T) {
			m := New(&mockAuthorizer{},
				WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: &auth.Identity{Subject: "carol", TenantID: "acme"}}),
				WithUserService(&mockUserServicer{user: &user.User{ID: 11, Username: "carol", TenantID: "acme", RoleIDs: roles, Lifecycle: user.LifecycleActive}}),
			)
			_, _, err := m.authenticate(context.Background())
			var unresolved *TenantOwnerUnresolvedError
			var required *role.ResolverRequiredError
			if !errors.As(err, &unresolved) || !errors.As(err, &required) {
				t.Fatalf("want *TenantOwnerUnresolvedError wrapping *role.ResolverRequiredError, got %v", err)
			}
		})
	}
}

// A holder certificate and a sealed tenant's role-less identity take their
// own early returns; both are still decided, not left at false by default.
func TestTenantOwner_EarlyReturnPathsAreDecided(t *testing.T) {
	cause := errors.New("store down")
	t.Run("sealed tenant", func(t *testing.T) {
		// A role on the authenticated identity makes the decision a store
		// read; a role-less one is "not owner" without one.
		claimed := &auth.Identity{Subject: "carol", TenantID: "acme", Roles: []string{role.SystemRoleID("acme", role.RoleNameAdmin)}}
		m := newTestMiddleware(&mockAuthorizer{},
			WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: claimed}),
			WithUserService(&mockUserServicer{err: tenant.ErrTenantSealed}),
			WithRoleResolver(failingRoles{err: cause}),
		)
		if _, _, err := m.authenticate(context.Background()); !errors.Is(err, cause) {
			t.Fatalf("want the role store error, got %v", err)
		}
	})
	t.Run("no role store", func(t *testing.T) {
		m := New(&mockAuthorizer{},
			WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: &auth.Identity{Subject: "carol", TenantID: "acme"}}),
			WithUserService(&mockUserServicer{err: tenant.ErrTenantSealed}),
		)
		var required *role.ResolverRequiredError
		if _, _, err := m.authenticate(context.Background()); !errors.As(err, &required) {
			t.Fatalf("want *role.ResolverRequiredError, got %v", err)
		}
	})
}

// For the system tenant a failed linearizable read is retried against the
// StaleRead fallback, as the user lookup and the authorizer do, so a draining
// node still admits its operator.
func TestTenantOwner_SystemTenantFallsBackToStaleRead(t *testing.T) {
	f := newOwnerFixture(t, tenantid.System, []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)},
		WithRoleResolver(failingRoles{err: errors.New("host draining")}),
		WithRoleResolverFallback(authtest.BuiltInRoles(t, tenantid.System)),
	)
	if !f.request(t) {
		t.Fatal("system Admin via the fallback: want TenantOwner")
	}
}

// The fallback holds the system tenant only, so any other tenant's failed
// read stands and refuses the request.
func TestTenantOwner_FallbackNeverAnswersForAnotherTenant(t *testing.T) {
	cause := errors.New("host draining")
	f := newOwnerFixture(t, "acme", []string{role.SystemRoleID("acme", role.RoleNameAdmin)},
		WithRoleResolver(failingRoles{err: cause}),
		WithRoleResolverFallback(authtest.BuiltInRoles(t, tenantid.System, "acme")),
	)
	f.authn.identity = &auth.Identity{Subject: "carol", TenantID: "acme"}
	if _, _, err := f.m.authenticate(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("want the primary's error, got %v", err)
	}
}

func TestTenantOwner_FallbackFailureReportsThePrimaryError(t *testing.T) {
	cause := errors.New("read index not ready")
	f := newOwnerFixture(t, tenantid.System, []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)},
		WithRoleResolver(failingRoles{err: cause}),
		WithRoleResolverFallback(failingRoles{err: errors.New("fallback down")}),
	)
	f.authn.identity = &auth.Identity{Subject: "carol", TenantID: tenantid.System}
	if _, _, err := f.m.authenticate(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("want the primary's error, got %v", err)
	}
}

// A sealed tenant is authoritative: the fallback is not consulted.
func TestTenantOwner_SealedTenantIsNotRetried(t *testing.T) {
	f := newOwnerFixture(t, tenantid.System, []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)},
		WithRoleResolver(failingRoles{err: tenant.ErrTenantSealed}),
		WithRoleResolverFallback(authtest.BuiltInRoles(t, tenantid.System)),
	)
	f.authn.identity = &auth.Identity{Subject: "carol", TenantID: tenantid.System}
	if _, _, err := f.m.authenticate(context.Background()); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("want ErrTenantSealed, got %v", err)
	}
}

// On-behalf forwarding re-decides TenantOwner for the asserted caller on the
// serving node, from the serving node's role store, on every request.
func TestTenantOwner_OnBehalfDecidedOnTheServingNode(t *testing.T) {
	admin := role.SystemRoleID("acme", role.RoleNameAdmin)
	operator := role.SystemRoleID("acme", role.RoleNameOperator)
	storeUser := &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: []string{admin}}
	m := newTestMiddleware(&capturingAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "test-authn", err: auth.ErrNoPeerCertificate}),
		WithUserService(&mockUserServicer{user: storeUser}),
		WithRoleResolver(authtest.BuiltInRoles(t, "acme")),
	)
	nodeCert, nodeKey := obNodeCert(t, obNodeID)

	forward := func() *auth.Identity {
		t.Helper()
		var seen *auth.Identity
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = auth.IdentityFromContext(r.Context())
			w.WriteHeader(http.StatusNoContent)
		})
		r := buildOnBehalfRequest(t, nodeCert, nodeKey, obAliceSubject, "acme", obNodeID, time.Now(), []byte("x"), http.MethodPut, "/kv/foo")
		m.RESTMiddleware(testRESTRoutes(), next).ServeHTTP(httptest.NewRecorder(), r)
		if seen == nil {
			t.Fatal("the forwarded request did not reach the handler")
		}
		return seen
	}

	if !forward().TenantOwner {
		t.Fatal("forwarded request for acme's Admin: want TenantOwner")
	}
	storeUser.RoleIDs = []string{operator}
	if forward().TenantOwner {
		t.Fatal("the forwarded request after Admin was withdrawn: want no TenantOwner")
	}
}

// ownerClaimingVerifier verifies a delegation through v, then claims
// TenantOwner on the identity it returns: the claim the auth chain must not
// carry, whichever verifier makes it.
type ownerClaimingVerifier struct{ v DelegationVerifier }

func (o ownerClaimingVerifier) VerifyDelegation(ctx context.Context, der []byte) (*auth.Identity, error) {
	id, err := o.v.VerifyDelegation(ctx, der)
	if id != nil {
		id.TenantOwner = true
	}
	return id, err
}

func TestTenantOwner_DelegatedClaimIsCleared(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	m := dlgLeader(t, ownerClaimingVerifier{v: d.Authenticator})
	_, id, err := m.AuthenticateDelegation(context.Background(), []*x509.Certificate{d.Certificate})
	if err != nil {
		t.Fatalf("AuthenticateDelegation: %v", err)
	}
	if id.TenantOwner {
		t.Fatal("a delegated identity carried the verifier's TenantOwner claim; it holds no role and owns nothing")
	}
}

func TestTenantOwner_DelegatedWithoutRoleStoreRefuses(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	m := New(&mockAuthorizer{}, WithDelegationVerifier(d.Authenticator))
	_, _, err := m.AuthenticateDelegation(context.Background(), []*x509.Certificate{d.Certificate})
	var unresolved *TenantOwnerUnresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("want *TenantOwnerUnresolvedError, got %v", err)
	}
}
