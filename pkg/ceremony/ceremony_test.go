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
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
}

func testBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	require.NoError(t, err)
	b, err := seal.NewStorageBarrier(
		testLogger(),
		seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "test/root-key"},
		strategy,
	)
	require.NoError(t, err)
	return b
}

// testCredentialService is an in-memory implementation of CredentialServicer.
type testCredentialService struct {
	creds map[string][]byte
}

func newTestCredentialService() *testCredentialService {
	return &testCredentialService{creds: make(map[string][]byte)}
}

func (s *testCredentialService) StoreCredential(_ context.Context, name string, value []byte) error {
	s.creds[name] = value
	return nil
}

// errorCredentialService injects errors for testing.
type errorCredentialService struct {
	failOn string
}

func (s *errorCredentialService) StoreCredential(_ context.Context, name string, _ []byte) error {
	if name == s.failOn {
		return errors.New("credential store error")
	}
	return nil
}

// mockCA is a test CeremonyCA that generates real ECDSA self-signed certificates.
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

func (m *mockCA) SignCSR(csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
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

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      csr.Subject,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Duration(opts.ValidityDays) * 24 * time.Hour),
		KeyUsage:     opts.KeyUsage,
		ExtKeyUsage:  opts.ExtKeyUsage,
		DNSNames:     csr.DNSNames,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, m.cert, csr.PublicKey, m.key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(certDER)
}

func (m *mockCA) CACertificate() (*x509.Certificate, error) {
	return m.cert, nil
}

func (m *mockCA) CABundle() ([]byte, error) {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: m.certDER,
	}), nil
}

func (m *mockCA) IssueCertificate(req *IssueCertRequest) (*IssuedCert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: req.CommonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Duration(req.ValidityDays) * 24 * time.Hour),
		KeyUsage:     req.KeyUsage,
		ExtKeyUsage:  req.ExtKeyUsage,
		DNSNames:     req.SANs,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, m.cert, &key.PublicKey, m.key)
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

	return &IssuedCert{
		Certificate: cert,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
	}, nil
}

// errorCA wraps a mockCA with injectable errors.
type errorCA struct {
	*mockCA
	failSignCSR          bool
	failCACertificate    bool
	failCABundle         bool
	failIssueCertificate bool
	issueCallCount       int
	failOnIssueCall      int // 0 = never fail, 1 = fail on first, 2 = fail on second, etc.
}

func newErrorCA(t *testing.T) *errorCA {
	t.Helper()
	return &errorCA{mockCA: newMockCA(t)}
}

func (e *errorCA) SignCSR(csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	if e.failSignCSR {
		return nil, errors.New("sign CSR error")
	}
	return e.mockCA.SignCSR(csrPEM, opts)
}

func (e *errorCA) CACertificate() (*x509.Certificate, error) {
	if e.failCACertificate {
		return nil, errors.New("CA certificate error")
	}
	return e.mockCA.CACertificate()
}

func (e *errorCA) CABundle() ([]byte, error) {
	if e.failCABundle {
		return nil, errors.New("CA bundle error")
	}
	return e.mockCA.CABundle()
}

func (e *errorCA) IssueCertificate(req *IssueCertRequest) (*IssuedCert, error) {
	e.issueCallCount++
	if e.failIssueCertificate {
		return nil, errors.New("issue certificate error")
	}
	if e.failOnIssueCall > 0 && e.issueCallCount == e.failOnIssueCall {
		return nil, errors.New("issue certificate error on call")
	}
	return e.mockCA.IssueCertificate(req)
}

// generateCSRPEM generates a real ECDSA CSR PEM for testing.
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

// signNonce signs a nonce with an ECDSA key for challenge-response testing.
func signNonce(t *testing.T, key *ecdsa.PrivateKey, nonce []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(nonce)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)

	sig, err := asn1.Marshal(struct{ R, S *big.Int }{R: r, S: s})
	require.NoError(t, err)
	return sig
}

func defaultConfig() *CeremonyConfig {
	return &CeremonyConfig{
		SOPin:    "test-so-pin",
		UserPin:  "test-user-pin",
		Hostname: "localhost",
		SANs:     []string{"localhost", "127.0.0.1"},
	}
}

