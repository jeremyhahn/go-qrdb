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

package auth

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

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// These tests hold the mTLS authenticator to the delegation admission table
// in docs/identity-rbac/api.md: every check, in the order the authenticator
// applies it, refuses with the typed error that table names, and a
// delegation that passes them all becomes a service-principal identity.

const (
	delegTenant      = "acme"
	delegPrincipal   = "billing"
	delegTrustDomain = "db.example.com"
)

func delegGrant() delegation.Grant {
	return delegation.Grant{Tenant: delegTenant, Principal: delegPrincipal, Scope: []string{"kv:read", "kv:write"}}
}

// delegLeafSpec describes a delegation certificate. The zero value, after
// defaults, is a well-formed delegation for delegGrant whose Subject OU names
// the grant's tenant.
type delegLeafSpec struct {
	grant     delegation.Grant
	ou        []string
	uris      []*url.URL
	notBefore time.Time
	notAfter  time.Time
	// ext replaces the extension built from grant; noExt omits it.
	ext   *pkix.Extension
	noExt bool
}

func delegLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, s delegLeafSpec) *x509.Certificate {
	t.Helper()
	if s.grant.Tenant == "" {
		s.grant = delegGrant()
	}
	if s.ou == nil {
		s.ou = []string{s.grant.Tenant}
	}
	if s.notBefore.IsZero() {
		s.notBefore = time.Now().Add(-time.Minute)
	}
	if s.notAfter.IsZero() {
		s.notAfter = s.notBefore.Add(delegation.DefaultLifetime)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0xd1),
		Subject:      pkix.Name{CommonName: ServicePrincipalSubjectPrefix + s.grant.Principal, OrganizationalUnit: s.ou},
		URIs:         s.uris,
		NotBefore:    s.notBefore,
		NotAfter:     s.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	switch {
	case s.ext != nil:
		tmpl.ExtraExtensions = []pkix.Extension{*s.ext}
	case !s.noExt:
		ext, err := s.grant.Extension()
		if err != nil {
			t.Fatalf("grant extension: %v", err)
		}
		tmpl.ExtraExtensions = []pkix.Extension{ext}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate (leaf): %v", err)
	}
	return leaf
}

// delegChainVerifier is a trust pool holding one CA. It verifies a chain the
// way pkg/clienttrust does for a delegation (the critical extension marked
// handled through delegation.Verifiable) but cannot bind an issuer to a
// tenant: it does not implement TenantIssuerVerifier.
type delegChainVerifier struct {
	ca *x509.Certificate
}

func (v *delegChainVerifier) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, intermediates []*x509.Certificate) ([]*x509.Certificate, error) {
	roots := x509.NewCertPool()
	roots.AddCert(v.ca)
	inter := x509.NewCertPool()
	for _, c := range intermediates {
		inter.AddCert(c)
	}
	chains, err := delegation.Verifiable(leaf).Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, err
	}
	return chains[0], nil
}

func (v *delegChainVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (v *delegChainVerifier) VerifyIssuer(_ context.Context, _ string, chain []*x509.Certificate) error {
	if len(chain) < 2 || !chain[1].Equal(v.ca) {
		return errDelegIssuerRefused
	}
	return nil
}

var errDelegIssuerRefused = errors.New("issuer is not the tenant's login CA")

// delegTenantVerifier adds the tenant binding: its one CA is tenant's login
// CA and no other tenant's.
type delegTenantVerifier struct {
	delegChainVerifier
	tenant string
}

func (v *delegTenantVerifier) VerifyTenantIssuer(_ context.Context, tenantID string, issuer *x509.Certificate) error {
	if tenantID != v.tenant || issuer == nil || !issuer.Equal(v.ca) {
		return errDelegIssuerRefused
	}
	return nil
}

var (
	_ IssuerVerifier       = (*delegChainVerifier)(nil)
	_ TenantIssuerVerifier = (*delegTenantVerifier)(nil)
)

// delegRevocation answers every revocation lookup with revoked and err.
type delegRevocation struct {
	revoked bool
	err     error
}

func (r delegRevocation) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) {
	return r.revoked, r.err
}

