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
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateDeterministicVectors creates reproducible random vectors using a fixed seed.
func generateDeterministicVectors(seed int64, dim int) ([]float32, []float32) {
	rng := rand.New(rand.NewSource(seed))
	a := make([]float32, dim)
	b := make([]float32, dim)
	for i := 0; i < dim; i++ {
		a[i] = rng.Float32()*2 - 1 // [-1, 1]
		b[i] = rng.Float32()*2 - 1
	}
	return a, b
}

// simdEpsilon returns an appropriate absolute tolerance for comparing scalar vs
// SIMD results at a given vector dimensionality. SIMD and scalar implementations
// use different summation orders, causing float32 accumulation drift proportional
// to sqrt(dim) * machine_epsilon. We use a conservative multiplier to avoid
// flaky tests while still catching real computation errors.
func simdEpsilon(dim int) float64 {
	// Base epsilon for float32 precision (~1e-7) scaled by sqrt(dim).
	return math.Max(1e-4, math.Sqrt(float64(dim))*2e-5)
}

func TestSIMDL2MatchesScalar(t *testing.T) {
	dims := []int{3, 128, 768, 1536}

	for _, dim := range dims {
		t.Run(intToString(dim), func(t *testing.T) {
			a, b := generateDeterministicVectors(42, dim)
			scalar := L2(a, b)
			simd := L2SIMDVek(a, b)
			assert.InDelta(t, float64(scalar), float64(simd), simdEpsilon(dim),
				"L2 SIMD result %f does not match scalar %f for dim=%d", simd, scalar, dim)
		})
	}
}