func mofnConfig(t *testing.T, threshold, numOfficers int) (*CeremonyConfig, []*ecdsa.PrivateKey) {
	t.Helper()
	cfg := defaultConfig()
	cfg.Threshold = threshold

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
	cfg.Officers = officers
	return cfg, keys
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewCeremonyService_NilConfig(t *testing.T) {
	_, err := NewCeremonyService(nil, testBarrier(t), newTestCredentialService(), testLogger())
	assert.ErrorIs(t, err, ErrNilConfig)
}

func TestNewCeremonyService_NilBarrier(t *testing.T) {
	_, err := NewCeremonyService(defaultConfig(), nil, newTestCredentialService(), testLogger())
	assert.ErrorIs(t, err, ErrNilBarrier)
}

func TestNewCeremonyService_NilCredService(t *testing.T) {
	_, err := NewCeremonyService(defaultConfig(), testBarrier(t), nil, testLogger())
	assert.ErrorIs(t, err, ErrNilCredentialService)
}

func TestNewCeremonyService_NilLogger(t *testing.T) {
	_, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), nil)
	assert.ErrorIs(t, err, ErrNilLogger)
}

func TestNewCeremonyService_Valid(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)
	assert.NotNil(t, svc)
	assert.Equal(t, StateAwaitingInit, svc.State())
}

// ---------------------------------------------------------------------------
// State tests
// ---------------------------------------------------------------------------

func TestState_ReturnsCorrectState(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	assert.Equal(t, StateAwaitingInit, svc.State())
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestInitialize_SingleAdminSuccess(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newMockCA(t)
	result, err := svc.Initialize(context.Background(), ca)
	require.NoError(t, err)

	assert.Equal(t, StateOperational, result.State)
	assert.Equal(t, 0, result.ShareCount)
	assert.NotEmpty(t, result.SPKIPin)
	assert.NotEmpty(t, result.CACertPEM)
	assert.NotEmpty(t, result.TLSCertPEM)
	assert.NotEmpty(t, result.SOCertPEM)
	assert.NotEmpty(t, result.SOKeyPEM)
	assert.Equal(t, StateOperational, svc.State())
}

func TestInitialize_MofNSuccess(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newMockCA(t)
	result, err := svc.Initialize(context.Background(), ca)
	require.NoError(t, err)

	assert.Equal(t, StateEnrolling, result.State)
	assert.Equal(t, 3, result.ShareCount)
	assert.NotEmpty(t, result.SPKIPin)
	assert.Equal(t, StateEnrolling, svc.State())
}

func TestInitialize_AlreadyInitialized(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newMockCA(t)
	_, err = svc.Initialize(context.Background(), ca)
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), ca)
	assert.ErrorIs(t, err, ErrAlreadyInitialized)
}

func TestInitialize_EmptySOPin(t *testing.T) {
	cfg := defaultConfig()
	cfg.SOPin = ""
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrInvalidSOPIN)
}

func TestInitialize_EmptyUserPin(t *testing.T) {
	cfg := defaultConfig()
	cfg.UserPin = ""
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrInvalidUserPIN)
}

func TestInitialize_MofN_NoOfficers(t *testing.T) {
	cfg := defaultConfig()
	cfg.Threshold = 2
	cfg.Officers = nil
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrNoOfficers)
}

func TestInitialize_MofN_ThresholdExceedsOfficers(t *testing.T) {
	cfg, _ := mofnConfig(t, 5, 3) // threshold > officers
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrInvalidThreshold)
}

func TestInitialize_MofN_DuplicateUsername(t *testing.T) {
	csrPEM1, _ := generateCSRPEM(t, "officer-dup")
	csrPEM2, _ := generateCSRPEM(t, "officer-dup")
	cfg := defaultConfig()
	cfg.Threshold = 2
	cfg.Officers = []OfficerConfig{
		{Username: "officer-dup", CSRPEM: csrPEM1},
		{Username: "officer-dup", CSRPEM: csrPEM2},
	}
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrDuplicateUsername)
}

