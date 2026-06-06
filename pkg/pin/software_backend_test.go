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
	"errors"
	"sync"
	"testing"
)

// memStore is an in-memory Storage implementation used to exercise the
// SoftwareBackend's persistence path without an external backend.
type memStore struct {
	mu sync.RWMutex
	kv map[string][]byte
}

func newMemStore() *memStore { return &memStore{kv: make(map[string][]byte)} }

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.kv[key]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (m *memStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[key] = value
	return nil
}

// faultyStore lets tests trigger storage failures on demand. Read/write
// errors are independent so tests can simulate "load works, persist fails"
// and vice versa.
type faultyStore struct {
	mu       sync.RWMutex
	kv       map[string][]byte
	getErr   error // returned by Get when set (overrides not-found)
	putErr   error // returned by Put when set
	getCalls int
	putCalls int
}

func newFaultyStore() *faultyStore { return &faultyStore{kv: make(map[string][]byte)} }

func (f *faultyStore) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	f.getCalls++
	err := f.getErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	v, ok := f.kv[key]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (f *faultyStore) Put(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls++
	if f.putErr != nil {
		return f.putErr
	}
	f.kv[key] = value
	return nil
}

// newTestBackend returns a SoftwareBackend with a fresh in-memory store
// and a fast Argon2id config so tests stay quick.
func newTestBackend(t *testing.T) (*SoftwareBackend, *memStore) {
	t.Helper()
	store := newMemStore()
	b, err := NewSoftwareBackend(store, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}
	return b, store
}

// fastHashConfig returns Argon2id parameters tuned for test speed (still
// produces a valid record but completes in microseconds rather than the
// production ~200ms).
func fastHashConfig() HashConfig {
	return HashConfig{
		Algorithm: HashArgon2id,
		Time:      1,
		Memory:    8 * 1024,
		Threads:   1,
		KeyLen:    32,
		SaltLen:   16,
	}
}

func TestSoftwareBackend_FreshState(t *testing.T) {
	b, _ := newTestBackend(t)
	if b.IsInitialized() {
		t.Fatal("fresh backend reports IsInitialized()=true")
	}
	if b.SOPINSet() || b.UserPINSet() {
		t.Fatal("fresh backend reports a PIN as set")
	}
	if b.GetLockoutStatus() != nil {
		t.Fatal("software backend must have no lockout state")
	}
	if err := b.ResetLockout("any"); err != nil {
		t.Fatalf("ResetLockout software: %v", err)
	}
}

func TestSoftwareBackend_SetSOPIN_FirstTimeAndChange(t *testing.T) {
	b, _ := newTestBackend(t)

	if err := b.SetSOPIN("", "so-secret-1"); err != nil {
		t.Fatalf("first SetSOPIN: %v", err)
	}
	if !b.SOPINSet() || !b.IsInitialized() {
		t.Fatal("expected SOPINSet/IsInitialized true after first set")
	}

	// Re-setting without a currentSOPIN must error.
	if err := b.SetSOPIN("", "so-secret-2"); !errors.Is(err, ErrPINAlreadySet) {
		t.Fatalf("re-set without current: want ErrPINAlreadySet, got %v", err)
	}

	// Re-setting with the wrong current must error.
	if err := b.SetSOPIN("wrong-pin", "so-secret-2"); !errors.Is(err, ErrInvalidCurrentPIN) {
		t.Fatalf("re-set with wrong current: want ErrInvalidCurrentPIN, got %v", err)
	}

	// Re-setting with the correct current must succeed (acts like Change).
	if err := b.SetSOPIN("so-secret-1", "so-secret-2"); err != nil {
		t.Fatalf("re-set with correct current: %v", err)
	}

	// ChangeSOPIN happy path on the new value.
	if err := b.ChangeSOPIN("so-secret-2", "so-secret-3"); err != nil {
		t.Fatalf("ChangeSOPIN: %v", err)
	}
	if err := b.VerifySOPIN("so-secret-3"); err != nil {
		t.Fatalf("VerifySOPIN after change: %v", err)
	}
	if err := b.VerifySOPIN("so-secret-1"); err == nil {
		t.Fatal("VerifySOPIN old PIN: want err, got nil")
	}
}

func TestSoftwareBackend_SetUserPIN_RequiresSOPIN(t *testing.T) {
	b, _ := newTestBackend(t)

	if err := b.SetUserPIN("anything", "user-pin"); !errors.Is(err, ErrSOPINRequired) {
		t.Fatalf("SetUserPIN before SO: want ErrSOPINRequired, got %v", err)
	}

	_ = b.SetSOPIN("", "so-pin-init")
	if err := b.SetUserPIN("wrong-so", "user-pin"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("SetUserPIN with wrong SO: want ErrPINInvalid, got %v", err)
	}
	if err := b.SetUserPIN("so-pin-init", "user-pin-1"); err != nil {
		t.Fatalf("SetUserPIN happy: %v", err)
	}
	if err := b.SetUserPIN("so-pin-init", "user-pin-2"); !errors.Is(err, ErrPINAlreadySet) {
		t.Fatalf("re-SetUserPIN: want ErrPINAlreadySet, got %v", err)
	}
}

