// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package vector provides the storage layer for vector embeddings and metadata.
package vector

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// PebbleVectorStorage implements the Storage interface using PebbleDB.
type PebbleVectorStorage struct {
	db      *pebble.DB
	config  *Config
	logger  interfaces.Logger
	metrics *vectorMetrics

	// ID allocation per namespace
	idAllocators map[string]*atomic.Uint64
	idMu         sync.RWMutex

	// Namespace info cache
	nsCache map[string]*NamespaceInfo
	nsMu    sync.RWMutex

	closed atomic.Bool

	// Semaphores for rate limiting
	readSem  chan struct{}
	writeSem chan struct{}
}

// NewPebbleVectorStorage creates a new PebbleDB vector storage instance.
func NewPebbleVectorStorage(config *Config, logger interfaces.Logger) (*PebbleVectorStorage, error) {
	if config == nil {
		return nil, ErrInvalidConfig
	}
	if logger == nil {
		return nil, ErrInvalidConfig
	}

	// Validate and apply defaults
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.WithDefaults()

	// Convert config to PebbleDB options
	opts := config.ToPebbleOptions()

	// Open the database
	db, err := pebble.Open(config.DataDir, opts)
	if err != nil {
		return nil, err
	}

	// Create semaphores for rate limiting
	readSem := make(chan struct{}, config.MaxConcurrentReads)
	writeSem := make(chan struct{}, config.MaxConcurrentWrites)

	s := &PebbleVectorStorage{
		db:           db,
		config:       config,
		logger:       logger.WithField("component", "vector_storage"),
		metrics:      newVectorMetrics(),
		idAllocators: make(map[string]*atomic.Uint64),
		nsCache:      make(map[string]*NamespaceInfo),
		readSem:      readSem,
		writeSem:     writeSem,
	}

	// Load existing namespaces and initialize ID allocators
	if err := s.loadNamespaces(); err != nil {
		_ = db.Close() //nolint:gosec // G104: Error deliberately ignored - cleanup during error path
		return nil, err
	}

	s.logger.Info("PebbleDB vector storage initialized")

	return s, nil
}

// loadNamespaces loads existing namespaces and initializes ID allocators.
func (s *PebbleVectorStorage) loadNamespaces() error {
	prefix := schema.EncodeNamespacesPrefix()
	opts := &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: schema.PrefixSuccessor(prefix),
	}

	iter, err := s.db.NewIter(opts)
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		info, err := schema.DecodeNamespaceInfo(iter.Value())
		if err != nil {
			s.logger.Warnf("Failed to decode namespace info: %v", err)
			continue
		}

		s.nsCache[info.Name] = fromSchemaNamespaceInfo(info)

		// Initialize ID allocator
		seqKey := schema.EncodeIDSequenceKey(info.Name)
		seqValue, closer, err := s.db.Get(seqKey)
		if err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}

		var currentID uint64
		if err == nil {
			if len(seqValue) >= 8 {
				currentID = uint64(seqValue[0]) | uint64(seqValue[1])<<8 |
					uint64(seqValue[2])<<16 | uint64(seqValue[3])<<24 |
					uint64(seqValue[4])<<32 | uint64(seqValue[5])<<40 |
					uint64(seqValue[6])<<48 | uint64(seqValue[7])<<56
			}
			_ = closer.Close() //nolint:gosec // G104: Error deliberately ignored - cleanup during error path
		}

		allocator := &atomic.Uint64{}
		allocator.Store(currentID)
		s.idAllocators[info.Name] = allocator

		count++
	}

	if count > 0 {
		s.metrics.namespacesCount.Set(float64(count))
	}

	return iter.Error()
}