func TestInitialize_MofN_EmptyOfficerUsername(t *testing.T) {
	csrPEM, _ := generateCSRPEM(t, "")
	cfg := defaultConfig()
	cfg.Threshold = 2
	cfg.Officers = []OfficerConfig{
		{Username: "officer-1", CSRPEM: csrPEM},
		{Username: "", CSRPEM: csrPEM},
	}
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestInitialize_MofN_InvalidCSR(t *testing.T) {
	cfg := defaultConfig()
	cfg.Threshold = 2
	cfg.Officers = []OfficerConfig{
		{Username: "officer-1", CSRPEM: []byte("not-a-valid-csr")},
		{Username: "officer-2", CSRPEM: []byte("also-not-valid")},
	}
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	var csrErr *CSRParseError
	assert.ErrorAs(t, err, &csrErr)
}

func TestInitialize_IssueTLSCertFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failOnIssueCall = 1 // fail on TLS cert issuance
	_, err = svc.Initialize(context.Background(), ca)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_IssueSOCertFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failOnIssueCall = 2 // fail on SO cert issuance
	_, err = svc.Initialize(context.Background(), ca)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_CACertificateFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failCACertificate = true
	_, err = svc.Initialize(context.Background(), ca)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_CABundleFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failCABundle = true
	_, err = svc.Initialize(context.Background(), ca)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_StoreSOPinCredentialFails(t *testing.T) {
	svc, err := NewCeremonyService(
		defaultConfig(), testBarrier(t),
		&errorCredentialService{failOn: "ceremony/so-pin"},
		testLogger(),
	)
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_StoreUserPinCredentialFails(t *testing.T) {
	svc, err := NewCeremonyService(
		defaultConfig(), testBarrier(t),
		&errorCredentialService{failOn: "ceremony/user-pin"},
		testLogger(),
	)
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	assert.ErrorIs(t, err, ErrInitFailed)
}

// ---------------------------------------------------------------------------
// BeginClaimCert tests
// ---------------------------------------------------------------------------

func TestBeginClaimCert_Success(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)
	assert.NotEmpty(t, challenge.Nonce)
	assert.Equal(t, "officer-0", challenge.Username)
	assert.True(t, challenge.ExpiresAt.After(time.Now()))
}

func TestBeginClaimCert_NotEnrollingState(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	// Single admin mode goes straight to operational.
	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.BeginClaimCert(context.Background(), "officer-0")
	assert.ErrorIs(t, err, ErrNotInEnrollingState)
}

func TestBeginClaimCert_NotInitialized(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.BeginClaimCert(context.Background(), "officer-0")
	assert.ErrorIs(t, err, ErrNotInEnrollingState)
}

func TestBeginClaimCert_UnknownUsername(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.BeginClaimCert(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrPendingCertNotFound)
}

func TestBeginClaimCert_EmptyUsername(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.BeginClaimCert(context.Background(), "")
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// ---------------------------------------------------------------------------
// CompleteClaimCert tests
// ---------------------------------------------------------------------------

func TestCompleteClaimCert_ECDSASuccess(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	sig := signNonce(t, keys[0], challenge.Nonce)
	nonceHex := hex.EncodeToString(challenge.Nonce)

	result, err := svc.CompleteClaimCert(context.Background(), "officer-0", nonceHex, sig)
	require.NoError(t, err)
	assert.NotEmpty(t, result.CertPEM)
	assert.NotEmpty(t, result.CACertPEM)
}

func TestCompleteClaimCert_WrongSignature(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	nonceHex := hex.EncodeToString(challenge.Nonce)
	badSig := []byte("bad-signature-bytes")

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", nonceHex, badSig)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestCompleteClaimCert_ExpiredChallenge(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	// Manually expire the challenge.
	svc.mu.Lock()
	svc.challenges["officer-0"].ExpiresAt = time.Now().Add(-time.Second)
	svc.mu.Unlock()

	sig := signNonce(t, keys[0], challenge.Nonce)
	nonceHex := hex.EncodeToString(challenge.Nonce)

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", nonceHex, sig)
	assert.ErrorIs(t, err, ErrChallengeExpired)
}

func TestCompleteClaimCert_NotEnrollingState(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", "abc", []byte("sig"))
	assert.ErrorIs(t, err, ErrNotInEnrollingState)
}

func TestCompleteClaimCert_EmptyUsername(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.CompleteClaimCert(context.Background(), "", "abc", []byte("sig"))
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestCompleteClaimCert_NoChallengeExists(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", "abc", []byte("sig"))
	assert.ErrorIs(t, err, ErrChallengeNotFound)
}

func TestCompleteClaimCert_InvalidNonceHex(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", "not-valid-hex!!!", []byte("sig"))
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestCompleteClaimCert_NonceMismatch(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	// Use a different nonce.
	wrongNonce := make([]byte, 32)
	_, err = rand.Read(wrongNonce)
	require.NoError(t, err)

	sig := signNonce(t, keys[0], wrongNonce)
	wrongNonceHex := hex.EncodeToString(wrongNonce)
	_ = challenge

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", wrongNonceHex, sig)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestCompleteClaimCert_AlreadyClaimed(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	// First claim succeeds.
	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)
	sig := signNonce(t, keys[0], challenge.Nonce)
	nonceHex := hex.EncodeToString(challenge.Nonce)
	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", nonceHex, sig)
	require.NoError(t, err)

	// Second claim attempt should fail at BeginClaimCert.
	_, err = svc.BeginClaimCert(context.Background(), "officer-0")
	assert.ErrorIs(t, err, ErrCertAlreadyClaimed)
}

// ---------------------------------------------------------------------------
// ClaimShare tests
// ---------------------------------------------------------------------------

func TestClaimShare_Success(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	share, err := svc.ClaimShare(context.Background(), "officer-0")
	require.NoError(t, err)
	assert.NotEmpty(t, share)
}

func TestClaimShare_NotEnrollingState(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	// Not initialized yet.
	_, err = svc.ClaimShare(context.Background(), "officer-0")
	assert.ErrorIs(t, err, ErrNotInEnrollingState)
}

func TestClaimShare_UnknownUsername(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.ClaimShare(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestClaimShare_EmptyUsername(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.ClaimShare(context.Background(), "")
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestClaimShare_AlreadyClaimed(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.ClaimShare(context.Background(), "officer-0")
	require.NoError(t, err)

	_, err = svc.ClaimShare(context.Background(), "officer-0")
	assert.ErrorIs(t, err, ErrShareAlreadyClaimed)
}

func TestClaimShare_AllClaimsTransitionToOperational(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	ctx := context.Background()

	// Claim all certs and shares for all officers.
	for i := 0; i < 3; i++ {
		username := fmt.Sprintf("officer-%d", i)

		// Claim cert.
		challenge, err := svc.BeginClaimCert(ctx, username)
		require.NoError(t, err)
		sig := signNonce(t, keys[i], challenge.Nonce)
		nonceHex := hex.EncodeToString(challenge.Nonce)
		_, err = svc.CompleteClaimCert(ctx, username, nonceHex, sig)
		require.NoError(t, err)

		// Claim share.
		_, err = svc.ClaimShare(ctx, username)
		require.NoError(t, err)
	}

	assert.Equal(t, StateOperational, svc.State())
}

// ---------------------------------------------------------------------------
// SignCSRInit tests
// ---------------------------------------------------------------------------

func TestSignCSRInit_SingleAdminSuccess(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	result, err := svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", csrPEM, "admin")
	require.NoError(t, err)
	assert.NotEmpty(t, result.CertPEM)
}

func TestSignCSRInit_EnrollingStateSuccess(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "new-user")

	result, err := svc.SignCSRInit(context.Background(), "new-user", "test-so-pin", csrPEM, "operator")
	require.NoError(t, err)
	assert.NotEmpty(t, result.CertPEM)
}

func TestSignCSRInit_WrongSOPin(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "wrong-pin", csrPEM, "admin")
	assert.ErrorIs(t, err, ErrSOPINMismatch)
}

func TestSignCSRInit_EmptySOPin(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "", csrPEM, "admin")
	assert.ErrorIs(t, err, ErrInvalidSOPIN)
}

func TestSignCSRInit_InvalidRole(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", csrPEM, "superadmin")
	assert.ErrorIs(t, err, ErrInvalidRole)
}

func TestSignCSRInit_EmptyCSR(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", nil, "admin")
	assert.ErrorIs(t, err, ErrMissingCSRPEM)
}

func TestSignCSRInit_EmptyRole(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", csrPEM, "")
	assert.ErrorIs(t, err, ErrMissingRole)
}

func TestSignCSRInit_EmptyUsername(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "", "test-so-pin", csrPEM, "admin")
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestSignCSRInit_NotInitialized(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", csrPEM, "admin")
	assert.ErrorIs(t, err, ErrNotInitializedOrEnrolling)
}

func TestSignCSRInit_AllRoleProfiles(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	roles := []string{"so", "admin", "operator", "user", "auditor", "custodian"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			csrPEM, _ := generateCSRPEM(t, "test-"+role)
			result, err := svc.SignCSRInit(context.Background(), "test-"+role, "test-so-pin", csrPEM, role)
			require.NoError(t, err)
			assert.NotEmpty(t, result.CertPEM)

			// Parse the cert to verify it was issued correctly.
			block, _ := pem.Decode(result.CertPEM)
			require.NotNil(t, block)
			cert, err := x509.ParseCertificate(block.Bytes)
			require.NoError(t, err)
			assert.NotNil(t, cert)
		})
	}
}

func TestSignCSRInit_CASignCSRFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	_, err = svc.Initialize(context.Background(), ca)
	require.NoError(t, err)

	ca.failSignCSR = true
	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", csrPEM, "admin")
	assert.ErrorIs(t, err, ErrCSRSigningFailed)
}

