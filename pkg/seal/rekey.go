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
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Rekey re-splits the existing root key into a fresh set of M-of-N Shamir
// shares (new threshold/total), returning them to the Security Officers. No
// shares are persisted, and none are read from storage.
//
// The barrier MUST already be unsealed: the SOs re-convene and submit a quorum
// of their *current* shares via UnsealWithShare to unseal the barrier, after
// which the root key is held in memory. Rekey reads that in-memory root key
// (GetMasterKey), splits it under the new parameters, and updates the
// key-material-free sealed envelope. The new shares are distributed to the SOs;
// once the envelope's threshold/total are updated, future unseals require the
// new share set.
func (b *StorageBarrier) Rekey(ctx context.Context, newThreshold, newTotal int) (*ShamirInitResult, error) {
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

	// The barrier is unsealed, so the root key is available in memory. Read it
	// and re-split it; nothing is loaded from or written to storage.
	rootKey, err := b.GetMasterKey()
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(rootKey)

	newShareVals, err := NewShamirShareSplitter().Split(rootKey, newThreshold, newTotal)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShamirSplitFailed, err)
	}

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

	result := &ShamirInitResult{
		Shares:      newShareVals,
		Threshold:   newThreshold,
		TotalShares: newTotal,
	}

	emitAudit(b.config.AuditLogger, "rekey", "barrier",
		b.config.RootKeyPath, "allow", map[string]string{
			"new_threshold": itoa(newThreshold),
			"new_total":     itoa(newTotal),
		})

	return result, nil
}
