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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestCAPEM generates a self-signed ECDSA P-256 CA and returns its
// certificate PEM plus the private key in both SEC1 and PKCS#8 PEM forms.
func newTestCAPEM(t *testing.T) (certPEM, sec1KeyPEM, pkcs8KeyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qrdb-test-ca", Organization: []string{"go-quicraft"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		MaxPathLenZero:        true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	sec1DER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	sec1KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1DER})

	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pkcs8KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8DER})

	return certPEM, sec1KeyPEM, pkcs8KeyPEM
}

// newTestCSRPEM generates an ECDSA P-256 key pair and a PEM-encoded CSR with
// the given common name.
func newTestCSRPEM(t *testing.T, cn string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: cn},
		DNSNames: []string{"so.example.test"},
	}, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

// newTestLocalCA returns a LocalCA built from a fresh test CA (SEC1 key form).
func newTestLocalCA(t *testing.T) *LocalCA {
	t.Helper()
	certPEM, sec1KeyPEM, _ := newTestCAPEM(t)
	ca, err := NewLocalCA(certPEM, sec1KeyPEM)
	require.NoError(t, err)
	return ca
}

// ---------------------------------------------------------------------------
// NewLocalCA
// ---------------------------------------------------------------------------

func TestNewLocalCA_SEC1Key(t *testing.T) {
	certPEM, sec1KeyPEM, _ := newTestCAPEM(t)

	ca, err := NewLocalCA(certPEM, sec1KeyPEM)
	require.NoError(t, err)
	require.NotNil(t, ca)

	cert, err := ca.CACertificate(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "qrdb-test-ca", cert.Subject.CommonName)
}

func TestNewLocalCA_PKCS8Key(t *testing.T) {
	certPEM, _, pkcs8KeyPEM := newTestCAPEM(t)

	ca, err := NewLocalCA(certPEM, pkcs8KeyPEM)
	require.NoError(t, err)
	require.NotNil(t, ca)
}

func TestNewLocalCA_EmptyInputs(t *testing.T) {
	certPEM, sec1KeyPEM, _ := newTestCAPEM(t)

	tests := []struct {
		name string
		cert []byte
		key  []byte
	}{
		{"empty cert", nil, sec1KeyPEM},
		{"empty key", certPEM, nil},
		{"both empty", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ca, err := NewLocalCA(tc.cert, tc.key)
			assert.Nil(t, ca)
			assert.ErrorIs(t, err, ErrNilCA)
		})
	}
}

func TestNewLocalCA_BadCertPEM(t *testing.T) {
	_, sec1KeyPEM, _ := newTestCAPEM(t)

	tests := []struct {
		name string
		cert []byte
	}{
		{"no PEM block", []byte("not pem at all")},
		{"bad DER", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ca, err := NewLocalCA(tc.cert, sec1KeyPEM)
			assert.Nil(t, ca)
			var parseErr *CACertParseError
			assert.True(t, errors.As(err, &parseErr), "expected *CACertParseError, got %T: %v", err, err)
		})
	}
}

func TestNewLocalCA_BadKeyPEM(t *testing.T) {
	certPEM, _, _ := newTestCAPEM(t)

	// An X25519 PKCS#8 key, as a static DER fixture rather than a generated
	// one: crypto/ecdh refuses to generate an X25519 key under
	// GODEBUG=fips140=only, and this test is about parseCAPrivateKeyPEM's
	// rejection, not about X25519. Either way NewLocalCA must return a
	// *CAKeyParseError -- outside FIPS mode because *ecdh.PrivateKey is not a
	// crypto.Signer, in FIPS mode because the parse itself is refused.
	//
	// SEQUENCE { INTEGER 0, SEQUENCE { OID 1.3.101.110 (id-X25519) },
	//            OCTET STRING { OCTET STRING (32-byte scalar) } }
	x25519DER := append([]byte{
		0x30, 0x2e,
		0x02, 0x01, 0x00,
		0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e,
		0x04, 0x22, 0x04, 0x20,
	}, make([]byte, 32)...)
	for i := 16; i < len(x25519DER); i++ {
		x25519DER[i] = byte(i - 15)
	}
	x25519PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x25519DER})

	tests := []struct {
		name string
		key  []byte
	}{
		{"no PEM block", []byte("not pem at all")},
		{"bad DER", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("garbage")})},
		{"non-signer key", x25519PEM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ca, err := NewLocalCA(certPEM, tc.key)
			assert.Nil(t, ca)
			var parseErr *CAKeyParseError
			assert.True(t, errors.As(err, &parseErr), "expected *CAKeyParseError, got %T: %v", err, err)
		})
	}
}

// ---------------------------------------------------------------------------
// SignCSR
// ---------------------------------------------------------------------------

