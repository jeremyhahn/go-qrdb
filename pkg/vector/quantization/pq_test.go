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
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/kmeans"
)

func TestPQ_TrainAndEncodeDecode(t *testing.T) {
	dim := 128
	m := 8
	nbits := 8

	pq, err := NewPQQuantizer(dim, m, nbits)
	require.NoError(t, err)

	vectors := generateRandomVectors(1000, dim)
	require.NoError(t, pq.Train(vectors))
	assert.True(t, pq.IsTrained())

	// Encode and decode a vector, verify reasonable reconstruction.
	for i := 0; i < 5; i++ {
		codes, err := pq.Encode(vectors[i])
		require.NoError(t, err)
		assert.Len(t, codes, m)

		decoded, err := pq.Decode(codes)
		require.NoError(t, err)
		assert.Len(t, decoded, dim)

		// PQ reconstruction should have limited error. Check cosine similarity.
		var dotProduct, normA, normB float32
		for d := 0; d < dim; d++ {
			dotProduct += vectors[i][d] * decoded[d]
			normA += vectors[i][d] * vectors[i][d]
			normB += decoded[d] * decoded[d]
		}
		cosineSim := dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
		assert.Greater(t, cosineSim, float32(0.5), "PQ reconstruction should maintain reasonable cosine similarity")
	}
}

// pqRecallAt10 trains a fresh quantizer and returns how many of the true
// top-10 neighbours its asymmetric distance table recovers.
//
// Training draws from a PCG seeded with seed (through the newTrainRNG seam),
// so the same seed always yields the same codebook and the same recall. When
// discard is set the trained codebooks are zeroed before encoding: every
// vector then quantizes to the same point, which is the control for a
// quantizer that throws the vector away.
func pqRecallAt10(t *testing.T, dim, m, nbits int, seed uint64, discard bool) int {
	t.Helper()

	orig := newTrainRNG
	t.Cleanup(func() { newTrainRNG = orig })
	newTrainRNG = func() *rand.Rand {
		// #nosec G404 -- deterministic test fixture: k-means++ seeding, no secret derives from it.
		return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	}

	pq, err := NewPQQuantizer(dim, m, nbits)
	require.NoError(t, err)

	trainingVecs := generateRandomVectors(800, dim)
	require.NoError(t, pq.Train(trainingVecs))
	if discard {
		for _, cb := range pq.codebooks {
			clear(cb)
		}
	}

	queryVec := trainingVecs[0]
	dbVectors := trainingVecs[1:201] // 200 database vectors

	type result struct {
		idx  int
		dist float32
	}
	rank := func(rs []result) {
		for i := 0; i < len(rs); i++ {
			for j := i + 1; j < len(rs); j++ {
				if rs[j].dist < rs[i].dist {
					rs[i], rs[j] = rs[j], rs[i]
				}
			}
		}
	}

	// True top-10 in float32.
	distFn := hnsw.GetDistanceFunc(hnsw.L2Distance)
	trueResults := make([]result, len(dbVectors))
	for i, v := range dbVectors {
		trueResults[i] = result{idx: i, dist: distFn(queryVec, v)}
	}
	rank(trueResults)
	trueTop10 := make(map[int]bool, 10)
	for _, r := range trueResults[:10] {
		trueTop10[r.idx] = true
	}

	// PQ top-10 via the distance table.
	table, err := pq.ComputeDistanceTable(queryVec, hnsw.L2Distance)
	require.NoError(t, err)

	pqResults := make([]result, len(dbVectors))
	for i, v := range dbVectors {
		codes, err := pq.Encode(v)
		require.NoError(t, err)
		pqResults[i] = result{idx: i, dist: pq.AsymmetricDistanceFromTable(table, codes)}
	}
	rank(pqResults)

	recall := 0
	for _, r := range pqResults[:10] {
		if trueTop10[r.idx] {
			recall++
		}
	}
	return recall
}

