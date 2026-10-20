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

package clienttrust

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
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The delegation tests run the real chain: a Pool trusting two tenants'
// login CAs (Key B), and the mTLS authenticator every transport uses,
// configured as a node accepting delegations for delegatedPrincipal.
const (
	delegatedPrincipal = "xkms"
	delegationTD       = "db.example.com"
)

// delegationRevoker is a scripted auth.RevocationChecker.
type delegationRevoker struct {
	revoked bool
	err     error
	calls   int
}

func (r *delegationRevoker) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) {
	r.calls++
	return r.revoked, r.err
}

// signingCA is a tenant login CA a test signs delegations with.
type signingCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// delegationFixture is the trust pool, its two tenant CAs ("acme" and
// "globex"), a cluster CA the pool also trusts, and the revocation script.
type delegationFixture struct {
	pool    *Pool
	acme    signingCA
	globex  signingCA
	cluster signingCA
	revoker *delegationRevoker
}

func newDelegationFixture(t *testing.T) *delegationFixture {
	t.Helper()
	acmePEM, acmeCert, acmeKey := genCAWithKey(t, "acme-login-ca")
	globexPEM, globexCert, globexKey := genCAWithKey(t, "globex-login-ca")
	_, clusterCert, clusterKey := genCAWithKey(t, "cluster-ca")
	pool := New()
	pool.SetKeyBProvider(&fakeCertProvider{
		pems:     [][]byte{acmePEM, globexPEM},
		registry: map[string]*x509.Certificate{"acme": acmeCert, "globex": globexCert},
	})
	pool.SetClusterCA(&fakeClusterCA{cert: clusterCert})
	rebuild(t, pool)
	return &delegationFixture{
		pool:    pool,
		acme:    signingCA{acmeCert, acmeKey},
		globex:  signingCA{globexCert, globexKey},
		cluster: signingCA{clusterCert, clusterKey},
		revoker: &delegationRevoker{},
	}
}

// authenticator is the node's mTLS authenticator, accepting delegations for
// delegatedPrincipal.
func (f *delegationFixture) authenticator(opts ...auth.MTLSOption) *auth.MTLSAuthenticator {
	base := []auth.MTLSOption{
		auth.WithIssuerVerifier(f.pool),
		auth.WithRevocationChecker(f.revoker),
		auth.WithDelegatedPrincipal(delegatedPrincipal),
	}
	return auth.NewMTLSAuthenticator(append(base, opts...)...)
}

// acmeGrant is the delegation the positive case presents.
func acmeGrant() delegation.Grant {
	return delegation.Grant{Tenant: "acme", Principal: delegatedPrincipal, Scope: []string{"kv:read", "kv:write"}}
}

// issueDelegation signs a delegation certificate carrying g with ca: a
// one-hour client certificate naming g's principal as a service of g's
// tenant in a SPIFFE SAN. mutate, when non-nil, edits the template first.
func issueDelegation(t *testing.T, ca signingCA, g delegation.Grant, mutate func(*x509.Certificate)) *x509.Certificate {
	t.Helper()
	ext, err := g.Extension()
	if err != nil {
		t.Fatalf("Extension: %v", err)
	}
	san, err := spiffe.ID{TrustDomain: g.Tenant, Kind: spiffe.KindService, Name: g.Principal}.URI()
	if err != nil {
		t.Fatalf("SPIFFE URI: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:    big.NewInt(0x7d1),
		Subject:         pkix.Name{CommonName: auth.ServicePrincipalSubjectPrefix + g.Principal, OrganizationalUnit: []string{g.Tenant}},
		NotBefore:       now.Add(-time.Minute),
		NotAfter:        now.Add(delegation.DefaultLifetime),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:            []*url.URL{san},
		ExtraExtensions: []pkix.Extension{ext},
	}
	if mutate != nil {
		mutate(tmpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return leaf
}

// presented is the context a transport hands the authenticator for a
// connection that presented leaf (crypto/tls built no chains: Pool.Wire
// leaves verification to the authenticator).
func presented(leaf *x509.Certificate) context.Context {
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}})
}

