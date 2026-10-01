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

package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	qrdbkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// pebbleUpdateHandler processes a single decoded command within a PebbleDB
// batch. It receives the store, the batch, and the command payload. It
// returns the result, any watch notifications, and an error.
type pebbleUpdateHandler func(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error)

// pebbleNotification captures a single change event for delivery to the
// watch hub after the PebbleDB batch is committed.
type pebbleNotification struct {
	eventType watch.EventType
	key       []byte
	kv        *qrdbkv.KeyValue
	prevKv    *qrdbkv.KeyValue
}

// PebbleStateMachine implements sm.DiskStateMachine backed by PebbleDB. It provides
// persistent key-value storage with crash recovery, prefix-based listing via
// native PebbleDB iterators, and point-in-time snapshots.
//
// When configured with optional components (lessor, indexes, time-series,
// watch hub), PebbleStateMachine supports the full extended protocol including
// batch operations, compare-and-swap transactions, lease management,
// secondary indexes, and time-series storage -- all with direct-to-disk
// durability and without MVCC versioning overhead.
//
// Thread safety: PebbleDB handles its own internal synchronization. Lookup
// may be called concurrently with Update as required by the DiskStateMachine
// contract.
type PebbleStateMachine struct {
	mu   sync.RWMutex
	db   *pebble.DB
	dir  string
	opts *pebble.Options

	// hub delivers watch notifications after committed batches.
	hub *watch.Hub

	// lessor manages lease lifecycle. Nil if leases are not enabled.
	lessor *lease.Lessor

	// indexes holds registered secondary indexes by name.
	indexes map[string]index.Index

	// indexOwners holds the owner recorded for each index by name
	// (index.IndexConfig.Owner, go-qrdb#426). An absent entry is the empty
	// owner, the explicit index API.
	indexOwners map[string]string

	// ts is the time-series storage engine. Nil if not configured.
	ts *timeseries.Storage

	// enableTimeSeries indicates that a time-series storage engine
	// should be created in Open() using the same PebbleDB instance.
	enableTimeSeries bool

	// updateHandlers maps op codes to their update handlers for O(1) dispatch.
	updateHandlers map[byte]pebbleUpdateHandler
}

// NewPebbleStateMachine creates a new PebbleStateMachine with the given PebbleDB options.
// If opts is nil, default options are used. The store must be opened via
// Open() before use.
func NewPebbleStateMachine(opts *pebble.Options) *PebbleStateMachine {
	if opts == nil {
		opts = &pebble.Options{}
	}
	s := &PebbleStateMachine{
		opts:        opts,
		indexes:     make(map[string]index.Index),
		indexOwners: make(map[string]string),
	}
	s.updateHandlers = s.buildUpdateHandlers()
	return s
}

// buildUpdateHandlers constructs the map-based dispatch table for all
// supported opcodes. Base KV opcodes (1-3) and extended protocol opcodes
// (10+) are registered here for O(1) lookup during Update.
func (s *PebbleStateMachine) buildUpdateHandlers() map[byte]pebbleUpdateHandler {
	return map[byte]pebbleUpdateHandler{
		// Base KV operations.
		kv.OpPut:         pebbleHandlePut,
		kv.OpGet:         pebbleHandleGet,
		kv.OpDelete:      pebbleHandleDelete,
		kv.OpPutIfAbsent: pebbleHandlePutIfAbsent,

		// MVCC-compatible Put/Delete (same semantics, no revision tracking).
		protocol.OpMVCCPut:         pebbleHandleMVCCPut,
		protocol.OpMVCCPutIfAbsent: pebbleHandleMVCCPutIfAbsent,
		protocol.OpMVCCDelete:      pebbleHandleMVCCDelete,

		// Batch and transaction operations.
		protocol.OpBatch:            pebbleHandleBatch,
		protocol.OpTransaction:      pebbleHandleTransaction,
		protocol.OpCompareAndDelete: pebbleHandleCompareAndDelete,
		protocol.OpCompareAndSwap:   pebbleHandleCompareAndSwap,

		// Lease operations.
		protocol.OpLeaseGrant:     pebbleHandleLeaseGrant,
		protocol.OpLeaseRevoke:    pebbleHandleLeaseRevoke,
		protocol.OpLeaseKeepAlive: pebbleHandleLeaseKeepAlive,

		// Index operations.
		protocol.OpIndexCreate: s.pebbleHandleIndexCreate,
		protocol.OpIndexDrop:   s.pebbleHandleIndexDrop,

		// Time-series operations.
		protocol.OpTSWrite:        s.pebbleHandleTSWrite,
		protocol.OpTSWriteBatch:   s.pebbleHandleTSWriteBatch,
		protocol.OpTSWritePoint:   s.pebbleHandleTSWritePoint,
		protocol.OpTSDeleteSeries: s.pebbleHandleTSDeleteSeries,
		protocol.OpTSSetRetention: s.pebbleHandleTSSetRetention,
	}
}

// SetWatchHub attaches a watch hub to the store. When set, the store
// emits watch notifications after each committed batch. Must be called
// before the store is opened.
func (s *PebbleStateMachine) SetWatchHub(hub *watch.Hub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hub = hub
}

// SetLessor attaches a lessor to the store. When set, lease
// grant/revoke/keep-alive commands are processed. Must be called
// before the store is opened.
func (s *PebbleStateMachine) SetLessor(lr *lease.Lessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lessor = lr
}

// Lessor returns the attached lessor. Returns nil if no lessor is set.
func (s *PebbleStateMachine) Lessor() *lease.Lessor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lessor
}

// SetTimeSeries attaches a time-series storage engine to the store.
// Must be called before the store is opened.
func (s *PebbleStateMachine) SetTimeSeries(ts *timeseries.Storage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ts = ts
}

// SetTimeSeriesEnabled instructs the PebbleStateMachine to create a
// time-series storage engine when the database is opened. Must be
// called before Open().
func (s *PebbleStateMachine) SetTimeSeriesEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enableTimeSeries = enabled
}

// TimeSeries returns the attached time-series storage. Returns nil if
// not configured.
func (s *PebbleStateMachine) TimeSeries() *timeseries.Storage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ts
}

// DB returns the underlying PebbleDB instance. Returns nil if the store
// has not been opened.
func (s *PebbleStateMachine) DB() *pebble.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

