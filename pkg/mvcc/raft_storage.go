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

// Raft-backed MVCC storage from QRDB. Provides transactional semantics
// with revision tracking, optional Raft integration for distributed consensus,
// and Prometheus metrics.

package mvcc

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// RaftNode defines the minimal Raft interface needed by MVCC for transaction commits.
// This interface allows MVCC to submit batched operations through Raft without circular dependencies.
type RaftNode interface {
	ProposeWithMode(ctx context.Context, shardID uint64, data []byte, mode writemode.Mode) (sm.Result, error)
}

// RaftStorage provides MVCC (Multi-Version Concurrency Control) storage
// with optional Raft integration for distributed consensus.
// It provides transactional semantics with revision tracking.
type RaftStorage struct {
	store     storage.Storage
	logger    *logger.Logger
	revMgr    *revisionManager
	mu        sync.RWMutex // Protects metadata operations
	metrics   *raftMVCCMetrics
	raftNode  RaftNode // Raft node for distributed transaction commits (optional)
	shardID   uint64   // Shard ID for Raft proposals (required if raftNode is set)
	compacted int64    // Atomic: revision at which last compaction occurred
}

// raftMVCCMetrics holds Prometheus metrics for MVCC operations.
type raftMVCCMetrics struct {
	putOps        *metrics.Counter
	getOps        *metrics.Counter
	deleteOps     *metrics.Counter
	rangeOps      *metrics.Counter
	txnOps        *metrics.Counter
	putLatency    *metrics.Histogram
	getLatency    *metrics.Histogram
	deleteLatency *metrics.Histogram
	rangeLatency  *metrics.Histogram
	txnLatency    *metrics.Histogram
	revision      *metrics.Gauge
	conflicts     *metrics.Counter
	compactions   *metrics.Counter
}

// RaftStorageConfig holds configuration for Raft-backed MVCC storage.
type RaftStorageConfig struct {
	Store    storage.Storage
	Logger   *logger.Logger
	RaftNode RaftNode // Optional: Enable distributed transactions through Raft
	ShardID  uint64   // Required if RaftNode is set: Shard ID for Raft proposals
}

// NewRaftStorage creates a new Raft-backed MVCC storage instance.
func NewRaftStorage(config *RaftStorageConfig) (*RaftStorage, error) {
	if config.Store == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.NewRaftStorage",
			"storage is required",
		)
	}

	if config.Logger == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.NewRaftStorage",
			"logger is required",
		)
	}

	// Initialize revision manager
	revMgr, err := newRevisionManager(config.Store)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.NewRaftStorage",
			"failed to initialize revision manager",
		)
	}

	s := &RaftStorage{
		store:    config.Store,
		logger:   config.Logger.WithField("component", "mvcc"),
		revMgr:   revMgr,
		raftNode: config.RaftNode,
		shardID:  config.ShardID,
	}

	// Load compacted revision from storage (defaults to 0 if not found).
	//
	// Absence has two spellings and both mean "fresh store, compacted stays 0":
	// storage.Reader.Get documents (nil, nil) and every shipped engine returns
	// it, while a decorator may report a typed ErrNotFound. Testing only the
	// error arm let an empty read fall through to DecodeRevision, which
	// rejects a zero-length slice — the same defect as in newRevisionManager,
	// and the reason a fresh store could not be opened with any engine.
	compactedData, err := config.Store.Get(CompactedKey())
	switch {
	case absent(compactedData, err):
		// Fresh store: compacted stays 0.
	case err != nil:
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.NewRaftStorage",
			"failed to load compacted revision",
		)
	default:
		compactedRev, decErr := DecodeRevision(compactedData)
		if decErr != nil {
			return nil, errors.Wrapf(
				decErr,
				errors.ErrStorage,
				"mvcc.NewRaftStorage",
				"failed to decode compacted revision",
			)
		}
		atomic.StoreInt64(&s.compacted, compactedRev)
	}

	s.initMetrics()

	if s.raftNode != nil {
		s.logger.Infof("MVCC storage initialized at revision %d with Raft integration (shard %d)", revMgr.getCurrent(), s.shardID)
	} else {
		s.logger.Infof("MVCC storage initialized at revision %d (local mode, no Raft)", revMgr.getCurrent())
	}

	return s, nil
}

