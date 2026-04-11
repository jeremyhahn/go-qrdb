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
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHNSWIndex(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)
		assert.NotNil(t, idx)
		assert.Equal(t, 16, idx.config.M)
		assert.Equal(t, 32, idx.config.MMax)
		assert.Equal(t, 200, idx.config.EfConstruction)
		assert.Equal(t, 50, idx.config.EfSearch)
	})

	t.Run("custom config", func(t *testing.T) {
		config := &Config{
			M:              8,
			MMax:           16,
			EfConstruction: 100,
			EfSearch:       20,
			Ml:             1.0 / math.Log(8),
			DistanceMetric: CosineDistance,
		}

		idx, err := NewHNSWIndex(config)
		require.NoError(t, err)
		assert.NotNil(t, idx)
		assert.Equal(t, 8, idx.config.M)
		assert.Equal(t, CosineDistance, idx.config.DistanceMetric)
	})

	t.Run("invalid config", func(t *testing.T) {
		config := &Config{
			M:              -1,
			EfConstruction: 100,
		}

		_, err := NewHNSWIndex(config)
		assert.Error(t, err)
	})
}

func TestHNSWIndex_Insert(t *testing.T) {
	t.Run("insert single vector", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vector := []float32{1, 2, 3}
		err = idx.Insert(1, vector)
		require.NoError(t, err)

		assert.Equal(t, int64(1), idx.Count())
		assert.Equal(t, int32(3), idx.dimensions.Load())
	})

	t.Run("insert multiple vectors", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		for i := uint64(1); i <= 10; i++ {
			vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		assert.Equal(t, int64(10), idx.Count())
	})

	t.Run("invalid ID", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vector := []float32{1, 2, 3}
		err = idx.Insert(0, vector)
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidID, err)
	})

	t.Run("duplicate ID", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vector := []float32{1, 2, 3}
		err = idx.Insert(1, vector)
		require.NoError(t, err)

		err = idx.Insert(1, vector)
		assert.Error(t, err)
		assert.Equal(t, ErrDuplicateID, err)
	})

	t.Run("invalid dimensions", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vector1 := []float32{1, 2, 3}
		err = idx.Insert(1, vector1)
		require.NoError(t, err)

		vector2 := []float32{1, 2}
		err = idx.Insert(2, vector2)
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidVector, err)
	})
}

func TestHNSWIndex_Search(t *testing.T) {
	t.Run("search basic", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert test data
		vectors := [][]float32{
			{1, 0, 0},
			{0, 1, 0},
			{0, 0, 1},
			{1, 1, 0},
			{1, 0, 1},
		}

		for i, v := range vectors {
			err = idx.Insert(uint64(i+1), v)
			require.NoError(t, err)
		}

		// Search for nearest to {1, 0, 0}
		query := []float32{1, 0, 0}
		results, err := idx.Search(query, 3)
		require.NoError(t, err)
		assert.Len(t, results, 3)

		// First result should be exact match
		assert.Equal(t, uint64(1), results[0].ID)
		assert.InDelta(t, 0, results[0].Distance, 1e-6)
	})

	t.Run("search with k larger than index size", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vectors := [][]float32{
			{1, 0},
			{0, 1},
		}

		for i, v := range vectors {
			err = idx.Insert(uint64(i+1), v)
			require.NoError(t, err)
		}

		query := []float32{1, 0}
		results, err := idx.Search(query, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("search empty index", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		query := []float32{1, 0, 0}
		_, err = idx.Search(query, 1)
		assert.Error(t, err)
		assert.Equal(t, ErrEmptyIndex, err)
	})

	t.Run("search invalid dimensions", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vector := []float32{1, 2, 3}
		err = idx.Insert(1, vector)
		require.NoError(t, err)

		query := []float32{1, 2}
		_, err = idx.Search(query, 1)
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidVector, err)
	})
}

func TestHNSWIndex_Delete(t *testing.T) {
	t.Run("delete existing node", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vector := []float32{1, 2, 3}
		err = idx.Insert(1, vector)
		require.NoError(t, err)

		err = idx.Delete(1)
		require.NoError(t, err)
		assert.Equal(t, int64(0), idx.Count())
	})

	t.Run("delete non-existent node", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Delete(999)
		assert.Error(t, err)
		assert.Equal(t, ErrNotFound, err)
	})

	t.Run("delete entry point", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert multiple vectors
		for i := uint64(1); i <= 5; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		entryPoint := idx.entryPoint.Load()
		err = idx.Delete(entryPoint)
		require.NoError(t, err)

		// Entry point should be updated
		newEntryPoint := idx.entryPoint.Load()
		assert.NotEqual(t, entryPoint, newEntryPoint)
	})
}

