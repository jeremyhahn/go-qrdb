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

package service

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// BarrierServicer provides barrier lifecycle operations including
// initialization, seal/unseal, Shamir share management, rekey,
// recovery, and root token generation.
type BarrierServicer interface {
	// BarrierInitialize initializes the barrier with a secret credential.
	BarrierInitialize(ctx context.Context, secret string) error

	// BarrierUnseal unseals the barrier with a secret credential.
	BarrierUnseal(ctx context.Context, secret string) error

	// BarrierSeal transitions the barrier to the sealed state. Returns
	// ErrShamirBarrierRequiresQuorum when the active strategy is Shamir;
	// callers must use BarrierShamirSealVote instead.
	BarrierSeal(ctx context.Context) error

	// BarrierStatus returns the current barrier status.
	BarrierStatus(ctx context.Context) (*BarrierStatusInfo, error)

	// BarrierInitializeShamir initializes the barrier with Shamir secret
	// sharing, splitting the root key into threshold-of-totalShares shares.
	// Shares are distributed to enrolled Security Officers via the
	// CeremonyService; they do not appear in the returned ShamirInitInfo.
	BarrierInitializeShamir(ctx context.Context, secret string, threshold, totalShares int) (*ShamirInitInfo, error)

	// BarrierUnsealWithShare submits a single Shamir share toward the
	// quorum threshold.
	BarrierUnsealWithShare(ctx context.Context, share string) (*UnsealShareProgress, error)

	// BarrierUnsealWithShares unseals the barrier using a complete set
	// of Shamir shares in a single call.
	BarrierUnsealWithShares(ctx context.Context, shares []string) error

	// BarrierRekey generates new Shamir shares for the existing root key
	// with the specified threshold and total parameters. Shares are
	// distributed to enrolled Security Officers; they do not appear in the
	// returned ShamirInitInfo.
	BarrierRekey(ctx context.Context, threshold, total int) (*ShamirInitInfo, error)

	// BarrierGenerateRecoveryKeys generates a set of Shamir recovery
	// shares from the root key for disaster recovery. Recovery shares are
	// distributed to enrolled recovery officers; they do not appear in the
	// returned RecoveryKeysInfo.
	BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*RecoveryKeysInfo, error)

	// BarrierRecoverWithKeys unseals the barrier using recovery shares.
	BarrierRecoverWithKeys(ctx context.Context, keys []string) error

	// BarrierDeleteRecoveryKeys removes recovery key metadata from storage.
	BarrierDeleteRecoveryKeys(ctx context.Context) error

	// BarrierHasRecoveryKeys reports whether recovery key metadata exists.
	BarrierHasRecoveryKeys(ctx context.Context) (bool, error)

	// BarrierGenerateRootToken creates a new root token by proving
	// knowledge of the master key through Shamir shares.
	BarrierGenerateRootToken(ctx context.Context, shares []string) (*RootTokenInfo, error)

	// BarrierOfficerRegister registers a Security Officer for the system barrier.
	// The officer's CSR is validated, signed by the ceremony CA, and the public
	// enrollment record persisted. No share bytes are produced at this step.
	BarrierOfficerRegister(ctx context.Context, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error)

	// BarrierOfficerList returns all enrolled Security Officers for the system barrier.
	BarrierOfficerList(ctx context.Context) ([]*BarrierOfficerStatus, error)

	// BarrierOfficerClaimStatus returns a per-officer claim status snapshot
	// for the system barrier. Never includes share bytes.
	BarrierOfficerClaimStatus(ctx context.Context) ([]*BarrierOfficerStatus, error)

	// BarrierShareClaimBegin issues a challenge nonce to the named Security
	// Officer for the system barrier share claim handshake.
	BarrierShareClaimBegin(ctx context.Context, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error)

	// BarrierShareClaimComplete verifies the officer's nonce signature and
	// returns the share bytes exactly once via the officer's own mTLS session.
	BarrierShareClaimComplete(ctx context.Context, username, nonceHex string, signature []byte, callerCertThumbprint string) (*BarrierShareClaimResult, error)

	// BarrierShamirSealVote submits a Shamir share as a seal vote. When the
	// quorum threshold is met the barrier is sealed automatically.
	// Returns ErrShamirBarrierRequiresQuorum if the barrier is already sealed.
	BarrierShamirSealVote(ctx context.Context, share string) (*UnsealShareProgress, error)

	// BarrierRecoveryOfficerRegister registers a Security Officer for recovery
	// key distribution. Recovery officers are independent from unseal officers.
	BarrierRecoveryOfficerRegister(ctx context.Context, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error)

	// BarrierRecoveryShareClaimBegin issues a challenge nonce for a recovery officer.
	BarrierRecoveryShareClaimBegin(ctx context.Context, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error)

	// BarrierRecoveryShareClaimComplete verifies the recovery officer's nonce
	// signature and returns the recovery share bytes exactly once.
	BarrierRecoveryShareClaimComplete(ctx context.Context, username, nonceHex string, signature []byte, callerCertThumbprint string) (*BarrierShareClaimResult, error)
}

