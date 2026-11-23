package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

const peerCredTestTenant = "acme"

// peerCredMiddleware is a middleware whose only authenticator maps uid 1000
// to the account pc-user of peerCredTestTenant, over a store answering u.
func peerCredMiddleware(users user.UserServicer) *AuthMiddleware {
	uid := uint32(1000)
	authn := auth.NewPeerCredentialAuthenticator([]auth.PeerCredentialMapping{{UID: &uid, TenantID: peerCredTestTenant, Username: "pc-user"}})
	return newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(authn), WithUserService(users))
}

func peerCredContext() context.Context {
	return auth.WithPeerCredential(context.Background(), auth.PeerCredential{UID: 1000, GID: 1000, PID: 42})
}

func peerCredUser() *user.User {
	return &user.User{ID: 11, Username: "pc-user", TenantID: peerCredTestTenant, RoleIDs: []string{"role-reader"}, Lifecycle: user.LifecycleActive}
}

// TestAuthenticate_PeerCredentialRolesFromBoundUser: the identity's roles are
// the bound store account's RoleIDs and nothing else.
func TestAuthenticate_PeerCredentialRolesFromBoundUser(t *testing.T) {
	users := &mockUserServicer{user: peerCredUser()}
	_, id, err := peerCredMiddleware(users).Authenticate(peerCredContext())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if users.lastUser != "pc-user" {
		t.Fatalf("store looked up %q, want the mapped account pc-user", users.lastUser)
	}
	if len(id.Roles) != 1 || id.Roles[0] != "role-reader" {
		t.Fatalf("Roles = %v, want exactly the bound account's [role-reader]", id.Roles)
	}
	if id.UserID != 11 || id.TenantID != peerCredTestTenant {
		t.Fatalf("identity bound to user %d in %q, want 11 in %q", id.UserID, id.TenantID, peerCredTestTenant)
	}
}

// peerCredMiddlewareWith is peerCredMiddleware over the given authorizer, and
// with no user store at all when users is nil.
func peerCredMiddlewareWith(authz *mockAuthorizer, users user.UserServicer) *AuthMiddleware {
	uid := uint32(1000)
	authn := auth.NewPeerCredentialAuthenticator([]auth.PeerCredentialMapping{{UID: &uid, TenantID: peerCredTestTenant, Username: "pc-user"}})
	opts := []Option{WithAuthenticator(authn)}
	if users != nil {
		opts = append(opts, WithUserService(users))
	}
	return newTestMiddleware(authz, opts...)
}

// requireAccountMissing fails t unless err is the peer-credential
// missing-account refusal, no identity was returned, and the authorizer was
// never consulted.
func requireAccountMissing(t *testing.T, id *auth.Identity, err error, authz *mockAuthorizer) {
	t.Helper()
	if !errors.Is(err, auth.ErrPeerCredentialAccountMissing) {
		t.Fatalf("Authenticate err = %v, want ErrPeerCredentialAccountMissing", err)
	}
	if id != nil {
		t.Fatalf("Authenticate returned identity %+v with the refusal, want nil", id)
	}
	if n := authz.calls.Load(); n != 0 {
		t.Fatalf("authorizer consulted %d times for a refused peer credential, want 0", n)
	}
}

// TestAuthenticate_PeerCredentialMissingAccountRefused: a mapping naming an
// account the store does not hold is refused at authentication, so no policy
// (an ABAC subject.authenticated allow included) can admit it.
func TestAuthenticate_PeerCredentialMissingAccountRefused(t *testing.T) {
	authz := &mockAuthorizer{}
	users := &mockUserServicer{err: &user.UserNotFoundError{Lookup: "pc-user"}}
	_, id, err := peerCredMiddlewareWith(authz, users).Authenticate(peerCredContext())
	requireAccountMissing(t, id, err, authz)
}

// requireSealedRefusal asserts a peer credential whose tenant IAM is sealed
// was refused with the sealed answer (a retryable *StoreUnavailableError
// wrapping tenant.ErrTenantSealed), with no identity and no authorizer call.
func requireSealedRefusal(t *testing.T, id *auth.Identity, err error, authz *mockAuthorizer) {
	t.Helper()
	var unavailable *StoreUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Authenticate err = %v, want *StoreUnavailableError", err)
	}
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Authenticate err = %v, want it to wrap tenant.ErrTenantSealed", err)
	}
	if id != nil {
		t.Fatalf("Authenticate returned identity %+v with the refusal, want nil", id)
	}
	if n := authz.calls.Load(); n != 0 {
		t.Fatalf("authorizer consulted %d times for a refused peer credential, want 0", n)
	}
}