// Open initializes the PebbleDB database at the given directory and returns
// the last applied Raft log index for crash recovery. Returns 0 for a fresh
// database.
func (s *PebbleStateMachine) Open(_ context.Context, initialDir string, _ <-chan struct{}) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.opts != nil && s.opts.Logger == nil {
		s.opts.Logger = pebbleStorage.NewLogger(nil)
	}
	db, err := pebble.Open(initialDir, s.opts)
	if err != nil {
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.db = db
	s.dir = initialDir

	// Create time-series storage if enabled. Uses the same PebbleDB
	// instance so that TS data shares the same durability guarantees.
	if s.enableTimeSeries && s.ts == nil {
		ts, tsErr := timeseries.New(db)
		if tsErr != nil {
			_ = db.Close()
			s.db = nil
			return 0, &OpenError{Dir: initialDir, Err: tsErr}
		}
		s.ts = ts
	}

	lastApplied, err := s.readLastApplied()
	if err != nil {
		_ = s.db.Close()
		s.db = nil
		s.ts = nil
		return 0, &ReadError{Key: metaKeyLastApplied, Err: err}
	}

	return lastApplied, nil
}

// readLastApplied reads the persisted last applied index from the metadata key.
func (s *PebbleStateMachine) readLastApplied() (uint64, error) {
	val, closer, err := s.db.Get([]byte(metaKeyLastApplied))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = closer.Close() }()

	if len(val) != 8 {
		return 0, nil
	}
	return binary.LittleEndian.Uint64(val), nil
}

// Update applies committed entries to the PebbleDB store using an atomic
// batch. Each entry is decoded using either the base KV protocol or the
// extended wire protocol and dispatched to the appropriate handler via
// map-based O(1) lookup. The last applied index is persisted in the same
// batch for crash recovery.
func (s *PebbleStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	// The batch is INDEXED so that handlers can read their own writes: a
	// conditional put's existence guard and a transaction's value precondition
	// must observe the entries already applied into this same batch. The apply
	// loop packs a timing-dependent number of committed entries into one Update
	// call, so a guard that read committed storage instead would make that
	// packing observable and two replicas that packed the same log differently
	// would reach different states.
	batch := s.db.NewIndexedBatch()
	defer func() { _ = batch.Close() }()

	var lastIndex uint64
	var notifications []pebbleNotification

	for i, e := range entries {
		if len(e.Cmd) == 0 {
			return &protocol.InvalidCommandError{Reason: "empty command"}
		}

		var result sm.Result
		var notifs []pebbleNotification
		var err error

		op := e.Cmd[0]
		handler, ok := s.updateHandlers[op]
		if ok {
			// Base KV handlers decode through kv.DecodeCommand and therefore
			// need the full command including its op byte; extended protocol
			// handlers take the payload with the op byte stripped. The split is
			// by decoder, not by numeric range: kv.OpPutIfAbsent sorts above
			// kv.OpDelete but is still a base KV command.
			payload := e.Cmd
			if !isBaseKVOp(op) {
				payload = e.Cmd[1:]
			}
			result, notifs, err = handler(s, batch, payload)
		} else {
			err = &protocol.UnknownOpError{Op: op}
		}

		// An opcode this state machine has no handler for is a refusal, not a
		// fault: it is answered in the result payload rather than halting Apply
		// and costing the shard every subsequent linearizable read
		// (go-qrdb#520).
		resolved, stop, applyErr := statemachine.ResolveApplyError(slog.Default(), e.Index, result, err)
		if stop {
			return applyErr
		}
		results[i] = resolved

		// Notifications from a refused entry are deliberately dropped: it
		// mutated nothing, so there is nothing for a watcher to observe.
		if err == nil && len(notifs) > 0 {
			notifications = append(notifications, notifs...)
		}

		if e.Index > lastIndex {
			lastIndex = e.Index
		}
	}

	// Persist the last applied index in the same atomic batch.
	if lastIndex > 0 {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], lastIndex)
		if err := batch.Set([]byte(metaKeyLastApplied), buf[:], pebble.Sync); err != nil {
			return &WriteError{Err: err}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}

	// Deliver watch notifications after the batch is committed.
	if s.hub != nil && len(notifications) > 0 {
		for _, n := range notifications {
			switch n.eventType {
			case watch.EventPut:
				s.hub.NotifyPut(n.key, n.kv, n.prevKv)
			case watch.EventDelete:
				s.hub.NotifyDelete(n.key, n.kv, n.prevKv)
			}
		}
	}

	return nil
}

// Lookup reads state from the PebbleDB store. Supports the following query types:
//   - kv.ListQuery: returns sorted []string of keys matching the prefix
//   - lease.LeaseTimeToLiveQuery: returns *lease.Lease for the given ID
//   - lease.LeasesQuery: returns []*lease.Lease for all active leases
//   - index.IndexQueryRequest: returns []uint64 entity IDs matching the value
//   - index.IndexScanRequest: returns map[string][]uint64 for the range
//   - index.IndexListRequest: returns []string of index names
//   - timeseries.TSQueryRequest: returns time-series query results
//   - timeseries.TSAggregateRequest: returns aggregation results
//   - timeseries.TSRichQueryRequest: returns rich query results
//   - timeseries.TSGetRetentionRequest: returns retention policy
//   - timeseries.TSListMetricsRequest: returns []string of metric names
//   - []byte: returns a copy of the value for the given key
//   - string: returns a copy of the value for the given key
func (s *PebbleStateMachine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	switch q := query.(type) {
	case kv.ListQuery:
		return s.listKeys(q.Prefix)

	case lease.LeaseTimeToLiveQuery:
		if s.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		// See pkg/engine's MVCCEngine.Lookup (the "mvcc" raft.sm_type's
		// answer to this same query type, against the same Lessor type) for
		// why a miss here is reported as *service.LeaseNotFoundError rather
		// than the generic query error this used to return (go-qrdb#387):
		// no transport's error mapping recognized the latter as a
		// lease-not-found condition, so it never reached a caller as
		// anything but an internal fault.
		l, err := s.lessor.LookupOrNotFound(q.ID)
		if err != nil {
			return nil, &service.LeaseNotFoundError{LeaseID: q.ID}
		}
		return l, nil

	case lease.LeasesQuery:
		if s.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		return s.lessor.Leases(), nil

	case index.IndexQueryRequest:
		idx, ok := s.indexes[q.IndexName]
		if !ok {
			return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
		}
		return idx.Get(q.Value)

	case index.IndexScanRequest:
		idx, ok := s.indexes[q.IndexName]
		if !ok {
			return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
		}
		return idx.Scan(q.Start, q.End)

	case index.IndexListRequest:
		names := make([]string, 0, len(s.indexes))
		for name := range s.indexes {
			names = append(names, name)
		}
		return names, nil

	case timeseries.TSQueryRequest:
		if s.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return s.ts.Query(q.Metric, q.Start, q.End, q.Opts...)

	case timeseries.TSAggregateRequest:
		if s.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return s.ts.Aggregate(q.Metric, q.Start, q.End, q.Func, q.Interval)

	case timeseries.TSRichQueryRequest:
		if s.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return s.ts.QueryRich(context.Background(), q.Request)

	case timeseries.TSGetRetentionRequest:
		if s.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return s.ts.GetRetentionPolicy(q.Metric)

	case timeseries.TSListMetricsRequest:
		if s.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return s.ts.ListMetrics(q.Prefix, q.Limit)

	case *statemachine.PrefixQuery:
		return s.scanPrefix(q)

	case []byte:
		return s.getKey(q)

	case string:
		return s.getKey([]byte(q))

	default:
		return nil, &InvalidQueryError{Reason: "unsupported query type"}
	}
}

