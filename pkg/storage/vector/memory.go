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
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Compile-time interface assertion.
var _ Storage = (*MemoryVectorStorage)(nil)

// MemoryVectorStorage implements Storage entirely in process memory, with no
// on-disk footprint of any kind: no data directory, no WAL, no SSTables.
//
// It exists for a node whose primary KV engine is "memory" (go-qrdb#578):
// before this type, every node -- regardless of its primary engine -- routed
// vector storage through PebbleVectorStorage, which requires a non-empty
// data directory (EmptyVectorDataDirError). A memory-engine node had no data
// directory to give it, so the vector subsystem followed the primary engine
// the same way pkg/storage/memory already does for the KV engine (see
// pkg/server/lifecycle.NewLocalStorage's database.BackendMemory case): a
// dedicated in-memory type satisfying the same interface, rather than
// PebbleDB layered over an in-memory filesystem.
//
// Behavioral parity with PebbleVectorStorage, intentionally preserved:
//   - UpsertVector/DeleteVector/the Scan* methods operate on a namespace's
//     vectors regardless of whether CreateNamespace was ever called for it.
//   - NextID/BatchAllocateIDs/GetNamespaceInfo/ListNamespaces operate on the
//     namespace registry CreateNamespace/DeleteNamespace populate, and
//     report ErrNamespaceNotFound for a namespace only ever reached through
//     Upsert/Delete/Scan.
//   - DeleteNamespace removes both the registry entry and every vector
//     stored under it.
//   - Compact and CleanupTombstones are no-ops: there is no on-disk
//     structure to compact and no tombstone, since delete is immediate.
//
// Thread safety: a single RWMutex guards both the vector data and the
// namespace registry; every exported method takes it for the duration of
// its work, matching pkg/storage/memory.MemoryStorage's granularity.
type MemoryVectorStorage struct {
	mu sync.RWMutex

	// vectors holds every namespace's vectors, keyed by namespace then ID.
	// Populated by UpsertVector regardless of namespace registration, so a
	// Scan* call reaches vectors stored into a namespace CreateNamespace was
	// never called for -- the same lenience PebbleVectorStorage has, because
	// its Upsert/Delete/Scan paths never consult the namespace registry.
	vectors map[string]map[uint64]*VectorValue

	// registry holds namespace metadata and ID allocators, populated only by
	// CreateNamespace and cleared by DeleteNamespace.
	registry map[string]*memNamespace

	closed atomic.Bool
}

// memNamespace is one registered namespace's metadata and ID allocator.
type memNamespace struct {
	info      NamespaceInfo
	allocator atomic.Uint64
}

// NewMemoryVectorStorage creates an empty in-memory vector store.
func NewMemoryVectorStorage() *MemoryVectorStorage {
	return &MemoryVectorStorage{
		vectors:  make(map[string]map[uint64]*VectorValue),
		registry: make(map[string]*memNamespace),
	}
}

// cloneVectorValue returns a deep copy of v, so the caller's slice/map
// mutations after the call never alias what the store retains, and a value
// this store returns never aliases what it retains either.
func cloneVectorValue(v *VectorValue) *VectorValue {
	cp := *v
	if v.Embedding != nil {
		cp.Embedding = append([]float32(nil), v.Embedding...)
	}
	if v.Metadata != nil {
		cp.Metadata = make(map[string]string, len(v.Metadata))
		for k, val := range v.Metadata {
			cp.Metadata[k] = val
		}
	}
	return &cp
}

func (s *MemoryVectorStorage) checkOpen() error {
	if s.closed.Load() {
		return ErrStorageClosed
	}
	return nil
}

// UpsertVector inserts or overwrites a vector in namespace, creating the
// namespace's vector map on first write if needed (parity with
// PebbleVectorStorage, which never consults the namespace registry here).
func (s *MemoryVectorStorage) UpsertVector(ctx context.Context, namespace string, vec *VectorValue) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}
	if vec == nil {
		return ErrInvalidVector
	}
	if vec.ID == 0 {
		return ErrInvalidID
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.vectors[namespace]
	if !ok {
		ns = make(map[uint64]*VectorValue)
		s.vectors[namespace] = ns
	}
	ns[vec.ID] = cloneVectorValue(vec)
	return nil
}

// GetVector retrieves a vector by ID.
func (s *MemoryVectorStorage) GetVector(ctx context.Context, namespace string, id uint64) (*VectorValue, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrContextCanceled
	}
	if id == 0 {
		return nil, ErrInvalidID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	vec, ok := s.vectors[namespace][id]
	if !ok {
		return nil, ErrVectorNotFound
	}
	return cloneVectorValue(vec), nil
}

// DeleteVector removes a vector by ID. Deletion is immediate: there is no
// tombstone, since there is no compaction to defer the removal past.
func (s *MemoryVectorStorage) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}
	if id == 0 {
		return ErrInvalidID
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.vectors[namespace]
	if !ok {
		return ErrVectorNotFound
	}
	if _, ok := ns[id]; !ok {
		return ErrVectorNotFound
	}
	delete(ns, id)
	return nil
}

