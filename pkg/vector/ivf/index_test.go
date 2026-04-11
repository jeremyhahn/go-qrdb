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
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfig returns a fast configuration for unit tests (4 centroids, small training sets)
func testConfig(dimensions int) *Config {
	return &Config{
		NumCentroids:   4,
		Dimensions:     dimensions,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  2,
		DistanceMetric: L2Distance,
	}
}

func TestNewIndex(t *testing.T) {
	config := DefaultConfig(128)
	idx, err := NewIndex(config)

	require.NoError(t, err)
	assert.NotNil(t, idx)
	assert.False(t, idx.IsTrained())
	assert.Equal(t, 0, idx.Size())
}

func TestNewIndex_InvalidConfig(t *testing.T) {
	config := &Config{
		NumCentroids: 0, // Invalid
		Dimensions:   128,
	}

	_, err := NewIndex(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config")
}

func TestIndex_Train(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	idx, err := NewIndex(config)
	require.NoError(t, err)

	// Create training data
	vectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {0.2, 0.2},
		{5.0, 5.0}, {5.1, 5.1}, {5.2, 5.2},
		{10.0, 0.0}, {10.1, 0.1}, {10.2, 0.2},
		{0.0, 10.0}, {0.1, 10.1}, {0.2, 10.2},
	}

	err = idx.Train(vectors)
	require.NoError(t, err)
	assert.True(t, idx.IsTrained())
}

func TestIndex_Train_EmptyVectors(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	err := idx.Train([][]float32{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty vector set")
}

func TestIndex_Train_WrongDimensions(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	vectors := [][]float32{
		make([]float32, 64), // Wrong dimension
	}

	err := idx.Train(vectors)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dimensions")
}

func TestIndex_Add(t *testing.T) {
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

	// Train first
	trainingVectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1},
		{5.0, 5.0}, {5.1, 5.1},
	}
	require.NoError(t, idx.Train(trainingVectors))

	// Add vectors
	err := idx.Add("vec1", []float32{0.2, 0.2}, map[string]interface{}{"label": "a"})
	require.NoError(t, err)

	err = idx.Add("vec2", []float32{5.2, 5.2}, map[string]interface{}{"label": "b"})
	require.NoError(t, err)

	assert.Equal(t, 2, idx.Size())
}

