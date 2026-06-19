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
	"log/slog"
)

// ChangePassword re-wraps the root key with a new password-derived wrapping
// key. The barrier must be unsealed and have a passphrase (software) strategy
// registered under StrategyPassphrase.
//
// For hardware-backed strategies (TPM2, PKCS#11, cloud KMS), this operation
// is not applicable since they do not use password-derived keys.
//
// The operation is atomic with respect to the persisted sealed root key blob:
// the new blob is written only after the re-seal succeeds.
func (b *StorageBarrier) ChangePassword(ctx context.Context, newCreds Credentials) error {
	if b.IsSealed() {
		return ErrSealed
	}

	if len(newCreds.Secret) == 0 {
		return ErrInvalidPassphrase
	}

	// Verify that a passphrase (software) strategy is registered under
	// StrategyPassphrase.
	strategyKey := StrategyPassphrase
	oldStrat, ok := b.strategies[strategyKey]
	if !ok {
		return ErrStrategyNotFound
	}

	// Get the current root key from the unsealed barrier.
	rootKey, err := b.barrier.RootKey()
	if err != nil {
		return err
	}

	// Capture the current epoch state before re-sealing so we can
	// carry it forward into the new sealed blob.
	existing := b.barrier.SealedRootKeyData()

	// Create a new SoftwareStrategy with the new password.
	newStrat, err := NewSoftwareStrategy([]byte(newCreds.Secret))
	if err != nil {
		return err
	}

	// Re-seal the root key with the new strategy.
	sealed, err := newStrat.SealRootKey(ctx, rootKey, newCreds)
	if err != nil {
		// Close the new strategy to release its enclave.
		_ = newStrat.Close()
		return err
	}

	// Carry forward the epoch, salt, and known epochs from the
	// existing sealed root key. SealRootKey only produces the
	// ciphertext and strategy fields; the epoch state is managed
	// by the EpochBarrier and must be preserved across re-wraps.
	if existing != nil {
		sealed.Epoch = existing.Epoch
		sealed.Salt = existing.Salt
		sealed.KnownEpochs = existing.KnownEpochs
		sealed.Version = existing.Version
	}

	// Persist the new sealed root key blob.
	blob, err := json.Marshal(sealed)
	if err != nil {
		_ = newStrat.Close()
		return err
	}
	if err := b.base.Put(ctx, b.config.RootKeyPath, blob); err != nil {
		_ = newStrat.Close()
		return err
	}

	// Update the EpochBarrier's sealed state so SealedRootKeyData() is consistent.
	if err := b.barrier.SetSealedRootKey(sealed); err != nil {
		_ = newStrat.Close()
		return err
	}

	// Close the old strategy to release its enclave, then swap under the same
	// registry key it was found under.
	_ = oldStrat.Close()
	b.strategies[strategyKey] = newStrat

	emitAudit(b.config.AuditLogger, "change_password", "barrier",
		b.config.RootKeyPath, "allow", nil)

	b.logger.Info("barrier password changed",
		slog.String("strategy", string(strategyKey)))

	return nil
}