// TestLocalCA_SignCSR_AssignsTheIdentity pins the §6 fix: the certificate
// carries the subject the caller assigns, CN=Username, OU=[TenantID,
// Profile], and nothing the CSR asked for -- not its common name, not its
// OUs, not its SANs.
func TestLocalCA_SignCSR_AssignsTheIdentity(t *testing.T) {
	ca := newTestLocalCA(t)
	csrPEM := newTestCSRPEM(t, "so-1")

	cert, err := ca.SignCSR(context.Background(), csrPEM, &SignCSROptions{
		TenantID:     "acme",
		Username:     "so-1",
		Profile:      "custodian",
		ValidityDays: 365,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		TrustDomain:  testTrustDomain,
	})
	require.NoError(t, err)
	require.NotNil(t, cert)

	assert.Equal(t, "so-1", cert.Subject.CommonName)
	assert.Equal(t, []string{"acme", "custodian"}, cert.Subject.OrganizationalUnit)
	assert.Empty(t, cert.DNSNames, "the CSR's DNS SAN must not be signed")
	assert.Equal(t, []string{"spiffe://acme/user/so-1"}, uriStrings(cert),
		"the one SAN is the agreeing tenant SPIFFE ID")
	assert.Equal(t, x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment, cert.KeyUsage)
	assert.Contains(t, cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 365), cert.NotAfter, time.Hour)

	// The signed certificate must chain to the CA — the property the
	// holder claim flow depends on for mTLS client authentication.
	caCert, err := ca.CACertificate(t.Context())
	require.NoError(t, err)
	assert.NoError(t, cert.CheckSignatureFrom(caCert))
}

func TestLocalCA_SignCSR_EmptyProfileIsTenantOnly(t *testing.T) {
	ca := newTestLocalCA(t)
	csrPEM := newTestCSRPEM(t, "so-2")

	cert, err := ca.SignCSR(context.Background(), csrPEM, &SignCSROptions{
		TenantID:     "acme",
		Username:     "so-2",
		ValidityDays: 30,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		TrustDomain:  testTrustDomain,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"acme"}, cert.Subject.OrganizationalUnit)
}

func TestLocalCA_SignCSR_InvalidInputs(t *testing.T) {
	ca := newTestLocalCA(t)
	validOpts := &SignCSROptions{TenantID: "acme", Username: "so", ValidityDays: 1, TrustDomain: testTrustDomain}

	// CSR whose embedded signature has been corrupted: re-encode a valid
	// CSR DER with flipped bytes near the end (the signature field).
	tamperedDER := func() []byte {
		block, _ := pem.Decode(newTestCSRPEM(t, "so-3"))
		require.NotNil(t, block)
		der := make([]byte, len(block.Bytes))
		copy(der, block.Bytes)
		der[len(der)-5] ^= 0xFF
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	}()

	tests := []struct {
		name string
		csr  []byte
		opts *SignCSROptions
	}{
		{"nil opts", newTestCSRPEM(t, "so-4"), nil},
		{"no PEM block", []byte("not pem"), validOpts},
		{"bad DER", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("garbage")}), validOpts},
		{"tampered signature", tamperedDER, validOpts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cert, err := ca.SignCSR(context.Background(), tc.csr, tc.opts)
			assert.Nil(t, cert)
			var parseErr *CSRParseError
			assert.True(t, errors.As(err, &parseErr), "expected *CSRParseError, got %T: %v", err, err)
		})
	}
}

// ---------------------------------------------------------------------------
// CACertificate / CABundle
// ---------------------------------------------------------------------------

func TestLocalCA_CACertificate(t *testing.T) {
	certPEM, sec1KeyPEM, _ := newTestCAPEM(t)
	ca, err := NewLocalCA(certPEM, sec1KeyPEM)
	require.NoError(t, err)

	cert, err := ca.CACertificate(t.Context())
	require.NoError(t, err)
	assert.True(t, cert.IsCA)
	assert.Equal(t, "qrdb-test-ca", cert.Subject.CommonName)
}

func TestLocalCA_CABundle(t *testing.T) {
	certPEM, sec1KeyPEM, _ := newTestCAPEM(t)
	ca, err := NewLocalCA(certPEM, sec1KeyPEM)
	require.NoError(t, err)

	bundle, err := ca.CABundle()
	require.NoError(t, err)
	assert.Equal(t, certPEM, bundle)
}

// ---------------------------------------------------------------------------
// IssueCertificate
// ---------------------------------------------------------------------------

