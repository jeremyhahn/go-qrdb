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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
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
func pqRecallAt10(t *testing.T, dim, m, nbits int) int {
	t.Helper()

	pq, err := NewPQQuantizer(dim, m, nbits)
	require.NoError(t, err)

	trainingVecs := generateRandomVectors(800, dim)
	require.NoError(t, pq.Train(trainingVecs))

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
// It averages over several independent trials rather than asserting on one.
// PQ's k-means++ initialization draws from the GLOBAL rand source
// (pq.go), so each training run produces a different codebook and a
// single-trial recall varies widely: measured over 30 runs the distribution
// was {2:1, 3:4, 4:8, 5:8, 6:8, 7:1}. A one-shot `recall >= 3` therefore
// failed roughly 3% of the time -- which is exactly what happened in a full
// suite run on 2026-08-15, passing five times in isolation straight after.
//
// Averaging keeps the assertion meaningful: a genuinely broken quantizer
// returns essentially random neighbours (expected recall 0.5/10), which is far
// below the bound, while ordinary codebook variance no longer trips it.
func TestPQ_RecallAccuracy(t *testing.T) {
	// Sized for CI: k-means training dominates the runtime and runs under
	// -race on a shared runner, where 5 trials over 2000 training vectors
	// ran ~8 minutes and blew the test binary's 10-minute budget. 3 trials
	// over 800 vectors keeps the statistical margin intact — a broken
	// quantizer still measures ~0.5/10 mean recall against the 3.0 bound,
	// while a trained codebook comfortably clears it — at a fraction of
	// the cost.
	const (
		dim, m, nbits = 64, 8, 8
		trials        = 3
		minMeanRecall = 3.0
	)

	total := 0
	for i := 0; i < trials; i++ {
		total += pqRecallAt10(t, dim, m, nbits)
	}
	mean := float64(total) / float64(trials)

	assert.GreaterOrEqual(t, mean, minMeanRecall,
		"PQ mean recall@10 over %d trials should be >= %.1f, got %.1f", trials, minMeanRecall, mean)
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
