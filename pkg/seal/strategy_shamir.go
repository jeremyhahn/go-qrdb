// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/shamir"
)

const shareKeyPrefix = "shamir-share-"

// Compile-time interface check.
var _ SealingStrategy = (*ShamirStrategy)(nil)

// ShamirStrategy implements SealingStrategy by splitting the root key itself
// into M-of-N Shamir shares. No password is required; the root key IS the
// Shamir secret. Shares are persisted to the provided StorageBackend for
// distribution to custodians and later recovery.
type ShamirStrategy struct {
	storage   StorageBackend
	threshold int
	total     int
}

// NewShamirStrategy creates a ShamirStrategy that splits root keys into
// threshold-of-total Shamir shares. The storage backend is used to persist
// and retrieve individual shares.
func NewShamirStrategy(store StorageBackend, threshold, total int) (*ShamirStrategy, error) {
	if store == nil {
		return nil, ErrShamirNilStorage
	}
	if threshold < 2 || threshold > total {
		return nil, ErrShamirThresholdInvalid
	}
	return &ShamirStrategy{
		storage:   store,
		threshold: threshold,
		total:     total,
	}, nil
}

// ID returns the strategy identifier for Shamir secret sharing.
func (s *ShamirStrategy) ID() StrategyID {
	return StrategyShamir
}

// Available reports whether this strategy can be used. The Shamir strategy
// is always available as it requires no hardware.
func (s *ShamirStrategy) Available() bool {
	return true
}

// HardwareBacked returns false because Shamir secret sharing is a
// software-only protection mechanism.
func (s *ShamirStrategy) HardwareBacked() bool {
	return false
}

// SealRootKey splits the root key into Shamir shares and persists them
// to storage. The returned SealedRootKey envelope records the threshold
// and total share counts but does not contain the root key itself.
func (s *ShamirStrategy) SealRootKey(ctx context.Context, rootKey []byte, creds Credentials) (*SealedRootKey, error) {
	shares, err := shamir.Split(rootKey, s.threshold, s.total)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirSplitFailed, err)
	}

	if err := s.persistShares(shares); err != nil {
		return nil, err
	}

	return &SealedRootKey{
		Version:         sealedRootKeyVersion,
		Strategy:        StrategyShamir,
		ShamirThreshold: s.threshold,
		ShamirTotal:     s.total,
		CreatedAt:       time.Now(),
	}, nil
}

// UnsealRootKey reconstructs the root key from all persisted shares.
// At least threshold shares must exist in storage.
func (s *ShamirStrategy) UnsealRootKey(ctx context.Context, sealed *SealedRootKey, creds Credentials) ([]byte, error) {
	if sealed == nil {
		return nil, ErrNilSealedData
	}

	shares, err := s.LoadAllShares()
	if err != nil {
		return nil, err
	}

	if len(shares) < s.threshold {
		return nil, ErrShamirQuorumIncomplete
	}

	secret, err := shamir.Combine(shares)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirCombineFailed, err)
	}

	return secret, nil
}

// BarrierEncryptor returns a software-based symmetric encryptor derived from
// the provided root key using HKDF-SHA256.
func (s *ShamirStrategy) BarrierEncryptor(ctx context.Context, rootKey []byte) (SymmetricEncrypter, error) {
	return newSoftwareBarrierEncryptor(rootKey)
}

// LoadShare loads a single Shamir share by its 1-based index from storage.
func (s *ShamirStrategy) LoadShare(index int) (*shamir.Share, error) {
	key := shamirShareKey(index)
	data, err := s.storage.Get(key)
	if err != nil {
		return nil, fmt.Errorf("%w: index %d: %v", ErrShamirShareNotFound, index, err)
	}

	var share shamir.Share
	if err := json.Unmarshal(data, &share); err != nil {
		return nil, fmt.Errorf("%w: index %d: %v", ErrShamirSerializationFailed, index, err)
	}

	return &share, nil
}