// UpsertVector inserts or updates a vector.
func (s *PebbleVectorStorage) UpsertVector(ctx context.Context, namespace string, vec *VectorValue) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	// Check context before proceeding
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}

	if vec == nil {
		return ErrInvalidVector
	}

	// Acquire write semaphore
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return ErrContextCanceled
	}

	start := time.Now()
	defer func() {
		s.metrics.upsertLatency.Observe(time.Since(start).Seconds())
	}()

	s.metrics.upsertOps.Inc()

	// Encode vector value
	value, err := schema.EncodeVectorValue(toSchemaVectorValue(vec))
	if err != nil {
		s.metrics.errors.Inc()
		return err
	}

	// Create batch for atomic operation
	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	// Write primary vector key
	vectorKey := schema.EncodeVectorKey(namespace, vec.ID, vec.Version)
	if err := batch.Set(vectorKey, value, pebble.NoSync); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	// Delete old metadata indexes if this is an update
	if vec.Version > 1 {
		oldVectorKey := schema.EncodeVectorKey(namespace, vec.ID, vec.Version-1)
		oldValue, closer, err := s.db.Get(oldVectorKey)
		if err == nil {
			oldVec, decodeErr := schema.DecodeVectorValue(oldValue)
			_ = closer.Close() //nolint:gosec // G104: Error deliberately ignored - cleanup during error path
			if decodeErr == nil {
				// Delete old metadata indexes
				for field, fieldValue := range oldVec.Metadata {
					metaKey := schema.EncodeMetadataKey(namespace, field, fieldValue, vec.ID, vec.Version-1)
					_ = batch.Delete(metaKey, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit

					reverseKey := schema.EncodeReverseKey(namespace, vec.ID, field, vec.Version-1)
					_ = batch.Delete(reverseKey, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit
				}
			}
		}
	}

	// Create metadata indexes
	for field, fieldValue := range vec.Metadata {
		metaKey := schema.EncodeMetadataKey(namespace, field, fieldValue, vec.ID, vec.Version)
		if err := batch.Set(metaKey, []byte{}, pebble.NoSync); err != nil {
			s.metrics.errors.Inc()
			return err
		}

		// Create reverse index
		reverseKey := schema.EncodeReverseKey(namespace, vec.ID, field, vec.Version)
		reverseValue := []byte(fieldValue)
		if err := batch.Set(reverseKey, reverseValue, pebble.NoSync); err != nil {
			s.metrics.errors.Inc()
			return err
		}
	}

	// Update namespace stats
	statsKey := schema.EncodeStatsKey(namespace, "count")
	_ = batch.Set(statsKey, encodeUint64(vec.ID), pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit

	// Commit batch
	writeOpts := pebble.NoSync
	if s.config.SyncWrites {
		writeOpts = pebble.Sync
	}

	if err := batch.Commit(writeOpts); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	s.metrics.bytesWritten.Add(float64(len(value)))
	s.metrics.vectorsStored.Inc()

	return nil
}

// GetVector retrieves a vector by ID.
func (s *PebbleVectorStorage) GetVector(ctx context.Context, namespace string, id uint64) (*VectorValue, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	// Check context before proceeding
	if err := ctx.Err(); err != nil {
		return nil, ErrContextCanceled
	}

	if id == 0 {
		return nil, ErrInvalidID
	}

	// Acquire read semaphore
	select {
	case s.readSem <- struct{}{}:
		defer func() { <-s.readSem }()
	case <-ctx.Done():
		return nil, ErrContextCanceled
	}

	start := time.Now()
	defer func() {
		s.metrics.getLatency.Observe(time.Since(start).Seconds())
	}()

	s.metrics.getOps.Inc()

	// Find the latest version
	vectorKeyPrefix := schema.EncodeVectorKeyPrefix(namespace, id)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: vectorKeyPrefix,
		UpperBound: schema.PrefixSuccessor(vectorKeyPrefix),
	})
	if err != nil {
		s.metrics.errors.Inc()
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	// Seek to last version
	if !iter.Last() {
		return nil, ErrVectorNotFound
	}

	// Decode value
	value := iter.Value()
	schemaVec, err := schema.DecodeVectorValue(value)
	if err != nil {
		s.metrics.errors.Inc()
		return nil, err
	}

	s.metrics.bytesRead.Add(float64(len(value)))

	return fromSchemaVectorValue(schemaVec), nil
}

// DeleteVector deletes a vector by ID.
func (s *PebbleVectorStorage) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	// Check context before proceeding
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}

	if id == 0 {
		return ErrInvalidID
	}

	// Acquire write semaphore
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return ErrContextCanceled
	}

	start := time.Now()
	defer func() {
		s.metrics.deleteLatency.Observe(time.Since(start).Seconds())
	}()

	s.metrics.deleteOps.Inc()

	// Create batch for atomic deletion
	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	// Find all versions
	vectorKeyPrefix := schema.EncodeVectorKeyPrefix(namespace, id)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: vectorKeyPrefix,
		UpperBound: schema.PrefixSuccessor(vectorKeyPrefix),
	})
	if err != nil {
		s.metrics.errors.Inc()
		return err
	}

	found := false
	for iter.First(); iter.Valid(); iter.Next() {
		found = true
		key := iter.Key()
		_, _, version, err := schema.DecodeVectorKey(key)
		if err != nil {
			continue
		}

		// Delete vector key
		vectorKey := schema.EncodeVectorKey(namespace, id, version)
		_ = batch.Delete(vectorKey, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit

		// Delete metadata indexes using reverse index
		reversePrefix := schema.EncodeReverseKeyPrefix(namespace, id)
		reverseIter, reverseErr := s.db.NewIter(&pebble.IterOptions{
			LowerBound: reversePrefix,
			UpperBound: schema.PrefixSuccessor(reversePrefix),
		})
		if reverseErr != nil {
			continue
		}

		for reverseIter.First(); reverseIter.Valid(); reverseIter.Next() {
			_, _, field, ver, err := schema.DecodeReverseKey(reverseIter.Key())
			if err != nil {
				continue
			}

			if ver != version {
				continue
			}

			// Get the field value
			fieldValue := string(reverseIter.Value())

			// Delete metadata index
			metaKey := schema.EncodeMetadataKey(namespace, field, fieldValue, id, version)
			_ = batch.Delete(metaKey, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit

			// Delete reverse index
			reverseKey := schema.EncodeReverseKey(namespace, id, field, version)
			_ = batch.Delete(reverseKey, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit
		}
		_ = reverseIter.Close() //nolint:gosec // G104: Error deliberately ignored - iterator cleanup

		// Create tombstone
		tombstoneKey := schema.EncodeTombstoneKey(namespace, id, uint64(time.Now().Unix())) //nolint:gosec // G115: Safe conversion - Unix timestamp always positive
		_ = batch.Set(tombstoneKey, []byte{}, pebble.NoSync)                                //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit
	}
	_ = iter.Close() //nolint:gosec // G104: Error deliberately ignored - iterator cleanup

	if !found {
		return ErrVectorNotFound
	}

	// Commit batch
	writeOpts := pebble.NoSync
	if s.config.SyncWrites {
		writeOpts = pebble.Sync
	}

	if err := batch.Commit(writeOpts); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	s.metrics.vectorsStored.Dec()

	return nil
}

// BatchUpsertVectors inserts or updates multiple vectors atomically.
func (s *PebbleVectorStorage) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*VectorValue) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	// Check context before proceeding
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}

	if len(vecs) == 0 {
		return ErrEmptyBatch
	}

	// Acquire write semaphore
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return ErrContextCanceled
	}

	start := time.Now()
	defer func() {
		s.metrics.batchLatency.Observe(time.Since(start).Seconds())
	}()

	batch := newVectorBatch(s.db, s.metrics)
	defer func() { _ = batch.Close() }()

	for _, vec := range vecs {
		if err := batch.UpsertVector(namespace, vec); err != nil {
			s.metrics.errors.Inc()
			return err
		}
	}

	if err := batch.Commit(); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	s.metrics.vectorsStored.Add(float64(len(vecs)))

	return nil
}

