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

package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Notification captures a single change event for delivery to the watch hub
// after the storage batch is committed.
type Notification struct {
	EventType service.EventType
	Key       []byte
	KV        *service.KeyValue
	PrevKV    *service.KeyValue
}

// mvccHandler processes a single decoded command within a storage batch.
// The handler receives the MVCC store, the lessor (may be nil), the batch,
// and the command payload. It returns the result, any watch notifications,
// and an error.
type mvccHandler func(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error)

// ---------------------------------------------------------------------------
// MVCCEngine
// ---------------------------------------------------------------------------

// MVCCEngine wraps an mvcc.Store and its subsystems (indexes, leases,
// watches, time-series) behind the Engine interface. It handles binary
// protocol decoding and dispatches to the appropriate store methods.
//
// Thread safety: Update() does not require a mutex for the MVCC store because
// LocalBackend (and the Raft apply loop) serialize Update() calls, and
// mvcc.Store and storage.Storage handle their own synchronization. The
// indexes/indexFields maps, however, are owned by the engine itself and are
// read by Lookup() concurrently with index create/drop in Update(); those
// maps -- indexes, indexFields and indexScopes -- are therefore guarded by
// indexMu (single-writer/multi-reader).
type MVCCEngine struct {
	store       *mvcc.Store
	stg         storage.Storage
	indexMu     sync.RWMutex
	indexes     map[string]index.Index
	indexFields map[string]string

	// indexScopes holds the key scope of each index by name: the byte prefix
	// every key it indexes must carry (see ComposeIndexScope). A nil entry is
	// an index with no derivable and no declared scope, which covers the whole
	// state machine. Guarded by indexMu with indexes and indexFields.
	indexScopes map[string][]byte

	lessor   *lease.Lessor
	hub      *watch.Hub
	ts       *timeseries.Storage
	pebbleDB *pebble.DB

	// updateHandlers maps op codes to their update handlers.
	updateHandlers map[byte]mvccHandler
}

// MVCCOption configures an MVCCEngine during construction.
type MVCCOption func(*MVCCEngine)

// WithMVCCLessor attaches a lessor to the engine for lease operations.
func WithMVCCLessor(lr *lease.Lessor) MVCCOption {
	return func(e *MVCCEngine) {
		e.lessor = lr
	}
}

// WithMVCCWatchHub attaches a watch hub for delivering change notifications.
func WithMVCCWatchHub(hub *watch.Hub) MVCCOption {
	return func(e *MVCCEngine) {
		e.hub = hub
	}
}

// WithMVCCTimeSeries attaches a time-series storage engine.
func WithMVCCTimeSeries(ts *timeseries.Storage) MVCCOption {
	return func(e *MVCCEngine) {
		e.ts = ts
	}
}

// WithMVCCPebbleDB provides a pebble.DB instance for creating PebbleIndex
// instances. When not provided, index creation falls back to MemoryIndex.
func WithMVCCPebbleDB(db *pebble.DB) MVCCOption {
	return func(e *MVCCEngine) {
		e.pebbleDB = db
	}
}

// NewMVCCEngine creates a new MVCCEngine backed by the given storage.
// The MVCC store is initialized from the storage, loading any persisted
// revision and compacted markers. Options configure optional subsystems.
func NewMVCCEngine(stg storage.Storage, opts ...MVCCOption) (*MVCCEngine, error) {
	store, err := mvcc.New(stg)
	if err != nil {
		return nil, err
	}
	e := &MVCCEngine{
		store:       store,
		stg:         stg,
		indexes:     make(map[string]index.Index),
		indexFields: make(map[string]string),
		indexScopes: make(map[string][]byte),
	}
	for _, opt := range opts {
		opt(e)
	}
	// Attach the revision-index replayer so watches created with a start
	// revision replay durable history before switching to live delivery.
	if e.hub != nil {
		e.hub.SetHistory(newMVCCHistoryAdapter(e.store))
	}
	e.updateHandlers = e.buildUpdateHandlers()
	return e, nil
}

// Type returns the engine type identifier.
func (e *MVCCEngine) Type() string { return "mvcc" }

// Store returns the underlying MVCC store.
func (e *MVCCEngine) Store() *mvcc.Store { return e.store }

// Lessor returns the attached lessor. Returns nil if no lessor is configured.
func (e *MVCCEngine) Lessor() *lease.Lessor { return e.lessor }

// TimeSeries returns the attached time-series storage. Returns nil if not
// configured.
func (e *MVCCEngine) TimeSeries() *timeseries.Storage { return e.ts }

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

// Update applies committed entries to the MVCC store using an atomic storage
// batch. Each entry is decoded using the extended wire protocol and dispatched
// to the appropriate handler. Watch notifications are delivered after the
// batch is committed.
func (e *MVCCEngine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	notifications, err := e.UpdateEntries(batch, entries, results)
	if err != nil {
		return err
	}

	// Persist revision counter in the same batch.
	rev := e.store.CurrentRevision()
	if rev > 0 {
		if err := batch.Set(mvcc.RevisionKey(), mvcc.EncodeRevision(rev)); err != nil {
			return err
		}
	}

	if err := batch.Commit(); err != nil {
		return err
	}

	e.DeliverNotifications(notifications)
	return nil
}

// UpdateEntries dispatches entries to their handlers using the provided batch,
// without committing the batch. Returns the accumulated notifications for
// delivery after the caller commits the batch. This method is used by
// MVCCStateMachine which needs to inject Raft metadata (lastApplied) into the
// same batch before committing.
func (e *MVCCEngine) UpdateEntries(batch storage.Batch, entries []sm.Entry, results []sm.Result) ([]Notification, error) {
	var notifications []Notification

	for i, entry := range entries {
		cmd, err := protocol.DecodeCommand(entry.Cmd)
		if err != nil {
			return nil, err
		}

		handler, ok := e.updateHandlers[cmd.Op]
		if !ok {
			return nil, &protocol.UnknownOpError{Op: cmd.Op}
		}

		result, notifs, err := handler(e.store, e.lessor, batch, cmd.Payload)
		if err != nil {
			return nil, err
		}
		results[i] = result

		if len(notifs) > 0 {
			notifications = append(notifications, notifs...)
		}
	}

	return notifications, nil
}

// DeliverNotifications sends accumulated notifications to the watch hub.
// Called after a batch is committed. Safe to call with nil hub or empty slice.
func (e *MVCCEngine) DeliverNotifications(notifications []Notification) {
	if e.hub == nil || len(notifications) == 0 {
		return
	}
	for _, n := range notifications {
		switch n.EventType {
		case service.EventPut:
			e.hub.NotifyPut(n.Key, n.KV, n.PrevKV)
		case service.EventDelete:
			e.hub.NotifyDelete(n.Key, n.KV, n.PrevKV)
		}
	}
}

