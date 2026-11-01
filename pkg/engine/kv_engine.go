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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	qkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
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
	indexScopes map[string][]byte
	// indexOwners holds the owner recorded for each index created through
	// OpIndexCreate (index.IndexConfig.Owner, go-qrdb#426). An absent entry
	// is the empty owner: the explicit index API, and every index registered
	// locally rather than through a command.
	indexOwners map[string]string
	// indexValueTypes holds the term encoding each index created through
	// OpIndexCreate was built with (see IndexCreateValueType). An absent entry
	// is a string index.
	indexValueTypes map[string]index.IndexValueType
	entityIndexes   map[string][]EntityIndex
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
	logger              *logger.Logger
	handlers            map[byte]kvHandler
	extHandlers         map[byte]extHandler
	// indexMgr owns every vector namespace's in-memory ANN index. Nil is a
	// valid, fully no-op configuration; see MVCCEngine's matching field.
	indexMgr *VectorIndexManager
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

// WithKVVectorIndex attaches a vector index manager; see
// WithMVCCVectorIndex, MVCCEngine's matching option, for dir and enc.
func WithKVVectorIndex(dir string, enc *atrest.Encryptor) KVOption {
	return func(e *KVEngine) {
		e.indexMgr = NewVectorIndexManager(dir, enc)
	}
}

// NewKVEngine creates a KVEngine backed by the given storage.
func NewKVEngine(stg storage.Storage, opts ...KVOption) *KVEngine {
	e := &KVEngine{
		stg:                 stg,
		indexes:             make(map[string]index.Index),
		indexFields:         make(map[string]string),
		indexScopes:         make(map[string][]byte),
		indexOwners:         make(map[string]string),
		indexValueTypes:     make(map[string]index.IndexValueType),
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

// WarmVectorIndex populates the engine's vector index manager from the
// records already committed to its storage. See MVCCEngine.WarmVectorIndex.
func (e *KVEngine) WarmVectorIndex() error {
	return e.indexMgr.Warm(e.stg)
}

// VectorIndex returns the engine's vector index manager, or nil when
// WithKVVectorIndex was not given at construction.
func (e *KVEngine) VectorIndex() *VectorIndexManager {
	return e.indexMgr
}

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
		// #nosec G602 -- i ranges over entries.
		result, err := e.apply(entries[i].Cmd)
		// #nosec G602 -- i ranges over entries.
		resolved, stop, applyErr := statemachine.ResolveApplyError(slog.Default(), entries[i].Index, result, err)
		if stop {
			// The failure marker predates the classifier and is kept: Raft
			// discards results for a halted Apply, but LocalBackend calls
			// Update directly and reads them.
			// #nosec G602 -- the sm.StateMachine Update contract pre-allocates results with len(results) == len(entries), and i ranges over entries.
			results[i] = sm.Result{Value: 1}
			return applyErr
		}
		// #nosec G602 -- the sm.StateMachine Update contract pre-allocates results with len(results) == len(entries), and i ranges over entries.
		results[i] = resolved
	}
	return nil
}

// apply dispatches one complete command. It is the single dispatch point for
// both a log entry and the inner command of an OpGuarded entry, so the two
// can never disagree about which opcodes this engine serves.
func (e *KVEngine) apply(data []byte) (sm.Result, error) {
	// Try base KV protocol (OpPut=1, OpDelete=3).
	cmd, err := kv.DecodeCommand(data)
	if err == nil {
		handler, ok := e.handlers[cmd.Op]
		if !ok {
			return sm.Result{}, &UnsupportedOperationError{Op: cmd.Op}
		}
		return handler(e, cmd)
	}

	// Try extended protocol for index and batch operations.
	extCmd, extErr := protocol.DecodeCommand(data)
	if extErr != nil {
		// Undecodable by both protocols: this is a deterministic consequence
		// of the specific committed bytes (corruption or a version-skewed
		// writer), not engine state. Surface it as an application error
		// rather than reporting Value:1 (SUCCESS) — silently dropping the
		// write — or returning a Go error, which would stall consensus.
		decErr := &CommandDecodeError{KVErr: err, ExtErr: extErr}
		return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorCorrupt, decErr.Error())}, nil
	}
	extFn, ok := e.extHandlers[extCmd.Op]
	if !ok {
		return sm.Result{}, &UnsupportedOperationError{Op: extCmd.Op}
	}
	return extFn(extCmd.Payload)
}