// BarrierStatusInfo describes the current state of the storage barrier.
type BarrierStatusInfo struct {
	// Sealed is true when the barrier is in sealed state.
	Sealed bool

	// Strategy identifies which sealing strategy protects the root key.
	Strategy string

	// HardwareBacked is true when the active strategy uses dedicated
	// cryptographic hardware.
	HardwareBacked bool

	// InitializedAt records when the barrier was first initialized.
	InitializedAt time.Time

	// ShamirThreshold is the minimum number of Shamir shares required to
	// unseal. Zero when the active strategy is not Shamir.
	ShamirThreshold int

	// ShamirTotal is the total number of Shamir shares generated at
	// initialization or last rekey. Zero when the active strategy is not Shamir.
	ShamirTotal int
}

// ShamirInitInfo contains the output of a Shamir initialization or rekey
// operation. Share material is never present here; Security Officers retrieve
// their shares via the BarrierShareClaimBegin / BarrierShareClaimComplete flow.
type ShamirInitInfo struct {
	// Threshold is the minimum number of shares required for reconstruction.
	Threshold int

	// TotalShares is the total number of shares generated.
	TotalShares int

	// ShareCount is the number of shares successfully sealed to officer enclaves.
	ShareCount int
}

// UnsealShareProgress reports the current state of quorum-based unsealing.
type UnsealShareProgress struct {
	// Required is the minimum number of shares needed to unseal.
	Required int

	// Submitted is the number of shares provided so far.
	Submitted int

	// Complete is true when the quorum threshold has been met.
	Complete bool
}

// RecoveryKeysInfo contains the output of recovery key generation. Recovery
// share material is never present here; recovery officers retrieve their shares
// via the BarrierRecoveryShareClaimBegin / BarrierRecoveryShareClaimComplete flow.
type RecoveryKeysInfo struct {
	// Threshold is the minimum number of keys required for recovery.
	Threshold int

	// Total is the total number of keys generated.
	Total int

	// ShareCount is the number of recovery shares successfully sealed to officer enclaves.
	ShareCount int
}

// RootTokenInfo represents a generated root token.
type RootTokenInfo struct {
	// Token is the hex-encoded root token value.
	Token string

	// CreatedAt records when the token was generated.
	CreatedAt time.Time
}

// BarrierOfficerRegisterRequest carries the parameters for registering a
// Security Officer against the system or recovery barrier.
type BarrierOfficerRegisterRequest struct {
	// Username is the unique identifier for the officer.
	Username string

	// CSRPEM is the PEM-encoded certificate signing request from the officer.
	CSRPEM []byte

	// AuthenticatorType identifies which hardware or software backs the
	// officer's signing credential.
	AuthenticatorType string

	// AuthenticatorRef is an opaque reference to the credential within the
	// backing authenticator.
	AuthenticatorRef string
}

// BarrierOfficerRegisterResult is the outcome of a successful officer registration.
type BarrierOfficerRegisterResult struct {
	// CertPEM is the PEM-encoded signed certificate for the officer.
	CertPEM []byte

	// ShareIndex is the 1-based Shamir share index assigned to this officer.
	ShareIndex int
}

// BarrierOfficerStatus is a non-secret view of an enrolled Security Officer.
type BarrierOfficerStatus struct {
	// Username is the officer's unique identifier.
	Username string

	// CertThumbprint is the SHA-256 fingerprint of the officer's mTLS certificate.
	CertThumbprint string

	// ShareIndex is the 1-based Shamir share index assigned to this officer.
	ShareIndex int

	// AuthFactor identifies the backing authenticator type.
	AuthFactor string

	// EnrolledAt records when the officer was registered.
	EnrolledAt time.Time

	// ClaimedAt records when the officer claimed their share. Zero = unclaimed.
	ClaimedAt time.Time

	// SharePending is true when a share is sealed in the enclave awaiting claim.
	SharePending bool
}