// ---------------------------------------------------------------------------
// SPKIPin tests
// ---------------------------------------------------------------------------

func TestSPKIPin_BeforeInit(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.SPKIPin()
	assert.ErrorIs(t, err, ErrCANotInitialized)
}

func TestSPKIPin_AfterInit(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	pin, err := svc.SPKIPin()
	require.NoError(t, err)
	assert.NotEmpty(t, pin)
}

func TestSPKIPin_CACertificateFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	_, err = svc.Initialize(context.Background(), ca)
	require.NoError(t, err)

	ca.failCACertificate = true
	_, err = svc.SPKIPin()
	var bundleErr *CABundleError
	assert.ErrorAs(t, err, &bundleErr)
}

// ---------------------------------------------------------------------------
// ThresholdRegistry accessor test
// ---------------------------------------------------------------------------

func TestThresholdRegistry_Accessor(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	reg := svc.ThresholdRegistry()
	assert.NotNil(t, reg)
}

// ---------------------------------------------------------------------------
// Cleanup tests
// ---------------------------------------------------------------------------

func TestCleanup(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	// Issue a challenge to verify it gets cleaned up.
	_, err = svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	svc.Cleanup()

	// Shares should be destroyed.
	svc.mu.RLock()
	assert.Empty(t, svc.challenges)
	svc.mu.RUnlock()
}