// listKeys returns sorted keys matching the given prefix using a native PebbleDB
// prefix iterator. The metadata key is excluded from results.
func (s *PebbleStateMachine) listKeys(prefix string) ([]string, error) {
	iterOpts := &pebble.IterOptions{}
	if prefix != "" {
		lower := []byte(prefix)
		upper := prefixUpperBound(lower)
		iterOpts.LowerBound = lower
		if upper != nil {
			iterOpts.UpperBound = upper
		}
	}

	iter, err := s.db.NewIter(iterOpts)
	if err != nil {
		return nil, &ReadError{Key: prefix, Err: err}
	}
	defer func() { _ = iter.Close() }()

	// When the prefix itself lies inside a reserved namespace whose owner
	// enumerates it, skip the internal key filter so that subsystem (the RBAC
	// stores, the relational row and index reader) can list its own records
	// through the PebbleStateMachine path.
	skipFilter := skipInternalKeyFilter(prefix)

	var keys []string
	for iter.First(); iter.Valid(); iter.Next() {
		k := string(iter.Key())
		// Exclude internal metadata keys (prefixed with \x00) and
		// system keys used for roles, users, and indexes.
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		if !skipFilter && isInternalKey(k) {
			continue
		}
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, &ReadError{Key: prefix, Err: err}
	}

	sort.Strings(keys)
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

// scanPrefix performs a prefix scan returning a map of key→value pairs.
// If limit > 0, at most that many entries are returned.
func (s *PebbleStateMachine) scanPrefix(pq *statemachine.PrefixQuery) (map[string][]byte, error) {
	prefix := string(pq.Prefix)
	iterOpts := &pebble.IterOptions{}
	if len(pq.Prefix) > 0 {
		upper := prefixUpperBound(pq.Prefix)
		iterOpts.LowerBound = pq.Prefix
		if upper != nil {
			iterOpts.UpperBound = upper
		}
	}

	iter, err := s.db.NewIter(iterOpts)
	if err != nil {
		return nil, &ReadError{Key: prefix, Err: err}
	}
	defer func() { _ = iter.Close() }()

	skipFilter := skipInternalKeyFilter(prefix)

	result := make(map[string][]byte)
	for iter.First(); iter.Valid(); iter.Next() {
		k := string(iter.Key())
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		if !skipFilter && isInternalKey(k) {
			continue
		}
		if prefix == "" || strings.HasPrefix(k, prefix) {
			val, err := iter.ValueAndErr()
			if err != nil {
				return nil, &ReadError{Key: k, Err: err}
			}
			cp := make([]byte, len(val))
			copy(cp, val)
			result[k] = cp
			if pq.Limit > 0 && len(result) >= pq.Limit {
				break
			}
		}
	}
	if err := iter.Error(); err != nil {
		return nil, &ReadError{Key: prefix, Err: err}
	}

	return result, nil
}

// prefixUpperBound computes the upper bound for a prefix scan. Returns nil
// if no upper bound can be computed (prefix is all 0xFF bytes).
func prefixUpperBound(prefix []byte) []byte {
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

// getKey retrieves a single key's value, returning a defensive copy.
func (s *PebbleStateMachine) getKey(key []byte) ([]byte, error) {
	val, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, &kv.KeyNotFoundError{Key: string(key)}
		}
		return nil, &ReadError{Key: string(key), Err: err}
	}
	defer func() { _ = closer.Close() }()

	cp := make([]byte, len(val))
	copy(cp, val)
	return cp, nil
}

// BytesLookup performs a zero-allocation byte-slice lookup for the NALookup
// interface. Returns a defensive copy of the value.
func (s *PebbleStateMachine) BytesLookup(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	return s.getKey(key)
}

// Sync flushes all pending writes to durable storage. Called before snapshot
// operations to ensure consistency.
func (s *PebbleStateMachine) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	if err := s.db.Flush(); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}

// PrepareSnapshot creates a point-in-time snapshot of the database for use
// by SaveSnapshot. The returned *pebble.Snapshot must be closed after use.
func (s *PebbleStateMachine) PrepareSnapshot() (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	return s.db.NewSnapshot(), nil
}

// SaveSnapshot serializes the database state captured by PrepareSnapshot
// to the writer using the same wire format as the in-memory KV stores:
// [count:4 LE] then for each pair: [keyLen:4 LE][key][valLen:4 LE][val].
// Internal metadata keys are excluded from the snapshot.
func (s *PebbleStateMachine) SaveSnapshot(_ context.Context, snapshotCtx interface{}, w io.Writer, stopper <-chan struct{}) error {
	snap, ok := snapshotCtx.(*pebble.Snapshot)
	if !ok {
		return &SnapshotError{Op: "save", Err: &InvalidQueryError{Reason: "invalid snapshot context type"}}
	}
	defer func() { _ = snap.Close() }()

	// First pass: count non-metadata entries.
	iter, err := snap.NewIter(nil)
	if err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}

	var counter snapshotEntryCounter
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		if err := counter.add(); err != nil {
			_ = iter.Close()
			return err
		}
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return &SnapshotError{Op: "save", Err: err}
	}
	_ = iter.Close()

	if err := binary.Write(w, binary.LittleEndian, counter.count()); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}

	// Second pass: write entries.
	iter, err = snap.NewIter(nil)
	if err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		select {
		case <-stopper:
			return context.Canceled
		default:
		}

		k := iter.Key()
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}

		v, err := iter.ValueAndErr()
		if err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}

		if err := writeSnapshotEntry(w, k, v); err != nil {
			return err
		}
	}

	return iter.Error()
}

// RecoverFromSnapshot restores the database state from the reader, replacing
// all existing data. Reads the same wire format written by SaveSnapshot.
func (s *PebbleStateMachine) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	// Clear all existing data.
	if err := s.clearAll(); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	for i := uint32(0); i < count; i++ {
		select {
		case <-stopper:
			return context.Canceled
		default:
		}

		var keyLen uint32
		if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}
		key := make([]byte, keyLen)
		if _, err := io.ReadFull(r, key); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}

		var valLen uint32
		if err := binary.Read(r, binary.LittleEndian, &valLen); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}
		val := make([]byte, valLen)
		if _, err := io.ReadFull(r, val); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}

		if err := batch.Set(key, val, pebble.Sync); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	return nil
}