// ---------------------------------------------------------------------------
// Lookup
// ---------------------------------------------------------------------------

// Lookup reads state from the MVCC store. Supports the following query types:
//   - service.MVCCGetQuery: returns *service.KeyValue for the latest value
//   - service.MVCCGetAtRevisionQuery: returns *service.KeyValue at a historical revision
//   - service.MVCCRangeQuery: returns *service.MVCCRangeResult with key-values and revision
//   - service.MVCCRevisionQuery: returns int64 current revision
//   - service.MVCCScanChangesQuery: returns *service.MVCCScanChangesResult with
//     the distinct keys mutated in a revision window, including deleted ones
//   - service.LeaseTimeToLiveQuery: returns *lease.Lease
//   - service.LeasesQuery: returns []*lease.Lease
//   - service.IndexQueryRequest: returns []uint64 matching entity IDs
//   - service.IndexScanRequest: returns map[string][]uint64
//   - service.IndexListRequest: returns []string of index names
//   - service.TSQueryRequest: returns time-series data points
//   - service.TSAggregateRequest: returns aggregated results
//   - service.TSRichQueryRequest: returns rich query results
//   - service.TSGetRetentionRequest: returns retention policy
//   - service.TSListMetricsRequest: returns metric names
//   - *statemachine.PrefixQuery: returns map[string][]byte of matching keys
//   - kv.ListQuery: returns sorted []string of user keys
//   - []byte: returns value bytes (convenience for simple key lookup)
//   - string: returns value bytes (convenience for simple key lookup)
func (e *MVCCEngine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	switch q := query.(type) {
	case service.MVCCGetQuery:
		return e.store.Get(q.Key)

	case service.MVCCGetAtRevisionQuery:
		return e.store.GetAtRevision(q.Key, q.Revision)

	case service.MVCCRangeQuery:
		var opts []service.RangeOption
		if q.Revision != 0 {
			opts = append(opts, service.WithRevision(q.Revision))
		}
		if q.Limit != 0 {
			opts = append(opts, service.WithLimit(q.Limit))
		}
		if q.CountOnly {
			opts = append(opts, service.WithCountOnly())
		}
		if q.KeysOnly {
			opts = append(opts, service.WithKeysOnly())
		}
		if q.SortOrder != service.SortNone || q.SortTarget != service.SortByKey {
			opts = append(opts, service.WithSort(q.SortOrder, q.SortTarget))
		}
		kvs, count, rev, err := e.store.Range(q.Start, q.End, opts...)
		if err != nil {
			return nil, err
		}
		return &service.MVCCRangeResult{KeyValues: kvs, Count: count, Revision: rev}, nil

	case service.MVCCRevisionQuery:
		return e.store.CurrentRevision(), nil

	case service.MVCCScanChangesQuery:
		return e.scanChanges(q)

	case service.LeaseTimeToLiveQuery:
		if e.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		l := e.lessor.Lookup(q.ID)
		if l == nil {
			return nil, &InvalidQueryError{Reason: "lease not found"}
		}
		return l, nil

	case service.LeasesQuery:
		if e.lessor == nil {
			return nil, &InvalidQueryError{Reason: "lessor not configured"}
		}
		return e.lessor.Leases(), nil

	case service.IndexQueryRequest:
		e.indexMu.RLock()
		idx, ok := e.indexes[q.IndexName]
		e.indexMu.RUnlock()
		if !ok {
			return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
		}
		return idx.Get(q.Value)

	case service.IndexScanRequest:
		e.indexMu.RLock()
		idx, ok := e.indexes[q.IndexName]
		e.indexMu.RUnlock()
		if !ok {
			return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
		}
		return idx.Scan(q.Start, q.End)

	case service.IndexListRequest:
		e.indexMu.RLock()
		names := make([]string, 0, len(e.indexes))
		for name := range e.indexes {
			names = append(names, name)
		}
		e.indexMu.RUnlock()
		return names, nil

	case service.TSQueryRequest:
		if e.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return e.ts.Query(q.Metric, q.Start, q.End, q.Opts...)

	case service.TSAggregateRequest:
		if e.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return e.ts.Aggregate(q.Metric, q.Start, q.End, q.Func, q.Interval)

	case service.TSRichQueryRequest:
		if e.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return e.ts.QueryRich(context.Background(), q.Request)

	case service.TSGetRetentionRequest:
		if e.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return e.ts.GetRetentionPolicy(q.Metric)

	case service.TSListMetricsRequest:
		if e.ts == nil {
			return nil, &InvalidQueryError{Reason: "time-series storage not configured"}
		}
		return e.ts.ListMetrics(q.Prefix, q.Limit)

	case *statemachine.PrefixQuery:
		return e.lookupPrefix(q)

	case kv.ListQuery:
		return e.listKeys(q.Prefix)

	case []byte:
		result, err := e.store.Get(q)
		if err != nil {
			var knf *mvcc.KeyNotFoundError
			if errors.As(err, &knf) {
				return nil, nil
			}
			return nil, err
		}
		return result.Value, nil

	case string:
		result, err := e.store.Get([]byte(q))
		if err != nil {
			var knf *mvcc.KeyNotFoundError
			if errors.As(err, &knf) {
				return nil, nil
			}
			return nil, err
		}
		return result.Value, nil

	default:
		return nil, &InvalidQueryError{Reason: "unsupported query type"}
	}
}

