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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// Compile-time interface check.
var (
	_ kvstore.KVStore                  = (*tenantBarrierKVStore)(nil)
	_ kvstore.MultiGuardedBatchKVStore = (*tenantBarrierKVStore)(nil)
)

// tenantBarrierKVStore adapts a *TenantBarrier to the kvstore.KVStore
// interface so the IAM stores (user, role) can persist their records
// THROUGH a barrier-isolated tenant's own cryptographic barrier. Every
// value written via Put is encrypted with the tenant's DEK and stored
// under the tenant's "{tenantID}/" namespace; every value read via Get is
// decrypted with that same DEK. A barrier-isolated tenant's IAM data is
// therefore both physically namespaced and cryptographically isolated
// from the shared system store and from every other tenant.
//
// Index operations REFUSE with kvstore.ErrIndexUnsupported. The user and
// role stores maintain their own MANUAL secondary-index keys
// (username/cert-thumbprint, name) via Put/Get/Delete/Scan and never call
// the kvstore index API, and the barrier stores ciphertext, so nothing
// beneath this wrapper can read a field value to index. See
// RegisterEntityIndexes.
type tenantBarrierKVStore struct {
	barrier *TenantBarrier
}

// NewTenantBarrierKVStore wraps a *TenantBarrier as a kvstore.KVStore. It
// returns ErrNilTenantBarrier when barrier is nil. The seal state is read
// LIVE on every operation by the underlying TenantBarrier, so the wrapper
// itself carries no cached sealed/unsealed decision: a Put/Get against a
// sealed tenant barrier surfaces ErrSealed.
func NewTenantBarrierKVStore(barrier *TenantBarrier) (kvstore.KVStore, error) {
	if barrier == nil {
		return nil, ErrNilTenantBarrier
	}
	return &tenantBarrierKVStore{barrier: barrier}, nil
}

// Put encrypts value with the tenant DEK and stores it under the
// namespaced key. Returns ErrSealed when the tenant barrier is sealed.
func (s *tenantBarrierKVStore) Put(ctx context.Context, key string, value []byte) error {
	return s.barrier.Put(ctx, key, value)
}

// PutIfAbsent encrypts and stores value under key only when the key is absent,
// reporting whether this call created it. The atomicity is the tenant
// barrier's; this type only adds the tenant scoping.
func (s *tenantBarrierKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return s.barrier.PutIfAbsent(ctx, key, value)
}

// CompareAndDelete removes key only when its stored value still equals
// expected, delegating to the tenant barrier's guarded delete.
func (s *tenantBarrierKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return s.barrier.CompareAndDelete(ctx, key, expected)
}

// CompareAndSwap replaces the key's value only when it still equals expected.
func (s *tenantBarrierKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return s.barrier.CompareAndSwap(ctx, key, expected, next)
}

// PutAllIfUnchanged applies every write atomically, and only while guardKey
// still holds guardValue: PutAllIfAllUnchanged with a single guard.
func (s *tenantBarrierKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return s.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged encrypts every write with the tenant DEK and applies
// them atomically, only while every guard's key still holds its expected
// value (nil: absent). A tenant's home store on a standalone node is this
// type under a namespace, and the tenant CA it holds is written with this
// verb. An empty guard or write key is refused with *kvstore.EmptyKeyError
// before anything is read, as every MultiGuardedBatchKVStore refuses it.
func (s *tenantBarrierKVStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	for i, g := range guards {
		if g.Key == "" {
			return false, &kvstore.EmptyKeyError{Field: "guard", Index: i}
		}
	}
	for i, w := range writes {
		if w.Key == "" {
			return false, &kvstore.EmptyKeyError{Field: "write", Index: i}
		}
	}
	return s.barrier.PutAllIfAllUnchanged(ctx, guards, writes)
}

// Get decrypts and returns the value for key. It translates the seal
// subsystem's ErrSecretNotFound into (nil, nil) to satisfy the
// kvstore.KVStore contract ("returns nil, nil when key does not exist").
// All other errors — including ErrSealed for a sealed tenant — propagate
// unchanged so callers fail closed rather than treating a sealed barrier
// as an empty store.
func (s *tenantBarrierKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.barrier.Get(ctx, key)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// Delete removes the namespaced key from the tenant's storage.
func (s *tenantBarrierKVStore) Delete(ctx context.Context, key string) error {
	return s.barrier.Delete(ctx, key)
}

// Scan iterates over key-value pairs matching prefix, decrypting each
// value with the tenant DEK, invoking fn for every entry.
func (s *tenantBarrierKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return s.barrier.Scan(ctx, prefix, fn)
}

// List returns the namespaced keys matching prefix (keys are not
// encrypted; only values are).
func (s *tenantBarrierKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.barrier.List(ctx, prefix)
}

// Exists reports whether the namespaced key exists. It translates
// ErrSecretNotFound into (false, nil); all other errors (including
// ErrSealed) propagate so callers fail closed.
func (s *tenantBarrierKVStore) Exists(ctx context.Context, key string) (bool, error) {
	ok, err := s.barrier.Exists(ctx, key)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}

// RegisterEntityIndexes refuses with kvstore.ErrIndexUnsupported: the
// TenantBarrier stores ciphertext, so nothing beneath this wrapper can read
// a field value to index it. Building the index ABOVE the encryption -- the
// only place the plaintext exists -- would put the indexed field value into
// an index KEY in the clear, which is the isolation the barrier is there to
// provide. That is a cryptographic design decision (deterministic or keyed-
// hash index terms, and what they cost range scans), not an implementation
// detail, and it is not made here.
//
// The methods previously returned (nil, nil), which made every DAO index
// query over this store answer "no records" with no error. The typed error
// lets pkg/dao detect the gap at construction and plan full scans, so the
// same queries now return correct results.
func (s *tenantBarrierKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return kvstore.ErrIndexUnsupported
}

// QueryIndex refuses; see RegisterEntityIndexes.
func (s *tenantBarrierKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanIndex refuses; see RegisterEntityIndexes.
func (s *tenantBarrierKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// QueryCompoundIndex refuses; see RegisterEntityIndexes.
func (s *tenantBarrierKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanCompoundIndex refuses; see RegisterEntityIndexes.
func (s *tenantBarrierKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