func TestSoftwareBackend_VerifyAndChangeUserPIN(t *testing.T) {
	b, _ := newTestBackend(t)
	_ = b.SetSOPIN("", "so-pin-init")
	_ = b.SetUserPIN("so-pin-init", "user-pin-1")

	if err := b.VerifyUserPIN("user-pin-1"); err != nil {
		t.Fatalf("VerifyUserPIN happy: %v", err)
	}
	if err := b.VerifyUserPIN("wrong"); err == nil {
		t.Fatal("VerifyUserPIN wrong: want err, got nil")
	}

	if err := b.ChangeUserPIN("user-pin-1", "user-pin-2"); err != nil {
		t.Fatalf("ChangeUserPIN: %v", err)
	}
	if err := b.VerifyUserPIN("user-pin-2"); err != nil {
		t.Fatalf("VerifyUserPIN after change: %v", err)
	}
	if err := b.VerifyUserPIN("user-pin-1"); err == nil {
		t.Fatal("VerifyUserPIN old: want err, got nil")
	}
}

func TestSoftwareBackend_TooShortPIN(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.SetSOPIN("", "12345"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("short PIN: want ErrPINTooShort, got %v", err)
	}
}

func TestSoftwareBackend_PersistenceAcrossReinit(t *testing.T) {
	store := newMemStore()
	b1, err := NewSoftwareBackend(store, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}
	_ = b1.SetSOPIN("", "so-pin-init")
	_ = b1.SetUserPIN("so-pin-init", "user-pin-init")

	// Reconstruct against the same store; previous state must be visible.
	b2, err := NewSoftwareBackend(store, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend (reload): %v", err)
	}
	if !b2.SOPINSet() || !b2.UserPINSet() {
		t.Fatal("reloaded backend must report both PINs as set")
	}
	if err := b2.VerifySOPIN("so-pin-init"); err != nil {
		t.Fatalf("reloaded VerifySOPIN: %v", err)
	}
	if err := b2.VerifyUserPIN("user-pin-init"); err != nil {
		t.Fatalf("reloaded VerifyUserPIN: %v", err)
	}
}

func TestSoftwareBackend_FIDO2HashCache(t *testing.T) {
	b, _ := newTestBackend(t)
	_ = b.SetSOPIN("", "so-pin-init")
	_ = b.SetUserPIN("so-pin-init", "user-pin-init")

	expected := ComputeFIDO2PINHash("user-pin-init")
	if !b.VerifyFIDO2Hash(expected) {
		t.Fatal("VerifyFIDO2Hash with correct hash: want true, got false")
	}
	if b.VerifyFIDO2Hash(append([]byte("x"), expected[:FIDO2PINHashSize-1]...)) {
		t.Fatal("VerifyFIDO2Hash with mutated hash: want false, got true")
	}

	b.Close()
	if b.VerifyFIDO2Hash(expected) {
		t.Fatal("VerifyFIDO2Hash after Close: want false, got true")
	}
}

func TestSoftwareBackend_Strategy(t *testing.T) {
	b, _ := newTestBackend(t)
	if b.Strategy() != StrategySoftware {
		t.Fatalf("Strategy = %q, want %q", b.Strategy(), StrategySoftware)
	}
}

// =============================================================================
// Storageless construction
// =============================================================================

