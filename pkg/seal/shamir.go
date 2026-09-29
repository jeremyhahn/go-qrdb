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

	"github.com/jeremyhahn/go-enclave"
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

	// The credential secret is protected memory; open it into a fresh locked
	// enclave and hand SealAndDistribute a working copy it owns and wipes on
	// return. The caller's credential is untouched — it still owns and
	// destroys it — so the []byte aliasing this used to rely on (and warn
	// about) no longer exists.
	if creds.Secret == nil {
		return nil, ErrInvalidPassphrase
	}
	secretEnclave, err := creds.Secret.Open()
	if err != nil {
		return nil, err
	}
	defer secretEnclave.Destroy()
	secretBuf, err := copyEnclaveToBuffer(secretEnclave)
	if err != nil {
		return nil, err
	}
	defer secretBuf.Destroy()

	//secure:unborrowed injected-interface b.distributor is a consumer-supplied share distributor (go-qrdb#428)
	shareCount, err := b.distributor.SealAndDistribute(ctx, secretBuf.Bytes(), threshold, total)
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

// copyEnclaveToBufferHook runs, still inside both enclaves' held borrows,
// immediately after the copy lands and before either WithBytes call returns.
// It is a no-op in production — this is a direct assignment, not a
// conditional, so there is no branch to skip — and exists purely so a test
// can inject a delay: the copy itself completes in nanoseconds, far too fast
// to reliably race enclave.WipeAll against without instrumentation, which
// would otherwise leave a regression that reverted this function back to an
// unguarded dst.Bytes() read passing every existing test right up until a
// real WipeAll actually landed in the (now unguarded) gap in production.
var copyEnclaveToBufferHook = func() {}

