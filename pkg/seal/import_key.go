// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// ImportedKey holds the derivation context transported from a leader node to
// its replicas during multi-node tenant Raft group bootstrapping. Replicas
// must derive identical DEKs — DEK = HKDF(rootKey, salt, epoch) — so the
// leader ships this struct rather than each node generating its own root key.
//
// Salt is non-secret and may be logged; RootKey is SECURITY-CRITICAL and must
// be transmitted only over a mutually-authenticated, encrypted channel (mTLS)
// and wiped from memory after use.
type ImportedKey struct {
	// RootKey is the plaintext root key generated during Initialize on the
	// leader. It is the high-value secret that produces all DEKs via HKDF.
	RootKey []byte

	// Salt is the 32-byte random HKDF salt generated during Initialize. It is
	// NOT secret but must be identical on every replica so HKDF derives the
	// same DEKs as the leader.
	Salt []byte

	// Epoch is the current key rotation epoch on the leader. Replicas must use
	// the same epoch to derive the same current DEK.
	Epoch uint64

	// KnownEpochs lists every epoch whose DEK must be re-derived on unseal.
	// Replicas need the full set so they can decrypt data written at any epoch.
	KnownEpochs []uint64

	// HKDFVersion is the HKDF info-string layout the leader derives DEKs
	// with. Replicas must persist and derive with the same version or the
	// derived DEKs diverge (the zero value is interpreted downstream as
	// the legacy v1 layout, matching exports from older leaders).
	HKDFVersion HKDFVersion
}

// Validate reports the first structural error found in the ImportedKey.
// A valid ImportedKey has a non-empty RootKey, a 32-byte Salt, Epoch > 0,
// and a KnownEpochs slice that contains Epoch.
func (k *ImportedKey) Validate() error {
	if k == nil {
		return ErrImportedKeyNil
	}
	if len(k.RootKey) == 0 {
		return ErrImportedRootKeyEmpty
	}
	if len(k.Salt) != 32 {
		return ErrImportedSaltInvalid
	}
	if k.Epoch == 0 {
		return ErrImportedEpochZero
	}
	for _, ep := range k.KnownEpochs {
		if ep == k.Epoch {
			return nil
		}
	}
	return ErrImportedEpochNotInKnown
}

// ExportRootKeyContext returns the derivation context that a leader node ships
// to follower replicas so they can derive identical DEKs after importing the
// root key via ImportRootKey.
//
// The barrier must be unsealed; calling on a sealed barrier returns ErrSealed.
//
// The RootKey field in the returned ImportedKey is SECURITY-CRITICAL. The
// caller is responsible for transmitting it only over a mutually-authenticated,
// encrypted channel (mTLS) and wiping it from memory after use.
func (b *StorageBarrier) ExportRootKeyContext(ctx context.Context) (*ImportedKey, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}
	rootKey, err := b.GetMasterKey(ctx)
	if err != nil {
		return nil, err
	}
	sealed := b.barrier.SealedRootKeyData()
	if sealed == nil {
		secure.Wipe(rootKey)
		return nil, ErrCorruptRootKey
	}
	return &ImportedKey{
		RootKey:     rootKey,
		Salt:        sealed.Salt,
		Epoch:       sealed.Epoch,
		KnownEpochs: sealed.KnownEpochs,
		HKDFVersion: sealed.HKDFVersion,
	}, nil
}

