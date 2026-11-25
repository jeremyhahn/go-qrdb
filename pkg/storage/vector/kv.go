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

package vector

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// Compile-time interface assertion.
var _ Storage = (*KVVectorStorage)(nil)

// Key layout of a KVVectorStorage inside its KV store.
const (
	kvNamespacePrefix = "ns/"
	kvVectorPrefix    = "v/"
	kvIDPrefix        = "id/"
	kvGenPrefix       = "gen/"
	kvIDHexLen        = 16
)

// KVVectorStorage implements Storage over a kvstore.KVStore, so a store that
// is itself a tenant's isolated home (a Raft home group or a barrier backend)
// holds that tenant's vectors.
//
// Keys: "ns/<namespace>" holds the NamespaceInfo, "v/<namespace>/<id>" (id as
// 16 lower-case hex digits) holds one VectorValue, and "id/<namespace>" holds
// the last allocated ID as 8 big-endian bytes. Values use the msgpack schema
// encoding the Pebble store uses. Semantics and errors match
// MemoryVectorStorage: Upsert/Delete/Scan never consult the namespace
// registry, NextID/BatchAllocateIDs/GetNamespaceInfo/ListNamespaces do.
//
// The KV store is not owned: Close marks this view closed and leaves the
// store open.
type KVVectorStorage struct {
	kv kvstore.KVStore

	// mu serialises read-modify-write sequences (namespace create/update and,
	// when the store has no CompareAndSwap, ID allocation).
	mu sync.Mutex

	closed atomic.Bool
}

// NewKVVectorStorage returns a vector store over kv. A nil kv is refused with
// ErrInvalidConfig.
func NewKVVectorStorage(kv kvstore.KVStore) (*KVVectorStorage, error) {
	if kv == nil {
		return nil, fmt.Errorf("%w: nil KV store", ErrInvalidConfig)
	}
	return &KVVectorStorage{kv: kv}, nil
}

func kvNamespaceKey(namespace string) string { return kvNamespacePrefix + namespace }

func kvIDKey(namespace string) string { return kvIDPrefix + namespace }

func kvGenKey(namespace string) string { return kvGenPrefix + namespace }

func kvVectorNSPrefix(namespace string) string { return kvVectorPrefix + namespace + "/" }

func kvVectorKey(namespace string, id uint64) string {
	return fmt.Sprintf("%s%016x", kvVectorNSPrefix(namespace), id)
}

// kvParseVectorID returns the ID a vector key under prefix carries, and false
// for a key of a longer namespace name that merely shares the prefix.
func kvParseVectorID(key, prefix string) (uint64, bool) {
	rest := strings.TrimPrefix(key, prefix)
	if len(rest) != kvIDHexLen {
		return 0, false
	}
	id, err := strconv.ParseUint(rest, 16, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func (s *KVVectorStorage) checkOpen() error {
	if s.closed.Load() {
		return ErrStorageClosed
	}
	return nil
}

func (s *KVVectorStorage) checkOpenCtx(ctx context.Context) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ErrContextCanceled
	}
	return nil
}

func (s *KVVectorStorage) putVector(ctx context.Context, namespace string, vec *VectorValue) error {
	value, err := schema.EncodeVectorValue(toSchemaVectorValue(vec))
	if err != nil {
		return err
	}
	return s.kv.Put(ctx, kvVectorKey(namespace, vec.ID), value)
}

// UpsertVector inserts or overwrites a vector in namespace.
func (s *KVVectorStorage) UpsertVector(ctx context.Context, namespace string, vec *VectorValue) error {
	if err := s.checkOpenCtx(ctx); err != nil {
		return err
	}
	if vec == nil {
		return ErrInvalidVector
	}
	if vec.ID == 0 {
		return ErrInvalidID
	}
	if err := s.putVector(ctx, namespace, vec); err != nil {
		return err
	}
	return s.advance(ctx, namespace)
}

// GetVector retrieves a vector by ID.
func (s *KVVectorStorage) GetVector(ctx context.Context, namespace string, id uint64) (*VectorValue, error) {
	if err := s.checkOpenCtx(ctx); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, ErrInvalidID
	}
	raw, err := s.kv.Get(ctx, kvVectorKey(namespace, id))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, ErrVectorNotFound
	}
	sv, err := schema.DecodeVectorValue(raw)
	if err != nil {
		return nil, err
	}
	return fromSchemaVectorValue(sv), nil
}

