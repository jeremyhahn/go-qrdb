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

// UpdateIndexes extracts JSON field values from the written value and updates
// all registered secondary indexes. If the key had a previous value with
// different indexed fields, the old index entries are removed first.
// Non-JSON values or missing fields are silently skipped.
//
// Old index entry deletions are best-effort: failures are ignored so that a
// stale entry in a non-unique index does not block the write. New index entry
// insertions are strict: any error (including unique constraint violations) is
// returned immediately so the caller can abort the write before it commits.
func UpdateIndexes(indexes map[string]index.Index, indexFields map[string]string, key, value []byte, prevKv *service.KeyValue) error {
	if len(indexes) == 0 {
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
// queues index updates into the provided batch for atomic commit. If the key
// had a previous value with different indexed fields, the old index entries
// are removed first. Each index must implement index.BatchableIndex.
func UpdateIndexesBatch(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, key, value []byte, prevKv *qkv.KeyValue) error {
	if len(indexes) == 0 {
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

// RemoveIndexes removes index entries for a deleted key. The previous value
// is parsed as JSON to find indexed field values. Returns the first deletion
// error encountered; all indexes are still attempted regardless.
func RemoveIndexes(indexes map[string]index.Index, indexFields map[string]string, key []byte, prevKv *service.KeyValue) error {
	if len(indexes) == 0 || prevKv == nil || len(prevKv.Value) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(prevKv.Value, &fields) != nil {
		return nil
	}

	var firstErr error
	for idxName, idx := range indexes {
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
// deletions into the provided batch. The previous value is parsed as JSON
// to find indexed field values. Each index must implement index.BatchableIndex.
func RemoveIndexesBatch(batch storage.Batch, indexes map[string]index.Index, indexFields map[string]string, key []byte, prevKv *qkv.KeyValue) error {
	if len(indexes) == 0 || prevKv == nil || len(prevKv.Value) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(prevKv.Value, &fields) != nil {
		return nil
	}

	for idxName, idx := range indexes {
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

// BackfillIndex scans all key-value pairs returned by the scanner function
// and populates the given index with entries for any JSON values containing
// the specified field name. Errors on individual entries are silently skipped
// to ensure partial data doesn't prevent index creation.
func BackfillIndex(scanner func() ([]*service.KeyValue, error), idx index.Index, fieldName string) {
	kvs, err := scanner()
	if err != nil {
		return
	}

	for _, kv := range kvs {
		if len(kv.Value) == 0 {
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
		_ = idx.Put(kv.Key, val)
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
// index updates into the provided batch. Each compound index has multiple
// JSON field names; all are extracted from a single JSON parse.
func UpdateCompoundIndexesBatch(batch storage.Batch, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, key, value []byte, prevKv *qkv.KeyValue) error {
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

// RemoveCompoundIndexesBatch removes compound index entries for a deleted key.
func RemoveCompoundIndexesBatch(batch storage.Batch, compoundIndexes map[string]*index.CompoundIndex, compoundIndexFields map[string][]string, key []byte, prevKv *qkv.KeyValue) error {
	if len(compoundIndexes) == 0 || prevKv == nil || len(prevKv.Value) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(prevKv.Value, &fields) != nil {
		return nil
	}

	for idxName, idx := range compoundIndexes {
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
