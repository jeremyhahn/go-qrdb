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
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	// challengeNonceSize is the number of random bytes in a claim nonce.
	challengeNonceSize = 32

	// challengeTTL is the duration a claim nonce remains valid.
	challengeTTL = 5 * time.Minute
)

// CeremonyService orchestrates the initialization ceremony and is the share
// holder authority for the system barrier: it enrols holders (Security
// Officers and key custodians alike), distributes the unseal and recovery
// keys' shares to them as envelopes, runs the claim handshake, issues quorum
// challenges and authenticates every share submission. It is the barrier's
// seal.CeremonyDistributor and seal.HolderVerifier, and NewCeremonyService
// installs it as both.
type CeremonyService struct {
	// soPIN serializes and counts SO PIN checks; see soPINAttempts.
	soPIN soPINAttempts

	mu      sync.Mutex
	config  *CeremonyConfig
	barrier *seal.StorageBarrier
	logger  *slog.Logger

	state CeremonyState
	deps  HolderDeps
	pins  pin.PINBackend

	system   *holderScope
	recovery *holderScope

	thresholdRegistry *ThresholdRegistry
}

// Compile-time checks.
var (
	_ seal.CeremonyDistributor = (*CeremonyService)(nil)
	_ seal.HolderVerifier      = (*CeremonyService)(nil)
)

// NewCeremonyService creates the ceremony service for barrier and installs it
// as the barrier's distributor and holder verifier. Holder records are stored
// behind the barrier (encrypted), cached in memory while it is sealed.
func NewCeremonyService(config *CeremonyConfig, barrier *seal.StorageBarrier, logger *slog.Logger) (*CeremonyService, error) {
	if config == nil {
		return nil, ErrNilConfig
	}
	if barrier == nil {
		return nil, ErrNilBarrier
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	sealed := barrier.IsSealed
	system, err := newHolderScope(ScopeSystem, seal.DomainUnseal, tenantid.System, barrier, config.ShareTTL, sealed, logger)
	if err != nil {
		return nil, err
	}
	recovery, err := newHolderScope(ScopeRecovery, seal.DomainRecovery, tenantid.System, barrier, config.ShareTTL, sealed, logger)
	if err != nil {
		return nil, err
	}
	s := &CeremonyService{
		config:            config,
		barrier:           barrier,
		logger:            logger,
		state:             StateAwaitingInit,
		system:            system,
		recovery:          recovery,
		thresholdRegistry: NewThresholdRegistry(),
	}
	barrier.SetCeremonyDistributor(s)
	barrier.SetHolderVerifier(s)
	return s, nil
}

// Barrier returns the barrier the service is the distributor and holder
// verifier of.
func (s *CeremonyService) Barrier() *seal.StorageBarrier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.barrier
}

// Rebind moves the service to barrier, a new barrier instance over the same
// storage, and installs it there as the distributor and holder verifier.
// The enrolled holders, the envelopes not yet claimed and the pending
// challenges all stay with the service; holder records are read and written
// through barrier from now on, and are reconciled with its storage the first
// time it is seen unsealed.
//
// It exists for an in-process data-plane restart, such as the one a
// system-scope encryption migration performs: the restart builds a new
// barrier, and a new ceremony service would lose the envelopes the migration
// has just distributed, leaving a sealed barrier no holder could open.
// Rebinding to the barrier the service already serves changes nothing.
func (s *CeremonyService) Rebind(barrier *seal.StorageBarrier) {
	s.mu.Lock()
	if barrier == nil || barrier == s.barrier {
		s.mu.Unlock()
		return
	}
	s.barrier = barrier
	s.system.rebind(barrier)
	s.recovery.rebind(barrier)
	s.mu.Unlock()
	barrier.SetCeremonyDistributor(s)
	barrier.SetHolderVerifier(s)
}

// SetCA wires the CeremonyCA used to issue holder and login certificates.
// It never overwrites a CA already set: the first CA wired is authoritative.
func (s *CeremonyService) SetCA(ca CeremonyCA) {
	if ca == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deps.CA == nil {
		s.deps.CA = ca
	}
}

// SetTrustDomain sets the SPIFFE trust domain the CA assigns identities in.
func (s *CeremonyService) SetTrustDomain(trustDomain string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps.TrustDomain = trustDomain
}

// SetFactorRegistry installs the authentication factors holders may enrol
// and authenticate with (hardware factors come from go-xkms).
func (s *CeremonyService) SetFactorRegistry(r *authfactors.FactorRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps.Factors = r
}

// SetQuorumBeginLimiter installs the throttle quorum-challenge requests pass
// (HolderDeps.QuorumBeginLimiter). It is carried to every tenant and database
// holder authority built from HolderDeps.
func (s *CeremonyService) SetQuorumBeginLimiter(l QuorumBeginLimitFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps.QuorumBeginLimiter = l
}

