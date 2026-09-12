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
	"log/slog"
	"sort"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	qkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Compile-time interface checks.
var (
	_ Engine           = (*KVEngine)(nil)
	_ EntityRegisterer = (*KVEngine)(nil)
)

// kvHandler processes a single decoded kv.Command and returns the result.
type kvHandler func(e *KVEngine, cmd kv.Command) (sm.Result, error)

// extHandler processes a decoded extended protocol payload and returns the result.
type extHandler func(payload []byte) (sm.Result, error)

// KVEngine is a bare-bones key-value engine that wraps storage.Storage
// directly for maximum speed. It supports basic Put, Get, Delete, List,
// and Scan operations with optional secondary indexes and watch
// notifications. No MVCC overhead, no revision tracking, no leases,
// no time-series.
type KVEngine struct {
	stg         storage.Storage
	indexes     map[string]index.Index
	indexFields map[string]string
	// indexScopes holds the key scope of each index by name: the byte prefix
	// every key it indexes must carry (see ComposeIndexScope). A nil entry is
	// an index with no derivable and no declared scope, which covers the whole
	// state machine.
	indexScopes   map[string][]byte
	entityIndexes map[string][]EntityIndex
	// compoundIndexes stores compound (multi-field) indexes keyed by
	// their fully-qualified name (e.g., "mydb.messages.ConversationID_Timestamp").
	compoundIndexes map[string]*index.CompoundIndex
	// compoundIndexFields maps compound index name to an ordered list of
	// JSON field names used for value extraction during updates.
	compoundIndexFields map[string][]string
	// compoundIndexScopes holds the key scope of each compound index by name,
	// exactly as indexScopes does for single-field indexes.
	compoundIndexScopes map[string][]byte
	hub                 *watch.Hub
	pebbleDB            *pebble.DB
	logger              *logger.Logger
	handlers            map[byte]kvHandler
	extHandlers         map[byte]extHandler
	// database is the optional database namespace prefix for index names.
	// When non-empty, RegisterEntityType prefixes index names as
	// "{database}.{entityType}.{FieldName}" for per-database isolation.
	database string
}

// KVOption configures a KVEngine instance.
type KVOption func(*KVEngine)

// WithKVWatchHub attaches a watch.Hub for real-time change notifications
// on Put and Delete operations.
func WithKVWatchHub(hub *watch.Hub) KVOption {
	return func(e *KVEngine) {
		e.hub = hub
	}
}

// WithKVPebbleDB provides a PebbleDB instance for durable secondary
// indexes. When set, index create operations use PebbleIndex; otherwise
// they fall back to MemoryIndex.
func WithKVPebbleDB(db *pebble.DB) KVOption {
	return func(e *KVEngine) {
		e.pebbleDB = db
	}
}

// WithKVLogger attaches a logger for index creation and entity registration
// diagnostics.
func WithKVLogger(log *logger.Logger) KVOption {
	return func(e *KVEngine) {
		e.logger = log
	}
}

// WithKVDatabase sets the database namespace prefix for index names.
// When non-empty, RegisterEntityType prefixes index names as
// "{database}.{entityType}.{FieldName}" to isolate indexes across databases.
// Leave empty for backward-compatible single-database behaviour.
func WithKVDatabase(database string) KVOption {
	return func(e *KVEngine) {
		e.database = database
	}
}

// NewKVEngine creates a KVEngine backed by the given storage.
func NewKVEngine(stg storage.Storage, opts ...KVOption) *KVEngine {
	e := &KVEngine{
		stg:                 stg,
		indexes:             make(map[string]index.Index),
		indexFields:         make(map[string]string),
		indexScopes:         make(map[string][]byte),
		entityIndexes:       make(map[string][]EntityIndex),
		compoundIndexes:     make(map[string]*index.CompoundIndex),
		compoundIndexFields: make(map[string][]string),
		compoundIndexScopes: make(map[string][]byte),
	}
	for _, opt := range opts {
		opt(e)
	}
	e.handlers = e.buildHandlers()
	e.extHandlers = e.buildExtHandlers()
	return e
}

// Type returns the engine type identifier.
func (e *KVEngine) Type() string { return "kv" }

// Update applies a batch of entries to the underlying storage engine.
// Each entry is decoded using the base KV wire protocol first. If that
// fails, the extended protocol is tried to support index operations.
// MVCC, lease, transaction, and time-series opcodes are refused with
// UnsupportedOperationError, which travels back in the result payload rather
// than halting Apply (statemachine.ResolveApplyError; go-qrdb#520). An entry
// undecodable by both protocols yields an application-error result (see
// CommandDecodeError) rather than being silently reported as applied.
func (e *KVEngine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		// Try base KV protocol (OpPut=1, OpDelete=3).
		cmd, err := kv.DecodeCommand(entries[i].Cmd)
		if err == nil {
			var result sm.Result
			var hErr error
			handler, ok := e.handlers[cmd.Op]
			if ok {
				result, hErr = handler(e, cmd)
			} else {
				hErr = &UnsupportedOperationError{Op: cmd.Op}
			}
			resolved, stop, applyErr := statemachine.ResolveApplyError(slog.Default(), entries[i].Index, result, hErr)
			if stop {
				// The failure marker predates the classifier and is kept: Raft
				// discards results for a halted Apply, but LocalBackend calls
				// Update directly and reads them.
				results[i] = sm.Result{Value: 1}
				return applyErr
			}
			results[i] = resolved
			continue
		}

		// Try extended protocol for index and batch operations.
		extCmd, extErr := protocol.DecodeCommand(entries[i].Cmd)
		if extErr != nil {
			// Undecodable by both protocols: this is a deterministic
			// consequence of the specific committed bytes (corruption or a
			// version-skewed writer), not engine state. Surface it as an
			// application error rather than reporting Value:1 (SUCCESS) —
			// silently dropping the write — or returning a Go error, which
			// would stall consensus.
			decErr := &CommandDecodeError{KVErr: err, ExtErr: extErr}
			results[i] = sm.Result{Data: protocol.EncodeAppError(decErr.Error())}
			continue
		}

		var result sm.Result
		var extHErr error
		extFn, ok := e.extHandlers[extCmd.Op]
		if ok {
			result, extHErr = extFn(extCmd.Payload)
		} else {
			extHErr = &UnsupportedOperationError{Op: extCmd.Op}
		}
		resolved, stop, applyErr := statemachine.ResolveApplyError(slog.Default(), entries[i].Index, result, extHErr)
		if stop {
			results[i] = sm.Result{Value: 1}
			return applyErr
		}
		results[i] = resolved
	}
	return nil
}

