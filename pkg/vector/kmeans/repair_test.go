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

package kmeans

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// l2Squared is a minimal, package-local squared-L2 distance -- deliberately
// not imported from anywhere, since this package has no imports of its own
// (see the package doc comment) and its tests should not need any either.
func l2Squared(a, b []float32) float32 {
	var sum float32
	for i := range a {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// TestRepairEmptyClusters_YieldsKDistinctCentroidsWhereDataAllows is the
// direct proof that this repair does what it exists to do: degenerate
// clustering state -- one cluster with no assigned vectors -- is corrected
// into k distinct centroids, PROVIDED the input data actually contains k
// distinct positions for it to draw from. Both callers of this package
// (pkg/vector/ivf and pkg/vector/quantization) can reach this same starting
// state: a k-means++ initialization or refinement step left one cluster
// with a duplicate (or otherwise unclaimed) centroid despite there being a
// genuinely distinct, unclaimed point available in the training data.
func TestRepairEmptyClusters_YieldsKDistinctCentroidsWhereDataAllows(t *testing.T) {
	// 3 well-separated groups plus one FOURTH distinct, unclaimed point
	// (vectors[6]) that no cluster has claimed -- vectors[6] is what the
	// empty cluster 3 should end up donated.
	vectors := [][]float32{
		{0, 0}, {0.1, 0.1}, // cluster 0
		{10, 10}, {10.1, 10.1}, // cluster 1
		{20, 20}, {20.1, 20.1}, // cluster 2
		{30, 30}, // the farthest point in cluster 2, about to be donated
	}
	// vectors[6] was folded into cluster 2 by an assignment step that never
	// gave cluster 3 a point of its own -- exactly what a degenerate
	// k-means++ initialization (or an unlucky Lloyd iteration) leaves
	// behind.
	assignments := []int{0, 0, 1, 1, 2, 2, 2}
	clusterSizes := []int{2, 2, 3, 0}
	centroids := [][]float32{
		{0.05, 0.05},
		{10.05, 10.05},
		{20.0333, 20.0333}, // cluster 2's mean, pulled toward vectors[6]
		{20.0333, 20.0333}, // cluster 3: a DUPLICATE of cluster 2's centroid
	}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, l2Squared)

	require.Len(t, result, 4)

	distinct := map[[2]float32]bool{}
	for _, c := range result {
		require.Len(t, c, 2)
		distinct[[2]float32{c[0], c[1]}] = true
	}
	assert.Len(t, distinct, 4, "4 distinct positions were available in the data, so the repair must produce 4 distinct centroids: got %v", result)

	// The specific mechanism: the farthest point of the largest cluster (2,
	// with 3 points) was donated to the empty one (3).
	assert.Equal(t, []float32{30, 30}, result[3])
	assert.Equal(t, 3, assignments[6])
	assert.Equal(t, []int{2, 2, 2, 1}, clusterSizes)
}

// TestRepairEmptyClusters_CannotExceedTheDistinctPointsAvailable is the
// converse of the test above, and equally load-bearing: when the training
// data itself has FEWER distinct positions than clusters, no repair can
// manufacture a distinct position that does not exist. This is not a defect
// in the repair -- it is the boundary the "where distinct points allow"
// qualifier exists to name.
//
// DESIGN DECISION: the search below does find a "farthest" candidate here --
// every candidate ties at distance 0 to its own centroid, and ties are
// broken by scanning order, not by a distance-must-exceed-zero threshold
// (see RepairEmptyClusters's doc comment on why that threshold does not
// exist) -- but that candidate's VALUE is {0, 0}, identical to the centroid
// it came from. RepairEmptyClusters deliberately SKIPS the donation in this
// case rather than performing it: moving a vector whose value already equals
// its origin centroid cannot create a new distinct centroid, so doing it
// anyway would be a pure bookkeeping mutation (reassigning which vector
// nominally belongs to which cluster) with no effect on the SET of distinct
// centroids -- the only thing this repair exists to grow. The alternative
// (donate anyway) is not wrong, exactly -- with every candidate identical,
// any assignment is equally "valid" -- but it is pointless churn this
// package chooses not to perform. This test pins that choice precisely: NOT
// just that the distinct-value count stays at 1 (true either way), but that
// assignments and clusterSizes are completely untouched.
func TestRepairEmptyClusters_CannotExceedTheDistinctPointsAvailable(t *testing.T) {
	vectors := [][]float32{
		{0, 0}, {0, 0}, {0, 0}, {0, 0}, // every vector is the SAME single point
	}
	assignments := []int{0, 0, 0, 0}
	clusterSizes := []int{4, 0}
	centroids := [][]float32{
		{0, 0},
		{0, 0}, // empty, and a duplicate: there is nowhere else to look
	}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, l2Squared)

	require.Len(t, result, 2)
	assert.Equal(t, []float32{0, 0}, result[1])
	assert.Equal(t, []int{0, 0, 0, 0}, assignments, "no vector should have been reassigned: the only candidate is value-identical to its own centroid")
	assert.Equal(t, []int{4, 0}, clusterSizes, "cluster sizes must be untouched: donating a value-identical point is skipped, not performed")
}

