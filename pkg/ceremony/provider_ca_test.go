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
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
)

// newTestProviderCA returns a ProviderCA over a cakey.StaticProvider wrapping
// a fresh test CA, mirroring newTestLocalCA for the provider-backed path.
func newTestProviderCA(t *testing.T) *ProviderCA {
	t.Helper()
	certPEM, sec1KeyPEM, _ := newTestCAPEM(t)

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	keyBlock, _ := pem.Decode(sec1KeyPEM)
	require.NotNil(t, keyBlock)
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	require.NoError(t, err)

	sp, err := cakey.NewStaticProvider(key, cert)
	require.NoError(t, err)

	ca, err := NewProviderCA(sp)
	require.NoError(t, err)
	return ca
}

func TestNewProviderCA_NilProviderRefused(t *testing.T) {
	_, err := NewProviderCA(nil)
	var nilErr *NilCeremonyCAProviderError
	if !errors.As(err, &nilErr) {
		t.Fatalf("NewProviderCA(nil) = %v, want *NilCeremonyCAProviderError", err)
	}
}

func TestProviderCA_SignCSR(t *testing.T) {
	ca := newTestProviderCA(t)
	csrPEM := newTestCSRPEM(t, "attacker-chosen-cn")

	cert, err := ca.SignCSR(context.Background(), csrPEM, &SignCSROptions{
		TenantID:     "system",
		Username:     "officer1",
		ValidityDays: 1,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err)
	require.Equal(t, "officer1", cert.Subject.CommonName)
	require.Equal(t, []string{"system"}, cert.Subject.OrganizationalUnit)
	require.Empty(t, cert.URIs, "ProviderCA must carry over no SAN from the CSR")
}

func TestProviderCA_SignCSR_InvalidOptions(t *testing.T) {
	ca := newTestProviderCA(t)
	csrPEM := newTestCSRPEM(t, "cn")
	_, err := ca.SignCSR(context.Background(), csrPEM, &SignCSROptions{Username: "officer1"})
	var subjErr *CertSubjectError
	if !errors.As(err, &subjErr) {
		t.Fatalf("SignCSR with no TenantID = %v, want *CertSubjectError", err)
	}
}

func TestProviderCA_CACertificateAndBundle(t *testing.T) {
	ca := newTestProviderCA(t)
	cert, err := ca.CACertificate(t.Context())
	require.NoError(t, err)
	require.True(t, cert.IsCA)

	bundle, err := ca.CABundle()
	require.NoError(t, err)
	block, _ := pem.Decode(bundle)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE", block.Type)
}

// certOnlyFakeProvider implements cakey.Provider AND cakey.CertOnlyProvider,
// recording which accessor was called -- it exists to prove
// ProviderCA.CACertificate prefers CertificateOnly (the accessor that can
// never generate a CA as a side effect) over Certificate on a provider that
// offers both, such as cakey.SoftwareProvider (go-qrdb#591's review, B5).
type certOnlyFakeProvider struct {
	cert              *x509.Certificate
	certificateCalled bool
	certOnlyCalled    bool
}

func (f *certOnlyFakeProvider) Signer(context.Context) (crypto.Signer, error) {
	return nil, errors.New("certOnlyFakeProvider: Signer not implemented")
}

func (f *certOnlyFakeProvider) Certificate(context.Context) (*x509.Certificate, error) {
	f.certificateCalled = true
	return f.cert, nil
}

// CertificateOnly implements cakey.CertOnlyProvider.
func (f *certOnlyFakeProvider) CertificateOnly(context.Context) (*x509.Certificate, error) {
	f.certOnlyCalled = true
	return f.cert, nil
}

// Compile-time interface checks.
var _ cakey.Provider = (*certOnlyFakeProvider)(nil)
var _ cakey.CertOnlyProvider = (*certOnlyFakeProvider)(nil)

// TestProviderCA_CACertificate_PrefersCertOnlyAccessor proves CACertificate
// calls CertificateOnly, never the auto-establishing Certificate, when the
// wrapped provider offers the cert-only accessor: a TLS handshake's trust
// path (pkg/clienttrust.Pool) must never generate a CA merely because one
// has not been established yet (go-qrdb#591's review, B5).
func TestProviderCA_CACertificate_PrefersCertOnlyAccessor(t *testing.T) {
	certPEM, _, _ := newTestCAPEM(t)
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	fake := &certOnlyFakeProvider{cert: cert}
	ca, err := NewProviderCA(fake)
	require.NoError(t, err)

	got, err := ca.CACertificate(t.Context())
	require.NoError(t, err)
	require.Same(t, cert, got)
	require.True(t, fake.certOnlyCalled, "CACertificate must call CertificateOnly when the provider offers it")
	require.False(t, fake.certificateCalled, "CACertificate must never call the auto-establishing Certificate when CertificateOnly is available")
}

// TestProviderCA_CACertificate_FallsBackWithoutCertOnlyAccessor proves
// CACertificate still resolves through the ordinary Certificate accessor
// for a provider that does not implement CertOnlyProvider (cakey.
// StaticProvider, which newTestProviderCA wraps) -- safe there because
// StaticProvider's own Certificate never generates anything either.
func TestProviderCA_CACertificate_FallsBackWithoutCertOnlyAccessor(t *testing.T) {
	ca := newTestProviderCA(t)
	cert, err := ca.CACertificate(t.Context())
	require.NoError(t, err)
	require.True(t, cert.IsCA)
}

func TestProviderCA_IssueCertificate(t *testing.T) {
	ca := newTestProviderCA(t)
	issued, err := ca.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName:   "node-1",
		SANs:         []string{"node1.example.test", "10.0.0.1"},
		ValidityDays: 1,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err)
	require.Equal(t, "node-1", issued.Certificate.Subject.CommonName)
	require.Contains(t, string(issued.CertPEM), "BEGIN CERTIFICATE")
	require.Contains(t, string(issued.KeyPEM), "BEGIN PRIVATE KEY")
}