// Lookup performs a read query against the storage engine. Supported
// query types:
//   - []byte: interpreted as a key for Get
//   - string: interpreted as a key for Get
//   - kv.ListQuery: lists keys matching the prefix
//   - *statemachine.PrefixQuery: scans keys matching the prefix
//   - index.IndexQueryRequest: exact-match index query
//   - index.IndexScanRequest: range index scan
//   - index.IndexListRequest: list all index names
//
// All other types return InvalidQueryError.
func (e *KVEngine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	switch q := query.(type) {
	case []byte:
		return e.lookupKey(q)

	case string:
		return e.lookupKey([]byte(q))

	case kv.ListQuery:
		return e.listKeys(q.Prefix)

	case *statemachine.PrefixQuery:
		return e.scanPrefix(q)

	case statemachine.PrefixQuery:
		return e.scanPrefix(&q)

	case index.IndexQueryRequest:
		return e.lookupIndexQuery(q)

	case index.IndexScanRequest:
		return e.lookupIndexScan(q)

	case index.IndexListRequest:
		return e.lookupIndexList()

	case index.CompoundIndexQueryRequest:
		return e.lookupCompoundIndexQuery(q)

	case index.CompoundIndexScanRequest:
		return e.lookupCompoundIndexScan(q)

	default:
		return nil, &InvalidQueryError{Reason: "unsupported query type"}
	}
}

