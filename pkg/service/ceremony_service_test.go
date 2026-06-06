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
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
func (ca *testCeremonyCA) IssueCertificate(req *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
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
func (ca *testCeremonyCA) SignCSR(csrPEM []byte, opts *ceremony.SignCSROptions) (*x509.Certificate, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, ceremony.ErrInvalidCSR
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      csr.Subject,
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

// testCredentialService implements ceremony.CredentialServicer using an
// in-memory map for unit testing.
type testCredentialService struct {
	creds map[string][]byte
}

// newTestCredentialService creates a new in-memory credential service.
func newTestCredentialService() *testCredentialService {
	return &testCredentialService{creds: make(map[string][]byte)}
}

// StoreCredential stores a credential in memory.
func (s *testCredentialService) StoreCredential(_ context.Context, name string, value []byte) error {
	stored := make([]byte, len(value))
	copy(stored, value)
	s.creds[name] = stored
	return nil
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

// newTestCeremonyService creates a ceremony.CeremonyService and wraps it
// in the service-layer CeremonyService for testing. It uses an in-memory
// barrier, a test credential service, and single-admin configuration.
func newTestCeremonyService(t *testing.T) (CeremonyServicer, *testCeremonyCA) {
	t.Helper()

	barrier, _ := newTestSoftwareBarrier(t)
	err := barrier.Initialize(context.Background(), seal.Credentials{})
	require.NoError(t, err)

	credSvc := newTestCredentialService()
	config := &ceremony.CeremonyConfig{
		SOPin:    "test-so-pin",
		UserPin:  "test-user-pin",
		Hostname: "localhost",
		SANs:     []string{"localhost", "127.0.0.1"},
	}

	inner, err := ceremony.NewCeremonyService(config, barrier, credSvc, testLogger())
	require.NoError(t, err)

	ca := newTestCeremonyCA(t)

	return NewCeremonyService(inner), ca
}

// newTestCeremonyServiceWithConfig creates a ceremony.CeremonyService with
// a custom configuration, allowing tests to control threshold and officer
// settings.
func newTestCeremonyServiceWithConfig(t *testing.T, config *ceremony.CeremonyConfig) (CeremonyServicer, *testCeremonyCA) {
	t.Helper()

	barrier, _ := newTestSoftwareBarrier(t)
	err := barrier.Initialize(context.Background(), seal.Credentials{})
	require.NoError(t, err)

	credSvc := newTestCredentialService()
	inner, err := ceremony.NewCeremonyService(config, barrier, credSvc, testLogger())
	require.NoError(t, err)

	ca := newTestCeremonyCA(t)

	return NewCeremonyService(inner), ca
}

func TestNewCeremonyService(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	assert.NotNil(t, svc)
}

func TestNewCeremonyService_InterfaceCompliance(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	iface := CeremonyServicer(svc)
	assert.NotNil(t, iface)
}

func TestCeremonyService_CeremonyStatus_AwaitingInit(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	status, err := svc.CeremonyStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, "awaiting_init", status.State)
	assert.Empty(t, status.SPKIPin, "SPKI pin should be empty before initialization")
}

func TestCeremonyService_CeremonyInitialize_SingleAdmin(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	result, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.NotEmpty(t, result.SPKIPin, "SPKI pin should be set after init")
	assert.NotEmpty(t, result.CACertPEM, "CA cert PEM should be set")
	assert.NotEmpty(t, result.TLSCertPEM, "TLS cert PEM should be set")
	assert.NotEmpty(t, result.SOCertPEM, "SO cert PEM should be set")
	assert.NotEmpty(t, result.SOKeyPEM, "SO key PEM should be set")
	assert.Equal(t, "operational", result.State, "single-admin should go straight to operational")
	assert.Equal(t, 0, result.ShareCount, "single-admin should have zero shares")
}

func TestCeremonyService_CeremonyInitialize_AlreadyInitialized(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	_, err = svc.CeremonyInitialize(ctx, ca, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrAlreadyInitialized)
}

func TestCeremonyService_CeremonyStatus_AfterInit(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	status, err := svc.CeremonyStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, "operational", status.State)
	assert.NotEmpty(t, status.SPKIPin, "SPKI pin should be set after init")
}

func TestCeremonyService_CeremonyBeginClaimCert_NotEnrolling(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyBeginClaimCert(ctx, "officer1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNotInEnrollingState)
}

func TestCeremonyService_CeremonyCompleteClaimCert_NotEnrolling(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyCompleteClaimCert(ctx, "officer1", "deadbeef", []byte("sig"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNotInEnrollingState)
}

func TestCeremonyService_CeremonyClaimShare_NotEnrolling(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyClaimShare(ctx, "officer1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNotInEnrollingState)
}

func TestCeremonyService_CeremonySignCSR_NotInitialized(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonySignCSR(ctx, "user1", "test-so-pin", "-----BEGIN CERTIFICATE REQUEST-----\n...", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNotInitializedOrEnrolling)
}

func TestCeremonyService_CeremonySignCSR_EmptyUsername(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	_, err = svc.CeremonySignCSR(ctx, "", "test-so-pin", "csr-pem", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrMissingUsername)
}

func TestCeremonyService_CeremonySignCSR_EmptySOPin(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	_, err = svc.CeremonySignCSR(ctx, "user1", "", "csr-pem", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrInvalidSOPIN)
}

func TestCeremonyService_CeremonySignCSR_WrongSOPin(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	_, err = svc.CeremonySignCSR(ctx, "user1", "wrong-pin", "csr-pem", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrSOPINMismatch)
}

func TestCeremonyService_CeremonySignCSR_MissingCSR(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	_, err = svc.CeremonySignCSR(ctx, "user1", "test-so-pin", "", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrMissingCSRPEM)
}

func TestCeremonyService_CeremonySignCSR_MissingRole(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	_, err = svc.CeremonySignCSR(ctx, "user1", "test-so-pin", "some-csr", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrMissingRole)
}

func TestCeremonyService_CeremonySignCSR_InvalidRole(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	_, err = svc.CeremonySignCSR(ctx, "user1", "test-so-pin", "some-csr", "nonexistent-role")
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrInvalidRole)
}

func TestCeremonyService_CeremonySignCSR_ValidCSR(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	// Generate a valid CSR.
	csrKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "test-user"},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, csrKey)
	require.NoError(t, err)

	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	result, err := svc.CeremonySignCSR(ctx, "test-user", "test-so-pin", string(csrPEM), "admin")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.CertPEM, "signed certificate PEM should not be empty")

	// Verify the returned PEM is a valid certificate.
	block, _ := pem.Decode(result.CertPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "test-user", cert.Subject.CommonName)
}