// delegAuthenticator is an authenticator that accepts delegations for
// delegPrincipal, verifying against ca as delegTenant's login CA, with
// nothing revoked. opts follow the defaults, so a test overrides one.
func delegAuthenticator(ca *x509.Certificate, opts ...MTLSOption) *MTLSAuthenticator {
	base := []MTLSOption{
		WithIssuerVerifier(&delegTenantVerifier{delegChainVerifier: delegChainVerifier{ca: ca}, tenant: delegTenant}),
		WithRevocationChecker(delegRevocation{}),
		WithDelegatedPrincipal(delegPrincipal),
	}
	return NewMTLSAuthenticatorWithTrustDomain(delegTrustDomain, append(base, opts...)...)
}

func delegSPIFFE(t *testing.T, trustDomain, tenant, kind, name string) *url.URL {
	t.Helper()
	u, err := spiffe.ID{TrustDomain: trustDomain, TenantID: tenant, Kind: kind, Name: name}.URI()
	if err != nil {
		t.Fatalf("SPIFFE URI: %v", err)
	}
	return u
}

// requireDelegRefused asserts err is a *DelegationRefusedError whose reason
// contains reason, and that it classifies as an unusable certificate.
func requireDelegRefused(t *testing.T, err error, reason string) *DelegationRefusedError {
	t.Helper()
	var refused *DelegationRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %T (%v), want *DelegationRefusedError", err, err)
	}
	if !strings.Contains(refused.Reason, reason) {
		t.Fatalf("Reason = %q, want it to contain %q", refused.Reason, reason)
	}
	if !errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("errors.Is(err, ErrInvalidCertificate) = false for %v", err)
	}
	return refused
}

func TestVerifyDelegation_AdmitsServicePrincipal(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})

	id, err := delegAuthenticator(ca).VerifyDelegation(context.Background(), leaf.Raw)
	if err != nil {
		t.Fatalf("VerifyDelegation: %v", err)
	}
	if id.Subject != ServicePrincipalSubjectPrefix+delegPrincipal {
		t.Errorf("Subject = %q, want %q", id.Subject, ServicePrincipalSubjectPrefix+delegPrincipal)
	}
	if id.TenantID != delegTenant {
		t.Errorf("TenantID = %q, want %q", id.TenantID, delegTenant)
	}
	for key, want := range map[string]string{
		ServicePrincipalMetadataKey: delegPrincipal,
		DelegationSerialMetadataKey: "d1",
		"tenant_source":             "ou",
	} {
		if got := id.Metadata[key]; got != want {
			t.Errorf("Metadata[%q] = %q, want %q", key, got, want)
		}
	}

	d, ok := DelegationOf(id)
	if !ok {
		t.Fatal("DelegationOf: no delegation on an admitted delegation identity")
	}
	if d.Principal != delegPrincipal || d.Serial != "d1" || !d.NotAfter.Equal(leaf.NotAfter) {
		t.Errorf("Delegation = %+v, want principal %q serial d1 NotAfter %v", d, delegPrincipal, leaf.NotAfter)
	}
	if strings.Join(d.Scope, ",") != "kv:read,kv:write" {
		t.Errorf("Scope = %v, want the grant's scope", d.Scope)
	}
	if !leaf.Equal(&x509.Certificate{Raw: d.Certificate}) {
		t.Error("Certificate is not the delegation certificate's DER")
	}
}