// Close releases all resources held by secondary indexes. The
// underlying storage is not closed by the engine; ownership remains
// with the caller.
func (e *KVEngine) Close(_ context.Context) error {
	for _, idx := range e.indexes {
		_ = idx.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handler builders
// ---------------------------------------------------------------------------

// buildHandlers creates the base KV protocol handler dispatch map.
func (e *KVEngine) buildHandlers() map[byte]kvHandler {
	return map[byte]kvHandler{
		kv.OpPut:         kvHandlePut,
		kv.OpPutIfAbsent: kvHandlePutIfAbsent,
		kv.OpDelete:      kvHandleDelete,
	}
}

// buildExtHandlers creates the extended protocol handler dispatch map.
// Index, batch, the single-key guarded verbs, and the multi-key compare-and-
// execute transaction are supported; MVCC-, lease-, and time-series-specific
// opcodes are absent and will trigger UnsupportedOperationError.
func (e *KVEngine) buildExtHandlers() map[byte]extHandler {
	return map[byte]extHandler{
		protocol.OpIndexCreate:      e.handleIndexCreate,
		protocol.OpIndexDrop:        e.handleIndexDrop,
		protocol.OpBatch:            e.handleBatch,
		protocol.OpCompareAndDelete: e.handleCompareAndDelete,
		protocol.OpCompareAndSwap:   e.handleCompareAndSwap,
		protocol.OpTransaction:      e.handleTransaction,
	}
}

// ---------------------------------------------------------------------------
// Base KV handlers
// ---------------------------------------------------------------------------

// kvHandlePut stores a key-value pair, updates secondary indexes atomically
// in the same batch, and emits a watch notification after commit.
func kvHandlePut(e *KVEngine, cmd kv.Command) (sm.Result, error) {
	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Read previous value for index cleanup and watch notification.
	var prevKv *qkv.KeyValue
	if len(e.indexes) > 0 || e.hub != nil {
		oldVal, _ := e.stg.Get(cmd.Key)
		if oldVal != nil {
			prevKv = &qkv.KeyValue{Key: cmd.Key, Value: oldVal}
		}
	}

	// Queue data write.
	if err := batch.Set(cmd.Key, cmd.Value); err != nil {
		return sm.Result{Value: 1}, err
	}

	// Queue index updates into the SAME batch.
	if len(e.indexes) > 0 {
		if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, cmd.Key, cmd.Value, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}

	// Queue compound index updates into the SAME batch.
	if len(e.compoundIndexes) > 0 {
		if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, cmd.Key, cmd.Value, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}

	// Commit atomically.
	if err := batch.Commit(); err != nil {
		return sm.Result{Value: 1}, err
	}

	// Watch notification AFTER successful commit.
	if e.hub != nil {
		kvVal := &qkv.KeyValue{Key: cmd.Key, Value: cmd.Value}
		e.hub.NotifyPut(cmd.Key, kvVal, prevKv)
	}

	return sm.Result{Value: uint64(len(cmd.Value))}, nil
}

// kvHandlePutIfAbsent processes a base KV conditional put (kv.OpPutIfAbsent),
// storing only when the key is absent.
//
// The existence check runs HERE, inside the apply path, against the state this
// replica has applied up to this entry's log position — the same input on
// every replica producing the same decision, which is what keeps the replicas
// identical. Checking on the proposer and then proposing a plain Put would be
// a different operation, with a window in which two proposers both observe
// "absent" and both write.
//
// A declined write is a normal outcome reported through Result.Value
// (kv.ResultNotStored), never an error: the caller that lost the race got a
// correct answer, and an error here would stall consensus instead.
//
// It mirrors PebbleStateMachine's pebbleHandlePutIfAbsent and MVCCEngine's
// handleBaseKVPutIfAbsent, which is where this opcode's semantics are defined.
// Without a handler here, every conditional write against a KVEngine-backed
// store failed the entire proposal with UnsupportedOperationError — including
// the claim tenant.IdentityIndex takes before writing an identity record
// (#41). That claim is what stops one tenant from creating a role carrying
// ANOTHER tenant's caller-supplied role id, capturing that id's entry in the
// system-plane index, and silently redirecting every by-id operation an
// administrator performs on it.
func kvHandlePutIfAbsent(e *KVEngine, cmd kv.Command) (sm.Result, error) {
	// Decline before opening a batch: the losing caller writes nothing at all.
	//
	// The read error is PROPAGATED, never folded into "absent". storage.Reader
	// documents a missing key as (nil, nil), so a non-nil error here is a real
	// fault — and treating a fault as absence would overwrite the holder and
	// report ResultStored. Worse, in a replicated group the fault is LOCAL:
	// replicas whose reads succeeded would decline the same entry this one
	// stored, and the group would diverge from an identical log. A genuine
	// non-deterministic fault is exactly the case a state machine must return
	// as a Go error rather than decide.
	existing, gerr := e.stg.Get(cmd.Key)
	if gerr != nil {
		return sm.Result{Value: 1}, gerr
	}
	if existing != nil {
		return sm.Result{Value: kv.ResultNotStored}, nil
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Queue data write.
	if err := batch.Set(cmd.Key, cmd.Value); err != nil {
		return sm.Result{Value: 1}, err
	}

	// The guard established the key is absent, so there is no previous value
	// for the index updates to retract: prevKv is nil by construction, not by
	// omission.
	if len(e.indexes) > 0 {
		if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, cmd.Key, cmd.Value, nil); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if len(e.compoundIndexes) > 0 {
		if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, cmd.Key, cmd.Value, nil); err != nil {
			return sm.Result{Value: 1}, err
		}
	}

	// Commit atomically.
	if err := batch.Commit(); err != nil {
		return sm.Result{Value: 1}, err
	}

	// Watch notification AFTER successful commit, with no prevKv.
	if e.hub != nil {
		e.hub.NotifyPut(cmd.Key, &qkv.KeyValue{Key: cmd.Key, Value: cmd.Value}, nil)
	}

	return sm.Result{Value: kv.ResultStored}, nil
}

// handleCompareAndDelete removes a key only when its stored value still
// equals the expected one.
//
// The comparison runs HERE, inside the apply path, against the state this
// replica has applied at this entry's log position — the same input on every
// replica producing the same decision. Comparing on the proposer and then
// proposing a plain delete is two operations with a window between them, and
// that window is the whole defect: two releasers both read holder A, the
// first deletes, a third party legitimately claims the now-absent key, and
// the second delete removes THEIR value (#41).
//
// A refused delete is a normal outcome reported through Result.Value
// (protocol.ResultNotDeleted), never an error — the caller that lost the race
// got a correct answer, and an error would stall consensus instead.
//
// The existence read PROPAGATES its error rather than treating a fault as
// "no match". The read-fault rule applies to a delete guard exactly as it
// does to a conditional put's: a local fault read as "value differs" would
// leave the key on this replica while peers that read successfully removed
// it, diverging the group from an identical log.
func (e *KVEngine) handleCompareAndDelete(payload []byte) (sm.Result, error) {
	key, expected, err := protocol.DecodeCompareAndDelete(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}

	current, gerr := e.stg.Get(key)
	if gerr != nil {
		return sm.Result{Value: 1}, gerr
	}
	if current == nil || !bytes.Equal(current, expected) {
		return sm.Result{Value: protocol.ResultNotDeleted}, nil
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	prevKv := &qkv.KeyValue{Key: key, Value: current}

	if err := batch.Delete(key); err != nil {
		return sm.Result{Value: 1}, err
	}
	if len(e.indexes) > 0 {
		if err := RemoveIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, key, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if len(e.compoundIndexes) > 0 {
		if err := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, key, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if err := batch.Commit(); err != nil {
		return sm.Result{Value: 1}, err
	}

	if e.hub != nil {
		e.hub.NotifyDelete(key, &qkv.KeyValue{Key: key}, prevKv)
	}

	return sm.Result{Value: protocol.ResultDeleted}, nil
}

// handleCompareAndSwap replaces a key's value only when its stored value still
// equals expected.
//
// An absent key is NOT created — a swap moves a record between two states, and
// a record that does not exist has no first state. Use OpMVCCPutIfAbsent to
// create.
//
// The index reconciliation is a REPLACE, and it goes through the same
// UpdateIndexesBatch the unguarded put uses, passing the previous value so the
// old entries are retracted as the new ones land. Calling RemoveIndexesBatch
// first as the guarded DELETE does would retract twice — Update already does
// it from prevKv — and a second definition of "what it means to store a value"
// is exactly the drift the guard is meant to avoid.
func (e *KVEngine) handleCompareAndSwap(payload []byte) (sm.Result, error) {
	key, expected, next, err := protocol.DecodeCompareAndSwap(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}

	current, gerr := e.stg.Get(key)
	if gerr != nil {
		return sm.Result{Value: 1}, gerr
	}
	if current == nil || !bytes.Equal(current, expected) {
		return sm.Result{Value: protocol.ResultNotSwapped}, nil
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	prevKv := &qkv.KeyValue{Key: key, Value: current}
	nextKv := &qkv.KeyValue{Key: key, Value: next}

	if err := batch.Set(key, next); err != nil {
		return sm.Result{Value: 1}, err
	}
	if len(e.indexes) > 0 {
		if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, key, next, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if len(e.compoundIndexes) > 0 {
		if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, key, next, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if err := batch.Commit(); err != nil {
		return sm.Result{Value: 1}, err
	}

	if e.hub != nil {
		e.hub.NotifyPut(key, nextKv, prevKv)
	}

	return sm.Result{Value: protocol.ResultSwapped}, nil
}

// handleTransaction applies an etcd-style compare-and-execute transaction over
// the base KV store: every compare is evaluated against current state, and the
// success or failure operation set is then applied atomically in a single
// batch. It is the file/objstore engine's counterpart to the MVCC and PebbleDB
// transaction handlers, so CompareAndSwap, PutIfAbsent, and multi-key
// conditional writes have identical semantics on every engine (go-qrdb#117).
//
// This engine keeps no MVCC revisions, so create_revision/mod_revision compares
// have nothing to compare against. They are refused with an application error
// — exactly as the PebbleDB backend refuses them — rather than silently reading
// revision 0: a client written for the MVCC engine does read-then-CAS on a
// revision, would read 0 here, compare equal, and commit, so two concurrent
// writers both "succeed" and one update is lost with no signal. The refusal is
// encoded into Result.Data (not returned as a Go error, which would stall the
// Raft apply loop for every client).
func (e *KVEngine) handleTransaction(payload []byte) (sm.Result, error) {
	txn, err := protocol.DecodeTransaction(payload)
	if err != nil {
		return sm.Result{}, err
	}

	for _, dc := range txn.Compares {
		if service.CompareTarget(dc.Target) == service.CompareCreateRevision ||
			service.CompareTarget(dc.Target) == service.CompareModRevision {
			return appErrResult("transaction: create_revision/mod_revision compares require an " +
				"MVCC state machine; this node runs a non-MVCC (file/objstore) backend. " +
				"Compare on value or version instead."), nil
		}
	}

	succeeded := true
	for _, dc := range txn.Compares {
		ok, cerr := e.evaluateTxnCompare(dc)
		if cerr != nil {
			// A storage read failure is a genuine, possibly non-deterministic
			// fault, NOT a compare miss: swallowing it would let one replica
			// read the key and take the failure branch while another fails the
			// read and takes the success branch — a state-machine divergence,
			// and a fail-open on absence guards. Return it to the Raft layer.
			return sm.Result{}, cerr
		}
		if !ok {
			succeeded = false
			break
		}
	}

	ops := txn.SuccessOps
	if !succeeded {
		ops = txn.FailureOps
	}

	// Pre-validate unique-index constraints for the whole selected op set
	// (read-only, delete-aware) so a violation rejects the transaction
	// atomically via a non-fatal application error — no partial writes, no
	// consensus stall — matching the MVCC and batch handlers.
	if verr := CheckUniqueViolationsBatch(e.indexes, e.indexFields, e.indexScopes, putsFromOps(ops), deleteKeysFromOps(ops)); verr != nil {
		if res, handled, aerr := asAppErrResult(verr); handled {
			return res, aerr
		}
		return sm.Result{}, verr
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Watch notifications are collected during the batch and emitted only after
	// a successful commit, so a failed commit publishes nothing.
	type txnNotify struct {
		put    bool
		key    []byte
		kv     *qkv.KeyValue
		prevKv *qkv.KeyValue
	}
	var notifs []txnNotify

	for _, op := range ops {
		switch op.Type {
		case protocol.OpMVCCPut:
			var prevKv *qkv.KeyValue
			if len(e.indexes) > 0 || len(e.compoundIndexes) > 0 || e.hub != nil {
				// Read through the batch, not the committed store: an earlier
				// operation in this same transaction may have already written or
				// deleted this key, and index maintenance and the watch prevKv
				// must observe that (read-your-own-writes). Reading committed
				// state would leave a stale index entry after put→put on one
				// indexed key and emit an incorrect previous value.
				oldVal, gerr := batch.Get(op.Key)
				if gerr != nil {
					return sm.Result{}, gerr
				}
				if oldVal != nil {
					prevKv = &qkv.KeyValue{Key: op.Key, Value: oldVal}
				}
			}
			if err := batch.Set(op.Key, op.Value); err != nil {
				return sm.Result{}, err
			}
			if len(e.indexes) > 0 {
				if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, op.Key, op.Value, prevKv); err != nil {
					return sm.Result{}, err
				}
			}
			if len(e.compoundIndexes) > 0 {
				if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, op.Key, op.Value, prevKv); err != nil {
					return sm.Result{}, err
				}
			}
			if e.hub != nil {
				notifs = append(notifs, txnNotify{put: true, key: op.Key,
					kv: &qkv.KeyValue{Key: op.Key, Value: op.Value}, prevKv: prevKv})
			}

		case protocol.OpMVCCDelete:
			var prevKv *qkv.KeyValue
			if len(e.indexes) > 0 || len(e.compoundIndexes) > 0 || e.hub != nil {
				// Read through the batch, not the committed store: an earlier
				// operation in this same transaction may have already written or
				// deleted this key, and index maintenance and the watch prevKv
				// must observe that (read-your-own-writes). Reading committed
				// state would leave a stale index entry after put→put on one
				// indexed key and emit an incorrect previous value.
				oldVal, gerr := batch.Get(op.Key)
				if gerr != nil {
					return sm.Result{}, gerr
				}
				if oldVal != nil {
					prevKv = &qkv.KeyValue{Key: op.Key, Value: oldVal}
				}
			}
			if err := batch.Delete(op.Key); err != nil {
				return sm.Result{}, err
			}
			if len(e.indexes) > 0 {
				if err := RemoveIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, op.Key, prevKv); err != nil {
					return sm.Result{}, err
				}
			}
			if len(e.compoundIndexes) > 0 {
				if err := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, op.Key, prevKv); err != nil {
					return sm.Result{}, err
				}
			}
			if e.hub != nil {
				notifs = append(notifs, txnNotify{put: false, key: op.Key,
					kv: &qkv.KeyValue{Key: op.Key}, prevKv: prevKv})
			}

		default:
			return sm.Result{}, &protocol.UnknownOpError{Op: op.Type}
		}
	}

	if err := batch.Commit(); err != nil {
		return sm.Result{}, err
	}

	for _, n := range notifs {
		if n.put {
			e.hub.NotifyPut(n.key, n.kv, n.prevKv)
		} else {
			e.hub.NotifyDelete(n.key, n.kv, n.prevKv)
		}
	}

	// Value carries the number of applied operations; Data[0] is the
	// succeeded flag, matching the MVCC and PebbleDB transaction result shape.
	var data [1]byte
	if succeeded {
		data[0] = 1
	}
	return sm.Result{Value: uint64(len(ops)), Data: data[:]}, nil
}

// evaluateTxnCompare evaluates one transaction compare against current
// committed KV state. It reuses decodedCompareToService + mvcc.Evaluate so a
// value or version compare decides identically to the MVCC engine;
// create/mod-revision targets are refused before this is reached. Existence is
// projected onto the non-MVCC store as version 1 (present) or 0 (absent), so a
// version compare degrades honestly and a value compare against an absent key
// matches only an equal, empty target. A storage read error is returned, never
// folded into the boolean as "absent" — see the caller for why that would
// diverge replicas.
func (e *KVEngine) evaluateTxnCompare(dc protocol.DecodedCompare) (bool, error) {
	cmp := decodedCompareToService(dc)
	current, err := e.stg.Get(dc.Key)
	if err != nil {
		return false, err
	}
	var kv *service.KeyValue
	if current != nil {
		kv = &service.KeyValue{Key: dc.Key, Value: current, Version: 1}
	}
	return mvcc.Evaluate(&cmp, kv), nil
}

// kvHandleDelete removes a key, cleans up secondary indexes atomically
// in the same batch, and emits a watch notification after commit.
func kvHandleDelete(e *KVEngine, cmd kv.Command) (sm.Result, error) {
	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Read previous value for index cleanup and watch notification.
	var prevKv *qkv.KeyValue
	if len(e.indexes) > 0 || e.hub != nil {
		oldVal, _ := e.stg.Get(cmd.Key)
		if oldVal != nil {
			prevKv = &qkv.KeyValue{Key: cmd.Key, Value: oldVal}
		}
	}

	// Queue data delete.
	if err := batch.Delete(cmd.Key); err != nil {
		return sm.Result{Value: 1}, err
	}

	// Queue index removals into the SAME batch.
	if len(e.indexes) > 0 {
		if err := RemoveIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, cmd.Key, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}

	// Queue compound index removals into the SAME batch.
	if len(e.compoundIndexes) > 0 {
		if err := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, cmd.Key, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}

	// Commit atomically.
	if err := batch.Commit(); err != nil {
		return sm.Result{Value: 1}, err
	}

	// Watch notification AFTER successful commit.
	if e.hub != nil {
		kvVal := &qkv.KeyValue{Key: cmd.Key}
		e.hub.NotifyDelete(cmd.Key, kvVal, prevKv)
	}

	return sm.Result{}, nil
}

// ---------------------------------------------------------------------------
// Extended protocol handlers
// ---------------------------------------------------------------------------

// handleIndexCreate creates a storage-backed secondary index. After
// creation, all existing key-value pairs are scanned to backfill the
// index.
func (e *KVEngine) handleIndexCreate(payload []byte) (sm.Result, error) {
	name, fieldName, opts, err := protocol.DecodeIndexCreateOpts(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}

	// Do not recreate an existing index.
	if _, exists := e.indexes[name]; exists {
		return sm.Result{}, nil
	}

	cfg := index.IndexConfig{
		Name: name,
		Type: index.IndexTypeNormal,
		// Derived from the command instead of hardcoded true. A DAO field
		// tagged index:"case_insensitive" used to reach here as a
		// case-SENSITIVE index, so a lookup for "Alpha" over a stored "alpha"
		// returned empty with a nil error (go-qrdb#34).
		CaseSensitive: !opts.CaseInsensitive,
		// Sparse was decoded and discarded here. It is now recorded on the
		// config for fidelity with the command, but nothing observable changes
		// yet: no Index implementation reads IndexConfig.Sparse — skipping
		// absent fields happens in the backfill/maintenance callers. Stated
		// rather than claimed, since the field reads like a behaviour switch.
		Sparse: opts.Sparse,
		// The key scope the creator declared, relative to the namespace the
		// name carries (go-qrdb#410).
		KeyPrefix: opts.KeyPrefix,
	}
	if opts.Unique {
		cfg.Type = index.IndexTypeUnique
		cfg.Unique = true
	}
	if opts.Numeric {
		cfg.ValueType = index.IndexValueTypeNumeric
	}

	log := e.logger
	if log == nil {
		log = defaultLogger()
	}

	idx, idxErr := index.NewStoragePebbleIndex(e.stg, cfg, log)
	if idxErr != nil {
		return sm.Result{Value: 1}, idxErr
	}

	scope := ComposeIndexScope(name, opts.KeyPrefix)
	e.indexes[name] = idx
	e.indexFields[name] = fieldName
	e.indexScopes[name] = scope

	// Backfill the index with the existing data INSIDE its scope. An unscoped
	// backfill would seed the new index with every matching key in the state
	// machine, other databases' and other tenants' included (go-qrdb#410).
	BackfillIndex(e.scanAllKVs, idx, fieldName, scope)

	return sm.Result{}, nil
}

// handleIndexDrop removes a secondary index by name.
func (e *KVEngine) handleIndexDrop(payload []byte) (sm.Result, error) {
	name, err := protocol.DecodeIndexDrop(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}

	idx, exists := e.indexes[name]
	if !exists {
		return sm.Result{}, nil
	}

	_ = idx.Close()
	delete(e.indexes, name)
	delete(e.indexFields, name)
	delete(e.indexScopes, name)

	return sm.Result{}, nil
}

// handleBatch executes multiple put/delete operations and their index
// updates atomically in a single batch, then emits watch notifications
// after successful commit.
func (e *KVEngine) handleBatch(payload []byte) (sm.Result, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Collect previous values for index cleanup and watch notification.
	type opMeta struct {
		prevKv *qkv.KeyValue
	}
	metas := make([]opMeta, len(ops))

	for i, op := range ops {
		switch op.Type {
		case protocol.OpFSMPut:
			if len(e.indexes) > 0 || e.hub != nil {
				oldVal, _ := e.stg.Get(op.Key)
				if oldVal != nil {
					metas[i].prevKv = &qkv.KeyValue{Key: op.Key, Value: oldVal}
				}
			}
			if bErr := batch.Set(op.Key, op.Value); bErr != nil {
				_ = batch.Rollback()
				return sm.Result{Value: 1}, bErr
			}
			if len(e.indexes) > 0 {
				if bErr := UpdateIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, op.Key, op.Value, metas[i].prevKv); bErr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, bErr
				}
			}
			if len(e.compoundIndexes) > 0 {
				if bErr := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, op.Key, op.Value, metas[i].prevKv); bErr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, bErr
				}
			}
		case protocol.OpFSMDelete:
			if len(e.indexes) > 0 || len(e.compoundIndexes) > 0 || e.hub != nil {
				oldVal, _ := e.stg.Get(op.Key)
				if oldVal != nil {
					metas[i].prevKv = &qkv.KeyValue{Key: op.Key, Value: oldVal}
				}
			}
			if bErr := batch.Delete(op.Key); bErr != nil {
				_ = batch.Rollback()
				return sm.Result{Value: 1}, bErr
			}
			if len(e.indexes) > 0 {
				if bErr := RemoveIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, op.Key, metas[i].prevKv); bErr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, bErr
				}
			}
			if len(e.compoundIndexes) > 0 {
				if bErr := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, op.Key, metas[i].prevKv); bErr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, bErr
				}
			}
		default:
			_ = batch.Rollback()
			return sm.Result{Value: 1}, &UnsupportedOperationError{Op: op.Type}
		}
	}

	if err := batch.Commit(); err != nil {
		return sm.Result{Value: 1}, err
	}

	// Watch notifications AFTER successful commit.
	for i, op := range ops {
		if e.hub == nil {
			break
		}
		switch op.Type {
		case protocol.OpFSMPut:
			kvVal := &qkv.KeyValue{Key: op.Key, Value: op.Value}
			e.hub.NotifyPut(op.Key, kvVal, metas[i].prevKv)
		case protocol.OpFSMDelete:
			kvVal := &qkv.KeyValue{Key: op.Key}
			e.hub.NotifyDelete(op.Key, kvVal, metas[i].prevKv)
		}
	}

	return sm.Result{Value: uint64(len(ops))}, nil
}

