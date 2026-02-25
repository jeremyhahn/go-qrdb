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
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/shamir"
)

const defaultQuorumTTL = 5 * time.Minute

// ShamirInitResult contains the output of a Shamir-based barrier initialization,
// including the generated shares that must be distributed to custodians.
type ShamirInitResult struct {
	Shares      []string `json:"shares"`
	Threshold   int      `json:"threshold"`
	TotalShares int      `json:"total_shares"`
}

// QuorumProgress reports the current state of a threshold quorum accumulator.
type QuorumProgress struct {
	Required  int  `json:"required"`
	Submitted int  `json:"submitted"`
	Complete  bool `json:"complete"`
}

// shamirAccumulator collects Shamir shares from individual custodians until
// the threshold is met. Shares are deduplicated by their base64 Value and
// maintained in insertion order for deterministic reconstruction.
type shamirAccumulator struct {
	mu        sync.Mutex
	shares    map[string]struct{} // dedup set keyed by share Value
	ordered   []*shamir.Share     // insertion-order shares
	threshold int
	total     int
	ttl       time.Duration
	createdAt time.Time
}

// newShamirAccumulator creates an accumulator that accepts shares for the
// given threshold/total configuration and expires after ttl.
func newShamirAccumulator(threshold, total int, ttl time.Duration) *shamirAccumulator {
	return &shamirAccumulator{
		shares:    make(map[string]struct{}),
		ordered:   make([]*shamir.Share, 0, threshold),
		threshold: threshold,
		total:     total,
		ttl:       ttl,
		createdAt: time.Now(),
	}
}

// expired reports whether the accumulator's TTL has elapsed.
func (a *shamirAccumulator) expired() bool {
	return time.Since(a.createdAt) > a.ttl
}

// addShare adds a share to the accumulator. It rejects expired accumulators
// and duplicate shares (identified by their Value field).
func (a *shamirAccumulator) addShare(share *shamir.Share) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.expired() {
		return ErrShamirQuorumExpired
	}

	if _, exists := a.shares[share.Value]; exists {
		return ErrShamirDuplicateShare
	}

	a.shares[share.Value] = struct{}{}
	a.ordered = append(a.ordered, share)
	return nil
}

// progress returns the current quorum progress without acquiring the lock.
// The caller must hold a.mu if concurrent access is possible; however, this
// method is intentionally lock-free so Barrier methods can call it after
// already holding the lock via addShare.
func (a *shamirAccumulator) progress() *QuorumProgress {
	a.mu.Lock()
	defer a.mu.Unlock()

	submitted := len(a.ordered)
	return &QuorumProgress{
		Required:  a.threshold,
		Submitted: submitted,
		Complete:  submitted >= a.threshold,
	}
}

// getShares returns a copy of the accumulated shares in insertion order.
func (a *shamirAccumulator) getShares() []*shamir.Share {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]*shamir.Share, len(a.ordered))
	copy(out, a.ordered)
	return out
}

// InitializeShamir initializes the barrier using Shamir secret sharing.
// If a ShamirStrategy is registered, the root key itself is split into shares
// (direct mode). Otherwise, the credential secret is split and used to
// unseal a password-protected root key (credential mode).
func (b *Barrier) InitializeShamir(ctx context.Context, creds Credentials) (*ShamirInitResult, error) {
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
func (b *Barrier) initializeShamirDirect(ctx context.Context, shamirStrat *ShamirStrategy) (*ShamirInitResult, error) {
	exists, err := b.base.Exists(b.config.RootKeyPath)
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

	shares, err := shamirStrat.LoadAllShares()
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
func (b *Barrier) UnsealWithShare(ctx context.Context, shareValue string) (*QuorumProgress, error) {
	if b.config.Shamir == nil {
		return nil, ErrShamirNotConfigured
	}

	if b.state.Load() == 1 {
		return nil, ErrAlreadyUnsealed
	}

	threshold := b.config.Shamir.Threshold
	total := b.config.Shamir.TotalShares
	ttl := defaultQuorumTTL

	// Get or create the accumulator. Reset if the previous one expired.
	acc := b.loadOrCreateAccumulator(threshold, total, ttl)

	share := &shamir.Share{
		Index:     len(acc.getShares()) + 1,
		Threshold: threshold,
		Total:     total,
		Value:     shareValue,
	}

	if err := acc.addShare(share); err != nil {
		return nil, err
	}

	prog := acc.progress()
	if !prog.Complete {
		return prog, nil
	}

	// Threshold met: reconstruct the secret and unseal.
	shares := acc.getShares()
	b.accumulator.Store((*shamirAccumulator)(nil))

	secret, err := shamir.Combine(shares)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirCombineFailed, err)
	}

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

// loadOrCreateAccumulator returns the current accumulator or creates a new one.
// If the existing accumulator has expired, it is replaced with a fresh one.
func (b *Barrier) loadOrCreateAccumulator(threshold, total int, ttl time.Duration) *shamirAccumulator {
	for {
		val := b.accumulator.Load()
		if acc, ok := val.(*shamirAccumulator); ok && acc != nil && !acc.expired() {
			return acc
		}

		fresh := newShamirAccumulator(threshold, total, ttl)
		b.accumulator.Store(fresh)
		return fresh
	}
}

// UnsealWithShares unseals the barrier using a complete set of Shamir shares
// in a single call. This is useful when all shares are available at once
// (e.g., from an automated recovery process).
func (b *Barrier) UnsealWithShares(ctx context.Context, shareValues []string) error {
	if b.config.Shamir == nil {
		return ErrShamirNotConfigured
	}

	if b.state.Load() == 1 {
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
