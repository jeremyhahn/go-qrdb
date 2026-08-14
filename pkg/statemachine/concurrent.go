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

package statemachine

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cespare/xxhash/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

const concurrentShardCount = 16

// concurrentShard is a single partition of the in-memory cache for the
// ConcurrentStateMachine, protected by a read-write mutex. A nil value
// entry serves as a tombstone for a deleted key.
type concurrentShard struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// ConcurrentOption configures a ConcurrentStateMachine at construction time.
type ConcurrentOption func(*ConcurrentStateMachine)

// WithConcurrentWatchManager attaches a WatchManager that receives
// key-change notifications for every successful write or delete.
func WithConcurrentWatchManager(wm WatchManager) ConcurrentOption {
	return func(c *ConcurrentStateMachine) {
		c.watchManager = wm
	}
}

// WithConcurrentLogger sets the structured logger for the state machine.
func WithConcurrentLogger(logger *slog.Logger) ConcurrentOption {
	return func(c *ConcurrentStateMachine) {
		c.logger = logger
	}
}

// WithConcurrentIndexManager attaches an IndexManager that handles
// secondary index create, drop, query, scan, and list operations.
func WithConcurrentIndexManager(im IndexManager) ConcurrentOption {
	return func(c *ConcurrentStateMachine) {
		c.indexManager = im
	}
}

// WithConcurrentLeaseManager attaches a LeaseManager that handles lease
// grant, revoke, and keep-alive operations. When not set, lease operations
// return ServiceNotConfiguredError.
func WithConcurrentLeaseManager(lm LeaseManager) ConcurrentOption {
	return func(c *ConcurrentStateMachine) {
		c.leaseManager = lm
	}
}

// WithConcurrentTimeSeriesStore attaches a TimeSeriesStore that handles
// time-series write, delete, retention, and query operations.
func WithConcurrentTimeSeriesStore(ts TimeSeriesStore) ConcurrentOption {
	return func(c *ConcurrentStateMachine) {
		c.tsStore = ts
	}
}

// WithConcurrentMVCCStore attaches an MVCCStore that handles MVCC put,
// delete, range, compact, and transaction operations.
func WithConcurrentMVCCStore(ms MVCCStore) ConcurrentOption {
	return func(c *ConcurrentStateMachine) {
		c.mvccStore = ms
	}
}

// ConcurrentStateMachine is a high-performance sharded in-memory cache
// backed by persistent storage. It implements sm.ConcurrentStateMachine
// from go-quicraft, allowing concurrent Lookups to proceed while Update
// is running on the apply worker goroutine.
//
// The state is distributed across 16 shards, each protected by its own
// sync.RWMutex. Writes lock only the target shard, and reads acquire a
// read lock only on the relevant shard, enabling high concurrency.
//
// PrepareSnapshot captures a consistent point-in-time deep copy of all
// shards by locking them in order, which is then serialized by
// SaveSnapshot without holding any locks.
type ConcurrentStateMachine struct {
	shardID      uint64
	replicaID    uint64
	storage      storage.Storage
	shards       [concurrentShardCount]concurrentShard
	revision     atomic.Uint64
	handlers     map[byte]updateHandler
	logger       *slog.Logger
	watchManager WatchManager
	indexManager IndexManager
	leaseManager LeaseManager
	tsStore      TimeSeriesStore
	mvccStore    MVCCStore

	// wg tracks outstanding async persistence goroutines so Close can
	// wait for them to finish.
	wg sync.WaitGroup
}