// ---------------------------------------------------------------------------
// Entity registration
// ---------------------------------------------------------------------------

// RegisterEntityType creates storage-backed indexes for the given entity
// type and backfills them from existing data matching the entity prefix.
// Each EntityIndex definition results in a StoragePebbleIndex. When a
// database namespace is configured via WithKVDatabase, index names are
// prefixed as "{database}.{entityType}.{FieldName}"; otherwise they use
// the legacy "{entityType}.{FieldName}" format for backward compatibility.
// All indexes are maintained atomically during Put/Delete via batch
// operations.
func (e *KVEngine) RegisterEntityType(entityType string, indexes []EntityIndex) error {
	log := e.logger
	if log == nil {
		log = defaultLogger()
	}

	for _, def := range indexes {
		// Compound indexes: multi-field.
		if def.IsCompound() {
			if err := e.registerCompoundIndex(entityType, def, log); err != nil {
				return err
			}
			continue
		}

		// Single-field indexes.
		idxName := e.buildIndexName(entityType, def.FieldName)

		// Skip if already registered.
		if _, exists := e.indexes[idxName]; exists {
			continue
		}

		idxType := index.IndexTypeNormal
		if def.Unique {
			idxType = index.IndexTypeUnique
		}

		// An entity index covers exactly the entity type's keyspace,
		// "{entityType}/", which is also the prefix its backfill scans below.
		// Declaring it here is what makes the guarantee filterIndexes gives
		// the DAO path hold on the Raft apply path too (go-qrdb#410).
		entityScope := entityType + "/"

		cfg := index.IndexConfig{
			Name:          idxName,
			Type:          idxType,
			Unique:        def.Unique,
			Sparse:        def.Sparse,
			CaseSensitive: def.CaseSensitive,
			KeyPrefix:     entityScope,
		}

		idx, err := index.NewStoragePebbleIndex(e.stg, cfg, log)
		if err != nil {
			return err
		}

		e.indexes[idxName] = idx
		e.indexFields[idxName] = def.JSONFieldName
		e.indexScopes[idxName] = ComposeIndexScope(idxName, entityScope)

		// Backfill from existing keys matching the entity prefix.
		prefix := []byte(entityScope)
		if bErr := BackfillIndexWithPrefix(e.stg, idx, def.JSONFieldName, prefix); bErr != nil {
			return bErr
		}
	}

	e.entityIndexes[entityType] = indexes
	return nil
}

