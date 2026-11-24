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

package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	qerrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// errEmbeddedStorageRequired is returned by NewEmbeddedKVStore when it is
// given no storage engine to run over. It stays private: the approved
// embedder surface is the constructors and index types, not this sentinel.
var errEmbeddedStorageRequired = errors.New("sdk: embedded KV store needs a storage engine")

// NewEmbeddedKVStore returns a KVStore that runs the go-qrdb KV engine
// in-process over stg, with secondary indexes maintained atomically on every
// Put and Delete. It is the store an embedding application hands to NewDAO
// when it owns its storage (for example sdkstorage.NewPebbleStorage) and runs
// no server. An overwrite whose value is not a JSON object, or carries none of
// an index's fields, removes the record's old secondary and compound index
// entries, so a unique value it held is released.
//
// The store is a MultiGuardedBatchKVStore: PutAllIfUnchanged and
// PutAllIfAllUnchanged apply a guarded batch as one KV-engine transaction,
// all-or-nothing, with the unique-index check and index updates of the same
// step, so NewDAOTransactional and NewDAOUnitOfWork run over it as they do
// over the service and client stores.
//
// Get reports an absent key as (nil, nil); every other storage failure is
// returned. A query on an index that was never registered reports no matches.
// A definition the engine cannot maintain exactly (a caller-qualified index
// name, a key prefix other than "{entityType}/", a numeric index, or an
// entity type containing "/", whose namespace the engine would compose twice) is
// refused with ErrKVIndexUnsupported, so a DAO downgrades that query kind to
// a full scan instead of querying an index nobody built.
//
// The store is safe for concurrent use. Put, Delete, the guarded batches and
// RegisterEntityIndexes are serialized, so reading a key's old value, checking unique indexes,
// committing the batch and backfilling a newly registered index run as one
// step and two writers never leave a stale or duplicate index entry. Reads
// share a lock and run concurrently with each other. A Scan callback runs
// on a snapshot taken under the read lock and invoked after the lock is
// released, so it may read from or write to the store.
func NewEmbeddedKVStore(stg storage.Storage) (KVStore, error) {
	if stg == nil {
		return nil, errEmbeddedStorageRequired
	}
	return &embeddedKVStore{stg: stg, eng: engine.NewKVEngine(stg)}, nil
}

// embeddedKVStore is the KVStore NewEmbeddedKVStore returns.
var _ kvstore.MultiGuardedBatchKVStore = (*embeddedKVStore)(nil)

type embeddedKVStore struct {
	stg storage.Storage
	eng *engine.KVEngine
	// mu serializes writes and index registration against each other and
	// against reads; see NewEmbeddedKVStore.
	mu sync.RWMutex
}

func (s *embeddedKVStore) Put(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eng.PutWithIndexes([]byte(key), value)
}

// PutAllIfUnchanged applies writes only while guardKey holds guardValue (nil:
// absent); it is PutAllIfAllUnchanged with one guard.
func (s *embeddedKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return s.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged applies writes, in order, only while every guard
// holds: a nil Guard.Value requires the key to be absent, any other value
// requires it to be present and equal. The guards and writes go to the KV
// engine as one transaction, so the batch is all-or-nothing and runs the
// ordered unique-index check and the index updates of the same step. A
// unique-index conflict writes nothing and returns, as Put does, a
// *errors.QRDBError with Code ErrConflict; its cause is the engine's
// *engine.UniqueConstraintError. applied is false, with a nil error, when a guard did not hold.
func (s *embeddedKVStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for i, g := range guards {
		if g.Key == "" {
			return false, &kvstore.EmptyKeyError{Field: "guard", Index: i}
		}
	}
	for i, w := range writes {
		if w.Key == "" {
			return false, &kvstore.EmptyKeyError{Field: "write", Index: i}
		}
	}
	cmd, err := embeddedGuardedTxn(guards, writes)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]sm.Result, 1)
	if err := s.eng.Update(ctx, []sm.Entry{{Cmd: cmd}}, results); err != nil {
		return false, err
	}
	data := results[0].Data
	if msg, kind, ok := protocol.DecodeAppErrorKind(data); ok {
		if kind == protocol.AppErrorConflict {
			var cause error = errors.New(msg)
			if uce := embeddedUniqueConflict(msg); uce != nil {
				cause = uce
			}
			return false, &qerrors.QRDBError{Code: qerrors.ErrConflict, Op: "embedded.PutAllIfAllUnchanged", Err: cause}
		}
		return false, fmt.Errorf("embedded kv store: guarded batch refused: %s", msg)
	}
	return len(data) > 0 && data[0] == 1, nil
}

