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
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// IndexManager coordinates multiple indexes for entity types.
// It automatically creates and maintains indexes based on struct tags.
type IndexManager struct {
	storage  storage.Storage
	logger   *logger.Logger
	indexes  map[string]Index // map[fieldName]Index
	metadata map[string]*IndexMetadata
	mu       sync.RWMutex
	closed   bool
}

// NewIndexManager creates a new index manager.
func NewIndexManager(storage storage.Storage, log *logger.Logger) (*IndexManager, error) {
	if storage == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewIndexManager",
			"storage is required",
		)
	}

	if log == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewIndexManager",
			"logger is required",
		)
	}

	mgr := &IndexManager{
		storage:  storage,
		logger:   log.WithField("component", "index-manager"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	mgr.logger.Info("Index manager initialized")

	return mgr, nil
}

// RegisterIndexes parses struct tags and creates indexes for the entity type.
// This should be called once for each entity type that uses indexes.
func (mgr *IndexManager) RegisterIndexes(entityType reflect.Type) error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if mgr.closed {
		return errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.RegisterIndexes",
			"manager is closed",
		)
	}

	// Parse index tags
	metadata, err := ParseIndexTags(entityType)
	if err != nil {
		return errors.Wrapf(
			err,
			errors.ErrInvalidInput,
			"index.IndexManager.RegisterIndexes",
			"failed to parse index tags",
		)
	}

	// Create indexes
	for fieldName, meta := range metadata {
		// Check if index already exists
		if _, exists := mgr.indexes[fieldName]; exists {
			mgr.logger.Warnf("Index for field %s already exists, skipping", fieldName)
			continue
		}

		// Create new index
		idx, err := NewStoragePebbleIndex(mgr.storage, meta.Config, mgr.logger)
		if err != nil {
			return errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.IndexManager.RegisterIndexes",
				"failed to create index for field %s",
				fieldName,
			)
		}

		mgr.indexes[fieldName] = idx
		mgr.metadata[fieldName] = meta

		mgr.logger.Infof("Registered %s index: %s (type: %s)",
			meta.Config.Type, fieldName, meta.FieldType)
	}

	return nil
}

// UpdateIndexes updates indexes when an entity is created or modified.
// It handles both inserts and updates by comparing old and new values.
func (mgr *IndexManager) UpdateIndexes(oldEntity, newEntity interface{}, entityKey []byte) error {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.UpdateIndexes",
			"manager is closed",
		)
	}

	// Use batch for atomic index updates
	batch := mgr.storage.Batch()
	defer func() { _ = batch.Close() }()

	var errs []error

	for fieldName, idx := range mgr.indexes {
		// Get old value if entity exists
		var oldValue []byte
		if oldEntity != nil {
			val, err := GetFieldValue(oldEntity, fieldName)
			if err != nil {
				errs = append(errs, errors.Wrapf(
					err,
					errors.ErrInternal,
					"index.IndexManager.UpdateIndexes",
					"failed to get old value for field %s",
					fieldName,
				))
				continue
			}
			oldValue = val
		}

		// Get new value
		newValue, err := GetFieldValue(newEntity, fieldName)
		if err != nil {
			errs = append(errs, errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.IndexManager.UpdateIndexes",
				"failed to get new value for field %s",
				fieldName,
			))
			continue
		}

		// If value changed, update the index
		if !bytesEqual(oldValue, newValue) {
			// Delete old index entry if it exists
			if oldValue != nil {
				if err := idx.Delete(entityKey, oldValue); err != nil {
					errs = append(errs, errors.Wrapf(
						err,
						errors.ErrInternal,
						"index.IndexManager.UpdateIndexes",
						"failed to delete old index entry for field %s",
						fieldName,
					))
					continue
				}
			}

			// Add new index entry
			if err := idx.Put(entityKey, newValue); err != nil {
				errs = append(errs, errors.Wrapf(
					err,
					errors.ErrInternal,
					"index.IndexManager.UpdateIndexes",
					"failed to add index entry for field %s",
					fieldName,
				))
				continue
			}
		}
	}

	if len(errs) > 0 {
		// Return first error
		return errs[0]
	}

	return nil
}