// assertDelegationRefused checks the refusal is the typed delegation
// refusal, an unusable credential on every transport, and bound nothing.
func assertDelegationRefused(t *testing.T, id *auth.Identity, err error) *auth.DelegationRefusedError {
	t.Helper()
	if id != nil {
		t.Fatalf("refused delegation produced identity %+v", id)
	}
	var refused *auth.DelegationRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v (%T), want *auth.DelegationRefusedError", err, err)
	}
	if !errors.Is(err, auth.ErrInvalidCertificate) {
		t.Fatalf("err = %v does not match auth.ErrInvalidCertificate", err)
	}
	return refused
}

func TestDelegation_AdmittedFromTheTenantsOwnCA(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.acme, acmeGrant(), nil)

	id, err := f.authenticator().Authenticate(presented(leaf))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != auth.ServicePrincipalSubjectPrefix+delegatedPrincipal || id.TenantID != "acme" {
		t.Fatalf("identity = %s in %q, want the service principal in acme", id.Subject, id.TenantID)
	}
	if !auth.IsServicePrincipal(id) || id.Metadata["tenant_source"] != "spiffe" {
		t.Fatalf("metadata = %v, want the service-principal marker and tenant_source spiffe", id.Metadata)
	}
	d, ok := auth.DelegationOf(id)
	if !ok {
		t.Fatal("DelegationOf reports no delegation")
	}
	if d.Principal != delegatedPrincipal || !slices.Equal(d.Scope, acmeGrant().Scope) || d.Serial != "7d1" {
		t.Fatalf("delegation = %+v, want principal, scope and serial from the certificate", d)
	}
	if !d.NotAfter.Equal(leaf.NotAfter) || !slices.Equal(d.Certificate, leaf.Raw) {
		t.Fatal("delegation does not carry the certificate and its expiry")
	}
	if f.revoker.calls != 1 {
		t.Fatalf("revocation consulted %d times, want 1", f.revoker.calls)
	}
}

// TestDelegation_RefusedWithoutAnAgreeingOUAndSPIFFESAN: a delegation names
// its tenant in both its Subject OU and its service SPIFFE ID SAN, and they
// agree; the OU alone, or an OU naming another tenant, names none.
func TestDelegation_RefusedWithoutAnAgreeingOUAndSPIFFESAN(t *testing.T) {
	f := newDelegationFixture(t)
	for name, tc := range map[string]struct {
		mutate func(*x509.Certificate)
		cause  error
	}{
		"Subject OU without a SPIFFE SAN": {func(c *x509.Certificate) { c.URIs = nil }, spiffe.ErrNoTenantSPIFFEID},
		"SPIFFE SAN without a Subject OU": {func(c *x509.Certificate) { c.Subject.OrganizationalUnit = nil }, auth.ErrNoTenantOU},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := f.authenticator().Authenticate(presented(issueDelegation(t, f.acme, acmeGrant(), tc.mutate)))
			var cte *auth.CertificateTenantError
			if id != nil || !errors.As(err, &cte) || !errors.Is(err, tc.cause) || !errors.Is(err, auth.ErrInvalidCertificate) {
				t.Fatalf("Authenticate = (%+v, %v), want a *auth.CertificateTenantError wrapping %v", id, err, tc.cause)
			}
		})
	}

	t.Run("Subject OU names another tenant", func(t *testing.T) {
		other := issueDelegation(t, f.acme, acmeGrant(), func(c *x509.Certificate) {
			c.Subject.OrganizationalUnit = []string{"globex"}
		})
		id, err := f.authenticator().Authenticate(presented(other))
		var mismatch *auth.TenantNameMismatchError
		if id != nil || !errors.As(err, &mismatch) || mismatch.OU != "globex" || mismatch.SPIFFETenant != "acme" {
			t.Fatalf("Authenticate = (%+v, %v), want a *auth.TenantNameMismatchError of OU globex against acme", id, err)
		}
	})
}

