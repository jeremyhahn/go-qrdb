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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// dlgGrant is the acme delegation to dlgPrincipal these tests forward.
var dlgGrant = delegation.Grant{Tenant: "acme", Principal: dlgPrincipal, Scope: []string{string(permission.KVGet), string(permission.KVDelete)}}

// dlgPayload is the forwarded request body every assertion binds.
var dlgPayload = []byte("kv get payload")

// dlgBinding is the forwarded call every assertion binds.
func dlgBinding() forward.Binding { return forward.RPCBinding("quic", "kv.Get", dlgPayload) }

// dlgRevoked answers that every certificate is revoked.
type dlgRevoked struct{}

func (dlgRevoked) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) { return true, nil }

// dlgNode is a cluster node forwarding a call on behalf of a caller: its
// signing key, and the leader-side context carrying its verified TLS state.
type dlgNode struct {
	key *ecdsa.PrivateKey
	ctx context.Context
}

func newDLGNode(t *testing.T) *dlgNode {
	t.Helper()
	cert, key := obNodeCert(t, obNodeID)
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
	return &dlgNode{key: key, ctx: auth.WithTLSState(context.Background(), state)}
}

// assertion is the assertion the node builds when it forwards caller.
func (n *dlgNode) assertion(caller *auth.Identity) forward.CallerAssertion {
	a := dlgBinding().Assertion(caller.Subject, caller.TenantID, obNodeID, time.Now())
	a.Carry(caller)
	return a
}

// forward presents a, signed by the node, to the leader m.
func (n *dlgNode) forward(t *testing.T, m *AuthMiddleware, a forward.CallerAssertion) (context.Context, error) {
	t.Helper()
	ab, sb, err := forward.Sign(a, n.key)
	if err != nil {
		t.Fatal(err)
	}
	return m.AuthenticateQUICOnBehalf(n.ctx, "kv.Get", dlgPayload, ab, sb)
}

// dlgLeader is the leader's middleware: no authenticator of its own admits
// the node's connection, and delegations verify against v (none when nil).
func dlgLeader(t *testing.T, v DelegationVerifier) *AuthMiddleware {
	t.Helper()
	opts := []Option{WithAuditLogger(&mockAuditLogger{})}
	if v != nil {
		opts = append(opts, WithDelegationVerifier(v))
	}
	return dlgMiddleware(t, &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}, opts...)
}

