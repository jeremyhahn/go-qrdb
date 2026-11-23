//go:build raft

package statemachine

import (
	"bytes"
	"context"
	"encoding/hex"
	"reflect"
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

// TestVectorConsensus_SnapshotNamespacesBytesUnchanged pins the snapshot the
// consensus FSM writes: a 4-byte big-endian length, then the MessagePack
// namespaces map. One namespace only, because MessagePack map order is not
// sorted. The literal must also restore to the same namespaces.
func TestVectorConsensus_SnapshotNamespacesBytesUnchanged(t *testing.T) {
	const want = "0000002081a26e7383a46e616d65a26e73a364696dce00000002a66d6574726963a24c32"
	namespaces := map[string]*ConsensusNamespaceInfo{
		"ns": {Name: "ns", Dimension: 2, DistanceMetric: "L2"},
	}
	src, _ := installingFSM(t, nil)
	src.namespaces = namespaces
	got := hex.EncodeToString(snapshotOf(t, src).Bytes())
	if got != want {
		t.Fatalf("snapshot encoding changed:\n got %s\nwant %s", got, want)
	}

	raw, err := hex.DecodeString(want)
	if err != nil {
		t.Fatalf("DecodeString error = %v", err)
	}
	dst, _ := installingFSM(t, nil)
	if err := dst.RecoverFromSnapshot(context.Background(), bytes.NewReader(raw), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}
	if !reflect.DeepEqual(dst.namespaces, namespaces) {
		t.Fatalf("restored namespaces = %+v, want %+v", dst.namespaces, namespaces)
	}
}

// TestVectorStateMachine_SnapshotBytesUnchanged pins the VectorSnapshot
// encoding through the restore path: SaveSnapshot stamps the wall clock, so
// the fixed literal (an 8-byte length, then the MessagePack snapshot, with no
// index section) is decoded and the namespaces it restores are compared.
func TestVectorStateMachine_SnapshotBytesUnchanged(t *testing.T) {
	// Timestamp 1700000200, Version 11.
	const literal = "000000000000015a83a26e7381a4646f63738ba44e616d65a4646f6373a944696d656e73696f6ece00000003ab566563746f72436f756e74cf0000000000000005a9437265617465644174d3000000006553f100a9557064617465644174d3000000006553f164ab496e646578506172616d7381a16da23136a854656e616e744944a874656e616e742d61ab4f776e6572557365724944a6757365722d31a651756f74617386aa4d6178566563746f7273d300000000000003e8af4d617853746f726167654279746573d30000000000100000ad4d617844696d656e73696f6e73d200000200ac526174654c696d6974515053d20000000ab2526174654c696d6974577269746573515053d200000005ac4d6178426174636853697a65d200000064a64c6162656c7381a3656e76a474657374ac53746f726167654279746573d30000000000001000a27473d3000000006553f1c8a3766572cf000000000000000b"
	want := map[string]*NamespaceState{
		"docs": {
			Name:         "docs",
			Dimension:    3,
			VectorCount:  5,
			CreatedAt:    1700000000,
			UpdatedAt:    1700000100,
			IndexParams:  map[string]string{"m": "16"},
			TenantID:     "tenant-a",
			OwnerUserID:  "user-1",
			Quotas:       &NamespaceQuotas{MaxVectors: 1000, MaxStorageBytes: 1 << 20, MaxDimensions: 512, RateLimitQPS: 10, RateLimitWritesQPS: 5, MaxBatchSize: 100},
			Labels:       map[string]string{"env": "test"},
			StorageBytes: 4096,
		},
	}
	raw, err := hex.DecodeString(literal)
	if err != nil {
		t.Fatalf("DecodeString error = %v", err)
	}
	vsm, err := newTestVectorStateMachine(NewMockStorageAdapter(), NewMockHNSWIndex(), nil, newTestLogger())
	if err != nil {
		t.Fatalf("newTestVectorStateMachine error = %v", err)
	}
	if err := vsm.RecoverFromSnapshot(context.Background(), bytes.NewReader(raw), make(chan struct{})); err != nil {
		t.Fatalf("RecoverFromSnapshot error = %v", err)
	}
	if !reflect.DeepEqual(vsm.namespaces, want) {
		t.Fatalf("restored namespaces = %+v, want %+v", vsm.namespaces, want)
	}
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
