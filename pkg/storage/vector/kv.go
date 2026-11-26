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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
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
	kvIncPrefix       = "inc/"
	kvIncLen          = 16
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
	kv kvstore.MultiGuardedBatchKVStore

	closed atomic.Bool
}

// UnguardedStoreError is returned by NewKVVectorStorage for a store that does
// not implement kvstore.MultiGuardedBatchKVStore: every write commits its data
// with the namespace generation in one guarded batch, so a store without that
// primitive cannot keep replicas' cached indexes coherent. It matches
// ErrInvalidConfig under errors.Is.
type UnguardedStoreError struct {
	// Store is the Go type of the refused store.
	Store string
}

func (e *UnguardedStoreError) Error() string {
	return fmt.Sprintf("%v: KV store %s has no multi-guarded batch", ErrInvalidConfig, e.Store)
}

// Unwrap lets errors.Is(err, ErrInvalidConfig) match.
func (e *UnguardedStoreError) Unwrap() error { return ErrInvalidConfig }

// NewKVVectorStorage returns a vector store over kv. A nil kv is refused with
// ErrInvalidConfig.
func NewKVVectorStorage(kv kvstore.KVStore) (*KVVectorStorage, error) {
	if kv == nil {
		return nil, fmt.Errorf("%w: nil KV store", ErrInvalidConfig)
	}
	guarded, ok := kv.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		return nil, &UnguardedStoreError{Store: fmt.Sprintf("%T", kv)}
	}
	return &KVVectorStorage{kv: guarded}, nil
}

func kvNamespaceKey(namespace string) string { return kvNamespacePrefix + namespace }

func kvIDKey(namespace string) string { return kvIDPrefix + namespace }

func kvGenKey(namespace string) string { return kvGenPrefix + namespace }

func kvIncKey(namespace string) string { return kvIncPrefix + namespace }

// kvVectorNSPrefix is the key prefix of one incarnation's vectors: a
// recreated namespace gets a new incarnation, so a vector a replica wrote
// into the old one is never read back under the new one.
func kvVectorNSPrefix(namespace string, inc []byte) string {
	return kvVectorPrefix + namespace + "/" + hex.EncodeToString(inc) + "/"
}

