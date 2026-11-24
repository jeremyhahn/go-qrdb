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
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	qkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// EntityIndex defines an index to be created for an entity type's field.
// It is used by RegisterEntityType to set up storage-backed indexes.
// For compound indexes, FieldNames and JSONFieldNames are used instead
// of the singular FieldName and JSONFieldName.
type EntityIndex struct {
	// FieldName is the Go struct field name (used as the index name component).
	// Used for single-field indexes. Ignored when FieldNames is non-empty.
	FieldName string

	// JSONFieldName is the JSON tag name (used for field extraction from serialized bytes).
	// Used for single-field indexes. Ignored when JSONFieldNames is non-empty.
	JSONFieldName string

	// FieldNames is an ordered list of Go struct field names for compound indexes.
	FieldNames []string

	// JSONFieldNames is an ordered list of JSON tag names for compound indexes.
	JSONFieldNames []string

	// Unique enforces that each indexed value maps to at most one key.
	Unique bool

	// Sparse skips indexing keys where the indexed field is absent or empty.
	Sparse bool

	// CaseSensitive indicates whether string comparisons are case-sensitive.
	CaseSensitive bool
}

// IsCompound reports whether this is a compound (multi-field) index.
func (ei EntityIndex) IsCompound() bool {
	return len(ei.FieldNames) > 1
}

// uniqueIndexChecker is the optional capability an index exposes to report
// whether it enforces a unique constraint. Every index type the engines and
// state machines create (index.StoragePebbleIndex, index.MemoryIndex and
// pebbleindex.Index) implements it; an index that does not is
// treated as non-unique and cannot raise a unique-constraint conflict.
type uniqueIndexChecker interface {
	IsUnique() bool
}