// TestPQ_RecallAccuracy checks that product quantization retains most of the
// true nearest neighbours.
//
// Train seeds k-means++ from the clock in production, so every run trains a
// different codebook and a single-trial recall varies widely: measured over 30
// runs the distribution was {2:1, 3:4, 4:8, 5:8, 6:8, 7:1}. Even a 3-trial
// mean then fell under the bound a few percent of the time (main red, run
// 56527, mean 2.67), which judged the clock, not the quantizer. The test now
// trains each trial on a fixed seed through the unexported newTrainRNG seam,
// and generateRandomVectors draws its data from a fixed seed, so the measured
// recall is the same on every run: seeds 1, 2 and 3 (the first three tried,
// not a pick) measure 5, 4 and 6, a mean of 5.0 against the 3.0 bound. A failure therefore means
// the quantizer changed.
//
// The discard control keeps the test's power: with the codebooks zeroed every
// vector quantizes to one point and recall collapses to chance (0 on each of the
// same seeds), far below the bound.
func TestPQ_RecallAccuracy(t *testing.T) {
	// Sized for CI: k-means training dominates the runtime and runs under
	// -race on a shared runner, where 5 trials over 2000 training vectors
	// ran ~8 minutes and blew the test binary's 10-minute budget.
	const (
		dim, m, nbits = 64, 8, 8
		minMeanRecall = 3.0
	)
	seeds := []uint64{1, 2, 3}

	meanRecall := func(discard bool) float64 {
		total := 0
		for _, seed := range seeds {
			r := pqRecallAt10(t, dim, m, nbits, seed, discard)
			t.Logf("seed %d discard=%v: recall@10 = %d", seed, discard, r)
			total += r
		}
		return float64(total) / float64(len(seeds))
	}

	mean := meanRecall(false)
	assert.GreaterOrEqual(t, mean, minMeanRecall,
		"PQ mean recall@10 over %d seeds should be >= %.1f, got %.2f", len(seeds), minMeanRecall, mean)

	control := meanRecall(true)
	assert.Less(t, control, minMeanRecall/2,
		"a quantizer that discards the vector must measure far below the bound, got %.2f", control)
}

func TestPQ_DistanceTable(t *testing.T) {
	dim := 32
	m := 4
	nbits := 8

	pq, err := NewPQQuantizer(dim, m, nbits)
	require.NoError(t, err)

	vectors := generateRandomVectors(500, dim)
	require.NoError(t, pq.Train(vectors))

	query := vectors[0]

	// Compute distance table.
	table, err := pq.ComputeDistanceTable(query, hnsw.DotProductDistance)
	require.NoError(t, err)
	assert.Len(t, table, m)
	assert.Len(t, table[0], 256)

	// Verify that table-based distances preserve ranking order by comparing
	// multiple database vectors. The dot product metric is additive across
	// sub-vectors so table-based and direct distances should be very close.
	type result struct {
		idx        int
		tableDist  float32
		directDist float32
	}
	results := make([]result, 20)
	distFn := hnsw.GetDistanceFunc(hnsw.DotProductDistance)

	for i := 0; i < 20; i++ {
		target := vectors[i+1]
		codes, err := pq.Encode(target)
		require.NoError(t, err)

		tableDist := pq.AsymmetricDistanceFromTable(table, codes)
		decoded, err := pq.Decode(codes)
		require.NoError(t, err)
		directDist := distFn(query, decoded)

		results[i] = result{idx: i, tableDist: tableDist, directDist: directDist}

		// For dot product, table distance should exactly match direct distance
		// because dot product is additive: sum(sub_dot) = total_dot.
		assert.InDelta(t, directDist, tableDist, 0.01,
			"vector %d: table=%.4f direct=%.4f", i, tableDist, directDist)
	}

	// Also verify ranking consistency: sort by table distance and by direct
	// distance, the orderings should be identical for dot product.
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].tableDist < results[i].tableDist {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	// Check that at least top-5 by table distance are in top-7 by direct distance.
	directRank := make(map[int]int)
	directResults := make([]result, len(results))
	copy(directResults, results)
	for i := 0; i < len(directResults); i++ {
		for j := i + 1; j < len(directResults); j++ {
			if directResults[j].directDist < directResults[i].directDist {
				directResults[i], directResults[j] = directResults[j], directResults[i]
			}
		}
	}
	for rank, r := range directResults {
		directRank[r.idx] = rank
	}

	topMatches := 0
	for _, r := range results[:5] {
		if directRank[r.idx] < 7 {
			topMatches++
		}
	}
	assert.GreaterOrEqual(t, topMatches, 4, "table distance should preserve top-5 ranking")
}

