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
	"sort"
	"sync"
)

// MemoryIndex is an in-memory implementation of the Index interface.
// It uses a map of field value strings to entity ID slices, protected
// by a read-write mutex for concurrent access.
type MemoryIndex struct {
	mu     sync.RWMutex
	name   string
	unique bool
	data   map[string][]uint64
}

// NewMemoryIndex creates a new in-memory index with the given name.
// When unique is true, each field value may map to at most one entity ID.
func NewMemoryIndex(name string, unique bool) *MemoryIndex {
	return &MemoryIndex{
		name:   name,
		unique: unique,
		data:   make(map[string][]uint64),
	}
}

// Name returns the index name.
func (m *MemoryIndex) Name() string {
	return m.name
}

// IsUnique returns true if the index enforces a unique constraint.
func (m *MemoryIndex) IsUnique() bool {
	return m.unique
}

// Put adds an index entry mapping fieldValue to entityID. If the index
// enforces uniqueness and a different entity already maps to the same
// field value, a UniqueViolationError is returned. Duplicate entityID
// entries for the same field value are silently ignored.
func (m *MemoryIndex) Put(entityID uint64, fieldValue []byte) error {
	if fieldValue == nil {
		return &NilFieldValueError{Op: "put"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return &ClosedError{Name: m.name}
	}

	key := string(fieldValue)
	ids := m.data[key]

	// Check unique constraint.
	if m.unique && len(ids) > 0 && ids[0] != entityID {
		return &UniqueViolationError{
			IndexName:  m.name,
			FieldValue: fieldValue,
			ExistingID: ids[0],
			NewID:      entityID,
		}
	}

	// Check for duplicate entityID.
	for _, id := range ids {
		if id == entityID {
			return nil
		}
	}

	m.data[key] = append(ids, entityID)
	return nil
}

// Get retrieves all entity IDs associated with the given field value.
// Returns an empty slice if no entities are found. The returned slice
// is a copy and may be freely modified by the caller.
func (m *MemoryIndex) Get(fieldValue []byte) ([]uint64, error) {
	if fieldValue == nil {
		return nil, &NilFieldValueError{Op: "get"}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.data == nil {
		return nil, &ClosedError{Name: m.name}
	}

	ids := m.data[string(fieldValue)]
	if len(ids) == 0 {
		return []uint64{}, nil
	}

	cp := make([]uint64, len(ids))
	copy(cp, ids)
	return cp, nil
}

// Delete removes the mapping from fieldValue to entityID. If the field
// value's entity list becomes empty, the map entry is removed entirely.
func (m *MemoryIndex) Delete(entityID uint64, fieldValue []byte) error {
	if fieldValue == nil {
		return &NilFieldValueError{Op: "delete"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return &ClosedError{Name: m.name}
	}

	key := string(fieldValue)
	ids := m.data[key]

	for i, id := range ids {
		if id == entityID {
			// Remove by swapping with last element and truncating.
			ids[i] = ids[len(ids)-1]
			ids = ids[:len(ids)-1]
			if len(ids) == 0 {
				delete(m.data, key)
			} else {
				m.data[key] = ids
			}
			return nil
		}
	}

	return nil
}

// Scan retrieves all index entries with field values in the half-open
// interval [start, end). Returns a map of field value strings to copies
// of their entity ID slices.
func (m *MemoryIndex) Scan(start, end []byte) (map[string][]uint64, error) {
	if start == nil {
		return nil, &NilFieldValueError{Op: "scan start"}
	}
	if end == nil {
		return nil, &NilFieldValueError{Op: "scan end"}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.data == nil {
		return nil, &ClosedError{Name: m.name}
	}

	startStr := string(start)
	endStr := string(end)

	// Collect and sort keys for deterministic output.
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make(map[string][]uint64)
	for _, k := range keys {
		if k >= startStr && k < endStr {
			ids := m.data[k]
			cp := make([]uint64, len(ids))
			copy(cp, ids)
			result[k] = cp
		}
	}

	return result, nil
}

// GetAll retrieves all entity IDs in the index, deduplicated and sorted
// for deterministic output.
func (m *MemoryIndex) GetAll() ([]uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.data == nil {
		return nil, &ClosedError{Name: m.name}
	}

	seen := make(map[uint64]struct{})
	for _, ids := range m.data {
		for _, id := range ids {
			seen[id] = struct{}{}
		}
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

// Clear removes all entries from the index by resetting the internal map.
func (m *MemoryIndex) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return &ClosedError{Name: m.name}
	}

	m.data = make(map[string][]uint64)
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
