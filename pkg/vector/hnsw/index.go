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

package hnsw

import (
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
)

// HNSWIndex represents a Hierarchical Navigable Small World index.
type HNSWIndex struct {
	config       *Config
	nodes        sync.Map // map[uint64]*HNSWNode
	entryPoint   atomic.Uint64
	maxLayer     atomic.Int32
	distanceFunc DistanceFunc
	nodeCount    atomic.Int64
	dimensions   atomic.Int32
	rng          *rand.Rand
	rngMu        sync.Mutex
}

// NewHNSWIndex creates a new HNSW index with the given configuration.
func NewHNSWIndex(config *Config) (*HNSWIndex, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	idx := &HNSWIndex{
		config:       config,
		distanceFunc: GetDistanceFunc(config.DistanceMetric),
		// #nosec G404 -- and //nolint:gosec for the golangci-lint leg; see the
		// matching note in pkg/vector/ivf/kmeans.go.
		//
		// ON THE MERITS: this rng draws which layers a node joins in the HNSW
		// graph. Nothing secret depends on it and it produces no key or
		// identifier. The seed is FIXED at 42 so an index built twice from the
		// same vectors has the same shape, which is what makes the recall
		// numbers in test/performance reproducible.
		//
		// The one residual, stated rather than left for a reader to notice: a
		// fixed seed makes layer assignment predictable, so an adversary who
		// controls insertion order could in principle craft a worst-case graph.
		// That is an availability concern about HNSW's structure, not the
		// confidentiality concern G404 exists to flag, and it is not made worse
		// by math/rand -- a CSPRNG with a fixed seed is equally predictable.
		rng: rand.New(rand.NewSource(42)), //nolint:gosec // G404: see the note above
	}

	// Initialize maxLayer to -1 for empty index
	idx.maxLayer.Store(-1)

	return idx, nil
}

// GetConfig returns a copy of the index configuration.
func (idx *HNSWIndex) GetConfig() *Config {
	if idx.config == nil {
		return DefaultConfig()
	}
	// Return a copy to prevent external modification
	configCopy := *idx.config
	return &configCopy
}

// Insert adds a new vector to the index, selecting its layer from the
// index's internal random source. See InsertAtLayer for a deterministic
// alternative.
func (idx *HNSWIndex) Insert(id uint64, vector []float32) error {
	return idx.insertAtLayer(id, vector, idx.selectLayer())
}

// InsertAtLayer adds a new vector to the index at a caller-supplied layer,
// instead of drawing one from the index's internal random source. It exists
// so a replicated namespace can place a node deterministically -- the same
// (namespace, id) pair always computes to the same layer on every replica
// (see vector.DeterministicLayer) -- without disturbing Insert's existing
// behaviour for every other caller, which still draws from idx.rng.
func (idx *HNSWIndex) InsertAtLayer(id uint64, vector []float32, layer int) error {
	return idx.insertAtLayer(id, vector, layer)
}

// insertAtLayer is Insert and InsertAtLayer's shared body.
func (idx *HNSWIndex) insertAtLayer(id uint64, vector []float32, layer int) error {
	if id == 0 {
		return ErrInvalidID
	}

	// Set dimensions on first insert using atomic compare-and-swap
	if idx.dimensions.Load() == 0 {
		idx.dimensions.CompareAndSwap(0, int32(len(vector))) //nolint:gosec // G115: Safe conversion - vector length bounded
	}

	// Validate vector dimensions
	if len(vector) != int(idx.dimensions.Load()) {
		return ErrInvalidVector
	}

	// Check for duplicate ID
	if _, exists := idx.nodes.Load(id); exists {
		return ErrDuplicateID
	}

	// Create new node
	node := NewNode(id, vector, layer)

	// If this is the first node, make it the entry point
	if idx.nodeCount.Load() == 0 {
		idx.nodes.Store(id, node)
		idx.entryPoint.Store(id)
		idx.maxLayer.Store(int32(layer)) //nolint:gosec // G115: Safe conversion - layer from selectLayer() with bounded range
		idx.nodeCount.Add(1)
		return nil
	}

	// Insert the node into the index
	idx.insert(node)
	idx.nodes.Store(id, node)
	idx.nodeCount.Add(1)

	// Update max layer if necessary
	currentMaxLayer := idx.maxLayer.Load()
	if int32(layer) > currentMaxLayer { //nolint:gosec // G115: Safe conversion - layer from selectLayer() with bounded range
		idx.maxLayer.Store(int32(layer)) //nolint:gosec // G115: Safe conversion - layer from selectLayer() with bounded range
		idx.entryPoint.Store(id)
	}

	return nil
}