func TestVerifyDelegation_SPIFFESourcedTenant(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{
		ou:   []string{},
		uris: []*url.URL{delegSPIFFE(t, delegTrustDomain, delegTenant, spiffe.KindService, delegPrincipal)},
	})

	id, err := delegAuthenticator(ca).VerifyDelegation(context.Background(), leaf.Raw)
	if err != nil {
		t.Fatalf("VerifyDelegation: %v", err)
	}
	if id.TenantID != delegTenant || id.Metadata["tenant_source"] != "spiffe" {
		t.Errorf("TenantID = %q, tenant_source = %q; want %q from spiffe", id.TenantID, id.Metadata["tenant_source"], delegTenant)
	}
}

func TestDelegationOf_ReturnsACopy(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})
	id, err := delegAuthenticator(ca).VerifyDelegation(context.Background(), leaf.Raw)
	if err != nil {
		t.Fatalf("VerifyDelegation: %v", err)
	}

	first, _ := DelegationOf(id)
	first.Scope[0] = "kv:delete"
	first.Certificate[0] ^= 0xff

	second, _ := DelegationOf(id)
	if second.Scope[0] != "kv:read" {
		t.Errorf("Scope[0] = %q after mutating an earlier copy, want kv:read", second.Scope[0])
	}
	if second.Certificate[0] != leaf.Raw[0] {
		t.Error("Certificate changed after mutating an earlier copy")
	}
}

func TestDelegationOf_NoDelegation(t *testing.T) {
	for name, id := range map[string]*Identity{
		"nil identity":   nil,
		"user login":     {Subject: "alice", TenantID: delegTenant},
		"empty identity": {},
	} {
		if d, ok := DelegationOf(id); ok {
			t.Errorf("%s: DelegationOf = %+v, true; want false", name, d)
		}
	}
}

func TestVerifyDelegation_RefusesUnusableCertificate(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	plain := delegLeaf(t, ca, caKey, delegLeafSpec{noExt: true})

	ext, err := delegGrant().Extension()
	if err != nil {
		t.Fatalf("grant extension: %v", err)
	}
	ext.Critical = false
	nonCritical := delegLeaf(t, ca, caKey, delegLeafSpec{ext: &ext})

	otherCA, otherKey := testCA(t, "other-login-ca")
	foreign := delegLeaf(t, otherCA, otherKey, delegLeafSpec{})

	cases := []struct {
		name   string
		der    []byte
		auth   *MTLSAuthenticator
		reason string
		cause  any
	}{
		{"not a certificate", []byte("not a certificate"), delegAuthenticator(ca), "does not parse", nil},
		{"no delegation extension", plain.Raw, delegAuthenticator(ca), "carries no delegation", nil},
		{"extension not critical", nonCritical.Raw, delegAuthenticator(ca), "malformed delegation", new(*delegation.ExtensionError)},
		{"no trust pool", foreign.Raw, NewMTLSAuthenticator(WithDelegatedPrincipal(delegPrincipal)), "no trust pool", nil},
		{"signed by another CA", foreign.Raw, delegAuthenticator(ca), "does not verify", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := tc.auth.VerifyDelegation(context.Background(), tc.der)
			if id != nil {
				t.Fatalf("identity = %+v, want nil", id)
			}
			refused := requireDelegRefused(t, err, tc.reason)
			if tc.cause != nil && !errors.As(err, tc.cause) {
				t.Errorf("cause = %v, want %T", refused.Err, tc.cause)
			}
		})
	}
}

