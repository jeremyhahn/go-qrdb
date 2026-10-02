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
	bundle, err := p.GenerateClientCert(context.Background(), &GenerateRequest{Username: username, TenantID: tenantID})
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
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.CACertPEM()) {
		t.Fatal("CA PEM did not load into a pool")
	}
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
	want := "spiffe://" + sanTestDomain + "/tenant/" + sanTestTenant + "/user/" + sanTestSubject
	if got := leaf.URIs[0].String(); got != want {
		t.Fatalf("SAN = %q, want %q", got, want)
	}
	// The OU is still written, so a node with no pin resolves the same tenant.
	if ou := leaf.Subject.OrganizationalUnit; len(ou) != 1 || ou[0] != sanTestTenant {
		t.Fatalf("OU = %v, want [%q]", ou, sanTestTenant)
	}

	pinned := auth.NewMTLSAuthenticatorWithTrustDomain(sanTestDomain)
	tenantID, fromSPIFFE, err := pinned.TenantFromCert(leaf)
	if err != nil || !fromSPIFFE || tenantID != sanTestTenant {
		t.Fatalf("TenantFromCert = (%q, %v, %v), want (%q, true, nil)", tenantID, fromSPIFFE, err, sanTestTenant)
	}

	id, err := authenticate(t, pinned, verifiedState(t, p, leaf))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != sanTestSubject || id.TenantID != sanTestTenant || id.Metadata["tenant_source"] != "spiffe" {
		t.Fatalf("identity = {Subject:%q TenantID:%q source:%q}, want {%q %q spiffe}",
			id.Subject, id.TenantID, id.Metadata["tenant_source"], sanTestSubject, sanTestTenant)
	}
}

func TestGenerateClientCert_TrustDomain_ForeignPinRefusesFailClosed(t *testing.T) {
	p, _, _ := initializeProvider(t, WithTrustDomain(sanTestDomain))
	leaf := issueParsed(t, p, sanTestTenant, sanTestSubject)
	state := verifiedState(t, p, leaf)

	foreign := auth.NewMTLSAuthenticatorWithTrustDomain("other.example.com")
	id, err := authenticate(t, foreign, state)
	if !errors.Is(err, auth.ErrInvalidCertificate) {
		t.Fatalf("Authenticate under a foreign pin = (%v, %v), want ErrInvalidCertificate", id, err)
	}
	// And it does not fall back to the OU, which names the right tenant: the
	// control fires on the SAN it cannot accept rather than routing around it.
	if _, _, terr := foreign.TenantFromCert(leaf); !errors.Is(terr, auth.ErrInvalidCertificate) {
		t.Fatalf("TenantFromCert under a foreign pin err = %v, want ErrInvalidCertificate", terr)
	}
}

func TestGenerateClientCert_NoTrustDomain_EmitsNoSANAndResolvesThroughOU(t *testing.T) {
	for name, opts := range map[string][]ProviderOption{
		"no option":          nil,
		"empty trust domain": {WithTrustDomain("")},
	} {
		t.Run(name, func(t *testing.T) {
			p, _, _ := initializeProvider(t, opts...)
			leaf := issueParsed(t, p, sanTestTenant, sanTestSubject)

			if len(leaf.URIs) != 0 || len(leaf.DNSNames) != 0 {
				t.Fatalf("SANs = URIs %v DNS %v, want none", leaf.URIs, leaf.DNSNames)
			}
			// No SubjectAltName extension at all: the certificate is shaped
			// exactly as it was before the option existed.
			for _, ext := range leaf.Extensions {
				if ext.Id.Equal(oidSubjectAltName) {
					t.Fatal("certificate carries a SubjectAltName extension with no trust domain configured")
				}
			}

			for _, a := range []*auth.MTLSAuthenticator{
				auth.NewMTLSAuthenticator(),
				auth.NewMTLSAuthenticatorWithTrustDomain(sanTestDomain),
			} {
				id, err := authenticate(t, a, verifiedState(t, p, leaf))
				if err != nil {
					t.Fatalf("Authenticate: %v", err)
				}
				if id.TenantID != sanTestTenant || id.Metadata["tenant_source"] != "ou" {
					t.Fatalf("identity tenant = %q via %q, want %q via ou", id.TenantID, id.Metadata["tenant_source"], sanTestTenant)
				}
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
			bundle, err := p.GenerateClientCert(context.Background(), &GenerateRequest{Username: tc.username, TenantID: tc.tenant})
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

// TestGenerateClientCert_TrustDomain_SameInputsWithoutDomainStillMint shows
// the refusal is caused by the SAN, not by the request: the identical request
// mints under a provider with no trust domain, whose certificate carries only
// the OU. (A separator in the OU is a legacy-path concern this issue does not
// change.)
func TestGenerateClientCert_TrustDomain_SameInputsWithoutDomainStillMint(t *testing.T) {
	p, _, _ := initializeProvider(t)
	leaf := issueParsed(t, p, "acme/evil", sanTestSubject)
	if len(leaf.URIs) != 0 {
		t.Fatalf("URIs = %v, want none", leaf.URIs)
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
	san, err := url.Parse(spiffe.ID{TrustDomain: sanTestDomain, TenantID: sanTestTenant, Kind: spiffe.KindUser, Name: sanTestSubject}.String())
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
	if err != nil || id.Metadata["tenant_source"] != "spiffe" {
		t.Fatalf("re-issued certificate = (%+v, %v), want a SPIFFE-derived identity", id, err)
	}
}
