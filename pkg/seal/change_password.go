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

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// KDFDescriber reports the KDF a strategy derives with. ChangePassword needs
// exactly this much of the strategy it replaces: the version and work factor
// to carry into the replacement, so a password change never silently resets a
// deployment's tuned factor (#156).
//
// It is an interface rather than a concrete type because a strategy that
// COMPOSES a software one -- the shape a consumer reaches for to add auditing
// or metrics around sealing -- still holds the very parameters the re-seal
// requires. Asserting *SoftwareStrategy refused those compositions even though
// they could answer perfectly well, which pushed consumers toward keeping a
// duplicate strategy of their own.
type KDFDescriber interface {
	// KDFVersion reports the KDF in use, so the replacement stays on the
	// same FIPS leg: a PBKDF2 strategy stays PBKDF2, an Argon2id one stays
	// Argon2id.
	KDFVersion() KDFVersion

	// KDFParams reports the work factor the sealed blob was written at. A
	// blob records the KDF version byte and never the params, so the live
	// strategy is the only place this number exists.
	KDFParams() KDFParams
}

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

	// A nil secret is "no credentials supplied"; a destroyed one is a
	// credential the caller has already released. Neither is a passphrase.
	if newCreds.Secret == nil || !newCreds.Secret.IsAlive() {
		return ErrInvalidPassphrase
	}

	// Verify that a passphrase (software) strategy is registered under
	// StrategyPassphrase.
	strategyKey := StrategyPassphrase
	oldStrat, ok := b.strategyByID(strategyKey)
	if !ok {
		return ErrStrategyNotFound
	}

	// Get the current root key from the unsealed barrier. RootKey() returns a
	// fresh heap copy the caller owns; it must be wiped before this function
	// returns (mirrors MintKeyedShamirSibling's defer secure.Wipe(rootKey)) —
	// otherwise a 32-byte plaintext root key, which derives every DEK, is left
	// resident in memory after every password change.
	rootKey, err := b.barrier.RootKey()
	if err != nil {
		return err
	}
	defer secure.Wipe(rootKey)

	// Capture the current epoch state before re-sealing so we can
	// carry it forward into the new sealed blob.
	existing := b.barrier.SealedRootKeyData()

	// Create the replacement strategy as a DEFERRED one, which binds its
	// passphrase at each Seal/Unseal from Credentials.Secret rather than at
	// construction.
	//
	// This is a security fix, not a style choice (go-qrdb#157). A BOUND
	// strategy — what NewSoftwareStrategy returns — ignores the credentials
	// handed to UnsealRootKey entirely; go-quicraft documents it outright:
	// "When deferPassphrase is false (default), the ctx and creds parameters
	// are unused; the passphrase is provided at construction time."
	//
	// Installing a bound strategy here therefore stopped the barrier checking
	// credentials at all from the first password change onward — measured:
	// afterwards a WRONG passphrase, the OLD passphrase and the new one all
	// unsealed successfully. pkg/database/barrier_manager.go registers
	// deferred strategies precisely to avoid that, and its comment says a
	// bound one "would make wrong-secret unseal attempts succeed for the
	// lifetime of the process". This function was reintroducing exactly that.
	//
	// Always-deferred rather than preserving whichever kind was registered:
	// the binding mode is not observable (deferPassphrase is unexported with
	// no accessor — go-quicraft#43 asks for one), and a caller relying on
	// credential-free unseal after a credential change is relying on the
	// bypass. After a password change the password must be checked.
	//
	// The KDF version and work factor ARE preserved, and this is correctness
	// rather than hardening (#156, go-quicraft#72). A sealed root-key blob
	// records the KDF version byte and never the params, so a blob sealed at
	// a non-default factor can only be unsealed by a strategy carrying that
	// same factor — the live strategy object is the only place the number
	// exists. Building the replacement with the no-argument constructor
	// silently reset the factor to the library defaults on every password
	// change: the blob it wrote could then be opened only by a default-factor
	// strategy, which a restart constructing the deployment's tuned one from
	// configuration is not, and for a deployment tuned ABOVE the defaults the
	// reset also weakened the credential that wraps the root key. The version
	// is carried with the params so the FIPS leg
	// cannot be got wrong: a PBKDF2 strategy stays PBKDF2 and an Argon2id one
	// stays Argon2id, and the constructor refuses a version the build or the
	// runtime FIPS policy does not allow, exactly as the bound ones do.
	software, ok := oldStrat.(KDFDescriber)
	if !ok {
		return ErrPassphraseStrategyNotSoftware
	}
	newStrat, err := NewDeferredSoftwareStrategyWithParams(software.KDFVersion(), software.KDFParams())
	if err != nil {
		return err
	}

	// Re-seal the root key with the new strategy. The deferred path reads
	// newCreds.Secret at this call, so the blob is wrapped under the NEW
	// passphrase.
	sealed, err := newStrat.SealRootKey(ctx, rootKey, newCreds)
	if err != nil {
		// Close the new strategy to release its enclave.
		_ = newStrat.Close()
		return err
	}

	// Carry forward the epoch, salt, known epochs, and HKDF version
	// from the existing sealed root key. SealRootKey only produces the
	// ciphertext and strategy fields; the epoch state is managed by the
	// EpochBarrier and must be preserved across re-wraps — dropping the
	// HKDF version would re-derive DEKs under the legacy v1 layout and
	// render existing data undecryptable.
	if existing != nil {
		sealed.Epoch = existing.Epoch
		sealed.Salt = existing.Salt
		sealed.KnownEpochs = existing.KnownEpochs
		sealed.Version = existing.Version
		sealed.HKDFVersion = existing.HKDFVersion
	}

	// Re-authenticate the metadata under the root key. SealRootKey produced
	// a fresh ciphertext and carries no MAC, and the epoch state above was
	// copied in from a different blob — so without this the persisted blob
	// fails authentication on the next Unseal. It must be the last mutation
	// before marshalling: the MAC covers the ciphertext as well as the
	// metadata carried forward.
	if err := b.barrier.StampSealedStateMAC(sealed); err != nil {
		_ = newStrat.Close()
		return err
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
	b.setStrategy(strategyKey, newStrat)

	emitAudit(b.config.AuditLogger, "change_password", "barrier",
		b.config.RootKeyPath, "allow", nil)

	b.logger.Info("barrier password changed",
		slog.String("strategy", string(strategyKey)))

	return nil
}
