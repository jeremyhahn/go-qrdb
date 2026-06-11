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
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Compile-time checks: CeremonyService satisfies seal.CeremonyDistributor.
var _ seal.CeremonyDistributor = (*CeremonyService)(nil)

// Compile-time check: recoveryDistributorAdapter satisfies seal.CeremonyDistributor.
var _ seal.CeremonyDistributor = (*recoveryDistributorAdapter)(nil)

// recoveryDistributorAdapter adapts CeremonyService's recovery-scoped state
// to the seal.CeremonyDistributor interface. It is returned by
// CeremonyService.RecoveryDistributor().
type recoveryDistributorAdapter struct {
	svc *CeremonyService
}

// SealAndDistribute seals shares for recovery officers.
func (a *recoveryDistributorAdapter) SealAndDistribute(ctx context.Context, shareSecret []byte, threshold, total int) (int, error) {
	defer secure.Wipe(shareSecret)

	a.svc.mu.Lock()
	defer a.svc.mu.Unlock()

	if err := a.svc.ensureRecoveryState(); err != nil {
		return 0, err
	}
	return sealSharesToOfficers(ctx, shareSecret, threshold, total,
		a.svc.recoveryRegistry, a.svc.recoveryShareStore)
}

// RekeyAndDistribute destroys prior recovery enclaves and seals new shares.
func (a *recoveryDistributorAdapter) RekeyAndDistribute(ctx context.Context, newShareSecret []byte, newThreshold, newTotal int) (int, error) {
	defer secure.Wipe(newShareSecret)

	a.svc.mu.Lock()
	defer a.svc.mu.Unlock()

	if err := a.svc.ensureRecoveryState(); err != nil {
		return 0, err
	}
	a.svc.recoveryShareStore.Reset()
	if err := a.svc.recoveryRegistry.ResetClaimedAt(ctx); err != nil {
		a.svc.logger.Warn("failed to reset recovery claimed-at timestamps",
			slog.String("error", err.Error()))
	}
	return sealSharesToOfficers(ctx, newShareSecret, newThreshold, newTotal,
		a.svc.recoveryRegistry, a.svc.recoveryShareStore)
}

// RecoveryDistributor returns a seal.CeremonyDistributor that operates on the
// recovery-scope store and officer registry.
func (s *CeremonyService) RecoveryDistributor() seal.CeremonyDistributor {
	return &recoveryDistributorAdapter{svc: s}
}

// RegisterRecoveryOfficer registers a security officer for the recovery key
// scope. Recovery officers are independent from the unseal officer set.
func (s *CeremonyService) RegisterRecoveryOfficer(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ca == nil {
		return nil, ErrCANotInitialized
	}
	if req.Username == "" {
		return nil, ErrMissingUsername
	}
	if len(req.CSRPEM) == 0 {
		return nil, ErrMissingCSRPEM
	}
	if err := req.AuthenticatorType.Validate(); err != nil {
		return nil, err
	}

	if err := s.ensureRecoveryState(); err != nil {
		return nil, err
	}

	// Uniqueness check within the recovery scope.
	if _, err := s.recoveryRegistry.Load(ctx, req.Username); err == nil {
		return nil, ErrDuplicateUsername
	}

	cert, csr, certPEM, err := signOfficerCSR(ctx, s.ca, req.CSRPEM)
	if err != nil {
		return nil, err
	}

	// Determine share index within recovery scope.
	shareIndex := len(s.recoveryRegistry.Cached()) + 1

	thumb := certThumbprint(cert.Raw)
	pubPEM, err := marshalPublicKeyPEM(csr.PublicKey)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	persisted := &PersistentOfficer{
		Username:       req.Username,
		CertThumbprint: thumb,
		PubKeyPEM:      pubPEM,
		ShareIndex:     shareIndex,
		AuthFactor:     authFactorFromAuthenticator(req.AuthenticatorType.Normalized()),
		EnrolledAt:     time.Now().UTC(),
	}

	registryWritable := !s.barrier.IsSealed()
	if registryWritable {
		if err := s.recoveryRegistry.Save(ctx, persisted); err != nil {
			return nil, err
		}
	} else {
		s.recoveryRegistry.mu.Lock()
		s.recoveryRegistry.cache[persisted.Username] = persisted
		s.recoveryRegistry.mu.Unlock()
	}

	return &RegisterOfficerResult{
		CertPEM:    certPEM,
		ShareIndex: shareIndex,
	}, nil
}

// BeginRecoveryShareClaim issues a challenge nonce for a recovery officer.
func (s *CeremonyService) BeginRecoveryShareClaim(ctx context.Context, username, callerCertThumbprint string) (*ChallengeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureRecoveryState(); err != nil {
		return nil, err
	}
	return beginClaimShareLocked(ctx, username, callerCertThumbprint,
		s.recoveryRegistry, s.recoveryShareStore, s.challenges, "recovery:")
}

// CompleteRecoveryShareClaim verifies the challenge and returns the recovery share.
func (s *CeremonyService) CompleteRecoveryShareClaim(
	ctx context.Context,
	username, nonceHex string,
	signature []byte,
	callerCertThumbprint string,
) (*ShareClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureRecoveryState(); err != nil {
		return nil, err
	}
	result, err := completeClaimShareLocked(ctx, username, nonceHex, signature, callerCertThumbprint,
		s.recoveryRegistry, s.recoveryShareStore, s.pendingCerts, s.challenges, "recovery:")
	if err != nil {
		return nil, err
	}
	if s.recoveryRegistry != nil {
		if markErr := s.recoveryRegistry.MarkClaimed(ctx, username, time.Now().UTC()); markErr != nil {
			s.logger.Warn("failed to mark recovery share claimed",
				slog.String("username", username),
				slog.String("error", markErr.Error()))
		}
	}
	return result, nil
}

// RecoveryOfficerClaimStatus returns per-officer claim status for recovery officers.
func (s *CeremonyService) RecoveryOfficerClaimStatus(_ context.Context) ([]*OfficerStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.recoveryRegistry == nil {
		return nil, nil
	}
	return officerStatusFromRegistry(s.recoveryRegistry, s.recoveryShareStore), nil
}

// ensureRecoveryState lazily initializes the recovery-scoped share store and
// officer registry. Must be called under s.mu.
func (s *CeremonyService) ensureRecoveryState() error {
	if s.recoveryShareStore == nil {
		s.recoveryShareStore = newSealedShareStore()
	}
	if s.recoveryRegistry == nil {
		reg, err := NewScopedOfficerRegistry(s.barrier, ScopeRecovery)
		if err != nil {
			return err
		}
		s.recoveryRegistry = reg
	}
	return nil
}