// initMetrics initializes Prometheus metrics.
func (s *RaftStorage) initMetrics() {
	s.metrics = &raftMVCCMetrics{
		putOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "put_operations_total",
			Help:      "Total number of MVCC Put operations",
		}),
		getOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "get_operations_total",
			Help:      "Total number of MVCC Get operations",
		}),
		deleteOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "delete_operations_total",
			Help:      "Total number of MVCC Delete operations",
		}),
		rangeOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "range_operations_total",
			Help:      "Total number of MVCC Range operations",
		}),
		txnOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "transaction_operations_total",
			Help:      "Total number of MVCC transactions",
		}),
		putLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "put_duration_seconds",
			Help:      "Histogram of MVCC Put operation latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		getLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "get_duration_seconds",
			Help:      "Histogram of MVCC Get operation latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		deleteLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "delete_duration_seconds",
			Help:      "Histogram of MVCC Delete operation latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		rangeLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "range_duration_seconds",
			Help:      "Histogram of MVCC Range operation latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		txnLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "transaction_duration_seconds",
			Help:      "Histogram of MVCC transaction latencies",
			Buckets:   []float64{.001, .005, .01, .05, .1, .5, 1, 2, 5},
		}),
		revision: metrics.NewGauge(metrics.GaugeOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "revision",
			Help:      "Current MVCC revision",
		}),
		conflicts: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "conflicts_total",
			Help:      "Total number of transaction conflicts",
		}),
		compactions: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "mvcc_raft",
			Name:      "compactions_total",
			Help:      "Total number of compactions",
		}),
	}
}

// Put stores a key-value pair and returns the new KeyValue with revision metadata.
func (s *RaftStorage) Put(ctx context.Context, key, value []byte) (*RaftKeyValue, int64, error) {
	start := time.Now()
	defer func() {
		s.metrics.putLatency.Observe(time.Since(start).Seconds())
	}()
	s.metrics.putOps.Inc()

	if len(key) == 0 {
		return nil, 0, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.Put",
			"key cannot be empty",
		)
	}

	// Check context before expensive I/O
	if err := ctx.Err(); err != nil {
		return nil, 0, errors.Wrap(
			err,
			errors.ErrCanceled,
			"mvcc.Put",
		)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Get current metadata (if exists)
	var meta KeyMeta
	metaKey := EncodeMetaKey(key)
	metaData, err := s.store.Get(metaKey)
	isNew := false

	switch {
	case absent(metaData, err):
		// New key - will be created.
		isNew = true
	case err != nil:
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Put",
			"failed to read metadata",
		)
	default:
		// Existing key - decode metadata
		decoded, decErr := DecodeMeta(metaData)
		if decErr != nil {
			return nil, 0, errors.Wrapf(
				decErr,
				errors.ErrStorage,
				"mvcc.Put",
				"failed to decode metadata",
			)
		}
		meta = *decoded
	}

	// Get next revision
	rev := s.revMgr.next()
	s.metrics.revision.Set(float64(rev))

	// Update metadata
	if isNew {
		meta.CreateRevision = rev
		meta.ModRevision = rev
		meta.Version = 1
	} else {
		meta.ModRevision = rev
		meta.Version++
	}

	// Create batch for atomic write
	batch := s.store.Batch()
	defer func() { _ = batch.Close() }()

	// Write versioned data
	dataKey := EncodeDataKey(key, rev)
	if err := batch.Set(dataKey, value); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Put",
			"failed to write data",
		)
	}

	// Write metadata
	if err := batch.Set(metaKey, EncodeMeta(&meta)); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Put",
			"failed to write metadata",
		)
	}

	// Persist revision in batch
	if err := s.revMgr.persistInBatch(batch); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Put",
			"failed to persist revision",
		)
	}

	// Commit atomically
	if err := batch.Commit(); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Put",
			"failed to commit batch",
		)
	}

	// Build result
	kv := &RaftKeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
	}

	return kv, rev, nil
}

