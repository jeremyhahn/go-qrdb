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

const shardCount = 256

// shard is a single partition of the in-memory cache, protected by a
// read-write mutex. A nil value entry serves as a tombstone for a
// deleted key (the key exists in the map but its value is nil).
type shard struct {
	mu   sync.RWMutex
	data map[string][]byte

	// order keeps this shard's asynchronous storage writes in apply order.
	// See persistOrder.
	order persistOrder

	// claims maps a key with an IN-FLIGHT guarded write to that write's
	// persistence ticket, so a claim whose storage write fails can tell
	// whether the cache entry it would restore is still its own.
	//
	// It is not a cache of every key: an entry exists only between the guard
	// admitting a claim and that claim's storage write settling, and the
	// shard lock is released in between. Any later write to the key removes
	// the entry, which is what makes the test exact — see withdrawClaim.
	claims map[string]uint64
}

// updateHandler is the function signature for write operation handlers.
// The payload is the entry command data after the leading op byte.
// The revision is the monotonically increasing MVCC counter.
type updateHandler func(payload []byte, revision uint64) (sm.Result, error)

// Option configures a FastStateMachine at construction time.
type Option func(*FastStateMachine)

// WithWatchManager attaches a WatchManager that receives key-change
// notifications for every successful write or delete.
func WithWatchManager(wm WatchManager) Option {
	return func(f *FastStateMachine) {
		f.watchManager = wm
	}
}

// WithLogger sets the structured logger for the state machine.
func WithLogger(logger *slog.Logger) Option {
	return func(f *FastStateMachine) {
		f.logger = logger
	}
}

// WithIndexManager attaches an IndexManager that handles secondary index
// create, drop, query, scan, and list operations.
func WithIndexManager(im IndexManager) Option {
	return func(f *FastStateMachine) {
		f.indexManager = im
	}
}

// WithLeaseManager attaches a LeaseManager that handles lease grant,
// revoke, and keep-alive operations. When not set, lease operations
// return ServiceNotConfiguredError.
func WithLeaseManager(lm LeaseManager) Option {
	return func(f *FastStateMachine) {
		f.leaseManager = lm
	}
}

// WithTimeSeriesStore attaches a TimeSeriesStore that handles time-series
// write, delete, retention, and query operations. When not set, time-series
// operations return ServiceNotConfiguredError.
func WithTimeSeriesStore(ts TimeSeriesStore) Option {
	return func(f *FastStateMachine) {
		f.tsStore = ts
	}
}

// WithMVCCStore attaches an MVCCStore that handles MVCC put, delete,
// range, compact, and transaction operations. When not set, MVCC
// operations return ServiceNotConfiguredError.
func WithMVCCStore(ms MVCCStore) Option {
	return func(f *FastStateMachine) {
		f.mvccStore = ms
	}
}