// clearAll removes all entries from the database using a range deletion.
func (s *PebbleStateMachine) clearAll() error {
	iter, err := s.db.NewIter(nil)
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()

	if !iter.First() {
		return iter.Error()
	}
	firstKey := make([]byte, len(iter.Key()))
	copy(firstKey, iter.Key())

	if !iter.Last() {
		return iter.Error()
	}
	// DeleteRange is [start, end) so we need end > lastKey.
	lastKey := make([]byte, len(iter.Key())+1)
	copy(lastKey, iter.Key())
	lastKey[len(lastKey)-1] = 0xFF

	if err := iter.Error(); err != nil {
		return err
	}

	return s.db.DeleteRange(firstKey, lastKey, pebble.Sync)
}

// Close releases all resources held by the PebbleDB store.
func (s *PebbleStateMachine) Close(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	err := s.db.Close()
	s.db = nil
	return err
}

// GetHash computes a deterministic xxhash64 hash of all user key-value pairs
// in sorted order. Internal metadata keys are excluded.
func (s *PebbleStateMachine) GetHash() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return 0, &NotOpenError{}
	}

	h := xxhash.New()

	iter, err := s.db.NewIter(nil)
	if err != nil {
		return 0, &ReadError{Key: "", Err: err}
	}
	defer func() { _ = iter.Close() }()

	// PebbleDB iterates in sorted order by default.
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		v, err := iter.ValueAndErr()
		if err != nil {
			return 0, &ReadError{Key: string(k), Err: err}
		}
		_, _ = h.Write(k)
		_, _ = h.Write(v)
	}
	if err := iter.Error(); err != nil {
		return 0, &ReadError{Key: "", Err: err}
	}

	return h.Sum64(), nil
}

// ---------------------------------------------------------------------------
// Base KV update handlers
// ---------------------------------------------------------------------------

// A Raft state machine must not return a Go error from Update() for a
// deterministic consequence of valid client input (an invalid lease TTL, a
// time-series validation failure, or an operation against a subsystem not
// configured on this SM): that halts lastApplied and stalls the group. Such
// outcomes are encoded into sm.Result.Data with their kind —
// statemachine.CapabilityResult for an absent subsystem,
// statemachine.RefusalResult for a subsystem's own refusal — and surfaced to
// the client as a typed error by service.checkResultError.

// pebbleHandlePut processes a base KV Put command (OpPut=1). The payload
// is the full encoded command including the op byte.
func pebbleHandlePut(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	cmd, err := kv.DecodeCommand(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Capture previous value for watch notifications.
	var prevKv *qrdbkv.KeyValue
	if s.hub != nil {
		if val, getErr := s.getKeyInBatch(batch, cmd.Key); getErr == nil {
			prevKv = &qrdbkv.KeyValue{Key: cmd.Key, Value: val}
		}
	}

	if err := batch.Set(cmd.Key, cmd.Value, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventPut,
			key:       cmd.Key,
			kv:        &qrdbkv.KeyValue{Key: cmd.Key, Value: cmd.Value},
			prevKv:    prevKv,
		}}
	}

	return sm.Result{Value: uint64(len(cmd.Value))}, notifs, nil
}

// pebbleHandlePutIfAbsent processes a base KV conditional put
// (OpPutIfAbsent=4). The payload is the full encoded command including the op
// byte.
//
// The existence check runs here, inside the Update path, against the state this
// replica has applied up to this log position — the same input on every replica
// producing the same decision, which is what keeps them identical. Doing the
// check on the proposer and then sending an unconditional Put would be a
// different operation with a window in which two proposers both observe
// "absent".
//
// A declined write is a normal outcome reported through Result.Value, not an
// error: the caller that lost the race received a correct answer, and returning
// an error here would surface as a failed proposal instead.
func pebbleHandlePutIfAbsent(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	cmd, err := kv.DecodeCommand(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	absent, getErr := s.keyIsAbsentInBatch(batch, cmd.Key)
	if getErr != nil {
		return sm.Result{}, nil, getErr
	}
	if !absent {
		return sm.Result{Value: kv.ResultNotStored}, nil, nil
	}

	if err := batch.Set(cmd.Key, cmd.Value, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	// No prevKv: the key was absent, which is the only path that reaches here.
	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventPut,
			key:       cmd.Key,
			kv:        &qrdbkv.KeyValue{Key: cmd.Key, Value: cmd.Value},
		}}
	}

	return sm.Result{Value: kv.ResultStored}, notifs, nil
}

// pebbleHandleCompareAndDelete removes a key only when its stored value still
// equals the expected one.
//
// The comparison runs HERE, inside the apply path, against the state this
// replica has applied at this entry's log position. Comparing on the proposer
// and then proposing a plain delete leaves a window in which another caller
// legitimately claims the key, so the delete would remove a value the
// proposer never observed (go-qrdb#41).
//
// A refused delete is a normal outcome reported through Result.Value
// (protocol.ResultNotDeleted), never an error that would stall consensus.
//
// The read propagates its error rather than treating a fault as "no match":
// a local fault read that way would leave the key here while peers that read
// successfully removed it, diverging the group from an identical log.
func pebbleHandleCompareAndDelete(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	key, expected, err := protocol.DecodeCompareAndDelete(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	current, getErr := s.getKeyInBatch(batch, key)
	if getErr != nil {
		var notFound *kv.KeyNotFoundError
		if !errors.As(getErr, &notFound) {
			return sm.Result{}, nil, getErr
		}
		return sm.Result{Value: protocol.ResultNotDeleted}, nil, nil
	}
	if !bytes.Equal(current, expected) {
		return sm.Result{Value: protocol.ResultNotDeleted}, nil, nil
	}

	if derr := batch.Delete(key, pebble.Sync); derr != nil {
		return sm.Result{}, nil, &WriteError{Err: derr}
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventDelete,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key},
			prevKv:    &qrdbkv.KeyValue{Key: key, Value: current},
		}}
	}

	return sm.Result{Value: protocol.ResultDeleted}, notifs, nil
}

