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
	"encoding/json"
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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/index/pebbleindex"
	qrdbkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
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
	mu    sync.RWMutex
	db    *pebble.DB
	seals *pebbleStorage.SealWatch
	dir   string
	opts  *pebble.Options

	// hub delivers watch notifications after committed batches.
	hub *watch.Hub

	// lessor manages lease lifecycle. Nil if leases are not enabled.
	lessor *lease.Lessor

	// logs is the in-memory authority for the state machine's verifiable
	// logs (protocol.OpVerifiableLog), and auditChain its audit instance
	// (protocol.OpAuditAppend). Both are nil if no registry is attached,
	// exactly as a nil lessor means leases are not enabled.
	logs       *vlog.Chain
	auditChain *audit.Chain

	// indexes holds registered secondary indexes by name.
	indexes map[string]index.Index

	// indexOwners holds the owner recorded for each index by name
	// (index.IndexConfig.Owner, go-qrdb#426). An absent entry is the empty
	// owner, the explicit index API.
	indexOwners map[string]string

	// indexFields holds the JSON field name each index was created for, by
	// index name. Required to maintain the index on every subsequent write.
	indexFields map[string]string

	// indexScopes holds the key scope each index is maintained under, by
	// index name (engine.ComposeIndexScope). A nil/absent entry is the
	// unscoped index: every key in this state machine is in scope.
	indexScopes map[string][]byte

	// indexValueTypes holds the term encoding each index was built with, by
	// index name (engine.IndexCreateValueType).
	indexValueTypes map[string]index.IndexValueType

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
		indexFields: make(map[string]string),
		indexScopes: make(map[string][]byte),

		indexValueTypes: make(map[string]index.IndexValueType),
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
		protocol.OpDeleteRange:     pebbleHandleDeleteRange,

		// Batch and transaction operations.
		protocol.OpBatch:            pebbleHandleBatch,
		protocol.OpTransaction:      pebbleHandleTransaction,
		protocol.OpCompareAndDelete: pebbleHandleCompareAndDelete,
		protocol.OpCompareAndSwap:   pebbleHandleCompareAndSwap,
		protocol.OpGuarded:          pebbleHandleGuarded,

		// Chunked import session staging (go-qrdb#576): a reserved,
		// lease-bound raw keyspace (pkg/importstage), never through the
		// base KV Put path above -- see pebbleHandleImportStage.
		protocol.OpImportStage: pebbleHandleImportStage,

		// Lease operations.
		protocol.OpLeaseGrant:      pebbleHandleLeaseGrant,
		protocol.OpLeaseRevoke:     pebbleHandleLeaseRevoke,
		protocol.OpLeaseKeepAlive:  pebbleHandleLeaseKeepAlive,
		protocol.OpLeaseCheckpoint: pebbleHandleLeaseCheckpoint,

		// Replicated audit chain (append and retention prune, one opcode,
		// two sub-types -- see protocol.OpAuditAppend).
		protocol.OpAuditAppend:   s.pebbleHandleAuditAppend,
		protocol.OpVerifiableLog: s.pebbleHandleVerifiableLog,

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

// dispatch applies one complete, non-empty command into batch through the
// handler table. It is the single dispatch point for both a log entry and the
// inner command of an OpGuarded entry, so the two can never disagree about
// which opcodes exist or how their payloads are framed.
func (s *PebbleStateMachine) dispatch(batch *pebble.Batch, cmd []byte) (sm.Result, []pebbleNotification, error) {
	op := cmd[0]
	handler, ok := s.updateHandlers[op]
	if !ok {
		return sm.Result{}, nil, &protocol.UnknownOpError{Op: op}
	}
	// Base KV handlers decode through kv.DecodeCommand and therefore need the
	// full command including its op byte; extended protocol handlers take the
	// payload with the op byte stripped. The split is by decoder, not by
	// numeric range: kv.OpPutIfAbsent sorts above kv.OpDelete but is still a
	// base KV command.
	payload := cmd
	if !isBaseKVOp(op) {
		payload = cmd[1:]
	}
	return handler(s, batch, payload)
}

// pebbleHandleGuarded applies an OpGuarded entry: the guard is read through
// the indexed batch -- the read pebbleHandleCompareAndSwap uses, which sees
// the writes of earlier entries in the same Update -- and the inner command
// is applied through dispatch only when every compare holds. A refused guard
// is a successful application that changes nothing (see protocol.OpGuarded).
func pebbleHandleGuarded(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	compares, inner, err := protocol.DecodeGuarded(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}
	// Checked BEFORE the guard is evaluated, and as a REFUSAL rather than
	// through the GuardReader below: a reserved key reaching the reader
	// would surface as *protocol.GuardReadError only on a genuine storage
	// fault, which halts apply -- the wrong answer for a condition every
	// replica reaches identically from the same committed command. A
	// compare's result is also observable to the proposer (whether it held
	// at all), so letting one evaluate against a reserved key would make an
	// ordinary guarded write an extraction oracle over state no caller may
	// read directly.
	if res, refused := statemachine.ReservedKeyRefusal(statemachine.CompareKeys(compares)...); refused {
		return res, nil, nil
	}
	refusedKey, ok, err := protocol.EvaluateGuard(compares, func(key []byte) ([]byte, error) {
		val, gerr := s.getKeyInBatch(batch, key)
		if gerr != nil {
			var notFound *kv.KeyNotFoundError
			if errors.As(gerr, &notFound) {
				return nil, nil
			}
			return nil, gerr
		}
		return val, nil
	})
	if err != nil {
		return sm.Result{}, nil, err
	}
	if !ok {
		return sm.Result{Data: protocol.EncodeGuardRefusal(refusedKey)}, nil, nil
	}
	return s.dispatch(batch, inner)
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

// SetLogs attaches the state machine's log registry to the store: the named
// verifiable logs protocol.OpVerifiableLog applies to and the audit instance
// protocol.OpAuditAppend applies to. Must be called before the store is
// opened.
func (s *PebbleStateMachine) SetLogs(l *vlog.Chain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = l
	s.auditChain = audit.ChainOn(l)
}

// Logs returns the attached log registry. Returns nil if none is set.
func (s *PebbleStateMachine) Logs() *vlog.Chain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs
}

// AuditChain returns the audit instance of the attached log registry.
// Returns nil if none is set.
func (s *PebbleStateMachine) AuditChain() *audit.Chain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.auditChain
}

// SetTimeSeries attaches a time-series storage engine to the store.
// Must be called before the store is opened.
func (s *PebbleStateMachine) SetTimeSeries(ts *timeseries.Storage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ts = ts
}

