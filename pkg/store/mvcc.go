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
	"context"
	"encoding/binary"
	"io"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// MVCC query types are defined in pkg/service/types.go:
//   service.MVCCGetQuery, service.MVCCGetAtRevisionQuery,
//   service.MVCCRangeQuery, service.MVCCRevisionQuery, service.MVCCRangeResult

// ---------------------------------------------------------------------------
// Update handler type
// ---------------------------------------------------------------------------

// watchNotification captures a single change event for delivery to the
// watch hub after the PebbleDB batch is committed.
type watchNotification struct {
	eventType service.EventType
	key       []byte
	kv        *service.KeyValue // new state after the operation
	prevKv    *service.KeyValue // previous state before the operation (may be nil)
}

// mvccUpdateHandler processes a single decoded command within a PebbleDB batch.
// The handler receives the MVCC store, the lessor (may be nil), the batch,
// and the command payload. It returns the result, any watch notifications,
// and an error.
type mvccUpdateHandler func(s *mvcc.Store, lr *lease.Lessor, batch *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error)

// ---------------------------------------------------------------------------
// MVCCStateMachine
// ---------------------------------------------------------------------------

// MVCCStateMachine implements sm.DiskStateMachine backed by an MVCC-versioned
// PebbleDB store. Every write creates a new revision while retaining historical
// versions for point-in-time reads. Commands are dispatched via the extended
// wire protocol defined in the protocol package.
//
// Thread safety: Lookup may be called concurrently with Update. PebbleDB
// handles its own internal synchronization for concurrent reads during writes.
type MVCCStateMachine struct {
	mu     sync.RWMutex
	db     *pebble.DB
	store  *mvcc.Store
	dir    string
	opts   *pebble.Options
	hub    *watch.Hub
	lessor *lease.Lessor

	// indexes holds registered secondary indexes by name.
	indexes map[string]index.Index

	// ts is the time-series storage engine. Nil if not configured.
	ts *timeseries.Storage

	// updateHandlers maps op codes to their update handlers.
	updateHandlers map[byte]mvccUpdateHandler
}

// NewMVCCStateMachine creates a new MVCCStateMachine with the given PebbleDB
// options. If opts is nil, default options are used. The state machine must
// be opened via Open() before use.
func NewMVCCStateMachine(opts *pebble.Options) *MVCCStateMachine {
	if opts == nil {
		opts = &pebble.Options{}
	}
	s := &MVCCStateMachine{
		opts:    opts,
		indexes: make(map[string]index.Index),
	}
	s.updateHandlers = map[byte]mvccUpdateHandler{
		protocol.OpMVCCPut:        handleMVCCPut,
		protocol.OpMVCCDelete:     handleMVCCDelete,
		protocol.OpMVCCCompact:    handleMVCCCompact,
		protocol.OpTransaction:    handleTransaction,
		protocol.OpBatch:          handleBatch,
		protocol.OpLeaseGrant:     handleLeaseGrant,
		protocol.OpLeaseRevoke:    handleLeaseRevoke,
		protocol.OpLeaseKeepAlive: handleLeaseKeepAlive,
		protocol.OpIndexCreate:    s.handleIndexCreate,
		protocol.OpIndexDrop:      s.handleIndexDrop,
		protocol.OpTSWrite:        s.handleTSWrite,
		protocol.OpTSWriteBatch:   s.handleTSWriteBatch,
	}
	return s
}

// Store returns the underlying MVCC store. Returns nil if the state machine
// has not been opened.
func (s *MVCCStateMachine) Store() *mvcc.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store
}

// SetWatchHub attaches a watch hub to the state machine. When set,
// the state machine will emit watch notifications after each committed
// batch. Must be called before the state machine is opened.
func (s *MVCCStateMachine) SetWatchHub(hub *watch.Hub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hub = hub
}

// SetLessor attaches a lessor to the state machine. When set, lease
// grant/revoke/keep-alive commands are processed, and key-lease
// attachments are tracked. Must be called before the state machine
// is opened.
func (s *MVCCStateMachine) SetLessor(lr *lease.Lessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lessor = lr
}

// Lessor returns the attached lessor. Returns nil if no lessor is set.
func (s *MVCCStateMachine) Lessor() *lease.Lessor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lessor
}

// SetTimeSeries attaches a time-series storage engine to the state machine.
// Must be called before the state machine is opened.
func (s *MVCCStateMachine) SetTimeSeries(ts *timeseries.Storage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ts = ts
}

// TimeSeries returns the attached time-series storage. Returns nil if
// not configured.
func (s *MVCCStateMachine) TimeSeries() *timeseries.Storage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ts
}

