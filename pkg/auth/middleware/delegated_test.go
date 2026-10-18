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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// dlgPrincipal is the service principal these tests delegate to. The host
// grants it kv:get and kv:put; the delegation's scope is kv:get and
// kv:delete, so the request holds exactly kv:get.
const dlgPrincipal = "deleg-mw-test"

func init() {
	permission.RegisterRoleGrant(dlgPrincipal, permission.KVGet, permission.KVPut)
}

// dlgIssuer is an auth.IssuerVerifier plus auth.TenantIssuerVerifier that
// binds "acme" to one CA. The chain itself arrives verified in the TLS
// state, so VerifyPeerChain is never reached.
type dlgIssuer struct{ acme *x509.Certificate }

func (d *dlgIssuer) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	return nil, errors.New("not reached: the chain arrives verified")
}
func (d *dlgIssuer) IsClusterCA(context.Context, *x509.Certificate) bool { return false }
func (d *dlgIssuer) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return errors.New("not reached: a delegation is bound by VerifyTenantIssuer")
}
func (d *dlgIssuer) VerifyTenantIssuer(_ context.Context, tenantID string, issuer *x509.Certificate) error {
	if tenantID != "acme" || issuer == nil || !issuer.Equal(d.acme) {
		return errors.New("not the tenant's login CA")
	}
	return nil
}

var (
	_ auth.IssuerVerifier       = (*dlgIssuer)(nil)
	_ auth.TenantIssuerVerifier = (*dlgIssuer)(nil)
)

// dlgUnrevoked answers that no certificate is revoked.
type dlgUnrevoked struct{}

func (dlgUnrevoked) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) {
	return false, nil
}

// dlgPresented returns the context a transport hands the chain for a
// connection that presented an acme delegation to dlgPrincipal, plus the
// authenticator that verifies it.
func dlgPresented(t *testing.T) (context.Context, *auth.MTLSAuthenticator) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "acme-login-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	ext, err := delegation.Grant{Tenant: "acme", Principal: dlgPrincipal, Scope: []string{string(permission.KVGet), string(permission.KVDelete)}}.Extension()
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0xabc), Subject: pkix.Name{CommonName: "delegation", OrganizationalUnit: []string{"acme"}},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(delegation.DefaultLifetime),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, ExtraExtensions: []pkix.Extension{ext},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
	a := auth.NewMTLSAuthenticator(auth.WithIssuerVerifier(&dlgIssuer{acme: ca}),
		auth.WithRevocationChecker(dlgUnrevoked{}), auth.WithDelegatedPrincipal(dlgPrincipal))
	return auth.WithTLSState(context.Background(), state), a
}

// dlgMiddleware is the real RBAC chain with the delegation's authenticator
// first and a user store that would resolve any subject to an account
// holding the tenant admin role -- so a request's permissions provably come
// from the delegation, never from an account.
func dlgMiddleware(t *testing.T, a auth.Authenticator, opts ...Option) *AuthMiddleware {
	t.Helper()
	stored := &user.User{Username: auth.ServicePrincipalSubjectPrefix + dlgPrincipal, TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: []string{"admin"}}
	roles := authtest.BuiltInRoles(t, "acme", "globex", tenantid.System)
	base := []Option{WithAuthenticator(a), WithUserService(&mockUserServicer{user: stored}), WithRoleResolver(roles)}
	return New(rbac.New(roles), append(base, opts...)...)
}

func TestDelegated_AdmittedAsTheServicePrincipalInTheTenant(t *testing.T) {
	ctx, a := dlgPresented(t)
	ctx, id, err := dlgMiddleware(t, a, WithAuditLogger(&mockAuditLogger{})).Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != auth.ServicePrincipalSubjectPrefix+dlgPrincipal || id.TenantID != "acme" {
		t.Fatalf("identity = %s in %q, want the service principal in acme", id.Subject, id.TenantID)
	}
	if len(id.Roles) != 0 || len(id.RoleBindings) != 0 || id.Attributes != nil {
		t.Fatalf("identity carries roles %v, bindings %v, attributes %v; a delegation confers its scope only", id.Roles, id.RoleBindings, id.Attributes)
	}
	if !slices.Equal(id.ScopePermissions, []string{string(permission.KVGet)}) {
		t.Fatalf("ScopePermissions = %v, want exactly the scope the principal itself holds", id.ScopePermissions)
	}
	if bound := auth.IdentityFromContext(ctx); bound != id {
		t.Fatal("the delegated identity is not the one bound to the request")
	}
}

// TestDelegated_AuthorityIsScopeIntersectedWithTheGrant: inside both, the
// request is allowed; in the scope but not granted, or granted but outside
// the scope, it is denied.
func TestDelegated_AuthorityIsScopeIntersectedWithTheGrant(t *testing.T) {
	ctx, a := dlgPresented(t)
	m := dlgMiddleware(t, a, WithAuditLogger(&mockAuditLogger{}))
	ctx, id, err := m.Authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.authorize(ctx, id, permission.KVGet); err != nil {
		t.Fatalf("kv:get (scoped and granted): %v", err)
	}
	for _, p := range []permission.Permission{permission.KVDelete, permission.KVPut, permission.KVList} {
		if err := m.authorize(ctx, id, p); err == nil {
			t.Errorf("%s: allowed, want denied outside scope ∩ grant", p)
		}
	}
}