// SetPINBackend installs the PIN backend. Without one, every PIN-gated
// ceremony operation fails closed with pin.ErrBackendNotConfigured.
func (s *CeremonyService) SetPINBackend(b pin.PINBackend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins = b
}

// HolderDeps returns the holder dependencies, for tenant ceremonies to share.
func (s *CeremonyService) HolderDeps() HolderDeps {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deps
}

// CA returns the CeremonyCA, or nil before one is wired.
func (s *CeremonyService) CA() CeremonyCA {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deps.CA
}

// State returns the current ceremony state.
func (s *CeremonyService) State() CeremonyState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// ThresholdRegistry returns the registry of vendor threshold initializers.
func (s *CeremonyService) ThresholdRegistry() *ThresholdRegistry {
	return s.thresholdRegistry
}

// SPKIPin computes the SPKI pin of the CA certificate.
func (s *CeremonyService) SPKIPin() (string, error) {
	ca := s.CA()
	if ca == nil {
		return "", ErrCANotInitialized
	}
	caCert, err := ca.CACertificate()
	if err != nil {
		return "", &CABundleError{Err: err}
	}
	return computeSPKIPin(caCert)
}

// scope returns the holder scope of domain.
func (s *CeremonyService) scope(domain seal.QuorumDomain) (*holderScope, error) {
	switch domain {
	case seal.DomainUnseal:
		return s.system, nil
	case seal.DomainRecovery:
		return s.recovery, nil
	default:
		return nil, &seal.QuorumOpInvalidError{Domain: domain}
	}
}

// Initialize performs the initialization ceremony: it establishes the SO and
// User PINs through the PINBackend (verifying them instead when barrier
// initialization already did), issues the server TLS certificate, and — with
// Threshold >= 2 — enrols Officers as share holders and initializes the
// barrier's Shamir custody, which splits the barrier UNSEAL KEY M-of-N and
// seals one envelope to each holder. Nothing else is split.
func (s *CeremonyService) Initialize(ctx context.Context, ca CeremonyCA, opts *InitializeOptions) (*InitResult, error) {
	if opts == nil {
		return nil, ErrInvalidSOPIN
	}
	if err := validateInitOptions(opts); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.state != StateAwaitingInit {
		s.mu.Unlock()
		return nil, ErrAlreadyInitialized
	}
	if ca == nil {
		s.mu.Unlock()
		return nil, ErrNilCA
	}
	if err := pin.Provision(s.pins, opts.SOPin, opts.UserPin); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.deps.CA = ca

	tlsCert, err := ca.IssueCertificate(ctx, &IssueCertRequest{
		CommonName:   opts.Hostname,
		SANs:         opts.SANs,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ValidityDays: 365,
	})
	if err != nil {
		s.mu.Unlock()
		s.logger.Error("failed to issue TLS certificate", slog.String("error", err.Error()))
		return nil, ErrInitFailed
	}
	caCert, err := ca.CACertificate()
	if err != nil {
		s.mu.Unlock()
		return nil, &CABundleError{Err: err}
	}
	spkiPin, err := computeSPKIPin(caCert)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	caBundle, err := ca.CABundle()
	if err != nil {
		s.mu.Unlock()
		return nil, &CABundleError{Err: err}
	}
	result := &InitResult{SPKIPin: spkiPin, CACertPEM: caBundle, TLSCertPEM: tlsCert.CertPEM}

	if opts.Threshold < 2 {
		s.becomeOperationalLocked(ctx)
		s.mu.Unlock()
		result.State = StateOperational
		s.logger.Info("ceremony initialized in single-admin mode")
		return result, nil
	}

	for _, o := range opts.Officers {
		reg, rerr := s.system.register(ctx, &s.deps, RegisterOfficerRequest{
			Username: o.Username,
			Role:     o.Role,
			CSRPEM:   o.CSRPEM,
			Enroller: opts.Enroller,
		})
		if rerr != nil {
			s.mu.Unlock()
			return nil, rerr
		}
		result.Holders = append(result.Holders, reg)
	}
	s.mu.Unlock()

	// InitializeShamir calls back into this service (the distributor), so
	// the lock is released first.
	s.barrier.SetShamirConfig(&seal.ShamirConfig{Threshold: opts.Threshold, TotalShares: len(opts.Officers)})
	params, err := s.barrier.InitializeShamir(ctx, opts.Policy)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.state = StateEnrolling
	s.mu.Unlock()
	result.ShareCount = params.ShareCount
	result.State = StateEnrolling
	s.logger.Info("ceremony initialized; share holders must claim their envelopes",
		slog.Int("threshold", params.Threshold), slog.Int("holders", params.TotalShares))
	return result, nil
}