func TestCeremonyService_CeremonyInitialize_MofN(t *testing.T) {
	// Generate CSRs for officers.
	officers := make([]ceremony.OfficerConfig, 3)
	for i := range officers {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		csrTemplate := &x509.CertificateRequest{
			Subject: pkix.Name{CommonName: "officer" + string(rune('1'+i))},
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, key)
		require.NoError(t, err)

		csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
		officers[i] = ceremony.OfficerConfig{
			Username: "officer" + string(rune('1'+i)),
			CSRPEM:   csrPEM,
		}
	}

	config := &ceremony.CeremonyConfig{
		SOPin:     "test-so-pin",
		UserPin:   "test-user-pin",
		Hostname:  "localhost",
		SANs:      []string{"localhost"},
		Threshold: 2,
		Officers:  officers,
	}

	svc, ca := newTestCeremonyServiceWithConfig(t, config)
	ctx := context.Background()

	result, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "enrolling", result.State, "M-of-N should enter enrolling state")
	assert.Equal(t, 3, result.ShareCount, "should have 3 shares for 3 officers")
	assert.NotEmpty(t, result.SPKIPin)
	assert.NotEmpty(t, result.CACertPEM)
	assert.NotEmpty(t, result.TLSCertPEM)
	assert.NotEmpty(t, result.SOCertPEM)
	assert.NotEmpty(t, result.SOKeyPEM)
}

