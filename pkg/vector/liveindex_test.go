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
	"testing"
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

func TestNewLiveIndex_UnknownIndexTypeFallsBackToHNSW(t *testing.T) {
	rec := NamespaceRecord{Dimension: 2, DistanceMetric: DistanceCosine, IndexType: 99}
	idx, err := NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if _, ok := idx.(*hnswLiveIndex); !ok {
		t.Fatalf("NewLiveIndex(unknown index type) = %T, want *hnswLiveIndex", idx)
	}
}

func TestHNSWLiveIndex_UpsertDeleteSearchMarshalRoundTrip(t *testing.T) {
	for _, metric := range []DistanceMetric{DistanceCosine, DistanceEuclidean, DistanceDotProduct, 99} {
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
