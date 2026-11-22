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

//go:build qrdb_vector

package engine

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// TestVectorIndexManager_Search_TrainedIVFAnswers proves the untrained
// refusal lifts once the namespace reaches its training floor.
// It needs the real IVF index: without qrdb_vector, pkg/vector/ivf is the
// stub, so training fails and the namespace stays warming.
func TestVectorIndexManager_Search_TrainedIVFAnswers(t *testing.T) {
	m := NewVectorIndexManager("", nil)
	effects := []vector.IndexEffect{
		{Op: vector.IndexEffectNamespaceCreate, Namespace: "ns", Record: vector.NamespaceRecord{
			Dimension: 2, DistanceMetric: vector.DistanceEuclidean, IndexType: vector.IndexIVFFlat,
			IndexParams: map[string]string{"num_centroids": "2"},
		}},
		{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 1, Embedding: []float32{0, 0}},
		{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 2, Embedding: []float32{10, 10}},
	}
	m.ApplyEffects(effects[:2])
	if _, err := m.Search("ns", []float32{0, 0}, 1); err == nil {
		t.Fatal("Search below the training floor = nil error, want *service.VectorIndexUntrainedError")
	}
	m.ApplyEffects(effects[2:])
	hits, err := m.Search("ns", []float32{0, 0}, 1)
	if err != nil {
		t.Fatalf("Search after training = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Errorf("Search after training = %+v, want ID 1", hits)
	}
}