func TestCeremonyService_MofN_BeginAndCompleteClaimCert(t *testing.T) {
	// Generate key pairs and CSRs for officers. The keys are retained
	// so that we can sign the nonce challenge later.
	type officerEntry struct {
		key      *ecdsa.PrivateKey
		username string
	}

	entries := make([]officerEntry, 3)
	officers := make([]ceremony.OfficerConfig, 3)
	for i := range entries {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		username := "officer" + string(rune('1'+i))
		csrTemplate := &x509.CertificateRequest{
			Subject: pkix.Name{CommonName: username},
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, key)
		require.NoError(t, err)

		csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
		entries[i] = officerEntry{key: key, username: username}
		officers[i] = ceremony.OfficerConfig{
			Username:          username,
			CSRPEM:            csrPEM,
			AuthenticatorType: ceremony.AuthenticatorPIV,
			AuthenticatorRef:  "9a",
		}
	}

	config := &ceremony.CeremonyConfig{
		SOPin:     "test-so-pin",
		UserPin:   "test-user-pin",
		Hostname:  "localhost",
		SANs:      []string{"localhost"},
		Threshold: 2,
		Officers:  officers,
	}

	svc, ca := newTestCeremonyServiceWithConfig(t, config)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	// Exercise the full BeginClaimCert -> CompleteClaimCert flow for
	// one officer to cover the service-layer success mapping paths.
	challenge, err := svc.CeremonyBeginClaimCert(ctx, entries[0].username)
	require.NoError(t, err)
	require.NotNil(t, challenge)
	assert.NotEmpty(t, challenge.NonceHex)
	assert.False(t, challenge.ExpiresAt.IsZero())

	// Decode the hex nonce and sign it with the officer's private key.
	nonceBytes, err := hex.DecodeString(challenge.NonceHex)
	require.NoError(t, err)

	digest := sha256.Sum256(nonceBytes)
	sig, err := ecdsa.SignASN1(rand.Reader, entries[0].key, digest[:])
	require.NoError(t, err)

	claimResult, err := svc.CeremonyCompleteClaimCert(ctx, entries[0].username, challenge.NonceHex, sig)
	require.NoError(t, err)
	require.NotNil(t, claimResult)
	assert.NotEmpty(t, claimResult.CertPEM, "claimed certificate PEM should not be empty")
	assert.NotEmpty(t, claimResult.CACertPEM, "CA cert PEM should not be empty")

	// Verify the claimed certificate is valid PEM.
	block, _ := pem.Decode(claimResult.CertPEM)
	require.NotNil(t, block)
	_, err = x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
}

func TestCeremonyService_MofN_ClaimShare(t *testing.T) {
	type officerEntry struct {
		key      *ecdsa.PrivateKey
		username string
	}

	entries := make([]officerEntry, 3)
	officers := make([]ceremony.OfficerConfig, 3)
	for i := range entries {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		username := "officer" + string(rune('1'+i))
		csrTemplate := &x509.CertificateRequest{
			Subject: pkix.Name{CommonName: username},
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, key)
		require.NoError(t, err)

		csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
		entries[i] = officerEntry{key: key, username: username}
		officers[i] = ceremony.OfficerConfig{Username: username, CSRPEM: csrPEM}
	}

	config := &ceremony.CeremonyConfig{
		SOPin:     "test-so-pin",
		UserPin:   "test-user-pin",
		Hostname:  "localhost",
		SANs:      []string{"localhost"},
		Threshold: 2,
		Officers:  officers,
	}

	svc, ca := newTestCeremonyServiceWithConfig(t, config)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	// Claim share for the first officer.
	share, err := svc.CeremonyClaimShare(ctx, entries[0].username)
	require.NoError(t, err)
	assert.NotEmpty(t, share, "share should not be empty")
}

func TestCeremonyService_CeremonyBeginClaimCert_EmptyUsername(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyBeginClaimCert(ctx, "")
	require.Error(t, err)
	// Ceremony is not in enrolling state, so the error is about state, not username.
	assert.ErrorIs(t, err, ceremony.ErrNotInEnrollingState)
}