func TestLocalCA_IssueCertificate(t *testing.T) {
	ca := newTestLocalCA(t)
	key := newDeviceSigner(t)

	issued, err := ca.IssueCertificate(context.Background(), &IssueCertRequest{
		PublicKey:    key.Public(),
		CommonName:   "node-1",
		SANs:         []string{"node1.example.test", "127.0.0.1"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ValidityDays: 30,
	})
	require.NoError(t, err)
	require.NotNil(t, issued)

	assert.Equal(t, "node-1", issued.Certificate.Subject.CommonName)
	assert.Equal(t, []string{"node1.example.test"}, issued.Certificate.DNSNames)
	require.Len(t, issued.Certificate.IPAddresses, 1)
	assert.True(t, issued.Certificate.IPAddresses[0].Equal([]byte{127, 0, 0, 1}))

	// The leaf must chain to the CA.
	caCert, err := ca.CACertificate(t.Context())
	require.NoError(t, err)
	assert.NoError(t, issued.Certificate.CheckSignatureFrom(caCert))

	// The certificate binds the caller's key, and CertPEM is that certificate.
	assert.True(t, key.Public().(interface{ Equal(crypto.PublicKey) bool }).Equal(issued.Certificate.PublicKey))
	certBlock, _ := pem.Decode(issued.CertPEM)
	require.NotNil(t, certBlock)
	assert.Equal(t, issued.Certificate.Raw, certBlock.Bytes)
}

func TestLocalCA_IssueCertificate_NilRequest(t *testing.T) {
	ca := newTestLocalCA(t)

	issued, err := ca.IssueCertificate(context.Background(), nil)
	assert.Nil(t, issued)
	var encErr *CertEncodeError
	assert.True(t, errors.As(err, &encErr), "expected *CertEncodeError, got %T: %v", err, err)
}

// The CA never generates a key to stand in for a missing one.
func TestLocalCA_IssueCertificate_NoPublicKey(t *testing.T) {
	ca := newTestLocalCA(t)

	issued, err := ca.IssueCertificate(context.Background(), &IssueCertRequest{CommonName: "node-1", ValidityDays: 1})
	assert.Nil(t, issued)
	var keyErr *IssueCertKeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, "PublicKey", keyErr.Field)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestSplitSANs(t *testing.T) {
	tests := []struct {
		name    string
		sans    []string
		wantDNS []string
		wantIPs int
	}{
		{"mixed", []string{"a.example", "10.0.0.1", "b.example", "::1"}, []string{"a.example", "b.example"}, 2},
		{"only dns", []string{"a.example"}, []string{"a.example"}, 0},
		{"only ips", []string{"192.168.1.1"}, nil, 1},
		{"empty", nil, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dns, ips := splitSANs(tc.sans)
			assert.Equal(t, tc.wantDNS, dns)
			assert.Len(t, ips, tc.wantIPs)
		})
	}
}

func TestRandomSerial(t *testing.T) {
	a, err := randomSerial()
	require.NoError(t, err)
	b, err := randomSerial()
	require.NoError(t, err)

	assert.NotEqual(t, 0, a.Sign())
	// Two random 128-bit serials colliding would indicate a broken RNG.
	assert.NotEqual(t, a, b)
	assert.LessOrEqual(t, a.BitLen(), localCASerialBits)
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestCACertParseError(t *testing.T) {
	inner := errors.New("boom")
	err := &CACertParseError{Err: inner}
	assert.Equal(t, "ceremony: CA certificate parse failed: boom", err.Error())
	assert.ErrorIs(t, err, inner)
}

func TestCAKeyParseError(t *testing.T) {
	inner := errors.New("boom")
	err := &CAKeyParseError{Err: inner}
	assert.Equal(t, "ceremony: CA key parse failed: boom", err.Error())
	assert.ErrorIs(t, err, inner)
}

// TestLocalCA_SignCSR_DropsRequestedURISANs pins that a URI SAN the CSR asks
// for never reaches the certificate: a tenant-scoped SPIFFE ID the requester
// chose would otherwise be a tenant claim this CA vouches for (go-qrdb#519).
// The certificate's one SAN is the agreeing tenant SPIFFE ID the options name.
func TestLocalCA_SignCSR_DropsRequestedURISANs(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	requested, err := url.Parse("spiffe://other/user/alice")
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "alice", OrganizationalUnit: []string{"acme"}},
		URIs:    []*url.URL{requested},
	}, key)
	require.NoError(t, err)
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})

	cert, err := newTestLocalCA(t).SignCSR(context.Background(), csrPEM, &SignCSROptions{
		TenantID:     "acme",
		Username:     "alice",
		ValidityDays: 30,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		TrustDomain:  testTrustDomain,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"spiffe://acme/user/alice"}, uriStrings(cert),
		"the CSR's URI SAN is dropped: the one SAN is the options' agreeing tenant SPIFFE ID")
}