// TestDelegation_RefusedWhenSignedByAnotherTenantsCA: every tenant's login
// CA is trusted on the shared listener, so the chain verifies; the issuer
// binding is what refuses a CA speaking for a tenant it does not belong to.
func TestDelegation_RefusedWhenSignedByAnotherTenantsCA(t *testing.T) {
	globexGrant := acmeGrant()
	globexGrant.Tenant = "globex"
	for name, tc := range map[string]struct {
		ca    func(*delegationFixture) signingCA
		grant delegation.Grant
	}{
		"acme delegation signed by globex": {func(f *delegationFixture) signingCA { return f.globex }, acmeGrant()},
		"globex delegation signed by acme": {func(f *delegationFixture) signingCA { return f.acme }, globexGrant},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDelegationFixture(t)
			leaf := issueDelegation(t, tc.ca(f), tc.grant, nil)
			id, err := f.authenticator().Authenticate(presented(leaf))
			assertDelegationRefused(t, id, err)
			var notTrusted *IssuerNotTrustedError
			if !errors.As(err, &notTrusted) || notTrusted.TenantID != tc.grant.Tenant {
				t.Fatalf("err = %v, want the issuer binding's *IssuerNotTrustedError for %q", err, tc.grant.Tenant)
			}
		})
	}
}

// TestDelegation_ThePlatformCannotMintOne: the cluster CA signs node and
// holder certificates for any tenant, and is still not a tenant's own CA.
func TestDelegation_ThePlatformCannotMintOne(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.cluster, acmeGrant(), nil)
	id, err := f.authenticator().Authenticate(presented(leaf))
	if id != nil || !errors.Is(err, auth.ErrInvalidCertificate) {
		t.Fatalf("Authenticate = (%+v, %v), want a refused certificate", id, err)
	}
}

func TestDelegation_TheSystemTenantCannotDelegate(t *testing.T) {
	f := newDelegationFixture(t)
	g := acmeGrant()
	g.Tenant = tenantid.System
	leaf := issueDelegation(t, f.acme, g, nil)
	id, err := f.authenticator().Authenticate(presented(leaf))
	refused := assertDelegationRefused(t, id, err)
	if refused.Reason != "the system tenant cannot delegate" {
		t.Fatalf("Reason = %q, want the system-tenant refusal", refused.Reason)
	}
}

func TestDelegation_Expired(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.acme, acmeGrant(), func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-2 * time.Hour)
		c.NotAfter = time.Now().Add(-time.Hour)
	})
	a := f.authenticator()

	id, err := a.Authenticate(presented(leaf))
	if id != nil || !errors.Is(err, auth.ErrInvalidCertificate) {
		t.Fatalf("Authenticate = (%+v, %v), want an invalid certificate", id, err)
	}
	if f.revoker.calls != 0 {
		t.Fatal("an expired delegation reached revocation: chain verification must refuse it first")
	}

	id, err = a.VerifyDelegation(context.Background(), leaf.Raw)
	assertDelegationRefused(t, id, err)
}

// TestPool_VerifyPeerChainReadsTheSetClock: a certificate's lifetime is read
// on the clock SetClock installs, so a delegation valid on the wall clock is
// expired once that clock passes its NotAfter; clearing the clock returns
// the pool to wall time.
func TestPool_VerifyPeerChainReadsTheSetClock(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.acme, acmeGrant(), nil)
	clk := clocktest.NewAt(time.Now())
	f.pool.SetClock(clk)
	if _, err := f.pool.VerifyPeerChain(context.Background(), leaf, nil); err != nil {
		t.Fatalf("VerifyPeerChain inside the lifetime = %v; want verified", err)
	}

	clk.Advance(leaf.NotAfter.Sub(clk.Now()) + time.Second)
	_, err := f.pool.VerifyPeerChain(context.Background(), leaf, nil)
	var invalid x509.CertificateInvalidError
	if !errors.As(err, &invalid) || invalid.Reason != x509.Expired {
		t.Fatalf("VerifyPeerChain past NotAfter on the set clock = %v; want x509.Expired", err)
	}

	f.pool.SetClock(nil)
	if _, err := f.pool.VerifyPeerChain(context.Background(), leaf, nil); err != nil {
		t.Fatalf("VerifyPeerChain after clearing the clock = %v; want verified on wall time", err)
	}
}

