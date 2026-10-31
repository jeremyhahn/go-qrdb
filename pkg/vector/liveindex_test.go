// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vector

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

func TestNewLiveIndex_EveryIndexTypeConstructs(t *testing.T) {
	for _, indexType := range []IndexType{IndexHNSW, IndexFlat, IndexIVFFlat} {
		rec := NamespaceRecord{Dimension: 2, DistanceMetric: DistanceCosine, IndexType: indexType}
		idx, err := NewLiveIndex("ns", rec)
		if err != nil {
			t.Fatalf("NewLiveIndex(indexType=%d): %v", indexType, err)
		}
		if idx == nil {
			t.Fatalf("NewLiveIndex(indexType=%d) returned a nil index with no error", indexType)
		}
	}
}

// TestNewLiveIndex_RefusesUnknownIndexType proves an index type this build
// does not recognise is refused, never built as HNSW.
func TestNewLiveIndex_RefusesUnknownIndexType(t *testing.T) {
	for _, indexType := range []IndexType{3, 99, 255} {
		rec := NamespaceRecord{Dimension: 2, DistanceMetric: DistanceCosine, IndexType: indexType}
		idx, err := NewLiveIndex("ns", rec)
		var refused *InvalidIndexTypeError
		if !errors.As(err, &refused) {
			t.Fatalf("NewLiveIndex(indexType=%d) = %T, %v; want *InvalidIndexTypeError", indexType, idx, err)
		}
		if refused.Namespace != "ns" || refused.IndexType != indexType {
			t.Fatalf("refusal = %+v, want namespace ns, index type %d", refused, indexType)
		}
		if idx != nil {
			t.Fatalf("NewLiveIndex(indexType=%d) returned an index with its refusal", indexType)
		}
	}
}

// TestNewLiveIndex_RefusesUnknownMetric proves a metric this build does not
// recognise is refused on every index type, never read as cosine.
func TestNewLiveIndex_RefusesUnknownMetric(t *testing.T) {
	for _, indexType := range []IndexType{IndexHNSW, IndexFlat, IndexIVFFlat} {
		for _, metric := range []DistanceMetric{4, 99, 255} {
			rec := NamespaceRecord{Dimension: 2, DistanceMetric: metric, IndexType: indexType}
			idx, err := NewLiveIndex("ns", rec)
			var refused *InvalidDistanceMetricError
			if !errors.As(err, &refused) {
				t.Fatalf("NewLiveIndex(indexType=%d, metric=%d) = %T, %v; want *InvalidDistanceMetricError", indexType, metric, idx, err)
			}
			if refused.Metric != metric {
				t.Fatalf("refusal names metric %d, want %d", refused.Metric, metric)
			}
		}
	}
}

// TestNewLiveIndex_RefusesZeroDimension proves a record with no dimension
// builds no index, whatever its type.
func TestNewLiveIndex_RefusesZeroDimension(t *testing.T) {
	for _, indexType := range []IndexType{IndexHNSW, IndexFlat, IndexIVFFlat} {
		rec := NamespaceRecord{DistanceMetric: DistanceCosine, IndexType: indexType}
		idx, err := NewLiveIndex("ns", rec)
		var refused *InvalidDimensionError
		if !errors.As(err, &refused) {
			t.Fatalf("NewLiveIndex(indexType=%d, dimension=0) = %T, %v; want *InvalidDimensionError", indexType, idx, err)
		}
		if refused.Namespace != "ns" {
			t.Fatalf("refusal names namespace %q, want ns", refused.Namespace)
		}
	}
}

// TestNewLiveIndex_DispatchCoversEveryRecordValue proves the live index builds
// every metric and index type the record accepts, and nothing else: a value
// added to one set and not the other is a namespace the engine stores and then
// cannot index, or one it indexes but refuses to store.
func TestNewLiveIndex_DispatchCoversEveryRecordValue(t *testing.T) {
	for _, set := range []struct {
		name string
		got  int
		want int
	}{
		{"hnsw metrics", len(hnswDistanceMetrics), len(distanceMetrics)},
		{"ivf metrics", len(ivfDistanceMetrics), len(distanceMetrics)},
		{"index types", len(liveIndexBuilders), len(indexTypes)},
	} {
		if set.got != set.want {
			t.Errorf("%s: %d entries, want %d", set.name, set.got, set.want)
		}
	}
	for metric := range distanceMetrics {
		if _, ok := hnswDistanceMetrics[metric]; !ok {
			t.Errorf("metric %d has no hnsw mapping", metric)
		}
		if _, ok := ivfDistanceMetrics[metric]; !ok {
			t.Errorf("metric %d has no ivf mapping", metric)
		}
	}
	for indexType := range indexTypes {
		if _, ok := liveIndexBuilders[indexType]; !ok {
			t.Errorf("index type %d has no live-index builder", indexType)
		}
	}
}

