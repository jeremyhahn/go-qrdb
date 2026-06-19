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

package server

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// namespacedIndexService wraps an IndexServicer so that index names are
// transparently prefixed with the composite (tenant, db) namespace (isolating
// each database's index catalog) and stripped from listings. The entry keys
// returned by Query and Scan are the keys of the indexed documents, which are
// stored under the same namespace prefix when written through the namespaced
// KV/MVCC services, so those returned keys also have the prefix stripped.
type namespacedIndexService struct {
	inner  service.IndexServicer
	prefix string
}

// Compile-time interface check.
var _ service.IndexServicer = (*namespacedIndexService)(nil)

// newNamespacedIndexService creates an IndexServicer that prefixes all index
// names with the given composite namespace prefix.
func newNamespacedIndexService(inner service.IndexServicer, prefix string) *namespacedIndexService {
	return &namespacedIndexService{inner: inner, prefix: prefix}
}

// stripByteSlices removes the namespace prefix from every entry key.
func (s *namespacedIndexService) stripByteSlices(keys [][]byte) [][]byte {
	if keys == nil {
		return nil
	}
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = stripBytePrefix(k, s.prefix)
	}
	return out
}

// CreateIndex creates a secondary index under the prefixed name.
func (s *namespacedIndexService) CreateIndex(ctx context.Context, name, fieldName string, config index.IndexConfig) error {
	return s.inner.CreateIndex(ctx, s.prefix+name, fieldName, config)
}

// DropIndex removes a secondary index by prefixed name.
func (s *namespacedIndexService) DropIndex(ctx context.Context, name string) error {
	return s.inner.DropIndex(ctx, s.prefix+name)
}

// Query returns the entry keys matching value in the prefixed index, with the
// namespace prefix stripped from the returned keys.
func (s *namespacedIndexService) Query(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	keys, err := s.inner.Query(ctx, s.prefix+indexName, value)
	if err != nil {
		return nil, err
	}
	return s.stripByteSlices(keys), nil
}

// Scan returns entries in the prefixed index with values in [start, end). The
// namespace prefix is stripped from the returned entry keys.
func (s *namespacedIndexService) Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	result, err := s.inner.Scan(ctx, s.prefix+indexName, start, end)
	if err != nil {
		return nil, err
	}
	out := make(map[string][][]byte, len(result))
	for value, keys := range result {
		out[value] = s.stripByteSlices(keys)
	}
	return out, nil
}

// ListIndexes returns the names of all indexes within the namespace, with the
// namespace prefix stripped.
func (s *namespacedIndexService) ListIndexes(ctx context.Context) ([]string, error) {
	names, err := s.inner.ListIndexes(ctx)
	if err != nil {
		return nil, err
	}
	scoped := make([]string, 0, len(names))
	for _, n := range names {
		if len(n) >= len(s.prefix) && n[:len(s.prefix)] == s.prefix {
			scoped = append(scoped, n[len(s.prefix):])
		}
	}
	return scoped, nil
}
