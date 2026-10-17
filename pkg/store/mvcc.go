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
	"log/slog"
	"sync"

	"github.com/cespare/xxhash/v2"
	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// MVCCStateMachine
// ---------------------------------------------------------------------------

// MVCCStateMachine implements sm.DiskStateMachine as a thin Raft wrapper
// around MVCCEngine. All handler dispatch, query evaluation, and subsystem
// management is delegated to the engine. This state machine adds only the
// Raft-specific concerns: opening/closing the PebbleDB database, persisting
// the last applied log index, snapshot save/restore, and synchronization.
//
// Thread safety: Lookup may be called concurrently with Update. The engine
// and PebbleDB handle their own internal synchronization for concurrent
// reads during writes.
type MVCCStateMachine struct {
	mu      sync.RWMutex
	db      *pebble.DB
	storage storage.Storage
	engine  *engine.MVCCEngine
	dir     string
	opts    *pebble.Options

	// Pre-Open configuration stored until Open() is called.
	hub              *watch.Hub
	lessor           *lease.Lessor
	logs             *vlog.Chain
	enableTimeSeries bool

	// noSync disables fsync on PebbleDB writes. When true, batch.Commit()
	// does not wait for the OS to flush data to disk. This trades durability
	// for performance and is appropriate only for testing environments.
	noSync bool
}

// pebbleSyncs times the synced commits of a storage wrapper over the state
// machine database in dir, whose path names the shard in a slow-sync
// warning.
func pebbleSyncs(dir string) pebbleStorage.Option {
	return pebbleStorage.WithSyncObserver(syncwatch.ForStorage(syncwatch.BackendPebble, syncwatch.WithAttrs(slog.String("dir", dir))))
}

// NewMVCCStateMachine creates a new MVCCStateMachine with the given PebbleDB
// options. If opts is nil, default options are used. The state machine must
// be opened via Open() before use.
func NewMVCCStateMachine(opts *pebble.Options) *MVCCStateMachine {
	if opts == nil {
		opts = &pebble.Options{}
	}
	return &MVCCStateMachine{
		opts: opts,
	}
}

// Store returns the underlying MVCC store. Returns nil if the state machine
// has not been opened.
func (s *MVCCStateMachine) Store() *mvcc.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engine != nil {
		return s.engine.Store()
	}
	return nil
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
	if s.engine != nil {
		return s.engine.Lessor()
	}
	return s.lessor
}

// SetLogs attaches the state machine's log registry: the named verifiable
// logs protocol.OpVerifiableLog applies to and the audit instance
// protocol.OpAuditAppend applies to. Must be called before the state machine
// is opened.
func (s *MVCCStateMachine) SetLogs(l *vlog.Chain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = l
}

// Logs returns the attached log registry. Returns nil if none is configured.
func (s *MVCCStateMachine) Logs() *vlog.Chain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs
}

// AuditChain returns the audit instance of the attached log registry.
// Returns nil if none is configured.
func (s *MVCCStateMachine) AuditChain() *audit.Chain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engine != nil {
		return s.engine.AuditChain()
	}
	return audit.ChainOn(s.logs)
}

// SetTimeSeries attaches a time-series storage engine to the state machine.
// Must be called before the state machine is opened.
func (s *MVCCStateMachine) SetTimeSeries(ts *timeseries.Storage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enableTimeSeries = ts != nil
}

// TimeSeries returns the attached time-series storage. Returns nil if
// not configured.
func (s *MVCCStateMachine) TimeSeries() *timeseries.Storage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engine != nil {
		return s.engine.TimeSeries()
	}
	return nil
}

