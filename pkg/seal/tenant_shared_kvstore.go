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
	"bytes"
	"context"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// tenantIAMSharedPrefix namespaces a dedicated-isolation tenant's IAM
// records inside the shared replicated store. The "__tiam:" segment keeps
// the space disjoint from every IAM prefix the stores themselves write
// ("__sys:users/...", "__sys:roles/..."), so an un-namespaced shared-store
// read can never resolve another tenant's record even by accident.
const tenantIAMSharedPrefix = "__tiam:"

// Compile-time interface checks. Note which type carries which: the base
// wrapper is NOT an AtomicKVStore, and only the atomic variant is.
var (
	_ kvstore.KVStore       = (*tenantSharedKVStore)(nil)
	_ kvstore.AtomicKVStore = (*atomicTenantSharedKVStore)(nil)
)

// tenantSharedKVStore persists a dedicated-isolation tenant's IAM records
// in the SHARED replicated store, encrypted with the tenant's own DEK.
//
// Why not the tenant's dedicated barrier backend (tenantBarrierKVStore):
// that backend is a NODE-LOCAL store ({DataRoot}/barrier pebble). IAM must
// be readable on EVERY node of a cluster — the auth middleware resolves
// users and roles on whichever replica serves the request — and it must
// survive the loss of any single node. Riding the shared store gives IAM
// the system shard's Raft replication; encrypting every value with the
// tenant DEK preserves the cryptographic isolation the dedicated barrier
// provided: an operator reading the shared store sees ciphertext, a sealed
// tenant's records are unreadable (the DEK is unavailable), and no other
// tenant's DEK decrypts them.
//
// Keys are namespaced "__tiam:{tenantID}/{key}" so records are also
// logically disjoint per tenant. The seal state is read live through the
// TenantBarrier on every operation — a sealed barrier fails every
// Encrypt/Decrypt closed, and the store resolver additionally refuses to
// hand this store out at all while sealed.
//
// Index operations REFUSE with kvstore.ErrIndexUnsupported, exactly as on
// tenantBarrierKVStore: the shared store holds only ciphertext for this
// tenant, so nothing beneath this wrapper can read a field value to index.
// See RegisterEntityIndexes.
type tenantSharedKVStore struct {
	barrier *TenantBarrier
	shared  kvstore.KVStore
	prefix  string
}

// NewTenantSharedKVStore wraps the shared replicated store as a
// tenant-DEK-encrypting kvstore.KVStore for the given tenant's IAM. See
// tenantSharedKVStore for the placement/replication rationale.
// The returned store implements kvstore.AtomicKVStore ONLY when the shared
// store does. That distinction is the point of the two types: AtomicKVStore
// is optional and discovered by type assertion precisely so a caller can fail
// closed at CONSTRUCTION —
//
//	atomic, ok := store.(kvstore.AtomicKVStore)
//	if !ok { return ErrAtomicWriteUnsupported }
//
// — and a single type that always defines PutIfAbsent makes that assertion
// succeed unconditionally, so the caller learns the truth only at the first
// write. A guard that always passes is not a guard.
func NewTenantSharedKVStore(barrier *TenantBarrier, shared kvstore.KVStore) (kvstore.KVStore, error) {
	if barrier == nil {
		return nil, ErrNilTenantBarrier
	}
	if shared == nil {
		return nil, ErrNilStorageBackend
	}
	base := &tenantSharedKVStore{
		barrier: barrier,
		shared:  shared,
		prefix:  tenantIAMSharedPrefix + barrier.TenantID() + "/",
	}
	if atomicShared, ok := shared.(kvstore.AtomicKVStore); ok {
		return &atomicTenantSharedKVStore{
			tenantSharedKVStore: base,
			shared:              atomicShared,
		}, nil
	}
	return base, nil
}

// atomicTenantSharedKVStore is the tenant IAM wrapper over a shared store that
// CAN perform an atomic conditional write. It exists as a separate type so
// the kvstore.AtomicKVStore assertion answers honestly; everything else is
// inherited from the embedded base wrapper.
type atomicTenantSharedKVStore struct {
	*tenantSharedKVStore
	shared kvstore.AtomicKVStore
}

// PutIfAbsent encrypts value with the tenant DEK and stores it only when the
// key is absent. The atomicity is the shared store's, which this type only
// exists to have proved; the wrapper adds the tenant scoping and encryption.
//
// The check and the write must be one operation. The fallback that used to
// live here — Exists then Put — was the exact TOCTOU AtomicKVStore exists to
// eliminate, delivered silently: the assertion passed, the call returned
// stored=true, and two nodes racing the same create-if-absent could both win,
// redeeming a single-use token twice.
func (s *atomicTenantSharedKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	ciphertext, err := s.barrier.Barrier().Encrypt(value)
	if err != nil {
		return false, err
	}
	return s.shared.PutIfAbsent(ctx, s.prefix+key, ciphertext)
}