// Search performs k-NN search for the given query vector.
func (idx *HNSWIndex) Search(query []float32, k int) ([]*SearchResult, error) {
	// Check if index is empty first
	if idx.nodeCount.Load() == 0 {
		return nil, ErrEmptyIndex
	}

	// Then check dimensions
	if len(query) != int(idx.dimensions.Load()) {
		return nil, ErrInvalidVector
	}

	if k <= 0 {
		k = 1
	}

	// Get entry point
	entryPointID := idx.entryPoint.Load()
	entryPointNode, ok := idx.nodes.Load(entryPointID)
	if !ok {
		return nil, ErrNotFound
	}

	ep, ok := entryPointNode.(*HNSWNode)
	if !ok {
		return nil, ErrInternal
	}

	// Search from top layer to layer 1
	currentLayer := int(idx.maxLayer.Load())
	candidates := []*candidate{
		{id: ep.ID, distance: idx.distanceFunc(query, ep.Vector)},
	}

	for layer := currentLayer; layer > 0; layer-- {
		candidates = idx.searchLayer(query, candidates, 1, layer)
	}

	// Search layer 0 with ef parameter
	ef := idx.config.EfSearch
	if k > ef {
		ef = k
	}
	candidates = idx.searchLayer(query, candidates, ef, 0)

	// Convert to results and sort
	results := make([]*SearchResult, 0, k)
	for i := 0; i < len(candidates) && i < k; i++ {
		results = append(results, &SearchResult{
			ID:       candidates[i].id,
			Distance: candidates[i].distance,
		})
	}

	return results, nil
}

// Get retrieves a node by ID.
func (idx *HNSWIndex) Get(id uint64) (*HNSWNode, error) {
	node, ok := idx.nodes.Load(id)
	if !ok {
		return nil, ErrNotFound
	}
	n, ok := node.(*HNSWNode)
	if !ok {
		return nil, ErrInternal
	}
	return n, nil
}

// Delete removes a node from the index.
func (idx *HNSWIndex) Delete(id uint64) error {
	node, ok := idx.nodes.Load(id)
	if !ok {
		return ErrNotFound
	}

	n, ok := node.(*HNSWNode)
	if !ok {
		return ErrInternal
	}

	// Remove all connections to this node
	idx.nodes.Range(func(key, value interface{}) bool {
		otherNode, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		for layer := 0; layer <= otherNode.Layer; layer++ {
			otherNode.RemoveConnection(layer, id)
		}
		return true
	})

	// Remove the node itself
	idx.nodes.Delete(id)
	idx.nodeCount.Add(-1)

	// Update entry point if necessary
	if idx.entryPoint.Load() == id {
		idx.updateEntryPoint()
	}

	// Update max layer if necessary
	if n.Layer == int(idx.maxLayer.Load()) {
		idx.updateMaxLayer()
	}

	return nil
}

// Count returns the number of nodes in the index.
func (idx *HNSWIndex) Count() int64 {
	return idx.nodeCount.Load()
}

// selectLayer randomly selects a layer for a new node using exponential decay.
func (idx *HNSWIndex) selectLayer() int {
	idx.rngMu.Lock()
	defer idx.rngMu.Unlock()

	// Generate random number in (0, 1)
	uniform := idx.rng.Float64()
	// Use exponential decay: -ln(uniform) * ml
	layer := int(math.Floor(-math.Log(uniform) * idx.config.Ml))

	return layer
}

// updateEntryPoint updates the entry point to the node with the highest layer.
func (idx *HNSWIndex) updateEntryPoint() {
	var maxLayer int32 = -1
	var newEntryPoint uint64

	idx.nodes.Range(func(key, value interface{}) bool {
		node, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		if int32(node.Layer) > maxLayer { //nolint:gosec // G115: Safe conversion - layer value bounded
			maxLayer = int32(node.Layer) //nolint:gosec // G115: Safe conversion - layer value bounded
			newEntryPoint = node.ID
		}
		return true
	})

	if newEntryPoint != 0 {
		idx.entryPoint.Store(newEntryPoint)
		idx.maxLayer.Store(maxLayer)
	} else {
		// No nodes left, reset to empty state
		idx.maxLayer.Store(-1)
	}
}

// updateMaxLayer recalculates the maximum layer in the index.
func (idx *HNSWIndex) updateMaxLayer() {
	var maxLayer int32 = -1

	idx.nodes.Range(func(key, value interface{}) bool {
		node, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		if int32(node.Layer) > maxLayer { //nolint:gosec // G115: Safe conversion - layer value bounded
			maxLayer = int32(node.Layer) //nolint:gosec // G115: Safe conversion - layer value bounded
		}
		return true
	})

	idx.maxLayer.Store(maxLayer)
}

// SearchResult represents a search result with distance.
type SearchResult struct {
	ID       uint64
	Distance float32
}

// candidate represents a candidate node during search.
type candidate struct {
	id       uint64
	distance float32
}

// candidateHeap is a max-heap of candidates for maintaining top-k results.
type candidateHeap []*candidate

func (h candidateHeap) Len() int           { return len(h) }
func (h candidateHeap) Less(i, j int) bool { return h[i].distance > h[j].distance }
func (h candidateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *candidateHeap) Push(x interface{}) {
	if c, ok := x.(*candidate); ok {
		*h = append(*h, c)
	}
}

func (h *candidateHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
