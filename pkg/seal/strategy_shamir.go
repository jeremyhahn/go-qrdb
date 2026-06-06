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

package seal

import (
	"context"
	"time"
)

// Compile-time interface check.
var _ SealingStrategy = (*ShamirStrategy)(nil)

// ShamirStrategy is the Shamir M-of-N sealing strategy. The root key IS the
// Shamir secret: at initialization it is split into N shares (threshold M),
// which are returned to the Security Officers exactly once and **never
// persisted server-side**. Unsealing requires the SOs to re-submit a quorum of
// shares to the in-memory accumulator (see StorageBarrier.UnsealWithShare);
// there is no disk auto-unseal path.
//
// Domain ownership: this custody concern lives in go-qrdb (the database
// server), not the go-quicraft consensus engine. go-quicraft owns only the L1
// encryptor (EpochBarrier), the dependency-free None/Software primitives, and
// the SealingStrategy/ShareSplitter/ShareAccumulator interfaces this type
// satisfies. The Shamir GF(256) math is go-quicraft/pkg/crypto/shamir.
//
// The split itself is performed by StorageBarrier.initializeShamirDirect /
// Rekey using the in-memory ShareSplitter against the already-unsealed root
// key; this strategy's SealRootKey only produces the (key-material-free)
// SealedRootKey envelope. There is no share-storage surface: shares are never
// written to or read from any backend.
type ShamirStrategy struct {
	threshold int
	total     int
}

// NewShamirStrategy creates a ShamirStrategy for threshold-of-total shares.
func NewShamirStrategy(threshold, total int) (*ShamirStrategy, error) {
	if threshold < 2 || threshold > total {
		return nil, ErrShamirThresholdInvalid
	}
	return &ShamirStrategy{
		threshold: threshold,
		total:     total,
	}, nil
}

// ID returns the strategy identifier for Shamir secret sharing.
func (s *ShamirStrategy) ID() StrategyID { return StrategyShamir }

// Available reports whether this strategy can be used. Always true (no hardware).
func (s *ShamirStrategy) Available() bool { return true }

// HardwareBacked returns false: Shamir is a software-only protection mechanism.
func (s *ShamirStrategy) HardwareBacked() bool { return false }

// Close is a no-op for the Shamir strategy.
func (s *ShamirStrategy) Close() error { return nil }

// SealRootKey returns the (key-material-free) SealedRootKey envelope recording
// the Shamir threshold/total. It does NOT split or persist the root key: the
// split is performed in memory by the StorageBarrier and the resulting shares
// are handed to the Security Officers, never stored.
func (s *ShamirStrategy) SealRootKey(_ context.Context, _ []byte, _ Credentials) (*SealedRootKey, error) {
	return &SealedRootKey{
		Version:         1,
		Strategy:        StrategyShamir,
		ShamirThreshold: s.threshold,
		ShamirTotal:     s.total,
		CreatedAt:       time.Now(),
	}, nil
}

// UnsealRootKey always fails closed: a Shamir barrier cannot be unsealed from
// persisted shares (there are none). The barrier is unsealed exclusively via
// StorageBarrier.UnsealWithShare(s), where the SOs re-submit their shares to an
// in-memory quorum accumulator. This guarantees there is no disk auto-unseal.
func (s *ShamirStrategy) UnsealRootKey(_ context.Context, _ *SealedRootKey, _ Credentials) ([]byte, error) {
	return nil, ErrShamirUnsealRequiresQuorum
}

// BarrierEncryptor returns (nil, nil): the Shamir strategy uses the
// EpochBarrier's epoch-based HKDF DEKs. The root key is reconstructed from the
// SOs' shares and fed to the EpochBarrier for DEK derivation.
func (s *ShamirStrategy) BarrierEncryptor(_ context.Context, _ []byte) (SymmetricEncrypter, error) {
	return nil, nil
}

// Threshold returns the minimum number of shares required for reconstruction.
func (s *ShamirStrategy) Threshold() int { return s.threshold }

// TotalShares returns the total number of shares generated at init/rekey.
func (s *ShamirStrategy) TotalShares() int { return s.total }
