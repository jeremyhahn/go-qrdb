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

// The seed is REQUIRED, not decorative. This test asserts that the three
// returned centroids are more than 1.0 apart, which is a property of the
// clustering k-means happened to find — and k-means++ on twelve points lands in
// a local minimum often enough to redden CI. Measured over 60,000 seeds on this
// exact dataset and config: 28 produce two centroids within 1.0 of each other
// (1 in 2,143). Seed 1 in that set reached CI run 11336 on an unrelated PR and
// failed with "0.15811394 is not greater than 1" (go-qrdb#291).
//
// Seed 291 is verified: minimum pairwise centroid separation 7.071067, a margin
// of 6.07 over the threshold. The distribution is bimodal — 59,972 of the first
// 60,000 seeds give exactly 7.071067 (the true centroids of the three planted
// clusters) and the remaining 28 collapse — so there is no marginal seed to
// pick, only a correct one or a degenerate one.
func TestKMeans_Cluster(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  100,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
		Seed:           291,
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

// Seeded because the assertion is on the clustering OUTCOME (centroid
// separation), not on an invariant. Measured 0 failures in 60,000 seeds, which
// bounds the rate at roughly 1 in 20,000 rather than establishing zero — the
// seed removes the class instead of relying on that bound (go-qrdb#291).
func TestKMeans_KMeansPlusPlusInit(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
		Seed:           291,
	}

	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1},
		{10.0, 10.0}, {10.1, 10.1},
	}

	km := NewKMeans(config)
	centroids, _, err := km.Cluster(vectors)

	require.NoError(t, err)
	require.Len(t, centroids, 2)

	// K-means++ should choose well-separated initial centroids
	dist := L2(centroids[0], centroids[1])
	assert.Greater(t, dist, float32(5.0))
}

// Seeded: asserts where the centroids landed and how the vectors split, both
// outcome properties. Measured 0 failures in 60,000 seeds (go-qrdb#291).
func TestKMeans_Convergence(t *testing.T) {
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

// Seeded: asserts the returned centroids are all within 0.1 of each other,
// which is an outcome property. Measured 0 failures in 60,000 seeds
// (go-qrdb#291).
func TestKMeans_HandleEmptyClusters(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
		Seed:           291,
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

// The empty-cluster repair is reachable from Cluster and it is LOAD-BEARING.
//
// go-qrdb#291 proposed recomputing assignments/clusterSizes against the
// centroid set actually returned, before handleEmptyClusters runs, because on
// two of Cluster's three exit paths those counts describe the PREVIOUS
// centroids. The control-flow reading is correct. The change was measured
// rather than assumed, and the measurement refused it:
//
//	dataset: the twelve points of TestKMeans_Cluster, 60,000 seeds
//	shipped config (maxIter=100, conv=1e-4): all 60,000 runs exit on the
//	   !changed path, where the counts are consistent by construction —
//	   0 runs carry stale counts, so the proposed change is a no-op there
//	forcing config (maxIter=2, conv=0, random): 2,162 runs carry stale
//	   counts; degenerate clusterings 16,356 as shipped vs 17,394 recomputed
//
// Recomputing is WORSE by 1,038 runs in 60,000. The reason is visible in this
// test: a fresh recount finds no empty cluster (1,894 -> 0), so the repair
// stops firing and the coincident centroid it would have separated is returned
// as-is. The stale counts are what let the repair run at all.
//
// This test pins that. Seed 75 with the config below deterministically leaves
// cluster 1 empty at the exit, holding the all-zero placeholder that
// recomputeCentroids leaves for a zero-size cluster; the repair overwrites it
// with a training vector. Apply the recompute and this test goes red, because
// cluster 1 is no longer empty under the fresh counts and centroid 1 stays at
// the origin.
func TestKMeans_Cluster_EmptyClusterRepairIsLoadBearing(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  2,   // stops before assignments settle, so the loop
		Convergence:    0.0, // exits on the iteration bound, not on !changed
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
		Seed:           75,
	}

	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {-0.1, 0.1}, {0.1, -0.1},
		{5.0, 5.0}, {5.1, 5.1}, {4.9, 5.1}, {5.1, 4.9},
		{10.0, 0.0}, {10.1, 0.1}, {9.9, 0.1}, {10.1, -0.1},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)
	require.NoError(t, err)
	require.Len(t, centroids, 3)
	require.Len(t, assignments, len(vectors))

	// Emit the inputs so a reader can audit this at rest without re-running it.
	t.Logf("seed=%d maxIterations=%d convergence=%v init=%q centroids=%v assignments=%v",
		config.Seed, config.MaxIterations, config.Convergence, config.InitMethod,
		centroids, assignments)

	// Cluster 1 was empty at the exit. recomputeCentroids divides by the
	// cluster size only when it is nonzero, so an unrepaired empty cluster is
	// returned as the zero vector — a centroid at the origin that no vector
	// chose, which then attracts queries near the origin for the life of the
	// index.
	placeholder := []float32{0.0, 0.0}
	assert.NotEqual(t, placeholder, centroids[1],
		"centroid 1 is the all-zero placeholder recomputeCentroids leaves for an "+
			"empty cluster, so handleEmptyClusters did not repair it. Either the "+
			"repair stopped running, or assignments/clusterSizes were recomputed "+
			"against the returned centroids before it ran — which go-qrdb#291 "+
			"measured as 1,038 MORE degenerate clusterings in 60,000, not fewer")

	// The repair moves the farthest point of the largest cluster into the empty
	// centroid, so the centroid must now BE one of the training vectors.
	isTrainingVector := false
	for _, vec := range vectors {
		if L2(centroids[1], vec) == 0 {
			isTrainingVector = true
			break
		}
	}
	assert.True(t, isTrainingVector,
		"centroid 1 is %v, which is not any training vector: the repair is "+
			"supposed to seed an empty cluster from the farthest point of the "+
			"largest one", centroids[1])

	// And every cluster is non-empty under the assignments returned alongside
	// it, which is the property the repair exists to restore.
	sizes := make([]int, config.NumCentroids)
	for _, a := range assignments {
		require.GreaterOrEqual(t, a, 0)
		require.Less(t, a, config.NumCentroids)
		sizes[a]++
	}
	for i, size := range sizes {
		assert.Positive(t, size,
			"cluster %d is empty in the returned assignments (sizes %v): the "+
				"repair left a centroid no vector belongs to", i, sizes)
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

	require.Len(t, result, 4)

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