// pebbleHandleCompareAndSwap replaces a key's value only when the value in
// this batch still equals expected.
//
// The read goes through getKeyInBatch, not the database, for the same reason
// the delete does: earlier operations in the same batch are not yet visible to
// the store, so reading past them would evaluate the guard against a stale
// value and decide differently from a replica whose batch was split
// differently.
//
// An absent key is NOT created — a swap moves a record between states, which
// is not a statement about a record that does not exist. Creating here would
// also make the operation indistinguishable from PutIfAbsent at a call site.
func pebbleHandleCompareAndSwap(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	key, expected, next, err := protocol.DecodeCompareAndSwap(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	current, getErr := s.getKeyInBatch(batch, key)
	if getErr != nil {
		var notFound *kv.KeyNotFoundError
		if !errors.As(getErr, &notFound) {
			return sm.Result{}, nil, getErr
		}
		return sm.Result{Value: protocol.ResultNotSwapped}, nil, nil
	}
	if !bytes.Equal(current, expected) {
		return sm.Result{Value: protocol.ResultNotSwapped}, nil, nil
	}

	if serr := batch.Set(key, next, pebble.Sync); serr != nil {
		return sm.Result{}, nil, &WriteError{Err: serr}
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventPut,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key, Value: next},
			prevKv:    &qrdbkv.KeyValue{Key: key, Value: current},
		}}
	}

	return sm.Result{Value: protocol.ResultSwapped}, notifs, nil
}

// pebbleHandleGet processes a base KV Get command (OpGet=2). Gets are
// no-ops in the Update path (reads happen in Lookup).
func pebbleHandleGet(_ *PebbleStateMachine, _ *pebble.Batch, _ []byte) (sm.Result, []pebbleNotification, error) {
	return sm.Result{Value: 0}, nil, nil
}

// pebbleHandleDelete processes a base KV Delete command (OpDelete=3). The
// payload is the full encoded command including the op byte.
func pebbleHandleDelete(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	cmd, err := kv.DecodeCommand(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Capture previous value for watch notifications.
	var prevKv *qrdbkv.KeyValue
	if s.hub != nil {
		if val, getErr := s.getKeyInBatch(batch, cmd.Key); getErr == nil {
			prevKv = &qrdbkv.KeyValue{Key: cmd.Key, Value: val}
		}
	}

	if err := batch.Delete(cmd.Key, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventDelete,
			key:       cmd.Key,
			kv:        &qrdbkv.KeyValue{Key: cmd.Key},
			prevKv:    prevKv,
		}}
	}

	return sm.Result{Value: 1}, notifs, nil
}

// ---------------------------------------------------------------------------
// Extended protocol update handlers (MVCC-compatible, no revision tracking)
// ---------------------------------------------------------------------------

// pebbleHandleMVCCPut processes an MVCC Put command without revision
// tracking. The key-value pair is stored directly in PebbleDB. Lease
// attachment is tracked if a lessor is configured.
func pebbleHandleMVCCPut(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Capture previous value for watch notifications.
	var prevKv *qrdbkv.KeyValue
	if s.hub != nil {
		if val, getErr := s.getKeyInBatch(batch, key); getErr == nil {
			prevKv = &qrdbkv.KeyValue{Key: key, Value: val}
		}
	}

	if err := batch.Set(key, value, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	// Track key-lease attachment.
	if s.lessor != nil && leaseID > 0 {
		s.lessor.AttachKey(leaseID, key)
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventPut,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key, Value: value, LeaseID: leaseID},
			prevKv:    prevKv,
		}}
	}

	return sm.Result{Value: uint64(len(value))}, notifs, nil
}

// pebbleHandleMVCCPutIfAbsent processes an MVCC conditional put
// (OpMVCCPutIfAbsent=65), storing only when the key is absent.
//
// It shares DecodeMVCCPut with the unconditional handler because the payload
// shape is identical; only the opcode and the guard differ. The guard runs
// inside Update so every replica evaluates it against the same applied state.
//
// There is no prevKv on the notification: the write only happens when the key
// was absent, so there is no previous value to report.
func pebbleHandleMVCCPutIfAbsent(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	absent, getErr := s.keyIsAbsentInBatch(batch, key)
	if getErr != nil {
		return sm.Result{}, nil, getErr
	}
	if !absent {
		return sm.Result{Value: kv.ResultNotStored}, nil, nil
	}

	if err := batch.Set(key, value, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	if s.lessor != nil && leaseID > 0 {
		s.lessor.AttachKey(leaseID, key)
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventPut,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key, Value: value, LeaseID: leaseID},
		}}
	}

	return sm.Result{Value: kv.ResultStored}, notifs, nil
}

// pebbleHandleMVCCDelete processes an MVCC Delete command without
// revision tracking. The key is deleted directly from PebbleDB.
func pebbleHandleMVCCDelete(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	key, err := protocol.DecodeMVCCDelete(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Capture previous value for watch notifications.
	var prevKv *qrdbkv.KeyValue
	if s.hub != nil {
		if val, getErr := s.getKeyInBatch(batch, key); getErr == nil {
			prevKv = &qrdbkv.KeyValue{Key: key, Value: val}
		}
	}

	if err := batch.Delete(key, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	// Detach key from lease if it had one.
	if s.lessor != nil && prevKv != nil && prevKv.LeaseID > 0 {
		s.lessor.DetachKey(prevKv.LeaseID, key)
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventDelete,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key},
			prevKv:    prevKv,
		}}
	}

	return sm.Result{Value: 1}, notifs, nil
}

// ---------------------------------------------------------------------------
// Batch handler
// ---------------------------------------------------------------------------

// pebbleHandleBatch processes a batch of operations. All operations are
// executed atomically within the PebbleDB batch.
func pebbleHandleBatch(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Pre-validate the OPCODES: an operation this state machine cannot execute
	// refuses the whole batch and stages nothing (validatePebbleOps).
	if verr := validatePebbleOps(ops); verr != nil {
		return sm.Result{}, nil, verr
	}

	var notifs []pebbleNotification
	for _, op := range ops {
		opNotifs, execErr := pebbleExecuteOp(s, batch, op)
		if execErr != nil {
			return sm.Result{}, nil, execErr
		}
		notifs = append(notifs, opNotifs...)
	}

	return sm.Result{Value: uint64(len(ops))}, notifs, nil
}

// ---------------------------------------------------------------------------
// Transaction handler (compare-and-swap without MVCC revisions)
// ---------------------------------------------------------------------------