// registerCompoundIndex creates and registers a compound (multi-field) index.
func (e *KVEngine) registerCompoundIndex(entityType string, def EntityIndex, log *logger.Logger) error {
	idxName := e.buildCompoundIndexName(entityType, def.FieldNames)

	// Skip if already registered.
	if _, exists := e.compoundIndexes[idxName]; exists {
		return nil
	}

	idxType := index.IndexTypeNormal
	if def.Unique {
		idxType = index.IndexTypeUnique
	}

	// A compound entity index covers the entity type's keyspace, the same
	// prefix its backfill scans below (go-qrdb#410).
	entityScope := entityType + "/"

	cfg := index.IndexConfig{
		Name:          idxName,
		Type:          idxType,
		FieldNames:    def.FieldNames,
		Unique:        def.Unique,
		Sparse:        def.Sparse,
		CaseSensitive: def.CaseSensitive,
		KeyPrefix:     entityScope,
	}

	idx, err := index.NewCompoundIndex(e.stg, cfg, log)
	if err != nil {
		return err
	}

	e.compoundIndexes[idxName] = idx
	e.compoundIndexFields[idxName] = def.JSONFieldNames
	e.compoundIndexScopes[idxName] = ComposeIndexScope(idxName, entityScope)

	// Backfill from existing data.
	prefix := []byte(entityScope)
	if bErr := BackfillCompoundIndexWithPrefix(e.stg, idx, def.JSONFieldNames, prefix); bErr != nil {
		return bErr
	}

	return nil
}