// listKeys returns a sorted slice of user keys matching the given prefix.
// It iterates the MVCC metadata namespace using canonical keyspace encoding
// to extract user keys.
func (e *MVCCEngine) listKeys(prefix string) ([]string, error) {
	iter := e.stg.NewIterator(&storage.IteratorOptions{
		LowerBound: mvcc.MetaLowerBound(),
		UpperBound: mvcc.MetaUpperBound(),
	})
	defer func() { _ = iter.Close() }()

	// When the prefix lies inside a reserved namespace whose owner enumerates
	// it, skip the internal key filter so that subsystem can list its own
	// records.
	skipFilter := skipInternalKeyFilter(prefix)

	var keys []string
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		userKey, decErr := mvcc.DecodeMetaKey(iter.Key())
		if decErr != nil {
			continue
		}
		userKeyStr := string(userKey)
		if !skipFilter && isInternalKey(userKeyStr) {
			continue
		}
		if prefix == "" || strings.HasPrefix(userKeyStr, prefix) {
			keys = append(keys, userKeyStr)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	sort.Strings(keys)
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

// lookupPrefix returns a map[string][]byte of all keys matching the prefix.
// It iterates the MVCC metadata namespace and reads the latest value for
// each matching key from the MVCC store.
func (e *MVCCEngine) lookupPrefix(q *statemachine.PrefixQuery) (map[string][]byte, error) {
	iter := e.stg.NewIterator(&storage.IteratorOptions{
		LowerBound: mvcc.MetaLowerBound(),
		UpperBound: mvcc.MetaUpperBound(),
	})
	defer func() { _ = iter.Close() }()

	prefix := string(q.Prefix)
	skipFilter := skipInternalKeyFilter(prefix)

	result := make(map[string][]byte)
	count := 0
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		userKey, decErr := mvcc.DecodeMetaKey(iter.Key())
		if decErr != nil {
			continue
		}
		if !strings.HasPrefix(string(userKey), prefix) {
			continue
		}
		if !skipFilter && isInternalKey(string(userKey)) {
			continue
		}
		kvResult, err := e.store.Get(userKey)
		if err != nil || kvResult == nil {
			continue
		}
		result[string(userKey)] = kvResult.Value
		count++
		if q.Limit > 0 && count >= q.Limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

// Close releases all resources held by the engine. All registered indexes
// are closed. The underlying storage is not owned by the engine and must
// be closed by the caller.
func (e *MVCCEngine) Close(_ context.Context) error {
	e.indexMu.Lock()
	defer e.indexMu.Unlock()
	for _, idx := range e.indexes {
		_ = idx.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handler dispatch table
// ---------------------------------------------------------------------------

// buildUpdateHandlers constructs the op code to handler dispatch map.
func (e *MVCCEngine) buildUpdateHandlers() map[byte]mvccHandler {
	return map[byte]mvccHandler{
		kv.OpPut:                    e.handleBaseKVPut,
		kv.OpPutIfAbsent:            e.handleBaseKVPutIfAbsent,
		kv.OpDelete:                 e.handleBaseKVDelete,
		protocol.OpMVCCPut:          e.handleMVCCPut,
		protocol.OpMVCCPutIfAbsent:  e.handleMVCCPutIfAbsent,
		protocol.OpMVCCDelete:       e.handleMVCCDelete,
		protocol.OpMVCCCompact:      handleMVCCCompact,
		protocol.OpTransaction:      e.handleTransaction,
		protocol.OpCompareAndDelete: e.handleCompareAndDelete,
		protocol.OpCompareAndSwap:   e.handleCompareAndSwap,
		protocol.OpBatch:            e.handleBatch,
		protocol.OpLeaseGrant:       handleLeaseGrant,
		protocol.OpLeaseRevoke:      handleLeaseRevoke,
		protocol.OpLeaseKeepAlive:   handleLeaseKeepAlive,
		protocol.OpIndexCreate:      e.handleIndexCreate,
		protocol.OpIndexDrop:        e.handleIndexDrop,
		protocol.OpTSWrite:          e.handleTSWrite,
		protocol.OpTSWriteBatch:     e.handleTSWriteBatch,
		protocol.OpTSWritePoint:     e.handleTSWritePoint,
		protocol.OpTSDeleteSeries:   e.handleTSDeleteSeries,
		protocol.OpTSSetRetention:   e.handleTSSetRetention,
	}
}

// ---------------------------------------------------------------------------
// Application-error helpers
//
// A Raft state machine must never return a Go error from Update() for an
// outcome that is a deterministic consequence of valid client input (e.g. a
// unique-index violation, an invalid lease TTL, or an operation against a
// subsystem not configured on this SM): doing so halts lastApplied and stalls
// the whole group. Such outcomes are encoded into sm.Result.Data via
// protocol.EncodeAppError and surfaced to the client by the service layer
// (service.checkResultError). Only genuine non-deterministic faults (decode of
// a committed entry, storage IO) are returned as Go errors.
// ---------------------------------------------------------------------------

// appErrResult builds a non-fatal application-error result payload.
func appErrResult(msg string) sm.Result {
	return sm.Result{Data: protocol.EncodeAppError(msg)}
}

// asAppErrResult converts a unique-constraint violation into a non-fatal
// application-error result. It returns (result, true, nil) when err is a
// *UniqueConstraintError, and (zero, false, err) for any other (genuinely
// fatal) error so the caller can return it to the Raft layer unchanged.
func asAppErrResult(err error) (sm.Result, bool, error) {
	var uce *UniqueConstraintError
	if errors.As(err, &uce) {
		return appErrResult(uce.Error()), true, nil
	}
	return sm.Result{}, false, err
}

// putsFromOps extracts the indexable puts from a decoded operation set for
// batch unique-constraint pre-validation.
func putsFromOps(ops []protocol.DecodedOp) []indexedPut {
	var puts []indexedPut
	for _, op := range ops {
		if op.Type == protocol.OpMVCCPut {
			puts = append(puts, indexedPut{Key: op.Key, Value: op.Value})
		}
	}
	return puts
}

// deleteKeysFromOps extracts the keys an operation set deletes, so batch
// pre-validation can treat the unique values they hold as freed.
func deleteKeysFromOps(ops []protocol.DecodedOp) [][]byte {
	var keys [][]byte
	for _, op := range ops {
		if op.Type == protocol.OpMVCCDelete {
			keys = append(keys, op.Key)
		}
	}
	return keys
}

// ---------------------------------------------------------------------------
// Base KV handlers
// ---------------------------------------------------------------------------

// decodeBaseKVPut splits a base KV put payload, [keyLen:2 LE][key][value],
// into its key and value. kv.OpPut and kv.OpPutIfAbsent share this shape
// exactly (see kv.EncodePutIfAbsent) -- only the opcode and the guard differ --
// so both handlers decode through here.
func decodeBaseKVPut(payload []byte) (key, value []byte, err error) {
	if len(payload) < 2 {
		return nil, nil, &protocol.InvalidCommandError{Reason: "base kv put: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(keyLen)+2 > len(payload) {
		return nil, nil, &protocol.InvalidCommandError{Reason: "base kv put: key length exceeds payload"}
	}
	return payload[2 : 2+keyLen], payload[2+keyLen:], nil
}

// handleBaseKVPut processes a base KV Put command (kv.OpPut) by decoding the
// simple [keyLen:2 LE][key][value] payload and delegating to the MVCC store
// with leaseID=0. After writing, registered secondary indexes are updated.
func (e *MVCCEngine) handleBaseKVPut(s *mvcc.Store, _ *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	key, value, err := decodeBaseKVPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	prevKv, _ := s.GetInBatch(batch, key)

	// Read-only pre-check: reject a unique-index violation as a non-fatal
	// application error (no state written) rather than letting idx.Put return a
	// fatal error from the apply path, which would stall consensus.
	if verr := CheckUniqueViolation(e.indexes, e.indexFields, e.indexScopes, key, value); verr != nil {
		if res, handled, err := asAppErrResult(verr); handled {
			return res, nil, err
		}
		return sm.Result{}, nil, verr
	}

	// Apply index updates. Uniqueness was pre-validated above, so any error
	// here is a genuine storage fault and is correctly fatal.
	if err := UpdateIndexes(e.indexes, e.indexFields, e.indexScopes, key, value, prevKv); err != nil {
		return sm.Result{}, nil, err
	}

	kvResult, err := s.PutInBatch(batch, key, value, 0, s.CurrentRevision()+1)
	if err != nil {
		return sm.Result{}, nil, err
	}
	s.SetRevision(kvResult.ModRevision)

	notifs := []Notification{{
		EventType: service.EventPut,
		Key:       key,
		KV:        kvResult,
		PrevKV:    prevKv,
	}}
	return sm.Result{Value: uint64(kvResult.ModRevision)}, notifs, nil
}

// handleBaseKVPutIfAbsent processes a base KV conditional put
// (kv.OpPutIfAbsent), storing only when the key is absent.
//
// The existence check runs here, inside the apply path, against the state this
// replica has applied up to this entry's log position -- the same input on
// every replica producing the same decision. Checking on the proposer and then
// proposing a plain Put would be a different operation, with a window in which
// two proposers both observe "absent" and both write.
//
// A declined write is a normal outcome reported through Result.Value
// (kv.ResultNotStored), never an error: the caller that lost the race got a
// correct answer, and an error here would stall consensus instead.
//
// It mirrors PebbleStateMachine's pebbleHandlePutIfAbsent, which is where this
// opcode's semantics are defined; without a handler here, every conditional
// write against an MVCC-engine-backed store -- including the metadata write
// service.TableService.CreateTable performs, and therefore every CREATE TABLE --
// failed the whole proposal with protocol.UnknownOpError.
func (e *MVCCEngine) handleBaseKVPutIfAbsent(s *mvcc.Store, _ *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	key, value, err := decodeBaseKVPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	absent, getErr := keyIsAbsentInBatch(s, batch, key)
	if getErr != nil {
		return sm.Result{}, nil, getErr
	}
	if !absent {
		return sm.Result{Value: kv.ResultNotStored}, nil, nil
	}
	return e.putIfAbsentInBatch(s, nil, batch, key, value, 0)
}

// handleCompareAndDelete removes a key only when its stored value still
// equals the expected one.
//
// The comparison runs inside the apply path against the state this replica
// has applied at this entry's log position, for the same reason the
// conditional put's guard does: comparing on the proposer and then proposing
// a plain delete leaves a window in which another caller legitimately claims
// the key, so the delete removes a value the proposer never observed
// (go-qrdb#41).
//
// A refused delete is reported through Result.Value, never as an error. A
// read FAULT is returned as an error, because deciding "no match" on a fault
// would leave the key here while peers that read successfully removed it.
func (e *MVCCEngine) handleCompareAndDelete(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	key, expected, err := protocol.DecodeCompareAndDelete(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	current, getErr := getInBatchAllowingAbsence(s, batch, key)
	if getErr != nil {
		return sm.Result{}, nil, getErr
	}
	if current == nil || !bytes.Equal(current.Value, expected) {
		return sm.Result{Value: protocol.ResultNotDeleted}, nil, nil
	}

	// The write half mirrors handleMVCCDelete exactly: a tombstone at a fresh
	// revision, lease detach, index retraction, and a DELETE notification
	// carrying the previous value.
	rev := s.CurrentRevision() + 1
	prevKv, derr := s.DeleteInBatch(batch, key, rev)
	if derr != nil {
		return sm.Result{}, nil, derr
	}
	s.SetRevision(rev)

	if lr != nil && prevKv != nil && prevKv.LeaseID > 0 {
		lr.DetachKey(prevKv.LeaseID, key)
	}
	if ierr := RemoveIndexes(e.indexes, e.indexFields, e.indexScopes, key, prevKv); ierr != nil {
		return sm.Result{}, nil, ierr
	}

	notifs := []Notification{{
		EventType: service.EventDelete,
		Key:       key,
		KV:        &service.KeyValue{Key: key, ModRevision: rev},
		PrevKV:    prevKv,
	}}
	return sm.Result{Value: protocol.ResultDeleted}, notifs, nil
}

// handleCompareAndSwap replaces a key's value only when the value visible in
// this batch still equals expected.
//
// The read uses getInBatchAllowingAbsence for the reason the guarded delete
// does: an earlier operation in the same batch is not yet visible to the
// store, so reading past it would evaluate the guard against a stale value and
// decide differently from a replica whose batch was split differently.
//
// An absent key is NOT created. A swap moves a record between two states, and
// there is no first state for a record that does not exist; creating here
// would also make this indistinguishable from OpMVCCPutIfAbsent.
//
// The LEASE SURVIVES the swap. This edits a value, and a value edit is not a
// reason to detach a lease the key already holds — dropping it would silently
// convert a leased key into a permanent one, which is a change to the record's
// lifetime that nobody asked for. The write half otherwise mirrors
// handleMVCCPut: unique pre-check, index update, a fresh revision, and a PUT
// notification carrying the previous value.
func (e *MVCCEngine) handleCompareAndSwap(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	key, expected, next, err := protocol.DecodeCompareAndSwap(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	current, getErr := getInBatchAllowingAbsence(s, batch, key)
	if getErr != nil {
		return sm.Result{}, nil, getErr
	}
	if current == nil || !bytes.Equal(current.Value, expected) {
		return sm.Result{Value: protocol.ResultNotSwapped}, nil, nil
	}

	// Read-only pre-check, as in handleMVCCPut: a unique-index violation is a
	// non-fatal application error, not a reason to stall consensus.
	if verr := CheckUniqueViolation(e.indexes, e.indexFields, e.indexScopes, key, next); verr != nil {
		if res, handled, aerr := asAppErrResult(verr); handled {
			return res, nil, aerr
		}
		return sm.Result{}, nil, verr
	}
	if ierr := UpdateIndexes(e.indexes, e.indexFields, e.indexScopes, key, next, current); ierr != nil {
		return sm.Result{}, nil, ierr
	}

	kvResult, perr := s.PutInBatch(batch, key, next, current.LeaseID, s.CurrentRevision()+1)
	if perr != nil {
		return sm.Result{}, nil, perr
	}
	s.SetRevision(kvResult.ModRevision)

	notifs := []Notification{{
		EventType: service.EventPut,
		Key:       key,
		KV:        kvResult,
		PrevKV:    current,
	}}
	return sm.Result{Value: protocol.ResultSwapped}, notifs, nil
}

// getInBatchAllowingAbsence reads a key inside the apply batch, reporting a
// MISSING key as a nil value and a key that could not be READ as an error.
//
// Every apply-path guard needs this distinction and none may improvise it. Only
// *mvcc.KeyNotFoundError means absent; anything else is a storage fault and
// goes back to the Raft layer. A fault folded into "absent" decides the guard
// as though the key were not there — and because the fault is LOCAL to one
// replica, replicas whose reads succeeded decide the same log entry the other
// way and the group diverges from an identical log (go-qrdb#41, go-qrdb#49).
//
// mvcc.Store.getFrom returns either an error or a non-nil KeyValue, so a nil
// value here means absent and nothing else.
func getInBatchAllowingAbsence(s *mvcc.Store, batch storage.Batch, key []byte) (*service.KeyValue, error) {
	kvResult, err := s.GetInBatch(batch, key)
	if err != nil {
		var notFound *mvcc.KeyNotFoundError
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, err
	}
	return kvResult, nil
}

// keyIsAbsentInBatch answers the existence guard both conditional-put handlers
// ask. It is getInBatchAllowingAbsence for callers that need only the verdict.
func keyIsAbsentInBatch(s *mvcc.Store, batch storage.Batch, key []byte) (bool, error) {
	kvResult, err := getInBatchAllowingAbsence(s, batch, key)
	if err != nil {
		return false, err
	}
	return kvResult == nil, nil
}

// putIfAbsentInBatch performs the write half of a conditional put whose
// absence guard has already passed, shared by the base-KV and MVCC opcodes:
// it pre-validates unique indexes, updates them, writes the value at a fresh
// revision, attaches any lease, and returns the resulting PUT notification.
//
// The returned Result carries kv.ResultStored on success, or the non-fatal
// application-error result of a unique-index violation (no state written) --
// which the caller returns verbatim, exactly as the unconditional put handlers
// do with theirs.
func (e *MVCCEngine) putIfAbsentInBatch(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, key, value []byte, leaseID int64) (sm.Result, []Notification, error) {
	// Read-only pre-check: reject a unique-index violation as a non-fatal
	// application error (no state written) rather than letting idx.Put return a
	// fatal error from the apply path, which would stall consensus.
	if verr := CheckUniqueViolation(e.indexes, e.indexFields, e.indexScopes, key, value); verr != nil {
		if res, handled, err := asAppErrResult(verr); handled {
			return res, nil, err
		}
		return sm.Result{}, nil, verr
	}

	// The guard established the key is absent, so there is no previous value
	// for the index update to retire, and none to report on the notification.
	if err := UpdateIndexes(e.indexes, e.indexFields, e.indexScopes, key, value, nil); err != nil {
		return sm.Result{}, nil, err
	}

	kvResult, err := s.PutInBatch(batch, key, value, leaseID, s.CurrentRevision()+1)
	if err != nil {
		return sm.Result{}, nil, err
	}
	s.SetRevision(kvResult.ModRevision)

	if lr != nil && leaseID > 0 {
		lr.AttachKey(leaseID, key)
	}

	return sm.Result{Value: kv.ResultStored}, []Notification{{
		EventType: service.EventPut,
		Key:       key,
		KV:        kvResult,
	}}, nil
}

// handleBaseKVDelete processes a base KV Delete command (kv.OpDelete) by
// decoding the simple [keyLen:2 LE][key] payload and delegating to the MVCC
// store. After deleting, registered secondary indexes are updated.
func (e *MVCCEngine) handleBaseKVDelete(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	if len(payload) < 2 {
		return sm.Result{}, nil, &protocol.InvalidCommandError{Reason: "base kv delete: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(keyLen)+2 > len(payload) {
		return sm.Result{}, nil, &protocol.InvalidCommandError{Reason: "base kv delete: key length exceeds payload"}
	}
	key := payload[2 : 2+keyLen]

	rev := s.CurrentRevision() + 1
	prevKv, err := s.DeleteInBatch(batch, key, rev)
	if err != nil {
		return sm.Result{}, nil, err
	}
	s.SetRevision(rev)

	if lr != nil && prevKv != nil && prevKv.LeaseID > 0 {
		lr.DetachKey(prevKv.LeaseID, key)
	}

	if err := RemoveIndexes(e.indexes, e.indexFields, e.indexScopes, key, prevKv); err != nil {
		return sm.Result{}, nil, err
	}

	deleteKv := &service.KeyValue{
		Key:         key,
		ModRevision: rev,
	}
	notifs := []Notification{{
		EventType: service.EventDelete,
		Key:       key,
		KV:        deleteKv,
		PrevKV:    prevKv,
	}}
	return sm.Result{Value: uint64(rev)}, notifs, nil
}

// ---------------------------------------------------------------------------
// MVCC handlers
// ---------------------------------------------------------------------------

// handleMVCCPut processes an MVCC Put command. Allocates a new revision and
// writes the key-value pair with metadata into the batch. Registered
// secondary indexes are updated with field values extracted from the JSON
// value.
func (e *MVCCEngine) handleMVCCPut(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	prevKv, _ := s.GetInBatch(batch, key)

	// Read-only pre-check: reject a unique-index violation as a non-fatal
	// application error (no state written) rather than letting idx.Put return a
	// fatal error from the apply path, which would stall consensus.
	if verr := CheckUniqueViolation(e.indexes, e.indexFields, e.indexScopes, key, value); verr != nil {
		if res, handled, err := asAppErrResult(verr); handled {
			return res, nil, err
		}
		return sm.Result{}, nil, verr
	}

	// Apply index updates. Uniqueness was pre-validated above, so any error
	// here is a genuine storage fault and is correctly fatal.
	if err := UpdateIndexes(e.indexes, e.indexFields, e.indexScopes, key, value, prevKv); err != nil {
		return sm.Result{}, nil, err
	}

	kvResult, err := s.PutInBatch(batch, key, value, leaseID, s.CurrentRevision()+1)
	if err != nil {
		return sm.Result{}, nil, err
	}
	s.SetRevision(kvResult.ModRevision)

	if lr != nil && leaseID > 0 {
		lr.AttachKey(leaseID, key)
	}

	notifs := []Notification{{
		EventType: service.EventPut,
		Key:       key,
		KV:        kvResult,
		PrevKV:    prevKv,
	}}

	return sm.Result{Value: uint64(kvResult.ModRevision)}, notifs, nil
}

// handleMVCCPutIfAbsent processes an MVCC conditional put
// (protocol.OpMVCCPutIfAbsent), storing only when the key is absent.
//
// It shares protocol.DecodeMVCCPut with the unconditional handler because the
// payload shape is identical; only the opcode and the guard differ. See
// handleBaseKVPutIfAbsent for why the guard must run here, in the apply path,
// and why a declined write is a result rather than an error.
func (e *MVCCEngine) handleMVCCPutIfAbsent(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	absent, getErr := keyIsAbsentInBatch(s, batch, key)
	if getErr != nil {
		return sm.Result{}, nil, getErr
	}
	if !absent {
		return sm.Result{Value: kv.ResultNotStored}, nil, nil
	}
	return e.putIfAbsentInBatch(s, lr, batch, key, value, leaseID)
}

// handleMVCCDelete processes an MVCC Delete command. Allocates a new revision
// and writes a tombstone into the batch. Registered secondary indexes are
// updated to remove entries for the deleted key.
func (e *MVCCEngine) handleMVCCDelete(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
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

	if lr != nil && prevKv != nil && prevKv.LeaseID > 0 {
		lr.DetachKey(prevKv.LeaseID, key)
	}

	if err := RemoveIndexes(e.indexes, e.indexFields, e.indexScopes, key, prevKv); err != nil {
		return sm.Result{}, nil, err
	}

	deleteKv := &service.KeyValue{
		Key:         key,
		ModRevision: rev,
	}
	notifs := []Notification{{
		EventType: service.EventDelete,
		Key:       key,
		KV:        deleteKv,
		PrevKV:    prevKv,
	}}

	return sm.Result{Value: uint64(rev)}, notifs, nil
}

// handleMVCCCompact processes an MVCC Compact command. The compaction is
// queued into the apply batch: it must see the revisions earlier entries in
// the same batch wrote, and it must become durable in the same commit as they
// do rather than one step ahead of them.
func handleMVCCCompact(s *mvcc.Store, _ *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	revision, err := protocol.DecodeMVCCCompact(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if err := s.CompactInBatch(batch, revision); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// ---------------------------------------------------------------------------
// Transaction and batch handlers
// ---------------------------------------------------------------------------

// handleTransaction processes a compare-and-swap transaction. Evaluates all
// compares against current state, then atomically executes the success or
// failure operation set at a single new revision.
//
// A compare read that FAILS is not a compare that missed. The read error used
// to be discarded here, which made a storage fault indistinguishable from an
// absent key — both arrived at mvcc.Evaluate as a nil value, and the branch was
// chosen on that nil. Since a fault is local to one replica, replicas that read
// the key successfully would run SuccessOps where the faulting replica ran
// FailureOps, from the same log entry: state-machine divergence, which is the
// one outcome a Raft state machine may never produce. Refusing to decide is
// correct precisely when the fault is non-deterministic, so the transaction
// returns the error and executes NEITHER branch (go-qrdb#49).
func (e *MVCCEngine) handleTransaction(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	txn, err := protocol.DecodeTransaction(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	succeeded := true
	for _, dc := range txn.Compares {
		cmp := decodedCompareToService(dc)
		kvResult, getErr := getInBatchAllowingAbsence(s, batch, cmp.Key)
		if getErr != nil {
			return sm.Result{}, nil, getErr
		}
		if !mvcc.Evaluate(&cmp, kvResult) {
			succeeded = false
			break
		}
	}

	var ops []protocol.DecodedOp
	if succeeded {
		ops = txn.SuccessOps
	} else {
		ops = txn.FailureOps
	}

	// Pre-validate unique-index constraints for the whole selected op set
	// (read-only, delete-aware) so a violation rejects the transaction
	// atomically via a non-fatal application error — no partial writes, no
	// consensus stall.
	if verr := CheckUniqueViolationsBatch(e.indexes, e.indexFields, e.indexScopes, putsFromOps(ops), deleteKeysFromOps(ops)); verr != nil {
		if res, handled, err := asAppErrResult(verr); handled {
			return res, nil, err
		}
		return sm.Result{}, nil, verr
	}

	rev := s.CurrentRevision() + 1
	var notifs []Notification
	for _, op := range ops {
		opNotifs, opErr := e.executeDecodedOpWithNotify(s, lr, batch, op, rev)
		if opErr != nil {
			return sm.Result{}, nil, opErr
		}
		notifs = append(notifs, opNotifs...)
	}
	if len(ops) > 0 {
		s.SetRevision(rev)
	}

	var data [1]byte
	if succeeded {
		data[0] = 1
	}
	return sm.Result{Value: uint64(rev), Data: data[:]}, notifs, nil
}

// handleBatch processes a batch of operations. All operations are executed
// atomically at a single revision.
func (e *MVCCEngine) handleBatch(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	// Pre-validate unique-index constraints for the whole batch (read-only,
	// delete-aware) so a violation rejects the batch atomically via a non-fatal
	// application error rather than stalling consensus mid-apply.
	if verr := CheckUniqueViolationsBatch(e.indexes, e.indexFields, e.indexScopes, putsFromOps(ops), deleteKeysFromOps(ops)); verr != nil {
		if res, handled, err := asAppErrResult(verr); handled {
			return res, nil, err
		}
		return sm.Result{}, nil, verr
	}

	rev := s.CurrentRevision() + 1
	var notifs []Notification
	for _, op := range ops {
		opNotifs, opErr := e.executeDecodedOpWithNotify(s, lr, batch, op, rev)
		if opErr != nil {
			return sm.Result{}, nil, opErr
		}
		notifs = append(notifs, opNotifs...)
	}
	if len(ops) > 0 {
		s.SetRevision(rev)
	}

	return sm.Result{Value: uint64(rev)}, notifs, nil
}

// decodedCompareToService converts a protocol.DecodedCompare to a
// service.Compare.
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
// notifications. Used by both transaction and batch handlers. Secondary
// indexes are updated for put and delete operations.
func (e *MVCCEngine) executeDecodedOpWithNotify(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, op protocol.DecodedOp, rev int64) ([]Notification, error) {
	switch op.Type {
	case protocol.OpMVCCPut:
		prevKv, _ := s.GetInBatch(batch, op.Key)
		// Validate index constraints before writing to the batch so that a
		// unique violation aborts the operation without leaving partial state.
		if err := UpdateIndexes(e.indexes, e.indexFields, e.indexScopes, op.Key, op.Value, prevKv); err != nil {
			return nil, err
		}
		kvResult, err := s.PutInBatch(batch, op.Key, op.Value, op.LeaseID, rev)
		if err != nil {
			return nil, err
		}
		if lr != nil && op.LeaseID > 0 {
			lr.AttachKey(op.LeaseID, op.Key)
		}
		return []Notification{{
			EventType: service.EventPut,
			Key:       op.Key,
			KV:        kvResult,
			PrevKV:    prevKv,
		}}, nil

	case protocol.OpMVCCDelete:
		prevKv, err := s.DeleteInBatch(batch, op.Key, rev)
		if err != nil {
			return nil, err
		}
		if lr != nil && prevKv != nil && prevKv.LeaseID > 0 {
			lr.DetachKey(prevKv.LeaseID, op.Key)
		}
		if err := RemoveIndexes(e.indexes, e.indexFields, e.indexScopes, op.Key, prevKv); err != nil {
			return nil, err
		}
		deleteKv := &service.KeyValue{
			Key:         op.Key,
			ModRevision: rev,
		}
		return []Notification{{
			EventType: service.EventDelete,
			Key:       op.Key,
			KV:        deleteKv,
			PrevKV:    prevKv,
		}}, nil

	default:
		return nil, &protocol.UnknownOpError{Op: op.Type}
	}
}

// ---------------------------------------------------------------------------
// Lease handlers
// ---------------------------------------------------------------------------

// handleLeaseGrant creates a new lease with the specified TTL.
func handleLeaseGrant(_ *mvcc.Store, lr *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if lr == nil {
		// Lease subsystem not configured on this SM: surface as a non-fatal
		// application error so the apply loop keeps advancing.
		return appErrResult((&NotOpenError{}).Error()), nil, nil
	}

	info, err := lr.Grant(ttl)
	if err != nil {
		// Invalid TTL (and similar input-shaped grant failures) are
		// deterministic application errors, not corruption: surface via the
		// result payload rather than stalling consensus.
		return appErrResult(err.Error()), nil, nil
	}

	return sm.Result{Value: uint64(info.ID)}, nil, nil
}

// handleLeaseRevoke expires a lease and deletes all attached keys at a new
// revision. If the lease does not exist the operation is a no-op; Raft
// state machine handlers must be idempotent so cleanup proposals (e.g.
// deferred test teardown or duplicate revoke) do not produce a fatal Apply
// error that would stall consensus.
func handleLeaseRevoke(s *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if lr == nil {
		return appErrResult((&NotOpenError{}).Error()), nil, nil
	}

	keys, err := lr.Revoke(leaseID)
	if err != nil {
		var nfe *lease.NotFoundError
		if errors.As(err, &nfe) {
			// Lease already gone — idempotent success.
			return sm.Result{Value: 0}, nil, nil
		}
		return sm.Result{}, nil, err
	}

	var notifs []Notification
	if len(keys) > 0 {
		rev := s.CurrentRevision() + 1
		for _, key := range keys {
			prevKv, delErr := s.DeleteInBatch(batch, key, rev)
			if delErr != nil {
				// A lease-attached key that is already absent is expected (it
				// was deleted independently); a genuine storage write failure is
				// not. Log it so a silent durability fault is observable rather
				// than swallowed. The op stays deterministic across replicas.
				slog.Warn("engine: lease revoke could not delete attached key",
					"lease_id", leaseID, "error", delErr)
				continue
			}
			deleteKv := &service.KeyValue{
				Key:         key,
				ModRevision: rev,
			}
			notifs = append(notifs, Notification{
				EventType: service.EventDelete,
				Key:       key,
				KV:        deleteKv,
				PrevKV:    prevKv,
			})
		}
		if len(notifs) > 0 {
			s.SetRevision(rev)
		}
	}

	return sm.Result{Value: uint64(len(keys))}, notifs, nil
}

// handleLeaseKeepAlive resets a lease's TTL.
func handleLeaseKeepAlive(_ *mvcc.Store, lr *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if lr == nil {
		return appErrResult((&NotOpenError{}).Error()), nil, nil
	}

	_, err = lr.KeepAlive(leaseID)
	if err != nil {
		var nfe *lease.NotFoundError
		if errors.As(err, &nfe) {
			// Lease already gone — idempotent success.
			return sm.Result{Value: 0}, nil, nil
		}
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: uint64(leaseID)}, nil, nil
}

// ---------------------------------------------------------------------------
// Index handlers
// ---------------------------------------------------------------------------

// handleIndexCreate creates a new secondary index. When a pebbleDB instance
// is available, the index is backed by PebbleDB. Otherwise, an in-memory
// index is used. After creation, existing data is backfilled into the index.
func (e *MVCCEngine) handleIndexCreate(store *mvcc.Store, _ *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	name, fieldName, opts, err := protocol.DecodeIndexCreateOpts(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	e.indexMu.Lock()
	if _, exists := e.indexes[name]; exists {
		// Index already present — idempotent success so Raft Apply does not
		// stall on duplicate create proposals. NOTE: existence is keyed by name
		// only; a re-create that reuses the name with a DIVERGENT definition
		// (different field/uniqueness) is a no-op and the original definition
		// wins. Reconciling a divergent re-create must be rejected at the
		// service layer before the proposal (returning an error here would
		// stall consensus).
		e.indexMu.Unlock()
		return sm.Result{Value: 1}, nil, nil
	}

	valueType := index.IndexValueTypeString
	if opts.Numeric {
		valueType = index.IndexValueTypeNumeric
	}

	// Sparse is carried on the config for fidelity with the command, but no
	// Index implementation reads it: skipping absent fields happens in the
	// backfill/maintenance callers (see indexing.go), not in the index.
	cfg := index.IndexConfig{
		Name:      name,
		Unique:    opts.Unique,
		Sparse:    opts.Sparse,
		ValueType: valueType,
		// The key scope the creator declared, relative to the namespace the
		// name carries. Recorded on the config so the index's definition is
		// complete; the apply path reads the composed scope out of
		// e.indexScopes (go-qrdb#410).
		KeyPrefix: opts.KeyPrefix,
		// Derived from the command instead of left at its zero value. This
		// engine used to discard the flag, so an index:"case_insensitive"
		// definition became a case-SENSITIVE index and a lookup for "Alpha"
		// over a stored "alpha" answered empty with a nil error (go-qrdb#34).
		CaseSensitive: !opts.CaseInsensitive,
	}

	var idx index.Index
	if e.pebbleDB != nil {
		pebbleIdx, pebbleErr := index.NewPebbleIndexWithConfig(e.pebbleDB, name, cfg)
		if pebbleErr != nil {
			e.indexMu.Unlock()
			return sm.Result{}, nil, pebbleErr
		}
		idx = pebbleIdx
	} else {
		idx = index.NewMemoryIndexWithConfig(name, cfg)
	}

	e.indexMu.Unlock()

	// The key prefix every key this index may hold must carry: the namespace
	// read back off the name plus the prefix the creator declared. It bounds
	// both the backfill below and every subsequent maintenance call
	// (go-qrdb#410).
	scope := ComposeIndexScope(name, opts.KeyPrefix)

	// Backfill BEFORE publishing the index into e.indexes/e.indexFields, so a
	// concurrent Lookup (which only takes indexMu.RLock() to fetch the *index.Index
	// reference, then reads it unlocked) can never observe a partially-populated
	// index. Update() entries are applied strictly sequentially by the single
	// Raft apply goroutine, so no concurrent write can race this handler for the
	// same index name; only concurrent reads via Lookup are possible, and those
	// are safe once the index is only made visible in its fully-backfilled state.
	// The backfill scan reads through the apply batch so the index includes the
	// keys written by earlier entries of the same batch. Scanning committed
	// storage would make the index's contents depend on how the apply loop
	// happened to group the log.
	//
	// The scan is bounded by the scope rather than filtered after the fact, so
	// creating an index on one database does not read every other database's
	// keys on the way past.
	start, end := backfillRange(scope)
	BackfillIndex(func() ([]*service.KeyValue, error) {
		kvs, _, _, err := store.RangeInBatch(batch, start, end)
		return kvs, err
	}, idx, fieldName, scope)

	e.indexMu.Lock()
	e.indexes[name] = idx
	e.indexFields[name] = fieldName
	e.indexScopes[name] = scope
	e.indexMu.Unlock()

	return sm.Result{Value: 1}, nil, nil
}

// handleIndexDrop removes a secondary index and clears all its entries.
// If the index does not exist the operation is a no-op; Raft state machine
// handlers must be idempotent so that cleanup proposals (e.g. deferred test
// teardown) do not return a fatal Apply error that would stall consensus.
func (e *MVCCEngine) handleIndexDrop(_ *mvcc.Store, _ *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	name, err := protocol.DecodeIndexDrop(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	e.indexMu.Lock()
	idx, ok := e.indexes[name]
	if !ok {
		// Index already gone — idempotent success.
		e.indexMu.Unlock()
		return sm.Result{Value: 1}, nil, nil
	}
	delete(e.indexes, name)
	delete(e.indexFields, name)
	delete(e.indexScopes, name)
	e.indexMu.Unlock()

	if err := idx.Clear(); err != nil {
		return sm.Result{}, nil, err
	}
	if err := idx.Close(); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// ---------------------------------------------------------------------------
// Time-series handlers
// ---------------------------------------------------------------------------

// handleTSWrite stores a single time-series data point.
func (e *MVCCEngine) handleTSWrite(_ *mvcc.Store, _ *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	if e.ts == nil {
		// Time-series subsystem not configured on this SM: non-fatal
		// application error so the apply loop keeps advancing.
		return appErrResult((&NotOpenError{}).Error()), nil, nil
	}

	params, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	if err := e.ts.Write(params.Metric, params.Value, tsTimestampFromNano(params.Timestamp), params.Tags); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// handleTSWriteBatch stores multiple time-series data points atomically.
func (e *MVCCEngine) handleTSWriteBatch(_ *mvcc.Store, _ *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	if e.ts == nil {
		// Time-series subsystem not configured on this SM: non-fatal
		// application error so the apply loop keeps advancing.
		return appErrResult((&NotOpenError{}).Error()), nil, nil
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

	if err := e.ts.WriteBatch(points); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: uint64(len(points))}, nil, nil
}

// handleTSWritePoint stores a single validated time-series data point.
func (e *MVCCEngine) handleTSWritePoint(_ *mvcc.Store, _ *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	if e.ts == nil {
		// Time-series subsystem not configured on this SM: non-fatal
		// application error so the apply loop keeps advancing.
		return appErrResult((&NotOpenError{}).Error()), nil, nil
	}

	params, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}

	dp := &timeseries.DataPoint{
		Metric:    params.Metric,
		Value:     params.Value,
		Timestamp: tsTimestampFromNano(params.Timestamp),
		Tags:      params.Tags,
	}
	if err := e.ts.WritePoint(context.Background(), dp); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// handleTSDeleteSeries deletes time-series data matching the request criteria.
func (e *MVCCEngine) handleTSDeleteSeries(_ *mvcc.Store, _ *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	if e.ts == nil {
		// Time-series subsystem not configured on this SM: non-fatal
		// application error so the apply loop keeps advancing.
		return appErrResult((&NotOpenError{}).Error()), nil, nil
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
		req.StartTime = tsTimestampFromNano(params.StartNs)
	}
	if params.EndNs != 0 {
		req.EndTime = tsTimestampFromNano(params.EndNs)
	}

	if err := e.ts.DeleteSeries(context.Background(), req); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// handleTSSetRetention sets the retention policy for a metric.
func (e *MVCCEngine) handleTSSetRetention(_ *mvcc.Store, _ *lease.Lessor, _ storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	if e.ts == nil {
		// Time-series subsystem not configured on this SM: non-fatal
		// application error so the apply loop keeps advancing.
		return appErrResult((&NotOpenError{}).Error()), nil, nil
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

	if err := e.ts.SetRetentionPolicy(params.Metric, policy); err != nil {
		return sm.Result{}, nil, err
	}

	return sm.Result{Value: 1}, nil, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// tsTimestampFromNano converts a nanosecond Unix timestamp to time.Time.
func tsTimestampFromNano(ns int64) time.Time {
	return time.Unix(0, ns)
}

// isInternalKey reports whether k belongs to an internal subsystem's reserved
// namespace and must therefore be excluded from a user-facing key listing.
func isInternalKey(k string) bool {
	return service.IsInternalKey(k)
}

// skipInternalKeyFilter reports whether an enumeration rooted at prefix is the
// owning subsystem reading its own reserved namespace back, in which case the
// filter must not be applied or the subsystem would be hidden from its own
// records.
func skipInternalKeyFilter(prefix string) bool {
	return service.SkipInternalKeyFilter(prefix)
}

// scanChanges returns the distinct keys mutated in the query's inclusive
// revision window, restricted to the query's prefix and sorted ascending.
//
// A key mutated several times in the window appears once: the caller wants the
// SET of affected keys, not the change log. Values are not returned (see
// service.MVCCScanChangesQuery).
//
// No internal-key filtering happens here, matching the MVCCRangeQuery path,
// which likewise returns whatever the requested range contains. The internal
// namespace is excluded by the caller that needs it excluded — for selective
// restore that is service.MVCCKVService, where the refusal is a security
// guard with its own tests, rather than a property a general-purpose query
// silently imposes on every future consumer.
//
// mvcc.Store.ScanChanges fails closed with a *mvcc.RevisionCompactedError when
// a change's data entry has been compacted out from under the scan; that error
// is propagated unchanged so the caller cannot mistake a truncated history for
// a complete one.
func (e *MVCCEngine) scanChanges(q service.MVCCScanChangesQuery) (*service.MVCCScanChangesResult, error) {
	result := &service.MVCCScanChangesResult{Revision: e.store.CurrentRevision()}

	prefix := string(q.Prefix)
	seen := make(map[string]struct{})

	err := e.store.ScanChanges(q.StartRevision, q.EndRevision, func(ch mvcc.Change) bool {
		key := string(ch.Key)
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return true
		}
		if _, dup := seen[key]; dup {
			return true
		}
		if q.Limit > 0 && int64(len(result.Keys)) >= q.Limit {
			result.Truncated = true
			return false
		}
		seen[key] = struct{}{}
		result.Keys = append(result.Keys, key)
		return true
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(result.Keys)
	return result, nil
}

// Compile-time assertion.
var _ Engine = (*MVCCEngine)(nil)