// BarrierShareClaimChallenge is the challenge issued by BarrierShareClaimBegin.
type BarrierShareClaimChallenge struct {
	// NonceHex is the hex-encoded random challenge bytes the officer must sign.
	NonceHex string

	// ExpiresAt is the deadline by which the officer must complete the claim.
	ExpiresAt time.Time
}

// BarrierShareClaimResult is returned to the Security Officer exactly once
// upon a successful BarrierShareClaimComplete. The share bytes must be wiped
// by the caller after presenting them to the user.
type BarrierShareClaimResult struct {
	// ShareIndex is the 1-based Shamir share index for this officer.
	ShareIndex int

	// ShareBytes are the raw share bytes. This field is intentionally excluded
	// from JSON serialization and must only traverse the SO's own mTLS session.
	ShareBytes []byte `json:"-"`
}

// BarrierService implements BarrierServicer by delegating to a
// StorageBarrier instance from the seal package and, optionally, to a
// CeremonyService for Security Officer share distribution.
type BarrierService struct {
	barrier  *seal.StorageBarrier
	ceremony *ceremony.CeremonyService
}

// Compile-time interface check.
var _ BarrierServicer = (*BarrierService)(nil)

// NewBarrierService creates a new BarrierServicer backed by the given
// StorageBarrier.
func NewBarrierService(barrier *seal.StorageBarrier) BarrierServicer {
	return &BarrierService{barrier: barrier}
}

// NewBarrierServiceWithCeremony creates a new BarrierServicer backed by
// the given StorageBarrier and wired to the CeremonyService for Security
// Officer share distribution. The CeremonyService is set as the barrier's
// CeremonyDistributor and RecoveryDistributor.
func NewBarrierServiceWithCeremony(barrier *seal.StorageBarrier, svc *ceremony.CeremonyService) BarrierServicer {
	barrier.SetCeremonyDistributor(svc)
	barrier.SetRecoveryDistributor(svc.RecoveryDistributor())
	return &BarrierService{barrier: barrier, ceremony: svc}
}

// BarrierInitialize initializes the barrier with a secret credential by
// delegating to the StorageBarrier's Initialize method.
func (s *BarrierService) BarrierInitialize(ctx context.Context, secret string) error {
	return s.barrier.Initialize(ctx, seal.Credentials{Secret: []byte(secret)})
}

// BarrierUnseal unseals the barrier with a secret credential by delegating
// to the StorageBarrier's Unseal method.
func (s *BarrierService) BarrierUnseal(ctx context.Context, secret string) error {
	return s.barrier.Unseal(ctx, seal.Credentials{Secret: []byte(secret)})
}

// BarrierSeal transitions the barrier to the sealed state. For Shamir-protected
// barriers the single-admin seal path is blocked; callers must use
// BarrierShamirSealVote to collect the required quorum before sealing.
func (s *BarrierService) BarrierSeal(_ context.Context) error {
	if s.barrier.ActiveStrategy() == seal.StrategyShamir {
		return seal.ErrShamirBarrierRequiresQuorum
	}
	return s.barrier.Seal()
}

// BarrierStatus returns the current barrier status, mapping the seal
// package's BarrierStatus to the service-layer BarrierStatusInfo type.
func (s *BarrierService) BarrierStatus(_ context.Context) (*BarrierStatusInfo, error) {
	status := s.barrier.Status()
	return &BarrierStatusInfo{
		Sealed:          status.Sealed,
		Strategy:        string(status.Strategy),
		HardwareBacked:  status.HardwareBacked,
		InitializedAt:   status.InitializedAt,
		ShamirThreshold: status.ShamirThreshold,
		ShamirTotal:     status.ShamirTotal,
	}, nil
}

// BarrierInitializeShamir initializes the barrier with Shamir secret sharing.
// When threshold and totalShares are both non-zero the barrier's Shamir config
// is updated to honor the request parameters (request wins). When either is
// zero the existing daemon configuration is preserved as the fallback.
// Shares are distributed to enrolled Security Officers via the CeremonyService
// and do not appear in the returned ShamirInitInfo.
func (s *BarrierService) BarrierInitializeShamir(ctx context.Context, secret string, threshold, totalShares int) (*ShamirInitInfo, error) {
	if threshold != 0 || totalShares != 0 {
		s.barrier.SetShamirConfig(&seal.ShamirConfig{
			Threshold:   threshold,
			TotalShares: totalShares,
		})
	}

	result, err := s.barrier.InitializeShamir(ctx, seal.Credentials{Secret: []byte(secret)})
	if err != nil {
		return nil, err
	}

	return &ShamirInitInfo{
		Threshold:   result.Threshold,
		TotalShares: result.TotalShares,
		ShareCount:  result.ShareCount,
	}, nil
}

