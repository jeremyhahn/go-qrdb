//go:build !qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package hnsw

import (
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// Compile-time assertion that IndexAdapter implements interfaces.HNSWIndex.
var _ interfaces.HNSWIndex = (*IndexAdapter)(nil)

// IndexAdapter wraps a concrete HNSWIndex to implement the interfaces.HNSWIndex interface.
// This stub version returns errors for all operations when vector support is not compiled.
type IndexAdapter struct {
	index *HNSWIndex
}

// NewIndexAdapter creates a new IndexAdapter wrapping the given HNSWIndex.
func NewIndexAdapter(index *HNSWIndex) *IndexAdapter {
	return &IndexAdapter{index: index}
}

// Insert adds a new vector to the index with the given ID.
func (a *IndexAdapter) Insert(id uint64, vector []float32) error {
	return ErrNotCompiled
}

// Delete removes a vector from the index by ID.
func (a *IndexAdapter) Delete(id uint64) error {
	return ErrNotCompiled
}

// Search performs k-nearest neighbor search for the given query vector.
func (a *IndexAdapter) Search(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
	return nil, ErrNotCompiled
}

// Count returns the number of vectors currently in the index.
func (a *IndexAdapter) Count() int64 {
	return 0
}

// GetStats returns statistics about the index.
func (a *IndexAdapter) GetStats() *interfaces.HNSWStats {
	return nil
}

// Unwrap returns the underlying concrete HNSWIndex.
func (a *IndexAdapter) Unwrap() *HNSWIndex {
	return a.index
}

// IndexFactory implements interfaces.HNSWIndexFactory for creating HNSW indexes.
type IndexFactory struct{}

// NewIndexFactory creates a new IndexFactory.
func NewIndexFactory() *IndexFactory {
	return &IndexFactory{}
}

// CreateIndex creates a new HNSW index with the specified configuration.
func (f *IndexFactory) CreateIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	return nil, ErrNotCompiled
}
