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

package ivf

import (
	"math"
	"os"
	"path/filepath"
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

// TestKMeans_Cluster_ConvergenceByMovement tests the convergence path based on centroid movement
func TestKMeans_Cluster_ConvergenceByMovement(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  100,
		Convergence:    0.0001, // Very small threshold
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create slightly overlapping clusters that will converge by movement
	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {0.2, 0.2},
		{0.3, 0.3}, {0.4, 0.4}, {0.5, 0.5},
		{5.0, 5.0}, {5.1, 5.1}, {5.2, 5.2},
		{5.3, 5.3}, {5.4, 5.4}, {5.5, 5.5},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 2)
	assert.Len(t, assignments, len(vectors))
}

// TestKMeans_InitializeKMeansPlusPlus_DuplicateVectors tests k-means++ with duplicate vectors
func TestKMeans_InitializeKMeansPlusPlus_DuplicateVectors(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create vectors where some are duplicates or very close
	// This tests the totalWeight == 0 path in k-means++
	vectors := [][]float32{
		{1.0, 1.0}, {1.0, 1.0}, {1.0, 1.0},
		{2.0, 2.0}, {2.0, 2.0}, {2.0, 2.0},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 3)
	assert.Len(t, assignments, len(vectors))
}

// TestKMeans_HandleEmptyClusters_NoFarthestPoint tests edge case in handleEmptyClusters
func TestKMeans_HandleEmptyClusters_NoFarthestPoint(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  5, // Limit iterations to force empty clusters
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create a small dataset that might result in empty clusters
	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1},
		{10.0, 10.0}, {10.1, 10.1},
	}

	km := NewKMeans(config)
	centroids, _, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 4)
}

// TestKMeans_HandleEmptyClusters_AllEmptyButOne tests when all clusters are empty except one
func TestKMeans_HandleEmptyClusters_AllEmptyButOne(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Use 3 vectors for 3 clusters - should work
	vectors := [][]float32{
		{0.0, 0.0},
		{1.0, 1.0},
		{2.0, 2.0},
	}

	km := NewKMeans(config)
	centroids, _, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 3)
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
	assert.Len(t, centroids, 3)

	// Empty cluster should have zero centroid
	assert.Equal(t, float32(0.0), centroids[2][0])
	assert.Equal(t, float32(0.0), centroids[2][1])
}

// TestIndex_Save_SuccessfulMarshal tests the happy path thoroughly
func TestIndex_Save_SuccessfulMarshal(t *testing.T) {
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

	// Add vector with complex metadata to exercise marshal path
	metadata := map[string]interface{}{
		"string": "value",
		"number": 42,
		"float":  3.14,
		"bool":   true,
		"nested": map[string]interface{}{
			"key": "nested_value",
		},
		"array": []interface{}{1, 2, 3},
	}

	require.NoError(t, idx.Add("vec1", []float32{0.2, 0.2}, metadata))

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "index.json")
	err := idx.Save(path)
	require.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(path)
	assert.NoError(t, err)
}

// TestKMeans_Cluster_AllPathsCovered tests error handling in recompute step
func TestKMeans_Cluster_AllPathsCovered(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     2,
		MaxIterations:  2, // Limit iterations
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create a dataset that won't converge in 2 iterations
	vectors := [][]float32{
		{0.0, 0.0}, {0.5, 0.5}, {1.0, 1.0},
		{5.0, 5.0}, {5.5, 5.5}, {6.0, 6.0},
		{10.0, 10.0}, {10.5, 10.5}, {11.0, 11.0},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 2)
	assert.Len(t, assignments, len(vectors))
}

