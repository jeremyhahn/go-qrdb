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
	"bytes"
	"sort"
	"strings"
	"sync"
)

// MemoryIndex is an in-memory implementation of the Index interface.
// It uses a map of field value strings to entity key slices, protected
// by a read-write mutex for concurrent access.
type MemoryIndex struct {
	mu     sync.RWMutex
	name   string
	unique bool
	// caseInsensitive folds string field values before they are used as map
	// keys, on write, lookup, delete and scan bounds alike. Folding on only
	// some of those paths is worse than not folding at all: a lookup would
	// answer empty with a nil error over an index holding the match.
	caseInsensitive bool
	data            map[string][][]byte
}

// NewMemoryIndex creates a new case-sensitive in-memory index with the given
// name. When unique is true, each field value may map to at most one entity
// key.
func NewMemoryIndex(name string, unique bool) *MemoryIndex {
	return NewMemoryIndexWithConfig(name, IndexConfig{Unique: unique, CaseSensitive: true})
}

// NewMemoryIndexWithConfig creates a new in-memory index configured by config.
// Only Unique and CaseSensitive are meaningful here; a MemoryIndex holds raw
// string terms and has no numeric encoding.
//
// Note config.CaseSensitive is named for the DEFAULT, so its zero value folds
// case. Callers that mean "the ordinary index" must set it to true, which is
// what NewMemoryIndex does.
func NewMemoryIndexWithConfig(name string, config IndexConfig) *MemoryIndex {
	return &MemoryIndex{
		name:            name,
		unique:          config.Unique,
		caseInsensitive: !config.CaseSensitive,
		data:            make(map[string][][]byte),
	}
}

// foldKey renders a raw field value as the map key this index stores it
// under, folding case when the index is case-insensitive.
func (m *MemoryIndex) foldKey(fieldValue []byte) string {
	if m.caseInsensitive {
		return strings.ToLower(string(fieldValue))
	}
	return string(fieldValue)
}

// foldBound renders a Scan boundary the same way foldKey renders a stored
// term, so a bound and the terms it is compared against live in the same
// domain. Without this an upper bound of "M" would exclude every folded term
// on a case-insensitive index, since 'm' > 'M' in byte order.
func (m *MemoryIndex) foldBound(bound []byte) []byte {
	if bound == nil || !m.caseInsensitive {
		return bound
	}
	return []byte(strings.ToLower(string(bound)))
}

// Name returns the index name.
func (m *MemoryIndex) Name() string {
	return m.name
}

// IsUnique returns true if the index enforces a unique constraint.
func (m *MemoryIndex) IsUnique() bool {
	return m.unique
}

// Put adds an index entry mapping fieldValue to entityKey. If the index
// enforces uniqueness and a different entity already maps to the same
// field value, a UniqueViolationError is returned. Duplicate entityKey
// entries for the same field value are silently ignored.
func (m *MemoryIndex) Put(entityKey []byte, fieldValue []byte) error {
	if fieldValue == nil {
		return &NilFieldValueError{Op: "put"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return &ClosedError{Name: m.name}
	}

	key := m.foldKey(fieldValue)
	keys := m.data[key]

	// Check unique constraint.
	if m.unique && len(keys) > 0 && !bytes.Equal(keys[0], entityKey) {
		return &UniqueViolationError{
			IndexName:   m.name,
			FieldValue:  fieldValue,
			ExistingKey: keys[0],
			NewKey:      entityKey,
		}
	}

	// Check for duplicate entityKey.
	for _, k := range keys {
		if bytes.Equal(k, entityKey) {
			return nil
		}
	}

	// Store a copy of the entity key.
	keyCopy := make([]byte, len(entityKey))
	copy(keyCopy, entityKey)
	m.data[key] = append(keys, keyCopy)
	return nil
}

// Get retrieves all entity keys associated with the given field value.
// Returns an empty slice if no entities are found. The returned slices
// are copies and may be freely modified by the caller.
func (m *MemoryIndex) Get(fieldValue []byte) ([][]byte, error) {
	if fieldValue == nil {
		return nil, &NilFieldValueError{Op: "get"}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.data == nil {
		return nil, &ClosedError{Name: m.name}
	}

	keys := m.data[m.foldKey(fieldValue)]
	if len(keys) == 0 {
		return [][]byte{}, nil
	}

	cp := make([][]byte, len(keys))
	for i, k := range keys {
		c := make([]byte, len(k))
		copy(c, k)
		cp[i] = c
	}
	return cp, nil
}

// Delete removes the mapping from fieldValue to entityKey. If the field
// value's entity list becomes empty, the map entry is removed entirely.
func (m *MemoryIndex) Delete(entityKey []byte, fieldValue []byte) error {
	if fieldValue == nil {
		return &NilFieldValueError{Op: "delete"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return &ClosedError{Name: m.name}
	}

	key := m.foldKey(fieldValue)
	keys := m.data[key]

	for i, k := range keys {
		if bytes.Equal(k, entityKey) {
			// Remove by swapping with last element and truncating.
			keys[i] = keys[len(keys)-1]
			keys = keys[:len(keys)-1]
			if len(keys) == 0 {
				delete(m.data, key)
			} else {
				m.data[key] = keys
			}
			return nil
		}
	}

	return nil
}

// Scan retrieves all index entries with field values in the half-open
// interval [start, end). Returns a map of field value strings to copies
// of their entity key slices. A nil start or end means unbounded on that
// side, matching the semantics of the other Index implementations and the
// [start, end) contract documented on the Index interface.
func (m *MemoryIndex) Scan(start, end []byte) (map[string][][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.data == nil {
		return nil, &ClosedError{Name: m.name}
	}

	// Collect and sort keys for deterministic output.
	fieldKeys := make([]string, 0, len(m.data))
	for k := range m.data {
		fieldKeys = append(fieldKeys, k)
	}
	sort.Strings(fieldKeys)

	lower := m.foldBound(start)
	upper := m.foldBound(end)

	result := make(map[string][][]byte)
	for _, k := range fieldKeys {
		keyBytes := []byte(k)
		if lower != nil && bytes.Compare(keyBytes, lower) < 0 {
			continue
		}
		if upper != nil && bytes.Compare(keyBytes, upper) >= 0 {
			continue
		}

		keys := m.data[k]
		cp := make([][]byte, len(keys))
		for i, ek := range keys {
			c := make([]byte, len(ek))
			copy(c, ek)
			cp[i] = c
		}
		result[k] = cp
	}

	return result, nil
}

// GetAll retrieves all entity keys in the index, deduplicated and sorted
// for deterministic output.
func (m *MemoryIndex) GetAll() ([][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.data == nil {
		return nil, &ClosedError{Name: m.name}
	}

	seen := make(map[string][]byte)
	for _, keys := range m.data {
		for _, k := range keys {
			seen[string(k)] = k
		}
	}

	result := make([][]byte, 0, len(seen))
	for _, k := range seen {
		c := make([]byte, len(k))
		copy(c, k)
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		return bytes.Compare(result[i], result[j]) < 0
	})

	return result, nil
}

// Clear removes all entries from the index by resetting the internal map.
func (m *MemoryIndex) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return &ClosedError{Name: m.name}
	}

	m.data = make(map[string][][]byte)
	return nil
}

// Close releases resources held by the index. After Close is called,
// all subsequent operations return a ClosedError.
func (m *MemoryIndex) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data = nil
	return nil
}

// Compile-time assertion.
var _ Index = (*MemoryIndex)(nil)
