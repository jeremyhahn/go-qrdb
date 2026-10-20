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
)

// ExportSealedBlob returns the raw sealed root-key blob persisted at
// RootKeyPath. The blob is a JSON-encoded SealedRootKey that was produced
// during Initialize (or a prior import) and is already encrypted by the
// tenant's own sealing strategy. It is safe to ship verbatim to follower
// replicas for self-managed import via ImportSealedBlob.
//
// The barrier does NOT need to be unsealed: the blob is at-rest ciphertext
// sealed by the tenant's strategy, so no plaintext key material is exposed by
// this call.
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
// exists at RootKeyPath, and ErrImportSealedBlobShamir, persisting nothing,
// for a Shamir blob: that key is bound to its node's own wrapped-key and
// holder-roster records, so a replica keys itself through ArmShamir.
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
	if sealed.Strategy == StrategyShamir {
		return ErrImportSealedBlobShamir
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
