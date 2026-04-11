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

package ivf

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
)

// Index represents an IVF (Inverted File) index for fast approximate nearest neighbor search.
type Index interface {
	// Train trains the index on a set of vectors to learn cluster centroids
	Train(vectors [][]float32) error

	// Add adds a vector with ID and metadata to the index
	Add(id string, vector []float32, metadata map[string]interface{}) error

	// Search searches for k nearest neighbors, searching nprobe clusters
	Search(query []float32, k int, nprobe int) ([]Result, error)

	// Remove removes a vector from the index by ID
	Remove(id string) error

	// Save saves the index to disk
	Save(path string) error

	// Load loads the index from disk
	Load(path string) error

	// Size returns the number of vectors in the index
	Size() int

	// IsTrained returns whether the index has been trained
	IsTrained() bool
}

// Result represents a search result with ID, distance, and metadata.
type Result struct {
	ID       string                 `json:"id"`
	Distance float32                `json:"distance"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Entry represents a vector entry in an inverted list.
type Entry struct {
	ID       string
	Vector   []float32
	Distance float32 // Distance to centroid (precomputed)
}

// IndexedVector stores the complete vector information.
type IndexedVector struct {
	ID       string
	Vector   []float32
	Metadata map[string]interface{}
}

// ivfIndex is the concrete implementation of the IVF index.
type ivfIndex struct {
	mu sync.RWMutex

	config       *Config
	centroids    [][]float32               // Cluster centers
	lists        [][]Entry                 // Inverted lists (one per centroid)
	metadata     map[string]*IndexedVector // ID -> vector + metadata
	trained      bool
	distanceFunc DistanceFunc
}

// NewIndex creates a new IVF index with the given configuration.
func NewIndex(config *Config) (Index, error) {
	if err := config.Validate(); err != nil {
		return nil, api.NewIVFInvalidConfigError("ivf.NewIndex", err)
	}

	return &ivfIndex{
		config:       config,
		lists:        make([][]Entry, config.NumCentroids),
		metadata:     make(map[string]*IndexedVector),
		trained:      false,
		distanceFunc: GetDistanceFunc(config.DistanceMetric),
	}, nil
}

// Train trains the index by running k-means clustering on the training vectors.
func (idx *ivfIndex) Train(vectors [][]float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if len(vectors) == 0 {
		return api.NewIVFEmptyTrainingSetError("ivf.Train")
	}

	// Validate dimensions
	for i, vec := range vectors {
		if len(vec) != idx.config.Dimensions {
			return api.NewIVFDimensionMismatchError("ivf.Train", i, len(vec), idx.config.Dimensions)
		}
	}

	// Run k-means clustering
	kmeans := NewKMeans(idx.config)
	centroids, _, err := kmeans.Cluster(vectors)
	if err != nil {
		return api.NewIVFClusteringFailedError("ivf.Train", err)
	}

	// Initialize inverted lists
	idx.centroids = centroids
	idx.lists = make([][]Entry, idx.config.NumCentroids)
	for i := range idx.lists {
		idx.lists[i] = make([]Entry, 0)
	}

	idx.trained = true
	return nil
}

// Add adds a vector to the index. The index must be trained first.
func (idx *ivfIndex) Add(id string, vector []float32, metadata map[string]interface{}) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if !idx.trained {
		return api.NewIVFNotTrainedError("ivf.Add", "adding vectors")
	}

	if len(vector) != idx.config.Dimensions {
		return api.NewIVFDimensionMismatchError("ivf.Add", -1, len(vector), idx.config.Dimensions)
	}

	// Check if ID already exists
	if _, exists := idx.metadata[id]; exists {
		return api.NewIVFDuplicateIDError("ivf.Add", id)
	}

	// Find nearest centroid
	nearestCentroid := idx.findNearestCentroidUnsafe(vector)
	distToCentroid := idx.distanceFunc(vector, idx.centroids[nearestCentroid])

	// Add to inverted list
	entry := Entry{
		ID:       id,
		Vector:   copyVector(vector),
		Distance: distToCentroid,
	}
	idx.lists[nearestCentroid] = append(idx.lists[nearestCentroid], entry)

	// Store metadata
	idx.metadata[id] = &IndexedVector{
		ID:       id,
		Vector:   copyVector(vector),
		Metadata: metadata,
	}

	return nil
}

// Search performs approximate nearest neighbor search.
func (idx *ivfIndex) Search(query []float32, k int, nprobe int) ([]Result, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if !idx.trained {
		return nil, api.NewIVFNotTrainedError("ivf.Search", "searching")
	}

	if len(query) != idx.config.Dimensions {
		return nil, api.NewIVFQueryDimensionError("ivf.Search", len(query), idx.config.Dimensions)
	}

	if nprobe <= 0 {
		nprobe = idx.config.DefaultNProbe
	}
	if nprobe > idx.config.NumCentroids {
		nprobe = idx.config.NumCentroids
	}

	// Find nprobe nearest centroids
	nearestCentroids := FindNearestCentroids(query, idx.centroids, nprobe, idx.distanceFunc)

	// Search within those inverted lists
	candidates := make([]Result, 0, nprobe*10) // Preallocate
	for _, centroidID := range nearestCentroids {
		for _, entry := range idx.lists[centroidID] {
			dist := idx.distanceFunc(query, entry.Vector)
			vecMeta := idx.metadata[entry.ID]
			candidates = append(candidates, Result{
				ID:       entry.ID,
				Distance: dist,
				Metadata: vecMeta.Metadata,
			})
		}
	}

	// Sort by distance
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Distance < candidates[j].Distance
	})

	// Return top k
	if len(candidates) > k {
		candidates = candidates[:k]
	}

	return candidates, nil
}

// Remove removes a vector from the index by ID.
func (idx *ivfIndex) Remove(id string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if !idx.trained {
		return api.NewIVFNotTrainedError("ivf.Remove", "")
	}

	// Check if ID exists
	vecMeta, exists := idx.metadata[id]
	if !exists {
		return api.NewIVFVectorNotFoundError("ivf.Remove", id)
	}

	// Find which inverted list it's in
	nearestCentroid := idx.findNearestCentroidUnsafe(vecMeta.Vector)

	// Remove from inverted list
	list := idx.lists[nearestCentroid]
	for i, entry := range list {
		if entry.ID == id {
			// Swap with last element and truncate
			list[i] = list[len(list)-1]
			idx.lists[nearestCentroid] = list[:len(list)-1]
			break
		}
	}

	// Remove from metadata
	delete(idx.metadata, id)

	return nil
}

// Save saves the index to disk.
func (idx *ivfIndex) Save(path string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if !idx.trained {
		return api.NewIVFUntrainedSaveError("ivf.Save")
	}

	// Create serializable structure
	data := struct {
		Config    *Config
		Centroids [][]float32
		Lists     [][]Entry
		Metadata  map[string]*IndexedVector
	}{
		Config:    idx.config,
		Centroids: idx.centroids,
		Lists:     idx.lists,
		Metadata:  idx.metadata,
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return api.NewIVFSaveError("ivf.Save", "marshal index", err)
	}

	// Write to file
	if err := os.WriteFile(path, jsonData, 0600); err != nil {
		return api.NewIVFSaveError("ivf.Save", "write index file", err)
	}

	return nil
}

// Load loads the index from disk.
func (idx *ivfIndex) Load(path string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Read file
	jsonData, err := os.ReadFile(path)
	if err != nil {
		return api.NewIVFSaveError("ivf.Load", "read index file", err)
	}

	// Unmarshal from JSON
	var data struct {
		Config    *Config
		Centroids [][]float32
		Lists     [][]Entry
		Metadata  map[string]*IndexedVector
	}

	if err := json.Unmarshal(jsonData, &data); err != nil {
		return api.NewIVFSaveError("ivf.Load", "unmarshal index", err)
	}

	// Restore index state
	idx.config = data.Config
	idx.centroids = data.Centroids
	idx.lists = data.Lists
	idx.metadata = data.Metadata
	idx.trained = true
	idx.distanceFunc = GetDistanceFunc(idx.config.DistanceMetric)

	return nil
}

// Size returns the number of vectors in the index.
func (idx *ivfIndex) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.metadata)
}

// IsTrained returns whether the index has been trained.
func (idx *ivfIndex) IsTrained() bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.trained
}

// findNearestCentroidUnsafe finds the nearest centroid without locking (caller must lock).
func (idx *ivfIndex) findNearestCentroidUnsafe(vec []float32) int {
	minDist := float32(math.MaxFloat32)
	nearest := 0

	for i, centroid := range idx.centroids {
		dist := idx.distanceFunc(vec, centroid)
		if dist < minDist {
			minDist = dist
			nearest = i
		}
	}

	return nearest
}

// Stats returns statistics about the index.
func (idx *ivfIndex) Stats() map[string]interface{} {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	listSizes := make([]int, len(idx.lists))
	totalVectors := 0
	minSize := int(math.MaxInt32)
	maxSize := 0

	for i, list := range idx.lists {
		size := len(list)
		listSizes[i] = size
		totalVectors += size
		if size < minSize {
			minSize = size
		}
		if size > maxSize {
			maxSize = size
		}
	}

	avgSize := 0.0
	if len(idx.lists) > 0 {
		avgSize = float64(totalVectors) / float64(len(idx.lists))
	}

	return map[string]interface{}{
		"num_centroids":   len(idx.centroids),
		"total_vectors":   totalVectors,
		"avg_list_size":   avgSize,
		"min_list_size":   minSize,
		"max_list_size":   maxSize,
		"trained":         idx.trained,
		"dimensions":      idx.config.Dimensions,
		"distance_metric": idx.config.DistanceMetric.String(),
	}
}
