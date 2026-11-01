//go:build raft

package statemachine

import (
	"bytes"
	"context"
	"testing"

	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// snapshotOf returns the snapshot src writes.
func snapshotOf(t *testing.T, src *VectorConsensusFSM) *bytes.Buffer {
	t.Helper()
	var snap bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), &snap, nil); err != nil {
		t.Fatalf("SaveSnapshot error = %v", err)
	}
	return &snap
}

// installingFSM returns a node whose storage already holds the namespace's
// vectors but which built no index over them: the state a snapshot install
// leaves behind once the storage it shares carries the data.
func installingFSM(t *testing.T, vectors map[uint64][]float32) (*VectorConsensusFSM, *mockVectorStorage) {
	t.Helper()
	storage := newMockVectorStorage()
	fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 2, storage, createTestVectorLogger(t)).(*VectorConsensusFSM)
	if err := storage.CreateNamespace(context.Background(), &vectorStorage.NamespaceInfo{Name: "ns", Dimension: 2, DistanceMetric: "L2"}); err != nil {
		t.Fatalf("CreateNamespace error = %v", err)
	}
	for id, embedding := range vectors {
		if err := storage.UpsertVector(context.Background(), "ns", &vectorStorage.VectorValue{ID: id, Embedding: embedding}); err != nil {
			t.Fatalf("UpsertVector(%d) error = %v", id, err)
		}
	}
	return fsm, storage
}

// nearest returns the ID of the vector the namespace index finds nearest to query.
func nearest(t *testing.T, fsm *VectorConsensusFSM, namespace string, query []float32) uint64 {
	t.Helper()
	index, ok := fsm.hnswIndexes[namespace]
	if !ok {
		t.Fatalf("namespace %q has no index", namespace)
	}
	results, err := index.Search(query, 1)
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search returned %d results, want 1", len(results))
	}
	return results[0].ID
}

func TestRecoverFromSnapshot_RebuildsTheIndexFromStorage(t *testing.T) {
	src := newMetricNamespaceFSM(t, "L2")
	dst, _ := installingFSM(t, map[uint64][]float32{
		1: {0, 0},
		2: {10, 10},
		3: {-10, 5},
	})

	if err := dst.RecoverFromSnapshot(context.Background(), snapshotOf(t, src), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}

	if got := dst.hnswIndexes["ns"].Count(); got != 3 {
		t.Fatalf("recovered index holds %d vectors, want 3", got)
	}
	for query, want := range map[[2]float32]uint64{{1, 1}: 1, {9, 9}: 2, {-9, 4}: 3} {
		if got := nearest(t, dst, "ns", query[:]); got != want {
			t.Errorf("nearest(%v) = %d, want %d", query, got, want)
		}
	}
}

func TestRecoverFromSnapshot_ReplacesAnIndexBuiltBeforeTheInstall(t *testing.T) {
	src := newMetricNamespaceFSM(t, "L2")
	dst, storage := installingFSM(t, map[uint64][]float32{7: {3, 4}})
	// The node's pre-install index holds a vector storage no longer has.
	applyVectorCmd(t, dst, 1, &VectorCommand{Op: OpUpsertVector, Namespace: "ns", ID: 9, Vector: []float32{100, 100}})
	delete(storage.vectors["ns"], 9)

	if err := dst.RecoverFromSnapshot(context.Background(), snapshotOf(t, src), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}

	if got := dst.hnswIndexes["ns"].Count(); got != 1 {
		t.Fatalf("recovered index holds %d vectors, want 1", got)
	}
	if got := nearest(t, dst, "ns", []float32{99, 99}); got != 7 {
		t.Errorf("nearest = %d, want 7", got)
	}
}

func TestRecoverFromSnapshot_DropsANamespaceTheSnapshotDoesNotName(t *testing.T) {
	src := newMetricNamespaceFSM(t, "L2")
	dst, _ := installingFSM(t, map[uint64][]float32{1: {1, 2}})
	applyVectorCmd(t, dst, 1, &VectorCommand{Op: OpCreateNamespace, Namespace: "gone", Dimension: 2})

	if err := dst.RecoverFromSnapshot(context.Background(), snapshotOf(t, src), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}

	if _, ok := dst.namespaces["gone"]; ok {
		t.Error("a namespace the snapshot does not name survived the install")
	}
	if _, ok := dst.hnswIndexes["gone"]; ok {
		t.Error("an index for a namespace the snapshot does not name survived the install")
	}
	if got := nearest(t, dst, "ns", []float32{1, 2}); got != 1 {
		t.Errorf("nearest = %d, want 1", got)
	}
}

func TestRecoverFromSnapshot_SkipsAStoredVectorOfTheWrongDimension(t *testing.T) {
	src := newMetricNamespaceFSM(t, "L2")
	dst, _ := installingFSM(t, map[uint64][]float32{
		1: {1, 1},
		2: {1, 1, 1},
	})

	if err := dst.RecoverFromSnapshot(context.Background(), snapshotOf(t, src), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}

	if got := dst.hnswIndexes["ns"].Count(); got != 1 {
		t.Fatalf("recovered index holds %d vectors, want 1", got)
	}
	if got := nearest(t, dst, "ns", []float32{1, 1}); got != 1 {
		t.Errorf("nearest = %d, want 1", got)
	}
}

func TestRecoverFromSnapshot_ScanFailureLeavesTheNamespaceWithoutAnIndex(t *testing.T) {
	src := newMetricNamespaceFSM(t, "L2")
	dst, storage := installingFSM(t, map[uint64][]float32{1: {1, 1}})
	storage.scanNSErr = errScanFailed

	if err := dst.RecoverFromSnapshot(context.Background(), snapshotOf(t, src), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}

	if _, ok := dst.hnswIndexes["ns"]; ok {
		t.Error("a namespace whose vectors could not be read has an index")
	}
	if _, ok := dst.namespaces["ns"]; !ok {
		t.Error("the namespace the snapshot names is missing")
	}
}