// FastStateMachine is a high-performance sharded in-memory cache backed
// by persistent storage. Writes land in the cache first (for fast reads)
// and are flushed asynchronously to the underlying storage.Storage.
//
// It implements sm.StateMachine from go-quicraft so it can be plugged
// directly into a QuicRaft node.
type FastStateMachine struct {
	shardID      uint64
	replicaID    uint64
	storage      storage.Storage
	shards       [shardCount]shard
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

// NewFastStateMachine creates a new FastStateMachine with the given
// shard/replica identity, backing storage, and optional configuration.
func NewFastStateMachine(shardID, replicaID uint64, store storage.Storage, opts ...Option) *FastStateMachine {
	f := &FastStateMachine{
		shardID:   shardID,
		replicaID: replicaID,
		storage:   store,
		logger:    slog.Default(),
	}

	for i := range f.shards {
		f.shards[i].data = make(map[string][]byte)
		f.shards[i].claims = make(map[string]uint64)
		f.shards[i].order.init()
	}

	for _, opt := range opts {
		opt(f)
	}

	// Build the map-based dispatch table for all supported operations.
	f.handlers = map[byte]updateHandler{
		// Basic KV
		kv.OpPut:         f.handlePut,
		kv.OpPutIfAbsent: f.handlePutIfAbsent,
		kv.OpDelete:      f.handleDelete,
		protocol.OpBatch: f.handleBatch,

		// Guarded single-key verbs. They complete the set kv.OpPutIfAbsent
		// opens: a guarded create with no guarded release or edit is a
		// primitive an unguarded Delete or Put can un-decide (go-qrdb#41,
		// #56). pkg/store and both pkg/engine engines have dispatched them
		// since they were added; these four maps did not, so every sm_type
		// served by this package refused them with "unknown opcode: 66"
		// (go-qrdb#175).
		protocol.OpCompareAndDelete: f.handleCompareAndDelete,
		protocol.OpCompareAndSwap:   f.handleCompareAndSwap,

		// A write wrapped with a guard evaluated at this entry's log
		// position -- the drop fence rides on it. See guarded_write.go.
		protocol.OpGuarded: f.handleGuarded,

		// A replicated delete of every key in [start, end), gated by the
		// range-delete feature. See delete_range.go.
		protocol.OpDeleteRange: f.handleDeleteRange,

		// MVCC
		protocol.OpMVCCPut:    f.handleMVCCPut,
		protocol.OpMVCCDelete: f.handleMVCCDelete,
		protocol.OpMVCCRange:  f.handleMVCCRange,

		// Transactions
		protocol.OpTransaction: f.handleTransaction,

		// Leases
		protocol.OpLeaseGrant:     f.handleLeaseGrant,
		protocol.OpLeaseRevoke:    f.handleLeaseRevoke,
		protocol.OpLeaseKeepAlive: f.handleLeaseKeepAlive,

		// Secondary indexes
		protocol.OpIndexCreate: f.handleIndexCreate,
		protocol.OpIndexDrop:   f.handleIndexDrop,

		// Time series
		protocol.OpTSWrite:        f.handleTSWrite,
		protocol.OpTSWriteBatch:   f.handleTSWriteBatch,
		protocol.OpTSWritePoint:   f.handleTSWritePoint,
		protocol.OpTSDeleteSeries: f.handleTSDeleteSeries,
		protocol.OpTSSetRetention: f.handleTSSetRetention,
	}

	return f
}

// ---------------------------------------------------------------------------
// sm.StateMachine interface
// ---------------------------------------------------------------------------

// Update applies committed log entries to the state machine.
func (f *FastStateMachine) Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		cmd := entries[i].Cmd
		if len(cmd) == 0 {
			results[i] = sm.Result{}
			continue
		}

		op := cmd[0]
		var result sm.Result
		var err error
		if handler, ok := f.handlers[op]; ok {
			revision := f.revision.Add(1)
			result, err = handler(cmd[1:], revision)
		} else {
			err = &InvalidOpcodeError{Op: op}
		}
		// #nosec G602 -- i ranges over entries.
		resolved, stop, applyErr := ResolveApplyError(f.logger, entries[i].Index, result, err)
		if stop {
			return applyErr
		}
		// #nosec G602 -- the sm.StateMachine Update contract pre-allocates results with len(results) == len(entries), and i ranges over entries.
		results[i] = resolved
	}
	return nil
}

// Lookup reads state without going through Raft. Supported query types:
//   - []byte: point lookup by key (returns []byte value or nil)
//   - *PrefixQuery: prefix scan returning map[string][]byte
//   - kv.ListQuery: returns sorted []string of keys matching the prefix
//   - index.IndexQueryRequest: exact-match index query returning []uint64
//   - index.IndexScanRequest: range index scan returning map[string][]uint64
//   - index.IndexListRequest: list all indexes returning []interface{}
//   - lease.LeaseTimeToLiveQuery: lease TTL lookup returning *lease.Lease
//   - lease.LeasesQuery: list all leases returning []*lease.Lease
//   - service.MVCCGetQuery: MVCC point lookup
//   - service.MVCCRangeQuery: MVCC range query
//   - service.MVCCRevisionQuery: current MVCC revision
//   - service.MVCCScanChangesQuery: keys changed in a revision window
//   - timeseries.TSQueryRequest: time-series range query
//   - timeseries.TSAggregateRequest: time-series aggregation
//   - timeseries.TSRichQueryRequest: rich time-series query
//   - timeseries.TSGetRetentionRequest: retention policy lookup
//   - timeseries.TSListMetricsRequest: list metrics by prefix
func (f *FastStateMachine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	// Try index lookups.
	if result, handled, err := f.lookupIndex(query); handled {
		return result, err
	}

	// Try lease lookups.
	if result, handled, err := f.lookupLease(query); handled {
		return result, err
	}

	// Try MVCC lookups.
	if result, handled, err := f.lookupMVCC(query); handled {
		return result, err
	}

	// Try time-series lookups.
	if result, handled, err := f.lookupTimeSeries(query); handled {
		return result, err
	}

	switch q := query.(type) {
	case []byte:
		return f.lookupKey(q)
	case *PrefixQuery:
		return f.lookupPrefix(q)
	case kv.ListQuery:
		return f.listKeys(q.Prefix)
	default:
		return nil, &IncompleteDataError{Reason: "unsupported query type"}
	}
}