func TestVerifyDelegation_RefusesEachAdmissionCheck(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	now := time.Now().Add(-time.Minute)
	systemGrant := delegGrant()
	systemGrant.Tenant = tenantid.System

	cases := []struct {
		name   string
		spec   delegLeafSpec
		opts   []MTLSOption
		reason string
	}{
		{"node accepts no delegations", delegLeafSpec{}, []MTLSOption{WithDelegatedPrincipal("")}, "names principal billing"},
		{"another principal", delegLeafSpec{}, []MTLSOption{WithDelegatedPrincipal("payroll")}, "names principal billing"},
		{"lifetime over the maximum", delegLeafSpec{notBefore: now, notAfter: now.Add(delegation.MaxLifetime + time.Hour)}, nil, "lifetime"},
		{"no revocation checker", delegLeafSpec{}, []MTLSOption{WithRevocationChecker(nil)}, "no revocation checker"},
		{"OU names another tenant", delegLeafSpec{ou: []string{"globex"}}, nil, "does not name the delegated tenant"},
		{"no OU and no SPIFFE SAN", delegLeafSpec{ou: []string{}}, nil, "does not name the delegated tenant"},
		{"SPIFFE SAN names another service", delegLeafSpec{
			uris: []*url.URL{delegSPIFFE(t, delegTrustDomain, delegTenant, spiffe.KindService, "payroll")},
		}, nil, "SPIFFE SAN does not name"},
		{"SPIFFE SAN names a user", delegLeafSpec{
			uris: []*url.URL{delegSPIFFE(t, delegTrustDomain, delegTenant, spiffe.KindUser, delegPrincipal)},
		}, nil, "SPIFFE SAN does not name"},
		{"system tenant", delegLeafSpec{grant: systemGrant}, nil, "system tenant cannot delegate"},
		{"pool cannot bind issuer to tenant", delegLeafSpec{}, []MTLSOption{WithIssuerVerifier(&delegChainVerifier{ca: ca})}, "issuer cannot be bound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := delegLeaf(t, ca, caKey, tc.spec)
			id, err := delegAuthenticator(ca, tc.opts...).VerifyDelegation(context.Background(), leaf.Raw)
			if id != nil {
				t.Fatalf("identity = %+v, want nil", id)
			}
			requireDelegRefused(t, err, tc.reason)
		})
	}
}

// TestVerifyDelegation_ForeignPrincipalIsTyped proves a delegation naming a
// principal the node does not accept is refused with
// ErrForeignDelegationPrincipal as its cause, whether the node accepts
// another principal or none, so a caller can tell it from every other
// refusal without reading the reason text.
func TestVerifyDelegation_ForeignPrincipalIsTyped(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})
	for _, accepted := range []string{"payroll", ""} {
		_, err := delegAuthenticator(ca, WithDelegatedPrincipal(accepted)).VerifyDelegation(context.Background(), leaf.Raw)
		if !errors.Is(err, ErrForeignDelegationPrincipal) {
			t.Fatalf("accepting %q: err = %v, want ErrForeignDelegationPrincipal", accepted, err)
		}
		requireDelegRefused(t, err, "names principal billing")
	}

	// The principal the node accepts is never refused as foreign.
	if _, err := delegAuthenticator(ca).VerifyDelegation(context.Background(), leaf.Raw); errors.Is(err, ErrForeignDelegationPrincipal) {
		t.Fatalf("accepted principal refused as foreign: %v", err)
	}
}

func TestVerifyDelegation_RefusesForeignTenantIssuer(t *testing.T) {
	// The chain verifies (the CA is in the pool) but the pool binds that CA
	// to another tenant: the issuer is not acme's own login CA.
	ca, caKey := testCA(t, "globex-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})
	verifier := &delegTenantVerifier{delegChainVerifier: delegChainVerifier{ca: ca}, tenant: "globex"}

	_, err := delegAuthenticator(ca, WithIssuerVerifier(verifier)).VerifyDelegation(context.Background(), leaf.Raw)
	refused := requireDelegRefused(t, err, `issuer is not tenant "acme"'s login CA`)
	if !errors.Is(err, errDelegIssuerRefused) {
		t.Errorf("Err = %v, want the verifier's refusal wrapped", refused.Err)
	}
}

func TestVerifyDelegation_Revocation(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})

	t.Run("revoked", func(t *testing.T) {
		_, err := delegAuthenticator(ca, WithRevocationChecker(delegRevocation{revoked: true})).
			VerifyDelegation(context.Background(), leaf.Raw)
		if !errors.Is(err, ErrCertificateRevoked) {
			t.Fatalf("err = %v, want ErrCertificateRevoked", err)
		}
	})
	t.Run("state unreadable fails closed", func(t *testing.T) {
		lookup := errors.New("revocation store unavailable")
		_, err := delegAuthenticator(ca, WithRevocationChecker(delegRevocation{err: lookup})).
			VerifyDelegation(context.Background(), leaf.Raw)
		var rce *RevocationCheckError
		if !errors.As(err, &rce) || !errors.Is(err, lookup) {
			t.Fatalf("err = %T (%v), want *RevocationCheckError wrapping the lookup failure", err, err)
		}
	})
}

