//go:build raft

package statemachine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/vmihailenco/msgpack/v5"

	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

func TestCreateHNSWIndex_BuildsTheNamedMetric(t *testing.T) {
	tests := []struct {
		name string
		want hnsw.DistanceMetric
	}{
		{"", hnsw.L2Distance},
		{"euclidean", hnsw.L2Distance},
		{"COSINE", hnsw.CosineDistance},
		{"dot_product", hnsw.DotProductDistance},
		{"MANHATTAN", hnsw.ManhattanDistance},
	}
	for _, tt := range tests {
		fsm := NewVectorConsensusFSM(VectorShardID, 1, createTestVectorLogger(t)).(*VectorConsensusFSM)
		if err := fsm.createHNSWIndex("ns", tt.name); err != nil {
			t.Fatalf("createHNSWIndex(%q) error = %v", tt.name, err)
		}
		idx := fsm.hnswIndexes["ns"]
		if idx == nil {
			t.Fatalf("createHNSWIndex(%q) built no index", tt.name)
		}
		if got := idx.GetConfig().DistanceMetric; got != tt.want {
			t.Errorf("createHNSWIndex(%q) metric = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCreateHNSWIndex_RefusesAnUnknownMetric(t *testing.T) {
	fsm := NewVectorConsensusFSM(VectorShardID, 1, createTestVectorLogger(t)).(*VectorConsensusFSM)
	err := fsm.createHNSWIndex("ns", "HAMMING")
	var unknown *hnsw.UnknownDistanceMetricError
	if !errors.As(err, &unknown) {
		t.Fatalf("createHNSWIndex error = %T (%v), want *hnsw.UnknownDistanceMetricError", err, err)
	}
	if _, ok := fsm.hnswIndexes["ns"]; ok {
		t.Error("createHNSWIndex built an index under a metric nobody asked for")
	}
}

// applyVectorCmd applies one command to fsm and fails the test on an error.
func applyVectorCmd(t *testing.T, fsm *VectorConsensusFSM, index uint64, cmd *VectorCommand) {
	t.Helper()
	results := make([]sm.Result, 1)
	entries := []sm.Entry{{Index: index, Cmd: encodeVectorCmd(t, cmd)}}
	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(%v) error = %v", cmd.Op, err)
	}
}

// newMetricNamespaceFSM returns an FSM over a fresh storage double holding a
// two-dimensional namespace "ns" created with metric, with its index dropped
// so the next write builds it lazily.
func newMetricNamespaceFSM(t *testing.T, metric string) *VectorConsensusFSM {
	t.Helper()
	fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, newMockVectorStorage(), createTestVectorLogger(t)).(*VectorConsensusFSM)
	applyVectorCmd(t, fsm, 1, &VectorCommand{Op: OpCreateNamespace, Namespace: "ns", Dimension: 2, DistanceMetric: metric})
	delete(fsm.hnswIndexes, "ns")
	return fsm
}

// indexMetric returns the metric of the index fsm holds for namespace.
func indexMetric(t *testing.T, fsm *VectorConsensusFSM, namespace string) hnsw.DistanceMetric {
	t.Helper()
	idx := fsm.hnswIndexes[namespace]
	if idx == nil {
		t.Fatalf("no index for namespace %q", namespace)
	}
	return idx.GetConfig().DistanceMetric
}

func TestApplyUpsertVector_LazyIndexKeepsTheNamespaceMetric(t *testing.T) {
	fsm := newMetricNamespaceFSM(t, "cosine")
	applyVectorCmd(t, fsm, 2, &VectorCommand{Op: OpUpsertVector, Namespace: "ns", ID: 1, Vector: []float32{1, 0}})
	if got := indexMetric(t, fsm, "ns"); got != hnsw.CosineDistance {
		t.Errorf("lazily built index metric = %v, want %v", got, hnsw.CosineDistance)
	}
}

func TestApplyUpsertVector_LazyIndexWithoutAMetricIsL2(t *testing.T) {
	fsm := NewVectorConsensusFSM(VectorShardID, 1, createTestVectorLogger(t)).(*VectorConsensusFSM)
	applyVectorCmd(t, fsm, 1, &VectorCommand{Op: OpUpsertVector, Namespace: "ns", ID: 1, Vector: []float32{1, 0}})
	if got := indexMetric(t, fsm, "ns"); got != hnsw.L2Distance {
		t.Errorf("lazily built index metric = %v, want %v", got, hnsw.L2Distance)
	}
}

func TestApplyBatchUpsert_LazyIndexKeepsTheNamespaceMetric(t *testing.T) {
	fsm := newMetricNamespaceFSM(t, "MANHATTAN")
	applyVectorCmd(t, fsm, 2, &VectorCommand{
		Op: OpBatchUpsert, Namespace: "ns", IDs: []uint64{1, 2}, Vectors: [][]float32{{1, 0}, {0, 1}},
	})
	if got := indexMetric(t, fsm, "ns"); got != hnsw.ManhattanDistance {
		t.Errorf("lazily built index metric = %v, want %v", got, hnsw.ManhattanDistance)
	}
}

func TestApplyBatchUpsert_LazyIndexWithoutAMetricIsL2(t *testing.T) {
	fsm := newMetricNamespaceFSM(t, "")
	applyVectorCmd(t, fsm, 2, &VectorCommand{
		Op: OpBatchUpsert, Namespace: "ns", IDs: []uint64{1}, Vectors: [][]float32{{1, 0}},
	})
	if got := indexMetric(t, fsm, "ns"); got != hnsw.L2Distance {
		t.Errorf("lazily built index metric = %v, want %v", got, hnsw.L2Distance)
	}
}

func TestRecoverFromSnapshot_KeepsTheNamespaceMetric(t *testing.T) {
	src := newMetricNamespaceFSM(t, "dot_product")
	var snap bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), &snap, nil); err != nil {
		t.Fatalf("SaveSnapshot error = %v", err)
	}

	// The installing node's storage holds no record of the namespace yet, so
	// the metric can only come from the snapshot.
	dst := NewVectorConsensusFSMWithStorage(VectorShardID, 2, newMockVectorStorage(), createTestVectorLogger(t)).(*VectorConsensusFSM)
	if err := dst.RecoverFromSnapshot(context.Background(), &snap, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}
	if got := indexMetric(t, dst, "ns"); got != hnsw.DotProductDistance {
		t.Errorf("recovered index metric = %v, want %v", got, hnsw.DotProductDistance)
	}
}

