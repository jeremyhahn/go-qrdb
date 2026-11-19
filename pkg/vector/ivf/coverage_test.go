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
	"github.com/stretchr/testify/require"
)

// TestL2Squared_MismatchedDimensions tests the error path for mismatched dimensions
func TestL2Squared_MismatchedDimensions(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{1.0, 2.0}

	result := L2Squared(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

// TestL2SIMD_MismatchedDimensions tests the error path for mismatched dimensions
func TestL2SIMD_MismatchedDimensions(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{1.0, 2.0}

	result := L2SIMD(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

// TestL2SIMD_RemainderPath tests vectors that don't divide evenly by 4
func TestL2SIMD_RemainderPath(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"size 1", 1},
		{"size 2", 2},
		{"size 3", 3},
		{"size 5", 5},
		{"size 6", 6},
		{"size 7", 7},
		{"size 9", 9},
		{"size 10", 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := make([]float32, tt.size)
			b := make([]float32, tt.size)
			for i := 0; i < tt.size; i++ {
				a[i] = float32(i + 1)
				b[i] = float32(tt.size - i)
			}

			regular := L2(a, b)
			simd := L2SIMD(a, b)
			assert.InDelta(t, regular, simd, 0.0001)
		})
	}
}

// TestL2SquaredSIMD_MismatchedDimensions tests the error path for mismatched dimensions
func TestL2SquaredSIMD_MismatchedDimensions(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{1.0, 2.0}

	result := L2SquaredSIMD(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

// TestL2SquaredSIMD_RemainderPath tests vectors that don't divide evenly by 4
func TestL2SquaredSIMD_RemainderPath(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"size 1", 1},
		{"size 2", 2},
		{"size 3", 3},
		{"size 5", 5},
		{"size 6", 6},
		{"size 7", 7},
		{"size 9", 9},
		{"size 10", 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := make([]float32, tt.size)
			b := make([]float32, tt.size)
			for i := 0; i < tt.size; i++ {
				a[i] = float32(i + 1)
				b[i] = float32(tt.size - i)
			}

			regular := L2Squared(a, b)
			simd := L2SquaredSIMD(a, b)
			assert.InDelta(t, regular, simd, 0.0001)
		})
	}
}

// TestCosineSIMD_MismatchedDimensions tests the error path for mismatched dimensions
func TestCosineSIMD_MismatchedDimensions(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{1.0, 2.0}

	result := CosineSIMD(a, b)
	assert.Equal(t, float32(math.MaxFloat32), result)
}

// TestCosineSIMD_ZeroVectors tests the zero vector edge cases
func TestCosineSIMD_ZeroVectors(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
	}{
		{
			name:     "both zero vectors",
			a:        []float32{0.0, 0.0, 0.0, 0.0},
			b:        []float32{0.0, 0.0, 0.0, 0.0},
			expected: 1.0,
		},
		{
			name:     "first zero vector",
			a:        []float32{0.0, 0.0, 0.0, 0.0},
			b:        []float32{1.0, 2.0, 3.0, 4.0},
			expected: 1.0,
		},
		{
			name:     "second zero vector",
			a:        []float32{1.0, 2.0, 3.0, 4.0},
			b:        []float32{0.0, 0.0, 0.0, 0.0},
			expected: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CosineSIMD(tt.a, tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestCosineSIMD_RemainderPath tests vectors that don't divide evenly by 4
func TestCosineSIMD_RemainderPath(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"size 1", 1},
		{"size 2", 2},
		{"size 3", 3},
		{"size 5", 5},
		{"size 6", 6},
		{"size 7", 7},
		{"size 9", 9},
		{"size 10", 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := make([]float32, tt.size)
			b := make([]float32, tt.size)
			for i := 0; i < tt.size; i++ {
				a[i] = float32(i + 1) // Avoid zeros
				b[i] = float32(tt.size - i)
			}

			regular := Cosine(a, b)
			simd := CosineSIMD(a, b)
			assert.InDelta(t, regular, simd, 0.0001)
		})
	}
}

// TestIndex_Train_KMeansError tests error handling when k-means fails
func TestIndex_Train_KMeansError(t *testing.T) {
	// This test ensures the error wrapping path is covered
	config := &Config{
		NumCentroids:   10,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	// Too few vectors to create 10 clusters - will fail in k-means
	vectors := [][]float32{
		{1.0, 1.0},
		{2.0, 2.0},
	}

	err := idx.Train(vectors)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "k-means clustering failed")
}

// TestIndex_Save_WriteError tests error handling when file write fails
func TestIndex_Save_WriteError(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	trainingVectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1},
		{5.0, 5.0}, {5.1, 5.1},
	}
	require.NoError(t, idx.Train(trainingVectors))

	// Try to save to an invalid path (directory that doesn't exist)
	err := idx.Save("/nonexistent/directory/that/does/not/exist/index.json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write index file")
}

// TestKMeans_RecomputeCentroids_EmptyCluster tests recomputing with empty clusters
func TestKMeans_RecomputeCentroids_EmptyCluster(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	km := NewKMeans(config)

	// Manually create a scenario with an empty cluster
	vectors := [][]float32{
		{0.0, 0.0},
		{1.0, 1.0},
		{2.0, 2.0},
	}

	// All assigned to first two clusters (cluster 2 is empty)
	assignments := []int{0, 0, 1}
	clusterSizes := []int{2, 1, 0}

	centroids, err := km.recomputeCentroids(vectors, assignments, clusterSizes)
	require.NoError(t, err)
	require.Len(t, centroids, 3)

	// Empty cluster should have zero centroid
	assert.Equal(t, float32(0.0), centroids[2][0])
	assert.Equal(t, float32(0.0), centroids[2][1])
}
