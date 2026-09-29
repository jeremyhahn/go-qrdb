// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// testCeremonyCA is an in-memory CA that implements ceremony.CeremonyCA
// using a self-signed ECDSA certificate for unit testing.
type testCeremonyCA struct {
	caKey  *ecdsa.PrivateKey
	caCert *x509.Certificate
}

// newTestCeremonyCA creates a self-signed ECDSA P-256 CA certificate and
// returns a testCeremonyCA suitable for ceremony tests.
func newTestCeremonyCA(t *testing.T) *testCeremonyCA {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	caCert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)

	return &testCeremonyCA{caKey: caKey, caCert: caCert}
}

// CACertificate returns the CA certificate.
func (ca *testCeremonyCA) CACertificate() (*x509.Certificate, error) {
	return ca.caCert, nil
}

// CABundle returns the PEM-encoded CA certificate chain.
func (ca *testCeremonyCA) CABundle() ([]byte, error) {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ca.caCert.Raw,
	}), nil
}

// IssueCertificate generates a new key pair and issues a certificate
// signed by the test CA.
func (ca *testCeremonyCA) IssueCertificate(_ context.Context, req *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: req.CommonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(req.ValidityDays) * 24 * time.Hour),
		KeyUsage:     req.KeyUsage,
		ExtKeyUsage:  req.ExtKeyUsage,
		DNSNames:     req.SANs,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return &ceremony.IssuedCert{
		Certificate: cert,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
	}, nil
}

// SignCSR signs a PEM-encoded CSR with the test CA.
func (ca *testCeremonyCA) SignCSR(_ context.Context, csrPEM []byte, opts *ceremony.SignCSROptions) (*x509.Certificate, error) {
	// Faithful to the CeremonyCA contract: the caller assigns the identity
	// and the CSR contributes only its public key.
	if opts == nil || opts.Subject.CommonName == "" {
		return nil, ceremony.ErrCertIdentityRequired
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, ceremony.ErrInvalidCSR
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, err
	}

	rawSubject, err := opts.RawSubject()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		RawSubject:   rawSubject,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(opts.ValidityDays) * 24 * time.Hour),
		KeyUsage:     opts.KeyUsage,
		ExtKeyUsage:  opts.ExtKeyUsage,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, csr.PublicKey, ca.caKey)
	if err != nil {
		return nil, err
	}

	return x509.ParseCertificate(certDER)
}

// newTestOfficerCSR generates a valid PEM-encoded ECDSA P-256 CSR for the
// given common name, suitable for CeremonySignCSR tests.
func newTestOfficerCSR(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName},
	}, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}
