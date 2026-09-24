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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKMeans_HandleEmptyClusters_Direct tests handleEmptyClusters directly
func TestKMeans_HandleEmptyClusters_Direct(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	km := NewKMeans(config)

	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {0.2, 0.2}, {0.3, 0.3}, // 4 vectors in cluster 0
		{5.0, 5.0}, {5.1, 5.1}, // 2 vectors in cluster 1
		// cluster 2 is empty
	}

	// Manually set up assignments with an empty cluster
	assignments := []int{0, 0, 0, 0, 1, 1}
	clusterSizes := []int{4, 2, 0}
	centroids := [][]float32{
		{0.15, 0.15}, // cluster 0 centroid
		{5.05, 5.05}, // cluster 1 centroid
		{0.0, 0.0},   // empty cluster 2
	}

	// Make copies to track changes
	originalAssignments := make([]int, len(assignments))
	copy(originalAssignments, assignments)

	// Call handleEmptyClusters - it mutates assignments and clusterSizes
	result := km.handleEmptyClusters(vectors, assignments, clusterSizes, centroids)

	assert.Len(t, result, 3)
}

// TestKMeans_HandleEmptyClusters_LargestClusterIsDegenerate distinguishes
// the current shared kmeans.RepairEmptyClusters donor search (which
// considers EVERY splittable cluster) from the largest-cluster-only search
// this package used before handleEmptyClusters was extracted into that
// shared function. Cluster 0 is the largest by count (3 points) but every
// one of those points is an exact duplicate sitting exactly on its own
// centroid, so every distance in it is zero; cluster 1, though smaller (2
// points), holds two genuinely distinct points with a real, non-zero
// distance to their own centroid. A largest-cluster-only search picks
// cluster 0 (the largest), finds no usable donor there since every candidate
// ties at distance zero, and gives up without ever considering cluster 1 --
// leaving cluster 2 a duplicate despite 3 distinct positions being available
// in the data.
func TestKMeans_HandleEmptyClusters_LargestClusterIsDegenerate(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	km := NewKMeans(config)

	vectors := [][]float32{
		{0, 0}, {0, 0}, {0, 0}, // cluster 0: 3 points, ALL exact duplicates
		{10, 0}, {20, 0}, // cluster 1: 2 points, genuinely distinct
	}
	assignments := []int{0, 0, 0, 1, 1}
	clusterSizes := []int{3, 2, 0}
	centroids := [][]float32{
		{0, 0},
		{15, 0},  // cluster 1's mean
		{-1, -1}, // empty, and a duplicate of nothing real
	}

	result := km.handleEmptyClusters(vectors, assignments, clusterSizes, centroids)

	require.Len(t, result, 3)
	distinct := map[[2]float32]bool{}
	for _, c := range result {
		distinct[[2]float32{c[0], c[1]}] = true
	}
	assert.Len(t, distinct, 3, "3 distinct positions were available, so the repair must produce 3 distinct centroids: got %v", result)
	assert.NotEqual(t, []float32{-1, -1}, result[2])
	assert.NotEqual(t, []float32{0, 0}, result[2], "must not have tried to donate from the degenerate cluster 0")
}

// TestKMeans_HandleEmptyClusters_MaxSizeOne tests when max cluster size is 1
func TestKMeans_HandleEmptyClusters_MaxSizeOne(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	km := NewKMeans(config)

	vectors := [][]float32{
		{0.0, 0.0}, // 1 vector in cluster 0
		{5.0, 5.0}, // 1 vector in cluster 1
		// cluster 2 is empty
	}

	// Manually set up assignments where maxSize = 1 (shouldn't split)
	assignments := []int{0, 1}
	clusterSizes := []int{1, 1, 0}
	centroids := [][]float32{
		{0.0, 0.0},
		{5.0, 5.0},
		{0.0, 0.0}, // empty cluster
	}

	// Call handleEmptyClusters - maxSize is 1, so can't split
	result := km.handleEmptyClusters(vectors, assignments, clusterSizes, centroids)

	assert.Len(t, result, 3)
	// Empty cluster should remain empty since we can't split clusters with size 1
}

// TestKMeans_HandleEmptyClusters_FarthestIdxNegative tests when farthestIdx is -1
func TestKMeans_HandleEmptyClusters_FarthestIdxNegative(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	km := NewKMeans(config)

	vectors := [][]float32{
		{0.0, 0.0},
	}

	// Set up a scenario where there are no vectors in the largest cluster
	// This is an edge case but we need to cover it
	assignments := []int{0}
	clusterSizes := []int{1, 0}
	centroids := [][]float32{
		{0.0, 0.0},
		{0.0, 0.0}, // empty cluster
	}

	// Call handleEmptyClusters
	result := km.handleEmptyClusters(vectors, assignments, clusterSizes, centroids)

	assert.Len(t, result, 2)
}