// Get retrieves a key-value pair at a specific revision.
// If revision is 0, returns the latest version.
func (s *RaftStorage) Get(ctx context.Context, key []byte, revision int64) (*RaftKeyValue, error) {
	start := time.Now()
	defer func() {
		s.metrics.getLatency.Observe(time.Since(start).Seconds())
	}()
	s.metrics.getOps.Inc()

	if len(key) == 0 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.Get",
			"key cannot be empty",
		)
	}

	// Check context
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(
			err,
			errors.ErrCanceled,
			"mvcc.Get",
		)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getWithoutLock(ctx, key, revision)
}

// getWithoutLock retrieves a key-value pair without acquiring locks (internal use).
// Caller must hold at least s.mu.RLock().
func (s *RaftStorage) getWithoutLock(ctx context.Context, key []byte, revision int64) (*RaftKeyValue, error) {
	// Get metadata
	metaKey := EncodeMetaKey(key)
	metaData, err := s.store.Get(metaKey)
	if absent(metaData, err) {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"mvcc.Get",
			"key not found",
		)
	}
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Get",
			"failed to read metadata",
		)
	}

	meta, decErr := DecodeMeta(metaData)
	if decErr != nil {
		return nil, errors.Wrapf(
			decErr,
			errors.ErrStorage,
			"mvcc.Get",
			"failed to decode metadata",
		)
	}

	// Determine which revision to read
	targetRev := revision
	if targetRev == 0 {
		targetRev = meta.ModRevision
	}

	// Validate revision
	if targetRev < meta.CreateRevision {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"mvcc.Get",
			"revision before creation",
		)
	}

	// Read versioned data
	dataKey := EncodeDataKey(key, meta.ModRevision)
	value, err := s.store.Get(dataKey)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Get",
			"failed to read data",
		)
	}

	kv := &RaftKeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
	}

	return kv, nil
}

// Delete removes a key and returns the previous KeyValue.
func (s *RaftStorage) Delete(ctx context.Context, key []byte) (*RaftKeyValue, int64, error) {
	start := time.Now()
	defer func() {
		s.metrics.deleteLatency.Observe(time.Since(start).Seconds())
	}()
	s.metrics.deleteOps.Inc()

	if len(key) == 0 {
		return nil, 0, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.Delete",
			"key cannot be empty",
		)
	}

	// Check context
	if err := ctx.Err(); err != nil {
		return nil, 0, errors.Wrap(
			err,
			errors.ErrCanceled,
			"mvcc.Delete",
		)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Get current value before delete (without acquiring lock again)
	prevKV, err := s.getWithoutLock(ctx, key, 0)
	if err != nil {
		if errors.IsNotFound(err) {
			// Key doesn't exist - no-op
			return nil, s.revMgr.getCurrent(), nil
		}
		return nil, 0, err
	}

	// Get next revision
	rev := s.revMgr.next()
	s.metrics.revision.Set(float64(rev))

	// Create batch for atomic delete
	batch := s.store.Batch()
	defer func() { _ = batch.Close() }()

	// Delete metadata
	metaKey := EncodeMetaKey(key)
	if err := batch.Delete(metaKey); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Delete",
			"failed to delete metadata",
		)
	}

	// Delete data (current version)
	dataKey := EncodeDataKey(key, prevKV.ModRevision)
	if err := batch.Delete(dataKey); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Delete",
			"failed to delete data",
		)
	}

	// Persist revision
	if err := s.revMgr.persistInBatch(batch); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Delete",
			"failed to persist revision",
		)
	}

	// Commit atomically
	if err := batch.Commit(); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Delete",
			"failed to commit batch",
		)
	}

	return prevKV, rev, nil
}