func TestHNSWIndex_Get(t *testing.T) {
	t.Run("get existing node", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vector := []float32{1, 2, 3}
		err = idx.Insert(1, vector)
		require.NoError(t, err)

		node, err := idx.Get(1)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), node.ID)
		assert.Equal(t, vector, node.Vector)
	})

	t.Run("get non-existent node", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		_, err = idx.Get(999)
		assert.Error(t, err)
		assert.Equal(t, ErrNotFound, err)
	})
}

func TestHNSWIndex_SelectLayer(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Generate many layers to check distribution
	layers := make(map[int]int)
	iterations := 1000

	for i := 0; i < iterations; i++ {
		layer := idx.selectLayer()
		layers[layer]++
	}

	// Layer 0 should be most common
	assert.Greater(t, layers[0], layers[1])
	assert.Greater(t, layers[1], layers[2])

	// Should have multiple layers
	assert.GreaterOrEqual(t, len(layers), 3)
}

func TestHNSWIndex_Concurrent(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	const numGoroutines = 10
	const vectorsPerGoroutine = 100

	done := make(chan bool, numGoroutines)

	// Concurrent inserts
	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			for i := 0; i < vectorsPerGoroutine; i++ {
				id := uint64(goroutineID*vectorsPerGoroutine + i + 1)
				vector := []float32{float32(id), float32(id * 2), float32(id * 3)}
				err := idx.Insert(id, vector)
				if err != nil {
					t.Errorf("Insert failed: %v", err)
				}
			}
			done <- true
		}(g)
	}

	// Wait for all inserts to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	assert.Equal(t, int64(numGoroutines*vectorsPerGoroutine), idx.Count())

	// Concurrent searches
	for g := 0; g < numGoroutines; g++ {
		go func() {
			query := []float32{50, 100, 150}
			_, err := idx.Search(query, 10)
			if err != nil {
				t.Errorf("Search failed: %v", err)
			}
			done <- true
		}()
	}

	// Wait for all searches to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}

func TestHNSWIndex_GetStats(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Empty index
	stats := idx.GetStats()
	assert.Equal(t, int64(0), stats.NodeCount)
	assert.Equal(t, int(-1), stats.MaxLayer)

	// Add vectors
	for i := uint64(1); i <= 100; i++ {
		vector := []float32{float32(i), float32(i * 2)}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	stats = idx.GetStats()
	assert.Equal(t, int64(100), stats.NodeCount)
	assert.GreaterOrEqual(t, stats.MaxLayer, 0)
	assert.Greater(t, stats.AvgConnections, 0.0)
	assert.Equal(t, 2, stats.Dimensions)
}

func TestHNSWIndex_LargeScale(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Reduced from 10000 to 1000 for faster unit tests
	const numVectors = 1000
	const dimensions = 128

	// Generate random vectors
	rng := rand.New(rand.NewSource(42))
	vectors := make([][]float32, numVectors)
	for i := 0; i < numVectors; i++ {
		vec := make([]float32, dimensions)
		for j := 0; j < dimensions; j++ {
			vec[j] = rng.Float32()
		}
		vectors[i] = vec
	}

	// Insert all vectors
	for i, vec := range vectors {
		err = idx.Insert(uint64(i+1), vec)
		require.NoError(t, err)

		if i%100 == 0 {
			t.Logf("Inserted %d vectors", i)
		}
	}

	assert.Equal(t, int64(numVectors), idx.Count())

	// Perform searches
	for i := 0; i < 10; i++ {
		query := vectors[rng.Intn(numVectors)]
		results, err := idx.Search(query, 10)
		require.NoError(t, err)
		assert.Len(t, results, 10)

		// First result should be very close (likely the vector itself)
		assert.Less(t, results[0].Distance, float32(0.1))
	}
}

func TestCalculateRecall(t *testing.T) {
	t.Run("perfect recall", func(t *testing.T) {
		results := []*SearchResult{
			{ID: 1, Distance: 0.1},
			{ID: 2, Distance: 0.2},
			{ID: 3, Distance: 0.3},
		}

		groundTruth := []*SearchResult{
			{ID: 1, Distance: 0.1},
			{ID: 2, Distance: 0.2},
			{ID: 3, Distance: 0.3},
		}

		recall := CalculateRecall(results, groundTruth, 3)
		assert.Equal(t, 1.0, recall)
	})

	t.Run("partial recall", func(t *testing.T) {
		results := []*SearchResult{
			{ID: 1, Distance: 0.1},
			{ID: 2, Distance: 0.2},
			{ID: 4, Distance: 0.3},
		}

		groundTruth := []*SearchResult{
			{ID: 1, Distance: 0.1},
			{ID: 2, Distance: 0.2},
			{ID: 3, Distance: 0.3},
		}

		recall := CalculateRecall(results, groundTruth, 3)
		assert.InDelta(t, 0.666, recall, 0.01)
	})

	t.Run("no recall", func(t *testing.T) {
		results := []*SearchResult{
			{ID: 4, Distance: 0.1},
			{ID: 5, Distance: 0.2},
			{ID: 6, Distance: 0.3},
		}

		groundTruth := []*SearchResult{
			{ID: 1, Distance: 0.1},
			{ID: 2, Distance: 0.2},
			{ID: 3, Distance: 0.3},
		}

		recall := CalculateRecall(results, groundTruth, 3)
		assert.Equal(t, 0.0, recall)
	})

	t.Run("empty ground truth", func(t *testing.T) {
		results := []*SearchResult{
			{ID: 1, Distance: 0.1},
		}

		var groundTruth []*SearchResult

		recall := CalculateRecall(results, groundTruth, 1)
		assert.Equal(t, 0.0, recall)
	})
}

// Benchmark tests
func BenchmarkHNSW_Insert(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)

	vector := []float32{1, 2, 3, 4, 5, 6, 7, 8}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.Insert(uint64(i+1), vector)
	}
}

