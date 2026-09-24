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

package quantization

import (
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

const (
	// defaultMaxKMeansIterations is the default maximum number of k-means iterations.
	defaultMaxKMeansIterations = 25
)

// PQQuantizer provides high compression by splitting vectors into M sub-spaces
// and quantizing each sub-space independently using k-means centroids. A vector
// of D dimensions is compressed to M bytes (with nbits=8), achieving D*4/M
// compression ratio.
type PQQuantizer struct {
	dimensions int
	m          int         // number of sub-quantizers
	nbits      int         // bits per code (typically 8)
	subDim     int         // dimensions per sub-space (dimensions / m)
	nCentroids int         // 2^nbits centroids per sub-space
	codebooks  [][]float32 // [m][nCentroids * subDim] flattened centroids
	trained    atomic.Bool
	mu         sync.RWMutex // protects codebook writes during training
}

// NewPQQuantizer creates a new Product Quantizer. The dimensions must be evenly
// divisible by m. Nbits determines the number of centroids per sub-space (2^nbits).
func NewPQQuantizer(dimensions, m, nbits int) (*PQQuantizer, error) {
	if m <= 0 {
		return nil, &InvalidConfigError{Message: "m must be positive"}
	}
	if nbits <= 0 || nbits > 8 {
		return nil, &InvalidConfigError{Message: "nbits must be between 1 and 8"}
	}
	if dimensions <= 0 {
		return nil, &InvalidConfigError{Message: "dimensions must be positive"}
	}
	if dimensions%m != 0 {
		return nil, &InvalidConfigError{Message: "dimensions must be evenly divisible by m"}
	}

	nCentroids := 1 << nbits
	subDim := dimensions / m

	codebooks := make([][]float32, m)
	for i := range codebooks {
		codebooks[i] = make([]float32, nCentroids*subDim)
	}

	return &PQQuantizer{
		dimensions: dimensions,
		m:          m,
		nbits:      nbits,
		subDim:     subDim,
		nCentroids: nCentroids,
		codebooks:  codebooks,
	}, nil
}

// Train builds codebooks using k-means clustering on the provided training vectors.
// Each sub-space is trained independently using the corresponding sub-vector slices.
func (pq *PQQuantizer) Train(vectors [][]float32) error {
	if len(vectors) == 0 {
		return &EmptyVectorSetError{Op: "PQQuantizer.Train"}
	}

	if len(vectors) < pq.nCentroids {
		return &InsufficientVectorsError{
			Required: pq.nCentroids,
			Got:      len(vectors),
		}
	}

	for i, v := range vectors {
		if len(v) != pq.dimensions {
			_ = i // used for context
			return &DimensionError{Got: len(v), Expected: pq.dimensions}
		}
	}

	// Train each sub-quantizer independently.
	pq.mu.Lock()
	defer pq.mu.Unlock()

	// One rng, shared across every sub-quantizer's k-means++ run, mirroring
	// how the single package-level source this replaced was shared across
	// all of them. Seeded from the clock: nothing here needs to be
	// reproducible in production, only injectable in tests (see
	// kmeansCluster's doc comment).
	// #nosec G404 -- k-means++ seeding; statistical sampling, not security.
	now := uint64(time.Now().UnixNano())
	rng := rand.New(rand.NewPCG(now, now^0x9E3779B97F4A7C15))

	for sub := 0; sub < pq.m; sub++ {
		// Extract sub-vectors for this sub-space.
		subVectors := make([][]float32, len(vectors))
		offset := sub * pq.subDim
		for i, v := range vectors {
			subVectors[i] = v[offset : offset+pq.subDim]
		}

		centroids, err := kmeansCluster(subVectors, pq.nCentroids, defaultMaxKMeansIterations, rng)
		if err != nil {
			return &QuantizationError{
				Op:      "PQQuantizer.Train",
				Message: "k-means failed for sub-quantizer",
				Err:     err,
			}
		}

		// Flatten centroids into codebook.
		for c := 0; c < pq.nCentroids; c++ {
			copy(pq.codebooks[sub][c*pq.subDim:(c+1)*pq.subDim], centroids[c])
		}
	}

	pq.trained.Store(true)
	return nil
}

// IsTrained returns whether the codebooks have been trained.
func (pq *PQQuantizer) IsTrained() bool {
	return pq.trained.Load()
}

// Encode compresses a float32 vector to M codes (one uint8 per sub-quantizer).
// Each code is the index of the nearest centroid in the corresponding sub-space.
func (pq *PQQuantizer) Encode(vector []float32) ([]uint8, error) {
	if !pq.trained.Load() {
		return nil, &QuantizationError{
			Op:      "PQQuantizer.Encode",
			Message: "codebooks not trained",
			Err:     ErrNotTrained,
		}
	}

	if len(vector) != pq.dimensions {
		return nil, &DimensionError{Got: len(vector), Expected: pq.dimensions}
	}

	codes := make([]uint8, pq.m)
	for sub := 0; sub < pq.m; sub++ {
		offset := sub * pq.subDim
		subVec := vector[offset : offset+pq.subDim]
		codes[sub] = pq.findNearestCentroid(sub, subVec)
	}
	return codes, nil
}

// Decode reconstructs a float32 vector from PQ codes by looking up the centroid
// for each sub-space. This is a lossy reconstruction.
func (pq *PQQuantizer) Decode(codes []uint8) ([]float32, error) {
	if !pq.trained.Load() {
		return nil, &QuantizationError{
			Op:      "PQQuantizer.Decode",
			Message: "codebooks not trained",
			Err:     ErrNotTrained,
		}
	}

	if len(codes) != pq.m {
		return nil, &DimensionError{Got: len(codes), Expected: pq.m}
	}

	decoded := make([]float32, pq.dimensions)
	for sub := 0; sub < pq.m; sub++ {
		centroidIdx := int(codes[sub])
		srcOffset := centroidIdx * pq.subDim
		dstOffset := sub * pq.subDim
		copy(decoded[dstOffset:dstOffset+pq.subDim], pq.codebooks[sub][srcOffset:srcOffset+pq.subDim])
	}
	return decoded, nil
}

// ComputeDistanceTable precomputes distances from query sub-vectors to all centroids.
// Returns a [m][nCentroids] distance table that enables O(m) distance lookups per
// database vector instead of O(dimensions).
func (pq *PQQuantizer) ComputeDistanceTable(query []float32, metric hnsw.DistanceMetric) ([][]float32, error) {
	if !pq.trained.Load() {
		return nil, &QuantizationError{
			Op:      "PQQuantizer.ComputeDistanceTable",
			Message: "codebooks not trained",
			Err:     ErrNotTrained,
		}
	}

	if len(query) != pq.dimensions {
		return nil, &DimensionError{Got: len(query), Expected: pq.dimensions}
	}

	distFn := hnsw.GetDistanceFunc(metric)
	table := make([][]float32, pq.m)

	for sub := 0; sub < pq.m; sub++ {
		table[sub] = make([]float32, pq.nCentroids)
		queryOffset := sub * pq.subDim
		querySub := query[queryOffset : queryOffset+pq.subDim]

		for c := 0; c < pq.nCentroids; c++ {
			centroidOffset := c * pq.subDim
			centroid := pq.codebooks[sub][centroidOffset : centroidOffset+pq.subDim]
			table[sub][c] = distFn(querySub, centroid)
		}
	}

	return table, nil
}

// AsymmetricDistanceFromTable computes the approximate distance from a query to a
// database vector using a precomputed distance table. This is O(m) instead of
// O(dimensions), providing the key PQ speed advantage.
func (pq *PQQuantizer) AsymmetricDistanceFromTable(table [][]float32, codes []uint8) float32 {
	var dist float32
	for sub := 0; sub < pq.m; sub++ {
		dist += table[sub][codes[sub]]
	}
	return dist
}

// EncodedSize returns the size in bytes of an encoded vector.
func (pq *PQQuantizer) EncodedSize() int {
	return pq.m
}

// Dimensions returns the dimensionality of vectors this quantizer handles.
func (pq *PQQuantizer) Dimensions() int {
	return pq.dimensions
}

// M returns the number of sub-quantizers.
func (pq *PQQuantizer) M() int {
	return pq.m
}

// NCentroids returns the number of centroids per sub-space.
func (pq *PQQuantizer) NCentroids() int {
	return pq.nCentroids
}

// findNearestCentroid finds the nearest centroid for a sub-vector in the given
// sub-quantizer's codebook.
func (pq *PQQuantizer) findNearestCentroid(sub int, subVec []float32) uint8 {
	bestDist := float32(math.MaxFloat32)
	bestIdx := 0

	for c := 0; c < pq.nCentroids; c++ {
		offset := c * pq.subDim
		centroid := pq.codebooks[sub][offset : offset+pq.subDim]

		var dist float32
		for d := 0; d < pq.subDim; d++ {
			diff := subVec[d] - centroid[d]
			dist += diff * diff
		}

		if dist < bestDist {
			bestDist = dist
			bestIdx = c
		}
	}

	return uint8(bestIdx)
}

// kmeansCluster runs k-means clustering on the provided vectors and returns k centroids.
// Uses k-means++ initialization for better convergence.
//
// rng supplies the randomness for centroid initialisation. It is an explicit
// parameter -- rather than the package-level math/rand/v2 functions this
// replaced -- so a test can inject a reproducibly-seeded source and assert on
// the EXACT centroids kmeansPlusPlusInit selects, mirroring ivf.KMeans's rng
// field. Train constructs one clock-seeded *rand.Rand and shares it across
// every sub-quantizer's call, the same way the single package-level source it
// replaced was implicitly shared across all of them.
func kmeansCluster(vectors [][]float32, k, maxIter int, rng *rand.Rand) ([][]float32, error) {
	n := len(vectors)
	if n == 0 {
		return nil, &EmptyVectorSetError{Op: "kmeansCluster"}
	}
	if k <= 0 {
		return nil, &InvalidConfigError{Message: "k must be positive"}
	}
	if n < k {
		return nil, &InsufficientVectorsError{Required: k, Got: n}
	}

	dim := len(vectors[0])
	centroids := kmeansPlusPlusInit(vectors, k, rng)

	// Iterative refinement
	assignments := make([]int, n)
	newCentroids := make([][]float32, k)
	counts := make([]int, k)

	for iter := 0; iter < maxIter; iter++ {
		// Assignment step
		changed := false
		for i := 0; i < n; i++ {
			bestCluster := 0
			bestDist := float32(math.MaxFloat32)
			for c := 0; c < k; c++ {
				d := l2Squared(vectors[i], centroids[c])
				if d < bestDist {
					bestDist = d
					bestCluster = c
				}
			}
			if assignments[i] != bestCluster {
				assignments[i] = bestCluster
				changed = true
			}
		}

		if !changed && iter > 0 {
			break // Converged
		}

		// Update step: recompute centroids
		for c := 0; c < k; c++ {
			newCentroids[c] = make([]float32, dim)
			counts[c] = 0
		}

		for i := 0; i < n; i++ {
			c := assignments[i]
			counts[c]++
			for d := 0; d < dim; d++ {
				newCentroids[c][d] += vectors[i][d]
			}
		}

		for c := 0; c < k; c++ {
			if counts[c] > 0 {
				invCount := 1.0 / float32(counts[c])
				for d := 0; d < dim; d++ {
					newCentroids[c][d] *= invCount
				}
				copy(centroids[c], newCentroids[c])
			}
			// Empty clusters keep their previous centroid.
		}
	}

	return centroids, nil
}

// kmeansPlusPlusInit picks k initial centroids from vectors using k-means++
// weighted sampling, and is kmeansCluster's initialization step, extracted so
// it is directly testable: a test can assert on the EXACT centroids it
// returns, independent of the refinement iterations kmeansCluster runs
// afterward, mirroring ivf.KMeans.initializeKMeansPlusPlus.
//
// distances[i] tracks the squared distance from vectors[i] to the NEAREST
// centroid chosen so far and is updated with a single pass against just the
// newly-added centroid on every iteration -- not recomputed from scratch
// against every previously-chosen centroid. Recomputing from scratch is
// O(n*k^2): choosing the LAST of k centroids re-derives the same minimums
// already found while choosing the first k-1, so the total cost grows
// quadratically in k. At nbits=8 (256 centroids per sub-space, the shipped
// default) that quadratic term is what drove TestPQ_RecallAccuracy past
// testing's 10-minute alarm under -race -- confirmed by a mid-run trace
// sitting in this loop, still making forward progress rather than
// deadlocked. This incremental form does the same initialization in O(n*k)
// distance evaluations.
//
// distances[i] is tracked through clampedMin, sentinel-initialised to
// math.MaxFloat32, so an l2Squared result that overflows float32 to +Inf (or
// is NaN) can never replace it and propagate into the weighted-sampling sum
// below -- it is clamped to MaxFloat32 instead, exactly as the
// pre-optimisation per-candidate scan (which re-initialised this same
// sentinel for every existing centroid on every iteration) behaved. Storing
// the raw l2Squared result directly here would let a single overflowing pair
// make that point's weight +Inf, which then either degenerates the weighted
// draw to whichever row is scanned first with a running sum already at
// +Inf, or -- once folded into later iterations by the incremental update
// below -- keeps re-selecting that same +Inf-weighted row.
// TestKMeansPlusPlusInit_OverflowDistanceClamped pins this directly against
// referenceKMeansPlusPlusInit, an unclamped test oracle sharing the same
// injected rng: the two must select IDENTICAL centroids on ordinary input,
// and DIVERGE on input whose pairwise l2Squared genuinely overflows.
//
// l2Squared, unlike ivf's generic DistanceFunc, is already the squared
// distance in one step -- there is no separate "square the clamped minimum"
// step the way ivf.KMeans.initializeKMeansPlusPlus has, so l2Squared's own
// output never needs a second, float64 squaring pass to avoid re-overflowing
// (contrast squaredWeight in pkg/vector/ivf).
func kmeansPlusPlusInit(vectors [][]float32, k int, rng *rand.Rand) [][]float32 {
	n := len(vectors)
	dim := len(vectors[0])

	centroids := make([][]float32, k)
	centroids[0] = make([]float32, dim)
	// #nosec G404 -- k-means++ seeding; statistical sampling, not security.
	copy(centroids[0], vectors[rng.IntN(n)])

	distances := make([]float32, n)
	for i := 0; i < n; i++ {
		distances[i] = clampedMin(float32(math.MaxFloat32), l2Squared(vectors[i], centroids[0]))
	}

	for c := 1; c < k; c++ {
		var totalDist float64
		for i := 0; i < n; i++ {
			totalDist += float64(distances[i])
		}

		var selected int
		if totalDist == 0 {
			// Every remaining point coincides with an already-chosen
			// centroid: duplicate/degenerate input, or fewer distinct
			// points than k. No point has a distance that would bias the
			// draw, so choose uniformly at random.
			// #nosec G404 -- k-means++ seeding; statistical sampling, not security.
			selected = rng.IntN(n)
		} else {
			// #nosec G404 -- k-means++ weighted selection; statistical sampling, not security.
			threshold := rng.Float64() * totalDist
			var cumulative float64
			selected = n - 1
			for i := 0; i < n; i++ {
				cumulative += float64(distances[i])
				if cumulative >= threshold {
					selected = i
					break
				}
			}
		}

		centroids[c] = make([]float32, dim)
		copy(centroids[c], vectors[selected])

		// Fold the new centroid into the running minimum with one pass,
		// instead of rescanning every previously-chosen centroid.
		for i := 0; i < n; i++ {
			distances[i] = clampedMin(distances[i], l2Squared(vectors[i], centroids[c]))
		}
	}

	return centroids
}

// l2Squared computes the squared L2 distance between two vectors.
func l2Squared(a, b []float32) float32 {
	var sum float32
	for i := range a {
		diff := a[i] - b[i]
		sum += diff * diff
	}
	return sum
}

// clampedMin returns the smaller of current and candidate, but a candidate
// that fails a plain "<" comparison against current -- because it is +Inf or
// NaN -- can never replace it. Used in kmeansCluster to track the minimum
// squared distance to the nearest already-chosen k-means++ centroid, so an
// l2Squared result that overflows float32 to +Inf (or is NaN) is clamped to
// whatever finite value was already found -- or to the sentinel
// math.MaxFloat32 on the very first comparison -- rather than propagating
// into the weighted-sampling sum.
func clampedMin(current, candidate float32) float32 {
	if candidate < current {
		return candidate
	}
	return current
}