// pebbleHandleTransaction processes a compare-and-swap transaction.
// Evaluates all compares against current PebbleDB state, then atomically
// executes the success or failure operation set.
func pebbleHandleTransaction(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	txn, err := protocol.DecodeTransaction(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Reject revision-based compares BEFORE evaluating anything. This state
	// machine keeps no MVCC revisions, so a create/mod-revision compare has
	// nothing meaningful to compare against. Silently treating every key as
	// revision 0 is the dangerous answer: a client written for the MVCC engine
	// does read-then-CAS, reads revision 0 here, compares equal, and commits —
	// so two concurrent writers both "succeed" and one update is lost with no
	// signal. Failing the transaction with an actionable message turns that
	// silent lost update into an obvious misconfiguration.
	//
	// This is a client-input outcome, so it is encoded into Result.Data as an
	// application error rather than returned as a Go error: returning an error
	// from Update() would stall the Raft apply loop for every client.
	for _, dc := range txn.Compares {
		if dc.Target == compareTargetCreateRevision || dc.Target == compareTargetModRevision {
			return statemachine.CapabilityResult("transaction: create_revision/mod_revision compares require " +
				"an MVCC state machine; this node runs a non-MVCC backend (set raft.sm_type: mvcc). " +
				"Compare on value or version instead."), nil, nil
		}
	}

	// Evaluate all compares against current state.
	succeeded := true
	for _, dc := range txn.Compares {
		if !pebbleEvaluateCompare(s, batch, dc) {
			succeeded = false
			break
		}
	}

	// Select the operation set to execute.
	var ops []protocol.DecodedOp
	if succeeded {
		ops = txn.SuccessOps
	} else {
		ops = txn.FailureOps
	}

	// Pre-validate the OPCODES of the selected set: an operation this state
	// machine cannot execute refuses the whole transaction and stages nothing.
	if verr := validatePebbleOps(ops); verr != nil {
		return sm.Result{}, nil, verr
	}

	// Execute all operations atomically.
	var notifs []pebbleNotification
	for _, op := range ops {
		opNotifs, execErr := pebbleExecuteOp(s, batch, op)
		if execErr != nil {
			return sm.Result{}, nil, execErr
		}
		notifs = append(notifs, opNotifs...)
	}

	// Encode result: Value = op count, Data[0] = succeeded flag.
	var data [1]byte
	if succeeded {
		data[0] = 1
	}
	return sm.Result{Value: uint64(len(ops)), Data: data[:]}, notifs, nil
}

// Transaction compare targets on the wire. The values mirror lock.CompareTarget
// (CompareVersion, CompareCreateRevision, CompareModRevision, CompareValue in
// iota order); they are duplicated here as untyped byte constants because
// protocol.DecodedCompare carries Target as a raw byte and pkg/store does not
// import pkg/lock.
const (
	compareTargetVersion        byte = 0
	compareTargetCreateRevision byte = 1
	compareTargetModRevision    byte = 2
	compareTargetValue          byte = 3
)

// pebbleEvaluateCompare evaluates a single transaction compare against the
// current PebbleDB state. This state machine keeps no MVCC revisions, so only
// value and version comparisons are meaningful: version degrades honestly (0
// when the key is absent, 1 when present), while create/mod-revision compares
// are rejected up front by pebbleHandleTransaction and therefore never reach
// this function. They fall through to the fail-closed default rather than
// silently comparing every key against revision 0.
func pebbleEvaluateCompare(s *PebbleStateMachine, batch *pebble.Batch, dc protocol.DecodedCompare) bool {
	val, err := s.getKeyInBatch(batch, dc.Key)

	// Build the target value for comparison.
	switch dc.Target {
	case compareTargetValue:
		if err != nil {
			// Key does not exist: only equal-to-zero-length succeeds.
			return (dc.Result == 0) && len(dc.Value) == 0
		}
		cmp := bytes.Compare(val, dc.Value)
		return evaluateCompareResult(dc.Result, cmp)

	case compareTargetVersion:
		// Without MVCC, version is 0 if key doesn't exist, 1 if it does.
		var version int64
		if err == nil {
			version = 1
		}
		return evaluateCompareInt(dc.Result, version, dc.Version)

	default:
		// Includes compareTargetCreateRevision and compareTargetModRevision,
		// which pebbleHandleTransaction rejects before evaluation. Fail closed
		// if one ever reaches here by another path.
		return false
	}
}

// evaluateCompareResult maps a comparison result (from bytes.Compare) to
// the expected compare result byte.
func evaluateCompareResult(result byte, cmp int) bool {
	switch result {
	case 0: // Equal
		return cmp == 0
	case 1: // Greater
		return cmp > 0
	case 2: // Less
		return cmp < 0
	case 3: // NotEqual
		return cmp != 0
	default:
		return false
	}
}

// evaluateCompareInt compares two int64 values using the compare result byte.
func evaluateCompareInt(result byte, actual, expected int64) bool {
	switch result {
	case 0: // Equal
		return actual == expected
	case 1: // Greater
		return actual > expected
	case 2: // Less
		return actual < expected
	case 3: // NotEqual
		return actual != expected
	default:
		return false
	}
}

// pebbleOpSupported reports whether pebbleExecuteOp can execute op. It is the
// single definition of that set, so the pre-flight check below and the
// dispatch itself cannot drift apart.
func pebbleOpSupported(op byte) bool {
	switch op {
	case protocol.OpMVCCPut, protocol.OpMVCCDelete:
		return true
	default:
		return false
	}
}

// validatePebbleOps rejects an operation set naming an operation this state
// machine cannot execute, BEFORE any of it is staged into the batch.
//
// The batch and transaction handlers walk their operations applying each in
// turn into the caller's pebble.Batch, so without this they refuse only on
// REACHING the unknown one, having already staged every operation before it --
// and the apply loop commits that batch regardless. While such a refusal
// halted Apply that was the lesser of two problems: the shard was poisoned
// anyway. Now that an unrecognised opcode is an application-level refusal the
// shard survives (go-qrdb#520), a partial write would become a lie the caller
// acts on: told the batch was refused, with half of it committed.
//
// A batch is atomic or it is nothing.
func validatePebbleOps(ops []protocol.DecodedOp) error {
	for _, op := range ops {
		if !pebbleOpSupported(op.Type) {
			return &protocol.UnknownOpError{Op: op.Type}
		}
	}
	return nil
}

// pebbleExecuteOp executes a single decoded operation (Put or Delete)
// within the PebbleDB batch. Returns watch notifications.
func pebbleExecuteOp(s *PebbleStateMachine, batch *pebble.Batch, op protocol.DecodedOp) ([]pebbleNotification, error) {
	switch op.Type {
	case protocol.OpMVCCPut:
		var prevKv *qrdbkv.KeyValue
		if s.hub != nil {
			if val, err := s.getKeyInBatch(batch, op.Key); err == nil {
				prevKv = &qrdbkv.KeyValue{Key: op.Key, Value: val}
			}
		}

		if err := batch.Set(op.Key, op.Value, pebble.Sync); err != nil {
			return nil, &WriteError{Err: err}
		}

		if s.lessor != nil && op.LeaseID > 0 {
			s.lessor.AttachKey(op.LeaseID, op.Key)
		}

		var notifs []pebbleNotification
		if s.hub != nil {
			notifs = []pebbleNotification{{
				eventType: watch.EventPut,
				key:       op.Key,
				kv:        &qrdbkv.KeyValue{Key: op.Key, Value: op.Value, LeaseID: op.LeaseID},
				prevKv:    prevKv,
			}}
		}
		return notifs, nil

	case protocol.OpMVCCDelete:
		var prevKv *qrdbkv.KeyValue
		if s.hub != nil {
			if val, err := s.getKeyInBatch(batch, op.Key); err == nil {
				prevKv = &qrdbkv.KeyValue{Key: op.Key, Value: val}
			}
		}

		if err := batch.Delete(op.Key, pebble.Sync); err != nil {
			return nil, &WriteError{Err: err}
		}

		if s.lessor != nil && prevKv != nil && prevKv.LeaseID > 0 {
			s.lessor.DetachKey(prevKv.LeaseID, op.Key)
		}

		var notifs []pebbleNotification
		if s.hub != nil {
			notifs = []pebbleNotification{{
				eventType: watch.EventDelete,
				key:       op.Key,
				kv:        &qrdbkv.KeyValue{Key: op.Key},
				prevKv:    prevKv,
			}}
		}
		return notifs, nil

	default:
		return nil, &protocol.UnknownOpError{Op: op.Type}
	}
}

// ---------------------------------------------------------------------------
// Lease handlers
// ---------------------------------------------------------------------------

// pebbleHandleLeaseGrant creates a new lease with the specified TTL.
func pebbleHandleLeaseGrant(s *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}

	info, grantErr := s.lessor.Grant(ttl)
	if grantErr != nil {
		// Invalid TTL (and similar input-shaped grant failures) are
		// deterministic application errors: surface via the result payload
		// rather than stalling consensus.
		return statemachine.RefusalResult(grantErr), nil, nil
	}

	return sm.Result{Value: uint64(info.ID)}, nil, nil
}