// TestRepairEmptyClusters_LargestClusterIsDegenerate pins the bug an earlier
// version of this function had: the donor search only considered the SINGLE
// LARGEST cluster. Cluster 0 here is the largest by count (3 points) but
// every one of those points is an exact duplicate sitting exactly on its own
// centroid -- every distance in it is zero -- while cluster 1, though
// smaller (2 points), holds two genuinely distinct points with a real,
// positive distance to their own centroid. The old version picked cluster 0
// (largest), found no usable donor there, and gave up without ever looking
// at cluster 1 -- leaving cluster 2 a duplicate despite 3 distinct positions
// being available in the data. Scanning every splittable cluster, not only
// the largest, finds cluster 1's farthest point instead.
func TestRepairEmptyClusters_LargestClusterIsDegenerate(t *testing.T) {
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

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, l2Squared)

	require.Len(t, result, 3)
	distinct := map[[2]float32]bool{}
	for _, c := range result {
		distinct[[2]float32{c[0], c[1]}] = true
	}
	assert.Len(t, distinct, 3, "3 distinct positions were available, so the repair must produce 3 distinct centroids: got %v", result)

	// The specific mechanism: cluster 1 (smaller, but non-degenerate) donates
	// its farthest point, not cluster 0 (larger, but every point is a
	// zero-distance duplicate).
	assert.NotEqual(t, []float32{-1, -1}, result[2])
	assert.NotEqual(t, []float32{0, 0}, result[2], "must not have tried to donate from the degenerate cluster 0")
	assert.Equal(t, []int{3, 1, 1}, clusterSizes)
}

// TestRepairEmptyClusters_SignedDistanceFunction pins the bug a second
// earlier version of this function had: the donor search seeded maxDist at
// a fixed 0 and only accepted a candidate whose distance was strictly
// greater than that -- an unstated assumption that distFunc never returns a
// negative value. pkg/vector/ivf's DotProductDistance breaks that assumption
// outright: it is signed, and a MORE NEGATIVE value is CLOSER (see its doc
// comment), so a cluster made up entirely of reasonably close points can
// have every one of its distances negative. Against a zero-seeded threshold,
// none of them is ever "> 0", so the old code reported no donor even though
// a real one -- the least-negative, i.e. genuinely farthest, point -- was
// sitting right there.
//
// signedDist below mirrors that shape without needing the real
// DotProductDistance: it is l2Squared shifted down by a constant large
// enough that every distance produced here is negative, while preserving
// l2Squared's own ordering (so the "farthest" point in the signed sense is
// exactly the farthest point in the ordinary sense).
func TestRepairEmptyClusters_SignedDistanceFunction(t *testing.T) {
	signedDist := func(a, b []float32) float32 {
		return l2Squared(a, b) - 1000 // every real distance here becomes negative
	}

	vectors := [][]float32{
		{0, 0}, {1, 0}, {5, 0}, // cluster 0: 3 points at increasing distance
	}
	assignments := []int{0, 0, 0}
	clusterSizes := []int{3, 0}
	centroids := [][]float32{
		{0, 0},
		{-1, -1}, // empty
	}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, signedDist)

	require.Len(t, result, 2)
	require.NotEqual(t, []float32{-1, -1}, result[1],
		"a genuine donor existed (vectors[2], signed distance -975) but a zero-seeded threshold rejects every negative distance")
	// {5, 0} is farthest from centroid 0 in the ordinary sense, and signedDist
	// preserves that ordering (it is order-preserving relative to l2Squared,
	// just shifted), so it is exactly the point the repair should select.
	assert.Equal(t, []float32{5, 0}, result[1])
}

