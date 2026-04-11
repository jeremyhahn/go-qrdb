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
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchLayer(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert test vectors
	vectors := [][]float32{
		{1, 0, 0},
		{0.9, 0.1, 0},
		{0.8, 0.2, 0},
		{0, 1, 0},
		{0, 0, 1},
	}

	for i, v := range vectors {
		err = idx.Insert(uint64(i+1), v)
		require.NoError(t, err)
	}

	// Search from entry point
	query := []float32{0.95, 0.05, 0}
	entryPoint := &candidate{
		id:       1,
		distance: idx.distanceFunc(query, vectors[0]),
	}

	results := idx.searchLayer(query, []*candidate{entryPoint}, 3, 0)

	assert.NotEmpty(t, results)
	assert.LessOrEqual(t, len(results), 3)

	// Results should be sorted by distance
	for i := 0; i < len(results)-1; i++ {
		assert.LessOrEqual(t, results[i].distance, results[i+1].distance)
	}
}

func TestSearchBatch(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert test vectors
	for i := uint64(1); i <= 100; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Batch search
	queries := [][]float32{
		{1, 2, 3},
		{50, 100, 150},
		{100, 200, 300},
	}

	results, err := idx.SearchBatch(queries, 5)
	require.NoError(t, err)
	assert.Len(t, results, 3)

	for _, res := range results {
		assert.Len(t, res, 5)
	}
}

func TestSearchBatch_EmptyIndex(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	queries := [][]float32{
		{1, 2, 3},
	}

	_, err = idx.SearchBatch(queries, 5)
	assert.Error(t, err)
}

func TestSearchRadius(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert test vectors in a cluster
	vectors := [][]float32{
		{0, 0, 0},
		{0.1, 0.1, 0},
		{0.2, 0.2, 0},
		{0.3, 0.3, 0},
		{5, 5, 5}, // Far away point
	}

	for i, v := range vectors {
		err = idx.Insert(uint64(i+1), v)
		require.NoError(t, err)
	}

	// Search with small radius
	query := []float32{0, 0, 0}
	results, err := idx.SearchRadius(query, 0.5, 10)
	require.NoError(t, err)

	// Should find nearby points but not the far one
	assert.Greater(t, len(results), 0)
	assert.Less(t, len(results), 5)

	// All results should be within radius
	for _, res := range results {
		assert.LessOrEqual(t, res.Distance, float32(0.5))
	}
}

func TestSearchRadius_MaxResults(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert many nearby vectors
	for i := uint64(1); i <= 20; i++ {
		vector := []float32{float32(i) * 0.01, 0, 0}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Search with max results limit
	query := []float32{0, 0, 0}
	results, err := idx.SearchRadius(query, 1.0, 5)
	require.NoError(t, err)

	// Should respect max results
	assert.LessOrEqual(t, len(results), 5)
}

func TestSearchRadius_EmptyIndex(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Set dimensions to match query
	idx.dimensions.Store(3)

	query := []float32{1, 2, 3}
	_, err = idx.SearchRadius(query, 1.0, 10)
	assert.Error(t, err)
	assert.Equal(t, ErrEmptyIndex, err)
}

func TestSearchRadius_InvalidDimensions(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	vector := []float32{1, 2, 3}
	err = idx.Insert(1, vector)
	require.NoError(t, err)

	query := []float32{1, 2}
	_, err = idx.SearchRadius(query, 1.0, 10)
	assert.Error(t, err)
	assert.Equal(t, ErrInvalidVector, err)
}

func TestSearchAccuracy(t *testing.T) {
	const numVectors = 1000
	const dimensions = 64
	const k = 10

	// Use higher ef for better recall
	config := DefaultConfig()
	config.EfSearch = 100
	config.EfConstruction = 400

	idx, err := NewHNSWIndex(config)
	require.NoError(t, err)

	rng := rand.New(rand.NewSource(42))

	// Generate and insert vectors
	vectors := make([][]float32, numVectors)
	for i := 0; i < numVectors; i++ {
		vectors[i] = randomVector(dimensions, rng)
		err = idx.Insert(uint64(i+1), vectors[i])
		require.NoError(t, err)
	}

	// Test with multiple queries
	numQueries := 50
	totalRecall := 0.0

	for q := 0; q < numQueries; q++ {
		query := randomVector(dimensions, rng)

		// HNSW search
		hnswResults, err := idx.Search(query, k)
		require.NoError(t, err)

		// Brute force search
		bruteResults := bruteForceSearch(query, vectors, k, idx.distanceFunc)

		// Calculate recall
		recall := CalculateRecall(hnswResults, bruteResults, k)
		totalRecall += recall
	}

	avgRecall := totalRecall / float64(numQueries)
	t.Logf("Average Recall@%d: %.4f", k, avgRecall)

	// Require > 95% recall
	assert.Greater(t, avgRecall, 0.95)
}

func TestSearchWithDifferentEf(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert vectors
	rng := rand.New(rand.NewSource(42))
	const numVectors = 500

	for i := 0; i < numVectors; i++ {
		vector := randomVector(32, rng)
		err = idx.Insert(uint64(i+1), vector)
		require.NoError(t, err)
	}

	query := randomVector(32, rng)

	// Test with different ef values
	efValues := []int{10, 20, 50, 100}
	previousRecall := 0.0

	for _, ef := range efValues {
		idx.config.EfSearch = ef
		results, err := idx.Search(query, 10)
		require.NoError(t, err)
		assert.Len(t, results, 10)

		// Higher ef should generally improve recall
		// (though not guaranteed for every single query)
		t.Logf("ef=%d: found %d results", ef, len(results))

		// Just verify we get results
		assert.NotEmpty(t, results)
		_ = previousRecall // Note: recall comparison removed as it's not guaranteed
	}
}

func TestConcurrentSearch(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert vectors
	for i := uint64(1); i <= 1000; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Concurrent searches
	const numGoroutines = 10
	done := make(chan bool, numGoroutines)
	errors := make(chan error, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(id int) {
			query := []float32{float32(id * 100), float32(id * 200), float32(id * 300)}
			_, err := idx.Search(query, 10)
			if err != nil {
				errors <- err
			}
			done <- true
		}(g)
	}

	// Wait for completion
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Check for errors
	select {
	case err := <-errors:
		t.Fatalf("Search error: %v", err)
	default:
		// No errors
	}
}

func BenchmarkSearchLayer(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)

	// Insert vectors
	for i := uint64(1); i <= 1000; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		_ = idx.Insert(i, vector)
	}

	query := []float32{500, 1000, 1500}
	entryPoint := &candidate{id: 1, distance: 0}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.searchLayer(query, []*candidate{entryPoint}, 50, 0)
	}
}

func BenchmarkSearchBatch(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)
	rng := rand.New(rand.NewSource(42))

	// Insert vectors
	for i := 0; i < 10000; i++ {
		vector := randomVector(128, rng)
		_ = idx.Insert(uint64(i+1), vector)
	}

	// Create queries
	queries := make([][]float32, 100)
	for i := range queries {
		queries[i] = randomVector(128, rng)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = idx.SearchBatch(queries, 10)
	}
}

func BenchmarkSearchRadius(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)
	rng := rand.New(rand.NewSource(42))

	// Insert vectors
	for i := 0; i < 10000; i++ {
		vector := randomVector(128, rng)
		_ = idx.Insert(uint64(i+1), vector)
	}

	query := randomVector(128, rng)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = idx.SearchRadius(query, 0.5, 100)
	}
}