// ---------------------------------------------------------------------------
// Error types tests
// ---------------------------------------------------------------------------

func TestShamirSplitError(t *testing.T) {
	inner := errors.New("inner error")
	err := &ShamirSplitError{Err: inner}
	assert.Contains(t, err.Error(), "shamir split failed")
	assert.ErrorIs(t, err, inner)
}

func TestCSRParseError(t *testing.T) {
	inner := errors.New("parse error")
	err := &CSRParseError{Err: inner}
	assert.Contains(t, err.Error(), "CSR parse failed")
	assert.ErrorIs(t, err, inner)
}

func TestCertEncodeError(t *testing.T) {
	inner := errors.New("encode error")
	err := &CertEncodeError{Err: inner}
	assert.Contains(t, err.Error(), "cert encode failed")
	assert.ErrorIs(t, err, inner)
}

func TestShareStoreError(t *testing.T) {
	inner := errors.New("store error")
	err := &ShareStoreError{Err: inner}
	assert.Contains(t, err.Error(), "share store failed")
	assert.ErrorIs(t, err, inner)
}

func TestCABundleError(t *testing.T) {
	inner := errors.New("bundle error")
	err := &CABundleError{Err: inner}
	assert.Contains(t, err.Error(), "CA bundle failed")
	assert.ErrorIs(t, err, inner)
}

func TestSPKIPinError(t *testing.T) {
	inner := errors.New("pin error")
	err := &SPKIPinError{Err: inner}
	assert.Contains(t, err.Error(), "SPKI pin computation failed")
	assert.ErrorIs(t, err, inner)
}

// ---------------------------------------------------------------------------
// validateUniqueOfficerUsernames tests
// ---------------------------------------------------------------------------