// BatchUpsertVectors upserts every vector in vecs. The batch is atomic with
// respect to other callers (the whole operation holds the write lock), but
// reports the first per-vector error it hits rather than rolling back the
// vectors already applied -- the same all-or-nothing-at-the-caller's-risk
// contract BatchUpsertVectors's own doc comment on PebbleVectorStorage
// carries, since a Pebble batch is likewise committed as a whole only once
// every per-vector encode has already succeeded.
func (s *MemoryVectorStorage) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*VectorValue) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}
	if len(vecs) == 0 {
		return ErrEmptyBatch
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.vectors[namespace]
	if !ok {
		ns = make(map[uint64]*VectorValue)
		s.vectors[namespace] = ns
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
		ns[vec.ID] = cloneVectorValue(vec)
	}
	return nil
}

// BatchDeleteVectors deletes every vector named in ids. A missing ID is not
// an error: a batch delete is idempotent over IDs the caller may already
// believe are gone.
func (s *MemoryVectorStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}
	if len(ids) == 0 {
		return ErrEmptyBatch
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ns := s.vectors[namespace]
	for _, id := range ids {
		if ns != nil {
			delete(ns, id)
		}
	}
	return nil
}

// matchesFilter reports whether vec's metadata satisfies op against field and
// value. FilterIn and FilterNotIn are not evaluated -- PebbleVectorStorage's
// ScanByMultipleFilters switch has no case for them either, so a filter using
// one always passes there, and this mirrors that rather than silently
// narrowing results PebbleVectorStorage would have returned.
func matchesFilter(vec *VectorValue, field, value string, op FilterOperator) bool {
	metaValue, exists := vec.Metadata[field]
	switch op {
	case FilterEqual:
		return exists && metaValue == value
	case FilterNotEqual:
		return !exists || metaValue != value
	default:
		return true
	}
}

// sortedByID returns vecs sorted by ascending vector ID, so paginated scans
// are deterministic across calls even though the backing map is not ordered.
func sortedByID(vecs []*VectorValue) []*VectorValue {
	sort.Slice(vecs, func(i, j int) bool { return vecs[i].ID < vecs[j].ID })
	return vecs
}

// applyScanOptions slices results by opts' Offset and Limit. OrderBy and
// Descending are accepted but not applied, exactly as
// PebbleVectorStorage's three Scan* methods already leave them unused.
func applyScanOptions(results []*VectorValue, opts *ScanOptions) []*VectorValue {
	if opts == nil {
		return results
	}
	if opts.Offset > 0 {
		if opts.Offset >= len(results) {
			return results[:0]
		}
		results = results[opts.Offset:]
	}
	if opts.Limit > 0 && opts.Limit < len(results) {
		results = results[:opts.Limit]
	}
	return results
}

// ScanByMetadata scans vectors in namespace whose field equals value.
func (s *MemoryVectorStorage) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *ScanOptions) ([]*VectorValue, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrContextCanceled
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	results := make([]*VectorValue, 0)
	for _, vec := range s.vectors[namespace] {
		if matchesFilter(vec, field, value, FilterEqual) {
			results = append(results, cloneVectorValue(vec))
		}
	}
	return applyScanOptions(sortedByID(results), opts), nil
}

