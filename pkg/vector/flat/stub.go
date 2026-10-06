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

//go:build !qrdb_vector

// Package flat provides a brute-force vector index. This stub version is
// compiled when vector support is disabled and returns errors for all
// operations.
package flat

import (
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// Stub errors for when vector support is not compiled in.
var (
	ErrEmptyIndex          = errors.New("flat index is empty")
	ErrInvalidVector       = errors.New("invalid vector dimensions")
	ErrNotFound            = errors.New("vector not found")
	ErrDuplicateID         = errors.New("duplicate vector ID")
	ErrInvalidK            = errors.New("k must be greater than zero")
	ErrNotCompiled   error = &NotCompiledError{}
)

// DimensionMismatchError indicates a vector has the wrong number of dimensions.
type DimensionMismatchError struct {
	// Got is the actual number of dimensions.
	Got int

	// Expected is the expected number of dimensions.
	Expected int
}

// Error implements the error interface.
func (e *DimensionMismatchError) Error() string {
	return "dimension mismatch: got " + strconv.Itoa(e.Got) + ", expected " + strconv.Itoa(e.Expected)
}

// Unwrap returns the sentinel ErrInvalidVector error.
func (e *DimensionMismatchError) Unwrap() error {
	return ErrInvalidVector
}

// SearchResult represents a single search result from the flat index.
type SearchResult struct {
	// ID is the unique identifier of the matched vector.
	ID uint64

	// Distance is the computed distance between the query and this result.
	Distance float32
}

// Index is a brute-force vector index.
type Index struct{}

// NewIndex creates a new flat brute-force index with the given distance metric.
func NewIndex(distMetric hnsw.DistanceMetric) *Index {
	return &Index{}
}

// Insert adds a vector with the given ID to the index.
func (idx *Index) Insert(id uint64, vector []float32) error {
	return ErrNotCompiled
}

// Delete removes a vector from the index by ID.
func (idx *Index) Delete(id uint64) error {
	return ErrNotCompiled
}

// Search performs k-nearest neighbor search.
func (idx *Index) Search(query []float32, k int) ([]SearchResult, error) {
	return nil, ErrNotCompiled
}

// Marshal serializes the index to bytes.
func (idx *Index) Marshal() ([]byte, error) {
	return nil, ErrNotCompiled
}

// Unmarshal restores the index from bytes produced by Marshal.
func (idx *Index) Unmarshal(data []byte) error {
	return ErrNotCompiled
}

// Count returns the number of vectors currently in the index.
func (idx *Index) Count() int64 {
	return 0
}

// Dimensions returns the dimensionality of vectors in the index.
func (idx *Index) Dimensions() int {
	return 0
}

// Close releases index resources.
func (idx *Index) Close() error {
	return nil
}

// NotCompiledError reports that vector support was not built into this
// binary. It is a TYPE, not a bare errors.New sentinel, because the Raft Apply
// classifier reads the protocol.CapabilityError declaration below to tell a
// refusal from a fault: an untyped sentinel takes the fatal branch, halting
// the state machine that received it and costing the shard every subsequent
// linearizable read (go-qrdb#520).
type NotCompiledError struct{}

// Error returns the refusal, naming the build tag that enables the subsystem.
func (e *NotCompiledError) Error() string {
	return "vector support not compiled: build with -tags qrdb_vector"
}

// Capability names the absent subsystem, declaring this an application-level
// refusal rather than a fault. See protocol.CapabilityError.
func (e *NotCompiledError) Capability() string {
	return "vector"
}

// NotCompiledError is an absent capability, not a fault.
var _ protocol.CapabilityError = (*NotCompiledError)(nil)
