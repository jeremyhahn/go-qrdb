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
	"time"

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

	// BarrierSeal transitions the barrier to the sealed state.
	BarrierSeal(ctx context.Context) error

	// BarrierStatus returns the current barrier status.
	BarrierStatus(ctx context.Context) (*BarrierStatusInfo, error)

	// BarrierInitializeShamir initializes the barrier with Shamir secret
	// sharing, splitting the root key into threshold-of-totalShares shares.
	BarrierInitializeShamir(ctx context.Context, secret string, threshold, totalShares int) (*ShamirInitInfo, error)

	// BarrierUnsealWithShare submits a single Shamir share toward the
	// quorum threshold.
	BarrierUnsealWithShare(ctx context.Context, share string) (*UnsealShareProgress, error)

	// BarrierUnsealWithShares unseals the barrier using a complete set
	// of Shamir shares in a single call.
	BarrierUnsealWithShares(ctx context.Context, shares []string) error

	// BarrierRekey generates new Shamir shares for the existing root key
	// with the specified threshold and total parameters.
	BarrierRekey(ctx context.Context, threshold, total int) (*ShamirInitInfo, error)

	// BarrierGenerateRecoveryKeys generates a set of Shamir recovery
	// shares from the root key for disaster recovery.
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
// operation. The Shares must be distributed to custodians and stored securely.
type ShamirInitInfo struct {
	// Shares are the Shamir share values for distribution.
	Shares []string

	// Threshold is the minimum number of shares required for reconstruction.
	Threshold int

	// TotalShares is the total number of shares generated.
	TotalShares int
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

// RecoveryKeysInfo contains the output of recovery key generation.
type RecoveryKeysInfo struct {
	// Keys are the recovery share values for distribution.
	Keys []string

	// Threshold is the minimum number of keys required for recovery.
	Threshold int

	// Total is the total number of keys generated.
	Total int
}

// RootTokenInfo represents a generated root token.
type RootTokenInfo struct {
	// Token is the hex-encoded root token value.
	Token string

	// CreatedAt records when the token was generated.
	CreatedAt time.Time
}

// BarrierService implements BarrierServicer by delegating to a
// StorageBarrier instance from the seal package.
type BarrierService struct {
	barrier *seal.StorageBarrier
}

// Compile-time interface check.
var _ BarrierServicer = (*BarrierService)(nil)

// NewBarrierService creates a new BarrierServicer backed by the given
// StorageBarrier.
func NewBarrierService(barrier *seal.StorageBarrier) BarrierServicer {
	return &BarrierService{barrier: barrier}
}

// BarrierInitialize initializes the barrier with a secret credential by
// delegating to the StorageBarrier's Initialize method.
func (s *BarrierService) BarrierInitialize(ctx context.Context, secret string) error {
	return s.barrier.Initialize(ctx, seal.Credentials{Secret: secret})
}

// BarrierUnseal unseals the barrier with a secret credential by delegating
// to the StorageBarrier's Unseal method.
func (s *BarrierService) BarrierUnseal(ctx context.Context, secret string) error {
	return s.barrier.Unseal(ctx, seal.Credentials{Secret: secret})
}

// BarrierSeal transitions the barrier to the sealed state by delegating
// to the StorageBarrier's Seal method.
func (s *BarrierService) BarrierSeal(_ context.Context) error {
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
// The resulting shares are mapped to the service-layer ShamirInitInfo type.
func (s *BarrierService) BarrierInitializeShamir(ctx context.Context, secret string, threshold, totalShares int) (*ShamirInitInfo, error) {
	if threshold != 0 || totalShares != 0 {
		s.barrier.SetShamirConfig(&seal.ShamirConfig{
			Threshold:   threshold,
			TotalShares: totalShares,
		})
	}

	result, err := s.barrier.InitializeShamir(ctx, seal.Credentials{Secret: secret})
	if err != nil {
		return nil, err
	}

	return &ShamirInitInfo{
		Shares:      result.Shares,
		Threshold:   result.Threshold,
		TotalShares: result.TotalShares,
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
// the specified threshold and total parameters. The result is mapped to the
// service-layer ShamirInitInfo type.
func (s *BarrierService) BarrierRekey(ctx context.Context, threshold, total int) (*ShamirInitInfo, error) {
	result, err := s.barrier.Rekey(ctx, threshold, total)
	if err != nil {
		return nil, err
	}

	return &ShamirInitInfo{
		Shares:      result.Shares,
		Threshold:   result.Threshold,
		TotalShares: result.TotalShares,
	}, nil
}

// BarrierGenerateRecoveryKeys generates a set of Shamir recovery shares
// from the root key. The RecoveryKeyResult from the seal package is mapped
// to the service-layer RecoveryKeysInfo type.
func (s *BarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*RecoveryKeysInfo, error) {
	result, err := s.barrier.GenerateRecoveryKeys(ctx, threshold, total)
	if err != nil {
		return nil, err
	}

	return &RecoveryKeysInfo{
		Keys:      result.Shares,
		Threshold: result.Threshold,
		Total:     result.TotalShares,
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