// BatchDeleteVectors deletes multiple vectors atomically.
func (s *PebbleVectorStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	// Check context before proceeding
	if err := ctx.Err(); err != nil {
		return ErrContextCanceled
	}

	if len(ids) == 0 {
		return ErrEmptyBatch
	}

	// Acquire write semaphore
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return ErrContextCanceled
	}

	start := time.Now()
	defer func() {
		s.metrics.batchLatency.Observe(time.Since(start).Seconds())
	}()

	batch := newVectorBatch(s.db, s.metrics)
	defer func() { _ = batch.Close() }()

	for _, id := range ids {
		if err := batch.DeleteVector(namespace, id); err != nil {
			s.metrics.errors.Inc()
			return err
		}
	}

	if err := batch.Commit(); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	s.metrics.vectorsStored.Sub(float64(len(ids)))

	return nil
}

// ScanByMetadata scans vectors by a single metadata field.
func (s *PebbleVectorStorage) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *ScanOptions) ([]*VectorValue, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	start := time.Now()
	defer func() {
		s.metrics.scanLatency.Observe(time.Since(start).Seconds())
	}()

	s.metrics.scanOps.Inc()

	prefix := schema.EncodeMetadataKeyPrefix(namespace, field, value)
	iter := newMetadataIterator(s.db, prefix)
	defer func() { _ = iter.Close() }()

	results := make([]*VectorValue, 0)
	limit := -1
	offset := 0

	if opts != nil {
		limit = opts.Limit
		offset = opts.Offset
	}

	count := 0
	for iter.Next() {
		if count < offset {
			count++
			continue
		}

		if limit > 0 && len(results) >= limit {
			break
		}

		schemaVec, err := iter.Value()
		if err != nil {
			continue
		}

		results = append(results, fromSchemaVectorValue(schemaVec))
		count++
	}

	if err := iter.Err(); err != nil {
		s.metrics.errors.Inc()
		return nil, err
	}

	return results, nil
}

