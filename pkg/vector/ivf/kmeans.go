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
	"math"
	"math/rand"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/kmeans"
)

// KMeans implements the k-means clustering algorithm.
type KMeans struct {
	config       *Config
	distanceFunc DistanceFunc

	// rng supplies the randomness for centroid initialisation.
	//
	// A field rather than the package-global source so a caller can make
	// clustering REPRODUCIBLE. Production leaves Config.Seed at zero and gets
	// a source seeded from the clock, which is the right default: fixed
	// initialisation across every index in a fleet would give every node the
	// same unlucky clustering on the same data.
	//
	// A test that measures clustering QUALITY needs the opposite. Recall is a
	// property of the centroid set, and Train produces ONE set shared by every
	// query — so an unlucky initialisation moves every query's result
	// together, and averaging over queries does not reduce that variance at
	// all. Only fixing the initialisation does (go-qrdb#92).
	rng *rand.Rand
}

// NewKMeans creates a new k-means clusterer.
func NewKMeans(config *Config) *KMeans {
	seed := config.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	return &KMeans{
		config:       config,
		distanceFunc: GetDistanceFunc(config.DistanceMetric),
		// #nosec G404 -- and //nolint:gosec below for the golangci-lint leg; the
		// two scanners read different directives and only one of them was here
		// before #293.
		//
		// ON THE MERITS, not by assertion: this rng draws initial centroid
		// positions for k-means. Its output is a clustering of vectors the
		// caller already supplied, so predicting the draw reveals nothing that
		// was secret and forges nothing -- there is no key, nonce, token or
		// identifier downstream of it. config.Seed exists so an index can be
		// rebuilt identically, which a CSPRNG would make impossible.
		rng: rand.New(rand.NewSource(seed)), //nolint:gosec // G404: see the note above
	}
}

// Cluster performs k-means clustering on the input vectors.
// Returns the centroids and cluster assignments.
func (km *KMeans) Cluster(vectors [][]float32) ([][]float32, []int, error) {
	if len(vectors) == 0 {
		return nil, nil, api.NewKMeansEmptyVectorSetError("kmeans.Cluster")
	}

	if len(vectors) < km.config.NumCentroids {
		return nil, nil, api.NewKMeansInsufficientVectorsError("kmeans.Cluster", km.config.NumCentroids, len(vectors))
	}

	// Every vector must match the configured dimensionality. Feeding a
	// mismatched vector through the distance funcs below does not panic --
	// L2/Cosine/DotProduct treat a length mismatch as math.MaxFloat32 -- but
	// k-means++ then squares that sentinel while accumulating totalWeight
	// (initializeKMeansPlusPlus), overflowing float32 to +Inf and putting the
	// weighted-sampling draw at the mercy of an Inf/NaN comparison instead of
	// failing loudly here.
	for i, vec := range vectors {
		if len(vec) != km.config.Dimensions {
			return nil, nil, api.NewKMeansDimensionMismatchError("kmeans.Cluster", i, len(vec), km.config.Dimensions)
		}
	}

	// Initialize centroids
	centroids, err := km.initializeCentroids(vectors)
	if err != nil {
		return nil, nil, api.NewKMeansInitError("kmeans.Cluster", err)
	}

	// Track cluster assignments
	assignments := make([]int, len(vectors))
	clusterSizes := make([]int, km.config.NumCentroids)

	// Iterative refinement
	for iter := 0; iter < km.config.MaxIterations; iter++ {
		// Assignment step: assign each vector to nearest centroid
		changed := false
		for i := 0; i < km.config.NumCentroids; i++ {
			clusterSizes[i] = 0
		}

		for i, vec := range vectors {
			nearest := km.findNearestCentroid(vec, centroids)
			if nearest != assignments[i] {
				changed = true
				assignments[i] = nearest
			}
			clusterSizes[nearest]++
		}

		// Check for convergence
		if !changed {
			break
		}

		// Update step: recompute centroids
		newCentroids, err := km.recomputeCentroids(vectors, assignments, clusterSizes)
		if err != nil {
			return nil, nil, api.NewKMeansRecomputeError("kmeans.Cluster", iter, err)
		}

		// Check for convergence by centroid movement
		maxMovement := km.maxCentroidMovement(centroids, newCentroids)
		centroids = newCentroids

		if maxMovement < km.config.Convergence {
			break
		}
	}

	// assignments/clusterSizes are handed over AS THE LOOP LEFT THEM, and that
	// is deliberate. On the !changed exit they describe the returned centroids.
	// On the other two exits — convergence by movement, and the iteration bound
	// — the update step replaced centroids after these counts were measured, so
	// they describe the PREVIOUS centroid set. Recomputing them here to match
	// the returned set is the obvious tidy-up and it was measured (go-qrdb#291)
	// before being rejected:
	//
	//	60,000 seeds, the twelve points of TestKMeans_Cluster
	//	shipped config (maxIter=100, conv=1e-4): 60,000/60,000 runs take the
	//	   !changed exit, 0 carry stale counts — recomputing changes nothing
	//	forcing config (maxIter=2, conv=0, random): 2,162 runs carry stale
	//	   counts; degenerate clusterings 16,356 shipped vs 17,394 recomputed
	//
	// Recomputing is worse by 1,038 in 60,000. A fresh recount finds no empty
	// cluster at all (1,894 -> 0), so the repair below stops firing and a
	// zero-size cluster is returned holding the all-zero vector that
	// recomputeCentroids leaves behind — a centroid at the origin that no
	// vector chose, which then attracts every query near the origin. The stale
	// counts are what let the repair run.
	//
	// TestKMeans_Cluster_EmptyClusterRepairIsLoadBearing pins this: apply the
	// recompute and it goes red on exactly that origin centroid.
	//
	// Handle empty clusters by redistributing
	centroids = km.handleEmptyClusters(vectors, assignments, clusterSizes, centroids)

	return centroids, assignments, nil
}

