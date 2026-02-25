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

package index

import (
	"encoding/binary"
	"sort"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
)

// entityIDLen is the byte length of a big-endian encoded uint64 entity ID.
const entityIDLen = 8

// keyPrefix is the index key namespace prefix.
const keyPrefix = "idx:"

// keySeparator separates components within an index key.
const keySeparator = ":"

// PebbleIndex is a PebbleDB-backed implementation of the Index interface.
// Keys are stored with the composite format:
//
//	"idx:" + name + ":" + fieldValue + ":" + entityID(8 bytes big-endian)
//
// PebbleIndex does not own the PebbleDB instance and will not close it.
type PebbleIndex struct {
	db     *pebble.DB
	name   string
	unique bool
	closed atomic.Bool
}

// NewPebbleIndex creates a new PebbleDB-backed index with the given name.
// The caller retains ownership of the PebbleDB instance. Returns a
// NilDBError if db is nil or an EmptyNameError if name is empty.
func NewPebbleIndex(db *pebble.DB, name string, unique bool) (*PebbleIndex, error) {
	if db == nil {
		return nil, &NilDBError{}
	}
	if name == "" {
		return nil, &EmptyNameError{}
	}
	return &PebbleIndex{
		db:     db,
		name:   name,
		unique: unique,
	}, nil
}

// Name returns the index name.
func (p *PebbleIndex) Name() string {
	return p.name
}

// IsUnique returns true if the index enforces a unique constraint.
func (p *PebbleIndex) IsUnique() bool {
	return p.unique
}

// Put adds an index entry mapping fieldValue to entityID. If the index
// enforces uniqueness and a different entity already maps to the same
// field value, a UniqueViolationError is returned. The entry is stored
// with an empty value; the composite key encodes all information.
func (p *PebbleIndex) Put(entityID uint64, fieldValue []byte) error {
	if p.closed.Load() {
		return &ClosedError{Name: p.name}
	}
	if fieldValue == nil {
		return &NilFieldValueError{Op: "put"}
	}

	if p.unique {
		existing, err := p.getEntityIDs(fieldValue)
		if err != nil {
			return err
		}
		for _, id := range existing {
			if id == entityID {
				return nil
			}
			return &UniqueViolationError{
				IndexName:  p.name,
				FieldValue: fieldValue,
				ExistingID: id,
				NewID:      entityID,
			}
		}
	}

	key := p.buildKey(fieldValue, entityID)
	return p.db.Set(key, nil, pebble.Sync)
}

// Get retrieves all entity IDs associated with the given field value.
// Returns an empty slice if no entities are found.
func (p *PebbleIndex) Get(fieldValue []byte) ([]uint64, error) {
	if p.closed.Load() {
		return nil, &ClosedError{Name: p.name}
	}
	if fieldValue == nil {
		return nil, &NilFieldValueError{Op: "get"}
	}

	ids, err := p.getEntityIDs(fieldValue)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []uint64{}, nil
	}
	return ids, nil
}

// Delete removes the index entry for the given entity ID and field value.
func (p *PebbleIndex) Delete(entityID uint64, fieldValue []byte) error {
	if p.closed.Load() {
		return &ClosedError{Name: p.name}
	}
	if fieldValue == nil {
		return &NilFieldValueError{Op: "delete"}
	}

	key := p.buildKey(fieldValue, entityID)
	return p.db.Delete(key, pebble.Sync)
}

