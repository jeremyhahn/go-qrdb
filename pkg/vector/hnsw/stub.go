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

package hnsw

import (
	"errors"
	"io"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// Stub errors for when vector support is not compiled in.
var (
	ErrInvalidConfig       = errors.New("invalid HNSW configuration")
	ErrNotFound            = errors.New("node not found")
	ErrEmptyIndex          = errors.New("index is empty")
	ErrInvalidVector       = errors.New("invalid vector dimensions")
	ErrInvalidID           = errors.New("invalid node ID")
	ErrDuplicateID         = errors.New("duplicate node ID")
	ErrInternal            = errors.New("internal error")
	ErrNotCompiled   error = &NotCompiledError{}
)

// DistanceMetric represents a distance function type.
type DistanceMetric int

const (
	L2Distance DistanceMetric = iota
	CosineDistance
	DotProductDistance
	ManhattanDistance
	HammingDistance
)

// DistanceFunc is a function that computes the distance between two vectors.
type DistanceFunc func(a, b []float32) float32

// Config holds HNSW index configuration parameters.
type Config struct {
	M              int
	MMax           int
	EfConstruction int
	EfSearch       int
	Ml             float64
	DistanceMetric DistanceMetric
}

// DefaultConfig returns the default HNSW configuration.
func DefaultConfig() *Config {
	return &Config{
		M:              16,
		MMax:           32,
		EfConstruction: 200,
		EfSearch:       50,
		Ml:             0.36067,
		DistanceMetric: L2Distance,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	return nil
}

// HNSWNode represents a node in the HNSW graph.
type HNSWNode struct {
	ID          uint64
	Vector      []float32
	Layer       int
	Connections map[int][]uint64
}

// NewNode creates a new HNSW node.
func NewNode(id uint64, vector []float32, layer int) *HNSWNode {
	return &HNSWNode{
		ID:          id,
		Vector:      vector,
		Layer:       layer,
		Connections: make(map[int][]uint64),
	}
}

// AddConnection adds a bidirectional connection between this node and another.
func (n *HNSWNode) AddConnection(layer int, targetID uint64) {}

// GetConnections returns the connections for a specific layer.
func (n *HNSWNode) GetConnections(layer int) []uint64 {
	return nil
}

// RemoveConnection removes a connection at a specific layer.
func (n *HNSWNode) RemoveConnection(layer int, targetID uint64) {}

// SetConnections replaces all connections at a specific layer.
func (n *HNSWNode) SetConnections(layer int, connections []uint64) {}

// ConnectionCount returns the number of connections at a specific layer.
func (n *HNSWNode) ConnectionCount(layer int) int {
	return 0
}

// Copy creates a deep copy of the node.
func (n *HNSWNode) Copy() *HNSWNode {
	return nil
}

// SearchResult represents a search result with distance.
type SearchResult struct {
	ID       uint64
	Distance float32
}

// SerializedNode represents a node in serialized form.
type SerializedNode struct {
	ID          uint64
	Vector      []float32
	Layer       int
	Connections map[int][]uint64
}

// HNSWIndex represents a Hierarchical Navigable Small World index.
type HNSWIndex struct{}

// NewHNSWIndex creates a new HNSW index with the given configuration.
func NewHNSWIndex(config *Config) (*HNSWIndex, error) {
	return nil, ErrNotCompiled
}

// GetConfig returns a copy of the index configuration.
func (idx *HNSWIndex) GetConfig() *Config {
	return nil
}

// Insert adds a new vector to the index.
func (idx *HNSWIndex) Insert(id uint64, vector []float32) error {
	return ErrNotCompiled
}

// InsertAtLayer adds a new vector to the index at a caller-supplied layer.
func (idx *HNSWIndex) InsertAtLayer(id uint64, vector []float32, layer int) error {
	return ErrNotCompiled
}

// Search performs k-NN search for the given query vector.
func (idx *HNSWIndex) Search(query []float32, k int) ([]*SearchResult, error) {
	return nil, ErrNotCompiled
}

// Get retrieves a node by ID.
func (idx *HNSWIndex) Get(id uint64) (*HNSWNode, error) {
	return nil, ErrNotCompiled
}

// Delete removes a node from the index.
func (idx *HNSWIndex) Delete(id uint64) error {
	return ErrNotCompiled
}

// Count returns the number of nodes in the index.
func (idx *HNSWIndex) Count() int64 {
	return 0
}

// Stats holds statistics about the HNSW index.
type Stats struct {
	NodeCount      int64
	MaxLayer       int
	AvgConnections float64
	Dimensions     int
}

// GetStats returns statistics about the index.
func (idx *HNSWIndex) GetStats() *Stats {
	return &Stats{}
}

// Save persists the index to PebbleDB.
func (idx *HNSWIndex) Save(db *pebble.DB) error {
	return ErrNotCompiled
}

// Load restores the index from PebbleDB.
func (idx *HNSWIndex) Load(db *pebble.DB) error {
	return ErrNotCompiled
}

// Snapshot creates a snapshot of the index.
func (idx *HNSWIndex) Snapshot(w io.Writer) error {
	return ErrNotCompiled
}

// Restore restores the index from a snapshot.
func (idx *HNSWIndex) Restore(r io.Reader) error {
	return ErrNotCompiled
}

// GetDistanceFunc returns the distance function for the given metric.
func GetDistanceFunc(metric DistanceMetric) DistanceFunc {
	return nil
}

// L2 computes the Euclidean distance between two vectors.
func L2(a, b []float32) float32 {
	return 0
}

// Cosine computes the cosine distance between two vectors.
func Cosine(a, b []float32) float32 {
	return 0
}

// DotProduct computes the dot product between two vectors.
func DotProduct(a, b []float32) float32 {
	return 0
}

// L2SIMD is an optimized version of L2 using SIMD-friendly patterns.
func L2SIMD(a, b []float32) float32 {
	return 0
}

// CosineSIMD is an optimized version of Cosine using SIMD-friendly patterns.
func CosineSIMD(a, b []float32) float32 {
	return 0
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