// BarrierUnsealWithShare submits a single Shamir share toward the quorum
// threshold. The QuorumProgress from the seal package is mapped to the
// service-layer UnsealShareProgress type.
func (s *BarrierService) BarrierUnsealWithShare(ctx context.Context, share string) (*UnsealShareProgress, error) {
	progress, err := s.barrier.UnsealWithShare(ctx, share)
	if err != nil {
		return nil, err
	}

	return &UnsealShareProgress{
		Required:  progress.Required,
		Submitted: progress.Submitted,
		Complete:  progress.Complete,
	}, nil
}

// BarrierUnsealWithShares unseals the barrier using a complete set of
// Shamir shares in a single call.
func (s *BarrierService) BarrierUnsealWithShares(ctx context.Context, shares []string) error {
	return s.barrier.UnsealWithShares(ctx, shares)
}

// BarrierRekey generates new Shamir shares for the existing root key with
// the specified threshold and total parameters. Shares are distributed to
// enrolled Security Officers; they do not appear in the returned ShamirInitInfo.
func (s *BarrierService) BarrierRekey(ctx context.Context, threshold, total int) (*ShamirInitInfo, error) {
	result, err := s.barrier.Rekey(ctx, threshold, total)
	if err != nil {
		return nil, err
	}

	return &ShamirInitInfo{
		Threshold:   result.Threshold,
		TotalShares: result.TotalShares,
		ShareCount:  result.ShareCount,
	}, nil
}

// BarrierGenerateRecoveryKeys generates a set of Shamir recovery shares
// from the root key. Recovery shares are distributed to enrolled recovery
// officers; they do not appear in the returned RecoveryKeysInfo.
func (s *BarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*RecoveryKeysInfo, error) {
	result, err := s.barrier.GenerateRecoveryKeys(ctx, threshold, total)
	if err != nil {
		return nil, err
	}

	return &RecoveryKeysInfo{
		Threshold:  result.Threshold,
		Total:      result.TotalShares,
		ShareCount: result.ShareCount,
	}, nil
}

// BarrierRecoverWithKeys unseals the barrier using recovery shares that
// were previously generated by BarrierGenerateRecoveryKeys.
func (s *BarrierService) BarrierRecoverWithKeys(ctx context.Context, keys []string) error {
	return s.barrier.RecoverWithKeys(ctx, keys)
}

// BarrierDeleteRecoveryKeys removes recovery key metadata from storage.
func (s *BarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	return s.barrier.DeleteRecoveryKeys(ctx)
}

// BarrierHasRecoveryKeys reports whether recovery key metadata exists
// in storage.
func (s *BarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	return s.barrier.HasRecoveryKeys(ctx)
}

// BarrierGenerateRootToken creates a new root token by proving knowledge
// of the master key through Shamir shares. The RootToken from the seal
// package is mapped to the service-layer RootTokenInfo type.
func (s *BarrierService) BarrierGenerateRootToken(ctx context.Context, shares []string) (*RootTokenInfo, error) {
	token, err := s.barrier.GenerateRootToken(ctx, shares)
	if err != nil {
		return nil, err
	}

	return &RootTokenInfo{
		Token:     token.Token,
		CreatedAt: token.CreatedAt,
	}, nil
}

// BarrierOfficerRegister registers a Security Officer for the system barrier.
// The officer's CSR is validated, signed by the ceremony CA, and the public
// enrollment record persisted. No share bytes are produced at registration time.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierOfficerRegister(ctx context.Context, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	result, err := s.ceremony.RegisterOfficer(ctx, ceremony.RegisterOfficerRequest{
		Username:          req.Username,
		CSRPEM:            req.CSRPEM,
		AuthenticatorType: ceremony.AuthenticatorType(req.AuthenticatorType),
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, err
	}

	return &BarrierOfficerRegisterResult{
		CertPEM:    result.CertPEM,
		ShareIndex: result.ShareIndex,
	}, nil
}

// BarrierOfficerList returns all enrolled Security Officers for the system barrier.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierOfficerList(ctx context.Context) ([]*BarrierOfficerStatus, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	statuses, err := s.ceremony.OfficerClaimStatus(ctx)
	if err != nil {
		return nil, err
	}

	return mapOfficerStatuses(statuses), nil
}

// BarrierOfficerClaimStatus returns a per-officer claim status snapshot for
// the system barrier. Never includes share bytes.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierOfficerClaimStatus(ctx context.Context) ([]*BarrierOfficerStatus, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	statuses, err := s.ceremony.OfficerClaimStatus(ctx)
	if err != nil {
		return nil, err
	}

	return mapOfficerStatuses(statuses), nil
}

