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

package authtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
)

// Delegation is a tenant's login CA, a delegation certificate it issued, and
// the mTLS authenticator of a node that accepts delegations for the grant's
// principal and trusts that CA as the tenant's own. Only the authenticator
// can produce a delegated identity (auth.DelegationOf), so a test of what a
// delegated caller may do starts here.
type Delegation struct {
	// CA is the tenant's login CA certificate.
	CA *x509.Certificate

	// Certificate is the delegation certificate, valid for
	// delegation.DefaultLifetime from a minute ago.
	Certificate *x509.Certificate

	// Authenticator verifies Certificate in full: chain, tenant issuer,
	// lifetime and principal. Revocation reports nothing revoked.
	Authenticator *auth.MTLSAuthenticator

	grant delegation.Grant
}

// NewDelegation mints a login CA for g.Tenant and a delegation certificate
// carrying g, signed by it.
func NewDelegation(t testing.TB, g delegation.Grant) *Delegation {
	t.Helper()
	ext, err := g.Extension()
	if err != nil {
		t.Fatalf("authtest: delegation extension: %v", err)
	}
	now := time.Now()
	caKey := generateKey(t)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: g.Tenant + "-login-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	ca := createCertificate(t, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	leafKey := generateKey(t)
	leaf := createCertificate(t, &x509.Certificate{
		SerialNumber:    big.NewInt(0xd1),
		Subject:         pkix.Name{CommonName: auth.ServicePrincipalSubjectPrefix + g.Principal, OrganizationalUnit: []string{g.Tenant}},
		NotBefore:       now.Add(-time.Minute),
		NotAfter:        now.Add(delegation.DefaultLifetime),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{ext},
	}, ca, &leafKey.PublicKey, caKey)
	d := &Delegation{CA: ca, Certificate: leaf, grant: g}
	d.Authenticator = d.NewAuthenticator()
	return d
}

// NewAuthenticator returns an mTLS authenticator that trusts the delegation's
// CA as its tenant's login CA, accepts delegations for its principal and
// finds nothing revoked, with opts applied last so a test can replace any of
// those.
func (d *Delegation) NewAuthenticator(opts ...auth.MTLSOption) *auth.MTLSAuthenticator {
	base := []auth.MTLSOption{
		auth.WithIssuerVerifier(&tenantLoginCA{tenantID: d.grant.Tenant, ca: d.CA}),
		auth.WithRevocationChecker(unrevoked{}),
		auth.WithDelegatedPrincipal(d.grant.Principal),
	}
	return auth.NewMTLSAuthenticator(append(base, opts...)...)
}

// Context returns ctx carrying the TLS state of a connection that presented
// the delegation certificate, with no chain built yet -- the state
// pkg/clienttrust.Pool.Wire leaves for the authenticator to verify.
func (d *Delegation) Context(ctx context.Context) context.Context {
	return auth.WithTLSState(ctx, &tls.ConnectionState{PeerCertificates: []*x509.Certificate{d.Certificate}})
}

// Identity authenticates the delegation certificate and returns the
// delegated identity.
func (d *Delegation) Identity(t testing.TB) *auth.Identity {
	t.Helper()
	id, err := d.Authenticator.Authenticate(d.Context(context.Background()))
	if err != nil {
		t.Fatalf("authtest: authenticate delegation: %v", err)
	}
	return id
}

// tenantLoginCA is the trust a node holds for one tenant's login CA: the
// chain verifies to it, and it is that tenant's own issuer.
type tenantLoginCA struct {
	tenantID string
	ca       *x509.Certificate
}

var (
	_ auth.IssuerVerifier       = (*tenantLoginCA)(nil)
	_ auth.TenantIssuerVerifier = (*tenantLoginCA)(nil)
)

func (c *tenantLoginCA) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, intermediates []*x509.Certificate) ([]*x509.Certificate, error) {
	roots := x509.NewCertPool()
	roots.AddCert(c.ca)
	inter := x509.NewCertPool()
	for _, ic := range intermediates {
		inter.AddCert(ic)
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

func (c *tenantLoginCA) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (c *tenantLoginCA) VerifyIssuer(_ context.Context, tenantID string, chain []*x509.Certificate) error {
	if len(chain) < 2 {
		return &IssuerRefusedError{TenantID: tenantID}
	}
	return c.VerifyTenantIssuer(context.Background(), tenantID, chain[1])
}

func (c *tenantLoginCA) VerifyTenantIssuer(_ context.Context, tenantID string, issuer *x509.Certificate) error {
	if tenantID != c.tenantID || issuer == nil || !issuer.Equal(c.ca) {
		return &IssuerRefusedError{TenantID: tenantID}
	}
	return nil
}

// IssuerRefusedError reports that a Delegation's trust refused an issuer for
// TenantID.
type IssuerRefusedError struct {
	TenantID string
}

func (e *IssuerRefusedError) Error() string {
	return "authtest: issuer is not tenant " + e.TenantID + "'s login CA"
}

// unrevoked is a revocation checker that finds nothing revoked.
type unrevoked struct{}

func (unrevoked) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) { return false, nil }

func generateKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("authtest: generate key: %v", err)
	}
	return key
}

func createCertificate(t testing.TB, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatalf("authtest: create certificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("authtest: parse certificate: %v", err)
	}
	return c
}
