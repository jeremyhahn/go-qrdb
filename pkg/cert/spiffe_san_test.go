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

package cert

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// These tests close go-qrdb#519: the CA this product ships must mint
// certificates that reach the authenticator's pinned-trust-domain derivation,
// and they prove it against the REAL authenticator over a REAL verified chain,
// not against a parser in isolation. A test that only inspected the SAN would
// pass against a SAN the authenticator ignores.

const (
	sanTestDomain  = "db.example.com"
	sanTestTenant  = "acme"
	sanTestSubject = "alice"
)

// oidSubjectAltName is the X.509 SubjectAltName extension (RFC 5280 §4.2.1.6).
var oidSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}

// issueParsed generates a client certificate and returns it parsed.
func issueParsed(t *testing.T, p *DefaultCertProvider, tenantID, username string) *x509.Certificate {
	t.Helper()
	bundle, err := generateForTest(context.Background(), p, &testRequest{Username: username, TenantID: tenantID})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	return parsePEMCert(t, bundle.CertPEM)
}

func parsePEMCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("certificate PEM did not decode")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return c
}

// verifiedState builds the TLS connection state a server would hold after
// verifying leaf against the provider's CA: the chain comes from x509.Verify,
// exactly as crypto/tls produces it, so the authenticator reads a chain it
// would really be handed.
func verifiedState(t *testing.T, p *DefaultCertProvider, leaf *x509.Certificate) *tls.ConnectionState {
	t.Helper()
	tenantID := tenantid.System
	if len(leaf.Subject.OrganizationalUnit) > 0 && leaf.Subject.OrganizationalUnit[0] != "" {
		tenantID = leaf.Subject.OrganizationalUnit[0]
	}
	_, caCert, err := p.getCAMaterial(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("getCAMaterial(%s): %v", tenantID, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatalf("leaf does not verify against its own CA: %v", err)
	}
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: chains}
}

func authenticate(t *testing.T, a *auth.MTLSAuthenticator, state *tls.ConnectionState) (*auth.Identity, error) {
	t.Helper()
	return a.Authenticate(auth.WithTLSState(context.Background(), state))
}

func TestGenerateClientCert_TrustDomain_EmitsTenantSANThePinnedAuthenticatorResolves(t *testing.T) {
	p, _, _ := initializeProvider(t, WithTrustDomain(sanTestDomain))
	leaf := issueParsed(t, p, sanTestTenant, sanTestSubject)

	if len(leaf.URIs) != 1 {
		t.Fatalf("URIs = %v, want exactly one tenant SPIFFE SAN", leaf.URIs)
	}
	want := "spiffe://" + sanTestTenant + "/user/" + sanTestSubject
	if got := leaf.URIs[0].String(); got != want {
		t.Fatalf("SAN = %q, want %q", got, want)
	}
	// The OU is written too, agreeing with the SAN, as the authenticator requires.
	if ou := leaf.Subject.OrganizationalUnit; len(ou) != 1 || ou[0] != sanTestTenant {
		t.Fatalf("OU = %v, want [%q]", ou, sanTestTenant)
	}

	pinned := auth.NewMTLSAuthenticatorWithTrustDomain(sanTestDomain)
	tenantID, err := pinned.TenantFromCert(leaf)
	if err != nil || tenantID != sanTestTenant {
		t.Fatalf("TenantFromCert = (%q, %v), want (%q, nil)", tenantID, err, sanTestTenant)
	}

	id, err := authenticate(t, pinned, verifiedState(t, p, leaf))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != sanTestSubject || id.TenantID != sanTestTenant {
		t.Fatalf("identity = {Subject:%q TenantID:%q}, want {%q %q}",
			id.Subject, id.TenantID, sanTestSubject, sanTestTenant)
	}
}

func TestGenerateClientCert_TrustDomain_TenantNamingTheClusterRefusesFailClosed(t *testing.T) {
	p, _, _ := initializeProvider(t, WithTrustDomain(sanTestDomain))
	leaf := issueParsed(t, p, sanTestTenant, sanTestSubject)
	state := verifiedState(t, p, leaf)

	// An authenticator whose cluster trust domain is the tenant's own name
	// cannot tell that tenant apart from the cluster, so it refuses the leaf.
	clash := auth.NewMTLSAuthenticatorWithTrustDomain(sanTestTenant)
	id, err := authenticate(t, clash, state)
	if !errors.Is(err, auth.ErrInvalidCertificate) {
		t.Fatalf("Authenticate under a clashing pin = (%v, %v), want ErrInvalidCertificate", id, err)
	}
	// And it does not fall back to the OU, which names the same tenant: the
	// control fires on the SAN it cannot accept rather than routing around it.
	if _, terr := clash.TenantFromCert(leaf); !errors.Is(terr, auth.ErrTenantIsClusterTrustDomain) {
		t.Fatalf("TenantFromCert under a clashing pin err = %v, want ErrTenantIsClusterTrustDomain", terr)
	}
}