// validateInitOptions checks the request before any work is done.
func validateInitOptions(opts *InitializeOptions) error {
	if opts.SOPin == "" {
		return ErrInvalidSOPIN
	}
	if opts.UserPin == "" {
		return ErrInvalidUserPIN
	}
	if opts.Threshold < 2 {
		return nil
	}
	if len(opts.Officers) == 0 {
		return ErrNoOfficers
	}
	if opts.Threshold > len(opts.Officers) {
		return ErrInvalidThreshold
	}
	seen := make(map[string]bool, len(opts.Officers))
	for _, o := range opts.Officers {
		if o.Username == "" {
			return ErrMissingUsername
		}
		if seen[o.Username] {
			return ErrDuplicateUsername
		}
		seen[o.Username] = true
	}
	return nil
}

// SignCSRInit issues a LOGIN certificate for username in the given RBAC role,
// under SO PIN authorization. The CA assigns the identity: CN = username,
// OU = [system, profile] and a SPIFFE user URI; nothing is taken from the CSR
// but its public key. The SO PIN is verified by the PINBackend (hashed,
// constant-time), never compared here.
func (s *CeremonyService) SignCSRInit(ctx context.Context, username, soPin string, csrPEM []byte, role string) (*SignCSRInitResult, error) {
	s.mu.Lock()
	state, deps, pins := s.state, s.deps, s.pins
	s.mu.Unlock()

	if state != StateEnrolling && state != StateOperational {
		return nil, ErrNotInitializedOrEnrolling
	}
	if deps.CA == nil {
		return nil, ErrCANotInitialized
	}
	if username == "" {
		return nil, ErrMissingUsername
	}
	if soPin == "" {
		return nil, ErrInvalidSOPIN
	}
	if err := s.soPIN.check(func() error { return pin.RequireSOPIN(pins, soPin) }); err != nil {
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
	ident, err := principalIdentity(deps.TrustDomain, tenantid.System, spiffe.KindUser, username, profile.profile)
	if err != nil {
		return nil, err
	}
	cert, err := deps.CA.SignCSR(ctx, csrPEM, &SignCSROptions{
		Subject:      ident.subject,
		URIs:         ident.uris,
		Profile:      profile.profile,
		ValidityDays: profile.validityDays,
		KeyUsage:     profile.keyUsage,
		ExtKeyUsage:  profile.extKeyUsage,
	})
	if err != nil {
		s.logger.Error("CSR signing failed", slog.String("username", username), slog.String("role", role), slog.String("error", err.Error()))
		return nil, ErrCSRSigningFailed
	}
	s.logger.Info("CSR signed", slog.String("username", username), slog.String("role", role))
	return &SignCSRInitResult{CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}, nil
}

// AuthorizeSOPIN verifies soPIN as the SO PIN that gates a governance
// operation on the barrier -- a rekey, a holder revocation. It refuses when
// no SO PIN has been provisioned (pin.ErrPINNotSet) or no PIN backend is
// wired (pin.ErrBackendNotConfigured), and it shares the lockout every SO PIN
// check of this ceremony counts against: once the free failures are spent,
// any SO PIN check, the right PIN included, is refused with
// *SOPINLockedError until the backoff passes.
func (s *CeremonyService) AuthorizeSOPIN(soPIN string) error {
	s.mu.Lock()
	pins := s.pins
	s.mu.Unlock()
	return s.soPIN.check(func() error { return pin.RequireProvisionedSOPIN(pins, soPIN) })
}

// RegisterOfficer enrols a share holder — Security Officer or key custodian —
// for domain's key. The CA issues the holder a certificate; no share exists
// until the next distribution of the domain (initialization, recovery-key
// generation or a rekey).
func (s *CeremonyService) RegisterOfficer(ctx context.Context, domain seal.QuorumDomain, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseClosedAnonymousEnrollmentLocked(ctx); err != nil {
		return nil, err
	}
	h, err := s.scope(domain)
	if err != nil {
		return nil, err
	}
	return h.register(ctx, &s.deps, req)
}

// ListOfficers returns every holder of domain with its claim state. It never
// includes share material.
func (s *CeremonyService) ListOfficers(ctx context.Context, domain seal.QuorumDomain) ([]*OfficerStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(domain)
	if err != nil {
		return nil, err
	}
	return h.status(ctx)
}

// BeginClaimShare issues the claim nonce to a holder presenting its enrolled
// certificate over mTLS.
func (s *CeremonyService) BeginClaimShare(ctx context.Context, domain seal.QuorumDomain, username, callerThumbprint string) (*ChallengeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(domain)
	if err != nil {
		return nil, err
	}
	return h.beginClaim(ctx, username, callerThumbprint)
}

// CompleteClaimShare verifies the holder's signature over the claim nonce and
// delivers its share envelope exactly once.
func (s *CeremonyService) CompleteClaimShare(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, callerThumbprint string) (*ShareClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(domain)
	if err != nil {
		return nil, err
	}
	res, err := h.completeClaim(ctx, username, nonceHex, signature, callerThumbprint)
	if err != nil {
		return nil, err
	}
	if s.state == StateEnrolling && s.system.allClaimed() {
		s.becomeOperationalLocked(ctx)
		s.logger.Info("all share envelopes claimed, ceremony operational")
	}
	return res, nil
}

// BeginQuorum issues the factor challenge a holder answers to submit a share
// to op in domain. It works while the barrier is sealed.
func (s *CeremonyService) BeginQuorum(ctx context.Context, domain seal.QuorumDomain, op seal.QuorumOp, username, factor string) (*QuorumChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(domain)
	if err != nil {
		return nil, err
	}
	return h.beginQuorum(ctx, &s.deps, s.barrier, op, username, factor)
}

// RevokeOfficer revokes a holder of domain. Revocation is a rekey: it opens a
// rekey round that needs a quorum of the REMAINING holders and, on
// completion, redistributes the domain's key to them without the revoked
// holder. threshold is the new M (zero keeps the current one).
func (s *CeremonyService) RevokeOfficer(ctx context.Context, domain seal.QuorumDomain, username string, threshold int) (*seal.QuorumRoundInfo, error) {
	if username == "" {
		return nil, ErrMissingUsername
	}
	return s.barrier.StartRekey(ctx, domain, seal.RekeyParams{Threshold: threshold, Remove: []string{username}})
}

// PrepareRoster implements seal.CeremonyDistributor.
func (s *CeremonyService) PrepareRoster(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(domain)
	if err != nil {
		return nil, err
	}
	return h.prepareRoster(ctx, domain, threshold, policy, exclude)
}

// Distribute implements seal.CeremonyDistributor.
func (s *CeremonyService) Distribute(ctx context.Context, secret *secure.Buffer, roster *seal.HolderRoster, digest []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(roster.Domain)
	if err != nil {
		return 0, err
	}
	return h.distribute(ctx, secret, roster, digest)
}

// Committed implements seal.CeremonyDistributor.
func (s *CeremonyService) Committed(ctx context.Context, roster *seal.HolderRoster, removed []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(roster.Domain)
	if err != nil {
		return err
	}
	return h.committed(ctx, roster, removed)
}

// VerifyHolder implements seal.HolderVerifier.
func (s *CeremonyService) VerifyHolder(ctx context.Context, domain seal.QuorumDomain, op seal.QuorumOp, holder string, roster *seal.HolderRoster, digest []byte, proof seal.HolderProof) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(domain)
	if err != nil {
		return err
	}
	return h.verifyHolder(ctx, &s.deps, domain, op, holder, roster, digest, proof)
}

