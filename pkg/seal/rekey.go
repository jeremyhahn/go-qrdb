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

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Rekey generates a fresh unseal key, splits it into a fresh set of M-of-N
// Shamir shares (new threshold/total), seals them to the enrolled Security
// Officers via the CeremonyDistributor, and re-wraps the root key under the
// new unseal key (see unsealkey.go). No share bytes are persisted or
// returned.
//
// Persisting the re-wrapped blob CRYPTOGRAPHICALLY REVOKES every previously
// distributed share — including shares officers have already claimed: a
// prior-round quorum reconstructs an unseal key that no longer decrypts the
// wrapped root key (ErrUnsealKeyMismatch). The root key itself never
// rotates and no stored data is re-encrypted. Barriers initialized before
// unseal-key indirection are upgraded in place: their first Rekey writes
// the first wrapped blob, after which the legacy root-key shares are dead.
//
// The barrier MUST already be unsealed: the SOs re-convene and submit a quorum
// of their current shares via UnsealWithShare to unseal the barrier, after
// which the root key is held in memory. Rekey reads that in-memory root key
// (GetMasterKey), wraps it under the new unseal key, and updates the
// key-material-free sealed envelope. Future unseals require the new share set.
//
// ErrCeremonyDistributorNotConfigured is returned when no distributor has been
// wired via SetCeremonyDistributor.
func (b *StorageBarrier) Rekey(ctx context.Context, newThreshold, newTotal int) (*ShamirInitParams, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}

	shamirStrat := b.ShamirStrategy()
	if shamirStrat == nil {
		return nil, ErrShamirNotConfigured
	}
	if b.distributor == nil {
		return nil, ErrCeremonyDistributorNotConfigured
	}

	if newThreshold < 2 || newTotal < newThreshold {
		return nil, ErrShamirThresholdInvalid
	}

	// The barrier is unsealed, so the root key is available in memory.
	rootKey, err := b.GetMasterKey()
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(rootKey)

	// Create a new ShamirStrategy carrying the new parameters so the barrier's
	// ShamirStrategy() accessor reports the updated threshold/total.
	newStrat, err := NewShamirStrategy(newThreshold, newTotal)
	if err != nil {
		return nil, err
	}

	// Update the (key-material-free) sealed envelope with the new parameters.
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, err
	}
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return nil, ErrCorruptRootKey
	}
	sealed.ShamirThreshold = newThreshold
	sealed.ShamirTotal = newTotal
	updatedBlob, err := json.Marshal(&sealed)
	if err != nil {
		return nil, err
	}
	if err := b.base.Put(ctx, b.config.RootKeyPath, updatedBlob); err != nil {
		return nil, err
	}

	// Replace the strategy in the barrier's strategy map.
	b.strategies[StrategyShamir] = newStrat

	// Update config if present.
	if b.config.Shamir != nil {
		b.config.Shamir.Threshold = newThreshold
		b.config.Shamir.TotalShares = newTotal
	}

	// Generate the new unseal key and compute the re-wrapped root key blob
	// up front (the distributor wipes the unseal key on return).
	unsealKey, err := generateUnsealKey()
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(unsealKey)

	wrappedBlob, err := b.wrapRootKey(unsealKey, rootKey)
	if err != nil {
		return nil, err
	}

	// Ordering is deliberate: distribute the new shares FIRST, persist the
	// re-wrapped blob LAST. If distribution fails, the old wrapped blob (and
	// thus the old shares) remain valid and Rekey can simply be re-run.
	// Persisting first would risk lockout: old shares cryptographically dead
	// with the new shares never distributed.
	shareCount, err := b.distributor.RekeyAndDistribute(ctx, unsealKey, newThreshold, newTotal)
	if err != nil {
		return nil, err
	}

	// Persisting the re-wrapped root key revokes every previously
	// distributed share, claimed or not. Legacy barriers (no prior wrapped
	// blob) are upgraded to unseal-key indirection here.
	if err := b.base.Put(ctx, b.wrappedRootKeyPath(), wrappedBlob); err != nil {
		return nil, err
	}

	emitAudit(b.config.AuditLogger, "rekey", "barrier",
		b.config.RootKeyPath, "allow", map[string]string{
			"new_threshold": itoa(newThreshold),
			"new_total":     itoa(newTotal),
		})

	return &ShamirInitParams{
		Threshold:   newThreshold,
		TotalShares: newTotal,
		ShareCount:  shareCount,
	}, nil
}
