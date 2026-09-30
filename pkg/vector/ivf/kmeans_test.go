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
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
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

// TestKMeans_Cluster_DimensionMismatch pins that Cluster refuses a vector
// whose length disagrees with Config.Dimensions with a typed error, rather
// than feeding it to the distance funcs -- which treat a length mismatch as
// math.MaxFloat32, a sentinel that k-means++ then squares while summing
// totalWeight (go-qrdb#565), overflowing to +Inf.
func TestKMeans_Cluster_DimensionMismatch(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     128,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	vectors := make([][]float32, 4)
	for i := range vectors {
		vectors[i] = make([]float32, 128)
	}
	vectors[3] = make([]float32, 64) // wrong dimension

	km := NewKMeans(config)
	_, _, err := km.Cluster(vectors)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dimensions")

	var dimErr *api.KMeansDimensionMismatchError
	require.ErrorAs(t, err, &dimErr)
	assert.Equal(t, 3, dimErr.Index)
	assert.Equal(t, 64, dimErr.Got)
	assert.Equal(t, 128, dimErr.Expected)
}

// TestKMeans_Cluster_AllIdenticalPoints pins that clustering a set of
// perfectly identical vectors terminates deterministically -- rather than
// stalling in the k-means++ weighted-sampling loop, where every remaining
// point's distance to the already-chosen centroids is zero from the second
// centroid onward -- and still returns exactly NumCentroids centroids.
func TestKMeans_Cluster_AllIdenticalPoints(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     8,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	point := []float32{1, 2, 3, 4, 5, 6, 7, 8}
	vectors := make([][]float32, 50)
	for i := range vectors {
		vectors[i] = copyVector(point)
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 4)
	assert.Len(t, assignments, len(vectors))
}

