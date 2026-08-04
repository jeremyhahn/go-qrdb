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

func (m *mockCA) IssueCertificate(_ context.Context, req *IssueCertRequest) (*IssuedCert, error) {
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

func (e *errorCA) SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	if e.failSignCSR {
		return nil, errors.New("sign CSR error")
	}
	return e.mockCA.SignCSR(ctx, csrPEM, opts)
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

func (e *errorCA) IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error) {
	e.issueCallCount++
	if e.failIssueCertificate {
		return nil, errors.New("issue certificate error")
	}
	if e.failOnIssueCall > 0 && e.issueCallCount == e.failOnIssueCall {
		return nil, errors.New("issue certificate error on call")
	}
	return e.mockCA.IssueCertificate(ctx, req)
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
	result, err := svc.Initialize(context.Background(), ca, nil)
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
	result, err := svc.Initialize(context.Background(), ca, nil)
	require.NoError(t, err)

	assert.Equal(t, StateEnrolling, result.State)
	assert.Equal(t, 3, result.ShareCount)
	assert.NotEmpty(t, result.SPKIPin)
	assert.Equal(t, StateEnrolling, svc.State())
}

// TestInitialize_OptsOverridesPersistIntoConfig proves that non-empty
// InitializeOptions fields (beyond SOPin/UserPin) override the corresponding
// CeremonyConfig fields and that the effective values are persisted back
// into s.config, exactly like the existing SOPin/UserPin precedence. This is
// the regression guard for the bug where CredentialSealStrategy, Threshold,
// Officers, Hostname, and SANs supplied on the Initialize call were silently
// dropped and only the construction-time config was ever honored.
func TestInitialize_OptsOverridesPersistIntoConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.CredentialSealStrategy = "config-strategy"
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newMockCA(t)
	_, err = svc.Initialize(context.Background(), ca, &InitializeOptions{
		Hostname:               "override.example",
		SANs:                   []string{"alt1", "alt2"},
		CredentialSealStrategy: "override-strategy",
	})
	require.NoError(t, err)

	assert.Equal(t, "override.example", svc.config.Hostname)
	assert.Equal(t, []string{"alt1", "alt2"}, svc.config.SANs)
	assert.Equal(t, "override-strategy", svc.config.CredentialSealStrategy)
}

// TestInitialize_OptsEmptyFieldsFallBackToConfig proves that empty/zero
// InitializeOptions fields leave the construction-time CeremonyConfig values
// unchanged (the fallback half of the precedence rule).
func TestInitialize_OptsEmptyFieldsFallBackToConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.CredentialSealStrategy = "config-strategy"
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newMockCA(t)
	_, err = svc.Initialize(context.Background(), ca, &InitializeOptions{})
	require.NoError(t, err)

	assert.Equal(t, "localhost", svc.config.Hostname)
	assert.Equal(t, "config-strategy", svc.config.CredentialSealStrategy)
}

// TestInitialize_OptsThresholdAndOfficersOverrideConfig proves that a
// per-request Threshold/Officers pair drives the M-of-N enrollment path even
// when the construction-time config is single-admin.
func TestInitialize_OptsThresholdAndOfficersOverrideConfig(t *testing.T) {
	cfg := defaultConfig() // single-admin: no Threshold/Officers configured
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	officers := make([]OfficerConfig, 3)
	for i := 0; i < 3; i++ {
		username := fmt.Sprintf("opt-officer-%d", i)
		csrPEM, _ := generateCSRPEM(t, username)
		officers[i] = OfficerConfig{Username: username, CSRPEM: csrPEM}
	}

	ca := newMockCA(t)
	result, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		Threshold: 2,
		Officers:  officers,
	})
	require.NoError(t, err)

	assert.Equal(t, StateEnrolling, result.State)
	assert.Equal(t, 3, result.ShareCount)
	assert.Equal(t, StateEnrolling, svc.State())
}

// TestInitialize_OptsThresholdWithoutOfficers_FailsClosed proves that a
// per-request Threshold >= 2 with no resolved Officers (neither supplied on
// the request nor configured) fails closed with the typed ErrNoOfficers
// rather than silently falling back to the single-admin path — the fix for
// the bug where `qrdb init --barrier-strategy shamir --shares N --threshold
// M` used to silently ignore the request and report ShareCount: 0.
func TestInitialize_OptsThresholdWithoutOfficers_FailsClosed(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{Threshold: 2})
	assert.ErrorIs(t, err, ErrNoOfficers)
}

