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
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

// TestSignHolderCSR_RefusesBeforeAndAfterSigning: a CSR that is not PEM, not
// a CSR or not self-signed is refused before the CA is asked; a CA failure is
// a *CertEncodeError.
func TestSignHolderCSR_RefusesBeforeAndAfterSigning(t *testing.T) {
	ctx := context.Background()
	ca := newTestLocalCA(t)
	opts := &SignCSROptions{TenantID: "acme", Username: "carol", ValidityDays: 1, TrustDomain: testTrustDomain}
	var parseErr *CSRParseError
	_, _, err := signHolderCSR(ctx, ca, []byte("not pem"), opts)
	assert.True(t, errors.As(err, &parseErr), err)
	_, _, err = signHolderCSR(ctx, ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte{1, 2, 3}}), opts)
	assert.True(t, errors.As(err, &parseErr), err)

	good := newTestCSRPEM(t, "carol")
	blk, _ := pem.Decode(good)
	tampered := append([]byte(nil), blk.Bytes...)
	tampered[len(tampered)-1] ^= 0xff
	_, _, err = signHolderCSR(ctx, ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: tampered}), opts)
	assert.True(t, errors.As(err, &parseErr), "a CSR whose self-signature does not verify: %v", err)

	var encErr *CertEncodeError
	_, _, err = signHolderCSR(ctx, failingCA{}, good, opts)
	assert.True(t, errors.As(err, &encErr), err)

	// No trust domain: the SAN cannot be named, so the request is refused
	// before the CA is asked -- failingCA would have answered *CertEncodeError.
	var subjErr *CertSubjectError
	noDomain := *opts
	noDomain.TrustDomain = ""
	_, _, err = signHolderCSR(ctx, failingCA{}, good, &noDomain)
	require.True(t, errors.As(err, &subjErr), err)
	assert.Equal(t, "SubjectAltName", subjErr.Field)
	assert.ErrorIs(t, err, spiffe.ErrTrustDomainEmpty)
	assert.False(t, errors.As(err, &encErr), "the CA was asked to sign an identity the SAN cannot name")

	cert, csr, err := signHolderCSR(ctx, ca, good, opts)
	require.NoError(t, err)
	assert.Equal(t, "carol", cert.Subject.CommonName)
	assert.NotNil(t, csr)
}