// pebbleHandleLeaseRevoke expires a lease and deletes all attached keys.
func pebbleHandleLeaseRevoke(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}

	keys, revokeErr := s.lessor.Revoke(leaseID)
	if revokeErr != nil {
		var nfe *lease.NotFoundError
		if errors.As(revokeErr, &nfe) {
			// Lease already gone — idempotent success so Raft Apply does not
			// stall on duplicate/replayed revoke proposals (e.g. the leader-side
			// expiry sweeper or deferred teardown).
			return sm.Result{Value: 0}, nil, nil
		}
		return sm.Result{}, nil, revokeErr
	}

	// Delete all attached keys.
	var notifs []pebbleNotification
	for _, key := range keys {
		var prevKv *qrdbkv.KeyValue
		if s.hub != nil {
			if val, getErr := s.getKeyInBatch(batch, key); getErr == nil {
				prevKv = &qrdbkv.KeyValue{Key: key, Value: val}
			}
		}

		if delErr := batch.Delete(key, pebble.Sync); delErr != nil {
			continue // key may already be deleted
		}

		if s.hub != nil {
			notifs = append(notifs, pebbleNotification{
				eventType: watch.EventDelete,
				key:       key,
				kv:        &qrdbkv.KeyValue{Key: key},
				prevKv:    prevKv,
			})
		}
	}

	return sm.Result{Value: uint64(len(keys))}, notifs, nil
}

// pebbleHandleLeaseKeepAlive resets a lease's TTL.
func pebbleHandleLeaseKeepAlive(s *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}

	_, kaErr := s.lessor.KeepAlive(leaseID)
	if kaErr != nil {
		var nfe *lease.NotFoundError
		if errors.As(kaErr, &nfe) {
			// Lease already gone — idempotent success so Raft Apply does not
			// stall on duplicate/replayed keepalive proposals.
			return sm.Result{Value: 0}, nil, nil
		}
		return sm.Result{}, nil, kaErr
	}

	return sm.Result{Value: uint64(leaseID)}, nil, nil
}

// ---------------------------------------------------------------------------
// Index handlers
// ---------------------------------------------------------------------------

// pebbleHandleIndexCreate creates a new secondary index backed by the
// same PebbleDB instance.
func (s *PebbleStateMachine) pebbleHandleIndexCreate(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	name, _, opts, err := protocol.DecodeIndexCreateOpts(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if _, exists := s.indexes[name]; exists {
		// A create naming an index ANOTHER owner holds is refused rather than
		// adopted (go-qrdb#426), as an application error so consensus advances.
		owner := s.indexOwners[name]
		if index.CheckCreateOwner(name, owner, opts.Owner) != nil {
			return sm.Result{Data: protocol.EncodeIndexOwnershipError(name, owner)}, nil, nil
		}
		// Index already present under the same owner — idempotent success so
		// Raft Apply does not stall on duplicate create proposals. Existence
		// is keyed by name only; a same-owner re-create with a divergent
		// definition is a no-op (original wins) and must be rejected at the
		// service layer before the proposal if desired.
		return sm.Result{Value: 1}, nil, nil
	}

	valueType := index.IndexValueTypeString
	if opts.Numeric {
		valueType = index.IndexValueTypeNumeric
	}

	// Sparse is carried on the config for fidelity with the command; no Index
	// implementation reads it, so nothing observable changes here.
	//
	// CaseSensitive is derived from the command instead of left at its zero
	// value. This handler used to discard the flag, so an
	// index:"case_insensitive" definition became a case-SENSITIVE index and a
	// lookup for "Alpha" over a stored "alpha" answered empty with a nil error
	// (go-qrdb#34).
	idx, idxErr := index.NewPebbleIndexWithConfig(s.db, name, index.IndexConfig{
		Name:          name,
		Unique:        opts.Unique,
		Sparse:        opts.Sparse,
		ValueType:     valueType,
		CaseSensitive: !opts.CaseInsensitive,
		Owner:         opts.Owner,
	})
	if idxErr != nil {
		return sm.Result{}, nil, idxErr
	}
	s.indexes[name] = idx
	s.indexOwners[name] = opts.Owner

	return sm.Result{Value: 1}, nil, nil
}

// pebbleHandleIndexDrop removes a secondary index and clears all its entries.
// If the index does not exist the operation is a no-op; Raft state machine
// handlers must be idempotent so that cleanup proposals (e.g. deferred test
// teardown) do not return a fatal Apply error that would stall consensus.
func (s *PebbleStateMachine) pebbleHandleIndexDrop(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	name, dropOpts, err := protocol.DecodeIndexDropOpts(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	idx, ok := s.indexes[name]
	if !ok {
		// Index already gone — idempotent success.
		return sm.Result{Value: 1}, nil, nil
	}
	// An owner-checked drop removes only its caller's index (go-qrdb#426).
	if owner := s.indexOwners[name]; index.CheckDropOwner(name, owner, dropOpts.CheckOwner, dropOpts.Owner) != nil {
		return sm.Result{Data: protocol.EncodeIndexOwnershipError(name, owner)}, nil, nil
	}

	if err := idx.Clear(); err != nil {
		return sm.Result{}, nil, err
	}
	if err := idx.Close(); err != nil {
		return sm.Result{}, nil, err
	}
	delete(s.indexes, name)
	delete(s.indexOwners, name)

	return sm.Result{Value: 1}, nil, nil
}

// ---------------------------------------------------------------------------
// Time-series handlers
// ---------------------------------------------------------------------------

// pebbleHandleTSWrite stores a single time-series data point.
func (s *PebbleStateMachine) pebbleHandleTSWrite(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	if s.ts == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "timeseries"}), nil, nil
	}

	params, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if err := s.ts.Write(params.Metric, params.Value, time.Unix(0, params.Timestamp), params.Tags); err != nil {
		return statemachine.RefusalResult(err), nil, nil
	}

	return sm.Result{Value: 1}, nil, nil
}