func TestNewSoftwareBackend_NoStore(t *testing.T) {
	b, err := NewSoftwareBackend(nil, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend(nil): %v", err)
	}
	if err := b.SetSOPIN("", "so-pin-1"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	if err := b.VerifySOPIN("so-pin-1"); err != nil {
		t.Fatalf("VerifySOPIN: %v", err)
	}
}

// =============================================================================
// Load failure paths
// =============================================================================

func TestNewSoftwareBackend_LoadFailure(t *testing.T) {
	store := newFaultyStore()
	store.getErr = errors.New("disk read failed")
	_, err := NewSoftwareBackend(store, fastHashConfig())
	if err == nil {
		t.Fatal("NewSoftwareBackend: want load error, got nil")
	}
	var loadErr *ErrStorageLoadFailed
	if !errors.As(err, &loadErr) {
		t.Fatalf("err = %v, want *ErrStorageLoadFailed", err)
	}
}

func TestNewSoftwareBackend_CorruptedRecord(t *testing.T) {
	store := newMemStore()
	// Pre-seed corrupted JSON under the SO PIN storage key.
	if err := store.Put(context.Background(), soPINStorageKey, []byte("not-json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err := NewSoftwareBackend(store, fastHashConfig())
	if err == nil {
		t.Fatal("NewSoftwareBackend: want decode error, got nil")
	}
	var loadErr *ErrStorageLoadFailed
	if !errors.As(err, &loadErr) {
		t.Fatalf("err = %v, want *ErrStorageLoadFailed", err)
	}
}

func TestNewSoftwareBackend_UserPINLoadFailureOnly(t *testing.T) {
	// SO PIN loads cleanly, user PIN load fails after a successful SO load
	// — exercises the second statement in loadPersistedRecords.
	store := newMemStore()
	if err := store.Put(context.Background(), userPINStorageKey, []byte("not-json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err := NewSoftwareBackend(store, fastHashConfig())
	if err == nil {
		t.Fatal("NewSoftwareBackend: want decode error, got nil")
	}
	var loadErr *ErrStorageLoadFailed
	if !errors.As(err, &loadErr) {
		t.Fatalf("err = %v, want *ErrStorageLoadFailed", err)
	}
	if loadErr.Key != userPINStorageKey {
		t.Fatalf("Key = %q, want %q", loadErr.Key, userPINStorageKey)
	}
}

// =============================================================================
// Persist failure paths
// =============================================================================

func TestSetSOPIN_PersistFailure(t *testing.T) {
	store := newFaultyStore()
	store.putErr = errors.New("disk write failed")
	b, err := NewSoftwareBackend(store, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}
	err = b.SetSOPIN("", "so-pin-1")
	if err == nil {
		t.Fatal("SetSOPIN: want persist error, got nil")
	}
	var persistErr *ErrStoragePersistFailed
	if !errors.As(err, &persistErr) {
		t.Fatalf("err = %v, want *ErrStoragePersistFailed", err)
	}
	if persistErr.Key != soPINStorageKey {
		t.Fatalf("Key = %q, want %q", persistErr.Key, soPINStorageKey)
	}
	if b.SOPINSet() {
		t.Fatal("SOPINSet must remain false when persist failed")
	}
}

func TestSetUserPIN_PersistFailure(t *testing.T) {
	store := newFaultyStore()
	b, err := NewSoftwareBackend(store, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}
	if err := b.SetSOPIN("", "so-pin-1"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	// Now trip the next Put on user-pin.
	store.mu.Lock()
	store.putErr = errors.New("disk write failed")
	store.mu.Unlock()

	err = b.SetUserPIN("so-pin-1", "user-pin-1")
	if err == nil {
		t.Fatal("SetUserPIN: want persist error, got nil")
	}
	var persistErr *ErrStoragePersistFailed
	if !errors.As(err, &persistErr) {
		t.Fatalf("err = %v, want *ErrStoragePersistFailed", err)
	}
	if b.UserPINSet() {
		t.Fatal("UserPINSet must remain false when persist failed")
	}
}

// =============================================================================
// ChangeSOPIN / ChangeUserPIN edge cases
// =============================================================================

func TestChangeSOPIN_TooShort(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.ChangeSOPIN("cur", "ab"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
}

func TestChangeSOPIN_NotSet(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.ChangeSOPIN("any", "new-so-pin"); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("err = %v, want ErrPINNotSet", err)
	}
}

func TestChangeSOPIN_WrongCurrent(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.SetSOPIN("", "so-pin-1"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	if err := b.ChangeSOPIN("wrong-pin", "new-so-pin"); !errors.Is(err, ErrInvalidCurrentPIN) {
		t.Fatalf("err = %v, want ErrInvalidCurrentPIN", err)
	}
}

func TestChangeUserPIN_TooShort(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.ChangeUserPIN("cur", "ab"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
}

func TestChangeUserPIN_NotSet(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.ChangeUserPIN("any", "new-user-pin"); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("err = %v, want ErrPINNotSet", err)
	}
}

func TestChangeUserPIN_WrongCurrent(t *testing.T) {
	b, _ := newTestBackend(t)
	_ = b.SetSOPIN("", "so-pin-1")
	_ = b.SetUserPIN("so-pin-1", "user-pin-1")
	if err := b.ChangeUserPIN("wrong-pin", "new-user-pin"); !errors.Is(err, ErrInvalidCurrentPIN) {
		t.Fatalf("err = %v, want ErrInvalidCurrentPIN", err)
	}
}

// =============================================================================
// Verify edge cases
// =============================================================================

func TestVerifySOPIN_NotSet(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.VerifySOPIN("any"); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("err = %v, want ErrPINNotSet", err)
	}
}

func TestVerifyUserPIN_NotSet(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.VerifyUserPIN("any"); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("err = %v, want ErrPINNotSet", err)
	}
}

func TestVerifyUserPIN_WrongValue(t *testing.T) {
	b, _ := newTestBackend(t)
	_ = b.SetSOPIN("", "so-pin-1")
	_ = b.SetUserPIN("so-pin-1", "user-pin-1")
	if err := b.VerifyUserPIN("wrong-pin"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("err = %v, want ErrPINInvalid", err)
	}
}

func TestVerifyUserPIN_RepopulatesFIDO2HashAfterReload(t *testing.T) {
	// First boot: set up and confirm cache.
	store := newMemStore()
	b1, err := NewSoftwareBackend(store, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}
	_ = b1.SetSOPIN("", "so-pin-1")
	_ = b1.SetUserPIN("so-pin-1", "user-pin-1")

	// Second boot: load from store. FIDO2 cache starts empty.
	b2, err := NewSoftwareBackend(store, fastHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend (reload): %v", err)
	}
	hashBefore := ComputeFIDO2PINHash("user-pin-1")
	if b2.VerifyFIDO2Hash(hashBefore) {
		t.Fatal("FIDO2 cache must be empty before any verify on reload")
	}

	// A successful VerifyUserPIN must repopulate the cache.
	if err := b2.VerifyUserPIN("user-pin-1"); err != nil {
		t.Fatalf("VerifyUserPIN: %v", err)
	}
	if !b2.VerifyFIDO2Hash(hashBefore) {
		t.Fatal("FIDO2 cache must be populated after successful verify")
	}
}

// =============================================================================
// VerifyFIDO2Hash edge cases
// =============================================================================

func TestVerifyFIDO2Hash_WrongLength(t *testing.T) {
	b, _ := newTestBackend(t)
	if b.VerifyFIDO2Hash(nil) {
		t.Fatal("nil hash must return false")
	}
	if b.VerifyFIDO2Hash([]byte{1, 2, 3}) {
		t.Fatal("short hash must return false")
	}
	if b.VerifyFIDO2Hash(make([]byte, FIDO2PINHashSize+1)) {
		t.Fatal("long hash must return false")
	}
}

func TestVerifyFIDO2Hash_NoCachedValue(t *testing.T) {
	b, _ := newTestBackend(t)
	if b.VerifyFIDO2Hash(make([]byte, FIDO2PINHashSize)) {
		t.Fatal("no cached hash must return false")
	}
}

// =============================================================================
// Close idempotency
// =============================================================================

func TestClose_Idempotent(t *testing.T) {
	b, _ := newTestBackend(t)
	_ = b.SetSOPIN("", "so-pin-1")
	_ = b.SetUserPIN("so-pin-1", "user-pin-1")
	b.Close()
	b.Close() // second Close must not panic.
	if b.VerifyFIDO2Hash(ComputeFIDO2PINHash("user-pin-1")) {
		t.Fatal("VerifyFIDO2Hash after Close: want false")
	}
}

func TestClose_OnFreshBackend(t *testing.T) {
	b, _ := newTestBackend(t)
	// No PIN ever set — Close must still be safe (no cached hash to scrub).
	b.Close()
}

// =============================================================================
// verifyRecord edge cases
// =============================================================================

func TestVerifyRecord_NilRecord(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.verifyRecord("any", nil); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("err = %v, want ErrPINNotSet", err)
	}
}

func TestVerifyRecord_UnsupportedAlgorithm(t *testing.T) {
	b, _ := newTestBackend(t)
	bogus := &PINRecord{Algorithm: "bcrypt", Hash: []byte{1}, Salt: []byte{2}}
	if err := b.verifyRecord("any", bogus); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("err = %v, want ErrPINInvalid (verify must collapse hash error)", err)
	}
}

// =============================================================================
// zeroBytes
// =============================================================================

func TestZeroBytes(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	zeroBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("b[%d] = %d, want 0", i, v)
		}
	}
}

func TestZeroBytes_Empty(t *testing.T) {
	zeroBytes(nil)
	zeroBytes([]byte{})
}

// =============================================================================
// FIDO2 hash cache rotation
// =============================================================================

func TestCacheFIDO2Hash_OverwritesPrevious(t *testing.T) {
	b, _ := newTestBackend(t)
	_ = b.SetSOPIN("", "so-pin-1")
	_ = b.SetUserPIN("so-pin-1", "user-pin-1")

	first := ComputeFIDO2PINHash("user-pin-1")
	if !b.VerifyFIDO2Hash(first) {
		t.Fatal("initial VerifyFIDO2Hash must succeed")
	}

	if err := b.ChangeUserPIN("user-pin-1", "user-pin-2"); err != nil {
		t.Fatalf("ChangeUserPIN: %v", err)
	}
	if b.VerifyFIDO2Hash(first) {
		t.Fatal("old hash must no longer verify after change")
	}
	if !b.VerifyFIDO2Hash(ComputeFIDO2PINHash("user-pin-2")) {
		t.Fatal("new hash must verify after change")
	}
}
