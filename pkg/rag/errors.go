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

package rag

import "strconv"

// DocumentTooLargeError is returned when a document exceeds the configured
// maximum document size.
type DocumentTooLargeError struct {
	Size    int64
	MaxSize int64
}

// Error returns a human-readable description of the size violation.
func (e *DocumentTooLargeError) Error() string {
	return "rag: document size " + strconv.FormatInt(e.Size, 10) +
		" exceeds maximum " + strconv.FormatInt(e.MaxSize, 10)
}

// ChunkingError wraps an error that occurred during text chunking.
type ChunkingError struct {
	Cause error
}

// Error returns a human-readable description of the chunking failure.
func (e *ChunkingError) Error() string {
	return "rag: chunking failed: " + e.Cause.Error()
}

// Unwrap returns the underlying error.
func (e *ChunkingError) Unwrap() error {
	return e.Cause
}

// EmbeddingError wraps an error that occurred during embedding generation.
type EmbeddingError struct {
	Cause error
}

// Error returns a human-readable description of the embedding failure.
func (e *EmbeddingError) Error() string {
	return "rag: embedding failed: " + e.Cause.Error()
}

// Unwrap returns the underlying error.
func (e *EmbeddingError) Unwrap() error {
	return e.Cause
}

// RetrievalError wraps an error that occurred during vector search.
type RetrievalError struct {
	Cause error
}

// Error returns a human-readable description of the retrieval failure.
func (e *RetrievalError) Error() string {
	return "rag: retrieval failed: " + e.Cause.Error()
}

// Unwrap returns the underlying error.
func (e *RetrievalError) Unwrap() error {
	return e.Cause
}

// DocumentNotFoundError is returned when a document ID is not found in the
// given namespace.
type DocumentNotFoundError struct {
	ID        string
	Namespace string
}

// Error returns a human-readable description of the missing document.
func (e *DocumentNotFoundError) Error() string {
	return "rag: document not found: " + e.Namespace + "/" + e.ID
}

// EmptyQueryError is returned when a retrieve request contains an empty query
// string.
type EmptyQueryError struct{}

// Error returns a human-readable description.
func (e *EmptyQueryError) Error() string {
	return "rag: query must not be empty"
}

// InvalidNamespaceError is returned when a namespace string is empty or
// otherwise invalid.
type InvalidNamespaceError struct {
	Namespace string
}

// Error returns a human-readable description.
func (e *InvalidNamespaceError) Error() string {
	return "rag: invalid namespace: " + e.Namespace
}
