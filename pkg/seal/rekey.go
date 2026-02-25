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

	"github.com/awnumar/memguard"
	"github.com/jeremyhahn/go-qrdb/pkg/shamir"
)

// Rekey generates a new set of Shamir shares for the existing root key.
// The old shares are deleted and replaced with new shares split using the
// provided threshold and total parameters. The sealed root key blob is
// updated with the new Shamir parameters.
//
// The barrier must be unsealed and have a ShamirStrategy registered.
func (b *Barrier) Rekey(ctx context.Context, newThreshold, newTotal int) (*ShamirInitResult, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}

	shamirStrat := b.ShamirStrategy()
	if shamirStrat == nil {
		return nil, ErrShamirNotConfigured
	}

	if newThreshold < 2 || newTotal < newThreshold {
		return nil, ErrShamirThresholdInvalid
	}

	// Load existing shares and reconstruct the root key.
	existingShares, err := shamirStrat.LoadAllShares()
	if err != nil {
		return nil, err
	}

	rootKey, err := shamir.Combine(existingShares)
	if err != nil {
		return nil, ErrShamirCombineFailed
	}
	defer memguard.WipeBytes(rootKey)

	// Delete old shares from storage using the package-level key function.
	for i := 1; i <= shamirStrat.total; i++ {
		_ = shamirStrat.Storage().Delete(shamirShareKey(i))
	}

	// Create a new ShamirStrategy with the same storage but new parameters.
	newStrat, err := NewShamirStrategy(shamirStrat.Storage(), newThreshold, newTotal)
	if err != nil {
		return nil, err
	}

	// Re-seal the root key with the new strategy.
	if _, err := newStrat.SealRootKey(ctx, rootKey, Credentials{}); err != nil {
		return nil, err
	}

	// Update the sealed root key blob with new Shamir parameters.
	blob, err := b.base.Get(b.config.RootKeyPath)
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
	if err := b.base.Put(b.config.RootKeyPath, updatedBlob); err != nil {
		return nil, err
	}

	// Replace the strategy in the barrier's strategy map.
	b.strategies[StrategyShamir] = newStrat

	// Update config if present.
	if b.config.Shamir != nil {
		b.config.Shamir.Threshold = newThreshold
		b.config.Shamir.TotalShares = newTotal
	}

	// Load the new shares and build the result.
	newShares, err := newStrat.LoadAllShares()
	if err != nil {
		return nil, err
	}

	result := &ShamirInitResult{
		Shares:      make([]string, len(newShares)),
		Threshold:   newThreshold,
		TotalShares: newTotal,
	}
	for i, share := range newShares {
		result.Shares[i] = share.Value
	}

	emitAudit(b.config.AuditLogger, "rekey", "barrier",
		b.config.RootKeyPath, "allow", map[string]string{
			"new_threshold": itoa(newThreshold),
			"new_total":     itoa(newTotal),
		})

	return result, nil
}