// Open initializes the PebbleDB database, creates the MVCCEngine with all
// configured subsystems, and returns the last applied Raft log index for
// crash recovery. Returns 0 for a fresh database.
func (s *MVCCStateMachine) Open(_ context.Context, initialDir string, _ <-chan struct{}) (uint64, error) {
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

	// Wrap the pebble.DB in a storage.Storage interface for the MVCC store.
	// syncWrites=true for durability by default; noSync=true disables fsync
	// (suitable for testing where fsync latency causes proposal timeouts).
	syncWrites := !s.noSync
	s.storage = pebbleStorage.NewFromDB(db, syncWrites, pebbleSyncs(initialDir))

	// Build engine options from pre-Open configuration.
	var engineOpts []engine.MVCCOption
	if s.hub != nil {
		engineOpts = append(engineOpts, engine.WithMVCCWatchHub(s.hub))
	}
	if s.lessor != nil {
		engineOpts = append(engineOpts, engine.WithMVCCLessor(s.lessor))
	}
	if s.logs != nil {
		engineOpts = append(engineOpts, engine.WithMVCCLogs(s.logs))
	}
	// Create time-series storage if enabled. It rides the same storage.Storage
	// the engine itself is built over, so a time-series write lands in the
	// same apply batch as everything else the entry does.
	if s.enableTimeSeries {
		ts, tsErr := timeseries.New(s.storage)
		if tsErr != nil {
			_ = db.Close()
			s.db = nil
			s.storage = nil
			return 0, &OpenError{Dir: initialDir, Err: tsErr}
		}
		engineOpts = append(engineOpts, engine.WithMVCCTimeSeries(ts))
	}

	e, err := engine.NewMVCCEngine(s.storage, engineOpts...)
	if err != nil {
		_ = db.Close()
		s.db = nil
		s.storage = nil
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.engine = e

	lastApplied, err := s.readLastApplied()
	if err != nil {
		_ = e.Close(context.Background())
		_ = db.Close()
		s.db = nil
		s.storage = nil
		s.engine = nil
		return 0, &ReadError{Key: metaKeyLastApplied, Err: err}
	}

	return lastApplied, nil
}

// readLastApplied reads the persisted last applied index from the metadata key.
func (s *MVCCStateMachine) readLastApplied() (uint64, error) {
	val, err := s.storage.Get([]byte(metaKeyLastApplied))
	if err != nil {
		return 0, err
	}
	return decodeLastApplied(val), nil
}

// Update applies committed entries to the MVCC store using an atomic PebbleDB
// batch. Each entry is decoded by the engine and dispatched to the appropriate
// handler. The revision counter and last applied index are persisted in the
// same batch for crash recovery. Watch notifications are delivered after the
// batch is committed.
func (s *MVCCStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	if err := s.update(entries, results); err != nil {
		return s.resyncLogs(err)
	}
	return nil
}

// resyncLogs rebuilds the log registry from committed storage after an
// Update that did not commit, exactly as PebbleStateMachine.resyncLogs.
func (s *MVCCStateMachine) resyncLogs(applyErr error) error {
	if s.logs == nil {
		return applyErr
	}
	if err := s.logs.Restore(s.storage); err != nil {
		return &LogsResyncError{ApplyErr: applyErr, RestoreErr: err}
	}
	return applyErr
}

// update applies entries in one batch; Update holds s.mu.
func (s *MVCCStateMachine) update(entries []sm.Entry, results []sm.Result) error {
	batch := s.storage.Batch()
	defer func() { _ = batch.Close() }()

	notifications, vectorEffects, err := s.engine.UpdateEntries(batch, entries, results)
	if err != nil {
		return err
	}

	// Persist the revision counter in the same batch.
	rev := s.engine.Store().CurrentRevision()
	if rev > 0 {
		if err := batch.Set(mvcc.RevisionKey(), mvcc.EncodeRevision(rev)); err != nil {
			return &WriteError{Err: err}
		}
	}

	// Persist the last applied index in the same atomic batch.
	var lastIndex uint64
	for _, e := range entries {
		if e.Index > lastIndex {
			lastIndex = e.Index
		}
	}
	if lastIndex > 0 {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], lastIndex)
		if err := batch.Set([]byte(metaKeyLastApplied), buf[:]); err != nil {
			return &WriteError{Err: err}
		}
	}

	if err := batch.Commit(); err != nil {
		return &WriteError{Err: err}
	}
	s.engine.Store().MarkCommitted(rev)

	// Deliver watch notifications and apply vector index effects after the
	// batch is committed.
	s.engine.DeliverNotifications(notifications)
	s.engine.ApplyVectorIndexEffects(vectorEffects)

	return nil
}

// Lookup reads state from the MVCC store by delegating to the engine.
// See engine.MVCCEngine.Lookup for supported query types. A
// statedigest.Query is answered here, from a snapshot; see stateDigest.
func (s *MVCCStateMachine) Lookup(ctx context.Context, query interface{}) (interface{}, error) {
	if _, ok := query.(statedigest.Query); ok {
		return s.stateDigest()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	return s.engine.Lookup(ctx, query)
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

	var counter snapshotEntryCounter
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if isNodeLocalKey(k) {
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
		if isNodeLocalKey(k) {
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
// all existing data. After recovery, the engine is re-created to reflect the
// restored revision and compacted markers.
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

	batch := s.storage.Batch()
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

		if err := batch.Set(key, val); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}
	}

	if err := batch.Commit(); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	// Close the old engine before re-creating.
	if s.engine != nil {
		_ = s.engine.Close(context.Background())
	}

	// Re-create engine to load revision/compacted from restored data.
	var engineOpts []engine.MVCCOption
	if s.hub != nil {
		engineOpts = append(engineOpts, engine.WithMVCCWatchHub(s.hub))
	}
	if s.lessor != nil {
		engineOpts = append(engineOpts, engine.WithMVCCLessor(s.lessor))
	}
	if s.logs != nil {
		engineOpts = append(engineOpts, engine.WithMVCCLogs(s.logs))
	}
	if s.engine != nil && s.engine.TimeSeries() != nil {
		engineOpts = append(engineOpts, engine.WithMVCCTimeSeries(s.engine.TimeSeries()))
	}

	e, err := engine.NewMVCCEngine(s.storage, engineOpts...)
	if err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}
	s.engine = e

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

// Close releases all resources held by the engine and PebbleDB store.
func (s *MVCCStateMachine) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	if s.engine != nil {
		_ = s.engine.Close(ctx)
		s.engine = nil
	}

	err := s.db.Close()
	s.db = nil
	s.storage = nil
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

	iter := s.storage.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if isNodeLocalKey(k) {
			continue
		}
		v := iter.Value()
		_, _ = h.Write(k)
		_, _ = h.Write(v)
	}
	if err := iter.Error(); err != nil {
		return 0, &ReadError{Key: "", Err: err}
	}

	return h.Sum64(), nil
}

// Compile-time assertions.
var (
	_ sm.DiskStateMachine = (*MVCCStateMachine)(nil)
	_ sm.Hasher           = (*MVCCStateMachine)(nil)
)