// handleGuarded applies an OpGuarded entry. The guard is read through
// e.stg.Get -- the read handleCompareAndSwap uses -- and the inner command is
// applied through apply only when every compare holds. A refused guard is a
// successful application that changes nothing (see protocol.OpGuarded).
func (e *KVEngine) handleGuarded(payload []byte) (sm.Result, error) {
	compares, inner, err := protocol.DecodeGuarded(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}
	// No compare may read the reserved vector namespace, checked BEFORE the
	// guard is evaluated: whether the guard held is itself the oracle bit
	// here, so the refusal must come before evaluation, not after (go-qrdb
	// vector-replicated review, B3-residual).
	if err := vector.RefuseForgedKeys(statemachine.CompareKeys(compares)...); err != nil {
		return sm.Result{Value: 1}, err
	}
	// Checked BEFORE the guard is evaluated, and as a REFUSAL rather than
	// through e.stg.Get below -- see pkg/store/pebble.go's
	// pebbleHandleGuarded for why: a reserved key reaching the reader
	// surfaces as *protocol.GuardReadError only on a genuine storage fault,
	// which halts apply, and a compare's result is observable to the
	// proposer regardless of outcome.
	if res, refused := statemachine.ReservedKeyRefusal(statemachine.CompareKeys(compares)...); refused {
		return res, nil
	}
	refusedKey, ok, err := protocol.EvaluateGuard(compares, e.stg.Get)
	if err != nil {
		return sm.Result{Value: 1}, err
	}
	if !ok {
		return sm.Result{Data: protocol.EncodeGuardRefusal(refusedKey)}, nil
	}
	return e.apply(inner)
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

	case service.VectorSearchQuery:
		if e.indexMgr == nil {
			return nil, &InvalidQueryError{Reason: "vector index not configured"}
		}
		hits, err := e.indexMgr.Search(q.Namespace, q.Query, q.K)
		if err != nil {
			return nil, err
		}
		results := make([]service.VectorSearchHit, len(hits))
		for i, h := range hits {
			results[i] = service.VectorSearchHit{ID: h.ID, Distance: h.Distance}
		}
		return results, nil

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
// Index, batch, the single-key guarded verbs, the multi-key compare-and-
// execute transaction, and guarded writes (protocol.OpGuarded) are supported; MVCC-, lease-, and time-series-specific
// opcodes are absent and will trigger UnsupportedOperationError.
func (e *KVEngine) buildExtHandlers() map[byte]extHandler {
	return map[byte]extHandler{
		protocol.OpIndexCreate:      e.handleIndexCreate,
		protocol.OpIndexDrop:        e.handleIndexDrop,
		protocol.OpBatch:            e.handleBatch,
		protocol.OpDeleteRange:      e.handleDeleteRange,
		protocol.OpCompareAndDelete: e.handleCompareAndDelete,
		protocol.OpCompareAndSwap:   e.handleCompareAndSwap,
		protocol.OpTransaction:      e.handleTransaction,
		protocol.OpGuarded:          e.handleGuarded,
		protocol.OpImportStage:      e.handleImportStage,

		// Vector engine: the file/objstore backends compose KVEngine (no
		// MVCC), and "every engine on every backend" includes them
		// (docs/vectors/engineering.md "Target: the vector engine"). Each
		// call opens its own batch and commits it, exactly as handleBatch
		// does, because KVEngine -- unlike MVCCEngine -- does not share one
		// batch across the entries packed into a single Update call.
		protocol.OpVectorNamespaceCreate:  e.adaptVectorHandler(protocol.OpVectorNamespaceCreate, applyVectorNamespaceCreate),
		protocol.OpVectorNamespaceDelete:  e.adaptVectorHandler(protocol.OpVectorNamespaceDelete, applyVectorNamespaceDelete),
		protocol.OpVectorUpsert:           e.adaptVectorHandler(protocol.OpVectorUpsert, applyVectorUpsert),
		protocol.OpVectorBatchUpsert:      e.adaptVectorHandler(protocol.OpVectorBatchUpsert, applyVectorBatchUpsert),
		protocol.OpVectorDelete:           e.adaptVectorHandler(protocol.OpVectorDelete, applyVectorDelete),
		protocol.OpVectorBatchDelete:      e.adaptVectorHandler(protocol.OpVectorBatchDelete, applyVectorBatchDelete),
		protocol.OpVectorUpdateMetadata:   e.adaptVectorHandler(protocol.OpVectorUpdateMetadata, applyVectorUpdateMetadata),
		protocol.OpVectorIndexReconfigure: e.adaptVectorHandler(protocol.OpVectorIndexReconfigure, applyVectorIndexReconfigure),
	}
}

// adaptVectorHandler lifts a vector apply function into the extHandler shape
// KVEngine's dispatch table requires: it opens a batch over e.stg, runs fn,
// and commits only when fn reports no error -- a refusal (one of
// pkg/vector's typed errors) and a fault both leave the batch uncommitted,
// which for a refusal is correct because every vector apply function
// validates before it writes (see vector_handlers.go's package doc comment).
//
// op names which of the eight vector opcodes fn applies, so this can compute
// the command's index effect (vectorIndexEffects) through the SAME batch fn
// just staged into, before that batch commits -- and apply it to the index
// manager only AFTER the commit succeeds (docs/vectors/engineering.md "Apply
// and the index"), exactly as MVCCEngine.UpdateEntries and
// MVCCEngine.ApplyVectorIndexEffects do for the raft-backed engine.
func (e *KVEngine) adaptVectorHandler(op byte, fn func(storage.Batch, []byte) (sm.Result, error)) extHandler {
	return func(payload []byte) (sm.Result, error) {
		batch := e.stg.Batch()
		defer func() { _ = batch.Close() }()

		preNextID := vectorBatchUpsertPreNextID(batch, protocol.Command{Op: op, Payload: payload})

		result, err := fn(batch, payload)
		if err != nil {
			return result, err
		}

		effects, eerr := vectorIndexEffects(batch, op, payload, result, preNextID)
		if eerr != nil {
			return sm.Result{}, eerr
		}

		if err := batch.Commit(); err != nil {
			return sm.Result{}, err
		}

		e.indexMgr.ApplyEffects(effects)
		return result, nil
	}
}

// ---------------------------------------------------------------------------
// Base KV handlers
// ---------------------------------------------------------------------------

// kvHandlePut stores a key-value pair, updates secondary indexes atomically
// in the same batch, and emits a watch notification after commit.
func kvHandlePut(e *KVEngine, cmd kv.Command) (sm.Result, error) {
	if res, refused := statemachine.ReservedKeyRefusal(cmd.Key); refused {
		return res, nil
	}
	if err := vector.RefuseForgedKeys(cmd.Key); err != nil {
		return sm.Result{Value: 1}, err
	}
	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Read previous value for index cleanup and watch notification.
	var prevKv *qkv.KeyValue
	if len(e.indexes) > 0 || e.hub != nil {
		oldVal, gerr := e.stg.Get(cmd.Key)
		if gerr != nil {
			return sm.Result{Value: 1}, gerr
		}
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
	if res, refused := statemachine.ReservedKeyRefusal(cmd.Key); refused {
		return res, nil
	}
	if err := vector.RefuseForgedKeys(cmd.Key); err != nil {
		return sm.Result{Value: 1}, err
	}
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
	if err := vector.RefuseForgedKeys(key); err != nil {
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
	if err := vector.RefuseForgedKeys(key); err != nil {
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
	if res, refused := statemachine.ReservedKeyRefusal(append(statemachine.OpKeys(txn.SuccessOps), statemachine.OpKeys(txn.FailureOps)...)...); refused {
		return res, nil
	}
	// The compare side needs the identical refusal: a compare's result is
	// observable to the proposer (which branch ran) regardless of outcome,
	// so evaluating one against a reserved key would make an ordinary
	// transaction an extraction oracle over state no caller may read
	// directly (go-qrdb audit-replicated review addendum).
	if res, refused := statemachine.ReservedKeyRefusal(statemachine.CompareKeys(txn.Compares)...); refused {
		return res, nil
	}

	// No compare may read the reserved vector namespace, checked before any
	// compare is evaluated: a Less/Greater result or a guard's held/not-held
	// bit would otherwise answer a binary-search probe against the stored
	// bytes of a key this transaction never had standing to read (go-qrdb
	// vector-replicated review, B3-residual).
	if err := vector.RefuseForgedKeys(statemachine.CompareKeys(txn.Compares)...); err != nil {
		return sm.Result{}, err
	}

	for _, dc := range txn.Compares {
		if service.CompareTarget(dc.Target) == service.CompareCreateRevision ||
			service.CompareTarget(dc.Target) == service.CompareModRevision {
			return statemachine.CapabilityResult("transaction: create_revision/mod_revision compares require an " +
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

	// No operation in the selected branch may touch the reserved vector
	// namespace. Checked before anything in this set is staged, so a
	// transaction naming a forged key anywhere in it writes nothing at all
	// (go-qrdb vector-replicated review, B3).
	if err := vector.RefuseForgedKeys(statemachine.OpKeys(ops)...); err != nil {
		return sm.Result{}, err
	}

	// This engine keeps no leases. A put naming one is refused before any of
	// the set is staged: stored lease-less, it would never expire.
	if res, refused := statemachine.OpsLeaseRefusal(nil, ops); refused {
		return res, nil
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Pre-validate unique-index constraints for the whole selected op set
	// (read-only, delete-aware) so a violation rejects the transaction
	// atomically via a non-fatal application error — no partial writes, no
	// consensus stall — matching the MVCC and batch handlers. Read through
	// the batch so a uniqueness check sees an index write staged earlier in
	// this same batch (storage.Reader).
	if verr := CheckUniqueViolationsBatch(batch, e.indexes, e.indexFields, e.indexScopes, putsFromOps(ops), deleteKeysFromOps(ops)); verr != nil {
		if res, handled, aerr := asAppErrResult(verr); handled {
			return res, aerr
		}
		return sm.Result{}, verr
	}

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
	if res, refused := statemachine.ReservedKeyRefusal(cmd.Key); refused {
		return res, nil
	}
	if err := vector.RefuseForgedKeys(cmd.Key); err != nil {
		return sm.Result{Value: 1}, err
	}
	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Read previous value for index cleanup and watch notification.
	var prevKv *qkv.KeyValue
	if len(e.indexes) > 0 || e.hub != nil {
		oldVal, gerr := e.stg.Get(cmd.Key)
		if gerr != nil {
			return sm.Result{Value: 1}, gerr
		}
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

	// Do not recreate an existing index, and do not let a different owner
	// adopt it either: a create naming an index another owner holds is
	// refused as an application error (go-qrdb#426). A same-owner create
	// asking for a different value type rebuilds the index at that type
	// instead (see IndexCreateValueType).
	valueType := IndexCreateValueType(opts)
	existing, exists := e.indexes[name]
	// The shard's index cap is checked against the index set the create
	// applies over, so every replica reaches the same verdict.
	if index.CreateExceedsCap(exists, len(e.indexes), opts.MaxIndexes) {
		return sm.Result{Data: protocol.EncodeLimitRefusal(protocol.LimitIndexes, opts.MaxIndexes)}, nil
	}
	if exists {
		owner := e.indexOwners[name]
		if index.CheckCreateOwner(name, owner, opts.Owner) != nil {
			return sm.Result{Data: protocol.EncodeIndexOwnershipError(name, owner)}, nil
		}
		if e.indexValueTypes[name] == valueType {
			return sm.Result{}, nil
		}
		if err := existing.Close(); err != nil {
			return sm.Result{Value: 1}, err
		}
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
		Owner:     opts.Owner,
		ValueType: valueType,
	}
	if opts.Unique {
		cfg.Type = index.IndexTypeUnique
		cfg.Unique = true
	}
	// A create builds the index now, so its terms are written at the current
	// encoding; the clear below removes any written earlier.
	cfg.EncodingVersion = index.NumericEncodingVersion

	log := e.logger
	if log == nil {
		log = defaultLogger()
	}

	idx, idxErr := index.NewStoragePebbleIndex(e.stg, cfg, log)
	if idxErr != nil {
		return sm.Result{Value: 1}, idxErr
	}

	// Entries already stored under the name -- left by a create applied
	// again over storage that outlived the in-memory index, possibly in an
	// older term encoding -- are cleared before the backfill rebuilds them.
	if err := idx.Clear(); err != nil {
		return sm.Result{Value: 1}, err
	}

	scope := ComposeIndexScope(name, opts.KeyPrefix)
	e.indexes[name] = idx
	e.indexFields[name] = fieldName
	e.indexScopes[name] = scope
	e.indexOwners[name] = opts.Owner
	e.indexValueTypes[name] = valueType

	// Backfill the index with the existing data INSIDE its scope. An unscoped
	// backfill would seed the new index with every matching key in the state
	// machine, other databases' and other tenants' included (go-qrdb#410).
	BackfillIndex(e.scanAllKVs, idx, fieldName, scope)

	return sm.Result{}, nil
}

// handleIndexDrop removes a secondary index by name.
func (e *KVEngine) handleIndexDrop(payload []byte) (sm.Result, error) {
	name, dropOpts, err := protocol.DecodeIndexDropOpts(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}

	idx, exists := e.indexes[name]
	if !exists {
		return sm.Result{}, nil
	}
	// An owner-checked drop removes only its caller's index (go-qrdb#426).
	if owner := e.indexOwners[name]; index.CheckDropOwner(name, owner, dropOpts.CheckOwner, dropOpts.Owner) != nil {
		return sm.Result{Data: protocol.EncodeIndexOwnershipError(name, owner)}, nil
	}

	_ = idx.Close()
	delete(e.indexes, name)
	delete(e.indexFields, name)
	delete(e.indexScopes, name)
	delete(e.indexOwners, name)
	delete(e.indexValueTypes, name)

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
	return e.applyBatchOps(ops)
}

// handleDeleteRange applies protocol.OpDeleteRange: it deletes every key in
// [start, end) -- through the end of the keyspace when end is empty -- that
// listKeys would report, as a batch of OpFSMDelete naming exactly those keys
// would. It goes through applyBatchOps rather than a storage range tombstone
// so a range delete keeps every guarantee a delete has: a reserved start
// refuses, secondary-index entries are removed, and watchers see one delete
// event per key. The vector engine's records sort among client keys but are
// not client data, so a range over them leaves them in place, as
// PebbleStateMachine's does.
func (e *KVEngine) handleDeleteRange(payload []byte) (sm.Result, error) {
	start, end, err := statemachine.DecodeDeleteRange(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}
	if res, refused := statemachine.ReservedKeyRefusal(start); refused {
		return res, nil
	}
	iter := e.stg.NewIterator(&storage.IteratorOptions{
		LowerBound: start,
		UpperBound: end,
		OnlyKeys:   true,
	})
	var keys [][]byte
	for ok := iter.SeekToFirst(); ok && iter.Valid(); ok = iter.Next() {
		if vector.IsForgedKey(iter.Key()) {
			continue
		}
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	iterErr := iter.Error()
	_ = iter.Close()
	if iterErr != nil {
		return sm.Result{Value: 1}, iterErr
	}
	return e.applyBatchOps(deleteRangeOps(protocol.OpFSMDelete, keys))
}

// applyBatchOps applies a decoded operation set and its index updates
// atomically in one storage batch, then emits watch notifications after the
// commit. It is the single body behind OpBatch and OpDeleteRange.
func (e *KVEngine) applyBatchOps(ops []protocol.DecodedOp) (sm.Result, error) {
	if res, refused := statemachine.ReservedKeyRefusal(statemachine.OpKeys(ops)...); refused {
		return res, nil
	}
	// No operation in the batch may touch the reserved vector namespace,
	// checked before anything in it is staged (go-qrdb vector-replicated
	// review, B3).
	if err := vector.RefuseForgedKeys(statemachine.OpKeys(ops)...); err != nil {
		return sm.Result{Value: 1}, err
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	// Collect previous values for index cleanup and watch notification. Each
	// is read through the batch, so an op that follows an earlier write to the
	// same key in this batch sees that write as its previous value and
	// retracts its index entries, not the value committed before the batch.
	type opMeta struct {
		prevKv *qkv.KeyValue
	}
	metas := make([]opMeta, len(ops))

	for i, op := range ops {
		switch op.Type {
		case protocol.OpFSMPut:
			if len(e.indexes) > 0 || e.hub != nil {
				oldVal, gerr := batch.Get(op.Key)
				if gerr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, gerr
				}
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
				oldVal, gerr := batch.Get(op.Key)
				if gerr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, gerr
				}
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

// PutWithIndexes atomically writes a key/value to storage and updates every
// index whose key scope covers the key.
//
// It selects indexes by SCOPE, the same rule the Raft apply path uses
// (handleKVPut -> UpdateIndexesBatch), and never by parsing the key. It used
// to take the key's FIRST path segment as its entity type and keep only
// indexes NAMED "{entityType}." -- which a namespaced key defeats: under a
// tenant namespace "acme/workitem/1" read as entity type "acme", and the
// tenant's "acme/workitem.State" index was never maintained (go-qrdb#40). A
// scope is declared at registration and composed with the namespace the index
// name carries, so it holds for a bare, a per-tenant and a per-(tenant,
// database) key alike, with nothing about the layout assumed here.
func (e *KVEngine) PutWithIndexes(key, value []byte) error {
	// A fault reading the previous value is returned, not taken for "no
	// previous value": that would write the key without retracting the
	// previous value's index entries, leaving them pointing at a value that no
	// longer carries those fields. Absence (nil, nil) is "no previous value".
	prevVal, err := e.stg.Get(key)
	if err != nil {
		return err
	}
	var prevKv *qkv.KeyValue
	if prevVal != nil {
		prevKv = &qkv.KeyValue{Key: key, Value: prevVal}
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	if err := batch.Set(key, value); err != nil {
		return err
	}
	if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, key, value, prevKv); err != nil {
		return err
	}
	if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, key, value, prevKv); err != nil {
		return err
	}
	return batch.Commit()
}

// DeleteWithIndexes atomically removes a key and its entries from every index
// whose key scope covers it; see PutWithIndexes for why the selection is by
// scope.
func (e *KVEngine) DeleteWithIndexes(key []byte) error {
	// As in PutWithIndexes, a read fault is returned rather than taken for
	// absence, which would delete the key and leave its index entries behind.
	prevVal, err := e.stg.Get(key)
	if err != nil {
		return err
	}
	var prevKv *qkv.KeyValue
	if prevVal != nil {
		prevKv = &qkv.KeyValue{Key: key, Value: prevVal}
	}

	batch := e.stg.Batch()
	defer func() { _ = batch.Close() }()

	if err := batch.Delete(key); err != nil {
		return err
	}
	if err := RemoveIndexesBatch(batch, e.indexes, e.indexFields, e.indexScopes, key, prevKv); err != nil {
		return err
	}
	if err := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, e.compoundIndexScopes, key, prevKv); err != nil {
		return err
	}
	return batch.Commit()
}

// ---------------------------------------------------------------------------
// Lookup helpers
// ---------------------------------------------------------------------------

// lookupKey performs a point lookup by key.
// lookupKey answers not-found for a key in the reserved 0x00 namespace
// rather than reading it: this engine's storage is raw, with no key
// transform of its own (unlike MVCCEngine's metadata-prefixed Get), so a
// caller naming a reserved key precisely would otherwise read it directly
// (go-qrdb audit-replicated review, B2/N5).
func (e *KVEngine) lookupKey(key []byte) (interface{}, error) {
	if statemachine.IsReservedKey(key) {
		return nil, nil
	}
	// The internal-key discovery guard (pkg/service) keeps a "__vec:" key out
	// of bulk enumeration, but documents itself as NOT stopping a caller that
	// already knows the key from reading it with Get. For most reserved
	// namespaces that is an accepted, measured gap; for vector it is closed
	// here, because an exact-key Get of a predictable, sequential vector key
	// (vector.VectorKey(ns, id) for small, guessable id) would otherwise hand
	// a caller a raw embedding through the ordinary user-facing KV surface
	// rather than through vector's own permissioned capabilities
	// (go-qrdb vector-replicated review, B3).
	if vector.IsForgedKey(key) {
		return nil, &vector.ForgedKeyError{Key: key}
	}
	val, err := e.stg.Get(key)
	if err != nil {
		return nil, err
	}
	if val == nil {
		return nil, nil
	}
	return val, nil
}

// listKeys returns a sorted slice of key strings matching the prefix,
// excluding the reserved 0x00 namespace.
func (e *KVEngine) listKeys(prefix string) (interface{}, error) {
	keys, err := e.stg.List([]byte(prefix))
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		if statemachine.IsReservedKey(k) {
			continue
		}
		result = append(result, string(k))
	}
	sort.Strings(result)
	return result, nil
}

// scanPrefix scans keys matching a prefix and returns a map of key to
// value bytes, excluding the reserved 0x00 namespace.
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
		if statemachine.IsReservedKey(iter.Key()) {
			continue
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
		return nil, &index.NotFoundError{Name: q.IndexName}
	}
	return idx.Get(q.Value)
}

// lookupIndexScan performs a range index scan.
func (e *KVEngine) lookupIndexScan(q index.IndexScanRequest) (interface{}, error) {
	idx, exists := e.indexes[q.IndexName]
	if !exists {
		return nil, &index.NotFoundError{Name: q.IndexName}
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
		return nil, &index.NotFoundError{Name: q.IndexName}
	}
	return idx.GetCompound(q.Values)
}

// lookupCompoundIndexScan performs a compound index prefix + range scan.
func (e *KVEngine) lookupCompoundIndexScan(q index.CompoundIndexScanRequest) (interface{}, error) {
	idx, exists := e.compoundIndexes[q.IndexName]
	if !exists {
		return nil, &index.NotFoundError{Name: q.IndexName}
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