// DeleteVector removes a vector by ID; a missing vector is ErrVectorNotFound.
func (s *KVVectorStorage) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	if err := s.checkOpenCtx(ctx); err != nil {
		return err
	}
	if id == 0 {
		return ErrInvalidID
	}
	key := kvVectorKey(namespace, id)
	ok, err := s.kv.Exists(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return ErrVectorNotFound
	}
	if err := s.kv.Delete(ctx, key); err != nil {
		return err
	}
	return s.advance(ctx, namespace)
}

// BatchUpsertVectors validates every vector, then writes each one.
func (s *KVVectorStorage) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*VectorValue) error {
	if err := s.checkOpenCtx(ctx); err != nil {
		return err
	}
	if len(vecs) == 0 {
		return ErrEmptyBatch
	}
	for _, vec := range vecs {
		if vec == nil {
			return ErrInvalidVector
		}
		if vec.ID == 0 {
			return ErrInvalidID
		}
	}
	for _, vec := range vecs {
		if err := s.putVector(ctx, namespace, vec); err != nil {
			return err
		}
	}
	return s.advance(ctx, namespace)
}

// BatchDeleteVectors deletes every vector named in ids; a missing ID is not
// an error.
func (s *KVVectorStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	if err := s.checkOpenCtx(ctx); err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrEmptyBatch
	}
	for _, id := range ids {
		if err := s.kv.Delete(ctx, kvVectorKey(namespace, id)); err != nil {
			return err
		}
	}
	return s.advance(ctx, namespace)
}

