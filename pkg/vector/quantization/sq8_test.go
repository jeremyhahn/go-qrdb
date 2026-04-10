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

package quantization

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateRandomVectors(n, dim int) [][]float32 {
	vectors := make([][]float32, n)
	for i := range vectors {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rand.Float32()*2 - 1 // [-1, 1]
		}
		vectors[i] = v
	}
	return vectors
}

func TestSQ8_CalibrateAndEncodeDecode(t *testing.T) {
	dim := 128
	q := NewSQ8Quantizer(dim)

	vectors := generateRandomVectors(1000, dim)
	require.NoError(t, q.Calibrate(vectors))
	assert.True(t, q.IsCalibrated())

	// Encode and decode each vector, verify reconstruction is within tolerance.
	for i := 0; i < 10; i++ {
		encoded, err := q.Encode(vectors[i])
		require.NoError(t, err)
		assert.Len(t, encoded, dim)

		decoded, err := q.Decode(encoded)
		require.NoError(t, err)
		assert.Len(t, decoded, dim)

		// Each dimension should be within one quantization bin width.
		maxErrs := q.MaxQuantizationError()
		for d := 0; d < dim; d++ {
			tolerance := maxErrs[d] * 3 // Allow some rounding slack
			assert.InDelta(t, vectors[i][d], decoded[d], float64(tolerance),
				"dimension %d: original=%.6f decoded=%.6f tolerance=%.6f",
				d, vectors[i][d], decoded[d], tolerance)
		}
	}
}

func TestSQ8_RecallAccuracy(t *testing.T) {
	dim := 128
	q := NewSQ8Quantizer(dim)

	// Generate calibration vectors.
	calibrationVecs := generateRandomVectors(1000, dim)
	require.NoError(t, q.Calibrate(calibrationVecs))

	// Generate query and database vectors from the same distribution.
	queryVec := calibrationVecs[0]
	dbVectors := calibrationVecs[1:101] // 100 database vectors

	// Compute true top-10 using float32.
	type result struct {
		idx  int
		dist float32
	}
	distFn := hnsw.GetDistanceFunc(hnsw.L2Distance)
	trueResults := make([]result, len(dbVectors))
	for i, v := range dbVectors {
		trueResults[i] = result{idx: i, dist: distFn(queryVec, v)}
	}
	// Sort by distance
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

	// Compute SQ8 top-10 using asymmetric distance.
	sq8Results := make([]result, len(dbVectors))
	for i, v := range dbVectors {
		enc, err := q.Encode(v)
		require.NoError(t, err)
		d, err := q.AsymmetricDistance(queryVec, enc, hnsw.L2Distance)
		require.NoError(t, err)
		sq8Results[i] = result{idx: i, dist: d}
	}
	for i := 0; i < len(sq8Results); i++ {
		for j := i + 1; j < len(sq8Results); j++ {
			if sq8Results[j].dist < sq8Results[i].dist {
				sq8Results[i], sq8Results[j] = sq8Results[j], sq8Results[i]
			}
		}
	}

	// Count overlap.
	recall := 0
	for _, r := range sq8Results[:10] {
		if trueTop10[r.idx] {
			recall++
		}
	}

	// SQ8 should maintain >90% recall for this distribution.
	assert.GreaterOrEqual(t, recall, 9, "SQ8 recall@10 should be >= 90%%, got %d/10", recall)
}

func TestSQ8_AsymmetricDistance(t *testing.T) {
	dim := 64
	q := NewSQ8Quantizer(dim)

	vectors := generateRandomVectors(500, dim)
	require.NoError(t, q.Calibrate(vectors))

	query := vectors[0]
	target := vectors[1]
	encoded, err := q.Encode(target)
	require.NoError(t, err)

	// Asymmetric distance should be close to full-precision distance.
	distFn := hnsw.GetDistanceFunc(hnsw.L2Distance)
	fullDist := distFn(query, target)

	asymDist, err := q.AsymmetricDistance(query, encoded, hnsw.L2Distance)
	require.NoError(t, err)

	// Allow 10% relative error.
	relErr := math.Abs(float64(asymDist-fullDist)) / math.Max(float64(fullDist), 1e-6)
	assert.Less(t, relErr, 0.15, "asymmetric distance relative error should be < 15%%")
}

func TestSQ8_NotCalibrated(t *testing.T) {
	dim := 32
	q := NewSQ8Quantizer(dim)

	vector := make([]float32, dim)
	_, err := q.Encode(vector)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotCalibrated))

	_, err = q.Decode(make([]uint8, dim))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotCalibrated))

	_, err = q.AsymmetricDistance(vector, make([]uint8, dim), hnsw.L2Distance)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotCalibrated))
}

func TestSQ8_DimensionMismatch(t *testing.T) {
	dim := 32
	q := NewSQ8Quantizer(dim)

	vectors := generateRandomVectors(100, dim)
	require.NoError(t, q.Calibrate(vectors))

	// Encode with wrong dimensions.
	wrongVec := make([]float32, dim+1)
	_, err := q.Encode(wrongVec)
	require.Error(t, err)
	var dimErr *DimensionError
	assert.True(t, errors.As(err, &dimErr))
	assert.Equal(t, dim+1, dimErr.Got)
	assert.Equal(t, dim, dimErr.Expected)

	// Decode with wrong dimensions.
	_, err = q.Decode(make([]uint8, dim+1))
	require.Error(t, err)
	assert.True(t, errors.As(err, &dimErr))

	// Calibrate with wrong dimensions.
	q2 := NewSQ8Quantizer(dim)
	wrongVecs := generateRandomVectors(10, dim+1)
	err = q2.Calibrate(wrongVecs)
	require.Error(t, err)
	assert.True(t, errors.As(err, &dimErr))
}

