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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Fixtures
//
// These tests run the REAL auth.MTLSAuthenticator against the REAL
// user.UserServicer (kvstore-backed, the production implementation) on a
// production in-memory KVStore. A user-store double that answered
// "binding matches" would assert the double, not the authentication path —
// and the binding predicate lives on user.User, so a narrower double would
// prove the opposite of what it appears to.
// ---------------------------------------------------------------------------

// certKey is a minted leaf plus the private key it was issued for, so a test
// can mint a SECOND certificate for the SAME key (the renewal case) or for a
// DIFFERENT key (the impersonation case).
type certKey struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// mintCert issues a self-signed leaf naming cn/ou. A nil key generates a
// fresh one; passing an existing key reissues for the same public key, which
// is what makes the SPKI thumbprint stable across renewal. With no spiffeURI
// the leaf carries the SPIFFE ID SAN agreeing with ou
// (authtest.SetClientTenant), the shape every tenant certificate must have;
// a spiffeURI replaces it verbatim.
func mintCert(t *testing.T, cn, ou string, key *ecdsa.PrivateKey, spiffeURI string) certKey {
	t.Helper()
	if key == nil {
		var err error
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	switch {
	case ou != "" && spiffeURI == "":
		authtest.SetClientTenant(t, tmpl, ou, cn)
	case ou != "":
		tmpl.Subject.OrganizationalUnit = []string{ou}
	}
	if spiffeURI != "" {
		u, err := url.Parse(spiffeURI)
		if err != nil {
			t.Fatalf("parse spiffe uri: %v", err)
		}
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return certKey{cert: cert, key: key}
}

// mtlsContext presents ck as the verified peer certificate, exactly as every
// transport does before the authenticator chain runs.
func mtlsContext(ck certKey) context.Context {
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{ck.cert},
		VerifiedChains:   [][]*x509.Certificate{{ck.cert}},
	})
}

// newRealUserStore builds the production kvstore-backed UserServicer.
func newRealUserStore(t *testing.T) user.UserServicer {
	t.Helper()
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	svc, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return svc
}

// createUser creates an enabled user in the real store.
func createUser(t *testing.T, svc user.UserServicer, tenantID, username string, roles []string) *user.User {
	t.Helper()
	u, err := svc.CreateUser(context.Background(), &user.CreateUserRequest{
		Username:             username,
		TenantID:             tenantID,
		WithoutEmailLoginKey: true,
		RoleIDs:              roles,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s/%s): %v", tenantID, username, err)
	}
	return u
}

