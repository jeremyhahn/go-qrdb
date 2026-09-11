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
	"github.com/jeremyhahn/go-quicraft/pkg/enclave"

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

// copyEnclaveToBuffer copies the reconstructed secret out of enc into a freshly
// allocated PROTECTED (mlock'd, guard-paged) buffer the caller MUST Destroy. The
// secret therefore never lands in an ordinary garbage-collected heap allocation
// — the layered protected-memory rule holds across the whole downstream
// operation, not just until a wipe.
//
// The destination and its view are obtained BEFORE the source is borrowed, and
// the copy happens inside the source borrow, so no protected ALLOCATION runs
// while any enclave borrow is held. That ordering matters because the barrier's
// unseal/verify — which the caller runs next, on buf.Bytes() — allocate
// protected memory of their own, and go-quicraft v0.9's MemoryEnclave.WithBytes
// holds the enclave package's purgeMu as a READ lock across its callback; taking
// that lock EXCLUSIVELY for an allocation while already holding it as a reader
// self-deadlocks. The caller reads the secret via buf.Bytes() with no borrow
// held and Destroys the buffer when the downstream operation returns.
//
// pkg/secure and go-quicraft's enclave package have SEPARATE purgeMu mutexes, so
// allocating this secure.Buffer does not contend with the source enclave's
// borrow.
//
// # Why dst is read with Bytes and not borrowed across the copy
//
// UNBORROWED, and not for the injected-interface reason the other surviving
// Bytes readers give (go-qrdb#74). Holding dst's borrow across enc.WithBytes
// would nest a SECOND protected-memory borrow inside the first, which
// go-quicraft's MemoryEnclave.WithBytes forbids in terms — "Reading a DIFFERENT
// enclave from inside fn is ALSO forbidden" — because the two packages hold
// separate process-wide purge locks and Go's RWMutex admits no new reader once
// a writer is queued. Taking them in one order here would also fix a
// cross-package lock order for the whole process, which is not a decision this
// function gets to make on its own.
//
// So the window on view is real and open: a purge landing during the copy
// unmaps the destination. It closes when the underlying primitive stops having
// a process-wide purge (go-qrdb#145 and the go-enclave adoption), not before.
// TestCopyEnclaveToBuffer_AllocatesOutsideEveryBorrow pins what IS true today:
// nothing purge-class runs under either borrow.
func copyEnclaveToBuffer(enc *enclave.MemoryEnclave) (*secure.Buffer, error) {
	dst, err := secure.NewBuffer(enc.Size())
	if err != nil {
		return nil, err
	}
	view := dst.Bytes()
	if err := enc.WithBytes(func(src []byte) error {
		copy(view, src)
		return nil
	}); err != nil {
		dst.Destroy()
		return nil, err
	}
	return dst, nil
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

	// Wrap the operator's share string into protected memory at once, then
	// hand ownership to the accumulator. NewProtectedShare takes ownership of
	// the byte copy and wipes it. Index is unauthenticated metadata that
	// CombineProtected ignores — it reads the x-coordinate from the value — so
	// a positional label suffices, and it stays within [1, total] because the
	// quorum completes (and the accumulator resets) at threshold <= total. The
	// caller's string itself cannot be wiped; that residue is the transport
	// boundary go-quicraft #35 names.
	raw := []byte(shareValue)
	share, err := shamir.NewProtectedShare(acc.GetProgress().Submitted+1, threshold, total, raw)
	// NewProtectedShare wipes raw on success but, by contract, leaves it
	// untouched on a validation failure — so wipe unconditionally: a malformed
	// submission must not strand a plaintext copy on the heap.
	secure.Wipe(raw)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "protect share", Cause: err}
	}

	prog, err := acc.AddProtectedShare(ctx, share)
	if err != nil {
		return nil, err
	}

	if !prog.Complete {
		return prog, nil
	}

	// Threshold met: reconstruct the secret into protected memory and unseal.
	secretEnclave, err := acc.ReconstructProtected(ctx)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Cause: err}
	}
	// Destroy the reconstructed secret once the unseal has consumed it, and
	// destroy every accumulated share immediately — a subsequent unseal
	// requires the Security Officers to submit fresh shares.
	defer secretEnclave.Destroy()
	acc.Reset()

	// Copy the secret into a protected buffer and unseal OUTSIDE the enclave
	// borrow: the unseal allocates protected memory, which deadlocks under
	// WithBytes (see copyEnclaveToBuffer). The buffer is destroyed the moment
	// the unseal returns.
	secretBuf, err := copyEnclaveToBuffer(secretEnclave)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "read reconstructed secret", Cause: err}
	}
	defer secretBuf.Destroy()

	if shamirStrategy := b.ShamirStrategy(); shamirStrategy != nil {
		// Direct mode: the reconstructed secret is the unseal key (or, on
		// legacy barriers, the root key itself); unsealDirect resolves it.
		if err := b.unsealDirect(ctx, secretBuf.Bytes()); err != nil {
			return nil, err
		}
	} else if err := b.Unseal(ctx, Credentials{Secret: secretBuf.Bytes()}); err != nil {
		// Credential mode: the reconstructed secret is the password.
		return nil, err
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

	// Each share is one mlock'd allocation, so the set is destroyed however
	// this function unwinds — including the error return inside the loop.
	shares := make([]*shamir.ProtectedShare, 0, len(shareValues))
	defer func() {
		for _, share := range shares {
			share.Destroy()
		}
	}()
	for i, val := range shareValues {
		// NewProtectedShare takes ownership of raw and wipes it on success, so
		// the copy is cleared as it enters protected memory; wipe unconditionally
		// so a validation failure (which leaves raw untouched, by contract) does
		// not strand a plaintext copy on the heap. The caller's string cannot be
		// wiped — shareValues is []string because operators submit share strings
		// through the transport, the residue at the transport boundary that
		// go-quicraft#35 names (the string-mandating interface was removed in
		// go-quicraft v0.9.0-alpha).
		raw := []byte(val)
		share, err := shamir.NewProtectedShare(i+1, threshold, b.config.Shamir.TotalShares, raw)
		secure.Wipe(raw)
		if err != nil {
			return &ShamirError{Sentinel: ErrShamirCombineFailed, Cause: err}
		}
		shares = append(shares, share)
	}

	secretEnclave, err := shamir.CombineProtected(shares)
	if err != nil {
		return &ShamirError{Sentinel: ErrShamirCombineFailed, Cause: err}
	}
	// The reconstructed secret no longer exists as an ordinary heap
	// allocation. It used to, cleaned up with secure.Wipe — which is a real
	// wipe, but only after the value had already lived in collectable memory
	// for the duration of the unseal.
	defer secretEnclave.Destroy()

	// Copy the secret into a protected buffer and unseal OUTSIDE the enclave
	// borrow: the unseal allocates protected memory, which deadlocks under
	// WithBytes (see copyEnclaveToBuffer). The buffer is destroyed the moment
	// the unseal returns.
	secretBuf, err := copyEnclaveToBuffer(secretEnclave)
	if err != nil {
		return &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "read reconstructed secret", Cause: err}
	}
	defer secretBuf.Destroy()

	if shamirStrategy := b.ShamirStrategy(); shamirStrategy != nil {
		return b.unsealDirect(ctx, secretBuf.Bytes())
	}
	return b.Unseal(ctx, Credentials{Secret: secretBuf.Bytes()})
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
	seen      map[string]struct{}      // SHA-256 hex digests of submitted shares (one-way)
	shares    []*shamir.ProtectedShare // protected share values, in submission order
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
		shares:    make([]*shamir.ProtectedShare, 0, threshold),
		threshold: threshold,
		total:     total,
		ttl:       ttl,
		createdAt: time.Now(),
	}
}

