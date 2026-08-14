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
)

// RegularOption configures a RegularStateMachine at construction time.
type RegularOption func(*RegularStateMachine)

// WithRegularWatchManager attaches a WatchManager that receives key-change
// notifications for every successful write or delete.
func WithRegularWatchManager(wm WatchManager) RegularOption {
	return func(r *RegularStateMachine) {
		r.watchManager = wm
	}
}

// WithRegularLogger sets the structured logger for the state machine.
func WithRegularLogger(logger *slog.Logger) RegularOption {
	return func(r *RegularStateMachine) {
		r.logger = logger
	}
}

// WithRegularIndexManager attaches an IndexManager that handles secondary
// index create, drop, query, scan, and list operations.
func WithRegularIndexManager(im IndexManager) RegularOption {
	return func(r *RegularStateMachine) {
		r.indexManager = im
	}
}

// WithRegularLeaseManager attaches a LeaseManager that handles lease grant,
// revoke, and keep-alive operations. When not set, lease operations return
// ServiceNotConfiguredError.
func WithRegularLeaseManager(lm LeaseManager) RegularOption {
	return func(r *RegularStateMachine) {
		r.leaseManager = lm
	}
}

// WithRegularTimeSeriesStore attaches a TimeSeriesStore that handles
// time-series write, delete, retention, and query operations. When not
// set, time-series operations return ServiceNotConfiguredError.
func WithRegularTimeSeriesStore(ts TimeSeriesStore) RegularOption {
	return func(r *RegularStateMachine) {
		r.tsStore = ts
	}
}

// WithRegularMVCCStore attaches an MVCCStore that handles MVCC put, delete,
// range, compact, and transaction operations. When not set, MVCC operations
// return ServiceNotConfiguredError.
func WithRegularMVCCStore(ms MVCCStore) RegularOption {
	return func(r *RegularStateMachine) {
		r.mvccStore = ms
	}
}

// RegularStateMachine is a pure in-memory state machine that implements
// sm.StateMachine and sm.Hasher. Unlike FastStateMachine, it has no
// persistent storage layer and no sharded cache. All state is held in a
// single map protected by a read-write mutex.
//
// It supports the full protocol dispatch: basic KV, batch, MVCC,
// transactions, leases, secondary indexes, and time series.
type RegularStateMachine struct {
	mu       sync.RWMutex
	data     map[string][]byte
	revision atomic.Uint64
	handlers map[byte]updateHandler
	logger   *slog.Logger

	watchManager WatchManager
	indexManager IndexManager
	leaseManager LeaseManager
	tsStore      TimeSeriesStore
	mvccStore    MVCCStore
}

// Compile-time interface assertions.
var (
	_ sm.StateMachine = (*RegularStateMachine)(nil)
	_ sm.Hasher       = (*RegularStateMachine)(nil)
)

// NewRegularStateMachine creates a new pure in-memory state machine with
// the given optional configuration.
func NewRegularStateMachine(opts ...RegularOption) *RegularStateMachine {
	r := &RegularStateMachine{
		data:   make(map[string][]byte),
		logger: slog.Default(),
	}

	for _, opt := range opts {
		opt(r)
	}

	r.handlers = map[byte]updateHandler{
		// Basic KV
		kv.OpPut:         r.handlePut,
		kv.OpDelete:      r.handleDelete,
		protocol.OpBatch: r.handleBatch,

		// MVCC
		protocol.OpMVCCPut:     r.handleMVCCPut,
		protocol.OpMVCCDelete:  r.handleMVCCDelete,
		protocol.OpMVCCRange:   r.handleMVCCRange,
		protocol.OpMVCCCompact: r.handleMVCCCompact,

		// Transactions
		protocol.OpTransaction: r.handleTransaction,

		// Leases
		protocol.OpLeaseGrant:     r.handleLeaseGrant,
		protocol.OpLeaseRevoke:    r.handleLeaseRevoke,
		protocol.OpLeaseKeepAlive: r.handleLeaseKeepAlive,

		// Secondary indexes
		protocol.OpIndexCreate: r.handleIndexCreate,
		protocol.OpIndexDrop:   r.handleIndexDrop,

		// Time series
		protocol.OpTSWrite:        r.handleTSWrite,
		protocol.OpTSWriteBatch:   r.handleTSWriteBatch,
		protocol.OpTSWritePoint:   r.handleTSWritePoint,
		protocol.OpTSDeleteSeries: r.handleTSDeleteSeries,
		protocol.OpTSSetRetention: r.handleTSSetRetention,
	}

	return r
}

// NewRegularCreateFunc returns an sm.CreateFunc that creates
// RegularStateMachine instances with the given options. The shardID and
// replicaID parameters from QuicRaft are not used by this state machine.
func NewRegularCreateFunc(opts ...RegularOption) sm.CreateFunc {
	return sm.NewCreateFunc(func(_, _ uint64) sm.StateMachine {
		return NewRegularStateMachine(opts...)
	})
}

