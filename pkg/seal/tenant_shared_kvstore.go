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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// tenantIAMSharedPrefix namespaces a dedicated-isolation tenant's IAM
// records inside the shared replicated store. The "__tiam:" segment keeps
// the space disjoint from every IAM prefix the stores themselves write
// ("__sys:users/...", "__sys:roles/..."), so an un-namespaced shared-store
// read can never resolve another tenant's record even by accident.
const tenantIAMSharedPrefix = "__tiam:"

// Compile-time interface check.
var _ kvstore.KVStore = (*tenantSharedKVStore)(nil)

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
// Index operations are no-ops, exactly as on tenantBarrierKVStore: the IAM
// stores maintain their own manual secondary-index keys.
type tenantSharedKVStore struct {
	barrier *TenantBarrier
	shared  kvstore.KVStore
	prefix  string
}

// NewTenantSharedKVStore wraps the shared replicated store as a
// tenant-DEK-encrypting kvstore.KVStore for the given tenant's IAM. See
// tenantSharedKVStore for the placement/replication rationale.
func NewTenantSharedKVStore(barrier *TenantBarrier, shared kvstore.KVStore) (kvstore.KVStore, error) {
	if barrier == nil {
		return nil, ErrNilTenantBarrier
	}
	if shared == nil {
		return nil, ErrNilStorageBackend
	}
	return &tenantSharedKVStore{
		barrier: barrier,
		shared:  shared,
		prefix:  tenantIAMSharedPrefix + barrier.TenantID() + "/",
	}, nil
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

// PutIfAbsent encrypts and stores value only when the key is absent. The
// atomicity is the shared store's when it implements AtomicKVStore;
// otherwise it degrades to the same check-then-put every non-atomic
// KVStore provides.
func (s *tenantSharedKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	ciphertext, err := s.barrier.Barrier().Encrypt(value)
	if err != nil {
		return false, err
	}
	if atomicStore, ok := s.shared.(kvstore.AtomicKVStore); ok {
		return atomicStore.PutIfAbsent(ctx, s.prefix+key, ciphertext)
	}
	exists, err := s.shared.Exists(ctx, s.prefix+key)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	return true, s.shared.Put(ctx, s.prefix+key, ciphertext)
}

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

// RegisterEntityIndexes is a no-op; the IAM stores keep manual indexes.
func (s *tenantSharedKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

// QueryIndex is a no-op; the IAM stores keep manual indexes.
func (s *tenantSharedKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

// ScanIndex is a no-op; the IAM stores keep manual indexes.
func (s *tenantSharedKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// QueryCompoundIndex is a no-op; the IAM stores keep manual indexes.
func (s *tenantSharedKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

// ScanCompoundIndex is a no-op; the IAM stores keep manual indexes.
func (s *tenantSharedKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
