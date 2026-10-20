// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package seal

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// keyFingerprintLabel is the HKDF info a barrier's key fingerprint is derived
// under. It is fixed: nothing else is derived under it, and changing it
// changes every fingerprint.
const keyFingerprintLabel = "qrdb/seal/key-fingerprint/v1"

// keyFingerprintLen is the fingerprint's length in bytes.
const keyFingerprintLen = 16

// KeyFingerprint returns the hex HKDF-SHA-256 of this barrier's root key under
// a fixed purpose label: a public name for the key that does not reveal it.
// Two barriers hold the same root key exactly when their fingerprints match,
// which is how an operator sees that each node serving a tenant's barrier
// holds its own key. It is empty, with no error, for a barrier in
// no-encryption mode, which holds no lasting root key. A sealed barrier
// returns *KeyFingerprintError wrapping ErrSealed.
func (b *StorageBarrier) KeyFingerprint(ctx context.Context) (string, error) {
	if active, _ := b.activeStrategy.Load().(StrategyID); active == StrategyNone {
		return "", nil
	}
	root, err := b.GetMasterKey(ctx)
	if err != nil {
		return "", &KeyFingerprintError{Err: err}
	}
	defer secure.Wipe(root)
	fingerprint, err := hkdf.Key(sha256.New, root, nil, keyFingerprintLabel, keyFingerprintLen)
	if err != nil {
		return "", &KeyFingerprintError{Err: err}
	}
	return hex.EncodeToString(fingerprint), nil
}

// KeyFingerprintError is returned when a barrier's key fingerprint could not
// be made: the barrier is sealed, or its key could not be read or derived from.
type KeyFingerprintError struct {
	Err error
}

// Error returns a human-readable description.
func (e *KeyFingerprintError) Error() string {
	return "seal: cannot fingerprint the barrier key: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KeyFingerprintError) Unwrap() error { return e.Err }