// initializeCentroids initializes cluster centroids using k-means++ or random selection.
func (km *KMeans) initializeCentroids(vectors [][]float32) ([][]float32, error) {
	if km.config.InitMethod == "kmeans++" {
		return km.initializeKMeansPlusPlus(vectors)
	}
	return km.initializeRandom(vectors)
}

// initializeKMeansPlusPlus implements k-means++ initialization for better initial centroids.
//
// minDist[i] tracks the RAW (unsquared) distance from vectors[i] to the
// NEAREST centroid chosen so far -- "nearest" defined exactly as
// findNearestCentroid defines it, by comparing distanceFunc's raw output, and
// squared only once, when it is turned into a sampling weight. It is updated
// with a single pass against just the newly-added centroid on every
// iteration -- not recomputed from scratch against every previously-chosen
// centroid. That distinction is the difference between O(n*k*d) and
// O(n*k^2*d) total work: the earlier from-scratch form re-derived, at
// k=255 (DefaultConfig's 256 centroids), the same minimums it had already
// found for k=1..254, so choosing the LAST centroid did as much work as
// choosing the first 255 combined. Reading go-qrdb#565's -race trace mid-run
// confirmed the goroutine was making forward progress inside this loop (not
// deadlocked), and an unmodified build did complete -- in 77s for one call,
// under -race, against the exact TestIndex_Search_WrongDimensions inputs
// (1000 vectors, 256 centroids, 128 dims). Three of those in one `-count=3`
// invocation, alongside the rest of the package's tests, is what crossed
// testing's 10-minute alarm and left the coverage profile unwritten. The
// incremental form does the same initialization in O(n*k) distance
// evaluations (256,000 here, versus ~32.6 million), which measures in
// milliseconds under -race.
//
// Comparing RAW distances rather than their squares before picking the
// minimum is required, not stylistic: for a signed metric such as
// DotProductDistance (lower, more negative values are CLOSER -- see its doc
// comment), -10 is nearer than 3, but (-10)^2=100 is not less than 3^2=9. A
// version that tracked min(d^2) instead of min(d)^2 would therefore treat 3
// as the nearer centroid and assign this point a k-means++ sampling weight
// of 9 instead of the correct 100, silently changing the selection
// distribution -- and thus the resulting clustering -- for every signed
// metric. TestKMeans_InitializeKMeansPlusPlus_SignedMetricWeights pins the
// two forms against each other over many seeds using DotProductDistance.
//
// minDist is sentinel-initialised to math.MaxFloat32 and only ever lowered
// by "<", the same idiom findNearestCentroid uses, so a distanceFunc result
// that overflows to +Inf (or is NaN) can never win the comparison and
// propagate into the weight sum -- it is silently clamped to the sentinel
// instead, exactly as the pre-optimisation from-scratch scan behaved.
func (km *KMeans) initializeKMeansPlusPlus(vectors [][]float32) ([][]float32, error) {
	centroids := make([][]float32, 0, km.config.NumCentroids)

	// Choose first centroid randomly.
	firstIdx := km.rng.Intn(len(vectors))
	centroids = append(centroids, copyVector(vectors[firstIdx]))

	minDist := make([]float32, len(vectors))
	for i, vec := range vectors {
		minDist[i] = clampedMin(float32(math.MaxFloat32), km.distanceFunc(vec, centroids[0]))
	}

	// Choose remaining centroids with probability proportional to D(x)^2.
	//
	// The weight and its running sum are computed in float64 even though
	// minDist is float32: clampedMin only stops an already-overflowed
	// DISTANCE from replacing a smaller one, it does nothing to stop the
	// SQUARING of a large-but-finite value -- including its own sentinel,
	// math.MaxFloat32 -- from overflowing float32's ~3.4e38 range. Squaring
	// in float32 (math.MaxFloat32*math.MaxFloat32) is itself +Inf, which
	// would put target and every cumulative comparison below back at the
	// mercy of Inf/NaN arithmetic despite the clamp. float64 has headroom to
	// spare for that square (up to ~1.16e77, against a ~1.8e308 ceiling),
	// summed over every point. squaredWeight isolates this and
	// TestSquaredWeight_LargeFiniteDistanceNoOverflow pins it directly: a
	// distance that overflows when squared in float32 does not overflow
	// squaredWeight.
	weights := make([]float64, len(vectors))
	for k := 1; k < km.config.NumCentroids; k++ {
		var totalWeight float64
		for i, d := range minDist {
			weights[i] = squaredWeight(d)
			totalWeight += weights[i]
		}

		var nextIdx int
		if totalWeight == 0 {
			// Every remaining vector coincides with an already-chosen
			// centroid: duplicate/degenerate input, or fewer distinct points
			// than NumCentroids. No point has a distance that would bias the
			// draw, so choose UNIFORMLY at random rather than scanning for a
			// point that is not "already chosen" -- with fewer distinct
			// points than centroids that scan can find nothing, which used
			// to leave this iteration's centroid unappended and return a
			// short (and thus invariant-violating) centroid slice.
			// handleEmptyClusters repairs the resulting duplicate centroid
			// once real cluster sizes are known.
			nextIdx = km.rng.Intn(len(vectors))
		} else {
			target := km.rng.Float64() * totalWeight
			cumulative := float64(0)
			nextIdx = len(vectors) - 1
			for i, weight := range weights {
				cumulative += weight
				if cumulative >= target {
					nextIdx = i
					break
				}
			}
		}

		next := copyVector(vectors[nextIdx])
		centroids = append(centroids, next)

		// Fold the new centroid into the running minimum with one pass,
		// instead of rescanning every previously-chosen centroid.
		for i, vec := range vectors {
			minDist[i] = clampedMin(minDist[i], km.distanceFunc(vec, next))
		}
	}

	return centroids, nil
}

