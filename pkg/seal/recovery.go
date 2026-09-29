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
	"errors"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// RecoveryKeyResult contains the output of recovery key generation. The
// recovery shares are sealed to the enrolled recovery holders and never
// returned; only the share-set shape is reported.
type RecoveryKeyResult struct {
	ShareCount  int `json:"share_count"`
	Threshold   int `json:"threshold"`
	TotalShares int `json:"total_shares"`
}

// ErrRecoveryKeysExist is returned by GenerateRecoveryKeys when the barrier
// already has a recovery key. Replacing it (a new threshold, a lost or
// revoked recovery holder) is a rekey of the recovery domain, which needs a
// quorum of the current recovery holders.
var ErrRecoveryKeysExist = errors.New("qrdb/seal: recovery keys already exist; rekey the recovery domain to replace them")

// GenerateRecoveryKeys creates the recovery-key domain: an independent random
// recovery key that wraps the root key in its own blob
// (<RootKeyPath>.recovery.wrapped) bound to the recovery holders' roster,
// split threshold-of-N to those holders. The root key itself is never split.
//
// Because the recovery key is independent of the unseal key, a rekey of either
// domain revokes that domain's earlier shares without touching the other.
//
// The barrier must be unsealed. It is refused with ErrRecoveryKeysExist when
// a recovery key already exists.
func (b *StorageBarrier) GenerateRecoveryKeys(ctx context.Context, threshold int, policy FactorPolicy) (*RecoveryKeyResult, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}
	if threshold < 2 {
		return nil, ErrShamirThresholdInvalid
	}
	exists, err := b.base.Exists(ctx, b.wrappedKeyPath(DomainRecovery))
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrRecoveryKeysExist
	}
	rootKey, err := b.masterKeyBuffer()
	if err != nil {
		return nil, err
	}
	defer rootKey.Destroy()

	params, err := b.redistribute(ctx, DomainRecovery, rootKey, threshold, policy, nil, 0)
	if err != nil {
		return nil, err
	}
	return &RecoveryKeyResult{ShareCount: params.ShareCount, Threshold: params.Threshold, TotalShares: params.TotalShares}, nil
}

// unsealFromRootKey unseals the EpochBarrier with a root key a completed
// quorum authenticated (by opening a domain's wrapped blob). The strategy
// that sealed the root key cannot reproduce it — a Shamir strategy keeps no
// key material, and a recovery may run without the passphrase — so a
// recovery strategy hands the in-hand key to the EpochBarrier, delegating
// encryptor construction to the sealing strategy. rootKey is consumed.
//
// active is recorded as the barrier's active strategy: StrategyShamir for a
// Shamir unseal, "recovery" for a recovery-key unseal.
func (b *StorageBarrier) unsealFromRootKey(ctx context.Context, rootKey *secure.Buffer, active StrategyID) error {
	if b.state.Load() == stateUnsealed {
		return ErrAlreadyUnsealed
	}
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return ErrCorruptRootKey
	}
	delegate, ok := b.strategyByID(sealed.Strategy)
	if !ok {
		return ErrStrategyNotFound
	}
	if err := rootKey.WithBytes(func(key []byte) error {
		return b.stampNoneOriginSealedState(key, &sealed)
	}); err != nil {
		return err
	}
	if err := b.barrier.SetSealedRootKey(&sealed); err != nil {
		return err
	}
	// newRecoveryStrategyFromBuffer consumes rootKey, so the key never
	// passes through an ordinary heap slice; Close destroys the strategy's
	// protected copy, which Barrier.Unseal does not retain.
	recStrat, err := newRecoveryStrategyFn(rootKey, delegate)
	if err != nil {
		return err
	}
	defer func() { _ = recStrat.Close() }()
	if err := b.barrier.Unseal(ctx, recStrat, Credentials{}); err != nil {
		return err
	}

	b.activeStrategy.Store(active)
	b.state.Store(stateUnsealed)
	b.restoreInitializedAt(ctx)
	emitAudit(b.config.AuditLogger, "unseal", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"strategy": string(active)})
	return nil
}

// recoveryStrategy wraps a pre-recovered root key so the EpochBarrier can
// be unsealed without going through the original strategy's KDF. This is
// needed because the original strategy may require credentials (e.g., a
// deferred-passphrase software strategy) that are not available during
// disaster recovery.
type recoveryStrategy struct {
	// rootKey holds the pre-recovered root key in protected memory for the
	// whole barrier-unseal operation, not an ordinary heap []byte. The most
	// sensitive recovery material must live in an mlock'd, guard-paged enclave
	// while the strategy is alive, and be genuinely wiped by Close — a plain
	// slice cleared only at the end left it in collectable memory for the
	// duration (found in review of go-qrdb#46).
	rootKey  *secure.Secret
	delegate SealingStrategy
}