func TestPQ_NotTrained(t *testing.T) {
	pq, err := NewPQQuantizer(64, 8, 8)
	require.NoError(t, err)

	vec := make([]float32, 64)
	_, err = pq.Encode(vec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotTrained))

	_, err = pq.Decode(make([]uint8, 8))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotTrained))

	_, err = pq.ComputeDistanceTable(vec, hnsw.L2Distance)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotTrained))
}

func TestPQ_DimensionMismatch(t *testing.T) {
	// Dimensions not divisible by m.
	_, err := NewPQQuantizer(65, 8, 8)
	require.Error(t, err)
	var configErr *InvalidConfigError
	assert.True(t, errors.As(err, &configErr))
}

func TestPQ_InvalidConfig(t *testing.T) {
	tests := []struct {
		name       string
		dim, m, nb int
		errMsg     string
	}{
		{"m=0", 64, 0, 8, "m must be positive"},
		{"nbits=0", 64, 8, 0, "nbits must be between 1 and 8"},
		{"nbits=9", 64, 8, 9, "nbits must be between 1 and 8"},
		{"dim=0", 0, 8, 8, "dimensions must be positive"},
		{"dim not divisible", 65, 8, 8, "dimensions must be evenly divisible by m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPQQuantizer(tt.dim, tt.m, tt.nb)
			require.Error(t, err)
			var configErr *InvalidConfigError
			assert.True(t, errors.As(err, &configErr))
			assert.Contains(t, configErr.Error(), tt.errMsg)
		})
	}
}

func TestPQ_EmptyTrainingSet(t *testing.T) {
	pq, err := NewPQQuantizer(64, 8, 8)
	require.NoError(t, err)

	err = pq.Train(nil)
	require.Error(t, err)
	var emptyErr *EmptyVectorSetError
	assert.True(t, errors.As(err, &emptyErr))

	err = pq.Train([][]float32{})
	require.Error(t, err)
	assert.True(t, errors.As(err, &emptyErr))
}

func TestPQ_InsufficientTrainingVectors(t *testing.T) {
	pq, err := NewPQQuantizer(64, 8, 8)
	require.NoError(t, err)

	// Need at least 256 vectors for nbits=8.
	vectors := generateRandomVectors(100, 64)
	err = pq.Train(vectors)
	require.Error(t, err)
	var insuffErr *InsufficientVectorsError
	assert.True(t, errors.As(err, &insuffErr))
	assert.Equal(t, 256, insuffErr.Required)
	assert.Equal(t, 100, insuffErr.Got)
}

func TestPQ_TrainDimensionMismatch(t *testing.T) {
	pq, err := NewPQQuantizer(64, 8, 8)
	require.NoError(t, err)

	vectors := generateRandomVectors(500, 32) // Wrong dimension
	err = pq.Train(vectors)
	require.Error(t, err)
	var dimErr *DimensionError
	assert.True(t, errors.As(err, &dimErr))
}

func TestPQ_EncodeDimensionMismatch(t *testing.T) {
	pq, err := NewPQQuantizer(64, 8, 8)
	require.NoError(t, err)

	vectors := generateRandomVectors(500, 64)
	require.NoError(t, pq.Train(vectors))

	wrongVec := make([]float32, 32)
	_, err = pq.Encode(wrongVec)
	require.Error(t, err)
	var dimErr *DimensionError
	assert.True(t, errors.As(err, &dimErr))
}