// TestGenerateClientCert_NoTrustDomain_IsRefusedBeforeSigning pins that a
// provider with no trust domain mints nothing: the certificate would carry
// only the OU, and the authenticator, which requires an agreeing SPIFFE ID
// SAN beside it, would refuse it on every node, pinned or not. The refusal
// names the identity, unwraps to spiffe.ErrTrustDomainEmpty, and leaves no
// record behind.
func TestGenerateClientCert_NoTrustDomain_IsRefusedBeforeSigning(t *testing.T) {
	for name, opts := range map[string][]ProviderOption{
		"no option":          nil,
		"empty trust domain": {WithTrustDomain("")},
	} {
		t.Run(name, func(t *testing.T) {
			kv := newMockKVStore()
			p := NewDefaultProvider(kv, newMockBarrier(), opts...)
			if err := p.Initialize(context.Background()); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			bundle, err := generateForTest(context.Background(), p, &testRequest{Username: sanTestSubject, TenantID: sanTestTenant})
			var sidErr *SPIFFEIdentityError
			if !errors.As(err, &sidErr) || !errors.Is(err, spiffe.ErrTrustDomainEmpty) {
				t.Fatalf("GenerateClientCert = (%v, %v), want *SPIFFEIdentityError wrapping spiffe.ErrTrustDomainEmpty", bundle, err)
			}
			if sidErr.TenantID != sanTestTenant || sidErr.Username != sanTestSubject || sidErr.TrustDomain != "" {
				t.Fatalf("error names %+v, want the refused identity", sidErr)
			}
			keys, _ := kv.List(context.Background(), prefixCerts)
			if len(keys) != 0 {
				t.Fatalf("a refused certificate left records behind: %v", keys)
			}
		})
	}
}

// TestGenerateClientCert_TrustDomain_RefusesSANThatWouldDisagreeWithOU pins
// the #519 refusal: when the SAN cannot carry exactly the tenant and name the
// Subject carries, no certificate is minted — not signed, not persisted.
func TestGenerateClientCert_TrustDomain_RefusesSANThatWouldDisagreeWithOU(t *testing.T) {
	cases := []struct {
		name, domain, tenant, username string
	}{
		{"separator in tenant", sanTestDomain, "acme/evil", sanTestSubject},
		{"separator in username", sanTestDomain, sanTestTenant, "alice/svc/x"},
		{"trust domain is not a bare host", "db.example.com/tenant/other", sanTestTenant, sanTestSubject},
		{"whitespace-only tenant", sanTestDomain, "   ", sanTestSubject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, kv, _ := initializeProvider(t, WithTrustDomain(tc.domain))
			bundle, err := generateForTest(context.Background(), p, &testRequest{Username: tc.username, TenantID: tc.tenant})
			var sidErr *SPIFFEIdentityError
			if !errors.As(err, &sidErr) {
				t.Fatalf("GenerateClientCert = (%v, %v), want *SPIFFEIdentityError", bundle, err)
			}
			if sidErr.TenantID != tc.tenant || sidErr.Username != tc.username || sidErr.TrustDomain != tc.domain {
				t.Fatalf("error names %+v, want the refused identity", sidErr)
			}
			if !strings.Contains(sidErr.Error(), "refusing to mint") {
				t.Fatalf("error text %q does not say what was refused", sidErr.Error())
			}
			keys, _ := kv.List(context.Background(), prefixCerts)
			if len(keys) != 0 {
				t.Fatalf("a refused certificate left records behind: %v", keys)
			}
		})
	}
}

// TestGenerateClientCert_TrustDomain_ReissueWithSANKeepsSPKIBinding confirms,
// rather than assumes, #519's rotation claim: an account enrolled on a key
// whose certificate had no SAN keeps authenticating when the same key is
// re-issued with the SAN added, because the binding is over the
// SubjectPublicKeyInfo and the SAN is not part of it.
func TestGenerateClientCert_TrustDomain_ReissueWithSANKeepsSPKIBinding(t *testing.T) {
	p, _, _ := initializeProvider(t)
	original := issueParsed(t, p, sanTestTenant, sanTestSubject)

	caSigner, caCert, err := p.getCAMaterial(context.Background(), sanTestTenant)
	if err != nil {
		t.Fatalf("getCAMaterial: %v", err)
	}
	san, err := url.Parse(spiffe.ID{TrustDomain: sanTestTenant, Kind: spiffe.KindUser, Name: sanTestSubject}.String())
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	serial, err := generateSerial()
	if err != nil {
		t.Fatalf("generateSerial: %v", err)
	}
	now := time.Now().UTC()
	reissuedDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sanTestSubject, OrganizationalUnit: []string{sanTestTenant}},
		NotBefore:    now,
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{san},
	}, caCert, original.PublicKey, caSigner)
	if err != nil {
		t.Fatalf("re-issue: %v", err)
	}
	reissued, err := x509.ParseCertificate(reissuedDER)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	enrolled := &user.User{Username: sanTestSubject, TenantID: sanTestTenant}
	enrolled.AddCertBinding(user.CertBinding{Thumbprint: user.ComputeSPKIThumbprint(original), Kind: user.CertBindingSPKIThumbprint})

	if !enrolled.MatchesCert(original) {
		t.Fatal("the enrolled certificate does not match its own binding")
	}
	if !enrolled.MatchesCert(reissued) {
		t.Fatal("re-issuing the enrolled key with a SPIFFE SAN broke the SPKI binding")
	}
	// And the re-issued certificate now resolves through the pin.
	id, err := authenticate(t, auth.NewMTLSAuthenticatorWithTrustDomain(sanTestDomain), verifiedState(t, p, reissued))
	if err != nil || id.TenantID != sanTestTenant {
		t.Fatalf("re-issued certificate = (%+v, %v), want an identity in tenant %q", id, err, sanTestTenant)
	}
}