// ---------------------------------------------------------------------------
// sm.StateMachine interface
// ---------------------------------------------------------------------------

// Update applies committed log entries to the state machine.
func (r *RegularStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		cmd := entries[i].Cmd
		if len(cmd) == 0 {
			results[i] = sm.Result{}
			continue
		}

		op := cmd[0]
		handler, ok := r.handlers[op]
		if !ok {
			return &InvalidOpcodeError{Op: op}
		}

		revision := r.revision.Add(1)
		result, err := handler(cmd[1:], revision)
		resolved, stop, applyErr := resolveApplyError(r.logger, entries[i].Index, result, err)
		if stop {
			return applyErr
		}
		results[i] = resolved
	}
	return nil
}

// Lookup reads state without going through Raft. Supported query types:
//   - []byte: point lookup by key (returns []byte value or nil)
//   - *PrefixQuery: prefix scan returning map[string][]byte
//   - kv.ListQuery: returns sorted []string of keys matching the prefix
//   - index.IndexQueryRequest: exact-match index query
//   - index.IndexScanRequest: range index scan
//   - index.IndexListRequest: list all indexes
//   - lease.LeaseTimeToLiveQuery: lease TTL lookup
//   - lease.LeasesQuery: list all leases
//   - MVCCGetQuery: MVCC point lookup
//   - MVCCRangeQuery: MVCC range query
//   - MVCCRevisionQuery: current MVCC revision
//   - MVCCScanChangesQuery: keys changed in a revision window
//   - timeseries.TSQueryRequest: time-series range query
//   - timeseries.TSAggregateRequest: time-series aggregation
//   - timeseries.TSRichQueryRequest: rich time-series query
//   - timeseries.TSGetRetentionRequest: retention policy lookup
//   - timeseries.TSListMetricsRequest: list metrics by prefix
func (r *RegularStateMachine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	// Try index lookups.
	if result, handled, err := r.lookupIndex(query); handled {
		return result, err
	}

	// Try lease lookups.
	if result, handled, err := r.lookupLease(query); handled {
		return result, err
	}

	// Try MVCC lookups.
	if result, handled, err := r.lookupMVCC(query); handled {
		return result, err
	}

	// Try time-series lookups.
	if result, handled, err := r.lookupTimeSeries(query); handled {
		return result, err
	}

	switch q := query.(type) {
	case []byte:
		return r.lookupKey(q)
	case *PrefixQuery:
		return r.lookupPrefix(q)
	case kv.ListQuery:
		return r.listKeys(q.Prefix)
	default:
		return nil, &IncompleteDataError{Reason: "unsupported query type"}
	}
}

