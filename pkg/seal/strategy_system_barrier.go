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
	"errors"
	"time"
)

// StrategySystemBarrier identifies the qrdb-application sealing strategy whose
// unseal authority is the SYSTEM barrier. A tenant root key sealed with this
// strategy is wrapped (AEAD) under the system barrier's master key and can be
// unwrapped credential-free whenever the system barrier is unsealed. The
// system barrier is always unsealed at boot, so this strategy enables
// auto-init at tenant create and credential-free re-attach at restart — the
// opt-in "system-managed auto-unseal" key-management mode.
//
// This realizes the locked Single KEK/DEK custody model: qrdb owns tenant key
// custody by wrapping each tenant DEK's root key under the one system KEK,
// rather than holding a separate per-tenant credential.
const StrategySystemBarrier StrategyID = "system-barrier"

// ErrSystemBarrierUnavailable is returned when a SystemBarrierStrategy operation
// is attempted but the backing system barrier is nil or sealed. The system
// barrier is the unseal authority for system-managed tenants; it must be
// unsealed before such a tenant barrier can be initialized or unsealed.
var ErrSystemBarrierUnavailable = errors.New("qrdb/seal: system barrier unavailable (nil or sealed)")

// SystemBarrierStrategy is a SealingStrategy that wraps and unwraps a tenant's
// root key using the system StorageBarrier's authenticated encryption. It
// invents no new cryptography: SealRootKey delegates to the system barrier's
// Encrypt (AEAD under the system master key) and UnsealRootKey delegates to its
// Decrypt. The system barrier owns nonce/epoch management and key rotation.
// A wrapped tenant root key stays under the epoch it was written in, and the
// system barrier pins that epoch so a WAL purge cannot retire its DEK, which
// is what lets the key survive restarts and rotations (see epoch_pins.go).
//
// The strategy holds no key material itself; it composes the already-unsealed
// system barrier as its KEK. It is constructed fresh on every tenant-barrier
// registration (create and boot re-attach) pointing at the live system barrier.
type SystemBarrierStrategy struct {
	system *StorageBarrier
}

// Compile-time interface check.
var _ SealingStrategy = (*SystemBarrierStrategy)(nil)

// NewSystemBarrierStrategy returns a SystemBarrierStrategy that wraps tenant
// root keys under the given system StorageBarrier. The system barrier must be
// the daemon's already-unsealed system barrier; a nil barrier yields a strategy
// that fails closed (Available reports false, Seal/Unseal return
// ErrSystemBarrierUnavailable).
func NewSystemBarrierStrategy(system *StorageBarrier) *SystemBarrierStrategy {
	return &SystemBarrierStrategy{system: system}
}

// ID returns StrategySystemBarrier.
func (s *SystemBarrierStrategy) ID() StrategyID { return StrategySystemBarrier }

// Available reports whether the strategy can seal/unseal right now: the system
// barrier must be present and unsealed. The barrier's bestStrategy consults
// this at Initialize time; a sealed system barrier defers tenant
// initialization rather than producing an unusable sealed blob.
func (s *SystemBarrierStrategy) Available() bool {
	return s.system != nil && !s.system.IsSealed()
}

// HardwareBacked returns false: the system barrier is software-managed (its own
// strategy may be hardware-backed, but this wrapping layer adds no hardware).
func (s *SystemBarrierStrategy) HardwareBacked() bool { return false }

// SealRootKey wraps the tenant root key under the system barrier's master key
// via AEAD and records the ciphertext in a SealedRootKey. Credentials are
// ignored: the system barrier is the sole unseal authority. The epoch/salt/
// known-epoch fields are populated by the EpochBarrier after this returns, so
// only Strategy and Ciphertext are set here (mirroring the software strategy).
func (s *SystemBarrierStrategy) SealRootKey(_ context.Context, rootKey []byte, _ Credentials) (*SealedRootKey, error) {
	if !s.Available() {
		return nil, ErrSystemBarrierUnavailable
	}
	ciphertext, err := s.system.Encrypt(rootKey)
	if err != nil {
		return nil, err
	}
	return &SealedRootKey{
		Strategy:   StrategySystemBarrier,
		Version:    1,
		Ciphertext: ciphertext,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// UnsealRootKey unwraps the tenant root key by decrypting the stored ciphertext
// with the system barrier's master key. Credentials are ignored. The returned
// root key is owned by the caller and must be zeroed after use.
func (s *SystemBarrierStrategy) UnsealRootKey(_ context.Context, sealed *SealedRootKey, _ Credentials) ([]byte, error) {
	if !s.Available() {
		return nil, ErrSystemBarrierUnavailable
	}
	if sealed == nil {
		return nil, ErrCorruptRootKey
	}
	if sealed.Strategy != StrategySystemBarrier {
		return nil, ErrStrategyMismatch
	}
	return s.system.Decrypt(sealed.Ciphertext)
}

// BarrierEncryptor returns (nil, nil) so the tenant barrier uses its own
// epoch-based HKDF DEKs derived from the unwrapped root key, exactly like the
// software strategy. The wrapping layer protects only the root key at rest.
func (s *SystemBarrierStrategy) BarrierEncryptor(_ context.Context, _ []byte) (SymmetricEncrypter, error) {
	return nil, nil
}

// Close is a no-op: the strategy holds no resources of its own (the system
// barrier's lifecycle is owned by the daemon, not by this strategy).
func (s *SystemBarrierStrategy) Close() error { return nil }
