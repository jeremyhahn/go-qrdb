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

	// DropIndex removes a secondary index by name, WHOEVER owns it. Returns
	// IndexNotFoundError if the index does not exist. It is the drop a
	// whole-scope teardown issues (DROP DATABASE, tenant deletion), which owns
	// everything in its scope; no transport reaches it. The explicit index
	// API drops with DropOwnedIndex(name, ExplicitOwner), which refuses an
	// index a collection owns.
	DropIndex(ctx context.Context, name string) error

	// DropOwnedIndex removes a secondary index by name only when the owner
	// recorded on it is owner (see IndexConfig.Owner). An index recorded under
	// a different owner is left in place and the call returns an error
	// satisfying errors.As(*OwnershipError); an absent index is success, as
	// for DropIndex. It is the drop an object's teardown issues for the
	// indexes it created, so it cannot remove an index somebody else holds
	// under the same name (go-qrdb#426).
	DropOwnedIndex(ctx context.Context, name, owner string) error

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