// SaveSnapshot serializes the in-memory map to the writer.
// A deep copy is taken under the read lock so serialization proceeds
// without blocking concurrent operations.
//
// Format: [entryCount:4 LE][entries...].
// Each entry: [keyLen:4 LE][key][valueLen:4 LE][value].
func (r *RegularStateMachine) SaveSnapshot(_ context.Context, w io.Writer, stopper <-chan struct{}) error {
	// Deep-copy under read lock.
	r.mu.RLock()
	clone := make(map[string][]byte, len(r.data))
	for k, v := range r.data {
		cp := make([]byte, len(v))
		copy(cp, v)
		clone[k] = cp
	}
	r.mu.RUnlock()

	// Write entry count.
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(clone)))
	if _, err := w.Write(countBuf); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	// Write each entry.
	buf := make([]byte, 4)
	for k, v := range clone {
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}

		// Key length + key.
		binary.LittleEndian.PutUint32(buf, uint32(len(k)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write([]byte(k)); err != nil {
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

// RecoverFromSnapshot restores the in-memory map from the reader,
// replacing all existing state.
func (r *RegularStateMachine) RecoverFromSnapshot(_ context.Context, rd io.Reader, stopper <-chan struct{}) error {
	// Read entry count.
	countBuf := make([]byte, 4)
	if _, err := io.ReadFull(rd, countBuf); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
	}
	entryCount := binary.LittleEndian.Uint32(countBuf)

	newData := make(map[string][]byte, int(entryCount))
	lenBuf := make([]byte, 4)

	for i := uint32(0); i < entryCount; i++ {
		select {
		case <-stopper:
			return &SnapshotError{Op: "recover", Inner: context.Canceled}
		default:
		}

		// Read key.
		if _, err := io.ReadFull(rd, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		keyLen := binary.LittleEndian.Uint32(lenBuf)
		keyBuf := make([]byte, keyLen)
		if _, err := io.ReadFull(rd, keyBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		// Read value.
		if _, err := io.ReadFull(rd, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		valLen := binary.LittleEndian.Uint32(lenBuf)
		valBuf := make([]byte, valLen)
		if _, err := io.ReadFull(rd, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		newData[string(keyBuf)] = valBuf
	}

	// Atomically replace the data map.
	r.mu.Lock()
	r.data = newData
	r.mu.Unlock()

	return nil
}

// Close releases all resources held by the state machine.
func (r *RegularStateMachine) Close(_ context.Context) error {
	r.mu.Lock()
	r.data = nil
	r.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// sm.Hasher interface
// ---------------------------------------------------------------------------

// GetHash computes a deterministic xxhash64 hash over all key-value pairs
// in sorted key order. This is used for cross-replica consistency
// verification.
func (r *RegularStateMachine) GetHash() (uint64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	h := xxhash.New()
	keys := make([]string, 0, len(r.data))
	for k := range r.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write(r.data[k])
	}
	return h.Sum64(), nil
}

// ---------------------------------------------------------------------------
// Internal: lookup helpers
// ---------------------------------------------------------------------------

// lookupKey performs a point lookup by key.
func (r *RegularStateMachine) lookupKey(key []byte) (interface{}, error) {
	r.mu.RLock()
	val, found := r.data[string(key)]
	r.mu.RUnlock()

	if !found {
		return nil, nil
	}

	// Return a copy to prevent callers from mutating internal state.
	cp := make([]byte, len(val))
	copy(cp, val)
	return cp, nil
}

// lookupPrefix performs a prefix scan returning all matching key-value pairs.
// When pq.Limit > 0, the scan stops after collecting that many entries.
func (r *RegularStateMachine) lookupPrefix(pq *PrefixQuery) (interface{}, error) {
	prefix := string(pq.Prefix)
	limit := pq.Limit
	if limit <= 0 {
		limit = 10000 // Default limit to prevent unbounded scans
	}

	result := make(map[string][]byte)

	r.mu.RLock()
	for k, v := range r.data {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		cp := make([]byte, len(v))
		copy(cp, v)
		result[k] = cp
		if len(result) >= limit {
			break
		}
	}
	r.mu.RUnlock()

	return result, nil
}

// listKeys returns a sorted slice of keys matching the given prefix.
// When prefix is empty, all keys are returned.
func (r *RegularStateMachine) listKeys(prefix string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var keys []string
	for k := range r.data {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

// ---------------------------------------------------------------------------
// Internal: KV write handlers
// ---------------------------------------------------------------------------

// handlePut decodes a KV Put command and writes the key-value pair.
// Payload format: [keyLen:2 LE][key][value].
func (r *RegularStateMachine) handlePut(payload []byte, revision uint64) (sm.Result, error) {
	if len(payload) < 2 {
		return sm.Result{}, &IncompleteDataError{Reason: "put: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return sm.Result{}, &IncompleteDataError{Reason: "put: key length exceeds payload"}
	}
	key := string(payload[2 : 2+keyLen])
	value := payload[2+keyLen:]

	// Make an owned copy of the value so it survives after the entry
	// buffer is recycled.
	ownedValue := make([]byte, len(value))
	copy(ownedValue, value)

	r.mu.Lock()
	r.data[key] = ownedValue
	r.mu.Unlock()

	if r.watchManager != nil {
		r.watchManager.Notify(key, ownedValue, revision, false)
	}

	return sm.Result{Value: revision}, nil
}

// handleDelete decodes a KV Delete command and removes the key.
// Payload format: [keyLen:2 LE][key].
func (r *RegularStateMachine) handleDelete(payload []byte, revision uint64) (sm.Result, error) {
	if len(payload) < 2 {
		return sm.Result{}, &IncompleteDataError{Reason: "delete: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return sm.Result{}, &IncompleteDataError{Reason: "delete: key length exceeds payload"}
	}
	key := string(payload[2 : 2+keyLen])

	r.mu.Lock()
	delete(r.data, key)
	r.mu.Unlock()

	if r.watchManager != nil {
		r.watchManager.Notify(key, nil, revision, true)
	}

	return sm.Result{Value: revision}, nil
}

// handleBatch decodes and applies a batch of operations atomically.
// Payload format: [opCount:2 LE][ops...] (protocol.DecodeBatch format).
func (r *RegularStateMachine) handleBatch(payload []byte, revision uint64) (sm.Result, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "batch: " + err.Error()}
	}

	r.mu.Lock()
	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case kv.OpPut:
			ownedValue := make([]byte, len(op.Value))
			copy(ownedValue, op.Value)
			r.data[key] = ownedValue
			if r.watchManager != nil {
				r.watchManager.Notify(key, ownedValue, revision, false)
			}
		case kv.OpDelete:
			delete(r.data, key)
			if r.watchManager != nil {
				r.watchManager.Notify(key, nil, revision, true)
			}
		default:
			r.mu.Unlock()
			return sm.Result{}, &InvalidOpcodeError{Op: op.Type}
		}
	}
	r.mu.Unlock()

	return sm.Result{Value: revision}, nil
}