// ScanByMultipleFilters scans vectors by multiple metadata filters.
func (s *PebbleVectorStorage) ScanByMultipleFilters(ctx context.Context, namespace string, filters []MetadataFilter, opts *ScanOptions) ([]*VectorValue, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	if len(filters) == 0 {
		return s.ScanNamespace(ctx, namespace, opts)
	}

	// For simplicity, scan by first filter and then filter in memory
	// A production implementation would use a more sophisticated approach
	firstFilter := filters[0]
	candidates, err := s.ScanByMetadata(ctx, namespace, firstFilter.Field, firstFilter.Value, nil)
	if err != nil {
		return nil, err
	}

	// Filter by remaining filters
	results := make([]*VectorValue, 0)
	for _, vec := range candidates {
		match := true
		for i := 1; i < len(filters); i++ { //nolint:gosec // G602: Slice bounds checked - iterating from 1 to len
			filter := filters[i] //nolint:gosec // G602: Slice bounds checked - iterating from 1 to len(filters)
			metaValue, exists := vec.Metadata[filter.Field]
			if !exists {
				match = false
				break
			}

			switch filter.Operator {
			case FilterEqual:
				if metaValue != filter.Value {
					match = false
				}
			case FilterNotEqual:
				if metaValue == filter.Value {
					match = false
				}
			}
		}

		if match {
			results = append(results, vec)
		}
	}

	// Apply scan options
	if opts != nil {
		if opts.Offset > 0 && opts.Offset < len(results) {
			results = results[opts.Offset:] //nolint:gosec // Bounds checked
		}
		if opts.Limit > 0 && opts.Limit < len(results) {
			results = results[:opts.Limit] //nolint:gosec // Bounds checked
		}
	}

	return results, nil
}

