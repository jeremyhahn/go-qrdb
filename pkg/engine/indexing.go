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
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	qkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
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
// whether it enforces a unique constraint. MemoryIndex and PebbleIndex (the
// only index types the engine creates) implement it; an index that does not is
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
func CheckUniqueViolation(indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte) error {
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
		existing, err := idx.Get(fieldVal)
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

// indexedPut is a (key, value) write evaluated for unique-constraint
// admissibility by CheckUniqueViolationsBatch.
type indexedPut struct {
	Key   []byte
	Value []byte
}

// CheckUniqueViolationsBatch validates a batch of writes against the unique
// secondary indexes WITHOUT modifying any index. It accounts for two things a
// naive per-put check cannot: (1) keys DELETED in the same batch free the
// unique values they hold (so a delete-then-reinsert of the same value is
// admissible, matching ordered execution), and (2) conflicts BETWEEN puts in
// the batch (two puts mapping different keys to the same unique value).
//
// It lets transaction/batch handlers reject the whole operation set atomically
// — before any state is written — so a unique violation never reaches idx.Put
// on the apply path and never stalls consensus. Returns a *UniqueConstraintError
// on the first conflict, a non-typed error on an index read failure, or nil
// when every write is admissible.
func CheckUniqueViolationsBatch(indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, puts []indexedPut, deletedKeys [][]byte) error {
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
			existing, err := idx.Get(fieldVal)
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
			mapKey := idxName + "\x00" + string(fieldVal)
			if prev, seen := pending[mapKey]; seen && !bytes.Equal(prev, p.Key) {
				return &UniqueConstraintError{Index: idxName, Field: fieldName}
			}
			pending[mapKey] = p.Key
		}
	}
	return nil
}

// UpdateIndexes extracts JSON field values from the written value and updates
// every registered secondary index whose key scope covers key (see
// indexScopes and ComposeIndexScope; an index with no scope covers the whole
// state machine). If the key had a previous value with different indexed
// fields, the old index entries are removed first.
// Non-JSON values or missing fields are silently skipped.
//
// Old index entry deletions are best-effort: failures are ignored so that a
// stale entry in a non-unique index does not block the write. New index entry
// insertions are strict: any error (including unique constraint violations) is
// returned immediately so the caller can abort the write before it commits.
func UpdateIndexes(indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte, prevKv *service.KeyValue) error {
	if len(indexes) == 0 || !anyIndexInScope(indexes, indexScopes, key) {
		return nil
	}

	// Parse the new value as a flat JSON object.
	var newFields map[string]json.RawMessage
	if json.Unmarshal(value, &newFields) != nil {
		return nil // not JSON; nothing to index
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
// are removed first. Each index must implement index.BatchableIndex.
func UpdateIndexesBatch(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, indexScopes map[string][]byte, key, value []byte, prevKv *qkv.KeyValue) error {
	if len(indexes) == 0 || !anyIndexInScope(indexes, indexScopes, key) {
		return nil
	}

	// Parse the new value as a flat JSON object.
	var newFields map[string]json.RawMessage
	if json.Unmarshal(value, &newFields) != nil {
		return nil // not JSON; nothing to index
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
	kvs, err := scanner()
	if err != nil {
		// The scan must not fail an index-create apply (returning an error
		// there would stall consensus), but a silently-incomplete backfill is
		// an operability hazard — log it so it is observable.
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
		if err := idx.Put(kv.Key, val); err != nil {
			// A single entry's insert must not abort the backfill (that would
			// leave the index even more incomplete), but the resulting gap is
			// an operability hazard — log it so it is observable, mirroring
			// the scan-failure Warn above.
			slog.Warn("engine: index backfill entry failed; index built without this entry",
				"field", fieldName, "key", string(kv.Key), "error", err)
		}
	}
}

// BackfillIndexWithPrefix scans key-value pairs matching the given prefix
// and populates the index with entries for any JSON values containing the
// specified field name. Unlike BackfillIndex, it uses a storage iterator
// with prefix bounds for efficient scanning of entity-scoped keys.
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
		_ = idx.Put(key, val)
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
// JSON field names; all are extracted from a single JSON parse.
func UpdateCompoundIndexesBatch(batch storage.Batch, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, compoundIndexScopes map[string][]byte, key, value []byte, prevKv *qkv.KeyValue) error {
	if len(compoundIndexes) == 0 {
		return nil
	}

	var newFields map[string]json.RawMessage
	if json.Unmarshal(value, &newFields) != nil {
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
		newValues := extractCompoundFieldValues(newFields, fieldNames)
		if newValues != nil {
			if err := idx.PutCompoundBatch(batch, key, newValues); err != nil {
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
// prefix and populates the compound index from JSON field values.
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

		_ = idx.PutCompound(key, values)
	}

	return iter.Error()
}

// extractCompoundFieldValues extracts ordered field values from parsed JSON.
// Returns nil if any field is missing (sparse behavior).
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