// CheckUniqueViolation reports the unique-constraint conflict that writing
// value under key would cause, WITHOUT modifying any index. Only indexes whose
// key scope covers key are consulted: a unique index outside the key's scope
// must not be able to reject the write, which would let one tenant's stored
// value deny another tenant's -- and, by denying it, reveal that it exists.
//
// It mirrors the constraint UpdateIndexes enforces but as a read-only
// pre-check, so an update
// handler can reject the write via an application-error result instead of
// letting idx.Put return a fatal error from the Raft apply path (which would
// stall consensus). It returns a *UniqueConstraintError on conflict, a
// non-typed error if an index read fails (a genuine IO fault the caller may
// treat as fatal), or nil when the write is admissible.
func CheckUniqueViolation(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte) error {
	if len(indexes) == 0 || !anyIndexInScope(indexes, indexScopes, key) {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil {
		return nil // not JSON; nothing to index, nothing to violate
	}
	for idxName, idx := range indexes {
		if !KeyInScope(indexScopes[idxName], key) {
			continue
		}
		u, ok := idx.(uniqueIndexChecker)
		if !ok || !u.IsUnique() {
			continue
		}
		fieldName, ok := indexFields[idxName]
		if !ok {
			continue
		}
		raw, exists := fields[fieldName]
		if !exists {
			continue
		}
		fieldVal := UnquoteJSON(raw)
		existing, err := getIndexValues(batch, idx, fieldVal)
		if err != nil {
			return err
		}
		for _, k := range existing {
			if !bytes.Equal(k, key) {
				return &UniqueConstraintError{Index: idxName, Field: fieldName}
			}
		}
	}
	return nil
}

// getIndexValues reads idx's entries for fieldVal through batch when idx
// implements index.BatchableIndex -- so an uncommitted write staged by an
// earlier entry of the same apply batch is seen -- and falls back to idx.Get
// (committed storage only) for an index that does not.
func getIndexValues(batch storage.Batch, idx index.Index, fieldVal []byte) ([][]byte, error) {
	if bidx, ok := idx.(index.BatchableIndex); ok {
		return bidx.GetInBatch(batch, fieldVal)
	}
	return idx.Get(fieldVal)
}

// indexedWrite is one op of a transaction or batch, in op order, evaluated
// for unique-constraint admissibility by CheckUniqueViolationsBatch: a put of
// Value to Key, or (Delete set) a delete of Key.
type indexedWrite struct {
	Key    []byte
	Value  []byte
	Delete bool
}

// CheckUniqueViolationsBatch validates an ordered set of writes against the
// unique secondary indexes WITHOUT modifying any index. It replays the writes
// in op order, as apply runs them, and so accounts for what a naive per-put
// check cannot: (1) a delete or an overwrite of a key releases the unique
// entries the value it holds before that write carries (read through prev,
// then the set's own earlier writes), so a LATER op of the set may claim
// them (a delete-then-reinsert, or moving a value from one key to another),
// while an EARLIER op may not; an overwrite with a non-JSON value, or one
// without the indexed field, releases those entries and claims nothing. A
// stale entry the prior value does not carry (one a legacy plain non-JSON
// overwrite kept) is not released, since apply cannot retract it: a set
// that claims its term for another key is refused here; and (2) conflicts
// BETWEEN writes of the set (two keys claiming the same unique value), where a
// later write of a key drops that key's earlier claim.
//
// It lets transaction/batch handlers reject the whole operation set atomically
// — before any state is written — so a unique violation never reaches idx.Put
// on the apply path and never stalls consensus. Returns a *UniqueConstraintError
// on the first conflict, a non-typed error on an index or prev read failure,
// or nil when every write is admissible. prev returns the value a key holds
// before the set ((nil, nil) when absent); nil prev means every key is absent.
func CheckUniqueViolationsBatch(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, writes []indexedWrite, prev func(key []byte) ([]byte, error)) error {
	if len(indexes) == 0 || len(writes) == 0 {
		return nil
	}
	// released holds the (index, term, holder) entries an earlier write of
	// the set retracts: those the written key's value carried before that
	// write, the only entries apply removes. A stale entry the value does not
	// carry (one a legacy non-JSON overwrite kept) stays held.
	released := make(map[uniqueHolder]struct{})
	cur := make(map[string][]byte)
	// pending records the key each (index, value) is claimed by within this
	// set, so an intra-set duplicate is caught too; claims indexes it by key so
	// a later write of the same key can drop the key's earlier claims.
	pending := make(map[uniqueClaim]string)
	claims := make(map[string][]uniqueClaim)
	for _, w := range writes {
		key := string(w.Key)
		for _, mapKey := range claims[key] {
			if pending[mapKey] == key {
				delete(pending, mapKey)
			}
		}
		delete(claims, key)
		before, err := valueBeforeWrite(cur, prev, w.Key)
		if err != nil {
			return err
		}
		releaseUniqueEntries(released, indexes, indexFields, indexScopes, w.Key, before)
		if w.Delete {
			cur[key] = nil
			continue
		}
		cur[key] = w.Value
		var fields map[string]json.RawMessage
		if json.Unmarshal(w.Value, &fields) != nil {
			continue // not JSON; nothing to index
		}
		for idxName, idx := range indexes {
			if !KeyInScope(indexScopes[idxName], w.Key) {
				continue
			}
			u, ok := idx.(uniqueIndexChecker)
			if !ok || !u.IsUnique() {
				continue
			}
			fieldName, ok := indexFields[idxName]
			if !ok {
				continue
			}
			raw, exists := fields[fieldName]
			if !exists {
				continue
			}
			fieldVal := UnquoteJSON(raw)
			// The term the index stores (case-folded or numeric-encoded per
			// its config) keys both the released entries and the claims, so
			// two raw spellings of one stored term collide here and not in
			// PutBatch at apply.
			term, indexable := uniqueIndexTerm(idx, fieldVal)
			if !indexable {
				continue // the index stores no entry for this value
			}
			mapKey := uniqueClaim{idxName, string(term)}
			existing, err := getIndexValues(batch, idx, fieldVal)
			if err != nil {
				return err
			}
			for _, k := range existing {
				if string(k) == key {
					continue // the key's own entry
				}
				if _, isReleased := released[uniqueHolder{mapKey, string(k)}]; isReleased {
					continue // an entry an earlier write of the set retracted
				}
				return &UniqueConstraintError{Index: idxName, Field: fieldName}
			}
			if prevKey, seen := pending[mapKey]; seen && prevKey != key {
				return &UniqueConstraintError{Index: idxName, Field: fieldName}
			}
			pending[mapKey] = key
			claims[key] = append(claims[key], mapKey)
		}
	}
	return nil
}

// valueBeforeWrite returns the value key holds before the next write of a
// set: the set's own last write of key, else prev(key), else nil (absent, or
// prev nil: every key absent before the set).
func valueBeforeWrite(cur map[string][]byte, prev func(key []byte) ([]byte, error), key []byte) ([]byte, error) {
	if v, ok := cur[string(key)]; ok {
		return v, nil
	}
	if prev == nil {
		return nil, nil
	}
	return prev(key)
}

// uniqueIndexTerm returns the term idx stores for fieldVal (normalized under
// the index's Config(), raw when it has none) and false when the index
// stores no entry for it.
// uniqueClaim names one index's stored term (or compound tuple). It is a
// struct, not a separator-joined string, because keys and JSON strings may
// hold NUL: a joined key could make two distinct (index, term) pairs collide.
type uniqueClaim struct{ index, term string }

// uniqueHolder names a uniqueClaim held by one key.
type uniqueHolder struct {
	uniqueClaim
	key string
}

func uniqueIndexTerm(idx index.Index, fieldVal []byte) ([]byte, bool) {
	if c, hasCfg := idx.(interface{ Config() index.IndexConfig }); hasCfg {
		return index.NormalizeFieldValue(fieldVal, c.Config())
	}
	return fieldVal, true
}

// releaseUniqueEntries adds to released the (index, term, key) entry of every
// unique single-field index in scope that value carries: the entries apply
// removes when key is overwritten or deleted. A value that is not a JSON
// object carries none.
func releaseUniqueEntries(released map[uniqueHolder]struct{}, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte) {
	fields := parseIndexFields(value)
	if fields == nil {
		return
	}
	for idxName, idx := range indexes {
		if !KeyInScope(indexScopes[idxName], key) {
			continue
		}
		u, ok := idx.(uniqueIndexChecker)
		if !ok || !u.IsUnique() {
			continue
		}
		fieldName, ok := indexFields[idxName]
		if !ok {
			continue
		}
		raw, exists := fields[fieldName]
		if !exists {
			continue
		}
		term, indexable := uniqueIndexTerm(idx, UnquoteJSON(raw))
		if !indexable {
			continue
		}
		released[uniqueHolder{uniqueClaim{idxName, string(term)}, string(key)}] = struct{}{}
	}
}

// indexedPut is a (key, value) write evaluated for unique-constraint
// admissibility by checkUniqueViolationsBatchUnordered.
type indexedPut struct {
	Key   []byte
	Value []byte
}

// checkUniqueViolationsBatchUnordered is the legacy unique pre-check, the
// semantics every OpTransaction and OpBatch command without the
// protocol.CmdFlagOrderedUnique trailer keeps, so a log written by an older
// binary (or proposed before the cluster feature ordered-unique-check was
// enabled) replays to the same state it committed. Op order is ignored: only
// a key DELETED in the set frees the unique values it holds, a put may update
// its own key's entry, and two puts mapping different keys to one unique
// value conflict. An overwrite does not free the overwritten key's values, so
// moving a value between keys is refused. Like CheckUniqueViolationsBatch it
// modifies no index and returns a *UniqueConstraintError on the first
// conflict.
func checkUniqueViolationsBatchUnordered(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, puts []indexedPut, deletedKeys [][]byte) error {
	if len(indexes) == 0 || len(puts) == 0 {
		return nil
	}
	deleted := make(map[string]struct{}, len(deletedKeys))
	for _, k := range deletedKeys {
		deleted[string(k)] = struct{}{}
	}
	// pending records the key each (index, value) is claimed by within this
	// batch, so an intra-batch duplicate is caught too.
	pending := make(map[string][]byte)
	for _, p := range puts {
		var fields map[string]json.RawMessage
		if json.Unmarshal(p.Value, &fields) != nil {
			continue // not JSON; nothing to index
		}
		for idxName, idx := range indexes {
			if !KeyInScope(indexScopes[idxName], p.Key) {
				continue
			}
			u, ok := idx.(uniqueIndexChecker)
			if !ok || !u.IsUnique() {
				continue
			}
			fieldName, ok := indexFields[idxName]
			if !ok {
				continue
			}
			raw, exists := fields[fieldName]
			if !exists {
				continue
			}
			fieldVal := UnquoteJSON(raw)
			existing, err := getIndexValues(batch, idx, fieldVal)
			if err != nil {
				return err
			}
			for _, k := range existing {
				if bytes.Equal(k, p.Key) {
					continue // the key updating its own entry
				}
				if _, isDeleted := deleted[string(k)]; isDeleted {
					continue // the conflicting key is removed in this batch
				}
				return &UniqueConstraintError{Index: idxName, Field: fieldName}
			}
			// This unordered legacy check keeps the NUL-joined key so unflagged logs replay as committed.
			mapKey := idxName + "\x00" + string(fieldVal)
			if prev, seen := pending[mapKey]; seen && !bytes.Equal(prev, p.Key) {
				return &UniqueConstraintError{Index: idxName, Field: fieldName}
			}
			pending[mapKey] = p.Key
		}
	}
	return nil
}

// checkCompoundUniqueBatch pre-checks the unique compound indexes over a set
// of writes in op order, reading each index through batch, the check
// CheckUniqueViolationsBatch makes for single-field indexes. A tuple entry
// the value of a key carried before an earlier write of the set overwrote or
// deleted it (read through prev; nil prev: every key absent) is free for the
// writes after it; a stale entry that value does not carry (one a legacy
// plain non-JSON overwrite kept) stays held, and two writes of the set cannot claim one tuple for two
// keys. It reads only, so a violation refuses the whole set before anything
// is staged; apply (PutCompoundBatchOrdered) then admits exactly what it
// admits. Indexes are visited in name order so the refusal names the same
// index on every replica.
func checkCompoundUniqueBatch(batch storage.Batch, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, compoundIndexScopes map[string][]byte, writes []indexedWrite, prev func(key []byte) ([]byte, error)) error {
	if len(compoundIndexes) == 0 || len(writes) == 0 {
		return nil
	}
	names := slices.Sorted(maps.Keys(compoundIndexes))
	// released holds the (index, tuple, holder) entries an earlier write of
	// the set retracts: those the written key's value carried before that
	// write. pending records the key each (index, tuple) is claimed by within
	// the set, and claims indexes those by key so a later write of the same
	// key drops the key's earlier claims.
	released := make(map[uniqueHolder]struct{})
	cur := make(map[string][]byte)
	pending := make(map[uniqueClaim]string)
	claims := make(map[string][]uniqueClaim)
	for _, w := range writes {
		key := string(w.Key)
		for _, mapKey := range claims[key] {
			if pending[mapKey] == key {
				delete(pending, mapKey)
			}
		}
		delete(claims, key)
		before, err := valueBeforeWrite(cur, prev, w.Key)
		if err != nil {
			return err
		}
		releaseCompoundUniqueEntries(released, compoundIndexes, compoundIndexFields, compoundIndexScopes, w.Key, before)
		if w.Delete {
			cur[key] = nil
			continue
		}
		cur[key] = w.Value
		fields := parseIndexFields(w.Value)
		if fields == nil {
			continue // not a JSON object; nothing to index
		}
		for _, idxName := range names {
			idx := compoundIndexes[idxName]
			if !idx.Config().Unique || !KeyInScope(compoundIndexScopes[idxName], w.Key) {
				continue
			}
			fieldNames, ok := compoundIndexFields[idxName]
			if !ok {
				continue
			}
			values := extractCompoundFieldValues(fields, fieldNames)
			if values == nil {
				continue // sparse: a missing field indexes nothing
			}
			conflict := &UniqueConstraintError{Index: idxName, Field: strings.Join(fieldNames, ",")}
			mapKey := uniqueClaim{idxName, string(idx.CompoundTuple(values))}
			existing, err := idx.GetCompoundInBatch(batch, values)
			if err != nil {
				return err
			}
			for _, k := range existing {
				if string(k) == key {
					continue // the key's own entry
				}
				if _, isReleased := released[uniqueHolder{mapKey, string(k)}]; !isReleased {
					return conflict
				}
			}
			if prevKey, seen := pending[mapKey]; seen && prevKey != key {
				return conflict
			}
			pending[mapKey] = key
			claims[key] = append(claims[key], mapKey)
		}
	}
	return nil
}

// releaseCompoundUniqueEntries adds to released the (index, tuple, key)
// entry of every unique compound index in scope that value carries: the
// entries apply removes when key is overwritten or deleted.
func releaseCompoundUniqueEntries(released map[uniqueHolder]struct{}, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, compoundIndexScopes map[string][]byte, key, value []byte) {
	fields := parseIndexFields(value)
	if fields == nil {
		return
	}
	for idxName, idx := range compoundIndexes {
		if !idx.Config().Unique || !KeyInScope(compoundIndexScopes[idxName], key) {
			continue
		}
		fieldNames, ok := compoundIndexFields[idxName]
		if !ok {
			continue
		}
		values := extractCompoundFieldValues(fields, fieldNames)
		if values == nil {
			continue
		}
		released[uniqueHolder{uniqueClaim{idxName, string(idx.CompoundTuple(values))}, string(key)}] = struct{}{}
	}
}

// UpdateIndexes extracts JSON field values from the written value and updates
// every registered secondary index whose key scope covers key (see
// indexScopes and ComposeIndexScope; an index with no scope covers the whole
// state machine). If the key had a previous value with different indexed
// fields, the old index entries are removed first. The new value need not be
// JSON: one that is not a JSON object, or lacks an indexed field, adds no
// entry for it, and the old entries are removed all the same.
//
// Old index entry deletions are best-effort: failures are ignored so that a
// stale entry in a non-unique index does not block the write. New index entry
// insertions are strict: any error (including unique constraint violations) is
// returned immediately so the caller can abort the write before it commits.
func UpdateIndexes(indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte, prevKv *service.KeyValue) error {
	if len(indexes) == 0 || !anyIndexInScope(indexes, indexScopes, key) {
		return nil
	}

	// Parse the new value as a flat JSON object. A value that is not one
	// indexes nothing, but the previous value's entries are still removed
	// below, so overwriting a record with non-JSON bytes releases them.
	newFields := parseIndexFields(value)

	// Parse the old value (if any) for index cleanup.
	var oldFields map[string]json.RawMessage
	if prevKv != nil && len(prevKv.Value) > 0 {
		_ = json.Unmarshal(prevKv.Value, &oldFields) // best-effort
	}

	for idxName, idx := range indexes {
		if !KeyInScope(indexScopes[idxName], key) {
			continue
		}
		fieldName, ok := indexFields[idxName]
		if !ok {
			continue
		}

		// Remove old index entry if the field existed in the previous value.
		// Deletion failures are best-effort: a stale reverse mapping does not
		// affect correctness of new writes.
		if oldFields != nil {
			if oldRaw, exists := oldFields[fieldName]; exists {
				oldVal := UnquoteJSON(oldRaw)
				_ = idx.Delete(key, oldVal)
			}
		}

		// Add new index entry if the field exists in the new value.
		// Propagate errors so unique constraint violations surface immediately.
		if newRaw, exists := newFields[fieldName]; exists {
			newVal := UnquoteJSON(newRaw)
			if err := idx.Put(key, newVal); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateIndexesBatch extracts JSON field values from the written value and
// queues index updates into the provided batch for atomic commit. Only the
// indexes whose key scope covers key are touched (see indexScopes). If the key
// had a previous value with different indexed fields, the old index entries
// are removed first. A new value that is not JSON is skipped and the old
// entries are kept (see updateIndexesBatch for the ordered cleanup). Each
// index must implement index.BatchableIndex.
func UpdateIndexesBatch(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte, prevKv *qkv.KeyValue) error {
	return updateIndexesBatch(batch, indexes, indexFields, indexScopes, key, value, prevKv, false)
}

// updateIndexesBatch is UpdateIndexesBatch with the non-JSON cleanup
// versioned by ordered. With ordered false (every command without
// protocol.CmdFlagOrderedUnique, and every existing log) a new value that is
// not JSON returns at once and the previous value's entries are kept, as the
// binaries that committed those logs applied them. With ordered true (a
// transaction or batch proposed with ordered-unique-check) such a value adds
// no entry and the previous value's entries are removed, releasing a unique
// value the record held.
func updateIndexesBatch(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte, prevKv *qkv.KeyValue, ordered bool) error {
	if len(indexes) == 0 || !anyIndexInScope(indexes, indexScopes, key) {
		return nil
	}

	// Parse the new value as a flat JSON object.
	var newFields map[string]json.RawMessage
	if ordered {
		newFields = parseIndexFields(value)
	} else if json.Unmarshal(value, &newFields) != nil {
		return nil // not JSON; nothing to index, old entries kept
	}

	// Parse the old value (if any) for index cleanup.
	var oldFields map[string]json.RawMessage
	if prevKv != nil && len(prevKv.Value) > 0 {
		_ = json.Unmarshal(prevKv.Value, &oldFields) // best-effort
	}

	for idxName, idx := range indexes {
		if !KeyInScope(indexScopes[idxName], key) {
			continue
		}
		fieldName, ok := indexFields[idxName]
		if !ok {
			continue
		}

		bIdx, bOk := idx.(index.BatchableIndex)
		if !bOk {
			continue
		}

		// Remove old index entry if the field existed in the previous value.
		if oldFields != nil {
			if oldRaw, exists := oldFields[fieldName]; exists {
				oldVal := UnquoteJSON(oldRaw)
				if err := bIdx.DeleteBatch(batch, key, oldVal); err != nil {
					return err
				}
			}
		}

		// Add new index entry if the field exists in the new value.
		if newRaw, exists := newFields[fieldName]; exists {
			newVal := UnquoteJSON(newRaw)
			if err := bIdx.PutBatch(batch, key, newVal); err != nil {
				return err
			}
		}
	}
	return nil
}

// RemoveIndexes removes index entries for a deleted key from every index whose
// key scope covers it (see indexScopes). The previous value is parsed as JSON
// to find indexed field values. Returns the first deletion error encountered;
// all indexes in scope are still attempted regardless.
func RemoveIndexes(indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key []byte, prevKv *service.KeyValue) error {
	if len(indexes) == 0 || prevKv == nil || len(prevKv.Value) == 0 {
		return nil
	}
	if !anyIndexInScope(indexes, indexScopes, key) {
		return nil
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(prevKv.Value, &fields) != nil {
		return nil
	}

	var firstErr error
	for idxName, idx := range indexes {
		if !KeyInScope(indexScopes[idxName], key) {
			continue
		}
		fieldName, ok := indexFields[idxName]
		if !ok {
			continue
		}
		if raw, exists := fields[fieldName]; exists {
			val := UnquoteJSON(raw)
			if err := idx.Delete(key, val); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// RemoveIndexesBatch removes index entries for a deleted key by queuing
// deletions into the provided batch, for every index whose key scope covers
// the key (see indexScopes). The previous value is parsed as JSON
// to find indexed field values. Each index must implement index.BatchableIndex.
func RemoveIndexesBatch(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key []byte, prevKv *qkv.KeyValue) error {
	if len(indexes) == 0 || prevKv == nil || len(prevKv.Value) == 0 {
		return nil
	}
	if !anyIndexInScope(indexes, indexScopes, key) {
		return nil
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(prevKv.Value, &fields) != nil {
		return nil
	}

	for idxName, idx := range indexes {
		if !KeyInScope(indexScopes[idxName], key) {
			continue
		}
		fieldName, ok := indexFields[idxName]
		if !ok {
			continue
		}

		bIdx, bOk := idx.(index.BatchableIndex)
		if !bOk {
			continue
		}

		if raw, exists := fields[fieldName]; exists {
			val := UnquoteJSON(raw)
			if err := bIdx.DeleteBatch(batch, key, val); err != nil {
				return err
			}
		}
	}
	return nil
}

// BackfillIndex scans the key-value pairs returned by the scanner function
// and populates the given index with entries for any JSON values containing
// the specified field name whose key falls inside scope. Errors on individual
// entries are silently skipped to ensure partial data doesn't prevent index
// creation.
//
// The scope filter is what keeps index CREATION from doing in one step what
// unscoped maintenance did over time: a backfill that ignored it would seed a
// new index with every matching key in the state machine, including keys
// belonging to other databases and other tenants (go-qrdb#410). A nil scope
// backfills everything, which is the unscoped index.
func BackfillIndex(scanner func() ([]*service.KeyValue, error), idx index.Index, fieldName string, scope []byte) {
	backfillEach(scanner, fieldName, scope, idx.Put)
}

// BackfillIndexInBatch is BackfillIndex with every entry queued into the
// apply batch through PutBatch instead of written straight to storage, so a
// rebuild cleared through the same batch commits atomically with the entries
// applied before it.
func BackfillIndexInBatch(batch storage.Batch, scanner func() ([]*service.KeyValue, error), idx interface {
	PutBatch(storage.Batch, []byte, []byte) error
}, fieldName string, scope []byte) {
	backfillEach(scanner, fieldName, scope, func(key, val []byte) error {
		return idx.PutBatch(batch, key, val)
	})
}

// backfillEach runs the scan and hands every in-scope key whose JSON value
// carries fieldName to put, logging (never returning) scan and entry
// failures: an index-create apply must not fail, which would stall
// consensus, but a silently-incomplete backfill is an operability hazard.
func backfillEach(scanner func() ([]*service.KeyValue, error), fieldName string, scope []byte, put func(key, val []byte) error) {
	kvs, err := scanner()
	if err != nil {
		slog.Warn("engine: index backfill scan failed; index created without backfill",
			"field", fieldName, "error", err)
		return
	}

	for _, kv := range kvs {
		if len(kv.Value) == 0 || !KeyInScope(scope, kv.Key) {
			continue
		}

		var fields map[string]json.RawMessage
		if json.Unmarshal(kv.Value, &fields) != nil {
			continue
		}

		raw, exists := fields[fieldName]
		if !exists {
			continue
		}

		val := UnquoteJSON(raw)
		if err := put(kv.Key, val); err != nil {
			// A single entry's insert must not abort the backfill (that would
			// leave the index even more incomplete), but the resulting gap is
			// an operability hazard — log it so it is observable.
			slog.Warn("engine: index backfill entry failed; index built without this entry",
				"field", fieldName, "key", string(kv.Key), "error", err)
		}
	}
}

// UniqueScopeCheck reports whether two keys of a scope hold the same normalized value of a field, under the backfill's skip rules.
type UniqueScopeCheck struct {
	fieldName string
	scope     []byte
	cfg       index.IndexConfig
	seen      map[string]string
}

// NewUniqueScopeCheck returns a check for fieldName over the keys in scope, normalizing values as cfg does.
func NewUniqueScopeCheck(fieldName string, scope []byte, cfg index.IndexConfig) *UniqueScopeCheck {
	return &UniqueScopeCheck{fieldName: fieldName, scope: scope, cfg: cfg, seen: make(map[string]string)}
}

// Conflict records key's term and reports whether a different key already holds the same term.
func (c *UniqueScopeCheck) Conflict(key, value []byte) bool {
	if len(value) == 0 || !KeyInScope(c.scope, key) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil {
		return false
	}
	raw, exists := fields[c.fieldName]
	if !exists {
		return false
	}
	term, ok := index.NormalizeFieldValue(UnquoteJSON(raw), c.cfg)
	if !ok {
		return false
	}
	prev, seen := c.seen[string(term)]
	if seen {
		return prev != string(key)
	}
	c.seen[string(term)] = string(key)
	return false
}

// ScanConflict runs the check over every key the scanner returns.
func (c *UniqueScopeCheck) ScanConflict(scanner func() ([]*service.KeyValue, error)) (bool, error) {
	kvs, err := scanner()
	if err != nil {
		return false, err
	}
	for _, kv := range kvs {
		if c.Conflict(kv.Key, kv.Value) {
			return true, nil
		}
	}
	return false, nil
}

// RescopeConflictResult is the refusal a unique-index rescope gets when its new scope holds duplicate values.
func RescopeConflictResult(name string) sm.Result {
	return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorConflict, "unique index "+name+": new key scope holds duplicate values")}
}

// BackfillIndexWithPrefix scans key-value pairs matching the given prefix
// and populates the index with entries for any JSON values containing the
// specified field name. Unlike BackfillIndex, it uses a storage iterator
// with prefix bounds for efficient scanning of entity-scoped keys.
// Empty values, non-JSON values and values without the field are skipped;
// any error from idx.Put (a unique violation among existing records or a
// storage write failure) stops the backfill and is returned wrapped with
// the entity key, leaving the index partially populated for the caller to
// clear.
func BackfillIndexWithPrefix(stg storage.Storage, idx index.Index, fieldName string, prefix []byte) error {
	iter := stg.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: prefixEndBytes(prefix),
	})
	defer func() { _ = iter.Close() }()

	for ok := iter.SeekToFirst(); ok && iter.Valid(); ok = iter.Next() {
		if !bytes.HasPrefix(iter.Key(), prefix) {
			break
		}

		if len(iter.Value()) == 0 {
			continue
		}

		var fields map[string]json.RawMessage
		if json.Unmarshal(iter.Value(), &fields) != nil {
			continue
		}

		raw, exists := fields[fieldName]
		if !exists {
			continue
		}

		// Copy key to avoid iterator invalidation.
		key := make([]byte, len(iter.Key()))
		copy(key, iter.Key())

		val := UnquoteJSON(raw)
		if err := idx.Put(key, val); err != nil {
			return fmt.Errorf("backfill %s: %w", key, err)
		}
	}

	if err := iter.Error(); err != nil {
		return err
	}
	return nil
}

// UnquoteJSON returns the unquoted string value from a JSON token. For
// quoted strings it strips the surrounding quotes. For other JSON types
// (numbers, booleans) it returns the raw bytes as-is.
func UnquoteJSON(raw json.RawMessage) []byte {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []byte(s)
	}
	// Not a string (number, bool, etc) -- use raw representation.
	return []byte(raw)
}

// UpdateCompoundIndexesBatch extracts JSON field values and queues compound
// index updates into the provided batch, for every compound index whose key
// scope covers key (see compoundIndexScopes). Each compound index has multiple
// JSON field names; all are extracted from a single JSON parse. A new value
// that is not JSON is skipped and the previous value's compound entries are
// kept; one lacking any of the fields adds no entry.
func UpdateCompoundIndexesBatch(batch storage.Batch, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, compoundIndexScopes map[string][]byte, key, value []byte, prevKv *qkv.KeyValue) error {
	return updateCompoundIndexesBatch(batch, compoundIndexes, compoundIndexFields, compoundIndexScopes, key, value, prevKv, false)
}

// updateCompoundIndexesBatch is UpdateCompoundIndexesBatch; ordered checks a
// unique index through the batch (PutCompoundBatchOrdered), as an op-ordered
// (CmdFlagOrderedUnique) transaction applies, instead of committed storage,
// and removes the previous value's compound entries when the new value is not
// JSON (unordered keeps them, as existing logs were applied).
func updateCompoundIndexesBatch(batch storage.Batch, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, compoundIndexScopes map[string][]byte, key, value []byte, prevKv *qkv.KeyValue, ordered bool) error {
	if len(compoundIndexes) == 0 {
		return nil
	}

	// A new value that is not JSON: unordered (existing logs) returns at once
	// and keeps the previous value's entries; ordered adds no entry but still
	// removes the previous value's entries below.
	var newFields map[string]json.RawMessage
	if ordered {
		newFields = parseIndexFields(value)
	} else if json.Unmarshal(value, &newFields) != nil {
		return nil
	}

	var oldFields map[string]json.RawMessage
	if prevKv != nil && len(prevKv.Value) > 0 {
		_ = json.Unmarshal(prevKv.Value, &oldFields)
	}

	for idxName, idx := range compoundIndexes {
		if !KeyInScope(compoundIndexScopes[idxName], key) {
			continue
		}
		fieldNames, ok := compoundIndexFields[idxName]
		if !ok {
			continue
		}

		// Remove old compound entry.
		if oldFields != nil {
			oldValues := extractCompoundFieldValues(oldFields, fieldNames)
			if oldValues != nil {
				_ = idx.DeleteCompoundBatch(batch, key, oldValues)
			}
		}

		// Add new compound entry.
		if newFields == nil {
			continue
		}
		newValues := extractCompoundFieldValues(newFields, fieldNames)
		if newValues != nil {
			put := idx.PutCompoundBatch
			if ordered {
				put = idx.PutCompoundBatchOrdered
			}
			if err := put(batch, key, newValues); err != nil {
				return err
			}
		}
	}
	return nil
}

// RemoveCompoundIndexesBatch removes compound index entries for a deleted key
// from every compound index whose key scope covers it.
func RemoveCompoundIndexesBatch(batch storage.Batch, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, compoundIndexScopes map[string][]byte, key []byte, prevKv *qkv.KeyValue) error {
	if len(compoundIndexes) == 0 || prevKv == nil || len(prevKv.Value) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(prevKv.Value, &fields) != nil {
		return nil
	}

	for idxName, idx := range compoundIndexes {
		if !KeyInScope(compoundIndexScopes[idxName], key) {
			continue
		}
		fieldNames, ok := compoundIndexFields[idxName]
		if !ok {
			continue
		}

		values := extractCompoundFieldValues(fields, fieldNames)
		if values != nil {
			if err := idx.DeleteCompoundBatch(batch, key, values); err != nil {
				return err
			}
		}
	}
	return nil
}

// BackfillCompoundIndexWithPrefix scans key-value pairs matching the given
// prefix and populates the compound index from JSON field values. Empty
// values, non-JSON values and values missing any of the fields are skipped;
// any error from idx.PutCompound (a unique violation among existing records
// or a storage write failure) stops the backfill and is returned wrapped
// with the entity key, leaving the index partially populated for the caller
// to clear.
func BackfillCompoundIndexWithPrefix(stg storage.Storage, idx *index.CompoundIndex, fieldNames []string, prefix []byte) error {
	iter := stg.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: prefixEndBytes(prefix),
	})
	defer func() { _ = iter.Close() }()

	for ok := iter.SeekToFirst(); ok && iter.Valid(); ok = iter.Next() {
		if !bytes.HasPrefix(iter.Key(), prefix) {
			break
		}

		if len(iter.Value()) == 0 {
			continue
		}

		var fields map[string]json.RawMessage
		if json.Unmarshal(iter.Value(), &fields) != nil {
			continue
		}

		values := extractCompoundFieldValues(fields, fieldNames)
		if values == nil {
			continue
		}

		key := make([]byte, len(iter.Key()))
		copy(key, iter.Key())

		if err := idx.PutCompound(key, values); err != nil {
			return fmt.Errorf("backfill %s: %w", key, err)
		}
	}

	return iter.Error()
}

// extractCompoundFieldValues extracts ordered field values from parsed JSON.
// Returns nil if any field is missing (sparse behavior).
// parseIndexFields parses value as a flat JSON object for index maintenance.
// It returns nil when value is not a JSON object (arbitrary bytes, an array,
// a scalar or null): such a value has no indexable fields.
func parseIndexFields(value []byte) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil {
		return nil
	}
	return fields
}

func extractCompoundFieldValues(fields map[string]json.RawMessage, fieldNames []string) [][]byte {
	values := make([][]byte, len(fieldNames))
	for i, name := range fieldNames {
		raw, exists := fields[name]
		if !exists {
			return nil // sparse: skip if any field is missing
		}
		values[i] = UnquoteJSON(raw)
	}
	return values
}