// TestDelegation_LifetimeAboveTheCap: a CA that issued past
// delegation.MaxLifetime is refused, whatever it put in the certificate.
func TestDelegation_LifetimeAboveTheCap(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.acme, acmeGrant(), func(c *x509.Certificate) {
		c.NotAfter = c.NotBefore.Add(delegation.MaxLifetime + time.Minute)
	})
	id, err := f.authenticator().Authenticate(presented(leaf))
	assertDelegationRefused(t, id, err)
	var lifetime *delegation.LifetimeError
	if !errors.As(err, &lifetime) {
		t.Fatalf("err = %v, want *delegation.LifetimeError", err)
	}
}

func TestDelegation_Revocation(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		f := newDelegationFixture(t)
		f.revoker.revoked = true
		id, err := f.authenticator().Authenticate(presented(issueDelegation(t, f.acme, acmeGrant(), nil)))
		if id != nil || !errors.Is(err, auth.ErrCertificateRevoked) {
			t.Fatalf("Authenticate = (%+v, %v), want ErrCertificateRevoked", id, err)
		}
	})
	t.Run("unreadable state fails closed", func(t *testing.T) {
		f := newDelegationFixture(t)
		f.revoker.err = errors.New("revocation state sealed")
		id, err := f.authenticator().Authenticate(presented(issueDelegation(t, f.acme, acmeGrant(), nil)))
		var checkErr *auth.RevocationCheckError
		if id != nil || !errors.As(err, &checkErr) {
			t.Fatalf("Authenticate = (%+v, %v), want *auth.RevocationCheckError", id, err)
		}
	})
	t.Run("no checker", func(t *testing.T) {
		f := newDelegationFixture(t)
		a := auth.NewMTLSAuthenticator(auth.WithIssuerVerifier(f.pool), auth.WithDelegatedPrincipal(delegatedPrincipal))
		id, err := a.Authenticate(presented(issueDelegation(t, f.acme, acmeGrant(), nil)))
		assertDelegationRefused(t, id, err)
	})
}

// TestDelegation_NamingADifferentPrincipal: a node accepts delegations for
// its own service principal only, and for none when it has none.
func TestDelegation_NamingADifferentPrincipal(t *testing.T) {
	f := newDelegationFixture(t)
	other := acmeGrant()
	other.Principal = "quicbaas"
	id, err := f.authenticator().Authenticate(presented(issueDelegation(t, f.acme, other, nil)))
	assertDelegationRefused(t, id, err)

	none := auth.NewMTLSAuthenticator(auth.WithIssuerVerifier(f.pool), auth.WithRevocationChecker(f.revoker))
	id, err = none.Authenticate(presented(issueDelegation(t, f.acme, acmeGrant(), nil)))
	assertDelegationRefused(t, id, err)
	if f.revoker.calls != 0 {
		t.Fatal("a delegation for another principal reached revocation")
	}
}