// clampedMin returns the smaller of current and candidate, but a candidate
// that fails a plain "<" comparison against current -- because it is +Inf or
// NaN -- can never replace it. Used above to track the minimum distance to
// the nearest already-chosen k-means++ centroid, so a distanceFunc result
// that overflows float32 to +Inf (or is NaN, from a NaN-producing input) is
// clamped to whatever finite value was already found -- or to the sentinel
// math.MaxFloat32 on the very first comparison -- rather than propagating
// into the weighted-sampling sum in initializeKMeansPlusPlus.
func clampedMin(current, candidate float32) float32 {
	// A non-finite candidate is an OVERFLOW ARTIFACT, not a legitimate
	// distance, and must be clamped to a finite bound before it is ever
	// compared -- for every sign, not just +Inf. DotProductDistance's raw
	// output is signed (see its doc comment), and its own float32
	// accumulation can overflow to EITHER +Inf (very unaligned, very large
	// vectors) or -Inf (very aligned, very large vectors); a bare "<"
	// comparison lets -Inf through as if it were a genuinely, legitimately
	// closer point than any real one, since -Inf compares less than every
	// finite value including this function's own sentinel. squaredWeight
	// then squares that -Inf into +Inf, exactly the failure clampedMin
	// exists to prevent -- clamping only +Inf and leaving -Inf unclamped
	// closes half the door. NaN (e.g. from 0 * Inf inside an accumulation)
	// is clamped alongside +Inf rather than left to rely on "NaN compares
	// false to everything" doing the right thing here implicitly.
	switch {
	case math.IsNaN(float64(candidate)):
		candidate = math.MaxFloat32
	case math.IsInf(float64(candidate), 1):
		candidate = math.MaxFloat32
	case math.IsInf(float64(candidate), -1):
		candidate = -math.MaxFloat32
	}

	if candidate < current {
		return candidate
	}
	return current
}