// cosineLike is pkg/vector/ivf's Cosine distance function, copied rather
// than imported (this package has no imports of its own besides the
// standard library -- see the package doc comment). It is included here,
// not a synthetic NaN literal, so TestRepairEmptyClusters_SkipsNaNCandidates
// exercises the exact computation ivf's own configurable distance function
// performs, not a stand-in for it.
func cosineLike(a, b []float32) float32 {
	var dotProduct, normA, normB float32
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 1.0
	}
	similarity := dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
	return 1.0 - similarity
}

// TestRepairEmptyClusters_SkipsNaNCandidates pins the third bug review
// caught: the donor search accepted the FIRST eligible candidate's distance
// unconditionally (via "farthestIdx < 0"), including a NaN one, and then
// never let anything replace it -- IEEE 754 defines both "x > NaN" and
// "NaN > x" as false, so once maxDist is NaN, no later comparison can ever
// be true again. cosineLike({1e20, 0}, {5e19, 0}) produces exactly this NaN:
// dividing one float32 overflow (the dot product) by another (the norm
// product) is Inf/Inf, which is NaN -- from perfectly finite input, the same
// shape pkg/vector/ivf's real Cosine distance can produce. vectors[0] is
// scanned first and hits this NaN; vectors[1] is an ordinary finite
// candidate. A search that does not skip the NaN candidate donates
// vectors[0] regardless -- the wrong point, and in general an unusable one.
func TestRepairEmptyClusters_SkipsNaNCandidates(t *testing.T) {
	huge, mid := float32(1e20), float32(5e19)
	require.True(t, math.IsNaN(float64(cosineLike([]float32{huge, 0}, []float32{mid, 0}))),
		"test setup: this input must reproduce Cosine's NaN overflow for this test to prove anything")

	vectors := [][]float32{
		{huge, 0}, // scanned first: distance to the centroid below is NaN
		{1, 0},    // ordinary finite candidate
	}
	assignments := []int{0, 0}
	clusterSizes := []int{2, 0}
	centroids := [][]float32{
		{mid, 0},
		{-1, -1}, // empty
	}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, cosineLike)

	require.Len(t, result, 2)
	require.NotEqual(t, []float32{-1, -1}, result[1],
		"a genuine finite donor existed (vectors[1]) but an unskipped NaN candidate blocks every later comparison")
	assert.False(t, math.IsNaN(float64(result[1][0])), "must not have donated the NaN-distance candidate")
	assert.Equal(t, []float32{1, 0}, result[1])
}

// dotProductLike is pkg/vector/ivf's DotProductDistance function, copied for
// the same reason cosineLike is above. Unlike Cosine, it can overflow to a
// clean +Inf or -Inf (never NaN) for aligned, large-magnitude finite input,
// since it never divides one overflow by another.
func dotProductLike(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return -sum
}