// LoadAllShares loads all Shamir shares from storage by listing keys with
// the share prefix and deserializing each one.
func (s *ShamirStrategy) LoadAllShares() ([]*shamir.Share, error) {
	keys, err := s.storage.List(shareKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirStorageFailed, err)
	}

	if len(keys) == 0 {
		return nil, ErrShamirNoSharesFound
	}

	shares := make([]*shamir.Share, 0, len(keys))
	for _, key := range keys {
		data, err := s.storage.Get(key)
		if err != nil {
			return nil, fmt.Errorf("%w: key %s: %v", ErrShamirStorageFailed, key, err)
		}

		var share shamir.Share
		if err := json.Unmarshal(data, &share); err != nil {
			return nil, fmt.Errorf("%w: key %s: %v", ErrShamirSerializationFailed, key, err)
		}
		shares = append(shares, &share)
	}

	return shares, nil
}

// DeleteShare removes a single Shamir share from storage by its 1-based index.
func (s *ShamirStrategy) DeleteShare(index int) error {
	key := shamirShareKey(index)
	if err := s.storage.Delete(key); err != nil {
		return fmt.Errorf("%w: index %d: %v", ErrShamirStorageFailed, index, err)
	}
	return nil
}

// DeleteAllShares removes all Shamir shares from storage.
func (s *ShamirStrategy) DeleteAllShares() error {
	keys, err := s.storage.List(shareKeyPrefix)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrShamirStorageFailed, err)
	}

	for _, key := range keys {
		if err := s.storage.Delete(key); err != nil {
			return fmt.Errorf("%w: key %s: %v", ErrShamirStorageFailed, key, err)
		}
	}

	return nil
}

// VerifyShares loads all persisted shares and validates their consistency.
// Each share must have matching threshold and total values, and no duplicate
// indices may exist.
func (s *ShamirStrategy) VerifyShares() error {
	shares, err := s.LoadAllShares()
	if err != nil {
		return err
	}

	if len(shares) == 0 {
		return ErrShamirNoSharesFound
	}

	seenIndices := make(map[int]struct{}, len(shares))
	for _, share := range shares {
		if share.Threshold != s.threshold {
			return fmt.Errorf("%w: share %d threshold %d != configured %d",
				ErrShamirVerificationFailed, share.Index, share.Threshold, s.threshold)
		}
		if share.Total != s.total {
			return fmt.Errorf("%w: share %d total %d != configured %d",
				ErrShamirVerificationFailed, share.Index, share.Total, s.total)
		}
		if _, exists := seenIndices[share.Index]; exists {
			return fmt.Errorf("%w: duplicate share index %d",
				ErrShamirVerificationFailed, share.Index)
		}
		seenIndices[share.Index] = struct{}{}
	}

	return nil
}

// ShareCount returns the number of shares currently persisted in storage.
func (s *ShamirStrategy) ShareCount() (int, error) {
	keys, err := s.storage.List(shareKeyPrefix)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrShamirStorageFailed, err)
	}
	return len(keys), nil
}

// Threshold returns the minimum number of shares required for reconstruction.
func (s *ShamirStrategy) Threshold() int {
	return s.threshold
}

// TotalShares returns the total number of shares generated during initialization.
func (s *ShamirStrategy) TotalShares() int {
	return s.total
}

// Storage returns the underlying storage backend used for share persistence.
func (s *ShamirStrategy) Storage() StorageBackend {
	return s.storage
}

// persistShares serializes each share to JSON and stores it in the backend
// under the key "shamir-share-{index}".
func (s *ShamirStrategy) persistShares(shares []*shamir.Share) error {
	for _, share := range shares {
		data, err := json.Marshal(share)
		if err != nil {
			return fmt.Errorf("%w: index %d: %v", ErrShamirSerializationFailed, share.Index, err)
		}

		key := shamirShareKey(share.Index)
		if err := s.storage.Put(key, data); err != nil {
			return fmt.Errorf("%w: index %d: %v", ErrShamirStorageFailed, share.Index, err)
		}
	}
	return nil
}

// shamirShareKey returns the storage key for a share at the given 1-based index.
func shamirShareKey(index int) string {
	var b strings.Builder
	b.WriteString(shareKeyPrefix)
	b.WriteString(fmt.Sprintf("%d", index))
	return b.String()
}