// TestAuthenticate_PeerCredentialSealedTenantRefused pins that a peer
// credential is never admitted unresolved while its tenant's IAM is sealed:
// the mapped account cannot be verified (it may be deleted, disabled or
// mTLS-only), so the request gets the sealed answer instead of an identity.
func TestAuthenticate_PeerCredentialSealedTenantRefused(t *testing.T) {
	authz := &mockAuthorizer{}
	users := &mockUserServicer{err: tenant.ErrTenantSealed}
	_, id, err := peerCredMiddlewareWith(authz, users).Authenticate(peerCredContext())
	requireSealedRefusal(t, id, err, authz)
}

// TestAuthenticate_PeerCredentialSealedTenantNotSealedWindowAdmitted pins
// that the refusal closes the sealed-window fallback too: the context the
// refused authentication leaves carries no sealed-marked identity, so
// AuthorizeTenantBarrierSealedOp does not admit barrier status or unseal.
func TestAuthenticate_PeerCredentialSealedTenantNotSealedWindowAdmitted(t *testing.T) {
	authz := &mockAuthorizer{}
	m := peerCredMiddlewareWith(authz, &mockUserServicer{err: tenant.ErrTenantSealed})
	ctx, id, err := m.Authenticate(peerCredContext())
	requireSealedRefusal(t, id, err, authz)
	for _, perm := range []permission.Permission{permission.TenantBarrierStatus, permission.TenantBarrierUnseal} {
		if aerr := m.AuthorizeTenantBarrierSealedOp(ctx, peerCredTestTenant, perm); aerr == nil {
			t.Fatalf("sealed-window %s admitted a refused peer credential", perm)
		}
	}
}

// TestAuthenticate_PeerCredentialNoUserStoreRefused: with no user store the
// mapped account cannot be resolved, so the peer credential is refused.
func TestAuthenticate_PeerCredentialNoUserStoreRefused(t *testing.T) {
	authz := &mockAuthorizer{}
	_, id, err := peerCredMiddlewareWith(authz, nil).Authenticate(peerCredContext())
	requireAccountMissing(t, id, err, authz)
}

// TestAuthenticate_PeerCredentialDeletedAccountRefused: the same middleware
// admits the mapped account, and once the account is deleted the next
// request by the same peer credential is refused.
func TestAuthenticate_PeerCredentialDeletedAccountRefused(t *testing.T) {
	authz := &mockAuthorizer{}
	users := &mockUserServicer{user: peerCredUser()}
	m := peerCredMiddlewareWith(authz, users)
	if _, id, err := m.Authenticate(peerCredContext()); err != nil || id == nil {
		t.Fatalf("Authenticate before the delete: identity %v err %v, want the bound account", id, err)
	}
	users.user = nil
	users.err = &user.UserNotFoundError{Lookup: "pc-user"}
	_, id, err := m.Authenticate(peerCredContext())
	requireAccountMissing(t, id, err, authz)
}

// TestAuthenticate_PeerCredentialRefusedByAuthMethodsPolicy: an account that
// permits only mtls refuses the peercred method.
func TestAuthenticate_PeerCredentialRefusedByAuthMethodsPolicy(t *testing.T) {
	u := peerCredUser()
	u.AuthMethods = []string{auth.MethodMTLS}
	if _, _, err := peerCredMiddleware(&mockUserServicer{user: u}).Authenticate(peerCredContext()); err == nil {
		t.Fatal("Authenticate admitted method peercred for an account permitting only mtls")
	}

	u.AuthMethods = []string{auth.MethodPeerCred}
	if _, _, err := peerCredMiddleware(&mockUserServicer{user: u}).Authenticate(peerCredContext()); err != nil {
		t.Fatalf("Authenticate refused an account permitting peercred: %v", err)
	}
}

// TestAuthenticate_PeerCredentialDisabledUserRefused: a disabled bound
// account is refused.
func TestAuthenticate_PeerCredentialDisabledUserRefused(t *testing.T) {
	u := peerCredUser()
	u.Lifecycle = user.LifecycleDisabled
	if _, _, err := peerCredMiddleware(&mockUserServicer{user: u}).Authenticate(peerCredContext()); err == nil {
		t.Fatal("Authenticate admitted a disabled account by its peer credential")
	}
}
