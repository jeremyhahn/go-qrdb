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