// pebbleHandleTSWriteBatch stores multiple time-series data points.
func (s *PebbleStateMachine) pebbleHandleTSWriteBatch(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	if s.ts == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "timeseries"}), nil, nil
	}

	params, err := protocol.DecodeTSWriteBatch(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	points := make([]timeseries.DataPoint, len(params))
	for i, p := range params {
		points[i] = timeseries.DataPoint{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: time.Unix(0, p.Timestamp),
			Tags:      p.Tags,
		}
	}

	if err := s.ts.WriteBatch(points); err != nil {
		return statemachine.RefusalResult(err), nil, nil
	}

	return sm.Result{Value: uint64(len(points))}, nil, nil
}

// pebbleHandleTSWritePoint stores a single validated time-series data point.
func (s *PebbleStateMachine) pebbleHandleTSWritePoint(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	if s.ts == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "timeseries"}), nil, nil
	}

	params, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	dp := &timeseries.DataPoint{
		Metric:    params.Metric,
		Value:     params.Value,
		Timestamp: time.Unix(0, params.Timestamp),
		Tags:      params.Tags,
	}
	if err := s.ts.WritePoint(context.Background(), dp); err != nil {
		return statemachine.RefusalResult(err), nil, nil
	}

	return sm.Result{Value: 1}, nil, nil
}

// pebbleHandleTSDeleteSeries deletes time-series data matching criteria.
func (s *PebbleStateMachine) pebbleHandleTSDeleteSeries(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	if s.ts == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "timeseries"}), nil, nil
	}

	params, err := protocol.DecodeTSDeleteSeries(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	req := &timeseries.DeleteRequest{
		Metric: params.Metric,
		Tags:   params.Tags,
	}
	if params.StartNs != 0 {
		req.StartTime = time.Unix(0, params.StartNs)
	}
	if params.EndNs != 0 {
		req.EndTime = time.Unix(0, params.EndNs)
	}

	if err := s.ts.DeleteSeries(context.Background(), req); err != nil {
		return statemachine.RefusalResult(err), nil, nil
	}

	return sm.Result{Value: 1}, nil, nil
}

// pebbleHandleTSSetRetention sets the retention policy for a metric.
func (s *PebbleStateMachine) pebbleHandleTSSetRetention(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	if s.ts == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "timeseries"}), nil, nil
	}

	params, err := protocol.DecodeTSSetRetention(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	policy := &timeseries.RetentionPolicy{
		Duration:    time.Duration(params.DurationNs),
		Resolution:  time.Duration(params.ResolutionNs),
		Aggregation: timeseries.AggFunc(params.Aggregation),
	}

	if err := s.ts.SetRetentionPolicy(params.Metric, policy); err != nil {
		return statemachine.RefusalResult(err), nil, nil
	}

	return sm.Result{Value: 1}, nil, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// baseKVOps is the set of opcodes whose handlers decode through
// kv.DecodeCommand and therefore need the full command, op byte included.
var baseKVOps = map[byte]struct{}{
	kv.OpPut:         {},
	kv.OpGet:         {},
	kv.OpDelete:      {},
	kv.OpPutIfAbsent: {},
}

// isBaseKVOp reports whether op belongs to the base KV protocol.
func isBaseKVOp(op byte) bool {
	_, ok := baseKVOps[op]
	return ok
}

// keyIsAbsentInBatch answers the existence guard both conditional-put handlers
// ask, distinguishing a MISSING key from a key that could not be READ.
//
// Only *kv.KeyNotFoundError means absent. Any other error is a real read
// fault and is returned to the caller, which returns it to the Raft layer:
// folding it into "absent" would overwrite the holder and report
// ResultStored, and because such a fault is LOCAL to one replica, replicas
// whose reads succeeded would decline the same entry this one stored and the
// group would diverge from an identical log.
//
// It exists as a helper because the base-KV and MVCC conditional puts must
// classify identically. They did not: the base-KV handler was corrected first
// and the MVCC one kept reading every error as absence, which is exactly the
// drift a shared helper prevents.
func (s *PebbleStateMachine) keyIsAbsentInBatch(batch *pebble.Batch, key []byte) (bool, error) {
	if _, err := s.getKeyInBatch(batch, key); err != nil {
		var notFound *kv.KeyNotFoundError
		if errors.As(err, &notFound) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// getKeyInBatch retrieves a key's value as the apply batch sees it: the
// batch's own uncommitted writes layered over the database. Every read taken
// while applying entries must go through here, so that an entry observes the
// writes of the entries the apply loop packed ahead of it in the same batch.
// Reading the database directly would make that packing — which is decided by
// each node's local timing — visible in the resulting state.
func (s *PebbleStateMachine) getKeyInBatch(batch *pebble.Batch, key []byte) ([]byte, error) {
	val, closer, err := batch.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, &kv.KeyNotFoundError{Key: string(key)}
		}
		return nil, &ReadError{Key: string(key), Err: err}
	}
	defer func() { _ = closer.Close() }()

	cp := make([]byte, len(val))
	copy(cp, val)
	return cp, nil
}

// Compile-time assertions.
var (
	_ sm.DiskStateMachine = (*PebbleStateMachine)(nil)
	_ sm.Hasher           = (*PebbleStateMachine)(nil)
	_ sm.NALookup         = (*PebbleStateMachine)(nil)
)