// TestCheckIssuedSubject_EachField: a certificate is refused for the first
// field that is not what the options named.
func TestCheckIssuedSubject_EachField(t *testing.T) {
	ca := newTestLocalCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(der)
	require.NoError(t, err)
	opts := &SignCSROptions{TenantID: "acme", Username: "carol", Profile: "custodian", TrustDomain: testTrustDomain}

	spiffeURI := func(raw string) []*url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return []*url.URL{u}
	}
	agreeing := spiffeURI("spiffe://acme/user/carol")
	issue := func(subject pkix.Name, uris []*url.URL, ips []net.IP, pub any) *x509.Certificate {
		t.Helper()
		serial, err := randomSerial()
		require.NoError(t, err)
		tmpl := &x509.Certificate{SerialNumber: serial, Subject: subject, URIs: uris, IPAddresses: ips, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
		out, err := x509.CreateCertificate(rand.Reader, tmpl, ca.caCert, pub, ca.caKey)
		require.NoError(t, err)
		cert, err := x509.ParseCertificate(out)
		require.NoError(t, err)
		return cert
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	right := pkix.Name{CommonName: "carol", OrganizationalUnit: []string{"acme", "custodian"}}
	cases := map[string]struct {
		cert *x509.Certificate
		want string
	}{
		"common name":          {issue(pkix.Name{CommonName: "admin", OrganizationalUnit: []string{"acme", "custodian"}}, agreeing, nil, &key.PublicKey), "CommonName"},
		"tenant OU":            {issue(pkix.Name{CommonName: "carol", OrganizationalUnit: []string{"system", "custodian"}}, agreeing, nil, &key.PublicKey), "OrganizationalUnit"},
		"SAN":                  {issue(right, agreeing, []net.IP{net.ParseIP("192.0.2.1")}, &key.PublicKey), "SubjectAltName"},
		"no SPIFFE ID":         {issue(right, nil, nil, &key.PublicKey), "SubjectAltName"},
		"foreign tenant ID":    {issue(right, spiffeURI("spiffe://globex/user/carol"), nil, &key.PublicKey), "SubjectAltName"},
		"foreign trust domain": {issue(right, spiffeURI("spiffe://other.example/user/carol"), nil, &key.PublicKey), "SubjectAltName"},
		"extra URI SAN":        {issue(right, append(spiffeURI("spiffe://acme/user/carol"), agreeing...), nil, &key.PublicKey), "SubjectAltName"},
		"public key":           {issue(right, agreeing, nil, &other.PublicKey), "PublicKey"},
	}
	for name, tc := range cases {
		var subjErr *CertSubjectError
		err := checkIssuedSubject(tc.cert, csr, opts)
		if assert.True(t, errors.As(err, &subjErr), "%s: %v", name, err) {
			assert.Equal(t, tc.want, subjErr.Field, name)
			assert.NotEmpty(t, subjErr.Error())
		}
	}
	assert.NoError(t, checkIssuedSubject(issue(right, agreeing, nil, &key.PublicKey), csr, opts))
	var missing *CertSubjectError
	assert.True(t, errors.As(checkIssuedSubject(issue(right, agreeing, nil, &key.PublicKey), csr, &SignCSROptions{Username: "carol", TrustDomain: testTrustDomain}), &missing))
	noDomain := *opts
	noDomain.TrustDomain = ""
	assert.ErrorIs(t, checkIssuedSubject(issue(right, agreeing, nil, &key.PublicKey), csr, &noDomain), spiffe.ErrTrustDomainEmpty,
		"without a trust domain no certificate's SAN can be the agreeing one")
}

// TestHolderErrors_Messages: every holder error names its subject.
func TestHolderErrors_Messages(t *testing.T) {
	for _, err := range []error{
		&QuorumRateLimitedError{RetryAfter: time.Second},
		&StagedDistributionMissingError{Distribution: "d2"},
		&HolderRecordConflictError{Username: "carol"},
		&UnsupportedHolderKeyError{Algorithm: "RSA"},
	} {
		assert.NotEmpty(t, err.Error(), "%T", err)
	}
}

// TestHolderScope_DecoyTableIsBounded: storing a decoy drops expired ones and
// ones for another roster, and evicts the oldest at maxPendingDecoys.
func TestHolderScope_DecoyTableIsBounded(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	h := e.svc.system
	digest := []byte("roster")
	now := time.Now()
	decoy := func(id string, issued time.Time, expires time.Time, d []byte) *pendingQuorumChallenge {
		return &pendingQuorumChallenge{digest: d, issued: issued, decoy: true,
			challenge: &authfactors.Challenge{ChallengeID: id, ExpiresAt: expires}}
	}
	h.decoys = map[string]*pendingQuorumChallenge{}
	h.decoys["expired"] = decoy("expired", now, now.Add(-time.Second), digest)
	h.decoys["stale-roster"] = decoy("stale-roster", now, now.Add(time.Hour), []byte("old"))
	h.storeDecoy(decoy("fresh", now, now.Add(time.Hour), digest), digest)
	assert.NotContains(t, h.decoys, "expired")
	assert.NotContains(t, h.decoys, "stale-roster")
	assert.Contains(t, h.decoys, "fresh")

	h.decoys = map[string]*pendingQuorumChallenge{}
	for i := 0; i < maxPendingDecoys; i++ {
		id := "d" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		h.decoys[id] = decoy(id, now.Add(time.Duration(i)*time.Millisecond), now.Add(time.Hour), digest)
	}
	h.storeDecoy(decoy("newest", now.Add(time.Hour), now.Add(time.Hour), digest), digest)
	assert.Len(t, h.decoys, maxPendingDecoys)
	assert.NotContains(t, h.decoys, "dAa", "the oldest decoy is evicted first")
	assert.Contains(t, h.decoys, "newest")
}

// TestDecoyFactor_FallsBackToMTLS: a registered factor that derives decoys
// issues its own; one that does not, or an unknown name, gets mtls's.
func TestDecoyFactor_FallsBackToMTLS(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	deps := e.svc.HolderDeps()
	f, enroller := decoyFactor(&deps, testFactorName)
	assert.Equal(t, testFactorName, f.Name())
	assert.NotNil(t, enroller)
	f, _ = decoyFactor(&deps, "no-such-factor")
	assert.Equal(t, mtls.Name, f.Name())
}