// DeleteIndexes removes all index entries for an entity.
func (mgr *IndexManager) DeleteIndexes(entity interface{}, entityKey []byte) error {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.DeleteIndexes",
			"manager is closed",
		)
	}

	var errs []error

	for fieldName, idx := range mgr.indexes {
		// Get field value
		fieldValue, err := GetFieldValue(entity, fieldName)
		if err != nil {
			errs = append(errs, errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.IndexManager.DeleteIndexes",
				"failed to get value for field %s",
				fieldName,
			))
			continue
		}

		// Delete from index
		if err := idx.Delete(entityKey, fieldValue); err != nil {
			errs = append(errs, errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.IndexManager.DeleteIndexes",
				"failed to delete from index %s",
				fieldName,
			))
			continue
		}
	}

	if len(errs) > 0 {
		// Return first error
		return errs[0]
	}

	return nil
}

// QueryIndex retrieves entity keys that match the given field value.
func (mgr *IndexManager) QueryIndex(indexName string, value interface{}) ([][]byte, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return nil, errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.QueryIndex",
			"manager is closed",
		)
	}

	idx, exists := mgr.indexes[indexName]
	if !exists {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"index.IndexManager.QueryIndex",
			"index %s not found",
			indexName,
		)
	}

	// Convert value to bytes
	valueBytes, err := interfaceToBytes(value)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrInvalidInput,
			"index.IndexManager.QueryIndex",
			"failed to convert value to bytes",
		)
	}

	// Query the index
	return idx.Get(valueBytes)
}

// QueryIndexWithContext retrieves entity keys with context support.
func (mgr *IndexManager) QueryIndexWithContext(ctx context.Context, indexName string, value interface{}) ([][]byte, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return nil, errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.QueryIndexWithContext",
			"manager is closed",
		)
	}

	idx, exists := mgr.indexes[indexName]
	if !exists {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"index.IndexManager.QueryIndexWithContext",
			"index %s not found",
			indexName,
		)
	}

	// Check if index supports context
	idxWithCtx, ok := idx.(IndexWithContext)
	if !ok {
		// Fallback to regular query
		return mgr.QueryIndex(indexName, value)
	}

	// Convert value to bytes
	valueBytes, err := interfaceToBytes(value)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrInvalidInput,
			"index.IndexManager.QueryIndexWithContext",
			"failed to convert value to bytes",
		)
	}

	return idxWithCtx.GetWithContext(ctx, valueBytes)
}

// ScanIndex performs a range scan on an index.
func (mgr *IndexManager) ScanIndex(indexName string, start, end interface{}) (map[string][][]byte, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return nil, errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.ScanIndex",
			"manager is closed",
		)
	}

	idx, exists := mgr.indexes[indexName]
	if !exists {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"index.IndexManager.ScanIndex",
			"index %s not found",
			indexName,
		)
	}

	// Convert start and end to bytes
	var startBytes, endBytes []byte
	var err error

	if start != nil {
		startBytes, err = interfaceToBytes(start)
		if err != nil {
			return nil, errors.Wrapf(
				err,
				errors.ErrInvalidInput,
				"index.IndexManager.ScanIndex",
				"failed to convert start value to bytes",
			)
		}
	}

	if end != nil {
		endBytes, err = interfaceToBytes(end)
		if err != nil {
			return nil, errors.Wrapf(
				err,
				errors.ErrInvalidInput,
				"index.IndexManager.ScanIndex",
				"failed to convert end value to bytes",
			)
		}
	}

	return idx.Scan(startBytes, endBytes)
}

// GetIndex returns an index by name.
func (mgr *IndexManager) GetIndex(name string) (Index, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return nil, errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.GetIndex",
			"manager is closed",
		)
	}

	idx, exists := mgr.indexes[name]
	if !exists {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"index.IndexManager.GetIndex",
			"index %s not found",
			name,
		)
	}

	return idx, nil
}

// ListIndexes returns names of all registered indexes.
func (mgr *IndexManager) ListIndexes() []string {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	names := make([]string, 0, len(mgr.indexes))
	for name := range mgr.indexes {
		names = append(names, name)
	}

	return names
}

// GetMetadata returns metadata for an index.
func (mgr *IndexManager) GetMetadata(name string) (*IndexMetadata, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return nil, errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.GetMetadata",
			"manager is closed",
		)
	}

	meta, exists := mgr.metadata[name]
	if !exists {
		return nil, errors.Newf(
			errors.ErrNotFound,
			"index.IndexManager.GetMetadata",
			"metadata for index %s not found",
			name,
		)
	}

	return meta, nil
}