func TestPQ_EncodedSize(t *testing.T) {
	pq, err := NewPQQuantizer(128, 16, 8)
	require.NoError(t, err)
	assert.Equal(t, 16, pq.EncodedSize())
}

func TestPQ_Dimensions(t *testing.T) {
	pq, err := NewPQQuantizer(128, 16, 8)
	require.NoError(t, err)
	assert.Equal(t, 128, pq.Dimensions())
}

func TestPQ_M_Accessor(t *testing.T) {
	pq, err := NewPQQuantizer(64, 8, 8)
	require.NoError(t, err)
	assert.Equal(t, 8, pq.M())
}

func TestPQ_SmallNbits(t *testing.T) {
	// Test with nbits=4 (16 centroids).
	dim := 16
	m := 4
	nbits := 4

	pq, err := NewPQQuantizer(dim, m, nbits)
	require.NoError(t, err)
	assert.Equal(t, 16, pq.NCentroids())

	vectors := generateRandomVectors(100, dim)
	require.NoError(t, pq.Train(vectors))

	codes, err := pq.Encode(vectors[0])
	require.NoError(t, err)
	assert.Len(t, codes, m)

	// All codes should be < 16 (2^4).
	for _, c := range codes {
		assert.Less(t, c, uint8(16))
	}
}

// TestClampedMin_OverflowAndNaN pins clampedMin's clamping behaviour
// directly and deterministically: a candidate that is +Inf or NaN must never
// replace current, matching the sentinel-init idiom kmeansCluster's
// refinement loop uses (start at math.MaxFloat32, or the running minimum,
// and only ever move down via "<").
func TestClampedMin_OverflowAndNaN(t *testing.T) {
	sentinel := float32(math.MaxFloat32)
	posInf := float32(math.Inf(1))
	nan := float32(math.NaN())

	tests := []struct {
		name      string
		current   float32
		candidate float32
		want      float32
	}{
		{"finite candidate replaces sentinel", sentinel, 5, 5},
		{"smaller finite candidate replaces a running minimum", float32(10), float32(5), float32(5)},
		{"larger finite candidate does not replace a running minimum", float32(5), float32(10), float32(5)},
		{"+Inf candidate never replaces the sentinel", sentinel, posInf, sentinel},
		{"+Inf candidate never replaces a finite running minimum", float32(5), posInf, float32(5)},
		{"NaN candidate never replaces the sentinel", sentinel, nan, sentinel},
		{"NaN candidate never replaces a finite running minimum", float32(5), nan, float32(5)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, clampedMin(tt.current, tt.candidate))
		})
	}

	// Contrast against the shape this replaces: an unconditional assignment
	// (`distances[i] = l2Squared(...)`) with no "<" comparison at all, which
	// is what kmeansCluster's initial per-candidate precompute did before
	// this fix. That shape takes an overflowing (or NaN) l2Squared result
	// unchanged, rather than clamping it to whatever was already known.
	unconditionalAssign := func(_, candidate float32) float32 { return candidate }
	assert.NotEqual(t, sentinel, unconditionalAssign(sentinel, posInf),
		"the parent shape (direct assignment) lets +Inf through uncontested")
	require.True(t, math.IsInf(float64(unconditionalAssign(sentinel, posInf)), 1))
	assert.NotEqual(t, unconditionalAssign(sentinel, posInf), clampedMin(sentinel, posInf))
}