// buildCompoundIndexName constructs the index name for a compound index
// by joining field names with underscores.
func (e *KVEngine) buildCompoundIndexName(entityType string, fieldNames []string) string {
	joined := fieldNames[0]
	for _, f := range fieldNames[1:] {
		joined += "_" + f
	}
	if e.database != "" {
		return e.database + "." + entityType + "." + joined
	}
	return entityType + "." + joined
}

// buildIndexName constructs the storage index name for an entity type field.
// When the engine has a database namespace configured, the name is prefixed
// as "{database}.{entityType}.{fieldName}" for per-database index isolation.
// Without a database prefix the legacy format "{entityType}.{fieldName}" is
// used for backward compatibility.
func (e *KVEngine) buildIndexName(entityType, fieldName string) string {
	if e.database != "" {
		return e.database + "." + entityType + "." + fieldName
	}
	return entityType + "." + fieldName
}

// defaultLogger creates a minimal logger for index operations when no
// logger has been configured via WithKVLogger.
func defaultLogger() *logger.Logger {
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		// LogConfig with known-valid values cannot fail validation.
		panic("engine: failed to create default logger: " + err.Error())
	}
	return log
}

// ---------------------------------------------------------------------------
// Standalone helpers (for embedded use without Raft)
// ---------------------------------------------------------------------------

