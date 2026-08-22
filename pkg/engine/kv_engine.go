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
	"sort"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	qkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
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
	stg           storage.Storage
	indexes       map[string]index.Index
	indexFields   map[string]string
	entityIndexes map[string][]EntityIndex
	// compoundIndexes stores compound (multi-field) indexes keyed by
	// their fully-qualified name (e.g., "mydb.messages.ConversationID_Timestamp").
	compoundIndexes map[string]*index.CompoundIndex
	// compoundIndexFields maps compound index name to an ordered list of
	// JSON field names used for value extraction during updates.
	compoundIndexFields map[string][]string
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
		entityIndexes:       make(map[string][]EntityIndex),
		compoundIndexes:     make(map[string]*index.CompoundIndex),
		compoundIndexFields: make(map[string][]string),
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
// MVCC, lease, transaction, and time-series opcodes return
// UnsupportedOperationError. An entry undecodable by both protocols yields
// an application-error result (see CommandDecodeError) rather than being
// silently reported as applied.
func (e *KVEngine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		// Try base KV protocol (OpPut=1, OpDelete=3).
		cmd, err := kv.DecodeCommand(entries[i].Cmd)
		if err == nil {
			handler, ok := e.handlers[cmd.Op]
			if !ok {
				results[i] = sm.Result{Value: 1}
				return &UnsupportedOperationError{Op: cmd.Op}
			}
			result, hErr := handler(e, cmd)
			if hErr != nil {
				results[i] = sm.Result{Value: 1}
				return hErr
			}
			results[i] = result
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

		extFn, ok := e.extHandlers[extCmd.Op]
		if !ok {
			results[i] = sm.Result{Value: 1}
			return &UnsupportedOperationError{Op: extCmd.Op}
		}

		result, extHErr := extFn(extCmd.Payload)
		if extHErr != nil {
			results[i] = sm.Result{Value: 1}
			return extHErr
		}
		results[i] = result
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
// Only index create and index drop are supported; all MVCC, lease,
// transaction, and time-series opcodes are absent and will trigger
// UnsupportedOperationError.
func (e *KVEngine) buildExtHandlers() map[byte]extHandler {
	return map[byte]extHandler{
		protocol.OpIndexCreate:      e.handleIndexCreate,
		protocol.OpIndexDrop:        e.handleIndexDrop,
		protocol.OpBatch:            e.handleBatch,
		protocol.OpCompareAndDelete: e.handleCompareAndDelete,
		protocol.OpCompareAndSwap:   e.handleCompareAndSwap,
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
		if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, cmd.Key, cmd.Value, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}

	// Queue compound index updates into the SAME batch.
	if len(e.compoundIndexes) > 0 {
		if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, cmd.Key, cmd.Value, prevKv); err != nil {
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
		if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, cmd.Key, cmd.Value, nil); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if len(e.compoundIndexes) > 0 {
		if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, cmd.Key, cmd.Value, nil); err != nil {
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
		if err := RemoveIndexesBatch(batch, e.indexes, e.indexFields, key, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if len(e.compoundIndexes) > 0 {
		if err := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, key, prevKv); err != nil {
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
		if err := UpdateIndexesBatch(batch, e.indexes, e.indexFields, key, next, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}
	if len(e.compoundIndexes) > 0 {
		if err := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, key, next, prevKv); err != nil {
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
		if err := RemoveIndexesBatch(batch, e.indexes, e.indexFields, cmd.Key, prevKv); err != nil {
			return sm.Result{Value: 1}, err
		}
	}

	// Queue compound index removals into the SAME batch.
	if len(e.compoundIndexes) > 0 {
		if err := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, cmd.Key, prevKv); err != nil {
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
	name, fieldName, unique, _, numeric, err := protocol.DecodeIndexCreate(payload)
	if err != nil {
		return sm.Result{Value: 1}, err
	}

	// Do not recreate an existing index.
	if _, exists := e.indexes[name]; exists {
		return sm.Result{}, nil
	}

	cfg := index.IndexConfig{
		Name:          name,
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}
	if unique {
		cfg.Type = index.IndexTypeUnique
		cfg.Unique = true
	}
	if numeric {
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

	e.indexes[name] = idx
	e.indexFields[name] = fieldName

	// Backfill the index with existing data.
	BackfillIndex(e.scanAllKVs, idx, fieldName)

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
				if bErr := UpdateIndexesBatch(batch, e.indexes, e.indexFields, op.Key, op.Value, metas[i].prevKv); bErr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, bErr
				}
			}
			if len(e.compoundIndexes) > 0 {
				if bErr := UpdateCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, op.Key, op.Value, metas[i].prevKv); bErr != nil {
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
				if bErr := RemoveIndexesBatch(batch, e.indexes, e.indexFields, op.Key, metas[i].prevKv); bErr != nil {
					_ = batch.Rollback()
					return sm.Result{Value: 1}, bErr
				}
			}
			if len(e.compoundIndexes) > 0 {
				if bErr := RemoveCompoundIndexesBatch(batch, e.compoundIndexes, e.compoundIndexFields, op.Key, metas[i].prevKv); bErr != nil {
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

		cfg := index.IndexConfig{
			Name:          idxName,
			Type:          idxType,
			Unique:        def.Unique,
			Sparse:        def.Sparse,
			CaseSensitive: def.CaseSensitive,
		}

		idx, err := index.NewStoragePebbleIndex(e.stg, cfg, log)
		if err != nil {
			return err
		}

		e.indexes[idxName] = idx
		e.indexFields[idxName] = def.JSONFieldName

		// Backfill from existing keys matching the entity prefix.
		prefix := []byte(entityType + "/")
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

	cfg := index.IndexConfig{
		Name:          idxName,
		Type:          idxType,
		FieldNames:    def.FieldNames,
		Unique:        def.Unique,
		Sparse:        def.Sparse,
		CaseSensitive: def.CaseSensitive,
	}

	idx, err := index.NewCompoundIndex(e.stg, cfg, log)
	if err != nil {
		return err
	}

	e.compoundIndexes[idxName] = idx
	e.compoundIndexFields[idxName] = def.JSONFieldNames

	// Backfill from existing data.
	prefix := []byte(entityType + "/")
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
	if err := UpdateIndexesBatch(batch, filteredIndexes, filteredFields, key, value, prevKv); err != nil {
		return err
	}
	if err := UpdateCompoundIndexesBatch(batch, filteredCompound, filteredCompoundFields, key, value, prevKv); err != nil {
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
	if err := RemoveIndexesBatch(batch, filteredIndexes, filteredFields, key, prevKv); err != nil {
		return err
	}
	if err := RemoveCompoundIndexesBatch(batch, filteredCompound, filteredCompoundFields, key, prevKv); err != nil {
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