// TestInitialize_OptsFailedValidation_RollsBackConfig proves that a failed
// opts-driven Initialize call (validateConfig error) does not leave the
// per-request overrides mutated into s.config. Without the rollback a
// failed Threshold-only attempt would otherwise "poison" the next,
// otherwise-valid Initialize call on the same service instance — the
// service remains at StateAwaitingInit after a failure, so a caller can
// legitimately retry.
func TestInitialize_OptsFailedValidation_RollsBackConfig(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newMockCA(t)

	// First attempt: Threshold with no Officers fails closed.
	_, err = svc.Initialize(context.Background(), ca, &InitializeOptions{Threshold: 2})
	require.ErrorIs(t, err, ErrNoOfficers)
	assert.Equal(t, StateAwaitingInit, svc.State())

	// Second attempt: no opts at all (single-admin). If the first attempt's
	// Threshold=2 had leaked into s.config, this would now also fail with
	// ErrNoOfficers even though nothing in this call requested M-of-N.
	result, err := svc.Initialize(context.Background(), ca, nil)
	require.NoError(t, err, "a failed opts-driven attempt must not poison a subsequent call")
	assert.Equal(t, StateOperational, result.State)
	assert.Equal(t, 0, result.ShareCount)
}

func TestInitialize_AlreadyInitialized(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newMockCA(t)
	_, err = svc.Initialize(context.Background(), ca, nil)
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), ca, nil)
	assert.ErrorIs(t, err, ErrAlreadyInitialized)
}

func TestInitialize_EmptySOPin(t *testing.T) {
	cfg := defaultConfig()
	cfg.SOPin = ""
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	assert.ErrorIs(t, err, ErrInvalidSOPIN)
}

func TestInitialize_EmptyUserPin(t *testing.T) {
	cfg := defaultConfig()
	cfg.UserPin = ""
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	assert.ErrorIs(t, err, ErrInvalidUserPIN)
}

func TestInitialize_MofN_NoOfficers(t *testing.T) {
	cfg := defaultConfig()
	cfg.Threshold = 2
	cfg.Officers = nil
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	assert.ErrorIs(t, err, ErrNoOfficers)
}

func TestInitialize_MofN_ThresholdExceedsOfficers(t *testing.T) {
	cfg, _ := mofnConfig(t, 5, 3) // threshold > officers
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	var csrErr *CSRParseError
	assert.ErrorAs(t, err, &csrErr)
}

func TestInitialize_IssueTLSCertFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failOnIssueCall = 1 // fail on TLS cert issuance
	_, err = svc.Initialize(context.Background(), ca, nil)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_IssueSOCertFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failOnIssueCall = 2 // fail on SO cert issuance
	_, err = svc.Initialize(context.Background(), ca, nil)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_CACertificateFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failCACertificate = true
	_, err = svc.Initialize(context.Background(), ca, nil)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_CABundleFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failCABundle = true
	_, err = svc.Initialize(context.Background(), ca, nil)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_StoreSOPinCredentialFails(t *testing.T) {
	svc, err := NewCeremonyService(
		defaultConfig(), testBarrier(t),
		&errorCredentialService{failOn: "ceremony/so-pin"},
		testLogger(),
	)
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	assert.ErrorIs(t, err, ErrInitFailed)
}

func TestInitialize_StoreUserPinCredentialFails(t *testing.T) {
	svc, err := NewCeremonyService(
		defaultConfig(), testBarrier(t),
		&errorCredentialService{failOn: "ceremony/user-pin"},
		testLogger(),
	)
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	assert.ErrorIs(t, err, ErrInitFailed)
}

// ---------------------------------------------------------------------------
// BeginClaimCert tests
// ---------------------------------------------------------------------------

func TestBeginClaimCert_Success(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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
	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	_, err = svc.BeginClaimCert(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrPendingCertNotFound)
}