// TestDelegation_SPIFFESANMustNameTheDelegatedService: the SAN agrees with
// the grant on tenant, kind and name, or the certificate is refused.
func TestDelegation_SPIFFESANMustNameTheDelegatedService(t *testing.T) {
	for name, id := range map[string]spiffe.ID{
		"user kind":  {TrustDomain: "acme", Kind: spiffe.KindUser, Name: delegatedPrincipal},
		"other name": {TrustDomain: "acme", Kind: spiffe.KindService, Name: "quicbaas"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDelegationFixture(t)
			san, err := id.URI()
			if err != nil {
				t.Fatal(err)
			}
			leaf := issueDelegation(t, f.acme, acmeGrant(), func(c *x509.Certificate) { c.URIs = []*url.URL{san} })
			got, err := f.authenticator().Authenticate(presented(leaf))
			assertDelegationRefused(t, got, err)
		})
	}
}

// TestDelegation_SPIFFESANInAnotherTenantIsATenantDisagreement: a delegated
// leaf whose SAN names another tenant's trust domain disagrees with its own
// OU, so it is refused as a certificate naming two tenants before the grant is
// compared at all.
func TestDelegation_SPIFFESANInAnotherTenantIsATenantDisagreement(t *testing.T) {
	f := newDelegationFixture(t)
	san, err := spiffe.ID{TrustDomain: "globex", Kind: spiffe.KindService, Name: delegatedPrincipal}.URI()
	if err != nil {
		t.Fatal(err)
	}
	leaf := issueDelegation(t, f.acme, acmeGrant(), func(c *x509.Certificate) { c.URIs = []*url.URL{san} })
	got, err := f.authenticator().Authenticate(presented(leaf))
	var mismatch *auth.TenantNameMismatchError
	if got != nil || !errors.As(err, &mismatch) || mismatch.SPIFFETenant != "globex" {
		t.Fatalf("Authenticate = (%v, %v), want a *auth.TenantNameMismatchError naming globex", got, err)
	}
}

// TestDelegation_MalformedExtensionIsRefused: the pool never marks a
// malformed delegation handled, so its chain does not verify; and where a
// chain arrives already verified, the authenticator refuses it itself.
func TestDelegation_MalformedExtensionIsRefused(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.acme, acmeGrant(), func(c *x509.Certificate) {
		c.ExtraExtensions = []pkix.Extension{{Id: delegation.OID, Critical: true, Value: []byte{0x30, 0x03, 0x02}}}
	})
	if _, err := f.pool.VerifyPeerChain(context.Background(), leaf, nil); err == nil {
		t.Fatal("the pool verified a chain whose leaf carries a malformed critical delegation")
	}
	id, err := f.authenticator().Authenticate(presented(leaf))
	if id != nil || !errors.Is(err, auth.ErrInvalidCertificate) || f.revoker.calls != 0 {
		t.Fatalf("Authenticate = (%+v, %v), want an invalid certificate refused before revocation", id, err)
	}

	verified := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, f.acme.cert}}}
	id, err = f.authenticator().Authenticate(auth.WithTLSState(context.Background(), verified))
	refused := assertDelegationRefused(t, id, err)
	if refused.Reason != "malformed delegation" {
		t.Fatalf("Reason = %q, want the malformed-delegation refusal", refused.Reason)
	}
}

// TestDelegation_UnawareVerifiersRejectIt: the extension is critical, so a
// verifier that does not know it -- plain crypto/x509 -- refuses the
// certificate rather than reading it as an ordinary login.
func TestDelegation_UnawareVerifiersRejectIt(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.acme, acmeGrant(), nil)
	roots := x509.NewCertPool()
	roots.AddCert(f.acme.cert)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if !errors.As(err, new(x509.UnhandledCriticalExtension)) {
		t.Fatalf("plain Verify = %v, want x509.UnhandledCriticalExtension", err)
	}
	if _, err := f.pool.VerifyPeerChain(context.Background(), leaf, nil); err != nil {
		t.Fatalf("Pool.VerifyPeerChain: %v", err)
	}
}

// issuerOnly hides Pool's TenantIssuerVerifier, as an embedder's own
// IssuerVerifier would lack it.
type issuerOnly struct{ auth.IssuerVerifier }