func TestCeremonyService_CeremonyInitialize_InvalidConfig_NoSOPin(t *testing.T) {
	config := &ceremony.CeremonyConfig{
		SOPin:    "",
		UserPin:  "test-user-pin",
		Hostname: "localhost",
	}

	svc, ca := newTestCeremonyServiceWithConfig(t, config)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrInvalidSOPIN)
}

func TestCeremonyService_CeremonyInitialize_InvalidConfig_NoUserPin(t *testing.T) {
	config := &ceremony.CeremonyConfig{
		SOPin:    "test-so-pin",
		UserPin:  "",
		Hostname: "localhost",
	}

	svc, ca := newTestCeremonyServiceWithConfig(t, config)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrInvalidUserPIN)
}

func TestCeremonyService_NilCeremonyServicerFields(t *testing.T) {
	// Verify that NewCeremonyService returns a valid CeremonyServicer
	// and that the internal ceremony field is correctly assigned.
	barrier, _ := newTestSoftwareBarrier(t)
	err := barrier.Initialize(context.Background(), seal.Credentials{})
	require.NoError(t, err)

	credSvc := newTestCredentialService()
	config := &ceremony.CeremonyConfig{
		SOPin:    "test-so-pin",
		UserPin:  "test-user-pin",
		Hostname: "localhost",
	}

	inner, err := ceremony.NewCeremonyService(config, barrier, credSvc, testLogger())
	require.NoError(t, err)

	svc := NewCeremonyService(inner)
	assert.NotNil(t, svc)

	// Verify status is accessible immediately.
	status, err := svc.CeremonyStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "awaiting_init", status.State)
}

func TestCeremonyService_CeremonySignCSR_AllRoles(t *testing.T) {
	svc, ca := newTestCeremonyService(t)
	ctx := context.Background()

	_, err := svc.CeremonyInitialize(ctx, ca, nil)
	require.NoError(t, err)

	roles := []string{"so", "admin", "operator", "user", "auditor", "custodian"}

	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			csrKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			require.NoError(t, err)

			csrTemplate := &x509.CertificateRequest{
				Subject: pkix.Name{CommonName: "test-" + role},
			}
			csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, csrKey)
			require.NoError(t, err)

			csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

			result, err := svc.CeremonySignCSR(ctx, "test-"+role, "test-so-pin", string(csrPEM), role)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.NotEmpty(t, result.CertPEM)
		})
	}
}

func TestCeremonyService_CeremonyCredentialServiceNil(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	err := barrier.Initialize(context.Background(), seal.Credentials{})
	require.NoError(t, err)

	config := &ceremony.CeremonyConfig{
		SOPin:    "test-so-pin",
		UserPin:  "test-user-pin",
		Hostname: "localhost",
	}

	_, err = ceremony.NewCeremonyService(config, barrier, nil, testLogger())
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNilCredentialService)
}

func TestCeremonyService_CeremonyNilConfig(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	err := barrier.Initialize(context.Background(), seal.Credentials{})
	require.NoError(t, err)

	credSvc := newTestCredentialService()

	_, err = ceremony.NewCeremonyService(nil, barrier, credSvc, testLogger())
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNilConfig)
}

func TestCeremonyService_CeremonyNilBarrier(t *testing.T) {
	credSvc := newTestCredentialService()
	config := &ceremony.CeremonyConfig{
		SOPin:    "test-so-pin",
		UserPin:  "test-user-pin",
		Hostname: "localhost",
	}

	_, err := ceremony.NewCeremonyService(config, nil, credSvc, testLogger())
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNilBarrier)
}

func TestCeremonyService_CeremonyNilLogger(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	err := barrier.Initialize(context.Background(), seal.Credentials{})
	require.NoError(t, err)

	credSvc := newTestCredentialService()
	config := &ceremony.CeremonyConfig{
		SOPin:    "test-so-pin",
		UserPin:  "test-user-pin",
		Hostname: "localhost",
	}

	_, err = ceremony.NewCeremonyService(config, barrier, credSvc, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ceremony.ErrNilLogger)
}

