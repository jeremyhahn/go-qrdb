//go:build qrdb_fts

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

// Package fts provides full-text search for QRDB entities. It implements
// a custom inverted index on top of storage.Storage so that FTS data
// participates in Raft replication and is stored alongside entity data.
//
// Build with -tags qrdb_fts to include FTS support. Without the tag,
// a no-op stub is compiled instead.
package fts

import (
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// FTSIndex provides full-text search capabilities for a named entity field.
// It wraps an InvertedIndex with a higher-level API suitable for DAO integration.
type FTSIndex struct {
	index *InvertedIndex
}

// FTSOption configures an FTSIndex.
type FTSOption func(*ftsConfig)

type ftsConfig struct {
	analyzer *Analyzer
}

// WithAnalyzer sets a custom analyzer for the FTS index.
func WithAnalyzer(a *Analyzer) FTSOption {
	return func(c *ftsConfig) {
		c.analyzer = a
	}
}

// NewFTSIndex creates a full-text search index backed by the given storage.
// The name uniquely identifies this FTS index (e.g., "messages.Body").
func NewFTSIndex(store storage.Storage, name string, opts ...FTSOption) *FTSIndex {
	cfg := &ftsConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &FTSIndex{
		index: NewInvertedIndex(store, name, cfg.analyzer),
	}
}

// IndexDocument indexes a text document associated with an entity key.
// Any previous index entries for this entity are replaced.
func (f *FTSIndex) IndexDocument(entityKey []byte, text string) error {
	return f.index.Index(entityKey, text)
}

// RemoveDocument removes all index entries for an entity key.
func (f *FTSIndex) RemoveDocument(entityKey []byte) error {
	return f.index.Remove(entityKey)
}

// Search finds entity keys matching the query using AND semantics
// (all query terms must appear in the document).
func (f *FTSIndex) Search(query string) ([][]byte, error) {
	return f.index.Search(query)
}

// SearchOr finds entity keys matching the query using OR semantics
// (any query term may appear in the document).
func (f *FTSIndex) SearchOr(query string) ([][]byte, error) {
	return f.index.SearchOr(query)
}

// Available reports whether FTS is compiled into this build.
func Available() bool {
	return true
}