// ImportRootKey initializes this barrier as a Raft replica by importing the
// root-key derivation context from the leader node. It re-seals the leader's
// root key under THIS node's sealing strategy (producing a node-local
// ciphertext) and then unseals the barrier, yielding identical DEKs to the
// leader.
//
// The recipe:
//  1. Validate imported.
//  2. If RootKeyPath already exists → ErrAlreadyInitialized (idempotent).
//  3. Select bestStrategy and call SealRootKey with a local copy of the root
//     key; wipe the copy immediately after sealing.
//  4. Populate the sealed key's Epoch/Salt/KnownEpochs from imported — these
//     shared values guarantee identical DEK derivation to the leader.
//  5. PutIfAbsent; if !stored → ErrAlreadyInitialized (CAS guard).
//  6. persistMetadata + emitAudit.
//  7. Unseal via the normal path to derive DEKs and transition to unsealed.
//
// After ImportRootKey returns nil, the barrier is in the unsealed state.
func (b *StorageBarrier) ImportRootKey(ctx context.Context, imported *ImportedKey, creds Credentials) error {
	if err := imported.Validate(); err != nil {
		return err
	}

	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	if exists {
		return ErrAlreadyInitialized
	}

	strategy, err := b.bestStrategy()
	if err != nil {
		emitAudit(b.config.AuditLogger, "import-root-key", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error()})
		return err
	}

	// Make a local copy so we can wipe it after sealing without touching
	// the caller's ImportedKey, which they may need to retain.
	rootKeyCopy := make([]byte, len(imported.RootKey))
	copy(rootKeyCopy, imported.RootKey)

	sealed, sealErr := strategy.SealRootKey(ctx, rootKeyCopy, creds)
	secure.Wipe(rootKeyCopy) // wipe the transient plaintext copy immediately
	if sealErr != nil {
		emitAudit(b.config.AuditLogger, "import-root-key", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": sealErr.Error(), "strategy": string(strategy.ID())})
		return sealErr
	}

	// Populate the shared derivation parameters from the leader context.
	// Salt/Epoch/KnownEpochs/HKDFVersion are what guarantee identical DEK
	// derivation (a zero HKDFVersion is interpreted as the legacy v1
	// layout on unseal, matching exports from older leaders).
	sealed.Version = 1
	sealed.Epoch = imported.Epoch
	saltCopy := make([]byte, len(imported.Salt))
	copy(saltCopy, imported.Salt)
	sealed.Salt = saltCopy
	epochs := make([]uint64, len(imported.KnownEpochs))
	copy(epochs, imported.KnownEpochs)
	sealed.KnownEpochs = epochs
	sealed.HKDFVersion = imported.HKDFVersion

	// Authenticate the metadata under the imported root key. SealRootKey
	// produces no MAC and every field set just above is covered by one, so an
	// unstamped blob would be refused by the Unseal at the end of this
	// function. The barrier is still sealed here — there is no unsealed
	// barrier to stamp with until this very blob has been persisted and
	// read back — so the stamp comes from the key in hand.
	if err := quicraftseal.StampSealedStateMACWithRootKey(
		imported.RootKey, b.config.DeploymentID, sealed); err != nil {
		emitAudit(b.config.AuditLogger, "import-root-key", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(strategy.ID())})
		return err
	}

	blob, err := json.Marshal(sealed)
	if err != nil {
		return err
	}

	// Atomic compare-and-set: authoritative guard against concurrent imports.
	stored, err := b.base.PutIfAbsent(ctx, b.config.RootKeyPath, blob)
	if err != nil {
		return err
	}
	if !stored {
		return ErrAlreadyInitialized
	}

	initAt := time.Now().UTC()
	b.initializedAt.Store(initAt)

	if err := b.persistMetadata(ctx, barrierMetadata{InitializedAt: initAt}); err != nil {
		emitAudit(b.config.AuditLogger, "import-root-key", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(strategy.ID())})
		return err
	}

	emitAudit(b.config.AuditLogger, "import-root-key", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"strategy": string(strategy.ID())})

	b.logger.Info("barrier root key imported",
		slog.String("strategy", string(strategy.ID())),
		slog.Uint64("epoch", imported.Epoch))

	// Unseal via the normal path: loads the stored blob, sets up the
	// EpochBarrier with the imported Salt/Epoch/KnownEpochs, and derives
	// all DEKs — giving this node identical DEKs to the leader.
	return b.Unseal(ctx, creds)
}

// ExportSealedBlob returns the raw sealed root-key blob persisted at
// RootKeyPath. The blob is a JSON-encoded SealedRootKey that was produced
// during Initialize (or a prior import) and is already encrypted by the
// tenant's own sealing strategy. It is safe to ship verbatim to follower
// replicas for self-managed import via ImportSealedBlob.
//
// Unlike ExportRootKeyContext, the barrier does NOT need to be unsealed:
// the blob is at-rest ciphertext sealed by the tenant's strategy, so no
// plaintext key material is exposed by this call.
//
// Returns ErrNotInitialized when no root-key blob has been persisted yet.
func (b *StorageBarrier) ExportSealedBlob(ctx context.Context) ([]byte, error) {
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, ErrNotInitialized
		}
		return nil, err
	}
	if blob == nil {
		return nil, ErrNotInitialized
	}
	return blob, nil
}

// ImportSealedBlob initializes this replica barrier with a portable sealed
// root key blob produced by the leader under the tenant's OWN sealing
// strategy (node-independent). The blob is persisted verbatim via PutIfAbsent;
// the barrier remains sealed. The tenant unseals this replica later using
// their own credential.
//
// ImportSealedBlob does NOT unseal the barrier. Callers must invoke Unseal
// separately once the tenant's credential is available.
//
// Returns ErrAlreadyInitialized (idempotent) if a root key blob already
// exists at RootKeyPath.
func (b *StorageBarrier) ImportSealedBlob(ctx context.Context, blob []byte) error {
	if blob == nil {
		return ErrImportSealedBlobNil
	}

	// Pre-validate structural soundness before writing to storage.
	// SetSealedRootKey performs the same checks on Unseal; validating here
	// surfaces corruption at import time rather than at the next boot.
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return ErrImportSealedBlobInvalid
	}
	if sealed.Epoch == 0 || len(sealed.Salt) != 32 {
		return ErrImportSealedBlobInvalid
	}
	epochFound := false
	for _, ep := range sealed.KnownEpochs {
		if ep == sealed.Epoch {
			epochFound = true
			break
		}
	}
	if !epochFound {
		return ErrImportSealedBlobInvalid
	}

	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	if exists {
		return ErrAlreadyInitialized
	}

	stored, err := b.base.PutIfAbsent(ctx, b.config.RootKeyPath, blob)
	if err != nil {
		return err
	}
	if !stored {
		return ErrAlreadyInitialized
	}

	initAt := time.Now().UTC()
	b.initializedAt.Store(initAt)

	if err := b.persistMetadata(ctx, barrierMetadata{InitializedAt: initAt}); err != nil {
		emitAudit(b.config.AuditLogger, "import-sealed-blob", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error()})
		return err
	}

	emitAudit(b.config.AuditLogger, "import-sealed-blob", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"strategy": string(sealed.Strategy)})

	b.logger.Info("barrier sealed blob imported (replica unseals later)",
		slog.String("strategy", string(sealed.Strategy)),
		slog.Uint64("epoch", sealed.Epoch))

	return nil
}
