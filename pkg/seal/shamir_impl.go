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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Compile-time interface checks.
var (
	_ ShareAccumulator = (*shamirShareAccumulator)(nil)
	_ ShareSplitter    = (*shamirShareSplitter)(nil)
)

// shamirShareAccumulator implements ShareAccumulator using go-quicraft's
// Shamir secret-sharing library.
//
// Security model: submitted share material is held only inside memguard
// secure enclaves (never as raw, garbage-collected strings/bytes), and is
// destroyed the instant the quorum is reset — which the unseal path triggers
// immediately after a successful reconstruction. Deduplication uses a SHA-256
// digest of each share, so the raw share value is never retained as a map
// key. Plaintext is materialized only transiently, under the lock, during
// Reconstruct.
type shamirShareAccumulator struct {
	mu        sync.Mutex
	seen      map[string]struct{} // SHA-256 hex digests of submitted shares (one-way)
	shares    []*secure.Secret    // enclave-sealed share values, in submission order
	threshold int
	total     int
	ttl       time.Duration
	createdAt time.Time
}

// NewShamirShareAccumulator creates a ShareAccumulator for the given
// threshold/total configuration with the specified TTL.
func NewShamirShareAccumulator(threshold, total int, ttl time.Duration) ShareAccumulator {
	return &shamirShareAccumulator{
		seen:      make(map[string]struct{}),
		shares:    make([]*secure.Secret, 0, threshold),
		threshold: threshold,
		total:     total,
		ttl:       ttl,
		createdAt: time.Now(),
	}
}

// AddShare seals a share into a memguard enclave toward the quorum. It rejects
// expired accumulators and duplicate shares (identified by a SHA-256 digest,
// so the raw share value is never retained outside the enclave).
func (a *shamirShareAccumulator) AddShare(_ context.Context, share string) (*QuorumProgress, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.expired() {
		return nil, ErrShamirQuorumExpired
	}

	digest := sha256.Sum256([]byte(share))
	key := hex.EncodeToString(digest[:])
	if _, exists := a.seen[key]; exists {
		return nil, ErrShamirDuplicateShare
	}

	// Seal a copy of the share into an enclave; NewSecret wipes the copy.
	sec, err := secure.NewSecret([]byte(share))
	if err != nil {
		return nil, fmt.Errorf("%w: seal share: %v", ErrShamirSplitFailed, err)
	}

	a.seen[key] = struct{}{}
	a.shares = append(a.shares, sec)

	submitted := len(a.shares)
	return &QuorumProgress{
		Required:  a.threshold,
		Submitted: submitted,
		Complete:  submitted >= a.threshold,
	}, nil
}

// Reconstruct attempts to reconstruct the secret from accumulated shares.
// Returns an error if the threshold has not been met. Share plaintext is
// materialized only transiently: each enclave is opened into a buffer that is
// destroyed before return.
func (a *shamirShareAccumulator) Reconstruct(_ context.Context) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.shares) < a.threshold {
		return nil, ErrShamirQuorumIncomplete
	}

	bufs := make([]*secure.Buffer, 0, len(a.shares))
	defer func() {
		for _, b := range bufs {
			b.Destroy()
		}
	}()

	combineShares := make([]*shamir.Share, 0, len(a.shares))
	for i, sec := range a.shares {
		buf, err := sec.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: open share: %v", ErrShamirCombineFailed, err)
		}
		bufs = append(bufs, buf)
		combineShares = append(combineShares, &shamir.Share{
			Index:     i + 1,
			Threshold: a.threshold,
			Total:     a.total,
			Value:     string(buf.Bytes()),
		})
	}

	return shamir.CombineUnverified(combineShares)
}

// Reset destroys every accumulated share enclave and resets the TTL timer.
// The unseal path calls this immediately after a successful reconstruction,
// so reconstructed-quorum shares never linger.
func (a *shamirShareAccumulator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, sec := range a.shares {
		sec.Destroy()
	}
	a.seen = make(map[string]struct{})
	a.shares = make([]*secure.Secret, 0, a.threshold)
	a.createdAt = time.Now()
}

// GetProgress returns the current quorum progress without modifying state.
// This allows callers to check the accumulator's state without submitting
// a new share. This method is safe for concurrent use.
func (a *shamirShareAccumulator) GetProgress() *QuorumProgress {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &QuorumProgress{
		Required:  a.threshold,
		Submitted: len(a.shares),
		Complete:  len(a.shares) >= a.threshold,
	}
}

// Expired reports whether the accumulator session has exceeded its TTL.
func (a *shamirShareAccumulator) Expired() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.expired()
}

// expired reports whether the TTL has elapsed (must hold a.mu).
func (a *shamirShareAccumulator) expired() bool {
	return time.Since(a.createdAt) > a.ttl
}

// shamirShareSplitter implements ShareSplitter using go-qrdb's Shamir library.
type shamirShareSplitter struct{}

// NewShamirShareSplitter creates a ShareSplitter.
func NewShamirShareSplitter() ShareSplitter {
	return &shamirShareSplitter{}
}

// Split divides the secret into threshold-of-total Shamir shares.
func (s *shamirShareSplitter) Split(secret []byte, threshold, total int) ([]string, error) {
	shares, err := shamir.Split(secret, threshold, total)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirSplitFailed, err)
	}
	result := make([]string, len(shares))
	for i, share := range shares {
		result[i] = share.Value
	}
	return result, nil
}