// Open initializes the PebbleDB database and MVCC store, returning the last
// applied Raft log index for crash recovery. Returns 0 for a fresh database.
func (s *MVCCStateMachine) Open(_ context.Context, initialDir string, _ <-chan struct{}) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := pebble.Open(initialDir, s.opts)
	if err != nil {
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.db = db
	s.dir = initialDir

	store, err := mvcc.New(db)
	if err != nil {
		_ = db.Close()
		s.db = nil
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.store = store

	lastApplied, err := s.readLastApplied()
	if err != nil {
		_ = db.Close()
		s.db = nil
		s.store = nil
		return 0, &ReadError{Key: metaKeyLastApplied, Err: err}
	}

	return lastApplied, nil
}

// readLastApplied reads the persisted last applied index from the metadata key.
func (s *MVCCStateMachine) readLastApplied() (uint64, error) {
	val, closer, err := s.db.Get([]byte(metaKeyLastApplied))
	if err != nil {
		if err == pebble.ErrNotFound {
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

// Update applies committed entries to the MVCC store using an atomic PebbleDB
// batch. Each entry is decoded using the extended wire protocol and dispatched
// to the appropriate handler. Write operations (Put, Delete) each allocate a
// new MVCC revision. The last applied index is persisted in the same batch
// for crash recovery.
func (s *MVCCStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	var lastIndex uint64
	var notifications []watchNotification

	for i, e := range entries {
		cmd, err := protocol.DecodeCommand(e.Cmd)
		if err != nil {
			return err
		}

		handler, ok := s.updateHandlers[cmd.Op]
		if !ok {
			return &protocol.UnknownOpError{Op: cmd.Op}
		}

		result, notifs, err := handler(s.store, s.lessor, batch, cmd.Payload)
		if err != nil {
			return err
		}
		results[i] = result

		if len(notifs) > 0 {
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
			case service.EventPut:
				s.hub.NotifyPut(n.key, n.kv, n.prevKv)
			case service.EventDelete:
				s.hub.NotifyDelete(n.key, n.kv, n.prevKv)
			}
		}
	}

	return nil
}

// Lookup reads state from the MVCC store. Supports the following query types:
//   - service.MVCCGetQuery: returns *service.KeyValue for the latest value
//   - service.MVCCGetAtRevisionQuery: returns *service.KeyValue at a historical revision
//   - service.MVCCRangeQuery: returns *service.MVCCRangeResult with key-values and revision
//   - service.MVCCRevisionQuery: returns int64 current revision
//   - []byte: returns *service.KeyValue (convenience for simple key lookup)
//   - string: returns *service.KeyValue (convenience for simple key lookup)
func (s *MVCCStateMachine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	switch q := query.(type) {
	case service.MVCCGetQuery:
		return s.store.Get(q.Key)

	case service.MVCCGetAtRevisionQuery:
		return s.store.GetAtRevision(q.Key, q.Revision)

	case service.MVCCRangeQuery:
		kvs, rev, err := s.store.Range(q.Start, q.End, q.Opts...)
		if err != nil {
			return nil, err
		}
		return &service.MVCCRangeResult{KeyValues: kvs, Revision: rev}, nil

	case service.MVCCRevisionQuery:
		return s.store.CurrentRevision(), nil

	case service.LeaseTimeToLiveQuery:
		if s.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		l := s.lessor.Lookup(q.ID)
		if l == nil {
			return nil, &ReadError{Key: "lease", Err: &InvalidQueryError{Reason: "lease not found"}}
		}
		return l, nil

	case service.LeasesQuery:
		if s.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		return s.lessor.Leases(), nil

	case service.IndexQueryRequest:
		idx, ok := s.indexes[q.IndexName]
		if !ok {
			return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
		}
		return idx.Get(q.Value)

	case service.IndexScanRequest:
		idx, ok := s.indexes[q.IndexName]
		if !ok {
			return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
		}
		return idx.Scan(q.Start, q.End)

	case service.IndexListRequest:
		names := make([]string, 0, len(s.indexes))
		for name := range s.indexes {
			names = append(names, name)
		}
		return names, nil

	case service.TSQueryRequest:
		if s.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return s.ts.Query(q.Metric, q.Start, q.End, q.Opts...)

	case service.TSAggregateRequest:
		if s.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return s.ts.Aggregate(q.Metric, q.Start, q.End, q.Func, q.Interval)

	case []byte:
		return s.store.Get(q)

	case string:
		return s.store.Get([]byte(q))

	default:
		return nil, &InvalidQueryError{Reason: "unsupported query type"}
	}
}

// Sync flushes all pending writes to durable storage.
func (s *MVCCStateMachine) Sync() error {
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

// PrepareSnapshot creates a point-in-time snapshot of the database.
func (s *MVCCStateMachine) PrepareSnapshot() (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	return s.db.NewSnapshot(), nil
}

// SaveSnapshot serializes the database state captured by PrepareSnapshot
// to the writer. Format: [count:4 LE] then for each pair:
// [keyLen:4 LE][key][valLen:4 LE][val]. Internal metadata keys are excluded.
func (s *MVCCStateMachine) SaveSnapshot(_ context.Context, snapshotCtx interface{}, w io.Writer, stopper <-chan struct{}) error {
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

	var count uint32
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		count++
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return &SnapshotError{Op: "save", Err: err}
	}
	_ = iter.Close()

	if err := binary.Write(w, binary.LittleEndian, count); err != nil {
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

		if err := binary.Write(w, binary.LittleEndian, uint32(len(k))); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
		if _, err := w.Write(k); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
		if err := binary.Write(w, binary.LittleEndian, uint32(len(v))); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
		if _, err := w.Write(v); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
	}

	return iter.Error()
}

// RecoverFromSnapshot restores the database state from the reader, replacing
// all existing data. After recovery, the MVCC store is re-initialized to
// reflect the restored revision and compacted markers.
func (s *MVCCStateMachine) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
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

	// Re-initialize the MVCC store to load revision/compacted from restored data.
	store, err := mvcc.New(s.db)
	if err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}
	s.store = store

	return nil
}

// clearAll removes all entries from the database using a range deletion.
func (s *MVCCStateMachine) clearAll() error {
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
func (s *MVCCStateMachine) Close(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	err := s.db.Close()
	s.db = nil
	s.store = nil
	return err
}

// GetHash computes a deterministic xxhash64 hash of all user key-value pairs
// in sorted order. Internal metadata keys are excluded.
func (s *MVCCStateMachine) GetHash() (uint64, error) {
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
// Update handlers
// ---------------------------------------------------------------------------

// handleMVCCPut processes an MVCC Put command. Allocates a new revision and
// writes the key-value pair with metadata into the batch.
func handleMVCCPut(s *mvcc.Store, lr *lease.Lessor, batch *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Capture previous value for watch notifications.
	prevKv, _ := s.Get(key)

	kv, err := s.PutInBatch(batch, key, value, leaseID, s.CurrentRevision()+1)
	if err != nil {
		return sm.Result{}, nil, err
	}
	s.SetRevision(kv.ModRevision)

	// Track key-lease attachment.
	if lr != nil && leaseID > 0 {
		lr.AttachKey(leaseID, key)
	}

	notifs := []watchNotification{{
		eventType: service.EventPut,
		key:       key,
		kv:        kv,
		prevKv:    prevKv,
	}}

	return sm.Result{Value: uint64(kv.ModRevision)}, notifs, nil
}

// handleMVCCDelete processes an MVCC Delete command. Allocates a new revision
// and writes a tombstone into the batch.
func handleMVCCDelete(s *mvcc.Store, lr *lease.Lessor, batch *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	key, err := protocol.DecodeMVCCDelete(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	rev := s.CurrentRevision() + 1
	prevKv, err := s.DeleteInBatch(batch, key, rev)
	if err != nil {
		return sm.Result{}, nil, err
	}
	s.SetRevision(rev)

	// Detach key from lease if it had one.
	if lr != nil && prevKv != nil && prevKv.LeaseID > 0 {
		lr.DetachKey(prevKv.LeaseID, key)
	}

	// Build delete notification with tombstone entry.
	deleteKv := &service.KeyValue{
		Key:         key,
		ModRevision: rev,
	}
	notifs := []watchNotification{{
		eventType: service.EventDelete,
		key:       key,
		kv:        deleteKv,
		prevKv:    prevKv,
	}}

	return sm.Result{Value: uint64(rev)}, notifs, nil
}

// handleMVCCCompact processes an MVCC Compact command. Compaction runs
// directly on the store (not in the batch) since it manages its own
// iteration and deletion.
func handleMVCCCompact(s *mvcc.Store, _ *lease.Lessor, _ *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	revision, err := protocol.DecodeMVCCCompact(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if err := s.Compact(revision); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// handleTransaction processes a compare-and-swap transaction. Evaluates all
// compares against current state, then atomically executes the success or
// failure operation set at a single new revision.
func handleTransaction(s *mvcc.Store, lr *lease.Lessor, batch *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	txn, err := protocol.DecodeTransaction(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Evaluate all compares.
	succeeded := true
	for _, dc := range txn.Compares {
		cmp := decodedCompareToService(dc)

		// Look up the current value for the compare key.
		kv, _ := s.Get(cmp.Key)
		if !mvcc.Evaluate(&cmp, kv) {
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

	// Execute all operations at a single revision.
	rev := s.CurrentRevision() + 1
	var notifs []watchNotification
	for _, op := range ops {
		opNotifs, err := executeDecodedOpWithNotify(s, lr, batch, op, rev)
		if err != nil {
			return sm.Result{}, nil, err
		}
		notifs = append(notifs, opNotifs...)
	}
	if len(ops) > 0 {
		s.SetRevision(rev)
	}

	// Encode result: Value = revision, Data[0] = succeeded flag.
	var data [1]byte
	if succeeded {
		data[0] = 1
	}
	return sm.Result{Value: uint64(rev), Data: data[:]}, notifs, nil
}

// handleBatch processes a batch of operations. All operations are executed
// atomically at a single revision.
func handleBatch(s *mvcc.Store, lr *lease.Lessor, batch *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	rev := s.CurrentRevision() + 1
	var notifs []watchNotification
	for _, op := range ops {
		opNotifs, err := executeDecodedOpWithNotify(s, lr, batch, op, rev)
		if err != nil {
			return sm.Result{}, nil, err
		}
		notifs = append(notifs, opNotifs...)
	}
	if len(ops) > 0 {
		s.SetRevision(rev)
	}

	return sm.Result{Value: uint64(rev)}, notifs, nil
}

// ---------------------------------------------------------------------------
// Transaction/batch helpers
// ---------------------------------------------------------------------------

// decodedCompareToService converts a protocol.DecodedCompare to a service.Compare.
func decodedCompareToService(dc protocol.DecodedCompare) service.Compare {
	return service.Compare{
		Key:            dc.Key,
		Target:         service.CompareTarget(dc.Target),
		Result:         service.CompareResult(dc.Result),
		Version:        dc.Version,
		CreateRevision: dc.CreateRevision,
		ModRevision:    dc.ModRevision,
		Value:          dc.Value,
	}
}

// executeDecodedOpWithNotify executes a single decoded operation (Put or
// Delete) within a batch at the given revision and returns watch
// notifications. Used by both transaction and batch handlers.
func executeDecodedOpWithNotify(s *mvcc.Store, lr *lease.Lessor, batch *pebble.Batch, op protocol.DecodedOp, rev int64) ([]watchNotification, error) {
	switch op.Type {
	case protocol.OpMVCCPut:
		prevKv, _ := s.Get(op.Key)
		kv, err := s.PutInBatch(batch, op.Key, op.Value, op.LeaseID, rev)
		if err != nil {
			return nil, err
		}
		if lr != nil && op.LeaseID > 0 {
			lr.AttachKey(op.LeaseID, op.Key)
		}
		return []watchNotification{{
			eventType: service.EventPut,
			key:       op.Key,
			kv:        kv,
			prevKv:    prevKv,
		}}, nil

	case protocol.OpMVCCDelete:
		prevKv, err := s.DeleteInBatch(batch, op.Key, rev)
		if err != nil {
			return nil, err
		}
		if lr != nil && prevKv != nil && prevKv.LeaseID > 0 {
			lr.DetachKey(prevKv.LeaseID, op.Key)
		}
		deleteKv := &service.KeyValue{
			Key:         op.Key,
			ModRevision: rev,
		}
		return []watchNotification{{
			eventType: service.EventDelete,
			key:       op.Key,
			kv:        deleteKv,
			prevKv:    prevKv,
		}}, nil

	default:
		return nil, &protocol.UnknownOpError{Op: op.Type}
	}
}

// ---------------------------------------------------------------------------
// Lease handlers
// ---------------------------------------------------------------------------

// handleLeaseGrant creates a new lease with the specified TTL. The
// lease ID is auto-generated by the lessor. Returns the lease ID in
// sm.Result.Value.
func handleLeaseGrant(_ *mvcc.Store, lr *lease.Lessor, _ *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if lr == nil {
		return sm.Result{}, nil, &NotOpenError{}
	}

	info, err := lr.Grant(ttl)
	if err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: uint64(info.ID)}, nil, nil
}

// handleLeaseRevoke expires a lease and returns the attached keys for
// deletion. The caller (Update method) is responsible for deleting the
// keys through additional batch operations. Returns the number of
// detached keys in sm.Result.Value.
func handleLeaseRevoke(s *mvcc.Store, lr *lease.Lessor, batch *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if lr == nil {
		return sm.Result{}, nil, &NotOpenError{}
	}

	keys, err := lr.Revoke(leaseID)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Delete all attached keys at a new revision.
	var notifs []watchNotification
	if len(keys) > 0 {
		rev := s.CurrentRevision() + 1
		for _, key := range keys {
			prevKv, delErr := s.DeleteInBatch(batch, key, rev)
			if delErr != nil {
				continue // key may already be deleted
			}
			deleteKv := &service.KeyValue{
				Key:         key,
				ModRevision: rev,
			}
			notifs = append(notifs, watchNotification{
				eventType: service.EventDelete,
				key:       key,
				kv:        deleteKv,
				prevKv:    prevKv,
			})
		}
		if len(notifs) > 0 {
			s.SetRevision(rev)
		}
	}

	return sm.Result{Value: uint64(len(keys))}, notifs, nil
}

// handleLeaseKeepAlive resets a lease's TTL. Returns the lease ID in
// sm.Result.Value.
func handleLeaseKeepAlive(_ *mvcc.Store, lr *lease.Lessor, _ *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if lr == nil {
		return sm.Result{}, nil, &NotOpenError{}
	}

	_, err = lr.KeepAlive(leaseID)
	if err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: uint64(leaseID)}, nil, nil
}

// ---------------------------------------------------------------------------
// Index handlers
// ---------------------------------------------------------------------------

// handleIndexCreate creates a new secondary index. The index is backed by
// the same PebbleDB instance as the MVCC store. Returns 1 in
// sm.Result.Value on success.
func (s *MVCCStateMachine) handleIndexCreate(_ *mvcc.Store, _ *lease.Lessor, _ *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	name, _, unique, sparse, err := protocol.DecodeIndexCreate(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}
	_ = sparse // reserved for future use

	if _, exists := s.indexes[name]; exists {
		return sm.Result{}, nil, &InvalidQueryError{Reason: "index already exists: " + name}
	}

	idx, err := index.NewPebbleIndex(s.db, name, unique)
	if err != nil {
		return sm.Result{}, nil, err
	}
	s.indexes[name] = idx

	return sm.Result{Value: 1}, nil, nil
}

// handleIndexDrop removes a secondary index and clears all its entries.
// Returns 1 in sm.Result.Value on success.
func (s *MVCCStateMachine) handleIndexDrop(_ *mvcc.Store, _ *lease.Lessor, _ *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	name, err := protocol.DecodeIndexDrop(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	idx, ok := s.indexes[name]
	if !ok {
		return sm.Result{}, nil, &InvalidQueryError{Reason: "index not found: " + name}
	}

	if err := idx.Clear(); err != nil {
		return sm.Result{}, nil, err
	}
	if err := idx.Close(); err != nil {
		return sm.Result{}, nil, err
	}
	delete(s.indexes, name)

	return sm.Result{Value: 1}, nil, nil
}

// ---------------------------------------------------------------------------
// Time-series handlers
// ---------------------------------------------------------------------------

// handleTSWrite stores a single time-series data point.
func (s *MVCCStateMachine) handleTSWrite(_ *mvcc.Store, _ *lease.Lessor, _ *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	if s.ts == nil {
		return sm.Result{}, nil, &NotOpenError{}
	}

	params, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if err := s.ts.Write(params.Metric, params.Value, tsTimestampFromNano(params.Timestamp), params.Tags); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// handleTSWriteBatch stores multiple time-series data points atomically.
func (s *MVCCStateMachine) handleTSWriteBatch(_ *mvcc.Store, _ *lease.Lessor, _ *pebble.Batch, payload []byte) (sm.Result, []watchNotification, error) {
	if s.ts == nil {
		return sm.Result{}, nil, &NotOpenError{}
	}

	params, err := protocol.DecodeTSWriteBatch(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	points := make([]service.DataPoint, len(params))
	for i, p := range params {
		points[i] = service.DataPoint{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: tsTimestampFromNano(p.Timestamp),
			Tags:      p.Tags,
		}
	}

	if err := s.ts.WriteBatch(points); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: uint64(len(points))}, nil, nil
}

// tsTimestampFromNano converts a nanosecond Unix timestamp to time.Time.
func tsTimestampFromNano(ns int64) time.Time {
	return time.Unix(0, ns)
}

// Compile-time assertions.
var (
	_ sm.DiskStateMachine = (*MVCCStateMachine)(nil)
	_ sm.Hasher           = (*MVCCStateMachine)(nil)
)