// ScanNamespace scans all vectors in a namespace.
func (s *PebbleVectorStorage) ScanNamespace(ctx context.Context, namespace string, opts *ScanOptions) ([]*VectorValue, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	start := time.Now()
	defer func() {
		s.metrics.scanLatency.Observe(time.Since(start).Seconds())
	}()

	s.metrics.scanOps.Inc()

	prefix := schema.EncodeNamespacePrefix(namespace)
	iter := newVectorIterator(s.db, prefix)
	defer func() { _ = iter.Close() }()

	// Use map to deduplicate by ID (keep latest version)
	vectorMap := make(map[uint64]*VectorValue)

	for iter.Next() {
		_, id, _, err := schema.DecodeVectorKey(iter.iter.Key())
		if err != nil {
			continue
		}

		schemaVec, err := iter.Value()
		if err != nil {
			continue
		}
		vec := fromSchemaVectorValue(schemaVec)

		// Keep latest version
		if existing, exists := vectorMap[id]; !exists || vec.Version > existing.Version {
			vectorMap[id] = vec
		}
	}

	if err := iter.Err(); err != nil {
		s.metrics.errors.Inc()
		return nil, err
	}

	// Convert map to slice
	results := make([]*VectorValue, 0, len(vectorMap))
	for _, vec := range vectorMap {
		results = append(results, vec)
	}

	// Apply scan options
	if opts != nil {
		if opts.Offset > 0 && opts.Offset < len(results) {
			results = results[opts.Offset:]
		}
		if opts.Limit > 0 && opts.Limit < len(results) {
			results = results[:opts.Limit]
		}
	}

	return results, nil
}

// CreateNamespace creates a new namespace.
func (s *PebbleVectorStorage) CreateNamespace(ctx context.Context, info *NamespaceInfo) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	if info == nil || info.Name == "" {
		return ErrInvalidNamespace
	}

	s.nsMu.Lock()
	defer s.nsMu.Unlock()

	// Check if namespace already exists
	if _, exists := s.nsCache[info.Name]; exists {
		return ErrNamespaceExists
	}

	// Encode namespace info
	value, err := schema.EncodeNamespaceInfo(toSchemaNamespaceInfo(info))
	if err != nil {
		return err
	}

	// Write namespace metadata
	key := schema.EncodeNamespaceMetaKey(info.Name)
	writeOpts := pebble.NoSync
	if s.config.SyncWrites {
		writeOpts = pebble.Sync
	}

	if err := s.db.Set(key, value, writeOpts); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	// Initialize ID allocator
	allocator := &atomic.Uint64{}
	allocator.Store(0)
	s.idAllocators[info.Name] = allocator

	// Initialize ID sequence
	seqKey := schema.EncodeIDSequenceKey(info.Name)
	if err := s.db.Set(seqKey, encodeUint64(0), writeOpts); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	// Update cache
	s.nsCache[info.Name] = info
	s.metrics.namespaceOps.Inc()
	s.metrics.namespacesCount.Inc()

	s.logger.Infof("Created namespace: %s", info.Name)

	return nil
}

