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
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKMeans_Cluster(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  100,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create three distinct clusters in 2D space
	vectors := [][]float32{
		// Cluster 1 (around 0,0)
		{0.0, 0.0}, {0.1, 0.1}, {-0.1, 0.1}, {0.1, -0.1},
		// Cluster 2 (around 5,5)
		{5.0, 5.0}, {5.1, 5.1}, {4.9, 5.1}, {5.1, 4.9},
		// Cluster 3 (around 10,0)
		{10.0, 0.0}, {10.1, 0.1}, {9.9, 0.1}, {10.1, -0.1},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 3)
	assert.Len(t, assignments, len(vectors))

	// Verify all centroids are different
	for i := 0; i < len(centroids); i++ {
		for j := i + 1; j < len(centroids); j++ {
			dist := L2(centroids[i], centroids[j])
			assert.Greater(t, dist, float32(1.0), "centroids %d and %d are too close", i, j)
		}
	}

	// Verify assignments are valid
	for _, a := range assignments {
		assert.GreaterOrEqual(t, a, 0)
		assert.Less(t, a, 3)
	}
}

func TestKMeans_EmptyVectors(t *testing.T) {
	config := DefaultConfig(2)
	km := NewKMeans(config)

	_, _, err := km.Cluster([][]float32{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty vector set")
}

func TestKMeans_TooFewVectors(t *testing.T) {
	config := &Config{
		NumCentroids:   10,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	vectors := [][]float32{
		{1.0, 2.0},
		{3.0, 4.0},
	}

	km := NewKMeans(config)
	_, _, err := km.Cluster(vectors)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot create 10 clusters from 2 vectors")
}

func TestKMeans_RandomInit(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {0.2, 0.2},
		{5.0, 5.0}, {5.1, 5.1}, {5.2, 5.2},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 2)
	assert.Len(t, assignments, len(vectors))
}

func TestKMeans_KMeansPlusPlusInit(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1},
		{10.0, 10.0}, {10.1, 10.1},
	}

	km := NewKMeans(config)
	centroids, _, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 2)

	// K-means++ should choose well-separated initial centroids
	dist := L2(centroids[0], centroids[1])
	assert.Greater(t, dist, float32(5.0))
}

func TestKMeans_Convergence(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  100,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Perfect clusters that should converge immediately
	vectors := [][]float32{
		{0.0, 0.0}, {0.0, 0.0}, {0.0, 0.0},
		{10.0, 10.0}, {10.0, 10.0}, {10.0, 10.0},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 2)

	// Verify centroids are at cluster centers
	hasZeroCluster := false
	hasTenCluster := false

	for _, centroid := range centroids {
		distToZero := L2(centroid, []float32{0.0, 0.0})
		distToTen := L2(centroid, []float32{10.0, 10.0})

		if distToZero < 0.1 {
			hasZeroCluster = true
		}
		if distToTen < 0.1 {
			hasTenCluster = true
		}
	}

	assert.True(t, hasZeroCluster)
	assert.True(t, hasTenCluster)

	// Verify assignments are correct
	for i, assignment := range assignments {
		if i < 3 {
			// First 3 vectors should be in same cluster
			assert.Equal(t, assignments[0], assignment)
		} else {
			// Last 3 vectors should be in same cluster (different from first)
			assert.Equal(t, assignments[3], assignment)
			assert.NotEqual(t, assignments[0], assignment)
		}
	}
}

func TestKMeans_HandleEmptyClusters(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// All vectors are identical - will create empty clusters
	vectors := [][]float32{
		{1.0, 1.0}, {1.0, 1.0}, {1.0, 1.0}, {1.0, 1.0},
	}

	km := NewKMeans(config)
	centroids, _, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 3)

	// Empty clusters should be handled by redistributing
	// All centroids should be identical in this case
	for i := 1; i < len(centroids); i++ {
		dist := L2(centroids[0], centroids[i])
		assert.Less(t, dist, float32(0.1))
	}
}