// Range returns all key-value pairs in the range [start, end) at a specific revision.
// If end is nil, returns only the key matching start.
// If revision is 0, uses the current revision.
// If limit is 0, returns all matching keys.
func (s *RaftStorage) Range(ctx context.Context, start, end []byte, revision int64, limit int64) ([]*RaftKeyValue, error) {
	startTime := time.Now()
	defer func() {
		s.metrics.rangeLatency.Observe(time.Since(startTime).Seconds())
	}()
	s.metrics.rangeOps.Inc()

	if len(start) == 0 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.Range",
			"start key cannot be empty",
		)
	}

	// Check context
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(
			err,
			errors.ErrCanceled,
			"mvcc.Range",
		)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine target revision
	targetRev := revision
	if targetRev == 0 {
		targetRev = s.revMgr.getCurrent()
	}

	// If no end key, just get single key
	if end == nil {
		kv, err := s.getWithoutLock(ctx, start, targetRev)
		if err != nil {
			if errors.IsNotFound(err) {
				return []*RaftKeyValue{}, nil
			}
			return nil, err
		}
		return []*RaftKeyValue{kv}, nil
	}

	// Range query - iterate over metadata keys
	var allMatches []*RaftKeyValue

	// Create iterator that scans all metadata keys
	opts := &storage.IteratorOptions{
		LowerBound: MetaLowerBound(),
		UpperBound: MetaUpperBound(),
	}

	iter := s.store.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	// Seeks once per key length rather than walking the namespace; see
	// ForEachMetaKeyInRange.
	var readErr error
	scanErr := ForEachMetaKeyInRange(iter, start, end, func(userKey []byte) bool {
		// Get the key-value at target revision (without acquiring lock again)
		kv, err := s.getWithoutLock(ctx, userKey, targetRev)
		if err != nil {
			if errors.IsNotFound(err) {
				return true // Skip keys that don't exist at this revision
			}
			readErr = err
			return false
		}
		allMatches = append(allMatches, kv)
		return true
	})
	if readErr != nil {
		return nil, readErr
	}

	// Sort results by key (lexicographic order)
	sort.Slice(allMatches, func(i, j int) bool {
		return bytes.Compare(allMatches[i].Key, allMatches[j].Key) < 0
	})

	// Apply limit
	results := allMatches
	if limit > 0 && int64(len(allMatches)) > limit {
		results = allMatches[:limit]
	}

	if err := scanErr; err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Range",
			"iterator error",
		)
	}

	return results, nil
}

// RaftCurrentRevision returns the current global revision.
func (s *RaftStorage) RaftCurrentRevision() int64 {
	return s.revMgr.getCurrent()
}