// TestForwardedDelegation_LeaderRebuildsTheDelegatedCaller: the leader
// re-verifies the delegation the forwarder carried and serves the request
// as the service principal, scope-intersected -- never as the node, and
// never as the account of the same name the user store would resolve.
func TestForwardedDelegation_LeaderRebuildsTheDelegatedCaller(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	n := newDLGNode(t)
	m := dlgLeader(t, d.Authenticator)

	caller := d.Identity(t)
	ab, sb, err := forward.SignCaller(caller, dlgBinding(), n.key, obNodeID, time.Now())
	if err != nil {
		t.Fatalf("SignCaller: %v", err)
	}
	ctx, err := m.AuthenticateQUICOnBehalf(n.ctx, "kv.Get", dlgPayload, ab, sb)
	if err != nil {
		t.Fatalf("AuthenticateQUICOnBehalf: %v", err)
	}
	id := auth.IdentityFromContext(ctx)
	got, ok := auth.DelegationOf(id)
	if !ok || got.Principal != dlgPrincipal || id.Subject != caller.Subject || id.TenantID != "acme" {
		t.Fatalf("identity = %+v, want the delegated caller in acme", id)
	}
	if !isOnBehalfIdentity(id) || id.Metadata["forwarder_nodeid"] != obNodeID {
		t.Fatalf("metadata = %v, want on-behalf from %s", id.Metadata, obNodeID)
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
}

// TestForwardedDelegation_Refused: every way the forwarded delegation can
// fail to be the caller's own, still valid, tenant-issued delegation is
// refused, and none falls back to the node or to an account.
func TestForwardedDelegation_Refused(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	n := newDLGNode(t)
	caller := d.Identity(t)
	other := authtest.NewDelegation(t, dlgGrant).Identity(t)

	for name, tc := range map[string]struct {
		verifier DelegationVerifier
		mutate   func(a *forward.CallerAssertion)
		caller   *auth.Identity
	}{
		"no delegation verifier on the leader":                    {caller: caller},
		"signed by a CA the leader does not hold as the tenant's": {verifier: d.Authenticator, caller: other},
		"revoked":                          {verifier: d.NewAuthenticator(auth.WithRevocationChecker(dlgRevoked{})), caller: caller},
		"leader accepts another principal": {verifier: d.NewAuthenticator(auth.WithDelegatedPrincipal("someone-else")), caller: caller},
		"asserted subject differs": {verifier: d.Authenticator, caller: caller, mutate: func(a *forward.CallerAssertion) {
			a.Subject = auth.ServicePrincipalSubjectPrefix + "someone-else"
		}},
		"asserted tenant differs":      {verifier: d.Authenticator, caller: caller, mutate: func(a *forward.CallerAssertion) { a.TenantID = "globex" }},
		"also claims a holder profile": {verifier: d.Authenticator, caller: caller, mutate: func(a *forward.CallerAssertion) { a.HolderProfile = "custodian" }},
		"not base64":                   {verifier: d.Authenticator, caller: caller, mutate: func(a *forward.CallerAssertion) { a.Delegation = "!not base64!" }},
		"not a certificate":            {verifier: d.Authenticator, caller: caller, mutate: func(a *forward.CallerAssertion) { a.Delegation = "AAAA" }},
	} {
		t.Run(name, func(t *testing.T) {
			a := n.assertion(tc.caller)
			if tc.mutate != nil {
				tc.mutate(&a)
			}
			ctx, err := n.forward(t, dlgLeader(t, tc.verifier), a)
			if err == nil {
				t.Fatalf("admitted as %+v; want refused", auth.IdentityFromContext(ctx))
			}
		})
	}
}

// TestForwardedDelegation_RevocationErrorStaysRetryable: a revocation source
// that cannot answer is reported as such, not collapsed into a refusal.
func TestForwardedDelegation_RevocationErrorStaysRetryable(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	n := newDLGNode(t)
	v := d.NewAuthenticator(auth.WithRevocationChecker(dlgUnavailable{}))
	_, err := n.forward(t, dlgLeader(t, v), n.assertion(d.Identity(t)))
	var rc *auth.RevocationCheckError
	if !errors.As(err, &rc) {
		t.Fatalf("err = %v, want *auth.RevocationCheckError", err)
	}
}

// dlgUnavailable is a revocation source that cannot answer.
type dlgUnavailable struct{}

func (dlgUnavailable) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) {
	return false, errors.New("crl unavailable")
}

// TestForwardedDelegation_OrdinaryAssertionUnchanged: an assertion with no
// delegation is the user path it always was, verifier or not.
func TestForwardedDelegation_OrdinaryAssertionUnchanged(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	n := newDLGNode(t)
	// An ordinary forwarded caller is an account, asserted by its immutable
	// user id (#670), and re-resolved by that id here.
	alice := &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}
	leader := dlgMiddleware(t, &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate},
		WithAuditLogger(&mockAuditLogger{}), WithDelegationVerifier(d.Authenticator), WithUserService(&mockUserServicer{user: alice}))
	ctx, err := n.forward(t, leader, n.assertion(&auth.Identity{Subject: obAliceSubject, TenantID: "acme"}))
	if err != nil {
		t.Fatalf("ordinary on-behalf: %v", err)
	}
	if id := auth.IdentityFromContext(ctx); id.Subject != obAliceSubject || id.Username != "alice" {
		t.Fatalf("identity = %+v, want user id %d", id, obAliceID)
	} else if _, ok := auth.DelegationOf(id); ok {
		t.Fatal("an ordinary caller came back delegated")
	}
}