// TestKMeans_Cluster_FewerDistinctPointsThanK pins that clustering terminates
// deterministically when the dataset has far fewer distinct positions than
// requested centroids -- the totalWeight==0 branch of k-means++ initialisation
// fires for most of the run, and must still return exactly NumCentroids
// centroids rather than silently returning fewer once it runs out of points
// that are not yet "chosen".
func TestKMeans_Cluster_FewerDistinctPointsThanK(t *testing.T) {
	config := &Config{
		NumCentroids:   10,
		Dimensions:     4,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Only 3 distinct positions repeated across 60 vectors, requesting 10
	// centroids.
	distinct := [][]float32{
		{0, 0, 0, 0},
		{10, 10, 10, 10},
		{20, 20, 20, 20},
	}
	vectors := make([][]float32, 60)
	for i := range vectors {
		vectors[i] = copyVector(distinct[i%len(distinct)])
	}

	// Assert directly on the INITIALIZER's own output, not merely on
	// Cluster()'s. recomputeCentroids allocates a full NumCentroids-length
	// slice on its very first call regardless of how many centroids
	// initialization actually produced, so a short slice returned by
	// initializeKMeansPlusPlus is invisible by the time Cluster returns --
	// the refinement step silently repairs the count before any assertion
	// downstream of it could observe the regression this pins. Reverting to
	// the parent implementation (the totalWeight==0 branch scanning for an
	// "unchosen" point instead of drawing uniformly) makes exactly this
	// assertion fail: with only 3 distinct positions, that scan runs out of
	// unchosen points and initializeKMeansPlusPlus returns roughly 3
	// centroids instead of the requested 10.
	km := NewKMeans(config)
	initial, err := km.initializeKMeansPlusPlus(vectors)
	require.NoError(t, err)
	assert.Len(t, initial, 10)

	// Cluster() itself must still complete end to end and hand back a full,
	// usable clustering built on top of that initialization.
	km2 := NewKMeans(config)
	centroids, assignments, err := km2.Cluster(vectors)
	require.NoError(t, err)
	assert.Len(t, centroids, 10)
	assert.Len(t, assignments, len(vectors))
}

// referenceKMeansPlusPlus is the pre-go-qrdb#565 k-means++ initializer's
// SELECTION shape, unchanged: for every candidate point it rescans every
// already-chosen centroid from scratch and takes the minimum of the RAW
// distanceFunc output before squaring it into a sampling weight. It is
// deliberately O(n*k^2) -- that cost is exactly what made the optimized
// initializeKMeansPlusPlus necessary -- and exists ONLY as a test oracle: an
// independent, obviously-correct-by-construction implementation to compare
// the optimized one against.
//
// Its weight/totalWeight/target/cumulative arithmetic is float64, matching
// initializeKMeansPlusPlus's own (later) overflow fix -- see squaredWeight's
// doc comment -- rather than the float32 arithmetic the original shipped
// with. Comparing raw-before-square is the property this test exists to pin;
// the arithmetic width is not, and leaving this oracle in float32 while
// production moved to float64 would make the two diverge on ordinary
// floating-point rounding in the weighted draw, for reasons unrelated to
// either bug this test guards against.
func referenceKMeansPlusPlus(rng *rand.Rand, vectors [][]float32, numCentroids int, distFunc DistanceFunc) [][]float32 {
	centroids := make([][]float32, 0, numCentroids)

	firstIdx := rng.Intn(len(vectors))
	centroids = append(centroids, copyVector(vectors[firstIdx]))

	weights := make([]float64, len(vectors))
	for k := 1; k < numCentroids; k++ {
		var totalWeight float64
		for i, vec := range vectors {
			minDist := float32(math.MaxFloat32)
			for _, centroid := range centroids {
				if d := distFunc(vec, centroid); d < minDist {
					minDist = d
				}
			}
			weights[i] = squaredWeight(minDist)
			totalWeight += weights[i]
		}

		if totalWeight == 0 {
			nextIdx := rng.Intn(len(vectors))
			centroids = append(centroids, copyVector(vectors[nextIdx]))
			continue
		}

		target := rng.Float64() * totalWeight
		cumulative := float64(0)
		nextIdx := len(vectors) - 1
		for i, weight := range weights {
			cumulative += weight
			if cumulative >= target {
				nextIdx = i
				break
			}
		}
		centroids = append(centroids, copyVector(vectors[nextIdx]))
	}

	return centroids
}

// TestKMeans_InitializeKMeansPlusPlus_SignedMetricWeights pins that
// initializeKMeansPlusPlus's incremental minimum-distance tracking produces
// the IDENTICAL selection distribution as referenceKMeansPlusPlus for a
// SIGNED metric (DotProductDistance, where a MORE NEGATIVE raw distance is
// CLOSER -- see its doc comment).
//
// The two implementations consume km.rng in the exact same sequence --
// Intn once for the first centroid, then one Float64 per subsequent
// centroid -- PROVIDED the zero-weight fallback never fires, which the
// dataset below (eight vectors of widely varying magnitude and direction,
// all pairwise distinct) guarantees. Given that, the two produce
// bit-identical centroid sequences, seed for seed, if and only if they
// compute the same weight for every point at every step -- so an
// implementation that compared min(d^2) instead of min(d)^2 (the bug this
// pins: -10 and 3 have min(d)=-10, weight 100, but min(d^2) picks 3,
// weight 9) would diverge from the reference on some seed in this range.
// Verified over 2,000 seeds with zero divergences.
func TestKMeans_InitializeKMeansPlusPlus_SignedMetricWeights(t *testing.T) {
	vectors := [][]float32{
		{100, 0}, {-100, 0},
		{1, 0}, {-1, 0},
		{0, 50}, {0, 1},
		{50, 50}, {-50, -50},
	}
	const numCentroids = 4
	distFunc := GetDistanceFunc(DotProductDistance)

	for seed := int64(1); seed <= 2000; seed++ {
		config := &Config{
			NumCentroids:   numCentroids,
			Dimensions:     2,
			MaxIterations:  25,
			Convergence:    0.0001,
			InitMethod:     "kmeans++",
			DefaultNProbe:  1,
			DistanceMetric: DotProductDistance,
			Seed:           seed,
		}

		km := NewKMeans(config)
		got, err := km.initializeKMeansPlusPlus(vectors)
		require.NoError(t, err)

		// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
		want := referenceKMeansPlusPlus(rand.New(rand.NewSource(seed)), vectors, numCentroids, distFunc)

		require.Lenf(t, got, len(want), "seed %d", seed)
		for i := range want {
			assert.Equalf(t, want[i], got[i], "seed %d, centroid %d", seed, i)
		}
	}
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

// TestClampedMin_OverflowAndNaN pins clampedMin's clamping behaviour
// directly, deterministically, with no dependence on RNG state: a candidate
// that is +Inf, -Inf or NaN must never be compared AS ITSELF -- each is
// clamped to a finite bound (+/-math.MaxFloat32, or math.MaxFloat32 for NaN)
// first, and only the clamped value is ever compared against current via
// "<". Both infinities are overflow artifacts here, not just +Inf: a signed
// metric's raw distance (DotProductDistance) can overflow to -Inf from
// finite inputs just as validly as it can overflow to +Inf (see
// TestKMeans_InitializeKMeansPlusPlus_DotProductOverflow), and an unclamped
// -Inf would compare less than every finite value -- including this
// function's own sentinel -- as if it were a genuinely closer point, when
// squaredWeight would then square it right back into +Inf.
func TestClampedMin_OverflowAndNaN(t *testing.T) {
	sentinel := float32(math.MaxFloat32)
	posInf := float32(math.Inf(1))
	negInf := float32(math.Inf(-1))
	nan := float32(math.NaN())

	tests := []struct {
		name      string
		current   float32
		candidate float32
		want      float32
	}{
		{"finite candidate replaces sentinel", sentinel, 5, 5},
		{"finite candidate replaces a smaller running minimum only if smaller", float32(5), float32(10), float32(5)},
		{"finite candidate replaces a larger running minimum", float32(10), float32(5), float32(5)},
		{"+Inf candidate never replaces the sentinel", sentinel, posInf, sentinel},
		{"+Inf candidate never replaces a finite running minimum", float32(5), posInf, float32(5)},
		{"NaN candidate never replaces the sentinel", sentinel, nan, sentinel},
		{"NaN candidate never replaces a finite running minimum", float32(5), nan, float32(5)},
		{"-Inf candidate is clamped to -MaxFloat32, not accepted as a literal -Inf minimum", sentinel, negInf, -sentinel},
		{"a clamped -MaxFloat32 candidate still legitimately replaces a less-negative running minimum", float32(-1000), negInf, -sentinel},
		{"-Inf candidate does not replace an already-more-negative running minimum", -sentinel, negInf, -sentinel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, clampedMin(tt.current, tt.candidate))
		})
	}

	// Contrast against the shape this replaces: an unconditional assignment
	// (`minDist[i] = d`) with no "<" comparison at all, which is what
	// initializeKMeansPlusPlus's very first per-vector precompute did before
	// this fix. That shape takes an overflowing (or NaN) distanceFunc result
	// unchanged, rather than clamping it to whatever was already known.
	unconditionalAssign := func(_, candidate float32) float32 { return candidate }
	assert.NotEqual(t, sentinel, unconditionalAssign(sentinel, posInf),
		"the parent shape (direct assignment) lets +Inf through uncontested")
	require.True(t, math.IsInf(float64(unconditionalAssign(sentinel, posInf)), 1))
	assert.NotEqual(t, unconditionalAssign(sentinel, posInf), clampedMin(sentinel, posInf))

	// Contrast against clamping +Inf only (the version this round's review
	// caught): -Inf left unclamped would be accepted as the new minimum
	// here, and squaredWeight would turn it right back into +Inf.
	clampPositiveOnly := func(current, candidate float32) float32 {
		if math.IsInf(float64(candidate), 1) || math.IsNaN(float64(candidate)) {
			candidate = math.MaxFloat32
		}
		if candidate < current {
			return candidate
		}
		return current
	}
	halfClamped := clampPositiveOnly(sentinel, negInf)
	assert.True(t, math.IsInf(float64(halfClamped), -1),
		"clamping +Inf alone leaves -Inf uncontested")
	assert.True(t, math.IsInf(squaredWeight(halfClamped), 1),
		"squaring an unclamped -Inf reproduces the overflow this fix exists to prevent")
	assert.False(t, math.IsInf(squaredWeight(clampedMin(sentinel, negInf)), 0),
		"squaring clampedMin's actual (fully clamped) result must not overflow")
}