// BarrierShareClaimBegin issues a challenge nonce to the named Security Officer
// for the system barrier share claim handshake.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierShareClaimBegin(ctx context.Context, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	challenge, err := s.ceremony.BeginClaimShare(ctx, username, callerCertThumbprint)
	if err != nil {
		return nil, err
	}

	return mapShareClaimChallenge(challenge), nil
}

// BarrierShareClaimComplete verifies the officer's nonce signature and returns
// the share bytes exactly once via the officer's own mTLS session.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierShareClaimComplete(
	ctx context.Context,
	username, nonceHex string,
	signature []byte,
	callerCertThumbprint string,
) (*BarrierShareClaimResult, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	result, err := s.ceremony.CompleteClaimShare(ctx, username, nonceHex, signature, callerCertThumbprint)
	if err != nil {
		return nil, err
	}

	return &BarrierShareClaimResult{
		ShareIndex: result.ShareIndex,
		ShareBytes: result.ShareBytes,
	}, nil
}

// BarrierShamirSealVote submits a Shamir share as a seal vote for the system
// barrier. When the quorum threshold is met the barrier is sealed automatically.
func (s *BarrierService) BarrierShamirSealVote(ctx context.Context, share string) (*UnsealShareProgress, error) {
	progress, err := s.barrier.SubmitSealVote(ctx, share)
	if err != nil {
		return nil, err
	}

	return &UnsealShareProgress{
		Required:  progress.Required,
		Submitted: progress.Submitted,
		Complete:  progress.Complete,
	}, nil
}

// BarrierRecoveryOfficerRegister registers a Security Officer for recovery key
// distribution. Recovery officers are independent from unseal officers.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierRecoveryOfficerRegister(ctx context.Context, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	result, err := s.ceremony.RegisterRecoveryOfficer(ctx, ceremony.RegisterOfficerRequest{
		Username:          req.Username,
		CSRPEM:            req.CSRPEM,
		AuthenticatorType: ceremony.AuthenticatorType(req.AuthenticatorType),
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, err
	}

	return &BarrierOfficerRegisterResult{
		CertPEM:    result.CertPEM,
		ShareIndex: result.ShareIndex,
	}, nil
}

// BarrierRecoveryShareClaimBegin issues a challenge nonce for a recovery officer.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierRecoveryShareClaimBegin(ctx context.Context, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	challenge, err := s.ceremony.BeginRecoveryShareClaim(ctx, username, callerCertThumbprint)
	if err != nil {
		return nil, err
	}

	return mapShareClaimChallenge(challenge), nil
}

// BarrierRecoveryShareClaimComplete verifies the recovery officer's nonce
// signature and returns the recovery share bytes exactly once.
// Returns ErrCeremonyNotConfigured when no CeremonyService has been wired.
func (s *BarrierService) BarrierRecoveryShareClaimComplete(
	ctx context.Context,
	username, nonceHex string,
	signature []byte,
	callerCertThumbprint string,
) (*BarrierShareClaimResult, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}

	result, err := s.ceremony.CompleteRecoveryShareClaim(ctx, username, nonceHex, signature, callerCertThumbprint)
	if err != nil {
		return nil, err
	}

	return &BarrierShareClaimResult{
		ShareIndex: result.ShareIndex,
		ShareBytes: result.ShareBytes,
	}, nil
}

// mapOfficerStatuses converts a slice of ceremony.OfficerStatus to the
// service-layer BarrierOfficerStatus slice.
func mapOfficerStatuses(statuses []*ceremony.OfficerStatus) []*BarrierOfficerStatus {
	out := make([]*BarrierOfficerStatus, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, &BarrierOfficerStatus{
			Username:       s.Username,
			CertThumbprint: s.CertThumbprint,
			ShareIndex:     s.ShareIndex,
			AuthFactor:     s.AuthFactor,
			EnrolledAt:     s.EnrolledAt,
			ClaimedAt:      s.ClaimedAt,
			SharePending:   s.SharePending,
		})
	}
	return out
}

// mapShareClaimChallenge converts a ceremony.ChallengeResponse to the
// service-layer BarrierShareClaimChallenge, encoding the nonce as hex.
func mapShareClaimChallenge(ch *ceremony.ChallengeResponse) *BarrierShareClaimChallenge {
	return &BarrierShareClaimChallenge{
		NonceHex:  hex.EncodeToString(ch.Nonce),
		ExpiresAt: ch.ExpiresAt,
	}
}