// NewConcurrentStateMachine creates a new ConcurrentStateMachine with
// the given shard/replica identity, backing storage, and optional
// configuration.
func NewConcurrentStateMachine(shardID, replicaID uint64, store storage.Storage, opts ...ConcurrentOption) *ConcurrentStateMachine {
	c := &ConcurrentStateMachine{
		shardID:   shardID,
		replicaID: replicaID,
		storage:   store,
		logger:    slog.Default(),
	}

	for i := range c.shards {
		c.shards[i].data = make(map[string][]byte)
	}

	for _, opt := range opts {
		opt(c)
	}

	// Build the map-based dispatch table for all supported operations.
	c.handlers = map[byte]updateHandler{
		// Basic KV
		kv.OpPut:         c.handlePut,
		kv.OpDelete:      c.handleDelete,
		protocol.OpBatch: c.handleBatch,

		// MVCC
		protocol.OpMVCCPut:     c.handleMVCCPut,
		protocol.OpMVCCDelete:  c.handleMVCCDelete,
		protocol.OpMVCCRange:   c.handleMVCCRange,
		protocol.OpMVCCCompact: c.handleMVCCCompact,

		// Transactions
		protocol.OpTransaction: c.handleTransaction,

		// Leases
		protocol.OpLeaseGrant:     c.handleLeaseGrant,
		protocol.OpLeaseRevoke:    c.handleLeaseRevoke,
		protocol.OpLeaseKeepAlive: c.handleLeaseKeepAlive,

		// Secondary indexes
		protocol.OpIndexCreate: c.handleIndexCreate,
		protocol.OpIndexDrop:   c.handleIndexDrop,

		// Time series
		protocol.OpTSWrite:        c.handleTSWrite,
		protocol.OpTSWriteBatch:   c.handleTSWriteBatch,
		protocol.OpTSWritePoint:   c.handleTSWritePoint,
		protocol.OpTSDeleteSeries: c.handleTSDeleteSeries,
		protocol.OpTSSetRetention: c.handleTSSetRetention,
	}

	return c
}

// NewConcurrentCreateFunc returns a sm.CreateFunc that produces a
// ConcurrentStateMachine for each shard/replica pair. The returned
// function closes over the storage factory and options.
func NewConcurrentCreateFunc(storeFactory func(shardID, replicaID uint64) storage.Storage, opts ...ConcurrentOption) sm.CreateFunc {
	return sm.NewConcurrentCreateFunc(func(shardID, replicaID uint64) sm.ConcurrentStateMachine {
		store := storeFactory(shardID, replicaID)
		return NewConcurrentStateMachine(shardID, replicaID, store, opts...)
	})
}

// ---------------------------------------------------------------------------
// sm.ConcurrentStateMachine interface
// ---------------------------------------------------------------------------

// Update applies committed log entries to the state machine. It dispatches
// each entry through the handler map based on the leading op byte.
func (c *ConcurrentStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		cmd := entries[i].Cmd
		if len(cmd) == 0 {
			results[i] = sm.Result{}
			continue
		}

		op := cmd[0]
		handler, ok := c.handlers[op]
		if !ok {
			return &InvalidOpcodeError{Op: op}
		}

		revision := c.revision.Add(1)
		result, err := handler(cmd[1:], revision)
		resolved, stop, applyErr := resolveApplyError(c.logger, entries[i].Index, result, err)
		if stop {
			return applyErr
		}
		results[i] = resolved
	}
	return nil
}

// Lookup reads state without going through Raft. It may be called
// concurrently with Update. Only the relevant shard is read-locked
// for point lookups. Supported query types:
//   - []byte: point lookup by key
//   - *PrefixQuery: prefix scan returning map[string][]byte
//   - kv.ListQuery: returns sorted []string of keys matching the prefix
func (c *ConcurrentStateMachine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	// Try index lookups.
	if result, handled, err := c.concurrentLookupIndex(query); handled {
		return result, err
	}

	// Try lease lookups.
	if result, handled, err := c.concurrentLookupLease(query); handled {
		return result, err
	}

	// Try MVCC lookups.
	if result, handled, err := c.concurrentLookupMVCC(query); handled {
		return result, err
	}

	// Try time-series lookups.
	if result, handled, err := c.concurrentLookupTimeSeries(query); handled {
		return result, err
	}

	switch q := query.(type) {
	case []byte:
		return c.lookupConcurrentKey(q)
	case *PrefixQuery:
		return c.lookupConcurrentPrefix(q)
	case kv.ListQuery:
		return c.listConcurrentKeys(q.Prefix)
	default:
		return nil, &IncompleteDataError{Reason: "unsupported query type"}
	}
}

