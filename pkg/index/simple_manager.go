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
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// SimpleIndexManager is a lightweight index manager that works with JSON data.
// It supports selective indexing based on registered entity type schemas.
type SimpleIndexManager struct {
	// indexes maps field names to indexes
	indexes map[string]*ExtendedMemoryIndex
	// registeredFields tracks which fields should be indexed per entity type
	// map[entityType]map[fieldName]bool
	registeredFields map[string]map[string]bool
	mu               sync.RWMutex
}

// NewSimpleIndexManager creates a new simple index manager.
func NewSimpleIndexManager() *SimpleIndexManager {
	return &SimpleIndexManager{
		indexes:          make(map[string]*ExtendedMemoryIndex),
		registeredFields: make(map[string]map[string]bool),
	}
}

// RegisterEntityType registers an entity type and extracts index information from struct tags.
// Only fields with `index:"true"` or similar tags will be indexed.
func (mgr *SimpleIndexManager) RegisterEntityType(entityType string, modelType reflect.Type) error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	// Parse index tags from the struct
	metadata, err := ParseIndexTags(modelType)
	if err != nil {
		return errors.Wrapf(
			err,
			errors.ErrInvalidInput,
			"index.SimpleIndexManager.RegisterEntityType",
			"failed to parse index tags for type %s",
			entityType,
		)
	}

	// Store which fields should be indexed for this entity type
	fields := make(map[string]bool)
	for fieldName, meta := range metadata {
		fields[fieldName] = true

		// Create index if it doesn't exist
		if _, exists := mgr.indexes[fieldName]; !exists {
			idx, err := NewExtendedMemoryIndex(&meta.Config)
			if err != nil {
				return errors.Wrapf(
					err,
					errors.ErrInternal,
					"index.SimpleIndexManager.RegisterEntityType",
					"failed to create index for field %s",
					fieldName,
				)
			}
			mgr.indexes[fieldName] = idx
		}
	}

	mgr.registeredFields[entityType] = fields
	return nil
}

// IndexEntity extracts fields from JSON data and indexes them.
// entityType is used to determine which fields should be indexed based on registration.
// If entityType is not registered, falls back to indexing all fields (for backward compatibility).
func (mgr *SimpleIndexManager) IndexEntity(entityKey []byte, jsonData []byte) error {
	return mgr.IndexEntityWithType(entityKey, "", jsonData)
}

// IndexEntityWithType indexes an entity with explicit entity type for selective indexing.
func (mgr *SimpleIndexManager) IndexEntityWithType(entityKey []byte, entityType string, jsonData []byte) error {
	if jsonData == nil {
		return nil
	}

	// Parse JSON
	var data map[string]interface{}
	if err := json.Unmarshal(jsonData, &data); err != nil {
		// Not JSON or invalid, skip indexing
		return nil
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	// Check if we have registered fields for this entity type
	registeredFields, hasRegistration := mgr.registeredFields[entityType]

	// Index each field
	for fieldName, fieldValue := range data {
		// Skip if we have registration and this field is not registered
		if hasRegistration && !registeredFields[fieldName] {
			continue
		}

		// Get index (should already exist if registered, but handle legacy case)
		idx, exists := mgr.indexes[fieldName]
		if !exists {
			// Create index for unregistered field (backward compatibility)
			if !hasRegistration {
				config := &IndexConfig{
					Name: fieldName,
					Type: IndexTypeNormal,
				}
				var err error
				idx, err = NewExtendedMemoryIndex(config)
				if err != nil {
					return errors.Wrapf(
						err,
						errors.ErrInternal,
						"index.SimpleIndexManager.IndexEntityWithType",
						"failed to create index for field %s",
						fieldName,
					)
				}
				mgr.indexes[fieldName] = idx
			} else {
				// Field not registered, skip
				continue
			}
		}

		// Convert field value to bytes
		valueBytes, err := valueToBytes(fieldValue)
		if err != nil {
			// Skip fields we can't index
			continue
		}

		// Add to index
		if err := idx.Put(entityKey, valueBytes); err != nil {
			return errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.SimpleIndexManager.IndexEntityWithType",
				"failed to index field %s",
				fieldName,
			)
		}
	}

	return nil
}

