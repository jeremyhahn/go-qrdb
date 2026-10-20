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
// trust domain -- and carry no SAN the CSR supplied: its one SAN is the
// holder's own tenant SPIFFE ID, agreeing with its OU.

type hostileCSR struct {
	name   string
	ou     []string
	spiffe string
}

var hostileCSRs = []hostileCSR{
	{name: "system admin", ou: []string{"system"}},
	{name: "foreign tenant", ou: []string{"globex"}},
	{name: "foreign SPIFFE", ou: []string{"globex"}, spiffe: "spiffe://globex/user/admin"},
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
		"pinned trust domain": auth.NewMTLSAuthenticatorWithTrustDomain(testTrustDomain),
	} {
		id, err := authn.Authenticate(auth.WithTLSState(context.Background(), state))
		require.NoError(t, err, name)
		require.Equal(t, username, id.Subject, "%s: the certificate authenticates as the CSR's CN, not the registered holder", name)
		require.Equal(t, tenantID, id.TenantID, "%s: the certificate authenticates outside the registering scope's tenant", name)
	}

	require.Equal(t, []string{"spiffe://" + tenantID + "/user/" + username}, uriStrings(leaf),
		"the certificate's only URI SAN must be the holder's agreeing tenant SPIFFE ID, never one from the CSR")
	require.Empty(t, leaf.DNSNames, "a DNS SAN from the CSR was signed")
	require.Empty(t, leaf.IPAddresses, "an IP SAN from the CSR was signed")
	require.Empty(t, leaf.EmailAddresses, "an e-mail SAN from the CSR was signed")
}

// hostileRequest is a holder registration of username whose CSR is the
// hostile case c, enrolled by an administrator.
func hostileRequest(t *testing.T, c hostileCSR, username string, role seal.HolderRole) RegisterOfficerRequest {
	t.Helper()
	return RegisterOfficerRequest{Username: username, Role: role, CSRPEM: newHostileCSRPEM(t, c), Enroller: "enrolling-admin"}
}

// newTenantHolders is the tenant-barrier holder authority for tenantID over
// ca.
func newTenantHolders(t *testing.T, tenantID string, ca CeremonyCA) *ScopedCeremonyService {
	t.Helper()
	svc, err := NewTenantCeremonyService(tenantID, seal.NewMemoryBackend(), HolderDeps{TrustDomain: testTrustDomain, CA: ca, Factors: testFactors(t)}, testLogger())
	require.NoError(t, err)
	return svc
}

// newSystemHolders is the system ceremony service over a Shamir barrier,
// signing with ca.
func newSystemHolders(t *testing.T, ca *LocalCA) *holderEnv {
	t.Helper()
	e := newHolderEnv(t, 2, 2)
	e.svc = nil
	e.ca = ca
	e.svc = e.newService(e.barrier)
	return e
}

func TestHolderCert_TenantHolderRegistration(t *testing.T) {
	for _, c := range hostileCSRs {
		for _, role := range []seal.HolderRole{seal.RoleSecurityOfficer, seal.RoleCustodian} {
			t.Run(c.name+"/"+string(role), func(t *testing.T) {
				b := newHolderCATestbed(t)
				res, err := newTenantHolders(t, "acme", b.ca).RegisterOfficer(context.Background(), hostileRequest(t, c, "acme-officer", role))
				require.NoError(t, err)
				b.assertAuthenticatesAs(t, res.CertPEM, "acme-officer", "acme")
			})
		}
	}
}

func TestHolderCert_DatabaseHolderRegistration(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			b := newHolderCATestbed(t)
			svc, err := NewDatabaseCeremonyService("acme", "vault", seal.NewMemoryBackend(), HolderDeps{TrustDomain: testTrustDomain, CA: b.ca, Factors: testFactors(t)}, testLogger())
			require.NoError(t, err)
			res, err := svc.RegisterOfficer(context.Background(), hostileRequest(t, c, "vault-officer", seal.RoleSecurityOfficer))
			require.NoError(t, err)
			b.assertAuthenticatesAs(t, res.CertPEM, "vault-officer", "acme")
		})
	}
}

func TestHolderCert_SystemHolderRegistration(t *testing.T) {
	for _, c := range hostileCSRs {
		for _, domain := range []seal.QuorumDomain{seal.DomainUnseal, seal.DomainRecovery} {
			t.Run(c.name+"/"+string(domain), func(t *testing.T) {
				b := newHolderCATestbed(t)
				e := newSystemHolders(t, b.ca)
				res, err := e.svc.RegisterOfficer(context.Background(), domain, hostileRequest(t, c, "so-1", seal.RoleSecurityOfficer))
				require.NoError(t, err)
				b.assertAuthenticatesAs(t, res.CertPEM, "so-1", "system")
			})
		}
	}
}

func TestHolderCert_SignCSRInit(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			b := newHolderCATestbed(t)
			// go-qrdb#548: sign-csr is HOLDER-only (so, custodian) and
			// PreOperationalOnly, so this must land -- and stay -- in
			// StateEnrolling rather than the single-admin initOpts(0) that
			// used to drive it straight to StateOperational.
			opts := enrollingInitOpts(t)
			svc := newTestCeremonyService(t, defaultConfig(), shamirBarrier(t, seal.NewMemoryBackend(), 2, 3))
			_, err := svc.Initialize(context.Background(), b.ca, opts)
			require.NoError(t, err)

			res, err := svc.SignCSRInit(context.Background(), "custodian-1", opts.SOPin, newHostileCSRPEM(t, c), "custodian")
			require.NoError(t, err)
			b.assertAuthenticatesAs(t, res.CertPEM, "custodian-1", "system")
		})
	}
}