// PutAtRevision stores a key-value pair at an externally-assigned revision.
// This is the Apply-path method used when Raft log index determines the revision.
// Unlike Put, it does NOT call revMgr.next(). Instead it uses the provided revision
// and calls revMgr.setIfGreater() to track the high-water mark.
func (s *RaftStorage) PutAtRevision(ctx context.Context, key, value []byte, leaseID, revision int64) (*RaftKeyValue, error) {
	start := time.Now()
	defer func() {
		s.metrics.putLatency.Observe(time.Since(start).Seconds())
	}()
	s.metrics.putOps.Inc()

	if len(key) == 0 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.PutAtRevision",
			"key cannot be empty",
		)
	}

	// Check context before expensive I/O
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(
			err,
			errors.ErrCanceled,
			"mvcc.PutAtRevision",
		)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Get current metadata (if exists)
	var meta KeyMeta
	metaKey := EncodeMetaKey(key)
	metaData, err := s.store.Get(metaKey)
	isNew := false

	switch {
	case absent(metaData, err):
		// New key - will be created.
		isNew = true
	case err != nil:
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.PutAtRevision",
			"failed to read metadata",
		)
	default:
		// Existing key - decode metadata
		decoded, decErr := DecodeMeta(metaData)
		if decErr != nil {
			return nil, errors.Wrapf(
				decErr,
				errors.ErrStorage,
				"mvcc.PutAtRevision",
				"failed to decode metadata",
			)
		}
		meta = *decoded
	}

	// Use the externally-assigned revision and update high-water mark
	s.revMgr.setIfGreater(revision)
	s.metrics.revision.Set(float64(revision))

	// Update metadata
	if isNew {
		meta.CreateRevision = revision
		meta.ModRevision = revision
		meta.Version = 1
	} else {
		meta.ModRevision = revision
		meta.Version++
	}
	meta.LeaseID = leaseID

	// Create batch for atomic write
	batch := s.store.Batch()
	defer func() { _ = batch.Close() }()

	// Write versioned data
	dataKey := EncodeDataKey(key, revision)
	if err := batch.Set(dataKey, value); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.PutAtRevision",
			"failed to write data",
		)
	}

	// Write metadata
	if err := batch.Set(metaKey, EncodeMeta(&meta)); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.PutAtRevision",
			"failed to write metadata",
		)
	}

	// Persist revision in batch
	if err := s.revMgr.persistInBatch(batch); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.PutAtRevision",
			"failed to persist revision",
		)
	}

	// Commit atomically
	if err := batch.Commit(); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.PutAtRevision",
			"failed to commit batch",
		)
	}

	// Build result
	kv := &RaftKeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
	}

	return kv, nil
}

// DeleteAtRevision removes a key at an externally-assigned revision.
// This is the Apply-path method used when Raft log index determines the revision.
// Unlike Delete, it does NOT call revMgr.next(). Instead it uses the provided revision
// and calls revMgr.setIfGreater() to update the high-water mark.
func (s *RaftStorage) DeleteAtRevision(ctx context.Context, key []byte, revision int64) (*RaftKeyValue, error) {
	start := time.Now()
	defer func() {
		s.metrics.deleteLatency.Observe(time.Since(start).Seconds())
	}()
	s.metrics.deleteOps.Inc()

	if len(key) == 0 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"mvcc.DeleteAtRevision",
			"key cannot be empty",
		)
	}

	// Check context before expensive I/O
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(
			err,
			errors.ErrCanceled,
			"mvcc.DeleteAtRevision",
		)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Get current value before delete (without acquiring lock again)
	prevKV, err := s.getWithoutLock(ctx, key, 0)
	if err != nil {
		if errors.IsNotFound(err) {
			// Key doesn't exist - update high-water mark and return no-op
			s.revMgr.setIfGreater(revision)
			s.metrics.revision.Set(float64(revision))
			return nil, nil
		}
		return nil, err
	}

	// Use the externally-assigned revision and update high-water mark
	s.revMgr.setIfGreater(revision)
	s.metrics.revision.Set(float64(revision))

	// Create batch for atomic delete
	batch := s.store.Batch()
	defer func() { _ = batch.Close() }()

	// Delete metadata
	metaKey := EncodeMetaKey(key)
	if err := batch.Delete(metaKey); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.DeleteAtRevision",
			"failed to delete metadata",
		)
	}

	// Delete data (current version)
	dataKey := EncodeDataKey(key, prevKV.ModRevision)
	if err := batch.Delete(dataKey); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.DeleteAtRevision",
			"failed to delete data",
		)
	}

	// Persist revision
	if err := s.revMgr.persistInBatch(batch); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.DeleteAtRevision",
			"failed to persist revision",
		)
	}

	// Commit atomically
	if err := batch.Commit(); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.DeleteAtRevision",
			"failed to commit batch",
		)
	}

	return prevKV, nil
}