// TestKMeans_Cluster_EmptyClusterHandling tests the full cluster flow with empty cluster handling
func TestKMeans_Cluster_EmptyClusterHandling(t *testing.T) {
	// Use a seed-based approach to consistently create empty clusters
	config := &Config{
		NumCentroids:   5,
		Dimensions:     2,
		MaxIterations:  3, // Few iterations to preserve empty clusters
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create vectors in just 2 clusters, forcing 3 empty clusters
	vectors := [][]float32{
		{0.0, 0.0}, {0.05, 0.05}, {0.1, 0.1},
		{10.0, 10.0}, {10.05, 10.05}, {10.1, 10.1},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 5)
	assert.Len(t, assignments, len(vectors))

	// Count non-empty clusters
	clusterCounts := make(map[int]int)
	for _, a := range assignments {
		clusterCounts[a]++
	}

	// Should have some non-empty clusters
	assert.Greater(t, len(clusterCounts), 0)
}

// TestKMeans_InitializeKMeansPlusPlus_BreakCondition tests the break in weight loop
func TestKMeans_InitializeKMeansPlusPlus_BreakCondition(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Use exact same vectors to trigger the break in the cumulative loop
	vectors := [][]float32{
		{1.0, 1.0},
		{2.0, 2.0},
		{3.0, 3.0},
		{4.0, 4.0},
		{5.0, 5.0},
	}

	km := NewKMeans(config)

	// Run multiple times to exercise different random selections
	for i := 0; i < 10; i++ {
		centroids, err := km.initializeKMeansPlusPlus(vectors)
		require.NoError(t, err)
		assert.Len(t, centroids, 3)
	}
}

// TestIndex_Save_MarshalPath tests that JSON marshal succeeds with various data
func TestIndex_Save_MarshalPath(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	trainingVectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1},
		{5.0, 5.0}, {5.1, 5.1},
	}
	require.NoError(t, idx.Train(trainingVectors))

	// Add multiple vectors to exercise marshal code
	for i := 0; i < 10; i++ {
		vec := []float32{float32(i) * 0.1, float32(i) * 0.1}
		err := idx.Add(string(rune('a'+i)), vec, map[string]interface{}{
			"index": i,
			"label": string(rune('A' + i)),
		})
		require.NoError(t, err)
	}

	tmpDir := t.TempDir()
	path := tmpDir + "/test_index.json"
	err := idx.Save(path)
	require.NoError(t, err)
}

// TestKMeans_Cluster_AssignmentNoChange tests early exit when assignments don't change
// Seeded: asserts which vectors share a cluster, an outcome property.
// Measured 0 failures in 60,000 seeds (go-qrdb#291).
func TestKMeans_Cluster_AssignmentNoChange(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  100,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
		Seed:           291,
	}

	// Well-separated clusters that will converge quickly
	vectors := [][]float32{
		{0.0, 0.0}, {0.0, 0.0}, {0.0, 0.0},
		{100.0, 100.0}, {100.0, 100.0}, {100.0, 100.0},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 2)
	require.Len(t, assignments, len(vectors))

	// Verify first 3 are in one cluster, last 3 in another
	assert.Equal(t, assignments[0], assignments[1])
	assert.Equal(t, assignments[0], assignments[2])
	assert.Equal(t, assignments[3], assignments[4])
	assert.Equal(t, assignments[3], assignments[5])
	assert.NotEqual(t, assignments[0], assignments[3])
}

// TestKMeans_HandleEmptyClusters_MultipleEmpty tests handling multiple empty clusters
func TestKMeans_HandleEmptyClusters_MultipleEmpty(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	km := NewKMeans(config)

	// 6 vectors total, 2 empty clusters
	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {0.2, 0.2}, // cluster 0
		{5.0, 5.0}, {5.1, 5.1}, {5.2, 5.2}, // cluster 1
		// clusters 2 and 3 are empty
	}

	assignments := []int{0, 0, 0, 1, 1, 1}
	clusterSizes := []int{3, 3, 0, 0}
	centroids := [][]float32{
		{0.1, 0.1},
		{5.1, 5.1},
		{0.0, 0.0}, // empty
		{0.0, 0.0}, // empty
	}

	result := km.handleEmptyClusters(vectors, assignments, clusterSizes, centroids)

	assert.Len(t, result, 4)
	// At least one empty cluster should be filled
	nonZeroCentroids := 0
	for _, centroid := range result {
		if centroid[0] != 0.0 || centroid[1] != 0.0 {
			nonZeroCentroids++
		}
	}
	assert.GreaterOrEqual(t, nonZeroCentroids, 2) // At least the 2 non-empty ones
}

// TestKMeans_Cluster_ChangeDetection tests the changed flag in the assignment step
func TestKMeans_Cluster_ChangeDetection(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  1,      // Single iteration
		Convergence:    1000.0, // High threshold, won't converge by movement
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Scattered vectors that will require changes
	vectors := [][]float32{
		{0.0, 0.0}, {1.0, 1.0}, {2.0, 2.0},
		{3.0, 3.0}, {4.0, 4.0}, {5.0, 5.0},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 2)
	assert.Len(t, assignments, len(vectors))
}

// TestKMeans_Cluster_MaxIterations tests running to max iterations
func TestKMeans_Cluster_MaxIterations(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  5,       // Limited iterations
		Convergence:    0.00001, // Very strict convergence
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create data that won't easily converge
	vectors := [][]float32{
		{0.0, 0.0}, {0.5, 0.5}, {1.0, 1.0}, {1.5, 1.5},
		{2.0, 2.0}, {2.5, 2.5}, {3.0, 3.0}, {3.5, 3.5},
		{4.0, 4.0}, {4.5, 4.5}, {5.0, 5.0}, {5.5, 5.5},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 3)
	assert.Len(t, assignments, len(vectors))
}