// AddProtectedShare adds one protected share toward the quorum, TAKING
// OWNERSHIP of it: the accumulator destroys the share whether or not it is
// accepted and whether or not the quorum ever completes, so a caller must not
// touch it again after this returns. It rejects expired accumulators and
// duplicate shares, identified by a SHA-256 digest computed inside the share's
// protected borrow so the raw value is never copied out to form the map key.
func (a *shamirShareAccumulator) AddProtectedShare(_ context.Context, share *shamir.ProtectedShare) (*QuorumProgress, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.expired() {
		share.Destroy()
		return nil, ErrShamirQuorumExpired
	}

	var key string
	if err := share.WithValue(func(v []byte) error {
		digest := sha256.Sum256(v)
		key = hex.EncodeToString(digest[:])
		return nil
	}); err != nil {
		share.Destroy()
		return nil, &ShamirError{Sentinel: ErrShamirSplitFailed, Op: "digest share", Cause: err}
	}
	if _, exists := a.seen[key]; exists {
		share.Destroy()
		return nil, ErrShamirDuplicateShare
	}

	a.seen[key] = struct{}{}
	a.shares = append(a.shares, share)

	submitted := len(a.shares)
	return &QuorumProgress{
		Required:  a.threshold,
		Submitted: submitted,
		Complete:  submitted >= a.threshold,
	}, nil
}

// ReconstructProtected reconstructs the secret from the accumulated shares into
// locked memory. It returns an error if the threshold has not been met. The
// caller owns the returned enclave and must Destroy it. The share plaintext is
// never materialized as an ordinary, garbage-collected []byte: CombineProtected
// works share by share inside protected memory and returns the secret in an
// enclave.
func (a *shamirShareAccumulator) ReconstructProtected(_ context.Context) (*enclave.MemoryEnclave, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.shares) < a.threshold {
		return nil, ErrShamirQuorumIncomplete
	}

	secretEnclave, err := shamir.CombineProtected(a.shares)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "combine shares", Cause: err}
	}
	return secretEnclave, nil
}

// Reset destroys every accumulated share enclave and resets the TTL timer.
// The unseal path calls this immediately after a successful reconstruction,
// so reconstructed-quorum shares never linger.
func (a *shamirShareAccumulator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, share := range a.shares {
		share.Destroy()
	}
	a.seen = make(map[string]struct{})
	a.shares = make([]*shamir.ProtectedShare, 0, a.threshold)
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

// shamirShareSplitter implements ShareSplitter using go-quicraft's Shamir
// library.
type shamirShareSplitter struct{}

// NewShamirShareSplitter creates a ShareSplitter.
func NewShamirShareSplitter() ShareSplitter {
	return &shamirShareSplitter{}
}

// SplitProtected divides the secret into threshold-of-total Shamir shares whose
// values live in protected memory. The caller owns the returned shares and MUST
// Destroy every one of them, including on its own error paths — each is one
// mlock'd allocation charged against RLIMIT_MEMLOCK.
//
// go-quicraft v0.9.0-alpha removed the string-returning Split interface this
// once had to satisfy: splitting into locked memory only to copy each value
// back into an immortal heap string defeated the protection, which is why the
// SA1019 residue this replaces existed (go-quicraft #35/#37).
func (s *shamirShareSplitter) SplitProtected(secret []byte, threshold, total int) ([]*shamir.ProtectedShare, error) {
	shares, err := shamir.SplitProtected(secret, threshold, total)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirSplitFailed, Cause: err}
	}
	return shares, nil
}