// embeddedGuardedTxn encodes a guarded batch as one KV-engine transaction:
// each guard becomes the compares the service store's guarded batch uses
// (absent: version == 0; expected value: version > 0 and value ==), each
// write a put or delete op in order, and there is no failure branch.
func embeddedGuardedTxn(guards []kvstore.Guard, writes []kvstore.KV) ([]byte, error) {
	compares := make([][]byte, 0, len(guards)*2)
	addCompare := func(key string, target service.CompareTarget, result service.CompareResult, value []byte) error {
		c, err := protocol.EncodeCompare([]byte(key), byte(target), byte(result), value, 0, 0, 0)
		if err != nil {
			return err
		}
		compares = append(compares, c)
		return nil
	}
	for _, g := range guards {
		if g.Value == nil {
			if err := addCompare(g.Key, service.CompareVersion, service.CompareEqual, nil); err != nil {
				return nil, err
			}
			continue
		}
		if err := addCompare(g.Key, service.CompareVersion, service.CompareGreater, nil); err != nil {
			return nil, err
		}
		if err := addCompare(g.Key, service.CompareValue, service.CompareEqual, g.Value); err != nil {
			return nil, err
		}
	}
	ops := make([][]byte, 0, len(writes))
	for _, w := range writes {
		var (
			op  []byte
			err error
		)
		if w.Delete {
			op, err = protocol.EncodeOp(protocol.OpMVCCDelete, []byte(w.Key), nil, nil, 0)
		} else {
			op, err = protocol.EncodeOp(protocol.OpMVCCPut, []byte(w.Key), w.Value, nil, 0)
		}
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	// The embedded store is one binary with no group to gate, so the
	// transaction carries the ordered unique check at once (a batch that
	// moves a unique value between keys commits, as ordered apply does).
	return protocol.EncodeTransactionFlags(compares, ops, nil, protocol.CmdFlagOrderedUnique)
}

// embeddedUniqueConflict rebuilds the *engine.UniqueConstraintError the KV
// engine reported as a conflict app error (asAppErrResult encodes its
// message), so the caller can name the index and field. It returns nil when the
// message is not the engine's unique-constraint message.
func embeddedUniqueConflict(msg string) *engine.UniqueConstraintError {
	const prefix = "engine: unique constraint violation on index "
	rest, ok := strings.CutPrefix(msg, prefix)
	if !ok {
		return nil
	}
	idx, field, ok := strings.Cut(rest, " (field ")
	if !ok || !strings.HasSuffix(field, ")") {
		return nil
	}
	return &engine.UniqueConstraintError{Index: idx, Field: strings.TrimSuffix(field, ")")}
}

func (s *embeddedKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, err := s.stg.Get([]byte(key))
	if err != nil {
		var notFound *storage.KeyNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, storage.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return value, nil
}

func (s *embeddedKVStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eng.DeleteWithIndexes([]byte(key))
}

func (s *embeddedKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := s.scanSnapshot(ctx, prefix)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(e.key, e.value); err != nil {
			return err
		}
	}
	return nil
}

// scanEntry is one copied key/value pair of a Scan snapshot.
type scanEntry struct {
	key   string
	value []byte
}

// scanSnapshot copies every pair under prefix while holding the read lock,
// so Scan can run its callback after the lock is released.
func (s *embeddedKVStore) scanSnapshot(ctx context.Context, prefix string) ([]scanEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	opts := &storage.IteratorOptions{LowerBound: []byte(prefix), UpperBound: prefixUpperBound([]byte(prefix))}
	it := s.stg.NewIterator(opts)
	defer func() { _ = it.Close() }()
	var entries []scanEntry
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries = append(entries, scanEntry{
			key:   string(it.Key()),
			value: append([]byte(nil), it.Value()...),
		})
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *embeddedKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, err := s.stg.List([]byte(prefix))
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(raw))
	for i, k := range raw {
		keys[i] = string(k)
	}
	return keys, nil
}

func (s *embeddedKVStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stg.Exists([]byte(key))
}