// PutWithIndexes atomically writes a key/value to storage and updates
// indexes registered for the key's entity type. Only indexes whose name
// starts with the entity type prefix are updated, preventing cross-entity
// index contamination.
func (e *KVEngine) PutWithIndexes(key, value []byte) error {
	prevVal, _ := e.stg.Get(key)
	var prevKv *qkv.KeyValue
	if prevVal != nil {
		prevKv = &qkv.KeyValue{Key: key, Value: prevVal}
	}

	entityType := entityTypeFromKey(key)
	filteredIndexes, filteredFields := e.filterIndexes(entityType)
	filteredCompound, filteredCompoundFields := e.filterCompoundIndexes(entityType)

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	if err := batch.Set(key, value); err != nil {
		return err
	}
	if err := UpdateIndexesBatch(batch, filteredIndexes, filteredFields, e.indexScopes, key, value, prevKv); err != nil {
		return err
	}
	if err := UpdateCompoundIndexesBatch(batch, filteredCompound, filteredCompoundFields, e.compoundIndexScopes, key, value, prevKv); err != nil {
		return err
	}
	return batch.Commit()
}

// DeleteWithIndexes atomically removes a key and its index entries.
// Only indexes for the key's entity type are cleaned up.
func (e *KVEngine) DeleteWithIndexes(key []byte) error {
	prevVal, _ := e.stg.Get(key)
	var prevKv *qkv.KeyValue
	if prevVal != nil {
		prevKv = &qkv.KeyValue{Key: key, Value: prevVal}
	}

	entityType := entityTypeFromKey(key)
	filteredIndexes, filteredFields := e.filterIndexes(entityType)
	filteredCompound, filteredCompoundFields := e.filterCompoundIndexes(entityType)

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	if err := batch.Delete(key); err != nil {
		return err
	}
	if err := RemoveIndexesBatch(batch, filteredIndexes, filteredFields, e.indexScopes, key, prevKv); err != nil {
		return err
	}
	if err := RemoveCompoundIndexesBatch(batch, filteredCompound, filteredCompoundFields, e.compoundIndexScopes, key, prevKv); err != nil {
		return err
	}
	return batch.Commit()
}

// entityTypeFromKey extracts the entity type prefix from a key.
// Keys follow the format "{entityType}/{id}".
func entityTypeFromKey(key []byte) string {
	for i, b := range key {
		if b == '/' {
			return string(key[:i])
		}
	}
	return string(key)
}