// SetRevision forcibly sets the current revision. This is used during
// snapshot recovery to restore the revision counter.
// Uses atomic store (single-writer assumption in Raft apply path).
func (s *RaftStorage) SetRevision(revision int64) {
	atomic.StoreInt64(&s.revMgr.current, revision)
	s.revMgr.dirty.Store(true)
	s.metrics.revision.Set(float64(revision))
}

// CompactedRevision returns the revision at which the last compaction occurred.
// Returns 0 if no compaction has been performed.
func (s *RaftStorage) CompactedRevision() int64 {
	return atomic.LoadInt64(&s.compacted)
}

// Compact removes old data revisions at or below the given revision for each
// key while preserving the latest (highest) revision per unique key. This
// ensures that keys whose only write is within the compaction range (e.g.,
// system keys written during bootstrap) remain readable after compaction.
//
// Data keys are sorted by [prefix][keyLen][key][complement(revision)], so
// within each user key group the highest revision appears first in iteration
// order. The method tracks the current key prefix and skips the first entry
// per group (the latest revision), deleting only subsequent older entries
// that fall at or below the compaction revision.
//
// This is the Apply-path compaction used by the state machine.
func (s *RaftStorage) Compact(revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Iterate all data keys with prefix prefixData
	opts := &storage.IteratorOptions{
		LowerBound: []byte{prefixData},
		UpperBound: []byte{prefixData + 1},
	}

	iter := s.store.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	batch := s.store.Batch()
	defer func() { _ = batch.Close() }()

	// Track the current user key prefix to detect group boundaries.
	// Data key format: [0x01][keyLen:8 BE][key][complement:8 BE]
	// The prefix for grouping is everything except the last 8 bytes.
	var currentPrefix []byte

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		raw := iter.Key()
		if len(raw) < 17 { // 1 + 8 + 0 + 8 minimum
			continue
		}

		_, keyRev, err := DecodeDataKey(raw)
		if err != nil {
			continue // Skip invalid keys
		}

		// Extract the key prefix (everything except the trailing 8-byte complement).
		keyPrefix := raw[:len(raw)-8]

		// Detect new key group: skip the first entry (latest revision) in each group.
		if !bytes.Equal(keyPrefix, currentPrefix) {
			currentPrefix = make([]byte, len(keyPrefix))
			copy(currentPrefix, keyPrefix)
			// This is the latest revision for this key. Preserve it.
			continue
		}

		// Subsequent (older) entry in the same key group.
		// Delete if at or below the compaction point.
		if keyRev <= revision {
			keyCopy := make([]byte, len(raw))
			copy(keyCopy, raw)
			if err := batch.Delete(keyCopy); err != nil {
				return errors.Wrapf(
					err,
					errors.ErrStorage,
					"mvcc.Compact",
					"failed to delete old revision",
				)
			}
		}
	}

	if err := iter.Error(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Compact",
			"iterator error",
		)
	}

	// Persist compacted marker in the same batch
	if err := batch.Set(CompactedKey(), EncodeRevision(revision)); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Compact",
			"failed to persist compacted revision",
		)
	}

	// Commit atomically
	if err := batch.Commit(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Compact",
			"failed to commit compaction batch",
		)
	}

	// Update in-memory state
	atomic.StoreInt64(&s.compacted, revision)
	s.metrics.compactions.Inc()

	return nil
}

// BeginRaftTxn creates a new Raft-backed transaction.
func (s *RaftStorage) BeginRaftTxn(ctx context.Context) (RaftTransaction, error) {
	s.metrics.txnOps.Inc()
	return newRaftTransaction(ctx, s)
}

// Close closes the MVCC storage and persists the current revision.
func (s *RaftStorage) Close() error {
	s.logger.Info("Closing MVCC storage")

	// Persist final revision
	if err := s.revMgr.persist(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Close",
			"failed to persist revision",
		)
	}

	// Close underlying storage
	if err := s.store.Close(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"mvcc.Close",
			"failed to close storage",
		)
	}

	s.logger.Info("MVCC storage closed successfully")
	return nil
}