// PrepareSnapshot captures a consistent point-in-time deep copy of all
// shard data. It locks all shards in order to prevent writes during the
// copy, then unlocks them. The returned map is passed to SaveSnapshot.
func (c *ConcurrentStateMachine) PrepareSnapshot() (interface{}, error) {
	// Lock all shards in order to get a consistent snapshot.
	for i := range c.shards {
		c.shards[i].mu.RLock()
	}

	clone := make(map[string][]byte)
	for i := range c.shards {
		for k, v := range c.shards[i].data {
			if v == nil {
				// Skip tombstones.
				continue
			}
			cp := make([]byte, len(v))
			copy(cp, v)
			clone[k] = cp
		}
	}

	for i := range c.shards {
		c.shards[i].mu.RUnlock()
	}

	return clone, nil
}

// SaveSnapshot serializes the snapshot context captured by PrepareSnapshot
// to the writer.
// Format: [entryCount:4 LE][entries...].
// Each entry: [keyLen:4 LE][key][valueLen:4 LE][value]
func (c *ConcurrentStateMachine) SaveSnapshot(_ context.Context, snapshotCtx interface{}, w io.Writer, stopper <-chan struct{}) error {
	data, ok := snapshotCtx.(map[string][]byte)
	if !ok {
		return &SnapshotError{Op: "save", Inner: &IncompleteDataError{Reason: "invalid snapshot context type"}}
	}

	// Write entry count.
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(data)))
	if _, err := w.Write(countBuf); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	buf := make([]byte, 4)
	for k, v := range data {
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}

		keyBytes := []byte(k)

		// Key length + key.
		binary.LittleEndian.PutUint32(buf, uint32(len(keyBytes)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write(keyBytes); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}

		// Value length + value.
		binary.LittleEndian.PutUint32(buf, uint32(len(v)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write(v); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
	}

	return nil
}

// RecoverFromSnapshot restores the in-memory cache from the reader,
// replacing all existing state. It waits for pending async persistence
// goroutines to complete, clears the storage, and rebuilds both cache
// and storage from the snapshot data.
func (c *ConcurrentStateMachine) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	// Wait for any in-flight async persistence to finish before clearing.
	c.wg.Wait()

	// Clear all cache shards.
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		s.data = make(map[string][]byte)
		s.mu.Unlock()
	}

	// Clear storage by iterating and deleting all existing keys.
	if err := c.clearStorage(); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
	}

	// Read entry count.
	countBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, countBuf); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
	}
	entryCount := binary.LittleEndian.Uint32(countBuf)

	lenBuf := make([]byte, 4)
	for i := uint32(0); i < entryCount; i++ {
		select {
		case <-stopper:
			return &SnapshotError{Op: "recover", Inner: context.Canceled}
		default:
		}

		// Read key.
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		keyLen := binary.LittleEndian.Uint32(lenBuf)
		keyBuf := make([]byte, keyLen)
		if _, err := io.ReadFull(r, keyBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		// Read value.
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		valLen := binary.LittleEndian.Uint32(lenBuf)
		valBuf := make([]byte, valLen)
		if _, err := io.ReadFull(r, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		key := string(keyBuf)
		s := c.concurrentShardFor(key)
		s.mu.Lock()
		s.data[key] = valBuf
		s.mu.Unlock()

		// Persist recovered entry to storage synchronously.
		if err := c.storage.Set(keyBuf, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
	}

	return nil
}

// Close releases all resources held by the state machine. It waits for
// outstanding async persistence goroutines to finish before returning.
func (c *ConcurrentStateMachine) Close(_ context.Context) error {
	c.wg.Wait()
	return nil
}

// ---------------------------------------------------------------------------
// sm.Hasher interface
// ---------------------------------------------------------------------------

// GetHash computes a deterministic xxhash64 hash of all key-value pairs
// across all shards. Keys are sorted to ensure determinism. Acquires
// read locks on all shards.
func (c *ConcurrentStateMachine) GetHash() (uint64, error) {
	h := xxhash.New()

	// Collect all keys from all shards under read locks.
	var allKeys []string
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if v != nil {
				allKeys = append(allKeys, k)
			}
		}
		s.mu.RUnlock()
	}
	sort.Strings(allKeys)

	for _, k := range allKeys {
		s := c.concurrentShardFor(k)
		s.mu.RLock()
		_, _ = h.Write([]byte(k))
		if v := s.data[k]; v != nil {
			_, _ = h.Write(v)
		}
		s.mu.RUnlock()
	}

	return h.Sum64(), nil
}

