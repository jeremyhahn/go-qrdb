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
	"encoding/json"
	"sync"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// InitializeShamir initializes the barrier using Shamir secret sharing.
// If a ShamirStrategy is registered, the root key itself is split into shares
// (direct mode). Otherwise, the credential secret is split and used to
// unseal a password-protected root key (credential mode).
//
// In both modes the generated shares are sealed to the enrolled Security
// Officers via the CeremonyDistributor; they are never returned in the
// response. ErrCeremonyDistributorNotConfigured is returned when no
// distributor has been wired via SetCeremonyDistributor.
//
// The threshold and total share counts come from b.config.Shamir, which the
// service layer populates via SetShamirConfig before calling this method.
// Callers that omit threshold/total in their request should preserve the
// existing config values rather than overwriting with zeros.
func (b *StorageBarrier) InitializeShamir(ctx context.Context, creds Credentials) (*ShamirInitParams, error) {
	if b.config.Shamir == nil {
		return nil, ErrShamirNotConfigured
	}
	if b.distributor == nil {
		return nil, ErrCeremonyDistributorNotConfigured
	}

	threshold := b.config.Shamir.Threshold
	total := b.config.Shamir.TotalShares

	if threshold < 2 || threshold > total {
		return nil, ErrShamirThresholdInvalid
	}

	// Direct mode: ShamirStrategy splits the root key itself.
	if shamirStrategy := b.ShamirStrategy(); shamirStrategy != nil {
		return b.initializeShamirDirect(ctx, shamirStrategy, threshold, total)
	}

	// Credential mode: initialize barrier with the credential, then
	// split the credential secret and seal shares to officers.
	if err := b.Initialize(ctx, creds); err != nil {
		return nil, err
	}

	// NOTE: []byte(creds.Secret) does NOT copy when Secret is already a []byte —
	// secret aliases the caller's credential buffer. SealAndDistribute wipes it
	// on return, so creds.Secret is zeroed as a side effect; callers must not
	// read creds.Secret after InitializeShamir returns.
	secret := []byte(creds.Secret)

	shareCount, err := b.distributor.SealAndDistribute(ctx, secret, threshold, total)
	if err != nil {
		return nil, err
	}

	return &ShamirInitParams{
		Threshold:   threshold,
		TotalShares: total,
		ShareCount:  shareCount,
	}, nil
}

// initializeShamirDirect initializes the barrier when a ShamirStrategy is
// registered. The EpochBarrier generates the root key during Initialize; we
// then read it back in memory, AEAD-wrap it under a fresh random unseal key
// (see unsealkey.go), and split the UNSEAL KEY — not the root key — into
// M-of-N shares. Each share is sealed to an enrolled Security Officer's
// ceremony enclave via the CeremonyDistributor. No share bytes are returned
// to the caller. The indirection lets Rekey cryptographically revoke all
// previously distributed shares by re-wrapping under a new unseal key.
//
// threshold and total come from the caller (b.config.Shamir after SetShamirConfig),
// not from the boot-time ShamirStrategy, so that the request parameters win.
func (b *StorageBarrier) initializeShamirDirect(ctx context.Context, shamirStrat *ShamirStrategy, threshold, total int) (*ShamirInitParams, error) {
	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrCorruptRootKey, Cause: err}
	}
	if exists {
		return nil, ErrAlreadyInitialized
	}

	// Initialize generates the root key, derives the initial DEK, persists the
	// (key-material-free) Shamir envelope, and leaves the barrier unsealed.
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		return nil, err
	}

	// Read the root key back in memory so it can be wrapped under the
	// unseal key below. It is wiped before this function returns.
	rootKey, err := b.GetMasterKey()
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(rootKey)

	// When the request parameters differ from the boot-time strategy, update
	// the persisted sealed envelope and replace the in-memory strategy so
	// that future UnsealWithShare uses the correct threshold/total.
	if threshold != shamirStrat.Threshold() || total != shamirStrat.TotalShares() {
		if updateErr := b.updateSealedShamirParams(ctx, threshold, total); updateErr != nil {
			return nil, updateErr
		}
	}

	// Unseal-key indirection: wrap the root key under a fresh unseal key and
	// persist the wrapped blob BEFORE distributing, so any claimed share set
	// always has a blob to unwrap. The unseal key — not the root key — is
	// what gets split to the officers; the distributor wipes it on return.
	unsealKey, err := generateUnsealKey()
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(unsealKey)

	wrappedBlob, err := b.wrapRootKey(unsealKey, rootKey)
	if err != nil {
		return nil, err
	}
	if err := b.base.Put(ctx, b.wrappedRootKeyPath(), wrappedBlob); err != nil {
		return nil, err
	}

	shareCount, err := b.distributor.SealAndDistribute(ctx, unsealKey, threshold, total)
	if err != nil {
		return nil, err
	}

	return &ShamirInitParams{
		Threshold:   threshold,
		TotalShares: total,
		ShareCount:  shareCount,
	}, nil
}

