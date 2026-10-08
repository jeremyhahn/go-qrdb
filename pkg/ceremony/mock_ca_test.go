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
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newTestCeremonyService builds a *CeremonyService over barrier and wires a
// PIN backend (pintest.NewBackend), the same way a composer wires one
// (server.WithPINBackend / lifecycle.ceremonyService) -- without it, every
// PIN-gated operation (Initialize, SignCSRInit) fails closed with
// pin.ErrBackendNotConfigured.
func newTestCeremonyService(t *testing.T, cfg *CeremonyConfig, barrier *seal.StorageBarrier) *CeremonyService {
	t.Helper()
	svc, err := NewCeremonyService(cfg, barrier, testLogger())
	require.NoError(t, err)
	svc.SetPINBackend(pintest.NewBackend(t))
	return svc
}

// noneStrategyBarrier returns a fresh, sealed *seal.StorageBarrier over an
// in-memory backend with the "none" strategy, for single-admin Initialize
// tests (go-qrdb#548: qrdb init brings the system barrier up itself for the
// single-admin path). A Shamir-configured barrier (shamirBarrier,
// newHolderEnv's own) cannot be initialized this way at all -- it requires
// InitializeShamir, never a credential -- so M-of-N and single-admin
// Initialize tests need barriers built differently.
func noneStrategyBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "test/root-key"}, seal.NewNoneStrategy())
	require.NoError(t, err)
	return b
}

// mockCA is a test CeremonyCA that generates real ECDSA self-signed
// certificates. Shared by the BYO-TLS, system-barrier-init and
// admin-credential test suites (go-qrdb#548).
type mockCA struct {
	key     *ecdsa.PrivateKey
	cert    *x509.Certificate
	certDER []byte
}

func newMockCA(t *testing.T) *mockCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)

	return &mockCA{key: key, cert: cert, certDER: certDER}
}

func (m *mockCA) SignCSR(_ context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, errors.New("failed to decode CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR signature invalid: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	rawSubject, err := opts.RawSubject()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		RawSubject:   rawSubject,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Duration(opts.ValidityDays) * 24 * time.Hour),
		KeyUsage:     opts.KeyUsage,
		ExtKeyUsage:  opts.ExtKeyUsage,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, m.cert, csr.PublicKey, m.key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(certDER)
}

func (m *mockCA) CACertificate(_ context.Context) (*x509.Certificate, error) {
	return m.cert, nil
}

func (m *mockCA) CABundle() ([]byte, error) {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: m.certDER,
	}), nil
}

func (m *mockCA) IssueCertificate(_ context.Context, req *IssueCertRequest) (*IssuedCert, error) {
	if req.PublicKey == nil {
		return nil, &IssueCertKeyError{Field: "PublicKey"}
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: req.CommonName, OrganizationalUnit: req.OrganizationalUnit},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Duration(req.ValidityDays) * 24 * time.Hour),
		KeyUsage:     req.KeyUsage,
		ExtKeyUsage:  req.ExtKeyUsage,
		DNSNames:     req.SANs,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, m.cert, req.PublicKey, m.key)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}

	return &IssuedCert{
		Certificate: cert,
		CertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
	}, nil
}

// generateCSRPEM generates an ECDSA P-256 key pair and a PEM-encoded CSR for
// cn, for tests exercising officer/admin CSR-sign paths.
func generateCSRPEM(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	require.NoError(t, err)

	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	return csrPEM, key
}

// defaultConfig returns a minimal CeremonyConfig for NewCeremonyService.
// Every per-run parameter (SOPin, UserPin, Hostname, SANs, Threshold,
// Officers, ...) lives on InitializeOptions, not here -- see defaultInitOpts.
func defaultConfig() *CeremonyConfig {
	return &CeremonyConfig{}
}

// defaultInitOpts returns single-admin InitializeOptions with the SO/User
// PINs and TLS hostname/SANs the ceremony requires, for tests that do not
// care about M-of-N or BYO-TLS specifics.
func defaultInitOpts() *InitializeOptions {
	return &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		Hostname:      "localhost",
		SANs:          []string{"localhost", "127.0.0.1"},
		AdminGenerate: true,
	}
}

// mofnInitOpts returns M-of-N InitializeOptions with numOfficers generated
// officer CSRs/keys and threshold officers required to reconstruct the
// barrier's unseal key.
func mofnInitOpts(t *testing.T, threshold, numOfficers int) (*InitializeOptions, []*ecdsa.PrivateKey) {
	t.Helper()
	opts := defaultInitOpts()
	opts.Threshold = threshold

	officers := make([]OfficerConfig, numOfficers)
	keys := make([]*ecdsa.PrivateKey, numOfficers)
	for i := 0; i < numOfficers; i++ {
		username := fmt.Sprintf("officer-%d", i)
		csrPEM, key := generateCSRPEM(t, username)
		officers[i] = OfficerConfig{
			Username: username,
			CSRPEM:   csrPEM,
		}
		keys[i] = key
	}
	opts.Officers = officers
	return opts, keys
}

// enrollingInitOpts is mofnInitOpts(t, 2, 3) with the officer keys discarded,
// for SignCSRInit tests that only need the ceremony to land in
// StateEnrolling (go-qrdb#548: sign-csr is PreOperationalOnly, so a
// single-admin config -- which commits StateOperational the instant
// Initialize returns -- can no longer exercise it) and otherwise care about
// nothing M-of-N-specific.
func enrollingInitOpts(t *testing.T) *InitializeOptions {
	t.Helper()
	opts, _ := mofnInitOpts(t, 2, 3)
	return opts
}