// CompareAndDelete removes the namespaced key only when the value stored
// there still decrypts to expected.
//
// It compares the CIPHERTEXT THIS CALL READ, not a fresh encryption of
// expected. Encryption here is randomised — go-quicraft's seal uses
// cipher.NewGCMWithRandomNonce — so encrypting the same plaintext twice
// produces different bytes and a ciphertext-of-expected guard would refuse
// every time. Reading the stored ciphertext, decrypting it to compare in
// plaintext, and then guarding the delete on those exact bytes preserves the
// guarantee exactly: any write landing in between replaces the stored
// ciphertext, so the guard still refuses. The atomicity remains the shared
// store's; this wrapper adds the tenant scoping and the encryption.
func (s *atomicTenantSharedKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	physical := s.prefix + key
	ciphertext, err := s.shared.Get(ctx, physical)
	if err != nil {
		return false, err
	}
	if ciphertext == nil {
		return false, nil
	}
	plaintext, derr := s.barrier.Barrier().Decrypt(ciphertext)
	if derr != nil {
		return false, derr
	}
	if !bytes.Equal(plaintext, expected) {
		return false, nil
	}
	return s.shared.CompareAndDelete(ctx, physical, ciphertext)
}

// Put encrypts value with the tenant DEK and stores it under the
// namespaced key in the shared store.
func (s *tenantSharedKVStore) Put(ctx context.Context, key string, value []byte) error {
	ciphertext, err := s.barrier.Barrier().Encrypt(value)
	if err != nil {
		return err
	}
	return s.shared.Put(ctx, s.prefix+key, ciphertext)
}

// The base wrapper deliberately has NO PutIfAbsent. See
// NewTenantSharedKVStore: defining one here would make the
// kvstore.AtomicKVStore assertion succeed even over a shared store that
// cannot perform an atomic write, which is the guard this whole split exists
// to keep honest.

// Get retrieves and decrypts the value for key, returning (nil, nil) when
// the key does not exist.
func (s *tenantSharedKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	ciphertext, err := s.shared.Get(ctx, s.prefix+key)
	if err != nil {
		return nil, err
	}
	if ciphertext == nil {
		return nil, nil
	}
	return s.barrier.Barrier().Decrypt(ciphertext)
}

// Delete removes the namespaced key from the shared store.
func (s *tenantSharedKVStore) Delete(ctx context.Context, key string) error {
	return s.shared.Delete(ctx, s.prefix+key)
}

// Scan iterates the tenant's records under prefix, decrypting each value
// and reporting keys with the namespace stripped.
func (s *tenantSharedKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return s.shared.Scan(ctx, s.prefix+prefix, func(key string, ciphertext []byte) error {
		plaintext, err := s.barrier.Barrier().Decrypt(ciphertext)
		if err != nil {
			return err
		}
		return fn(strings.TrimPrefix(key, s.prefix), plaintext)
	})
}

// List returns the tenant's keys under prefix, namespace stripped.
func (s *tenantSharedKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.shared.List(ctx, s.prefix+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = strings.TrimPrefix(key, s.prefix)
	}
	return out, nil
}

// Exists reports whether the namespaced key exists.
func (s *tenantSharedKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.shared.Exists(ctx, s.prefix+key)
}

// RegisterEntityIndexes refuses with kvstore.ErrIndexUnsupported.
//
// This store's whole purpose is that the shared replicated store holds only
// ciphertext (see the type comment). The engine underneath therefore cannot
// extract a JSON field from any value it holds for this tenant, so it cannot
// maintain an index over one. Building the index ABOVE the encryption — the
// only place the plaintext exists — would put the indexed field value into
// an index KEY in the shared store, in the clear, which is precisely the
// isolation this type is here to provide. That is a cryptographic design
// decision (deterministic/keyed-hash index terms, and what it costs range
// scans), not an implementation detail, and it is not made here.
//
// Refusing is not a regression: it is the honest form of what these methods
// already did. They returned (nil, nil), so every DAO index query over a
// dedicated tenant answered "no records" with no error. With the typed error
// pkg/dao detects the missing capability at construction and plans full
// scans instead, so those same queries now return CORRECT results.
func (s *tenantSharedKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return kvstore.ErrIndexUnsupported
}

// QueryIndex refuses; see RegisterEntityIndexes.
func (s *tenantSharedKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanIndex refuses; see RegisterEntityIndexes.
func (s *tenantSharedKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// QueryCompoundIndex refuses; see RegisterEntityIndexes.
func (s *tenantSharedKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanCompoundIndex refuses; see RegisterEntityIndexes.
func (s *tenantSharedKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