// updateSealedShamirParams writes new threshold/total values into the
// persisted (key-material-free) sealed envelope and replaces the in-memory
// ShamirStrategy so that future UnsealWithShare / Status calls reflect the
// updated scheme. This mirrors the pattern used in Rekey.
func (b *StorageBarrier) updateSealedShamirParams(ctx context.Context, threshold, total int) error {
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return ErrCorruptRootKey
	}
	sealed.ShamirThreshold = threshold
	sealed.ShamirTotal = total
	updatedBlob, err := json.Marshal(&sealed)
	if err != nil {
		return err
	}
	if err := b.base.Put(ctx, b.config.RootKeyPath, updatedBlob); err != nil {
		return err
	}

	newStrat, err := NewShamirStrategy(threshold, total)
	if err != nil {
		return err
	}
	b.setStrategy(StrategyShamir, newStrat)

	if b.config.Shamir != nil {
		b.config.Shamir.Threshold = threshold
		b.config.Shamir.TotalShares = total
	}
	return nil
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

	ttl := DefaultShareTTL
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
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Cause: err}
	}

	// Wipe the transient reconstructed secret once the unseal has consumed it,
	// and destroy every accumulated share immediately — a subsequent unseal
	// requires the Security Officers to submit fresh shares.
	defer secure.Wipe(secret)
	acc.Reset()

	if shamirStrategy := b.ShamirStrategy(); shamirStrategy != nil {
		// Direct mode: the reconstructed secret is the unseal key (or, on
		// legacy barriers, the root key itself); unsealDirect resolves it.
		if err := b.unsealDirect(ctx, secret); err != nil {
			return nil, err
		}
	} else {
		// Credential mode: the reconstructed secret is the password.
		if err := b.Unseal(ctx, Credentials{Secret: secret}); err != nil {
			return nil, err
		}
	}

	return prog, nil
}

// loadOrCreateAccumulator returns the current ShareAccumulator or creates a
// new one. If the existing accumulator has expired, it is replaced.
func (b *StorageBarrier) loadOrCreateAccumulator(threshold, total int, ttl time.Duration) ShareAccumulator {
	b.accumulatorMu.Lock()
	defer b.accumulatorMu.Unlock()

	if acc, ok := b.accumulator.Load().(ShareAccumulator); ok && acc != nil {
		if !acc.Expired() {
			return acc
		}
		// Expired: destroy its share enclaves before discarding so no share
		// material lingers in memory past the quorum TTL.
		acc.Reset()
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

	secret, err := shamir.CombineUnverified(shares) //nolint:staticcheck // SA1019: deprecated in go-quicraft v0.7.0-alpha in favour of the ProtectedShare family. Migrating is a key-custody change (mlock'd share values, per-share RLIMIT_MEMLOCK budgeting, destroy-as-consumed), tracked as #46 — not folded into a dependency bump. Closing #46 removes this directive.
	if err != nil {
		return &ShamirError{Sentinel: ErrShamirCombineFailed, Cause: err}
	}
	// Wipe the transient reconstructed secret once the unseal has consumed it,
	// matching the single-share UnsealWithShare path.
	defer secure.Wipe(secret)

	if shamirStrategy := b.ShamirStrategy(); shamirStrategy != nil {
		return b.unsealDirect(ctx, secret)
	}

	return b.Unseal(ctx, Credentials{Secret: secret})
}

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
		return nil, &ShamirError{Sentinel: ErrShamirSplitFailed, Op: "seal share", Cause: err}
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

	// Pass each share VALUE as a wipeable []byte straight from its enclave
	// buffer to CombineBytes. The buffers are destroyed by the deferred cleanup
	// above; this avoids ever materializing the share plaintext as an immutable,
	// garbage-collected (un-wipeable) Go string.
	values := make([][]byte, 0, len(a.shares))
	for _, sec := range a.shares {
		buf, err := sec.Open()
		if err != nil {
			return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "open share", Cause: err}
		}
		bufs = append(bufs, buf)
		values = append(values, buf.Bytes())
	}

	return shamir.CombineBytes(values, a.threshold)
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
	shares, err := shamir.Split(secret, threshold, total) //nolint:staticcheck // SA1019: deprecated in go-quicraft v0.7.0-alpha in favour of the ProtectedShare family. Migrating is a key-custody change (mlock'd share values, per-share RLIMIT_MEMLOCK budgeting, destroy-as-consumed), tracked as #46 — not folded into a dependency bump. Closing #46 removes this directive.
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirSplitFailed, Cause: err}
	}
	result := make([]string, len(shares))
	for i, share := range shares {
		result[i] = share.Value //nolint:staticcheck // SA1019: deprecated in go-quicraft v0.7.0-alpha in favour of the ProtectedShare family. Migrating is a key-custody change (mlock'd share values, per-share RLIMIT_MEMLOCK budgeting, destroy-as-consumed), tracked as #46 — not folded into a dependency bump. Closing #46 removes this directive.
	}
	return result, nil
}