func TestBeginClaimCert_EmptyUsername(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	_, err = svc.CompleteClaimCert(context.Background(), "", "abc", []byte("sig"))
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestCompleteClaimCert_NoChallengeExists(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	_, err = svc.CompleteClaimCert(context.Background(), "officer-0", "abc", []byte("sig"))
	assert.ErrorIs(t, err, ErrChallengeNotFound)
}

func TestCompleteClaimCert_InvalidNonceHex(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	_, err = svc.ClaimShare(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestClaimShare_EmptyUsername(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	_, err = svc.ClaimShare(context.Background(), "")
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestClaimShare_AlreadyClaimed(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "new-user")

	result, err := svc.SignCSRInit(context.Background(), "new-user", "test-so-pin", csrPEM, "operator")
	require.NoError(t, err)
	assert.NotEmpty(t, result.CertPEM)
}

func TestSignCSRInit_WrongSOPin(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "wrong-pin", csrPEM, "admin")
	assert.ErrorIs(t, err, ErrSOPINMismatch)
}

func TestSignCSRInit_EmptySOPin(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "", csrPEM, "admin")
	assert.ErrorIs(t, err, ErrInvalidSOPIN)
}

func TestSignCSRInit_InvalidRole(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", csrPEM, "not-a-real-role")
	assert.ErrorIs(t, err, ErrInvalidRole)
}

func TestSignCSRInit_EmptyCSR(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", nil, "admin")
	assert.ErrorIs(t, err, ErrMissingCSRPEM)
}

func TestSignCSRInit_EmptyRole(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "user-1")

	_, err = svc.SignCSRInit(context.Background(), "user-1", "test-so-pin", csrPEM, "")
	assert.ErrorIs(t, err, ErrMissingRole)
}

