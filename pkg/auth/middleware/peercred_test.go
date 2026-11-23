package middleware

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
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

// TestAuthenticate_PeerCredentialMissingAccountDenied: a mapping naming an
// account the store does not hold proceeds with no roles, so RBAC denies.
func TestAuthenticate_PeerCredentialMissingAccountDenied(t *testing.T) {
	users := &mockUserServicer{err: &user.UserNotFoundError{Lookup: "pc-user"}}
	_, id, err := peerCredMiddleware(users).Authenticate(peerCredContext())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if len(id.Roles) != 0 || len(id.RoleBindings) != 0 || len(id.ScopePermissions) != 0 {
		t.Fatalf("identity of a missing account carries roles %v bindings %v scopes %v, want none (default deny)", id.Roles, id.RoleBindings, id.ScopePermissions)
	}
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