// TestKMeans_InitializeKMeansPlusPlus_CumulativeWeightPath tests the weighted selection path
func TestKMeans_InitializeKMeansPlusPlus_CumulativeWeightPath(t *testing.T) {
	config := &Config{
		NumCentroids:   5,
		Dimensions:     3,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create well-separated vectors to exercise the weighted selection
	vectors := [][]float32{
		{0.0, 0.0, 0.0},
		{1.0, 0.0, 0.0},
		{0.0, 1.0, 0.0},
		{0.0, 0.0, 1.0},
		{5.0, 5.0, 5.0},
		{6.0, 5.0, 5.0},
		{5.0, 6.0, 5.0},
		{5.0, 5.0, 6.0},
		{10.0, 10.0, 10.0},
		{11.0, 10.0, 10.0},
	}

	km := NewKMeans(config)

	// Run k-means++ initialization multiple times to exercise the random selection
	for i := 0; i < 5; i++ {
		centroids, err := km.initializeKMeansPlusPlus(vectors)
		require.NoError(t, err)
		assert.Len(t, centroids, 5)

		// Verify all centroids are different
		for j := 0; j < len(centroids); j++ {
			for k := j + 1; k < len(centroids); k++ {
				dist := L2(centroids[j], centroids[k])
				assert.Greater(t, dist, float32(0.0))
			}
		}
	}
}

// TestKMeans_HandleEmptyClusters_ComplexScenario tests complex empty cluster handling
func TestKMeans_HandleEmptyClusters_ComplexScenario(t *testing.T) {
	config := &Config{
		NumCentroids:   5,
		Dimensions:     2,
		MaxIterations:  2, // Few iterations to create empty clusters
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Small dataset that will likely create multiple empty clusters
	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {0.2, 0.2},
		{5.0, 5.0}, {5.1, 5.1}, {5.2, 5.2},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 5)
	assert.Len(t, assignments, len(vectors))

	// Verify all assignments are valid
	for _, a := range assignments {
		assert.GreaterOrEqual(t, a, 0)
		assert.Less(t, a, 5)
	}
}

// TestIndex_Train_DimensionValidation tests all dimension validation paths
func TestIndex_Train_DimensionValidation(t *testing.T) {
	config := &Config{
		NumCentroids:   2,
		Dimensions:     3,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	// Mix of correct and incorrect dimensions
	vectors := [][]float32{
		{1.0, 2.0, 3.0}, // Correct
		{4.0, 5.0},      // Wrong - should fail
		{6.0, 7.0, 8.0}, // Correct
	}

	err := idx.Train(vectors)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "vector 1 has 2 dimensions, expected 3")
}

// TestKMeans_HandleEmptyClusters_EdgeCases tests various edge cases in handleEmptyClusters
func TestKMeans_HandleEmptyClusters_EdgeCases(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  1, // Single iteration to ensure empty clusters
		Convergence:    0.0001,
		InitMethod:     "random",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create vectors that will naturally cluster into fewer groups than centroids
	vectors := [][]float32{
		{0.0, 0.0}, {0.01, 0.01}, {0.02, 0.02}, {0.03, 0.03},
		{10.0, 10.0}, {10.01, 10.01}, {10.02, 10.02}, {10.03, 10.03},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 4)
	assert.Len(t, assignments, len(vectors))

	// Verify all assignments are valid
	for _, a := range assignments {
		assert.GreaterOrEqual(t, a, 0)
		assert.Less(t, a, 4)
	}
}

// TestKMeans_InitializeKMeansPlusPlus_ContinueLoop tests the continue path in k-means++
func TestKMeans_InitializeKMeansPlusPlus_ContinueLoop(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create vectors where many are duplicates
	vectors := [][]float32{
		{1.0, 1.0}, {1.0, 1.0}, {1.0, 1.0}, {1.0, 1.0},
		{2.0, 2.0}, {2.0, 2.0},
		{3.0, 3.0}, {3.0, 3.0},
		{4.0, 4.0}, {4.0, 4.0},
	}

	km := NewKMeans(config)
	centroids, err := km.initializeKMeansPlusPlus(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 4)
}

// TestKMeans_Cluster_NoConvergence tests when the algorithm doesn't converge early
func TestKMeans_Cluster_NoConvergence(t *testing.T) {
	config := &Config{
		NumCentroids:   3,
		Dimensions:     2,
		MaxIterations:  1, // Force no convergence
		Convergence:    0.0,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	// Create scattered data
	vectors := [][]float32{
		{0.0, 0.0}, {1.0, 1.0}, {2.0, 2.0},
		{3.0, 3.0}, {4.0, 4.0}, {5.0, 5.0},
		{6.0, 6.0}, {7.0, 7.0}, {8.0, 8.0},
	}

	km := NewKMeans(config)
	centroids, assignments, err := km.Cluster(vectors)

	require.NoError(t, err)
	assert.Len(t, centroids, 3)
	assert.Len(t, assignments, len(vectors))
}