// RegisterEntityIndexes maps each definition onto the engine's and registers
// them; see NewEmbeddedKVStore for the definitions it refuses. Registration
// is all-or-nothing: when the backfill of any index cannot write an entry (a
// unique index over existing duplicate values, or a storage write failure),
// the error is returned and none of the call's indexes is registered.
func (s *embeddedKVStore) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	defs := make([]engine.EntityIndex, 0, len(indexes))
	for _, def := range indexes {
		if err := embeddedIndexSupported(entityType, def); err != nil {
			return err
		}
		defs = append(defs, engine.EntityIndex{
			FieldName:      def.FieldName,
			JSONFieldName:  def.JSONFieldName,
			FieldNames:     def.FieldNames,
			JSONFieldNames: def.JSONFieldNames,
			Unique:         def.Unique,
			Sparse:         def.Sparse,
			CaseSensitive:  def.CaseSensitive,
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eng.RegisterEntityType(entityType, defs)
}

// embeddedIndexSupported refuses a definition the engine would build under a
// different name or scope than the caller will query it by.
func embeddedIndexSupported(entityType string, def kvstore.EntityIndex) error {
	// The engine composes an index's scope from the namespace it parses out
	// of the index name, so a nested entity type (tenant/db/member) would get
	// its namespace prepended twice and no write would ever be indexed.
	if strings.Contains(entityType, "/") {
		return fmt.Errorf("entity type %q is nested (contains \"/\"): %w", entityType, kvstore.ErrIndexUnsupported)
	}
	if def.Numeric {
		return fmt.Errorf("index %s.%s is numeric: %w", entityType, def.FieldName, kvstore.ErrIndexUnsupported)
	}
	if def.KeyPrefix != "" && def.KeyPrefix != entityType+"/" {
		return fmt.Errorf("index key prefix %q is not %q: %w", def.KeyPrefix, entityType+"/", kvstore.ErrIndexUnsupported)
	}
	if def.IndexName == "" {
		return nil
	}
	field := def.FieldName
	if len(def.FieldNames) > 0 {
		field = strings.Join(def.FieldNames, "_")
	}
	if want := entityType + "." + field; def.IndexName != want {
		return fmt.Errorf("index name %q is not %q: %w", def.IndexName, want, kvstore.ErrIndexUnsupported)
	}
	return nil
}

func (s *embeddedKVStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return embeddedKeys(s.eng.Lookup(ctx, index.IndexQueryRequest{IndexName: indexName, Value: value}))
}

func (s *embeddedKVStore) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return embeddedScan(s.eng.Lookup(ctx, index.IndexScanRequest{IndexName: indexName, Start: start, End: end}))
}

func (s *embeddedKVStore) QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return embeddedKeys(s.eng.Lookup(ctx, index.CompoundIndexQueryRequest{IndexName: indexName, Values: values}))
}

func (s *embeddedKVStore) ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return embeddedScan(s.eng.Lookup(ctx, index.CompoundIndexScanRequest{
		IndexName:    indexName,
		PrefixValues: prefixValues,
		RangeStart:   rangeStart,
		RangeEnd:     rangeEnd,
	}))
}

// isMissingIndex reports whether err says the index was never registered.
func isMissingIndex(err error) bool {
	var missing *index.NotFoundError
	return errors.As(err, &missing)
}

func embeddedKeys(result interface{}, err error) ([][]byte, error) {
	if err != nil {
		if isMissingIndex(err) {
			return [][]byte{}, nil
		}
		return nil, err
	}
	if result == nil {
		return [][]byte{}, nil
	}
	keys, ok := result.([][]byte)
	if !ok {
		return nil, fmt.Errorf("sdk: index query returned %T, want [][]byte", result)
	}
	return keys, nil
}

func embeddedScan(result interface{}, err error) (map[string][][]byte, error) {
	if err != nil {
		if isMissingIndex(err) {
			return map[string][][]byte{}, nil
		}
		return nil, err
	}
	if result == nil {
		return map[string][][]byte{}, nil
	}
	switch scan := result.(type) {
	case map[string][][]byte:
		return scan, nil
	case [][]byte:
		// A compound scan with no range bounds is a pure leading-field
		// prefix lookup: the engine answers it with the matching entity
		// keys alone, without their compound values. The keys are grouped
		// under the empty compound value, in index order.
		if len(scan) == 0 {
			return map[string][][]byte{}, nil
		}
		return map[string][][]byte{"": scan}, nil
	default:
		return nil, fmt.Errorf("sdk: index scan returned %T, want map[string][][]byte or [][]byte", result)
	}
}

// prefixUpperBound returns the smallest key greater than every key with the
// given prefix, or nil when no such key exists (empty or all-0xff prefix).
func prefixUpperBound(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
