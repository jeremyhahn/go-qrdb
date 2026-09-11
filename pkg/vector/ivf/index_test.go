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
	"sort"
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

// A pinned Seed is load-bearing here, not decoration.
//
// The assertions below are EXACT -- two results, "a" first -- and the search
// they are asserted against is APPROXIMATE: nprobe=2 visits two of the four
// inverted lists, so a query is answered from the clusters nearest the QUERY,
// which need not be the clusters holding the nearest VECTORS. Whether all four
// added vectors land in four distinct lists is a property of the k-means
// initialisation, and with a clock-seeded initialisation that property does not
// always hold: measured over 200,000 seeds, 48 of them (2.4e-4) cluster such
// that only ONE result is reachable within nprobe=2, and `Len(results, 2)`
// fails. That is not bad luck, it is the assertion being stronger than the
// algorithm guarantees.
//
// Seed makes the initialisation reproducible (see kmeans_seed_test.go, #92), so
// the clustering is fixed and the exact assertions are legitimate. If the
// clustering ever changes, this test fails on every run rather than one run in
// four thousand.
func TestIndex_Search(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  2,
		DistanceMetric: L2Distance,
		Seed:           288,
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
	require.Len(t, results, 2)

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
			// #nosec G404 -- test fixture vectors only; no key, nonce, token or identifier derives from these values.
			vec[j] = rand.Float32()
		}
		_ = idx.Add(fmt.Sprintf("vec%d", i), vec, nil)
	}

	query := make([]float32, 128)
	for i := range query {
		// #nosec G404 -- test fixture vectors only; no key, nonce, token or identifier derives from these values.
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

// Save/Load is a PERSISTENCE contract, so what is asserted is that the loaded
// index answers exactly as the source index did -- not that either of them
// achieves any particular recall.
//
// The distinction matters because this test used to conflate the two. It
// searched the loaded index with nprobe=1 and asserted `Len(results, 1)`, which
// is a recall claim about an approximate index: nprobe=1 visits the single
// cluster nearest the QUERY, and that cluster can legitimately be empty when
// the nearest VECTOR sits in a neighbouring one. Measured over 60,000 seeds,
// 3 of them (5.0e-5) cluster such that the list nearest (0,0) holds nothing and
// the search correctly returns zero results. Those same seeds return zero
// results BEFORE the save as well -- centroids, inverted lists, size and the
// trained flag all round-trip byte-identically -- so the failure was never
// about persistence at all (#288).
//
// Two changes make the assertions match the guarantee:
//   - Seed pins the k-means initialisation (see kmeans_seed_test.go, #92), so
//     the clustering is fixed rather than drawn from the clock.
//   - The search is compared against the SOURCE index's answer to the same
//     query. That is the round-trip property Save/Load actually owns, and it
//     holds for every clustering, degenerate ones included.
func TestIndex_SaveLoad(t *testing.T) {
	config := &Config{
		NumCentroids:   4,
		Dimensions:     2,
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  1,
		DistanceMetric: L2Distance,
		Seed:           288,
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

	// The source index's answer, taken BEFORE the save, is the reference the
	// loaded index is held to.
	query := []float32{0.0, 0.0}
	want, err := idx.Search(query, 1, 1)
	require.NoError(t, err)

	// Save
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "index.json")
	require.NoError(t, idx.Save(path))

	// Load into new index
	idx2, _ := NewIndex(config)
	require.NoError(t, idx2.Load(path))

	assert.True(t, idx2.IsTrained())
	assert.Equal(t, 2, idx2.Size())

	// The round trip preserved every vector, its cluster assignment and its
	// metadata -- so the same query returns the same answer.
	got, err := idx2.Search(query, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, want, got,
		"the loaded index answered differently from the source index: Save/Load "+
			"did not preserve the centroids, the inverted lists or the metadata")

	// Probing every cluster makes the search exhaustive, so this assertion is
	// about which vectors survived the round trip rather than about which
	// cluster the query happened to land in.
	exhaustive, err := idx2.Search(query, 2, config.NumCentroids)
	require.NoError(t, err)
	require.Len(t, exhaustive, 2)
	assert.Equal(t, "vec1", exhaustive[0].ID, "vec1 is nearest (0,0) and must rank first")
	assert.Equal(t, "vec2", exhaustive[1].ID)
	assert.Equal(t, "a", exhaustive[0].Metadata["label"])
	assert.Equal(t, "b", exhaustive[1].Metadata["label"])
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
	require.NoError(t, os.WriteFile(path, []byte("invalid json"), 0o600))

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
				// #nosec G404 -- test fixture vectors only; no key, nonce, token or identifier derives from these values.
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
				// #nosec G404 -- test fixture vectors only; no key, nonce, token or identifier derives from these values.
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

// IVF recall stays at or above the 70% floor the index promises.
//
// The floor is the product requirement and is unchanged. What changed is the
// measurement, twice, and the second correction matters more than the first.
//
// It began reading recall@10 off a SINGLE query against a randomly generated
// dataset. Recall@10 for one query is a ten-sample proportion, so the 0.7
// floor sat inside the sampling noise: it failed a CI run at 0.6 and passed
// 6/6 in isolation.
//
// Averaging over many queries fixes that sampling noise and nothing else. The
// dominant variance is UPSTREAM of the queries: Train runs k-means once and
// produces a single centroid set that every query then searches, so an unlucky
// initialisation moves all of them together. Averaging 50 queries over one
// clustering is still one draw (go-qrdb#92).
//
// So both sources are fixed. The dataset comes from a seeded local source, and
// Config.Seed fixes the clustering. Several seeds are then run rather than one:
// a single seed would be deterministic but would say nothing about
// initialisation quality in general, and the floor is a claim about the index,
// not about one lucky arrangement of centroids.
func TestIndex_SearchAccuracy(t *testing.T) {
	const (
		numVectors  = 1000
		dims        = 128
		topK        = 10
		numQueries  = 50
		recallFloor = 0.7
	)

	// Fixed dataset: identical on every run and every machine.
	vectors := seededVectors(numVectors, dims, 92)

	// Several fixed clusterings. Deterministic, and broad enough that the
	// floor is a statement about the index rather than about one seed.
	for _, seed := range []int64{1, 2, 3, 5, 8} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			idx, err := NewIndex(&Config{
				NumCentroids:   16,
				Dimensions:     dims,
				MaxIterations:  25,
				Convergence:    0.0001,
				InitMethod:     "kmeans++",
				DefaultNProbe:  8,
				DistanceMetric: L2Distance,
				Seed:           seed,
			})
			require.NoError(t, err)

			require.NoError(t, idx.Train(vectors))
			for i, vec := range vectors {
				require.NoError(t, idx.Add(fmt.Sprintf("vec%d", i), vec, nil))
			}

			total := 0.0
			for q := 0; q < numQueries; q++ {
				query := vectors[q]

				results, serr := idx.Search(query, topK, 12)
				require.NoError(t, serr)

				found := make(map[string]bool, len(results))
				for _, res := range results {
					found[res.ID] = true
				}

				matches := 0
				for _, id := range bruteForceTopK(vectors, query, topK) {
					if found[id] {
						matches++
					}
				}
				total += float64(matches) / float64(topK)
			}

			mean := total / float64(numQueries)
			t.Logf("mean recall@%d over %d queries: %.3f", topK, numQueries, mean)

			assert.GreaterOrEqual(t, mean, recallFloor,
				"mean IVF recall@%d must hold the %.0f%% floor", topK, recallFloor*100)
		})
	}
}

// seededVectors builds a reproducible dataset. A local source rather than the
// global one, so the fixture is unaffected by anything else the package does.
func seededVectors(count, dims int, seed int64) [][]float32 {
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	r := rand.New(rand.NewSource(seed))
	vectors := make([][]float32, count)
	for i := range vectors {
		vectors[i] = make([]float32, dims)
		for j := range vectors[i] {
			vectors[i][j] = r.Float32()*2 - 1 // [-1, 1]
		}
	}
	return vectors
}

// bruteForceTopK returns the ids of the topK nearest vectors to query, which is
// the ground truth recall is measured against.
func bruteForceTopK(vectors [][]float32, query []float32, topK int) []string {
	type scored struct {
		id   string
		dist float32
	}
	all := make([]scored, len(vectors))
	for i, vec := range vectors {
		all[i] = scored{id: fmt.Sprintf("vec%d", i), dist: L2(query, vec)}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].dist < all[j].dist })

	ids := make([]string, 0, topK)
	for i := 0; i < topK && i < len(all); i++ {
		ids = append(ids, all[i].id)
	}
	return ids
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
