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
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"sync"
	"time"

	"github.com/awnumar/memguard"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

const (
	// challengeNonceSize is the number of random bytes in a challenge nonce.
	challengeNonceSize = 32

	// challengeTTL is the duration a challenge nonce remains valid.
	challengeTTL = 5 * time.Minute

	// credentialSOPin is the credential store key for the SO PIN.
	credentialSOPin = "ceremony/so-pin"

	// credentialUserPin is the credential store key for the user PIN.
	credentialUserPin = "ceremony/user-pin"
)

// CredentialServicer is the interface the ceremony needs from the
// credential service to store sensitive credentials. This is defined
// locally to avoid circular import dependencies.
type CredentialServicer interface {
	// StoreCredential persists a named credential value.
	StoreCredential(ctx context.Context, name string, value []byte) error
}

// CeremonyService orchestrates the initialization ceremony for the
// system. It manages the state machine transitions from awaiting_init
// through enrolling to operational, handles M-of-N Shamir secret
// sharing for officer enrollment, and coordinates certificate issuance
// and claim operations.
type CeremonyService struct {
	mu          sync.RWMutex
	config      *CeremonyConfig
	barrier     *seal.StorageBarrier
	credService CredentialServicer
	logger      *slog.Logger

	state      CeremonyState
	ca         CeremonyCA
	shareStore *memguardShareStore

	pendingCerts map[string]*PendingCert
	challenges   map[string]*ChallengeResponse

	thresholdRegistry *ThresholdRegistry
}

// NewCeremonyService creates a new CeremonyService with the given
// configuration, storage barrier, credential service, and logger.
// Returns an error if any required parameter is nil.
func NewCeremonyService(
	config *CeremonyConfig,
	barrier *seal.StorageBarrier,
	credService CredentialServicer,
	logger *slog.Logger,
) (*CeremonyService, error) {
	if config == nil {
		return nil, ErrNilConfig
	}
	if barrier == nil {
		return nil, ErrNilBarrier
	}
	if credService == nil {
		return nil, ErrNilCredentialService
	}
	if logger == nil {
		return nil, ErrNilLogger
	}

	return &CeremonyService{
		config:            config,
		barrier:           barrier,
		credService:       credService,
		logger:            logger,
		state:             StateAwaitingInit,
		shareStore:        newMemguardShareStore(),
		pendingCerts:      make(map[string]*PendingCert),
		challenges:        make(map[string]*ChallengeResponse),
		thresholdRegistry: NewThresholdRegistry(),
	}, nil
}

// State returns the current ceremony state.
func (s *CeremonyService) State() CeremonyState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// ThresholdRegistry returns the threshold registry for registering
// and looking up vendor-specific threshold initializers.
func (s *CeremonyService) ThresholdRegistry() *ThresholdRegistry {
	return s.thresholdRegistry
}

// SPKIPin computes the SPKI pin from the CA certificate. Returns
// ErrCANotInitialized if the CA has not been set via Initialize.
func (s *CeremonyService) SPKIPin() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.ca == nil {
		return "", ErrCANotInitialized
	}
	caCert, err := s.ca.CACertificate()
	if err != nil {
		return "", &CABundleError{Err: err}
	}
	return computeSPKIPin(caCert)
}

