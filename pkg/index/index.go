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
// key-value store. Indexes map field values to entity keys for efficient
// lookups without scanning all keys.
package index

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Index defines the interface for a field index.
// Implementations must be safe for concurrent use.
type Index interface {
	// Put adds or updates an index entry mapping a field value to an entity key.
	Put(entityKey []byte, fieldValue []byte) error

	// Get retrieves all entity keys associated with the given field value.
	// Returns empty slice if no entities found.
	Get(fieldValue []byte) ([][]byte, error)

	// Delete removes the index entry for the given entity key and field value.
	Delete(entityKey []byte, fieldValue []byte) error

	// Scan retrieves all index entries within the given field value range [start, end).
	// Returns a map of field values to entity key slices.
	Scan(start, end []byte) (map[string][][]byte, error)

	// GetAll retrieves all entity keys in the index (deduplicated).
	GetAll() ([][]byte, error)

	// Clear removes all entries from the index.
	Clear() error

	// Close releases resources held by the index.
	Close() error
}

// IndexType defines the type of index constraint.
type IndexType int

const (
	// IndexTypeNormal indicates a standard non-unique index.
	// Multiple entities can have the same field value.
	IndexTypeNormal IndexType = iota

	// IndexTypeUnique indicates a unique index.
	// Only one entity can have a given field value.
	IndexTypeUnique
)

// String returns the string representation of the index type.
func (t IndexType) String() string {
	switch t {
	case IndexTypeNormal:
		return "normal"
	case IndexTypeUnique:
		return "unique"
	default:
		return "unknown"
	}
}

// IndexWithContext extends Index with context support for cancellation and timeouts.
type IndexWithContext interface {
	Index

	// PutWithContext adds or updates an index entry with context support.
	PutWithContext(ctx context.Context, entityKey []byte, fieldValue []byte) error

	// GetWithContext retrieves entity keys with context support.
	GetWithContext(ctx context.Context, fieldValue []byte) ([][]byte, error)

	// DeleteWithContext removes an index entry with context support.
	DeleteWithContext(ctx context.Context, entityKey []byte, fieldValue []byte) error

	// ScanWithContext performs a range scan with context support.
	ScanWithContext(ctx context.Context, start, end []byte) (map[string][][]byte, error)
}

// BatchableIndex extends Index with batch-aware operations that write
// index mutations into an externally provided storage.Batch. This enables
// atomic commits of both data and index entries in a single batch.
type BatchableIndex interface {
	Index

	// PutBatch queues an index entry addition into the provided batch.
	// The caller is responsible for committing the batch.
	PutBatch(batch storage.Batch, entityKey []byte, fieldValue []byte) error

	// DeleteBatch queues an index entry removal into the provided batch.
	// The caller is responsible for committing the batch.
	DeleteBatch(batch storage.Batch, entityKey []byte, fieldValue []byte) error
}

// IndexMetadata contains information about an indexed field.
type IndexMetadata struct {
	// FieldName is the name of the indexed field.
	FieldName string

	// FieldType is the Go type of the field (e.g., "string", "int", "uint64").
	FieldType string

	// Config contains the index configuration.
	Config IndexConfig

	// Tag contains the original struct tag value.
	Tag string
}
