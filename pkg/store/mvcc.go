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
	"sync/atomic"

	"github.com/cespare/xxhash/v2"
	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
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
// Thread safety: Lookup may be called concurrently with Update. Lookup and
// GetHash read the published readView, never live storage, so a concurrent
// commit is invisible to them until Update publishes it.
type MVCCStateMachine struct {
	mu      sync.RWMutex
	db      *pebble.DB
	seals   *pebbleStorage.SealWatch
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

	// syncClock, when set before Open, is the clock the sync observer times
	// the apply batch's synced commits with; nil is the real clock. Tests set
	// it to hold a commit inside its sync.
	syncClock clock.Clock

	// waitMu guards waitCh, the channel the one lock notifier closes once
	// s.mu has become readable; nil while no notifier runs. Every contended
	// reader waits on the same channel, so however many readers queue and
	// give up, at most one goroutine is parked on s.mu for them. See rlock.
	waitMu sync.Mutex
	waitCh chan struct{}

	// rlockWaiters counts the lock notifiers running: goroutines rlock has
	// parked on s.mu. It is at most one.
	rlockWaiters atomic.Int64

	// rlockQueued, when set, is called by rlock once a contended reader has
	// the notifier's channel and is about to wait on it and its ctx; nil
	// does nothing. rlockWaiterDone, when set, is called as the notifier
	// ends, after it has released its read hold. Tests set them to cancel a
	// reader known to be queued and to see the notifier end.
	rlockQueued     func()
	rlockWaiterDone func()

	// scanHeld, when set, is called by a full-state scan once it holds its
	// view and its scans hold and has released s.mu, before it reads; nil
	// does nothing. closeWaiting, when set, is called by Close just before
	// it waits for the scans. Tests set them to hold a scan open across an
	// Update or a Close.
	scanHeld     func(*readView)
	closeWaiting func()

	// scans counts the full-state scans (the state digest and GetHash)
	// running outside s.mu, each holding a view. A scan joins under s.mu
	// read-held while the database is open; Close, holding s.mu for writing
	// so no scan can join, waits for it to reach zero before it closes the
	// database, so no scan's snapshot outlives the database.
	scans sync.WaitGroup

	// applySeq numbers the Updates that reach the commit, in apply order;
	// publishedSeq is the last of them published, under the write lock. See
	// Update.
	applySeq     atomic.Uint64
	publishedSeq uint64

	// view is the published state Lookup and GetHash read; see readView.
	// Open, Update, RecoverFromSnapshot and Close replace it under the write
	// lock.
	view atomic.Pointer[readView]
}

