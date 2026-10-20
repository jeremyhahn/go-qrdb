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

package middleware

import (
	"context"
	"crypto/x509"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestAuthenticateDelegation_BindsTheDelegatedCaller proves an in-process
// caller presenting a tenant-issued delegation is bound, in the returned
// context, to the service principal inside the delegating tenant, holding
// the delegation's scope intersected with the principal's own grants and
// nothing else.
func TestAuthenticateDelegation_BindsTheDelegatedCaller(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	m := dlgLeader(t, d.Authenticator)

	for name, chain := range map[string][]*x509.Certificate{
		"leaf alone":           {d.Certificate},
		"leaf with its issuer": {d.Certificate, d.CA},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, id, err := m.AuthenticateDelegation(context.Background(), chain)
			if err != nil {
				t.Fatalf("AuthenticateDelegation: %v", err)
			}
			if auth.IdentityFromContext(ctx) != id || tenant.FromContext(ctx) != "acme" {
				t.Fatalf("context binds %+v in tenant %q, want the returned identity in acme", auth.IdentityFromContext(ctx), tenant.FromContext(ctx))
			}
			got, ok := auth.DelegationOf(id)
			if !ok || got.Principal != dlgPrincipal || id.Subject != auth.ServicePrincipalSubjectPrefix+dlgPrincipal || id.TenantID != "acme" {
				t.Fatalf("identity = %+v, want the delegated principal in acme", id)
			}
			if len(id.Roles) != 0 || !slices.Equal(id.ScopePermissions, []string{string(permission.KVGet)}) {
				t.Fatalf("roles %v, scope %v; want no roles and exactly kv:get", id.Roles, id.ScopePermissions)
			}
			if err := m.authorize(ctx, id, permission.KVGet); err != nil {
				t.Fatalf("kv:get: %v", err)
			}
			if err := m.authorize(ctx, id, permission.KVDelete); err == nil {
				t.Fatal("kv:delete allowed; the principal holds no grant for it")
			}
		})
	}
}

// TestAuthenticateDelegation_Refused proves every certificate the delegation
// verifier refuses, and every call it cannot judge, is refused with its typed
// error and binds no identity.
func TestAuthenticateDelegation_Refused(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	other := authtest.NewDelegation(t, dlgGrant)

	for name, tc := range map[string]struct {
		verifier DelegationVerifier
		chain    []*x509.Certificate
		is       error
	}{
		"no delegation verifier":                {chain: []*x509.Certificate{d.Certificate}, is: auth.ErrForeignDelegationPrincipal},
		"empty chain":                           {verifier: d.Authenticator},
		"nil leaf":                              {verifier: d.Authenticator, chain: []*x509.Certificate{nil}},
		"signed by another tenant login CA":     {verifier: d.Authenticator, chain: []*x509.Certificate{other.Certificate}},
		"not a delegation":                      {verifier: d.Authenticator, chain: []*x509.Certificate{d.CA}},
		"names a principal the node won't take": {verifier: d.NewAuthenticator(auth.WithDelegatedPrincipal("someone-else")), chain: []*x509.Certificate{d.Certificate}, is: auth.ErrForeignDelegationPrincipal},
		"revoked":                               {verifier: d.NewAuthenticator(auth.WithRevocationChecker(dlgRevoked{})), chain: []*x509.Certificate{d.Certificate}, is: auth.ErrCertificateRevoked},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, id, err := dlgLeader(t, tc.verifier).AuthenticateDelegation(context.Background(), tc.chain)
			if id != nil || auth.IdentityFromContext(ctx) != nil {
				t.Fatalf("admitted as %+v; want refused", id)
			}
			if tc.is != nil {
				if !errors.Is(err, tc.is) {
					t.Fatalf("err = %v, want %v", err, tc.is)
				}
				return
			}
			var refused *auth.DelegationRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %T (%v), want *auth.DelegationRefusedError", err, err)
			}
		})
	}
}