// DeleteEntity removes all index entries for an entity.
func (mgr *SimpleIndexManager) DeleteEntity(entityKey []byte, jsonData []byte) error {
	if jsonData == nil {
		return nil
	}

	// Parse JSON
	var data map[string]interface{}
	if err := json.Unmarshal(jsonData, &data); err != nil {
		// Not JSON or invalid, skip
		return nil
	}

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	// Delete from each indexed field
	for fieldName, fieldValue := range data {
		idx, exists := mgr.indexes[fieldName]
		if !exists {
			continue
		}

		// Convert field value to bytes
		valueBytes, err := valueToBytes(fieldValue)
		if err != nil {
			continue
		}

		// Remove from index
		if err := idx.Delete(entityKey, valueBytes); err != nil {
			return errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.SimpleIndexManager.DeleteEntity",
				"failed to delete from index %s",
				fieldName,
			)
		}
	}

	return nil
}

// QueryIndex retrieves entity keys matching a field value.
func (mgr *SimpleIndexManager) QueryIndex(indexName string, value interface{}) ([][]byte, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	idx, exists := mgr.indexes[indexName]
	if !exists {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"index.SimpleIndexManager.QueryIndex",
			"index %s not found",
			indexName,
		)
	}

	// Convert value to bytes
	valueBytes, err := valueToBytes(value)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrInvalidInput,
			"index.SimpleIndexManager.QueryIndex",
			"failed to convert value to bytes",
		)
	}

	return idx.Get(valueBytes)
}

// ScanIndex performs a range scan on an index.
func (mgr *SimpleIndexManager) ScanIndex(indexName string, start, end interface{}) (map[string][][]byte, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	idx, exists := mgr.indexes[indexName]
	if !exists {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"index.SimpleIndexManager.ScanIndex",
			"index %s not found",
			indexName,
		)
	}

	// Convert start and end to bytes
	var startBytes, endBytes []byte
	var err error

	if start != nil {
		startBytes, err = valueToBytes(start)
		if err != nil {
			return nil, errors.Wrapf(
				err,
				errors.ErrInvalidInput,
				"index.SimpleIndexManager.ScanIndex",
				"failed to convert start value",
			)
		}
	}

	if end != nil {
		endBytes, err = valueToBytes(end)
		if err != nil {
			return nil, errors.Wrapf(
				err,
				errors.ErrInvalidInput,
				"index.SimpleIndexManager.ScanIndex",
				"failed to convert end value",
			)
		}
	}

	return idx.Scan(startBytes, endBytes)
}

// ListIndexes returns the names of all indexes.
func (mgr *SimpleIndexManager) ListIndexes() []string {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	names := make([]string, 0, len(mgr.indexes))
	for name := range mgr.indexes {
		names = append(names, name)
	}
	return names
}

// Clear removes all entries from all indexes.
func (mgr *SimpleIndexManager) Clear() error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	for _, idx := range mgr.indexes {
		if err := idx.Clear(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all indexes.
func (mgr *SimpleIndexManager) Close() error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	for _, idx := range mgr.indexes {
		if err := idx.Close(); err != nil {
			return err
		}
	}
	mgr.indexes = nil
	return nil
}

// valueToBytes converts a JSON value to bytes for indexing.
func valueToBytes(value interface{}) ([]byte, error) {
	switch v := value.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	case float64:
		// JSON numbers are float64
		return []byte(fmt.Sprintf("%v", v)), nil
	case int:
		return []byte(fmt.Sprintf("%d", v)), nil
	case int64:
		return []byte(fmt.Sprintf("%d", v)), nil
	case uint64:
		return []byte(fmt.Sprintf("%d", v)), nil
	case bool:
		if v {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case nil:
		return []byte("null"), nil
	default:
		// For complex types, use JSON encoding
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return jsonBytes, nil
	}
}
