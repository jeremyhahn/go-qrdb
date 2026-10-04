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

package pin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"sync/atomic"
)

// Storage keys for the SO and User PIN records.
const (
	soPINStorageKey   = "so-pin"
	userPINStorageKey = "user-pin"
)

// SoftwareBackend implements PINBackend with Argon2id / PBKDF2 password
// hashing and pluggable Storage persistence. Concurrent reads use atomics
// (no locks).
//
// The FIDO2 PIN hash (CTAP2: SHA-256(PIN)[:16]) is cached in-memory for the
// authenticator to verify against. Cleared on Close. Software backends do
// not enforce native lockout — the hash itself is the protection — so
// GetLockoutStatus returns nil and ResetLockout is a no-op.
//
// SoftwareBackend is the canonical fallback when no hardware-backed
// PINBackend is configured.
type SoftwareBackend struct {
	store      Storage
	hashConfig HashConfig
	soRecord   atomic.Pointer[PINRecord]
	userRecord atomic.Pointer[PINRecord]
	fido2Hash  atomic.Pointer[[]byte]
	soPINSet   atomic.Bool
	userPINSet atomic.Bool
}

// Compile-time interface checks.
var (
	_ PINBackend        = (*SoftwareBackend)(nil)
	_ FIDO2HashVerifier = (*SoftwareBackend)(nil)
)