func kvVectorKey(namespace string, inc []byte, id uint64) string {
	return fmt.Sprintf("%s%016x", kvVectorNSPrefix(namespace, inc), id)
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

func vectorPut(namespace string, inc []byte, vec *VectorValue) (kvstore.KV, error) {
	value, err := schema.EncodeVectorValue(toSchemaVectorValue(vec))
	if err != nil {
		return kvstore.KV{}, err
	}
	return kvstore.KV{Key: kvVectorKey(namespace, inc, vec.ID), Value: value}, nil
}

// readIncarnation returns the namespace's current incarnation, or nil when
// the namespace does not exist.
func (s *KVVectorStorage) readIncarnation(ctx context.Context, namespace string) ([]byte, error) {
	inc, err := s.kv.Get(ctx, kvIncKey(namespace))
	if err != nil {
		return nil, err
	}
	if inc != nil && len(inc) != kvIncLen {
		return nil, fmt.Errorf("vector: namespace %q incarnation is %d bytes, want %d", namespace, len(inc), kvIncLen)
	}
	return inc, nil
}

// readVersion returns the raw incarnation and generation records. inc is nil
// when the namespace does not exist; genRaw is nil when the generation key
// was never written.
func (s *KVVectorStorage) readVersion(ctx context.Context, namespace string) (inc, genRaw []byte, gen uint64, err error) {
	inc, err = s.readIncarnation(ctx, namespace)
	if err != nil {
		return nil, nil, 0, err
	}
	genRaw, err = s.kv.Get(ctx, kvGenKey(namespace))
	if err != nil {
		return nil, nil, 0, err
	}
	if genRaw != nil {
		if gen, err = decodeID(genRaw); err != nil {
			return nil, nil, 0, err
		}
	}
	return inc, genRaw, gen, nil
}

// commit applies one write to the namespace's current incarnation in a single
// guarded batch together with the generation advance: the batch holds only
// while the incarnation and generation read are unchanged, so a write never
// lands in a deleted or recreated namespace and never lands without the
// generation step replicas rebuild on. A lost race re-reads and retries; a
// namespace that is gone (or was recreated under a new incarnation between
// the caller's read and the batch) is ErrNamespaceNotFound.
func (s *KVVectorStorage) commit(ctx context.Context, namespace string, build func(inc []byte) ([]kvstore.KV, error)) error {
	var first []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		inc, genRaw, gen, err := s.readVersion(ctx, namespace)
		if err != nil {
			return err
		}
		if inc == nil || (first != nil && !bytes.Equal(first, inc)) {
			return ErrNamespaceNotFound
		}
		first = inc
		writes, err := build(inc)
		if err != nil {
			return err
		}
		writes = append(writes, kvstore.KV{Key: kvGenKey(namespace), Value: encodeID(gen + 1)})
		applied, err := s.kv.PutAllIfAllUnchanged(ctx, []kvstore.Guard{
			{Key: kvIncKey(namespace), Value: inc},
			{Key: kvGenKey(namespace), Value: genRaw},
		}, writes)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
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
	return s.commit(ctx, namespace, func(inc []byte) ([]kvstore.KV, error) {
		put, err := vectorPut(namespace, inc, vec)
		if err != nil {
			return nil, err
		}
		return []kvstore.KV{put}, nil
	})
}

// GetVector retrieves a vector by ID.
func (s *KVVectorStorage) GetVector(ctx context.Context, namespace string, id uint64) (*VectorValue, error) {
	if err := s.checkOpenCtx(ctx); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, ErrInvalidID
	}
	inc, err := s.readIncarnation(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, ErrVectorNotFound
	}
	raw, err := s.kv.Get(ctx, kvVectorKey(namespace, inc, id))
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
	inc, err := s.readIncarnation(ctx, namespace)
	if err != nil {
		return err
	}
	if inc == nil {
		return ErrVectorNotFound
	}
	err = s.commit(ctx, namespace, func(inc []byte) ([]kvstore.KV, error) {
		key := kvVectorKey(namespace, inc, id)
		ok, err := s.kv.Exists(ctx, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrVectorNotFound
		}
		return []kvstore.KV{{Key: key, Delete: true}}, nil
	})
	if errors.Is(err, ErrNamespaceNotFound) {
		return ErrVectorNotFound
	}
	return err
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
	return s.commit(ctx, namespace, func(inc []byte) ([]kvstore.KV, error) {
		writes := make([]kvstore.KV, 0, len(vecs)+1)
		for _, vec := range vecs {
			put, err := vectorPut(namespace, inc, vec)
			if err != nil {
				return nil, err
			}
			writes = append(writes, put)
		}
		return writes, nil
	})
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
	return s.commit(ctx, namespace, func(inc []byte) ([]kvstore.KV, error) {
		writes := make([]kvstore.KV, 0, len(ids)+1)
		for _, id := range ids {
			writes = append(writes, kvstore.KV{Key: kvVectorKey(namespace, inc, id), Delete: true})
		}
		return writes, nil
	})
}