func TestDelegation_RefusedWithoutATenantIssuerBinding(t *testing.T) {
	f := newDelegationFixture(t)
	a := auth.NewMTLSAuthenticator(auth.WithIssuerVerifier(issuerOnly{f.pool}),
		auth.WithRevocationChecker(f.revoker), auth.WithDelegatedPrincipal(delegatedPrincipal))
	id, err := a.Authenticate(presented(issueDelegation(t, f.acme, acmeGrant(), nil)))
	assertDelegationRefused(t, id, err)
}

func TestVerifyDelegation(t *testing.T) {
	f := newDelegationFixture(t)
	leaf := issueDelegation(t, f.acme, acmeGrant(), nil)

	id, err := f.authenticator().VerifyDelegation(context.Background(), leaf.Raw)
	if err != nil {
		t.Fatalf("VerifyDelegation: %v", err)
	}
	if d, ok := auth.DelegationOf(id); !ok || id.TenantID != "acme" || d.Principal != delegatedPrincipal {
		t.Fatalf("identity = %+v, want the acme delegation", id)
	}

	plain := issueDelegation(t, f.acme, acmeGrant(), func(c *x509.Certificate) { c.ExtraExtensions = nil })
	for name, tc := range map[string]struct {
		a   *auth.MTLSAuthenticator
		der []byte
	}{
		"garbage":         {f.authenticator(), []byte("not a certificate")},
		"no delegation":   {f.authenticator(), plain.Raw},
		"no trust pool":   {auth.NewMTLSAuthenticator(auth.WithRevocationChecker(f.revoker), auth.WithDelegatedPrincipal(delegatedPrincipal)), leaf.Raw},
		"another tenant":  {f.authenticator(), issueDelegation(t, f.globex, acmeGrant(), nil).Raw},
		"other principal": {auth.NewMTLSAuthenticator(auth.WithIssuerVerifier(f.pool), auth.WithRevocationChecker(f.revoker), auth.WithDelegatedPrincipal("quicbaas")), leaf.Raw},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := tc.a.VerifyDelegation(context.Background(), tc.der)
			assertDelegationRefused(t, id, err)
		})
	}
}

func TestDelegationOf_ReturnsACopy(t *testing.T) {
	f := newDelegationFixture(t)
	id, err := f.authenticator().Authenticate(presented(issueDelegation(t, f.acme, acmeGrant(), nil)))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := auth.DelegationOf(id)
	d.Scope[0] = "admin:shutdown"
	d.Certificate[0] ^= 0xff
	again, _ := auth.DelegationOf(id)
	if again.Scope[0] != "kv:read" || again.Certificate[0] == d.Certificate[0] {
		t.Fatal("DelegationOf handed out the identity's own slices")
	}
	if _, ok := auth.DelegationOf(&auth.Identity{Subject: "alice"}); ok {
		t.Fatal("an ordinary identity reports a delegation")
	}
	if _, ok := auth.DelegationOf(nil); ok {
		t.Fatal("a nil identity reports a delegation")
	}
}

func TestPool_VerifyTenantIssuer(t *testing.T) {
	f := newDelegationFixture(t)
	ctx := context.Background()
	if err := f.pool.VerifyTenantIssuer(ctx, "acme", f.acme.cert); err != nil {
		t.Fatalf("own login CA: %v", err)
	}
	for name, issuer := range map[string]*x509.Certificate{
		"another tenant's CA": f.globex.cert,
		"the cluster CA":      f.cluster.cert,
		"nil":                 nil,
	} {
		var notTrusted *IssuerNotTrustedError
		if err := f.pool.VerifyTenantIssuer(ctx, "acme", issuer); !errors.As(err, &notTrusted) {
			t.Errorf("%s: err = %v, want *IssuerNotTrustedError", name, err)
		}
	}
	if err := New().VerifyTenantIssuer(ctx, "acme", f.acme.cert); err == nil {
		t.Fatal("a pool with no Key B provider bound an issuer to a tenant")
	}
}