// TestSquaredWeight_LargeFiniteDistanceNoOverflow pins that squaredWeight
// does NOT reproduce the overflow clampedMin exists to prevent: clampedMin
// only stops an already-`+Inf`/NaN DISTANCE from replacing a smaller one, it
// does nothing to stop SQUARING a large-but-finite value -- including its own
// sentinel, math.MaxFloat32 -- from overflowing float32's ~3.4e38 range.
// Every case here is a value that DOES overflow when squared in plain
// float32 arithmetic (asserted as a test-setup precondition), and must NOT
// overflow through squaredWeight.
func TestSquaredWeight_LargeFiniteDistanceNoOverflow(t *testing.T) {
	tests := []struct {
		name string
		d    float32
	}{
		{"an ordinary large finite distance", 3e19},
		{"the clampedMin sentinel itself", float32(math.MaxFloat32)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, math.IsInf(float64(tt.d*tt.d), 0),
				"test setup: %v must overflow when squared in float32 for this test to prove anything", tt.d)

			got := squaredWeight(tt.d)
			assert.False(t, math.IsInf(got, 0), "squaredWeight overflowed to +/-Inf")
			assert.False(t, math.IsNaN(got), "squaredWeight produced NaN")
			assert.Greater(t, got, 0.0)
		})
	}
}