// pebbleSyncs times the synced commits of a storage wrapper over the state
// machine database in dir, whose path names the shard in a slow-sync
// warning.
func pebbleSyncs(dir string, opts ...syncwatch.Option) pebbleStorage.Option {
	opts = append([]syncwatch.Option{syncwatch.WithAttrs(slog.String("dir", dir))}, opts...)
	return pebbleStorage.WithSyncObserver(syncwatch.ForStorage(syncwatch.BackendPebble, opts...))
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

	db, seals, err := openPebble(initialDir, s.opts)
	if err != nil {
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.db = db
	s.seals = seals
	s.dir = initialDir

	// Wrap the pebble.DB in a storage.Storage interface for the MVCC store.
	// syncWrites=true for durability by default; noSync=true disables fsync
	// (suitable for testing where fsync latency causes proposal timeouts).
	syncWrites := !s.noSync
	s.storage = pebbleStorage.NewFromDB(db, syncWrites, pebbleSyncs(initialDir, syncwatch.WithClock(s.syncClock)), pebbleStorage.WithSealWatch(seals))

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
			seals.Close()
			_ = db.Close()
			s.db = nil
			s.storage = nil
			return 0, &OpenError{Dir: initialDir, Err: tsErr}
		}
		engineOpts = append(engineOpts, engine.WithMVCCTimeSeries(ts))
	}

	e, err := engine.NewMVCCEngine(s.storage, engineOpts...)
	if err != nil {
		seals.Close()
		_ = db.Close()
		s.db = nil
		s.storage = nil
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.engine = e

	lastApplied, err := s.readLastApplied()
	if err != nil {
		_ = e.Close(context.Background())
		seals.Close()
		_ = db.Close()
		s.db = nil
		s.storage = nil
		s.engine = nil
		return 0, &ReadError{Key: metaKeyLastApplied, Err: err}
	}

	if err := s.publishView(); err != nil {
		_ = e.Close(context.Background())
		_ = db.Close()
		s.db = nil
		s.storage = nil
		s.engine = nil
		return 0, &OpenError{Dir: initialDir, Err: err}
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
//
// The batch is built and committed, fsync included, under the read lock, so
// Lookup, GetHash and Sync are not held behind a slow sync. Pebble's commit
// pipeline makes a batch visible in live storage before its WAL sync
// completes, so readers never read live storage: they read the readView
// Update last published. Update then takes the write lock to publish the
// batch -- only once Commit has returned nil -- with a new view, a snapshot
// paired with the batch's revision, then MarkCommitted, the watch deliveries
// and the vector index effects. A failed commit publishes nothing.
//
// Update is not called concurrently: the go-quicraft sm.DiskStateMachine
// contract has one apply loop per shard call Update for each batch of
// committed entries in log order, and only Lookup may run beside it. applySeq
// numbers the calls in that order, and publish refuses one that is not the
// next after publishedSeq, so publications follow apply order and a broken
// contract fails loudly instead of reordering revisions.
func (s *MVCCStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	// A batch that mutates in-memory state readers consult beside the
	// published view -- the index catalog, the lessor's tables, the
	// compaction floors -- is built under the exclusive lock, as every batch
	// was before #1287: those components change in place while the batch is
	// built, so a reader running beside the build would see the change
	// before the batch is published. See RequiresExclusiveApply.
	s.mu.RLock()
	if s.db == nil {
		s.mu.RUnlock()
		return &NotOpenError{}
	}
	if s.engine.RequiresExclusiveApply(entries) {
		s.mu.RUnlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.db == nil {
			return &NotOpenError{}
		}
		seq := s.applySeq.Add(1)
		staged, applyErr := s.commit(entries, results)
		return s.finishUpdate(seq, staged, applyErr)
	}
	seq := s.applySeq.Add(1)
	staged, applyErr := s.commit(entries, results)
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishUpdate(seq, staged, applyErr)
}

// finishUpdate publishes the batch numbered seq, which commit returned as
// staged and applyErr. The caller holds s.mu exclusively.
func (s *MVCCStateMachine) finishUpdate(seq uint64, staged committedBatch, applyErr error) error {
	if seq != s.publishedSeq+1 {
		return &ApplyOrderError{Seq: seq, Published: s.publishedSeq}
	}
	s.publishedSeq = seq
	// Close may have taken the write lock between a shared-lock commit and
	// here. The commit, if it succeeded, is durable and Open reads it back.
	if s.db == nil {
		return &NotOpenError{}
	}
	if applyErr != nil {
		return s.resyncLogs(applyErr)
	}
	return s.publish(staged)
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

// committedBatch is what a committed apply batch leaves Update to publish.
type committedBatch struct {
	rev           int64
	notifications []engine.Notification
	// applyVectors applies the batch's vector index effects. It is a closure
	// over the engine's effects so that this package, which a lean standalone
	// embedder compiles, never names the vector package's types.
	applyVectors func()
}

// readView is one published state: a storage snapshot and the revision
// committed when it was taken. Readers answer from the view they acquire, so
// the data they read and the revision they treat as current always describe
// the same commit, and a batch whose Commit has not returned -- visible in
// live storage before its WAL sync completes -- is in no view.
//
// refs counts the readers holding the view plus one while it is current. The
// snapshot closes when refs reaches zero: when the last reader of a superseded
// view releases it, or when the view is superseded or Close drops it with no
// reader left.
type readView struct {
	snap storage.Snapshot
	rev  int64
	refs atomic.Int64
}

// newReadView takes a snapshot of st paired with rev, held by its publisher.
func newReadView(st storage.Storage, rev int64) (*readView, error) {
	snap, err := st.Snapshot()
	if err != nil {
		return nil, &PublishError{Rev: rev, Err: err}
	}
	v := &readView{snap: snap, rev: rev}
	v.refs.Store(1)
	return v, nil
}

// release drops one reference, closing the snapshot with the last.
func (v *readView) release() {
	if v.refs.Add(-1) == 0 {
		_ = v.snap.Close()
	}
}

// acquireView returns the current view with a reference the caller releases,
// or nil when the state machine is not open. A view whose count has already
// reached zero has been superseded and closed; the loop reloads its successor.
func (s *MVCCStateMachine) acquireView() *readView {
	for {
		v := s.view.Load()
		if v == nil {
			return nil
		}
		if n := v.refs.Load(); n > 0 && v.refs.CompareAndSwap(n, n+1) {
			return v
		}
	}
}

// setView makes v the current view and drops the publisher's reference to
// the one it supersedes. v is nil when Close drops the current view.
func (s *MVCCStateMachine) setView(v *readView) {
	if old := s.view.Swap(v); old != nil {
		old.release()
	}
}

// publishView takes a view of the state committed through the store's
// committed revision and makes it current. The caller holds s.mu for writing.
func (s *MVCCStateMachine) publishView() error {
	v, err := newReadView(s.storage, s.engine.Store().CommittedRevision())
	if err != nil {
		return err
	}
	s.setView(v)
	return nil
}

// publish makes a committed batch visible: a view of it to readers, its
// revision to revision-scoped reads, then its watch events and vector index
// effects. Update holds s.mu for writing. The view is taken first, so a
// failure to take it publishes nothing and readers keep the previous state.
func (s *MVCCStateMachine) publish(b committedBatch) error {
	v, err := newReadView(s.storage, max(b.rev, s.engine.Store().CommittedRevision()))
	if err != nil {
		return err
	}
	s.setView(v)
	s.engine.Store().MarkCommitted(b.rev)
	s.engine.DeliverNotifications(b.notifications)
	if b.applyVectors != nil {
		b.applyVectors()
	}
	return nil
}

// commit applies entries in one batch and commits it; Update holds s.mu for
// reading.
func (s *MVCCStateMachine) commit(entries []sm.Entry, results []sm.Result) (committedBatch, error) {
	batch := s.storage.Batch()
	defer func() { _ = batch.Close() }()

	notifications, vectorEffects, err := s.engine.UpdateEntries(batch, entries, results)
	if err != nil {
		return committedBatch{}, err
	}

	// Persist the revision counter in the same batch.
	rev := s.engine.Store().CurrentRevision()
	if rev > 0 {
		if err := batch.Set(mvcc.RevisionKey(), mvcc.EncodeRevision(rev)); err != nil {
			return committedBatch{}, &WriteError{Err: err}
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
			return committedBatch{}, &WriteError{Err: err}
		}
	}

	if err := batch.Commit(); err != nil {
		return committedBatch{}, &WriteError{Err: err}
	}
	return committedBatch{
		rev:           rev,
		notifications: notifications,
		applyVectors:  func() { s.engine.ApplyVectorIndexEffects(vectorEffects) },
	}, nil
}

// Lookup reads state from the MVCC store by delegating to the engine, through
// the published readView. See engine.MVCCEngine.Lookup for supported query
// types. A statedigest.Query is answered from the same view; see
// readerStateDigest.
func (s *MVCCStateMachine) Lookup(ctx context.Context, query interface{}) (interface{}, error) {
	if err := s.rlock(ctx); err != nil {
		return nil, &ReadError{Key: "", Err: err}
	}

	if _, ok := query.(statedigest.Query); ok {
		// A full scan must not hold s.mu: Update's publish waits for it.
		v, err := s.beginScan()
		s.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		defer s.endScan(v)
		s.scanning(v)
		return readerStateDigest(v.snap)
	}

	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, &NotOpenError{}
	}
	v := s.acquireView()
	if v == nil {
		return nil, &NotOpenError{}
	}
	defer v.release()
	return s.engine.LookupAt(ctx, v.snap, v.rev, query)
}

// beginScan acquires the current view for a full-state scan and joins scans,
// so Close waits for the scan before it closes the database. The caller holds
// s.mu for reading and may release it once beginScan returns; it ends the scan
// with endScan.
func (s *MVCCStateMachine) beginScan() (*readView, error) {
	if s.db == nil {
		return nil, &NotOpenError{}
	}
	v := s.acquireView()
	if v == nil {
		return nil, &NotOpenError{}
	}
	s.scans.Add(1)
	return v, nil
}

// endScan releases a scan's view, closing its snapshot if it was the last
// holder, then leaves scans.
func (s *MVCCStateMachine) endScan(v *readView) {
	v.release()
	s.scans.Done()
}

// scanning is called by a full-state scan once it has released s.mu, before
// it reads its view.
func (s *MVCCStateMachine) scanning(v *readView) {
	if s.scanHeld != nil {
		s.scanHeld(v)
	}
}

// rlock takes s.mu for reading, or returns ctx's error once ctx ends first.
// A contended reader does not queue on s.mu itself, since a goroutine
// blocked in RLock cannot be cancelled: it waits on the channel of the one
// lock notifier (see notifyReadable) and tries again once s.mu has been
// readable. A caller that gives up leaves nothing behind.
func (s *MVCCStateMachine) rlock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for {
		if s.mu.TryRLock() {
			return nil
		}
		readable := s.lockNotifier()
		if s.rlockQueued != nil {
			s.rlockQueued()
		}
		select {
		case <-readable:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// lockNotifier returns the channel the running lock notifier closes once
// s.mu is readable, starting the notifier if none runs.
func (s *MVCCStateMachine) lockNotifier() <-chan struct{} {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if s.waitCh == nil {
		s.waitCh = make(chan struct{})
		s.rlockWaiters.Add(1)
		go s.notifyReadable(s.waitCh)
	}
	return s.waitCh
}

// notifyReadable is the lock notifier: it waits until s.mu can be read,
// detaches readable while it holds s.mu, releases s.mu, and wakes every
// reader waiting on readable. Detaching first means a reader that then still
// finds s.mu taken starts the next notifier rather than waiting on a closed
// channel. Taking waitMu inside the read section is safe: lockNotifier, the
// only other holder of waitMu, never takes s.mu.
func (s *MVCCStateMachine) notifyReadable(readable chan struct{}) {
	s.mu.RLock()
	s.waitMu.Lock()
	s.waitCh = nil
	s.waitMu.Unlock()
	s.mu.RUnlock()
	close(readable)
	s.rlockWaiters.Add(-1)
	if s.rlockWaiterDone != nil {
		s.rlockWaiterDone()
	}
}

// Sync flushes all pending writes to durable storage. It takes s.mu only for
// reading: the flush is safe beside a commit, and Close is what it must
// exclude.
func (s *MVCCStateMachine) Sync() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	if err := s.seals.Flush(s.db); err != nil {
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

	if err := s.publishView(); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}
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

	// Point readers hold s.mu for reading, so none holds a view here.
	// Full-state scans hold a view outside s.mu; none can join while Close
	// holds s.mu, and Close waits for the running ones to end, so every
	// view's snapshot is closed before the database closes.
	s.setView(nil)
	if s.closeWaiting != nil {
		s.closeWaiting()
	}
	s.scans.Wait()

	if s.engine != nil {
		_ = s.engine.Close(ctx)
		s.engine = nil
	}

	s.seals.Close()
	err := s.db.Close()
	s.db = nil
	s.storage = nil
	return err
}

// GetHash computes a deterministic xxhash64 hash of all user key-value pairs
// in sorted order, read from the published readView. Internal metadata keys
// are excluded.
func (s *MVCCStateMachine) GetHash() (uint64, error) {
	// A full scan must not hold s.mu: Update's publish waits for it.
	s.mu.RLock()
	v, err := s.beginScan()
	s.mu.RUnlock()
	if err != nil {
		return 0, err
	}
	defer s.endScan(v)
	s.scanning(v)

	h := xxhash.New()

	iter := v.snap.NewIterator(nil)
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
