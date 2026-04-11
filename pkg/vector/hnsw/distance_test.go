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

package hnsw

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestL2(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
		epsilon  float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1, 2, 3},
			b:        []float32{1, 2, 3},
			expected: 0,
			epsilon:  1e-6,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{0, 1, 0},
			expected: 1.414213562,
			epsilon:  1e-6,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1, 0},
			b:        []float32{-1, 0},
			expected: 2,
			epsilon:  1e-6,
		},
		{
			name:     "zero vectors",
			a:        []float32{0, 0, 0},
			b:        []float32{0, 0, 0},
			expected: 0,
			epsilon:  1e-6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := L2(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, float64(tt.epsilon))
		})
	}
}

func TestL2_InvalidDimensions(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}

	result := L2(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

func TestCosine(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
		epsilon  float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1, 2, 3},
			b:        []float32{1, 2, 3},
			expected: 0,
			epsilon:  1e-6,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{0, 1, 0},
			expected: 1,
			epsilon:  1e-6,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1, 0},
			b:        []float32{-1, 0},
			expected: 2,
			epsilon:  1e-6,
		},
		{
			name:     "parallel vectors",
			a:        []float32{1, 1, 1},
			b:        []float32{2, 2, 2},
			expected: 0,
			epsilon:  1e-6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Cosine(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, float64(tt.epsilon))
		})
	}
}

func TestCosine_ZeroVectors(t *testing.T) {
	tests := []struct {
		name string
		a    []float32
		b    []float32
	}{
		{
			name: "both zero",
			a:    []float32{0, 0, 0},
			b:    []float32{0, 0, 0},
		},
		{
			name: "first zero",
			a:    []float32{0, 0, 0},
			b:    []float32{1, 2, 3},
		},
		{
			name: "second zero",
			a:    []float32{1, 2, 3},
			b:    []float32{0, 0, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Cosine(tt.a, tt.b)
			assert.Equal(t, float32(1.0), result) // Max distance
		})
	}
}

func TestCosine_InvalidDimensions(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}

	result := Cosine(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
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
			a:        []float32{1, 2, 3},
			b:        []float32{1, 2, 3},
			expected: -14, // -(1 + 4 + 9)
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{0, 1, 0},
			expected: 0,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1, 0},
			b:        []float32{-1, 0},
			expected: 1, // -(-1)
		},
		{
			name:     "zero vectors",
			a:        []float32{0, 0, 0},
			b:        []float32{0, 0, 0},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DotProduct(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, 1e-6)
		})
	}
}

func TestDotProduct_InvalidDimensions(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}

	result := DotProduct(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

func TestL2SIMD(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
		epsilon  float32
	}{
		{
			name:     "small vectors",
			a:        []float32{1, 2, 3},
			b:        []float32{4, 5, 6},
			expected: 5.196152,
			epsilon:  1e-5,
		},
		{
			name:     "vectors with chunk size",
			a:        []float32{1, 2, 3, 4},
			b:        []float32{5, 6, 7, 8},
			expected: 8,
			epsilon:  1e-5,
		},
		{
			name:     "vectors larger than chunk",
			a:        []float32{1, 2, 3, 4, 5},
			b:        []float32{6, 7, 8, 9, 10},
			expected: 11.18034,
			epsilon:  1e-4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := L2SIMD(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, float64(tt.epsilon))

			// Verify it matches non-SIMD version
			expected := L2(tt.a, tt.b)
			assert.InDelta(t, expected, result, 1e-5)
		})
	}
}

func TestCosineSIMD(t *testing.T) {
	tests := []struct {
		name string
		a    []float32
		b    []float32
	}{
		{
			name: "small vectors",
			a:    []float32{1, 2, 3},
			b:    []float32{4, 5, 6},
		},
		{
			name: "vectors with chunk size",
			a:    []float32{1, 2, 3, 4},
			b:    []float32{5, 6, 7, 8},
		},
		{
			name: "vectors larger than chunk",
			a:    []float32{1, 2, 3, 4, 5},
			b:    []float32{6, 7, 8, 9, 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CosineSIMD(tt.a, tt.b)
			expected := Cosine(tt.a, tt.b)
			assert.InDelta(t, expected, result, 1e-5)
		})
	}
}

func TestGetDistanceFunc(t *testing.T) {
	tests := []struct {
		name   string
		metric DistanceMetric
	}{
		{
			name:   "L2Distance",
			metric: L2Distance,
		},
		{
			name:   "CosineDistance",
			metric: CosineDistance,
		},
		{
			name:   "DotProductDistance",
			metric: DotProductDistance,
		},
		{
			name:   "invalid metric defaults to L2",
			metric: DistanceMetric(999),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := GetDistanceFunc(tt.metric)
			assert.NotNil(t, fn)

			// Test that it works
			a := []float32{1, 2, 3}
			b := []float32{4, 5, 6}
			result := fn(a, b)
			assert.NotEqual(t, float32(math.MaxFloat32), result)
		})
	}
}

func BenchmarkL2(b *testing.B) {
	a := make([]float32, 128)
	vec := make([]float32, 128)
	for i := range a {
		a[i] = float32(i)
		vec[i] = float32(i + 1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2(a, vec)
	}
}

func BenchmarkL2SIMD(b *testing.B) {
	a := make([]float32, 128)
	vec := make([]float32, 128)
	for i := range a {
		a[i] = float32(i)
		vec[i] = float32(i + 1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L2SIMD(a, vec)
	}
}

func BenchmarkCosine(b *testing.B) {
	a := make([]float32, 128)
	vec := make([]float32, 128)
	for i := range a {
		a[i] = float32(i)
		vec[i] = float32(i + 1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Cosine(a, vec)
	}
}

func BenchmarkCosineSIMD(b *testing.B) {
	a := make([]float32, 128)
	vec := make([]float32, 128)
	for i := range a {
		a[i] = float32(i)
		vec[i] = float32(i + 1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CosineSIMD(a, vec)
	}
}

func BenchmarkDotProduct(b *testing.B) {
	a := make([]float32, 128)
	vec := make([]float32, 128)
	for i := range a {
		a[i] = float32(i)
		vec[i] = float32(i + 1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DotProduct(a, vec)
	}
}