// newRecoveryStrategyFn is indirected so a test can observe the temporary
// strategy unsealFromRootKey builds and assert its protected copy of the root
// key is destroyed; there is no other seam (go-qrdb#46).
var newRecoveryStrategyFn = newRecoveryStrategyFromBuffer

// newRecoveryStrategyFromBuffer builds a recovery strategy around a root key
// already in protected memory. It CONSUMES rootKey -- sealing it into the
// strategy's enclave destroys the buffer -- so the key never passes through an
// ordinary heap slice on the way in.
func newRecoveryStrategyFromBuffer(rootKey *secure.Buffer, delegate SealingStrategy) (*recoveryStrategy, error) {
	sec, err := secure.NewSecretFromBuffer(rootKey)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirSplitFailed, Op: "protect recovery key", Cause: err}
	}
	return &recoveryStrategy{rootKey: sec, delegate: delegate}, nil
}

func (r *recoveryStrategy) ID() StrategyID       { return StrategyID("recovery") }
func (r *recoveryStrategy) Available() bool      { return true }
func (r *recoveryStrategy) HardwareBacked() bool { return false }
func (r *recoveryStrategy) Close() error         { r.rootKey.Destroy(); return nil }

func (r *recoveryStrategy) SealRootKey(_ context.Context, _ []byte, _ Credentials) (*SealedRootKey, error) {
	return nil, ErrAlreadyInitialized
}

func (r *recoveryStrategy) UnsealRootKey(_ context.Context, _ *SealedRootKey, _ Credentials) ([]byte, error) {
	// Borrow the protected key only for the moment it takes to hand a copy to
	// Barrier.Unseal, which owns and wipes the returned slice. The enclave is
	// re-sealed the instant the buffer is destroyed.
	//
	// The copy runs inside a COORDINATED borrow (go-qrdb#74): the earlier
	// Size-then-Bytes pair took two separate borrows with a gap between, and a
	// concurrent destroy (a shutdown enclave.WipeAll) landing in that gap would
	// have handed Barrier.Unseal a correctly sized run of zeros. secure.CopyOut
	// holds one borrow across the copy and refuses an empty source, so a
	// destroyed enclave fails the unseal instead of silently unsealing with
	// nothing.
	//
	// Traced callees: CopyOut's callback is make + copy. Nothing here reaches a
	// SealingStrategy or the store, which is what makes THIS site convertible
	// while Recover's own rootKey read (recovery.go, above) is not.
	buf, err := r.rootKey.Open()
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "open recovery key", Cause: err}
	}
	defer buf.Destroy()
	key, err := secure.CopyOut(buf)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "read recovery key", Cause: err}
	}
	return key, nil
}

func (r *recoveryStrategy) BarrierEncryptor(ctx context.Context, rootKey []byte) (SymmetricEncrypter, error) {
	if r.delegate != nil {
		return r.delegate.BarrierEncryptor(ctx, rootKey)
	}
	return nil, nil
}

// DeleteRecoveryKeys removes the recovery-key domain. The recovery shares
// already distributed become useless: their key wraps nothing any more. The
// barrier must be unsealed.
func (b *StorageBarrier) DeleteRecoveryKeys(ctx context.Context) error {
	if b.IsSealed() {
		return ErrSealed
	}
	if err := b.base.Delete(ctx, b.wrappedKeyPath(DomainRecovery)); err != nil && !isSecretNotFound(err) {
		return err
	}
	return nil
}

// HasRecoveryKeys reports whether a recovery-key domain exists.
func (b *StorageBarrier) HasRecoveryKeys(ctx context.Context) (bool, error) {
	return b.base.Exists(ctx, b.wrappedKeyPath(DomainRecovery))
}

// itoa converts an int to a string without importing strconv.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	if i < 0 {
		return "-" + uitoa(uint(-i))
	}
	return uitoa(uint(i))
}

// uitoa converts an unsigned int to a string.
func uitoa(u uint) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}

// stampNoneOriginSealedState stamps the metadata MAC onto a sealed
// root-key record that legitimately carries none. quicraft's Initialize
// deliberately leaves a StrategyNone-sealed blob unstamped -- that strategy
// keeps no root key at rest to derive the MAC key from -- but every
// recovery-shaped unseal here runs the full MAC verification with the
// RECONSTRUCTED root key in hand. Stamping with that key upgrades the blob
// to authenticated metadata rather than refusing a state the none strategy
// itself defines as valid. Any other blob -- already stamped, or produced
// by a key-holding strategy -- is left untouched, so a stripped or
// tampered MAC on those still fails closed inside Unseal.
func (b *StorageBarrier) stampNoneOriginSealedState(rootKey []byte, sealed *SealedRootKey) error {
	if sealed == nil || len(sealed.StateMAC) != 0 || sealed.Strategy != StrategyNone {
		return nil
	}
	return quicraftseal.StampSealedStateMACWithRootKey(rootKey, b.config.DeploymentID, sealed)
}