// ScanByMultipleFilters scans namespace against every filter in filters (AND
// semantics), or the whole namespace when filters is empty.
func (s *MemoryVectorStorage) ScanByMultipleFilters(ctx context.Context, namespace string, filters []MetadataFilter, opts *ScanOptions) ([]*VectorValue, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if len(filters) == 0 {
		return s.ScanNamespace(ctx, namespace, opts)
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrContextCanceled
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	results := make([]*VectorValue, 0)
	for _, vec := range s.vectors[namespace] {
		match := true
		for _, f := range filters {
			if !matchesFilter(vec, f.Field, f.Value, f.Operator) {
				match = false
				break
			}
		}
		if match {
			results = append(results, cloneVectorValue(vec))
		}
	}
	return applyScanOptions(sortedByID(results), opts), nil
}

// ScanNamespace scans every vector in namespace.
func (s *MemoryVectorStorage) ScanNamespace(ctx context.Context, namespace string, opts *ScanOptions) ([]*VectorValue, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrContextCanceled
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ns := s.vectors[namespace]
	results := make([]*VectorValue, 0, len(ns))
	for _, vec := range ns {
		results = append(results, cloneVectorValue(vec))
	}
	return applyScanOptions(sortedByID(results), opts), nil
}

// CreateNamespace registers namespace metadata and resets its ID allocator.
// It does not touch any vector already stored under the same name by a prior
// Upsert -- parity with PebbleVectorStorage, whose CreateNamespace likewise
// never scans existing vector keys.
func (s *MemoryVectorStorage) CreateNamespace(ctx context.Context, info *NamespaceInfo) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if info == nil || info.Name == "" {
		return ErrInvalidNamespace
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.registry[info.Name]; exists {
		return ErrNamespaceExists
	}
	infoCopy := *info
	s.registry[info.Name] = &memNamespace{info: infoCopy}
	return nil
}

// DeleteNamespace removes namespace's registry entry and every vector stored
// under it.
func (s *MemoryVectorStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.registry[namespace]; !exists {
		return ErrNamespaceNotFound
	}
	delete(s.registry, namespace)
	delete(s.vectors, namespace)
	return nil
}

// ListNamespaces lists every registered namespace.
func (s *MemoryVectorStorage) ListNamespaces(ctx context.Context) ([]NamespaceInfo, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	results := make([]NamespaceInfo, 0, len(s.registry))
	for _, ns := range s.registry {
		results = append(results, ns.info)
	}
	return results, nil
}

// GetNamespaceInfo returns namespace's registered metadata, with VectorCount
// and StorageBytes refreshed from the live vector count (parity with
// PebbleVectorStorage, which likewise overlays GetStats onto the cached
// info rather than trusting a count that could have drifted).
func (s *MemoryVectorStorage) GetNamespaceInfo(ctx context.Context, namespace string) (*NamespaceInfo, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ns, exists := s.registry[namespace]
	if !exists {
		return nil, ErrNamespaceNotFound
	}
	stats := s.statsLocked(namespace)
	infoCopy := ns.info
	infoCopy.VectorCount = stats.Count
	infoCopy.StorageBytes = int64(stats.SizeBytes) //nolint:gosec // G115: size bounded by process memory
	return &infoCopy, nil
}

// statsLocked computes NamespaceStats for namespace. Callers hold s.mu.
func (s *MemoryVectorStorage) statsLocked(namespace string) *NamespaceStats {
	ns := s.vectors[namespace]
	count := uint64(len(ns))
	var sizeBytes uint64
	for _, vec := range ns {
		sizeBytes += vectorValueSize(vec)
	}
	return &NamespaceStats{
		Namespace: namespace,
		Count:     count,
		SizeBytes: sizeBytes,
	}
}

// vectorValueSize estimates vec's in-memory footprint for GetStats/
// GetNamespaceInfo's StorageBytes field. It is an estimate, not an exact
// accounting of Go's own allocator overhead -- exactly as
// PebbleVectorStorage's own GetStats sums encoded key/value lengths rather
// than true on-disk bytes.
func vectorValueSize(vec *VectorValue) uint64 {
	size := uint64(8 + 8 + 8 + 8) // ID, CreatedAt, UpdatedAt, Version
	size += uint64(len(vec.Embedding) * 4)
	for k, v := range vec.Metadata {
		size += uint64(len(k) + len(v))
	}
	return size
}

// GetStats returns statistics for namespace, counting whatever vectors are
// stored under it regardless of namespace registration (parity with
// PebbleVectorStorage, whose GetStats scans the raw key range rather than
// consulting the registry).
func (s *MemoryVectorStorage) GetStats(ctx context.Context, namespace string) (*NamespaceStats, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statsLocked(namespace), nil
}

// NextID allocates the next unique ID for namespace. The namespace must have
// been registered with CreateNamespace first (parity with
// PebbleVectorStorage, whose allocator lives only in its namespace cache).
func (s *MemoryVectorStorage) NextID(namespace string) (uint64, error) {
	if err := s.checkOpen(); err != nil {
		return 0, err
	}

	s.mu.RLock()
	ns, exists := s.registry[namespace]
	s.mu.RUnlock()
	if !exists {
		return 0, ErrNamespaceNotFound
	}
	return ns.allocator.Add(1), nil
}

// BatchAllocateIDs allocates count sequential IDs for namespace, which must
// already be registered.
func (s *MemoryVectorStorage) BatchAllocateIDs(namespace string, count int) ([]uint64, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if count <= 0 {
		return nil, ErrInvalidID
	}

	s.mu.RLock()
	ns, exists := s.registry[namespace]
	s.mu.RUnlock()
	if !exists {
		return nil, ErrNamespaceNotFound
	}

	startID := ns.allocator.Add(uint64(count))
	ids := make([]uint64, count)
	for i := 0; i < count; i++ {
		ids[i] = startID - uint64(count) + uint64(i) + 1
	}
	return ids, nil
}

// Compact is a no-op: there is no on-disk structure to compact.
func (s *MemoryVectorStorage) Compact(ctx context.Context, namespace string) error {
	return s.checkOpen()
}

// CleanupTombstones is a no-op: deletion is immediate (see DeleteVector), so
// no tombstone is ever created to clean up.
func (s *MemoryVectorStorage) CleanupTombstones(ctx context.Context, olderThan time.Duration) error {
	return s.checkOpen()
}

// Close releases every vector and namespace this store holds. Subsequent
// calls report ErrStorageClosed; calling Close twice is a no-op.
func (s *MemoryVectorStorage) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vectors = nil
	s.registry = nil
	return nil
}