// referenceKMeansPlusPlusInit is kmeansPlusPlusInit's SELECTION shape as it
// existed before the clampedMin fix: it assigns each candidate's distance
// directly from l2Squared's raw result, with no sentinel comparison at all,
// so an overflowing (+Inf) or NaN result propagates unchanged into the
// weighted-sampling sum. It exists ONLY as a test oracle, sharing the same
// injected rng as the real implementation so the two are directly
// comparable: TestKMeansPlusPlusInit_OverflowDistanceClamped asserts they
// select IDENTICAL centroids on ordinary input, and DIVERGE once l2Squared
// genuinely overflows -- which is what proves clampedMin is on the path
// actually exercised, not merely correct in isolation.
func referenceKMeansPlusPlusInit(rng *rand.Rand, vectors [][]float32, k int) [][]float32 {
	n := len(vectors)
	dim := len(vectors[0])

	centroids := make([][]float32, k)
	centroids[0] = make([]float32, dim)
	copy(centroids[0], vectors[rng.IntN(n)])

	distances := make([]float32, n)
	for i := 0; i < n; i++ {
		distances[i] = l2Squared(vectors[i], centroids[0])
	}

	for c := 1; c < k; c++ {
		var totalDist float64
		for i := 0; i < n; i++ {
			totalDist += float64(distances[i])
		}

		var selected int
		if totalDist == 0 {
			selected = rng.IntN(n)
		} else {
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

		for i := 0; i < n; i++ {
			if d := l2Squared(vectors[i], centroids[c]); d < distances[i] {
				distances[i] = d
			}
		}
	}

	return centroids
}

// kmeansPlusPlusInitEqual reports whether got and want selected the same
// centroids, comparing by value rather than by the (freshly-allocated, so
// never pointer-equal) slice headers.
func kmeansPlusPlusInitEqual(t *testing.T, got, want [][]float32) bool {
	t.Helper()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !assert.ObjectsAreEqual(got[i], want[i]) {
			return false
		}
	}
	return true
}