// Scan retrieves all index entries with field values in the half-open
// interval [start, end). Returns a map of field value strings to entity
// ID slices.
func (p *PebbleIndex) Scan(start, end []byte) (map[string][]uint64, error) {
	if p.closed.Load() {
		return nil, &ClosedError{Name: p.name}
	}
	if start == nil {
		return nil, &NilFieldValueError{Op: "scan start"}
	}
	if end == nil {
		return nil, &NilFieldValueError{Op: "scan end"}
	}

	lower := p.buildValuePrefix(start)
	upper := p.buildValuePrefix(end)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	result := make(map[string][]uint64)
	for iter.First(); iter.Valid(); iter.Next() {
		fv, eid, parseErr := p.parseKey(iter.Key())
		if parseErr != nil {
			return nil, parseErr
		}
		key := string(fv)
		result[key] = append(result[key], eid)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	return result, nil
}

// GetAll retrieves all entity IDs in the index, deduplicated and sorted
// for deterministic output.
func (p *PebbleIndex) GetAll() ([]uint64, error) {
	if p.closed.Load() {
		return nil, &ClosedError{Name: p.name}
	}

	prefix := p.buildIndexPrefix()
	upper := incrementBytes(prefix)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	seen := make(map[uint64]struct{})
	for iter.First(); iter.Valid(); iter.Next() {
		eid, extractErr := extractEntityID(iter.Key())
		if extractErr != nil {
			return nil, extractErr
		}
		seen[eid] = struct{}{}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	result := make([]uint64, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})

	return result, nil
}

// Clear removes all entries from the index using a batch deletion over
// the entire index prefix range.
func (p *PebbleIndex) Clear() error {
	if p.closed.Load() {
		return &ClosedError{Name: p.name}
	}

	prefix := p.buildIndexPrefix()
	upper := incrementBytes(prefix)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()

	batch := p.db.NewBatch()
	defer func() { _ = batch.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		if err := batch.Delete(k, pebble.Sync); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	return batch.Commit(pebble.Sync)
}

// Close marks the index as closed. Subsequent operations return a
// ClosedError. PebbleIndex does not own the PebbleDB instance, so
// the database is not closed.
func (p *PebbleIndex) Close() error {
	p.closed.Store(true)
	return nil
}

// getEntityIDs scans all entity IDs for a specific field value using
// a prefix iterator.
func (p *PebbleIndex) getEntityIDs(fieldValue []byte) ([]uint64, error) {
	prefix := p.buildValuePrefix(fieldValue)
	upper := incrementBytes(prefix)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	var ids []uint64
	for iter.First(); iter.Valid(); iter.Next() {
		eid, extractErr := extractEntityID(iter.Key())
		if extractErr != nil {
			return nil, extractErr
		}
		ids = append(ids, eid)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	return ids, nil
}

// buildKey constructs the full composite index key:
// "idx:" + name + ":" + fieldValue + ":" + entityID(8 bytes big-endian).
func (p *PebbleIndex) buildKey(fieldValue []byte, entityID uint64) []byte {
	prefix := p.buildValuePrefix(fieldValue)
	key := make([]byte, len(prefix)+entityIDLen)
	copy(key, prefix)
	binary.BigEndian.PutUint64(key[len(prefix):], entityID)
	return key
}

// buildValuePrefix constructs the prefix for a specific field value:
// "idx:" + name + ":" + fieldValue + ":".
func (p *PebbleIndex) buildValuePrefix(fieldValue []byte) []byte {
	indexPrefix := p.buildIndexPrefix()
	prefix := make([]byte, len(indexPrefix)+len(fieldValue)+len(keySeparator))
	n := copy(prefix, indexPrefix)
	n += copy(prefix[n:], fieldValue)
	copy(prefix[n:], keySeparator)
	return prefix
}

// buildIndexPrefix constructs the prefix for the entire index:
// "idx:" + name + ":".
func (p *PebbleIndex) buildIndexPrefix() []byte {
	prefix := make([]byte, len(keyPrefix)+len(p.name)+len(keySeparator))
	n := copy(prefix, keyPrefix)
	n += copy(prefix[n:], p.name)
	copy(prefix[n:], keySeparator)
	return prefix
}

// extractEntityID extracts the uint64 entity ID from the last 8 bytes
// of a composite index key.
func extractEntityID(key []byte) (uint64, error) {
	if len(key) < entityIDLen {
		return 0, &KeyTooShortError{}
	}
	return binary.BigEndian.Uint64(key[len(key)-entityIDLen:]), nil
}

// parseKey parses a full composite index key into its field value and
// entity ID components. The key must begin with the index prefix and
// contain at least the separator and 8-byte entity ID suffix.
func (p *PebbleIndex) parseKey(key []byte) (fieldValue []byte, entityID uint64, err error) {
	prefix := p.buildIndexPrefix()
	if len(key) < len(prefix) {
		return nil, 0, &KeyPrefixMismatchError{}
	}

	for i := range prefix {
		if key[i] != prefix[i] {
			return nil, 0, &KeyPrefixMismatchError{}
		}
	}

	// After the index prefix, the remaining bytes are:
	// fieldValue + ":" + entityID(8 bytes)
	rest := key[len(prefix):]
	// Minimum: 1 byte separator + 8 bytes entityID = 9
	if len(rest) < len(keySeparator)+entityIDLen {
		return nil, 0, &KeyTooShortError{}
	}

	// The separator before the entity ID is the last ":" before the
	// trailing 8 bytes. Find it by computing the offset.
	sepIdx := len(rest) - entityIDLen - len(keySeparator)
	if sepIdx < 0 {
		return nil, 0, &KeyTooShortError{}
	}

	fieldValue = make([]byte, sepIdx)
	copy(fieldValue, rest[:sepIdx])

	entityID = binary.BigEndian.Uint64(rest[sepIdx+len(keySeparator):])
	return fieldValue, entityID, nil
}

// incrementBytes returns a copy of b with its value incremented by one
// for use as an exclusive upper bound in range scans. If all bytes are
// 0xFF, returns a slice with an appended 0x00 byte to ensure the upper
// bound remains valid.
func incrementBytes(b []byte) []byte {
	result := make([]byte, len(b))
	copy(result, b)
	for i := len(result) - 1; i >= 0; i-- {
		if result[i] < 0xFF {
			result[i]++
			return result[:i+1]
		}
	}
	// All bytes are 0xFF; append a zero byte to create a valid upper bound.
	return append(result, 0x00)
}

// Compile-time assertion.
var _ Index = (*PebbleIndex)(nil)