// TestCeremonyService_CeremonyInitialize_PerRequestPINsReachService proves the
// SO and user PINs supplied on the CeremonyInitialize call (not the
// construction-time config) drive the ceremony. The service is built with
// EMPTY configured PINs: a nil/empty-opts call must fail closed, while a call
// carrying the PINs in the options succeeds. This is the regression guard for
// the gap where the transport request PINs were dropped and consumers had to
// mutate shared config per-init.
func TestCeremonyService_CeremonyInitialize_PerRequestPINsReachService(t *testing.T) {
	emptyConfig := &ceremony.CeremonyConfig{
		// SOPin and UserPin intentionally empty — they must come from opts.
		Hostname: "localhost",
		SANs:     []string{"localhost", "127.0.0.1"},
	}

	t.Run("nil opts fails closed", func(t *testing.T) {
		svc, ca := newTestCeremonyServiceWithConfig(t, &ceremony.CeremonyConfig{Hostname: "localhost"})
		_, err := svc.CeremonyInitialize(context.Background(), ca, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ceremony.ErrInvalidSOPIN,
			"empty configured PIN with no per-request PIN must fail closed")
	})

	t.Run("empty opts pins fail closed", func(t *testing.T) {
		svc, ca := newTestCeremonyServiceWithConfig(t, &ceremony.CeremonyConfig{Hostname: "localhost"})
		_, err := svc.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{})
		require.Error(t, err)
		assert.ErrorIs(t, err, ceremony.ErrInvalidSOPIN)
	})

	t.Run("so pin only still fails closed on missing user pin", func(t *testing.T) {
		svc, ca := newTestCeremonyServiceWithConfig(t, &ceremony.CeremonyConfig{Hostname: "localhost"})
		_, err := svc.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{SOPin: "so-pin"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ceremony.ErrInvalidUserPIN)
	})

	t.Run("per-request pins succeed where config is empty", func(t *testing.T) {
		// A fresh service each time because Initialize is one-shot.
		_ = emptyConfig
		svc, ca := newTestCeremonyServiceWithConfig(t, &ceremony.CeremonyConfig{
			Hostname: "localhost",
			SANs:     []string{"localhost", "127.0.0.1"},
		})
		result, err := svc.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
			SOPin:   "request-so-pin",
			UserPin: "request-user-pin",
		})
		require.NoError(t, err, "per-request PINs must satisfy the ceremony with empty config")
		require.NotNil(t, result)
		assert.NotEmpty(t, result.SPKIPin)
		assert.Equal(t, "operational", result.State)
	})
}

// TestCeremonyService_CeremonyInitialize_PerRequestPINsTakePrecedence proves
// that a non-empty per-request PIN overrides the configured PIN and that the
// effective PIN governs subsequent SO-authenticated operations (SignCSR),
// confirming the override is persisted consistently and not just consumed at
// init time.
func TestCeremonyService_CeremonyInitialize_PerRequestPINsTakePrecedence(t *testing.T) {
	svc, ca := newTestCeremonyServiceWithConfig(t, &ceremony.CeremonyConfig{
		SOPin:    "config-so-pin",
		UserPin:  "config-user-pin",
		Hostname: "localhost",
		SANs:     []string{"localhost", "127.0.0.1"},
	})

	_, err := svc.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin:   "override-so-pin",
		UserPin: "override-user-pin",
	})
	require.NoError(t, err)

	// The override SO PIN must now be the effective one: signing with the old
	// config PIN must be rejected, signing with the override PIN must be
	// accepted.
	csrPEM := newTestOfficerCSR(t, "operator-1")

	_, err = svc.CeremonySignCSR(context.Background(), "operator-1", "config-so-pin", string(csrPEM), "operator")
	require.Error(t, err, "stale config SO PIN must be rejected after per-request override")
	assert.ErrorIs(t, err, ceremony.ErrSOPINMismatch)

	res, err := svc.CeremonySignCSR(context.Background(), "operator-1", "override-so-pin", string(csrPEM), "operator")
	require.NoError(t, err, "override SO PIN must be accepted")
	assert.NotEmpty(t, res.CertPEM)
}