// TestKMeansPlusPlusInit_OverflowDistanceClamped makes kmeansCluster's
// first-centroid draw seedable (kmeansPlusPlusInit takes an injected
// *rand.Rand) specifically so this test can discriminate, rather than only
// checking that the output happens to still look reasonable: reverting
// kmeansPlusPlusInit's two clampedMin call sites to a direct
// `distances[i] = l2Squared(...)` assignment -- i.e. collapsing it onto
// referenceKMeansPlusPlusInit -- makes this test fail, which a test that
// merely asserts "no panic, finite output" cannot do, since both the clamped
// and unclamped forms copy finite input vectors into their centroid slices
// either way.
//
// Control case: on ordinary, non-overflowing input, the real and reference
// implementations -- given the SAME seeded rng -- must select IDENTICAL
// centroids over many seeds, since clampedMin(current, candidate) and a bare
// "if candidate < current" comparison agree whenever candidate is finite.
//
// Overflow case: on input whose pairwise l2Squared genuinely overflows
// float32 to +Inf, the two must DIVERGE on at least one seed in the same
// range: the reference's uncontested +Inf propagates into its weighted draw
// (degenerating it to whichever row is scanned first once the running sum
// reaches +Inf, or to a NaN threshold if the random draw lands on exactly
// zero, which then defaults to the last row), while the real implementation
// clamps that +Inf away and keeps drawing from a normal, finite-weighted
// distribution.
func TestKMeansPlusPlusInit_OverflowDistanceClamped(t *testing.T) {
	// numCentroids MUST be >= 3: with only 2, the incremental fold-in call
	// site (the second of kmeansPlusPlusInit's two clampedMin uses) runs
	// once, after the LAST centroid is already selected, so its output is
	// never read by another selection and the test cannot tell whether that
	// call site is clamped. With 3, the fold-in after selecting centroid 1
	// feeds the distances selecting centroid 2 reads, so both call sites are
	// on the path this test's assertions actually exercise.
	const numCentroids = 3
	const seeds = 500

	t.Run("control: no overflow, must match the reference on every seed", func(t *testing.T) {
		vectors := [][]float32{
			{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {5, 5}, {-5, -5},
		}
		require.False(t, math.IsInf(float64(l2Squared(vectors[0], vectors[1])), 0))

		for seed := uint64(1); seed <= seeds; seed++ {
			// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
			got := kmeansPlusPlusInit(vectors, numCentroids, rand.New(rand.NewPCG(seed, seed)))
			// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
			want := referenceKMeansPlusPlusInit(rand.New(rand.NewPCG(seed, seed)), vectors, numCentroids)
			require.Truef(t, kmeansPlusPlusInitEqual(t, got, want), "seed %d: got %v, want %v", seed, got, want)
		}
	})

	t.Run("overflow: must diverge from the unclamped reference on some seed", func(t *testing.T) {
		huge := float32(1e20)
		vectors := [][]float32{
			{huge, 0}, {-huge, 0}, {0, huge}, {0, -huge},
		}
		require.True(t, math.IsInf(float64(l2Squared(vectors[0], vectors[1])), 1),
			"test setup: this dataset must overflow l2Squared for this test to prove anything")

		diverged := false
		for seed := uint64(1); seed <= seeds; seed++ {
			// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
			got := kmeansPlusPlusInit(vectors, numCentroids, rand.New(rand.NewPCG(seed, seed)))
			// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
			want := referenceKMeansPlusPlusInit(rand.New(rand.NewPCG(seed, seed)), vectors, numCentroids)

			require.Len(t, got, numCentroids)
			for _, c := range got {
				for _, v := range c {
					assert.False(t, math.IsInf(float64(v), 0), "seed %d: clamped centroid component is +/-Inf", seed)
					assert.False(t, math.IsNaN(float64(v)), "seed %d: clamped centroid component is NaN", seed)
				}
			}

			if !kmeansPlusPlusInitEqual(t, got, want) {
				diverged = true
			}
		}
		assert.True(t, diverged, "the clamped and unclamped implementations never diverged over %d seeds on overflowing input -- clampedMin may not be on the exercised path", seeds)
	})
}

// TestKMeansCluster_RepairsEmptyClustersWiredWithL2Squared pins the EXACT
// arguments PQ's call site passes -- kmeans.RepairEmptyClusters(vectors,
// assignments, counts, centroids, l2Squared) -- precisely and
// deterministically, complementing rather than replacing
// pkg/vector/kmeans's own thorough, direct tests of the shared mechanism
// itself.
//
// This test calls kmeans.RepairEmptyClusters directly, which means it
// cannot detect the production call at pq.go's own call site being removed
// or miswired -- reverting or deleting that line would leave this test
// green, since it never goes through kmeansCluster at all.
// TestKMeansCluster_RepairsEmptyClusterThroughRealRefinement below is what
// closes that gap, by calling refineAndRepair -- the actual production
// function containing that call site -- with hand-picked initial centroids
// chosen to force an empty cluster deterministically, rather than trying to
// trigger one through kmeansCluster's randomized public entry point (which
// an exhaustive search -- 50,000+ seeds across several adversarial
// datasets -- found it never does; see that test's doc comment).
func TestKMeansCluster_RepairsEmptyClustersWiredWithL2Squared(t *testing.T) {
	vectors := [][]float32{
		{0, 0}, {0.1, 0.1}, // cluster 0
		{10, 10}, {10.1, 10.1}, // cluster 1
		{20, 20}, // cluster 2, and the point about to be donated
	}
	assignments := []int{0, 0, 1, 1, 1}
	counts := []int{2, 3, 0}
	centroids := [][]float32{
		{0.05, 0.05},
		{13.4, 13.4}, // cluster 1's mean, pulled toward vectors[4]
		{13.4, 13.4}, // cluster 2: a duplicate of cluster 1's centroid
	}

	result := kmeans.RepairEmptyClusters(vectors, assignments, counts, centroids, l2Squared)

	require.Len(t, result, 3)
	distinct := map[[2]float32]bool{}
	for _, c := range result {
		distinct[[2]float32{c[0], c[1]}] = true
	}
	assert.Len(t, distinct, 3, "3 distinct positions were available, so the repair must produce 3 distinct centroids: got %v", result)
	assert.Equal(t, []float32{20, 20}, result[2])
	assert.Equal(t, 2, assignments[4])
	assert.Equal(t, []int{2, 2, 1}, counts)
}

// TestKMeansCluster_RepairsEmptyClusterThroughRealRefinement is the
// regression test for the PRODUCTION wiring: it calls refineAndRepair --
// the real function kmeansCluster delegates to, containing the actual
// kmeans.RepairEmptyClusters call site -- with hand-picked initial centroids
// designed to force an empty cluster deterministically through ordinary
// Lloyd iteration, no randomness involved.
//
// centroids 0 and 1 are set to the EXACT same point. Vectors 0-2 are also
// exactly that point, so in the very first assignment step every one of
// them computes an IDENTICAL distance to centroid 0 and centroid 1; the
// assignment loop's strict "<" comparison means ties always keep the
// first (lower-indexed) candidate, so centroid 0 claims all three and
// centroid 1 claims none -- deterministically, on every run, for any
// maxIter >= 1. Centroid 2 independently claims vectors 3-4, which are far
// from the duplicate mass and distinct from each other, giving cluster 2 a
// genuine (non-zero-distance) farthest point to donate. This is exactly
// the shape TestRepairEmptyClusters_LargestClusterIsDegenerate exercises
// at the shared-function level (a large duplicate-cored cluster beside a
// smaller but genuinely spread one) -- reproduced here through the real
// kmeansCluster refinement path.
//
// Removing or miswiring refineAndRepair's call to
// kmeans.RepairEmptyClusters makes this test fail: without it, cluster 1
// stays exactly the duplicate it started as, and only 2 of the 3 available
// distinct positions end up represented.
func TestKMeansCluster_RepairsEmptyClusterThroughRealRefinement(t *testing.T) {
	vectors := [][]float32{
		{0, 0}, {0, 0}, {0, 0}, // 3 exact duplicates
		{10, 0}, {20, 0}, // 2 distinct points, far from the duplicate mass
	}
	initialCentroids := [][]float32{
		{0, 0}, // claims all 3 duplicates
		{0, 0}, // an exact duplicate of centroid 0: claims nothing, by the
		// tie-breaking argument above
		{15, 0}, // claims both far points
	}

	result := refineAndRepair(vectors, initialCentroids, 3, 1)

	require.Len(t, result, 3)
	distinct := map[[2]float32]bool{}
	for _, c := range result {
		distinct[[2]float32{c[0], c[1]}] = true
	}
	assert.Len(t, distinct, 3,
		"3 distinct positions were available (the duplicate mass, and each of the 2 far points), so refineAndRepair must produce 3 distinct centroids: got %v", result)
}

func BenchmarkPQ_Encode(b *testing.B) {
	dim := 128
	m := 8
	nbits := 8

	pq, _ := NewPQQuantizer(dim, m, nbits)
	vectors := generateRandomVectors(1000, dim)
	_ = pq.Train(vectors)
	testVec := vectors[0]

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, _ = pq.Encode(testVec)
	}
}

func BenchmarkPQ_AsymmetricDistance(b *testing.B) {
	dim := 128
	m := 8
	nbits := 8

	pq, _ := NewPQQuantizer(dim, m, nbits)
	vectors := generateRandomVectors(1000, dim)
	_ = pq.Train(vectors)

	query := vectors[0]
	codes, _ := pq.Encode(vectors[1])
	table, _ := pq.ComputeDistanceTable(query, hnsw.L2Distance)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = pq.AsymmetricDistanceFromTable(table, codes)
	}
}

func BenchmarkPQ_ComputeDistanceTable(b *testing.B) {
	dim := 128
	m := 8
	nbits := 8

	pq, _ := NewPQQuantizer(dim, m, nbits)
	vectors := generateRandomVectors(1000, dim)
	_ = pq.Train(vectors)
	query := vectors[0]

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, _ = pq.ComputeDistanceTable(query, hnsw.L2Distance)
	}
}
