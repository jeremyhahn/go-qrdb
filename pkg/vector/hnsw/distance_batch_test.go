//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package hnsw

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateBatchVectors creates a batch of reproducible random vectors.
func generateBatchVectors(seed int64, count, dim int) ([]float32, [][]float32) {
	rng := rand.New(rand.NewSource(seed))
	query := make([]float32, dim)
	for i := range query {
		query[i] = rng.Float32()*2 - 1
	}

	vectors := make([][]float32, count)
	for j := 0; j < count; j++ {
		v := make([]float32, dim)
		for i := range v {
			v[i] = rng.Float32()*2 - 1
		}
		vectors[j] = v
	}
	return query, vectors
}

func TestCalcDistanceBatchCorrectness(t *testing.T) {
	query, vectors := generateBatchVectors(50, 100, 128)

	metrics := []struct {
		name   string
		metric DistanceMetric
	}{
		{"L2", L2Distance},
		{"Cosine", CosineDistance},
		{"DotProduct", DotProductDistance},
		{"Manhattan", ManhattanDistance},
	}

	for _, tt := range metrics {
		t.Run(tt.name, func(t *testing.T) {
			results, err := CalcDistanceBatch(tt.metric, query, vectors)
			require.NoError(t, err)
			require.Len(t, results, len(vectors))

			distFn := GetDistanceFunc(tt.metric)
			for i, v := range vectors {
				expected := distFn(query, v)
				assert.InDelta(t, float64(expected), float64(results[i]), 1e-4,
					"mismatch at index %d", i)
			}
		})
	}
}

func TestCalcDistanceBatchConcurrency(t *testing.T) {
	// Use a large batch to exercise the concurrent path and verify
	// data race safety (run with -race).
	query, vectors := generateBatchVectors(51, 10000, 128)

	results, err := CalcDistanceBatch(L2Distance, query, vectors)
	require.NoError(t, err)
	require.Len(t, results, 10000)

	// Spot check a few values against individual computation.
	distFn := GetDistanceFunc(L2Distance)
	for _, idx := range []int{0, 500, 5000, 9999} {
		expected := distFn(query, vectors[idx])
		assert.InDelta(t, float64(expected), float64(results[idx]), 1e-4,
			"mismatch at index %d", idx)
	}
}

func TestCalcDistanceBatchEmptyInput(t *testing.T) {
	query := []float32{1, 2, 3}
	var vectors [][]float32

	results, err := CalcDistanceBatch(L2Distance, query, vectors)
	require.Error(t, err)
	assert.Nil(t, results)
	assert.True(t, errors.Is(err, ErrEmptyBatch))
}

func TestCalcDistanceBatchEmptyQuery(t *testing.T) {
	var query []float32
	vectors := [][]float32{{1, 2, 3}}

	results, err := CalcDistanceBatch(L2Distance, query, vectors)
	require.Error(t, err)
	assert.Nil(t, results)
	assert.True(t, errors.Is(err, ErrInvalidVector))
}

func TestCalcDistanceBatchDimensionMismatch(t *testing.T) {
	query := []float32{1, 2, 3}
	vectors := [][]float32{
		{1, 2, 3},
		{4, 5}, // Mismatched dimensions
		{7, 8, 9},
	}

	results, err := CalcDistanceBatch(L2Distance, query, vectors)
	require.Error(t, err)
	assert.Nil(t, results)
	assert.True(t, errors.Is(err, ErrDimensionMismatch))

	var dimErr *DimensionMismatchError
	require.True(t, errors.As(err, &dimErr))
	assert.Equal(t, 1, dimErr.Index)
	assert.Equal(t, 2, dimErr.Got)
	assert.Equal(t, 3, dimErr.Expected)
}

func TestCalcDistanceBatchSingleVector(t *testing.T) {
	query := []float32{1, 2, 3}
	vectors := [][]float32{{4, 5, 6}}

	results, err := CalcDistanceBatch(L2Distance, query, vectors)
	require.NoError(t, err)
	require.Len(t, results, 1)

	expected := GetDistanceFunc(L2Distance)(query, vectors[0])
	assert.InDelta(t, float64(expected), float64(results[0]), 1e-6)
}

func TestCalcDistanceBatchAllMetrics(t *testing.T) {
	query := []float32{0.5, -0.3, 0.8, 0.1}
	vectors := [][]float32{
		{0.1, 0.2, 0.3, 0.4},
		{-0.5, 0.6, -0.7, 0.8},
		{1.0, 0.0, 1.0, 0.0},
	}

	metrics := []DistanceMetric{
		L2Distance,
		CosineDistance,
		DotProductDistance,
		ManhattanDistance,
		HammingDistance,
	}

	for _, metric := range metrics {
		t.Run(intToString(int(metric)), func(t *testing.T) {
			results, err := CalcDistanceBatch(metric, query, vectors)
			require.NoError(t, err)
			require.Len(t, results, 3)

			distFn := GetDistanceFunc(metric)
			for i, v := range vectors {
				expected := distFn(query, v)
				assert.InDelta(t, float64(expected), float64(results[i]), 1e-4,
					"metric %d mismatch at index %d", metric, i)
			}
		})
	}
}

func TestCalcDistanceBatchSequentialThreshold(t *testing.T) {
	// Test at exactly the threshold boundary (256 vectors, should be sequential).
	query, vectors := generateBatchVectors(52, batchSequentialThreshold, 64)

	results, err := CalcDistanceBatch(L2Distance, query, vectors)
	require.NoError(t, err)
	require.Len(t, results, batchSequentialThreshold)

	distFn := GetDistanceFunc(L2Distance)
	for i, v := range vectors {
		expected := distFn(query, v)
		assert.InDelta(t, float64(expected), float64(results[i]), 1e-4)
	}
}

func TestCalcDistanceBatchJustAboveThreshold(t *testing.T) {
	// Test at threshold + 1 (should use concurrent path).
	count := batchSequentialThreshold + 1
	query, vectors := generateBatchVectors(53, count, 64)

	results, err := CalcDistanceBatch(L2Distance, query, vectors)
	require.NoError(t, err)
	require.Len(t, results, count)

	distFn := GetDistanceFunc(L2Distance)
	for i, v := range vectors {
		expected := distFn(query, v)
		assert.InDelta(t, float64(expected), float64(results[i]), 1e-4)
	}
}

// Benchmarks: Batch distances at various sizes

func BenchmarkCalcDistanceBatch_100(b *testing.B) {
	query, vectors := generateBatchVectors(200, 100, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcDistanceBatch(L2Distance, query, vectors) //nolint:errcheck // benchmark
	}
}

func BenchmarkCalcDistanceBatch_1000(b *testing.B) {
	query, vectors := generateBatchVectors(201, 1000, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcDistanceBatch(L2Distance, query, vectors) //nolint:errcheck // benchmark
	}
}

func BenchmarkCalcDistanceBatch_10000(b *testing.B) {
	query, vectors := generateBatchVectors(202, 10000, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcDistanceBatch(L2Distance, query, vectors) //nolint:errcheck // benchmark
	}
}

func BenchmarkCalcDistanceBatch_Cosine_1000(b *testing.B) {
	query, vectors := generateBatchVectors(203, 1000, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcDistanceBatch(CosineDistance, query, vectors) //nolint:errcheck // benchmark
	}
}

func BenchmarkCalcDistanceBatch_Manhattan_1000(b *testing.B) {
	query, vectors := generateBatchVectors(204, 1000, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcDistanceBatch(ManhattanDistance, query, vectors) //nolint:errcheck // benchmark
	}
}