func TestSIMDL2MatchesScalar_InvalidDimensions(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	result := L2SIMDVek(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

func TestSIMDCosineMatchesScalar(t *testing.T) {
	dims := []int{3, 128, 768, 1536}

	for _, dim := range dims {
		t.Run(intToString(dim), func(t *testing.T) {
			a, b := generateDeterministicVectors(43, dim)
			scalar := Cosine(a, b)
			simd := CosineSIMDVek(a, b)
			assert.InDelta(t, float64(scalar), float64(simd), simdEpsilon(dim),
				"Cosine SIMD result %f does not match scalar %f for dim=%d", simd, scalar, dim)
		})
	}
}

func TestSIMDCosineMatchesScalar_InvalidDimensions(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	result := CosineSIMDVek(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

func TestSIMDDotProductMatchesScalar(t *testing.T) {
	dims := []int{3, 128, 768, 1536}

	for _, dim := range dims {
		t.Run(intToString(dim), func(t *testing.T) {
			a, b := generateDeterministicVectors(44, dim)
			scalar := DotProduct(a, b)
			simd := DotProductSIMDVek(a, b)
			assert.InDelta(t, float64(scalar), float64(simd), simdEpsilon(dim),
				"DotProduct SIMD result %f does not match scalar %f for dim=%d", simd, scalar, dim)
		})
	}
}

func TestSIMDDotProductMatchesScalar_InvalidDimensions(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	result := DotProductSIMDVek(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

func TestSIMDManhattanMatchesScalar(t *testing.T) {
	dims := []int{3, 128, 768, 1536}

	for _, dim := range dims {
		t.Run(intToString(dim), func(t *testing.T) {
			a, b := generateDeterministicVectors(45, dim)
			scalar := Manhattan(a, b)
			simd := ManhattanSIMD(a, b)
			assert.InDelta(t, float64(scalar), float64(simd), simdEpsilon(dim),
				"Manhattan SIMD result %f does not match scalar %f for dim=%d", simd, scalar, dim)
		})
	}
}

func TestSIMDManhattanMatchesScalar_InvalidDimensions(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	result := ManhattanSIMD(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

func TestSIMDMismatchedDimensions(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{1.0, 2.0}

	tests := []struct {
		name string
		fn   DistanceFunc
	}{
		{"L2SIMDVek", L2SIMDVek},
		{"CosineSIMDVek", CosineSIMDVek},
		{"DotProductSIMDVek", DotProductSIMDVek},
		{"ManhattanSIMD", ManhattanSIMD},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.fn(a, b)
			assert.Equal(t, float32(math.MaxFloat32), result)
		})
	}
}

func TestSIMDEmptyVectors(t *testing.T) {
	a := []float32{}
	b := []float32{}

	tests := []struct {
		name     string
		fn       DistanceFunc
		expected float32
	}{
		{"L2SIMDVek", L2SIMDVek, 0},
		{"CosineSIMDVek", CosineSIMDVek, 1.0},
		{"DotProductSIMDVek", DotProductSIMDVek, 0},
		{"ManhattanSIMD", ManhattanSIMD, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.fn(a, b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSIMDZeroVectors(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{0, 0, 0}

	t.Run("L2SIMDVek returns 0", func(t *testing.T) {
		result := L2SIMDVek(a, b)
		assert.InDelta(t, 0.0, float64(result), 1e-6)
	})

	t.Run("CosineSIMDVek returns 1.0 for zero vectors", func(t *testing.T) {
		result := CosineSIMDVek(a, b)
		assert.Equal(t, float32(1.0), result)
	})

	t.Run("DotProductSIMDVek returns 0", func(t *testing.T) {
		result := DotProductSIMDVek(a, b)
		assert.InDelta(t, 0.0, float64(result), 1e-6)
	})

	t.Run("ManhattanSIMD returns 0", func(t *testing.T) {
		result := ManhattanSIMD(a, b)
		assert.InDelta(t, 0.0, float64(result), 1e-6)
	})
}

func TestSIMDCosineZeroAndNonZero(t *testing.T) {
	zero := []float32{0, 0, 0}
	nonZero := []float32{1, 2, 3}

	t.Run("first zero", func(t *testing.T) {
		result := CosineSIMDVek(zero, nonZero)
		assert.Equal(t, float32(1.0), result)
	})

	t.Run("second zero", func(t *testing.T) {
		result := CosineSIMDVek(nonZero, zero)
		assert.Equal(t, float32(1.0), result)
	})
}

func TestSIMDIdenticalVectors(t *testing.T) {
	a := []float32{1.5, -2.3, 0.7, 4.1}

	t.Run("L2SIMDVek identical returns 0", func(t *testing.T) {
		result := L2SIMDVek(a, a)
		assert.InDelta(t, 0.0, float64(result), 1e-6)
	})

	t.Run("CosineSIMDVek identical returns 0", func(t *testing.T) {
		result := CosineSIMDVek(a, a)
		assert.InDelta(t, 0.0, float64(result), 1e-4)
	})

	t.Run("ManhattanSIMD identical returns 0", func(t *testing.T) {
		result := ManhattanSIMD(a, a)
		assert.InDelta(t, 0.0, float64(result), 1e-6)
	})
}

func TestManhattanScalar(t *testing.T) {
	t.Run("known values", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{4, 1, 0}
		// |1-4| + |2-1| + |3-0| = 3 + 1 + 3 = 7
		result := Manhattan(a, b)
		assert.InDelta(t, 7.0, float64(result), 1e-6)
	})

	t.Run("mismatched dimensions", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1}
		result := Manhattan(a, b)
		assert.Equal(t, float32(math.MaxFloat32), result)
	})
}

func TestHamming(t *testing.T) {
	t.Run("identical binary vectors", func(t *testing.T) {
		a := []float32{1.0, 0.0, 1.0, 0.0}
		b := []float32{1.0, 0.0, 1.0, 0.0}
		result := Hamming(a, b)
		assert.Equal(t, float32(0), result)
	})

	t.Run("completely different binary vectors", func(t *testing.T) {
		a := []float32{1.0, 0.0, 1.0, 0.0}
		b := []float32{0.0, 1.0, 0.0, 1.0}
		result := Hamming(a, b)
		assert.Equal(t, float32(4), result)
	})

	t.Run("threshold at 0.5", func(t *testing.T) {
		// 0.6 >= 0.5 -> 1, 0.4 < 0.5 -> 0, 0.5 >= 0.5 -> 1, 0.49 < 0.5 -> 0
		a := []float32{0.6, 0.4, 0.5, 0.49}
		b := []float32{0.5, 0.5, 0.4, 0.6}
		// a bits: 1, 0, 1, 0
		// b bits: 1, 1, 0, 1
		// diff:   0, 1, 1, 1 = 3
		result := Hamming(a, b)
		assert.Equal(t, float32(3), result)
	})

	t.Run("mismatched dimensions", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{1}
		result := Hamming(a, b)
		assert.Equal(t, float32(math.MaxFloat32), result)
	})
}

func TestGetDistanceFuncMapDispatch(t *testing.T) {
	metrics := []struct {
		name   string
		metric DistanceMetric
	}{
		{"L2Distance", L2Distance},
		{"CosineDistance", CosineDistance},
		{"DotProductDistance", DotProductDistance},
		{"ManhattanDistance", ManhattanDistance},
		{"HammingDistance", HammingDistance},
	}

	a := []float32{1, 2, 3}
	b := []float32{4, 5, 6}

	for _, tt := range metrics {
		t.Run(tt.name, func(t *testing.T) {
			fn := GetDistanceFunc(tt.metric)
			require.NotNil(t, fn)
			result := fn(a, b)
			assert.NotEqual(t, float32(math.MaxFloat32), result)
		})
	}
}

func TestGetDistanceFuncUnknownMetric(t *testing.T) {
	fn := GetDistanceFunc(DistanceMetric(999))
	require.NotNil(t, fn)

	a := []float32{1, 2, 3}
	b := []float32{4, 5, 6}
	result := fn(a, b)
	// Should fall back to L2
	expected := L2SIMDVek(a, b)
	assert.InDelta(t, float64(expected), float64(result), 1e-6)
}

// intToString converts a dimension int to a string for subtest naming
// without importing strconv (keeps imports minimal in test files).
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 5)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	// Reverse the digits.
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

// Benchmarks: L2

func BenchmarkL2Scalar_128(b *testing.B) {
	a, vec := generateDeterministicVectors(100, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2(a, vec)
	}
}

func BenchmarkL2SIMDVek_128(b *testing.B) {
	a, vec := generateDeterministicVectors(100, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2SIMDVek(a, vec)
	}
}

func BenchmarkL2Scalar_768(b *testing.B) {
	a, vec := generateDeterministicVectors(100, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2(a, vec)
	}
}

func BenchmarkL2SIMDVek_768(b *testing.B) {
	a, vec := generateDeterministicVectors(100, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2SIMDVek(a, vec)
	}
}

func BenchmarkL2Scalar_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(100, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2(a, vec)
	}
}

func BenchmarkL2SIMDVek_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(100, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2SIMDVek(a, vec)
	}
}

// Benchmarks: Cosine

func BenchmarkCosineScalar_128(b *testing.B) {
	a, vec := generateDeterministicVectors(101, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Cosine(a, vec)
	}
}

func BenchmarkCosineSIMDVek_128(b *testing.B) {
	a, vec := generateDeterministicVectors(101, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CosineSIMDVek(a, vec)
	}
}

func BenchmarkCosineScalar_768(b *testing.B) {
	a, vec := generateDeterministicVectors(101, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Cosine(a, vec)
	}
}

func BenchmarkCosineSIMDVek_768(b *testing.B) {
	a, vec := generateDeterministicVectors(101, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CosineSIMDVek(a, vec)
	}
}

func BenchmarkCosineScalar_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(101, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Cosine(a, vec)
	}
}

func BenchmarkCosineSIMDVek_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(101, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CosineSIMDVek(a, vec)
	}
}

// Benchmarks: DotProduct

func BenchmarkDotProductScalar_128(b *testing.B) {
	a, vec := generateDeterministicVectors(102, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DotProduct(a, vec)
	}
}

func BenchmarkDotProductSIMDVek_128(b *testing.B) {
	a, vec := generateDeterministicVectors(102, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DotProductSIMDVek(a, vec)
	}
}

func BenchmarkDotProductScalar_768(b *testing.B) {
	a, vec := generateDeterministicVectors(102, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DotProduct(a, vec)
	}
}

func BenchmarkDotProductSIMDVek_768(b *testing.B) {
	a, vec := generateDeterministicVectors(102, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DotProductSIMDVek(a, vec)
	}
}

func BenchmarkDotProductScalar_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(102, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DotProduct(a, vec)
	}
}

func BenchmarkDotProductSIMDVek_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(102, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DotProductSIMDVek(a, vec)
	}
}

// Benchmarks: Manhattan

func BenchmarkManhattanScalar_128(b *testing.B) {
	a, vec := generateDeterministicVectors(103, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Manhattan(a, vec)
	}
}

func BenchmarkManhattanSIMD_128(b *testing.B) {
	a, vec := generateDeterministicVectors(103, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ManhattanSIMD(a, vec)
	}
}

func BenchmarkManhattanScalar_768(b *testing.B) {
	a, vec := generateDeterministicVectors(103, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Manhattan(a, vec)
	}
}

func BenchmarkManhattanSIMD_768(b *testing.B) {
	a, vec := generateDeterministicVectors(103, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ManhattanSIMD(a, vec)
	}
}

func BenchmarkManhattanScalar_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(103, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Manhattan(a, vec)
	}
}

func BenchmarkManhattanSIMD_1536(b *testing.B) {
	a, vec := generateDeterministicVectors(103, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ManhattanSIMD(a, vec)
	}
}