func TestSignCSRInit_EmptyUsername(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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
	_, err = svc.Initialize(context.Background(), ca, nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	pin, err := svc.SPKIPin()
	require.NoError(t, err)
	assert.NotEmpty(t, pin)
}

func TestSPKIPin_CACertificateFails(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	_, err = svc.Initialize(context.Background(), ca, nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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
	_, err = svc.Initialize(context.Background(), ca, nil)
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

	_, err = svc.Initialize(context.Background(), ca, nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
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

// ---------------------------------------------------------------------------
// AuthenticatorType tests
// ---------------------------------------------------------------------------

func TestAuthenticatorType_Validate(t *testing.T) {
	tests := []struct {
		name    string
		input   AuthenticatorType
		wantErr error
	}{
		{name: "empty is valid", input: "", wantErr: nil},
		{name: "piv is valid", input: AuthenticatorPIV, wantErr: nil},
		{name: "fido2 is valid", input: AuthenticatorFIDO2, wantErr: nil},
		{name: "software is valid", input: AuthenticatorSoftware, wantErr: nil},
		{name: "bogus value rejected", input: AuthenticatorType("bogus"), wantErr: ErrInvalidAuthenticatorType},
		{name: "uppercase rejected", input: AuthenticatorType("PIV"), wantErr: ErrInvalidAuthenticatorType},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.input.Validate()
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestAuthenticatorType_Normalized(t *testing.T) {
	tests := []struct {
		name  string
		input AuthenticatorType
		want  AuthenticatorType
	}{
		{name: "empty maps to software", input: "", want: AuthenticatorSoftware},
		{name: "piv round trips", input: AuthenticatorPIV, want: AuthenticatorPIV},
		{name: "fido2 round trips", input: AuthenticatorFIDO2, want: AuthenticatorFIDO2},
		{name: "software round trips", input: AuthenticatorSoftware, want: AuthenticatorSoftware},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.input.Normalized()
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestValidateOfficerAuthenticators_Valid ensures the config-level helper
// accepts all known authenticator types (including the empty default).
func TestValidateOfficerAuthenticators_Valid(t *testing.T) {
	officers := []OfficerConfig{
		{Username: "alice", AuthenticatorType: AuthenticatorPIV, AuthenticatorRef: "9a"},
		{Username: "bob", AuthenticatorType: AuthenticatorFIDO2, AuthenticatorRef: "cred-id-xyz"},
		{Username: "carol", AuthenticatorType: AuthenticatorSoftware},
		{Username: "dave"}, // empty defaults to software
	}
	err := validateOfficerAuthenticators(officers)
	assert.NoError(t, err)
}

// TestValidateOfficerAuthenticators_Invalid ensures the config-level helper
// rejects unknown authenticator types.
func TestValidateOfficerAuthenticators_Invalid(t *testing.T) {
	officers := []OfficerConfig{
		{Username: "alice", AuthenticatorType: AuthenticatorPIV},
		{Username: "bob", AuthenticatorType: AuthenticatorType("yubikey-direct")},
	}
	err := validateOfficerAuthenticators(officers)
	assert.ErrorIs(t, err, ErrInvalidAuthenticatorType)
}

// TestInitialize_MofN_InvalidAuthenticatorType ensures Initialize rejects
// a ceremony configuration with an unknown authenticator type before any
// cryptographic work happens.
func TestInitialize_MofN_InvalidAuthenticatorType(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	cfg.Officers[0].AuthenticatorType = AuthenticatorType("bogus")

	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	assert.ErrorIs(t, err, ErrInvalidAuthenticatorType)
}

// TestInitialize_MofN_AuthenticatorTypeRoundTrip asserts that
// AuthenticatorType + AuthenticatorRef on OfficerConfig survive ceremony
// enrollment and end up on the corresponding PendingCert. Empty types
// are normalized to AuthenticatorSoftware.
func TestInitialize_MofN_AuthenticatorTypeRoundTrip(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	cfg.Officers[0].AuthenticatorType = AuthenticatorPIV
	cfg.Officers[0].AuthenticatorRef = "9a"
	cfg.Officers[1].AuthenticatorType = AuthenticatorFIDO2
	cfg.Officers[1].AuthenticatorRef = "cred-id-xyz"
	// Officers[2] is left with empty AuthenticatorType — should default
	// to software via Normalized().

	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	pending0 := svc.pendingCerts[cfg.Officers[0].Username]
	require.NotNil(t, pending0)
	assert.Equal(t, AuthenticatorPIV, pending0.AuthenticatorType)
	assert.Equal(t, "9a", pending0.AuthenticatorRef)

	pending1 := svc.pendingCerts[cfg.Officers[1].Username]
	require.NotNil(t, pending1)
	assert.Equal(t, AuthenticatorFIDO2, pending1.AuthenticatorType)
	assert.Equal(t, "cred-id-xyz", pending1.AuthenticatorRef)

	pending2 := svc.pendingCerts[cfg.Officers[2].Username]
	require.NotNil(t, pending2)
	assert.Equal(t, AuthenticatorSoftware, pending2.AuthenticatorType,
		"empty AuthenticatorType should normalize to software")
	assert.Equal(t, "", pending2.AuthenticatorRef)
}

// TestMofN_ClaimCertFlow_WithAuthenticatorType ensures the existing
// challenge-response claim flow is unaffected by the new authenticator
// metadata — signature verification remains backend-agnostic.
func TestMofN_ClaimCertFlow_WithAuthenticatorType(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	cfg.Officers[0].AuthenticatorType = AuthenticatorPIV
	cfg.Officers[0].AuthenticatorRef = "9a"

	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.Initialize(ctx, newMockCA(t), nil)
	require.NoError(t, err)

	username := cfg.Officers[0].Username
	challenge, err := svc.BeginClaimCert(ctx, username)
	require.NoError(t, err)

	sig := signNonce(t, keys[0], challenge.Nonce)
	nonceHex := hex.EncodeToString(challenge.Nonce)

	result, err := svc.CompleteClaimCert(ctx, username, nonceHex, sig)
	require.NoError(t, err)
	assert.NotEmpty(t, result.CertPEM)
	assert.NotEmpty(t, result.CACertPEM)
}

// ---------------------------------------------------------------------------
// SetCA
// ---------------------------------------------------------------------------

// TestSetCA_WiresWhenUnset verifies that lifecycle wiring can install a
// CeremonyCA on a service that has not run Initialize, enabling officer
// registration in daemon deployments.
func TestSetCA_WiresWhenUnset(t *testing.T) {
	svc := newTestService(t)
	require.Nil(t, svc.CA())

	ca := newMockCA(t)
	svc.SetCA(ca)
	assert.Same(t, CeremonyCA(ca), svc.CA())
}

// TestSetCA_NilIsNoOp verifies that a nil CA argument leaves the service
// unchanged in both the unset and already-set states.
func TestSetCA_NilIsNoOp(t *testing.T) {
	svc := newTestService(t)
	svc.SetCA(nil)
	assert.Nil(t, svc.CA())

	ca := newMockCA(t)
	svc.SetCA(ca)
	svc.SetCA(nil)
	assert.Same(t, CeremonyCA(ca), svc.CA())
}

// TestSetCA_DoesNotOverwrite verifies that a CA already installed (e.g. by
// Initialize during the full ceremony) remains authoritative — SetCA from
// lifecycle wiring must never clobber it.
func TestSetCA_DoesNotOverwrite(t *testing.T) {
	svc := newTestService(t)

	first := newMockCA(t)
	second := newMockCA(t)
	svc.SetCA(first)
	svc.SetCA(second)
	assert.Same(t, CeremonyCA(first), svc.CA())
}

// TestSetCA_DoesNotOverwriteInitializeCA verifies the Initialize-then-SetCA
// ordering: the ceremony-established CA wins.
func TestSetCA_DoesNotOverwriteInitializeCA(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	initCA := newMockCA(t)
	_, err = svc.Initialize(context.Background(), initCA, nil)
	require.NoError(t, err)

	svc.SetCA(newMockCA(t))
	assert.Same(t, CeremonyCA(initCA), svc.CA())
}