func TestDelegated_IdentityGatedIsRefused(t *testing.T) {
	ctx, a := dlgPresented(t)
	m := dlgMiddleware(t, a, WithAuditLogger(&mockAuditLogger{}))
	ctx, id, err := m.Authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.authorize(ctx, id, permission.IdentityGated); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("IdentityGated = %v, want auth.ErrUnauthorized", err)
	}
}

// TestDelegated_EveryUseIsAuditedToTheTenant: allowed and denied alike, the
// record is keyed by the delegating tenant and names the delegation.
func TestDelegated_EveryUseIsAuditedToTheTenant(t *testing.T) {
	ctx, a := dlgPresented(t)
	sink := &mockAuditLogger{}
	m := dlgMiddleware(t, a, WithAuditLogger(sink))
	ctx, id, err := m.Authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.authorize(ctx, id, permission.KVGet)
	_ = m.authorize(ctx, id, permission.KVDelete)
	if len(sink.entries) != 2 {
		t.Fatalf("%d audit records, want one per decision", len(sink.entries))
	}
	for i, want := range []string{audit.ResultSuccess, audit.ResultDenied} {
		e := sink.entries[i]
		if e.TenantID != "acme" || e.Subject != id.Subject || e.Result != want {
			t.Errorf("record %d = %s/%s/%s, want acme/%s/%s", i, e.TenantID, e.Subject, e.Result, id.Subject, want)
		}
		if e.Metadata[delegationPrincipalAuditKey] != dlgPrincipal || e.Metadata[delegationSerialAuditKey] != "abc" ||
			e.Metadata[delegationScopeAuditKey] != "kv:get kv:delete" {
			t.Errorf("record %d metadata = %v, want the delegation's principal, serial and scope", i, e.Metadata)
		}
	}
}

// TestDelegated_NeverServedUnaudited: no logger, or a logger that fails
// under a fail-open policy, refuses a delegated request.
func TestDelegated_NeverServedUnaudited(t *testing.T) {
	for name, opts := range map[string][]Option{
		"no logger":               nil,
		"fail-open write failure": {WithAuditLogger(&mockAuditLogger{err: errors.New("disk full")}), WithAuditFailurePolicy(audit.FailOpen)},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, a := dlgPresented(t)
			m := dlgMiddleware(t, a, opts...)
			ctx, id, err := m.Authenticate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var gate *audit.GateError
			if err := m.authorize(ctx, id, permission.KVGet); !errors.As(err, &gate) {
				t.Fatalf("authorize = %v, want *audit.GateError", err)
			}
		})
	}
}

// TestDelegated_OrdinaryIdentityKeepsTheOperatorsPolicy is the control: an
// identity without a delegation is served under fail-open, and with no
// logger at all.
func TestDelegated_OrdinaryIdentityKeepsTheOperatorsPolicy(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "acme"}
	for name, m := range map[string]*AuthMiddleware{
		"no logger": New(&mockAuthorizer{}),
		"fail-open": New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{err: errors.New("disk full")}), WithAuditFailurePolicy(audit.FailOpen)),
	} {
		if err := m.authorize(context.Background(), id, permission.KVGet); err != nil {
			t.Errorf("%s: authorize = %v, want allowed", name, err)
		}
	}
}

func TestDelegationUnauditedError_Message(t *testing.T) {
	want := "auth/middleware: no audit logger is wired, and a delegated request is never served unaudited"
	if got := (&DelegationUnauditedError{}).Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestDelegatedPermissions_KeepsOnlyGrantedScope(t *testing.T) {
	got := delegatedPermissions(auth.Delegation{Principal: dlgPrincipal, Scope: []string{"kv:put", "kv:delete", "kv:get"}})
	if !slices.Equal(got, []string{"kv:put", "kv:get"}) {
		t.Fatalf("delegatedPermissions = %v, want [kv:put kv:get]", got)
	}
	if got := delegatedPermissions(auth.Delegation{Principal: "nobody-granted", Scope: []string{"kv:get"}}); len(got) != 0 {
		t.Fatalf("an ungranted principal holds %v, want nothing", got)
	}
}

func TestDelegationAuditMetadata_OrdinaryIdentityHasNone(t *testing.T) {
	for _, id := range []*auth.Identity{nil, {Subject: "alice"}, auth.NewServicePrincipal(dlgPrincipal, tenantid.System, []string{"kv:get"})} {
		if meta, ok := delegationAuditMetadata(id); ok || meta != nil {
			t.Fatalf("delegationAuditMetadata(%+v) = (%v, %v), want none", id, meta, ok)
		}
	}
}
