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

// Package kmeans holds k-means clustering logic shared by every ANN index
// family under pkg/vector that runs its own k-means or k-means++ (currently
// pkg/vector/ivf and pkg/vector/quantization), so it does not exist to be
// imported by anything outside pkg/vector. It imports nothing from this
// repository -- only the standard library -- so nothing importing it can
// create a cycle back here.
package kmeans

import "math"

// RepairEmptyClusters fixes clusters that finished a refinement pass with no
// vectors assigned to them, by moving the globally farthest displaceable
// point into each empty one in turn. Left unrepaired, an empty cluster's
// centroid is whatever k-means++ initialisation (or the caller's own
// empty-cluster handling) last left it as -- commonly a duplicate of another
// chosen centroid on degenerate input (fewer distinct points than the
// requested cluster count), which wastes a cluster slot for the life of the
// trained model.
//
// The donor search considers EVERY cluster with more than one member, not
// only the largest one. Restricting the search to the single largest cluster
// is a real bug, not a simplification: a cluster can be large in COUNT while
// every one of its points sits exactly on its own centroid (a mass of exact
// duplicates), in which case every distance in it is zero and the largest
// cluster has nothing usable to donate -- even though a SMALLER cluster
// elsewhere holds a point that is genuinely far from its own centroid and
// would happily fill the empty one. Scanning globally for the single
// farthest point, across every splittable cluster, finds that donor instead
// of giving up. See TestRepairEmptyClusters_LargestClusterIsDegenerate for
// exactly this shape.
//
// vectors, assignments and centroids must all describe the same clustering:
// len(assignments) == len(vectors), one assignment per vector, and
// clusterSizes and centroids each have exactly one entry per cluster.
// clusterSizes must accurately reflect assignments (each clusterSizes[c] is
// the count of vectors with assignments[j] == c) -- callers whose refinement
// loop hands over sizes that predate the returned centroids on purpose (see
// pkg/vector/ivf's own doc comment on this) still satisfy this precondition:
// it is assignments and clusterSizes that must agree with EACH OTHER, not
// centroids with either.
//
// distFunc need not be a true metric, correctly scaled, or even
// non-negative: this only ever compares its outputs to find a maximum, so a
// squared distance (cheap: no square root) works exactly as well as its
// unsquared counterpart, and a caller already computing squared distances
// elsewhere should keep doing so here rather than take an unnecessary square
// root. A SIGNED distFunc -- such as pkg/vector/ivf's DotProductDistance,
// where a MORE NEGATIVE value is CLOSER -- is also supported: the search
// below tracks the maximum by comparing candidates against each other, never
// against a fixed zero threshold, so it works whether every candidate
// distance is positive, negative, or a mix. See
// TestRepairEmptyClusters_SignedDistanceFunction.
//
// distFunc's output is also not assumed finite. ivf's configurable distance
// functions (Cosine in particular) can return NaN for large-but-finite
// input -- dividing one float32 overflow by another -- and any distFunc,
// squared or not, can overflow to +/-Inf the same way clampedMin's own doc
// comment describes for the callers of this package. A NaN candidate is
// skipped outright: NaN cannot be meaningfully compared as "farther" or
// "nearer" than anything, and accepting one as the running maximum would
// poison every later comparison (dist > NaN and NaN > dist are both always
// false in IEEE 754, so nothing could ever replace it). An Inf candidate is
// clamped to the same finite sentinel bound (+/-math.MaxFloat32) clampedMin
// uses, so it can still be selected as the farthest point when it genuinely
// is, without an unbounded value propagating any further than that. See
// TestRepairEmptyClusters_SkipsNaNCandidates and
// TestRepairEmptyClusters_ClampsInfiniteCandidates.
//
// assignments and clusterSizes are mutated in place. centroids is mutated
// and also returned, so a caller may use either the return value or the
// slice it passed in.
func RepairEmptyClusters(vectors [][]float32, assignments []int, clusterSizes []int, centroids [][]float32, distFunc func(a, b []float32) float32) [][]float32 {
	for i, size := range clusterSizes {
		if size != 0 {
			continue
		}

		// Find the single farthest point across EVERY cluster that can
		// spare one, as clusterSizes stands AT THIS POINT in the loop -- a
		// donation to an earlier empty cluster in this same pass has
		// already lowered its donor's size by the time a later empty
		// cluster looks for one.
		//
		// maxDist is seeded from the FIRST eligible, finite candidate, not a
		// fixed zero: distFunc's output is not guaranteed non-negative (a
		// signed metric can make every real distance negative), so comparing
		// against a zero-initialised threshold would silently reject every
		// candidate and report no donor even when a genuine one exists.
		var maxDist float32
		farthestIdx := -1
		for j, vec := range vectors {
			donor := assignments[j]
			// A cluster of size 1 has nothing to spare without creating a
			// new empty cluster in its place.
			if clusterSizes[donor] <= 1 {
				continue
			}
			// A vector whose value is identical to its own centroid cannot
			// seed a new distinct centroid: moving it would only duplicate a
			// value that already exists. It is excluded from the search here,
			// rather than rejected after the search, so a displaceable
			// candidate elsewhere still wins even when an identical one
			// scores as "farthest" under the metric -- which a signed metric
			// such as DotProductDistance can do.
			if vectorEqual(vec, centroids[donor]) {
				continue
			}

			dist := distFunc(vec, centroids[donor])
			switch {
			case math.IsNaN(float64(dist)):
				// Not a usable candidate at all: skip it rather than let it
				// become (or block replacing) the running maximum.
				continue
			case math.IsInf(float64(dist), 1):
				dist = math.MaxFloat32
			case math.IsInf(float64(dist), -1):
				dist = -math.MaxFloat32
			}

			if farthestIdx < 0 || dist > maxDist {
				maxDist = dist
				farthestIdx = j
			}
		}
		// No candidate at all -- every splittable cluster is made of vectors
		// identical to their own centroid (the totally-degenerate case, see
		// TestRepairEmptyClusters_CannotExceedTheDistinctPointsAvailable), or
		// every distance was NaN. Donating would only move a duplicate value,
		// churning bookkeeping without growing the set of distinct
		// centroids, so the empty cluster keeps the centroid it has.
		if farthestIdx < 0 {
			continue
		}
		donor := assignments[farthestIdx]

		// Move it into the empty cluster.
		centroids[i] = append([]float32(nil), vectors[farthestIdx]...)
		assignments[farthestIdx] = i
		clusterSizes[i] = 1
		clusterSizes[donor]--
	}

	return centroids
}

// vectorEqual reports whether a and b hold identical components. Used above
// to detect a donation that would not create a new distinct centroid value,
// independently of whatever distFunc's output happens to mean.
func vectorEqual(a, b []float32) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