func TestRecoverFromSnapshot_SnapshotWithoutMetricReadsStorage(t *testing.T) {
	// A snapshot written before the metric was tracked carries name and
	// dimension only.
	data, err := msgpack.Marshal(map[string]map[string]any{"ns": {"name": "ns", "dim": 2}})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var snap bytes.Buffer
	if err := binary.Write(&snap, binary.BigEndian, uint32(len(data))); err != nil {
		t.Fatalf("write length prefix: %v", err)
	}
	snap.Write(data)

	storage := newMockVectorStorage()
	storage.namespaces["ns"] = &vectorStorage.NamespaceInfo{Name: "ns", Dimension: 2, DistanceMetric: "cosine"}
	dst := NewVectorConsensusFSMWithStorage(VectorShardID, 2, storage, createTestVectorLogger(t)).(*VectorConsensusFSM)
	delete(dst.hnswIndexes, "ns")
	if err := dst.RecoverFromSnapshot(context.Background(), &snap, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}
	if got := indexMetric(t, dst, "ns"); got != hnsw.CosineDistance {
		t.Errorf("recovered index metric = %v, want %v", got, hnsw.CosineDistance)
	}
}

func TestNamespaceMetricLocked_TrackedMetricWinsOverStorage(t *testing.T) {
	storage := newMockVectorStorage()
	storage.namespaces["ns"] = &vectorStorage.NamespaceInfo{Name: "ns", DistanceMetric: "cosine"}
	fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, createTestVectorLogger(t)).(*VectorConsensusFSM)
	fsm.namespaces["ns"] = &ConsensusNamespaceInfo{Name: "ns", DistanceMetric: "manhattan"}
	if got := fsm.namespaceMetric("ns"); got != "manhattan" {
		t.Errorf("namespaceMetric = %q, want the tracked %q", got, "manhattan")
	}
}

func TestNamespaceMetricLocked_UnknownNamespaceIsEmpty(t *testing.T) {
	storage := newMockVectorStorage()
	fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, createTestVectorLogger(t)).(*VectorConsensusFSM)
	if got := fsm.namespaceMetric("absent"); got != "" {
		t.Errorf("namespaceMetric(absent) with storage = %q, want empty", got)
	}
	bare := NewVectorConsensusFSM(VectorShardID, 1, createTestVectorLogger(t)).(*VectorConsensusFSM)
	if got := bare.namespaceMetric("absent"); got != "" {
		t.Errorf("namespaceMetric(absent) without storage = %q, want empty", got)
	}
}
