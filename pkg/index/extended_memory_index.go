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
	"context"
	"sort"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// ExtendedMemoryIndex is an in-memory implementation of the Index and
// IndexWithContext interfaces that accepts an IndexConfig for configuration.
// It is thread-safe and suitable for testing, development, and lightweight
// index management via the SimpleIndexManager.
type ExtendedMemoryIndex struct {
	config *IndexConfig
	// data maps field values to lists of entity keys
	data map[string][][]byte
	mu   sync.RWMutex
}

// NewExtendedMemoryIndex creates a new in-memory index from an IndexConfig.
func NewExtendedMemoryIndex(config *IndexConfig) (*ExtendedMemoryIndex, error) {
	if config == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewExtendedMemoryIndex",
			"config is required",
		)
	}

	return &ExtendedMemoryIndex{
		config: config,
		data:   make(map[string][][]byte),
	}, nil
}

// Put adds or updates an index entry.
func (idx *ExtendedMemoryIndex) Put(entityKey []byte, fieldValue []byte) error {
	if fieldValue == nil {
		return errors.Newf(
			errors.ErrInvalidInput,
			"index.ExtendedMemoryIndex.Put",
			"field value cannot be nil",
		)
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	key := string(fieldValue)

	// Get existing entity keys for this value
	entityIDs, exists := idx.data[key]
	if !exists {
		// First entity with this value
		idx.data[key] = [][]byte{append([]byte(nil), entityKey...)}
		return nil
	}

	// Check if entity key already exists in the list
	for _, id := range entityIDs {
		if bytes.Equal(id, entityKey) {
			// Already indexed
			return nil
		}
	}

	// Add to list
	idx.data[key] = append(entityIDs, append([]byte(nil), entityKey...))
	return nil
}

// Get retrieves all entity keys for a field value.
func (idx *ExtendedMemoryIndex) Get(fieldValue []byte) ([][]byte, error) {
	if fieldValue == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.ExtendedMemoryIndex.Get",
			"field value cannot be nil",
		)
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	entityIDs, exists := idx.data[string(fieldValue)]
	if !exists {
		return [][]byte{}, nil
	}

	// Return a deep copy to avoid concurrent modification
	result := make([][]byte, len(entityIDs))
	for i, key := range entityIDs {
		result[i] = append([]byte(nil), key...)
	}
	return result, nil
}

// Delete removes an index entry.
func (idx *ExtendedMemoryIndex) Delete(entityKey []byte, fieldValue []byte) error {
	if fieldValue == nil {
		return errors.Newf(
			errors.ErrInvalidInput,
			"index.ExtendedMemoryIndex.Delete",
			"field value cannot be nil",
		)
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	key := string(fieldValue)
	entityIDs, exists := idx.data[key]
	if !exists {
		// Not found, nothing to do
		return nil
	}

	// Find and remove the entity key
	newIDs := make([][]byte, 0, len(entityIDs))
	for _, id := range entityIDs {
		if !bytes.Equal(id, entityKey) {
			newIDs = append(newIDs, id)
		}
	}

	if len(newIDs) == 0 {
		// Remove the key entirely if no more entities
		delete(idx.data, key)
	} else {
		idx.data[key] = newIDs
	}

	return nil
}

// Scan performs a range scan.
func (idx *ExtendedMemoryIndex) Scan(start, end []byte) (map[string][][]byte, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	result := make(map[string][][]byte)

	// Get all keys
	keys := make([]string, 0, len(idx.data))
	for k := range idx.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Filter by range
	for _, key := range keys {
		keyBytes := []byte(key)

		// Check start bound
		if start != nil && bytes.Compare(keyBytes, start) < 0 {
			continue
		}

		// Check end bound
		if end != nil && bytes.Compare(keyBytes, end) >= 0 {
			continue
		}

		// Deep copy entity keys
		entityIDs := idx.data[key]
		copied := make([][]byte, len(entityIDs))
		for i, k := range entityIDs {
			copied[i] = append([]byte(nil), k...)
		}
		result[key] = copied
	}

	return result, nil
}

// GetAll retrieves all entity keys in the index.
func (idx *ExtendedMemoryIndex) GetAll() ([][]byte, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// Use a map to deduplicate
	seen := make(map[string]struct{})
	for _, entityKeys := range idx.data {
		for _, key := range entityKeys {
			seen[string(key)] = struct{}{}
		}
	}

	// Convert to slice
	result := make([][]byte, 0, len(seen))
	for k := range seen {
		result = append(result, []byte(k))
	}

	// Sort for consistency
	sort.Slice(result, func(i, j int) bool {
		return bytes.Compare(result[i], result[j]) < 0
	})

	return result, nil
}

// Clear removes all entries from the index.
func (idx *ExtendedMemoryIndex) Clear() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.data = make(map[string][][]byte)
	return nil
}

// Close releases resources (no-op for memory index).
func (idx *ExtendedMemoryIndex) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.data = nil
	return nil
}

// PutWithContext adds an entry with context support.
func (idx *ExtendedMemoryIndex) PutWithContext(ctx context.Context, entityKey []byte, fieldValue []byte) error {
	// Check context
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return idx.Put(entityKey, fieldValue)
}

// GetWithContext retrieves entity keys with context support.
func (idx *ExtendedMemoryIndex) GetWithContext(ctx context.Context, fieldValue []byte) ([][]byte, error) {
	// Check context
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return idx.Get(fieldValue)
}

// DeleteWithContext removes an entry with context support.
func (idx *ExtendedMemoryIndex) DeleteWithContext(ctx context.Context, entityKey []byte, fieldValue []byte) error {
	// Check context
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return idx.Delete(entityKey, fieldValue)
}

// ScanWithContext performs a range scan with context support.
func (idx *ExtendedMemoryIndex) ScanWithContext(ctx context.Context, start, end []byte) (map[string][][]byte, error) {
	// Check context
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return idx.Scan(start, end)
}

// Compile-time assertions.
var (
	_ Index            = (*ExtendedMemoryIndex)(nil)
	_ IndexWithContext = (*ExtendedMemoryIndex)(nil)
)
