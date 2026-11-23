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
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
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

func TestVectorIndexManager_ApplyEffects_IndexErrorMarksNamespaceForRebuild(t *testing.T) {
	m := NewVectorIndexManager("", nil)
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectNamespaceCreate, Namespace: "ns", Record: vector.NamespaceRecord{Dimension: 2, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}},
	})
	// The FIRST insert into an empty HNSW index locks in its dimension from
	// whatever it is given (see hnsw.Insert), so this one succeeds and
	// fixes the index at dimension 2.
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 1, Embedding: []float32{1, 0}},
	})
	if _, err := m.Search("ns", []float32{1, 0}, 1); err != nil {
		t.Fatalf("Search after the first upsert: %v", err)
	}

	// A SECOND upsert whose embedding does not match the now-fixed
	// dimension is an index error (not a store error: the committed entry
	// already succeeded against the replicated record, which has no
	// dimension check of its own at the index-effect layer). The committed
	// entry must not be retracted, but the namespace must be marked for
	// rebuild rather than silently drop the vector from the live index.
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 2, Embedding: []float32{1, 2, 3}},
	})
	if _, err := m.Search("ns", []float32{1, 0}, 1); err == nil {
		t.Fatal("Search on a namespace marked for rebuild = nil error, want VectorIndexWarmingError")
	} else if _, ok := err.(*service.VectorIndexWarmingError); !ok {
		t.Fatalf("Search error = %T, want *service.VectorIndexWarmingError", err)
	}
}

func TestVectorIndexManager_ApplyEffects_DeleteOnReadyNamespaceSucceeds(t *testing.T) {
	m := NewVectorIndexManager("", nil)
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectNamespaceCreate, Namespace: "ns", Record: vector.NamespaceRecord{Dimension: 2, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}},
	})
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 1, Embedding: []float32{1, 0}},
		{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 2, Embedding: []float32{0, 1}},
	})
	m.ApplyEffects([]vector.IndexEffect{{Op: vector.IndexEffectDelete, Namespace: "ns", ID: 2}})

	hits, err := m.Search("ns", []float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range hits {
		if h.ID == 2 {
			t.Fatalf("DEFECT: ID 2 is still searchable after an IndexEffectDelete: %+v", hits)
		}
	}
}

func TestVectorIndexManager_ApplyEffects_ReconfigureDiscardsOldIndex(t *testing.T) {
	m := NewVectorIndexManager("", nil)
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectNamespaceCreate, Namespace: "ns", Record: vector.NamespaceRecord{Dimension: 2, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}},
	})
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 1, Embedding: []float32{1, 0}},
	})
	if _, err := m.Search("ns", []float32{1, 0}, 1); err != nil {
		t.Fatalf("Search before reconfigure: %v", err)
	}

	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectReconfigure, Namespace: "ns", Record: vector.NamespaceRecord{Dimension: 2, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexFlat, TrainingGeneration: 1}},
	})
	// A reconfigure's new index starts empty and warming: it has not been
	// rebuilt from records yet (that is RebuildNamespace's job), so a
	// search must refuse as warming, not silently answer from the
	// just-discarded old index.
	if _, err := m.Search("ns", []float32{1, 0}, 1); err == nil {
		t.Fatal("Search immediately after reconfigure = nil error, want VectorIndexWarmingError")
	} else if _, ok := err.(*service.VectorIndexWarmingError); !ok {
		t.Fatalf("Search error = %T, want *service.VectorIndexWarmingError", err)
	}
}

func TestVectorIndexManager_CheckpointAll_WritesEveryReadyNamespace(t *testing.T) {
	dir := t.TempDir()
	m := NewVectorIndexManager(dir, nil)
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectNamespaceCreate, Namespace: "a", Record: vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}},
		{Op: vector.IndexEffectNamespaceCreate, Namespace: "b", Record: vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}},
	})
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectUpsert, Namespace: "a", ID: 1, Embedding: []float32{1}},
		{Op: vector.IndexEffectUpsert, Namespace: "b", ID: 1, Embedding: []float32{1}},
	})
	if err := m.CheckpointAll(); err != nil {
		t.Fatalf("CheckpointAll: %v", err)
	}
	for _, ns := range []string{"a", "b"} {
		entries, err := os.ReadDir(filepath.Join(dir, "vector-index", ns))
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", ns, err)
		}
		if len(entries) == 0 {
			t.Fatalf("CheckpointAll wrote nothing for namespace %q", ns)
		}
	}
}

func TestVectorIndexManager_RemoveCheckpoints_DeletesFiles(t *testing.T) {
	dir := t.TempDir()
	m := NewVectorIndexManager(dir, nil)
	m.ApplyEffects([]vector.IndexEffect{
		{Op: vector.IndexEffectNamespaceCreate, Namespace: "ns", Record: vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}},
	})
	m.ApplyEffects([]vector.IndexEffect{{Op: vector.IndexEffectUpsert, Namespace: "ns", ID: 1, Embedding: []float32{1}}})
	if err := m.Checkpoint("ns"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	nsDir := filepath.Join(dir, "vector-index", "ns")
	if entries, err := os.ReadDir(nsDir); err != nil || len(entries) == 0 {
		t.Fatalf("checkpoint was not written: entries=%v err=%v", entries, err)
	}

	m.removeCheckpoints("ns")
	if _, err := os.Stat(nsDir); !os.IsNotExist(err) {
		t.Fatalf("removeCheckpoints did not remove %s: err=%v", nsDir, err)
	}
}

func TestVectorIndexManager_RebuildNamespace_ExistingRecordRebuildsFromStore(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}
	data, err := vector.EncodeNamespaceRecord(rec)
	if err != nil {
		t.Fatalf("EncodeNamespaceRecord: %v", err)
	}
	if err := stg.Set(vector.NamespaceKey("ns"), data); err != nil {
		t.Fatalf("Set(namespace record): %v", err)
	}
	vrec := vector.VectorRecord{Embedding: []float32{1}}
	vdata, err := vector.EncodeVectorRecord(vrec)
	if err != nil {
		t.Fatalf("EncodeVectorRecord: %v", err)
	}
	if err := stg.Set(vector.VectorKey("ns", 1), vdata); err != nil {
		t.Fatalf("Set(vector record): %v", err)
	}

	m := NewVectorIndexManager("", nil)
	if err := m.RebuildNamespace(stg, "ns"); err != nil {
		t.Fatalf("RebuildNamespace: %v", err)
	}
	hits, err := m.Search("ns", []float32{1}, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("Search = %+v, want ID 1", hits)
	}
}
