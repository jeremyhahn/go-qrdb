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

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestPQ_RecallAccuracy(t *testing.T) {
	dim := 64
	m := 8
	nbits := 8

	pq, err := NewPQQuantizer(dim, m, nbits)
	require.NoError(t, err)

	trainingVecs := generateRandomVectors(2000, dim)
	require.NoError(t, pq.Train(trainingVecs))

	queryVec := trainingVecs[0]
	dbVectors := trainingVecs[1:201] // 200 database vectors

	// Compute true top-10 using float32.
	distFn := hnsw.GetDistanceFunc(hnsw.L2Distance)
	type result struct {
		idx  int
		dist float32
	}
	trueResults := make([]result, len(dbVectors))
	for i, v := range dbVectors {
		trueResults[i] = result{idx: i, dist: distFn(queryVec, v)}
	}
	for i := 0; i < len(trueResults); i++ {
		for j := i + 1; j < len(trueResults); j++ {
			if trueResults[j].dist < trueResults[i].dist {
				trueResults[i], trueResults[j] = trueResults[j], trueResults[i]
			}
		}
	}
	trueTop10 := make(map[int]bool)
	for _, r := range trueResults[:10] {
		trueTop10[r.idx] = true
	}

	// Compute PQ top-10 using distance table.
	table, err := pq.ComputeDistanceTable(queryVec, hnsw.L2Distance)
	require.NoError(t, err)

	pqResults := make([]result, len(dbVectors))
	for i, v := range dbVectors {
		codes, err := pq.Encode(v)
		require.NoError(t, err)
		d := pq.AsymmetricDistanceFromTable(table, codes)
		pqResults[i] = result{idx: i, dist: d}
	}
	for i := 0; i < len(pqResults); i++ {
		for j := i + 1; j < len(pqResults); j++ {
			if pqResults[j].dist < pqResults[i].dist {
				pqResults[i], pqResults[j] = pqResults[j], pqResults[i]
			}
		}
	}

	recall := 0
	for _, r := range pqResults[:10] {
		if trueTop10[r.idx] {
			recall++
		}
	}

	// PQ with random data has inherent variance in recall. A threshold
	// of 30% avoids flakiness while still validating the core algorithm.
	assert.GreaterOrEqual(t, recall, 3, "PQ recall@10 should be >= 30%%, got %d/10", recall)
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
