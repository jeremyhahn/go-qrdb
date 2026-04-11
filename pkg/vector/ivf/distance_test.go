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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestL2Distance(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 0.0,
		},
		{
			name:     "different vectors",
			a:        []float32{0.0, 0.0, 0.0},
			b:        []float32{3.0, 4.0, 0.0},
			expected: 5.0, // sqrt(9 + 16) = 5
		},
		{
			name:     "negative values",
			a:        []float32{-1.0, -2.0, -3.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: float32(math.Sqrt(4 + 16 + 36)),
		},
		{
			name:     "mismatched dimensions",
			a:        []float32{1.0, 2.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: float32(math.MaxFloat32),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := L2(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, 0.0001)
		})
	}
}

func TestL2Squared(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 0.0,
		},
		{
			name:     "different vectors",
			a:        []float32{0.0, 0.0, 0.0},
			b:        []float32{3.0, 4.0, 0.0},
			expected: 25.0, // 9 + 16
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := L2Squared(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, 0.0001)
		})
	}
}

func TestCosineDistance(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 0.0, // cosine similarity = 1, distance = 0
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{0.0, 1.0, 0.0},
			expected: 1.0, // cosine similarity = 0, distance = 1
		},
		{
			name:     "opposite vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{-1.0, 0.0, 0.0},
			expected: 2.0, // cosine similarity = -1, distance = 2
		},
		{
			name:     "zero vector a",
			a:        []float32{0.0, 0.0, 0.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 1.0, // Maximum distance
		},
		{
			name:     "zero vector b",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{0.0, 0.0, 0.0},
			expected: 1.0, // Maximum distance
		},
		{
			name:     "mismatched dimensions",
			a:        []float32{1.0, 2.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: float32(math.MaxFloat32),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Cosine(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, 0.0001)
		})
	}
}

func TestDotProduct(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
	}{
		{
			name:     "positive dot product",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: -14.0, // -(1 + 4 + 9) = -14
		},
		{
			name:     "zero dot product",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{0.0, 1.0, 0.0},
			expected: 0.0,
		},
		{
			name:     "negative dot product",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{-1.0, 0.0, 0.0},
			expected: 1.0, // -(-1) = 1
		},
		{
			name:     "mismatched dimensions",
			a:        []float32{1.0, 2.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: float32(math.MaxFloat32),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DotProduct(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, 0.0001)
		})
	}
}

func TestL2SIMD(t *testing.T) {
	// Test that SIMD version produces same results as regular version
	tests := []struct {
		name string
		a    []float32
		b    []float32
	}{
		{
			name: "small vector",
			a:    []float32{1.0, 2.0, 3.0},
			b:    []float32{4.0, 5.0, 6.0},
		},
		{
			name: "exact multiple of 4",
			a:    []float32{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0},
			b:    []float32{8.0, 7.0, 6.0, 5.0, 4.0, 3.0, 2.0, 1.0},
		},
		{
			name: "not multiple of 4",
			a:    []float32{1.0, 2.0, 3.0, 4.0, 5.0},
			b:    []float32{5.0, 4.0, 3.0, 2.0, 1.0},
		},
		{
			name: "large vector",
			a:    make([]float32, 128),
			b:    make([]float32, 128),
		},
	}

	// Initialize large vector test
	for i := range tests[3].a {
		tests[3].a[i] = float32(i)
		tests[3].b[i] = float32(128 - i)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			regular := L2(tt.a, tt.b)
			simd := L2SIMD(tt.a, tt.b)
			assert.InDelta(t, regular, simd, 0.0001)
		})
	}
}

func TestL2SquaredSIMD(t *testing.T) {
	a := make([]float32, 128)
	b := make([]float32, 128)

	for i := range a {
		a[i] = float32(i)
		b[i] = float32(128 - i)
	}

	regular := L2Squared(a, b)
	simd := L2SquaredSIMD(a, b)

	assert.InDelta(t, regular, simd, 0.0001)
}

func TestCosineSIMD(t *testing.T) {
	a := make([]float32, 128)
	b := make([]float32, 128)

	for i := range a {
		a[i] = float32(i + 1)
		b[i] = float32(128 - i)
	}

	regular := Cosine(a, b)
	simd := CosineSIMD(a, b)

	assert.InDelta(t, regular, simd, 0.0001)
}

func TestGetDistanceFunc(t *testing.T) {
	tests := []struct {
		metric DistanceMetric
		a      []float32
		b      []float32
	}{
		{L2Distance, []float32{1.0, 2.0, 3.0}, []float32{4.0, 5.0, 6.0}},
		{CosineDistance, []float32{1.0, 2.0, 3.0}, []float32{4.0, 5.0, 6.0}},
		{DotProductDistance, []float32{1.0, 2.0, 3.0}, []float32{4.0, 5.0, 6.0}},
		{DistanceMetric(999), []float32{1.0, 2.0, 3.0}, []float32{4.0, 5.0, 6.0}}, // Unknown defaults to L2
	}

	for _, tt := range tests {
		t.Run(tt.metric.String(), func(t *testing.T) {
			distFunc := GetDistanceFunc(tt.metric)
			assert.NotNil(t, distFunc)

			// Should return a valid distance
			dist := distFunc(tt.a, tt.b)
			assert.False(t, math.IsNaN(float64(dist)))
			assert.False(t, math.IsInf(float64(dist), 0))
		})
	}
}

// Benchmarks
func BenchmarkL2(b *testing.B) {
	a := make([]float32, 1536)
	vec := make([]float32, 1536)
	for i := range a {
		a[i] = float32(i)
		vec[i] = float32(1536 - i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = L2(a, vec)
	}
}

func BenchmarkL2SIMD(b *testing.B) {
	a := make([]float32, 1536)
	vec := make([]float32, 1536)
	for i := range a {
		a[i] = float32(i)
		vec[i] = float32(1536 - i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = L2SIMD(a, vec)
	}
}

func BenchmarkCosine(b *testing.B) {
	a := make([]float32, 1536)
	vec := make([]float32, 1536)
	for i := range a {
		a[i] = float32(i + 1)
		vec[i] = float32(1536 - i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Cosine(a, vec)
	}
}

func BenchmarkCosineSIMD(b *testing.B) {
	a := make([]float32, 1536)
	vec := make([]float32, 1536)
	for i := range a {
		a[i] = float32(i + 1)
		vec[i] = float32(1536 - i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CosineSIMD(a, vec)
	}
}