// NewSoftwareBackend constructs a SoftwareBackend backed by store. When
// store is non-nil and contains existing PIN records under the canonical
// keys ("so-pin", "user-pin"), they are loaded immediately so the
// backend reports its prior state to subsequent SOPINSet / UserPINSet
// calls. Pass nil for in-memory-only operation (tests).
func NewSoftwareBackend(store Storage, hashConfig HashConfig) (*SoftwareBackend, error) {
	b := &SoftwareBackend{store: store, hashConfig: hashConfig}
	if store != nil {
		if err := b.loadPersistedRecords(); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// Strategy returns StrategySoftware.
func (b *SoftwareBackend) Strategy() StrategyID { return StrategySoftware }

// SetSOPIN sets the Security Officer PIN. First-time setup requires an
// empty currentSOPIN. Subsequent calls require the currently configured
// SO PIN; the call rejects with ErrPINAlreadySet when currentSOPIN is
// empty and the SO PIN is already configured.
func (b *SoftwareBackend) SetSOPIN(currentSOPIN, newSOPIN string) error {
	if err := validatePINLength(newSOPIN); err != nil {
		return err
	}
	if b.soPINSet.Load() {
		if currentSOPIN == "" {
			return ErrPINAlreadySet
		}
		if err := b.verifyRecord(currentSOPIN, b.soRecord.Load()); err != nil {
			return ErrInvalidCurrentPIN
		}
	}
	return b.hashAndStoreSOPIN(newSOPIN)
}

// SetUserPIN sets the user PIN. The SO PIN must already be configured
// and the supplied soPIN must match.
func (b *SoftwareBackend) SetUserPIN(soPIN, newUserPIN string) error {
	if err := validatePINLength(newUserPIN); err != nil {
		return err
	}
	if !b.soPINSet.Load() {
		return ErrSOPINRequired
	}
	if err := b.verifyRecord(soPIN, b.soRecord.Load()); err != nil {
		return ErrPINInvalid
	}
	if b.userPINSet.Load() {
		return ErrPINAlreadySet
	}
	if err := b.hashAndStoreUserPIN(newUserPIN); err != nil {
		return err
	}
	b.cacheFIDO2Hash([]byte(newUserPIN))
	return nil
}

// ChangeSOPIN changes the SO PIN. The current SO PIN must verify before
// the new one is persisted.
func (b *SoftwareBackend) ChangeSOPIN(currentSOPIN, newSOPIN string) error {
	if err := validatePINLength(newSOPIN); err != nil {
		return err
	}
	if !b.soPINSet.Load() {
		return ErrPINNotSet
	}
	if err := b.verifyRecord(currentSOPIN, b.soRecord.Load()); err != nil {
		return ErrInvalidCurrentPIN
	}
	return b.hashAndStoreSOPIN(newSOPIN)
}

// ChangeUserPIN changes the user PIN. The current user PIN must verify
// before the new one is persisted; the cached FIDO2 hash is refreshed.
func (b *SoftwareBackend) ChangeUserPIN(currentUserPIN, newUserPIN string) error {
	if err := validatePINLength(newUserPIN); err != nil {
		return err
	}
	if !b.userPINSet.Load() {
		return ErrPINNotSet
	}
	if err := b.verifyRecord(currentUserPIN, b.userRecord.Load()); err != nil {
		return ErrInvalidCurrentPIN
	}
	if err := b.hashAndStoreUserPIN(newUserPIN); err != nil {
		return err
	}
	b.cacheFIDO2Hash([]byte(newUserPIN))
	return nil
}

// VerifySOPIN re-derives the SO PIN hash and compares it to the stored
// record in constant time.
func (b *SoftwareBackend) VerifySOPIN(pin string) error {
	if !b.soPINSet.Load() {
		return ErrPINNotSet
	}
	return b.verifyRecord(pin, b.soRecord.Load())
}

// VerifyUserPIN re-derives the user PIN hash and compares it to the
// stored record in constant time. On the first successful verify after
// process restart, the FIDO2 hash cache is repopulated so the
// authenticator can answer CTAP2 PIN verifications without a separate
// PIN change.
func (b *SoftwareBackend) VerifyUserPIN(pin string) error {
	if !b.userPINSet.Load() {
		return ErrPINNotSet
	}
	if err := b.verifyRecord(pin, b.userRecord.Load()); err != nil {
		return err
	}
	if b.fido2Hash.Load() == nil {
		b.cacheFIDO2Hash([]byte(pin))
	}
	return nil
}

// IsInitialized reports whether the SO PIN has been set.
func (b *SoftwareBackend) IsInitialized() bool { return b.soPINSet.Load() }

// SOPINSet reports whether the SO PIN has been configured.
func (b *SoftwareBackend) SOPINSet() bool { return b.soPINSet.Load() }

// UserPINSet reports whether the user PIN has been configured.
func (b *SoftwareBackend) UserPINSet() bool { return b.userPINSet.Load() }

// GetLockoutStatus returns nil. The software backend relies on hashing
// strength rather than attempt counters; callers wanting lockout should
// inject a hardware-backed PINBackend (TPM2 / PKCS#11).
func (b *SoftwareBackend) GetLockoutStatus() *LockoutStatus { return nil }

// ResetLockout is a no-op for the software backend.
func (b *SoftwareBackend) ResetLockout(_ string) error { return nil }

// RollbackProvision implements ProvisionRollback: it un-sets the in-memory
// SO and User PIN state, so a subsequent Provision call within the SAME
// process behaves exactly as if neither had ever been set.
//
// The persisted record (when a Storage is configured) is NOT deleted --
// Storage has no delete primitive, only Get/Put -- so it is left stale on
// disk until the next successful SetSOPIN/SetUserPIN overwrites it. That
// is safe for the one thing this exists for (a same-process retry after an
// aborted multi-step provisioning transaction, go-qrdb#560 review B3):
// SOPINSet/UserPINSet answer from the in-memory flag this clears, never
// from storage presence, so the stale record is inert until this process
// writes over it. A crash between this call and that retry would reload
// the stale record on the NEXT process start and require the aborted PIN
// to unlock it -- an accepted residual gap, since Storage's interface
// offers no delete to close it.
func (b *SoftwareBackend) RollbackProvision() error {
	b.soRecord.Store(nil)
	b.userRecord.Store(nil)
	b.soPINSet.Store(false)
	b.userPINSet.Store(false)
	return nil
}

// VerifyFIDO2Hash verifies a CTAP2 PIN hash (SHA-256(PIN)[:16]) against
// the cached value in constant time. Returns false when no hash is
// cached (the user has not yet successfully verified post-restart).
func (b *SoftwareBackend) VerifyFIDO2Hash(hash []byte) bool {
	if len(hash) != FIDO2PINHashSize {
		return false
	}
	cached := b.fido2Hash.Load()
	if cached == nil || len(*cached) != FIDO2PINHashSize {
		return false
	}
	return subtle.ConstantTimeCompare(*cached, hash) == 1
}

// Close clears the cached FIDO2 hash and zeroes the underlying bytes.
// Safe to call multiple times.
func (b *SoftwareBackend) Close() {
	if cached := b.fido2Hash.Swap(nil); cached != nil {
		zeroBytes(*cached)
	}
}

// loadPersistedRecords loads both PIN records from storage at startup.
func (b *SoftwareBackend) loadPersistedRecords() error {
	if err := b.loadRecord(soPINStorageKey, &b.soRecord, &b.soPINSet); err != nil {
		return err
	}
	return b.loadRecord(userPINStorageKey, &b.userRecord, &b.userPINSet)
}

// loadRecord loads a single PIN record. A not-found result is treated as
// "fresh install" — flag stays unset, error returns nil.
func (b *SoftwareBackend) loadRecord(key string, ptr *atomic.Pointer[PINRecord], flag *atomic.Bool) error {
	data, err := b.store.Get(context.Background(), key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return &StorageLoadFailedError{Key: key, Cause: err}
	}
	var record PINRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return &StorageLoadFailedError{Key: key, Cause: err}
	}
	ptr.Store(&record)
	flag.Store(true)
	return nil
}

// verifyRecord re-derives pin under record's stored parameters and
// returns nil on a constant-time match. Returns ErrPINNotSet when
// record is nil; ErrPINInvalid otherwise.
func (b *SoftwareBackend) verifyRecord(pin string, record *PINRecord) error {
	if record == nil {
		return ErrPINNotSet
	}
	ok, err := verifyPINRecord(pin, record)
	if err != nil {
		return ErrPINInvalid
	}
	if !ok {
		return ErrPINInvalid
	}
	return nil
}

// hashAndStoreSOPIN derives the new SO PIN record, persists it (when a
// storage is configured), and updates the in-memory atomic state.
func (b *SoftwareBackend) hashAndStoreSOPIN(pin string) error {
	record, err := hashPINWithConfig(pin, b.hashConfig)
	if err != nil {
		return err
	}
	if b.store != nil {
		if err := b.persistRecord(soPINStorageKey, record); err != nil {
			return err
		}
	}
	b.soRecord.Store(record)
	b.soPINSet.Store(true)
	return nil
}

// hashAndStoreUserPIN derives the new user PIN record, persists it (when
// a storage is configured), and updates the in-memory atomic state.
func (b *SoftwareBackend) hashAndStoreUserPIN(pin string) error {
	record, err := hashPINWithConfig(pin, b.hashConfig)
	if err != nil {
		return err
	}
	if b.store != nil {
		if err := b.persistRecord(userPINStorageKey, record); err != nil {
			return err
		}
	}
	b.userRecord.Store(record)
	b.userPINSet.Store(true)
	return nil
}

// persistRecord marshals record to JSON and writes it under key.
func (b *SoftwareBackend) persistRecord(key string, record *PINRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return &StoragePersistFailedError{Key: key, Cause: err}
	}
	if err := b.store.Put(context.Background(), key, data); err != nil {
		return &StoragePersistFailedError{Key: key, Cause: err}
	}
	return nil
}

// cacheFIDO2Hash computes SHA-256(pinBytes)[:16] and stores it in the
// atomic pointer. The previous cache (if any) is zeroed.
func (b *SoftwareBackend) cacheFIDO2Hash(pinBytes []byte) {
	digest := sha256.Sum256(pinBytes)
	hash := make([]byte, FIDO2PINHashSize)
	copy(hash, digest[:FIDO2PINHashSize])
	zeroBytes(digest[:])
	if old := b.fido2Hash.Swap(&hash); old != nil {
		zeroBytes(*old)
	}
}

// zeroBytes wipes b in place. Used to scrub the FIDO2 hash from memory
// when it is rotated or the backend is closed.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