func TestIndex_Add_Untrained(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	err := idx.Add("vec1", make([]float32, 128), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be trained")
}

func TestIndex_Add_WrongDimensions(t *testing.T) {
	// Use small number of centroids for fast training in tests
	config := &Config{
		NumCentroids:   4,
		Dimensions:     128,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(100, 128)
	require.NoError(t, idx.Train(trainingVectors))

	err := idx.Add("vec1", make([]float32, 64), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dimensions")
}

func TestIndex_Add_DuplicateID(t *testing.T) {
	config := testConfig(128)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(100, 128)
	require.NoError(t, idx.Train(trainingVectors))

	vec := make([]float32, 128)
	require.NoError(t, idx.Add("vec1", vec, nil))

	err := idx.Add("vec1", vec, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestIndex_Search(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  2,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	// Train
	trainingVectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1}, {0.2, 0.2},
		{5.0, 5.0}, {5.1, 5.1}, {5.2, 5.2},
		{10.0, 0.0}, {10.1, 0.1}, {10.2, 0.2},
		{0.0, 10.0}, {0.1, 10.1}, {0.2, 10.2},
	}
	require.NoError(t, idx.Train(trainingVectors))

	// Add vectors
	vectors := map[string][]float32{
		"a": {0.0, 0.0},
		"b": {5.0, 5.0},
		"c": {10.0, 0.0},
		"d": {0.0, 10.0},
	}

	for id, vec := range vectors {
		require.NoError(t, idx.Add(id, vec, map[string]interface{}{"id": id}))
	}

	// Search near (0, 0)
	query := []float32{0.1, 0.1}
	results, err := idx.Search(query, 2, 2)

	require.NoError(t, err)
	assert.Len(t, results, 2)

	// First result should be "a" (closest)
	assert.Equal(t, "a", results[0].ID)
	assert.Less(t, results[0].Distance, float32(0.2))
}

func TestIndex_Search_Untrained(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	_, err := idx.Search(make([]float32, 128), 10, 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be trained")
}

func TestIndex_Search_WrongDimensions(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(1000, 128)
	require.NoError(t, idx.Train(trainingVectors))

	_, err := idx.Search(make([]float32, 64), 10, 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dimensions")
}

func TestIndex_Search_DefaultNProbe(t *testing.T) {
	config := testConfig(128)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(100, 128)
	require.NoError(t, idx.Train(trainingVectors))

	// Add some vectors
	for i := 0; i < 100; i++ {
		vec := make([]float32, 128)
		for j := range vec {
			vec[j] = rand.Float32()
		}
		_ = idx.Add(fmt.Sprintf("vec%d", i), vec, nil)
	}

	query := make([]float32, 128)
	for i := range query {
		query[i] = rand.Float32()
	}

	// Test with nprobe = 0 (should use default)
	results, err := idx.Search(query, 5, 0)
	require.NoError(t, err)
	assert.NotNil(t, results)
}

func TestIndex_Search_NProbeExceedsCentroids(t *testing.T) {
	config := testConfig(128)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(100, 128)
	require.NoError(t, idx.Train(trainingVectors))

	// Add some vectors
	for i := 0; i < 10; i++ {
		vec := make([]float32, 128)
		_ = idx.Add(fmt.Sprintf("vec%d", i), vec, nil)
	}

	query := make([]float32, 128)
	results, err := idx.Search(query, 5, 10000) // nprobe > num_centroids
	require.NoError(t, err)
	assert.NotNil(t, results)
}

func TestIndex_Remove(t *testing.T) {
	config := testConfig(128)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(100, 128)
	require.NoError(t, idx.Train(trainingVectors))

	// Add vectors
	vec := make([]float32, 128)
	require.NoError(t, idx.Add("vec1", vec, nil))
	require.NoError(t, idx.Add("vec2", vec, nil))
	assert.Equal(t, 2, idx.Size())

	// Remove
	err := idx.Remove("vec1")
	require.NoError(t, err)
	assert.Equal(t, 1, idx.Size())

	// Verify removed
	err = idx.Remove("vec1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndex_Remove_Untrained(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	err := idx.Remove("vec1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be trained")
}

func TestIndex_Remove_NotFound(t *testing.T) {
	config := testConfig(128)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(100, 128)
	require.NoError(t, idx.Train(trainingVectors))

	err := idx.Remove("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndex_SaveLoad(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	// Train and add vectors
	trainingVectors := [][]float32{
		{0.0, 0.0}, {0.1, 0.1},
		{5.0, 5.0}, {5.1, 5.1},
		{10.0, 0.0}, {10.1, 0.1},
		{0.0, 10.0}, {0.1, 10.1},
	}
	require.NoError(t, idx.Train(trainingVectors))

	require.NoError(t, idx.Add("vec1", []float32{0.2, 0.2}, map[string]interface{}{"label": "a"}))
	require.NoError(t, idx.Add("vec2", []float32{5.2, 5.2}, map[string]interface{}{"label": "b"}))

	// Save
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "index.json")
	err := idx.Save(path)
	require.NoError(t, err)

	// Load into new index
	idx2, _ := NewIndex(config)
	err = idx2.Load(path)
	require.NoError(t, err)

	assert.True(t, idx2.IsTrained())
	assert.Equal(t, 2, idx2.Size())

	// Search should work
	query := []float32{0.0, 0.0}
	results, err := idx2.Search(query, 1, 1)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "vec1", results[0].ID)
}

func TestIndex_Save_Untrained(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "index.json")

	err := idx.Save(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "untrained")
}

func TestIndex_Load_InvalidFile(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	err := idx.Load("/nonexistent/path.json")
	assert.Error(t, err)
}

func TestIndex_Load_InvalidJSON(t *testing.T) {
	config := DefaultConfig(128)
	idx, _ := NewIndex(config)

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalid.json")
	require.NoError(t, os.WriteFile(path, []byte("invalid json"), 0644))

	err := idx.Load(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestIndex_Stats(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
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
		{10.0, 0.0}, {10.1, 0.1},
		{0.0, 10.0}, {0.1, 10.1},
	}
	require.NoError(t, idx.Train(trainingVectors))

	for i := 0; i < 10; i++ {
		vec := []float32{float32(i), float32(i)}
		_ = idx.Add(fmt.Sprintf("vec%d", i), vec, nil)
	}

	stats := idx.(*ivfIndex).Stats()

	assert.Equal(t, 4, stats["num_centroids"])
	assert.Equal(t, 10, stats["total_vectors"])
	assert.True(t, stats["trained"].(bool))
	assert.Equal(t, 2, stats["dimensions"])
	assert.NotNil(t, stats["avg_list_size"])
	assert.NotNil(t, stats["min_list_size"])
	assert.NotNil(t, stats["max_list_size"])
}

func TestIndex_ConcurrentOperations(t *testing.T) {
	config := testConfig(128)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(100, 128)
	require.NoError(t, idx.Train(trainingVectors))

	// Add vectors concurrently (should be safe with mutex)
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			vec := make([]float32, 128)
			for j := range vec {
				vec[j] = rand.Float32()
			}
			_ = idx.Add(fmt.Sprintf("vec%d", id), vec, nil)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// Search concurrently
	for i := 0; i < 10; i++ {
		go func() {
			query := make([]float32, 128)
			for j := range query {
				query[j] = rand.Float32()
			}
			_, _ = idx.Search(query, 5, 2)
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	assert.Equal(t, 10, idx.Size())
}

func TestIndex_SearchAccuracy(t *testing.T) {
	// Test recall: ensure IVF finds most of the true nearest neighbors
	config := &Config{
		NumCentroids:   16,
		Dimensions:     128,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  8, // Search half the clusters
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	// Create dataset
	numVectors := 1000
	vectors := generateRandomVectors(numVectors, 128)

	// Train
	require.NoError(t, idx.Train(vectors))

	// Add vectors
	for i, vec := range vectors {
		_ = idx.Add(fmt.Sprintf("vec%d", i), vec, nil)
	}

	// Test query
	query := vectors[0] // Use a vector from the dataset

	// IVF search
	ivfResults, err := idx.Search(query, 10, 12) // Search most clusters
	require.NoError(t, err)

	// Brute force search (ground truth)
	type result struct {
		id   string
		dist float32
	}
	bruteForce := make([]result, numVectors)
	for i, vec := range vectors {
		bruteForce[i] = result{
			id:   fmt.Sprintf("vec%d", i),
			dist: L2(query, vec),
		}
	}

	// Sort brute force results
	for i := 0; i < 10; i++ {
		minIdx := i
		for j := i + 1; j < len(bruteForce); j++ {
			if bruteForce[j].dist < bruteForce[minIdx].dist {
				minIdx = j
			}
		}
		bruteForce[i], bruteForce[minIdx] = bruteForce[minIdx], bruteForce[i]
	}

	// Calculate recall@10
	ivfIDs := make(map[string]bool)
	for _, res := range ivfResults {
		ivfIDs[res.ID] = true
	}

	matches := 0
	for i := 0; i < 10; i++ {
		if ivfIDs[bruteForce[i].id] {
			matches++
		}
	}

	recall := float64(matches) / 10.0
	t.Logf("Recall@10: %.2f", recall)

	// IVF should achieve >80% recall with nprobe=8 (half the clusters)
	assert.GreaterOrEqual(t, recall, 0.7, "IVF recall should be >=70%%")
}

// Benchmarks
func BenchmarkIndex_Train(b *testing.B) {
	config := &Config{
		NumCentroids:   256,
		Dimensions:     1536,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  10,
		DistanceMetric: L2Distance,
	}

	vectors := generateRandomVectors(10000, 1536)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx, _ := NewIndex(config)
		_ = idx.Train(vectors)
	}
}

func BenchmarkIndex_Add(b *testing.B) {
	config := DefaultConfig(1536)
	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(10000, 1536)
	_ = idx.Train(trainingVectors)

	vectors := generateRandomVectors(b.N, 1536)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.Add(fmt.Sprintf("vec%d", i), vectors[i], nil)
	}
}

func BenchmarkIndex_Search(b *testing.B) {
	config := &Config{
		NumCentroids:   256,
		Dimensions:     1536,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  10,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	trainingVectors := generateRandomVectors(10000, 1536)
	_ = idx.Train(trainingVectors)

	// Add vectors
	for i, vec := range trainingVectors {
		_ = idx.Add(fmt.Sprintf("vec%d", i), vec, nil)
	}

	query := generateRandomVectors(1, 1536)[0]

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = idx.Search(query, 10, 10)
	}
}

func BenchmarkIndex_SearchVsFlat(b *testing.B) {
	config := &Config{
		NumCentroids:   256,
		Dimensions:     1536,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  10,
		DistanceMetric: L2Distance,
	}

	idx, _ := NewIndex(config)

	numVectors := 10000
	vectors := generateRandomVectors(numVectors, 1536)

	_ = idx.Train(vectors)
	for i, vec := range vectors {
		_ = idx.Add(fmt.Sprintf("vec%d", i), vec, nil)
	}

	query := generateRandomVectors(1, 1536)[0]

	b.Run("IVF", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = idx.Search(query, 10, 10)
		}
	})

	b.Run("Flat", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// Brute force search
			type result struct {
				idx  int
				dist float32
			}
			results := make([]result, len(vectors))
			for j, vec := range vectors {
				results[j] = result{idx: j, dist: L2(query, vec)}
			}
			// Partial sort for top 10
			for k := 0; k < 10 && k < len(results); k++ {
				minIdx := k
				for j := k + 1; j < len(results); j++ {
					if results[j].dist < results[minIdx].dist {
						minIdx = j
					}
				}
				results[k], results[minIdx] = results[minIdx], results[k]
			}
		}
	})
}