// copyEnclaveToBuffer copies the reconstructed secret out of enc into a freshly
// allocated PROTECTED (mlock'd, guard-paged) buffer the caller MUST Destroy. The
// secret therefore never lands in an ordinary garbage-collected heap allocation
// — the layered protected-memory rule holds across the whole downstream
// operation, not just until a wipe.
//
// The destination is allocated BEFORE the source is borrowed, so no protected
// ALLOCATION runs while any enclave borrow is held. Historically (go-quicraft
// pre-v0.11.0-alpha, before it consumed github.com/jeremyhahn/go-enclave
// directly) that ordering was load-bearing for a real deadlock:
// MemoryEnclave.WithBytes held that package's OWN internal purgeMu as a READ
// lock across its callback, and taking it EXCLUSIVELY for an allocation from
// inside an already-held read self-deadlocked. go-enclave has no such
// package-wide lock — every enclave, in pkg/secure and in go-quicraft's
// pkg/crypto/shamir alike, carries its own per-instance lock (see
// pkg/secure/doc.go and pkg/secure/borrowguard.go's own "go-enclave, not
// memguard" sections) — so allocating dst while enc's borrow is held could no
// longer deadlock against it. The ordering is kept anyway as allocation
// hygiene, and because it costs nothing to keep.
//
// The copy itself holds BOTH enclaves' borrows for its whole duration — dst's
// via its own WithBytes, not dst.Bytes(), which would release go-enclave's
// lock on dst the instant it returned. Doing that here would leave the exact
// gap SecretValue.WithBytes's own round-3 fix closed (go-qrdb#114 review):
// dst is freshly allocated and already registered on go-enclave's shutdown
// ledger the moment secure.NewBuffer returns, so a concurrent enclave.WipeAll
// needs no external reference to dst to destroy it mid-copy — an unguarded
// dst.Bytes() write could fault against pages WipeAll had already unmapped.
// Nesting the two borrows is safe: go-enclave gives every enclave its own
// independent lock, so holding dst's and enc's at once cannot deadlock either
// against the other or against anything else in the process.
func copyEnclaveToBuffer(enc *enclave.MemoryEnclave) (*secure.Buffer, error) {
	dst, err := secure.NewBuffer(enc.Size())
	if err != nil {
		return nil, err
	}
	if err := dst.WithBytes(func(view []byte) error {
		return enc.WithBytes(func(src []byte) error {
			copy(view, src)
			copyEnclaveToBufferHook()
			return nil
		})
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
	//
	// BOUNDED EXCEPTION to server.secure_memory_budget_mib (go-qrdb#114
	// review, round 3): NewProtectedShare builds its enclave.MemoryEnclave via
	// go-enclave's unbudgeted NewMemoryEnclaveSized, not through
	// enclave.ProcessAllocator — go-quicraft's pkg/crypto/shamir exposes no
	// allocator-aware variant as of the version this repository pins. Tracked
	// upstream as go-quicraft#204: an Allocator-accepting overload of
	// NewProtectedShare/SplitProtected/CombineProtected, or a package-level
	// SetAllocator the existing functions consult. The same is true of
	// CombineProtected below and in ReconstructProtected.
	// This is reachable only from a permission-gated caller
	// (barrier:unseal-share on every transport), one page-rounded enclave per
	// share. The count is bounded less tightly than the M-of-N suggests: the
	// accumulator normally holds at most threshold shares, but a failed
	// reconstruction does not reset it, so it keeps accepting distinct shares
	// until its TTL; and an expired accumulator is released on the NEXT
	// submission (loadOrCreateAccumulator), not by a timer. Not the
	// unauthenticated surface go-qrdb#114 closed elsewhere, but a real gap in
	// this budget's promise, stated precisely in docs/configuration/cli.md
	// ("Locked memory outside the budget") and tracked for a fix in
	// go-quicraft rather than papered over here.
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

	//secure:unborrowed injected-interface unsealWithKey dispatches to a SealingStrategy (go-qrdb#428)
	if err := b.unsealWithKey(ctx, secretBuf.Bytes()); err != nil {
		return nil, err
	}

	return prog, nil
}

// unsealWithSecret runs a credential-mode Unseal with secret as the
// passphrase. NewCredentials moves the secret into protected memory and wipes
// the source — which is the caller's protected buffer, destroyed on its own
// return in any case — and the credential is destroyed as soon as the unseal
// has consumed it.
func (b *StorageBarrier) unsealWithSecret(ctx context.Context, secret []byte) error {
	creds, err := NewCredentials(secret)
	if err != nil {
		return err
	}
	defer creds.Secret.Destroy()
	return b.Unseal(ctx, creds)
}

// unsealWithKey unseals the barrier from an already-reconstructed key. It is
// the single resolution step UnsealWithShare and UnsealWithShares share:
// directly when a ShamirStrategy is registered (the key is the unseal key, or
// on legacy barriers the root key itself), or as a credential-mode passphrase
// otherwise. It is deliberately unexported: the only way to reach it is
// through this barrier's own quorum accumulator.
//
// key is read but never retained: unsealDirect and unsealWithSecret each copy
// or wrap it into protected memory of their own, and this method does not
// wipe the caller's buffer, since ownership was never transferred.
//
// Returns ErrAlreadyUnsealed if the barrier is already unsealed.
func (b *StorageBarrier) unsealWithKey(ctx context.Context, key []byte) error {
	if b.state.Load() == stateUnsealed {
		return ErrAlreadyUnsealed
	}

	if shamirStrategy := b.ShamirStrategy(); shamirStrategy != nil {
		return b.unsealDirect(ctx, key)
	}
	return b.unsealWithSecret(ctx, key)
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
		// BOUNDED EXCEPTION to server.secure_memory_budget_mib: see the fuller
		// comment on UnsealWithShare's own NewProtectedShare call, above. Here
		// the bound is one page-rounded enclave per element of shareValues,
		// held for this call only, from a permission-gated caller, and at most
		// TotalShares of them: element i is built with index i+1, which
		// NewProtectedShare rejects above total BEFORE allocating, and the
		// deferred loop above destroys the ones already built.
		share, err := shamir.NewProtectedShare(i+1, threshold, b.config.Shamir.TotalShares, raw)
		secure.Wipe(raw)
		if err != nil {
			return &ShamirError{Sentinel: ErrShamirCombineFailed, Cause: err}
		}
		shares = append(shares, share)
	}

	// Same BOUNDED EXCEPTION as above: CombineProtected's result enclave is
	// also built via go-enclave's unbudgeted constructors, not
	// enclave.ProcessAllocator.
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

	//secure:unborrowed injected-interface unsealWithKey dispatches to a SealingStrategy (go-qrdb#428)
	return b.unsealWithKey(ctx, secretBuf.Bytes())
}

// Compile-time interface checks.
var (
	_ ShareAccumulator = (*shamirShareAccumulator)(nil)
	_ ShareSplitter    = (*shamirShareSplitter)(nil)
)

// shamirShareAccumulator implements ShareAccumulator using go-quicraft's
// Shamir secret-sharing library.
//
// Security model: submitted share material is held only inside go-enclave
// MemoryEnclaves (never as raw, garbage-collected strings/bytes), and is
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

	// Same BOUNDED EXCEPTION to server.secure_memory_budget_mib documented on
	// UnsealWithShare's NewProtectedShare call: CombineProtected's result
	// enclave is built via go-enclave's unbudgeted constructors, not
	// enclave.ProcessAllocator.
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
// mlock'd allocation charged against the kernel's RLIMIT_MEMLOCK ceiling, but
// NOT against server.secure_memory_budget_mib: the same BOUNDED EXCEPTION
// documented on UnsealWithShare's NewProtectedShare call applies here, and
// this call site is reached only from barrier initialization, an SO/operator-
// driven, one-time-per-ceremony path with total bounded by the M-of-N the
// operator configures.
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