// TestKMeans_DotProductOverflow_ClampedToFiniteWeight pins the specific
// failure this round's review caught: DotProductDistance's raw output is
// signed, and its own float32 accumulation overflows to -Inf for two
// well-aligned, sufficiently large-magnitude but otherwise perfectly finite
// vectors -- not merely a hypothetical -Inf, but one produced by the actual
// distance function initializeKMeansPlusPlus calls. A -Inf raw distance
// compares less than every finite value, including clampedMin's own
// sentinel, so leaving it unclamped would accept it as the new minimum and
// squaredWeight would square it right back into +Inf.
func TestKMeans_DotProductOverflow_ClampedToFiniteWeight(t *testing.T) {
	huge := float32(1e20)
	a := []float32{huge, 0}
	b := []float32{huge, 0} // identical direction and magnitude: maximally "close"

	raw := DotProduct(a, b)
	require.True(t, math.IsInf(float64(raw), -1),
		"test setup: DotProduct(a, b) must overflow to -Inf for this dataset")

	clamped := clampedMin(float32(math.MaxFloat32), raw)
	assert.False(t, math.IsInf(float64(clamped), 0), "clampedMin must clamp the -Inf raw distance to a finite bound")

	weight := squaredWeight(clamped)
	assert.False(t, math.IsInf(weight, 0), "the resulting k-means++ sampling weight must not be +/-Inf")
	assert.False(t, math.IsNaN(weight))
	assert.Greater(t, weight, 0.0)
}

// TestKMeans_InitializeKMeansPlusPlus_DotProductOverflow runs the full
// initializer end to end against a dataset that mixes an overflowing
// DotProduct pair with ordinary points, as a regression net alongside the
// precise unit-level proof above: it must complete without hanging or
// panicking and return exactly NumCentroids centroids built only from the
// input's own (large but FINITE) values, across many seeds.
func TestKMeans_InitializeKMeansPlusPlus_DotProductOverflow(t *testing.T) {
	huge := float32(1e20)
	vectors := [][]float32{
		{huge, 0}, {huge, 0}, {-huge, 0}, {-huge, 0},
		{1, 0}, {0, 1}, {-1, 0}, {0, -1},
	}
	require.True(t, math.IsInf(float64(DotProduct(vectors[0], vectors[1])), -1),
		"test setup: this dataset must overflow DotProduct for this test to prove anything")

	for seed := int64(1); seed <= 200; seed++ {
		config := &Config{
			NumCentroids:   3,
			Dimensions:     2,
			MaxIterations:  25,
			Convergence:    0.0001,
			InitMethod:     "kmeans++",
			DefaultNProbe:  1,
			DistanceMetric: DotProductDistance,
			Seed:           seed,
		}
		km := NewKMeans(config)
		centroids, err := km.initializeKMeansPlusPlus(vectors)
		require.NoError(t, err)
		require.Len(t, centroids, 3)

		for _, c := range centroids {
			for _, v := range c {
				assert.False(t, math.IsInf(float64(v), 0), "seed %d: centroid component is +/-Inf", seed)
				assert.False(t, math.IsNaN(float64(v)), "seed %d: centroid component is NaN", seed)
			}
		}
	}
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
			// #nosec G404 -- test fixture vectors only; no key, nonce, token or identifier derives from these values.
			vectors[i][j] = rand.Float32()*2 - 1 // Range [-1, 1]
		}
	}
	return vectors
}