// DeleteNamespace deletes a namespace and all its vectors.
func (s *PebbleVectorStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	s.nsMu.Lock()
	defer s.nsMu.Unlock()

	// Check if namespace exists
	if _, exists := s.nsCache[namespace]; !exists {
		return ErrNamespaceNotFound
	}

	// Delete all vectors in namespace
	prefix := schema.EncodeNamespacePrefix(namespace)
	opts := &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: schema.PrefixSuccessor(prefix),
	}

	iter, err := s.db.NewIter(opts)
	if err != nil {
		s.metrics.errors.Inc()
		return err
	}
	defer func() { _ = iter.Close() }()

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		_ = batch.Delete(iter.Key(), pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit
	}

	// Delete namespace metadata
	nsKey := schema.EncodeNamespaceMetaKey(namespace)
	_ = batch.Delete(nsKey, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit

	// Delete ID sequence
	seqKey := schema.EncodeIDSequenceKey(namespace)
	_ = batch.Delete(seqKey, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit

	// Commit batch
	writeOpts := pebble.NoSync
	if s.config.SyncWrites {
		writeOpts = pebble.Sync
	}

	if err := batch.Commit(writeOpts); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	// Update cache
	delete(s.nsCache, namespace)
	delete(s.idAllocators, namespace)
	s.metrics.namespaceOps.Inc()
	s.metrics.namespacesCount.Dec()

	s.logger.Infof("Deleted namespace: %s", namespace)

	return nil
}

// ListNamespaces lists all namespaces.
func (s *PebbleVectorStorage) ListNamespaces(ctx context.Context) ([]NamespaceInfo, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	s.nsMu.RLock()
	defer s.nsMu.RUnlock()

	results := make([]NamespaceInfo, 0, len(s.nsCache))
	for _, info := range s.nsCache {
		results = append(results, *info)
	}

	return results, nil
}

// GetNamespaceInfo gets namespace metadata.
func (s *PebbleVectorStorage) GetNamespaceInfo(ctx context.Context, namespace string) (*NamespaceInfo, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	s.nsMu.RLock()
	info, exists := s.nsCache[namespace]
	s.nsMu.RUnlock()

	if !exists {
		return nil, ErrNamespaceNotFound
	}

	// Get accurate vector count from storage
	stats, err := s.GetStats(ctx, namespace)
	if err == nil && stats != nil {
		// Create a copy to avoid modifying the cached info
		infoCopy := &NamespaceInfo{
			Name:           info.Name,
			VectorCount:    stats.Count,
			Dimension:      info.Dimension,
			CreatedAt:      info.CreatedAt,
			UpdatedAt:      info.UpdatedAt,
			DistanceMetric: info.DistanceMetric,
			IndexType:      info.IndexType,
			IndexConfig:    info.IndexConfig,
			Metadata:       info.Metadata,
			TenantID:       info.TenantID,
			OwnerUserID:    info.OwnerUserID,
			Labels:         info.Labels,
			StorageBytes:   int64(stats.SizeBytes), //nolint:gosec // G115: Safe - stats size bounded by storage capacity
		}
		return infoCopy, nil
	}

	return info, nil
}

// GetStats returns statistics for a namespace.
func (s *PebbleVectorStorage) GetStats(ctx context.Context, namespace string) (*NamespaceStats, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	// Count vectors in namespace
	prefix := schema.EncodeNamespacePrefix(namespace)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: schema.PrefixSuccessor(prefix),
	})
	if err != nil {
		s.metrics.errors.Inc()
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	count := uint64(0)
	sizeBytes := uint64(0)

	for iter.First(); iter.Valid(); iter.Next() {
		count++
		sizeBytes += uint64(len(iter.Key()) + len(iter.Value())) //nolint:gosec // G115: Safe conversion - lengths always positive
	}

	return &NamespaceStats{
		Namespace: namespace,
		Count:     count,
		SizeBytes: sizeBytes,
	}, nil
}

// NextID allocates a new unique ID for the namespace.
func (s *PebbleVectorStorage) NextID(namespace string) (uint64, error) {
	if s.closed.Load() {
		return 0, ErrStorageClosed
	}

	s.idMu.RLock()
	allocator, exists := s.idAllocators[namespace]
	s.idMu.RUnlock()

	if !exists {
		return 0, ErrNamespaceNotFound
	}

	// Atomically increment and get new ID
	newID := allocator.Add(1)

	// Persist ID sequence synchronously (NoSync is still fast)
	seqKey := schema.EncodeIDSequenceKey(namespace)
	if err := s.db.Set(seqKey, encodeUint64(newID), pebble.NoSync); err != nil {
		// Log but don't fail - ID is already allocated in memory
		s.logger.Warnf("Failed to persist ID sequence for namespace %s: %v", namespace, err)
	}

	s.metrics.idAllocations.Inc()

	return newID, nil
}

// BatchAllocateIDs allocates multiple IDs atomically.
func (s *PebbleVectorStorage) BatchAllocateIDs(namespace string, count int) ([]uint64, error) {
	if s.closed.Load() {
		return nil, ErrStorageClosed
	}

	if count <= 0 {
		return nil, ErrInvalidID
	}

	s.idMu.RLock()
	allocator, exists := s.idAllocators[namespace]
	s.idMu.RUnlock()

	if !exists {
		return nil, ErrNamespaceNotFound
	}

	// Atomically allocate batch of IDs
	startID := allocator.Add(uint64(count))
	ids := make([]uint64, count)
	for i := 0; i < count; i++ {
		ids[i] = startID - uint64(count) + uint64(i) + 1 //nolint:gosec // G115: Safe conversion - count and i bounded by slice
	}

	// Persist ID sequence synchronously (NoSync is still fast)
	seqKey := schema.EncodeIDSequenceKey(namespace)
	if err := s.db.Set(seqKey, encodeUint64(startID), pebble.NoSync); err != nil {
		// Log but don't fail - IDs are already allocated in memory
		s.logger.Warnf("Failed to persist ID sequence for namespace %s: %v", namespace, err)
	}

	s.metrics.idAllocations.Add(float64(count))

	return ids, nil
}

