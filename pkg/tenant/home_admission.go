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

package tenant

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// HomeAdmission admits operations on the home stores LocalHomes builds. A
// standalone home store is the tenant barrier's own backend, with no Raft
// group to stop, so the composer that re-encrypts a tenant's barrier fences
// and drains the tenant's home-store operations through this seam for the
// whole migration.
type HomeAdmission interface {
	// AdmitHome admits one operation on tenantID's home store. It returns
	// the release the operation calls exactly once when it is done, or the
	// error refusing the operation, which the operation returns unchanged.
	AdmitHome(tenantID string) (release func(), err error)
}

// LocalHomesOption configures LocalHomes.
type LocalHomesOption func(*LocalHomes)

// WithHomeAdmission makes every operation on a home store LocalHomes builds
// pass admission first. Without it every operation is admitted.
func WithHomeAdmission(admission HomeAdmission) LocalHomesOption {
	return func(h *LocalHomes) { h.admission = admission }
}

// barrierHomeKVStore is the method set of the tenant barrier's KV store
// that a home store keeps: the namespaced store above it serves guarded and
// atomic writes only when the store under it does.
type barrierHomeKVStore interface {
	kvstore.AtomicKVStore
	kvstore.MultiGuardedBatchKVStore
}

// admittedHomeStore is a home store whose every data operation holds
// admission for its whole duration, a Scan's callbacks included, and runs
// against the barrier the registry holds for the tenant when it is admitted:
// a store obtained before a migration and used after it writes under the
// keyed barrier the migration installed, never the one it replaced.
type admittedHomeStore struct {
	homes    *LocalHomes
	tenantID string
}

// Compile-time interface check.
var _ barrierHomeKVStore = (*admittedHomeStore)(nil)

// admitted runs op under one admission of s's tenant, against the tenant's
// current barrier store.
func admitted[T any](s *admittedHomeStore, op func(barrierHomeKVStore) (T, error)) (T, error) {
	var zero T
	release, err := s.homes.admission.AdmitHome(s.tenantID)
	if err != nil {
		return zero, err
	}
	defer release()
	entry, err := s.homes.entry(s.tenantID)
	if err != nil {
		return zero, err
	}
	return op(entry.guarded)
}

// admittedErr runs op, which returns only an error, under one admission.
func admittedErr(s *admittedHomeStore, op func(barrierHomeKVStore) error) error {
	_, err := admitted(s, func(inner barrierHomeKVStore) (struct{}, error) { return struct{}{}, op(inner) })
	return err
}

// Put implements kvstore.KVStore.
func (s *admittedHomeStore) Put(ctx context.Context, key string, value []byte) error {
	return admittedErr(s, func(inner barrierHomeKVStore) error { return inner.Put(ctx, key, value) })
}

// Get implements kvstore.KVStore.
func (s *admittedHomeStore) Get(ctx context.Context, key string) ([]byte, error) {
	return admitted(s, func(inner barrierHomeKVStore) ([]byte, error) { return inner.Get(ctx, key) })
}

// Delete implements kvstore.KVStore.
func (s *admittedHomeStore) Delete(ctx context.Context, key string) error {
	return admittedErr(s, func(inner barrierHomeKVStore) error { return inner.Delete(ctx, key) })
}

// Scan implements kvstore.KVStore.
func (s *admittedHomeStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return admittedErr(s, func(inner barrierHomeKVStore) error { return inner.Scan(ctx, prefix, fn) })
}

// List implements kvstore.KVStore.
func (s *admittedHomeStore) List(ctx context.Context, prefix string) ([]string, error) {
	return admitted(s, func(inner barrierHomeKVStore) ([]string, error) { return inner.List(ctx, prefix) })
}

// Exists implements kvstore.KVStore.
func (s *admittedHomeStore) Exists(ctx context.Context, key string) (bool, error) {
	return admitted(s, func(inner barrierHomeKVStore) (bool, error) { return inner.Exists(ctx, key) })
}

// RegisterEntityIndexes implements kvstore.KVStore. It reads and writes no
// record, so it needs no admission.
func (s *admittedHomeStore) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	entry, err := s.homes.entry(s.tenantID)
	if err != nil {
		return err
	}
	return entry.guarded.RegisterEntityIndexes(entityType, indexes)
}

// QueryIndex implements kvstore.KVStore.
func (s *admittedHomeStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	return admitted(s, func(inner barrierHomeKVStore) ([][]byte, error) { return inner.QueryIndex(ctx, indexName, value) })
}

// ScanIndex implements kvstore.KVStore.
func (s *admittedHomeStore) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	return admitted(s, func(inner barrierHomeKVStore) (map[string][][]byte, error) {
		return inner.ScanIndex(ctx, indexName, start, end)
	})
}

// QueryCompoundIndex implements kvstore.KVStore.
func (s *admittedHomeStore) QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error) {
	return admitted(s, func(inner barrierHomeKVStore) ([][]byte, error) {
		return inner.QueryCompoundIndex(ctx, indexName, values)
	})
}

// ScanCompoundIndex implements kvstore.KVStore.
func (s *admittedHomeStore) ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	return admitted(s, func(inner barrierHomeKVStore) (map[string][][]byte, error) {
		return inner.ScanCompoundIndex(ctx, indexName, prefixValues, rangeStart, rangeEnd)
	})
}

// PutIfAbsent implements kvstore.AtomicKVStore.
func (s *admittedHomeStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return admitted(s, func(inner barrierHomeKVStore) (bool, error) { return inner.PutIfAbsent(ctx, key, value) })
}

// CompareAndDelete implements kvstore.AtomicKVStore.
func (s *admittedHomeStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return admitted(s, func(inner barrierHomeKVStore) (bool, error) { return inner.CompareAndDelete(ctx, key, expected) })
}

// CompareAndSwap implements kvstore.AtomicKVStore.
func (s *admittedHomeStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return admitted(s, func(inner barrierHomeKVStore) (bool, error) {
		return inner.CompareAndSwap(ctx, key, expected, next)
	})
}

// PutAllIfUnchanged implements kvstore.GuardedBatchKVStore.
func (s *admittedHomeStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return admitted(s, func(inner barrierHomeKVStore) (bool, error) {
		return inner.PutAllIfUnchanged(ctx, guardKey, guardValue, writes)
	})
}

// PutAllIfAllUnchanged implements kvstore.MultiGuardedBatchKVStore.
func (s *admittedHomeStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	return admitted(s, func(inner barrierHomeKVStore) (bool, error) {
		return inner.PutAllIfAllUnchanged(ctx, guards, writes)
	})
}