// becomeOperationalLocked moves the ceremony to StateOperational and records
// that on the barrier (seal.StorageBarrier.MarkCeremonyOperational), where the
// boot-time sealed-window listener reads it after a restart, when this
// in-memory state is gone. Must be called with s.mu held.
//
// A failure to record it is logged, not returned: the transition has already
// happened -- on the claim path the last envelope has been released -- and
// EnrollmentOpen closes the anonymous enrollment exemption on an initialized
// barrier regardless, so only a ceremony that is operational on a barrier
// that is still uninitialized depends on the record alone.
func (s *CeremonyService) becomeOperationalLocked(ctx context.Context) {
	s.state = StateOperational
	if err := s.barrier.MarkCeremonyOperational(ctx); err != nil {
		s.logger.Error("failed to record the operational ceremony on the barrier; "+
			"holder enrollment stays closed while the barrier is initialized",
			slog.String("error", err.Error()))
	}
}

// EnrollmentOpen reports whether holder enrollment is still open to an
// unauthenticated caller -- the sealedwindow.PreOperationalOnly exemption the
// boot-time sealed-window listener grants officer and recovery-officer
// registration. RegisterOfficer has the CA issue a certificate for any CSR it
// is given, so the exemption is justified only while the enrollment ceremony
// it serves can still use a new holder.
//
// It is closed as soon as any of these holds, each read so that it survives a
// restart while sealed, when this service's in-memory state starts over:
//   - this ceremony is operational in memory;
//   - the barrier records an operational ceremony (MarkCeremonyOperational);
//   - the barrier is initialized: the shares a newly enrolled holder would
//     hold are distributed at initialization, and every later enrollment
//     (rekey, recovery) runs on the authenticated listener.
//
// It fails closed: an unreadable record is treated as closed.
func (s *CeremonyService) EnrollmentOpen(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// Cleanup destroys every held envelope and pending challenge.
func (s *CeremonyService) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.system.cleanup()
	s.recovery.cleanup()
}