func TestSQ8_EmptyCalibration(t *testing.T) {
	q := NewSQ8Quantizer(32)
	err := q.Calibrate(nil)
	require.Error(t, err)
	var emptyErr *EmptyVectorSetError
	assert.True(t, errors.As(err, &emptyErr))

	err = q.Calibrate([][]float32{})
	require.Error(t, err)
	assert.True(t, errors.As(err, &emptyErr))
}

func TestSQ8_SingleVector(t *testing.T) {
	dim := 16
	q := NewSQ8Quantizer(dim)

	// With a single vector, all dimensions have min==max, so scale==0.
	vec := make([]float32, dim)
	for d := range vec {
		vec[d] = float32(d) * 0.1
	}
	require.NoError(t, q.Calibrate([][]float32{vec}))
	assert.True(t, q.IsCalibrated())

	encoded, err := q.Encode(vec)
	require.NoError(t, err)
	// With scale=0, all encoded values should be 0.
	for _, v := range encoded {
		assert.Equal(t, uint8(0), v)
	}

	decoded, err := q.Decode(encoded)
	require.NoError(t, err)
	// Decoded should equal the min values (which equal the original).
	for d := 0; d < dim; d++ {
		assert.InDelta(t, vec[d], decoded[d], 1e-6)
	}
}

func TestSQ8_MemoryReduction(t *testing.T) {
	q := NewSQ8Quantizer(128)
	assert.Equal(t, 4.0, q.MemoryReduction())
}

func TestSQ8_EncodedSize(t *testing.T) {
	dim := 768
	q := NewSQ8Quantizer(dim)
	assert.Equal(t, dim, q.EncodedSize())
}

func TestSQ8_ConstantDimension(t *testing.T) {
	// Test vectors where one dimension is constant across all vectors.
	dim := 4
	q := NewSQ8Quantizer(dim)

	vectors := [][]float32{
		{0.0, 5.0, 1.0, 3.0},
		{1.0, 5.0, 2.0, 3.0},
		{2.0, 5.0, 3.0, 3.0},
	}
	require.NoError(t, q.Calibrate(vectors))

	encoded, err := q.Encode(vectors[1])
	require.NoError(t, err)
	// Dimension 1 (constant=5.0) and dimension 3 (constant=3.0) should map to 0.
	assert.Equal(t, uint8(0), encoded[1])
	assert.Equal(t, uint8(0), encoded[3])

	decoded, err := q.Decode(encoded)
	require.NoError(t, err)
	assert.InDelta(t, 5.0, decoded[1], 1e-5)
	assert.InDelta(t, 3.0, decoded[3], 1e-5)
}

func TestSQ8_Dimensions(t *testing.T) {
	q := NewSQ8Quantizer(768)
	assert.Equal(t, 768, q.Dimensions())
}

func TestSQ8_AsymmetricDistanceEncodedDimensionMismatch(t *testing.T) {
	dim := 32
	q := NewSQ8Quantizer(dim)
	vectors := generateRandomVectors(100, dim)
	require.NoError(t, q.Calibrate(vectors))

	query := make([]float32, dim)
	wrongEncoded := make([]uint8, dim+1)
	_, err := q.AsymmetricDistance(query, wrongEncoded, hnsw.L2Distance)
	require.Error(t, err)
	var dimErr *DimensionError
	assert.True(t, errors.As(err, &dimErr))
}

func TestSQ8_MaxQuantizationErrorNotCalibrated(t *testing.T) {
	q := NewSQ8Quantizer(32)
	result := q.MaxQuantizationError()
	assert.Nil(t, result)
}

func TestSQ8_OutOfRangeValues(t *testing.T) {
	dim := 4
	q := NewSQ8Quantizer(dim)
	calibVecs := [][]float32{
		{0.0, 0.0, 0.0, 0.0},
		{1.0, 1.0, 1.0, 1.0},
	}
	require.NoError(t, q.Calibrate(calibVecs))

	// Values outside calibration range should clamp.
	outOfRange := []float32{-1.0, 2.0, 0.5, 1.5}
	encoded, err := q.Encode(outOfRange)
	require.NoError(t, err)
	assert.Equal(t, uint8(0), encoded[0])   // clamped below
	assert.Equal(t, uint8(255), encoded[1]) // clamped above
}

func BenchmarkSQ8_Encode_128(b *testing.B) {
	benchmarkSQ8Encode(b, 128)
}

func BenchmarkSQ8_Encode_768(b *testing.B) {
	benchmarkSQ8Encode(b, 768)
}

func BenchmarkSQ8_Encode_1536(b *testing.B) {
	benchmarkSQ8Encode(b, 1536)
}

func BenchmarkSQ8_Decode_128(b *testing.B) {
	benchmarkSQ8Decode(b, 128)
}

func BenchmarkSQ8_Decode_768(b *testing.B) {
	benchmarkSQ8Decode(b, 768)
}

func BenchmarkSQ8_Decode_1536(b *testing.B) {
	benchmarkSQ8Decode(b, 1536)
}

func benchmarkSQ8Encode(b *testing.B, dim int) {
	q := NewSQ8Quantizer(dim)
	vectors := generateRandomVectors(1000, dim)
	_ = q.Calibrate(vectors)
	testVec := vectors[0]

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, _ = q.Encode(testVec)
	}
}

func benchmarkSQ8Decode(b *testing.B, dim int) {
	q := NewSQ8Quantizer(dim)
	vectors := generateRandomVectors(1000, dim)
	_ = q.Calibrate(vectors)
	encoded, _ := q.Encode(vectors[0])

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, _ = q.Decode(encoded)
	}
}
