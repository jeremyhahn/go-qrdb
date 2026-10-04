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
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
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

	// lastAdminCertPEM is the bootstrap admin's most recently issued or
	// adopted certificate (go-qrdb#548), held ONLY in memory -- see
	// LastIssuedAdminCertPEM.
	lastAdminCertPEM []byte

	// lastAdminSource records which path produced lastAdminCertPEM.
	lastAdminSource AdminCredentialSource
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

// SetCA wires the CeremonyCA used to issue holder and login certificates
// (SignCSRInit issues either, by role).
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

// SetFactorRegistry installs the authentication factors holders may enrol
// and authenticate with (hardware factors come from go-xkms).
func (s *CeremonyService) SetFactorRegistry(r *authfactors.FactorRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps.Factors = r
}

// SetQuorumLimiter installs the throttle quorum-challenge requests pass
// (HolderDeps.QuorumLimiter). It is carried to every tenant and database
// holder authority built from HolderDeps.
func (s *CeremonyService) SetQuorumLimiter(l QuorumLimitFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps.QuorumLimiter = l
}

// SetAuditLogger installs the compliance audit trail every holder event is
// recorded on (HolderDeps.Audit). It is carried to every tenant and database
// holder authority built from HolderDeps. Unlike the CA it replaces what was
// installed: a composer that reopens its trail hands the ceremony the new one.
func (s *CeremonyService) SetAuditLogger(l audit.AuditLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps.Audit = l
}

// SetAuditFailurePolicy installs the node's audit failure posture
// (HolderDeps.AuditPolicy): what a holder operation recorded before it takes
// effect does when the trail cannot record it. It is carried to every tenant
// and database holder authority built from HolderDeps.
func (s *CeremonyService) SetAuditFailurePolicy(p audit.FailurePolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps.AuditPolicy = p
}