// TestRepairEmptyClusters_ClampsInfiniteCandidates pins that an Inf
// candidate -- distinct from the NaN case above -- is clamped to a finite
// sentinel rather than left as a literal infinity. dotProductLike({1e20,
// 0}, {1e20, 0}) overflows its sum to +Inf, so the distance (its negation)
// is -Inf: DotProductDistance is signed, and a MORE NEGATIVE value is
// CLOSER, so -Inf reads as "impossibly close" -- the opposite of "farthest"
// -- and must not win the search just because -Inf compares less than
// nothing while ALSO evading the "is this candidate even finite" check a
// less careful clamp might apply only to +Inf.
func TestRepairEmptyClusters_ClampsInfiniteCandidates(t *testing.T) {
	huge := float32(1e20)
	require.True(t, math.IsInf(float64(dotProductLike([]float32{huge, 0}, []float32{huge, 0})), -1),
		"test setup: this input must reproduce DotProductDistance's -Inf overflow for this test to prove anything")
	require.True(t, math.IsInf(float64(dotProductLike([]float32{-huge, 0}, []float32{huge, 0})), 1),
		"test setup: this input must reproduce DotProductDistance's +Inf overflow for this test to prove anything")

	vectors := [][]float32{
		{huge, 0},  // scanned first: distance to the centroid is -Inf (== "impossibly close", not "farthest")
		{-huge, 0}, // distance to the centroid is +Inf (== "impossibly far": genuinely the farthest, anti-aligned)
		{1, 0},     // ordinary finite candidate, in between the two clamped extremes
	}
	assignments := []int{0, 0, 0}
	clusterSizes := []int{3, 0}
	centroids := [][]float32{
		{huge, 0},
		{-1, -1}, // empty
	}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, dotProductLike)

	require.Len(t, result, 2)
	for _, v := range result[1] {
		assert.False(t, math.IsInf(float64(v), 0), "must not have propagated a literal infinity into a centroid value")
	}
	// The clamped +Inf candidate (vectors[1], -huge,0) becomes +math.MaxFloat32,
	// which is larger than both the clamped -Inf candidate (-math.MaxFloat32)
	// and the ordinary finite one (dotProductLike(vectors[2], centroids[0]) =
	// -1e20), so it is correctly selected as the farthest.
	assert.Equal(t, []float32{-huge, 0}, result[1])
}

// scriptedDist returns the distance scripted for a vector, keyed by its first
// component, so a test can place exact values -- including +/-Inf and the
// finite +/-math.MaxFloat32 bounds -- at chosen scan positions.
func scriptedDist(script map[float32]float32) func(a, b []float32) float32 {
	return func(a, _ []float32) float32 { return script[a[0]] }
}

// TestRepairEmptyClusters_ClampTiesInfinityWithTheFiniteBound is the
// discriminating half of the clamp: ordering alone cannot see it (-Inf < any
// finite < +Inf holds with or without clamping), but a TIE can. Clamped, a
// +Inf candidate equals an earlier +MaxFloat32 one, and the earlier candidate
// keeps the maximum (strict >). Unclamped, +Inf would strictly exceed it and
// win. Removing the +Inf clamp branch makes this test fail.
func TestRepairEmptyClusters_ClampTiesInfinityWithTheFiniteBound(t *testing.T) {
	vectors := [][]float32{{1, 0}, {2, 0}}
	assignments := []int{0, 0}
	clusterSizes := []int{2, 0}
	centroids := [][]float32{{9, 9}, {-1, -1}}
	dist := scriptedDist(map[float32]float32{
		1: math.MaxFloat32,      // scanned first: the finite upper bound
		2: float32(math.Inf(1)), // clamps to exactly the same bound
	})

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, dist)

	assert.Equal(t, []float32{1, 0}, result[1], "a clamped +Inf must tie with, not beat, an earlier MaxFloat32")
	assert.Equal(t, []int{1, 0}, assignments)
	assert.Equal(t, []int{1, 1}, clusterSizes)
}

// TestRepairEmptyClusters_ClampTiesNegativeInfinityWithTheFiniteBound is the
// -Inf mirror: -Inf scanned FIRST seeds the maximum; a later -MaxFloat32
// candidate ties with it only if -Inf was clamped. Clamped, the first keeps
// the maximum; unclamped, -MaxFloat32 > -Inf would replace it. Removing the
// -Inf clamp branch makes this test fail.
func TestRepairEmptyClusters_ClampTiesNegativeInfinityWithTheFiniteBound(t *testing.T) {
	vectors := [][]float32{{1, 0}, {2, 0}}
	assignments := []int{0, 0}
	clusterSizes := []int{2, 0}
	centroids := [][]float32{{9, 9}, {-1, -1}}
	dist := scriptedDist(map[float32]float32{
		1: float32(math.Inf(-1)), // scanned first: clamps to -MaxFloat32
		2: -math.MaxFloat32,      // ties only with a clamped -Inf
	})

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, dist)

	assert.Equal(t, []float32{1, 0}, result[1], "a clamped -Inf must tie with a later -MaxFloat32")
}