// scanVectors returns every vector of namespace with an ID >= fromID that
// keep accepts, in ascending ID order.
func (s *KVVectorStorage) scanVectors(ctx context.Context, namespace string, fromID uint64, keep func(*VectorValue) bool) ([]*VectorValue, error) {
	results := make([]*VectorValue, 0)
	inc, err := s.readIncarnation(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return results, nil
	}
	prefix := kvVectorNSPrefix(namespace, inc)
	err = s.kv.Scan(ctx, prefix, func(key string, value []byte) error {
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
	value, err := schema.EncodeNamespaceInfo(toSchemaNamespaceInfo(info))
	if err != nil {
		return err
	}
	inc := make([]byte, kvIncLen)
	if _, err := rand.Read(inc); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		exists, err := s.kv.Exists(ctx, kvNamespaceKey(info.Name))
		if err != nil {
			return err
		}
		if exists {
			return ErrNamespaceExists
		}
		genRaw, err := s.kv.Get(ctx, kvGenKey(info.Name))
		if err != nil {
			return err
		}
		guards := []kvstore.Guard{{Key: kvNamespaceKey(info.Name)}}
		writes := []kvstore.KV{
			{Key: kvIDKey(info.Name), Value: encodeID(0)},
			{Key: kvIncKey(info.Name), Value: inc},
		}
		if genRaw == nil {
			// The generation is created once and never reset: a recreate
			// continues from the deleted incarnation's generation.
			guards = append(guards, kvstore.Guard{Key: kvGenKey(info.Name)})
			writes = append(writes, kvstore.KV{Key: kvGenKey(info.Name), Value: encodeID(0)})
		}
		// The record goes last, so a store that exposes a batch's writes in
		// order never shows the namespace before its counter and incarnation.
		writes = append(writes, kvstore.KV{Key: kvNamespaceKey(info.Name), Value: value})
		applied, err := s.kv.PutAllIfAllUnchanged(ctx, guards, writes)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
}

// createNamespaceIfAbsent creates info's counters and then its metadata, each
// only where absent, so a concurrent create on another replica of the store
// neither resets the ID counter nor replaces the metadata: the create whose
// metadata lands first wins and every other one is ErrNamespaceExists.
// UpdateNamespace replaces the namespace's index parameters and metadata in
// place; its vectors are untouched.
func (s *KVVectorStorage) UpdateNamespace(ctx context.Context, namespace string, update *NamespaceUpdate) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if namespace == "" || update == nil {
		return ErrInvalidNamespace
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		inc, err := s.readIncarnation(ctx, namespace)
		if err != nil {
			return err
		}
		raw, err := s.kv.Get(ctx, kvNamespaceKey(namespace))
		if err != nil {
			return err
		}
		if inc == nil || raw == nil {
			return ErrNamespaceNotFound
		}
		si, err := schema.DecodeNamespaceInfo(raw)
		if err != nil {
			return err
		}
		info := fromSchemaNamespaceInfo(si)
		info.IndexConfig = maps.Clone(update.IndexConfig)
		info.Metadata = maps.Clone(update.Metadata)
		info.UpdatedAt = update.UpdatedAt
		value, err := schema.EncodeNamespaceInfo(toSchemaNamespaceInfo(info))
		if err != nil {
			return err
		}
		applied, err := s.kv.PutAllIfAllUnchanged(ctx, []kvstore.Guard{
			{Key: kvIncKey(namespace), Value: inc},
			{Key: kvNamespaceKey(namespace), Value: raw},
		}, []kvstore.KV{{Key: kvNamespaceKey(namespace), Value: value}})
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
}

// DeleteNamespace removes the namespace's registry entry, its ID counter and
// every vector stored under it.
func (s *KVVectorStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	var inc []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cur, genRaw, gen, err := s.readVersion(ctx, namespace)
		if err != nil {
			return err
		}
		if cur == nil {
			return ErrNamespaceNotFound
		}
		// One batch retires the incarnation and steps the generation (kept,
		// never reset): a replica write racing this delete is refused by its
		// incarnation guard, and a recreate starts above every cached gen.
		applied, err := s.kv.PutAllIfAllUnchanged(ctx, []kvstore.Guard{
			{Key: kvIncKey(namespace), Value: cur},
			{Key: kvGenKey(namespace), Value: genRaw},
		}, []kvstore.KV{
			{Key: kvNamespaceKey(namespace), Delete: true},
			{Key: kvIncKey(namespace), Delete: true},
			{Key: kvIDKey(namespace), Delete: true},
			{Key: kvGenKey(namespace), Value: encodeID(gen + 1)},
		})
		if err != nil {
			return err
		}
		if applied {
			inc = cur
			break
		}
	}
	// The retired incarnation is unreachable now; sweep its vectors.
	prefix := kvVectorNSPrefix(namespace, inc)
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
	return nil
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
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
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
		applied, err := s.kv.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: key, Value: cur}},
			[]kvstore.KV{{Key: key, Value: encodeID(last + count)}})
		if err != nil {
			return 0, err
		}
		if applied {
			return last + count, nil
		}
	}
}

// Generation returns namespace's write generation, which every vector write
// and delete advances in the same guarded batch that lands it.
func (s *KVVectorStorage) Generation(ctx context.Context, namespace string) (uint64, error) {
	if err := s.checkOpenCtx(ctx); err != nil {
		return 0, err
	}
	inc, _, gen, err := s.readVersion(ctx, namespace)
	if err != nil {
		return 0, err
	}
	if inc == nil {
		return 0, ErrNamespaceNotFound
	}
	return gen, nil
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
