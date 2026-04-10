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

import "context"

// IndexService manages secondary indexes over the key-value store.
// Indexes enable efficient lookups by field values without scanning
// all keys.
type IndexServicer interface {
	// CreateIndex creates a new secondary index with the given name,
	// targeting the specified field. Returns IndexExistsError if an
	// index with the same name already exists.
	CreateIndex(ctx context.Context, name, fieldName string, config IndexConfig) error

	// DropIndex removes a secondary index by name. Returns
	// IndexNotFoundError if the index does not exist.
	DropIndex(ctx context.Context, name string) error

	// Query returns the keys of all entries matching the exact value
	// in the specified index. Returns IndexNotFoundError if the index
	// does not exist.
	Query(ctx context.Context, indexName string, value []byte) ([][]byte, error)

	// Scan returns entries in the specified index with values in the
	// half-open interval [start, end). The result maps index values
	// to their matching entry keys. Returns IndexNotFoundError if the
	// index does not exist.
	Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error)

	// ListIndexes returns the names of all registered indexes.
	ListIndexes(ctx context.Context) ([]string, error)
}