// ---------------------------------------------------------------------------
// Internal: shard selection
// ---------------------------------------------------------------------------

// concurrentShardFor returns the cache shard for the given key using
// xxhash64 with bitmask selection for the 16-shard layout.
func (c *ConcurrentStateMachine) concurrentShardFor(key string) *concurrentShard {
	h := xxhash.Sum64String(key)
	return &c.shards[h&(concurrentShardCount-1)]
}

// ---------------------------------------------------------------------------
// Internal: lookup helpers
// ---------------------------------------------------------------------------

// lookupConcurrentKey performs a point lookup. It checks the cache first
// and falls back to the backing storage on cache miss.
func (c *ConcurrentStateMachine) lookupConcurrentKey(key []byte) (interface{}, error) {
	k := string(key)
	s := c.concurrentShardFor(k)

	s.mu.RLock()
	val, found := s.data[k]
	s.mu.RUnlock()

	if found {
		if val == nil {
			return nil, nil
		}
		return val, nil
	}

	// Cache miss: fall back to storage.
	stored, err := c.storage.Get(key)
	if err != nil {
		return nil, &StorageError{Op: "get", Inner: err}
	}
	return stored, nil
}

// lookupConcurrentPrefix performs a prefix scan that merges the cache
// overlay with the backing storage. Tombstoned keys are excluded.
func (c *ConcurrentStateMachine) lookupConcurrentPrefix(pq *PrefixQuery) (interface{}, error) {
	prefix := string(pq.Prefix)
	limit := pq.Limit
	if limit <= 0 {
		limit = 10000
	}

	result := make(map[string][]byte)

	// Phase 1: scan storage via iterator.
	upperBound := prefixUpperBound(pq.Prefix)
	opts := &storage.IteratorOptions{
		LowerBound: pq.Prefix,
	}
	if upperBound != nil {
		opts.UpperBound = upperBound
	}

	iter := c.storage.NewIterator(opts)
	for iter.Seek(pq.Prefix); iter.Valid() && len(result) < limit; iter.Next() {
		k := string(iter.Key())
		if !strings.HasPrefix(k, prefix) {
			break
		}
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		result[k] = v
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, &StorageError{Op: "prefix-scan", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return nil, &StorageError{Op: "prefix-scan-close", Inner: err}
	}

	// Phase 2: overlay cache entries (cache wins over storage).
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			if v == nil {
				delete(result, k)
			} else {
				if len(result) < limit {
					result[k] = v
				}
			}
		}
		s.mu.RUnlock()
	}

	return result, nil
}

// listConcurrentKeys returns a sorted slice of keys matching the given
// prefix. When prefix is empty, all keys are returned. The result merges
// the backing storage with the cache overlay; tombstoned keys are excluded.
func (c *ConcurrentStateMachine) listConcurrentKeys(prefix string) ([]string, error) {
	seen := make(map[string]struct{})

	// Phase 1: scan storage via iterator.
	var iterOpts *storage.IteratorOptions
	if prefix != "" {
		lower := []byte(prefix)
		upper := prefixUpperBound(lower)
		iterOpts = &storage.IteratorOptions{LowerBound: lower}
		if upper != nil {
			iterOpts.UpperBound = upper
		}
	}

	iter := c.storage.NewIterator(iterOpts)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := string(iter.Key())
		if prefix == "" || strings.HasPrefix(k, prefix) {
			seen[k] = struct{}{}
		}
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, &StorageError{Op: "list-keys", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return nil, &StorageError{Op: "list-keys-close", Inner: err}
	}

	// Phase 2: overlay cache entries (cache wins over storage).
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if prefix != "" && !strings.HasPrefix(k, prefix) {
				continue
			}
			if v == nil {
				// Tombstone: remove from result.
				delete(seen, k)
			} else {
				seen[k] = struct{}{}
			}
		}
		s.mu.RUnlock()
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// ---------------------------------------------------------------------------
// Internal: write helpers
// ---------------------------------------------------------------------------

