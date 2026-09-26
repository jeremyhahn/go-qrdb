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

// SignCSR copies the CSR's URI SANs into the certificate it signs, so a
// tenant SPIFFE SAN the requester chose would become a tenant claim this CA
// vouches for (go-qrdb#519 review). One that disagrees with the Subject OU is
// refused; everything else is signed as before.

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

var signOpts = &SignCSROptions{ValidityDays: 30, KeyUsage: x509.KeyUsageDigitalSignature}

func TestLocalCA_SignCSR_RefusesTenantSANThatDisagreesWithOU(t *testing.T) {
	cases := map[string]struct {
		ou   []string
		uris []string
	}{
		"different tenant":        {[]string{"acme"}, []string{"spiffe://db.example.com/tenant/other/user/alice"}},
		"tenant SAN, no OU":       {nil, []string{"spiffe://db.example.com/tenant/acme/user/alice"}},
		"second SAN disagrees":    {[]string{"acme"}, []string{"spiffe://db.example.com/tenant/acme/user/alice", "spiffe://db.example.com/tenant/other/user/alice"}},
		"malformed tenant SPIFFE": {[]string{"acme"}, []string{"spiffe://db.example.com/tenant/acme/bogus-kind/alice"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cert, err := newTestLocalCA(t).SignCSR(context.Background(), csrWithURIs(t, tc.ou, tc.uris...), signOpts)
			var sanErr *CSRTenantSANError
			require.True(t, errors.As(err, &sanErr), "SignCSR = (%v, %v), want *CSRTenantSANError", cert, err)
			assert.Nil(t, cert)
			assert.Contains(t, sanErr.Error(), "refusing to sign")
		})
	}
}

func TestLocalCA_SignCSR_KeepsAgreeingAndNonTenantURIs(t *testing.T) {
	uris := []string{
		"spiffe://db.example.com/tenant/acme/user/alice",
		"spiffe://db.example.com/node/127.0.0.1:7000",
		"https://example.com/profile",
	}
	cert, err := newTestLocalCA(t).SignCSR(context.Background(), csrWithURIs(t, []string{"acme"}, uris...), signOpts)
	require.NoError(t, err)
	got := make([]string, 0, len(cert.URIs))
	for _, u := range cert.URIs {
		got = append(got, u.String())
	}
	assert.Equal(t, uris, got)
}

func TestCheckTenantSANsAgreeWithOU_NoURIs(t *testing.T) {
	assert.NoError(t, checkTenantSANsAgreeWithOU(nil, nil))
	assert.NoError(t, checkTenantSANsAgreeWithOU(nil, []string{"acme"}))
}
