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
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"sync"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
	// soPIN serializes and counts SO PIN checks; see soPINAttempts.
	soPIN soPINAttempts

	mu          sync.RWMutex
	config      *CeremonyConfig
	barrier     *seal.StorageBarrier
	credService CredentialServicer
	logger      *slog.Logger

	state      CeremonyState
	ca         CeremonyCA
	shareStore *sealedShareStore

	pendingCerts map[string]*PendingCert
	challenges   map[string]*ChallengeResponse

	thresholdRegistry *ThresholdRegistry

	// officerRegistry persists officer enrollment metadata behind the
	// data-plane barrier. Lazy-initialized in Initialize.
	officerRegistry *OfficerRegistry

	// extCAPool holds CA pools used to validate externally-supplied
	// officer certificates. Configured via SetExternalCAPools.
	extCAPool *x509.CertPool

	// ext holds Phase-4 custodian and auth-factor dependencies.
	// Set via SetExtension after construction.
	ext *CeremonyExtension

	// recoveryShareStore holds recovery-scope Shamir share enclaves. Lazily
	// initialized on first recovery officer registration or distribution.
	recoveryShareStore *sealedShareStore

	// recoveryRegistry holds recovery-scope officer enrollment metadata.
	// Lazily initialized on first recovery officer registration.
	recoveryRegistry *OfficerRegistry
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
		shareStore:        newSealedShareStore(),
		pendingCerts:      make(map[string]*PendingCert),
		challenges:        make(map[string]*ChallengeResponse),
		thresholdRegistry: NewThresholdRegistry(),
	}, nil
}

// CA returns the CeremonyCA used by this service. It is safe to call before
// Initialize; the return value is nil until a CA has been set either via
// Initialize or directly by lifecycle wiring that calls SetCA.
func (s *CeremonyService) CA() CeremonyCA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ca
}

