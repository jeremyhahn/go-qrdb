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

// Package index provides secondary indexing capabilities for the QRDB
// key-value store. Indexes map field values to entity IDs for efficient
// lookups without scanning all keys.
package index

// Index defines the interface for a field index.
// Implementations must be safe for concurrent use.
type Index interface {
	// Put adds or updates an index entry mapping a field value to an entity ID.
	Put(entityID uint64, fieldValue []byte) error

	// Get retrieves all entity IDs associated with the given field value.
	// Returns empty slice if no entities found.
	Get(fieldValue []byte) ([]uint64, error)

	// Delete removes the index entry for the given entity ID and field value.
	Delete(entityID uint64, fieldValue []byte) error

	// Scan retrieves all index entries within the given field value range [start, end).
	// Returns a map of field values to entity ID slices.
	Scan(start, end []byte) (map[string][]uint64, error)

	// GetAll retrieves all entity IDs in the index (deduplicated).
	GetAll() ([]uint64, error)

	// Clear removes all entries from the index.
	Clear() error

	// Close releases resources held by the index.
	Close() error
}
