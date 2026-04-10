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
	"fmt"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// InitializeShamir initializes the barrier using Shamir secret sharing.
// If a ShamirStrategy is registered, the root key itself is split into shares
// (direct mode). Otherwise, the credential secret is split and used to
// unseal a password-protected root key (credential mode).
func (b *StorageBarrier) InitializeShamir(ctx context.Context, creds Credentials) (*ShamirInitResult, error) {
	if b.config.Shamir == nil {
		return nil, ErrShamirNotConfigured
	}

	threshold := b.config.Shamir.Threshold
	total := b.config.Shamir.TotalShares

	if threshold < 2 || threshold > total {
		return nil, ErrShamirThresholdInvalid
	}

	// Direct mode: ShamirStrategy splits the root key itself.
	if strat := b.ShamirStrategy(); strat != nil {
		return b.initializeShamirDirect(ctx, strat)
	}

	// Credential mode: initialize barrier with the credential, then
	// split the credential secret into Shamir shares.
	if err := b.Initialize(ctx, creds); err != nil {
		return nil, err
	}

	shares, err := shamir.Split([]byte(creds.Secret), threshold, total)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirSplitFailed, err)
	}

	result := &ShamirInitResult{
		Shares:      make([]string, len(shares)),
		Threshold:   threshold,
		TotalShares: total,
	}
	for i, share := range shares {
		result.Shares[i] = share.Value
	}

	return result, nil
}

// initializeShamirDirect initializes the barrier when a ShamirStrategy is
// registered. The strategy's SealRootKey splits the root key into shares
// and persists them. This method loads the shares back and returns them.
func (b *StorageBarrier) initializeShamirDirect(ctx context.Context, shamirStrat *ShamirStrategy) (*ShamirInitResult, error) {
	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRootKey, err)
	}
	if exists {
		return nil, ErrAlreadyInitialized
	}

	// Initialize with empty credentials; the ShamirStrategy will handle
	// root key splitting during SealRootKey.
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		return nil, err
	}

	shares, err := shamirStrat.LoadAllShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirStorageFailed, err)
	}

	result := &ShamirInitResult{
		Shares:      make([]string, len(shares)),
		Threshold:   shamirStrat.threshold,
		TotalShares: shamirStrat.total,
	}
	for i, share := range shares {
		result.Shares[i] = share.Value
	}

	return result, nil
}

// UnsealWithShare submits a single Shamir share toward the quorum threshold.
// Once enough shares have been submitted, the barrier is automatically unsealed.
// The quorum accumulator expires after the configured TTL (default 5 minutes).
func (b *StorageBarrier) UnsealWithShare(ctx context.Context, shareValue string) (*QuorumProgress, error) {
	if b.config.Shamir == nil {
		return nil, ErrShamirNotConfigured
	}

	if b.state.Load() == stateUnsealed {
		return nil, ErrAlreadyUnsealed
	}

	threshold := b.config.Shamir.Threshold
	total := b.config.Shamir.TotalShares

	ttl := DefaultQuorumTTL
	if b.config.Shamir.QuorumTTL > 0 {
		ttl = b.config.Shamir.QuorumTTL
	}

	// Get or create the accumulator. Reset if the previous one expired.
	acc := b.loadOrCreateAccumulator(threshold, total, ttl)

	prog, err := acc.AddShare(ctx, shareValue)
	if err != nil {
		return nil, err
	}

	if !prog.Complete {
		return prog, nil
	}

	// Threshold met: reconstruct the secret and unseal.
	secret, err := acc.Reconstruct(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirCombineFailed, err)
	}

	// Clear the accumulator.
	acc.Reset()

	if strat := b.ShamirStrategy(); strat != nil {
		// Direct mode: the reconstructed secret IS the root key.
		if err := b.unsealDirect(secret); err != nil {
			return nil, err
		}
	} else {
		// Credential mode: the reconstructed secret is the password.
		if err := b.Unseal(ctx, Credentials{Secret: string(secret)}); err != nil {
			return nil, err
		}
	}

	return prog, nil
}

// loadOrCreateAccumulator returns the current ShareAccumulator or creates a
// new one. If the existing accumulator has expired, it is replaced.
func (b *StorageBarrier) loadOrCreateAccumulator(threshold, total int, ttl time.Duration) ShareAccumulator {
	val := b.accumulator.Load()
	if acc, ok := val.(ShareAccumulator); ok && acc != nil && !acc.Expired() {
		return acc
	}

	fresh := NewShamirShareAccumulator(threshold, total, ttl)
	b.accumulator.Store(fresh)
	return fresh
}

// UnsealWithShares unseals the barrier using a complete set of Shamir shares
// in a single call. This is useful when all shares are available at once
// (e.g., from an automated recovery process).
func (b *StorageBarrier) UnsealWithShares(ctx context.Context, shareValues []string) error {
	if b.config.Shamir == nil {
		return ErrShamirNotConfigured
	}

	if b.state.Load() == stateUnsealed {
		return ErrAlreadyUnsealed
	}

	threshold := b.config.Shamir.Threshold

	if len(shareValues) < threshold {
		return ErrShamirQuorumIncomplete
	}

	shares := make([]*shamir.Share, len(shareValues))
	for i, val := range shareValues {
		shares[i] = &shamir.Share{
			Index:     i + 1,
			Threshold: threshold,
			Total:     b.config.Shamir.TotalShares,
			Value:     val,
		}
	}

	secret, err := shamir.Combine(shares)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrShamirCombineFailed, err)
	}

	if strat := b.ShamirStrategy(); strat != nil {
		return b.unsealDirect(secret)
	}

	return b.Unseal(ctx, Credentials{Secret: string(secret)})
}