// SetPINBackend installs the PIN backend, decorated with the SO PIN lockout
// (pin.NewLockoutBackend, idempotent: a backend the composer already
// decorated keeps its one set of counters). Without one, every PIN-gated
// ceremony operation fails closed with pin.ErrBackendNotConfigured.
func (s *CeremonyService) SetPINBackend(b pin.PINBackend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins = pin.NewLockoutBackend(b)
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

// ReplaceCA swaps the CeremonyCA used for pre-operational signing with ca,
// but ONLY while the ceremony has not yet been initialized
// (State() == StateAwaitingInit). Once Initialize has established its own
// CA, that CA is authoritative and ReplaceCA is a no-op — the same
// invariant SetCA protects, generalized to a caller that needs to swap a
// PREVIOUSLY set convenience CA for a later, better one without waiting for
// the ceremony to ever run.
//
// This is what lets cmd/qrdbd move ceremony-sign-csr off the bootstrap
// window's cakey.StaticProvider-backed CeremonyCA (the in-memory cluster
// CA key, held before any store exists) onto the replicated store's
// cakey.SoftwareProvider (or an injected HSM/TPM/KMS provider) once that
// store is live: SetCA's own "never overwrite" rule, read literally, would
// make the bootstrap CA permanent for the life of the process, because it
// was the first value SetCA ever saw.
func (s *CeremonyService) ReplaceCA(ca CeremonyCA) {
	if ca == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateAwaitingInit {
		return
	}
	s.deps.CA = ca
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
func (s *CeremonyService) SPKIPin(ctx context.Context) (string, error) {
	ca := s.CA()
	if ca == nil {
		return "", ErrCANotInitialized
	}
	caCert, err := ca.CACertificate(ctx)
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

// resolveCABundlePEM returns the trust bundle a presented BYOC admin
// certificate validates against when AdminCertPoolPEM is empty: a
// configured CeremonyCA's own bundle, or this node's configured client-CA
// trust anchor on a BYO-TLS node (ca == nil). Read-only -- ca.CABundle() has
// no side effect on the CA or this node -- so Initialize can call it before
// provisioning the PINs or bringing the system barrier up (go-qrdb#548
// review: B3).
func (s *CeremonyService) resolveCABundlePEM(ca CeremonyCA) ([]byte, error) {
	if ca == nil {
		return s.config.ClientCACertPEM, nil
	}
	caBundle, err := ca.CABundle()
	if err != nil {
		return nil, &CABundleError{Err: err}
	}
	return caBundle, nil
}

// Initialize performs the initialization ceremony: it establishes the SO and
// User PINs through the PINBackend (verifying them instead when barrier
// initialization already did), brings the system barrier up for the
// single-admin path (go-qrdb#548: qrdb init is the ONLY system bootstrap),
// resolves the server TLS identity (a configured CeremonyCA self-issues it;
// a BYO-TLS node with ca == nil pins its own configured leaf instead),
// issues the bootstrap admin's certificate through exactly one of the
// CSR-sign / BYOC / generate paths (issueAdminCredential), and — with
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

	// Resolve the CA bundle BYOC validates against, and validate every
	// admin-credential input against it -- selection conflicts,
	// none-selected, the BYOC chain, and requires-a-CA -- BEFORE this call
	// provisions the PINs, brings the system barrier up, or issues the TLS
	// certificate (go-qrdb#548 review: B3, "init is atomic"). Both calls are
	// read-only/pure: ca.CABundle() reads the CA's own bundle with no side
	// effect, and validateAdminCredentialInputs never signs or generates
	// anything. caBundle is reused below rather than refetched once the TLS
	// certificate is actually issued.
	caBundle, err := s.resolveCABundlePEM(ca)
	if err != nil {
		return nil, err
	}
	if err := validateAdminCredentialInputs(ca, opts, caBundle); err != nil {
		return nil, err
	}

	// A BYO-TLS node (ca == nil) has no CA to self-issue a TLS certificate
	// with, so its OWN configured leaf (s.config.ServerCertPEM) must
	// already be present and parseable. Checked here, alongside the other
	// pure, no-side-effect validation above and for the same reason
	// (go-qrdb#548 review B3, "init is atomic"; go-qrdb#560 review item
	// 8): this used to be checked only once the ca != nil branch was
	// skipped, further down, by which point PIN provisioning and -- for
	// the single-admin path -- system barrier initialization had already
	// run. A misconfigured BYO-TLS node was left with both side effects
	// already committed on a call that reported failure. serverCert is
	// reused below (once past the lock) rather than reparsed.
	var serverCert *x509.Certificate
	if ca == nil {
		block, _ := pem.Decode(s.config.ServerCertPEM)
		if block == nil {
			return nil, &NoServerCertificateError{}
		}
		serverCert, err = x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, &NoServerCertificateError{Cause: err}
		}
	}

	s.mu.Lock()
	if s.state != StateAwaitingInit {
		s.mu.Unlock()
		return nil, ErrAlreadyInitialized
	}
	if err := pin.Provision(ctx, s.pins, opts.SOPin, opts.UserPin); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.deps.CA = ca

	// isMofN is decided once and reused below: the barrier initialization
	// this call performs branches on it, exactly like the officer
	// enrollment further down.
	isMofN := opts.Threshold >= 2

	// Initialize the system barrier (go-qrdb#548): qrdb init is the ONLY
	// system bootstrap, running in the sealed window before anything is
	// unsealed. `barrier init` refuses the system barrier unconditionally
	// and points here instead; `tenant barrier-init` is the separate,
	// untouched command for a tenant's own barrier.
	//
	// M-of-N is excepted: the barrier legitimately stays sealed through
	// enrollment and unseals later as officers reconstruct it from their
	// shares -- a single operator-supplied secret here would defeat the
	// quorum requirement that mode exists for.
	if !isMofN {
		if err := s.initializeSystemBarrierLocked(ctx, opts.Secret); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}

	// Resolve the TLS certificate, SPKI pin and CA bundle this call
	// returns. A CeremonyCA self-issues the node's TLS certificate and
	// pins its OWN certificate (the issuer): any certificate it issues
	// chains to it, so pinning the CA is the stable, reusable pin. A
	// BYO-TLS node (ca == nil) has no such CA -- the operator's leaf IS
	// the server identity, and this process holds no key to self-issue a
	// different one with -- so it pins THAT leaf's own public key
	// instead, and returns it, and the node's configured client-CA trust
	// anchor, unchanged rather than self-issuing or deriving anything.
	// caBundle was already resolved (and used to validate any BYOC admin
	// certificate) above, before the lock -- reused here rather than
	// refetched, so ca.CABundle() is called at most once per Initialize.
	var tlsCertPEM []byte
	var spkiPin string
	if ca != nil {
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
		tlsCertPEM = tlsCert.CertPEM

		caCert, err := ca.CACertificate(ctx)
		if err != nil {
			s.mu.Unlock()
			return nil, &CABundleError{Err: err}
		}
		spkiPin, err = computeSPKIPin(caCert)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
	} else {
		// serverCert was already resolved and validated up front, before
		// any side effect ran (see above): reused here, never reparsed.
		spkiPin, err = computeSPKIPin(serverCert)
		if err != nil {
			s.mu.Unlock()
			s.logger.Error("failed to compute SPKI pin from server certificate", slog.String("error", err.Error()))
			return nil, ErrInitFailed
		}
		tlsCertPEM = s.config.ServerCertPEM
	}

	result := &InitResult{SPKIPin: spkiPin, CACertPEM: caBundle, TLSCertPEM: tlsCertPEM}

	// Issue the bootstrap admin's certificate (go-qrdb#548). Signing/adopting
	// happens HERE, synchronously, where the CA is available; binding it to
	// the admin's user record happens later at first boot, once the admin
	// account exists (sign-csr-style identity minting needs no account; only
	// binding does).
	//
	// The result is kept ONLY in memory (s.lastAdminCert*) -- NOT written to
	// the seal/credential store, which is per-node by design (every cluster
	// node holds its own system barrier and master key, never shared) and so
	// cannot carry a record the Raft LEADER's first boot needs to read,
	// because initialize may run against any node. The composer (cmd/qrdbd,
	// sdk/go/node) is responsible for writing LastIssuedAdminCertPEM into the
	// REPLICATED system store as soon as it exists, which is durable, works
	// identically in standalone and Raft mode, and survives backup/restore.
	// A crash between this call returning and that write landing loses the
	// in-memory value; the admin-credential step is retriable (go-qrdb#548,
	// ReissueAdminCredential) until an admin binding exists, so that gap is
	// recoverable rather than a permanent loss. The key, when generated, is
	// NEVER written anywhere by this package.
	adminCertPEM, adminKeyPEM, adminSource, err := issueAdminCredential(ctx, ca, opts, caBundle)
	if err != nil {
		s.mu.Unlock()
		s.logger.Error("bootstrap admin credential issuance failed", slog.String("error", err.Error()))
		return nil, err
	}
	s.lastAdminCertPEM = adminCertPEM
	s.lastAdminSource = adminSource
	result.AdminCertPEM = adminCertPEM
	result.AdminKeyPEM = adminKeyPEM
	result.AdminCredentialSource = adminSource
	s.logger.Info("ceremony: bootstrap admin credential issued",
		slog.String("source", string(adminSource)))
	if adminSource == AdminCredentialGenerated {
		// The generate path is reachable from every transport, not only
		// `qrdb init`'s own client-side warning (cmd/qrdb/init.go): the
		// server itself must say, in its own log, that this credential does
		// not meet FedRAMP, HIPAA or PCI DSS 4.0 and is dev/test/evaluation
		// only (docs/firstboot/user.md).
		s.logger.Warn("ceremony: bootstrap admin credential GENERATED in-process -- " +
			"dev/test/evaluation ONLY, does not meet FedRAMP, HIPAA or PCI DSS 4.0; " +
			"use --admin-csr or --admin-cert for a production deployment")
	}

	if !isMofN {
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
	params, err := s.InitializeShamir(ctx, opts.Policy)
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

// InitializeShamir initializes the system barrier under Shamir custody
// (seal.StorageBarrier.InitializeShamir) with the unseal domain's
// initialization marked in flight (holderScope.initializeShamir): a
// registration, an unenrolment or a second initialization is refused with
// ErrInitializationInProgress until it has finished.
func (s *CeremonyService) InitializeShamir(ctx context.Context, policy seal.FactorPolicy) (*seal.ShamirInitParams, error) {
	s.mu.Lock()
	barrier := s.barrier
	s.mu.Unlock()
	return s.system.initializeShamir(ctx, &s.mu, barrier, policy)
}

// GenerateRecoveryKeys generates the system barrier's recovery key
// (seal.StorageBarrier.GenerateRecoveryKeys) with the recovery domain's
// initialization marked in flight: a recovery holder's registration or
// unenrolment, or a second generation, is refused with
// ErrInitializationInProgress until it has finished.
func (s *CeremonyService) GenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*seal.RecoveryKeyResult, error) {
	s.mu.Lock()
	barrier := s.barrier
	s.mu.Unlock()
	return s.recovery.generateRecoveryKeys(ctx, &s.mu, barrier, threshold, policy)
}

// initializeSystemBarrierLocked brings the system barrier up as part of
// Initialize (go-qrdb#548), using whatever strategy the daemon registered
// it with (seal.StorageBarrier.Initialize's own best-strategy selection --
// the same one `barrier init` has always used; this does not choose a
// different one). secret is BORROWED exactly as seal.StorageBarrier's own
// Initialize/Unseal methods treat their creds parameter: copied into
// protected memory and destroyed before this returns, leaving the caller's
// slice for it to wipe. Must be called with s.mu held for writing.
//
// A barrier already initialized -- a retry after a crash between this
// succeeding and the ceremony's own state committing, or simply not sealed
// to begin with -- is tolerated: Initialize's ErrAlreadyInitialized falls
// through to Unseal with the same secret, so a retry with the same
// credential converges rather than failing closed on work it already did.
//
// When the barrier is ALREADY unsealed, "nothing to do" is not the same as
// "safe to no-op" (go-qrdb#548 review, item 1): the caller just handed this
// a secret, and the only way to know it is the SAME secret that opened the
// barrier is to check it against the live key, through
// StorageBarrier.VerifyCredentials. A retry whose secret does not match is
// refused with *SystemBarrierCredentialMismatchError rather than reported
// operational -- this service must never let a caller believe a secret
// unseals a barrier it does not.
func (s *CeremonyService) initializeSystemBarrierLocked(ctx context.Context, secret []byte) error {
	if !s.barrier.IsSealed() {
		creds, err := seal.CredentialsFor(secret)
		if err != nil {
			return &SystemBarrierInitError{Cause: err}
		}
		defer creds.Secret.Destroy()
		if err := s.barrier.VerifyCredentials(ctx, creds); err != nil {
			return &SystemBarrierCredentialMismatchError{Cause: err}
		}
		return nil
	}
	creds, err := seal.CredentialsFor(secret)
	if err != nil {
		return &SystemBarrierInitError{Cause: err}
	}
	defer creds.Secret.Destroy()

	if err := s.barrier.Initialize(ctx, creds); err != nil {
		if !errors.Is(err, seal.ErrAlreadyInitialized) {
			s.logger.Error("failed to initialize system barrier", slog.String("error", err.Error()))
			return &SystemBarrierInitError{Cause: err}
		}
		if err := s.barrier.Unseal(ctx, creds); err != nil {
			s.logger.Error("failed to unseal already-initialized system barrier", slog.String("error", err.Error()))
			return &SystemBarrierInitError{Cause: err}
		}
	}
	return nil
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

// SignCSRInit issues a system-tenant certificate for username with the
// profile of the given role, under SO PIN authorization. The CA assigns the
// identity: CN = username and OU = [system, profile], with no Subject
// Alternative Name; nothing is taken from the CSR but its public key. The
// profile decides what the certificate is: role "so" and role "custodian"
// carry a holder profile (holderprofile.Is), so the certificate is a HOLDER
// certificate that authenticates with no roles and is refused every RBAC
// permission -- roleProfileMap carries no other roles (go-qrdb#548): a login
// certificate for a user-facing role is issued by the tenant Admin through
// the certs CSR-sign or BYOC path instead, which requires the caller to
// already be authenticated and authorized, not merely to know the SO PIN.
// The SO PIN is verified by the PINBackend (hashed, constant-time), never
// compared here.
//
// SignCSRInit is PreOperationalOnly (go-qrdb#548, matching
// sealedwindow.VerbCeremonySignCSR): it requires the enrolling state and
// refuses once the ceremony has reached operational, even through an
// authenticated transport. A node restarted sealed after the ceremony
// already completed cannot mint a fresh holder certificate through this
// path -- there is nothing left here for it to enroll.
func (s *CeremonyService) SignCSRInit(ctx context.Context, username, soPin string, csrPEM []byte, role string) (*SignCSRInitResult, error) {
	s.mu.Lock()
	state, deps, pins := s.state, s.deps, s.pins
	s.mu.Unlock()

	if state != StateEnrolling {
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
	if err := pin.RequireSOPIN(ctx, pins, soPin); err != nil {
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
	if !validPrincipalName(username) {
		return nil, &InvalidHolderNameError{Name: username}
	}
	cert, _, err := signHolderCSR(ctx, deps.CA, csrPEM, &SignCSROptions{
		TenantID:     tenantid.System,
		Username:     username,
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
// wired (pin.ErrBackendNotConfigured). The PIN backend the composer installs
// is decorated with the node's one SO PIN lockout (pin.NewLockoutBackend), so
// this check counts against the same counter as every other SO PIN check on
// the node: once the free failures are spent, the right PIN included is
// refused with *pin.SOPINLockedError until the backoff passes.
func (s *CeremonyService) AuthorizeSOPIN(ctx context.Context, soPIN string) error {
	s.mu.Lock()
	pins := s.pins
	s.mu.Unlock()
	return pin.RequireProvisionedSOPIN(ctx, pins, soPIN)
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
	res, err := h.completeClaim(ctx, &s.deps, username, nonceHex, signature, callerThumbprint)
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
	return s.StartRekey(ctx, domain, seal.RekeyParams{Threshold: threshold, Remove: []string{username}})
}

// StartRekey opens a rekey round for domain on the system barrier
// (seal.StorageBarrier.StartRekey) and records its opening, or its refusal,
// on the compliance trail. Every rekey of a system or recovery holder set,
// a revocation included, is opened here.
func (s *CeremonyService) StartRekey(ctx context.Context, domain seal.QuorumDomain, params seal.RekeyParams) (*seal.QuorumRoundInfo, error) {
	// The barrier calls back into PrepareRoster, which takes s.mu, so the
	// lock is held only to read what the rekey needs.
	s.mu.Lock()
	barrier, deps := s.barrier, s.deps
	h, err := s.scope(domain)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return h.startRekey(ctx, &deps, params, func(p seal.RekeyParams) (*seal.QuorumRoundInfo, error) {
		return barrier.StartRekey(ctx, domain, p)
	})
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
	return h.committed(ctx, &s.deps, roster, removed)
}

// AdmitSubmission implements seal.HolderVerifier: a share submission passes
// the caller's quorum throttle for domain's scope, in the submission bucket.
func (s *CeremonyService) AdmitSubmission(ctx context.Context, domain seal.QuorumDomain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.scope(domain)
	if err != nil {
		return err
	}
	_, err = h.throttle(ctx, &s.deps, quorumKindSubmit)
	return err
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