// scanVectors returns every vector of namespace with an ID >= fromID that
// keep accepts, in ascending ID order.
func (s *KVVectorStorage) scanVectors(ctx context.Context, namespace string, fromID uint64, keep func(*VectorValue) bool) ([]*VectorValue, error) {
	prefix := kvVectorNSPrefix(namespace)
	results := make([]*VectorValue, 0)
	err := s.kv.Scan(ctx, prefix, func(key string, value []byte) error {
		id, ok := kvParseVectorID(key, prefix)
		if !ok || id < fromID {
			return nil
		}
		sv, err := schema.DecodeVectorValue(value)
		if err != nil {
			return err
		}
		vec := fromSchemaVectorValue(sv)
		if keep == nil || keep(vec) {
			results = append(results, vec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sortedByID(results), nil
}

// ScanByMetadata scans vectors in namespace whose field equals value.
func (s *KVVectorStorage) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *ScanOptions) ([]*VectorValue, error) {
	if err := s.checkOpenCtx(ctx); err != nil {
		return nil, err
	}
	results, err := s.scanVectors(ctx, namespace, 0, func(vec *VectorValue) bool {
		return matchesFilter(vec, field, value, FilterEqual)
	})
	if err != nil {
		return nil, err
	}
	return applyScanOptions(results, opts), nil
}

// ScanByMultipleFilters scans namespace against every filter (AND), or the
// whole namespace when filters is empty.
func (s *KVVectorStorage) ScanByMultipleFilters(ctx context.Context, namespace string, filters []MetadataFilter, opts *ScanOptions) ([]*VectorValue, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if len(filters) == 0 {
		return s.ScanNamespace(ctx, namespace, opts)
	}
	if ctx.Err() != nil {
		return nil, ErrContextCanceled
	}
	results, err := s.scanVectors(ctx, namespace, 0, func(vec *VectorValue) bool {
		for _, f := range filters {
			if !matchesFilter(vec, f.Field, f.Value, f.Operator) {
				return false
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return applyScanOptions(results, opts), nil
}

// ScanNamespace scans every vector in namespace.
func (s *KVVectorStorage) ScanNamespace(ctx context.Context, namespace string, opts *ScanOptions) ([]*VectorValue, error) {
	if err := s.checkOpenCtx(ctx); err != nil {
		return nil, err
	}
	var fromID uint64
	if opts != nil {
		fromID = opts.FromID
	}
	results, err := s.scanVectors(ctx, namespace, fromID, nil)
	if err != nil {
		return nil, err
	}
	return applyScanOptions(results, opts), nil
}

func (s *KVVectorStorage) getNamespace(ctx context.Context, namespace string) (*NamespaceInfo, error) {
	raw, err := s.kv.Get(ctx, kvNamespaceKey(namespace))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, ErrNamespaceNotFound
	}
	si, err := schema.DecodeNamespaceInfo(raw)
	if err != nil {
		return nil, err
	}
	return fromSchemaNamespaceInfo(si), nil
}

func (s *KVVectorStorage) putNamespace(ctx context.Context, info *NamespaceInfo) error {
	value, err := schema.EncodeNamespaceInfo(toSchemaNamespaceInfo(info))
	if err != nil {
		return err
	}
	return s.kv.Put(ctx, kvNamespaceKey(info.Name), value)
}

func encodeID(id uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, id)
	return b
}

func decodeID(raw []byte) (uint64, error) {
	if len(raw) != 8 {
		return 0, fmt.Errorf("vector id counter is %d bytes, want 8", len(raw))
	}
	return binary.BigEndian.Uint64(raw), nil
}

// CreateNamespace registers namespace metadata and resets its ID allocator.
// It does not touch vectors already stored under the same name.
func (s *KVVectorStorage) CreateNamespace(ctx context.Context, info *NamespaceInfo) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if info == nil || info.Name == "" {
		return ErrInvalidNamespace
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exists, err := s.kv.Exists(ctx, kvNamespaceKey(info.Name))
	if err != nil {
		return err
	}
	if exists {
		return ErrNamespaceExists
	}
	if atomicKV, ok := s.kv.(kvstore.AtomicKVStore); ok {
		return s.createNamespaceIfAbsent(ctx, atomicKV, info)
	}
	if err := s.kv.Put(ctx, kvIDKey(info.Name), encodeID(0)); err != nil {
		return err
	}
	if err := s.kv.Put(ctx, kvGenKey(info.Name), encodeID(0)); err != nil {
		return err
	}
	infoCopy := *info
	return s.putNamespace(ctx, &infoCopy)
}

// createNamespaceIfAbsent creates info's counters and then its metadata, each
// only where absent, so a concurrent create on another replica of the store
// neither resets the ID counter nor replaces the metadata: the create whose
// metadata lands first wins and every other one is ErrNamespaceExists.
func (s *KVVectorStorage) createNamespaceIfAbsent(ctx context.Context, atomicKV kvstore.AtomicKVStore, info *NamespaceInfo) error {
	value, err := schema.EncodeNamespaceInfo(toSchemaNamespaceInfo(info))
	if err != nil {
		return err
	}
	for _, key := range []string{kvIDKey(info.Name), kvGenKey(info.Name)} {
		if _, err := atomicKV.PutIfAbsent(ctx, key, encodeID(0)); err != nil {
			return err
		}
	}
	stored, err := atomicKV.PutIfAbsent(ctx, kvNamespaceKey(info.Name), value)
	if err != nil {
		return err
	}
	if !stored {
		return ErrNamespaceExists
	}
	return nil
}

// UpdateNamespace replaces the namespace's index parameters and metadata in
// place; its vectors are untouched.
func (s *KVVectorStorage) UpdateNamespace(ctx context.Context, namespace string, update *NamespaceUpdate) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if namespace == "" || update == nil {
		return ErrInvalidNamespace
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.getNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	info.IndexConfig = maps.Clone(update.IndexConfig)
	info.Metadata = maps.Clone(update.Metadata)
	info.UpdatedAt = update.UpdatedAt
	return s.putNamespace(ctx, info)
}

// DeleteNamespace removes the namespace's registry entry, its ID counter and
// every vector stored under it.
func (s *KVVectorStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exists, err := s.kv.Exists(ctx, kvNamespaceKey(namespace))
	if err != nil {
		return err
	}
	if !exists {
		return ErrNamespaceNotFound
	}
	prefix := kvVectorNSPrefix(namespace)
	keys, err := s.kv.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if _, ok := kvParseVectorID(key, prefix); !ok {
			continue
		}
		if err := s.kv.Delete(ctx, key); err != nil {
			return err
		}
	}
	if err := s.kv.Delete(ctx, kvIDKey(namespace)); err != nil {
		return err
	}
	if err := s.kv.Delete(ctx, kvGenKey(namespace)); err != nil {
		return err
	}
	return s.kv.Delete(ctx, kvNamespaceKey(namespace))
}

// ListNamespaces lists every registered namespace.
func (s *KVVectorStorage) ListNamespaces(ctx context.Context) ([]NamespaceInfo, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	results := make([]NamespaceInfo, 0)
	err := s.kv.Scan(ctx, kvNamespacePrefix, func(_ string, value []byte) error {
		si, err := schema.DecodeNamespaceInfo(value)
		if err != nil {
			return err
		}
		results = append(results, *fromSchemaNamespaceInfo(si))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// GetNamespaceInfo returns namespace's registered metadata with VectorCount
// and StorageBytes refreshed from the stored vectors.
func (s *KVVectorStorage) GetNamespaceInfo(ctx context.Context, namespace string) (*NamespaceInfo, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	info, err := s.getNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	stats, err := s.stats(ctx, namespace)
	if err != nil {
		return nil, err
	}
	info.VectorCount = stats.Count
	info.StorageBytes = int64(stats.SizeBytes) //nolint:gosec // G115: size bounded by the store
	return info, nil
}

func (s *KVVectorStorage) stats(ctx context.Context, namespace string) (*NamespaceStats, error) {
	vecs, err := s.scanVectors(ctx, namespace, 0, nil)
	if err != nil {
		return nil, err
	}
	var size uint64
	for _, vec := range vecs {
		size += vectorValueSize(vec)
	}
	return &NamespaceStats{Namespace: namespace, Count: uint64(len(vecs)), SizeBytes: size}, nil
}

// GetStats returns statistics for namespace, counting whatever vectors are
// stored under it regardless of namespace registration.
func (s *KVVectorStorage) GetStats(ctx context.Context, namespace string) (*NamespaceStats, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.stats(ctx, namespace)
}

// allocate reserves count IDs for namespace and returns the last one. The
// counter is persisted, so a second KVVectorStorage over the same store
// never hands out an ID twice.
func (s *KVVectorStorage) allocate(namespace string, count uint64) (uint64, error) {
	return s.increment(context.Background(), kvIDKey(namespace), count)
}

// increment adds count to the counter stored under key and returns the new
// value; a missing counter is ErrNamespaceNotFound.
func (s *KVVectorStorage) increment(ctx context.Context, key string, count uint64) (uint64, error) {
	if atomicKV, ok := s.kv.(kvstore.AtomicKVStore); ok {
		for {
			cur, err := atomicKV.Get(ctx, key)
			if err != nil {
				return 0, err
			}
			if cur == nil {
				return 0, ErrNamespaceNotFound
			}
			last, err := decodeID(cur)
			if err != nil {
				return 0, err
			}
			swapped, err := atomicKV.CompareAndSwap(ctx, key, cur, encodeID(last+count))
			if err != nil {
				return 0, err
			}
			if swapped {
				return last + count, nil
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.kv.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	if cur == nil {
		return 0, ErrNamespaceNotFound
	}
	last, err := decodeID(cur)
	if err != nil {
		return 0, err
	}
	if err := s.kv.Put(ctx, key, encodeID(last+count)); err != nil {
		return 0, err
	}
	return last + count, nil
}

// advance moves namespace's write generation past a write that has landed.
func (s *KVVectorStorage) advance(ctx context.Context, namespace string) error {
	_, err := s.increment(ctx, kvGenKey(namespace), 1)
	if errors.Is(err, ErrNamespaceNotFound) {
		return nil
	}
	return err
}

// Generation returns namespace's write generation, which every vector write
// and delete advances after it lands.
func (s *KVVectorStorage) Generation(ctx context.Context, namespace string) (uint64, error) {
	if err := s.checkOpenCtx(ctx); err != nil {
		return 0, err
	}
	raw, err := s.kv.Get(ctx, kvGenKey(namespace))
	if err != nil {
		return 0, err
	}
	if raw == nil {
		return 0, ErrNamespaceNotFound
	}
	return decodeID(raw)
}

// NextID allocates the next unique ID for a registered namespace.
func (s *KVVectorStorage) NextID(namespace string) (uint64, error) {
	if err := s.checkOpen(); err != nil {
		return 0, err
	}
	return s.allocate(namespace, 1)
}

// BatchAllocateIDs allocates count sequential IDs for a registered namespace.
func (s *KVVectorStorage) BatchAllocateIDs(namespace string, count int) ([]uint64, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if count <= 0 {
		return nil, ErrInvalidID
	}
	end, err := s.allocate(namespace, uint64(count))
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, count)
	for i := range ids {
		ids[i] = end - uint64(count) + uint64(i) + 1
	}
	return ids, nil
}

// Compact is a no-op: the KV store owns its own compaction.
func (s *KVVectorStorage) Compact(ctx context.Context, namespace string) error {
	return s.checkOpen()
}

// CleanupTombstones is a no-op: deletion is immediate.
func (s *KVVectorStorage) CleanupTombstones(ctx context.Context, olderThan time.Duration) error {
	return s.checkOpen()
}

// Close marks the store closed; the KV store is not owned and stays open.
func (s *KVVectorStorage) Close() error {
	s.closed.Store(true)
	return nil
}