func TestValidateUniqueOfficerUsernames_Valid(t *testing.T) {
	officers := []OfficerConfig{
		{Username: "alice"},
		{Username: "bob"},
		{Username: "charlie"},
	}
	err := validateUniqueOfficerUsernames(officers)
	assert.NoError(t, err)
}

func TestValidateUniqueOfficerUsernames_Duplicate(t *testing.T) {
	officers := []OfficerConfig{
		{Username: "alice"},
		{Username: "alice"},
	}
	err := validateUniqueOfficerUsernames(officers)
	assert.ErrorIs(t, err, ErrDuplicateUsername)
}

func TestValidateUniqueOfficerUsernames_Empty(t *testing.T) {
	officers := []OfficerConfig{
		{Username: "alice"},
		{Username: ""},
	}
	err := validateUniqueOfficerUsernames(officers)
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// ---------------------------------------------------------------------------
// CompleteClaimCert with wrong key (key not matching CSR)
// ---------------------------------------------------------------------------

func TestCompleteClaimCert_WrongKey(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	// Sign with a different key (not the one used to generate the CSR).
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	digest := sha256.Sum256(challenge.Nonce)
	r, s, err := ecdsa.Sign(rand.Reader, wrongKey, digest[:])
	require.NoError(t, err)
	sig, err := asn1.Marshal(struct{ R, S *big.Int }{R: r, S: s})
	require.NoError(t, err)

	nonceHex := hex.EncodeToString(challenge.Nonce)
	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", nonceHex, sig)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

// ---------------------------------------------------------------------------
// CompleteClaimCert with CABundle error
// ---------------------------------------------------------------------------

func TestCompleteClaimCert_CABundleError(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	_, err = svc.Initialize(context.Background(), ca)
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(context.Background(), "officer-0")
	require.NoError(t, err)

	sig := signNonce(t, keys[0], challenge.Nonce)
	nonceHex := hex.EncodeToString(challenge.Nonce)

	// Make CABundle fail after the nonce verification succeeds.
	ca.failCABundle = true

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", nonceHex, sig)
	var bundleErr *CABundleError
	assert.ErrorAs(t, err, &bundleErr)
}

// ---------------------------------------------------------------------------
// MofN with SignCSR failure during initialization
// ---------------------------------------------------------------------------

func TestInitialize_MofN_SignCSRFails(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failSignCSR = true

	_, err = svc.Initialize(context.Background(), ca)
	var certErr *CertEncodeError
	assert.ErrorAs(t, err, &certErr)
}

// ---------------------------------------------------------------------------
// Partial claims do not transition to operational
// ---------------------------------------------------------------------------

func TestClaimShare_PartialClaimsStayEnrolling(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	ctx := context.Background()

	// Only claim for 2 out of 3 officers.
	for i := 0; i < 2; i++ {
		username := fmt.Sprintf("officer-%d", i)

		challenge, err := svc.BeginClaimCert(ctx, username)
		require.NoError(t, err)
		sig := signNonce(t, keys[i], challenge.Nonce)
		nonceHex := hex.EncodeToString(challenge.Nonce)
		_, err = svc.CompleteClaimCert(ctx, username, nonceHex, sig)
		require.NoError(t, err)

		_, err = svc.ClaimShare(ctx, username)
		require.NoError(t, err)
	}

	// Should still be enrolling since officer-2 has not claimed.
	assert.Equal(t, StateEnrolling, svc.State())
}

// ---------------------------------------------------------------------------
// Claim share without cert claim does not transition
// ---------------------------------------------------------------------------

func TestClaimShare_WithoutCertClaimStaysEnrolling(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	ctx := context.Background()

	// Claim all shares but only claim certs for first 2.
	for i := 0; i < 3; i++ {
		username := fmt.Sprintf("officer-%d", i)

		if i < 2 {
			challenge, err := svc.BeginClaimCert(ctx, username)
			require.NoError(t, err)
			sig := signNonce(t, keys[i], challenge.Nonce)
			nonceHex := hex.EncodeToString(challenge.Nonce)
			_, err = svc.CompleteClaimCert(ctx, username, nonceHex, sig)
			require.NoError(t, err)
		}

		_, err = svc.ClaimShare(ctx, username)
		require.NoError(t, err)
	}

	// officer-2 has not claimed their cert, so should remain enrolling.
	assert.Equal(t, StateEnrolling, svc.State())
}