func BenchmarkHNSW_Search_10K(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)
	rng := rand.New(rand.NewSource(42))

	// Insert 10K vectors
	const numVectors = 10000
	const dimensions = 128

	for i := 0; i < numVectors; i++ {
		vec := make([]float32, dimensions)
		for j := 0; j < dimensions; j++ {
			vec[j] = rng.Float32()
		}
		_ = idx.Insert(uint64(i+1), vec)
	}

	query := make([]float32, dimensions)
	for j := 0; j < dimensions; j++ {
		query[j] = rng.Float32()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = idx.Search(query, 10)
	}
}

func BenchmarkHNSW_Search_100K(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)
	rng := rand.New(rand.NewSource(42))

	const numVectors = 100000
	const dimensions = 128

	b.Log("Building index with 100K vectors...")
	for i := 0; i < numVectors; i++ {
		vec := make([]float32, dimensions)
		for j := 0; j < dimensions; j++ {
			vec[j] = rng.Float32()
		}
		_ = idx.Insert(uint64(i+1), vec)

		if i%10000 == 0 {
			b.Logf("Inserted %d vectors", i)
		}
	}

	query := make([]float32, dimensions)
	for j := 0; j < dimensions; j++ {
		query[j] = rng.Float32()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = idx.Search(query, 10)
	}
}

func BenchmarkHNSW_ConcurrentSearch(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)
	rng := rand.New(rand.NewSource(42))

	const numVectors = 10000
	const dimensions = 128

	for i := 0; i < numVectors; i++ {
		vec := make([]float32, dimensions)
		for j := 0; j < dimensions; j++ {
			vec[j] = rng.Float32()
		}
		_ = idx.Insert(uint64(i+1), vec)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		query := make([]float32, dimensions)
		for j := 0; j < dimensions; j++ {
			query[j] = rng.Float32()
		}

		for pb.Next() {
			_, _ = idx.Search(query, 10)
		}
	})
}

// Helper function to generate random vector
func randomVector(dim int, rng *rand.Rand) []float32 {
	vec := make([]float32, dim)
	for i := 0; i < dim; i++ {
		vec[i] = rng.Float32()
	}
	return vec
}

// Test recall on generated dataset
func TestHNSWIndex_Recall(t *testing.T) {
	const numVectors = 1000
	const dimensions = 128
	const numQueries = 100

	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	rng := rand.New(rand.NewSource(42))

	// Generate and insert vectors
	vectors := make([][]float32, numVectors)
	for i := 0; i < numVectors; i++ {
		vectors[i] = randomVector(dimensions, rng)
		err = idx.Insert(uint64(i+1), vectors[i])
		require.NoError(t, err)
	}

	// Test recall
	totalRecall := 0.0
	for i := 0; i < numQueries; i++ {
		query := randomVector(dimensions, rng)

		// HNSW search
		hnswResults, err := idx.Search(query, 10)
		require.NoError(t, err)

		// Brute force search for ground truth
		groundTruth := bruteForceSearch(query, vectors, 10, idx.distanceFunc)

		recall := CalculateRecall(hnswResults, groundTruth, 10)
		totalRecall += recall
	}

	avgRecall := totalRecall / float64(numQueries)
	t.Logf("Average Recall@10: %.4f", avgRecall)

	// Recall should be > 95%
	assert.Greater(t, avgRecall, 0.95, fmt.Sprintf("Expected recall > 0.95, got %.4f", avgRecall))
}

// bruteForceSearch performs exhaustive nearest neighbor search
func bruteForceSearch(query []float32, vectors [][]float32, k int, distFunc DistanceFunc) []*SearchResult {
	results := make([]*SearchResult, len(vectors))

	for i, vec := range vectors {
		dist := distFunc(query, vec)
		results[i] = &SearchResult{
			ID:       uint64(i + 1),
			Distance: dist,
		}
	}

	// Sort by distance
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Distance < results[i].Distance {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if k > len(results) {
		k = len(results)
	}

	return results[:k]
}
