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

package flat

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// TestFlatIndex_InsertAndSearch verifies that inserted vectors are found by search.
// TestDimensionMismatchError_ErrorAndUnwrap verifies the message format and
// that it unwraps to the sentinel ErrInvalidVector.
func TestDimensionMismatchError_ErrorAndUnwrap(t *testing.T) {
	err := &DimensionMismatchError{Got: 3, Expected: 2}
	want := "dimension mismatch: got 3, expected 2"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrInvalidVector) {
		t.Fatal("DimensionMismatchError does not unwrap to ErrInvalidVector")
	}
}

// TestFlatIndex_MarshalUnmarshalRoundTrips verifies that Marshal and
// Unmarshal reproduce an equivalent index: same count, same distance
// metric, same search results.
func TestFlatIndex_MarshalUnmarshalRoundTrips(t *testing.T) {
	idx := NewIndex(hnsw.CosineDistance)
	if err := idx.Insert(1, []float32{1, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := idx.Insert(2, []float32{0, 1}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	data, err := idx.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	restored := NewIndex(hnsw.L2Distance) // deliberately wrong metric; Unmarshal must overwrite it
	if err := restored.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := restored.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2", got)
	}
	if restored.distMetric != hnsw.CosineDistance {
		t.Fatalf("distMetric = %v, want CosineDistance (Unmarshal did not restore it)", restored.distMetric)
	}

	results, err := restored.Search([]float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != 1 {
		t.Fatalf("Search = %+v, want ID 1 first", results)
	}
}

// TestFlatIndex_UnmarshalDiscardsExistingVectors verifies that Unmarshal
// into a non-empty index replaces its contents rather than merging into
// them.
func TestFlatIndex_UnmarshalDiscardsExistingVectors(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	if err := idx.Insert(1, []float32{1, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	empty := NewIndex(hnsw.L2Distance)
	data, err := empty.Marshal()
	if err != nil {
		t.Fatalf("Marshal (empty): %v", err)
	}
	if err := idx.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := idx.Count(); got != 0 {
		t.Fatalf("Count after Unmarshal(empty) = %d, want 0 -- the old vector was not discarded", got)
	}
}

// TestFlatIndex_UnmarshalInvalidDataFails verifies that malformed bytes are
// refused rather than silently producing a half-restored index.
func TestFlatIndex_UnmarshalInvalidDataFails(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	if err := idx.Unmarshal([]byte("not json")); err == nil {
		t.Fatal("Unmarshal(invalid data) = nil error, want one")
	}
}

func TestFlatIndex_InsertAndSearch(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	// Insert 5 vectors in 3 dimensions.
	vectors := [][]float32{
		{1.0, 0.0, 0.0},
		{0.0, 1.0, 0.0},
		{0.0, 0.0, 1.0},
		{1.0, 1.0, 0.0},
		{0.0, 1.0, 1.0},
	}

	for i, v := range vectors {
		if err := idx.Insert(uint64(i+1), v); err != nil {
			t.Fatalf("Insert(%d) failed: %v", i+1, err)
		}
	}

	if idx.Count() != 5 {
		t.Fatalf("expected count 5, got %d", idx.Count())
	}

	// Search for nearest to [1.0, 0.0, 0.0] - should return ID 1 as closest.
	results, err := idx.Search([]float32{1.0, 0.0, 0.0}, 3)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First result should be exact match with distance 0.
	if results[0].ID != 1 {
		t.Errorf("expected first result ID=1, got ID=%d", results[0].ID)
	}
	if results[0].Distance > 1e-6 {
		t.Errorf("expected distance ~0, got %f", results[0].Distance)
	}
}

// TestFlatIndex_InsertAndSearch_InvalidQuery verifies search rejects bad queries.
func TestFlatIndex_InsertAndSearch_InvalidQuery(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Insert(1, []float32{1.0, 2.0, 3.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Wrong dimensions.
	_, err := idx.Search([]float32{1.0, 2.0}, 1)
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	var dimErr *DimensionMismatchError
	if !errors.As(err, &dimErr) {
		t.Fatalf("expected DimensionMismatchError, got %T: %v", err, err)
	}

	// Zero k.
	_, err = idx.Search([]float32{1.0, 2.0, 3.0}, 0)
	if !errors.Is(err, ErrInvalidK) {
		t.Fatalf("expected ErrInvalidK, got %v", err)
	}
}

// TestFlatIndex_100PercentRecall verifies that flat index returns the true
// nearest neighbors (100% recall) since it examines all vectors.
func TestFlatIndex_100PercentRecall(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	// Create a known set of vectors where we can compute exact nearest neighbors.
	// Query: [5.0, 5.0]
	// Nearest should be [5.0, 5.0] (dist=0), then [4.0, 5.0] (dist=1), etc.
	knownVectors := map[uint64][]float32{
		1: {0.0, 0.0},
		2: {10.0, 10.0},
		3: {5.0, 5.0},   // Exact match
		4: {4.0, 5.0},   // Distance 1.0
		5: {5.0, 6.0},   // Distance 1.0
		6: {3.0, 5.0},   // Distance 2.0
		7: {5.0, 3.0},   // Distance 2.0
		8: {100.0, 0.0}, // Far away
	}

	for id, vec := range knownVectors {
		if err := idx.Insert(id, vec); err != nil {
			t.Fatalf("Insert(%d) failed: %v", id, err)
		}
	}

	query := []float32{5.0, 5.0}
	results, err := idx.Search(query, 3)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First result must be the exact match.
	if results[0].ID != 3 {
		t.Errorf("expected first result ID=3 (exact match), got ID=%d dist=%f", results[0].ID, results[0].Distance)
	}
	if results[0].Distance > 1e-6 {
		t.Errorf("expected distance ~0 for exact match, got %f", results[0].Distance)
	}

	// Results must be sorted by distance ascending.
	for i := 1; i < len(results); i++ {
		if results[i].Distance < results[i-1].Distance {
			t.Errorf("results not sorted: result[%d].Distance=%f < result[%d].Distance=%f",
				i, results[i].Distance, i-1, results[i-1].Distance)
		}
	}
}

// TestFlatIndex_Delete verifies that deleted vectors are removed from search results.
func TestFlatIndex_Delete(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Insert(1, []float32{1.0, 0.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := idx.Insert(2, []float32{2.0, 0.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	if err := idx.Delete(1); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if idx.Count() != 1 {
		t.Fatalf("expected count 1 after delete, got %d", idx.Count())
	}

	results, err := idx.Search([]float32{1.0, 0.0}, 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for _, r := range results {
		if r.ID == 1 {
			t.Error("deleted vector ID=1 should not appear in search results")
		}
	}
}

// TestFlatIndex_Delete_NotFound verifies that deleting a non-existent vector returns error.
func TestFlatIndex_Delete_NotFound(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	err := idx.Delete(999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestFlatIndex_DimensionMismatch verifies that inserting a vector with wrong
// dimensions returns an error.
func TestFlatIndex_DimensionMismatch(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	// First insert establishes dimensions.
	if err := idx.Insert(1, []float32{1.0, 2.0, 3.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Second insert with wrong dimensions should fail.
	err := idx.Insert(2, []float32{1.0, 2.0})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	var dimErr *DimensionMismatchError
	if !errors.As(err, &dimErr) {
		t.Fatalf("expected DimensionMismatchError, got %T: %v", err, err)
	}
	if dimErr.Got != 2 || dimErr.Expected != 3 {
		t.Errorf("expected Got=2 Expected=3, got Got=%d Expected=%d", dimErr.Got, dimErr.Expected)
	}
}

// TestFlatIndex_DimensionMismatch_EmptyVector verifies that inserting an empty
// vector returns an error.
func TestFlatIndex_DimensionMismatch_EmptyVector(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	err := idx.Insert(1, []float32{})
	if !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("expected ErrInvalidVector, got %v", err)
	}
}

// TestFlatIndex_EmptyIndex verifies that searching an empty index returns an error.
func TestFlatIndex_EmptyIndex(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	_, err := idx.Search([]float32{1.0, 2.0, 3.0}, 5)
	if !errors.Is(err, ErrEmptyIndex) {
		t.Fatalf("expected ErrEmptyIndex, got %v", err)
	}
}

// TestFlatIndex_EmptyIndex_AfterClose verifies that searching after close returns error.
func TestFlatIndex_EmptyIndex_AfterClose(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Insert(1, []float32{1.0, 2.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	if err := idx.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	_, err := idx.Search([]float32{1.0, 2.0}, 1)
	if !errors.Is(err, ErrEmptyIndex) {
		t.Fatalf("expected ErrEmptyIndex after close, got %v", err)
	}
}

// TestFlatIndex_SearchKGreaterThanCount verifies that requesting more results
// than vectors in the index returns all vectors.
func TestFlatIndex_SearchKGreaterThanCount(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Insert(1, []float32{1.0, 0.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := idx.Insert(2, []float32{2.0, 0.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	results, err := idx.Search([]float32{1.5, 0.0}, 100)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results (all vectors), got %d", len(results))
	}
}

// TestFlatIndex_SearchKGreaterThanCount_Sorted verifies results are still sorted
// when k > count.
func TestFlatIndex_SearchKGreaterThanCount_Sorted(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Insert(1, []float32{10.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := idx.Insert(2, []float32{1.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := idx.Insert(3, []float32{5.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	results, err := idx.Search([]float32{0.0}, 100)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for i := 1; i < len(results); i++ {
		if results[i].Distance < results[i-1].Distance {
			t.Errorf("results not sorted at index %d: %f < %f",
				i, results[i].Distance, results[i-1].Distance)
		}
	}
}

// TestFlatIndex_SortAllResults_RequiresSwap exercises sortAllResults directly
// with a deliberately descending-distance input, forcing the insertion sort's
// swap branch to execute on every run.
//
// Search's only caller of sortAllResults builds its ids/distances slices from
// sync.Map.Range, whose iteration order is randomized by the Go runtime.
// Whether that order happens to need a swap to reach ascending distance is
// therefore a coin flip per invocation, and under -race with GOMAXPROCS=1 the
// scheduler serializes TestFlatIndex_ConcurrentInsertSearch's writers and
// readers differently than an unconstrained run, changing how many of those
// coin flips occur and occasionally landing all of them on "already sorted".
// That produced an intermittent two-statement, ~1.9pp swing in this package's
// measured coverage (92.3% vs 90.4%) purely from scheduler timing. Calling
// sortAllResults directly with known-unsorted input removes the dependency on
// map iteration order and goroutine interleaving, so the swap branch is
// covered deterministically regardless of contention.
func TestFlatIndex_SortAllResults_RequiresSwap(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	// Distances given in strictly descending order: every adjacent pair is
	// an inversion, so the insertion sort must swap on each outer iteration.
	ids := []uint64{1, 2, 3, 4}
	distances := []float32{4.0, 3.0, 2.0, 1.0}

	results := idx.sortAllResults(ids, distances)

	wantIDs := []uint64{4, 3, 2, 1}
	for i, r := range results {
		if r.ID != wantIDs[i] {
			t.Errorf("result[%d]: expected ID=%d, got ID=%d", i, wantIDs[i], r.ID)
		}
	}
	for i := 1; i < len(results); i++ {
		if results[i].Distance < results[i-1].Distance {
			t.Errorf("results not sorted at %d: %f < %f", i, results[i].Distance, results[i-1].Distance)
		}
	}
}

// TestFlatIndex_DuplicateID verifies that re-inserting an existing ID
// updates the vector (upsert behavior).
func TestFlatIndex_DuplicateID(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	// Insert initial vector.
	if err := idx.Insert(1, []float32{1.0, 0.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Upsert with same ID but different vector.
	if err := idx.Insert(1, []float32{10.0, 0.0}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Count should still be 1 (no duplicate).
	if idx.Count() != 1 {
		t.Fatalf("expected count 1 after upsert, got %d", idx.Count())
	}

	// Search near the new position should find it with small distance.
	results, err := idx.Search([]float32{10.0, 0.0}, 1)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if results[0].ID != 1 {
		t.Errorf("expected ID=1, got ID=%d", results[0].ID)
	}
	if results[0].Distance > 1e-6 {
		t.Errorf("expected distance ~0 for upserted vector, got %f", results[0].Distance)
	}
}

// TestFlatIndex_DuplicateID_DimensionMismatch verifies that upserting with
// different dimensions fails.
func TestFlatIndex_DuplicateID_DimensionMismatch(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Insert(1, []float32{1.0, 2.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	err := idx.Insert(1, []float32{1.0, 2.0, 3.0})
	if err == nil {
		t.Fatal("expected error for dimension mismatch on upsert, got nil")
	}
}

// TestFlatIndex_AllMetrics verifies the index works with all distance metrics.
func TestFlatIndex_AllMetrics(t *testing.T) {
	metrics := []struct {
		name   string
		metric hnsw.DistanceMetric
	}{
		{"L2", hnsw.L2Distance},
		{"Cosine", hnsw.CosineDistance},
		{"DotProduct", hnsw.DotProductDistance},
		{"Manhattan", hnsw.ManhattanDistance},
	}

	for _, tc := range metrics {
		t.Run(tc.name, func(t *testing.T) {
			idx := NewIndex(tc.metric)

			vectors := [][]float32{
				{1.0, 0.0, 0.0},
				{0.0, 1.0, 0.0},
				{0.0, 0.0, 1.0},
			}

			for i, v := range vectors {
				if err := idx.Insert(uint64(i+1), v); err != nil {
					t.Fatalf("Insert(%d) failed: %v", i+1, err)
				}
			}

			results, err := idx.Search([]float32{1.0, 0.0, 0.0}, 2)
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}

			if len(results) != 2 {
				t.Fatalf("expected 2 results, got %d", len(results))
			}

			// First result should be closest to [1.0, 0.0, 0.0].
			if results[0].ID != 1 {
				t.Errorf("expected first result ID=1, got ID=%d", results[0].ID)
			}
		})
	}
}

// TestFlatIndex_LargeDataset verifies correct recall on a 10K vector dataset.
func TestFlatIndex_LargeDataset(t *testing.T) {
	const (
		numVectors = 10000
		dims       = 32
		k          = 10
	)

	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewSource(42))
	idx := NewIndex(hnsw.L2Distance)

	// Insert random vectors.
	for i := uint64(1); i <= numVectors; i++ {
		vec := make([]float32, dims)
		for d := range vec {
			vec[d] = rng.Float32()
		}
		if err := idx.Insert(i, vec); err != nil {
			t.Fatalf("Insert(%d) failed: %v", i, err)
		}
	}

	if idx.Count() != numVectors {
		t.Fatalf("expected count %d, got %d", numVectors, idx.Count())
	}

	// Generate a random query.
	query := make([]float32, dims)
	for d := range query {
		query[d] = rng.Float32()
	}

	results, err := idx.Search(query, k)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != k {
		t.Fatalf("expected %d results, got %d", k, len(results))
	}

	// Verify results are sorted by distance ascending.
	for i := 1; i < len(results); i++ {
		if results[i].Distance < results[i-1].Distance {
			t.Errorf("results not sorted: result[%d].Distance=%f < result[%d].Distance=%f",
				i, results[i].Distance, i-1, results[i-1].Distance)
		}
	}

	// Verify that no vector outside the results is closer than the worst result.
	worstDist := results[k-1].Distance
	resultIDs := make(map[uint64]bool, k)
	for _, r := range results {
		resultIDs[r.ID] = true
	}

	// Brute-force verify: compute distance to all and check no closer one was missed.
	distFn := hnsw.GetDistanceFunc(hnsw.L2Distance)
	idx.vectors.Range(func(key, value any) bool {
		id := key.(uint64)
		if resultIDs[id] {
			return true
		}
		vec := value.([]float32)
		dist := distFn(query, vec)
		if dist < worstDist-1e-6 {
			t.Errorf("vector ID=%d with distance %f is closer than worst result distance %f but was not returned",
				id, dist, worstDist)
		}
		return true
	})
}

// TestFlatIndex_ConcurrentInsertSearch verifies thread safety under concurrent access.
func TestFlatIndex_ConcurrentInsertSearch(t *testing.T) {
	const (
		numWriters = 4
		numReaders = 4
		opsPerGo   = 250
		dims       = 8
	)

	idx := NewIndex(hnsw.L2Distance)

	// Seed with one vector so searches can proceed.
	if err := idx.Insert(0, make([]float32, dims)); err != nil {
		t.Fatalf("seed insert failed: %v", err)
	}

	var wg sync.WaitGroup

	// Concurrent writers.
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
			rng := rand.New(rand.NewSource(int64(workerID)))
			base := uint64(workerID*opsPerGo) + 1
			for i := 0; i < opsPerGo; i++ {
				vec := make([]float32, dims)
				for d := range vec {
					vec[d] = rng.Float32()
				}
				_ = idx.Insert(base+uint64(i), vec)
			}
		}(w)
	}

	// Concurrent readers.
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
			rng := rand.New(rand.NewSource(int64(readerID + 100)))
			for i := 0; i < opsPerGo; i++ {
				query := make([]float32, dims)
				for d := range query {
					query[d] = rng.Float32()
				}
				_, _ = idx.Search(query, 5)
			}
		}(r)
	}

	wg.Wait()

	// Verify index is still functional after concurrent access.
	if idx.Count() < 1 {
		t.Error("expected index to have vectors after concurrent operations")
	}

	query := make([]float32, dims)
	results, err := idx.Search(query, 3)
	if err != nil {
		t.Fatalf("Search after concurrent ops failed: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from search after concurrent operations")
	}
}

// TestFlatIndex_ConcurrentInsertSearch_DeleteDuringSearch verifies safety
// of concurrent deletes during searches.
func TestFlatIndex_ConcurrentInsertSearch_DeleteDuringSearch(t *testing.T) {
	const dims = 4
	idx := NewIndex(hnsw.L2Distance)

	// Insert initial vectors.
	for i := uint64(1); i <= 100; i++ {
		vec := make([]float32, dims)
		vec[0] = float32(i)
		if err := idx.Insert(i, vec); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	var wg sync.WaitGroup

	// Concurrent deletes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= 50; i++ {
			_ = idx.Delete(i)
		}
	}()

	// Concurrent searches.
	wg.Add(1)
	go func() {
		defer wg.Done()
		query := make([]float32, dims)
		for i := 0; i < 50; i++ {
			_, _ = idx.Search(query, 5)
		}
	}()

	wg.Wait()
}

// TestFlatIndex_DeleteBetweenPublishAndCount drives the interleaving Codex
// review 8596 found: a Delete removing the entry an Insert has just published,
// before that Insert settles its count. Count must stay non-negative and
// Search must not size a slice from it, both inside the window and after.
func TestFlatIndex_DeleteBetweenPublishAndCount(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{name: "new entry", existing: false},
		{name: "overwrite", existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := NewIndex(hnsw.L2Distance)
			query := []float32{0, 0, 0, 0}
			if tc.existing {
				if err := idx.Insert(1, []float32{1, 0, 0, 0}); err != nil {
					t.Fatalf("Insert failed: %v", err)
				}
			}
			hookRan := false
			idx.afterPublish = func() {
				hookRan = true
				if err := idx.Delete(1); err != nil {
					t.Fatalf("Delete inside the window failed: %v", err)
				}
				if got := idx.Count(); got < 0 {
					t.Fatalf("Count inside the window = %d, want >= 0", got)
				}
				if _, err := idx.Search(query, 1); err != ErrEmptyIndex { //nolint:errorlint // sentinel returned unwrapped
					t.Fatalf("Search inside the window: err = %v, want ErrEmptyIndex", err)
				}
			}
			if err := idx.Insert(1, []float32{2, 0, 0, 0}); err != nil {
				t.Fatalf("Insert failed: %v", err)
			}
			if !hookRan {
				t.Fatal("afterPublish did not run")
			}
			if got := idx.Count(); got != 0 {
				t.Fatalf("Count after the interleaving = %d, want 0", got)
			}
			if _, err := idx.Search(query, 1); err != ErrEmptyIndex { //nolint:errorlint // sentinel returned unwrapped
				t.Fatalf("Search after the interleaving: err = %v, want ErrEmptyIndex", err)
			}
		})
	}
}

// TestFlatIndex_ConcurrentInsertDeleteSearch_SameIDs runs Insert, Delete and
// Search over the same few IDs from several goroutines. Under -race it checks
// that Search never panics, that Count is never negative, and that Count
// agrees with the map once every writer has finished.
func TestFlatIndex_ConcurrentInsertDeleteSearch_SameIDs(t *testing.T) {
	const (
		ids        = 4
		writers    = 4
		iterations = 2000
	)
	idx := NewIndex(hnsw.L2Distance)
	vec := []float32{1, 2, 3, 4}
	query := []float32{0, 0, 0, 0}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if err := idx.Insert(uint64(i%ids), vec); err != nil {
					t.Errorf("Insert failed: %v", err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = idx.Delete(uint64(i % ids))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Search panicked: %v", r)
			}
		}()
		for i := 0; i < iterations; i++ {
			_, _ = idx.Search(query, 2)
			if got := idx.Count(); got < 0 {
				t.Errorf("Count = %d, want >= 0", got)
				return
			}
		}
	}()
	wg.Wait()

	var entries int64
	idx.vectors.Range(func(_, _ any) bool {
		entries++
		return true
	})
	if got := idx.Count(); got != entries {
		t.Fatalf("Count after the writers = %d, map holds %d", got, entries)
	}
}

// TestFlatIndex_Dimensions verifies dimensions are reported correctly.
func TestFlatIndex_Dimensions(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if idx.Dimensions() != 0 {
		t.Fatalf("expected 0 dimensions on empty index, got %d", idx.Dimensions())
	}

	if err := idx.Insert(1, []float32{1.0, 2.0, 3.0, 4.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	if idx.Dimensions() != 4 {
		t.Fatalf("expected 4 dimensions, got %d", idx.Dimensions())
	}
}

// TestFlatIndex_Dimensions_AfterClose verifies dimensions reset after close.
func TestFlatIndex_Dimensions_AfterClose(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Insert(1, []float32{1.0, 2.0}); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if idx.Dimensions() != 0 {
		t.Errorf("expected 0 dimensions after close, got %d", idx.Dimensions())
	}
}

// TestFlatIndex_NilVector verifies Insert rejects nil vector.
func TestFlatIndex_NilVector(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	err := idx.Insert(1, nil)
	if !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("expected ErrInvalidVector for nil vector, got %v", err)
	}
}

// TestFlatIndex_SearchResultsDoNotShareMemory verifies that modifying returned
// results does not affect index internals.
func TestFlatIndex_SearchResultsDoNotShareMemory(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	original := []float32{1.0, 2.0, 3.0}
	if err := idx.Insert(1, original); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Modify the original slice after insert.
	original[0] = 999.0

	results, err := idx.Search([]float32{1.0, 2.0, 3.0}, 1)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// The result should still be close to the original [1, 2, 3].
	if results[0].Distance > 1e-6 {
		t.Errorf("expected distance ~0 (vector should not be mutated), got %f", results[0].Distance)
	}
}

// TestFlatIndex_SearchResultsSorted verifies ascending sort on various dataset sizes.
func TestFlatIndex_SearchResultsSorted(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewSource(99))

	for i := uint64(1); i <= 500; i++ {
		vec := []float32{rng.Float32() * 100, rng.Float32() * 100}
		if err := idx.Insert(i, vec); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	results, err := idx.Search([]float32{50.0, 50.0}, 20)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for i := 1; i < len(results); i++ {
		if results[i].Distance < results[i-1].Distance-1e-9 {
			t.Errorf("results not sorted at %d: %f < %f", i, results[i].Distance, results[i-1].Distance)
		}
	}
}

// TestFlatIndex_Close verifies Close releases resources cleanly.
func TestFlatIndex_Close(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	for i := uint64(1); i <= 10; i++ {
		if err := idx.Insert(i, []float32{float32(i)}); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	if err := idx.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if idx.Count() != 0 {
		t.Errorf("expected count 0 after close, got %d", idx.Count())
	}
}

// TestFlatIndex_Close_Idempotent verifies Close can be called multiple times.
func TestFlatIndex_Close_Idempotent(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)

	if err := idx.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

// storeForeignEntries plants map entries Insert never writes, so the type
// guards in Search and Marshal are exercised.
func storeForeignEntries(idx *Index) {
	idx.vectors.Store("not-a-uint64", []float32{1, 2})
	idx.vectors.Store(uint64(99), "not-a-vector")
}

func TestFlatIndex_Search_SkipsForeignEntries(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	if err := idx.Insert(1, []float32{1, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	storeForeignEntries(idx)

	results, err := idx.Search([]float32{1, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != 1 {
		t.Fatalf("Search results = %+v, want only ID 1", results)
	}
}

func TestFlatIndex_Search_OnlyForeignEntriesIsEmpty(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	if err := idx.Insert(1, []float32{1, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	idx.vectors.Delete(uint64(1))
	storeForeignEntries(idx)

	if _, err := idx.Search([]float32{1, 0}, 1); !errors.Is(err, ErrEmptyIndex) {
		t.Fatalf("Search error = %v, want ErrEmptyIndex", err)
	}
}

func TestFlatIndex_Search_DistanceBatchError(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	if err := idx.Insert(1, []float32{1, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// A stored vector of the wrong width makes the batch computation fail.
	idx.vectors.Store(uint64(2), []float32{1, 0, 0})

	_, err := idx.Search([]float32{1, 0}, 1)
	var mismatch *hnsw.DimensionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Search error = %v, want *hnsw.DimensionMismatchError", err)
	}
}

func TestFlatIndex_Marshal_SkipsForeignEntries(t *testing.T) {
	idx := NewIndex(hnsw.CosineDistance)
	if err := idx.Insert(7, []float32{0.5, 0.5}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	storeForeignEntries(idx)

	data, err := idx.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	restored := NewIndex(hnsw.L2Distance)
	if err := restored.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := restored.Count(); got != 1 {
		t.Fatalf("restored Count() = %d, want 1", got)
	}
}

func TestFlatIndex_Marshal_NaNFails(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	if err := idx.Insert(1, []float32{float32(math.NaN()), 1}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := idx.Marshal(); err == nil {
		t.Fatal("Marshal(NaN vector) = nil error, want one")
	}
}

func TestFlatIndex_Unmarshal_InvalidVectorFails(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	err := idx.Unmarshal([]byte(`{"DistMetric":0,"Dimensions":2,"Vectors":{"1":[]}}`))
	if !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidVector", err)
	}
}

// BenchmarkFlatIndex_Search_1K benchmarks search performance on 1K vectors.
func BenchmarkFlatIndex_Search_1K(b *testing.B) {
	benchmarkFlatSearch(b, 1000, 32, 10)
}

// BenchmarkFlatIndex_Search_10K benchmarks search performance on 10K vectors.
func BenchmarkFlatIndex_Search_10K(b *testing.B) {
	benchmarkFlatSearch(b, 10000, 32, 10)
}

// BenchmarkFlatIndex_Search_100K benchmarks search performance on 100K vectors.
func BenchmarkFlatIndex_Search_100K(b *testing.B) {
	benchmarkFlatSearch(b, 100000, 32, 10)
}

// BenchmarkFlatIndex_Insert benchmarks insert performance.
func BenchmarkFlatIndex_Insert(b *testing.B) {
	idx := NewIndex(hnsw.L2Distance)
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewSource(42))
	dims := 32

	vec := make([]float32, dims)
	for d := range vec {
		vec[d] = rng.Float32()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.Insert(uint64(i), vec)
	}
}

func benchmarkFlatSearch(b *testing.B, numVectors, dims, k int) {
	b.Helper()
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewSource(42))
	idx := NewIndex(hnsw.L2Distance)

	for i := uint64(0); i < uint64(numVectors); i++ {
		vec := make([]float32, dims)
		for d := range vec {
			vec[d] = rng.Float32()
		}
		if err := idx.Insert(i, vec); err != nil {
			b.Fatalf("Insert failed: %v", err)
		}
	}

	query := make([]float32, dims)
	for d := range query {
		query[d] = rng.Float32()
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		results, err := idx.Search(query, k)
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
		if len(results) != k && len(results) != int(math.Min(float64(k), float64(numVectors))) {
			b.Fatalf("expected %d results, got %d", k, len(results))
		}
	}
}
