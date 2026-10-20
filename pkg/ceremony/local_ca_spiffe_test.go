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
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LocalCA.SignCSR takes the certificate's identity from SignCSROptions and
// nothing from the CSR but its key. A URI SAN the requester chose -- a tenant
// SPIFFE ID above all -- would be a tenant claim this CA vouches for, so none
// is ever carried over (go-qrdb#519). The one SAN the certificate carries is
// the tenant SPIFFE ID the options name, agreeing with its OU (go-qrdb#777).

func csrWithURIs(t *testing.T, ou []string, uris ...string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "alice", OrganizationalUnit: ou}}
	for _, raw := range uris {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		tmpl.URIs = append(tmpl.URIs, u)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func signOptsFor(tenantID, username string) *SignCSROptions {
	return &SignCSROptions{TenantID: tenantID, Username: username, ValidityDays: 30, KeyUsage: x509.KeyUsageDigitalSignature, TrustDomain: testTrustDomain}
}

func TestLocalCA_SignCSR_DropsEveryURISAN(t *testing.T) {
	uris := []string{
		"spiffe://other/user/alice",
		"spiffe://acme/user/alice",
		"spiffe://" + testTrustDomain + "/node/127.0.0.1:7000",
		"https://example.com/profile",
	}
	cert, err := newTestLocalCA(t).SignCSR(context.Background(), csrWithURIs(t, []string{"other"}, uris...), signOptsFor("acme", "bob"))
	require.NoError(t, err)
	assert.Equal(t, []string{"spiffe://acme/user/bob"}, uriStrings(cert),
		"every CSR URI SAN is dropped; the one SAN is the options' agreeing tenant SPIFFE ID")
	assert.Equal(t, "bob", cert.Subject.CommonName)
	assert.Equal(t, []string{"acme"}, cert.Subject.OrganizationalUnit)
}

func TestLocalCA_SignCSR_RequiresTheSubject(t *testing.T) {
	ca := newTestLocalCA(t)
	for name, opts := range map[string]*SignCSROptions{
		"no tenant":       signOptsFor("", "bob"),
		"no username":     signOptsFor("acme", ""),
		"no trust domain": {TenantID: "acme", Username: "bob", ValidityDays: 30, KeyUsage: x509.KeyUsageDigitalSignature},
	} {
		t.Run(name, func(t *testing.T) {
			cert, err := ca.SignCSR(context.Background(), csrWithURIs(t, nil), opts)
			var subjErr *CertSubjectError
			require.True(t, errors.As(err, &subjErr), "SignCSR = (%v, %v), want *CertSubjectError", cert, err)
			assert.Nil(t, cert)
		})
	}
}
