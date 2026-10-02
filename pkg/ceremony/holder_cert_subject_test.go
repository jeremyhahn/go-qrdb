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

package ceremony

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// The officer and share-holder certificates this package signs chain to the
// node's cluster CA, which the mTLS listener trusts for client logins. So the
// identity such a certificate authenticates as is decided HERE, not by the
// CSR: every signing path names the holder and the scope's tenant, and the CA
// ignores the CSR's Subject and SANs entirely.
//
// Each case below is a CSR a caller entitled to register an officer could
// submit: claiming the system tenant's "admin", another tenant, or another
// tenant through a SPIFFE SAN, plus DNS, IP and e-mail SANs. Whatever the CSR
// says, the certificate must authenticate as the registered holder, in the
// registering scope's tenant -- under both the OU rule and a pinned SPIFFE
// trust domain -- and carry no SAN the CSR supplied.

const holderTrustDomain = "qrdb.example"

type hostileCSR struct {
	name   string
	ou     []string
	spiffe string
}

var hostileCSRs = []hostileCSR{
	{name: "system admin", ou: []string{"system"}},
	{name: "foreign tenant", ou: []string{"globex"}},
	{name: "foreign SPIFFE", ou: []string{"globex"}, spiffe: "spiffe://" + holderTrustDomain + "/tenant/globex/user/admin"},
	{name: "no OU", ou: nil},
}

// newHostileCSRPEM builds a CSR claiming CN=admin with the case's OU and SANs.
func newHostileCSRPEM(t *testing.T, c hostileCSR) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.CertificateRequest{
		Subject:        pkix.Name{CommonName: "admin", OrganizationalUnit: c.ou},
		DNSNames:       []string{"evil.example"},
		IPAddresses:    []net.IP{net.ParseIP("192.0.2.66")},
		EmailAddresses: []string{"admin@evil.example"},
	}
	if c.spiffe != "" {
		u, err := url.Parse(c.spiffe)
		require.NoError(t, err)
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// holderCATestbed is a real LocalCA plus a pool that trusts it, as the mTLS
// listener does.
type holderCATestbed struct {
	ca    *LocalCA
	roots *x509.CertPool
}

func newHolderCATestbed(t *testing.T) *holderCATestbed {
	t.Helper()
	ca := newTestLocalCA(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca.caCert)
	return &holderCATestbed{ca: ca, roots: roots}
}

// assertAuthenticatesAs verifies certPEM against the CA and requires every
// mTLS authenticator configuration to read it as username in tenantID, with
// no SAN carried over from the CSR.
func (b *holderCATestbed) assertAuthenticatesAs(t *testing.T, certPEM []byte, username, tenantID string) {
	t.Helper()
	blk, _ := pem.Decode(certPEM)
	require.NotNil(t, blk, "issued certificate is not PEM")
	leaf, err := x509.ParseCertificate(blk.Bytes)
	require.NoError(t, err)

	chains, err := leaf.Verify(x509.VerifyOptions{Roots: b.roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err)
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: chains}
	for name, authn := range map[string]*auth.MTLSAuthenticator{
		"OU rule":             auth.NewMTLSAuthenticator(),
		"pinned trust domain": auth.NewMTLSAuthenticatorWithTrustDomain(holderTrustDomain),
	} {
		id, err := authn.Authenticate(auth.WithTLSState(context.Background(), state))
		require.NoError(t, err, name)
		require.Equal(t, username, id.Subject, "%s: the certificate authenticates as the CSR's CN, not the registered holder", name)
		require.Equal(t, tenantID, id.TenantID, "%s: the certificate authenticates outside the registering scope's tenant", name)
	}

	require.Empty(t, leaf.URIs, "a URI SAN from the CSR was signed")
	require.Empty(t, leaf.DNSNames, "a DNS SAN from the CSR was signed")
	require.Empty(t, leaf.IPAddresses, "an IP SAN from the CSR was signed")
	require.Empty(t, leaf.EmailAddresses, "an e-mail SAN from the CSR was signed")
}

func TestHolderCert_TenantOfficerRegistration(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			b := newHolderCATestbed(t)
			svc, err := NewTenantCeremonyService("acme", seal.NewMemoryBackend(), b.ca, testLogger())
			require.NoError(t, err)
			res, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
				Username: "acme-officer", CSRPEM: newHostileCSRPEM(t, c), AuthenticatorType: AuthenticatorSoftware,
			})
			require.NoError(t, err)
			b.assertAuthenticatesAs(t, res.CertPEM, "acme-officer", "acme")
		})
	}
}

// newSystemCeremonyWithCA is a system CeremonyService signing with the real
// LocalCA, ready for officer registration.
func newSystemCeremonyWithCA(t *testing.T, ca CeremonyCA) *CeremonyService {
	t.Helper()
	svc, err := NewCeremonyService(defaultConfig(), unsealedTestBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)
	svc.mu.Lock()
	svc.ca = ca
	require.NoError(t, svc.ensureOfficerRegistry())
	svc.mu.Unlock()
	return svc
}

func TestHolderCert_SystemOfficerRegistration(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			b := newHolderCATestbed(t)
			svc := newSystemCeremonyWithCA(t, b.ca)
			res, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
				Username: "so-1", CSRPEM: newHostileCSRPEM(t, c), AuthenticatorType: AuthenticatorSoftware,
			})
			require.NoError(t, err)
			b.assertAuthenticatesAs(t, res.CertPEM, "so-1", "system")
		})
	}
}

func TestHolderCert_RecoveryOfficerRegistration(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			b := newHolderCATestbed(t)
			svc := newSystemCeremonyWithCA(t, b.ca)
			res, err := svc.RegisterRecoveryOfficer(context.Background(), RegisterOfficerRequest{
				Username: "recovery-1", CSRPEM: newHostileCSRPEM(t, c), AuthenticatorType: AuthenticatorSoftware,
			})
			require.NoError(t, err)
			b.assertAuthenticatesAs(t, res.CertPEM, "recovery-1", "system")
		})
	}
}

func TestHolderCert_SignCSRInit(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			b := newHolderCATestbed(t)
			cfg := defaultConfig()
			svc, err := NewCeremonyService(cfg, unsealedTestBarrier(t), newTestCredentialService(), testLogger())
			require.NoError(t, err)
			_, err = svc.Initialize(context.Background(), b.ca, nil)
			require.NoError(t, err)

			res, err := svc.SignCSRInit(context.Background(), "operator-1", cfg.SOPin, newHostileCSRPEM(t, c), "operator")
			require.NoError(t, err)
			b.assertAuthenticatesAs(t, res.CertPEM, "operator-1", "system")
		})
	}
}

func TestHolderCert_InitialCeremonyOfficers(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			b := newHolderCATestbed(t)
			cfg, _ := mofnConfig(t, 2, 2)
			for i := range cfg.Officers {
				cfg.Officers[i].CSRPEM = newHostileCSRPEM(t, c)
			}
			svc, err := NewCeremonyService(cfg, unsealedTestBarrier(t), newTestCredentialService(), testLogger())
			require.NoError(t, err)
			_, err = svc.Initialize(context.Background(), b.ca, nil)
			require.NoError(t, err)

			for _, officer := range cfg.Officers {
				pending, ok := svc.pendingCerts[officer.Username]
				require.True(t, ok, "no certificate issued for %s", officer.Username)
				b.assertAuthenticatesAs(t, pending.CertPEM, officer.Username, "system")
			}
		})
	}
}