// SetCA wires the CeremonyCA used for officer CSR signing outside the full
// initialization ceremony. Lifecycle wiring calls it so that daemon
// deployments — where the node's cluster CA signs Security Officer client
// certificates — can register officers without running Initialize. It never
// overwrites a CA already set by Initialize: when a CA is present the call
// is a no-op so a ceremony-established CA remains authoritative.
func (s *CeremonyService) SetCA(ca CeremonyCA) {
	if ca == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ca != nil {
		return
	}
	s.ca = ca
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
//
// opts carries per-request parameters (the Security Officer and user PINs,
// plus the credential seal strategy, M-of-N threshold/officers, and TLS
// hostname/SANs). When a per-request field is non-empty (non-zero for
// Threshold, non-empty for the slice fields) it takes precedence over the
// corresponding CeremonyConfig field; empty/zero per-request fields fall
// back to the configured value. opts may be nil, in which case the
// configured values are used unchanged. If, after this resolution, a
// required PIN is still empty, validateConfig fails closed with
// ErrInvalidSOPIN / ErrInvalidUserPIN and no cryptographic work is
// performed; likewise a Threshold >= 2 with no resolved Officers fails
// closed with ErrNoOfficers rather than silently falling back to the
// single-admin path.
func (s *CeremonyService) Initialize(ctx context.Context, ca CeremonyCA, opts *InitializeOptions) (*InitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != StateAwaitingInit {
		return nil, ErrAlreadyInitialized
	}

	// Per-request values take precedence over the configured ones. We hold
	// the write lock and Initialize runs exactly once (state stays
	// StateAwaitingInit until the M-of-N/single-admin branch below
	// commits it), so persisting the effective values back into s.config
	// on SUCCESS is safe and keeps later operations that validate against
	// the configured SO PIN (e.g. SignCSRInit) consistent with the values
	// actually used at initialization. On a validateConfig failure the
	// mutation is rolled back so a subsequent Initialize call (e.g. a
	// retry with a different, valid request) is not left with orphaned
	// state from the failed attempt.
	if opts != nil {
		orig := *s.config
		if opts.SOPin != "" {
			s.config.SOPin = opts.SOPin
		}
		if opts.UserPin != "" {
			s.config.UserPin = opts.UserPin
		}
		if opts.CredentialSealStrategy != "" {
			s.config.CredentialSealStrategy = opts.CredentialSealStrategy
		}
		if opts.Threshold != 0 {
			s.config.Threshold = opts.Threshold
		}
		if len(opts.Officers) > 0 {
			s.config.Officers = opts.Officers
		}
		if opts.Hostname != "" {
			s.config.Hostname = opts.Hostname
		}
		if len(opts.SANs) > 0 {
			s.config.SANs = opts.SANs
		}

		if err := s.validateConfig(); err != nil {
			*s.config = orig
			return nil, err
		}
	} else if err := s.validateConfig(); err != nil {
		return nil, err
	}

	s.ca = ca

	// Issue TLS server certificate.
	tlsCert, err := ca.IssueCertificate(ctx, &IssueCertRequest{
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

// The username-only ClaimShare method that used to live here was DELETED.
// It released an officer's Shamir share to anyone who could name an enrolled
// officer, which defeats the M-of-N quorum the ceremony exists to enforce.
// The only supported share-claim path is the challenge-response flow in
// ceremony_officer.go: BeginClaimShare issues a single-use nonce bound to the
// caller's mTLS certificate thumbprint, and CompleteClaimShare re-verifies
// that thumbprint plus a signature over the nonce before releasing the share
// exactly once.

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
	if err := s.soPIN.check(soPin, s.config.SOPin); err != nil {
		return nil, err
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

	cert, _, err := signHolderCSR(ctx, s.ca, csrPEM, &SignCSROptions{
		TenantID:     tenantid.System,
		Username:     username,
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
		if err := validateOfficerAuthenticators(s.config.Officers); err != nil {
			return err
		}
	}

	return nil
}

// initializeMofN sets up M-of-N Shamir secret sharing. It splits the
// user PIN into shares, signs CSRs for each officer, and stores shares
// in the in-memory share store. Transitions to StateEnrolling.
func (s *CeremonyService) initializeMofN(ctx context.Context, result *InitResult) error {
	// SplitProtected neither copies nor wipes the secret, so this transient
	// copy of the PIN is ours to clear. It was previously left to the
	// collector.
	pin := []byte(s.config.UserPin)
	defer secure.Wipe(pin)

	shares, err := shamir.SplitProtected(pin, s.config.Threshold, len(s.config.Officers))
	if err != nil {
		return &ShamirSplitError{Err: err}
	}
	// Every share is one mlock'd allocation against RLIMIT_MEMLOCK — so a
	// share that outlives its use is not merely untidy, it is budget another
	// ceremony may need. Destroy the set however this function unwinds.
	defer func() {
		for _, share := range shares {
			share.Destroy()
		}
	}()

	// Lazily set up the persistent officer registry against the data-plane
	// barrier. The registry persists enrollment metadata only — secret share
	// material is never stored. Failure to construct a registry is fatal:
	// the ceremony cannot guarantee durable enrollment without it.
	if s.officerRegistry == nil {
		reg, regErr := NewOfficerRegistry(s.barrier)
		if regErr != nil {
			return regErr
		}
		s.officerRegistry = reg
	}

	// The barrier may legitimately be sealed at this point: in M-of-N mode
	// the daemon's barrier is unsealed AFTER officers submit shares.
	// Persistent registry writes are best-effort during initial enrollment;
	// callers can re-run RestoreFromRegistry after unseal if they want a
	// hot in-memory copy.
	registryWritable := !s.barrier.IsSealed()

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
		cert, _, err := signHolderCSR(ctx, s.ca, officer.CSRPEM, &SignCSROptions{
			TenantID:     tenantid.System,
			Username:     officer.Username,
			Profile:      holderprofile.Custodian,
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
			AuthenticatorType: officer.AuthenticatorType.Normalized(),
			AuthenticatorRef:  officer.AuthenticatorRef,
			Username:          officer.Username,
			CertPEM:           certPEM,
			CSRPubKey:         csr.PublicKey,
			Claimed:           false,
		}

		// Persist the public enrollment record. Secret share data is NOT
		// stored — it lives only in the in-memory sealed share store.
		thumb := certThumbprint(cert.Raw)
		pubPEM, marshalErr := marshalPublicKeyPEM(csr.PublicKey)
		if marshalErr != nil {
			return &CertEncodeError{Err: marshalErr}
		}
		persisted := &PersistentOfficer{
			Username:       officer.Username,
			CertThumbprint: thumb,
			PubKeyPEM:      pubPEM,
			ShareIndex:     i + 1,
			AuthFactor:     authFactorFromAuthenticator(officer.AuthenticatorType),
			EnrolledAt:     time.Now().UTC(),
		}
		if registryWritable {
			if err := s.officerRegistry.Save(ctx, persisted); err != nil {
				return err
			}
		} else {
			// Stash the record in the in-memory cache so RestoreFromRegistry
			// or post-unseal flushes can persist it durably later.
			s.officerRegistry.mu.Lock()
			s.officerRegistry.cache[persisted.Username] = persisted
			s.officerRegistry.mu.Unlock()
		}

		// Marshal the share and store in the sealed share store.
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
func (s *CeremonyService) initializeSingleAdmin(ctx context.Context, result *InitResult) error {
	result.ShareCount = 0
	result.State = StateOperational
	s.becomeOperationalLocked(ctx)

	s.logger.Info("single admin mode, ceremony operational")
	return nil
}

// becomeOperationalLocked moves the ceremony to StateOperational and records
// that on the barrier (seal.StorageBarrier.MarkCeremonyOperational), where the
// boot-time sealed-window listener reads it after a restart, when this
// in-memory state is gone. Must be called with s.mu held for writing.
//
// A failure to record it is logged, not returned: the transition has already
// happened -- on the claim path the last share has been released -- and
// EnrollmentOpen closes the anonymous enrollment exemption on an initialized
// barrier regardless, so only a ceremony that is operational on a barrier
// that is still uninitialized depends on the record alone.
func (s *CeremonyService) becomeOperationalLocked(ctx context.Context) {
	s.state = StateOperational
	if err := s.barrier.MarkCeremonyOperational(ctx); err != nil {
		s.logger.Error("failed to record the operational ceremony on the barrier; "+
			"officer enrollment stays closed while the barrier is initialized",
			slog.String("error", err.Error()))
	}
}

// EnrollmentOpen reports whether officer enrollment is still open to an
// unauthenticated caller -- the sealedwindow.PreOperationalOnly exemption the
// boot-time sealed-window listener grants officer and recovery-officer
// registration. RegisterOfficer CA-signs any CSR it is given and assigns a
// share index, so the exemption is justified only while the enrollment
// ceremony it serves can still use a new officer.
//
// It is closed as soon as any of these holds, each read so that it survives a
// restart while sealed, when this service's in-memory state starts over:
//   - this ceremony is operational in memory;
//   - the barrier records an operational ceremony (MarkCeremonyOperational);
//   - the barrier is initialized: the shares a newly enrolled officer would
//     hold are distributed at initialization, and every later enrollment
//     (rekey, recovery) runs on the authenticated listener.
//
// It fails closed: an unreadable record is treated as closed.
func (s *CeremonyService) EnrollmentOpen(ctx context.Context) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enrollmentOpenLocked(ctx)
}

// enrollmentProbeTimeout bounds EnrollmentOpen's reads of the barrier's
// plaintext store. The answer gates an anonymous request, and the boot-time
// listener holds its admission while it waits, so a store that stops
// answering must cost a refusal, not a request that never ends.
const enrollmentProbeTimeout = 2 * time.Second

// enrollmentOpenLocked is EnrollmentOpen with s.mu already held. Its reads
// run under ctx and enrollmentProbeTimeout, whichever ends first, and a read
// that does not finish counts as closed.
func (s *CeremonyService) enrollmentOpenLocked(ctx context.Context) bool {
	if s.state == StateOperational {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, enrollmentProbeTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return false
	}
	if marked, err := s.barrier.CeremonyOperational(ctx); err != nil || marked {
		return false
	}
	initialized, err := s.barrier.Initialized(ctx)
	return err == nil && !initialized
}

// refuseClosedAnonymousEnrollmentLocked returns ErrEnrollmentClosed when ctx
// is an unauthenticated request (sealedwindow.Anonymous) and enrollment has
// closed. The boot-time sealed-window listener's gate applies the same rule,
// but it samples enrollment before the request runs; this check runs under
// s.mu, in the same critical section as the registration it guards, so no
// anonymous registration can slip in once enrollment has closed. Must be
// called with s.mu held.
func (s *CeremonyService) refuseClosedAnonymousEnrollmentLocked(ctx context.Context) error {
	if sealedwindow.Anonymous(ctx) && !s.enrollmentOpenLocked(ctx) {
		return ErrEnrollmentClosed
	}
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

// validateOfficerAuthenticators checks that every officer's
// AuthenticatorType is either empty (meaning software default) or one
// of the known AuthenticatorType constants.
func validateOfficerAuthenticators(officers []OfficerConfig) error {
	for _, officer := range officers {
		if err := officer.AuthenticatorType.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// OfficerRegistry returns the persistent officer registry. It is non-nil
// only after a successful Initialize that ran in M-of-N mode, or after
// AttachOfficerRegistry has been called explicitly.
func (s *CeremonyService) OfficerRegistry() *OfficerRegistry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.officerRegistry
}

// AttachOfficerRegistry installs an existing persistent officer registry.
// The cmd/qrdbd boot path uses this so a registry built around the
// data-plane barrier survives across the unseal/restore handshake.
func (s *CeremonyService) AttachOfficerRegistry(reg *OfficerRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.officerRegistry = reg
}

// SetExternalCAPools configures the X.509 trust anchors used to validate
// officer certificates supplied to EnrollExternalOfficer. The pool MUST
// include both the additional_cas trust list and the mtls.client_ca_files
// trust list as documented in the unification plan; the caller is
// responsible for merging.
func (s *CeremonyService) SetExternalCAPools(pool *x509.CertPool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extCAPool = pool
}

// FlushPendingOfficers writes any officer records that were buffered in
// the registry's in-memory cache while the barrier was sealed. This is
// intended to be called once after the barrier becomes unsealed.
func (s *CeremonyService) FlushPendingOfficers(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.officerRegistry == nil {
		return nil
	}
	for _, officer := range s.officerRegistry.Cached() {
		if err := s.officerRegistry.Save(ctx, officer); err != nil {
			return err
		}
	}
	return nil
}

// RestoreFromRegistry rebuilds the in-memory pendingCerts map from the
// persistent officer registry. This is intended to be invoked once the
// barrier has been unsealed so that operations like BeginClaimCert and
// CompleteClaimCert can verify officer identities even if the daemon
// has been restarted between enrollment and claim.
//
// The caller is expected to have already populated the in-memory share
// store from out-of-band (e.g. from custodian-distributed shares); this
// method only rehydrates public certificate metadata.
func (s *CeremonyService) RestoreFromRegistry(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.officerRegistry == nil {
		return nil
	}

	if err := s.officerRegistry.Reload(ctx); err != nil {
		return err
	}

	for _, officer := range s.officerRegistry.Cached() {
		if _, exists := s.pendingCerts[officer.Username]; exists {
			continue
		}
		pubKey, err := parsePublicKeyPEM(officer.PubKeyPEM)
		if err != nil {
			s.logger.Warn("skipping persistent officer with invalid pubkey",
				slog.String("username", officer.Username),
				slog.String("error", err.Error()))
			continue
		}
		s.pendingCerts[officer.Username] = &PendingCert{
			Username:  officer.Username,
			CSRPubKey: pubKey,
			Claimed:   !officer.ClaimedAt.IsZero(),
		}
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
		secure.Wipe(challenge.Nonce)
		delete(s.challenges, username)
	}
}

// soPINMatches reports whether presented is the configured SO PIN, in time
// independent of where, and whether, the two differ. Both are hashed first, so
// the comparison runs over equal-length digests and leaks neither the PIN's
// length nor the length of the matching prefix: ceremony-sign-csr is reachable
// without authentication, and a byte-wise == there is a timing oracle.
func soPINMatches(presented, configured string) bool {
	p := sha256.Sum256([]byte(presented))
	c := sha256.Sum256([]byte(configured))
	return subtle.ConstantTimeCompare(p[:], c[:]) == 1
}