func TestProviderCA_IssueCertificate_NilRequest(t *testing.T) {
	ca := newTestProviderCA(t)
	_, err := ca.IssueCertificate(context.Background(), nil)
	var encErr *CertEncodeError
	if !errors.As(err, &encErr) {
		t.Fatalf("IssueCertificate(nil) = %v, want *CertEncodeError", err)
	}
}

// failingProvider is a cakey.Provider double that fails Signer and/or
// Certificate on demand, for exercising ProviderCA's error-wrapping paths.
type failingProvider struct {
	signerErr error
	certErr   error
	cert      *x509.Certificate
	signer    crypto.Signer
}

func (f *failingProvider) Signer(context.Context) (crypto.Signer, error) {
	if f.signerErr != nil {
		return nil, f.signerErr
	}
	return f.signer, nil
}

func (f *failingProvider) Certificate(context.Context) (*x509.Certificate, error) {
	if f.certErr != nil {
		return nil, f.certErr
	}
	return f.cert, nil
}

func TestProviderCA_MaterialErrors(t *testing.T) {
	boom := errors.New("boom")

	t.Run("certificate fails", func(t *testing.T) {
		ca, err := NewProviderCA(&failingProvider{certErr: boom})
		require.NoError(t, err)
		_, err = ca.IssueCertificate(context.Background(), &IssueCertRequest{CommonName: "x", ValidityDays: 1})
		var certErr *ProviderCACertificateError
		if !errors.As(err, &certErr) {
			t.Fatalf("IssueCertificate with failing Certificate = %v, want *ProviderCACertificateError", err)
		}
	})

	t.Run("signer fails", func(t *testing.T) {
		real := newTestProviderCA(t)
		realCert, err := real.CACertificate(t.Context())
		require.NoError(t, err)
		ca, err := NewProviderCA(&failingProvider{cert: realCert, signerErr: boom})
		require.NoError(t, err)
		_, err = ca.IssueCertificate(context.Background(), &IssueCertRequest{CommonName: "x", ValidityDays: 1})
		var signerErr *ProviderCASignerError
		if !errors.As(err, &signerErr) {
			t.Fatalf("IssueCertificate with failing Signer = %v, want *ProviderCASignerError", err)
		}
	})
}