// TestRepairEmptyClusters_IdenticalCandidateDoesNotBlockAnotherDonor pins that
// a vector identical to its own centroid is excluded from the search rather
// than aborting the donation. Under a signed metric the identical vectors can
// score as "farthest", but moving one would only duplicate an existing value;
// a genuinely displaceable vector in another cluster must still be donated.
func TestRepairEmptyClusters_IdenticalCandidateDoesNotBlockAnotherDonor(t *testing.T) {
	vectors := [][]float32{{0, 0}, {0, 0}, {10, 0}, {20, 0}}
	assignments := []int{0, 0, 1, 1}
	clusterSizes := []int{2, 2, 0}
	centroids := [][]float32{{0, 0}, {15, 0}, {0, 0}}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, dotProductLike)

	require.Len(t, result, 3)
	assert.Contains(t, [][]float32{{10, 0}, {20, 0}}, result[2], "the donor must come from the displaceable cluster")
	assert.Equal(t, 1, clusterSizes[2])
	assert.Equal(t, 1, clusterSizes[1])
	assert.Equal(t, 2, clusterSizes[0], "the identical-valued cluster must be left alone")
}

// TestRepairEmptyClusters_NoDonorWhenLargestClusterHasOnlyOnePoint pins the
// maxSize<=1 guard: a cluster holding a single point has nothing to spare
// without immediately creating a new empty cluster in its place, so the
// repair must leave both the donor and the empty cluster untouched.
func TestRepairEmptyClusters_NoDonorWhenLargestClusterHasOnlyOnePoint(t *testing.T) {
	vectors := [][]float32{{0, 0}, {5, 5}}
	assignments := []int{0, 1}
	clusterSizes := []int{1, 1, 0}
	centroids := [][]float32{{0, 0}, {5, 5}, {-1, -1}}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, l2Squared)

	require.Len(t, result, 3)
	assert.Equal(t, []float32{-1, -1}, result[2])
	assert.Equal(t, []int{1, 1, 0}, clusterSizes)
}

// TestRepairEmptyClusters_MultipleEmptyClustersInOnePass pins that repairing
// an earlier empty cluster's effect on clusterSizes (the donor's count drops
// by one) is visible to a LATER empty cluster's search for the largest
// donor in the SAME call, since clusterSizes is read fresh on every
// iteration of the outer loop.
func TestRepairEmptyClusters_MultipleEmptyClustersInOnePass(t *testing.T) {
	vectors := [][]float32{
		{0, 0}, {0.1, 0.1}, {0.2, 0.2}, {0.3, 0.3}, {10, 10},
	}
	assignments := []int{0, 0, 0, 0, 1}
	clusterSizes := []int{4, 1, 0, 0}
	centroids := [][]float32{
		{0.15, 0.15},
		{10, 10},
		{-1, -1},
		{-1, -1},
	}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, l2Squared)

	require.Len(t, result, 4)
	// Cluster 2 donates from cluster 0 (size 4, the largest); cluster 3 then
	// finds cluster 0 reduced to size 3, still the largest available
	// (cluster 1 has only 1), and donates from it too.
	assert.NotEqual(t, []float32{-1, -1}, result[2])
	assert.NotEqual(t, []float32{-1, -1}, result[3])
	assert.Equal(t, 1, clusterSizes[1], "the single-point cluster was never touched, since cluster 0 was always the larger donor")
}

// TestRepairEmptyClusters_MutatesAndReturnsTheSameCentroidsSlice documents
// the mutate-in-place contract: a caller may use either the return value or
// the slice it passed in.
func TestRepairEmptyClusters_MutatesAndReturnsTheSameCentroidsSlice(t *testing.T) {
	vectors := [][]float32{{0, 0}, {0.1, 0.1}, {10, 10}}
	assignments := []int{0, 0, 1}
	clusterSizes := []int{2, 1, 0}
	centroids := [][]float32{{0.05, 0.05}, {10, 10}, {-1, -1}}

	result := RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, l2Squared)

	assert.Same(t, &centroids[0], &result[0])
	assert.NotEqual(t, []float32{-1, -1}, centroids[2], "the caller's own slice is mutated, not just the return value")
}