// ClearIndex removes all entries from a specific index.
func (mgr *IndexManager) ClearIndex(name string) error {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.ClearIndex",
			"manager is closed",
		)
	}

	idx, exists := mgr.indexes[name]
	if !exists {
		return errors.Newf(
			errors.ErrNotFound,
			"index.IndexManager.ClearIndex",
			"index %s not found",
			name,
		)
	}

	return idx.Clear()
}

// ClearAllIndexes removes all entries from all indexes.
func (mgr *IndexManager) ClearAllIndexes() error {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.closed {
		return errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.ClearAllIndexes",
			"manager is closed",
		)
	}

	var errs []error

	for name, idx := range mgr.indexes {
		if err := idx.Clear(); err != nil {
			errs = append(errs, errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.IndexManager.ClearAllIndexes",
				"failed to clear index %s",
				name,
			))
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}

	return nil
}

// Close closes all indexes and releases resources.
func (mgr *IndexManager) Close() error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if mgr.closed {
		return nil
	}

	mgr.logger.Info("Closing index manager")

	var errs []error

	for name, idx := range mgr.indexes {
		if err := idx.Close(); err != nil {
			errs = append(errs, errors.Wrapf(
				err,
				errors.ErrInternal,
				"index.IndexManager.Close",
				"failed to close index %s",
				name,
			))
		}
	}

	mgr.closed = true
	mgr.logger.Info("Index manager closed")

	if len(errs) > 0 {
		return errs[0]
	}

	return nil
}

// interfaceToBytes converts an interface{} value to bytes for indexing.
func interfaceToBytes(value interface{}) ([]byte, error) {
	if value == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.interfaceToBytes",
			"value cannot be nil",
		)
	}

	// Handle common types
	switch v := value.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	case uint64:
		return encodeUint64(v), nil
	case uint32:
		return encodeUint64(uint64(v)), nil
	case uint:
		return encodeUint64(uint64(v)), nil
	case int64:
		return encodeInt64(v), nil
	case int32:
		return encodeInt64(int64(v)), nil
	case int:
		return encodeInt64(int64(v)), nil
	case bool:
		if v {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case float64:
		return encodeFloat64(v), nil
	case float32:
		return encodeFloat64(float64(v)), nil
	default:
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.interfaceToBytes",
			"unsupported type: %T",
			value,
		)
	}
}

// bytesEqual compares two byte slices.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RebuildIndex rebuilds a specific index by clearing and repopulating it.
// The populate function should be provided by the caller to load all entities.
func (mgr *IndexManager) RebuildIndex(name string, populate func(idx Index) error) error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if mgr.closed {
		return errors.Newf(
			errors.ErrStorage,
			"index.IndexManager.RebuildIndex",
			"manager is closed",
		)
	}

	idx, exists := mgr.indexes[name]
	if !exists {
		return errors.Newf(
			errors.ErrNotFound,
			"index.IndexManager.RebuildIndex",
			"index %s not found",
			name,
		)
	}

	mgr.logger.Infof("Rebuilding index: %s", name)

	// Clear existing entries
	if err := idx.Clear(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrInternal,
			"index.IndexManager.RebuildIndex",
			"failed to clear index %s",
			name,
		)
	}

	// Repopulate
	if err := populate(idx); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrInternal,
			"index.IndexManager.RebuildIndex",
			"failed to populate index %s",
			name,
		)
	}

	mgr.logger.Infof("Successfully rebuilt index: %s", name)

	return nil
}

// Stats returns statistics about the indexes.
func (mgr *IndexManager) Stats() map[string]interface{} {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	stats := make(map[string]interface{})
	stats["total_indexes"] = len(mgr.indexes)
	stats["closed"] = mgr.closed

	indexList := make([]map[string]interface{}, 0, len(mgr.indexes))
	for name, meta := range mgr.metadata {
		indexInfo := map[string]interface{}{
			"name":           name,
			"type":           meta.Config.Type.String(),
			"field_type":     meta.FieldType,
			"case_sensitive": meta.Config.CaseSensitive,
		}
		indexList = append(indexList, indexInfo)
	}
	stats["indexes"] = indexList

	return stats
}

// String returns a string representation of the manager.
func (mgr *IndexManager) String() string {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	return fmt.Sprintf("IndexManager{indexes: %d, closed: %v}", len(mgr.indexes), mgr.closed)
}