// concurrentCacheSet writes a key-value pair to the cache shard and
// asynchronously persists it to storage.
func (c *ConcurrentStateMachine) concurrentCacheSet(key string, value []byte) {
	s := c.concurrentShardFor(key)
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if err := c.storage.Set([]byte(key), value); err != nil {
			c.logger.Error("async storage set failed",
				"key", key,
				"error", err,
				"shard_id", c.shardID,
				"replica_id", c.replicaID)
		}
	}()
}

// concurrentCacheDelete sets a tombstone in the cache shard and
// asynchronously deletes the key from storage.
func (c *ConcurrentStateMachine) concurrentCacheDelete(key string) {
	s := c.concurrentShardFor(key)
	s.mu.Lock()
	s.data[key] = nil // tombstone
	s.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if err := c.storage.Delete([]byte(key)); err != nil {
			c.logger.Error("async storage delete failed",
				"key", key,
				"error", err,
				"shard_id", c.shardID,
				"replica_id", c.replicaID)
		}
	}()
}

// clearStorage removes all existing keys from the backing storage.
func (c *ConcurrentStateMachine) clearStorage() error {
	iter := c.storage.NewIterator(nil)
	var keys [][]byte
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		keys = append(keys, k)
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return err
	}
	if err := iter.Close(); err != nil {
		return err
	}

	for _, k := range keys {
		if err := c.storage.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handlers: basic KV
// ---------------------------------------------------------------------------

// handlePut decodes a KV Put command and writes the key-value pair.
// Payload format: [keyLen:2 LE][key][value]
func (c *ConcurrentStateMachine) handlePut(payload []byte, revision uint64) (sm.Result, error) {
	if len(payload) < 2 {
		return sm.Result{}, &IncompleteDataError{Reason: "put: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return sm.Result{}, &IncompleteDataError{Reason: "put: key length exceeds payload"}
	}
	key := string(payload[2 : 2+keyLen])
	value := payload[2+keyLen:]

	ownedValue := make([]byte, len(value))
	copy(ownedValue, value)

	c.concurrentCacheSet(key, ownedValue)

	if c.watchManager != nil {
		c.watchManager.Notify(key, ownedValue, revision, false)
	}

	return sm.Result{Value: revision}, nil
}

// handleDelete decodes a KV Delete command and removes the key.
// Payload format: [keyLen:2 LE][key]
func (c *ConcurrentStateMachine) handleDelete(payload []byte, revision uint64) (sm.Result, error) {
	if len(payload) < 2 {
		return sm.Result{}, &IncompleteDataError{Reason: "delete: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return sm.Result{}, &IncompleteDataError{Reason: "delete: key length exceeds payload"}
	}
	key := string(payload[2 : 2+keyLen])

	c.concurrentCacheDelete(key)

	if c.watchManager != nil {
		c.watchManager.Notify(key, nil, revision, true)
	}

	return sm.Result{Value: revision}, nil
}

// handleBatch decodes and applies a batch of operations atomically.
// Payload format: [opCount:2 LE][ops...]
func (c *ConcurrentStateMachine) handleBatch(payload []byte, revision uint64) (sm.Result, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "batch: " + err.Error()}
	}

	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case kv.OpPut:
			ownedValue := make([]byte, len(op.Value))
			copy(ownedValue, op.Value)
			c.concurrentCacheSet(key, ownedValue)
			if c.watchManager != nil {
				c.watchManager.Notify(key, ownedValue, revision, false)
			}
		case kv.OpDelete:
			c.concurrentCacheDelete(key)
			if c.watchManager != nil {
				c.watchManager.Notify(key, nil, revision, true)
			}
		default:
			return sm.Result{}, &InvalidOpcodeError{Op: op.Type}
		}
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Compile-time assertions
// ---------------------------------------------------------------------------

var (
	_ sm.ConcurrentStateMachine = (*ConcurrentStateMachine)(nil)
	_ sm.Hasher                 = (*ConcurrentStateMachine)(nil)
)