// tsAutoCommit runs fn against a fresh, synchronously-committed storage.Batch
// over this state machine's own PebbleDB, then commits it. pkg/timeseries's
// write methods take a caller-owned batch so a Raft apply loop can share one
// across an entry's whole effect; this disk state machine is not built over
// that abstraction (it manages its own *pebble.Batch per command), so each
// time-series write here gets its own batch and commits immediately --
// exactly the self-committing behaviour this state machine's time-series
// support has always had.
func (s *PebbleStateMachine) tsAutoCommit(fn func(storage.Batch) error) error {
	batch := pebbleStorage.NewFromDB(s.db, true, pebbleSyncs(s.dir), pebbleStorage.WithSealWatch(s.seals)).Batch()
	defer func() { _ = batch.Close() }()
	if err := fn(batch); err != nil {
		return err
	}
	return batch.Commit()
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

	db, seals, err := openPebble(initialDir, s.opts)
	if err != nil {
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.db = db
	s.seals = seals
	s.dir = initialDir

	// Create time-series storage if enabled. Uses the same PebbleDB
	// instance so that TS data shares the same durability guarantees.
	if s.enableTimeSeries && s.ts == nil {
		ts, tsErr := timeseries.New(pebbleStorage.NewFromDB(db, true, pebbleSyncs(initialDir), pebbleStorage.WithSealWatch(seals)))
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

	// A disk state machine resumes from lastApplied rather than replaying the
	// log, so the lessor's state has to come back from the lease table this
	// database already holds.
	if err := s.restoreLessor(); err != nil {
		_ = s.db.Close()
		s.db = nil
		s.ts = nil
		return 0, &OpenError{Dir: initialDir, Err: err}
	}

	// The replicated audit chain's head is kept in memory exactly like the
	// lessor's state, and for the same reason: a disk state machine resumes
	// from lastApplied rather than replaying the log.
	if err := s.restoreAuditChain(); err != nil {
		_ = s.db.Close()
		s.db = nil
		s.ts = nil
		return 0, &OpenError{Dir: initialDir, Err: err}
	}

	return lastApplied, nil
}

// restoreLessor rebuilds the attached lessor, if any, from the lease table
// persisted in the database. The CALLER MUST HOLD s.mu for writing.
func (s *PebbleStateMachine) restoreLessor() error {
	if s.lessor == nil {
		return nil
	}
	return s.lessor.Restore(pebbleStorage.NewFromDB(s.db, true))
}

// restoreAuditChain rebuilds the attached replicated audit Chain, if any,
// from the audit table persisted in the database. The CALLER MUST HOLD s.mu
// for writing.
func (s *PebbleStateMachine) restoreAuditChain() error {
	if s.auditChain == nil {
		return nil
	}
	return s.auditChain.Restore(pebbleStorage.NewFromDB(s.db, true))
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

	return decodeLastApplied(val), nil
}

// resyncLogs rebuilds the log registry from committed storage after an
// Update that did not commit: an append staged into the discarded batch has
// already advanced the registry's in-memory head, and a head for entries no
// replica holds must not outlive the failure. When the registry cannot be
// rebuilt either, it returns a *LogsResyncError carrying both errors.
func (s *PebbleStateMachine) resyncLogs(applyErr error) error {
	if err := s.restoreAuditChain(); err != nil {
		return &LogsResyncError{ApplyErr: applyErr, RestoreErr: err}
	}
	return applyErr
}

// Update applies committed entries to the PebbleDB store using an atomic
// batch. Each entry is decoded using either the base KV protocol or the
// extended wire protocol and dispatched to the appropriate handler via
// map-based O(1) lookup. The last applied index is persisted in the same
// batch for crash recovery. An Update that does not commit rebuilds the log
// registry before it returns (resyncLogs).
//
// The apply loop stays in this method rather than a helper: it is the seam
// test/buildgates checks for statemachine.ResolveApplyError (go-qrdb#520).
func (s *PebbleStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}
	defer func() {
		if err != nil {
			err = s.resyncLogs(err)
		}
	}()

	// Every update stages its log writes afresh over what is committed; see
	// vlog.Chain.BeginUpdate.
	if s.logs != nil {
		s.logs.BeginUpdate()
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

		if s.lessor != nil {
			s.lessor.Applying(e.Index)
		}
		result, notifs, err := s.dispatch(batch, e.Cmd)

		// An opcode this state machine has no handler for is a refusal, not a
		// fault: it is answered in the result payload rather than halting Apply
		// and costing the shard every subsequent linearizable read
		// (go-qrdb#520).
		resolved, stop, applyErr := statemachine.ResolveApplyError(slog.Default(), e.Index, result, err)
		if stop {
			return applyErr
		}
		// #nosec G602 -- the sm.StateMachine Update contract pre-allocates results with len(results) == len(entries), and i ranges over entries.
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
//   - audit.QueryRequest: returns *audit.Page for the confined tenant and filter
//   - audit.VerifyRequest: returns *audit.ChainContinuityReport for the confined tenant
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
	// The state digest scans a snapshot rather than the live database, so it
	// takes the lock only to open one; see stateDigest.
	if _, ok := query.(statedigest.Query); ok {
		return s.stateDigest()
	}

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
		// anything but an internal fault. A follower's *lease.StaleTTLError
		// passes through, as it does there.
		l, err := s.lessor.TimeToLive(q.ID)
		if err != nil {
			var nf *lease.NotFoundError
			if errors.As(err, &nf) {
				return nil, &service.LeaseNotFoundError{LeaseID: q.ID}
			}
			return nil, err
		}
		return l, nil

	case lease.LeaseKeepAliveQuery:
		if s.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		l, err := s.lessor.RenewLocal(q.ID)
		if err != nil {
			var nf *lease.NotFoundError
			if errors.As(err, &nf) {
				return nil, &service.LeaseNotFoundError{LeaseID: q.ID}
			}
			return nil, err
		}
		return l, nil

	case lease.LeaseCheckpointNowQuery:
		if s.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		answer, err := s.lessor.RequestCheckpoint()
		if err != nil {
			return nil, err
		}
		return answer, nil

	case lease.LeasesQuery:
		if s.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		return s.lessor.Leases(), nil

	case audit.QueryRequest:
		if s.auditChain == nil {
			return nil, &InvalidQueryError{Reason: "audit chain not configured"}
		}
		return s.auditChain.Query(pebbleStorage.NewFromDB(s.db, true), q.TenantID, q.Filter)

	case audit.VerifyRequest:
		if s.auditChain == nil {
			return nil, &InvalidQueryError{Reason: "audit chain not configured"}
		}
		return s.auditChain.Verify(pebbleStorage.NewFromDB(s.db, true), q.TenantID)

	case vlog.Request:
		if s.logs == nil {
			return nil, &InvalidQueryError{Reason: "log registry not configured"}
		}
		return vlog.ReadSnapshot(pebbleStorage.NewFromDB(s.db, true), q)

	case index.IndexQueryRequest:
		idx, ok := s.indexes[q.IndexName]
		if !ok {
			return nil, &index.NotFoundError{Name: q.IndexName}
		}
		return idx.Get(q.Value)

	case index.IndexScanRequest:
		idx, ok := s.indexes[q.IndexName]
		if !ok {
			return nil, &index.NotFoundError{Name: q.IndexName}
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
		return s.ts.QueryRich(q.Request)

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

	case service.ImportStageMetaQuery:
		return s.getStagedKey(importstage.MetaKey(q.SessionID))

	case service.ImportStageChunkQuery:
		return s.getStagedKey(importstage.ChunkKey(q.SessionID, q.Seq))

	case service.ImportStageOpenSessionsQuery:
		return countOpenImportSessions(s.db, q.TenantID)

	case []byte:
		return s.getClientKey(q)

	case string:
		return s.getClientKey([]byte(q))

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

// getClientKey performs a point lookup for a plain client Get/BytesLookup --
// the Lookup query carries the raw key itself, with no structured type
// naming it, unlike the lease table's own lease.LeaseTimeToLiveQuery or the
// import-staging queries above. It refuses -- reading as absent, exactly as
// listKeys and scanPrefix already exclude these keys from enumeration -- any
// key in the reserved 0x00 namespace: the lease table, the compaction
// table, and (go-qrdb#576) the import-staging table are state-machine
// bookkeeping, not client data, and the identical literal key must not be
// readable this way. ReservedKeyRefusal already refuses a client WRITE to
// one of these keys; this is that guard's read-side counterpart, which
// getKey alone does not provide (go-qrdb#576's security review: a plain Get
// of an import session's literal key answered with its data on sm_type
// pebble, the daemon's default).
func (s *PebbleStateMachine) getClientKey(key []byte) ([]byte, error) {
	if len(key) > 0 && key[0] == 0x00 {
		return nil, &kv.KeyNotFoundError{Key: string(key)}
	}
	return s.getKey(key)
}

// getStagedKey reads an import-staging record (pkg/importstage) by its
// literal key, translating getKey's *kv.KeyNotFoundError into (nil, nil) --
// found/not-found as a nil slice, not an error -- which is the contract
// pkg/service.BatchService.getSessionMeta/getSessionChunk read against.
// Unlike getClientKey it is reached only from the two ImportStage* query
// types above, never from a client-named raw key, so it does not refuse the
// reserved namespace itself.
func (s *PebbleStateMachine) getStagedKey(key []byte) ([]byte, error) {
	v, err := s.getKey(key)
	if err != nil {
		var notFound *kv.KeyNotFoundError
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, err
	}
	return v, nil
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

	// go-quicraft's rsm routes a []byte query here rather than to Lookup,
	// so the reserved namespace must be refused here too -- see
	// getClientKey, the identical guard Lookup's own []byte/string cases
	// use.
	return s.getClientKey(key)
}

// Sync flushes all pending writes to durable storage. Called before snapshot
// operations to ensure consistency.
func (s *PebbleStateMachine) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	if err := s.seals.Flush(s.db); err != nil {
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

	// The snapshot carried the lease table with the data it governs; the
	// lessor must now reflect it rather than the state it held before.
	if err := s.restoreLessor(); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	// The snapshot carried the replicated audit table with the data it
	// governs, for exactly the same reason: a recovering replica never saw
	// the log that produced it.
	if err := s.restoreAuditChain(); err != nil {
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
		if isNodeLocalKey(k) {
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
	if res, refused := statemachine.ReservedKeyRefusal(cmd.Key); refused {
		return res, nil, nil
	}

	// Capture previous value for index maintenance and watch notifications.
	oldValue, _ := s.getKeyInBatch(batch, cmd.Key)
	prevKv := s.watchPrevKV(batch, cmd.Key)

	// Index maintenance runs BEFORE the data write is staged: a unique
	// violation must refuse the whole write, never land it and then
	// discover the index disagrees.
	if res, handled, ierr := s.precheckAndUpdateIndexes(cmd.Key, oldValue, cmd.Value); handled {
		return res, nil, ierr
	}

	if err := batch.Set(cmd.Key, cmd.Value, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	// A base KV put carries no lease, so the key leaves any lease its previous
	// value was bound to.
	if err := s.bindLease(batch, cmd.Key, lease.NoLease); err != nil {
		return sm.Result{}, nil, err
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
	if res, refused := statemachine.ReservedKeyRefusal(cmd.Key); refused {
		return res, nil, nil
	}

	absent, getErr := s.keyIsAbsentInBatch(batch, cmd.Key)
	if getErr != nil {
		return sm.Result{}, nil, getErr
	}
	if !absent {
		return sm.Result{Value: kv.ResultNotStored}, nil, nil
	}

	// The key was absent, so there is no previous value to remove from any
	// index; still apply a unique violation BEFORE staging the write.
	if res, handled, ierr := s.precheckAndUpdateIndexes(cmd.Key, nil, cmd.Value); handled {
		return res, nil, ierr
	}

	if err := batch.Set(cmd.Key, cmd.Value, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}
	if err := s.bindLease(batch, cmd.Key, lease.NoLease); err != nil {
		return sm.Result{}, nil, err
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
	if res, refused := statemachine.ReservedKeyRefusal(key); refused {
		return res, nil, nil
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

	if err := s.removeIndexesOnDelete(key, current); err != nil {
		return sm.Result{}, nil, err
	}

	prevLease := s.leaseOf(key)
	if derr := batch.Delete(key, pebble.Sync); derr != nil {
		return sm.Result{}, nil, &WriteError{Err: derr}
	}
	if err := s.bindLease(batch, key, lease.NoLease); err != nil {
		return sm.Result{}, nil, err
	}

	var notifs []pebbleNotification
	if s.hub != nil {
		notifs = []pebbleNotification{{
			eventType: watch.EventDelete,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key},
			prevKv:    &qrdbkv.KeyValue{Key: key, Value: current, LeaseID: prevLease},
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
	if res, refused := statemachine.ReservedKeyRefusal(key); refused {
		return res, nil, nil
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

	if res, handled, ierr := s.precheckAndUpdateIndexes(key, current, next); handled {
		return res, nil, ierr
	}

	if serr := batch.Set(key, next, pebble.Sync); serr != nil {
		return sm.Result{}, nil, &WriteError{Err: serr}
	}

	// The lease survives the swap, as it does on the MVCC engine: a value edit
	// is not a reason to change the record's lifetime. The binding is
	// untouched, so the key stays in whatever lease it was in.
	var notifs []pebbleNotification
	if s.hub != nil {
		heldLease := s.leaseOf(key)
		notifs = []pebbleNotification{{
			eventType: watch.EventPut,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key, Value: next, LeaseID: heldLease},
			prevKv:    &qrdbkv.KeyValue{Key: key, Value: current, LeaseID: heldLease},
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
	if res, refused := statemachine.ReservedKeyRefusal(cmd.Key); refused {
		return res, nil, nil
	}
	notifs, err := s.deleteKey(batch, cmd.Key, nil)
	if err != nil {
		return sm.Result{}, nil, err
	}
	return sm.Result{Value: 1}, notifs, nil
}

// deleteKey stages the delete of key into the apply batch with everything a
// delete carries: the key's secondary-index entries are removed and its lease
// binding released in the same batch, and, when there is a watch hub, a
// delete event is appended to notifs. The caller has already refused a
// reserved key.
func (s *PebbleStateMachine) deleteKey(batch *pebble.Batch, key []byte, notifs []pebbleNotification) ([]pebbleNotification, error) {
	// Capture previous value for index maintenance and watch notifications.
	oldValue, _ := s.getKeyInBatch(batch, key)
	prevKv := s.watchPrevKV(batch, key)

	if err := s.removeIndexesOnDelete(key, oldValue); err != nil {
		return nil, err
	}
	if err := batch.Delete(key, pebble.Sync); err != nil {
		return nil, &WriteError{Err: err}
	}
	if err := s.bindLease(batch, key, lease.NoLease); err != nil {
		return nil, err
	}
	if s.hub != nil {
		notifs = append(notifs, pebbleNotification{
			eventType: watch.EventDelete,
			key:       key,
			kv:        &qrdbkv.KeyValue{Key: key},
			prevKv:    prevKv,
		})
	}
	return notifs, nil
}

// pebbleHandleDeleteRange applies protocol.OpDeleteRange: it deletes every
// key in [start, end) -- through the end of the keyspace when end is empty --
// as OpDelete deletes each one, and reports how many it deleted. The keys are
// read through the apply batch, so an earlier entry of the same apply is
// seen. It writes no storage range tombstone, which would leave the keys'
// index entries and lease bindings behind and tell no watcher.
//
// A start in the reserved 0x00 namespace refuses; any other start sorts above
// that whole namespace, so no reserved record is in its range. Bounds that do
// not describe a range are refused in the result, as the engines refuse them,
// rather than halting apply.
func pebbleHandleDeleteRange(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	start, end, err := statemachine.DecodeDeleteRange(payload)
	var rangeErr *statemachine.InvalidRangeError
	if errors.As(err, &rangeErr) {
		return statemachine.RefusalResult(rangeErr), nil, nil
	}
	if err != nil {
		return sm.Result{}, nil, err
	}
	if res, refused := statemachine.ReservedKeyRefusal(start); refused {
		return res, nil, nil
	}
	iter, err := batch.NewIter(&pebble.IterOptions{LowerBound: start, UpperBound: end})
	if err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}
	var keys [][]byte
	for ok := iter.First(); ok; ok = iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	iterErr := iter.Error()
	_ = iter.Close()
	if iterErr != nil {
		return sm.Result{}, nil, &WriteError{Err: iterErr}
	}
	var notifs []pebbleNotification
	for _, key := range keys {
		if notifs, err = s.deleteKey(batch, key, notifs); err != nil {
			return sm.Result{}, nil, err
		}
	}
	return sm.Result{Value: uint64(len(keys))}, notifs, nil
}

// ---------------------------------------------------------------------------
// Extended protocol update handlers (MVCC-compatible, no revision tracking)
// ---------------------------------------------------------------------------

// pebbleHandleMVCCPut processes an MVCC Put command without revision
// tracking. The key-value pair is stored directly in PebbleDB, and the key is
// bound to the lease the write names -- or released from any lease, when it
// names none -- in the same batch.
func pebbleHandleMVCCPut(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}
	if res, refused := statemachine.ReservedKeyRefusal(key); refused {
		return res, nil, nil
	}
	if res, refused := statemachine.LeaseRefusal(s.lessor, leaseID); refused {
		return res, nil, nil
	}

	// Capture previous value for watch notifications.
	prevKv := s.watchPrevKV(batch, key)

	if err := batch.Set(key, value, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	if err := s.bindLease(batch, key, leaseID); err != nil {
		return sm.Result{}, nil, err
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
	if res, refused := statemachine.ReservedKeyRefusal(key); refused {
		return res, nil, nil
	}
	if res, refused := statemachine.LeaseRefusal(s.lessor, leaseID); refused {
		return res, nil, nil
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

	if err := s.bindLease(batch, key, leaseID); err != nil {
		return sm.Result{}, nil, err
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
	if res, refused := statemachine.ReservedKeyRefusal(key); refused {
		return res, nil, nil
	}

	// Capture previous value for watch notifications.
	prevKv := s.watchPrevKV(batch, key)

	if err := batch.Delete(key, pebble.Sync); err != nil {
		return sm.Result{}, nil, &WriteError{Err: err}
	}

	// Release the key from the lease it was bound to, if any.
	if err := s.bindLease(batch, key, lease.NoLease); err != nil {
		return sm.Result{}, nil, err
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
	if res, refused := statemachine.ReservedKeyRefusal(statemachine.OpKeys(ops)...); refused {
		return res, nil, nil
	}

	// Pre-validate the OPCODES: an operation this state machine cannot execute
	// refuses the whole batch and stages nothing (validatePebbleOps).
	if verr := validatePebbleOps(ops); verr != nil {
		return sm.Result{}, nil, verr
	}
	if res, refused := statemachine.OpsLeaseRefusal(s.lessor, ops); refused {
		return res, nil, nil
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
	if res, refused := statemachine.ReservedKeyRefusal(append(statemachine.OpKeys(txn.SuccessOps), statemachine.OpKeys(txn.FailureOps)...)...); refused {
		return res, nil, nil
	}
	// The compare side needs the identical refusal, not only the write side:
	// a compare's result is observable to the proposer (which branch ran,
	// and for an ordering target, which way it leaned), so evaluating one
	// against a reserved key would make an ordinary transaction an
	// extraction oracle over state no caller may read directly.
	if res, refused := statemachine.ReservedKeyRefusal(statemachine.CompareKeys(txn.Compares)...); refused {
		return res, nil, nil
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
		if dc.Target == protocol.CompareTargetCreateRevision || dc.Target == protocol.CompareTargetModRevision {
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
	if res, refused := statemachine.OpsLeaseRefusal(s.lessor, ops); refused {
		return res, nil, nil
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

// pebbleEvaluateCompare evaluates a single transaction compare against the
// current PebbleDB state. This state machine keeps no MVCC revisions, so only
// value and version comparisons are meaningful: version degrades honestly (0
// when the key is absent, 1 when present), while create/mod-revision compares
// are rejected up front by pebbleHandleTransaction and therefore never reach
// this function. protocol.CompareHolds fails them closed rather than silently
// comparing every key against revision 0 if one ever arrives by another path.
func pebbleEvaluateCompare(s *PebbleStateMachine, batch *pebble.Batch, dc protocol.DecodedCompare) bool {
	val, err := s.getKeyInBatch(batch, dc.Key)
	if err != nil {
		val = nil
	}
	return protocol.CompareHolds(dc, val)
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
		oldValue, _ := s.getKeyInBatch(batch, op.Key)
		prevKv := s.watchPrevKV(batch, op.Key)

		// A Batch/Transaction op executes unconditionally once its opcodes
		// are validated: pebbleExecuteOp has no per-op result slot to carry
		// an application-level outcome the way a single Put's sm.Result
		// does, so a unique conflict here -- unlike the single-op paths --
		// still surfaces as an apply-level error. The pre-check still runs
		// first so a conflict leaves no partial index mutation behind.
		if verr := engine.CheckUniqueViolation(nil, s.indexes, s.indexFields, s.indexScopes, op.Key, op.Value); verr != nil {
			return nil, verr
		}
		if err := s.updateIndexesOnWrite(op.Key, oldValue, op.Value); err != nil {
			return nil, err
		}

		if err := batch.Set(op.Key, op.Value, pebble.Sync); err != nil {
			return nil, &WriteError{Err: err}
		}

		if err := s.bindLease(batch, op.Key, op.LeaseID); err != nil {
			return nil, err
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
		oldValue, _ := s.getKeyInBatch(batch, op.Key)
		prevKv := s.watchPrevKV(batch, op.Key)

		if err := s.removeIndexesOnDelete(op.Key, oldValue); err != nil {
			return nil, err
		}

		if err := batch.Delete(op.Key, pebble.Sync); err != nil {
			return nil, &WriteError{Err: err}
		}

		if err := s.bindLease(batch, op.Key, lease.NoLease); err != nil {
			return nil, err
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
func pebbleHandleLeaseGrant(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}

	info, grantErr := s.lessor.Grant(pebbleLeaseWriter{batch: batch}, ttl)
	if grantErr != nil {
		// A lease-table write fault is local to this replica and must halt
		// apply, never be answered as a refusal its peers did not make.
		var twe *lease.TableWriteError
		if errors.As(grantErr, &twe) {
			return sm.Result{}, nil, grantErr
		}
		// Invalid TTL (and similar input-shaped grant failures) are
		// deterministic application errors: surface via the result payload
		// rather than stalling consensus.
		return statemachine.RefusalResult(grantErr), nil, nil
	}

	return sm.Result{Value: uint64(info.ID)}, nil, nil
}

// pebbleHandleLeaseRevoke expires a lease and deletes the keys still bound to
// it. A key re-put under another lease or with none since it was bound here
// has already left the lease (lease.Lessor.Bind) and is untouched.
func pebbleHandleLeaseRevoke(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}

	keys, revokeErr := s.lessor.Revoke(pebbleLeaseWriter{batch: batch}, leaseID)
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
		prevKv := s.watchPrevKV(batch, key)
		if prevKv != nil {
			// The binding was released by Revoke above; the deleted value
			// belonged to this lease.
			prevKv.LeaseID = leaseID
		}

		// Deleting an absent key is not an error in Pebble, so a failure here
		// is a fault in the batch, local to this replica. Skipping the key
		// would leave it on this replica while its peers delete it.
		if delErr := batch.Delete(key, pebble.Sync); delErr != nil {
			return sm.Result{}, nil, &WriteError{Err: delErr}
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
func pebbleHandleLeaseKeepAlive(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}

	// The batch receives the deletion of the lease's checkpoint record, if it
	// has one (lease.Lessor.KeepAlive).
	l, kaErr := s.lessor.KeepAlive(pebbleLeaseWriter{batch: batch}, leaseID)
	if kaErr != nil {
		var nfe *lease.NotFoundError
		if errors.As(kaErr, &nfe) {
			// Lease already gone — idempotent success so Raft Apply does not
			// stall on duplicate/replayed keepalive proposals.
			return sm.Result{Value: 0, Data: protocol.EncodeLeaseKeepAliveNotFound(leaseID)}, nil, nil
		}
		return sm.Result{}, nil, kaErr
	}

	// The apply already computed the lease's post-reset state; carrying it
	// back in Data lets the service layer answer the caller from this
	// committed result instead of a second, separately linearized read
	// (go-qrdb#275).
	return sm.Result{Value: uint64(leaseID), Data: protocol.EncodeLeaseInfo(l.ID, l.GrantedTTL, l.RemainingTTL, l.Keys)}, nil, nil
}

// pebbleHandleLeaseCheckpoint records the leader's checkpoint of the remaining
// TTL of a batch of leases (lease.Lessor.ApplyCheckpoints), so that a replica
// that becomes leader counts each lease from what it had left. A storage fault
// is an apply fault, as for every lease-table write.
func pebbleHandleLeaseCheckpoint(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	token, index, cps, err := protocol.DecodeLeaseCheckpoint(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}

	if err := s.lessor.ApplyCheckpoints(pebbleLeaseWriter{batch: batch}, token, index, cps); err != nil {
		return sm.Result{}, nil, err
	}
	return sm.Result{Value: uint64(len(cps))}, nil, nil
}

// ---------------------------------------------------------------------------
// Replicated audit chain handler
// ---------------------------------------------------------------------------

// pebbleHandleAuditAppend applies one protocol.OpAuditAppend command: either
// sub-type, append or prune, against the attached audit.Chain. The sequence
// number and hash digest for an append are assigned by Chain.Append itself,
// never read from the payload, which is what makes every replica compute the
// identical chain from the identical committed entry.
func (s *PebbleStateMachine) pebbleHandleAuditAppend(_ *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	subtype, tenantID, body, err := protocol.DecodeAuditCommand(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if s.auditChain == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "audit"}), nil, nil
	}

	switch subtype {
	case protocol.AuditSubtypeAppend:
		var ev audit.AuditEvent
		if unmarshalErr := json.Unmarshal(body, &ev); unmarshalErr != nil {
			// The outer command decoded fine -- this is a well-formed body
			// that merely fails to unmarshal as an AuditEvent, a
			// deterministic consequence of the committed entry's own bytes
			// that every replica reaches identically, so it is a refusal,
			// not a halt. Contrast the unknown-subtype and truncated-framing
			// cases, which stay halts: those name a shape this binary does
			// not recognize at all, the same class as protocol.UnknownOpError.
			return statemachine.RefusalResult(&protocol.InvalidCommandError{Reason: "audit append: malformed event: " + unmarshalErr.Error()}), nil, nil
		}
		seq, appendErr := s.auditChain.Append(pebbleAuditWriter{batch: batch}, tenantID, ev)
		if appendErr != nil {
			// A table-write fault is local to this replica and must halt
			// apply, never be answered as a refusal its peers did not make --
			// exactly the lease-table TableWriteError rule. Everything else
			// Append can return (a missing Timestamp, an event that will not
			// re-marshal) is a deterministic consequence of the bytes in the
			// committed entry: every replica reaches the identical refusal
			// from the identical input, so it is encoded as an application
			// result, the same way an invalid lease TTL is, rather than
			// returned as a Go error that would stall Raft on a command every
			// replica is equally unable to apply.
			var twe *vlog.TableWriteError
			if errors.As(appendErr, &twe) {
				return sm.Result{}, nil, appendErr
			}
			return statemachine.RefusalResult(appendErr), nil, nil
		}
		return sm.Result{Value: seq}, nil, nil

	case protocol.AuditSubtypePrune:
		if len(body) != 8 {
			return sm.Result{}, nil, &protocol.InvalidCommandError{Reason: "audit prune: cutoff malformed"}
		}
		cutoff := binary.BigEndian.Uint64(body)
		// Prune computes its delete range from the Chain's own in-memory
		// head alone -- never from a storage read -- which is what makes it
		// independent of how many committed entries this Update call's
		// batch happens to pack together; see pkg/audit.Chain.Prune's doc
		// comment (go-qrdb audit-replicated review, B1). Every error it can
		// return is a fault in THIS replica's own storage, never a property
		// of the cutoff value, so there is nothing here that is a
		// legitimate per-replica refusal: it always halts apply.
		deleted, pruneErr := s.auditChain.Prune(pebbleAuditWriter{batch: batch}, tenantID, cutoff)
		if pruneErr != nil {
			return sm.Result{}, nil, pruneErr
		}
		return sm.Result{Value: uint64(deleted)}, nil, nil

	default:
		return sm.Result{}, nil, &protocol.InvalidCommandError{Reason: "audit command: unknown sub-type"}
	}
}

// pebbleHandleVerifiableLog applies one protocol.OpVerifiableLog command to
// the attached log registry, reading tree nodes from committed storage
// beneath what this update has staged. Classification -- refusal versus halt
// -- is statemachine.ApplyVerifiableLog's, shared with the MVCC engine.
func (s *PebbleStateMachine) pebbleHandleVerifiableLog(_ *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	res, err := statemachine.ApplyVerifiableLog(s.logs, pebbleStorage.NewFromDB(s.db, true), pebbleAuditWriter{batch: batch}, payload)
	return res, nil, err
}

// pebbleAuditWriter adapts a *pebble.Batch to vlog.Writer, mirroring
// pebbleLeaseWriter exactly.
type pebbleAuditWriter struct {
	batch *pebble.Batch
}

// Set stages key = value.
func (w pebbleAuditWriter) Set(key, value []byte) error {
	return w.batch.Set(key, value, pebble.Sync)
}

// Delete stages the removal of key.
func (w pebbleAuditWriter) Delete(key []byte) error {
	return w.batch.Delete(key, pebble.Sync)
}

// ---------------------------------------------------------------------------
// Chunked import session staging (go-qrdb#576)
//
// A session's metadata and staged chunks are written directly into
// PebbleDB through batch.Set, exactly like the lease table above -- never
// through the base KV Put path -- so the record is in the reserved 0x00
// namespace ReservedKeyRefusal already refuses a client write to, and
// getClientKey already refuses a client read of. See pkg/importstage and
// pkg/engine/mvcc_import_stage.go (the identical design on the "mvcc"
// raft.sm_type).
// ---------------------------------------------------------------------------

// pebbleHandleImportStage writes a session's metadata or one staged chunk.
func pebbleHandleImportStage(s *PebbleStateMachine, batch *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	cmd, err := protocol.DecodeImportStage(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}
	if s.lessor == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}
	// A staged record with no lease would never be reclaimed, so NoLease is
	// refused here rather than treated as "no lease named" the way an
	// ordinary put's optional lease is (statemachine.LeaseRefusal's own
	// rule, which this deliberately does not reuse).
	if cmd.LeaseID == lease.NoLease || !s.lessor.Has(cmd.LeaseID) {
		return statemachine.RefusalResult(&service.LeaseNotFoundError{LeaseID: cmd.LeaseID}), nil, nil
	}

	type write struct {
		key   []byte
		value []byte
	}
	var writes []write
	switch cmd.Kind {
	case protocol.ImportStageSetMeta:
		// Count and write commit at the SAME log position: Update() applies
		// every entry it was given, including a later ImportBegin packed
		// into the same batched call, strictly in order, so two concurrent
		// ImportBegin proposals can never both observe room for the last
		// slot of MaxSessions. countOpenImportSessions reads batch (the
		// in-flight apply batch merged over s.db), not s.db directly, so an
		// earlier entry in this SAME Update() call is already visible --
		// exactly the reason getKeyInBatch exists.
		if cmd.MaxSessions > 0 {
			open, err := countOpenImportSessions(batch, cmd.TenantID)
			if err != nil {
				return sm.Result{}, nil, err
			}
			if open >= int(cmd.MaxSessions) {
				return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorConflict, "import session quota exceeded")}, nil, nil
			}
		}
		writes = []write{
			{importstage.MetaKey(cmd.SessionID), cmd.Body},
			// The tenant-index entry commits in the SAME batch as the
			// metadata record, and is bound to the SAME lease, so it is
			// counted and reclaimed exactly in step with the session it
			// indexes -- see countOpenImportSessions and
			// pkg/importstage.TenantIndexKey.
			{importstage.TenantIndexKey(cmd.TenantID, cmd.SessionID), nil},
		}
	case protocol.ImportStageSetChunk:
		writes = []write{{importstage.ChunkKey(cmd.SessionID, cmd.Seq), cmd.Body}}
		if len(cmd.MetaBody) > 0 {
			// The chunk and the metadata that now accounts for it commit in
			// the SAME batch, exactly as putRawBatch committed them as one
			// Raft command before this package existed.
			writes = append(writes, write{importstage.MetaKey(cmd.SessionID), cmd.MetaBody})
		}
	default:
		return sm.Result{}, nil, &protocol.InvalidCommandError{Reason: "import stage: unknown kind"}
	}

	for _, w := range writes {
		if err := batch.Set(w.key, w.value, pebble.Sync); err != nil {
			return sm.Result{}, nil, &WriteError{Err: err}
		}
		if err := s.bindLease(batch, w.key, cmd.LeaseID); err != nil {
			return sm.Result{}, nil, err
		}
	}
	return sm.Result{Value: 1}, nil, nil
}

// countOpenImportSessions counts tenantID's open sessions via its tenant
// index (pkg/importstage.TenantIndexKey), reading only keys -- iter.Key(),
// never iter.ValueAndErr() -- so the cost is independent of how many bytes
// those sessions have staged (go-qrdb#576's security review measured the
// previous MVCC-range implementation, which read every chunk's value, at up
// to ~1 GiB per call at default limits). r is batch during
// pebbleHandleImportStage's own apply (an indexed *pebble.Batch, whose
// NewIter merges the batch's uncommitted writes over s.db -- see
// getKeyInBatch) or s.db for a Lookup query; *pebble.Batch and *pebble.DB
// both satisfy pebble.Reader.
func countOpenImportSessions(r pebble.Reader, tenantID string) (int, error) {
	prefix := importstage.TenantIndexScanPrefix(tenantID)
	iter, err := r.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return 0, &ReadError{Key: string(prefix), Err: err}
	}
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	if err := iter.Error(); err != nil {
		return 0, &ReadError{Key: string(prefix), Err: err}
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// Index handlers
// ---------------------------------------------------------------------------

// pebbleHandleIndexCreate creates a new secondary index backed by the
// same PebbleDB instance.
func (s *PebbleStateMachine) pebbleHandleIndexCreate(_ *PebbleStateMachine, _ *pebble.Batch, payload []byte) (sm.Result, []pebbleNotification, error) {
	name, fieldName, opts, err := protocol.DecodeIndexCreateOpts(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	valueType := engine.IndexCreateValueType(opts)
	if existing, exists := s.indexes[name]; exists {
		// A create naming an index ANOTHER owner holds is refused rather than
		// adopted (go-qrdb#426), as an application error so consensus advances.
		owner := s.indexOwners[name]
		if index.CheckCreateOwner(name, owner, opts.Owner) != nil {
			return sm.Result{Data: protocol.EncodeIndexOwnershipError(name, owner)}, nil, nil
		}
		// Index already present under the same owner and value type —
		// idempotent success so Raft Apply does not stall on duplicate create
		// proposals. Existence is otherwise keyed by name only: a same-owner
		// re-create diverging in field or uniqueness is a no-op (original
		// wins). A different value type falls through and rebuilds the index
		// at the requested one (engine.IndexCreateValueType).
		if s.indexValueTypes[name] == valueType {
			return sm.Result{Value: 1}, nil, nil
		}
		if err := existing.Close(); err != nil {
			return sm.Result{}, nil, err
		}
	}

	// Sparse is carried on the config for fidelity with the command; no Index
	// implementation reads it, so nothing observable changes here.
	//
	// CaseSensitive is derived from the command instead of left at its zero
	// value. This handler used to discard the flag, so an
	// index:"case_insensitive" definition became a case-SENSITIVE index and a
	// lookup for "Alpha" over a stored "alpha" answered empty with a nil error
	// (go-qrdb#34).
	idx, idxErr := pebbleindex.NewWithConfig(s.db, name, index.IndexConfig{
		Name:      name,
		Unique:    opts.Unique,
		Sparse:    opts.Sparse,
		ValueType: valueType,
		// A create builds the index now, at the current term encoding.
		EncodingVersion: index.NumericEncodingVersion,
		CaseSensitive:   !opts.CaseInsensitive,
		Owner:           opts.Owner,
	})
	if idxErr != nil {
		return sm.Result{}, nil, idxErr
	}

	// Entries already stored under the name -- left by a create applied
	// again over storage that outlived the in-memory index, possibly in an
	// older term encoding -- are cleared before the backfill rebuilds them.
	if err := idx.Clear(); err != nil {
		return sm.Result{}, nil, err
	}

	// The key scope composes the namespace half read back off name (every
	// transport namespaces both keys and index names identically) with the
	// declared half the creator named (opts.KeyPrefix, go-qrdb#410). Both
	// maintenance on every subsequent write and this create's own backfill
	// below apply it, which is what keeps an index named for one database
	// from indexing another database's keys sharing this same shard.
	scope := engine.ComposeIndexScope(name, opts.KeyPrefix)

	s.indexes[name] = idx
	s.indexOwners[name] = opts.Owner
	s.indexFields[name] = fieldName
	s.indexScopes[name] = scope
	s.indexValueTypes[name] = valueType

	s.backfillIndex(idx, fieldName, scope)

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
	delete(s.indexFields, name)
	delete(s.indexScopes, name)
	delete(s.indexValueTypes, name)

	return sm.Result{Value: 1}, nil, nil
}

// backfillIndex populates idx from every currently-stored key within scope
// whose value carries fieldName, for an index just created over EXISTING
// data. A scan failure or a single entry's insert failure must not fail the
// create apply (that would stall consensus), so both are logged and the
// index is left as complete as the backfill could make it -- mirroring
// pkg/engine's BackfillIndex contract for the "mvcc"/engine-backed state
// machines.
func (s *PebbleStateMachine) backfillIndex(idx index.Index, fieldName string, scope []byte) {
	start, end := pebbleBackfillRange(scope)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: start, UpperBound: end})
	if err != nil {
		slog.Warn("pebble: index backfill scan failed; index created without backfill",
			"field", fieldName, "error", err)
		return
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if statemachine.IsReservedKey(key) || !engine.KeyInScope(scope, key) {
			continue
		}
		val, verr := iter.ValueAndErr()
		if verr != nil || len(val) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(val, &fields) != nil {
			continue
		}
		raw, exists := fields[fieldName]
		if !exists {
			continue
		}
		keyCopy := make([]byte, len(key))
		copy(keyCopy, key)
		if err := idx.Put(keyCopy, engine.UnquoteJSON(raw)); err != nil {
			slog.Warn("pebble: index backfill entry failed; index built without this entry",
				"field", fieldName, "key", string(keyCopy), "error", err)
		}
	}
	if err := iter.Error(); err != nil {
		slog.Warn("pebble: index backfill scan failed partway; index built without the remainder",
			"field", fieldName, "error", err)
	}
}

// pebbleBackfillRange returns the [start, end) key range an index-create
// backfill scans for scope. A scoped index reads only its own keyspace; an
// unscoped one keeps the whole-keyspace range this engine has always used.
func pebbleBackfillRange(scope []byte) (start, end []byte) {
	if len(scope) == 0 {
		return []byte{0x00}, []byte{0xFF}
	}
	if e := prefixUpperBound(scope); e != nil {
		return scope, e
	}
	return scope, []byte{0xFF}
}

// updateIndexesOnWrite maintains every registered secondary index for a
// write of newValue under key, removing any entry oldValue (nil if there was
// none) contributed and adding the new one. A unique-constraint violation
// aborts and returns the error; callers MUST call this BEFORE staging the
// key/value write into the Raft-applied batch, so a refused write never
// lands in the state machine.
//
// index.Index writes its own entries directly (it is not an
// index.BatchableIndex over this engine's raw *pebble.Batch), so this is not
// joined into the SAME low-level Pebble transaction as the data write below
// it -- unlike pkg/engine's UpdateIndexesBatch, which shares one
// storage.Batch with both. The window that leaves open is a crash between
// this call and the data batch's Commit, which pebble.Sync-backed writes
// make vanishingly rare and which an operator recovers from exactly as they
// would any other interrupted Raft apply.
func (s *PebbleStateMachine) updateIndexesOnWrite(key, oldValue, newValue []byte) error {
	if len(s.indexes) == 0 || statemachine.IsReservedKey(key) {
		return nil
	}

	var newFields map[string]json.RawMessage
	if json.Unmarshal(newValue, &newFields) != nil {
		return nil // not a flat JSON object; nothing to index
	}
	var oldFields map[string]json.RawMessage
	if len(oldValue) > 0 {
		_ = json.Unmarshal(oldValue, &oldFields) // best-effort
	}

	for name, idx := range s.indexes {
		scope := s.indexScopes[name]
		if !engine.KeyInScope(scope, key) {
			continue
		}
		fieldName, ok := s.indexFields[name]
		if !ok {
			continue
		}

		if oldFields != nil {
			if oldRaw, exists := oldFields[fieldName]; exists {
				if err := idx.Delete(key, engine.UnquoteJSON(oldRaw)); err != nil {
					return err
				}
			}
		}
		if newRaw, exists := newFields[fieldName]; exists {
			if err := idx.Put(key, engine.UnquoteJSON(newRaw)); err != nil {
				return err
			}
		}
	}
	return nil
}

// precheckAndUpdateIndexes performs index maintenance for a write of
// newValue under key in the two steps a unique constraint requires: a
// READ-ONLY pre-check first (engine.CheckUniqueViolation), encoded as a
// non-fatal application result when it fails so Raft consensus still
// advances, and the actual index mutation (updateIndexesOnWrite) only once
// that pre-check has passed.
//
// Letting idx.Put's OWN internal uniqueness check discover the conflict --
// what this engine did before -- returns a Go error from the apply path.
// go-quicraft treats that as a state machine FAULT, not a declined write:
// an ordinary, expected unique-constraint rejection trips its SM circuit
// breaker and the shard stops serving, exactly the failure
// pkg/engine's MVCCEngine.handleBaseKVPut's own comment warns against for
// the identical reason.
//
// handled=true means the caller must return (res, notifs, err) immediately
// without touching its data batch. handled=false means maintenance
// succeeded (or there was nothing to maintain) and the caller proceeds to
// its own write.
func (s *PebbleStateMachine) precheckAndUpdateIndexes(key, oldValue, newValue []byte) (res sm.Result, handled bool, err error) {
	if verr := engine.CheckUniqueViolation(nil, s.indexes, s.indexFields, s.indexScopes, key, newValue); verr != nil {
		var uce *engine.UniqueConstraintError
		if errors.As(verr, &uce) {
			return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorConflict, uce.Error())}, true, nil
		}
		return sm.Result{}, true, verr
	}
	// Uniqueness was pre-validated above, so any error here is a genuine
	// storage fault and is correctly fatal.
	if err := s.updateIndexesOnWrite(key, oldValue, newValue); err != nil {
		return sm.Result{}, true, err
	}
	return sm.Result{}, false, nil
}

// removeIndexesOnDelete removes every registered secondary index's entry
// for key, given the value it held before the delete.
func (s *PebbleStateMachine) removeIndexesOnDelete(key, oldValue []byte) error {
	if len(s.indexes) == 0 || len(oldValue) == 0 || statemachine.IsReservedKey(key) {
		return nil
	}
	var oldFields map[string]json.RawMessage
	if json.Unmarshal(oldValue, &oldFields) != nil {
		return nil
	}
	for name, idx := range s.indexes {
		scope := s.indexScopes[name]
		if !engine.KeyInScope(scope, key) {
			continue
		}
		fieldName, ok := s.indexFields[name]
		if !ok {
			continue
		}
		if oldRaw, exists := oldFields[fieldName]; exists {
			if err := idx.Delete(key, engine.UnquoteJSON(oldRaw)); err != nil {
				return err
			}
		}
	}
	return nil
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

	if err := s.tsAutoCommit(func(b storage.Batch) error {
		return s.ts.Write(b, params.Metric, params.Value, time.Unix(0, params.Timestamp), params.Tags)
	}); err != nil {
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

	if err := s.tsAutoCommit(func(b storage.Batch) error {
		return s.ts.WriteBatch(b, points)
	}); err != nil {
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
	if err := s.tsAutoCommit(func(b storage.Batch) error {
		return s.ts.WritePoint(b, dp)
	}); err != nil {
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

	if err := s.tsAutoCommit(func(b storage.Batch) error {
		return s.ts.DeleteSeries(b, req)
	}); err != nil {
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

	if err := s.tsAutoCommit(func(b storage.Batch) error {
		return s.ts.SetRetentionPolicy(b, params.Metric, policy)
	}); err != nil {
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
	// Backstop: every caller (CAS, CAD, OpGuarded's reader) already checks
	// ReservedKeyRefusal on this exact key before reaching here, but the
	// check belongs on the read itself too, not only at each call site, so
	// a future caller that forgets it fails closed rather than silently
	// reading the raw table.
	if statemachine.IsReservedKey(key) {
		return nil, &kv.KeyNotFoundError{Key: string(key)}
	}
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

// pebbleLeaseWriter stages lease-table mutations into the apply batch, so a
// key's lease binding commits atomically with the write that changed it.
type pebbleLeaseWriter struct {
	batch *pebble.Batch
}

// Set stages key = value.
func (w pebbleLeaseWriter) Set(key, value []byte) error {
	return w.batch.Set(key, value, pebble.Sync)
}

// Delete stages the removal of key.
func (w pebbleLeaseWriter) Delete(key []byte) error {
	return w.batch.Delete(key, pebble.Sync)
}

// bindLease records, in the apply batch and the lessor, that key's newly
// written value names leaseID (lease.NoLease for none, which is also what a
// delete records). Every write path calls it, so a key always belongs to the
// lease of its latest value and to no other.
//
// With no lessor there is nothing to bind: a write naming a lease was already
// refused by statemachine.LeaseRefusal before it staged anything.
func (s *PebbleStateMachine) bindLease(batch *pebble.Batch, key []byte, leaseID int64) error {
	if s.lessor == nil {
		return nil
	}
	return s.lessor.Bind(pebbleLeaseWriter{batch: batch}, key, leaseID)
}

// leaseOf returns the lease key is bound to, or lease.NoLease.
func (s *PebbleStateMachine) leaseOf(key []byte) int64 {
	if s.lessor == nil {
		return lease.NoLease
	}
	return s.lessor.LeaseOf(key)
}

// watchPrevKV returns key's value as the apply batch sees it, with the lease
// it is bound to, for a watch notification's previous value. It returns nil
// when there is no watch hub to notify or the key is absent.
func (s *PebbleStateMachine) watchPrevKV(batch *pebble.Batch, key []byte) *qrdbkv.KeyValue {
	if s.hub == nil {
		return nil
	}
	val, err := s.getKeyInBatch(batch, key)
	if err != nil {
		return nil
	}
	return &qrdbkv.KeyValue{Key: key, Value: val, LeaseID: s.leaseOf(key)}
}

// Compile-time assertions.
var (
	_ lease.Writer        = pebbleLeaseWriter{}
	_ sm.DiskStateMachine = (*PebbleStateMachine)(nil)
	_ sm.Hasher           = (*PebbleStateMachine)(nil)
	_ sm.NALookup         = (*PebbleStateMachine)(nil)
)