// Compact manually triggers compaction for a namespace.
func (s *PebbleVectorStorage) Compact(ctx context.Context, namespace string) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	prefix := schema.EncodeNamespacePrefix(namespace)
	end := schema.PrefixSuccessor(prefix)

	s.logger.Infof("Starting compaction for namespace: %s", namespace)

	if err := s.db.Compact(ctx, prefix, end, true); err != nil {
		s.metrics.errors.Inc()
		return err
	}

	s.logger.Infof("Compaction completed for namespace: %s", namespace)

	return nil
}

// CleanupTombstones removes tombstones older than the specified duration.
func (s *PebbleVectorStorage) CleanupTombstones(ctx context.Context, olderThan time.Duration) error {
	if s.closed.Load() {
		return ErrStorageClosed
	}

	cutoffTime := uint64(time.Now().Add(-olderThan).Unix()) //nolint:gosec // G115: Safe conversion - Unix timestamp always positive

	// Iterate through all namespaces
	s.nsMu.RLock()
	namespaces := make([]string, 0, len(s.nsCache))
	for ns := range s.nsCache {
		namespaces = append(namespaces, ns)
	}
	s.nsMu.RUnlock()

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	count := 0
	for _, namespace := range namespaces {
		prefix := schema.EncodeTombstonePrefix(namespace)
		iter, err := s.db.NewIter(&pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: schema.PrefixSuccessor(prefix),
		})
		if err != nil {
			continue
		}

		for iter.First(); iter.Valid(); iter.Next() {
			key := iter.Key()

			// Extract timestamp from tombstone key
			// Format: t\x00{namespace}\x00{id:uint64}\x00{timestamp:uint64}
			if len(key) >= 8 {
				timestampBytes := key[len(key)-8:]
				timestamp := uint64(timestampBytes[0]) | uint64(timestampBytes[1])<<8 |
					uint64(timestampBytes[2])<<16 | uint64(timestampBytes[3])<<24 |
					uint64(timestampBytes[4])<<32 | uint64(timestampBytes[5])<<40 |
					uint64(timestampBytes[6])<<48 | uint64(timestampBytes[7])<<56

				if timestamp < cutoffTime {
					_ = batch.Delete(key, pebble.NoSync) //nolint:gosec // G104: Error deliberately ignored - batch operation, checked on commit
					count++
				}
			}
		}
		_ = iter.Close() //nolint:gosec // G104: Error deliberately ignored - iterator cleanup
	}

	if count > 0 {
		writeOpts := pebble.NoSync
		if s.config.SyncWrites {
			writeOpts = pebble.Sync
		}

		if err := batch.Commit(writeOpts); err != nil {
			s.metrics.errors.Inc()
			return err
		}

		s.logger.Infof("Cleaned up %d tombstones", count)
	}

	return nil
}

// Close closes the storage and releases all resources.
func (s *PebbleVectorStorage) Close() error {
	if s.closed.Load() {
		return nil
	}

	s.logger.Info("Closing vector storage")

	if err := s.db.Close(); err != nil {
		return err
	}

	s.closed.Store(true)
	s.logger.Info("Vector storage closed successfully")

	return nil
}

// encodeUint64 encodes a uint64 to little-endian bytes.
func encodeUint64(v uint64) []byte {
	b := make([]byte, 8)
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	b[4] = byte(v >> 32)
	b[5] = byte(v >> 40)
	b[6] = byte(v >> 48)
	b[7] = byte(v >> 56)
	return b
}