// SaveSnapshot serializes the in-memory cache to the writer.
// Format: [entryCount:4 LE][entries...].
// Each entry: [keyLen:4 LE][key][valueLen:4 LE][value].
func (f *FastStateMachine) SaveSnapshot(_ context.Context, w io.Writer, stopper <-chan struct{}) error {
	// Collect all non-tombstoned entries from cache.
	type entry struct {
		key   string
		value []byte
	}
	var entries []entry

	for i := range f.shards {
		s := &f.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if v != nil {
				entries = append(entries, entry{key: k, value: v})
			}
		}
		s.mu.RUnlock()

		// Check cancellation between shards.
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}
	}

	// Write entry count.
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(entries)))
	if _, err := w.Write(countBuf); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	// Write each entry.
	buf := make([]byte, 4)
	for _, e := range entries {
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}

		// Key length + key.
		binary.LittleEndian.PutUint32(buf, uint32(len(e.key)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write([]byte(e.key)); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}

		// Value length + value.
		binary.LittleEndian.PutUint32(buf, uint32(len(e.value)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write(e.value); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
	}

	return nil
}

// RecoverFromSnapshot restores the in-memory cache from the reader,
// replacing all existing state. It waits for pending async persistence
// goroutines to complete, clears the storage, and rebuilds both cache
// and storage from the snapshot data.
func (f *FastStateMachine) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	// Wait for any in-flight async persistence to finish before clearing.
	f.wg.Wait()

	// Clear all cache shards.
	for i := range f.shards {
		s := &f.shards[i]
		s.mu.Lock()
		s.data = make(map[string][]byte)
		s.mu.Unlock()
	}

	// Clear storage by iterating and deleting all existing keys.
	if err := f.clearStorage(); err != nil {
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
		s := f.shardFor(key)
		s.mu.Lock()
		s.data[key] = valBuf
		s.mu.Unlock()

		// Persist recovered entry to storage synchronously.
		if err := f.storage.Set(keyBuf, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
	}

	return nil
}

// clearStorage removes all existing keys from the backing storage.
func (f *FastStateMachine) clearStorage() error {
	iter := f.storage.NewIterator(nil)
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
		if err := f.storage.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// Close releases all resources held by the state machine. It waits for
// outstanding async persistence goroutines to finish before returning.
func (f *FastStateMachine) Close(_ context.Context) error {
	f.wg.Wait()
	return nil
}

// ---------------------------------------------------------------------------
// Internal: shard selection
// ---------------------------------------------------------------------------

// shardFor returns the cache shard for the given key using xxhash64.
func (f *FastStateMachine) shardFor(key string) *shard {
	h := xxhash.Sum64String(key)
	return &f.shards[h%shardCount]
}

// ---------------------------------------------------------------------------
// Internal: lookup helpers
// ---------------------------------------------------------------------------

// lookupKey performs a point lookup. It checks the cache first and falls
// back to the backing storage on cache miss.
func (f *FastStateMachine) lookupKey(key []byte) (interface{}, error) {
	k := string(key)
	s := f.shardFor(k)

	s.mu.RLock()
	val, found := s.data[k]
	s.mu.RUnlock()

	if found {
		if val == nil {
			// Tombstone: key was deleted. Return untyped nil so that
			// callers can check result == nil without hitting the Go
			// interface-nil trap (typed nil []byte != nil interface).
			return nil, nil
		}
		return val, nil
	}

	// Cache miss: fall back to storage.
	stored, err := f.storage.Get(key)
	if err != nil {
		return nil, &StorageError{Op: "get", Inner: err}
	}
	return stored, nil
}

// lookupPrefix performs a prefix scan that merges the cache overlay with
// the backing storage. Tombstoned keys are excluded from the result.
// When pq.Limit > 0, the scan stops after collecting that many entries.
func (f *FastStateMachine) lookupPrefix(pq *PrefixQuery) (interface{}, error) {
	prefix := string(pq.Prefix)
	limit := pq.Limit
	if limit <= 0 {
		limit = 10000 // Default limit to prevent unbounded scans
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

	iter := f.storage.NewIterator(opts)
	for iter.Seek(pq.Prefix); iter.Valid() && len(result) < limit; iter.Next() {
		k := string(iter.Key())
		if !strings.HasPrefix(k, prefix) {
			break
		}
		// Copy value since iterator buffers are reused.
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
	for i := range f.shards {
		s := &f.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			if v == nil {
				// Tombstone: remove from result.
				delete(result, k)
			} else if len(result) < limit {
				result[k] = v
			}
		}
		s.mu.RUnlock()
	}

	return result, nil
}

// prefixUpperBound computes the exclusive upper bound for a prefix scan.
// It increments the last byte of the prefix to create the boundary.
// Returns nil if the prefix is all 0xFF bytes (unbounded scan).
func prefixUpperBound(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		if upper[i] < 0xFF {
			upper[i]++
			return upper[:i+1]
		}
	}
	return nil
}

// listKeys returns a sorted slice of keys matching the given prefix.
// When prefix is empty, all keys are returned. The result merges the
// backing storage with the cache overlay; tombstoned keys are excluded.
func (f *FastStateMachine) listKeys(prefix string) ([]string, error) {
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

	iter := f.storage.NewIterator(iterOpts)
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
	for i := range f.shards {
		s := &f.shards[i]
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

// cacheSet writes a key-value pair to the cache shard and asynchronously
// persists it to storage.
func (f *FastStateMachine) cacheSet(key string, value []byte) {
	s := f.shardFor(key)
	s.mu.Lock()
	s.data[key] = value
	ticket := s.order.ticket()
	// A later write to this key retires any in-flight guarded claim on it: the
	// claim's rollback must not restore a value this write has superseded
	// (go-qrdb#94, Codex review on PR #110). Done under the same lock as the
	// cache write, so the retirement and the write are one step.
	delete(s.claims, key)
	s.mu.Unlock()

	f.persistAsync(s, ticket, key, value)
}

// persistAsync writes the pair to storage in the background. It is the single
// asynchronous write path, shared by cacheSet and by the guarded
// handlePutIfAbsent, so a write authorised by the guard reaches storage the
// same way an unconditional one does.
func (f *FastStateMachine) persistAsync(s *shard, ticket uint64, key string, value []byte) {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		s.order.run(ticket, func() {
			f.persistSet(key, value)
		})
	}()
}

// persistClaim is the guarded verb's synchronous write. The decision lives in
// persistClaimLocked, shared with the other sharded state machine; this adds
// only the log line, which needs this replica's identity. op names the verb in
// the StorageError so an operator reading a withdrawal knows which guard failed.
func (f *FastStateMachine) persistClaim(s *shard, claimed claim, key string, value []byte, op string) error {
	err := persistClaimLocked(&s.mu, s.data, s.claims, &s.order, f.storage.Set, op, claimed, key, value)
	if err != nil {
		f.logger.Error("guarded storage set failed; claim withdrawn",
			"key", key,
			"error", err,
			"shard_id", f.shardID,
			"replica_id", f.replicaID)
	}
	return err
}

// persistClaimDelete is the guarded DELETE's synchronous storage removal.
//
// It is the delete twin of persistClaim and not a flag on it, because the two
// differ in exactly one thing — Delete rather than Set — and every other
// property (waiting for this shard's earlier writes through the same ticket,
// withdrawing the cache tombstone when the removal does not reach disk) must
// stay identical. The value passed through is nil and unused: storage.Delete
// names only a key.
func (f *FastStateMachine) persistClaimDelete(s *shard, claimed claim, key string) error {
	err := persistClaimLocked(&s.mu, s.data, s.claims, &s.order,
		func(k, _ []byte) error { return f.storage.Delete(k) },
		"compare_and_delete_delete", claimed, key, nil)
	if err != nil {
		f.logger.Error("guarded storage delete failed; claim withdrawn",
			"key", key,
			"error", err,
			"shard_id", f.shardID,
			"replica_id", f.replicaID)
	}
	return err
}

// persistSet is the storage write itself, run in order by persistAsync.
func (f *FastStateMachine) persistSet(key string, value []byte) {
	if err := f.storage.Set([]byte(key), value); err != nil {
		f.logger.Error("async storage set failed",
			"key", key,
			"error", err,
			"shard_id", f.shardID,
			"replica_id", f.replicaID)
	}
}

// cacheDelete sets a tombstone in the cache shard and asynchronously
// deletes the key from storage. The tombstone persists in the cache
// until snapshot recovery clears it. This avoids a race condition where
// a concurrent prefix scan could read stale data from the storage
// iterator after the tombstone is prematurely cleaned.
func (f *FastStateMachine) cacheDelete(key string) {
	s := f.shardFor(key)
	s.mu.Lock()
	s.data[key] = nil // tombstone
	ticket := s.order.ticket()
	// A later write to this key retires any in-flight guarded claim on it: the
	// claim's rollback must not restore a value this write has superseded
	// (go-qrdb#94, Codex review on PR #110). Done under the same lock as the
	// cache write, so the retirement and the write are one step.
	delete(s.claims, key)
	s.mu.Unlock()

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		s.order.run(ticket, func() {
			if err := f.storage.Delete([]byte(key)); err != nil {
				f.logger.Error("async storage delete failed",
					"key", key,
					"error", err,
					"shard_id", f.shardID,
					"replica_id", f.replicaID)
			}
		})
	}()
}

// ---------------------------------------------------------------------------
// Handlers: basic KV (fully implemented)
// ---------------------------------------------------------------------------

// handlePut decodes a KV Put command and writes the key-value pair.
// Payload format: [keyLen:2 LE][key][value].
func (f *FastStateMachine) handlePut(payload []byte, revision uint64) (sm.Result, error) {
	keyBytes, value, err := decodeKeyValuePayload(payload, "put")
	if err != nil {
		return sm.Result{}, err
	}
	key := string(keyBytes)

	// Make an owned copy of the value so it survives after the entry
	// buffer is recycled.
	ownedValue := make([]byte, len(value))
	copy(ownedValue, value)

	f.cacheSet(key, ownedValue)

	if f.watchManager != nil {
		f.watchManager.Notify(key, ownedValue, revision, false)
	}

	return sm.Result{Value: revision}, nil
}

// handleDelete decodes a KV Delete command and removes the key.
// Payload format: [keyLen:2 LE][key].
func (f *FastStateMachine) handleDelete(payload []byte, revision uint64) (sm.Result, error) {
	keyBytes, _, err := decodeKeyValuePayload(payload, "delete")
	if err != nil {
		return sm.Result{}, err
	}
	key := string(keyBytes)

	f.cacheDelete(key)

	if f.watchManager != nil {
		f.watchManager.Notify(key, nil, revision, true)
	}

	return sm.Result{Value: revision}, nil
}

// handleBatch decodes and applies a batch of operations atomically.
// Payload format: [opCount:2 LE][ops...] (protocol.DecodeBatch format).
func (f *FastStateMachine) handleBatch(payload []byte, revision uint64) (sm.Result, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "batch: " + err.Error()}
	}
	if err := f.applyBatchOps(ops, revision); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: revision}, nil
}

// applyBatchOps applies decoded batch operations to the shard cache and
// notifies watchers of each.
func (f *FastStateMachine) applyBatchOps(ops []protocol.DecodedOp, revision uint64) error {
	// Refuse the whole batch before applying any of it: the loop below mutates
	// as it goes, so validating inside it would leave earlier operations
	// committed and notified for a batch the caller is told was refused.
	if opErr := validateBatchOps(ops); opErr != nil {
		return opErr
	}

	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case kv.OpPut, protocol.OpMVCCPut:
			ownedValue := make([]byte, len(op.Value))
			copy(ownedValue, op.Value)
			f.cacheSet(key, ownedValue)
			if f.watchManager != nil {
				f.watchManager.Notify(key, ownedValue, revision, false)
			}
		case kv.OpDelete, protocol.OpMVCCDelete:
			f.cacheDelete(key)
			if f.watchManager != nil {
				f.watchManager.Notify(key, nil, revision, true)
			}
		default:
			return &InvalidOpcodeError{Op: op.Type}
		}
	}
	return nil
}
