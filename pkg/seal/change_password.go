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
	"crypto/subtle"
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

// ChangePassword re-wraps the root key under a new passphrase. current is the
// passphrase the root key is wrapped under now and next the one it is wrapped
// under afterwards. The barrier must be unsealed and have a passphrase
// (software) strategy registered under StrategyPassphrase.
//
// current is checked before anything is written, against the persisted blob
// and through a credential-checking strategy, and a passphrase that does not
// open it is refused with ErrCredentialMismatch: an unsealed barrier is not
// proof that the caller knows the passphrase, so holding the session is not
// enough to replace it (#631).
//
// For hardware-backed strategies (TPM2, PKCS#11, cloud KMS), this operation
// is not applicable since they do not use password-derived keys.
//
// The write is guarded: the new blob replaces the persisted one only while
// the persisted one is still the blob current was checked against, so a
// rotation, rekey or second passphrase change that lands in between is not
// overwritten and the call fails with ErrPassphraseChangeConflict instead. A
// storage backend with no guarded swap is refused with
// ErrGuardedSwapUnsupported rather than written unguarded.
//
// The read of the live state, the guarded write and the live barrier's
// adoption of the new state are one operation. A second passphrase change,
// or a rotation, purge or backoff write through the barrier's state
// persister, attempted while it runs is refused with
// ErrRootKeyWriteInProgress and changes nothing: go-quicraft's scheduled
// rotation then rotates on its next tick.
//
// The blob carries the barrier's load generation forward unchanged. A
// passphrase change re-wraps the key this barrier already holds and is not a
// new load of it: writing a lower generation reads, on the next load, as a
// rollback of the blob ("load generation went BACKWARDS"), and a higher one
// as another loader having claimed it, which supersedes this barrier.
func (b *StorageBarrier) ChangePassword(ctx context.Context, current, next Credentials) error {
	if b.IsSealed() {
		return ErrSealed
	}

	// A nil secret is "no credentials supplied"; a destroyed one is a
	// credential the caller has already released. Neither is a passphrase.
	if !credentialPresent(current) || !credentialPresent(next) {
		return ErrInvalidPassphrase
	}

	// Verify that a passphrase (software) strategy is registered under
	// StrategyPassphrase.
	strategyKey := StrategyPassphrase
	oldStrat, ok := b.strategyByID(strategyKey)
	if !ok {
		return ErrStrategyNotFound
	}

	// Read the live state, write the re-wrapped blob and adopt it as one
	// operation: a second change, or a rotation or purge persisting a clone
	// of the live state, that landed between the write and the adoption
	// would leave disk and the live barrier on different wrappers, and the
	// next persist of the live state would restore the revoked passphrase.
	// Those writers refuse while this is held (see rootKeyRewrap).
	if !b.rootKeyRewrap.TryLock() {
		return ErrRootKeyWriteInProgress
	}
	defer b.rootKeyRewrap.Unlock()

	// Get the current root key from the unsealed barrier. RootKey() returns a
	// fresh heap copy the caller owns; it must be wiped before this function
	// returns (mirrors MintKeyedShamirSibling's defer secure.Wipe(rootKey)) —
	// otherwise a 32-byte plaintext root key, which derives every DEK, is left
	// resident in memory after every password change.
	rootKey, err := b.barrier.RootKey(ctx)
	if err != nil {
		return err
	}
	defer secure.Wipe(rootKey)

	// The persisted blob is both what current is checked against and the
	// guard of the write below.
	persistedBlob, err := b.persistedRootKeyBlob(ctx)
	if err != nil {
		return err
	}
	var persisted SealedRootKey
	if err := json.Unmarshal(persistedBlob, &persisted); err != nil {
		return ErrCorruptRootKey
	}
	if persisted.Strategy != strategyKey {
		return ErrStrategyMismatch
	}

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

	// Check current through the deferred strategy, never the registered one:
	// a bound strategy ignores the credentials it is handed, so asking it
	// would accept any passphrase at all. The blob must open to the very key
	// this barrier holds, compared in constant time.
	if err := openedToLiveKey(ctx, newStrat, &persisted, current, rootKey); err != nil {
		_ = newStrat.Close()
		emitAudit(b.config.AuditLogger, "change_password", "barrier",
			b.config.RootKeyPath, "deny", map[string]string{"error": err.Error()})
		return err
	}

	// Re-seal the root key with the new strategy. The deferred path reads
	// next.Secret at this call, so the blob is wrapped under the NEW
	// passphrase.
	sealed, err := newStrat.SealRootKey(ctx, rootKey, next)
	if err != nil {
		// Close the new strategy to release its enclave.
		_ = newStrat.Close()
		return err
	}

	// Carry forward the epoch state from the existing sealed root key.
	// SealRootKey only produces the ciphertext and strategy fields; the epoch
	// state is managed by the EpochBarrier and must be preserved across
	// re-wraps — dropping the HKDF version would re-derive DEKs under the
	// legacy v1 layout and render existing data undecryptable, dropping the
	// load generation reads as a rollback on the next load (see the doc
	// comment), and dropping the algorithm unrecords the data-path cipher.
	// The persisted unseal-backoff state is carried too: a passphrase change
	// is not a successful unseal and must not clear it.
	if existing != nil {
		sealed.Epoch = existing.Epoch
		sealed.Salt = existing.Salt
		sealed.KnownEpochs = existing.KnownEpochs
		sealed.Version = existing.Version
		sealed.HKDFVersion = existing.HKDFVersion
		sealed.LoadGeneration = existing.LoadGeneration
		sealed.Algorithm = existing.Algorithm
		sealed.FailedAttempts = existing.FailedAttempts
		sealed.LastFailedUnixNano = existing.LastFailedUnixNano
	}

	// Re-authenticate the metadata under the root key. SealRootKey produced
	// a fresh ciphertext and carries no MAC, and the epoch state above was
	// copied in from a different blob — so without this the persisted blob
	// fails authentication on the next Unseal. It must be the last mutation
	// before marshalling: the MAC covers the ciphertext as well as the
	// metadata carried forward.
	if err := b.barrier.StampSealedStateMAC(ctx, sealed); err != nil {
		_ = newStrat.Close()
		return err
	}

	// Persist the new sealed root key blob, guarded on the blob current was
	// checked against.
	blob, err := json.Marshal(sealed)
	if err != nil {
		_ = newStrat.Close()
		return err
	}
	guarded, ok := b.base.(guardedSwapBackend)
	if !ok {
		_ = newStrat.Close()
		return ErrGuardedSwapUnsupported
	}
	swapped, err := guarded.CompareAndSwap(ctx, b.config.RootKeyPath, persistedBlob, blob)
	if err != nil {
		_ = newStrat.Close()
		return err
	}
	if !swapped {
		_ = newStrat.Close()
		return ErrPassphraseChangeConflict
	}

	if hook := b.rootKeyWritten; hook != nil {
		hook()
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

// credentialPresent reports whether creds carries a live secret. A nil secret
// is "no credentials supplied"; a destroyed one is a credential the caller
// has already released.
func credentialPresent(creds Credentials) bool {
	return creds.Secret != nil && creds.Secret.IsAlive()
}

// openedToLiveKey unseals sealed with strategy and creds and reports whether
// it opened to live, the key the barrier holds, compared in constant time. A
// credential the strategy refuses and one that opens to a different key are
// the same answer, ErrCredentialMismatch: which of the two happened says
// nothing the caller may act on.
func openedToLiveKey(ctx context.Context, strategy SealingStrategy, sealed *SealedRootKey, creds Credentials, live []byte) error {
	candidate, err := strategy.UnsealRootKey(ctx, sealed, creds)
	if err != nil {
		return ErrCredentialMismatch
	}
	defer secure.Wipe(candidate)
	if len(candidate) == 0 || len(candidate) != len(live) ||
		subtle.ConstantTimeCompare(candidate, live) != 1 {
		return ErrCredentialMismatch
	}
	return nil
}