// enroll binds ck's SPKI thumbprint to the user through the canonical
// enrollment verb — the same call the six transports expose.
func enroll(t *testing.T, svc user.UserServicer, userID uint64, ck certKey) {
	t.Helper()
	if _, err := svc.AddCertBinding(context.Background(), userID,
		user.ComputeSPKIThumbprint(ck.cert), 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
}

// holdExpiredBinding leaves the account holding ck's binding with an expiry
// one hour in the past -- the state time produces from a live enrolment.
// AddCertBinding refuses to enrol a binding already expired, so the record is
// written the way it looks once the expiry has passed.
func holdExpiredBinding(t *testing.T, svc user.UserServicer, userID uint64, ck certKey) {
	t.Helper()
	if _, err := svc.UpdateUser(context.Background(), &user.UpdateUserRequest{ID: userID, CertBindings: []user.CertBinding{{
		Thumbprint: user.ComputeSPKIThumbprint(ck.cert),
		Kind:       user.CertBindingSPKIThumbprint,
		NotAfter:   time.Now().Add(-time.Hour).UTC(),
	}}}); err != nil {
		t.Fatalf("UpdateUser(CertBindings): %v", err)
	}
}

// mtlsMiddleware wires the REAL mTLS authenticator over the given user store.
// The default policy is "required" (go-qrdb#548); pass
// WithMTLSBindingPolicy(auth.MTLSBindingEnrolled) for a test that
// specifically covers the "enrolled" opt-out.
func mtlsMiddleware(svc user.UserServicer, opts ...Option) *AuthMiddleware {
	base := []Option{
		WithAuthenticator(auth.NewMTLSAuthenticator()),
		WithUserService(svc),
	}
	return newTestMiddleware(&mockAuthorizer{}, append(base, opts...)...)
}

// ---------------------------------------------------------------------------
// The defect: a certificate naming another user in another tenant
// ---------------------------------------------------------------------------

// TestAuthenticate_RefusesCertNamingAnEnrolledUserItIsNotBoundTo is the
// containment layer for the cross-tenant credential-minting path: an attacker
// holding a CA-signed certificate whose CN and OU name someone else must not
// become that someone else.
//
// alice@victim is enrolled — she owns a binding for her own key. The presented
// certificate names her exactly (CN=alice, OU=victim) and chains to the same
// CA, but was issued for a DIFFERENT key, so its SPKI thumbprint is not one of
// hers.
func TestAuthenticate_RefusesCertNamingAnEnrolledUserItIsNotBoundTo(t *testing.T) {
	svc := newRealUserStore(t)
	alice := createUser(t, svc, "victim", "alice", []string{"admin"})
	aliceCert := mintCert(t, "alice", "victim", nil, "")
	enroll(t, svc, alice.ID, aliceCert)

	// The minted impersonation credential: same CN, same OU, attacker's key.
	forged := mintCert(t, "alice", "victim", nil, "")

	m := mtlsMiddleware(svc)
	_, identity, err := m.authenticate(mtlsContext(forged))
	if err == nil {
		t.Fatalf("a certificate naming alice@victim but bound to no user authenticated as %+v", identity)
	}
	var want *CertBindingNotPermittedError
	if !errors.As(err, &want) {
		t.Fatalf("err = %T(%v), want *CertBindingNotPermittedError", err, err)
	}
	if want.Username != "alice" || want.TenantID != "victim" {
		t.Fatalf("error names %s/%s, want victim/alice", want.TenantID, want.Username)
	}
}

// TestAuthenticate_RefusalIsUnauthenticatedNotForbidden pins the failure
// class. The certificate is not this account's credential at all, so the
// answer is "you have not identified yourself" (401) rather than "you may not
// do that" (403) — the class AuthMethodNotPermittedError carries, where the
// credential IS the account's and the WAY in is refused.
func TestAuthenticate_RefusalIsUnauthenticatedNotForbidden(t *testing.T) {
	svc := newRealUserStore(t)
	alice := createUser(t, svc, "victim", "alice", nil)
	enroll(t, svc, alice.ID, mintCert(t, "alice", "victim", nil, ""))

	m := mtlsMiddleware(svc)
	_, _, err := m.authenticate(mtlsContext(mintCert(t, "alice", "victim", nil, "")))
	if err == nil {
		t.Fatal("expected refusal")
	}
	if got := ClassifyAuthError(err); got != AuthFailureUnauthenticated {
		t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnauthenticated", got)
	}
}

// ---------------------------------------------------------------------------
// The control: a legitimate certificate authenticates exactly as before
// ---------------------------------------------------------------------------

// TestAuthenticate_AdmitsCertMatchingItsBinding is the control for the test
// above: the same user, the same store, the certificate she actually enrolled.
func TestAuthenticate_AdmitsCertMatchingItsBinding(t *testing.T) {
	svc := newRealUserStore(t)
	alice := createUser(t, svc, "victim", "alice", []string{"admin", "operator"})
	aliceCert := mintCert(t, "alice", "victim", nil, "")
	enroll(t, svc, alice.ID, aliceCert)

	m := mtlsMiddleware(svc)
	_, identity, err := m.authenticate(mtlsContext(aliceCert))
	if err != nil {
		t.Fatalf("enrolled certificate refused: %v", err)
	}
	if identity.UserID == 0 || identity.Username != "alice" || identity.TenantID != "victim" {
		t.Fatalf("identity = %s/%s, want victim/alice", identity.TenantID, identity.AccountName())
	}
	if len(identity.Roles) != 2 {
		t.Fatalf("roles = %v, want the store's two", identity.Roles)
	}
}

// TestAuthenticate_AdmitsRenewedCertForTheSameKey is why the binding is over
// the SubjectPublicKeyInfo and not the certificate: a renewal keeps the key,
// so it keeps the thumbprint, so it keeps working. Certificate rotation must
// stay an operation, not a re-enrolment.
func TestAuthenticate_AdmitsRenewedCertForTheSameKey(t *testing.T) {
	svc := newRealUserStore(t)
	alice := createUser(t, svc, "victim", "alice", nil)
	original := mintCert(t, "alice", "victim", nil, "")
	enroll(t, svc, alice.ID, original)

	renewed := mintCert(t, "alice", "victim", original.key, "")
	if renewed.cert.Equal(original.cert) {
		t.Fatal("fixture did not mint a distinct certificate")
	}

	m := mtlsMiddleware(svc)
	if _, _, err := m.authenticate(mtlsContext(renewed)); err != nil {
		t.Fatalf("renewed certificate for the bound key refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The no-bindings decision, asserted in BOTH directions
// ---------------------------------------------------------------------------

// TestAuthenticate_NoBindingsAdmitsAndEnrollingOneCloses asserts the decision
// for an account with no bindings, and its converse, on ONE account so the
// difference is the enrolment and nothing else.
//
// An account with an EMPTY binding set states no restriction — the same
// statement User.AuthMethods makes — so every deployment that predates
// enrolment keeps working. The moment the operator enrols a binding, the
// account is closed: a certificate naming it that matches no binding is
// refused.
func TestAuthenticate_NoBindingsAdmitsAndEnrollingOneCloses(t *testing.T) {
	svc := newRealUserStore(t)
	bob := createUser(t, svc, "acme", "bob", []string{"reader"})
	unbound := mintCert(t, "bob", "acme", nil, "")

	// This test specifically covers the "enrolled" opt-out (go-qrdb#548):
	// under the default "required" posture, direction 1 below would be
	// refused, not admitted -- that is the whole point of the default
	// changing. "enrolled" is what this test is about, not a workaround.
	m := mtlsMiddleware(svc, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))

	// Direction 1: no bindings -> admitted under "enrolled".
	_, identity, err := m.authenticate(mtlsContext(unbound))
	if err != nil {
		t.Fatalf("unenrolled account refused: %v", err)
	}
	if identity.Subject != auth.UserSubject(bob.ID) || identity.Username != "bob" || identity.TenantID != "acme" {
		t.Fatalf("identity = %s/%s, want acme/bob", identity.TenantID, identity.AccountName())
	}

	// Direction 2: enrol a DIFFERENT certificate; the same presentation is now
	// refused. Nothing changed but the binding set.
	enroll(t, svc, bob.ID, mintCert(t, "bob", "acme", nil, ""))
	if _, _, err := m.authenticate(mtlsContext(unbound)); err == nil {
		t.Fatal("an enrolled account still admitted an unbound certificate")
	}
}

// TestAuthenticate_ExpiredBindingDoesNotAdmit closes the replay window on a
// decommissioned credential: an expired binding is not a binding, and the
// account still HAS a binding set, so the presentation is refused rather than
// falling back to the empty-set admission.
func TestAuthenticate_ExpiredBindingDoesNotAdmit(t *testing.T) {
	svc := newRealUserStore(t)
	carol := createUser(t, svc, "acme", "carol", nil)
	retired := mintCert(t, "carol", "acme", nil, "")
	holdExpiredBinding(t, svc, carol.ID, retired)

	m := mtlsMiddleware(svc)
	if _, _, err := m.authenticate(mtlsContext(retired)); err == nil {
		t.Fatal("an expired binding admitted its certificate")
	}
}

// ---------------------------------------------------------------------------
// The SPIFFE SAN path is NOT exempt
// ---------------------------------------------------------------------------

// TestAuthenticate_SPIFFETenantSANIsNotExempt asserts the check applies to a
// certificate whose tenant the SPIFFE SAN names, agreeing with its OU as every
// tenant certificate must. The SAN is issued by the SAME CA as the OU, so it
// carries no stronger claim to the identity it names than the OU does.
func TestAuthenticate_SPIFFETenantSANIsNotExempt(t *testing.T) {
	svc := newRealUserStore(t)
	dave := createUser(t, svc, "acme", "dave", []string{"admin"})
	enroll(t, svc, dave.ID, mintCert(t, "dave", "acme", nil, ""))

	forged := mintCert(t, "dave", "acme", nil, "spiffe://acme/user/dave")

	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(auth.NewMTLSAuthenticatorWithTrustDomain("example.com")),
		WithUserService(svc),
	)
	_, identity, err := m.authenticate(mtlsContext(forged))
	if err == nil {
		t.Fatalf("a SPIFFE-SAN certificate bound to no user authenticated as %+v", identity)
	}
	if cte := (*auth.CertificateTenantError)(nil); errors.As(err, &cte) {
		t.Fatalf("refused for its tenant fields (%v), not by the binding check this test is about", err)
	}
}

// ---------------------------------------------------------------------------
// Paths that must NOT be subjected to the check
// ---------------------------------------------------------------------------

// TestFinalizeIdentity_UnobservableMethodSkipsTheBindingCheck protects the
// forwarded on-behalf assertion and a capability module's own session-token
// plane. Both reach finalizeIdentity with unknownMethod(), and on both the
// peer certificate on the connection belongs to somebody OTHER than the
// subject — the forwarding cluster node, or whoever terminated the module's
// TLS. Checking a binding against that certificate would refuse every
// forwarded write to an enrolled user.
func TestFinalizeIdentity_UnobservableMethodSkipsTheBindingCheck(t *testing.T) {
	svc := newRealUserStore(t)
	alice := createUser(t, svc, "victim", "alice", []string{"admin"})
	enroll(t, svc, alice.ID, mintCert(t, "alice", "victim", nil, ""))

	// The connection presents a cluster NODE certificate, not alice's.
	ctx := mtlsContext(mintCert(t, "node-1", "victim", nil, ""))

	m := mtlsMiddleware(svc)
	_, identity, err := m.finalizeIdentity(ctx,
		&auth.Identity{Subject: "alice", TenantID: "victim"}, unknownMethod())
	if err != nil {
		t.Fatalf("on-behalf identity for an enrolled user refused: %v", err)
	}
	if len(identity.Roles) != 1 {
		t.Fatalf("roles = %v, want the store's one", identity.Roles)
	}
}

// TestFinalizeIdentity_UnknownUserIsUnaffected keeps the check off the
// not-found path UNDER "enrolled": there is no record, so there is no binding
// set to compare against, and the identity proceeds unenriched into
// default-deny exactly as it did before go-qrdb#517. This is specifically an
// "enrolled" behavior -- under the "required" default a certificate naming no
// account is refused outright (docs/identity-rbac/architecture.md), which is
// covered elsewhere (go-qrdb#548).
func TestFinalizeIdentity_UnknownUserIsUnaffected(t *testing.T) {
	svc := newRealUserStore(t)
	stranger := mintCert(t, "nobody", "acme", nil, "")

	m := mtlsMiddleware(svc, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	_, identity, err := m.authenticate(mtlsContext(stranger))
	if err != nil {
		t.Fatalf("unknown subject refused: %v", err)
	}
	if len(identity.Roles) != 0 {
		t.Fatalf("roles = %v, want none", identity.Roles)
	}
}

// ---------------------------------------------------------------------------
// The predicate itself
// ---------------------------------------------------------------------------

// TestIsCertBoundMethod covers the gate on WHICH methods the binding check
// applies to. The unobservable case is the load-bearing one; the blank and
// unnormalizable names are there because an authenticator naming itself "" was
// already once mistaken for an unobservable method.
func TestIsCertBoundMethod(t *testing.T) {
	cases := []struct {
		name   string
		method authMethod
		want   bool
	}{
		{"mtls", knownMethod(auth.MethodMTLS), true},
		{"case is folded", knownMethod("MTLS"), true},
		{"surrounding space is trimmed", knownMethod("  mtls  "), true},
		{"unobservable", unknownMethod(), false},
		{"observed but blank", knownMethod(""), false},
		{"observed but unnormalizable", knownMethod("   "), false},
		{"another method", knownMethod("webauthn"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCertBoundMethod(tc.method); got != tc.want {
				t.Fatalf("isCertBoundMethod(%+v) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// TestCheckCertBinding_FailsClosedWithoutAPeerCertificate covers the one
// admission the predicate must never make: the account has enrolled
// certificates, the method says one was presented, and none can be read back.
// That is not evidence of anything, so it is not an admission.
func TestCheckCertBinding_FailsClosedWithoutAPeerCertificate(t *testing.T) {
	bound := &user.User{Username: "alice", TenantID: "victim"}
	bound.AddCertBinding(user.CertBinding{Thumbprint: user.ComputeSPKIThumbprint(
		mintCert(t, "alice", "victim", nil, "").cert)})

	err := checkCertBinding(context.Background(), bound, knownMethod(auth.MethodMTLS), auth.MTLSBindingEnrolled)
	if err == nil {
		t.Fatal("an enrolled account was admitted with no peer certificate at all")
	}
	var refused *CertBindingNotPermittedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %T, want *CertBindingNotPermittedError", err)
	}
	if refused.PresentedThumbprint != "" {
		t.Fatalf("PresentedThumbprint = %q, want empty when nothing was presented", refused.PresentedThumbprint)
	}
	if got := refused.Error(); !strings.Contains(got, "<no peer certificate>") {
		t.Fatalf("Error() = %q, want it to name the absent certificate", got)
	}
}

// TestCheckCertBinding_NilUserIsNotAnAdmissionDecision guards the one caller
// shape finalizeIdentity can produce that has no record to judge: a user store
// miss proceeds unenriched, and a predicate that refused on nil would turn
// every unknown subject into a certificate-binding failure.
func TestCheckCertBinding_NilUserIsNotAnAdmissionDecision(t *testing.T) {
	if err := checkCertBinding(context.Background(), nil, knownMethod(auth.MethodMTLS), auth.MTLSBindingRequired); err != nil {
		t.Fatalf("checkCertBinding(nil user) = %v, want nil", err)
	}
}

// TestCertBindingNotPermittedError_NamesThePresentedThumbprint checks the
// operator-facing half: the message has to carry the value that resolves the
// refusal, which is the thumbprint to enrol.
func TestCertBindingNotPermittedError_NamesThePresentedThumbprint(t *testing.T) {
	err := &CertBindingNotPermittedError{
		Username:            "alice",
		TenantID:            "victim",
		PresentedThumbprint: "AAAABBBB",
	}
	got := err.Error()
	for _, want := range []string{"AAAABBBB", "alice", "victim"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Error() = %q, want it to contain %q", got, want)
		}
	}
}