// Initialize performs the initialization ceremony. It validates the
// configuration, stores the CA reference, issues TLS and SO certificates,
// stores credentials, and either starts M-of-N enrollment or transitions
// directly to operational state for single-admin mode.
func (s *CeremonyService) Initialize(ctx context.Context, ca CeremonyCA) (*InitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != StateAwaitingInit {
		return nil, ErrAlreadyInitialized
	}

	if err := s.validateConfig(); err != nil {
		return nil, err
	}

	s.ca = ca

	// Issue TLS server certificate.
	tlsCert, err := ca.IssueCertificate(&IssueCertRequest{
		CommonName:   s.config.Hostname,
		SANs:         s.config.SANs,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ValidityDays: 365,
	})
	if err != nil {
		s.logger.Error("failed to issue TLS certificate", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}

	// Issue SO certificate.
	soCert, err := ca.IssueCertificate(&IssueCertRequest{
		CommonName:   "Security Officer",
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 365,
	})
	if err != nil {
		s.logger.Error("failed to issue SO certificate", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}

	// Compute SPKI pin from CA certificate.
	caCert, err := ca.CACertificate()
	if err != nil {
		s.logger.Error("failed to get CA certificate", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}
	pin, err := computeSPKIPin(caCert)
	if err != nil {
		s.logger.Error("failed to compute SPKI pin", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}

	// Get CA bundle PEM.
	caBundle, err := ca.CABundle()
	if err != nil {
		s.logger.Error("failed to get CA bundle", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}

	// Store SO and User PINs as credentials.
	if err := s.credService.StoreCredential(ctx, credentialSOPin, []byte(s.config.SOPin)); err != nil {
		s.logger.Error("failed to store SO PIN credential", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}
	if err := s.credService.StoreCredential(ctx, credentialUserPin, []byte(s.config.UserPin)); err != nil {
		s.logger.Error("failed to store User PIN credential", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}

	result := &InitResult{
		SPKIPin:    pin,
		CACertPEM:  caBundle,
		TLSCertPEM: tlsCert.CertPEM,
		SOCertPEM:  soCert.CertPEM,
		SOKeyPEM:   soCert.KeyPEM,
	}

	// Determine M-of-N vs single admin mode.
	if s.config.Threshold >= 2 && len(s.config.Officers) > 0 {
		if err := s.initializeMofN(ctx, result); err != nil {
			return nil, err
		}
	} else {
		if err := s.initializeSingleAdmin(ctx, result); err != nil {
			return nil, err
		}
	}

	s.logger.Info("ceremony initialized",
		slog.String("state", string(s.state)),
		slog.String("spki_pin", pin),
		slog.Int("share_count", result.ShareCount))

	return result, nil
}

// BeginClaimCert starts the certificate claim handshake for the given
// officer username. It generates a random nonce that the officer must
// sign with their CSR private key to prove possession.
func (s *CeremonyService) BeginClaimCert(ctx context.Context, username string) (*ChallengeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != StateEnrolling {
		return nil, ErrNotInEnrollingState
	}

	if username == "" {
		return nil, ErrMissingUsername
	}

	pending, ok := s.pendingCerts[username]
	if !ok {
		return nil, ErrPendingCertNotFound
	}
	if pending.Claimed {
		return nil, ErrCertAlreadyClaimed
	}

	nonce := make([]byte, challengeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrInitFailed
	}

	challenge := &ChallengeResponse{
		Nonce:     nonce,
		Username:  username,
		ExpiresAt: time.Now().Add(challengeTTL),
	}
	s.challenges[username] = challenge

	s.logger.Info("challenge issued for certificate claim",
		slog.String("username", username))

	return challenge, nil
}

// CompleteClaimCert completes the certificate claim handshake by
// verifying the officer's nonce signature and returning the certificate.
func (s *CeremonyService) CompleteClaimCert(
	ctx context.Context,
	username string,
	nonceHex string,
	signature []byte,
) (*ClaimCertResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != StateEnrolling {
		return nil, ErrNotInEnrollingState
	}

	if username == "" {
		return nil, ErrMissingUsername
	}

	challenge, ok := s.challenges[username]
	if !ok {
		return nil, ErrChallengeNotFound
	}

	if time.Now().After(challenge.ExpiresAt) {
		delete(s.challenges, username)
		return nil, ErrChallengeExpired
	}

	// Decode the nonce hex and verify it matches the challenge.
	providedNonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, ErrChallengeVerificationFailed
	}
	if len(providedNonce) != len(challenge.Nonce) {
		return nil, ErrChallengeVerificationFailed
	}
	for i := range providedNonce {
		if providedNonce[i] != challenge.Nonce[i] {
			return nil, ErrChallengeVerificationFailed
		}
	}

	pending, ok := s.pendingCerts[username]
	if !ok {
		return nil, ErrUserNotFound
	}
	if pending.Claimed {
		return nil, ErrCertAlreadyClaimed
	}

	// Verify the signature using the officer's CSR public key.
	if err := verifyNonceSignature(pending.CSRPubKey, challenge.Nonce, signature); err != nil {
		delete(s.challenges, username)
		return nil, err
	}

	pending.Claimed = true
	delete(s.challenges, username)

	caBundle, err := s.ca.CABundle()
	if err != nil {
		return nil, &CABundleError{Err: err}
	}

	s.logger.Info("certificate claimed",
		slog.String("username", username))

	return &ClaimCertResult{
		CertPEM:   pending.CertPEM,
		CACertPEM: caBundle,
	}, nil
}

// ClaimShare retrieves the Shamir share for the given officer username
// from the memguard share store. If all officers have claimed both
// their certificates and shares, the ceremony transitions to the
// operational state.
func (s *CeremonyService) ClaimShare(ctx context.Context, username string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != StateEnrolling {
		return nil, ErrNotInEnrollingState
	}

	if username == "" {
		return nil, ErrMissingUsername
	}

	// Verify the user is a known officer.
	if _, ok := s.pendingCerts[username]; !ok {
		return nil, ErrUserNotFound
	}

	shareJSON, err := s.shareStore.Claim(username)
	if err != nil {
		return nil, err
	}

	s.logger.Info("share claimed",
		slog.String("username", username))

	// Check if all claims are complete.
	if s.allClaimsComplete() {
		s.state = StateOperational
		s.logger.Info("all claims complete, ceremony operational")
	}

	return shareJSON, nil
}

// SignCSRInit signs a CSR during the ceremony for an officer or service.
// Requires the enrolling or operational state and valid SO PIN.
func (s *CeremonyService) SignCSRInit(
	ctx context.Context,
	username string,
	soPin string,
	csrPEM []byte,
	role string,
) (*SignCSRInitResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.state != StateEnrolling && s.state != StateOperational {
		return nil, ErrNotInitializedOrEnrolling
	}

	if s.ca == nil {
		return nil, ErrCANotInitialized
	}

	if username == "" {
		return nil, ErrMissingUsername
	}
	if soPin == "" {
		return nil, ErrInvalidSOPIN
	}
	if soPin != s.config.SOPin {
		return nil, ErrSOPINMismatch
	}
	if len(csrPEM) == 0 {
		return nil, ErrMissingCSRPEM
	}
	if role == "" {
		return nil, ErrMissingRole
	}

	profile, ok := roleProfileMap[role]
	if !ok {
		return nil, ErrInvalidRole
	}

	cert, err := s.ca.SignCSR(csrPEM, &SignCSROptions{
		Profile:      profile.profile,
		ValidityDays: profile.validityDays,
		KeyUsage:     profile.keyUsage,
		ExtKeyUsage:  profile.extKeyUsage,
	})
	if err != nil {
		s.logger.Error("CSR signing failed",
			slog.String("username", username),
			slog.String("role", role),
			slog.String("error", err.Error()))
		return nil, ErrCSRSigningFailed
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})

	s.logger.Info("CSR signed",
		slog.String("username", username),
		slog.String("role", role))

	return &SignCSRInitResult{
		CertPEM: certPEM,
	}, nil
}

// validateConfig checks that the ceremony configuration is valid.
func (s *CeremonyService) validateConfig() error {
	if s.config.SOPin == "" {
		return ErrInvalidSOPIN
	}
	if s.config.UserPin == "" {
		return ErrInvalidUserPIN
	}

	if s.config.Threshold >= 2 {
		if len(s.config.Officers) == 0 {
			return ErrNoOfficers
		}
		if s.config.Threshold > len(s.config.Officers) {
			return ErrInvalidThreshold
		}
		if err := validateUniqueOfficerUsernames(s.config.Officers); err != nil {
			return err
		}
	}

	return nil
}

// initializeMofN sets up M-of-N Shamir secret sharing. It splits the
// user PIN into shares, signs CSRs for each officer, and stores shares
// in the memguard share store. Transitions to StateEnrolling.
func (s *CeremonyService) initializeMofN(ctx context.Context, result *InitResult) error {
	shares, err := shamir.Split(
		[]byte(s.config.UserPin),
		s.config.Threshold,
		len(s.config.Officers),
	)
	if err != nil {
		return &ShamirSplitError{Err: err}
	}

	for i, officer := range s.config.Officers {
		// Parse the officer's CSR to extract the public key.
		block, _ := pem.Decode(officer.CSRPEM)
		if block == nil {
			return &CSRParseError{Err: ErrInvalidCSR}
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			return &CSRParseError{Err: err}
		}
		if err := csr.CheckSignature(); err != nil {
			return &CSRParseError{Err: err}
		}

		// Sign the officer's CSR.
		cert, err := s.ca.SignCSR(officer.CSRPEM, &SignCSROptions{
			Profile:      "custodian",
			ValidityDays: 365,
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		if err != nil {
			return &CertEncodeError{Err: err}
		}

		certPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert.Raw,
		})

		// Store the pending certificate.
		s.pendingCerts[officer.Username] = &PendingCert{
			Username:  officer.Username,
			CertPEM:   certPEM,
			CSRPubKey: csr.PublicKey,
			Claimed:   false,
		}

		// Marshal the share and store in memguard.
		shareJSON, err := json.Marshal(shares[i])
		if err != nil {
			return &ShareStoreError{Err: err}
		}
		if err := s.shareStore.Store(officer.Username, shareJSON); err != nil {
			return &ShareStoreError{Err: err}
		}
	}

	result.ShareCount = len(shares)
	result.State = StateEnrolling
	s.state = StateEnrolling

	s.logger.Info("M-of-N enrollment started",
		slog.Int("threshold", s.config.Threshold),
		slog.Int("officers", len(s.config.Officers)),
		slog.Int("shares", len(shares)))

	return nil
}

// initializeSingleAdmin transitions directly to the operational state
// when M-of-N is not configured.
func (s *CeremonyService) initializeSingleAdmin(_ context.Context, result *InitResult) error {
	result.ShareCount = 0
	result.State = StateOperational
	s.state = StateOperational

	s.logger.Info("single admin mode, ceremony operational")
	return nil
}

// allClaimsComplete checks whether all officers have claimed both
// their certificates and their shares.
func (s *CeremonyService) allClaimsComplete() bool {
	for username, pending := range s.pendingCerts {
		if !pending.Claimed {
			return false
		}
		if !s.shareStore.Claimed(username) {
			return false
		}
	}
	return true
}

// validateUniqueOfficerUsernames checks that all officer usernames are
// unique and non-empty.
func validateUniqueOfficerUsernames(officers []OfficerConfig) error {
	seen := make(map[string]struct{}, len(officers))
	for _, officer := range officers {
		if officer.Username == "" {
			return ErrMissingUsername
		}
		if _, exists := seen[officer.Username]; exists {
			return ErrDuplicateUsername
		}
		seen[officer.Username] = struct{}{}
	}
	return nil
}

// Cleanup destroys all remaining shares and clears sensitive state.
// This should be called when the ceremony is abandoned or the service
// is shutting down.
func (s *CeremonyService) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.shareStore.DestroyAll()

	// Wipe any remaining challenge nonces.
	for username, challenge := range s.challenges {
		memguard.WipeBytes(challenge.Nonce)
		delete(s.challenges, username)
	}
}