func TestVerifyDelegation_SPIFFEInForeignTrustDomain(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{
		uris: []*url.URL{delegSPIFFE(t, "other.example.com", delegTenant, spiffe.KindService, delegPrincipal)},
	})

	_, err := delegAuthenticator(ca).VerifyDelegation(context.Background(), leaf.Raw)
	var cte *CertificateTenantError
	if !errors.As(err, &cte) || !errors.Is(err, spiffe.ErrTrustDomainMismatch) {
		t.Fatalf("err = %T (%v), want *CertificateTenantError wrapping ErrTrustDomainMismatch", err, err)
	}
}

func TestAuthenticate_DelegationCertificate(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})
	ctx := WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}})

	id, err := delegAuthenticator(ca).Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if _, ok := DelegationOf(id); !ok {
		t.Fatal("Authenticate read a delegation certificate as something other than a delegation")
	}
	if id.Subject != ServicePrincipalSubjectPrefix+delegPrincipal || id.TenantID != delegTenant {
		t.Errorf("identity = %q in %q, want the service principal in %q", id.Subject, id.TenantID, delegTenant)
	}
}

func TestAuthenticate_MalformedDelegationNeverALogin(t *testing.T) {
	// A delegation extension that is not critical would be ignored by a
	// verifier that does not know it; the authenticator refuses it rather
	// than reading the certificate as the user login it otherwise looks like.
	ca, caKey := testCA(t, "acme-login-ca")
	ext, err := delegGrant().Extension()
	if err != nil {
		t.Fatalf("grant extension: %v", err)
	}
	ext.Critical = false
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{ext: &ext})
	ctx := WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}})

	id, err := delegAuthenticator(ca).Authenticate(ctx)
	if id != nil {
		t.Fatalf("identity = %+v, want nil", id)
	}
	requireDelegRefused(t, err, "malformed delegation")
	var extErr *delegation.ExtensionError
	if !errors.As(err, &extErr) {
		t.Errorf("err = %v, want it to wrap *delegation.ExtensionError", err)
	}
}

func TestDelegationRefusedError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("x509: certificate signed by unknown authority")
	cases := []struct {
		name    string
		err     *DelegationRefusedError
		wantMsg string
		wantLen int
	}{
		{"without cause", &DelegationRefusedError{Reason: "no revocation checker"},
			ErrInvalidCertificate.Error() + ": delegation refused: no revocation checker", 1},
		{"with cause", &DelegationRefusedError{Reason: "certificate does not verify", Err: cause},
			ErrInvalidCertificate.Error() + ": delegation refused: certificate does not verify: " + cause.Error(), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tc.wantMsg)
			}
			if got := tc.err.Unwrap(); len(got) != tc.wantLen || got[0] != ErrInvalidCertificate {
				t.Errorf("Unwrap() = %v, want ErrInvalidCertificate first, %d errors", got, tc.wantLen)
			}
			if !errors.Is(tc.err, ErrInvalidCertificate) {
				t.Error("errors.Is(err, ErrInvalidCertificate) = false")
			}
			if tc.err.Err != nil && !errors.Is(tc.err, cause) {
				t.Error("errors.Is(err, cause) = false")
			}
		})
	}
}