// squaredWeight returns d*d computed in float64, so a d at or beyond
// float32's overflow threshold -- including the clampedMin sentinel,
// math.MaxFloat32 itself -- squares to a large but FINITE float64 value
// rather than overflowing the way squaring in float32 arithmetic would.
func squaredWeight(d float32) float64 {
	return float64(d) * float64(d)
}

// initializeRandom randomly selects initial centroids from the dataset.
func (km *KMeans) initializeRandom(vectors [][]float32) ([][]float32, error) {
	centroids := make([][]float32, km.config.NumCentroids)
	indices := km.rng.Perm(len(vectors))

	for i := 0; i < km.config.NumCentroids; i++ {
		centroids[i] = copyVector(vectors[indices[i]])
	}

	return centroids, nil
}

// findNearestCentroid finds the index of the nearest centroid to the given vector.
func (km *KMeans) findNearestCentroid(vec []float32, centroids [][]float32) int {
	minDist := float32(math.MaxFloat32)
	nearest := 0

	for i, centroid := range centroids {
		dist := km.distanceFunc(vec, centroid)
		if dist < minDist {
			minDist = dist
			nearest = i
		}
	}

	return nearest
}

// recomputeCentroids calculates new centroids as the mean of assigned vectors.
func (km *KMeans) recomputeCentroids(vectors [][]float32, assignments []int, clusterSizes []int) ([][]float32, error) {
	dims := km.config.Dimensions
	centroids := make([][]float32, km.config.NumCentroids)

	// Initialize centroids
	for i := range centroids {
		centroids[i] = make([]float32, dims)
	}

	// Sum up vectors in each cluster
	for i, vec := range vectors {
		cluster := assignments[i]
		for d := 0; d < dims; d++ {
			centroids[cluster][d] += vec[d]
		}
	}

	// Compute means
	for i := range centroids {
		if clusterSizes[i] > 0 {
			for d := 0; d < dims; d++ {
				centroids[i][d] /= float32(clusterSizes[i])
			}
		}
	}

	return centroids, nil
}

// maxCentroidMovement computes the maximum distance any centroid has moved.
func (km *KMeans) maxCentroidMovement(old, new [][]float32) float32 {
	maxDist := float32(0)

	for i := range old {
		dist := km.distanceFunc(old[i], new[i])
		if dist > maxDist {
			maxDist = dist
		}
	}

	return maxDist
}

// handleEmptyClusters fixes empty clusters by splitting large clusters.
func (km *KMeans) handleEmptyClusters(vectors [][]float32, assignments []int, clusterSizes []int, centroids [][]float32) [][]float32 {
	return kmeans.RepairEmptyClusters(vectors, assignments, clusterSizes, centroids, km.distanceFunc)
}

// copyVector creates a deep copy of a vector.
func copyVector(vec []float32) []float32 {
	copied := make([]float32, len(vec))
	copy(copied, vec)
	return copied
}

// FindNearestCentroids finds the k nearest centroids to the query vector.
func FindNearestCentroids(query []float32, centroids [][]float32, k int, distFunc DistanceFunc) []int {
	if k > len(centroids) {
		k = len(centroids)
	}

	// Compute distances to all centroids
	type centroidDist struct {
		idx  int
		dist float32
	}

	distances := make([]centroidDist, len(centroids))
	for i, centroid := range centroids {
		distances[i] = centroidDist{
			idx:  i,
			dist: distFunc(query, centroid),
		}
	}

	// Partial sort to find k nearest (more efficient than full sort)
	for i := 0; i < k; i++ {
		minIdx := i
		for j := i + 1; j < len(distances); j++ {
			if distances[j].dist < distances[minIdx].dist {
				minIdx = j
			}
		}
		if minIdx != i {
			distances[i], distances[minIdx] = distances[minIdx], distances[i]
		}
	}

	// Extract indices
	result := make([]int, k)
	for i := 0; i < k; i++ {
		result[i] = distances[i].idx
	}

	return result
}