// filterIndexes returns only the indexes whose names start with the given
// entity type prefix (e.g., "messages."). When the engine has a database
// namespace, the prefix includes it (e.g., "mydb.messages.").
func (e *KVEngine) filterIndexes(entityType string) (map[string]index.Index, map[string]string) {
	filtered := make(map[string]index.Index)
	filteredFields := make(map[string]string)

	// Try both prefixed and unprefixed names:
	// - "entityType." (legacy / no database)
	// - "database.entityType." (with database namespace)
	prefixes := []string{entityType + "."}
	if e.database != "" {
		prefixes = append(prefixes, e.database+"."+entityType+".")
	}

	for name, idx := range e.indexes {
		for _, prefix := range prefixes {
			if len(name) > len(prefix) && name[:len(prefix)] == prefix {
				filtered[name] = idx
				filteredFields[name] = e.indexFields[name]
				break
			}
		}
	}
	return filtered, filteredFields
}

// filterCompoundIndexes returns only compound indexes matching the entity type.
func (e *KVEngine) filterCompoundIndexes(entityType string) (map[string]*index.CompoundIndex, map[string][]string) {
	prefix := entityType + "."
	filtered := make(map[string]*index.CompoundIndex)
	filteredFields := make(map[string][]string)
	for name, idx := range e.compoundIndexes {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			filtered[name] = idx
			filteredFields[name] = e.compoundIndexFields[name]
		}
	}
	return filtered, filteredFields
}

// ---------------------------------------------------------------------------
// Lookup helpers
// ---------------------------------------------------------------------------

// lookupKey performs a point lookup by key.
func (e *KVEngine) lookupKey(key []byte) (interface{}, error) {
	val, err := e.stg.Get(key)
	if err != nil {
		return nil, err
	}
	if val == nil {
		return nil, nil
	}
	return val, nil
}

// listKeys returns a sorted slice of key strings matching the prefix.
func (e *KVEngine) listKeys(prefix string) (interface{}, error) {
	keys, err := e.stg.List([]byte(prefix))
	if err != nil {
		return nil, err
	}
	result := make([]string, len(keys))
	for i, k := range keys {
		result[i] = string(k)
	}
	sort.Strings(result)
	return result, nil
}

// scanPrefix scans keys matching a prefix and returns a map of key to
// value bytes.
func (e *KVEngine) scanPrefix(pq *statemachine.PrefixQuery) (interface{}, error) {
	result := make(map[string][]byte)
	count := 0

	iter := e.stg.NewIterator(&storage.IteratorOptions{
		LowerBound: pq.Prefix,
		UpperBound: prefixEndBytes(pq.Prefix),
	})
	defer func() { _ = iter.Close() }()

	for ok := iter.SeekToFirst(); ok && iter.Valid(); ok = iter.Next() {
		if !bytes.HasPrefix(iter.Key(), pq.Prefix) {
			break
		}

		// Copy key and value to avoid iterator invalidation.
		key := make([]byte, len(iter.Key()))
		copy(key, iter.Key())
		val := make([]byte, len(iter.Value()))
		copy(val, iter.Value())

		result[string(key)] = val
		count++

		if pq.Limit > 0 && count >= pq.Limit {
			break
		}
	}

	if err := iter.Error(); err != nil {
		return nil, err
	}

	return result, nil
}

// lookupIndexQuery performs an exact-match index query.
func (e *KVEngine) lookupIndexQuery(q index.IndexQueryRequest) (interface{}, error) {
	idx, exists := e.indexes[q.IndexName]
	if !exists {
		return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
	}
	return idx.Get(q.Value)
}

// lookupIndexScan performs a range index scan.
func (e *KVEngine) lookupIndexScan(q index.IndexScanRequest) (interface{}, error) {
	idx, exists := e.indexes[q.IndexName]
	if !exists {
		return nil, &InvalidQueryError{Reason: "index not found: " + q.IndexName}
	}
	return idx.Scan(q.Start, q.End)
}

// lookupIndexList returns the names of all registered indexes.
func (e *KVEngine) lookupIndexList() (interface{}, error) {
	names := make([]string, 0, len(e.indexes)+len(e.compoundIndexes))
	for name := range e.indexes {
		names = append(names, name)
	}
	for name := range e.compoundIndexes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// lookupCompoundIndexQuery performs an exact-match compound index query.
func (e *KVEngine) lookupCompoundIndexQuery(q index.CompoundIndexQueryRequest) (interface{}, error) {
	idx, exists := e.compoundIndexes[q.IndexName]
	if !exists {
		return nil, &InvalidQueryError{Reason: "compound index not found: " + q.IndexName}
	}
	return idx.GetCompound(q.Values)
}

// lookupCompoundIndexScan performs a compound index prefix + range scan.
func (e *KVEngine) lookupCompoundIndexScan(q index.CompoundIndexScanRequest) (interface{}, error) {
	idx, exists := e.compoundIndexes[q.IndexName]
	if !exists {
		return nil, &InvalidQueryError{Reason: "compound index not found: " + q.IndexName}
	}
	if q.RangeStart == nil && q.RangeEnd == nil {
		// Pure prefix scan -- use GetPrefix.
		return idx.GetPrefix(q.PrefixValues)
	}
	return idx.ScanCompound(q.PrefixValues, q.RangeStart, q.RangeEnd)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// scanAllKVs scans all key-value pairs in the storage for use by
// BackfillIndex.
func (e *KVEngine) scanAllKVs() ([]*service.KeyValue, error) {
	iter := e.stg.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var kvs []*service.KeyValue
	for ok := iter.SeekToFirst(); ok && iter.Valid(); ok = iter.Next() {
		key := make([]byte, len(iter.Key()))
		copy(key, iter.Key())
		val := make([]byte, len(iter.Value()))
		copy(val, iter.Value())

		kvs = append(kvs, &service.KeyValue{Key: key, Value: val})
	}

	if err := iter.Error(); err != nil {
		return nil, err
	}

	return kvs, nil
}

// prefixEndBytes computes the exclusive upper bound for a prefix scan.
// It increments the last byte of the prefix to form the end key.
func prefixEndBytes(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	// All bytes are 0xff; no upper bound needed.
	return nil
}
