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

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// Compile-time interface checks.
var (
	_ ShareAccumulator = (*shamirShareAccumulator)(nil)
	_ ShareSplitter    = (*shamirShareSplitter)(nil)
)

// shamirShareAccumulator implements ShareAccumulator using go-qrdb's
// Shamir secret sharing library. Shares are deduplicated by value and
// maintained in insertion order.
type shamirShareAccumulator struct {
	mu        sync.Mutex
	seen      map[string]struct{}
	shares    []*shamir.Share
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
		shares:    make([]*shamir.Share, 0, threshold),
		threshold: threshold,
		total:     total,
		ttl:       ttl,
		createdAt: time.Now(),
	}
}

// AddShare adds a share to the accumulator. It rejects expired accumulators
// and duplicate shares (identified by their string value).
func (a *shamirShareAccumulator) AddShare(_ context.Context, share string) (*QuorumProgress, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.expired() {
		return nil, ErrShamirQuorumExpired
	}

	if _, exists := a.seen[share]; exists {
		return nil, ErrShamirDuplicateShare
	}

	a.seen[share] = struct{}{}
	a.shares = append(a.shares, &shamir.Share{
		Index:     len(a.shares) + 1,
		Threshold: a.threshold,
		Total:     a.total,
		Value:     share,
	})

	submitted := len(a.shares)
	return &QuorumProgress{
		Required:  a.threshold,
		Submitted: submitted,
		Complete:  submitted >= a.threshold,
	}, nil
}

// Reconstruct attempts to reconstruct the secret from accumulated shares.
// Returns an error if the threshold has not been met.
func (a *shamirShareAccumulator) Reconstruct(_ context.Context) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.shares) < a.threshold {
		return nil, ErrShamirQuorumIncomplete
	}

	return shamir.Combine(a.shares)
}

// Reset clears all accumulated shares and resets the TTL timer.
func (a *shamirShareAccumulator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = make(map[string]struct{})
	a.shares = make([]*shamir.Share, 0, a.threshold)
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