func TestKMeans_DifferentMetrics(t *testing.T) {
	metrics := []DistanceMetric{L2Distance, CosineDistance, DotProductDistance}

	vectors := [][]float32{
		{1.0, 0.0}, {0.9, 0.1}, {0.8, 0.2},
		{0.0, 1.0}, {0.1, 0.9}, {0.2, 0.8},
	}

	for _, metric := range metrics {
		t.Run(metric.String(), func(t *testing.T) {
			config := &Config{
				NumCentroids:   2,
				Dimensions:     2,
				MaxIterations:  25,
				Convergence:    0.0001,
				InitMethod:     "kmeans++",
				DefaultNProbe:  1,
				DistanceMetric: metric,
			}

			km := NewKMeans(config)
			centroids, assignments, err := km.Cluster(vectors)

			require.NoError(t, err)
			assert.Len(t, centroids, 2)
			assert.Len(t, assignments, len(vectors))
		})
	}
}

func TestFindNearestCentroids(t *testing.T) {
	centroids := [][]float32{
		{0.0, 0.0},
		{5.0, 5.0},
		{10.0, 0.0},
		{0.0, 10.0},
	}

	query := []float32{1.0, 1.0}
	distFunc := L2

	tests := []struct {
		k    int
		want int // Number of results
	}{
		{k: 1, want: 1},
		{k: 2, want: 2},
		{k: 3, want: 3},
		{k: 5, want: 4}, // More than available centroids
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("k=%d", tt.k), func(t *testing.T) {
			result := FindNearestCentroids(query, centroids, tt.k, distFunc)
			assert.Len(t, result, tt.want)

			// Verify results are valid indices
			for _, idx := range result {
				assert.GreaterOrEqual(t, idx, 0)
				assert.Less(t, idx, len(centroids))
			}

			// Verify results are sorted by distance
			if len(result) > 1 {
				for i := 0; i < len(result)-1; i++ {
					dist1 := distFunc(query, centroids[result[i]])
					dist2 := distFunc(query, centroids[result[i+1]])
					assert.LessOrEqual(t, dist1, dist2)
				}
			}
		})
	}
}

func TestFindNearestCentroids_Ordering(t *testing.T) {
	centroids := [][]float32{
		{0.0, 0.0},  // dist = sqrt(2) ~ 1.41
		{5.0, 5.0},  // dist = sqrt(32) ~ 5.66
		{10.0, 0.0}, // dist = sqrt(82) ~ 9.06
		{1.0, 0.0},  // dist = 1.0
	}

	query := []float32{1.0, 1.0}
	result := FindNearestCentroids(query, centroids, 4, L2)

	assert.Len(t, result, 4)

	// Should be in order: [3, 0, 1, 2] based on distances
	assert.Equal(t, 3, result[0]) // Closest
	assert.Equal(t, 0, result[1])
	assert.Equal(t, 1, result[2])
	assert.Equal(t, 2, result[3]) // Farthest
}

func TestCopyVector(t *testing.T) {
	original := []float32{1.0, 2.0, 3.0}
	copied := copyVector(original)

	assert.Equal(t, original, copied)

	// Modify copy
	copied[0] = 99.0

	// Original should be unchanged
	assert.Equal(t, float32(1.0), original[0])
	assert.Equal(t, float32(99.0), copied[0])
}

// Benchmarks
func BenchmarkKMeans_Small(b *testing.B) {
	config := &Config{
		NumCentroids:   16,
		Dimensions:     128,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	vectors := generateRandomVectors(1000, 128)
	km := NewKMeans(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = km.Cluster(vectors)
	}
}

func BenchmarkKMeans_Medium(b *testing.B) {
	config := &Config{
		NumCentroids:   64,
		Dimensions:     256,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	vectors := generateRandomVectors(5000, 256)
	km := NewKMeans(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = km.Cluster(vectors)
	}
}

func BenchmarkKMeansPlusPlus_Init(b *testing.B) {
	config := &Config{
		NumCentroids:   256,
		Dimensions:     1536,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  10,
		DistanceMetric: L2Distance,
	}

	vectors := generateRandomVectors(10000, 1536)
	km := NewKMeans(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = km.initializeKMeansPlusPlus(vectors)
	}
}

func BenchmarkFindNearestCentroids(b *testing.B) {
	centroids := generateRandomVectors(256, 1536)
	query := generateRandomVectors(1, 1536)[0]
	distFunc := L2SIMD

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FindNearestCentroids(query, centroids, 10, distFunc)
	}
}

// Helper function to generate random vectors for testing
func generateRandomVectors(count, dims int) [][]float32 {
	vectors := make([][]float32, count)
	for i := 0; i < count; i++ {
		vectors[i] = make([]float32, dims)
		for j := 0; j < dims; j++ {
			vectors[i][j] = rand.Float32()*2 - 1 // Range [-1, 1]
		}
	}
	return vectors
}
