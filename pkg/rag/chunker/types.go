//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package chunker provides text segmentation strategies for the RAG pipeline.
// Documents are split into overlapping chunks suitable for embedding and
// similarity search.
package chunker

// Chunk represents a segment of text extracted from a document.
type Chunk struct {
	// Text is the chunk content.
	Text string

	// Index is the zero-based chunk ordinal within the document.
	Index int

	// Offset is the byte offset of this chunk in the original text.
	Offset int

	// Length is the byte length of this chunk.
	Length int

	// Metadata holds optional key-value pairs for this chunk.
	Metadata map[string]string
}

// Config configures the chunking behavior.
type Config struct {
	// Strategy selects the chunking algorithm: "fixed", "sentence", "paragraph".
	Strategy string

	// ChunkSize is the target chunk size in characters.
	ChunkSize int

	// Overlap is the number of overlapping characters between adjacent chunks.
	Overlap int

	// MinSize is the minimum chunk size; chunks smaller than this are merged
	// with the previous chunk.
	MinSize int
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Strategy:  "fixed",
		ChunkSize: 512,
		Overlap:   50,
		MinSize:   100,
	}
}
