// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const embeddedDelegationPrincipal = "server-deleg-test"

func init() {
	permission.RegisterRoleGrant(embeddedDelegationPrincipal, permission.KVGet)
}

// TestAuthenticateDelegation_ServerBindsTheDelegatedCaller proves the server
// hands an in-process delegation to its authentication chain's delegation
// check and returns the context that check bound.
func TestAuthenticateDelegation_ServerBindsTheDelegatedCaller(t *testing.T) {
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: embeddedDelegationPrincipal, Scope: []string{string(permission.KVGet), string(permission.KVDelete)}})
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t, "acme")), authmw.WithDelegationVerifier(d.Authenticator))

	ctx, err := serverWith(WithAuthMiddleware(mw)).AuthenticateDelegation(context.Background(), []*x509.Certificate{d.Certificate})
	if err != nil {
		t.Fatalf("AuthenticateDelegation: %v", err)
	}
	id := auth.IdentityFromContext(ctx)
	if _, ok := auth.DelegationOf(id); !ok || id.TenantID != "acme" || tenant.FromContext(ctx) != "acme" {
		t.Fatalf("context binds %+v in tenant %q; want the delegated principal in acme", id, tenant.FromContext(ctx))
	}
	if len(id.ScopePermissions) != 1 || id.ScopePermissions[0] != string(permission.KVGet) {
		t.Fatalf("scope = %v, want the delegation's scope cut to the principal's grants", id.ScopePermissions)
	}
}

// TestAuthenticateDelegation_ServerRefuses proves a server with no
// authentication chain, and a chain refusing the delegation, each return
// their typed error and no context.
func TestAuthenticateDelegation_ServerRefuses(t *testing.T) {
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: embeddedDelegationPrincipal, Scope: []string{string(permission.KVGet)}})
	foreign := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t, "acme")),
		authmw.WithDelegationVerifier(d.NewAuthenticator(auth.WithDelegatedPrincipal("someone-else"))))

	for name, tc := range map[string]struct {
		srv   *Server
		check func(error) bool
	}{
		"no authentication chain": {srv: serverWith(),
			check: func(err error) bool { var e *CallerAuthUnavailableError; return errors.As(err, &e) }},
		"a foreign principal": {srv: serverWith(WithAuthMiddleware(foreign)),
			check: func(err error) bool { return errors.Is(err, auth.ErrForeignDelegationPrincipal) }},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, err := tc.srv.AuthenticateDelegation(context.Background(), []*x509.Certificate{d.Certificate})
			if ctx != nil || !tc.check(err) {
				t.Fatalf("AuthenticateDelegation = %v, %v; want the typed refusal", ctx, err)
			}
		})
	}
}