func TestHNSWLiveIndex_UpsertDeleteSearchMarshalRoundTrip(t *testing.T) {
	for _, metric := range []DistanceMetric{DistanceCosine, DistanceEuclidean, DistanceDotProduct, DistanceManhattan} {
		rec := NamespaceRecord{Dimension: 2, DistanceMetric: metric, IndexType: IndexHNSW}
		idx, err := NewLiveIndex("ns", rec)
		if err != nil {
			t.Fatalf("NewLiveIndex(metric=%d): %v", metric, err)
		}
		if err := idx.Upsert(1, []float32{1, 0}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := idx.Upsert(2, []float32{0, 1}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		// Upsert again on an existing ID: must replace, not refuse as a
		// duplicate the way the underlying hnsw.Insert does.
		if err := idx.Upsert(1, []float32{1, 0}); err != nil {
			t.Fatalf("Upsert (replace): %v", err)
		}
		if got := idx.Count(); got != 2 {
			t.Fatalf("Count = %d, want 2", got)
		}

		hits, err := idx.Search([]float32{1, 0}, 1)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(hits) != 1 || hits[0].ID != 1 {
			t.Fatalf("Search = %+v, want ID 1 first", hits)
		}

		if err := idx.Delete(2); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := idx.Delete(2); err != nil {
			t.Fatalf("Delete (already absent) = %v, want nil (idempotent)", err)
		}
		if got := idx.Count(); got != 1 {
			t.Fatalf("Count after delete = %d, want 1", got)
		}

		data, err := idx.Marshal()
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		restored, err := NewLiveIndex("ns", rec)
		if err != nil {
			t.Fatalf("NewLiveIndex: %v", err)
		}
		if err := restored.Unmarshal(data); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got := restored.Count(); got != 1 {
			t.Fatalf("Count after Unmarshal = %d, want 1", got)
		}
	}
}

func TestFlatLiveIndex_UpsertDeleteSearchMarshalRoundTrip(t *testing.T) {
	rec := NamespaceRecord{Dimension: 2, DistanceMetric: DistanceCosine, IndexType: IndexFlat}
	idx, err := NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := idx.Upsert(1, []float32{1, 0}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := idx.Upsert(2, []float32{0, 1}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := idx.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2", got)
	}

	hits, err := idx.Search([]float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("Search = %+v, want ID 1 first", hits)
	}

	if err := idx.Delete(2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := idx.Delete(2); err != nil {
		t.Fatalf("Delete (already absent) = %v, want nil (idempotent)", err)
	}

	data, err := idx.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	restored, err := NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := restored.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := restored.Count(); got != 1 {
		t.Fatalf("Count after Unmarshal = %d, want 1", got)
	}
}

// An empty FLAT and an empty HNSW index answer a search with the same
// sentinel, so a caller needs neither index package to tell empty from failed.
func TestLiveIndex_EmptyFlatAndHNSWAnswerTheSameSentinel(t *testing.T) {
	for _, indexType := range []IndexType{IndexHNSW, IndexFlat} {
		idx, err := NewLiveIndex("ns", NamespaceRecord{Dimension: 2, DistanceMetric: DistanceEuclidean, IndexType: indexType})
		if err != nil {
			t.Fatalf("NewLiveIndex(indexType=%d): %v", indexType, err)
		}
		if _, err := idx.Search([]float32{1, 0}, 1); !errors.Is(err, hnsw.ErrEmptyIndex) {
			t.Errorf("Search on an empty index (indexType=%d) = %v, want hnsw.ErrEmptyIndex", indexType, err)
		}
	}
}

// A FLAT index emptied by deleting its only vector is empty again.
func TestFlatLiveIndex_EmptiedIndexAnswersErrEmptyIndex(t *testing.T) {
	idx, err := NewLiveIndex("ns", NamespaceRecord{Dimension: 2, DistanceMetric: DistanceEuclidean, IndexType: IndexFlat})
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := idx.Upsert(1, []float32{1, 0}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if hits, err := idx.Search([]float32{1, 0}, 1); err != nil || len(hits) != 1 {
		t.Fatalf("Search with one vector = %+v, %v; want one hit", hits, err)
	}
	if err := idx.Delete(1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := idx.Search([]float32{1, 0}, 1); !errors.Is(err, hnsw.ErrEmptyIndex) {
		t.Errorf("Search after deleting the only vector = %v, want hnsw.ErrEmptyIndex", err)
	}
}

func TestIVFLiveIndex_NotReadyUntilTrainingFloor(t *testing.T) {
	rec := NamespaceRecord{
		Dimension:      1,
		DistanceMetric: DistanceEuclidean,
		IndexType:      IndexIVFFlat,
		IndexParams:    map[string]string{"num_centroids": "2"},
	}
	idx, err := NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}

	if _, err := idx.Search([]float32{0}, 1); err != ErrIndexNotReady {
		t.Fatalf("Search before training = %v, want ErrIndexNotReady", err)
	}
	if _, err := idx.Marshal(); err != ErrIndexNotReady {
		t.Fatalf("Marshal before training = %v, want ErrIndexNotReady", err)
	}

	if err := idx.Upsert(1, []float32{0}); err != nil {
		t.Fatalf("Upsert 1: %v", err)
	}
	if got := idx.Count(); got != 1 {
		t.Fatalf("Count (buffered) = %d, want 1", got)
	}
	// Delete while still buffered (untrained): must not error and must
	// remove the buffered entry rather than reach into a nil trained index.
	if err := idx.Delete(1); err != nil {
		t.Fatalf("Delete (buffered): %v", err)
	}
	if got := idx.Count(); got != 0 {
		t.Fatalf("Count after buffered delete = %d, want 0", got)
	}

	// Reach the training floor (num_centroids=2).
	if err := idx.Upsert(1, []float32{0}); err != nil {
		t.Fatalf("Upsert 1: %v", err)
	}
	if err := idx.Upsert(2, []float32{10}); err != nil {
		t.Fatalf("Upsert 2 (training): %v", err)
	}

	hits, err := idx.Search([]float32{0}, 1)
	if err != nil {
		t.Fatalf("Search after training: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("Search after training = %+v, want ID 1 first", hits)
	}

	// Upsert and delete on a TRAINED index.
	if err := idx.Upsert(3, []float32{20}); err != nil {
		t.Fatalf("Upsert 3 (trained): %v", err)
	}
	if err := idx.Upsert(3, []float32{21}); err != nil {
		t.Fatalf("Upsert 3 (replace, trained): %v", err)
	}
	if err := idx.Delete(3); err != nil {
		t.Fatalf("Delete 3 (trained): %v", err)
	}
	if err := idx.Delete(3); err != nil {
		t.Fatalf("Delete 3 (already absent, trained) = %v, want nil (idempotent)", err)
	}

	data, err := idx.Marshal()
	if err != nil {
		t.Fatalf("Marshal (trained): %v", err)
	}
	restored, err := NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := restored.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := restored.Count(); got != 2 {
		t.Fatalf("Count after Unmarshal = %d, want 2", got)
	}
	restoredHits, err := restored.Search([]float32{0}, 1)
	if err != nil {
		t.Fatalf("Search (restored): %v", err)
	}
	if len(restoredHits) != 1 || restoredHits[0].ID != 1 {
		t.Fatalf("Search (restored) = %+v, want ID 1 first", restoredHits)
	}
}

func TestIVFLiveIndex_DefaultCentroidsWhenParamAbsentOrInvalid(t *testing.T) {
	for _, params := range []map[string]string{nil, {}, {"num_centroids": "not-a-number"}, {"num_centroids": "0"}, {"num_centroids": "-3"}} {
		rec := NamespaceRecord{Dimension: 1, DistanceMetric: DistanceCosine, IndexType: IndexIVFFlat, IndexParams: params}
		idx, err := NewLiveIndex("ns", rec)
		if err != nil {
			t.Fatalf("NewLiveIndex(params=%v): %v", params, err)
		}
		ivfIdx, ok := idx.(*ivfLiveIndex)
		if !ok {
			t.Fatalf("NewLiveIndex returned %T, want *ivfLiveIndex", idx)
		}
		if ivfIdx.numCentroids != defaultIVFCentroids {
			t.Fatalf("numCentroids = %d, want the default %d for params=%v", ivfIdx.numCentroids, defaultIVFCentroids, params)
		}
	}
}

// TestLiveIndex_ManhattanRanksFlatAndIVF pins that a Manhattan record is
// searched by Manhattan distance on the FLAT and IVF indexes: from the origin
// (5,0) is nearer by L1 (5 < 6) and (3,3) by L2 (4.24 < 5).
func TestLiveIndex_ManhattanRanksFlatAndIVF(t *testing.T) {
	for _, indexType := range []IndexType{IndexFlat, IndexIVFFlat} {
		rec := NamespaceRecord{
			Dimension:      2,
			DistanceMetric: DistanceManhattan,
			IndexType:      indexType,
			IndexParams:    map[string]string{"num_centroids": "1"},
		}
		idx, err := NewLiveIndex("ns", rec)
		if err != nil {
			t.Fatalf("NewLiveIndex(indexType=%d): %v", indexType, err)
		}
		if err := idx.Upsert(1, []float32{3, 3}); err != nil {
			t.Fatalf("Upsert 1: %v", err)
		}
		if err := idx.Upsert(2, []float32{5, 0}); err != nil {
			t.Fatalf("Upsert 2: %v", err)
		}
		hits, err := idx.Search([]float32{0, 0}, 2)
		if err != nil {
			t.Fatalf("Search(indexType=%d): %v", indexType, err)
		}
		if len(hits) != 2 || hits[0].ID != 2 || hits[0].Distance != 5 {
			t.Fatalf("Search(indexType=%d) = %+v, want ID 2 first at distance 5", indexType, hits)
		}
	}
}
